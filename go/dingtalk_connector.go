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

	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
	dingclient "github.com/open-dingtalk/dingtalk-stream-sdk-go/client"
)

const dingtalkTransport = "dingtalk"

type dingtalkConnector struct {
	runtime    *AgentRuntime
	platform   mgmtPlatform
	clientID   string
	secret     string
	apiBase    string
	oapiBase   string
	publicBase string
	mediaHost  string
	mediaPort  int
	admins     map[string]struct{}
	client     *http.Client
	stream     *dingclient.StreamClient
	state      nativeConnectorState
	cancel     context.CancelFunc
	server     *platformWebhookServer
	tokenMu    sync.Mutex
	token      string
	tokenUntil time.Time
	mediaMu    sync.RWMutex
	media      map[string]dingtalkMediaRecord
}

type dingtalkMediaRecord struct {
	DownloadCode string
	RobotCode    string
	Kind         string
	Name         string
	ExpiresAt    time.Time
}

func newDingTalkConnector(runtime *AgentRuntime, platform mgmtPlatform) (*dingtalkConnector, error) {
	clientID, _ := platform.Settings["client_id"].(string)
	clientID = strings.TrimSpace(clientID)
	if clientID == "" {
		return nil, platformConnectorStartupError(platform, "client_id")
	}
	secret := resolvePlatformCredential(platform, "client_secret")
	if secret == "" {
		return nil, platformConnectorStartupError(platform, "client_secret")
	}
	apiBase, _ := platform.Settings["dingtalk_api_base_url"].(string)
	if strings.TrimSpace(apiBase) == "" {
		apiBase = "https://api.dingtalk.com"
	}
	oapiBase, _ := platform.Settings["dingtalk_oapi_base_url"].(string)
	if strings.TrimSpace(oapiBase) == "" {
		oapiBase = "https://oapi.dingtalk.com"
	}
	publicBase, _ := platform.Settings["dingtalk_public_base_url"].(string)
	mediaHost, _ := platform.Settings["dingtalk_media_host"].(string)
	if strings.TrimSpace(mediaHost) == "" {
		mediaHost = "0.0.0.0"
	}
	return &dingtalkConnector{
		runtime: runtime, platform: platform, clientID: clientID, secret: secret,
		apiBase: strings.TrimRight(apiBase, "/"), oapiBase: strings.TrimRight(oapiBase, "/"),
		publicBase: strings.TrimRight(publicBase, "/"), mediaHost: mediaHost,
		mediaPort: kookIntSetting(platform, "dingtalk_media_port", 6202),
		admins:    nativePlatformAdminIDs(platform), client: &http.Client{Timeout: 30 * time.Second},
		state: newNativeConnectorState(platform), media: map[string]dingtalkMediaRecord{},
	}, nil
}

func (c *dingtalkConnector) ID() string   { return c.platform.ID }
func (c *dingtalkConnector) Type() string { return dingtalkTransport }

func (c *dingtalkConnector) Start(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	if c.publicBase != "" {
		mux := http.NewServeMux()
		mux.HandleFunc("/dingtalk-media/", c.handleMedia)
		server, err := startPlatformWebhookServer(ctx, c.mediaHost, c.mediaPort, mux)
		if err != nil {
			cancel()
			c.state.setError(err)
			return err
		}
		c.server = server
	}
	c.stream = dingclient.NewStreamClient(
		dingclient.WithAppCredential(dingclient.NewAppCredentialConfig(c.clientID, c.secret)),
		dingclient.WithAutoReconnect(true),
	)
	c.stream.RegisterChatBotCallbackRouter(c.handleMessage)
	c.state.setStatus("connecting")
	if err := c.stream.Start(ctx); err != nil {
		_ = c.server.Close()
		cancel()
		c.state.setError(err)
		return err
	}
	c.state.setStatus("connected")
	return nil
}

func (c *dingtalkConnector) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.stream != nil {
		c.stream.Close()
	}
	err := c.server.Close()
	c.state.setStatus("stopped")
	return err
}

func (c *dingtalkConnector) Health() platformConnectorHealth { return c.state.snapshot() }

func (c *dingtalkConnector) handleMessage(ctx context.Context, message *chatbot.BotCallbackDataModel) ([]byte, error) {
	if message == nil || message.MsgId == "" || message.SenderId == "" {
		return nil, nil
	}
	private := message.ConversationType != "2"
	targetID := message.ConversationId
	routeKind, conversationKind := "group", "group"
	if private {
		targetID = firstNonEmpty(message.SenderStaffId, message.SenderId)
		routeKind, conversationKind = "private", "private"
	}
	text := strings.TrimSpace(message.Text.Content)
	attachments := c.registerInboundMedia(message)
	if text == "" {
		text = nativeAttachmentOnlyPrompt(attachments)
	}
	occurred := time.Now().UTC()
	if message.CreateAt > 0 {
		occurred = time.UnixMilli(message.CreateAt).UTC()
	}
	err := c.runtime.acceptNativePlatformInbound(ctx, nativePlatformInbound{
		ConnectorID: c.ID(), Transport: dingtalkTransport, MessageID: message.MsgId,
		RouteKind: routeKind, TargetID: targetID, GuildID: firstNonEmpty(message.RobotCode, c.clientID),
		ChannelID: message.ConversationId, ConversationID: message.ConversationId,
		ConversationKind: conversationKind, SenderID: message.SenderId, SenderName: message.SenderNick,
		Text: text, Attachments: attachments, OccurredAt: occurred,
		IsWake: private || message.IsInAtList, IsAdmin: message.IsAdmin || nativePlatformIsAdmin(c.admins, message.SenderId),
		IsMentionBot: message.IsInAtList, IsCommand: nativePlatformIsCommand(text),
	})
	if err == nil {
		c.state.markEvent()
	}
	return nil, err
}

func (c *dingtalkConnector) registerInboundMedia(message *chatbot.BotCallbackDataModel) []transportAttachment {
	if c.publicBase == "" || message.Content == nil {
		return nil
	}
	raw, _ := json.Marshal(message.Content)
	var content map[string]any
	if json.Unmarshal(raw, &content) != nil {
		return nil
	}
	downloadCode, _ := content["downloadCode"].(string)
	if strings.TrimSpace(downloadCode) == "" {
		return nil
	}
	kind := "file"
	switch strings.ToLower(message.Msgtype) {
	case "picture", "image":
		kind = "image"
	case "audio", "voice":
		kind = "audio"
	case "video":
		kind = "video"
	}
	name, _ := content["fileName"].(string)
	mediaID := c.runtime.platformPseudonym("dingtalk-media", c.ID()+":"+message.MsgId+":"+downloadCode)
	c.mediaMu.Lock()
	c.media[mediaID] = dingtalkMediaRecord{
		DownloadCode: downloadCode, RobotCode: firstNonEmpty(message.RobotCode, c.clientID),
		Kind: kind, Name: name, ExpiresAt: time.Now().Add(15 * time.Minute),
	}
	c.mediaMu.Unlock()
	return []transportAttachment{{Kind: kind, Name: name, SourceURL: c.publicBase + "/dingtalk-media/" + url.PathEscape(mediaID)}}
}

func (c *dingtalkConnector) handleMedia(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.NotFound(w, r)
		return
	}
	id, err := url.PathUnescape(strings.TrimPrefix(r.URL.Path, "/dingtalk-media/"))
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
	token, err := c.accessToken(r.Context())
	if err != nil {
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	var download struct {
		DownloadURL string `json:"downloadUrl"`
	}
	err = c.apiJSON(r.Context(), http.MethodPost, "/v1.0/robot/messageFiles/download", token, map[string]any{
		"downloadCode": record.DownloadCode, "robotCode": record.RobotCode,
	}, &download)
	if err != nil || download.DownloadURL == "" || !c.safeDownloadURL(download.DownloadURL) {
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	request, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, download.DownloadURL, nil)
	response, err := c.client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		http.Error(w, "media unavailable", http.StatusBadGateway)
		return
	}
	defer response.Body.Close()
	w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxImageBytes+1))
}

func (c *dingtalkConnector) safeDownloadURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	api, _ := url.Parse(c.apiBase)
	return parsed.Scheme == "http" && api != nil && parsed.Host == api.Host && isLoopbackBindHost(parsed.Hostname())
}

func (c *dingtalkConnector) accessToken(ctx context.Context) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenUntil) {
		return c.token, nil
	}
	var response struct {
		AccessToken string `json:"accessToken"`
		ExpireIn    int    `json:"expireIn"`
	}
	if err := c.apiJSON(ctx, http.MethodPost, "/v1.0/oauth2/accessToken", "", map[string]any{
		"appKey": c.clientID, "appSecret": c.secret,
	}, &response); err != nil {
		return "", err
	}
	if response.AccessToken == "" {
		return "", errors.New("DingTalk access token is missing")
	}
	if response.ExpireIn < 120 {
		response.ExpireIn = 7200
	}
	c.token, c.tokenUntil = response.AccessToken, time.Now().Add(time.Duration(response.ExpireIn-60)*time.Second)
	return c.token, nil
}

func (c *dingtalkConnector) apiJSON(ctx context.Context, method, path, token string, payload any, output any) error {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, c.apiBase+path, body)
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if token != "" {
		request.Header.Set("x-acs-dingtalk-access-token", token)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &platformDeliveryError{Retryable: response.StatusCode == 429 || response.StatusCode >= 500, Reason: "dingtalk_api_failed", Cause: fmt.Errorf("status %d", response.StatusCode)}
	}
	if output != nil && len(data) > 0 {
		return json.Unmarshal(data, output)
	}
	return nil
}

func (c *dingtalkConnector) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	send := func(key string, params map[string]any) error {
		payload := map[string]any{"robotCode": firstNonEmpty(route.GuildID, c.clientID), "msgKey": key}
		encoded, _ := json.Marshal(params)
		payload["msgParam"] = string(encoded)
		path := "/v1.0/robot/groupMessages/send"
		if route.Kind == "private" {
			path = "/v1.0/robot/oToMessages/batchSend"
			payload["userIds"] = []string{route.TargetID}
		} else {
			payload["openConversationId"] = route.TargetID
		}
		return c.apiJSON(ctx, http.MethodPost, path, token, payload, nil)
	}
	if strings.TrimSpace(delivery.Message.Text) != "" {
		if err = send("sampleText", map[string]any{"content": delivery.Message.Text}); err != nil {
			return err
		}
	}
	for _, attachment := range delivery.Message.Attachments {
		mediaID, err := c.uploadMedia(ctx, token, attachment)
		if err != nil {
			return err
		}
		key, params := "sampleFile", map[string]any{"mediaId": mediaID, "fileName": filepath.Base(attachment.LocalPath)}
		switch attachment.Kind {
		case "image":
			key, params = "sampleImageMsg", map[string]any{"photoURL": mediaID}
		case "audio":
			key, params = "sampleAudio", map[string]any{"mediaId": mediaID, "duration": "1000"}
		case "video":
			key, params = "sampleVideo", map[string]any{"videoMediaId": mediaID, "videoType": "mp4", "duration": "1"}
		}
		if err = send(key, params); err != nil {
			return err
		}
	}
	c.state.markDelivery()
	return nil
}

func (c *dingtalkConnector) uploadMedia(ctx context.Context, token string, attachment agentAttachment) (string, error) {
	data, path, err := readNativeMedia(attachment)
	if err != nil {
		return "", err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("media", filepath.Base(path))
	if err != nil {
		return "", err
	}
	if _, err = part.Write(data); err != nil {
		return "", err
	}
	_ = writer.Close()
	mediaType := "file"
	if attachment.Kind == "image" {
		mediaType = "image"
	} else if attachment.Kind == "audio" {
		mediaType = "voice"
	}
	endpoint := c.oapiBase + "/media/upload?access_token=" + url.QueryEscape(token) + "&type=" + url.QueryEscape(mediaType)
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := c.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		ErrorCode int    `json:"errcode"`
		ErrorMsg  string `json:"errmsg"`
		MediaID   string `json:"media_id"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&result) != nil || response.StatusCode != http.StatusOK || result.ErrorCode != 0 || result.MediaID == "" {
		return "", errors.New("DingTalk media upload failed")
	}
	return result.MediaID, nil
}
