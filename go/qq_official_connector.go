package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/tencent-connect/botgo"
	"github.com/tencent-connect/botgo/dto"
	dtomessage "github.com/tencent-connect/botgo/dto/message"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/openapi/options"
	"github.com/tencent-connect/botgo/token"
	botgowebsocket "github.com/tencent-connect/botgo/websocket"
	"github.com/tencent-connect/botgo/sessions/local"
	"golang.org/x/oauth2"
)

const qqOfficialTransport = "qq_official"

const (
	qqOfficialInboundDedupeTTL   = 10 * time.Minute
	qqOfficialInboundDedupeLimit = 4096
	qqOfficialGatewayStaleAfter  = 3 * time.Minute
	qqOfficialGatewayWatchEvery  = 30 * time.Second
	qqOfficialReconnectCooldown  = time.Minute
	qqOfficialOutboundReplyTTL   = 30 * time.Minute
	qqOfficialOutboundReplyLimit = 1024
)

// botgo v0.2.1 does not expose a typed handler for this gateway event.
// AstrBot 4.26.8 handled it through a custom parser so ordinary group
// messages could still reach the group participation policy.
const qqOfficialGroupMessageCreate dto.EventType = "GROUP_MESSAGE_CREATE"

type qqOfficialRawAuthor struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	MemberOpenID string `json:"member_openid"`
	UserOpenID   string `json:"user_openid"`
}

type qqOfficialRawMention struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	IsYou    bool   `json:"is_you"`
}

type qqOfficialRawElement struct {
	ID          string                   `json:"id"`
	MessageID   string                   `json:"message_id"`
	Content     string                   `json:"content"`
	Attachments []*dto.MessageAttachment `json:"attachments"`
}

type qqOfficialRawMessage struct {
	ID               string                   `json:"id"`
	Content          string                   `json:"content"`
	Timestamp        dto.Timestamp            `json:"timestamp"`
	GroupID          string                   `json:"group_id"`
	GroupOpenID      string                   `json:"group_openid"`
	ChannelID        string                   `json:"channel_id"`
	GuildID          string                   `json:"guild_id"`
	MentionEveryone  bool                     `json:"mention_everyone"`
	Author           qqOfficialRawAuthor      `json:"author"`
	Attachments      []*dto.MessageAttachment `json:"attachments"`
	Mentions         []qqOfficialRawMention   `json:"mentions"`
	MessageReference *dto.MessageReference    `json:"message_reference"`
	MessageType      json.RawMessage          `json:"message_type"`
	MessageElements  []qqOfficialRawElement   `json:"msg_elements"`
}

type qqOfficialInboundMessage struct {
	ID                string
	Content           string
	Timestamp         dto.Timestamp
	GroupID           string
	ChannelID         string
	GuildID           string
	AuthorID          string
	AuthorName        string
	MentionEveryone   bool
	MentionBot        bool
	MentionOthers     bool
	ReplyToBot        bool
	Attachments       []*dto.MessageAttachment
	QuotedMessageID   string
	QuotedContent     string
	QuotedAttachments []*dto.MessageAttachment
}

type qqOfficialAPI interface {
	WS(context.Context, map[string]string, string) (*dto.WebsocketAP, error)
	PostGroupMessage(context.Context, string, dto.APIMessage, ...options.Option) (*dto.Message, error)
	PostC2CMessage(context.Context, string, dto.APIMessage, ...options.Option) (*dto.Message, error)
	PostMessage(context.Context, string, *dto.MessageToCreate, ...options.Option) (*dto.Message, error)
	PostDirectMessage(context.Context, *dto.DirectMessage, *dto.MessageToCreate, ...options.Option) (*dto.Message, error)
}

type qqOfficialConnector struct {
	runtime           *AgentRuntime
	platform          mgmtPlatform
	appID             string
	secret            string
	groupC2C          bool
	guildDM           bool
	adminIDs          map[string]struct{}
	api               qqOfficialAPI
	token             oauth2.TokenSource
	transport         string
	cancel            context.CancelFunc
	dedupeMu          sync.Mutex
	dedupe            map[string]time.Time
	outboundMu        sync.Mutex
	outboundMessages  map[string]time.Time
	healthMu          sync.RWMutex
	health            platformConnectorHealth
	botID             string
	botUsername       string
	botDisplayName    string
	gatewayActivityAt time.Time
	lastReconnectAt   time.Time
	requestReconnect  func() error
	eventContext      context.Context
	tokenKey          uintptr
}

var qqOfficialRegistry = struct {
	sync.RWMutex
	byToken map[uintptr]*qqOfficialConnector
}{byToken: map[uintptr]*qqOfficialConnector{}}

func qqTokenSourceKey(source oauth2.TokenSource) uintptr {
	if source == nil {
		return 0
	}
	value := reflect.ValueOf(source)
	if value.Kind() == reflect.Pointer {
		return value.Pointer()
	}
	return 0
}

func registerQQOfficialConnector(connector *qqOfficialConnector) {
	if connector == nil || connector.tokenKey == 0 {
		return
	}
	qqOfficialRegistry.Lock()
	qqOfficialRegistry.byToken[connector.tokenKey] = connector
	qqOfficialRegistry.Unlock()
}

func unregisterQQOfficialConnector(connector *qqOfficialConnector) {
	if connector == nil || connector.tokenKey == 0 {
		return
	}
	qqOfficialRegistry.Lock()
	if current := qqOfficialRegistry.byToken[connector.tokenKey]; current == connector {
		delete(qqOfficialRegistry.byToken, connector.tokenKey)
	}
	qqOfficialRegistry.Unlock()
}

func qqConnectorForPayload(payload *dto.WSPayload, fallback *qqOfficialConnector) *qqOfficialConnector {
	if payload == nil || payload.Session == nil {
		return fallback
	}
	key := qqTokenSourceKey(payload.Session.TokenSource)
	qqOfficialRegistry.RLock()
	connector := qqOfficialRegistry.byToken[key]
	qqOfficialRegistry.RUnlock()
	return connector
}

func (c *qqOfficialConnector) contextForEvents(fallback context.Context) context.Context {
	if c != nil && c.eventContext != nil {
		return c.eventContext
	}
	return fallback
}

func newQQOfficialConnector(runtime *AgentRuntime, platform mgmtPlatform) (*qqOfficialConnector, error) {
	appID, _ := platform.Settings["appid"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, platformConnectorStartupError(platform, "appid")
	}
	secret := resolvePlatformCredential(platform, "secret")
	if secret == "" {
		return nil, platformConnectorStartupError(platform, "secret")
	}
	groupC2C := true
	if configured, ok := platform.Settings["enable_group_c2c"].(bool); ok {
		groupC2C = configured
	}
	guildDM := true
	if configured, ok := platform.Settings["enable_guild_direct_message"].(bool); ok {
		guildDM = configured
	}
	adminIDs := map[string]struct{}{}
	adminRaw, _ := platform.Settings["admin_openids"].(string)
	if adminRaw == "" {
		adminRaw = os.Getenv("ERDAI_QQ_ADMIN_OPENIDS")
	}
	for _, value := range strings.FieldsFunc(adminRaw, func(value rune) bool {
		return value == ',' || value == ';' || value == '\n' || value == '\r'
	}) {
		if value = strings.TrimSpace(value); value != "" {
			adminIDs[value] = struct{}{}
		}
	}
	return &qqOfficialConnector{
		runtime: runtime, platform: platform, appID: appID, secret: secret,
		groupC2C: groupC2C, guildDM: guildDM, adminIDs: adminIDs,
		transport:        firstNonEmpty(platform.Type, qqOfficialTransport),
		dedupe:           map[string]time.Time{},
		outboundMessages: map[string]time.Time{},
		health:           platformConnectorHealth{ID: platform.ID, Type: platform.Type, Status: "starting"},
		botDisplayName:   strings.TrimSpace(platform.DisplayName),
		requestReconnect: signalQQGatewayReconnect,
	}, nil
}

func (c *qqOfficialConnector) ID() string   { return c.platform.ID }
func (c *qqOfficialConnector) Type() string { return firstNonEmpty(c.transport, qqOfficialTransport) }

func (c *qqOfficialConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.eventContext = ctx
	if err := c.initializeAPI(ctx); err != nil {
		cancel()
		c.setError(err)
		return err
	}
	registerQQOfficialConnector(c)
	botgowebsocket.RegisterResumeSignal(syscall.SIGHUP)
	intents := event.RegisterHandlers(c.handlers(ctx)...)
	c.markRequestedIntents(intents)
	c.registerRawMessageParsers(ctx)
	accessPoint, err := c.api.WS(ctx, nil, "")
	if err != nil {
		cancel()
		c.setError(err)
		return err
	}
	c.setStatus("connecting")
	go func() {
		if startErr := local.New().Start(accessPoint, c.token, &intents); startErr != nil && ctx.Err() == nil {
			c.setError(startErr)
		}
	}()
	go c.watchGateway(ctx)
	return nil
}

func (c *qqOfficialConnector) initializeAPI(ctx context.Context) error {
	if c.api != nil {
		return nil
	}
	botgo.SetLogger(qqSDKHealthLogger{
		onReceive: c.markGatewayActivity, onHandlerError: c.markInboundError,
	})
	credentials := &token.QQBotCredentials{AppID: c.appID, AppSecret: c.secret}
	tokenSource := token.NewQQBotTokenSource(credentials)
	if err := token.StartRefreshAccessToken(ctx, tokenSource); err != nil {
		return err
	}
	c.api = botgo.NewOpenAPI(c.appID, tokenSource).WithTimeout(15 * time.Second)
	c.token = tokenSource
	c.tokenKey = qqTokenSourceKey(tokenSource)
	return nil
}

func (c *qqOfficialConnector) registerRawMessageParsers(ctx context.Context) {
	for eventType, kind := range map[dto.EventType]string{
		dto.EventGroupAtMessageCreate: "group",
		dto.EventC2CMessageCreate:     "c2c",
		dto.EventAtMessageCreate:      "channel",
		dto.EventDirectMessageCreate:  "guild_dm",
	} {
		eventType, kind := eventType, kind
		event.RegisterHandler(dto.WSDispatchEvent, eventType, func(payload *dto.WSPayload, raw []byte) error {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return nil
			}
			return target.handleRawInbound(target.contextForEvents(ctx), payload, raw, kind)
		})
	}
}

func (c *qqOfficialConnector) handlers(ctx context.Context) []interface{} {
	handlers := []interface{}{
		event.ReadyHandler(func(payload *dto.WSPayload, data *dto.WSReadyData) {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return
			}
			target.markGatewayActivity("Ready")
			target.markReady(data)
		}),
		event.ErrorNotifyHandler(func(err error) {
			c.setReconnecting(err)
		}),
		event.PlainEventHandler(func(payload *dto.WSPayload, message []byte) error {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return nil
			}
			return target.handlePlainInbound(target.contextForEvents(ctx), payload, message)
		}),
		event.GroupATMessageEventHandler(func(payload *dto.WSPayload, data *dto.WSGroupATMessageData) error {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return nil
			}
			return target.handleInbound(target.contextForEvents(ctx), payload, dto.Message(*data), "group")
		}),
		event.C2CMessageEventHandler(func(payload *dto.WSPayload, data *dto.WSC2CMessageData) error {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return nil
			}
			return target.handleInbound(target.contextForEvents(ctx), payload, dto.Message(*data), "c2c")
		}),
	}
	handlers = append(handlers,
		event.ATMessageEventHandler(func(payload *dto.WSPayload, data *dto.WSATMessageData) error {
			target := qqConnectorForPayload(payload, c)
			if target == nil {
				return nil
			}
			return target.handleInbound(target.contextForEvents(ctx), payload, dto.Message(*data), "channel")
		}),
	)
	if c.guildDM {
		handlers = append(handlers,
			event.DirectMessageEventHandler(func(payload *dto.WSPayload, data *dto.WSDirectMessageData) error {
				target := qqConnectorForPayload(payload, c)
				if target == nil {
					return nil
				}
				return target.handleInbound(target.contextForEvents(ctx), payload, dto.Message(*data), "guild_dm")
			}),
		)
	}
	return handlers
}

func (c *qqOfficialConnector) handlePlainInbound(ctx context.Context, payload *dto.WSPayload, raw []byte) error {
	if payload == nil || payload.Type != qqOfficialGroupMessageCreate {
		return nil
	}
	return c.handleRawInbound(ctx, payload, raw, "group_plain")
}

func (c *qqOfficialConnector) handleRawInbound(ctx context.Context, payload *dto.WSPayload, raw []byte, kind string) error {
	c.markRawInbound(kind)
	message, err := parseQQOfficialRawMessage(raw)
	if err != nil {
		c.markInboundError(err)
		return err
	}
	inbound := normalizeQQOfficialRawMessage(message, kind)
	c.recognizeRawBotMention(&inbound, message)
	c.recognizeReplyToBot(&inbound)
	if err = c.handleNormalizedInbound(ctx, payload, inbound, kind); err != nil {
		c.markInboundError(err)
		return err
	}
	return nil
}

func parseQQOfficialRawMessage(raw []byte) (qqOfficialRawMessage, error) {
	var envelope struct {
		Data json.RawMessage `json:"d"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return qqOfficialRawMessage{}, fmt.Errorf("decode QQ event envelope: %w", err)
	}
	if len(envelope.Data) == 0 || string(envelope.Data) == "null" {
		return qqOfficialRawMessage{}, errors.New("QQ event data is missing")
	}

	// Decode fields independently. QQ has changed optional group-message shapes
	// across API revisions; one unfamiliar mention or quote must not drop the
	// entire addressed message.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(envelope.Data, &fields); err != nil {
		return qqOfficialRawMessage{}, fmt.Errorf("decode QQ event data: %w", err)
	}
	message := qqOfficialRawMessage{MessageType: fields["message_type"]}
	decodeQQRawField(fields, "id", &message.ID)
	decodeQQRawField(fields, "content", &message.Content)
	decodeQQRawField(fields, "timestamp", &message.Timestamp)
	decodeQQRawField(fields, "group_id", &message.GroupID)
	decodeQQRawField(fields, "group_openid", &message.GroupOpenID)
	decodeQQRawField(fields, "channel_id", &message.ChannelID)
	decodeQQRawField(fields, "guild_id", &message.GuildID)
	decodeQQRawField(fields, "mention_everyone", &message.MentionEveryone)
	decodeQQRawField(fields, "author", &message.Author)
	decodeQQRawField(fields, "attachments", &message.Attachments)
	decodeQQRawField(fields, "mentions", &message.Mentions)
	decodeQQRawField(fields, "message_reference", &message.MessageReference)
	decodeQQRawField(fields, "msg_elements", &message.MessageElements)
	return message, nil
}

func decodeQQRawField(fields map[string]json.RawMessage, name string, target any) {
	value := fields[name]
	if len(value) == 0 || string(value) == "null" {
		return
	}
	_ = json.Unmarshal(value, target)
}

func (c *qqOfficialConnector) Close() error {
	unregisterQQOfficialConnector(c)
	if c.cancel != nil {
		c.cancel()
	}
	c.setStatus("stopped")
	return nil
}

func (c *qqOfficialConnector) Health() platformConnectorHealth {
	c.healthMu.RLock()
	defer c.healthMu.RUnlock()
	return c.health
}

func (c *qqOfficialConnector) setStatus(status string) {
	c.healthMu.Lock()
	c.health.Status = status
	if status != "error" {
		c.health.LastError = ""
	}
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) setError(err error) {
	c.healthMu.Lock()
	c.health.Status = "error"
	c.health.LastError = redactConnectorError(err)
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) setReconnecting(err error) {
	c.healthMu.Lock()
	c.health.Status = "reconnecting"
	c.health.LastError = redactConnectorError(err)
	c.lastReconnectAt = time.Now().UTC()
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markGatewayActivity(opcode string) {
	now := time.Now().UTC()
	formatted := now.Format(time.RFC3339Nano)
	c.healthMu.Lock()
	c.gatewayActivityAt = now
	c.lastReconnectAt = time.Time{}
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	c.health.Details["lastGatewayAt"] = formatted
	c.health.Details["lastGatewayOpcode"] = opcode
	if opcode == "HeartbeatAck" && c.health.Status == "reconnecting" {
		c.health.Status = "connected"
		c.health.LastError = ""
	}
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markRequestedIntents(intents dto.Intent) {
	c.healthMu.Lock()
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	c.health.Details["requestedIntents"] = int(intents)
	c.health.Details["groupMessagesIntent"] = intents&dto.IntentGroupMessages != 0
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markRawInbound(kind string) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	c.healthMu.Lock()
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	count, _ := c.health.Details["rawInboundCount"].(int64)
	c.health.Details["rawInboundCount"] = count + 1
	c.health.Details["lastRawInboundAt"] = now
	c.health.Details["lastRawInboundKind"] = kind
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markInboundError(err error) {
	if err == nil {
		return
	}
	c.healthMu.Lock()
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	c.health.Details["lastInboundErrorAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	c.health.Details["lastInboundError"] = redactConnectorError(err)
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markReady(data *dto.WSReadyData) {
	c.healthMu.Lock()
	if data != nil {
		c.botID = strings.TrimSpace(data.User.ID)
		c.botUsername = strings.TrimSpace(data.User.Username)
	}
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	c.health.Details["botIdentityReady"] = c.botID != "" || c.botUsername != ""
	c.health.Status = "connected"
	c.health.LastError = ""
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) gatewayNeedsReconnect(now time.Time) bool {
	c.healthMu.Lock()
	defer c.healthMu.Unlock()
	if (c.health.Status != "connected" && c.health.Status != "reconnecting") || c.gatewayActivityAt.IsZero() ||
		now.Sub(c.gatewayActivityAt) < qqOfficialGatewayStaleAfter {
		return false
	}
	if !c.lastReconnectAt.IsZero() && now.Sub(c.lastReconnectAt) < qqOfficialReconnectCooldown {
		return false
	}
	c.lastReconnectAt = now
	c.health.Status = "reconnecting"
	c.health.LastError = "QQ gateway heartbeat stale; reconnect requested"
	return true
}

func (c *qqOfficialConnector) watchGateway(ctx context.Context) {
	ticker := time.NewTicker(qqOfficialGatewayWatchEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if !c.gatewayNeedsReconnect(now.UTC()) {
				continue
			}
			if c.requestReconnect == nil {
				c.setError(errors.New("QQ gateway reconnect is unavailable"))
				continue
			}
			if err := c.requestReconnect(); err != nil {
				c.setError(fmt.Errorf("QQ gateway reconnect failed: %w", err))
			}
		}
	}
}

func signalQQGatewayReconnect() error {
	process, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGHUP)
}

func (c *qqOfficialConnector) markEvent(kind string, mentionBot bool) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	c.healthMu.Lock()
	c.health.LastEventAt = &now
	if c.health.Details == nil {
		c.health.Details = map[string]any{}
	}
	c.health.Details["lastInboundKind"] = kind
	c.health.Details["lastMentionBot"] = mentionBot
	c.health.Details["lastInboundError"] = ""
	if c.health.Status != "error" {
		c.health.Status = "connected"
	}
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) markDelivery() {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	c.healthMu.Lock()
	c.health.LastDeliveryAt = &now
	c.health.Status = "connected"
	c.health.LastError = ""
	c.healthMu.Unlock()
}

func (c *qqOfficialConnector) handleInbound(ctx context.Context, payload *dto.WSPayload, message dto.Message, kind string) error {
	inbound := normalizeQQOfficialDTOMessage(message)
	inbound.MentionBot = kind == "group" || kind == "channel"
	if len(message.Mentions) > 1 {
		inbound.MentionOthers = true
	}
	c.recognizeReplyToBot(&inbound)
	return c.handleNormalizedInbound(ctx, payload, inbound, kind)
}

func normalizeQQOfficialDTOMessage(message dto.Message) qqOfficialInboundMessage {
	inbound := qqOfficialInboundMessage{
		ID: message.ID, Content: message.Content, Timestamp: message.Timestamp,
		GroupID: message.GroupID, ChannelID: message.ChannelID, GuildID: message.GuildID,
		MentionEveryone: message.MentionEveryone, Attachments: message.Attachments,
	}
	if message.Author != nil {
		inbound.AuthorID, inbound.AuthorName = message.Author.ID, message.Author.Username
	}
	if message.MessageReference != nil {
		inbound.QuotedMessageID = message.MessageReference.MessageID
	}
	return inbound
}

func normalizeQQOfficialRawMessage(message qqOfficialRawMessage, kind string) qqOfficialInboundMessage {
	authorID := firstNonEmpty(message.Author.ID, message.Author.MemberOpenID, message.Author.UserOpenID)
	if kind == "group" || kind == "group_plain" {
		authorID = firstNonEmpty(message.Author.MemberOpenID, message.Author.ID, message.Author.UserOpenID)
	} else if kind == "c2c" {
		authorID = firstNonEmpty(message.Author.UserOpenID, message.Author.ID, message.Author.MemberOpenID)
	}
	inbound := qqOfficialInboundMessage{
		ID: message.ID, Content: message.Content, Timestamp: message.Timestamp,
		GroupID:   firstNonEmpty(message.GroupOpenID, message.GroupID),
		ChannelID: message.ChannelID, GuildID: message.GuildID,
		AuthorID: authorID, AuthorName: message.Author.Username,
		MentionEveryone: message.MentionEveryone, Attachments: message.Attachments,
		MentionBot: kind == "group" || kind == "channel",
	}
	if message.MessageReference != nil {
		inbound.QuotedMessageID = message.MessageReference.MessageID
	}
	for _, mention := range message.Mentions {
		if !mention.IsYou {
			continue
		}
		inbound.MentionBot = true
		inbound.Content = strings.ReplaceAll(inbound.Content, "<@"+mention.ID+">", "")
		inbound.Content = strings.ReplaceAll(inbound.Content, "<@!"+mention.ID+">", "")
	}
	if qqOfficialMessageType(message.MessageType) == 103 && len(message.MessageElements) > 0 {
		quoted := message.MessageElements[0]
		inbound.QuotedMessageID = firstNonEmpty(inbound.QuotedMessageID, quoted.ID, quoted.MessageID)
		inbound.QuotedContent = quoted.Content
		inbound.QuotedAttachments = quoted.Attachments
	}
	return inbound
}

func (c *qqOfficialConnector) recognizeRawBotMention(inbound *qqOfficialInboundMessage, message qqOfficialRawMessage) {
	if inbound == nil {
		return
	}
	c.healthMu.RLock()
	botID := c.botID
	botUsername := c.botUsername
	botDisplayName := c.botDisplayName
	c.healthMu.RUnlock()

	for _, mention := range message.Mentions {
		matched := mention.IsYou ||
			(botID != "" && strings.TrimSpace(mention.ID) == botID) ||
			(botUsername != "" && strings.EqualFold(strings.TrimSpace(mention.Username), botUsername))
		if !matched {
			inbound.MentionOthers = true
			continue
		}
		inbound.MentionBot = true
		inbound.Content = stripQQOfficialMention(inbound.Content, mention.ID, "")
	}
	if botID != "" && qqOfficialContentMentionsID(inbound.Content, botID) {
		inbound.MentionBot = true
		inbound.Content = stripQQOfficialMention(inbound.Content, botID, "")
	}
	for _, name := range []string{botUsername, botDisplayName} {
		if name == "" || !qqOfficialContentMentionsName(inbound.Content, name) {
			continue
		}
		inbound.MentionBot = true
		inbound.Content = stripQQOfficialMention(inbound.Content, "", name)
	}
	if strings.Contains(inbound.Content, "<@") {
		inbound.MentionOthers = true
	}
}

func (c *qqOfficialConnector) recognizeReplyToBot(inbound *qqOfficialInboundMessage) {
	if inbound == nil || strings.TrimSpace(inbound.QuotedMessageID) == "" {
		return
	}
	inbound.ReplyToBot = c.isOutboundMessage(inbound.QuotedMessageID)
}

func qqOfficialContentMentionsID(content, botID string) bool {
	return strings.Contains(content, "<@"+botID+">") || strings.Contains(content, "<@!"+botID+">")
}

func qqOfficialContentMentionsName(content, name string) bool {
	return strings.Contains(content, "@"+name) || strings.Contains(content, "＠"+name)
}

func stripQQOfficialMention(content, botID, name string) string {
	replacements := make([]string, 0, 8)
	if botID = strings.TrimSpace(botID); botID != "" {
		replacements = append(replacements, "<@"+botID+">", "", "<@!"+botID+">", "")
	}
	if name = strings.TrimSpace(name); name != "" {
		replacements = append(replacements, "@"+name, "", "＠"+name, "")
	}
	if len(replacements) == 0 {
		return content
	}
	return strings.TrimSpace(strings.NewReplacer(replacements...).Replace(content))
}

func qqOfficialMessageType(raw json.RawMessage) int {
	if len(raw) == 0 {
		return 0
	}
	var number int
	if json.Unmarshal(raw, &number) == nil {
		return number
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		number, _ = strconv.Atoi(strings.TrimSpace(text))
	}
	return number
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (c *qqOfficialConnector) handleNormalizedInbound(ctx context.Context, payload *dto.WSPayload, message qqOfficialInboundMessage, kind string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if message.ID == "" || message.AuthorID == "" {
		return errors.New("QQ message identity is incomplete")
	}
	plainGroup := kind == "group_plain"
	if (kind == "group" || plainGroup || kind == "c2c") && !c.groupC2C {
		return nil
	}
	dedupeKind := kind
	if plainGroup && message.MentionBot {
		dedupeKind = "group"
	}
	dedupeKey, duplicate := c.claimAddressedInbound(dedupeKind, message.ID)
	if duplicate {
		return nil
	}
	completed := false
	defer func() {
		if !completed {
			c.releaseAddressedInbound(dedupeKey)
		}
	}()
	targetID, conversationID, conversationKind := message.GroupID, message.GroupID, "group"
	replyKind := kind
	if plainGroup {
		replyKind = "group"
	}
	switch kind {
	case "c2c":
		targetID, conversationID, conversationKind = message.AuthorID, message.AuthorID, "private"
	case "channel":
		targetID, conversationID = message.ChannelID, message.ChannelID
	case "guild_dm":
		targetID, conversationID, conversationKind = message.GuildID, message.GuildID, "private"
	}
	if targetID == "" || conversationID == "" {
		return errors.New("QQ message target is empty")
	}
	occurredAt := time.Now().UTC()
	if parsed, parseErr := message.Timestamp.Time(); parseErr == nil {
		occurredAt = parsed.UTC()
	}
	text := strings.TrimSpace(dtomessage.ETLInput(message.Content))
	attachments := append(qqTransportAttachments(message.QuotedAttachments), qqTransportAttachments(message.Attachments)...)
	if text == "" && len(attachments) > 0 {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	if err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: c.Type(), MessageID: message.ID,
		RouteKind: replyKind, TargetID: targetID, GuildID: message.GuildID, ChannelID: message.ChannelID,
		ConversationID: conversationID, ConversationKind: conversationKind,
		SenderID: message.AuthorID, SenderName: message.AuthorName, Text: text,
		ReplyToMessageID: message.QuotedMessageID, ReplyToText: message.QuotedContent,
		Attachments: attachments, OccurredAt: occurredAt,
		IsWake:       !plainGroup || message.MentionBot || message.ReplyToBot,
		IsAdmin:      nativePlatformIsAdmin(c.adminIDs, message.AuthorID),
		IsMentionBot: message.MentionBot, IsAtOthers: message.MentionOthers,
		IsCommand: nativePlatformIsCommand(text), IsAtAll: message.MentionEveryone,
	}); err != nil {
		return err
	}
	c.markEvent(kind, message.MentionBot)
	completed = true
	_ = payload
	return nil
}

func (c *qqOfficialConnector) claimAddressedInbound(kind, messageID string) (string, bool) {
	if kind == "group_plain" || strings.TrimSpace(messageID) == "" {
		return "", false
	}
	key := c.platform.ID + ":" + kind + ":" + messageID
	now := time.Now()
	c.dedupeMu.Lock()
	defer c.dedupeMu.Unlock()
	if c.dedupe == nil {
		c.dedupe = map[string]time.Time{}
	}
	if expiresAt, found := c.dedupe[key]; found && now.Before(expiresAt) {
		return key, true
	}
	if len(c.dedupe) >= qqOfficialInboundDedupeLimit {
		oldestKey := ""
		var oldest time.Time
		for candidate, expiresAt := range c.dedupe {
			if !now.Before(expiresAt) {
				delete(c.dedupe, candidate)
				continue
			}
			if oldestKey == "" || expiresAt.Before(oldest) {
				oldestKey, oldest = candidate, expiresAt
			}
		}
		if len(c.dedupe) >= qqOfficialInboundDedupeLimit && oldestKey != "" {
			delete(c.dedupe, oldestKey)
		}
	}
	c.dedupe[key] = now.Add(qqOfficialInboundDedupeTTL)
	return key, false
}

func (c *qqOfficialConnector) releaseAddressedInbound(key string) {
	if key == "" {
		return
	}
	c.dedupeMu.Lock()
	delete(c.dedupe, key)
	c.dedupeMu.Unlock()
}

func (c *qqOfficialConnector) recordOutboundMessage(message *dto.Message) {
	if message == nil || strings.TrimSpace(message.ID) == "" {
		return
	}
	now := time.Now()
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()
	if c.outboundMessages == nil {
		c.outboundMessages = map[string]time.Time{}
	}
	for id, expiresAt := range c.outboundMessages {
		if !now.Before(expiresAt) {
			delete(c.outboundMessages, id)
		}
	}
	if len(c.outboundMessages) >= qqOfficialOutboundReplyLimit {
		oldestID := ""
		var oldest time.Time
		for id, expiresAt := range c.outboundMessages {
			if oldestID == "" || expiresAt.Before(oldest) {
				oldestID, oldest = id, expiresAt
			}
		}
		delete(c.outboundMessages, oldestID)
	}
	c.outboundMessages[strings.TrimSpace(message.ID)] = now.Add(qqOfficialOutboundReplyTTL)
}

func (c *qqOfficialConnector) isOutboundMessage(messageID string) bool {
	messageID = strings.TrimSpace(messageID)
	if messageID == "" {
		return false
	}
	now := time.Now()
	c.outboundMu.Lock()
	defer c.outboundMu.Unlock()
	expiresAt, found := c.outboundMessages[messageID]
	if found && !now.Before(expiresAt) {
		delete(c.outboundMessages, messageID)
		return false
	}
	return found
}

func qqMessageWithQuotedContext(current, quotedID, quoted string) string {
	current = strings.TrimSpace(current)
	quoted = strings.TrimSpace(dtomessage.ETLInput(quoted))
	if quoted == "" && strings.TrimSpace(quotedID) == "" {
		return current
	}
	if quoted == "" {
		quoted = "（引用内容不可用）"
	}
	if current == "" {
		return "[引用消息]\n" + quoted
	}
	return "[引用消息]\n" + quoted + "\n[当前消息]\n" + current
}

func qqAttachmentOnlyPrompt(attachments []transportAttachment) string {
	for _, attachment := range attachments {
		if attachment.Kind == "image" {
			return "请看我发的图片，结合当前对话回应。"
		}
	}
	return "请查看我发的附件，结合当前对话回应。"
}

func qqTransportAttachments(values []*dto.MessageAttachment) []transportAttachment {
	attachments := make([]transportAttachment, 0, min(len(values), 3))
	for _, value := range values {
		if value == nil || strings.TrimSpace(value.URL) == "" {
			continue
		}
		contentType := strings.ToLower(strings.TrimSpace(value.ContentType))
		kind := "file"
		switch {
		case strings.HasPrefix(contentType, "image"):
			kind = "image"
		case strings.HasPrefix(contentType, "video"):
			kind = "video"
		case strings.HasPrefix(contentType, "audio"), contentType == "voice":
			kind = "audio"
		default:
			kind = qqAttachmentKindFromName(firstNonEmpty(value.FileName, value.URL))
		}
		attachments = append(attachments, transportAttachment{
			ID: fmt.Sprintf("attachment-%d", len(attachments)+1), Kind: kind,
			SourceURL: normalizeQQAttachmentURL(value.URL), Name: value.FileName,
		})
		if len(attachments) == 3 {
			break
		}
	}
	return attachments
}

func normalizeQQAttachmentURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme == "" {
		return "https://" + strings.TrimLeft(raw, "/")
	}
	return raw
}

func qqAttachmentKindFromName(raw string) string {
	parsed, _ := url.Parse(strings.TrimSpace(raw))
	extension := strings.ToLower(path.Ext(parsed.Path))
	if extension == "" {
		extension = strings.ToLower(path.Ext(parsed.Host))
	}
	switch extension {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return "image"
	case ".mp4", ".mov", ".webm", ".mkv":
		return "video"
	case ".mp3", ".wav", ".ogg", ".amr", ".m4a", ".aac":
		return "audio"
	default:
		return "file"
	}
}

type qqRichMediaUpload struct {
	FileType   uint64 `json:"file_type"`
	FileData   string `json:"file_data"`
	FileName   string `json:"file_name,omitempty"`
	SrvSendMsg bool   `json:"srv_send_msg"`
}

func (qqRichMediaUpload) GetEventID() string        { return "" }
func (qqRichMediaUpload) GetSendType() dto.SendType { return dto.RichMedia }

func (c *qqOfficialConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if c.api == nil {
		return &platformDeliveryError{Retryable: true, Reason: "qq_not_connected"}
	}
	if text := strings.TrimSpace(delivery.Message.Text); text != "" {
		sequence, err := c.runtime.nextPlatformMessageSequence(ctx, delivery.ReplyHandle)
		if err != nil {
			return err
		}
		message := &dto.MessageToCreate{
			Content: text, MsgType: dto.TextMsg, MsgID: route.MessageID, MsgSeq: sequence,
		}
		if err = c.sendText(ctx, route, message); err != nil {
			return &platformDeliveryError{Retryable: true, Reason: "qq_text_send_failed", Cause: err}
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		if err := c.sendAttachment(ctx, route, delivery.ReplyHandle, attachment); err != nil {
			return err
		}
	}
	c.markDelivery()
	return nil
}

func (c *qqOfficialConnector) sendText(ctx context.Context, route platformReplyRoute, message *dto.MessageToCreate) error {
	var sent *dto.Message
	var err error
	switch route.Kind {
	case "group":
		sent, err = c.api.PostGroupMessage(ctx, route.TargetID, message)
	case "c2c":
		sent, err = c.api.PostC2CMessage(ctx, route.TargetID, message)
	case "channel":
		sent, err = c.api.PostMessage(ctx, route.ChannelID, message)
	case "guild_dm":
		sent, err = c.api.PostDirectMessage(ctx, &dto.DirectMessage{GuildID: route.GuildID, ChannelID: route.ChannelID}, message)
	default:
		return errors.New("unsupported QQ reply kind")
	}
	if err == nil {
		c.recordOutboundMessage(sent)
	}
	return err
}

func (c *qqOfficialConnector) sendAttachment(ctx context.Context, route platformReplyRoute, replyHandle string, attachment agentAttachment) error {
	if route.Kind != "group" && route.Kind != "c2c" {
		return &platformDeliveryError{Retryable: false, Reason: "qq_attachment_kind_unsupported"}
	}
	cleanPath := filepath.Clean(attachment.LocalPath)
	if !strings.HasPrefix(cleanPath, mediaMountRoot+string(os.PathSeparator)) {
		return &platformDeliveryError{Retryable: false, Reason: "qq_attachment_path_invalid"}
	}
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return &platformDeliveryError{Retryable: false, Reason: "qq_attachment_missing", Cause: err}
	}
	if len(data) == 0 || len(data) > maxImageBytes {
		return &platformDeliveryError{Retryable: false, Reason: "qq_attachment_size_invalid"}
	}
	fileType := uint64(4)
	switch strings.ToLower(attachment.Kind) {
	case "image":
		fileType = 1
	case "video":
		fileType = 2
	case "audio":
		fileType = 3
	}
	fileName := strings.TrimSpace(attachment.Name)
	if fileName == "" {
		fileName = filepath.Base(cleanPath)
	}
	upload := qqRichMediaUpload{FileType: fileType, FileData: base64.StdEncoding.EncodeToString(data), FileName: fileName}
	var uploaded *dto.Message
	if route.Kind == "group" {
		uploaded, err = c.api.PostGroupMessage(ctx, route.TargetID, upload)
	} else {
		uploaded, err = c.api.PostC2CMessage(ctx, route.TargetID, upload)
	}
	if err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "qq_attachment_upload_failed", Cause: err}
	}
	if uploaded == nil || len(uploaded.FileInfo) == 0 {
		return &platformDeliveryError{Retryable: true, Reason: "qq_attachment_upload_empty"}
	}
	sequence, err := c.runtime.nextPlatformMessageSequence(ctx, replyHandle)
	if err != nil {
		return err
	}
	message := &dto.MessageToCreate{
		MsgType: dto.RichMediaMsg, MsgID: route.MessageID, MsgSeq: sequence,
		Media: &dto.MediaInfo{FileInfo: uploaded.FileInfo},
	}
	if route.Kind == "group" {
		uploaded, err = c.api.PostGroupMessage(ctx, route.TargetID, message)
	} else {
		uploaded, err = c.api.PostC2CMessage(ctx, route.TargetID, message)
	}
	if err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "qq_attachment_send_failed", Cause: err}
	}
	c.recordOutboundMessage(uploaded)
	return nil
}
