package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const wecomTransport = "wecom"

type wecomConnector struct {
	runtime      *AgentRuntime
	platform     mgmtPlatform
	corpID       string
	secret       string
	defaultAgent string
	apiBase      string
	publicBase   string
	webhookUUID  string
	crypto       *wechatWebhookCrypto
	host         string
	port         int
	adminIDs     map[string]struct{}
	client       *http.Client
	state        nativeConnectorState
	cancel       context.CancelFunc
	server       *platformWebhookServer
	tokenMu      sync.Mutex
	accessToken  string
	tokenExpiry  time.Time
	mediaMu      sync.RWMutex
	media        map[string]wecomMediaRecord
}

type wecomMediaRecord struct {
	MediaID   string
	ExpiresAt time.Time
}

type wecomKFMessage struct {
	MessageID      string `json:"msgid"`
	MessageType    string `json:"msgtype"`
	OpenKFID       string `json:"open_kfid"`
	ExternalUserID string `json:"external_userid"`
	SendTime       int64  `json:"send_time"`
	Text           struct {
		Content string `json:"content"`
	} `json:"text"`
	Image struct {
		MediaID string `json:"media_id"`
	} `json:"image"`
	Voice struct {
		MediaID string `json:"media_id"`
	} `json:"voice"`
	File struct {
		MediaID string `json:"media_id"`
	} `json:"file"`
	Video struct {
		MediaID string `json:"media_id"`
	} `json:"video"`
}

type wecomAPIError struct {
	Code    int
	Message string
}

func (e *wecomAPIError) Error() string { return fmt.Sprintf("WeCom error %d: %s", e.Code, e.Message) }

func newWecomConnector(runtime *AgentRuntime, platform mgmtPlatform) (*wecomConnector, error) {
	corpID, _ := platform.Settings["corpid"].(string)
	if strings.TrimSpace(corpID) == "" {
		return nil, platformConnectorStartupError(platform, "corpid")
	}
	secret := resolvePlatformCredential(platform, "secret")
	token := resolvePlatformCredential(platform, "token")
	aesKey := resolvePlatformCredential(platform, "encoding_aes_key")
	if secret == "" || token == "" || aesKey == "" {
		return nil, platformConnectorStartupError(platform, "secret, token or encoding_aes_key")
	}
	crypto, err := newWechatWebhookCrypto(token, aesKey, corpID)
	if err != nil {
		return nil, err
	}
	apiBase, _ := platform.Settings["api_base_url"].(string)
	apiBase = normalizeWechatAPIBase(apiBase, "https://qyapi.weixin.qq.com/cgi-bin")
	publicBase, _ := platform.Settings["wecom_public_base_url"].(string)
	host, _ := platform.Settings["callback_server_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	defaultAgent, _ := platform.Settings["agent_id"].(string)
	return &wecomConnector{
		runtime: runtime, platform: platform, corpID: strings.TrimSpace(corpID), secret: secret,
		defaultAgent: strings.TrimSpace(defaultAgent), apiBase: apiBase, publicBase: strings.TrimRight(publicBase, "/"),
		webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"), crypto: crypto,
		host: host, port: kookIntSetting(platform, "port", 6195), adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Timeout: 30 * time.Second}, state: newNativeConnectorState(platform), media: map[string]wecomMediaRecord{},
	}, nil
}

func (c *wecomConnector) ID() string   { return c.platform.ID }
func (c *wecomConnector) Type() string { return wecomTransport }

func (c *wecomConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	mux := http.NewServeMux()
	mux.HandleFunc(c.webhookPath(), c.handleWebhook)
	mux.HandleFunc("/wecom-media/", c.handleMedia)
	server, err := startPlatformWebhookServer(ctx, c.host, c.port, mux)
	if err != nil {
		cancel()
		c.state.setError(err)
		return err
	}
	c.server = server
	c.state.setStatus("connected")
	return nil
}

func (c *wecomConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *wecomConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *wecomConnector) webhookPath() string {
	if c.webhookUUID == "" {
		return "/callback/command"
	}
	return "/webhooks/" + url.PathEscape(c.webhookUUID)
}

func (c *wecomConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		plaintext, err := c.crypto.decryptMessage(
			r.URL.Query().Get("echostr"), r.URL.Query().Get("msg_signature"),
			r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"),
		)
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(plaintext)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	encrypted, err := wechatParseEncryptedBody(body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	plaintext, err := c.crypto.decryptMessage(
		encrypted, r.URL.Query().Get("msg_signature"), r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"),
	)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	message, err := wechatParseInboundMessage(plaintext)
	if err != nil {
		http.Error(w, "bad message", http.StatusBadRequest)
		return
	}
	if strings.EqualFold(message.Event, "kf_msg_or_event") {
		err = c.syncKFMessages(r.Context(), message)
	} else {
		err = c.acceptMessage(r.Context(), message)
	}
	if err != nil {
		http.Error(w, "event rejected", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	_, _ = w.Write([]byte("success"))
}

func (c *wecomConnector) acceptMessage(ctx context.Context, message wechatInboundMessage) error {
	messageID := firstNonEmpty(message.MessageID, fmt.Sprintf("%s:%d:%s", message.FromUserName, message.CreateTime, message.MessageType))
	text, attachments := c.parseInboundContent(message.MessageType, message.Content, message.PictureURL, message.MediaID, message.Recognition)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	if text == "" {
		text = "[" + firstNonEmpty(message.Event, message.MessageType, "event") + "]"
	}
	occurredAt := time.Now().UTC()
	if message.CreateTime > 0 {
		occurredAt = time.Unix(message.CreateTime, 0).UTC()
	}
	agentID := firstNonEmpty(message.AgentID, c.defaultAgent)
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: wecomTransport, MessageID: messageID,
		RouteKind: "app", TargetID: message.FromUserName, GuildID: agentID,
		ConversationID: "wecom:" + message.FromUserName, ConversationKind: "private",
		SenderID: message.FromUserName, SenderName: message.FromUserName, Text: text,
		Attachments: attachments, OccurredAt: occurredAt, IsWake: true,
		IsAdmin: nativePlatformIsAdmin(c.adminIDs, message.FromUserName), IsCommand: nativePlatformIsCommand(text),
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *wecomConnector) parseInboundContent(messageType, content, pictureURL, mediaID, recognition string) (string, []transportAttachment) {
	switch strings.ToLower(messageType) {
	case "text":
		return strings.TrimSpace(content), nil
	case "image":
		if strings.TrimSpace(pictureURL) != "" {
			return "", []transportAttachment{{Kind: "image", SourceURL: pictureURL}}
		}
		return c.mediaAttachment("image", mediaID)
	case "voice":
		text := strings.TrimSpace(recognition)
		_, attachments := c.mediaAttachment("audio", mediaID)
		return text, attachments
	case "video":
		return c.mediaAttachment("video", mediaID)
	case "file":
		return c.mediaAttachment("file", mediaID)
	default:
		return "[" + messageType + "]", nil
	}
}

func (c *wecomConnector) mediaAttachment(kind, mediaID string) (string, []transportAttachment) {
	if strings.TrimSpace(mediaID) == "" || c.publicBase == "" {
		return "[" + kind + "]", nil
	}
	token := c.registerMedia(mediaID)
	return "", []transportAttachment{{Kind: kind, SourceURL: c.publicBase + "/wecom-media/" + token}}
}

func (c *wecomConnector) registerMedia(mediaID string) string {
	token, err := newCoreUUID()
	if err != nil {
		token = c.runtime.platformPseudonym("wecom_media", mediaID+time.Now().String())
	}
	c.mediaMu.Lock()
	c.media[token] = wecomMediaRecord{MediaID: mediaID, ExpiresAt: time.Now().Add(time.Hour)}
	for key, record := range c.media {
		if record.ExpiresAt.Before(time.Now()) {
			delete(c.media, key)
		}
	}
	c.mediaMu.Unlock()
	return token
}

func (c *wecomConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/wecom-media/")
	c.mediaMu.RLock()
	record, found := c.media[token]
	c.mediaMu.RUnlock()
	if !found || record.ExpiresAt.Before(time.Now()) {
		http.NotFound(w, r)
		return
	}
	accessToken, err := c.token(r.Context())
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet,
		c.apiBase+"/media/get?access_token="+url.QueryEscape(accessToken)+"&media_id="+url.QueryEscape(record.MediaID), nil)
	response, err := c.client.Do(request)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Content-Disposition", response.Header.Get("Content-Disposition"))
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, io.LimitReader(response.Body, 20<<20))
	}
}

func (c *wecomConnector) syncKFMessages(ctx context.Context, event wechatInboundMessage) error {
	cursor := ""
	for page := 0; page < 10; page++ {
		var response struct {
			ErrorCode  int              `json:"errcode"`
			ErrorMsg   string           `json:"errmsg"`
			NextCursor string           `json:"next_cursor"`
			HasMore    int              `json:"has_more"`
			Messages   []wecomKFMessage `json:"msg_list"`
		}
		err := c.postJSON(ctx, "/kf/sync_msg", map[string]any{
			"cursor": cursor, "token": event.Token, "limit": 1000, "open_kfid": event.OpenKFID,
		}, &response)
		if err != nil {
			return err
		}
		for _, message := range response.Messages {
			if err = c.acceptKFMessage(ctx, message); err != nil {
				return err
			}
		}
		if response.HasMore == 0 || response.NextCursor == "" || response.NextCursor == cursor {
			break
		}
		cursor = response.NextCursor
	}
	return nil
}

func (c *wecomConnector) acceptKFMessage(ctx context.Context, message wecomKFMessage) error {
	if message.ExternalUserID == "" || message.MessageID == "" {
		return nil
	}
	mediaID := firstNonEmpty(message.Image.MediaID, message.Voice.MediaID, message.File.MediaID, message.Video.MediaID)
	text, attachments := c.parseInboundContent(message.MessageType, message.Text.Content, "", mediaID, "")
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	occurredAt := time.Now().UTC()
	if message.SendTime > 0 {
		occurredAt = time.Unix(message.SendTime, 0).UTC()
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: wecomTransport, MessageID: message.MessageID,
		RouteKind: "kf", TargetID: message.ExternalUserID, GuildID: message.OpenKFID,
		ConversationID: "wecom-kf:" + message.ExternalUserID, ConversationKind: "private",
		SenderID: message.ExternalUserID, SenderName: message.ExternalUserID, Text: text,
		Attachments: attachments, OccurredAt: occurredAt, IsWake: true,
		IsAdmin: nativePlatformIsAdmin(c.adminIDs, message.ExternalUserID), IsCommand: nativePlatformIsCommand(text),
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *wecomConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 2048) {
		if err := c.sendMessage(ctx, route, "text", map[string]any{"content": text}); err != nil {
			return wecomDeliveryError("wecom_text_send_failed", err)
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		mediaType := map[string]string{"image": "image", "audio": "voice", "video": "video", "file": "file"}[strings.ToLower(attachment.Kind)]
		if mediaType == "" {
			return &platformDeliveryError{Retryable: false, Reason: "wecom_attachment_kind_unsupported"}
		}
		mediaID, err := c.uploadMedia(ctx, mediaType, attachment)
		if err != nil {
			return wecomDeliveryError("wecom_media_upload_failed", err)
		}
		content := map[string]any{"media_id": mediaID}
		if mediaType == "video" {
			content["title"] = attachment.Name
			content["description"] = ""
		}
		if err = c.sendMessage(ctx, route, mediaType, content); err != nil {
			return wecomDeliveryError("wecom_media_send_failed", err)
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *wecomConnector) sendMessage(ctx context.Context, route platformReplyRoute, messageType string, content map[string]any) error {
	payload := map[string]any{"touser": route.TargetID, "msgtype": messageType, messageType: content}
	endpoint := "/message/send"
	if route.Kind == "kf" {
		endpoint = "/kf/send_msg"
		payload["open_kfid"] = route.GuildID
	} else {
		agentID := firstNonEmpty(route.GuildID, c.defaultAgent)
		if parsed, err := strconv.Atoi(agentID); err == nil {
			payload["agentid"] = parsed
		} else {
			payload["agentid"] = agentID
		}
		payload["safe"] = 0
		payload["enable_duplicate_check"] = 0
	}
	return c.postJSON(ctx, endpoint, payload, nil)
}

func (c *wecomConnector) token(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	endpoint := c.apiBase + "/gettoken?corpid=" + url.QueryEscape(c.corpID) + "&corpsecret=" + url.QueryEscape(c.secret)
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		ErrorCode   int    `json:"errcode"`
		ErrorMsg    string `json:"errmsg"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.ErrorCode != 0 || payload.AccessToken == "" {
		return "", &wecomAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	expires := payload.ExpiresIn
	if expires <= 0 {
		expires = 7200
	}
	c.accessToken, c.tokenExpiry = payload.AccessToken, time.Now().Add(time.Duration(expires-60)*time.Second)
	return c.accessToken, nil
}

func (c *wecomConnector) postJSON(ctx context.Context, endpoint string, input any, output any) error {
	accessToken, err := c.token(ctx)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+endpoint+"?access_token="+url.QueryEscape(accessToken), bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var payload struct {
		ErrorCode int    `json:"errcode"`
		ErrorMsg  string `json:"errmsg"`
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if len(body) > 0 {
		_ = json.Unmarshal(body, &payload)
		if output != nil {
			if err = json.Unmarshal(body, output); err != nil {
				return err
			}
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.ErrorCode != 0 {
		return &wecomAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	return nil
}

func (c *wecomConnector) uploadMedia(ctx context.Context, mediaType string, attachment agentAttachment) (string, error) {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return "", err
	}
	accessToken, err := c.token(ctx)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", firstNonEmpty(attachment.Name, filepath.Base(cleanPath)))
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	_ = writer.Close()
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost,
		c.apiBase+"/media/upload?access_token="+url.QueryEscape(accessToken)+"&type="+url.QueryEscape(mediaType), &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var payload struct {
		ErrorCode int    `json:"errcode"`
		ErrorMsg  string `json:"errmsg"`
		MediaID   string `json:"media_id"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.ErrorCode != 0 || payload.MediaID == "" {
		return "", &wecomAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	return payload.MediaID, nil
}

func wecomDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	if apiErr, ok := err.(*wecomAPIError); ok {
		retryable = apiErr.Code == -1 || apiErr.Code == 42001 || apiErr.Code == 45009
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func normalizeWechatAPIBase(value, fallback string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value == "" {
		value = fallback
	}
	if !strings.HasSuffix(value, "/cgi-bin") {
		value += "/cgi-bin"
	}
	return value
}
