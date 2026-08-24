package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

const discordTransport = "discord"

type discordConnector struct {
	runtime      *AgentRuntime
	platform     mgmtPlatform
	token        string
	apiBase      string
	gatewayURL   string
	activityName string
	allowBots    bool
	adminIDs     map[string]struct{}
	client       *http.Client
	dialer       *websocket.Dialer
	state        nativeConnectorState
	cancel       context.CancelFunc
	connMu       sync.RWMutex
	conn         *websocket.Conn
	writeMu      sync.Mutex
	botID        string
	botUsername  string
	sequence     atomic.Int64
}

type discordGatewayEnvelope struct {
	Op int             `json:"op"`
	D  json.RawMessage `json:"d"`
	S  *int64          `json:"s"`
	T  string          `json:"t"`
}

type discordUser struct {
	ID         string `json:"id"`
	Username   string `json:"username"`
	GlobalName string `json:"global_name"`
	Bot        bool   `json:"bot"`
}

type discordAttachment struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	URL         string `json:"url"`
}

type discordMessage struct {
	ID                string              `json:"id"`
	ChannelID         string              `json:"channel_id"`
	GuildID           string              `json:"guild_id"`
	Content           string              `json:"content"`
	Timestamp         string              `json:"timestamp"`
	Author            discordUser         `json:"author"`
	Mentions          []discordUser       `json:"mentions"`
	MentionEveryone   bool                `json:"mention_everyone"`
	Attachments       []discordAttachment `json:"attachments"`
	ReferencedMessage *discordMessage     `json:"referenced_message"`
}

func newDiscordConnector(runtime *AgentRuntime, platform mgmtPlatform) (*discordConnector, error) {
	token := resolvePlatformCredential(platform, "discord_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "discord_token")
	}
	apiBase, _ := platform.Settings["discord_api_base_url"].(string)
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	if apiBase == "" {
		apiBase = "https://discord.com/api/v10"
	}
	gatewayURL, _ := platform.Settings["discord_gateway_url"].(string)
	activityName, _ := platform.Settings["discord_activity_name"].(string)
	allowBots, _ := platform.Settings["discord_allow_bot_messages"].(bool)
	transport := &http.Transport{}
	dialer := *websocket.DefaultDialer
	if proxyRaw, _ := platform.Settings["discord_proxy"].(string); strings.TrimSpace(proxyRaw) != "" {
		proxyURL, err := url.Parse(strings.TrimSpace(proxyRaw))
		if err != nil {
			return nil, fmt.Errorf("Discord proxy is invalid: %w", err)
		}
		transport.Proxy = http.ProxyURL(proxyURL)
		dialer.Proxy = http.ProxyURL(proxyURL)
	}
	return &discordConnector{
		runtime: runtime, platform: platform, token: token, apiBase: apiBase,
		gatewayURL: strings.TrimSpace(gatewayURL), activityName: strings.TrimSpace(activityName),
		allowBots: allowBots, adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Transport: transport, Timeout: 30 * time.Second}, dialer: &dialer,
		state: newNativeConnectorState(platform),
	}, nil
}

func (c *discordConnector) ID() string   { return c.platform.ID }
func (c *discordConnector) Type() string { return discordTransport }

func (c *discordConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.state.setStatus("connecting")
	go c.run(ctx)
	return nil
}

func (c *discordConnector) Close() error {
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

func (c *discordConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *discordConnector) run(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
		if !waitContext(ctx, delay) {
			return
		}
		if delay < 30*time.Second {
			delay *= 2
		}
	}
}

func (c *discordConnector) runSession(ctx context.Context) error {
	gatewayURL := c.gatewayURL
	if gatewayURL == "" {
		var response struct {
			URL string `json:"url"`
		}
		if err := c.discordJSON(ctx, http.MethodGet, "/gateway/bot", nil, &response); err != nil {
			return err
		}
		gatewayURL = response.URL
	}
	if gatewayURL == "" {
		return errors.New("Discord gateway URL is empty")
	}
	separator := "?"
	if strings.Contains(gatewayURL, "?") {
		separator = "&"
	}
	conn, _, err := c.dialer.DialContext(ctx, gatewayURL+separator+"v=10&encoding=json", nil)
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
	var hello discordGatewayEnvelope
	if err = conn.ReadJSON(&hello); err != nil {
		return err
	}
	if hello.Op != 10 {
		return fmt.Errorf("Discord expected Hello, received op %d", hello.Op)
	}
	var helloData struct {
		HeartbeatInterval float64 `json:"heartbeat_interval"`
	}
	if err = json.Unmarshal(hello.D, &helloData); err != nil || helloData.HeartbeatInterval < 1000 {
		return errors.New("Discord Hello heartbeat interval is invalid")
	}
	identify := map[string]any{
		"op": 2,
		"d": map[string]any{
			"token": c.token, "intents": 37377,
			"properties": map[string]string{"os": "linux", "browser": "erdai-agent", "device": "erdai-agent"},
		},
	}
	if c.activityName != "" {
		identify["d"].(map[string]any)["presence"] = map[string]any{
			"since": nil, "afk": false, "status": "online",
			"activities": []map[string]any{{"name": c.activityName, "type": 0}},
		}
	}
	if err = c.writeGateway(conn, identify); err != nil {
		return err
	}
	heartbeatCtx, stopHeartbeat := context.WithCancel(ctx)
	defer stopHeartbeat()
	go c.heartbeat(heartbeatCtx, conn, time.Duration(helloData.HeartbeatInterval)*time.Millisecond)
	for ctx.Err() == nil {
		var envelope discordGatewayEnvelope
		if err = conn.ReadJSON(&envelope); err != nil {
			return err
		}
		if envelope.S != nil {
			c.sequence.Store(*envelope.S)
		}
		switch envelope.Op {
		case 0:
			if err = c.handleDispatch(ctx, envelope.T, envelope.D); err != nil {
				return err
			}
		case 1:
			if err = c.writeGateway(conn, map[string]any{"op": 1, "d": c.sequence.Load()}); err != nil {
				return err
			}
		case 7, 9:
			return fmt.Errorf("Discord gateway requested reconnect: op %d", envelope.Op)
		}
	}
	return ctx.Err()
}

func (c *discordConnector) heartbeat(ctx context.Context, conn *websocket.Conn, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.writeGateway(conn, map[string]any{"op": 1, "d": c.sequence.Load()}); err != nil {
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *discordConnector) writeGateway(conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *discordConnector) handleDispatch(ctx context.Context, eventType string, raw json.RawMessage) error {
	switch eventType {
	case "READY":
		var ready struct {
			User discordUser `json:"user"`
		}
		if err := json.Unmarshal(raw, &ready); err != nil {
			return err
		}
		c.botID, c.botUsername = ready.User.ID, ready.User.Username
		c.state.setStatus("connected")
		return nil
	case "MESSAGE_CREATE":
		var message discordMessage
		if err := json.Unmarshal(raw, &message); err != nil {
			return err
		}
		return c.handleMessage(ctx, message)
	default:
		return nil
	}
}

func (c *discordConnector) handleMessage(ctx context.Context, message discordMessage) error {
	if message.ID == "" || message.ChannelID == "" || message.Author.ID == "" || message.Author.ID == c.botID {
		return nil
	}
	if message.Author.Bot && !c.allowBots {
		return nil
	}
	mentionBot := false
	for _, mention := range message.Mentions {
		if mention.ID == c.botID && c.botID != "" {
			mentionBot = true
			break
		}
	}
	text := strings.TrimSpace(message.Content)
	if c.botID != "" {
		text = strings.ReplaceAll(text, "<@"+c.botID+">", "")
		text = strings.ReplaceAll(text, "<@!"+c.botID+">", "")
		text = strings.TrimSpace(text)
	}
	if message.ReferencedMessage != nil {
		quoted := strings.TrimSpace(message.ReferencedMessage.Content)
		if quoted != "" {
			text = "[引用消息]\n" + quoted + "\n[当前消息]\n" + text
		}
	}
	attachments := make([]transportAttachment, 0, min(len(message.Attachments), 3))
	for _, value := range message.Attachments {
		kind := discordAttachmentKind(value.ContentType, value.Filename)
		attachments = append(attachments, transportAttachment{Kind: kind, SourceURL: value.URL, Name: value.Filename})
		if len(attachments) == 3 {
			break
		}
	}
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	isGroup := message.GuildID != ""
	conversationKind := "private"
	if isGroup {
		conversationKind = "group"
	}
	command := nativePlatformIsCommand(text)
	occurredAt := time.Now().UTC()
	if parsed, err := time.Parse(time.RFC3339Nano, message.Timestamp); err == nil {
		occurredAt = parsed.UTC()
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: discordTransport, MessageID: message.ID,
		RouteKind: "channel", TargetID: message.ChannelID, GuildID: message.GuildID, ChannelID: message.ChannelID,
		ConversationID: message.ChannelID, ConversationKind: conversationKind,
		SenderID: message.Author.ID, SenderName: firstNonEmpty(message.Author.GlobalName, message.Author.Username),
		Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, message.Author.ID),
		IsMentionBot: mentionBot, IsCommand: command, IsAtAll: message.MentionEveryone,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func discordAttachmentKind(contentType, name string) string {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "image/"):
		return "image"
	case strings.HasPrefix(contentType, "video/"):
		return "video"
	case strings.HasPrefix(contentType, "audio/"):
		return "audio"
	default:
		return qqAttachmentKindFromName(name)
	}
}

func (c *discordConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.ChannelID == "" && route.TargetID == "" {
		return &platformDeliveryError{Retryable: false, Reason: "discord_channel_missing"}
	}
	channelID := firstNonEmpty(route.ChannelID, route.TargetID)
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 2000) {
		payload := map[string]any{"content": text, "allowed_mentions": map[string]any{"parse": []string{}}}
		if route.MessageID != "" {
			payload["message_reference"] = map[string]string{"message_id": route.MessageID, "channel_id": channelID}
		}
		if err := c.discordJSON(ctx, http.MethodPost, "/channels/"+url.PathEscape(channelID)+"/messages", payload, nil); err != nil {
			return &platformDeliveryError{Retryable: true, Reason: "discord_text_send_failed", Cause: err}
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		if err := c.sendAttachment(ctx, channelID, route.MessageID, attachment); err != nil {
			return err
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *discordConnector) sendAttachment(ctx context.Context, channelID, replyID string, attachment agentAttachment) error {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "discord_attachment_invalid", Cause: err}
	}
	payload := map[string]any{"attachments": []map[string]any{{"id": 0, "filename": firstNonEmpty(attachment.Name, filepath.Base(cleanPath))}}}
	if replyID != "" {
		payload["message_reference"] = map[string]string{"message_id": replyID, "channel_id": channelID}
	}
	payloadJSON, _ := json.Marshal(payload)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("payload_json", string(payloadJSON))
	part, err := writer.CreateFormFile("files[0]", firstNonEmpty(attachment.Name, filepath.Base(cleanPath)))
	if err == nil {
		_, err = part.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "discord_attachment_encode_failed", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+"/channels/"+url.PathEscape(channelID)+"/messages", &body)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bot "+c.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "discord_attachment_send_failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &platformDeliveryError{Retryable: response.StatusCode == 429 || response.StatusCode >= 500, Reason: "discord_attachment_send_failed", Cause: fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))}
	}
	return nil
}

func (c *discordConnector) discordJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, body)
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
		return fmt.Errorf("Discord %s %s status %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(payload)))
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}
