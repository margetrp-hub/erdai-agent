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
	"time"

	"github.com/gorilla/websocket"
)

const misskeyTransport = "misskey"

type misskeyConnector struct {
	runtime           *AgentRuntime
	platform          mgmtPlatform
	token             string
	instanceURL       string
	streamingURL      string
	defaultVisibility string
	localOnly         bool
	enableChat        bool
	enableFileUpload  bool
	uploadFolder      string
	maxMessageLength  int
	adminIDs          map[string]struct{}
	client            *http.Client
	dialer            *websocket.Dialer
	state             nativeConnectorState
	cancel            context.CancelFunc
	connMu            sync.RWMutex
	conn              *websocket.Conn
	writeMu           sync.Mutex
	botID             string
	botUsername       string
}

type misskeyUser struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
}

type misskeyFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

type misskeyNote struct {
	ID             string        `json:"id"`
	Text           string        `json:"text"`
	UserID         string        `json:"userId"`
	User           misskeyUser   `json:"user"`
	Mentions       []string      `json:"mentions"`
	Files          []misskeyFile `json:"files"`
	Visibility     string        `json:"visibility"`
	VisibleUserIDs []string      `json:"visibleUserIds"`
	CreatedAt      string        `json:"createdAt"`
	Poll           *struct {
		Multiple bool `json:"multiple"`
		Choices  []struct {
			Text  string `json:"text"`
			Votes int    `json:"votes"`
		} `json:"choices"`
	} `json:"poll"`
}

type misskeyChatMessage struct {
	ID         string        `json:"id"`
	Text       string        `json:"text"`
	FromUserID string        `json:"fromUserId"`
	FromUser   misskeyUser   `json:"fromUser"`
	ToRoomID   string        `json:"toRoomId"`
	Files      []misskeyFile `json:"files"`
	CreatedAt  string        `json:"createdAt"`
}

type misskeyHTTPError struct {
	Status int
	Body   string
}

func (e *misskeyHTTPError) Error() string {
	return fmt.Sprintf("Misskey status %d: %s", e.Status, e.Body)
}

func newMisskeyConnector(runtime *AgentRuntime, platform mgmtPlatform) (*misskeyConnector, error) {
	token := resolvePlatformCredential(platform, "misskey_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "misskey_token")
	}
	instanceURL, _ := platform.Settings["misskey_instance_url"].(string)
	instanceURL = strings.TrimRight(strings.TrimSpace(instanceURL), "/")
	parsed, err := url.Parse(instanceURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Misskey instance URL is invalid")
	}
	streamingURL := *parsed
	if streamingURL.Scheme == "https" {
		streamingURL.Scheme = "wss"
	} else {
		streamingURL.Scheme = "ws"
	}
	streamingURL.Path = strings.TrimRight(streamingURL.Path, "/") + "/streaming"
	query := streamingURL.Query()
	query.Set("i", token)
	streamingURL.RawQuery = query.Encode()
	defaultVisibility, _ := platform.Settings["misskey_default_visibility"].(string)
	if defaultVisibility == "" {
		defaultVisibility = "public"
	}
	localOnly, _ := platform.Settings["misskey_local_only"].(bool)
	enableChat, found := platform.Settings["misskey_enable_chat"].(bool)
	if !found {
		enableChat = true
	}
	enableFileUpload, found := platform.Settings["misskey_enable_file_upload"].(bool)
	if !found {
		enableFileUpload = true
	}
	uploadFolder, _ := platform.Settings["misskey_upload_folder"].(string)
	return &misskeyConnector{
		runtime: runtime, platform: platform, token: token, instanceURL: instanceURL,
		streamingURL: streamingURL.String(), defaultVisibility: defaultVisibility, localOnly: localOnly,
		enableChat: enableChat, enableFileUpload: enableFileUpload, uploadFolder: strings.TrimSpace(uploadFolder),
		maxMessageLength: kookIntSetting(platform, "max_message_length", 3000),
		adminIDs:         nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		dialer: websocket.DefaultDialer, state: newNativeConnectorState(platform),
	}, nil
}

func (c *misskeyConnector) ID() string   { return c.platform.ID }
func (c *misskeyConnector) Type() string { return misskeyTransport }

func (c *misskeyConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.state.setStatus("connecting")
	go c.run(ctx)
	return nil
}

func (c *misskeyConnector) Close() error {
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

func (c *misskeyConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *misskeyConnector) run(ctx context.Context) {
	if err := c.loadIdentity(ctx); err != nil {
		c.state.setError(err)
		return
	}
	delay := time.Second
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
		if !waitContext(ctx, delay) {
			return
		}
		if delay < 5*time.Minute {
			delay = time.Duration(float64(delay) * 1.5)
		}
	}
}

func (c *misskeyConnector) loadIdentity(ctx context.Context) error {
	var identity misskeyUser
	if err := c.misskeyJSON(ctx, "/i", map[string]any{}, &identity); err != nil {
		return err
	}
	if identity.ID == "" {
		return errors.New("Misskey bot identity is empty")
	}
	c.botID, c.botUsername = identity.ID, identity.Username
	return nil
}

func (c *misskeyConnector) runSession(ctx context.Context) error {
	conn, _, err := c.dialer.DialContext(ctx, c.streamingURL, nil)
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
	channels := map[string]string{}
	for _, channel := range []string{"main", "messaging", "messagingIndex"} {
		if channel != "main" && !c.enableChat {
			continue
		}
		id, idErr := newCoreUUID()
		if idErr != nil {
			return idErr
		}
		channels[id] = channel
		if err = c.writeStreaming(conn, map[string]any{
			"type": "connect", "body": map[string]any{"channel": channel, "id": id, "params": map[string]any{}},
		}); err != nil {
			return err
		}
	}
	c.state.setStatus("connected")
	for ctx.Err() == nil {
		var payload struct {
			Type string `json:"type"`
			Body struct {
				ID   string          `json:"id"`
				Type string          `json:"type"`
				Body json.RawMessage `json:"body"`
			} `json:"body"`
		}
		if err = conn.ReadJSON(&payload); err != nil {
			return err
		}
		if payload.Type != "channel" {
			continue
		}
		channel := channels[payload.Body.ID]
		switch payload.Body.Type {
		case "notification":
			if channel == "main" {
				err = c.handleNotification(ctx, payload.Body.Body)
			}
		case "newChatMessage":
			if channel == "messaging" || channel == "messagingIndex" {
				err = c.handleChat(ctx, payload.Body.Body)
			}
		}
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *misskeyConnector) writeStreaming(conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *misskeyConnector) handleNotification(ctx context.Context, raw json.RawMessage) error {
	var notification struct {
		Type string      `json:"type"`
		Note misskeyNote `json:"note"`
	}
	if err := json.Unmarshal(raw, &notification); err != nil {
		return err
	}
	if notification.Type != "mention" && notification.Type != "reply" && notification.Type != "quote" {
		return nil
	}
	if notification.Note.ID == "" || notification.Note.User.ID == c.botID {
		return nil
	}
	mentionBot := kookContains(notification.Note.Mentions, c.botID) || strings.Contains(notification.Note.Text, "@"+c.botUsername)
	if !mentionBot {
		return nil
	}
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(notification.Note.Text), "@"+c.botUsername))
	if poll := formatMisskeyPoll(notification.Note); poll != "" {
		text = strings.TrimSpace(text + "\n" + poll)
	}
	attachments := misskeyAttachments(notification.Note.Files)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	routeKind := "note"
	if notification.Note.Visibility == "specified" {
		routeKind = "note_specified"
	}
	return c.acceptInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: misskeyTransport, MessageID: notification.Note.ID,
		RouteKind: routeKind, TargetID: notification.Note.User.ID,
		ConversationID: "note:" + notification.Note.User.ID, ConversationKind: "private",
		SenderID: notification.Note.User.ID, SenderName: firstNonEmpty(notification.Note.User.Name, notification.Note.User.Username),
		Text: text, Attachments: attachments, OccurredAt: misskeyTime(notification.Note.CreatedAt),
		IsWake: true, IsAdmin: nativePlatformIsAdmin(c.adminIDs, notification.Note.User.ID), IsMentionBot: true,
		IsCommand: nativePlatformIsCommand(text),
	})
}

func (c *misskeyConnector) handleChat(ctx context.Context, raw json.RawMessage) error {
	var message misskeyChatMessage
	if err := json.Unmarshal(raw, &message); err != nil {
		return err
	}
	senderID := firstNonEmpty(message.FromUserID, message.FromUser.ID)
	if message.ID == "" || senderID == "" || senderID == c.botID {
		return nil
	}
	isGroup := message.ToRoomID != ""
	routeKind, targetID, conversationID, conversationKind := "chat", senderID, "chat:"+senderID, "private"
	if isGroup {
		routeKind, targetID, conversationID, conversationKind = "room", message.ToRoomID, "room:"+message.ToRoomID, "group"
	}
	mentionBot := strings.Contains(message.Text, "@"+c.botUsername)
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(message.Text), "@"+c.botUsername))
	attachments := misskeyAttachments(message.Files)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	command := nativePlatformIsCommand(text)
	return c.acceptInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: misskeyTransport, MessageID: message.ID,
		RouteKind: routeKind, TargetID: targetID,
		ConversationID: conversationID, ConversationKind: conversationKind,
		SenderID: senderID, SenderName: firstNonEmpty(message.FromUser.Name, message.FromUser.Username, senderID),
		Text: text, Attachments: attachments, OccurredAt: misskeyTime(message.CreatedAt),
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, senderID),
		IsMentionBot: mentionBot, IsCommand: command,
	})
}

func (c *misskeyConnector) acceptInbound(ctx context.Context, inbound nativePlatformInbound) error {
	if err := c.runtime.acceptNativePlatformInbound(ctx, inbound); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func misskeyAttachments(files []misskeyFile) []transportAttachment {
	attachments := make([]transportAttachment, 0, min(len(files), 3))
	for _, file := range files {
		if file.URL == "" || len(attachments) == 3 {
			continue
		}
		attachments = append(attachments, transportAttachment{
			Kind: discordAttachmentKind(file.Type, file.Name), SourceURL: file.URL, Name: file.Name,
		})
	}
	return attachments
}

func formatMisskeyPoll(note misskeyNote) string {
	if note.Poll == nil || len(note.Poll.Choices) == 0 {
		return ""
	}
	values := make([]string, 0, len(note.Poll.Choices))
	for index, choice := range note.Poll.Choices {
		values = append(values, fmt.Sprintf("(%d) %s [%d票]", index+1, choice.Text, choice.Votes))
	}
	mode := "单选"
	if note.Poll.Multiple {
		mode = "允许多选"
	}
	return "[投票] " + mode + " 选项: " + strings.Join(values, ", ")
}

func misskeyTime(raw string) time.Time {
	if value, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return value.UTC()
	}
	return time.Now().UTC()
}

func (c *misskeyConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	text := strings.TrimSpace(delivery.Message.Text)
	if len([]rune(text)) > c.maxMessageLength {
		text = string([]rune(text)[:c.maxMessageLength])
	}
	fileIDs := []string{}
	if len(delivery.Message.Attachments) > 0 && !c.enableFileUpload {
		return &platformDeliveryError{Retryable: false, Reason: "misskey_file_upload_disabled"}
	}
	for _, attachment := range delivery.Message.Attachments {
		fileID, err := c.uploadFile(ctx, attachment)
		if err != nil {
			return err
		}
		fileIDs = append(fileIDs, fileID)
	}
	var path string
	payload := map[string]any{"text": text}
	switch route.Kind {
	case "chat":
		path, payload["toUserId"] = "/chat/messages/create-to-user", route.TargetID
		if len(fileIDs) > 0 {
			payload["fileId"] = fileIDs[0]
		}
	case "room":
		path, payload["toRoomId"] = "/chat/messages/create-to-room", route.TargetID
		if len(fileIDs) > 0 {
			payload["fileIds"] = fileIDs
		}
	case "note", "note_specified":
		path = "/notes/create"
		payload["visibility"] = c.defaultVisibility
		payload["localOnly"] = c.localOnly
		payload["replyId"] = route.MessageID
		if route.Kind == "note_specified" {
			payload["visibility"] = "specified"
			payload["visibleUserIds"] = []string{route.TargetID, c.botID}
		}
		if len(fileIDs) > 0 {
			payload["fileIds"] = fileIDs
		}
	default:
		return &platformDeliveryError{Retryable: false, Reason: "misskey_route_unsupported"}
	}
	if err := c.misskeyJSON(ctx, path, payload, nil); err != nil {
		return misskeyDeliveryError("misskey_send_failed", err)
	}
	c.state.markDelivery()
	return nil
}

func (c *misskeyConnector) uploadFile(ctx context.Context, attachment agentAttachment) (string, error) {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "misskey_attachment_invalid", Cause: err}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("i", c.token)
	if c.uploadFolder != "" {
		_ = writer.WriteField("folderId", c.uploadFolder)
	}
	part, err := writer.CreateFormFile("file", firstNonEmpty(attachment.Name, filepath.Base(cleanPath)))
	if err == nil {
		_, err = part.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "misskey_attachment_encode_failed", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.instanceURL+"/api/drive/files/create", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return "", &platformDeliveryError{Retryable: true, Reason: "misskey_attachment_upload_failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", misskeyDeliveryError("misskey_attachment_upload_failed", &misskeyHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))})
	}
	var result struct {
		ID string `json:"id"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil || result.ID == "" {
		return "", &platformDeliveryError{Retryable: true, Reason: "misskey_attachment_upload_failed", Cause: errors.New("Misskey upload returned no file id")}
	}
	return result.ID, nil
}

func misskeyDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	var httpErr *misskeyHTTPError
	if errors.As(err, &httpErr) {
		retryable = httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func (c *misskeyConnector) misskeyJSON(ctx context.Context, path string, input, output any) error {
	payload := map[string]any{"i": c.token}
	if values, ok := input.(map[string]any); ok {
		for key, value := range values {
			payload[key] = value
		}
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.instanceURL+"/api"+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &misskeyHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}
