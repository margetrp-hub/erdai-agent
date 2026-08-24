package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/channel"
	"github.com/larksuite/oapi-sdk-go/v3/channel/normalize"
	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

const larkTransport = "lark"

type larkConnector struct {
	runtime     *AgentRuntime
	platform    mgmtPlatform
	appID       string
	appSecret   string
	mode        string
	domain      string
	publicBase  string
	host        string
	port        int
	path        string
	webhookUUID string
	admins      map[string]struct{}
	channel     larktypes.Channel
	state       nativeConnectorState
	cancel      context.CancelFunc
	server      *platformWebhookServer
	mediaMu     sync.RWMutex
	media       map[string]larkMediaRecord
}

type larkMediaRecord struct {
	FileKey   string
	MediaType string
	FileName  string
	ExpiresAt time.Time
}

func newLarkConnector(runtime *AgentRuntime, platform mgmtPlatform) (*larkConnector, error) {
	appID, _ := platform.Settings["app_id"].(string)
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return nil, platformConnectorStartupError(platform, "app_id")
	}
	appSecret := resolvePlatformCredential(platform, "app_secret")
	if appSecret == "" {
		return nil, platformConnectorStartupError(platform, "app_secret")
	}
	mode, _ := platform.Settings["lark_connection_mode"].(string)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "socket"
	}
	if mode != "socket" && mode != "webhook" {
		return nil, errors.New("Lark connection mode must be socket or webhook")
	}
	domain, _ := platform.Settings["domain"].(string)
	if strings.TrimSpace(domain) == "" {
		domain = "https://open.feishu.cn"
	}
	host, _ := platform.Settings["lark_webhook_host"].(string)
	if strings.TrimSpace(host) == "" {
		host = "0.0.0.0"
	}
	path, _ := platform.Settings["lark_webhook_path"].(string)
	if strings.TrimSpace(path) == "" {
		path = "/erdai-lark-webhook/callback"
	}
	publicBase, _ := platform.Settings["lark_public_base_url"].(string)
	return &larkConnector{
		runtime: runtime, platform: platform, appID: appID, appSecret: appSecret, mode: mode,
		domain: strings.TrimRight(domain, "/"), publicBase: strings.TrimRight(publicBase, "/"),
		host: host, port: kookIntSetting(platform, "lark_webhook_port", 6201), path: path,
		webhookUUID: resolvePlatformCredential(platform, "webhook_uuid"),
		admins:      nativePlatformAdminIDs(platform), state: newNativeConnectorState(platform),
		media: map[string]larkMediaRecord{},
	}, nil
}

func (c *larkConnector) ID() string   { return c.platform.ID }
func (c *larkConnector) Type() string { return larkTransport }

func (c *larkConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	dispatch := dispatcher.NewEventDispatcher(
		resolvePlatformCredential(c.platform, "lark_verification_token"),
		resolvePlatformCredential(c.platform, "lark_encrypt_key"),
	).OnP2MessageReceiveV1(c.handleP2Message)
	client := lark.NewClient(c.appID, c.appSecret,
		lark.WithOpenBaseUrl(c.domain), lark.WithLogLevel(larkcore.LogLevelWarn))
	var wsClient *larkws.Client
	if c.mode == "socket" {
		wsClient = larkws.NewClient(c.appID, c.appSecret,
			larkws.WithDomain(c.domain), larkws.WithEventHandler(dispatch),
			larkws.WithLogLevel(larkcore.LogLevelWarn), larkws.WithAutoReconnect(true))
	}
	c.channel = channel.NewChannel(client, wsClient)
	c.channel.OnReady(func() { c.state.setStatus("connected") })
	c.channel.OnReconnecting(func() { c.state.setStatus("reconnecting") })
	c.channel.OnReconnected(func() { c.state.setStatus("connected") })
	c.channel.OnDisconnected(func() { c.state.setStatus("disconnected") })
	c.channel.OnError(func(err error) { c.state.setError(err) })
	if c.mode == "webhook" || c.publicBase != "" {
		mux := http.NewServeMux()
		if c.mode == "webhook" {
			mux.HandleFunc(c.webhookPath(), httpserverext.NewEventHandlerFunc(dispatch))
		}
		mux.HandleFunc("/lark-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.host, c.port, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	if c.mode == "webhook" {
		c.state.setStatus("connected")
		return nil
	}
	c.state.setStatus("connecting")
	go func() {
		if err := c.channel.Start(ctx); err != nil && ctx.Err() == nil {
			c.state.setError(err)
		}
	}()
	return nil
}

func (c *larkConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.channel != nil {
		_ = c.channel.Stop(context.Background())
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *larkConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *larkConnector) webhookPath() string {
	if c.webhookUUID != "" {
		return "/webhooks/" + url.PathEscape(c.webhookUUID)
	}
	return "/" + strings.Trim(c.path, "/")
}

func (c *larkConnector) handleP2Message(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	message := normalize.ParseMessage(event)
	if message == nil || message.MessageID == "" || message.ChatID == "" || message.UserID == "" {
		return nil
	}
	if c.channel != nil {
		if bot := c.channel.GetBotIdentity(ctx); bot != nil {
			for index := range message.Mentions {
				mention := &message.Mentions[index]
				if mention.OpenID == bot.OpenID || mention.UserID == bot.UserID || mention.UserID == bot.OpenID {
					mention.IsBot = true
					message.MentionedBot = true
					message.Content = strings.ReplaceAll(message.Content, mention.Key, "")
				}
			}
		}
	}
	attachments := c.registerResources(message.Resources)
	text := strings.TrimSpace(message.Content)
	if text == "" {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	private := message.ChatType == "p2p"
	conversationKind, routeKind := "group", "group"
	if private {
		conversationKind, routeKind = "private", "private"
	}
	occurred := time.Now().UTC()
	if message.CreateTimeMs > 0 {
		occurred = time.UnixMilli(message.CreateTimeMs).UTC()
	}
	err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: larkTransport, MessageID: message.MessageID,
		RouteKind: routeKind, TargetID: message.ChatID, ChannelID: message.ChatID,
		ConversationID: message.ChatID, ConversationKind: conversationKind,
		SenderID: message.UserID, SenderName: message.UserID, Text: text,
		Attachments: attachments, OccurredAt: occurred, IsWake: private || message.MentionedBot,
		IsAdmin: nativePlatformIsAdmin(c.admins, message.UserID), IsMentionBot: message.MentionedBot,
		IsCommand: nativePlatformIsCommand(text), IsAtAll: message.MentionAll,
	})
	if err == nil {
		c.state.markEvent()
	}
	return err
}

func (c *larkConnector) registerResources(resources []larktypes.Resource) []transportAttachment {
	if c.publicBase == "" {
		return nil
	}
	attachments := make([]transportAttachment, 0, len(resources))
	for _, resource := range resources {
		if resource.FileKey == "" {
			continue
		}
		kind := strings.ToLower(resource.Type)
		if kind == "sticker" {
			kind = "image"
		}
		if kind != "image" && kind != "audio" && kind != "video" && kind != "file" {
			kind = "file"
		}
		mediaID := c.runtime.platformPseudonym("lark-media", c.ID()+":"+resource.FileKey+":"+resource.Type)
		c.mediaMu.Lock()
		c.media[mediaID] = larkMediaRecord{
			FileKey: resource.FileKey, MediaType: resource.Type,
			FileName: resource.FileName, ExpiresAt: time.Now().Add(15 * time.Minute),
		}
		c.mediaMu.Unlock()
		attachments = append(attachments, transportAttachment{
			Kind: kind, SourceURL: c.publicBase + "/lark-media/" + url.PathEscape(mediaID), Name: resource.FileName,
		})
	}
	return attachments
}

func (c *larkConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || c.channel == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/lark-media/"))
	if err != nil || id == "" {
		http.NotFound(w, r)
		return
	}
	c.mediaMu.RLock()
	record, found := c.media[id]
	c.mediaMu.RUnlock()
	if !found || time.Now().After(record.ExpiresAt) {
		http.NotFound(w, r)
		return
	}
	data, err := c.channel.DownloadFile(r.Context(), record.FileKey, record.MediaType)
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	if record.FileName != "" {
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=%q", strings.ReplaceAll(record.FileName, `"`, "")))
	}
	w.Header().Set("Content-Type", http.DetectContentType(data))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(data)
}

func (c *larkConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if c.channel == nil || route.TargetID == "" {
		return &platformDeliveryError{Retryable: true, Reason: "lark_not_connected"}
	}
	send := func(input *larktypes.SendInput) error {
		input.ChatID = route.TargetID
		input.ReplyMessageID = route.MessageID
		_, err := c.channel.Send(ctx, input)
		return err
	}
	if strings.TrimSpace(delivery.Message.Text) != "" {
		if err := send(&larktypes.SendInput{Text: delivery.Message.Text}); err != nil {
			return err
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		_, path, err := readNativeMedia(attachment)
		if err != nil {
			return err
		}
		input := &larktypes.SendInput{FilePath: path}
		if attachment.Kind == "image" {
			input.ImagePath, input.FilePath = path, ""
		}
		if err = send(input); err != nil {
			return err
		}
	}
	c.state.markDelivery()
	return nil
}
