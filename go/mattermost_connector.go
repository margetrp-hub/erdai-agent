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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const mattermostTransport = "mattermost"

type mattermostConnector struct {
	runtime        *AgentRuntime
	platform       mgmtPlatform
	token          string
	baseURL        string
	websocketURL   string
	reconnectDelay time.Duration
	adminIDs       map[string]struct{}
	client         *http.Client
	dialer         *websocket.Dialer
	state          nativeConnectorState
	cancel         context.CancelFunc
	connMu         sync.RWMutex
	conn           *websocket.Conn
	writeMu        sync.Mutex
	botID          string
	botUsername    string
	mentionPattern *regexp.Regexp
}

type mattermostPost struct {
	ID        string   `json:"id"`
	ChannelID string   `json:"channel_id"`
	UserID    string   `json:"user_id"`
	Message   string   `json:"message"`
	Type      string   `json:"type"`
	RootID    string   `json:"root_id"`
	CreateAt  int64    `json:"create_at"`
	FileIDs   []string `json:"file_ids"`
}

type mattermostHTTPError struct {
	Status int
	Body   string
}

func (e *mattermostHTTPError) Error() string {
	return fmt.Sprintf("Mattermost status %d: %s", e.Status, e.Body)
}

func newMattermostConnector(runtime *AgentRuntime, platform mgmtPlatform) (*mattermostConnector, error) {
	token := resolvePlatformCredential(platform, "mattermost_bot_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "mattermost_bot_token")
	}
	baseURL, _ := platform.Settings["mattermost_url"].(string)
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return nil, platformConnectorStartupError(platform, "mattermost_url")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, errors.New("Mattermost URL is invalid")
	}
	wsURL := *parsed
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path = strings.TrimRight(wsURL.Path, "/") + "/api/v4/websocket"
	return &mattermostConnector{
		runtime: runtime, platform: platform, token: token, baseURL: baseURL,
		websocketURL: wsURL.String(), reconnectDelay: kookDurationSetting(platform, "mattermost_reconnect_delay", 5*time.Second),
		adminIDs: nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		dialer: websocket.DefaultDialer, state: newNativeConnectorState(platform),
	}, nil
}

func (c *mattermostConnector) ID() string   { return c.platform.ID }
func (c *mattermostConnector) Type() string { return mattermostTransport }

func (c *mattermostConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.state.setStatus("connecting")
	go c.run(ctx)
	return nil
}

func (c *mattermostConnector) Close() error {
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

func (c *mattermostConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *mattermostConnector) run(ctx context.Context) {
	if err := c.loadIdentity(ctx); err != nil {
		c.state.setError(err)
		return
	}
	for ctx.Err() == nil {
		if err := c.runSession(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
		if !waitContext(ctx, c.reconnectDelay) {
			return
		}
	}
}

func (c *mattermostConnector) loadIdentity(ctx context.Context) error {
	var identity struct {
		ID       string `json:"id"`
		Username string `json:"username"`
	}
	if err := c.mattermostJSON(ctx, http.MethodGet, "/users/me", nil, &identity); err != nil {
		return err
	}
	if identity.ID == "" {
		return errors.New("Mattermost bot identity is empty")
	}
	c.botID, c.botUsername = identity.ID, identity.Username
	if c.botUsername != "" {
		c.mentionPattern = regexp.MustCompile(`(?i)(^|[^A-Za-z0-9_.-])@` + regexp.QuoteMeta(c.botUsername) + `([^A-Za-z0-9_.-]|$)`)
	}
	return nil
}

func (c *mattermostConnector) runSession(ctx context.Context) error {
	header := http.Header{"Authorization": {"Bearer " + c.token}}
	conn, _, err := c.dialer.DialContext(ctx, c.websocketURL, header)
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
	if err = c.writeGateway(conn, map[string]any{
		"seq": 1, "action": "authentication_challenge", "data": map[string]string{"token": c.token},
	}); err != nil {
		return err
	}
	c.state.setStatus("connected")
	for ctx.Err() == nil {
		var payload map[string]json.RawMessage
		if err = conn.ReadJSON(&payload); err != nil {
			return err
		}
		if err = c.handleWebSocketEvent(ctx, payload); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (c *mattermostConnector) writeGateway(conn *websocket.Conn, value any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(value)
}

func (c *mattermostConnector) handleWebSocketEvent(ctx context.Context, payload map[string]json.RawMessage) error {
	var event string
	_ = json.Unmarshal(payload["event"], &event)
	if event != "posted" {
		return nil
	}
	var data struct {
		Post        string `json:"post"`
		ChannelType string `json:"channel_type"`
		SenderName  string `json:"sender_name"`
	}
	if err := json.Unmarshal(payload["data"], &data); err != nil {
		return err
	}
	var post mattermostPost
	if err := json.Unmarshal([]byte(data.Post), &post); err != nil {
		return err
	}
	if post.ID == "" || post.ChannelID == "" || post.UserID == "" || post.UserID == c.botID || post.Type != "" {
		return nil
	}
	mentionBot := c.mentionPattern != nil && c.mentionPattern.MatchString(post.Message)
	text := strings.TrimSpace(post.Message)
	if mentionBot {
		text = strings.TrimSpace(c.mentionPattern.ReplaceAllString(text, "$1$2"))
	}
	attachments, err := c.inboundAttachments(ctx, post.FileIDs)
	if err != nil {
		return err
	}
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	isGroup := data.ChannelType != "D"
	conversationKind := "private"
	if isGroup {
		conversationKind = "group"
	}
	command := nativePlatformIsCommand(text)
	occurredAt := time.Now().UTC()
	if post.CreateAt > 0 {
		occurredAt = time.UnixMilli(post.CreateAt).UTC()
	}
	if err = c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: mattermostTransport, MessageID: post.ID,
		RouteKind: "channel", TargetID: post.ChannelID, ChannelID: post.ChannelID,
		ConversationID: post.ChannelID, ConversationKind: conversationKind,
		SenderID: post.UserID, SenderName: strings.TrimPrefix(firstNonEmpty(data.SenderName, post.UserID), "@"),
		Text: text, Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, post.UserID),
		IsMentionBot: mentionBot, IsCommand: command,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *mattermostConnector) inboundAttachments(ctx context.Context, fileIDs []string) ([]transportAttachment, error) {
	attachments := make([]transportAttachment, 0, min(len(fileIDs), 3))
	for _, fileID := range fileIDs {
		if strings.TrimSpace(fileID) == "" || len(attachments) == 3 {
			continue
		}
		var info struct {
			Name     string `json:"name"`
			MimeType string `json:"mime_type"`
		}
		if err := c.mattermostJSON(ctx, http.MethodGet, "/files/"+url.PathEscape(fileID)+"/info", nil, &info); err != nil {
			return nil, err
		}
		attachments = append(attachments, transportAttachment{
			Kind: discordAttachmentKind(info.MimeType, info.Name), Name: info.Name,
			SourceURL: c.baseURL + "/api/v4/files/" + url.PathEscape(fileID),
		})
	}
	return attachments, nil
}

func (c *mattermostConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.TargetID == "" {
		return &platformDeliveryError{Retryable: false, Reason: "mattermost_channel_missing"}
	}
	fileIDs := make([]string, 0, len(delivery.Message.Attachments))
	for _, attachment := range delivery.Message.Attachments {
		fileID, err := c.uploadFile(ctx, route.TargetID, attachment)
		if err != nil {
			return err
		}
		fileIDs = append(fileIDs, fileID)
	}
	texts := splitTelegramText(strings.TrimSpace(delivery.Message.Text), 16000)
	if len(texts) == 0 && len(fileIDs) > 0 {
		texts = []string{""}
	}
	for index, text := range texts {
		payload := map[string]any{"channel_id": route.TargetID, "message": text}
		if route.MessageID != "" {
			payload["root_id"] = route.MessageID
		}
		if index == len(texts)-1 && len(fileIDs) > 0 {
			payload["file_ids"] = fileIDs
		}
		if err := c.mattermostJSON(ctx, http.MethodPost, "/posts", payload, nil); err != nil {
			return mattermostDeliveryError("mattermost_post_failed", err)
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *mattermostConnector) uploadFile(ctx context.Context, channelID string, attachment agentAttachment) (string, error) {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "mattermost_attachment_invalid", Cause: err}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("channel_id", channelID)
	name := firstNonEmpty(attachment.Name, filepath.Base(cleanPath))
	part, err := writer.CreateFormFile("files", name)
	if err == nil {
		_, err = part.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", &platformDeliveryError{Retryable: false, Reason: "mattermost_attachment_encode_failed", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/v4/files", &body)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+c.token)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return "", &platformDeliveryError{Retryable: true, Reason: "mattermost_attachment_upload_failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return "", mattermostDeliveryError("mattermost_attachment_upload_failed", &mattermostHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))})
	}
	var result struct {
		FileInfos []struct {
			ID string `json:"id"`
		} `json:"file_infos"`
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil || len(result.FileInfos) == 0 || result.FileInfos[0].ID == "" {
		return "", &platformDeliveryError{Retryable: true, Reason: "mattermost_attachment_upload_failed", Cause: errors.New("Mattermost upload returned no file id")}
	}
	return result.FileInfos[0].ID, nil
}

func mattermostDeliveryError(reason string, err error) *platformDeliveryError {
	retryable := true
	var httpErr *mattermostHTTPError
	if errors.As(err, &httpErr) {
		retryable = httpErr.Status == http.StatusTooManyRequests || httpErr.Status >= 500
	}
	return &platformDeliveryError{Retryable: retryable, Reason: reason, Cause: err}
}

func (c *mattermostConnector) mattermostJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+"/api/v4"+path, body)
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
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &mattermostHTTPError{Status: response.StatusCode, Body: strings.TrimSpace(string(payload))}
	}
	if output == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}
