package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const webchatTransport = "webchat"

type webchatConnector struct {
	runtime  *AgentRuntime
	platform mgmtPlatform
	token    string
	host     string
	port     int
	path     string
	admins   map[string]struct{}
	state    nativeConnectorState
	cancel   context.CancelFunc
	server   *platformWebhookServer
}

type webchatInboundRequest struct {
	MessageID   string                `json:"messageId"`
	SenderID    string                `json:"senderId"`
	SenderName  string                `json:"senderName"`
	Text        string                `json:"text"`
	Attachments []transportAttachment `json:"attachments"`
}

type webchatStoredMessage struct {
	Sequence    int64             `json:"sequence"`
	DeliveryID  string            `json:"deliveryId"`
	Text        string            `json:"text"`
	Attachments []agentAttachment `json:"attachments"`
	CreatedAt   string            `json:"createdAt"`
}

func newWebchatConnector(runtime *AgentRuntime, platform mgmtPlatform) (*webchatConnector, error) {
	host, _ := platform.Settings["webchat_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "127.0.0.1"
	}
	path, _ := platform.Settings["webchat_api_path"].(string)
	path = "/" + strings.Trim(strings.TrimSpace(path), "/")
	if path == "/" {
		path = "/api/v1/webchat"
	}
	token := resolvePlatformCredential(platform, "webchat_token")
	if token == "" && !isLoopbackBindHost(host) {
		return nil, errors.New("webchat_token is required when WebChat listens outside loopback")
	}
	if _, err := runtime.db.Exec(`
		CREATE TABLE IF NOT EXISTS webchat_outbound_messages (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			connector_id TEXT NOT NULL,
			conversation_id TEXT NOT NULL,
			delivery_id TEXT NOT NULL UNIQUE,
			message_cipher BLOB NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS webchat_outbound_conversation_idx
			ON webchat_outbound_messages(connector_id, conversation_id, sequence);
	`); err != nil {
		return nil, err
	}
	return &webchatConnector{
		runtime: runtime, platform: platform, token: token, host: host,
		port: kookIntSetting(platform, "webchat_port", 6200), path: path,
		admins: nativePlatformAdminIDs(platform), state: newNativeConnectorState(platform),
	}, nil
}

func (c *webchatConnector) ID() string   { return c.platform.ID }
func (c *webchatConnector) Type() string { return webchatTransport }

func (c *webchatConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	mux := http.NewServeMux()
	mux.HandleFunc(c.path+"/conversations/", c.handleConversation)
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

func (c *webchatConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *webchatConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *webchatConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.Kind != "private" || strings.TrimSpace(route.TargetID) == "" {
		return &platformDeliveryError{Retryable: false, Reason: "webchat_route_invalid"}
	}
	payload, err := json.Marshal(webchatStoredMessage{
		DeliveryID: delivery.ID, Text: delivery.Message.Text,
		Attachments: delivery.Message.Attachments,
		CreatedAt:   time.Now().UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	ciphertext, err := c.runtime.encrypt(payload)
	if err != nil {
		return err
	}
	_, err = c.runtime.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO webchat_outbound_messages (
			connector_id, conversation_id, delivery_id, message_cipher, created_at
		) VALUES (?, ?, ?, ?, ?)
	`, c.ID(), route.TargetID, delivery.ID, ciphertext, time.Now().UTC().Format(time.RFC3339Nano))
	if err == nil {
		c.state.markDelivery()
	}
	return err
}

func (c *webchatConnector) handleConversation(w http.ResponseWriter, r *http.Request) {
	if !c.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	suffix := strings.TrimPrefix(r.URL.Path, c.path+"/conversations/")
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) != 2 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	conversationID, err := url.PathUnescape(parts[0])
	if err != nil || len(conversationID) > 200 {
		http.Error(w, "invalid conversation", http.StatusBadRequest)
		return
	}
	switch parts[1] {
	case "messages":
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c.handleInbound(w, r, conversationID)
	case "events":
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c.handleEvents(w, r, conversationID)
	default:
		http.NotFound(w, r)
	}
}

func (c *webchatConnector) authorized(r *http.Request) bool {
	if c.token == "" {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		return net.ParseIP(host).IsLoopback()
	}
	provided := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	return len(provided) == len(c.token) && subtle.ConstantTimeCompare([]byte(provided), []byte(c.token)) == 1
}

func (c *webchatConnector) handleInbound(w http.ResponseWriter, r *http.Request, conversationID string) {
	var input webchatInboundRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	input.MessageID = strings.TrimSpace(input.MessageID)
	input.SenderID = strings.TrimSpace(input.SenderID)
	input.Text = strings.TrimSpace(input.Text)
	if input.MessageID == "" || input.SenderID == "" || len(input.Attachments) > 3 || (input.Text == "" && len(input.Attachments) == 0) {
		http.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	for index := range input.Attachments {
		item := &input.Attachments[index]
		item.Kind = strings.ToLower(strings.TrimSpace(item.Kind))
		item.SourceURL = strings.TrimSpace(item.SourceURL)
		if !webchatAttachmentValid(*item) {
			http.Error(w, "invalid attachment", http.StatusBadRequest)
			return
		}
	}
	if input.Text == "" {
		input.Text = nativeAttachmentOnlyPrompt(input.Attachments)
	}
	err := c.runtime.acceptNativePlatformInbound(r.Context(), nativePlatformInbound{
		ConnectorID: c.ID(), Transport: webchatTransport, MessageID: input.MessageID,
		RouteKind: "private", TargetID: conversationID, ConversationID: conversationID,
		ConversationKind: "private", SenderID: input.SenderID, SenderName: input.SenderName,
		Text: input.Text, Attachments: input.Attachments, IsWake: true,
		IsAdmin:      nativePlatformIsAdmin(c.admins, input.SenderID),
		IsMentionBot: true, IsCommand: nativePlatformIsCommand(input.Text),
	})
	if err != nil {
		http.Error(w, "message rejected", http.StatusInternalServerError)
		return
	}
	c.state.markEvent()
	writeQQWebhookJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "messageId": input.MessageID})
}

func (c *webchatConnector) handleEvents(w http.ResponseWriter, r *http.Request, conversationID string) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	if after < 0 {
		after = 0
	}
	rows, err := c.runtime.db.QueryContext(r.Context(), `
		SELECT sequence, delivery_id, message_cipher, created_at
		FROM webchat_outbound_messages
		WHERE connector_id = ? AND conversation_id = ? AND sequence > ?
		ORDER BY sequence LIMIT 100
	`, c.ID(), conversationID, after)
	if err != nil {
		http.Error(w, "events unavailable", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	items := []webchatStoredMessage{}
	for rows.Next() {
		var item webchatStoredMessage
		var sequence int64
		var cipher []byte
		if err = rows.Scan(&sequence, &item.DeliveryID, &cipher, &item.CreatedAt); err != nil {
			break
		}
		plaintext, decryptErr := c.runtime.decrypt(cipher)
		if decryptErr != nil || json.Unmarshal(plaintext, &item) != nil {
			err = errors.New("stored WebChat message is invalid")
			break
		}
		item.Sequence = sequence
		items = append(items, item)
	}
	if err != nil || rows.Err() != nil {
		http.Error(w, "events unavailable", http.StatusInternalServerError)
		return
	}
	writeQQWebhookJSON(w, http.StatusOK, map[string]any{"items": items})
}

func webchatAttachmentValid(item transportAttachment) bool {
	if item.Kind != "image" && item.Kind != "audio" && item.Kind != "video" && item.Kind != "file" {
		return false
	}
	parsed, err := url.ParseRequestURI(item.SourceURL)
	return err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" && parsed.User == nil
}

func isLoopbackBindHost(host string) bool {
	host = strings.Trim(strings.TrimSpace(host), "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
