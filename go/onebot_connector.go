package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const oneBotTransport = "aiocqhttp"

type oneBotSegment struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data"`
}

type oneBotSender struct {
	UserID   json.Number `json:"user_id"`
	Nickname string      `json:"nickname"`
	Card     string      `json:"card"`
}

type oneBotEvent struct {
	PostType    string          `json:"post_type"`
	MessageType string          `json:"message_type"`
	MessageID   json.Number     `json:"message_id"`
	UserID      json.Number     `json:"user_id"`
	GroupID     json.Number     `json:"group_id"`
	SelfID      json.Number     `json:"self_id"`
	Time        int64           `json:"time"`
	RawMessage  string          `json:"raw_message"`
	Message     json.RawMessage `json:"message"`
	Sender      oneBotSender    `json:"sender"`
}

type oneBotResponse struct {
	Status  string          `json:"status"`
	RetCode int             `json:"retcode"`
	Message string          `json:"message"`
	Wording string          `json:"wording"`
	Echo    json.RawMessage `json:"echo"`
}

type oneBotConnector struct {
	runtime   *AgentRuntime
	platform  mgmtPlatform
	host      string
	port      int
	token     string
	adminIDs  map[string]struct{}
	state     nativeConnectorState
	server    *http.Server
	listener  net.Listener
	upgrader  websocket.Upgrader
	cancel    context.CancelFunc
	connMu    sync.RWMutex
	conn      *websocket.Conn
	writeMu   sync.Mutex
	pendingMu sync.Mutex
	pending   map[string]chan oneBotResponse
	sequence  atomic.Uint64
}

func newOneBotConnector(runtime *AgentRuntime, platform mgmtPlatform) (*oneBotConnector, error) {
	host, _ := platform.Settings["ws_reverse_host"].(string)
	host = strings.TrimSpace(host)
	if host == "" {
		host = "0.0.0.0"
	}
	port := intSetting(platform.Settings, "ws_reverse_port", 6199)
	if port < 1 || port > 65535 {
		return nil, errors.New("OneBot reverse WebSocket port is invalid")
	}
	return &oneBotConnector{
		runtime: runtime, platform: platform, host: host, port: port,
		token:    resolvePlatformCredential(platform, "ws_reverse_token"),
		adminIDs: nativePlatformAdminIDs(platform), state: newNativeConnectorState(platform),
		upgrader: websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }},
		pending:  map[string]chan oneBotResponse{},
	}, nil
}

func (c *oneBotConnector) ID() string   { return c.platform.ID }
func (c *oneBotConnector) Type() string { return oneBotTransport }

func (c *oneBotConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	mux := http.NewServeMux()
	mux.HandleFunc("/", c.handleWebSocket)
	c.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	listener, err := net.Listen("tcp", net.JoinHostPort(c.host, strconv.Itoa(c.port)))
	if err != nil {
		cancel()
		c.state.setError(err)
		return err
	}
	c.listener = listener
	c.state.setStatus("listening")
	go func() {
		if serveErr := c.server.Serve(listener); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) && ctx.Err() == nil {
			c.state.setError(serveErr)
		}
	}()
	return nil
}

func (c *oneBotConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	c.connMu.Lock()
	if c.conn != nil {
		_ = c.conn.Close()
		c.conn = nil
	}
	c.connMu.Unlock()
	var err error
	if c.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = c.server.Shutdown(ctx)
		cancel()
	}
	c.state.setStatus("stopped")
	return err
}

func (c *oneBotConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *oneBotConnector) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	if !c.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		c.state.setError(err)
		return
	}
	c.connMu.Lock()
	previous := c.conn
	c.conn = conn
	c.connMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	c.state.setStatus("connected")
	defer func() {
		c.connMu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.state.setStatus("listening")
		}
		c.connMu.Unlock()
		_ = conn.Close()
	}()
	for {
		_, payload, readErr := conn.ReadMessage()
		if readErr != nil {
			return
		}
		if err = c.handlePayload(r.Context(), payload); err != nil {
			c.state.setError(err)
		}
	}
}

func (c *oneBotConnector) authorized(r *http.Request) bool {
	if c.token == "" {
		return true
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	value = strings.TrimSpace(strings.TrimPrefix(value, "Bearer"))
	if value == c.token {
		return true
	}
	return r.URL.Query().Get("access_token") == c.token
}

func (c *oneBotConnector) handlePayload(ctx context.Context, payload []byte) error {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if rawEcho := envelope["echo"]; len(rawEcho) > 0 && string(rawEcho) != "null" {
		var response oneBotResponse
		if err := json.Unmarshal(payload, &response); err != nil {
			return err
		}
		echo := strings.Trim(string(response.Echo), "\"")
		c.pendingMu.Lock()
		waiter := c.pending[echo]
		delete(c.pending, echo)
		c.pendingMu.Unlock()
		if waiter != nil {
			waiter <- response
		}
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	var event oneBotEvent
	if err := decoder.Decode(&event); err != nil {
		return err
	}
	if event.PostType != "message" || event.UserID.String() == event.SelfID.String() {
		return nil
	}
	return c.handleMessage(ctx, event)
}

func (c *oneBotConnector) handleMessage(ctx context.Context, event oneBotEvent) error {
	messageID, senderID := event.MessageID.String(), event.UserID.String()
	if messageID == "" || senderID == "" {
		return errors.New("OneBot message identity is incomplete")
	}
	text, attachments, mentionBot, atOthers, atAll := normalizeOneBotMessage(event.Message, event.RawMessage, event.SelfID.String())
	isGroup := event.MessageType == "group" && event.GroupID.String() != ""
	targetID, kind, conversationID, conversationKind := senderID, "private", senderID, "private"
	if isGroup {
		targetID, kind, conversationID, conversationKind = event.GroupID.String(), "group", event.GroupID.String(), "group"
	}
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	command := nativePlatformIsCommand(text)
	occurredAt := time.Now().UTC()
	if event.Time > 0 {
		occurredAt = time.Unix(event.Time, 0).UTC()
	}
	name := firstNonEmpty(event.Sender.Card, event.Sender.Nickname, senderID)
	if isGroup {
		isAdmin := nativePlatformIsAdmin(c.adminIDs, senderID)
		decision, moderationErr := c.runtime.evaluateGroupModeration(ctx, c.ID(), conversationID, senderID, name, text, isAdmin)
		if moderationErr != nil {
			return moderationErr
		}
		if decision.Matched {
			c.runtime.recordGroupModerationAudit(decision, "group_moderation_detected", c.ID(), conversationID, messageID, "")
			if decision.Mode == "enforce" {
				go func() {
					retractContext, cancel := context.WithTimeout(context.Background(), 20*time.Second)
					defer cancel()
					if err := c.call(retractContext, "delete_msg", map[string]any{"message_id": messageID}); err != nil {
						c.runtime.recordGroupModerationAudit(decision, "group_moderation_retract_failed", c.ID(), conversationID, messageID, "onebot_delete_failed")
						return
					}
					c.runtime.recordGroupModerationAudit(decision, "group_moderation_retracted", c.ID(), conversationID, messageID, "")
				}()
				c.state.markEvent()
				return nil
			}
		}
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: oneBotTransport, MessageID: messageID,
		RouteKind: kind, TargetID: targetID, ConversationID: conversationID, ConversationKind: conversationKind,
		SenderID: senderID, SenderName: name, Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, senderID),
		IsMentionBot: mentionBot, IsAtOthers: atOthers, IsCommand: command, IsAtAll: atAll,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func normalizeOneBotMessage(raw json.RawMessage, fallback, selfID string) (string, []transportAttachment, bool, bool, bool) {
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil {
		return strings.TrimSpace(plain), nil, false, false, false
	}
	var segments []oneBotSegment
	if err := json.Unmarshal(raw, &segments); err != nil {
		return strings.TrimSpace(fallback), nil, false, false, false
	}
	var text strings.Builder
	attachments := []transportAttachment{}
	mentionBot, atOthers, atAll := false, false, false
	for _, segment := range segments {
		switch segment.Type {
		case "text", "markdown":
			text.WriteString(oneBotString(segment.Data, "text", "content", "markdown"))
		case "at":
			target := oneBotString(segment.Data, "qq", "id")
			if target == "all" {
				atAll = true
			} else if target == selfID {
				mentionBot = true
			} else if target != "" {
				atOthers = true
				text.WriteString("@" + firstNonEmpty(oneBotString(segment.Data, "name"), target) + " ")
			}
		case "reply":
			if id := oneBotString(segment.Data, "id"); id != "" {
				text.WriteString("[引用消息 " + id + "] ")
			}
		case "image", "record", "video", "file":
			kind := map[string]string{"record": "audio"}[segment.Type]
			if kind == "" {
				kind = segment.Type
			}
			source := oneBotString(segment.Data, "url", "file")
			if source != "" && !strings.HasPrefix(source, "base64://") {
				attachments = append(attachments, transportAttachment{Kind: kind, SourceURL: source, Name: oneBotString(segment.Data, "name", "file_id")})
			}
		}
		if len(attachments) == 3 {
			break
		}
	}
	return strings.TrimSpace(text.String()), attachments, mentionBot, atOthers, atAll
}

func oneBotString(data map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, found := data[key]; found && value != nil {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

func (c *oneBotConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	segments := []oneBotSegment{}
	if text := strings.TrimSpace(delivery.Message.Text); text != "" {
		segments = append(segments, oneBotSegment{Type: "text", Data: map[string]any{"text": text}})
	}
	for _, attachment := range delivery.Message.Attachments {
		data, _, err := readNativeMedia(attachment)
		if err != nil {
			return &platformDeliveryError{Retryable: false, Reason: "onebot_attachment_invalid", Cause: err}
		}
		kind := strings.ToLower(attachment.Kind)
		segmentType := map[string]string{"audio": "record"}[kind]
		if segmentType == "" {
			segmentType = kind
		}
		if segmentType != "image" && segmentType != "record" && segmentType != "video" && segmentType != "file" {
			return &platformDeliveryError{Retryable: false, Reason: "onebot_attachment_kind_unsupported"}
		}
		segments = append(segments, oneBotSegment{Type: segmentType, Data: map[string]any{
			"file": "base64://" + base64.StdEncoding.EncodeToString(data), "name": attachment.Name,
		}})
	}
	if len(segments) == 0 {
		return nil
	}
	action, params := "send_private_msg", map[string]any{"user_id": route.TargetID, "message": segments}
	if route.Kind == "group" {
		action, params = "send_group_msg", map[string]any{"group_id": route.TargetID, "message": segments}
	}
	if route.Kind != "group" && route.Kind != "private" {
		return &platformDeliveryError{Retryable: false, Reason: "onebot_reply_kind_unsupported"}
	}
	if err := c.call(ctx, action, params); err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "onebot_send_failed", Cause: err}
	}
	c.state.markDelivery()
	return nil
}

func (c *oneBotConnector) call(ctx context.Context, action string, params map[string]any) error {
	c.connMu.RLock()
	conn := c.conn
	c.connMu.RUnlock()
	if conn == nil {
		return errors.New("OneBot reverse WebSocket is not connected")
	}
	echo := fmt.Sprintf("erdai-%d", c.sequence.Add(1))
	waiter := make(chan oneBotResponse, 1)
	c.pendingMu.Lock()
	c.pending[echo] = waiter
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, echo)
		c.pendingMu.Unlock()
	}()
	c.writeMu.Lock()
	err := conn.WriteJSON(map[string]any{"action": action, "params": params, "echo": echo})
	c.writeMu.Unlock()
	if err != nil {
		return err
	}
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	select {
	case response := <-waiter:
		if response.Status != "ok" || response.RetCode != 0 {
			return fmt.Errorf("OneBot action failed: retcode=%d %s", response.RetCode, firstNonEmpty(response.Wording, response.Message))
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("OneBot action timed out")
	}
}

func intSetting(settings map[string]any, key string, fallback int) int {
	value, found := settings[key]
	if !found {
		return fallback
	}
	switch number := value.(type) {
	case float64:
		return int(number)
	case int:
		return number
	case json.Number:
		parsed, _ := number.Int64()
		return int(parsed)
	case string:
		parsed, err := strconv.Atoi(strings.TrimSpace(number))
		if err == nil {
			return parsed
		}
	}
	return fallback
}
