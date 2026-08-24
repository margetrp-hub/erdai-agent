package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const satoriTransport = "satori"

type satoriConnector struct {
	runtime           *AgentRuntime
	platform          mgmtPlatform
	token             string
	apiBase           string
	endpoint          string
	autoReconnect     bool
	heartbeatInterval time.Duration
	reconnectDelay    time.Duration
	adminIDs          map[string]struct{}
	client            *http.Client
	dialer            *websocket.Dialer
	state             nativeConnectorState
	cancel            context.CancelFunc
	connMu            sync.RWMutex
	conn              *websocket.Conn
	writeMu           sync.Mutex
	sequence          atomic.Int64
	botID             string
	defaultPlatform   string
}

type satoriUser struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Nick string `json:"nick"`
}

type satoriLogin struct {
	Platform string     `json:"platform"`
	User     satoriUser `json:"user"`
}

type satoriHTTPError struct {
	Status int
	Body   string
}

func (e *satoriHTTPError) Error() string {
	return fmt.Sprintf("Satori status %d: %s", e.Status, e.Body)
}

func newSatoriConnector(runtime *AgentRuntime, platform mgmtPlatform) (*satoriConnector, error) {
	token := resolvePlatformCredential(platform, "satori_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "satori_token")
	}
	apiBase, _ := platform.Settings["satori_api_base_url"].(string)
	endpoint, _ := platform.Settings["satori_endpoint"].(string)
	apiBase, endpoint = strings.TrimRight(strings.TrimSpace(apiBase), "/"), strings.TrimSpace(endpoint)
	if apiBase == "" || endpoint == "" {
		return nil, errors.New("Satori API base URL and endpoint are required")
	}
	autoReconnect, found := platform.Settings["satori_auto_reconnect"].(bool)
	if !found {
		autoReconnect = true
	}
	dialer := *websocket.DefaultDialer
	dialer.ReadBufferSize = 64 << 10
	dialer.WriteBufferSize = 64 << 10
	return &satoriConnector{
		runtime: runtime, platform: platform, token: token, apiBase: apiBase, endpoint: endpoint,
		autoReconnect:     autoReconnect,
		heartbeatInterval: kookDurationSetting(platform, "satori_heartbeat_interval", 10*time.Second),
		reconnectDelay:    kookDurationSetting(platform, "satori_reconnect_delay", 5*time.Second),
		adminIDs:          nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		dialer: &dialer, state: newNativeConnectorState(platform),
	}, nil
}

func (c *satoriConnector) ID() string   { return c.platform.ID }
func (c *satoriConnector) Type() string { return satoriTransport }

func (c *satoriConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.state.setStatus("connecting")
	go c.run(ctx)
	return nil
}

func (c *satoriConnector) Close() error {
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

func (c *satoriConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *satoriConnector) run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
		if !c.autoReconnect || !waitContext(ctx, c.reconnectDelay) {
			return
		}
	}
}

func (c *satoriConnector) runSession(ctx context.Context) error {
	conn, _, err := c.dialer.DialContext(ctx, c.endpoint, nil)
	if err != nil {
		return err
	}
	conn.SetReadLimit(10 << 20)
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
	body := map[string]any{"token": c.token}
	if c.sequence.Load() > 0 {
		body["sn"] = c.sequence.Load()
	}
	if err = c.writeGateway(conn, map[string]any{"op": 3, "body": body}); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go c.heartbeat(heartbeatCtx, conn)
	for ctx.Err() == nil {
		var payload struct {
			Op   int             `json:"op"`
			Body json.RawMessage `json:"body"`
		}
		if err = conn.ReadJSON(&payload); err != nil {
			return err
		}
		switch payload.Op {
		case 0:
			if err = c.handleEvent(ctx, payload.Body); err != nil {
				return err
			}
		case 1:
			if err = c.writeGateway(conn, map[string]any{"op": 2, "body": map[string]any{}}); err != nil {
				return err
			}
		case 2:
		case 4:
			if err = c.handleReady(payload.Body); err != nil {
				return err
			}
		case 5:
			var meta struct {
				SN int64 `json:"sn"`
			}
			if json.Unmarshal(payload.Body, &meta) == nil && meta.SN > 0 {
				c.sequence.Store(meta.SN)
			}
		}
	}
	return ctx.Err()
}

func (c *satoriConnector) handleReady(raw json.RawMessage) error {
	var ready struct {
		Logins []satoriLogin `json:"logins"`
		SN     int64         `json:"sn"`
	}
	if err := json.Unmarshal(raw, &ready); err != nil {
		return err
	}
	if ready.SN > 0 {
		c.sequence.Store(ready.SN)
	}
	if len(ready.Logins) > 0 {
		c.botID = ready.Logins[0].User.ID
		c.defaultPlatform = ready.Logins[0].Platform
	}
	c.state.setStatus("connected")
	return nil
}

func (c *satoriConnector) heartbeat(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(c.heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if c.writeGateway(conn, map[string]any{"op": 1, "body": map[string]any{}}) != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *satoriConnector) writeGateway(conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *satoriConnector) handleEvent(ctx context.Context, raw json.RawMessage) error {
	var event struct {
		Type      string          `json:"type"`
		SN        int64           `json:"sn"`
		Timestamp int64           `json:"timestamp"`
		Message   json.RawMessage `json:"message"`
		User      satoriUser      `json:"user"`
		Channel   struct {
			ID   string `json:"id"`
			Type int    `json:"type"`
		} `json:"channel"`
		Guild *struct {
			ID string `json:"id"`
		} `json:"guild"`
		Login satoriLogin `json:"login"`
	}
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	if event.SN > 0 {
		c.sequence.Store(event.SN)
	}
	if event.Type != "message-created" || event.User.ID == "" || event.User.ID == event.Login.User.ID {
		return nil
	}
	var message struct {
		ID      string `json:"id"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(event.Message, &message); err != nil {
		return err
	}
	if message.ID == "" || event.Channel.ID == "" {
		return nil
	}
	text, attachments, mentionBot, atAll := parseSatoriContent(message.Content, event.Login.User.ID)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	isGroup := event.Guild != nil && event.Guild.ID != ""
	conversationKind := "private"
	guildID := ""
	if isGroup {
		conversationKind, guildID = "group", event.Guild.ID
	}
	command := nativePlatformIsCommand(text)
	occurredAt := time.Now().UTC()
	if event.Timestamp > 0 {
		if event.Timestamp > 10_000_000_000 {
			occurredAt = time.UnixMilli(event.Timestamp).UTC()
		} else {
			occurredAt = time.Unix(event.Timestamp, 0).UTC()
		}
	}
	platformName := firstNonEmpty(event.Login.Platform, c.defaultPlatform)
	botID := firstNonEmpty(event.Login.User.ID, c.botID)
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: satoriTransport, MessageID: message.ID,
		RouteKind: "channel", TargetID: event.Channel.ID, GuildID: platformName, ChannelID: botID,
		ConversationID: platformName + ":" + event.Channel.ID, ConversationKind: conversationKind,
		SenderID: event.User.ID, SenderName: firstNonEmpty(event.User.Nick, event.User.Name, event.User.ID),
		Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, event.User.ID),
		IsMentionBot: mentionBot, IsCommand: command, IsAtAll: atAll,
	}); err != nil {
		return err
	}
	_ = guildID
	c.state.markEvent()
	return nil
}

func parseSatoriContent(content, botID string) (string, []transportAttachment, bool, bool) {
	decoder := xml.NewDecoder(strings.NewReader("<root>" + content + "</root>"))
	text := strings.Builder{}
	attachments := []transportAttachment{}
	mentionBot, atAll := false, false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return strings.TrimSpace(stripSatoriTags(content)), nil, false, false
		}
		switch value := token.(type) {
		case xml.CharData:
			text.Write([]byte(value))
		case xml.StartElement:
			name := strings.ToLower(value.Name.Local)
			attributes := map[string]string{}
			for _, attribute := range value.Attr {
				attributes[strings.ToLower(attribute.Name.Local)] = attribute.Value
			}
			switch name {
			case "at":
				mentionBot = mentionBot || (botID != "" && attributes["id"] == botID)
				atAll = atAll || attributes["type"] == "all" || attributes["id"] == "all"
			case "img", "image", "audio", "video", "file":
				if source := attributes["src"]; source != "" && len(attachments) < 3 {
					kind := map[string]string{"img": "image", "image": "image", "audio": "audio", "video": "video", "file": "file"}[name]
					attachments = append(attachments, transportAttachment{Kind: kind, SourceURL: source, Name: attributes["name"]})
				}
			}
		}
	}
	return strings.TrimSpace(text.String()), attachments, mentionBot, atAll
}

func stripSatoriTags(content string) string {
	inside := false
	var text strings.Builder
	for _, value := range content {
		switch value {
		case '<':
			inside = true
		case '>':
			inside = false
		default:
			if !inside {
				text.WriteRune(value)
			}
		}
	}
	return text.String()
}

func (c *satoriConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.TargetID == "" {
		return &platformDeliveryError{Retryable: false, Reason: "satori_channel_missing"}
	}
	content := strings.Builder{}
	if route.MessageID != "" {
		content.WriteString(`<reply id="` + xmlEscape(route.MessageID) + `"/>`)
	}
	content.WriteString(xmlEscape(strings.TrimSpace(delivery.Message.Text)))
	for _, attachment := range delivery.Message.Attachments {
		data, cleanPath, err := readNativeMedia(attachment)
		if err != nil {
			return &platformDeliveryError{Retryable: false, Reason: "satori_attachment_invalid", Cause: err}
		}
		mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(cleanPath)))
		if mimeType == "" {
			mimeType = map[string]string{"image": "image/png", "video": "video/mp4", "audio": "audio/wav", "file": "application/octet-stream"}[strings.ToLower(attachment.Kind)]
		}
		tag := map[string]string{"image": "img", "video": "video", "audio": "audio", "file": "file"}[strings.ToLower(attachment.Kind)]
		if tag == "" {
			return &platformDeliveryError{Retryable: false, Reason: "satori_attachment_kind_unsupported"}
		}
		source := "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
		content.WriteString("<" + tag + ` src="` + source + `"`)
		if attachment.Name != "" {
			content.WriteString(` name="` + xmlEscape(attachment.Name) + `"`)
		}
		content.WriteString("/>")
	}
	payload := map[string]any{"channel_id": route.TargetID, "content": content.String()}
	if err := c.satoriJSON(ctx, "/message.create", route.GuildID, route.ChannelID, payload, nil); err != nil {
		return satoriDeliveryError("satori_message_create_failed", err)
	}
	c.state.markDelivery()
	return nil
}

func xmlEscape(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}

func satoriDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	var httpErr *satoriHTTPError
	if errors.As(err, &httpErr) {
		retryable = httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func (c *satoriConnector) satoriJSON(ctx context.Context, path, platformName, botID string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("satori-platform", firstNonEmpty(platformName, c.defaultPlatform))
	request.Header.Set("satori-user-id", firstNonEmpty(botID, c.botID))
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &satoriHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}
