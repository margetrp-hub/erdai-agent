package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

const slackTransport = "slack"

type slackConnector struct {
	runtime       *AgentRuntime
	platform      mgmtPlatform
	botToken      string
	appToken      string
	signingSecret string
	webhookUUID   string
	mode          string
	apiBase       string
	publicBase    string
	host          string
	port          int
	path          string
	adminIDs      map[string]struct{}
	client        *http.Client
	dialer        *websocket.Dialer
	state         nativeConnectorState
	cancel        context.CancelFunc
	server        *platformWebhookServer
	connMu        sync.RWMutex
	conn          *websocket.Conn
	writeMu       sync.Mutex
	botID         string
	mediaMu       sync.RWMutex
	media         map[string]slackMediaRecord
}

type slackMediaRecord struct {
	URL       string
	ExpiresAt time.Time
}

type slackEventEnvelope struct {
	Type      string          `json:"type"`
	EventID   string          `json:"event_id"`
	Challenge string          `json:"challenge"`
	Event     slackEvent      `json:"event"`
	Payload   json.RawMessage `json:"payload"`
	Envelope  string          `json:"envelope_id"`
}

type slackEvent struct {
	Type        string      `json:"type"`
	SubType     string      `json:"subtype"`
	BotID       string      `json:"bot_id"`
	User        string      `json:"user"`
	Channel     string      `json:"channel"`
	ChannelType string      `json:"channel_type"`
	Text        string      `json:"text"`
	Timestamp   string      `json:"ts"`
	ThreadTS    string      `json:"thread_ts"`
	ClientMsgID string      `json:"client_msg_id"`
	Files       []slackFile `json:"files"`
}

type slackFile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	MimeType    string `json:"mimetype"`
	URLPrivate  string `json:"url_private"`
	DownloadURL string `json:"url_private_download"`
}

type slackAPIError struct{ Message string }

func (e *slackAPIError) Error() string { return "Slack API: " + e.Message }

func newSlackConnector(runtime *AgentRuntime, platform mgmtPlatform) (*slackConnector, error) {
	botToken := resolvePlatformCredential(platform, "bot_token")
	if botToken == "" {
		return nil, platformConnectorStartupError(platform, "bot_token")
	}
	mode, _ := platform.Settings["slack_connection_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "socket"
	}
	appToken := resolvePlatformCredential(platform, "app_token")
	signingSecret := resolvePlatformCredential(platform, "signing_secret")
	if mode == "socket" && appToken == "" {
		return nil, platformConnectorStartupError(platform, "app_token")
	}
	if mode == "webhook" && signingSecret == "" {
		return nil, platformConnectorStartupError(platform, "signing_secret")
	}
	if mode != "socket" && mode != "webhook" {
		return nil, errors.New("Slack connection mode must be socket or webhook")
	}
	apiBase, _ := platform.Settings["slack_api_base_url"].(string)
	if strings.TrimSpace(apiBase) == "" {
		apiBase = "https://slack.com/api"
	}
	publicBase, _ := platform.Settings["slack_public_base_url"].(string)
	host, _ := platform.Settings["slack_webhook_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	path, _ := platform.Settings["slack_webhook_path"].(string)
	if strings.TrimSpace(path) == "" {
		path = "/erdai-slack-webhook/callback"
	}
	return &slackConnector{
		runtime: runtime, platform: platform, botToken: botToken, appToken: appToken, signingSecret: signingSecret,
		webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"), mode: mode,
		apiBase: strings.TrimRight(apiBase, "/"), publicBase: strings.TrimRight(publicBase, "/"),
		host: host, port: kookIntSetting(platform, "slack_webhook_port", 6197), path: path,
		adminIDs: nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		dialer: websocket.DefaultDialer, state: newNativeConnectorState(platform), media: map[string]slackMediaRecord{},
	}, nil
}

func (c *slackConnector) ID() string   { return c.platform.ID }
func (c *slackConnector) Type() string { return slackTransport }

func (c *slackConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if err := c.auth(ctx); err != nil {
		cancel()
		c.state.setError(err)
		return err
	}
	if c.mode == "webhook" {
		mux := http.NewServeMux()
		mux.HandleFunc(c.webhookPath(), c.handleWebhook)
		mux.HandleFunc("/slack-media/", c.handleMedia)
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
	if c.publicBase != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/slack-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.host, c.port, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	c.state.setStatus("connecting")
	go c.runSocket(ctx)
	return nil
}

func (c *slackConnector) Close() error {
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

func (c *slackConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *slackConnector) webhookPath() string {
	if c.webhookUUID != "" {
		return "/webhooks/" + url.PathEscape(c.webhookUUID)
	}
	if strings.HasPrefix(c.path, "/") {
		return c.path
	}
	return "/" + c.path
}

func (c *slackConnector) auth(ctx context.Context) error {
	var response struct {
		OK     bool   `json:"ok"`
		UserID string `json:"user_id"`
		Error  string `json:"error"`
	}
	if err := c.apiForm(ctx, c.botToken, "/auth.test", nil, &response); err != nil {
		return err
	}
	if !response.OK || response.UserID == "" {
		return &slackAPIError{Message: firstNonEmpty(response.Error, "auth.test failed")}
	}
	c.botID = response.UserID
	return nil
}

func (c *slackConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	timestamp := r.Header.Get("X-Slack-Request-Timestamp")
	if !c.verifyWebhook(timestamp, r.Header.Get("X-Slack-Signature"), body) {
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var envelope slackEventEnvelope
	if json.Unmarshal(body, &envelope) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if envelope.Type == "url_verification" {
		writeQQWebhookJSON(w, http.StatusOK, map[string]any{"challenge": envelope.Challenge})
		return
	}
	if envelope.Type == "event_callback" {
		if err = c.handleEvent(r.Context(), envelope.EventID, envelope.Event); err != nil {
			http.Error(w, "event rejected", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (c *slackConnector) verifyWebhook(timestamp, signature string, body []byte) bool {
	parsed, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || time.Since(time.Unix(parsed, 0)) > 5*time.Minute || time.Until(time.Unix(parsed, 0)) > 5*time.Minute {
		return false
	}
	mac := hmac.New(sha256.New, []byte(c.signingSecret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":"))
	_, _ = mac.Write(body)
	expected := "v0=" + hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(expected), []byte(signature))
}

func (c *slackConnector) runSocket(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		endpoint, err := c.openSocket(ctx)
		if err == nil {
			err = c.readSocket(ctx, endpoint)
		}
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
	}
}

func (c *slackConnector) openSocket(ctx context.Context) (string, error) {
	var response struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := c.apiForm(ctx, c.appToken, "/apps.connections.open", nil, &response); err != nil {
		return "", err
	}
	if !response.OK || response.URL == "" {
		return "", &slackAPIError{Message: firstNonEmpty(response.Error, "socket URL missing")}
	}
	return response.URL, nil
}

func (c *slackConnector) readSocket(ctx context.Context, endpoint string) error {
	conn, response, err := c.dialer.DialContext(ctx, endpoint, nil)
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
	c.state.setStatus("connected")
	for ctx.Err() == nil {
		var envelope slackEventEnvelope
		if err = conn.ReadJSON(&envelope); err != nil {
			return err
		}
		if envelope.Envelope != "" {
			c.writeMu.Lock()
			err = conn.WriteJSON(map[string]any{"envelope_id": envelope.Envelope})
			c.writeMu.Unlock()
			if err != nil {
				return err
			}
		}
		if envelope.Type != "events_api" || len(envelope.Payload) == 0 {
			continue
		}
		var payload slackEventEnvelope
		if json.Unmarshal(envelope.Payload, &payload) == nil {
			if err = c.handleEvent(ctx, payload.EventID, payload.Event); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func (c *slackConnector) handleEvent(ctx context.Context, eventID string, event slackEvent) error {
	if (event.Type != "message" && event.Type != "app_mention") || event.BotID != "" ||
		event.SubType == "bot_message" || event.SubType == "message_changed" || event.SubType == "message_deleted" || event.User == c.botID {
		return nil
	}
	messageID := firstNonEmpty(event.ClientMsgID, event.Timestamp, eventID)
	if messageID == "" || event.Channel == "" || event.User == "" {
		return nil
	}
	isPrivate := event.ChannelType == "im" || event.ChannelType == "mpim"
	text := strings.TrimSpace(event.Text)
	mentionBot := strings.Contains(text, "<@"+c.botID+">") || event.Type == "app_mention"
	text = strings.TrimSpace(strings.ReplaceAll(text, "<@"+c.botID+">", ""))
	attachments := c.parseFiles(event.Files)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	conversationKind := "group"
	if isPrivate {
		conversationKind = "private"
	}
	occurredAt := time.Now().UTC()
	if parsed, err := strconv.ParseFloat(event.Timestamp, 64); err == nil {
		occurredAt = time.Unix(int64(parsed), int64((parsed-float64(int64(parsed)))*1e9)).UTC()
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: slackTransport, MessageID: messageID,
		RouteKind: "channel", TargetID: event.Channel, ChannelID: event.ThreadTS,
		ConversationID: "slack:" + event.Channel, ConversationKind: conversationKind,
		SenderID: event.User, SenderName: event.User, Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: isPrivate || mentionBot || nativePlatformIsCommand(text), IsAdmin: nativePlatformIsAdmin(c.adminIDs, event.User),
		IsMentionBot: mentionBot, IsCommand: nativePlatformIsCommand(text),
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *slackConnector) parseFiles(files []slackFile) []transportAttachment {
	attachments := make([]transportAttachment, 0, min(len(files), 3))
	for _, file := range files {
		source := firstNonEmpty(file.DownloadURL, file.URLPrivate)
		if source == "" || c.publicBase == "" {
			continue
		}
		token, err := newCoreUUID()
		if err != nil {
			token = c.runtime.platformPseudonym("slack_media", file.ID+time.Now().String())
		}
		c.mediaMu.Lock()
		c.media[token] = slackMediaRecord{URL: source, ExpiresAt: time.Now().Add(time.Hour)}
		c.mediaMu.Unlock()
		attachments = append(attachments, transportAttachment{
			Kind: discordAttachmentKind(file.MimeType, file.Name), SourceURL: c.publicBase + "/slack-media/" + token, Name: file.Name,
		})
	}
	return attachments
}

func (c *slackConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimPrefix(r.URL.Path, "/slack-media/")
	c.mediaMu.RLock()
	record, found := c.media[token]
	c.mediaMu.RUnlock()
	if !found || record.ExpiresAt.Before(time.Now()) {
		http.NotFound(w, r)
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, record.URL, nil)
	request.Header.Set("Authorization", "Bearer "+c.botToken)
	response, err := c.client.Do(request)
	if err != nil || response.StatusCode < 200 || response.StatusCode >= 300 {
		if response != nil {
			response.Body.Close()
		}
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=300")
	_, _ = io.Copy(w, io.LimitReader(response.Body, 20<<20))
}

func (c *slackConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 3900) {
		if err := c.postMessage(ctx, route, text); err != nil {
			return slackDeliveryError("slack_text_send_failed", err)
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		if err := c.uploadFile(ctx, route, attachment); err != nil {
			return slackDeliveryError("slack_file_upload_failed", err)
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *slackConnector) postMessage(ctx context.Context, route platformReplyRoute, text string) error {
	payload := map[string]any{"channel": route.TargetID, "text": text}
	if route.ChannelID != "" {
		payload["thread_ts"] = route.ChannelID
	}
	return c.apiJSON(ctx, c.botToken, "/chat.postMessage", payload, nil)
}

func (c *slackConnector) uploadFile(ctx context.Context, route platformReplyRoute, attachment agentAttachment) error {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return err
	}
	var slot struct {
		OK        bool   `json:"ok"`
		UploadURL string `json:"upload_url"`
		FileID    string `json:"file_id"`
		Error     string `json:"error"`
	}
	form := url.Values{"filename": {firstNonEmpty(attachment.Name, filepathBase(cleanPath))}, "length": {strconv.Itoa(len(data))}}
	if err = c.apiForm(ctx, c.botToken, "/files.getUploadURLExternal", form, &slot); err != nil {
		return err
	}
	if !slot.OK || slot.UploadURL == "" || slot.FileID == "" {
		return &slackAPIError{Message: firstNonEmpty(slot.Error, "upload slot missing")}
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, slot.UploadURL, bytes.NewReader(data))
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("Slack upload status %d", response.StatusCode)
	}
	payload := map[string]any{
		"files": []map[string]any{{"id": slot.FileID, "title": attachment.Name}}, "channel_id": route.TargetID,
	}
	if route.ChannelID != "" {
		payload["thread_ts"] = route.ChannelID
	}
	return c.apiJSON(ctx, c.botToken, "/files.completeUploadExternal", payload, nil)
}

func (c *slackConnector) apiForm(ctx context.Context, token, endpoint string, form url.Values, output any) error {
	if form == nil {
		form = url.Values{}
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+endpoint, strings.NewReader(form.Encode()))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.doAPI(request, output)
}

func (c *slackConnector) apiJSON(ctx context.Context, token, endpoint string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+endpoint, bytes.NewReader(encoded))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	return c.doAPI(request, output)
}

func (c *slackConnector) doAPI(request *http.Request, output any) error {
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	var status struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &status) != nil {
		return errors.New("Slack API returned invalid JSON")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !status.OK {
		return &slackAPIError{Message: firstNonEmpty(status.Error, response.Status)}
	}
	if output != nil {
		return json.Unmarshal(body, output)
	}
	return nil
}

func slackDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	if apiErr, ok := err.(*slackAPIError); ok {
		retryable = apiErr.Message == "ratelimited" || apiErr.Message == "internal_error" || apiErr.Message == "fatal_error"
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func filepathBase(value string) string {
	value = strings.ReplaceAll(value, "\\", "/")
	if index := strings.LastIndex(value, "/"); index >= 0 {
		return value[index+1:]
	}
	return value
}
