package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const telegramTransport = "telegram"

type telegramUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

type telegramFile struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MimeType string `json:"mime_type"`
	FilePath string `json:"file_path"`
}

type telegramPhoto struct {
	FileID string `json:"file_id"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

type telegramMessage struct {
	MessageID       int64            `json:"message_id"`
	MessageThreadID int64            `json:"message_thread_id"`
	Date            int64            `json:"date"`
	Chat            telegramChat     `json:"chat"`
	From            *telegramUser    `json:"from"`
	Text            string           `json:"text"`
	Caption         string           `json:"caption"`
	Photo           []telegramPhoto  `json:"photo"`
	Voice           *telegramFile    `json:"voice"`
	Audio           *telegramFile    `json:"audio"`
	Video           *telegramFile    `json:"video"`
	Document        *telegramFile    `json:"document"`
	ReplyToMessage  *telegramMessage `json:"reply_to_message"`
}

type telegramUpdate struct {
	UpdateID int64            `json:"update_id"`
	Message  *telegramMessage `json:"message"`
}

type telegramResponse[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

type telegramConnector struct {
	runtime      *AgentRuntime
	platform     mgmtPlatform
	token        string
	apiBase      string
	fileBase     string
	publicBase   string
	mediaHost    string
	mediaPort    int
	startMessage string
	adminIDs     map[string]struct{}
	client       *http.Client
	state        nativeConnectorState
	cancel       context.CancelFunc
	server       *platformWebhookServer
	mediaMu      sync.RWMutex
	media        map[string]telegramMediaRecord
	offset       int64
	botID        int64
	botUsername  string
}

type telegramMediaRecord struct {
	FilePath  string
	Name      string
	ExpiresAt time.Time
}

func newTelegramConnector(runtime *AgentRuntime, platform mgmtPlatform) (*telegramConnector, error) {
	token := resolvePlatformCredential(platform, "telegram_token")
	if token == "" {
		return nil, platformConnectorStartupError(platform, "telegram_token")
	}
	apiBase, _ := platform.Settings["telegram_api_base_url"].(string)
	fileBase, _ := platform.Settings["telegram_file_base_url"].(string)
	publicBase, _ := platform.Settings["telegram_public_base_url"].(string)
	mediaHost, _ := platform.Settings["telegram_media_host"].(string)
	if strings.TrimSpace(mediaHost) == "" {
		mediaHost = "0.0.0.0"
	}
	startMessage, _ := platform.Settings["start_message"].(string)
	apiBase = strings.TrimRight(strings.TrimSpace(apiBase), "/")
	fileBase = strings.TrimRight(strings.TrimSpace(fileBase), "/")
	if apiBase == "" {
		apiBase = "https://api.telegram.org/bot"
	}
	if fileBase == "" {
		fileBase = "https://api.telegram.org/file/bot"
	}
	return &telegramConnector{
		runtime: runtime, platform: platform, token: token, apiBase: apiBase, fileBase: fileBase,
		publicBase: strings.TrimRight(publicBase, "/"), mediaHost: mediaHost,
		mediaPort: kookIntSetting(platform, "telegram_media_port", 6204), media: map[string]telegramMediaRecord{},
		startMessage: startMessage, adminIDs: nativePlatformAdminIDs(platform),
		client: &http.Client{Timeout: 40 * time.Second}, state: newNativeConnectorState(platform),
	}, nil
}

func (c *telegramConnector) ID() string   { return c.platform.ID }
func (c *telegramConnector) Type() string { return telegramTransport }

func (c *telegramConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if c.publicBase != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/telegram-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.mediaHost, c.mediaPort, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	c.state.setStatus("connecting")
	go c.pollLoop(ctx)
	return nil
}

func (c *telegramConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *telegramConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *telegramConnector) pollLoop(ctx context.Context) {
	delay := time.Duration(intSetting(c.platform.Settings, "telegram_polling_restart_delay", 5)) * time.Second
	if delay < time.Second {
		delay = time.Second
	}
	for ctx.Err() == nil {
		if c.botID == 0 {
			if err := c.loadIdentity(ctx); err != nil {
				c.state.setError(err)
				if !waitContext(ctx, delay) {
					return
				}
				continue
			}
		}
		if err := c.pollOnce(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
			if !waitContext(ctx, delay) {
				return
			}
		}
	}
}

func (c *telegramConnector) loadIdentity(ctx context.Context) error {
	var response telegramResponse[telegramUser]
	if err := c.get(ctx, "getMe", nil, &response); err != nil {
		return err
	}
	if !response.OK || response.Result.ID == 0 {
		return fmt.Errorf("Telegram getMe failed: %s", response.Description)
	}
	c.botID, c.botUsername = response.Result.ID, response.Result.Username
	c.state.setStatus("connected")
	return nil
}

func (c *telegramConnector) pollOnce(ctx context.Context) error {
	query := url.Values{
		"offset": {strconv.FormatInt(c.offset, 10)}, "timeout": {"25"},
		"allowed_updates": {`["message"]`},
	}
	var response telegramResponse[[]telegramUpdate]
	if err := c.get(ctx, "getUpdates", query, &response); err != nil {
		return err
	}
	if !response.OK {
		return fmt.Errorf("Telegram getUpdates failed: %s", response.Description)
	}
	for _, update := range response.Result {
		if update.UpdateID >= c.offset {
			c.offset = update.UpdateID + 1
		}
		if update.Message != nil {
			if err := c.handleMessage(ctx, update.Message); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *telegramConnector) handleMessage(ctx context.Context, message *telegramMessage) error {
	if message == nil || message.MessageID == 0 || message.From == nil || message.From.ID == 0 || message.From.IsBot {
		return nil
	}
	text := strings.TrimSpace(firstNonEmpty(message.Text, message.Caption))
	mentionBot := c.telegramMentioned(text) || (message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && message.ReplyToMessage.From.ID == c.botID)
	if c.botUsername != "" {
		text = stripTelegramBotMention(text, c.botUsername)
	}
	if message.ReplyToMessage != nil {
		quoted := strings.TrimSpace(firstNonEmpty(message.ReplyToMessage.Text, message.ReplyToMessage.Caption))
		if quoted != "" {
			text = "[引用消息]\n" + quoted + "\n[当前消息]\n" + text
		}
	}
	attachments, err := c.telegramAttachments(ctx, message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(text) == "" {
		if len(attachments) > 0 {
			text = nativeAttachmentOnlyPrompt(attachments)
		} else {
			text = telegramAttachmentPlaceholder(message)
		}
	}
	chatID := strconv.FormatInt(message.Chat.ID, 10)
	isGroup := message.Chat.Type != "private"
	conversationID, targetID, kind, conversationKind := chatID, chatID, "private", "private"
	if isGroup {
		kind, conversationKind = "group", "group"
		if message.MessageThreadID != 0 {
			conversationID += "#" + strconv.FormatInt(message.MessageThreadID, 10)
		}
	}
	command := nativePlatformIsCommand(text)
	if command && strings.TrimSpace(text) == "/start" && strings.TrimSpace(c.startMessage) != "" {
		return c.sendJSON(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": c.startMessage, "message_thread_id": message.MessageThreadID})
	}
	name := strings.TrimSpace(strings.Join([]string{message.From.FirstName, message.From.LastName}, " "))
	name = firstNonEmpty(name, message.From.Username, strconv.FormatInt(message.From.ID, 10))
	occurredAt := time.Now().UTC()
	if message.Date > 0 {
		occurredAt = time.Unix(message.Date, 0).UTC()
	}
	if err = c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: telegramTransport, MessageID: strconv.FormatInt(message.MessageID, 10),
		RouteKind: kind, TargetID: targetID, ChannelID: strconv.FormatInt(message.MessageThreadID, 10),
		ConversationID: conversationID, ConversationKind: conversationKind,
		SenderID: strconv.FormatInt(message.From.ID, 10), SenderName: name, Text: text,
		Attachments: attachments, OccurredAt: occurredAt,
		IsWake: !isGroup || mentionBot || command, IsAdmin: nativePlatformIsAdmin(c.adminIDs, strconv.FormatInt(message.From.ID, 10)),
		IsMentionBot: mentionBot, IsCommand: command,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *telegramConnector) telegramMentioned(text string) bool {
	if c.botUsername == "" {
		return false
	}
	return strings.Contains(strings.ToLower(text), "@"+strings.ToLower(c.botUsername))
}

func stripTelegramBotMention(text, username string) string {
	needle := "@" + username
	lower, lowerNeedle := strings.ToLower(text), strings.ToLower(needle)
	for {
		index := strings.Index(lower, lowerNeedle)
		if index < 0 {
			break
		}
		text = text[:index] + text[index+len(needle):]
		lower = strings.ToLower(text)
	}
	parts := strings.Fields(text)
	if len(parts) > 0 && strings.HasPrefix(parts[0], "/") && strings.Contains(parts[0], "@") {
		parts[0] = strings.SplitN(parts[0], "@", 2)[0]
		text = strings.Join(parts, " ")
	}
	return strings.TrimSpace(text)
}

func (c *telegramConnector) telegramAttachments(ctx context.Context, message *telegramMessage) ([]transportAttachment, error) {
	if c.publicBase == "" {
		return nil, nil
	}
	values := []struct {
		kind string
		file telegramFile
	}{}
	if len(message.Photo) > 0 {
		values = append(values, struct {
			kind string
			file telegramFile
		}{"image", telegramFile{FileID: message.Photo[len(message.Photo)-1].FileID}})
	}
	for _, value := range []struct {
		kind string
		file *telegramFile
	}{{"audio", message.Voice}, {"audio", message.Audio}, {"video", message.Video}, {"file", message.Document}} {
		if value.file != nil && value.file.FileID != "" {
			values = append(values, struct {
				kind string
				file telegramFile
			}{value.kind, *value.file})
		}
	}
	attachments := []transportAttachment{}
	for _, value := range values {
		if len(attachments) == 3 {
			break
		}
		path, err := c.telegramFilePath(ctx, value.file.FileID)
		if err != nil {
			return nil, err
		}
		mediaID := c.runtime.platformPseudonym("telegram-media", c.ID()+":"+path)
		c.mediaMu.Lock()
		c.media[mediaID] = telegramMediaRecord{FilePath: strings.TrimLeft(path, "/"), Name: value.file.FileName, ExpiresAt: time.Now().Add(15 * time.Minute)}
		c.mediaMu.Unlock()
		attachments = append(attachments, transportAttachment{
			Kind: value.kind, Name: value.file.FileName,
			SourceURL: c.publicBase + "/telegram-media/" + url.PathEscape(mediaID),
		})
	}
	return attachments, nil
}

func telegramAttachmentPlaceholder(message *telegramMessage) string {
	if message == nil {
		return ""
	}
	if len(message.Photo) > 0 {
		return "[图片]"
	}
	if message.Voice != nil || message.Audio != nil {
		return "[语音]"
	}
	if message.Video != nil {
		return "[视频]"
	}
	if message.Document != nil {
		return "[文件]"
	}
	return ""
}

func (c *telegramConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	id, _ := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/telegram-media/"))
	c.mediaMu.RLock()
	record, found := c.media[id]
	c.mediaMu.RUnlock()
	if r.Method != http.MethodGet || !found || time.Now().After(record.ExpiresAt) {
		http.NotFound(w, r)
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, c.fileBase+c.token+"/"+record.FilePath, nil)
	response, err := c.client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	if record.Name != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", strings.ReplaceAll(record.Name, `"`, "")))
	}
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxImageBytes+1))
}

func (c *telegramConnector) telegramFilePath(ctx context.Context, fileID string) (string, error) {
	var response telegramResponse[telegramFile]
	if err := c.get(ctx, "getFile", url.Values{"file_id": {fileID}}, &response); err != nil {
		return "", err
	}
	if !response.OK || response.Result.FilePath == "" {
		return "", fmt.Errorf("Telegram getFile failed: %s", response.Description)
	}
	return response.Result.FilePath, nil
}

func (c *telegramConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	threadID, _ := strconv.ParseInt(route.ChannelID, 10, 64)
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 4096) {
		payload := map[string]any{"chat_id": route.TargetID, "text": text}
		if route.MessageID != "" {
			payload["reply_to_message_id"] = route.MessageID
		}
		if threadID != 0 {
			payload["message_thread_id"] = threadID
		}
		if err := c.sendJSON(ctx, "sendMessage", payload); err != nil {
			return &platformDeliveryError{Retryable: true, Reason: "telegram_text_send_failed", Cause: err}
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		if err := c.sendAttachment(ctx, route, attachment, threadID); err != nil {
			return err
		}
	}
	c.state.markDelivery()
	return nil
}

func splitTelegramText(text string, limit int) []string {
	if text == "" {
		return nil
	}
	runes := []rune(text)
	values := []string{}
	for len(runes) > limit {
		cut := limit
		for index := limit - 1; index > limit/2; index-- {
			if strings.ContainsRune("\n。！？!?；;，, ", runes[index]) {
				cut = index + 1
				break
			}
		}
		values = append(values, strings.TrimSpace(string(runes[:cut])))
		runes = []rune(strings.TrimSpace(string(runes[cut:])))
	}
	if len(runes) > 0 {
		values = append(values, string(runes))
	}
	return values
}

func (c *telegramConnector) sendAttachment(ctx context.Context, route platformReplyRoute, attachment agentAttachment, threadID int64) error {
	data, cleanPath, err := readNativeMedia(attachment)
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "telegram_attachment_invalid", Cause: err}
	}
	method, field := "sendDocument", "document"
	switch strings.ToLower(attachment.Kind) {
	case "image":
		method, field = "sendPhoto", "photo"
	case "video":
		method, field = "sendVideo", "video"
	case "audio":
		method, field = "sendVoice", "voice"
	case "file":
	default:
		return &platformDeliveryError{Retryable: false, Reason: "telegram_attachment_kind_unsupported"}
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("chat_id", route.TargetID)
	if route.MessageID != "" {
		_ = writer.WriteField("reply_to_message_id", route.MessageID)
	}
	if threadID != 0 {
		_ = writer.WriteField("message_thread_id", strconv.FormatInt(threadID, 10))
	}
	name := firstNonEmpty(attachment.Name, filepath.Base(cleanPath))
	part, err := writer.CreateFormFile(field, name)
	if err == nil {
		_, err = part.Write(data)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "telegram_attachment_encode_failed", Cause: err}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), &body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "telegram_attachment_send_failed", Cause: err}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &platformDeliveryError{Retryable: response.StatusCode >= 500 || response.StatusCode == 429, Reason: "telegram_attachment_send_failed", Cause: fmt.Errorf("status %d: %s", response.StatusCode, strings.TrimSpace(string(payload)))}
	}
	return nil
}

func (c *telegramConnector) endpoint(method string) string {
	return c.apiBase + c.token + "/" + method
}

func (c *telegramConnector) get(ctx context.Context, method string, query url.Values, output any) error {
	endpoint := c.endpoint(method)
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		payload, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf("Telegram %s status %d: %s", method, response.StatusCode, strings.TrimSpace(string(payload)))
	}
	return json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(output)
}

func (c *telegramConnector) sendJSON(ctx context.Context, method string, payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(method), bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result telegramResponse[json.RawMessage]
	if err = json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&result); err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		return fmt.Errorf("Telegram %s failed: status=%d %s", method, response.StatusCode, result.Description)
	}
	return nil
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
