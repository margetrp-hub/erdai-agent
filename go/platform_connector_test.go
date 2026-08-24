package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/event"
	"github.com/tencent-connect/botgo/openapi/options"
)

func TestConnectorHealthErrorsRedactCredentials(t *testing.T) {
	raw := errors.New(`Get "https://api.telegram.org/bot123456:telegram-secret/getUpdates?access_token=wechat-secret&api_key=provider-secret": Authorization: Bearer header-secret`)
	redacted := redactConnectorError(raw)
	for _, secret := range []string{"telegram-secret", "wechat-secret", "provider-secret", "header-secret"} {
		if strings.Contains(redacted, secret) {
			t.Fatalf("connector health error leaked %q: %s", secret, redacted)
		}
	}
	if strings.Count(redacted, "[redacted]") < 4 {
		t.Fatalf("connector health error was not fully redacted: %s", redacted)
	}
}

type fakeQQOfficialAPI struct {
	groupMessages []dto.APIMessage
	groupTargets  []string
	c2cMessages   []dto.APIMessage
}

func (f *fakeQQOfficialAPI) WS(context.Context, map[string]string, string) (*dto.WebsocketAP, error) {
	return &dto.WebsocketAP{}, nil
}

func (f *fakeQQOfficialAPI) PostGroupMessage(_ context.Context, target string, message dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	f.groupTargets = append(f.groupTargets, target)
	f.groupMessages = append(f.groupMessages, message)
	if _, ok := message.(qqRichMediaUpload); ok {
		return &dto.Message{ID: "bot-group-upload", FileInfo: []byte("uploaded-file-info")}, nil
	}
	return &dto.Message{ID: "bot-group-message"}, nil
}

func (f *fakeQQOfficialAPI) PostC2CMessage(_ context.Context, _ string, message dto.APIMessage, _ ...options.Option) (*dto.Message, error) {
	f.c2cMessages = append(f.c2cMessages, message)
	if _, ok := message.(qqRichMediaUpload); ok {
		return &dto.Message{ID: "bot-c2c-upload", FileInfo: []byte("uploaded-file-info")}, nil
	}
	return &dto.Message{ID: "bot-c2c-message"}, nil
}

func (f *fakeQQOfficialAPI) PostMessage(context.Context, string, *dto.MessageToCreate, ...options.Option) (*dto.Message, error) {
	return &dto.Message{}, nil
}

func (f *fakeQQOfficialAPI) PostDirectMessage(context.Context, *dto.DirectMessage, *dto.MessageToCreate, ...options.Option) (*dto.Message, error) {
	return &dto.Message{}, nil
}

func TestPlatformRouteIsEncryptedPersistentAndSequenced(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	route := platformReplyRoute{
		ConnectorID: "qq_official", Transport: qqOfficialTransport, Kind: "group",
		TargetID: "raw-group-openid", MessageID: "raw-message-id",
	}
	handle, err := runtime.rememberPlatformRoute(ctx, "event-stable", route)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := runtime.rememberPlatformRoute(ctx, "event-stable", route)
	if err != nil || duplicate != handle {
		t.Fatalf("duplicate route = %q, want %q: %v", duplicate, handle, err)
	}
	var ciphertext []byte
	if err = runtime.db.QueryRow("SELECT route_cipher FROM platform_reply_routes WHERE reply_handle = ?", handle).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(route.TargetID)) || bytes.Contains(ciphertext, []byte(route.MessageID)) {
		t.Fatal("platform route stored raw QQ identifiers")
	}
	resolved, err := runtime.platformRoute(ctx, handle)
	if err != nil || resolved != route {
		t.Fatalf("resolved route = %+v: %v", resolved, err)
	}
	first, err := runtime.nextPlatformMessageSequence(ctx, handle)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.nextPlatformMessageSequence(ctx, handle)
	if err != nil || first != 1 || second != 2 {
		t.Fatalf("message sequences = %d, %d: %v", first, second, err)
	}
}

func TestQQInboundEntersCoreWithoutHTTPBridge(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{"admin-openid": {}},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	message := dto.Message{
		ID: "message-raw", GroupID: "group-raw", Content: "<@!bot> 豆包，看看这个",
		Author: &dto.User{ID: "admin-openid", Username: "管理员"},
	}
	if err := connector.handleInbound(context.Background(), nil, message, "group"); err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "qq_official:message-raw")
	var transport, conversation, sender, replyHandle string
	var isAdmin bool
	if err := runtime.db.QueryRow(`
		SELECT transport, conversation_ref, sender_ref, reply_handle, is_admin
		FROM agent_runs WHERE event_id = ?
	`, eventID).Scan(&transport, &conversation, &sender, &replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	if transport != qqOfficialTransport || !isAdmin || conversation == message.GroupID || sender == message.Author.ID {
		t.Fatalf("normalized run = %q %q %q admin=%v", transport, conversation, sender, isAdmin)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.TargetID != message.GroupID || route.MessageID != message.ID {
		t.Fatalf("reply route = %+v: %v", route, err)
	}
}

func TestQQPlainGroupEventUsesCoreCapturePolicy(t *testing.T) {
	for _, test := range []struct {
		name         string
		capture      bool
		wantRuns     int
		wantObserved int
	}{
		{name: "capture disabled", capture: false, wantRuns: 0, wantObserved: 0},
		{name: "capture enabled", capture: true, wantRuns: 1, wantObserved: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
				"mode": "active", "captureUnaddressedGroups": test.capture,
			})
			setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
				"enabled": true, "initialProbability": 1.0, "afterReplyProbability": 1.0,
				"proactiveChatEnabled": true,
				"questionBoost":        0.0, "waterReduce": 0.0, "messageQualityEnabled": true,
				"replyDensityEnabled": false,
			})
			connector := &qqOfficialConnector{
				runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
				groupC2C: true, adminIDs: map[string]struct{}{},
				health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
			}
			now := time.Now().UTC().Format(time.RFC3339Nano)
			raw, err := json.Marshal(map[string]any{"d": map[string]any{
				"id": "plain-message", "group_openid": "group-raw", "content": "Does this plan make sense?",
				"timestamp": now,
				"author":    map[string]any{"member_openid": "member-openid", "username": "member"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			event.RegisterHandlers(connector.handlers(context.Background())...)
			payload := &dto.WSPayload{
				WSPayloadBase: dto.WSPayloadBase{OPCode: dto.WSDispatchEvent, Type: qqOfficialGroupMessageCreate},
				RawMessage:    raw,
			}
			if err = event.ParseAndHandle(payload); err != nil {
				t.Fatal(err)
			}
			var runs int
			if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil {
				t.Fatal(err)
			}
			if runs != test.wantRuns {
				t.Fatalf("plain group event created %d runs, want %d", runs, test.wantRuns)
			}
			recent, err := runtime.memory.RecentGroupEvents(context.Background(), runtime.platformPseudonym("conversation", "group-raw"), 6)
			if err != nil || len(recent) != test.wantObserved {
				t.Fatalf("observed plain group events = %d, want %d: %v", len(recent), test.wantObserved, err)
			}
			if test.wantRuns == 1 {
				eventID := runtime.platformPseudonym("event", "qq_official:plain-message")
				var replyHandle string
				if err = runtime.db.QueryRow("SELECT reply_handle FROM agent_runs WHERE event_id = ?", eventID).Scan(&replyHandle); err != nil {
					t.Fatal(err)
				}
				route, routeErr := runtime.platformRoute(context.Background(), replyHandle)
				if routeErr != nil || route.Kind != "group" {
					t.Fatalf("plain group reply route = %+v: %v", route, routeErr)
				}
			}
		})
	}
}

func TestQQRawGroupATPreservesQuoteAndNormalizesAttachments(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "quoted-message", "group_openid": "group-openid", "content": "my answer",
		"timestamp": "2026-08-04T00:00:00+00:00", "message_type": 103,
		"author":            map[string]any{"member_openid": "member-openid", "username": "member"},
		"message_reference": map[string]any{"message_id": "quoted-1"},
		"msg_elements": []any{map[string]any{
			"content": "quoted text", "attachments": []any{map[string]any{
				"content_type": "image/png", "filename": "quoted.png", "url": "img.example.com/quoted.png",
			}},
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := &dto.WSPayload{WSPayloadBase: dto.WSPayloadBase{Type: dto.EventGroupAtMessageCreate}}
	if err = connector.handleRawInbound(context.Background(), payload, raw, "group"); err != nil {
		t.Fatal(err)
	}
	var runID string
	var cipher []byte
	if err = runtime.db.QueryRow("SELECT id, input_cipher FROM agent_runs WHERE event_id = ?",
		runtime.platformPseudonym("event", "qq_official:quoted-message")).Scan(&runID, &cipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(cipher)
	if err != nil || strings.TrimSpace(string(plain)) != "my answer" {
		t.Fatalf("normalized quote = %q: %v", plain, err)
	}
	attachments := persistedRunAttachments(t, runtime, runID)
	if len(attachments) != 1 || attachments[0].Kind != "image" ||
		attachments[0].SourceURL != "https://img.example.com/quoted.png" {
		t.Fatalf("normalized quote attachments = %+v", attachments)
	}
	conversation := runtime.platformPseudonym("conversation", "group-openid")
	events, err := runtime.memory.RecentGroupEvents(context.Background(), conversation, 10)
	if err != nil || len(events) != 1 || events[0].ReplyToMessageID != "quoted-1" || events[0].UntrustedQuotedText != "quoted text" {
		t.Fatalf("structured quote context = %+v: %v", events, err)
	}
}

func TestQQRawGroupATToleratesOptionalShapeChanges(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "shape-change-message", "group_openid": "group-openid", "content": "@豆包 看看",
		"timestamp": 1722816000, "author": map[string]any{"member_openid": "member-openid"},
		"mentions": []any{"unexpected-shape"}, "attachments": map[string]any{"unexpected": true},
		"message_reference": "unexpected-shape",
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.handleRawInbound(context.Background(), nil, raw, "group"); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs WHERE event_id = ?",
		runtime.platformPseudonym("event", "qq_official:shape-change-message")).Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("tolerant group event runs = %d: %v", runs, err)
	}
	health := connector.Health()
	if health.Details["lastRawInboundKind"] != "group" || health.Details["lastInboundError"] != "" {
		t.Fatalf("group diagnostics = %+v", health.Details)
	}
}

func TestQQRawImageOnlyATCreatesOwnedRun(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "image-only", "group_openid": "group-openid", "content": "",
		"author":      map[string]any{"member_openid": "member-openid"},
		"attachments": []any{map[string]any{"filename": "photo.jpg", "url": "//img.example.com/photo.jpg"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	connector.registerRawMessageParsers(context.Background())
	payload := &dto.WSPayload{
		WSPayloadBase: dto.WSPayloadBase{OPCode: dto.WSDispatchEvent, Type: dto.EventGroupAtMessageCreate},
		RawMessage:    raw,
	}
	if err = event.ParseAndHandle(payload); err != nil {
		t.Fatal(err)
	}
	var runID string
	var cipher []byte
	if err = runtime.db.QueryRow("SELECT id, input_cipher FROM agent_runs WHERE event_id = ?",
		runtime.platformPseudonym("event", "qq_official:image-only")).Scan(&runID, &cipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(cipher)
	if err != nil || strings.TrimSpace(string(plain)) == "" {
		t.Fatalf("image-only input = %q: %v", plain, err)
	}
	attachments := persistedRunAttachments(t, runtime, runID)
	if len(attachments) != 1 || attachments[0].Kind != "image" ||
		attachments[0].SourceURL != "https://img.example.com/photo.jpg" {
		t.Fatalf("image-only attachments = %+v", attachments)
	}
}

func TestQQRawC2CPDFAttachmentCreatesOwnedRun(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "private-pdf", "content": "",
		"author": map[string]any{"user_openid": "private-member", "username": "成员"},
		"attachments": []any{map[string]any{
			"content_type": "application/pdf", "filename": "report.pdf",
			"url": "//files.example.com/report.pdf?token=short-lived",
		}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.handleRawInbound(context.Background(), nil, raw, "c2c"); err != nil {
		t.Fatal(err)
	}
	var runID string
	if err = runtime.db.QueryRow("SELECT id FROM agent_runs WHERE event_id = ?",
		runtime.platformPseudonym("event", "qq_official:private-pdf")).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	attachments := persistedRunAttachments(t, runtime, runID)
	if len(attachments) != 1 || attachments[0].Kind != "file" ||
		attachments[0].Name != "report.pdf" ||
		attachments[0].SourceURL != "https://files.example.com/report.pdf?token=short-lived" {
		t.Fatalf("private PDF attachments = %+v", attachments)
	}
}

func persistedRunAttachments(t *testing.T, runtime *AgentRuntime, runID string) []transportAttachment {
	t.Helper()
	var ciphertext []byte
	if err := runtime.db.QueryRow("SELECT attachments_cipher FROM agent_runs WHERE id = ?", runID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	encoded, err := runtime.decrypt(ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	var attachments []transportAttachment
	if err = json.Unmarshal(encoded, &attachments); err != nil {
		t.Fatal(err)
	}
	return attachments
}

func TestQQReplyToBotMessageWakesPlainGroup(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{}, api: &fakeQQOfficialAPI{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	if err := connector.sendText(context.Background(), platformReplyRoute{
		Kind: "group", TargetID: "group-openid",
	}, &dto.MessageToCreate{Content: "刚才说到这里。"}); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "reply-to-bot", "group_openid": "group-openid", "content": "然后呢",
		"author":            map[string]any{"member_openid": "member-openid", "username": "群友"},
		"message_reference": map[string]any{"message_id": "bot-group-message"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.handleRawInbound(context.Background(), nil, raw, "group_plain"); err != nil {
		t.Fatal(err)
	}
	assertQQPlainMentionOwnedRun(t, runtime, "reply-to-bot", "然后呢")
	conversation := runtime.platformPseudonym("conversation", "group-openid")
	events, err := runtime.memory.RecentGroupEvents(context.Background(), conversation, 10)
	if err != nil || len(events) != 1 || events[0].ReplyToMessageID != "bot-group-message" {
		t.Fatalf("structured reply context = %+v: %v", events, err)
	}
}

func TestQQPlainGroupAtOtherRespectsIgnorePolicy(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
		"mode": "active", "captureUnaddressedGroups": true,
	})
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "initialProbability": 1.0, "proactiveChatEnabled": true,
		"messageQualityEnabled": true, "ignoreAtOthers": true,
	})
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
		botID:  "bot-openid",
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "at-other", "group_openid": "group-openid", "content": "<@other-openid> 你怎么看",
		"author":   map[string]any{"member_openid": "member-openid", "username": "群友"},
		"mentions": []any{map[string]any{"id": "other-openid", "username": "别人"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.handleRawInbound(context.Background(), nil, raw, "group_plain"); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("@ other created %d runs", runs)
	}
}

func TestQQPlainGroupExplicitBotMentionAlwaysWins(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
		botID:  "bot-openid", botDisplayName: "豆包",
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "at-bot-and-other", "group_openid": "group-openid",
		"content": "<@bot-openid> <@other-openid> 你来评评理",
		"author":  map[string]any{"member_openid": "member-openid", "username": "群友"},
		"mentions": []any{
			map[string]any{"id": "bot-openid", "username": "豆包"},
			map[string]any{"id": "other-openid", "username": "别人"},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err = connector.handleRawInbound(context.Background(), nil, raw, "group_plain"); err != nil {
		t.Fatal(err)
	}
	assertQQPlainMentionOwnedRun(t, runtime, "at-bot-and-other", "<@other-openid> 你来评评理")
}

func TestQQPlainGroupEventIsIdempotentWithATEvent(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
		"mode": "active", "captureUnaddressedGroups": true,
	})
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "initialProbability": 1.0, "messageQualityEnabled": true,
	})
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	message := dto.Message{
		ID: "same-message", GroupID: "group-raw", Content: "豆包，你看看这个",
		Timestamp: "2026-08-04T00:00:00+00:00",
		Author:    &dto.User{ID: "member-openid", Username: "群友"},
	}
	raw, err := json.Marshal(map[string]any{"d": message})
	if err != nil {
		t.Fatal(err)
	}
	payload := &dto.WSPayload{WSPayloadBase: dto.WSPayloadBase{Type: qqOfficialGroupMessageCreate}}
	if err = connector.handlePlainInbound(context.Background(), payload, raw); err != nil {
		t.Fatal(err)
	}
	if err = connector.handleInbound(context.Background(), payload, message, "group"); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 1 {
		t.Fatalf("duplicate QQ events created %d runs", runs)
	}
}

func TestQQPlainGroupMentionMatchesReadyBotID(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport, DisplayName: "豆包"},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health:         platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
		botDisplayName: "豆包",
	}
	ready := &dto.WSReadyData{}
	ready.User.ID = "ready-bot-openid"
	ready.User.Username = "豆包"
	connector.markReady(ready)
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "plain-ready-mention", "group_openid": "group-openid", "content": "<@ready-bot-openid> 看看这个",
		"author":   map[string]any{"member_openid": "member-openid", "username": "群友"},
		"mentions": []any{map[string]any{"id": "ready-bot-openid", "username": "豆包"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := &dto.WSPayload{WSPayloadBase: dto.WSPayloadBase{Type: qqOfficialGroupMessageCreate}}
	if err = connector.handlePlainInbound(context.Background(), payload, raw); err != nil {
		t.Fatal(err)
	}
	assertQQPlainMentionOwnedRun(t, runtime, "plain-ready-mention", "看看这个")
	health := connector.Health()
	if health.Details["lastInboundKind"] != "group_plain" || health.Details["lastMentionBot"] != true {
		t.Fatalf("plain mention health = %+v", health.Details)
	}
}

func TestQQPlainGroupMentionMatchesConfiguredDisplayName(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connector := &qqOfficialConnector{
		runtime: runtime, platform: mgmtPlatform{ID: "qq_official", Type: qqOfficialTransport, DisplayName: "豆包"},
		groupC2C: true, adminIDs: map[string]struct{}{},
		health:         platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
		botDisplayName: "豆包",
	}
	raw, err := json.Marshal(map[string]any{"d": map[string]any{
		"id": "plain-name-mention", "group_openid": "group-openid", "content": "@豆包 别装没看见",
		"author": map[string]any{"member_openid": "member-openid", "username": "群友"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	payload := &dto.WSPayload{WSPayloadBase: dto.WSPayloadBase{Type: qqOfficialGroupMessageCreate}}
	if err = connector.handlePlainInbound(context.Background(), payload, raw); err != nil {
		t.Fatal(err)
	}
	assertQQPlainMentionOwnedRun(t, runtime, "plain-name-mention", "别装没看见")
}

func assertQQPlainMentionOwnedRun(t *testing.T, runtime *AgentRuntime, messageID, wantInput string) {
	t.Helper()
	var cipher []byte
	eventID := runtime.platformPseudonym("event", "qq_official:"+messageID)
	if err := runtime.db.QueryRow("SELECT input_cipher FROM agent_runs WHERE event_id = ?", eventID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(cipher)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(plain)); got != wantInput {
		t.Fatalf("plain mention input = %q, want %q", got, wantInput)
	}
}

func newDormantRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	runtime := newIdleRuntime(t)
	runtime.cancel()
	runtime.workers.Wait()
	return runtime
}

func TestQQAddressedInboundDedupeAllowsRetryAfterFailure(t *testing.T) {
	connector := &qqOfficialConnector{platform: mgmtPlatform{ID: "qq-one"}}
	key, duplicate := connector.claimAddressedInbound("group", "message-one")
	if duplicate || key == "" {
		t.Fatalf("first addressed claim = %q duplicate=%v", key, duplicate)
	}
	if _, duplicate = connector.claimAddressedInbound("group", "message-one"); !duplicate {
		t.Fatal("duplicate addressed event was not suppressed")
	}
	connector.releaseAddressedInbound(key)
	if _, duplicate = connector.claimAddressedInbound("group", "message-one"); duplicate {
		t.Fatal("failed addressed event could not be retried")
	}
	if key, duplicate = connector.claimAddressedInbound("group_plain", "message-one"); duplicate || key != "" {
		t.Fatalf("plain group claim = %q duplicate=%v", key, duplicate)
	}
}

func TestQQGatewayHeartbeatStalenessRequestsOneReconnect(t *testing.T) {
	now := time.Now().UTC()
	connector := &qqOfficialConnector{
		health:            platformConnectorHealth{Status: "connected"},
		gatewayActivityAt: now.Add(-qqOfficialGatewayStaleAfter - time.Second),
	}
	if !connector.gatewayNeedsReconnect(now) {
		t.Fatal("stale QQ gateway did not request reconnect")
	}
	if connector.Health().Status != "reconnecting" {
		t.Fatalf("health after stale heartbeat = %+v", connector.Health())
	}
	if connector.gatewayNeedsReconnect(now.Add(time.Second)) {
		t.Fatal("QQ gateway requested duplicate reconnect during cooldown")
	}
	connector.markGatewayActivity("HeartbeatAck")
	if connector.Health().Status != "connected" {
		t.Fatalf("health after heartbeat ACK = %+v", connector.Health())
	}
}

func TestQQGuildATHandlerDoesNotDependOnDirectMessages(t *testing.T) {
	for _, test := range []struct {
		guildDM      bool
		wantDirectDM bool
	}{
		{guildDM: false, wantDirectDM: false},
		{guildDM: true, wantDirectDM: true},
	} {
		connector := &qqOfficialConnector{guildDM: test.guildDM}
		var hasPlain, hasAT, hasDirectDM bool
		for _, handler := range connector.handlers(context.Background()) {
			switch handler.(type) {
			case event.PlainEventHandler:
				hasPlain = true
			case event.ATMessageEventHandler:
				hasAT = true
			case event.DirectMessageEventHandler:
				hasDirectDM = true
			}
		}
		if !hasPlain || !hasAT || hasDirectDM != test.wantDirectDM {
			t.Fatalf("guildDM=%v handlers: plain=%v at=%v direct=%v", test.guildDM, hasPlain, hasAT, hasDirectDM)
		}
	}
}

func TestQQDeliverySendsTextAndRichMediaThroughGo(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if err := os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(mediaMountRoot, "erdai-qq-*.png")
	if err != nil {
		t.Fatal(err)
	}
	path := file.Name()
	t.Cleanup(func() { _ = os.Remove(path) })
	if _, err = file.Write([]byte("fake image bytes")); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	route := platformReplyRoute{
		ConnectorID: "qq_official", Transport: qqOfficialTransport, Kind: "group",
		TargetID: "group-openid", MessageID: "source-message-id",
	}
	handle, err := runtime.rememberPlatformRoute(ctx, "delivery-event", route)
	if err != nil {
		t.Fatal(err)
	}
	fakeAPI := &fakeQQOfficialAPI{}
	connector := &qqOfficialConnector{
		runtime: runtime, api: fakeAPI,
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	delivery := leasedTransportDelivery{
		ReplyHandle: handle,
		Message: transportDeliveryMessage{
			Text: "我弄好了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: path, Name: "测试图片.png"}},
		},
	}
	if err = connector.Deliver(ctx, route, delivery); err != nil {
		t.Fatal(err)
	}
	if len(fakeAPI.groupMessages) != 3 {
		t.Fatalf("QQ calls = %d, want text, upload and media send", len(fakeAPI.groupMessages))
	}
	textMessage, ok := fakeAPI.groupMessages[0].(*dto.MessageToCreate)
	if !ok || textMessage.MsgID != route.MessageID || textMessage.MsgSeq != 1 {
		t.Fatalf("text message = %#v", fakeAPI.groupMessages[0])
	}
	upload, ok := fakeAPI.groupMessages[1].(qqRichMediaUpload)
	if !ok || upload.FileType != 1 || upload.FileData == "" || upload.FileName != "测试图片.png" {
		t.Fatalf("rich media upload = %#v", fakeAPI.groupMessages[1])
	}
	mediaMessage, ok := fakeAPI.groupMessages[2].(*dto.MessageToCreate)
	if !ok || mediaMessage.MsgType != dto.RichMediaMsg || mediaMessage.MsgSeq != 2 || mediaMessage.Media == nil {
		t.Fatalf("rich media message = %#v", fakeAPI.groupMessages[2])
	}
}

func TestQQProactiveGroupDeliveryOmitsMessageID(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	route := platformReplyRoute{
		ConnectorID: "qq_official", Transport: qqOfficialTransport, Kind: "group",
		TargetID: "group-openid",
	}
	handle, err := runtime.rememberPlatformRoute(ctx, "proactive-event", route)
	if err != nil {
		t.Fatal(err)
	}
	fakeAPI := &fakeQQOfficialAPI{}
	connector := &qqOfficialConnector{
		runtime: runtime, api: fakeAPI,
		health: platformConnectorHealth{ID: "qq_official", Type: qqOfficialTransport},
	}
	delivery := leasedTransportDelivery{
		ReplyHandle: handle,
		Message:     transportDeliveryMessage{Text: "proactive hello"},
	}
	if err = connector.Deliver(ctx, route, delivery); err != nil {
		t.Fatal(err)
	}
	if len(fakeAPI.groupMessages) != 1 || len(fakeAPI.groupTargets) != 1 || fakeAPI.groupTargets[0] != route.TargetID {
		t.Fatalf("proactive sends = targets %v messages %#v", fakeAPI.groupTargets, fakeAPI.groupMessages)
	}
	message, ok := fakeAPI.groupMessages[0].(*dto.MessageToCreate)
	if !ok || message.MsgID != "" || message.MsgSeq != 1 || message.Content != "proactive hello" {
		t.Fatalf("proactive message = %#v", fakeAPI.groupMessages[0])
	}
}
