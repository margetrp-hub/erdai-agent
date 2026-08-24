package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const lineTransport = "line"

type lineConnector struct {
	runtime         *AgentRuntime
	platform        mgmtPlatform
	accessToken     string
	channelSecret   string
	webhookUUID     string
	host            string
	port            int
	apiBase         string
	dataBase        string
	publicBase      string
	videoPreviewURL string
	adminIDs        map[string]struct{}
	client          *http.Client
	state           nativeConnectorState
	cancel          context.CancelFunc
	server          *platformWebhookServer
	mediaMu         sync.RWMutex
	media           map[string]lineMediaRecord
}

type lineMediaRecord struct {
	LocalPath string
	MessageID string
	MimeType  string
	ExpiresAt time.Time
}

type lineWebhookPayload struct {
	Destination string      `json:"destination"`
	Events      []lineEvent `json:"events"`
}

type lineEvent struct {
	Type           string `json:"type"`
	Mode           string `json:"mode"`
	WebhookEventID string `json:"webhookEventId"`
	Timestamp      int64  `json:"timestamp"`
	ReplyToken     string `json:"replyToken"`
	Source         struct {
		Type    string `json:"type"`
		UserID  string `json:"userId"`
		GroupID string `json:"groupId"`
		RoomID  string `json:"roomId"`
	} `json:"source"`
	Message struct {
		ID              string `json:"id"`
		Type            string `json:"type"`
		Text            string `json:"text"`
		FileName        string `json:"fileName"`
		ContentProvider *struct {
			Type               string `json:"type"`
			OriginalContentURL string `json:"originalContentUrl"`
		} `json:"contentProvider"`
		Mention *struct {
			Mentionees []struct {
				Index  int    `json:"index"`
				Length int    `json:"length"`
				Type   string `json:"type"`
				UserID string `json:"userId"`
				IsSelf bool   `json:"isSelf"`
			} `json:"mentionees"`
		} `json:"mention"`
	} `json:"message"`
}

type lineHTTPError struct {
	Status int
	Body   string
}

func (e *lineHTTPError) Error() string {
	return fmt.Sprintf("LINE status %d: %s", e.Status, e.Body)
}

func newLineConnector(runtime *AgentRuntime, platform mgmtPlatform) (*lineConnector, error) {
	accessToken := resolvePlatformCredential(platform, "channel_access_token")
	channelSecret := resolvePlatformCredential(platform, "channel_secret")
	if accessToken == "" || channelSecret == "" {
		return nil, errors.New("LINE channel_access_token and channel_secret are required")
	}
	apiBase, _ := platform.Settings["line_api_base_url"].(string)
	dataBase, _ := platform.Settings["line_data_base_url"].(string)
	publicBase, _ := platform.Settings["line_public_base_url"].(string)
	previewURL, _ := platform.Settings["line_video_preview_url"].(string)
	host, _ := platform.Settings["callback_server_host"].(string)
	if host == "" {
		host = "0.0.0.0"
	}
	return &lineConnector{
		runtime: runtime, platform: platform, accessToken: accessToken, channelSecret: channelSecret,
		webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"), host: host,
		port: kookIntSetting(platform, "port", 6193), apiBase: strings.TrimRight(apiBase, "/"),
		dataBase: strings.TrimRight(dataBase, "/"), publicBase: strings.TrimRight(publicBase, "/"),
		videoPreviewURL: strings.TrimSpace(previewURL), adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Timeout: 30 * time.Second}, state: newNativeConnectorState(platform), media: map[string]lineMediaRecord{},
	}, nil
}

func (c *lineConnector) ID() string   { return c.platform.ID }
func (c *lineConnector) Type() string { return lineTransport }

func (c *lineConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	mux := http.NewServeMux()
	mux.HandleFunc(c.webhookPath(), c.handleWebhook)
	mux.HandleFunc("/media/", c.handleMedia)
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

func (c *lineConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *lineConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *lineConnector) webhookPath() string {
	if c.webhookUUID == "" {
		return "/webhooks/line"
	}
	return "/webhooks/" + url.PathEscape(c.webhookUUID)
}

func (c *lineConnector) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	expected := hmac.New(sha256.New, []byte(c.channelSecret))
	_, _ = expected.Write(body)
	provided, err := base64.StdEncoding.DecodeString(strings.TrimSpace(r.Header.Get("X-Line-Signature")))
	if err != nil || !hmac.Equal(expected.Sum(nil), provided) {
		http.Error(w, "invalid signature", http.StatusBadRequest)
		return
	}
	var payload lineWebhookPayload
	if json.Unmarshal(body, &payload) != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	for _, event := range payload.Events {
		if err = c.handleEvent(r.Context(), payload.Destination, event); err != nil {
			http.Error(w, "event rejected", http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (c *lineConnector) handleEvent(ctx context.Context, destination string, event lineEvent) error {
	if event.Type != "message" || event.Mode == "standby" {
		return nil
	}
	messageID := firstNonEmpty(event.Message.ID, event.WebhookEventID)
	senderID := firstNonEmpty(event.Source.UserID, event.Source.GroupID, event.Source.RoomID)
	if messageID == "" || senderID == "" {
		return nil
	}
	isGroup := event.Source.Type == "group" || event.Source.Type == "room"
	targetID := event.Source.UserID
	if isGroup {
		targetID = firstNonEmpty(event.Source.GroupID, event.Source.RoomID)
	}
	text, attachments, mentionBot := c.parseMessage(event.Message, messageID)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	command := nativePlatformIsCommand(text)
	conversationKind := "private"
	if isGroup {
		conversationKind = "group"
	}
	occurredAt := time.Now().UTC()
	if event.Timestamp > 0 {
		occurredAt = time.UnixMilli(event.Timestamp).UTC()
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: lineTransport, MessageID: messageID,
		RouteKind: "target", TargetID: targetID, GuildID: destination, ChannelID: event.ReplyToken,
		ConversationID: event.Source.Type + ":" + targetID, ConversationKind: conversationKind,
		SenderID: senderID, SenderName: senderID, Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, senderID),
		IsMentionBot: mentionBot, IsCommand: command,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *lineConnector) parseMessage(message struct {
	ID              string `json:"id"`
	Type            string `json:"type"`
	Text            string `json:"text"`
	FileName        string `json:"fileName"`
	ContentProvider *struct {
		Type               string `json:"type"`
		OriginalContentURL string `json:"originalContentUrl"`
	} `json:"contentProvider"`
	Mention *struct {
		Mentionees []struct {
			Index  int    `json:"index"`
			Length int    `json:"length"`
			Type   string `json:"type"`
			UserID string `json:"userId"`
			IsSelf bool   `json:"isSelf"`
		} `json:"mentionees"`
	} `json:"mention"`
}, messageID string) (string, []transportAttachment, bool) {
	if message.Type == "text" {
		text, mentionBot := message.Text, false
		if message.Mention != nil {
			for _, mention := range message.Mention.Mentionees {
				if mention.IsSelf {
					mentionBot = true
					if mention.Index >= 0 && mention.Length > 0 && mention.Index+mention.Length <= len([]rune(text)) {
						runes := []rune(text)
						text = string(runes[:mention.Index]) + string(runes[mention.Index+mention.Length:])
					}
					break
				}
			}
		}
		return strings.TrimSpace(text), nil, mentionBot
	}
	kind := map[string]string{"image": "image", "video": "video", "audio": "audio", "file": "file"}[message.Type]
	if kind == "" {
		return "[" + message.Type + "]", nil, false
	}
	source := ""
	if message.ContentProvider != nil && message.ContentProvider.Type == "external" {
		source = message.ContentProvider.OriginalContentURL
	}
	if source == "" && c.publicBase != "" {
		token := c.registerMedia(lineMediaRecord{MessageID: messageID, ExpiresAt: time.Now().Add(time.Hour)})
		source = c.publicBase + "/media/" + token
	}
	if source == "" {
		return "[" + message.Type + "]", nil, false
	}
	return "", []transportAttachment{{Kind: kind, SourceURL: source, Name: message.FileName}}, false
}

func (c *lineConnector) registerMedia(record lineMediaRecord) string {
	token, err := newCoreUUID()
	if err != nil {
		token = c.runtime.platformPseudonym("line_media", firstNonEmpty(record.LocalPath, record.MessageID)+time.Now().String())
	}
	c.mediaMu.Lock()
	c.media[token] = record
	now := time.Now()
	for key, value := range c.media {
		if value.ExpiresAt.Before(now) {
			delete(c.media, key)
		}
	}
	c.mediaMu.Unlock()
	return token
}

func (c *lineConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(r.URL.Path, "/media/")
	c.mediaMu.RLock()
	record, found := c.media[token]
	c.mediaMu.RUnlock()
	if !found || record.ExpiresAt.Before(time.Now()) {
		http.NotFound(w, r)
		return
	}
	if record.LocalPath != "" {
		data, err := os.ReadFile(record.LocalPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		contentType := firstNonEmpty(record.MimeType, mime.TypeByExtension(filepath.Ext(record.LocalPath)), "application/octet-stream")
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=3600")
		if r.Method == http.MethodGet {
			_, _ = w.Write(data)
		}
		return
	}
	request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, c.dataBase+"/v2/bot/message/"+url.PathEscape(record.MessageID)+"/content", nil)
	if err != nil {
		http.Error(w, "upstream error", http.StatusBadGateway)
		return
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
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
	w.Header().Set("Cache-Control", "private, max-age=300")
	if r.Method == http.MethodGet {
		_, _ = io.Copy(w, io.LimitReader(response.Body, 20<<20))
	}
}

func (c *lineConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	messages := []map[string]any{}
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 5000) {
		messages = append(messages, map[string]any{"type": "text", "text": text})
	}
	for _, attachment := range delivery.Message.Attachments {
		if c.publicBase == "" {
			return &platformDeliveryError{Retryable: false, Reason: "line_public_media_url_missing"}
		}
		_, cleanPath, err := readNativeMedia(attachment)
		if err != nil {
			return &platformDeliveryError{Retryable: false, Reason: "line_attachment_invalid", Cause: err}
		}
		token := c.registerMedia(lineMediaRecord{LocalPath: cleanPath, MimeType: attachment.MimeType, ExpiresAt: time.Now().Add(time.Hour)})
		mediaURL := c.publicBase + "/media/" + token
		switch strings.ToLower(attachment.Kind) {
		case "image":
			messages = append(messages, map[string]any{"type": "image", "originalContentUrl": mediaURL, "previewImageUrl": mediaURL})
		case "audio":
			messages = append(messages, map[string]any{"type": "audio", "originalContentUrl": mediaURL, "duration": 1000})
		case "video":
			if c.videoPreviewURL == "" {
				return &platformDeliveryError{Retryable: false, Reason: "line_video_preview_url_missing"}
			}
			messages = append(messages, map[string]any{"type": "video", "originalContentUrl": mediaURL, "previewImageUrl": c.videoPreviewURL})
		default:
			return &platformDeliveryError{Retryable: false, Reason: "line_attachment_kind_unsupported"}
		}
	}
	if len(messages) == 0 {
		return nil
	}
	if len(messages) > 5 {
		messages = messages[:5]
	}
	var err error
	if route.ChannelID != "" {
		err = c.lineJSON(ctx, "/v2/bot/message/reply", map[string]any{"replyToken": route.ChannelID, "messages": messages, "notificationDisabled": false})
	}
	if route.ChannelID == "" || err != nil {
		err = c.lineJSON(ctx, "/v2/bot/message/push", map[string]any{"to": route.TargetID, "messages": messages, "notificationDisabled": false})
	}
	if err != nil {
		return lineDeliveryError("line_send_failed", err)
	}
	c.state.markDelivery()
	return nil
}

func lineDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	var httpErr *lineHTTPError
	if errors.As(err, &httpErr) {
		retryable = httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func (c *lineConnector) lineJSON(ctx context.Context, path string, input any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.apiBase+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.accessToken)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &lineHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	return nil
}
