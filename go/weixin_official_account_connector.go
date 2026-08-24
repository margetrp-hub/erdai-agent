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
	"strings"
	"sync"
	"time"
)

const weixinOfficialAccountTransport = "weixin_official_account"

type weixinOfficialAccountConnector struct {
	runtime        *AgentRuntime
	platform       mgmtPlatform
	appID          string
	secret         string
	tokenValue     string
	apiBase        string
	publicBase     string
	webhookUUID    string
	activeSend     bool
	placeholder    string
	passiveTimeout time.Duration
	crypto         *wechatWebhookCrypto
	host           string
	port           int
	adminIDs       map[string]struct{}
	client         *http.Client
	state          nativeConnectorState
	cancel         context.CancelFunc
	server         *platformWebhookServer
	tokenMu        sync.Mutex
	accessToken    string
	tokenExpiry    time.Time
	passiveMu      sync.Mutex
	pending        map[string]chan weixinPassiveReply
	cached         map[string][]weixinPassiveReply
	mediaMu        sync.RWMutex
	media          map[string]weixinMediaRecord
}

type weixinPassiveReply struct {
	Text      string
	MediaType string
	MediaID   string
}

type weixinMediaRecord struct {
	MediaID   string
	ExpiresAt time.Time
}

type weixinAPIError struct {
	Code    int
	Message string
}

func (e *weixinAPIError) Error() string {
	return fmt.Sprintf("Weixin Official Account error %d: %s", e.Code, e.Message)
}

func newWeixinOfficialAccountConnector(runtime *AgentRuntime, platform mgmtPlatform) (*weixinOfficialAccountConnector, error) {
	appID, _ := platform.Settings["appid"].(string)
	if strings.TrimSpace(appID) == "" {
		return nil, platformConnectorStartupError(platform, "appid")
	}
	secret := resolvePlatformCredential(platform, "secret")
	tokenValue := resolvePlatformCredential(platform, "token")
	aesKey := resolvePlatformCredential(platform, "encoding_aes_key")
	if secret == "" || tokenValue == "" || aesKey == "" {
		return nil, platformConnectorStartupError(platform, "secret, token or encoding_aes_key")
	}
	crypto, err := newWechatWebhookCrypto(tokenValue, aesKey, appID)
	if err != nil {
		return nil, err
	}
	apiBase, _ := platform.Settings["api_base_url"].(string)
	apiBase = normalizeWechatAPIBase(apiBase, "https://api.weixin.qq.com/cgi-bin")
	publicBase, _ := platform.Settings["weixin_public_base_url"].(string)
	host, _ := platform.Settings["callback_server_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	activeSend, _ := platform.Settings["active_send_mode"].(bool)
	placeholder, _ := platform.Settings["passive_reply_placeholder"].(string)
	if strings.TrimSpace(placeholder) == "" {
		placeholder = "我还在想。等下再回一句，我把结果给你。"
	}
	return &weixinOfficialAccountConnector{
		runtime: runtime, platform: platform, appID: strings.TrimSpace(appID), secret: secret, tokenValue: tokenValue,
		apiBase: apiBase, publicBase: strings.TrimRight(publicBase, "/"), webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"),
		activeSend: activeSend, placeholder: placeholder, passiveTimeout: 4 * time.Second, crypto: crypto,
		host: host, port: kookIntSetting(platform, "port", 6194), adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Timeout: 30 * time.Second}, state: newNativeConnectorState(platform),
		pending: map[string]chan weixinPassiveReply{}, cached: map[string][]weixinPassiveReply{}, media: map[string]weixinMediaRecord{},
	}, nil
}

func (c *weixinOfficialAccountConnector) ID() string   { return c.platform.ID }
func (c *weixinOfficialAccountConnector) Type() string { return weixinOfficialAccountTransport }

func (c *weixinOfficialAccountConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	mux := http.NewServeMux()
	mux.HandleFunc(c.webhookPath(), c.handleWebhook)
	mux.HandleFunc("/weixin-media/", c.handleMedia)
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

func (c *weixinOfficialAccountConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *weixinOfficialAccountConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *weixinOfficialAccountConnector) webhookPath() string {
	if c.webhookUUID == "" {
		return "/callback/command"
	}
	return "/webhooks/" + url.PathEscape(c.webhookUUID)
}

func (c *weixinOfficialAccountConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		c.handleVerify(w, r)
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
	messageBody, encrypted, err := c.decodeCallback(r, body)
	if err != nil {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	message, err := wechatParseInboundMessage(messageBody)
	if err != nil {
		http.Error(w, "bad message", http.StatusBadRequest)
		return
	}
	if !c.activeSend {
		if cached, ok := c.popCached(message.FromUserName); ok {
			c.writePassiveReply(w, message, cached, encrypted, r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"))
			return
		}
	}
	messageID := firstNonEmpty(message.MessageID, fmt.Sprintf("%s:%d:%s", message.FromUserName, message.CreateTime, message.MessageType))
	var replyChannel chan weixinPassiveReply
	if !c.activeSend {
		replyChannel = make(chan weixinPassiveReply, 1)
		c.passiveMu.Lock()
		c.pending[messageID] = replyChannel
		c.passiveMu.Unlock()
		defer func() {
			c.passiveMu.Lock()
			delete(c.pending, messageID)
			c.passiveMu.Unlock()
		}()
	}
	if err = c.acceptMessage(r.Context(), message, messageID); err != nil {
		http.Error(w, "event rejected", http.StatusInternalServerError)
		return
	}
	if c.activeSend {
		_, _ = w.Write([]byte("success"))
		return
	}
	select {
	case reply := <-replyChannel:
		c.writePassiveReply(w, message, reply, encrypted, r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"))
	case <-time.After(c.passiveTimeout):
		c.writePassiveReply(w, message, weixinPassiveReply{Text: c.placeholder}, encrypted, r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"))
	case <-r.Context().Done():
		return
	}
}

func (c *weixinOfficialAccountConnector) handleVerify(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	if query.Get("msg_signature") != "" {
		plaintext, err := c.crypto.decryptMessage(query.Get("echostr"), query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce"))
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write(plaintext)
		return
	}
	if !wechatVerifyPlainSignature(c.tokenValue, query.Get("signature"), query.Get("timestamp"), query.Get("nonce")) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	_, _ = w.Write([]byte(query.Get("echostr")))
}

func (c *weixinOfficialAccountConnector) decodeCallback(r *http.Request, body []byte) ([]byte, bool, error) {
	encrypted, err := wechatParseEncryptedBody(body)
	if err == nil {
		plaintext, decryptErr := c.crypto.decryptMessage(encrypted, r.URL.Query().Get("msg_signature"), r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce"))
		return plaintext, true, decryptErr
	}
	if !wechatVerifyPlainSignature(c.tokenValue, r.URL.Query().Get("signature"), r.URL.Query().Get("timestamp"), r.URL.Query().Get("nonce")) {
		return nil, false, fmt.Errorf("invalid signature")
	}
	return body, false, nil
}

func (c *weixinOfficialAccountConnector) acceptMessage(ctx context.Context, message wechatInboundMessage, messageID string) error {
	text, attachments := c.parseInboundContent(message)
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
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: weixinOfficialAccountTransport, MessageID: messageID,
		RouteKind: map[bool]string{true: "active", false: "passive"}[c.activeSend],
		TargetID:  message.FromUserName, GuildID: message.ToUserName, ChannelID: messageID,
		ConversationID: "weixin-official:" + message.FromUserName, ConversationKind: "private",
		SenderID: message.FromUserName, SenderName: message.FromUserName, Text: text,
		Attachments: attachments, OccurredAt: occurredAt, IsWake: true,
		IsAdmin: nativePlatformIsAdmin(c.adminIDs, message.FromUserName), IsCommand: nativePlatformIsCommand(text),
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *weixinOfficialAccountConnector) parseInboundContent(message wechatInboundMessage) (string, []transportAttachment) {
	switch strings.ToLower(message.MessageType) {
	case "text":
		return strings.TrimSpace(message.Content), nil
	case "image":
		if strings.TrimSpace(message.PictureURL) != "" {
			return "", []transportAttachment{{Kind: "image", SourceURL: message.PictureURL}}
		}
		return c.mediaAttachment("image", message.MediaID)
	case "voice":
		text := strings.TrimSpace(message.Recognition)
		_, attachments := c.mediaAttachment("audio", message.MediaID)
		return text, attachments
	case "video":
		return c.mediaAttachment("video", message.MediaID)
	default:
		return "[" + firstNonEmpty(message.Event, message.MessageType) + "]", nil
	}
}

func (c *weixinOfficialAccountConnector) mediaAttachment(kind, mediaID string) (string, []transportAttachment) {
	if mediaID == "" || c.publicBase == "" {
		return "[" + kind + "]", nil
	}
	token := c.registerMedia(mediaID)
	return "", []transportAttachment{{Kind: kind, SourceURL: c.publicBase + "/weixin-media/" + token}}
}

func (c *weixinOfficialAccountConnector) registerMedia(mediaID string) string {
	token, err := newCoreUUID()
	if err != nil {
		token = c.runtime.platformPseudonym("weixin_media", mediaID+time.Now().String())
	}
	c.mediaMu.Lock()
	c.media[token] = weixinMediaRecord{MediaID: mediaID, ExpiresAt: time.Now().Add(time.Hour)}
	for key, record := range c.media {
		if record.ExpiresAt.Before(time.Now()) {
			delete(c.media, key)
		}
	}
	c.mediaMu.Unlock()
	return token
}

func (c *weixinOfficialAccountConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/weixin-media/")
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

func (c *weixinOfficialAccountConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	replies := make([]weixinPassiveReply, 0, 4)
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 1024) {
		replies = append(replies, weixinPassiveReply{Text: text})
	}
	for _, attachment := range delivery.Message.Attachments {
		mediaType := map[string]string{"image": "image", "audio": "voice", "video": "video"}[strings.ToLower(attachment.Kind)]
		if mediaType == "" || (!c.activeSend && mediaType == "video") {
			return &platformDeliveryError{Retryable: false, Reason: "weixin_official_attachment_kind_unsupported"}
		}
		mediaID, err := c.uploadMedia(ctx, mediaType, attachment)
		if err != nil {
			return weixinDeliveryError("weixin_official_media_upload_failed", err)
		}
		replies = append(replies, weixinPassiveReply{MediaType: mediaType, MediaID: mediaID})
	}
	for _, reply := range replies {
		if c.activeSend {
			if err := c.sendActive(ctx, route.TargetID, reply); err != nil {
				return weixinDeliveryError("weixin_official_send_failed", err)
			}
		} else {
			c.queuePassive(route.ChannelID, route.TargetID, reply)
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *weixinOfficialAccountConnector) queuePassive(messageID, userID string, reply weixinPassiveReply) {
	c.passiveMu.Lock()
	defer c.passiveMu.Unlock()
	if channel := c.pending[messageID]; channel != nil {
		select {
		case channel <- reply:
			return
		default:
		}
	}
	c.cached[userID] = append(c.cached[userID], reply)
}

func (c *weixinOfficialAccountConnector) popCached(userID string) (weixinPassiveReply, bool) {
	c.passiveMu.Lock()
	defer c.passiveMu.Unlock()
	values := c.cached[userID]
	if len(values) == 0 {
		return weixinPassiveReply{}, false
	}
	reply := values[0]
	if len(values) == 1 {
		delete(c.cached, userID)
	} else {
		c.cached[userID] = values[1:]
	}
	return reply, true
}

func (c *weixinOfficialAccountConnector) writePassiveReply(w http.ResponseWriter, message wechatInboundMessage, reply weixinPassiveReply, encrypted bool, timestamp, nonce string) {
	var body []byte
	var err error
	if reply.Text != "" {
		body, err = wechatTextReplyXML(message, reply.Text)
	} else {
		body, err = wechatMediaReplyXML(message, reply.MediaType, reply.MediaID)
	}
	if err != nil {
		http.Error(w, "reply failed", http.StatusInternalServerError)
		return
	}
	if encrypted {
		encoded, encryptErr := c.crypto.encryptMessage(body, timestamp, nonce)
		if encryptErr != nil {
			http.Error(w, "reply failed", http.StatusInternalServerError)
			return
		}
		body = []byte(encoded)
	}
	w.Header().Set("Content-Type", "application/xml")
	_, _ = w.Write(body)
}

func (c *weixinOfficialAccountConnector) sendActive(ctx context.Context, userID string, reply weixinPassiveReply) error {
	payload := map[string]any{"touser": userID}
	if reply.Text != "" {
		payload["msgtype"] = "text"
		payload["text"] = map[string]any{"content": reply.Text}
	} else {
		payload["msgtype"] = reply.MediaType
		payload[reply.MediaType] = map[string]any{"media_id": reply.MediaID}
	}
	return c.postJSON(ctx, "/message/custom/send", payload)
}

func (c *weixinOfficialAccountConnector) token(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		return c.accessToken, nil
	}
	endpoint := c.apiBase + "/token?grant_type=client_credential&appid=" + url.QueryEscape(c.appID) + "&secret=" + url.QueryEscape(c.secret)
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
		return "", &weixinAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	expires := payload.ExpiresIn
	if expires <= 60 {
		expires = 7200
	}
	c.accessToken, c.tokenExpiry = payload.AccessToken, time.Now().Add(time.Duration(expires-60)*time.Second)
	return c.accessToken, nil
}

func (c *weixinOfficialAccountConnector) postJSON(ctx context.Context, endpoint string, input any) error {
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
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || payload.ErrorCode != 0 {
		return &weixinAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	return nil
}

func (c *weixinOfficialAccountConnector) uploadMedia(ctx context.Context, mediaType string, attachment agentAttachment) (string, error) {
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
		return "", &weixinAPIError{Code: payload.ErrorCode, Message: payload.ErrorMsg}
	}
	return payload.MediaID, nil
}

func weixinDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	if apiErr, ok := err.(*weixinAPIError); ok {
		retryable = apiErr.Code == -1 || apiErr.Code == 42001 || apiErr.Code == 45009
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}
