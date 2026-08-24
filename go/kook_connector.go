package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const kookTransport = "kook"

var kookMentionPattern = regexp.MustCompile(`\(met\)([^()]+)\(met\)`)
var kookAtPrefixPattern = regexp.MustCompile(`^@[^\s]+(\s*-\s*[^\s]+)?\s*`)

type kookConnector struct {
	runtime              *AgentRuntime
	platform             mgmtPlatform
	token                string
	apiBase              string
	gatewayURL           string
	adminIDs             map[string]struct{}
	client               *http.Client
	dialer               *websocket.Dialer
	state                nativeConnectorState
	cancel               context.CancelFunc
	connMu               sync.RWMutex
	conn                 *websocket.Conn
	writeMu              sync.Mutex
	botID                string
	botUsername          string
	botNickname          string
	lastSN               atomic.Int64
	lastPong             atomic.Int64
	heartbeatInterval    time.Duration
	heartbeatTimeout     time.Duration
	maxHeartbeatFailures int
	reconnectDelay       time.Duration
	maxReconnectDelay    time.Duration
}

type kookGatewayEnvelope struct {
	Signal int             `json:"s"`
	Data   json.RawMessage `json:"d"`
	SN     *int64          `json:"sn"`
}

type kookAuthor struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Nickname string `json:"nickname"`
	Bot      bool   `json:"bot"`
}

type kookMention struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

type kookExtra struct {
	Author     *kookAuthor `json:"author"`
	GuildID    string      `json:"guild_id"`
	Mention    []string    `json:"mention"`
	MentionAll bool        `json:"mention_all"`
	KMarkdown  *struct {
		RawContent  string        `json:"raw_content"`
		MentionPart []kookMention `json:"mention_part"`
	} `json:"kmarkdown"`
}

type kookMessageEvent struct {
	ChannelType string          `json:"channel_type"`
	Type        int             `json:"type"`
	TargetID    string          `json:"target_id"`
	AuthorID    string          `json:"author_id"`
	Content     json.RawMessage `json:"content"`
	MessageID   string          `json:"msg_id"`
	Timestamp   int64           `json:"msg_timestamp"`
	Extra       kookExtra       `json:"extra"`
}

type kookAPIResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type kookHTTPError struct {
	Status int
	Body   string
}

func (e *kookHTTPError) Error() string {
	return fmt.Sprintf("KOOK status %d: %s", e.Status, e.Body)
}

func newKookConnector(runtime *AgentRuntime, platform mgmtPlatform) (*kookConnector, error) {
	token := resolvePlatformCredential(platform, "kook_bot_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "kook_bot_token")
	}
	apiBase, _ := platform.Settings["kook_api_base_url"].(string)
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = "https://www.kookapp.cn/api/v3"
	}
	gatewayURL, _ := platform.Settings["kook_gateway_url"].(string)
	return &kookConnector{
		runtime: runtime, platform: platform, token: token, apiBase: apiBase,
		gatewayURL: strings.TrimSpace(gatewayURL), adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Timeout: 30 * time.Second}, dialer: websocket.DefaultDialer,
		state:                newNativeConnectorState(platform),
		heartbeatInterval:    kookDurationSetting(platform, "kook_heartbeat_interval", 30*time.Second),
		heartbeatTimeout:     kookDurationSetting(platform, "kook_heartbeat_timeout", 6*time.Second),
		maxHeartbeatFailures: kookIntSetting(platform, "kook_max_heartbeat_failures", 3),
		reconnectDelay:       kookDurationSetting(platform, "kook_reconnect_delay", time.Second),
		maxReconnectDelay:    kookDurationSetting(platform, "kook_max_reconnect_delay", 60*time.Second),
	}, nil
}

func kookDurationSetting(platform mgmtPlatform, field string, fallback time.Duration) time.Duration {
	value, ok := platform.Settings[field].(float64)
	if !ok || value <= 0 {
		return fallback
	}
	return time.Duration(value * float64(time.Second))
}

func kookIntSetting(platform mgmtPlatform, field string, fallback int) int {
	value, ok := platform.Settings[field].(float64)
	if !ok || value < 1 {
		return fallback
	}
	return int(value)
}

func (c *kookConnector) ID() string   { return c.platform.ID }
func (c *kookConnector) Type() string { return kookTransport }

func (c *kookConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.state.setStatus("connecting")
	go c.run(ctx)
	return nil
}

func (c *kookConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	c.state.setStatus("stopped")
	return nil
}

func (c *kookConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *kookConnector) run(ctx context.Context) {
	delay := c.reconnectDelay
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
		if !waitContext(ctx, delay) {
			return
		}
		if delay < c.maxReconnectDelay {
			delay *= 2
			if delay > c.maxReconnectDelay {
				delay = c.maxReconnectDelay
			}
		}
	}
}

func (c *kookConnector) runSession(ctx context.Context) error {
	if err := c.loadIdentity(ctx); err != nil {
		return err
	}
	gatewayURL, err := c.resolveGateway(ctx)
	if err != nil {
		return err
	}
	conn, _, err := c.dialer.DialContext(ctx, gatewayURL, nil)
	if err != nil {
		return err
	}
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	defer func() {
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
		}
		c.connMu.Unlock()
		_ = conn.Close()
	}()
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go c.heartbeat(heartbeatCtx, conn)
	for ctx.Err() == nil {
		envelope, readErr := c.readGateway(conn)
		if readErr != nil {
			return readErr
		}
		if envelope.SN != nil {
			c.lastSN.Store(*envelope.SN)
		}
		switch envelope.Signal {
		case 0:
			if err = c.handleMessage(ctx, envelope.Data); err != nil {
				return err
			}
		case 1:
			var hello struct {
				Code      int    `json:"code"`
				SessionID string `json:"session_id"`
			}
			if err = json.Unmarshal(envelope.Data, &hello); err != nil {
				return err
			}
			if hello.Code != 0 {
				return fmt.Errorf("KOOK gateway hello failed with code %d", hello.Code)
			}
			c.lastPong.Store(time.Now().UnixNano())
			c.state.setStatus("connected")
		case 2:
			if err = c.writeGateway(conn, map[string]any{"s": 3, "sn": c.lastSN.Load()}); err != nil {
				return err
			}
		case 3:
			c.lastPong.Store(time.Now().UnixNano())
		case 5:
			c.lastSN.Store(0)
			return errors.New("KOOK gateway requested reconnect")
		case 6:
			c.state.setStatus("connected")
		}
	}
	return ctx.Err()
}

func (c *kookConnector) loadIdentity(ctx context.Context) error {
	var identity struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Nickname string `json:"nickname"`
	}
	if err := c.kookJSON(ctx, http.MethodGet, "/user/me", nil, nil, &identity); err != nil {
		return err
	}
	if identity.ID == "" {
		return errors.New("KOOK bot identity is empty")
	}
	c.botID, c.botUsername, c.botNickname = identity.ID, identity.Username, identity.Nickname
	return nil
}

func (c *kookConnector) resolveGateway(ctx context.Context) (string, error) {
	if c.gatewayURL != "" {
		return c.gatewayURL, nil
	}
	query := url.Values{}
	if c.lastSN.Load() > 0 {
		query.Set("resume", "1")
		query.Set("sn", fmt.Sprint(c.lastSN.Load()))
	}
	var response struct {
		URL string `json:"url"`
	}
	if err := c.kookJSON(ctx, http.MethodGet, "/gateway/index", query, nil, &response); err != nil {
		return "", err
	}
	if response.URL == "" {
		return "", errors.New("KOOK gateway URL is empty")
	}
	return response.URL, nil
}

func (c *kookConnector) readGateway(conn *websocket.Conn) (kookGatewayEnvelope, error) {
	messageType, payload, err := conn.ReadMessage()
	if err != nil {
		return kookGatewayEnvelope{}, err
	}
	if messageType == websocket.BinaryMessage {
		reader, zipErr := zlib.NewReader(bytes.NewReader(payload))
		if zipErr != nil {
			return kookGatewayEnvelope{}, fmt.Errorf("KOOK gateway decompression failed: %w", zipErr)
		}
		payload, err = io.ReadAll(io.LimitReader(reader, 4<<20))
		closeErr := reader.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return kookGatewayEnvelope{}, err
		}
	}
	var envelope kookGatewayEnvelope
	if err = json.Unmarshal(payload, &envelope); err != nil {
		return envelope, err
	}
	return envelope, nil
}

func (c *kookConnector) heartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastPong := time.Unix(0, c.lastPong.Load())
			if !lastPong.IsZero() && time.Since(lastPong) > c.heartbeatInterval+c.heartbeatTimeout {
				failures++
			} else {
				failures = 0
			}
			if failures >= c.maxHeartbeatFailures || c.writeGateway(conn, map[string]any{"s": 2, "sn": c.lastSN.Load()}) != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *kookConnector) writeGateway(conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *kookConnector) handleMessage(ctx context.Context, raw json.RawMessage) error {
	var message kookMessageEvent
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	if message.MessageID == "" || message.AuthorID == "" || message.AuthorID == c.botID || message.Type == 255 {
		return nil
	}
	if message.Extra.Author != nil && message.Extra.Author.Bot {
		return nil
	}
	text, attachments := parseKookMessage(message)
	mentionBot := kookContains(message.Extra.Mention, c.botID) || kookTextMentions(text, c.botID)
	text = cleanKookMentions(text, c.botID)
	if mentionBot {
		text = strings.TrimSpace(kookAtPrefixPattern.ReplaceAllString(text, ""))
	}
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	isGroup := message.ChannelType == "GROUP" || message.ChannelType == "BROADCAST"
	conversationKind := "private"
	targetID, routeKind := message.AuthorID, "private"
	if isGroup {
		conversationKind, targetID, routeKind = "group", message.TargetID, "group"
	}
	if targetID == "" {
		return errors.New("KOOK message target is empty")
	}
	command := nativePlatformIsCommand(text)
	occurredAt := time.Now().UTC()
	if message.Timestamp > 0 {
		if message.Timestamp > 10_000_000_000 {
			occurredAt = time.UnixMilli(message.Timestamp).UTC()
		} else {
			occurredAt = time.Unix(message.Timestamp, 0).UTC()
		}
	}
	senderName := message.AuthorID
	if message.Extra.Author != nil {
		senderName = firstNonEmpty(message.Extra.Author.Nickname, message.Extra.Author.Username, message.AuthorID)
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: kookTransport, MessageID: message.MessageID,
		RouteKind: routeKind, TargetID: targetID, GuildID: message.Extra.GuildID, ChannelID: message.TargetID,
		ConversationID: targetID, ConversationKind: conversationKind,
		SenderID: message.AuthorID, SenderName: senderName, Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, message.AuthorID),
		IsMentionBot: mentionBot, IsCommand: command, IsAtAll: message.Extra.MentionAll,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func parseKookMessage(message kookMessageEvent) (string, []transportAttachment) {
	content := kookRawString(message.Content)
	switch message.Type {
	case 2, 3, 4, 8:
		kind := map[int]string{2: "image", 3: "video", 4: "file", 8: "audio"}[message.Type]
		if strings.HasPrefix(content, "http://") || strings.HasPrefix(content, "https://") {
			return "", []transportAttachment{{Kind: kind, SourceURL: content, Name: filepath.Base(content)}}
		}
	case 10:
		return parseKookCard(content)
	case 9:
		if message.Extra.KMarkdown != nil && strings.TrimSpace(message.Extra.KMarkdown.RawContent) != "" {
			return strings.TrimSpace(message.Extra.KMarkdown.RawContent), nil
		}
	}
	return strings.TrimSpace(content), nil
}

func kookRawString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}

func parseKookCard(content string) (string, []transportAttachment) {
	var cards any
	if json.Unmarshal([]byte(content), &cards) != nil {
		return "[卡片消息]", nil
	}
	texts := []string{}
	attachments := []transportAttachment{}
	var walk func(any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			moduleType, _ := typed["type"].(string)
			if source, _ := typed["src"].(string); source != "" && len(attachments) < 3 {
				kind := map[string]string{"image": "image", "video": "video", "audio": "audio", "file": "file"}[moduleType]
				if kind != "" {
					name, _ := typed["title"].(string)
					attachments = append(attachments, transportAttachment{Kind: kind, SourceURL: source, Name: name})
				}
			}
			if moduleType == "plain-text" || moduleType == "kmarkdown" {
				if text, _ := typed["content"].(string); strings.TrimSpace(text) != "" {
					texts = append(texts, strings.TrimSpace(text))
				}
			}
			for key, item := range typed {
				if key != "src" && key != "content" && key != "title" {
					walk(item)
				}
			}
		}
	}
	walk(cards)
	return strings.TrimSpace(strings.Join(texts, "\n")), attachments
}

func kookContains(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target && target != "" {
			return true
		}
	}
	return false
}

func kookTextMentions(text, botID string) bool {
	if botID == "" {
		return false
	}
	for _, match := range kookMentionPattern.FindAllStringSubmatch(text, -1) {
		if len(match) > 1 && strings.TrimSpace(match[1]) == botID {
			return true
		}
	}
	return false
}

func cleanKookMentions(text, botID string) string {
	return strings.TrimSpace(kookMentionPattern.ReplaceAllStringFunc(text, func(value string) string {
		match := kookMentionPattern.FindStringSubmatch(value)
		if len(match) > 1 && strings.TrimSpace(match[1]) == botID {
			return ""
		}
		return value
	}))
}

func (c *kookConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.TargetID == "" {
		return &platformDeliveryError{Retryable: false, Reason: "kook_target_missing"}
	}
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 4000) {
		if err := c.sendMessage(ctx, route, text, 9); err != nil {
			return kookDeliveryError("kook_text_send_failed", err)
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		remoteURL, err := c.uploadAsset(ctx, attachment)
		if err != nil {
			return err
		}
		messageType := map[string]int{"image": 2, "video": 3, "file": 4, "audio": 8}[strings.ToLower(attachment.Kind)]
		if messageType == 0 {
			return &platformDeliveryError{Retryable: false, Reason: "kook_attachment_kind_unsupported"}
		}
		if err = c.sendMessage(ctx, route, remoteURL, messageType); err != nil {
			return kookDeliveryError("kook_attachment_send_failed", err)
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *kookConnector) sendMessage(ctx context.Context, route platformReplyRoute, content string, messageType int) error {
	path := "/message/create"
	if route.Kind == "private" {
		path = "/direct-message/create"
	}
	payload := map[string]any{"target_id": route.TargetID, "content": content, "type": messageType}
	if route.MessageID != "" {
		payload["quote"] = route.MessageID
		payload["reply_msg_id"] = route.MessageID
	}
	return c.kookJSON(ctx, http.MethodPost, path, nil, payload, nil)
}

func (c *kookConnector) uploadAsset(ctx context.Context, attachment agentAttachment) (string, error) {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "kook_attachment_invalid", Cause: err}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", firstNonEmpty(attachment.Name, filepath.Base(cleanPath)))
	if err == nil {
		_, err = part.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "kook_attachment_encode_failed", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/asset/create", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bot "+c.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return "", &platformDeliveryError{Retryable: true, Reason: "kook_asset_upload_failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", kookDeliveryError("kook_asset_upload_failed", &kookHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))})
	}
	var envelope kookAPIResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
		return "", &platformDeliveryError{Retryable: true, Reason: "kook_asset_upload_failed", Cause: err}
	}
	if envelope.Code != 0 {
		return "", &platformDeliveryError{Retryable: false, Reason: "kook_asset_upload_rejected", Cause: errors.New(envelope.Message)}
	}
	var result struct {
		URL string `json:"url"`
	}
	if err = json.Unmarshal(envelope.Data, &result); err != nil || result.URL == "" {
		return "", &platformDeliveryError{Retryable: true, Reason: "kook_asset_upload_failed", Cause: errors.New("KOOK asset URL is empty")}
	}
	return result.URL, nil
}

func kookDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	var httpErr *kookHTTPError
	if errors.As(err, &httpErr) {
		retryable = httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func (c *kookConnector) kookJSON(ctx context.Context, method, path string, query url.Values, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	endpoint := c.apiBase + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bot "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &kookHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))}
	}
	var envelope kookAPIResponse
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
		return err
	}
	if envelope.Code != 0 {
		return fmt.Errorf("KOOK API code %d: %s", envelope.Code, envelope.Message)
	}
	if output != nil {
		return json.Unmarshal(envelope.Data, output)
	}
	return nil
}
