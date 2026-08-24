package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const wecomAIBotTransport = "wecom_ai_bot"

type wecomAIBotConnector struct {
	runtime     *AgentRuntime
	platform    mgmtPlatform
	mode        string
	botID       string
	secret      string
	botName     string
	token       string
	initialText string
	wsURL       string
	heartbeat   time.Duration
	pushWebhook string
	publicBase  string
	host        string
	port        int
	webhookUUID string
	crypto      *wechatWebhookCrypto
	admins      map[string]struct{}
	client      *http.Client
	dialer      *websocket.Dialer
	state       nativeConnectorState
	cancel      context.CancelFunc
	server      *platformWebhookServer
	connMu      sync.RWMutex
	conn        *websocket.Conn
	writeMu     sync.Mutex
	waitMu      sync.Mutex
	waiters     map[string]chan map[string]any
	mediaMu     sync.RWMutex
	media       map[string]wecomAIMediaRecord
}

type wecomAIMediaRecord struct {
	URL       string
	AESKey    string
	ExpiresAt time.Time
}

func newWecomAIBotConnector(runtime *AgentRuntime, platform mgmtPlatform) (*wecomAIBotConnector, error) {
	mode, _ := platform.Settings["wecom_ai_bot_connection_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "long_connection"
	}
	if mode != "long_connection" && mode != "webhook" {
		return nil, errors.New("WeCom AI Bot connection mode must be long_connection or webhook")
	}
	value := &wecomAIBotConnector{
		runtime: runtime, platform: platform, mode: mode,
		secret:      resolvePlatformCredential(platform, "wecomaibot_ws_secret"),
		token:       resolvePlatformCredential(platform, "wecomaibot_token"),
		pushWebhook: resolvePlatformCredential(platform, "msg_push_webhook_url"),
		webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"),
		admins:      nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		dialer: websocket.DefaultDialer, state: newNativeConnectorState(platform),
		waiters: map[string]chan map[string]any{}, media: map[string]wecomAIMediaRecord{},
	}
	value.botID, _ = platform.Settings["wecomaibot_ws_bot_id"].(string)
	value.botName, _ = platform.Settings["wecom_ai_bot_name"].(string)
	value.initialText, _ = platform.Settings["wecomaibot_init_respond_text"].(string)
	value.wsURL, _ = platform.Settings["wecomaibot_ws_url"].(string)
	if strings.TrimSpace(value.wsURL) == "" {
		value.wsURL = "wss://openws.work.weixin.qq.com"
	}
	value.heartbeat = time.Duration(kookIntSetting(platform, "wecomaibot_heartbeat_interval", 30)) * time.Second
	value.publicBase, _ = platform.Settings["wecom_ai_public_base_url"].(string)
	value.publicBase = strings.TrimRight(value.publicBase, "/")
	value.host, _ = platform.Settings["callback_server_host"].(string)
	if strings.TrimSpace(value.host) == "" {
		value.host = "0.0.0.0"
	}
	value.port = kookIntSetting(platform, "port", 6198)
	if mode == "long_connection" {
		if strings.TrimSpace(value.botID) == "" || value.secret == "" {
			return nil, platformConnectorStartupError(platform, "wecomaibot_ws_bot_id/wecomaibot_ws_secret")
		}
	} else {
		if value.token == "" {
			return nil, platformConnectorStartupError(platform, "wecomaibot_token")
		}
		crypto, err := newWechatJSONCrypto(value.token, resolvePlatformCredential(platform, "wecomaibot_encoding_aes_key"))
		if err != nil {
			return nil, err
		}
		value.crypto = crypto
	}
	if _, err := runtime.db.Exec(`
		CREATE TABLE IF NOT EXISTS wecom_ai_streams (
			connector_id TEXT NOT NULL,
			stream_id TEXT NOT NULL,
			content_cipher BLOB,
			finished INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT NOT NULL,
			PRIMARY KEY(connector_id, stream_id)
		)
	`); err != nil {
		return nil, err
	}
	return value, nil
}

func (c *wecomAIBotConnector) ID() string   { return c.platform.ID }
func (c *wecomAIBotConnector) Type() string { return wecomAIBotTransport }

func (c *wecomAIBotConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if c.mode == "webhook" || c.publicBase != "" {
		mux := http.NewServeMux()
		if c.mode == "webhook" {
			mux.HandleFunc(c.webhookPath(), c.handleWebhook)
		}
		mux.HandleFunc("/wecom-ai-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.host, c.port, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	if c.mode == "webhook" {
		c.state.setStatus("connected")
		return nil
	}
	c.state.setStatus("connecting")
	go c.runLongConnection(ctx)
	return nil
}

func (c *wecomAIBotConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *wecomAIBotConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *wecomAIBotConnector) webhookPath() string {
	if c.webhookUUID != "" {
		return "/webhooks/" + url.PathEscape(c.webhookUUID)
	}
	return "/webhook/wecom-ai-bot"
}

func (c *wecomAIBotConnector) runLongConnection(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		err := c.longConnectionOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		c.state.setError(err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay *= 2
		}
		c.state.setStatus("reconnecting")
	}
}

func (c *wecomAIBotConnector) longConnectionOnce(ctx context.Context) error {
	conn, response, err := c.dialer.DialContext(ctx, c.wsURL, nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		return err
	}
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	defer func() {
		_ = conn.Close()
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.connMu.Unlock()
	}()
	reqID := newWecomAIRequestID()
	if err = c.writeLong(map[string]any{
		"cmd": "aibot_subscribe", "headers": map[string]any{"req_id": reqID},
		"body": map[string]any{"bot_id": c.botID, "secret": c.secret},
	}); err != nil {
		return err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var subscribed map[string]any
	if err = conn.ReadJSON(&subscribed); err != nil {
		return err
	}
	if code := jsonInt(subscribed["errcode"]); code != 0 {
		return fmt.Errorf("WeCom AI subscribe failed: %d", code)
	}
	_ = conn.SetReadDeadline(time.Time{})
	c.state.setStatus("connected")
	heartbeat := time.NewTicker(c.heartbeat)
	defer heartbeat.Stop()
	readErr := make(chan error, 1)
	go func() {
		for {
			var payload map[string]any
			if err := conn.ReadJSON(&payload); err != nil {
				readErr <- err
				return
			}
			c.handleLongPayload(ctx, payload)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-readErr:
			return err
		case <-heartbeat.C:
			if _, err = c.sendLongCommand(ctx, "ping", newWecomAIRequestID(), nil); err != nil {
				return err
			}
		}
	}
}

func (c *wecomAIBotConnector) handleLongPayload(ctx context.Context, payload map[string]any) {
	headers, _ := payload["headers"].(map[string]any)
	reqID, _ := headers["req_id"].(string)
	cmd, _ := payload["cmd"].(string)
	if cmd == "aibot_msg_callback" {
		body, _ := payload["body"].(map[string]any)
		if body != nil {
			streamID := c.inboundMessage(ctx, body, reqID)
			if c.initialText != "" && streamID != "" {
				go func() {
					_, _ = c.sendLongCommand(ctx, "aibot_respond_msg", reqID, wecomAIStreamBody(streamID, c.initialText, false, nil))
				}()
			}
		}
		return
	}
	if reqID != "" {
		c.waitMu.Lock()
		waiter := c.waiters[reqID]
		c.waitMu.Unlock()
		if waiter != nil {
			select {
			case waiter <- payload:
			default:
			}
		}
	}
}

func (c *wecomAIBotConnector) sendLongCommand(ctx context.Context, cmd, reqID string, body map[string]any) (map[string]any, error) {
	waiter := make(chan map[string]any, 1)
	c.waitMu.Lock()
	c.waiters[reqID] = waiter
	c.waitMu.Unlock()
	defer func() {
		c.waitMu.Lock()
		delete(c.waiters, reqID)
		c.waitMu.Unlock()
	}()
	payload := map[string]any{"cmd": cmd, "headers": map[string]any{"req_id": reqID}}
	if body != nil {
		payload["body"] = body
	}
	if err := c.writeLong(payload); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-waiter:
		if code := jsonInt(response["errcode"]); code != 0 {
			return response, fmt.Errorf("WeCom AI command %s failed: %d", cmd, code)
		}
		return response, nil
	case <-time.After(10 * time.Second):
		return nil, errors.New("WeCom AI command timed out")
	}
}

func (c *wecomAIBotConnector) writeLong(payload map[string]any) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return errors.New("WeCom AI long connection is not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(payload)
}

func (c *wecomAIBotConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	signature, timestamp, nonce := query.Get("msg_signature"), query.Get("timestamp"), query.Get("nonce")
	if r.Method == http.MethodGet {
		echo := query.Get("echostr")
		plaintext, err := c.crypto.decryptMessage(echo, signature, timestamp, nonce)
		if err != nil {
			http.Error(w, "verify failed", http.StatusBadRequest)
			return
		}
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
	var envelope struct {
		Encrypt string `json:"encrypt"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Encrypt == "" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	plaintext, err := c.crypto.decryptMessage(envelope.Encrypt, signature, timestamp, nonce)
	if err != nil {
		http.Error(w, "decrypt failed", http.StatusBadRequest)
		return
	}
	var message map[string]any
	if json.Unmarshal(plaintext, &message) != nil {
		http.Error(w, "bad message", http.StatusBadRequest)
		return
	}
	msgType, _ := message["msgtype"].(string)
	var responseBody map[string]any
	if msgType == "stream" {
		stream, _ := message["stream"].(map[string]any)
		streamID, _ := stream["id"].(string)
		content, attachments, finished := c.loadStream(r.Context(), streamID)
		responseBody = wecomAIStreamBody(streamID, content, finished, attachments)
	} else {
		streamID := c.inboundMessage(r.Context(), message, "")
		responseBody = wecomAIStreamBody(streamID, c.initialText, false, nil)
	}
	encoded, _ := json.Marshal(responseBody)
	response, err := c.encryptJSON(encoded, timestamp, nonce)
	if err != nil {
		http.Error(w, "encrypt failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(response)
}

func (c *wecomAIBotConnector) inboundMessage(ctx context.Context, message map[string]any, reqID string) string {
	from, _ := message["from"].(map[string]any)
	senderID, _ := from["userid"].(string)
	chatType, _ := message["chattype"].(string)
	chatID, _ := message["chatid"].(string)
	private := chatType != "group"
	if private {
		chatID = senderID
	}
	text, attachments := c.parseMessage(message)
	if text == "" {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	messageID, _ := message["msgid"].(string)
	if messageID == "" {
		messageID = c.runtime.platformPseudonym("wecom-ai-message", c.ID()+":"+chatID+":"+strconv.FormatInt(time.Now().UnixNano(), 10))
	}
	streamID := c.runtime.platformPseudonym("wecom-ai-stream", c.ID()+":"+messageID)
	routeKind, conversationKind, targetID := "stream", "group", streamID
	if private {
		conversationKind = "private"
	}
	if reqID != "" {
		routeKind, targetID = "long_connection", reqID
	}
	_ = c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: wecomAIBotTransport, MessageID: messageID,
		RouteKind: routeKind, TargetID: targetID, GuildID: streamID, ChannelID: chatID,
		ConversationID: chatID, ConversationKind: conversationKind,
		SenderID: senderID, SenderName: senderID, Text: text, Attachments: attachments,
		IsWake: true, IsMentionBot: true, IsAdmin: nativePlatformIsAdmin(c.admins, senderID),
		IsCommand: nativePlatformIsCommand(text),
	})
	_, _ = c.runtime.db.ExecContext(ctx, `INSERT OR IGNORE INTO wecom_ai_streams (connector_id, stream_id, finished, updated_at) VALUES (?, ?, 0, ?)`, c.ID(), streamID, time.Now().UTC().Format(time.RFC3339Nano))
	c.state.markEvent()
	return streamID
}

func (c *wecomAIBotConnector) parseMessage(message map[string]any) (string, []transportAttachment) {
	msgType, _ := message["msgtype"].(string)
	items := []map[string]any{message}
	if msgType == "mixed" {
		mixed, _ := message["mixed"].(map[string]any)
		rawItems, _ := mixed["msg_item"].([]any)
		items = nil
		for _, raw := range rawItems {
			if item, ok := raw.(map[string]any); ok {
				items = append(items, item)
			}
		}
	}
	texts := []string{}
	attachments := []transportAttachment{}
	for _, item := range items {
		kind, _ := item["msgtype"].(string)
		if kind == "text" {
			text, _ := item["text"].(map[string]any)
			content, _ := text["content"].(string)
			content = strings.TrimSpace(strings.ReplaceAll(content, "@"+c.botName, ""))
			if content != "" {
				texts = append(texts, content)
			}
		}
		if kind == "image" && c.publicBase != "" {
			image, _ := item["image"].(map[string]any)
			source, _ := image["url"].(string)
			aesKey, _ := image["aeskey"].(string)
			if source != "" {
				id := c.runtime.platformPseudonym("wecom-ai-media", c.ID()+":"+source)
				c.mediaMu.Lock()
				c.media[id] = wecomAIMediaRecord{URL: source, AESKey: aesKey, ExpiresAt: time.Now().Add(15 * time.Minute)}
				c.mediaMu.Unlock()
				attachments = append(attachments, transportAttachment{Kind: "image", SourceURL: c.publicBase + "/wecom-ai-media/" + url.PathEscape(id)})
			}
		}
	}
	return strings.Join(texts, " "), attachments
}

func (c *wecomAIBotConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	id, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/wecom-ai-media/"))
	c.mediaMu.RLock()
	record, found := c.media[id]
	c.mediaMu.RUnlock()
	if r.Method != http.MethodGet || !found || time.Now().After(record.ExpiresAt) {
		http.NotFound(w, r)
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, record.URL, nil)
	if err != nil {
		http.NotFound(w, r)
		return
	}
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
		data, err = decryptWecomAIMedia(data, record.AESKey)
		if err != nil {
			http.Error(w, "media unavailable", http.StatusBadGateway)
			return
		}
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	_, _ = w.Write(data)
}

func (c *wecomAIBotConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	streamID := route.GuildID
	if streamID == "" {
		streamID = route.MessageID
	}
	attachments := []map[string]any{}
	for _, attachment := range delivery.Message.Attachments {
		if attachment.Kind != "image" {
			continue
		}
		data, _, err := readNativeMedia(attachment)
		if err != nil {
			return err
		}
		digest := md5.Sum(data)
		attachments = append(attachments, map[string]any{"msgtype": "image", "image": map[string]any{
			"base64": base64.StdEncoding.EncodeToString(data), "md5": hex.EncodeToString(digest[:]),
		}})
	}
	if route.Kind == "long_connection" {
		_, err := c.sendLongCommand(ctx, "aibot_respond_msg", route.TargetID, wecomAIStreamBody(streamID, delivery.Message.Text, true, attachments))
		if err != nil {
			return err
		}
		c.state.markDelivery()
		return nil
	}
	payload, _ := json.Marshal(map[string]any{"content": delivery.Message.Text, "attachments": attachments})
	ciphertext, err := c.runtime.encrypt(payload)
	if err != nil {
		return err
	}
	_, err = c.runtime.db.ExecContext(ctx, `UPDATE wecom_ai_streams SET content_cipher = ?, finished = 1, updated_at = ? WHERE connector_id = ? AND stream_id = ?`, ciphertext, time.Now().UTC().Format(time.RFC3339Nano), c.ID(), streamID)
	if err == nil {
		c.state.markDelivery()
	}
	return err
}

func (c *wecomAIBotConnector) loadStream(ctx context.Context, streamID string) (string, []map[string]any, bool) {
	var ciphertext []byte
	var finished int
	if c.runtime.db.QueryRowContext(ctx, `SELECT content_cipher, finished FROM wecom_ai_streams WHERE connector_id = ? AND stream_id = ?`, c.ID(), streamID).Scan(&ciphertext, &finished) != nil || len(ciphertext) == 0 {
		return "", nil, false
	}
	plaintext, err := c.runtime.decrypt(ciphertext)
	if err != nil {
		return "", nil, false
	}
	var payload struct {
		Content     string           `json:"content"`
		Attachments []map[string]any `json:"attachments"`
	}
	if json.Unmarshal(plaintext, &payload) != nil {
		return "", nil, false
	}
	return payload.Content, payload.Attachments, finished == 1
}

func (c *wecomAIBotConnector) encryptJSON(message []byte, timestamp, nonce string) ([]byte, error) {
	randomPrefix := make([]byte, 16)
	if _, err := rand.Read(randomPrefix); err != nil {
		return nil, err
	}
	length := []byte{byte(len(message) >> 24), byte(len(message) >> 16), byte(len(message) >> 8), byte(len(message))}
	plaintext := wechatPad(append(append(append([]byte{}, randomPrefix...), length...), message...))
	block, err := aes.NewCipher(c.crypto.key)
	if err != nil {
		return nil, err
	}
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, c.crypto.key[:aes.BlockSize]).CryptBlocks(ciphertext, plaintext)
	encrypted := base64.StdEncoding.EncodeToString(ciphertext)
	return json.Marshal(map[string]string{
		"encrypt": encrypted, "msgsignature": wechatSignature(c.crypto.token, timestamp, nonce, encrypted),
		"timestamp": timestamp, "nonce": nonce,
	})
}

func wecomAIStreamBody(streamID, content string, finished bool, items []map[string]any) map[string]any {
	stream := map[string]any{"id": streamID, "finish": finished, "content": content}
	if len(items) > 0 {
		stream["msg_item"] = items
	}
	return map[string]any{"msgtype": "stream", "stream": stream}
}

func newWecomAIRequestID() string {
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func jsonInt(value any) int {
	switch typed := value.(type) {
	case float64:
		return int(typed)
	case int:
		return typed
	case json.Number:
		value, _ := typed.Int64()
		return int(value)
	default:
		return 0
	}
}

func decryptWecomAIMedia(data []byte, encodedKey string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey + strings.Repeat("=", (4-len(encodedKey)%4)%4))
	if err != nil || len(key) != 32 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("WeCom AI media key is invalid")
	}
	plaintext := make([]byte, len(data))
	block, _ := aes.NewCipher(key)
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plaintext, data)
	return wechatUnpad(plaintext)
}
