package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const weixinOCTransport = "weixin_oc"

type weixinOCConnector struct {
	runtime     *AgentRuntime
	platform    mgmtPlatform
	baseURL     string
	cdnBaseURL  string
	botType     string
	publicBase  string
	mediaHost   string
	mediaPort   int
	qrPoll      time.Duration
	longPoll    time.Duration
	client      *http.Client
	admins      map[string]struct{}
	state       nativeConnectorState
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	server      *platformWebhookServer
	mu          sync.RWMutex
	account     weixinOCAccountState
	login       weixinOCLoginState
	media       map[string]weixinOCMediaRecord
	lastPollErr string
}

type weixinOCAccountState struct {
	Token         string            `json:"token"`
	AccountID     string            `json:"accountId"`
	SyncBuffer    string            `json:"syncBuffer"`
	BaseURL       string            `json:"baseUrl"`
	ContextTokens map[string]string `json:"contextTokens"`
}

type weixinOCLoginState struct {
	QRCode      string
	QRCodeValue string
	Status      string
	StartedAt   time.Time
}

type weixinOCMediaRecord struct {
	EncryptedParam string
	AESKey         string
	Kind           string
	Name           string
	ExpiresAt      time.Time
}

type weixinOCAPIError struct {
	Ret     int
	Code    int
	Message string
}

func (e *weixinOCAPIError) Error() string {
	return fmt.Sprintf("Weixin Personal API ret=%d errcode=%d: %s", e.Ret, e.Code, e.Message)
}

func newWeixinOCConnector(runtime *AgentRuntime, platform mgmtPlatform) (*weixinOCConnector, error) {
	baseURL, _ := platform.Settings["weixin_oc_base_url"].(string)
	if strings.TrimSpace(baseURL) == "" {
		baseURL = "https://ilinkai.weixin.qq.com"
	}
	cdnBaseURL, _ := platform.Settings["weixin_oc_cdn_base_url"].(string)
	if strings.TrimSpace(cdnBaseURL) == "" {
		cdnBaseURL = "https://novac2c.cdn.weixin.qq.com/c2c"
	}
	botType, _ := platform.Settings["weixin_oc_bot_type"].(string)
	if strings.TrimSpace(botType) == "" {
		botType = "3"
	}
	publicBase, _ := platform.Settings["weixin_oc_public_base_url"].(string)
	mediaHost, _ := platform.Settings["weixin_oc_media_host"].(string)
	if strings.TrimSpace(mediaHost) == "" {
		mediaHost = "127.0.0.1"
	}
	apiTimeout := time.Duration(kookIntSetting(platform, "weixin_oc_api_timeout_ms", 120000)) * time.Millisecond
	if apiTimeout < time.Second {
		apiTimeout = time.Second
	}
	value := &weixinOCConnector{
		runtime: runtime, platform: platform, baseURL: strings.TrimRight(baseURL, "/"),
		cdnBaseURL: strings.TrimRight(cdnBaseURL, "/"), botType: strings.TrimSpace(botType),
		publicBase: strings.TrimRight(publicBase, "/"), mediaHost: mediaHost,
		mediaPort: kookIntSetting(platform, "weixin_oc_media_port", 6203),
		qrPoll:    time.Duration(kookIntSetting(platform, "weixin_oc_qr_poll_interval", 1)) * time.Second,
		longPoll:  time.Duration(kookIntSetting(platform, "weixin_oc_long_poll_timeout_ms", 35000)) * time.Millisecond,
		client:    &http.Client{Timeout: apiTimeout}, admins: nativePlatformAdminIDs(platform),
		state: newNativeConnectorState(platform), media: map[string]weixinOCMediaRecord{},
		account: weixinOCAccountState{ContextTokens: map[string]string{}},
	}
	if value.qrPoll < time.Second {
		value.qrPoll = time.Second
	}
	if value.longPoll < time.Second {
		value.longPoll = time.Second
	}
	if err := value.loadAccountState(context.Background()); err != nil {
		return nil, fmt.Errorf("load Weixin Personal account state: %w", err)
	}
	return value, nil
}

func (c *weixinOCConnector) ID() string   { return c.platform.ID }
func (c *weixinOCConnector) Type() string { return weixinOCTransport }

func (c *weixinOCConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if c.publicBase != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/weixin-oc-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.mediaHost, c.mediaPort, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	c.mu.RLock()
	configured := c.account.Token != ""
	c.mu.RUnlock()
	if configured {
		c.state.setStatus("connecting")
	} else {
		c.state.setStatus("waiting_for_login")
	}
	c.wg.Add(1)
	go c.run(ctx)
	return nil
}

func (c *weixinOCConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *weixinOCConnector) Health() platformConnectorHealth {
	health := c.state.snapshot()
	c.mu.RLock()
	health.Details = map[string]any{
		"configured": c.account.Token != "", "accountId": c.account.AccountID,
		"loginStatus": c.login.Status, "qrAvailable": c.login.QRCodeValue != "",
		"lastPollError": c.lastPollErr,
	}
	c.mu.RUnlock()
	return health
}

func (c *weixinOCConnector) QRCodeValue() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.login.QRCodeValue
}

func (c *weixinOCConnector) run(ctx context.Context) {
	defer c.wg.Done()
	for ctx.Err() == nil {
		c.mu.RLock()
		loggedIn := c.account.Token != ""
		c.mu.RUnlock()
		if !loggedIn {
			if err := c.loginOnce(ctx); err != nil && ctx.Err() == nil {
				c.mu.Lock()
				c.lastPollErr = err.Error()
				c.mu.Unlock()
				c.state.setError(err)
				if !waitContext(ctx, 2*time.Second) {
					return
				}
			}
			if !waitContext(ctx, c.qrPoll) {
				return
			}
			continue
		}
		if err := c.pollUpdates(ctx); err != nil && ctx.Err() == nil {
			var apiErr *weixinOCAPIError
			if errors.As(err, &apiErr) && apiErr.Code == -14 {
				_ = c.clearAccountState(ctx)
				c.state.setStatus("waiting_for_login")
				continue
			}
			c.mu.Lock()
			c.lastPollErr = err.Error()
			c.mu.Unlock()
			c.state.setError(err)
			if !waitContext(ctx, 5*time.Second) {
				return
			}
		}
	}
}

func (c *weixinOCConnector) loginOnce(ctx context.Context) error {
	c.mu.RLock()
	login := c.login
	c.mu.RUnlock()
	if login.QRCode == "" || login.StartedAt.IsZero() || time.Since(login.StartedAt) >= 5*time.Minute {
		var response struct {
			QRCode      string `json:"qrcode"`
			QRCodeValue string `json:"qrcode_img_content"`
		}
		if err := c.requestJSON(ctx, http.MethodGet, "/ilink/bot/get_bot_qrcode", map[string]string{"bot_type": c.botType}, nil, false, nil, &response); err != nil {
			return err
		}
		if strings.TrimSpace(response.QRCode) == "" || strings.TrimSpace(response.QRCodeValue) == "" {
			return errors.New("Weixin Personal login response is missing QR code")
		}
		c.mu.Lock()
		c.login = weixinOCLoginState{QRCode: response.QRCode, QRCodeValue: response.QRCodeValue, Status: "wait", StartedAt: time.Now()}
		c.lastPollErr = ""
		c.mu.Unlock()
		c.state.setStatus("waiting_for_login")
		return nil
	}
	var response struct {
		Status    string `json:"status"`
		Token     string `json:"bot_token"`
		AccountID string `json:"ilink_bot_id"`
		BaseURL   string `json:"baseurl"`
	}
	pollCtx, cancel := context.WithTimeout(ctx, c.longPoll)
	defer cancel()
	if err := c.requestJSON(pollCtx, http.MethodGet, "/ilink/bot/get_qrcode_status", map[string]string{"qrcode": login.QRCode}, nil, false, map[string]string{"iLink-App-ClientVersion": "1"}, &response); err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(response.Status))
	c.mu.Lock()
	c.login.Status = status
	c.mu.Unlock()
	switch status {
	case "confirmed":
		if strings.TrimSpace(response.Token) == "" {
			return errors.New("Weixin Personal login succeeded without a bot token")
		}
		c.mu.Lock()
		c.account.Token = strings.TrimSpace(response.Token)
		c.account.AccountID = strings.TrimSpace(response.AccountID)
		if strings.TrimSpace(response.BaseURL) != "" {
			c.baseURL = strings.TrimRight(response.BaseURL, "/")
			c.account.BaseURL = c.baseURL
		}
		c.login.QRCode = ""
		c.login.QRCodeValue = ""
		c.lastPollErr = ""
		c.mu.Unlock()
		if err := c.saveAccountState(ctx); err != nil {
			return err
		}
		c.state.setStatus("connected")
	case "expired", "cancel", "canceled", "denied":
		c.mu.Lock()
		c.login.QRCode = ""
		c.login.QRCodeValue = ""
		c.mu.Unlock()
	}
	return nil
}

func (c *weixinOCConnector) pollUpdates(ctx context.Context) error {
	c.mu.RLock()
	syncBuffer := c.account.SyncBuffer
	c.mu.RUnlock()
	var response struct {
		Ret        int              `json:"ret"`
		ErrorCode  int              `json:"errcode"`
		ErrorText  string           `json:"errmsg"`
		SyncBuffer string           `json:"get_updates_buf"`
		Messages   []map[string]any `json:"msgs"`
	}
	payload := map[string]any{"base_info": map[string]any{"channel_version": "erdai-agent"}, "get_updates_buf": syncBuffer}
	pollCtx, cancel := context.WithTimeout(ctx, c.longPoll)
	defer cancel()
	if err := c.requestJSON(pollCtx, http.MethodPost, "/ilink/bot/getupdates", nil, payload, true, nil, &response); err != nil {
		return err
	}
	if response.Ret != 0 || response.ErrorCode != 0 {
		return &weixinOCAPIError{Ret: response.Ret, Code: response.ErrorCode, Message: response.ErrorText}
	}
	dirty := false
	c.mu.Lock()
	if response.SyncBuffer != "" && response.SyncBuffer != c.account.SyncBuffer {
		c.account.SyncBuffer = response.SyncBuffer
		dirty = true
	}
	c.lastPollErr = ""
	c.mu.Unlock()
	for _, message := range response.Messages {
		changed, err := c.acceptMessage(ctx, message)
		dirty = dirty || changed
		if err != nil {
			return err
		}
	}
	if dirty {
		if err := c.saveAccountState(ctx); err != nil {
			return err
		}
	}
	c.state.setStatus("connected")
	return nil
}

func (c *weixinOCConnector) acceptMessage(ctx context.Context, message map[string]any) (bool, error) {
	sender := strings.TrimSpace(anyString(message["from_user_id"]))
	if sender == "" {
		return false, nil
	}
	contextToken := strings.TrimSpace(anyString(message["context_token"]))
	changed := false
	if contextToken != "" {
		c.mu.Lock()
		if c.account.ContextTokens[sender] != contextToken {
			c.account.ContextTokens[sender] = contextToken
			changed = true
		}
		c.mu.Unlock()
	}
	items := anyObjectSlice(message["item_list"])
	text, attachments := c.parseItems(items)
	if text == "" {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	messageID := firstNonEmpty(anyString(message["message_id"]), anyString(message["msg_id"]))
	if messageID == "" {
		messageID = c.runtime.platformPseudonym("weixin-oc-message", c.ID()+":"+sender+":"+strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	occurred := time.Now().UTC()
	if millis := anyInt64(message["create_time_ms"]); millis > 0 {
		occurred = time.UnixMilli(millis).UTC()
	} else if seconds := anyInt64(message["create_time"]); seconds > 0 {
		occurred = time.Unix(seconds, 0).UTC()
	}
	err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: weixinOCTransport, MessageID: messageID,
		RouteKind: "private", TargetID: sender, ChannelID: sender, ConversationID: sender,
		ConversationKind: "private", SenderID: sender, SenderName: sender, Text: text,
		Attachments: attachments, OccurredAt: occurred, IsWake: true, IsMentionBot: true,
		IsAdmin: nativePlatformIsAdmin(c.admins, sender), IsCommand: nativePlatformIsCommand(text),
	})
	if err == nil {
		c.state.markEvent()
	}
	return changed, err
}

func (c *weixinOCConnector) parseItems(items []map[string]any) (string, []transportAttachment) {
	texts := []string{}
	attachments := []transportAttachment{}
	for _, item := range items {
		itemType := jsonInt(item["type"])
		switch itemType {
		case 1:
			text := strings.TrimSpace(anyString(anyMap(item["text_item"])["text"]))
			if text != "" {
				texts = append(texts, text)
			}
		case 2, 3, 4, 5:
			kind, key, fallback := "file", "file_item", "file.bin"
			if itemType == 2 {
				kind, key, fallback = "image", "image_item", "image.jpg"
			} else if itemType == 3 {
				kind, key, fallback = "audio", "voice_item", "voice.silk"
			} else if itemType == 5 {
				kind, key, fallback = "video", "video_item", "video.mp4"
			}
			container := anyMap(item[key])
			placeholder := map[string]string{"image": "[图片]", "audio": "[语音]", "video": "[视频]", "file": "[文件]"}[kind]
			if itemType == 3 {
				if voiceText := strings.TrimSpace(anyString(container["text"])); voiceText != "" {
					texts = append(texts, voiceText)
					placeholder = ""
				}
			}
			if placeholder != "" {
				texts = append(texts, placeholder)
			}
			media := anyMap(container["media"])
			param := strings.TrimSpace(anyString(media["encrypt_query_param"]))
			if param == "" || c.publicBase == "" {
				continue
			}
			aesKey := strings.TrimSpace(anyString(media["aes_key"]))
			if itemType == 2 && strings.TrimSpace(anyString(container["aeskey"])) != "" {
				rawKey, err := hex.DecodeString(strings.TrimSpace(anyString(container["aeskey"])))
				if err == nil {
					aesKey = base64.StdEncoding.EncodeToString(rawKey)
				}
			}
			name := firstNonEmpty(filepath.Base(anyString(container["file_name"])), fallback)
			id := c.runtime.platformPseudonym("weixin-oc-media", c.ID()+":"+param)
			c.mu.Lock()
			c.media[id] = weixinOCMediaRecord{EncryptedParam: param, AESKey: aesKey, Kind: kind, Name: name, ExpiresAt: time.Now().Add(15 * time.Minute)}
			c.mu.Unlock()
			attachments = append(attachments, transportAttachment{Kind: kind, Name: name, SourceURL: c.publicBase + "/weixin-oc-media/" + url.PathEscape(id)})
		}
	}
	return strings.Join(texts, "\n"), attachments
}

func (c *weixinOCConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	userID := strings.TrimSpace(route.TargetID)
	c.mu.RLock()
	token := c.account.Token
	contextToken := c.account.ContextTokens[userID]
	c.mu.RUnlock()
	if token == "" {
		return &platformDeliveryError{Retryable: true, Reason: "weixin_oc_not_logged_in"}
	}
	if contextToken == "" {
		return &platformDeliveryError{Retryable: false, Reason: "weixin_oc_context_token_missing"}
	}
	items := []map[string]any{}
	if text := strings.TrimSpace(delivery.Message.Text); text != "" {
		items = append(items, map[string]any{"type": 1, "text_item": map[string]any{"text": text}})
	}
	for _, attachment := range delivery.Message.Attachments {
		item, err := c.prepareMediaItem(ctx, userID, attachment)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	if len(items) == 0 {
		return &platformDeliveryError{Retryable: false, Reason: "weixin_oc_empty_message"}
	}
	clientID, _ := newCoreUUID()
	payload := map[string]any{"base_info": map[string]any{"channel_version": "erdai-agent"}, "msg": map[string]any{
		"from_user_id": "", "to_user_id": userID, "client_id": clientID,
		"message_type": 2, "message_state": 2, "context_token": contextToken, "item_list": items,
	}}
	var response struct {
		Ret       int    `json:"ret"`
		ErrorCode int    `json:"errcode"`
		ErrorText string `json:"errmsg"`
	}
	if err := c.requestJSON(ctx, http.MethodPost, "/ilink/bot/sendmessage", nil, payload, true, nil, &response); err != nil {
		return err
	}
	if response.Ret != 0 || response.ErrorCode != 0 {
		return &weixinOCAPIError{Ret: response.Ret, Code: response.ErrorCode, Message: response.ErrorText}
	}
	c.state.markDelivery()
	return nil
}

func (c *weixinOCConnector) prepareMediaItem(ctx context.Context, userID string, attachment agentAttachment) (map[string]any, error) {
	data, path, err := readNativeMedia(attachment)
	if err != nil {
		return nil, err
	}
	key := make([]byte, 16)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	fileKeyBytes := make([]byte, 16)
	if _, err = rand.Read(fileKeyBytes); err != nil {
		return nil, err
	}
	fileKey := hex.EncodeToString(fileKeyBytes)
	keyHex := hex.EncodeToString(key)
	digest := md5.Sum(data)
	itemType, mediaType := 4, 3
	containerKey, sizeKey := "file_item", "len"
	if attachment.Kind == "image" {
		itemType, mediaType, containerKey, sizeKey = 2, 1, "image_item", "mid_size"
	} else if attachment.Kind == "video" {
		itemType, mediaType, containerKey, sizeKey = 5, 2, "video_item", "video_size"
	}
	ciphertext := encryptWeixinOCMedia(data, key)
	payload := map[string]any{
		"filekey": fileKey, "media_type": mediaType, "to_user_id": userID,
		"rawsize": len(data), "rawfilemd5": hex.EncodeToString(digest[:]), "filesize": len(ciphertext),
		"no_need_thumb": true, "aeskey": keyHex, "base_info": map[string]any{"channel_version": "erdai-agent"},
	}
	var upload struct {
		Ret           int    `json:"ret"`
		ErrorCode     int    `json:"errcode"`
		ErrorText     string `json:"errmsg"`
		UploadParam   string `json:"upload_param"`
		UploadFullURL string `json:"upload_full_url"`
	}
	if err = c.requestJSON(ctx, http.MethodPost, "/ilink/bot/getuploadurl", nil, payload, true, nil, &upload); err != nil {
		return nil, err
	}
	if upload.Ret != 0 || upload.ErrorCode != 0 {
		return nil, &weixinOCAPIError{Ret: upload.Ret, Code: upload.ErrorCode, Message: upload.ErrorText}
	}
	uploadURL := strings.TrimSpace(upload.UploadFullURL)
	if uploadURL == "" && strings.TrimSpace(upload.UploadParam) != "" {
		uploadURL = c.cdnBaseURL + "/upload?encrypted_query_param=" + url.QueryEscape(upload.UploadParam) + "&filekey=" + url.QueryEscape(fileKey)
	}
	if uploadURL == "" || !c.safeMediaURL(uploadURL) {
		return nil, errors.New("Weixin Personal returned an unsafe media upload URL")
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, bytes.NewReader(ciphertext))
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Weixin Personal CDN upload returned HTTP %d", response.StatusCode)
	}
	encryptedParam := strings.TrimSpace(response.Header.Get("x-encrypted-param"))
	if encryptedParam == "" {
		return nil, errors.New("Weixin Personal CDN upload response is missing x-encrypted-param")
	}
	media := map[string]any{"encrypt_query_param": encryptedParam, "aes_key": base64.StdEncoding.EncodeToString([]byte(keyHex)), "encrypt_type": 1}
	container := map[string]any{"media": media, sizeKey: len(ciphertext)}
	if itemType == 4 {
		container["file_name"] = firstNonEmpty(attachment.Name, filepath.Base(path), "file.bin")
		container["len"] = strconv.Itoa(len(data))
	}
	return map[string]any{"type": itemType, containerKey: container}, nil
}

func (c *weixinOCConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	id, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/weixin-oc-media/"))
	c.mu.RLock()
	record, found := c.media[id]
	c.mu.RUnlock()
	if r.Method != http.MethodGet || !found || time.Now().After(record.ExpiresAt) {
		http.NotFound(w, r)
		return
	}
	downloadURL := c.cdnBaseURL + "/download?encrypted_query_param=" + url.QueryEscape(record.EncryptedParam)
	if !c.safeMediaURL(downloadURL) {
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, downloadURL, nil)
	response, err := c.client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil || len(data) > maxImageBytes {
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	if record.AESKey != "" {
		key, keyErr := parseWeixinOCMediaKey(record.AESKey)
		if keyErr != nil {
			http.Error(w, "media unavailable", http.StatusBadGateway)
			return
		}
		data, err = decryptWeixinOCMedia(data, key)
		if err != nil {
			http.Error(w, "media unavailable", http.StatusBadGateway)
			return
		}
	}
	if record.Name != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", strings.ReplaceAll(record.Name, `"`, "")))
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(data)
}

func (c *weixinOCConnector) requestJSON(ctx context.Context, method, endpoint string, query map[string]string, payload any, tokenRequired bool, headers map[string]string, output any) error {
	c.mu.RLock()
	baseURL, token := c.baseURL, c.account.Token
	c.mu.RUnlock()
	parsed, err := url.Parse(baseURL + "/" + strings.TrimLeft(endpoint, "/"))
	if err != nil {
		return err
	}
	values := parsed.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	parsed.RawQuery = values.Encode()
	var body io.Reader
	if payload != nil {
		encoded, encodeErr := json.Marshal(payload)
		if encodeErr != nil {
			return encodeErr
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, parsed.String(), body)
	if err != nil {
		return err
	}
	uin := make([]byte, 4)
	_, _ = rand.Read(uin)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("AuthorizationType", "ilink_bot_token")
	request.Header.Set("X-WECHAT-UIN", base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(uint32(uin[0])<<24|uint32(uin[1])<<16|uint32(uin[2])<<8|uint32(uin[3])), 10))))
	if tokenRequired {
		if token == "" {
			return errors.New("Weixin Personal bot token is missing")
		}
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Weixin Personal %s returned HTTP %d", endpoint, response.StatusCode)
	}
	if output == nil || len(bytes.TrimSpace(data)) == 0 {
		return nil
	}
	return json.Unmarshal(data, output)
}

func (c *weixinOCConnector) safeMediaURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	return parsed.Scheme == "http" && isLoopbackBindHost(parsed.Hostname())
}

func (c *weixinOCConnector) loadAccountState(ctx context.Context) error {
	var ciphertext []byte
	err := c.runtime.configStore.db.QueryRowContext(ctx, `SELECT state_cipher FROM platform_connector_state WHERE connector_id = ?`, c.ID()).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	plaintext, err := c.runtime.decrypt(ciphertext)
	if err != nil {
		return err
	}
	var state weixinOCAccountState
	if err = json.Unmarshal(plaintext, &state); err != nil {
		return err
	}
	if state.ContextTokens == nil {
		state.ContextTokens = map[string]string{}
	}
	c.account = state
	if strings.TrimSpace(state.BaseURL) != "" {
		c.baseURL = strings.TrimRight(state.BaseURL, "/")
	}
	return nil
}

func (c *weixinOCConnector) saveAccountState(ctx context.Context) error {
	c.mu.RLock()
	state := weixinOCAccountState{
		Token: c.account.Token, AccountID: c.account.AccountID, SyncBuffer: c.account.SyncBuffer,
		BaseURL: c.baseURL, ContextTokens: map[string]string{},
	}
	for userID, token := range c.account.ContextTokens {
		state.ContextTokens[userID] = token
	}
	c.mu.RUnlock()
	encoded, err := json.Marshal(state)
	if err != nil {
		return err
	}
	ciphertext, err := c.runtime.encrypt(encoded)
	if err != nil {
		return err
	}
	_, err = c.runtime.configStore.db.ExecContext(ctx, `INSERT INTO platform_connector_state (connector_id, state_cipher, updated_at)
		VALUES (?, ?, ?) ON CONFLICT(connector_id) DO UPDATE SET state_cipher = excluded.state_cipher, updated_at = excluded.updated_at`,
		c.ID(), ciphertext, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (c *weixinOCConnector) clearAccountState(ctx context.Context) error {
	c.mu.Lock()
	c.account = weixinOCAccountState{ContextTokens: map[string]string{}}
	c.login = weixinOCLoginState{}
	c.lastPollErr = "session expired"
	c.mu.Unlock()
	return c.saveAccountState(ctx)
}

func encryptWeixinOCMedia(data, key []byte) []byte {
	pad := aes.BlockSize - len(data)%aes.BlockSize
	padded := append(append([]byte{}, data...), bytes.Repeat([]byte{byte(pad)}, pad)...)
	block, _ := aes.NewCipher(key)
	output := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(output[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	return output
}

func decryptWeixinOCMedia(data, key []byte) ([]byte, error) {
	if len(key) != 16 || len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("invalid Weixin Personal media ciphertext")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	output := make([]byte, len(data))
	for offset := 0; offset < len(data); offset += aes.BlockSize {
		block.Decrypt(output[offset:offset+aes.BlockSize], data[offset:offset+aes.BlockSize])
	}
	pad := int(output[len(output)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(output) || !bytes.Equal(output[len(output)-pad:], bytes.Repeat([]byte{byte(pad)}, pad)) {
		return nil, errors.New("invalid Weixin Personal media padding")
	}
	return output[:len(output)-pad], nil
}

func parseWeixinOCMediaKey(value string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(value + strings.Repeat("=", (4-len(value)%4)%4))
	if err != nil {
		return nil, err
	}
	if len(decoded) == 16 {
		return decoded, nil
	}
	if len(decoded) == 32 {
		key, hexErr := hex.DecodeString(string(decoded))
		if hexErr == nil && len(key) == 16 {
			return key, nil
		}
	}
	return nil, errors.New("unsupported Weixin Personal media key")
}

func anyMap(value any) map[string]any {
	if typed, ok := value.(map[string]any); ok {
		return typed
	}
	return map[string]any{}
}

func anyObjectSlice(value any) []map[string]any {
	if typed, ok := value.([]map[string]any); ok {
		return typed
	}
	values, _ := value.([]any)
	output := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			output = append(output, item)
		}
	}
	return output
}

func anyString(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	return ""
}

func anyInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		result, _ := typed.Int64()
		return result
	default:
		return 0
	}
}
