package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	telegram "github.com/amarnathcjd/gogram/telegram"
)

const telegramUserTransport = "telegram_user"

func stringSetting(settings map[string]any, key, fallback string) string {
	if value, ok := settings[key].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func boolSetting(settings map[string]any, key string, fallback bool) bool {
	if value, ok := settings[key].(bool); ok {
		return value
	}
	return fallback
}

// telegramUserConnector is a personal Telegram account connector. It uses
// MTProto with one persistent session per connector instance, while sharing
// the Core event and Outbox contracts with the other native connectors.
type telegramUserConnector struct {
	runtime   *AgentRuntime
	platform  mgmtPlatform
	appID     int
	appHash   string
	session   string
	adminIDs  map[string]struct{}
	state     nativeConnectorState
	cancel    context.CancelFunc
	ready     chan struct{}
	readyOnce sync.Once
	wg        sync.WaitGroup

	clientMu sync.RWMutex
	client   *telegram.Client
	runCtx   context.Context

	authMu         sync.Mutex
	phone          string
	authStep       string
	authRunning    bool
	authFlowCancel context.CancelFunc
	codeInput      chan string
	passwordInput  chan string
	accountID      int64
	username       string
	fullName       string
}

func newTelegramUserConnector(runtime *AgentRuntime, platform mgmtPlatform) (*telegramUserConnector, error) {
	appID := intSetting(platform.Settings, "telegram_user_api_id", 0)
	if appID <= 0 {
		return nil, platformConnectorStartupError(platform, "telegram_user_api_id")
	}
	appHash := resolvePlatformCredential(platform, "api_hash")
	if appHash == "" {
		return nil, platformConnectorStartupError(platform, "api_hash")
	}
	sessionPath, err := telegramUserSessionPath(runtime, platform)
	if err != nil {
		return nil, err
	}
	return &telegramUserConnector{
		runtime: runtime, platform: platform, appID: appID, appHash: appHash,
		session: sessionPath, adminIDs: nativePlatformAdminIDs(platform),
		state: newNativeConnectorState(platform), ready: make(chan struct{}), authStep: "starting",
	}, nil
}

func telegramUserSessionPath(runtime *AgentRuntime, platform mgmtPlatform) (string, error) {
	dataRoot := filepath.Dir(strings.TrimSpace(runtime.mediaDir))
	if dataRoot == "." || dataRoot == "" {
		dataRoot = "."
	}
	configured, _ := platform.Settings["telegram_user_session_dir"].(string)
	configured = strings.TrimSpace(configured)
	if configured == "" {
		configured = filepath.Join(dataRoot, "telegram-sessions")
	} else if !filepath.IsAbs(configured) {
		configured = filepath.Join(dataRoot, configured)
	}
	configured = filepath.Clean(configured)
	root, err := filepath.Abs(dataRoot)
	if err != nil {
		return "", err
	}
	dir, err := filepath.Abs(configured)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("telegram_user_session_dir must stay under runtime data")
	}
	if err = os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create Telegram session directory: %w", err)
	}
	return filepath.Join(dir, platform.ID+".json"), nil
}

func (c *telegramUserConnector) ID() string   { return c.platform.ID }
func (c *telegramUserConnector) Type() string { return telegramUserTransport }

func (c *telegramUserConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.runCtx = ctx
	c.state.setStatus("connecting")
	c.wg.Add(1)
	go c.run(ctx)
	return nil
}

func (c *telegramUserConnector) run(ctx context.Context) {
	defer c.wg.Done()
	client, err := telegram.NewClient(telegram.ClientConfig{
		AppID:       int32(c.appID),
		AppHash:     c.appHash,
		Session:     c.session,
		SessionName: c.ID(),
		LogLevel:    telegram.LogWarn,
		DeviceConfig: telegram.DeviceConfig{
			DeviceModel:    stringSetting(c.platform.Settings, "telegram_user_device_model", "ErDai Agent"),
			SystemVersion:  stringSetting(c.platform.Settings, "telegram_user_system_version", "linux"),
			AppVersion:     stringSetting(c.platform.Settings, "telegram_user_app_version", "0.9.3"),
			SystemLangCode: stringSetting(c.platform.Settings, "telegram_user_lang_code", "zh-hans"),
			LangCode:       stringSetting(c.platform.Settings, "telegram_user_lang_code", "zh-hans"),
		},
	})
	if err != nil {
		c.state.setError(err)
		c.readyOnce.Do(func() { close(c.ready) })
		return
	}
	client.On(telegram.OnMessage, func(message *telegram.NewMessage) error {
		return c.handleMessage(ctx, message)
	})
	c.clientMu.Lock()
	c.client = client
	c.clientMu.Unlock()
	if err = client.Connect(); err != nil {
		c.state.setError(err)
		c.readyOnce.Do(func() { close(c.ready) })
		c.clientMu.Lock()
		c.client = nil
		c.clientMu.Unlock()
		return
	}
	c.readyOnce.Do(func() { close(c.ready) })
	authorized, authErr := client.IsAuthorized()
	if authErr != nil {
		c.state.setError(authErr)
	} else if authorized {
		c.setAuthorizedFromClient(client)
	} else {
		c.setAuthStep("auth_required")
	}
	<-ctx.Done()
	_ = client.Disconnect()
	c.clientMu.Lock()
	c.client = nil
	c.clientMu.Unlock()
}

func (c *telegramUserConnector) Close() error {
	c.authMu.Lock()
	if c.authFlowCancel != nil {
		c.authFlowCancel()
	}
	c.authMu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.state.setStatus("stopped")
	return nil
}

func (c *telegramUserConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *telegramUserConnector) waitReady(ctx context.Context) error {
	select {
	case <-c.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *telegramUserConnector) currentClient() (*telegram.Client, error) {
	c.clientMu.RLock()
	client := c.client
	c.clientMu.RUnlock()
	if client == nil {
		return nil, errors.New("Telegram MTProto client is not connected")
	}
	return client, nil
}

func (c *telegramUserConnector) setAuthStep(step string) {
	c.authMu.Lock()
	c.authStep = step
	c.authMu.Unlock()
	c.state.setStatus(step)
	c.state.setDetails(map[string]any{"authStep": step, "authorized": step == "connected"})
}

func (c *telegramUserConnector) setAuthorized(user *telegram.UserObj) {
	c.authMu.Lock()
	c.authStep = "connected"
	c.authRunning = false
	c.accountID, c.username, c.fullName = 0, "", ""
	if user != nil {
		c.accountID = user.ID
		c.username = user.Username
		c.fullName = strings.TrimSpace(strings.Join([]string{user.FirstName, user.LastName}, " "))
	}
	c.authMu.Unlock()
	c.state.setStatus("connected")
	c.state.setDetails(map[string]any{
		"authStep": "connected", "authorized": true,
		"accountId": c.accountID, "username": c.username, "displayName": c.fullName,
	})
}

func (c *telegramUserConnector) authStatus() map[string]any {
	c.authMu.Lock()
	defer c.authMu.Unlock()
	return map[string]any{
		"platformId": c.ID(), "transport": telegramUserTransport,
		"status": c.state.snapshot().Status, "authStep": c.authStep,
		"authorized": c.authStep == "connected", "accountId": c.accountID,
		"username": c.username, "displayName": c.fullName,
		"sessionFileConfigured": c.session != "",
	}
}

func (a *AgentRuntime) handleTelegramUserAuth(w http.ResponseWriter, r *http.Request, path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/platforms/"), "/")
	if len(parts) != 4 || parts[1] != telegramUserTransport || parts[2] != "auth" || parts[0] == "" {
		return mgmtNotFound("Telegram user auth route")
	}
	connector, ok := a.platformManager.TelegramUser(parts[0])
	if !ok {
		return mgmtNotFound("Telegram user connector")
	}
	switch parts[3] {
	case "status":
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		mgmtWriteData(w, http.StatusOK, connector.authStatus())
		return nil
	case "start":
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		var body struct {
			Phone string `json:"phone"`
		}
		if _, err := decodeCoreObject(r, coreFieldSet("phone"), "Telegram login", &body); err != nil {
			return err
		}
		value, err := connector.beginAuth(r.Context(), body.Phone)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	case "code":
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		var body struct {
			Code string `json:"code"`
		}
		if _, err := decodeCoreObject(r, coreFieldSet("code"), "Telegram login code", &body); err != nil {
			return err
		}
		value, err := connector.submitCode(r.Context(), body.Code)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	case "password":
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		var body struct {
			Password string `json:"password"`
		}
		if _, err := decodeCoreObject(r, coreFieldSet("password"), "Telegram 2FA password", &body); err != nil {
			return err
		}
		value, err := connector.submitPassword(r.Context(), body.Password)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	default:
		return mgmtNotFound("Telegram user auth action")
	}
}

func (c *telegramUserConnector) beginAuth(ctx context.Context, phone string) (map[string]any, error) {
	phone = strings.TrimSpace(phone)
	if phone == "" {
		phone, _ = c.platform.Settings["telegram_user_phone"].(string)
		phone = strings.TrimSpace(phone)
	}
	if len(phone) < 3 || len(phone) > 32 {
		return nil, errors.New("Telegram phone is required")
	}
	if err := c.waitReady(ctx); err != nil {
		return nil, err
	}
	client, err := c.currentClient()
	if err != nil {
		return nil, err
	}
	if authorized, checkErr := client.IsAuthorized(); checkErr == nil && authorized {
		c.setAuthorizedFromClient(client)
		return c.authStatus(), nil
	}
	c.authMu.Lock()
	if c.authStep == "connected" {
		c.authMu.Unlock()
		return c.authStatus(), nil
	}
	if c.authRunning {
		c.authMu.Unlock()
		return c.authStatus(), nil
	}
	flowParent := c.runCtx
	if flowParent == nil {
		flowParent = context.Background()
	}
	flowCtx, cancel := context.WithCancel(flowParent)
	c.phone = phone
	c.authStep = "auth_starting"
	c.authRunning = true
	c.authFlowCancel = cancel
	c.codeInput = make(chan string, 1)
	c.passwordInput = make(chan string, 1)
	c.authMu.Unlock()
	c.setAuthStep("auth_starting")
	c.wg.Add(1)
	go c.runAuthFlow(flowCtx, client, phone)
	return c.authStatus(), nil
}

func (c *telegramUserConnector) runAuthFlow(ctx context.Context, client *telegram.Client, phone string) {
	defer c.wg.Done()
	_, err := client.Login(phone, &telegram.LoginOptions{
		Ctx:        ctx,
		MaxRetries: 3,
		CodeCallback: func() (string, error) {
			c.setAuthStep("code_required")
			return c.waitAuthInput(ctx, true)
		},
		PasswordCallback: func() (string, error) {
			c.setAuthStep("password_required")
			return c.waitAuthInput(ctx, false)
		},
	})
	c.authMu.Lock()
	c.authRunning = false
	c.authFlowCancel = nil
	c.authMu.Unlock()
	if err != nil {
		if ctx.Err() == nil {
			c.setAuthStep("auth_error")
			c.state.setError(err)
		}
		return
	}
	c.setAuthorizedFromClient(client)
}

func (c *telegramUserConnector) waitAuthInput(ctx context.Context, code bool) (string, error) {
	c.authMu.Lock()
	input := c.passwordInput
	if code {
		input = c.codeInput
	}
	c.authMu.Unlock()
	select {
	case value := <-input:
		return value, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (c *telegramUserConnector) submitCode(ctx context.Context, code string) (map[string]any, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("Telegram code is required")
	}
	return c.submitAuthInput(ctx, "code_required", code, true)
}

func (c *telegramUserConnector) submitPassword(ctx context.Context, password string) (map[string]any, error) {
	password = strings.TrimSpace(password)
	if password == "" {
		return nil, errors.New("Telegram 2FA password is required")
	}
	return c.submitAuthInput(ctx, "password_required", password, false)
}

func (c *telegramUserConnector) submitAuthInput(ctx context.Context, requiredStep, value string, code bool) (map[string]any, error) {
	c.authMu.Lock()
	if !c.authRunning || c.authStep != requiredStep {
		c.authMu.Unlock()
		return nil, fmt.Errorf("Telegram %s is not requested", strings.TrimSuffix(requiredStep, "_required"))
	}
	input := c.passwordInput
	if code {
		input = c.codeInput
	}
	c.authMu.Unlock()
	select {
	case input <- value:
		return c.authStatus(), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("Telegram authentication input timed out")
	}
}

func (c *telegramUserConnector) setAuthorizedFromClient(client *telegram.Client) {
	authorized, err := client.IsAuthorized()
	if err != nil || !authorized {
		c.setAuthStep("auth_required")
		return
	}
	user, err := client.GetMe()
	if err != nil {
		c.state.setError(err)
		return
	}
	c.setAuthorized(user)
}

func (c *telegramUserConnector) handleMessage(ctx context.Context, message *telegram.NewMessage) error {
	if message == nil || message.Message == nil || message.IsOutgoing() || message.ID == 0 {
		return nil
	}
	targetID, targetType, kind, ok := telegramUserPeer(message)
	if !ok || !c.allowedChat(targetID) {
		return nil
	}
	senderID, senderName := telegramUserSender(message)
	if senderID == "" {
		return nil
	}
	text := strings.TrimSpace(message.MessageText())
	attachments := c.telegramInboundAttachments(ctx, message, targetID, senderID)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	if text == "" {
		return nil
	}
	command := nativePlatformIsCommand(text)
	isGroup := kind == "group"
	if isGroup && !boolSetting(c.platform.Settings, "telegram_user_receive_groups", true) {
		return nil
	}
	if !isGroup && !boolSetting(c.platform.Settings, "telegram_user_receive_private", true) {
		return nil
	}
	messageID := targetID + ":" + strconv.Itoa(int(message.ID))
	conversationID := targetID
	isWake := !isGroup || message.Message.Mentioned || command
	if isGroup && !isWake && !boolSetting(c.platform.Settings, "telegram_user_proactive_enabled", false) {
		return nil
	}
	isAdmin := nativePlatformIsAdmin(c.adminIDs, senderID) || nativePlatformIsAdmin(c.adminIDs, strings.TrimPrefix(senderID, "user:"))
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: telegramUserTransport, MessageID: messageID,
		RouteKind: kind, TargetID: targetID, TargetType: targetType,
		ConversationID: conversationID, ConversationKind: kind, SenderID: senderID,
		SenderName: senderName, Text: text, Attachments: attachments, OccurredAt: time.Unix(int64(message.Date()), 0).UTC(),
		ReplyToMessageID: telegramUserReplyID(message),
		IsWake:           isWake, IsAdmin: isAdmin, IsMentionBot: message.Message.Mentioned, IsCommand: command,
	}); err != nil {
		return err
	}
	c.state.markEvent()
	return nil
}

func (c *telegramUserConnector) allowedChat(targetID string) bool {
	value, _ := c.platform.Settings["telegram_user_allow_chats"].(string)
	value = strings.TrimSpace(value)
	if value == "" {
		return true
	}
	for _, item := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ';' || r == '\n' || r == '\r' || r == ' ' }) {
		if strings.TrimSpace(item) == targetID || strings.TrimPrefix(targetID, "user:") == strings.TrimSpace(item) {
			return true
		}
	}
	return false
}

func telegramUserPeer(message *telegram.NewMessage) (string, string, string, bool) {
	if message == nil || message.Message == nil || message.Message.PeerID == nil {
		return "", "", "", false
	}
	switch peer := message.Message.PeerID.(type) {
	case *telegram.PeerUser:
		return "user:" + strconv.FormatInt(peer.UserID, 10), "user", "private", true
	case *telegram.PeerChat:
		return "chat:" + strconv.FormatInt(peer.ChatID, 10), "chat", "group", true
	case *telegram.PeerChannel:
		return "channel:" + strconv.FormatInt(peer.ChannelID, 10), "channel", "group", true
	default:
		return "", "", "", false
	}
}

func telegramUserSender(message *telegram.NewMessage) (string, string) {
	if message != nil && message.Sender != nil {
		id := message.Sender.ID
		name := strings.TrimSpace(strings.Join([]string{message.Sender.FirstName, message.Sender.LastName}, " "))
		return "user:" + strconv.FormatInt(id, 10), firstNonEmpty(name, message.Sender.Username, strconv.FormatInt(id, 10))
	}
	return "", ""
}

func telegramUserReplyID(message *telegram.NewMessage) string {
	if message == nil || !message.IsReply() {
		return ""
	}
	if id := message.ReplyToMsgID(); id > 0 {
		return strconv.Itoa(int(id))
	}
	return ""
}

func (c *telegramUserConnector) telegramInboundAttachments(ctx context.Context, message *telegram.NewMessage, targetID, senderID string) []transportAttachment {
	if message == nil || !message.IsMedia() || !boolSetting(c.platform.Settings, "telegram_user_download_media", true) {
		return nil
	}
	mediaType := strings.ToLower(message.MediaType())
	kind := map[string]string{"photo": "image", "document": "file"}[mediaType]
	if kind == "" {
		return nil
	}
	fileName, fileExt := "", ""
	if message.File != nil {
		fileName, fileExt = message.File.Name, message.File.Ext
	}
	name := firstNonEmpty(fileName, "telegram-"+strconv.Itoa(int(message.ID)))
	if ext := strings.TrimSpace(fileExt); ext != "" && filepath.Ext(name) == "" {
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		name += ext
	}
	root := filepath.Join(filepath.Dir(c.session), "media", c.ID())
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil
	}
	path := filepath.Join(root, c.runtime.platformPseudonym("telegram-media", targetID+":"+strconv.Itoa(int(message.ID)))+filepath.Ext(name))
	downloadCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	if _, err := message.Download(&telegram.DownloadOptions{FileName: path, Ctx: downloadCtx, Resume: true}); err != nil {
		return nil
	}
	mimeType := ""
	if kind == "image" && filepath.Ext(name) != "" {
		mimeType = "image/" + strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")
	}
	return []transportAttachment{{Kind: kind, Name: name, MimeType: mimeType, LocalPath: path, SenderRef: senderID, MessageID: strconv.Itoa(int(message.ID))}}
}

func (c *telegramUserConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	client, err := c.currentClient()
	if err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "telegram_user_not_connected", Cause: err}
	}
	peer, err := telegramUserRoutePeer(route)
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "telegram_user_peer_unavailable", Cause: err}
	}
	for _, text := range splitTelegramText(strings.TrimSpace(delivery.Message.Text), 4096) {
		opts := &telegram.SendOptions{}
		if id := telegramUserRouteMessageID(route.MessageID); id > 0 {
			opts.ReplyID = int32(id)
		}
		if _, err = client.SendMessage(peer, text, opts); err != nil {
			return &platformDeliveryError{Retryable: true, Reason: "telegram_user_text_send_failed", Cause: err}
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		data, cleanPath, readErr := readNativeMedia(attachment)
		if readErr != nil {
			return &platformDeliveryError{Retryable: false, Reason: "telegram_user_attachment_invalid", Cause: readErr}
		}
		name := firstNonEmpty(attachment.Name, filepath.Base(cleanPath), "attachment")
		source := any(data)
		if cleanPath != "" {
			source = cleanPath
		}
		file, uploadErr := client.UploadFile(source, &telegram.UploadOptions{FileName: name})
		if uploadErr != nil {
			return &platformDeliveryError{Retryable: true, Reason: "telegram_user_attachment_upload_failed", Cause: uploadErr}
		}
		mediaOpts := &telegram.MediaOptions{FileName: name, ForceDocument: strings.ToLower(attachment.Kind) != "image"}
		if id := telegramUserRouteMessageID(route.MessageID); id > 0 {
			mediaOpts.ReplyID = int32(id)
		}
		if _, uploadErr = client.SendMedia(peer, file, mediaOpts); uploadErr != nil {
			return &platformDeliveryError{Retryable: true, Reason: "telegram_user_attachment_send_failed", Cause: uploadErr}
		}
	}
	c.state.markDelivery()
	return nil
}

func telegramUserRoutePeer(route platformReplyRoute) (int64, error) {
	targetType := strings.TrimSpace(route.TargetType)
	targetID := strings.TrimSpace(route.TargetID)
	parts := strings.SplitN(targetID, ":", 2)
	if len(parts) == 2 {
		if targetType == "" {
			targetType = parts[0]
		}
		targetID = parts[1]
	}
	id, err := strconv.ParseInt(targetID, 10, 64)
	if err != nil || id == 0 {
		return 0, errors.New("invalid Telegram peer id")
	}
	switch targetType {
	case "user":
		return id, nil
	case "chat":
		return -id, nil
	case "channel":
		return -1000000000000 - id, nil
	default:
		return 0, errors.New("unknown Telegram peer type")
	}
}

func telegramUserRouteMessageID(value string) int {
	parts := strings.Split(value, ":")
	if len(parts) == 0 {
		return 0
	}
	result, _ := strconv.Atoi(parts[len(parts)-1])
	return result
}
