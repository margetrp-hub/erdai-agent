package main

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestOneBotReverseWebSocketInboundAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connector, err := newOneBotConnector(runtime, mgmtPlatform{
		ID: "onebot-primary", Type: oneBotTransport,
		Settings: map[string]any{"admin_ids": "42"}, CredentialRefs: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.token = "reverse-token"
	server := httptest.NewServer(http.HandlerFunc(connector.handleWebSocket))
	defer server.Close()
	header := http.Header{"Authorization": {"Bearer reverse-token"}}
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	client, response, err := websocket.DefaultDialer.Dial(wsURL, header)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.WriteJSON(map[string]any{
		"post_type": "message", "message_type": "group", "message_id": 1001,
		"user_id": 42, "group_id": 88, "self_id": 99, "time": 1785772800,
		"sender": map[string]any{"user_id": 42, "nickname": "管理员", "card": "群管理"},
		"message": []map[string]any{
			{"type": "at", "data": map[string]any{"qq": "99"}},
			{"type": "text", "data": map[string]any{"text": " 帮我看看"}},
			{"type": "image", "data": map[string]any{"url": "https://img.example/test.png"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "onebot-primary:1001")
	waitForPlatformRun(t, runtime, eventID)
	var transport, replyHandle string
	var isAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`
		SELECT transport, reply_handle, is_admin, input_cipher
		FROM agent_runs WHERE event_id = ?
	`, eventID).Scan(&transport, &replyHandle, &isAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || transport != oneBotTransport || !isAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("OneBot run = transport=%q admin=%v input=%q err=%v", transport, isAdmin, plain, err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.Kind != "group" || route.TargetID != "88" || route.MessageID != "1001" {
		t.Fatalf("OneBot route = %+v: %v", route, err)
	}

	delivered := make(chan error, 1)
	go func() {
		delivered <- connector.Deliver(context.Background(), route, leasedTransportDelivery{
			Message: transportDeliveryMessage{Text: "查好了。"},
		})
	}()
	var request struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
		Echo   string         `json:"echo"`
	}
	if err = client.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if request.Action != "send_group_msg" || fmt.Sprint(request.Params["group_id"]) != "88" || request.Echo == "" {
		t.Fatalf("OneBot outbound request = %+v", request)
	}
	if err = client.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-delivered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("OneBot delivery did not complete")
	}
}

func TestTelegramBotAPIInboundTextMediaAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	var sentText string
	var sentPhoto bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/botTEST/getMe":
			_, _ = w.Write([]byte(`{"ok":true,"result":{"id":99,"is_bot":true,"username":"doubaobot","first_name":"豆包"}}`))
		case "/botTEST/getUpdates":
			_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":10,"message":{"message_id":7,"date":1785772800,"chat":{"id":-100,"type":"supergroup"},"from":{"id":42,"first_name":"群","last_name":"管理","username":"admin"},"text":"@doubaobot 帮我看看","photo":[{"file_id":"small","width":10,"height":10},{"file_id":"large","width":100,"height":100}]}}]}`))
		case "/botTEST/getFile":
			if r.URL.Query().Get("file_id") != "large" {
				t.Errorf("Telegram getFile id = %q", r.URL.Query().Get("file_id"))
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"large","file_path":"photos/large.jpg"}}`))
		case "/file/botTEST/photos/large.jpg":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("telegram-file"))
		case "/botTEST/sendMessage":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			sentText = fmt.Sprint(payload["text"])
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":8}}`))
		case "/botTEST/sendPhoto":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
			}
			file, _, err := r.FormFile("photo")
			if err != nil {
				t.Error(err)
			} else {
				file.Close()
				sentPhoto = true
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":9}}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_TELEGRAM_TEST_TOKEN", "TEST")
	connector, err := newTelegramConnector(runtime, mgmtPlatform{
		ID: "telegram-primary", Type: telegramTransport,
		Settings: map[string]any{
			"telegram_api_base_url":    server.URL + "/bot",
			"telegram_file_base_url":   server.URL + "/file/bot",
			"telegram_public_base_url": "http://core.test",
			"admin_ids":                "42",
		},
		CredentialRefs: map[string]any{"telegram_token": "ERDAI_TELEGRAM_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.loadIdentity(context.Background()); err != nil {
		t.Fatal(err)
	}
	inboundAttachments, err := connector.telegramAttachments(context.Background(), &telegramMessage{
		Photo: []telegramPhoto{{FileID: "small"}, {FileID: "large"}},
	})
	if err != nil || len(inboundAttachments) != 1 || inboundAttachments[0].Kind != "image" ||
		!strings.Contains(inboundAttachments[0].SourceURL, "/telegram-media/") || strings.Contains(inboundAttachments[0].SourceURL, "TEST") {
		t.Fatalf("Telegram parsed attachments = %+v: %v", inboundAttachments, err)
	}
	mediaID := strings.TrimPrefix(inboundAttachments[0].SourceURL, "http://core.test/telegram-media/")
	mediaRequest := httptest.NewRequest(http.MethodGet, "/telegram-media/"+mediaID, nil)
	mediaResponse := httptest.NewRecorder()
	connector.handleMedia(mediaResponse, mediaRequest)
	if mediaResponse.Code != http.StatusOK || mediaResponse.Body.String() != "telegram-file" {
		t.Fatalf("Telegram media proxy = %d %q", mediaResponse.Code, mediaResponse.Body.String())
	}
	if err = connector.pollOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "telegram-primary:7")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`
		SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?
	`, eventID).Scan(&replyHandle, &isAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !isAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("Telegram run = admin=%v input=%q err=%v", isAdmin, plain, err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.Kind != "group" || route.TargetID != "-100" || route.MessageID != "7" {
		t.Fatalf("Telegram route = %+v: %v", route, err)
	}
	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-telegram-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	if _, err = media.Write([]byte("png-data")); err != nil {
		t.Fatal(err)
	}
	media.Close()
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{
		Message: transportDeliveryMessage{
			Text:        "查好了。",
			Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if sentText != "查好了。" || !sentPhoto {
		t.Fatalf("Telegram outbound text=%q photo=%v", sentText, sentPhoto)
	}
}

func TestNormalizeOneBotMessageDistinguishesMentionTargets(t *testing.T) {
	raw, err := json.Marshal([]map[string]any{
		{"type": "at", "data": map[string]any{"qq": "4014088643", "name": "豆包"}},
		{"type": "text", "data": map[string]any{"text": "？"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	text, _, mentionBot, atOthers, atAll := normalizeOneBotMessage(raw, "", "3639159672")
	if mentionBot || !atOthers || atAll || text != "@豆包 ？" {
		t.Fatalf("other mention = text=%q mentionBot=%v atOthers=%v atAll=%v", text, mentionBot, atOthers, atAll)
	}

	raw, err = json.Marshal([]map[string]any{{"type": "at", "data": map[string]any{"qq": "3639159672"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, mentionBot, atOthers, atAll = normalizeOneBotMessage(raw, "", "3639159672")
	if !mentionBot || atOthers || atAll {
		t.Fatalf("self mention = mentionBot=%v atOthers=%v atAll=%v", mentionBot, atOthers, atAll)
	}

	raw, err = json.Marshal([]map[string]any{{"type": "at", "data": map[string]any{"qq": "all"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, _, mentionBot, atOthers, atAll = normalizeOneBotMessage(raw, "", "3639159672")
	if mentionBot || atOthers || !atAll {
		t.Fatalf("all mention = mentionBot=%v atOthers=%v atAll=%v", mentionBot, atOthers, atAll)
	}
}

func TestOneBotGroupAtOtherIsObservedWithoutCreatingRun(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
		"mode": "active", "captureUnaddressedGroups": true,
	})
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "initialProbability": 1.0, "proactiveChatEnabled": true,
		"messageQualityEnabled": true, "ignoreAtOthers": true,
	})
	connector, err := newOneBotConnector(runtime, mgmtPlatform{
		ID: "xiaoman-qq-connector", Type: oneBotTransport,
		Settings: map[string]any{}, CredentialRefs: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal([]map[string]any{
		{"type": "at", "data": map[string]any{"qq": "4014088643", "name": "豆包"}},
		{"type": "text", "data": map[string]any{"text": " ？"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	event := oneBotEvent{
		PostType: "message", MessageType: "group", MessageID: json.Number("2001"),
		UserID: json.Number("80404674"), GroupID: json.Number("88"), SelfID: json.Number("3639159672"),
		Message: raw, Sender: oneBotSender{Nickname: "管理员"},
	}
	if err = connector.handleMessage(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if runs != 0 {
		t.Fatalf("@ other created %d runs", runs)
	}
	var disposition, reason string
	eventID := runtime.platformPseudonym("event", "xiaoman-qq-connector:2001")
	if err = runtime.db.QueryRow(`SELECT disposition, reason FROM agent_transport_events WHERE event_id = ?`, eventID).
		Scan(&disposition, &reason); err != nil {
		t.Fatal(err)
	}
	if disposition != "observe" || reason != "mention_other_ignored" {
		t.Fatalf("@ other audit = disposition=%q reason=%q", disposition, reason)
	}
	var observed int
	if err = runtime.db.QueryRow("SELECT count(*) FROM conversation_events").Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if observed != 0 {
		t.Fatalf("@ other polluted the current instance with %d memory events", observed)
	}
}

func TestOneBotHighConfidenceAdIsRetractedWithoutChatRun(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	if _, err := runtime.configStore.db.Exec(`UPDATE agent_instances SET enabled = 1 WHERE id = 'doubao-qq'`); err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(groupModerationConfig{
		Mode: "enforce", ExecutorConnectorID: "onebot-moderator", GroupIDs: []string{"88"}, ExemptAdmins: true, MinimumScore: 3,
	})
	if _, err := runtime.configStore.db.Exec(`INSERT INTO agent_instance_capabilities(instance_id, capability_id, enabled, config_json, created_at, updated_at)
		VALUES ('doubao-qq', ?, 1, ?, 'now', 'now')`, groupModerationCapabilityID, string(config)); err != nil {
		t.Fatal(err)
	}
	connector, err := newOneBotConnector(runtime, mgmtPlatform{ID: "onebot-moderator", Type: oneBotTransport, Settings: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(connector.handleWebSocket))
	defer server.Close()
	client, response, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http"), nil)
	if response != nil && response.Body != nil {
		response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.WriteJSON(map[string]any{
		"post_type": "message", "message_type": "group", "message_id": 3001,
		"user_id": 100, "group_id": 88, "self_id": 99,
		"sender":  map[string]any{"user_id": 100, "nickname": "https://ad.example/register"},
		"message": []map[string]any{{"type": "text", "data": map[string]any{"text": "24小时低价充值，为你服务"}}},
	}); err != nil {
		t.Fatal(err)
	}
	var request struct {
		Action string         `json:"action"`
		Params map[string]any `json:"params"`
		Echo   string         `json:"echo"`
	}
	if err = client.ReadJSON(&request); err != nil {
		t.Fatal(err)
	}
	if request.Action != "delete_msg" || fmt.Sprint(request.Params["message_id"]) != "3001" {
		t.Fatalf("moderation request = %+v", request)
	}
	if err = client.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request.Echo}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var retracted int
		_ = runtime.configStore.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'group_moderation_retracted'`).Scan(&retracted)
		if retracted == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	var runs, events, retracted int
	_ = runtime.db.QueryRow(`SELECT count(*) FROM agent_runs`).Scan(&runs)
	_ = runtime.db.QueryRow(`SELECT count(*) FROM agent_transport_events`).Scan(&events)
	_ = runtime.configStore.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = 'group_moderation_retracted'`).Scan(&retracted)
	if runs != 0 || events != 0 || retracted != 1 {
		t.Fatalf("moderated ad leaked into chat: runs=%d events=%d retracted=%d", runs, events, retracted)
	}
}

func TestDiscordGatewayInboundAndRESTOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	identified := make(chan map[string]any, 1)
	sent := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gateway":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			if err = conn.WriteJSON(map[string]any{"op": 10, "d": map[string]any{"heartbeat_interval": 60000}}); err != nil {
				t.Error(err)
				return
			}
			var identify map[string]any
			if err = conn.ReadJSON(&identify); err != nil {
				t.Error(err)
				return
			}
			identified <- identify
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": 1, "t": "READY", "d": map[string]any{
				"user": map[string]any{"id": "99", "username": "doubao", "bot": true},
			}})
			_ = conn.WriteJSON(map[string]any{"op": 0, "s": 2, "t": "MESSAGE_CREATE", "d": map[string]any{
				"id": "m-1", "channel_id": "channel-1", "guild_id": "guild-1",
				"content": "<@99> 帮我看看", "timestamp": "2026-08-04T00:00:00Z",
				"author":   map[string]any{"id": "42", "username": "admin", "global_name": "群管理"},
				"mentions": []map[string]any{{"id": "99", "username": "doubao", "bot": true}},
			}})
			for {
				var ignored map[string]any
				if err = conn.ReadJSON(&ignored); err != nil {
					return
				}
			}
		case "/api/v10/channels/channel-1/messages":
			if r.Header.Get("Authorization") != "Bot DISCORD" {
				t.Errorf("Discord authorization = %q", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			sent <- payload
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"m-2"}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_DISCORD_TEST_TOKEN", "DISCORD")
	connector, err := newDiscordConnector(runtime, mgmtPlatform{
		ID: "discord-primary", Type: discordTransport,
		Settings: map[string]any{
			"discord_api_base_url": server.URL + "/api/v10",
			"discord_gateway_url":  "ws" + strings.TrimPrefix(server.URL, "http") + "/gateway",
			"admin_ids":            "42",
		},
		CredentialRefs: map[string]any{"discord_token": "ERDAI_DISCORD_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	select {
	case identify := <-identified:
		if fmt.Sprint(identify["op"]) != "2" {
			t.Fatalf("Discord identify = %+v", identify)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Discord did not identify")
	}
	eventID := runtime.platformPseudonym("event", "discord-primary:m-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	if err = runtime.db.QueryRow("SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?", eventID).Scan(&replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	if !isAdmin {
		t.Fatal("Discord administrator flag was not preserved")
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.ChannelID != "channel-1" || route.GuildID != "guild-1" || route.MessageID != "m-1" {
		t.Fatalf("Discord route = %+v: %v", route, err)
	}
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{
		Message: transportDeliveryMessage{Text: "看过了。"},
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case payload := <-sent:
		if payload["content"] != "看过了。" {
			t.Fatalf("Discord outbound payload = %+v", payload)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Discord outbound message was not sent")
	}
}

func TestKookGatewayInboundGroupAndPrivateWithRESTMediaOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	type sentMessage struct {
		Path    string
		Payload map[string]any
	}
	sent := make(chan sentMessage, 4)
	uploaded := make(chan bool, 1)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bot KOOK" && r.URL.Path != "/gateway" {
			t.Errorf("KOOK authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v3/user/me":
			_, _ = w.Write([]byte(`{"code":0,"message":"","data":{"id":"99","username":"doubao","nickname":"豆包"}}`))
		case "/api/v3/gateway/index":
			_, _ = w.Write([]byte(`{"code":0,"message":"","data":{"url":"` + "ws" + strings.TrimPrefix(server.URL, "http") + `/gateway"}}`))
		case "/gateway":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			_ = conn.WriteJSON(map[string]any{"s": 1, "d": map[string]any{"code": 0, "session_id": "session-1"}})
			group := map[string]any{"s": 0, "sn": 1, "d": map[string]any{
				"channel_type": "GROUP", "type": 9, "target_id": "channel-1", "author_id": "42",
				"content": "(met)99(met) 帮我看看", "msg_id": "kook-group-1", "msg_timestamp": int64(1785772800000),
				"extra": map[string]any{
					"guild_id": "guild-1", "mention": []string{"99"}, "mention_all": false,
					"author":    map[string]any{"id": "42", "username": "admin", "nickname": "群管理", "bot": false},
					"kmarkdown": map[string]any{"raw_content": "@豆包 帮我看看", "mention_part": []map[string]any{{"id": "99", "username": "豆包"}}},
				},
			}}
			var compressed bytes.Buffer
			writer := zlib.NewWriter(&compressed)
			_ = json.NewEncoder(writer).Encode(group)
			_ = writer.Close()
			_ = conn.WriteMessage(websocket.BinaryMessage, compressed.Bytes())
			_ = conn.WriteJSON(map[string]any{"s": 0, "sn": 2, "d": map[string]any{
				"channel_type": "PERSON", "type": 9, "target_id": "99", "author_id": "43",
				"content": "私聊一下", "msg_id": "kook-private-1", "msg_timestamp": int64(1785772800000),
				"extra": map[string]any{
					"mention": []string{}, "mention_all": false,
					"author":    map[string]any{"id": "43", "username": "friend", "nickname": "朋友", "bot": false},
					"kmarkdown": map[string]any{"raw_content": "私聊一下", "mention_part": []map[string]any{}},
				},
			}})
			for {
				var ignored map[string]any
				if err = conn.ReadJSON(&ignored); err != nil {
					return
				}
			}
		case "/api/v3/message/create", "/api/v3/direct-message/create":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			sent <- sentMessage{Path: r.URL.Path, Payload: payload}
			_, _ = w.Write([]byte(`{"code":0,"message":"","data":{"msg_id":"sent-1"}}`))
		case "/api/v3/asset/create":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
			} else {
				file.Close()
				uploaded <- true
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"","data":{"url":"https://assets.example/result.png"}}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_KOOK_TEST_TOKEN", "KOOK")
	connector, err := newKookConnector(runtime, mgmtPlatform{
		ID: "kook-primary", Type: kookTransport,
		Settings: map[string]any{
			"kook_api_base_url": server.URL + "/api/v3", "kook_heartbeat_interval": float64(60),
			"admin_ids": "42",
		},
		CredentialRefs: map[string]any{"kook_bot_token": "ERDAI_KOOK_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()

	groupEventID := runtime.platformPseudonym("event", "kook-primary:kook-group-1")
	privateEventID := runtime.platformPseudonym("event", "kook-primary:kook-private-1")
	waitForPlatformRun(t, runtime, groupEventID)
	waitForPlatformRun(t, runtime, privateEventID)
	var groupReply, privateReply string
	var groupAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, groupEventID).Scan(&groupReply, &groupAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !groupAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("KOOK group run = admin=%v input=%q err=%v", groupAdmin, plain, err)
	}
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, privateEventID).Scan(&privateReply); err != nil {
		t.Fatal(err)
	}
	groupRoute, err := runtime.platformRoute(context.Background(), groupReply)
	if err != nil || groupRoute.Kind != "group" || groupRoute.TargetID != "channel-1" || groupRoute.GuildID != "guild-1" {
		t.Fatalf("KOOK group route = %+v: %v", groupRoute, err)
	}
	privateRoute, err := runtime.platformRoute(context.Background(), privateReply)
	if err != nil || privateRoute.Kind != "private" || privateRoute.TargetID != "43" {
		t.Fatalf("KOOK private route = %+v: %v", privateRoute, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-kook-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), groupRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	first := <-sent
	second := <-sent
	if first.Path != "/api/v3/message/create" || first.Payload["content"] != "看过了。" || fmt.Sprint(first.Payload["quote"]) != "kook-group-1" {
		t.Fatalf("KOOK group text = %+v", first)
	}
	if second.Path != "/api/v3/message/create" || second.Payload["content"] != "https://assets.example/result.png" || fmt.Sprint(second.Payload["type"]) != "2" {
		t.Fatalf("KOOK group media = %+v", second)
	}
	select {
	case <-uploaded:
	case <-time.After(3 * time.Second):
		t.Fatal("KOOK media was not uploaded")
	}
	if err = connector.Deliver(context.Background(), privateRoute, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "说吧。"}}); err != nil {
		t.Fatal(err)
	}
	privateSent := <-sent
	if privateSent.Path != "/api/v3/direct-message/create" || privateSent.Payload["target_id"] != "43" {
		t.Fatalf("KOOK private text = %+v", privateSent)
	}
}

func TestMattermostWebSocketInboundAndRESTMediaOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	authenticated := make(chan map[string]any, 1)
	posts := make(chan map[string]any, 3)
	uploaded := make(chan bool, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer MATTERMOST" {
			t.Errorf("Mattermost authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/users/me":
			_, _ = w.Write([]byte(`{"id":"bot-99","username":"doubao"}`))
		case "/api/v4/websocket":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			var auth map[string]any
			if err = conn.ReadJSON(&auth); err != nil {
				t.Error(err)
				return
			}
			authenticated <- auth
			groupPost, _ := json.Marshal(map[string]any{
				"id": "mm-group-1", "channel_id": "channel-1", "user_id": "42",
				"message": "@doubao 帮我看看", "type": "", "root_id": "", "create_at": int64(1785772800000),
			})
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{
				"post": string(groupPost), "channel_type": "O", "sender_name": "admin",
			}})
			privatePost, _ := json.Marshal(map[string]any{
				"id": "mm-private-1", "channel_id": "dm-1", "user_id": "43",
				"message": "私聊一下", "type": "", "root_id": "", "create_at": int64(1785772800000),
			})
			_ = conn.WriteJSON(map[string]any{"event": "posted", "data": map[string]any{
				"post": string(privatePost), "channel_type": "D", "sender_name": "friend",
			}})
			for {
				var ignored map[string]any
				if err = conn.ReadJSON(&ignored); err != nil {
					return
				}
			}
		case "/api/v4/files":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
			}
			file, _, err := r.FormFile("files")
			if err != nil {
				t.Error(err)
			} else {
				file.Close()
				uploaded <- true
			}
			_, _ = w.Write([]byte(`{"file_infos":[{"id":"file-1"}]}`))
		case "/api/v4/posts":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			posts <- payload
			_, _ = w.Write([]byte(`{"id":"sent-1"}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_MATTERMOST_TEST_TOKEN", "MATTERMOST")
	connector, err := newMattermostConnector(runtime, mgmtPlatform{
		ID: "mattermost-primary", Type: mattermostTransport,
		Settings:       map[string]any{"mattermost_url": server.URL, "mattermost_reconnect_delay": float64(60), "admin_ids": "42"},
		CredentialRefs: map[string]any{"mattermost_bot_token": "ERDAI_MATTERMOST_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	select {
	case auth := <-authenticated:
		data, _ := auth["data"].(map[string]any)
		if auth["action"] != "authentication_challenge" || data["token"] != "MATTERMOST" {
			t.Fatalf("Mattermost auth challenge = %+v", auth)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Mattermost did not authenticate")
	}

	groupEventID := runtime.platformPseudonym("event", "mattermost-primary:mm-group-1")
	privateEventID := runtime.platformPseudonym("event", "mattermost-primary:mm-private-1")
	waitForPlatformRun(t, runtime, groupEventID)
	waitForPlatformRun(t, runtime, privateEventID)
	var groupReply, privateReply string
	var groupAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, groupEventID).Scan(&groupReply, &groupAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !groupAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("Mattermost group run = admin=%v input=%q err=%v", groupAdmin, plain, err)
	}
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, privateEventID).Scan(&privateReply); err != nil {
		t.Fatal(err)
	}
	groupRoute, err := runtime.platformRoute(context.Background(), groupReply)
	if err != nil || groupRoute.TargetID != "channel-1" || groupRoute.MessageID != "mm-group-1" {
		t.Fatalf("Mattermost group route = %+v: %v", groupRoute, err)
	}
	privateRoute, err := runtime.platformRoute(context.Background(), privateReply)
	if err != nil || privateRoute.TargetID != "dm-1" {
		t.Fatalf("Mattermost private route = %+v: %v", privateRoute, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-mattermost-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), groupRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-uploaded:
	case <-time.After(3 * time.Second):
		t.Fatal("Mattermost media was not uploaded")
	}
	groupPost := <-posts
	fileIDs, _ := groupPost["file_ids"].([]any)
	if groupPost["channel_id"] != "channel-1" || groupPost["message"] != "看过了。" || groupPost["root_id"] != "mm-group-1" || len(fileIDs) != 1 || fileIDs[0] != "file-1" {
		t.Fatalf("Mattermost group post = %+v", groupPost)
	}
	if err = connector.Deliver(context.Background(), privateRoute, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "说吧。"}}); err != nil {
		t.Fatal(err)
	}
	privatePost := <-posts
	if privatePost["channel_id"] != "dm-1" || privatePost["message"] != "说吧。" {
		t.Fatalf("Mattermost private post = %+v", privatePost)
	}
}

func TestMisskeyStreamingInboundAndNotesChatMediaOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	type apiCall struct {
		Path    string
		Payload map[string]any
	}
	calls := make(chan apiCall, 3)
	uploaded := make(chan bool, 1)
	subscribed := make(chan map[string]string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/streaming" && r.Header.Get("Authorization") != "Bearer MISSKEY" {
			t.Errorf("Misskey authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/i":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			if payload["i"] != "MISSKEY" {
				t.Errorf("Misskey identity payload = %+v", payload)
			}
			_, _ = w.Write([]byte(`{"id":"bot-99","username":"doubao","name":"豆包"}`))
		case "/streaming":
			if r.URL.Query().Get("i") != "MISSKEY" {
				t.Errorf("Misskey streaming token = %q", r.URL.Query().Get("i"))
			}
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			channels := map[string]string{}
			for len(channels) < 3 {
				var request struct {
					Type string `json:"type"`
					Body struct {
						Channel string `json:"channel"`
						ID      string `json:"id"`
					} `json:"body"`
				}
				if err = conn.ReadJSON(&request); err != nil {
					t.Error(err)
					return
				}
				channels[request.Body.Channel] = request.Body.ID
			}
			subscribed <- channels
			_ = conn.WriteJSON(map[string]any{"type": "channel", "body": map[string]any{
				"id": channels["main"], "type": "notification", "body": map[string]any{
					"type": "mention", "note": map[string]any{
						"id": "mk-note-1", "text": "@doubao 帮我看看", "userId": "42",
						"user":     map[string]any{"id": "42", "username": "admin", "name": "群管理"},
						"mentions": []string{"bot-99"}, "visibility": "home", "visibleUserIds": []string{},
						"createdAt": "2026-08-04T00:00:00Z", "files": []map[string]any{},
					},
				},
			}})
			_ = conn.WriteJSON(map[string]any{"type": "channel", "body": map[string]any{
				"id": channels["messaging"], "type": "newChatMessage", "body": map[string]any{
					"id": "mk-room-1", "text": "@doubao 房间聊聊", "fromUserId": "43", "toRoomId": "room-1",
					"fromUser":  map[string]any{"id": "43", "username": "friend", "name": "朋友"},
					"createdAt": "2026-08-04T00:00:00Z", "files": []map[string]any{},
				},
			}})
			for {
				var ignored map[string]any
				if err = conn.ReadJSON(&ignored); err != nil {
					return
				}
			}
		case "/api/drive/files/create":
			if err := r.ParseMultipartForm(2 << 20); err != nil {
				t.Error(err)
			}
			if r.FormValue("i") != "MISSKEY" {
				t.Errorf("Misskey upload token = %q", r.FormValue("i"))
			}
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Error(err)
			} else {
				file.Close()
				uploaded <- true
			}
			_, _ = w.Write([]byte(`{"id":"drive-1","name":"result.png"}`))
		case "/api/notes/create", "/api/chat/messages/create-to-room":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			calls <- apiCall{Path: r.URL.Path, Payload: payload}
			_, _ = w.Write([]byte(`{"id":"sent-1"}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_MISSKEY_TEST_TOKEN", "MISSKEY")
	connector, err := newMisskeyConnector(runtime, mgmtPlatform{
		ID: "misskey-primary", Type: misskeyTransport,
		Settings: map[string]any{
			"misskey_instance_url": server.URL, "misskey_default_visibility": "home",
			"misskey_enable_chat": true, "misskey_enable_file_upload": true, "max_message_length": float64(3000),
			"admin_ids": "42",
		},
		CredentialRefs: map[string]any{"misskey_token": "ERDAI_MISSKEY_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	select {
	case channels := <-subscribed:
		if channels["main"] == "" || channels["messaging"] == "" || channels["messagingIndex"] == "" {
			t.Fatalf("Misskey subscriptions = %+v", channels)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Misskey did not subscribe")
	}

	noteEventID := runtime.platformPseudonym("event", "misskey-primary:mk-note-1")
	roomEventID := runtime.platformPseudonym("event", "misskey-primary:mk-room-1")
	waitForPlatformRun(t, runtime, noteEventID)
	waitForPlatformRun(t, runtime, roomEventID)
	var noteReply, roomReply string
	var noteAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, noteEventID).Scan(&noteReply, &noteAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !noteAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("Misskey note run = admin=%v input=%q err=%v", noteAdmin, plain, err)
	}
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, roomEventID).Scan(&roomReply); err != nil {
		t.Fatal(err)
	}
	noteRoute, err := runtime.platformRoute(context.Background(), noteReply)
	if err != nil || noteRoute.Kind != "note" || noteRoute.TargetID != "42" || noteRoute.MessageID != "mk-note-1" {
		t.Fatalf("Misskey note route = %+v: %v", noteRoute, err)
	}
	roomRoute, err := runtime.platformRoute(context.Background(), roomReply)
	if err != nil || roomRoute.Kind != "room" || roomRoute.TargetID != "room-1" {
		t.Fatalf("Misskey room route = %+v: %v", roomRoute, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-misskey-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), noteRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-uploaded:
	case <-time.After(3 * time.Second):
		t.Fatal("Misskey media was not uploaded")
	}
	noteCall := <-calls
	fileIDs, _ := noteCall.Payload["fileIds"].([]any)
	if noteCall.Path != "/api/notes/create" || noteCall.Payload["text"] != "看过了。" || noteCall.Payload["replyId"] != "mk-note-1" || noteCall.Payload["visibility"] != "home" || len(fileIDs) != 1 || fileIDs[0] != "drive-1" {
		t.Fatalf("Misskey note call = %+v", noteCall)
	}
	if err = connector.Deliver(context.Background(), roomRoute, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "接着说。"}}); err != nil {
		t.Fatal(err)
	}
	roomCall := <-calls
	if roomCall.Path != "/api/chat/messages/create-to-room" || roomCall.Payload["toRoomId"] != "room-1" || roomCall.Payload["text"] != "接着说。" {
		t.Fatalf("Misskey room call = %+v", roomCall)
	}
}

func TestSatoriProtocolInboundAndMessageCreateMediaOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	identified := make(chan map[string]any, 1)
	created := make(chan struct {
		Payload  map[string]any
		Platform string
		BotID    string
	}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/events":
			conn, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			var identify map[string]any
			if err = conn.ReadJSON(&identify); err != nil {
				t.Error(err)
				return
			}
			identified <- identify
			login := map[string]any{"platform": "qq", "user": map[string]any{"id": "bot-99", "name": "豆包"}}
			_ = conn.WriteJSON(map[string]any{"op": 4, "body": map[string]any{"sn": 1, "logins": []any{login}}})
			_ = conn.WriteJSON(map[string]any{"op": 0, "body": map[string]any{
				"type": "message-created", "sn": 2, "timestamp": int64(1785772800000),
				"message": map[string]any{"id": "satori-group-1", "content": `<at id="bot-99"/>帮我看看<img src="https://img.example/in.png"/>`},
				"user":    map[string]any{"id": "42", "name": "admin", "nick": "群管理"},
				"channel": map[string]any{"id": "channel-1", "type": 0}, "guild": map[string]any{"id": "guild-1"}, "login": login,
			}})
			_ = conn.WriteJSON(map[string]any{"op": 0, "body": map[string]any{
				"type": "message-created", "sn": 3, "timestamp": int64(1785772800000),
				"message": map[string]any{"id": "satori-private-1", "content": "私聊一下"},
				"user":    map[string]any{"id": "43", "name": "friend", "nick": "朋友"},
				"channel": map[string]any{"id": "dm-1", "type": 1}, "guild": nil, "login": login,
			}})
			for {
				var ignored map[string]any
				if err = conn.ReadJSON(&ignored); err != nil {
					return
				}
			}
		case "/api/message.create":
			if r.Header.Get("Authorization") != "Bearer SATORI" {
				t.Errorf("Satori authorization = %q", r.Header.Get("Authorization"))
			}
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			created <- struct {
				Payload  map[string]any
				Platform string
				BotID    string
			}{payload, r.Header.Get("satori-platform"), r.Header.Get("satori-user-id")}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"sent-1"}]`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("ERDAI_SATORI_TEST_TOKEN", "SATORI")
	connector, err := newSatoriConnector(runtime, mgmtPlatform{
		ID: "satori-primary", Type: satoriTransport,
		Settings: map[string]any{
			"satori_api_base_url": server.URL + "/api", "satori_endpoint": "ws" + strings.TrimPrefix(server.URL, "http") + "/events",
			"satori_auto_reconnect": true, "satori_heartbeat_interval": float64(60), "satori_reconnect_delay": float64(60),
			"admin_ids": "42",
		},
		CredentialRefs: map[string]any{"satori_token": "ERDAI_SATORI_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	select {
	case identify := <-identified:
		body, _ := identify["body"].(map[string]any)
		if fmt.Sprint(identify["op"]) != "3" || body["token"] != "SATORI" {
			t.Fatalf("Satori identify = %+v", identify)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Satori did not identify")
	}

	groupEventID := runtime.platformPseudonym("event", "satori-primary:satori-group-1")
	privateEventID := runtime.platformPseudonym("event", "satori-primary:satori-private-1")
	waitForPlatformRun(t, runtime, groupEventID)
	waitForPlatformRun(t, runtime, privateEventID)
	var groupReply, privateReply string
	var groupAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, groupEventID).Scan(&groupReply, &groupAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !groupAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("Satori group run = admin=%v input=%q err=%v", groupAdmin, plain, err)
	}
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, privateEventID).Scan(&privateReply); err != nil {
		t.Fatal(err)
	}
	groupRoute, err := runtime.platformRoute(context.Background(), groupReply)
	if err != nil || groupRoute.TargetID != "channel-1" || groupRoute.GuildID != "qq" || groupRoute.ChannelID != "bot-99" {
		t.Fatalf("Satori group route = %+v: %v", groupRoute, err)
	}
	privateRoute, err := runtime.platformRoute(context.Background(), privateReply)
	if err != nil || privateRoute.TargetID != "dm-1" {
		t.Fatalf("Satori private route = %+v: %v", privateRoute, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-satori-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), groupRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-created:
		content := fmt.Sprint(call.Payload["content"])
		if call.Payload["channel_id"] != "channel-1" || call.Platform != "qq" || call.BotID != "bot-99" ||
			!strings.Contains(content, `<reply id="satori-group-1"/>看过了。`) || !strings.Contains(content, `<img src="data:image/png;base64,`) {
			t.Fatalf("Satori message.create = %+v headers=%q/%q", call.Payload, call.Platform, call.BotID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Satori message.create was not called")
	}
}

func TestLineSignedWebhookInboundReplyAndMediaProxyOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	sent := make(chan map[string]any, 1)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer LINE-TOKEN" {
			t.Errorf("LINE authorization = %q", r.Header.Get("Authorization"))
		}
		switch r.URL.Path {
		case "/v2/bot/message/reply", "/v2/bot/message/push":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			payload["_path"] = r.URL.Path
			sent <- payload
			w.WriteHeader(http.StatusOK)
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer apiServer.Close()
	t.Setenv("ERDAI_LINE_TEST_TOKEN", "LINE-TOKEN")
	t.Setenv("ERDAI_LINE_TEST_SECRET", "LINE-SECRET")
	t.Setenv("ERDAI_LINE_TEST_WEBHOOK", "line-hook")
	connector, err := newLineConnector(runtime, mgmtPlatform{
		ID: "line-primary", Type: lineTransport,
		Settings: map[string]any{
			"callback_server_host": "127.0.0.1", "port": float64(6193),
			"line_api_base_url": apiServer.URL, "line_data_base_url": apiServer.URL,
			"line_public_base_url": "", "line_video_preview_url": "", "admin_ids": "42",
		},
		CredentialRefs: map[string]any{
			"channel_access_token": "ERDAI_LINE_TEST_TOKEN", "channel_secret": "ERDAI_LINE_TEST_SECRET", "webhook_uuid": "ERDAI_LINE_TEST_WEBHOOK",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.port = 0
	connector.client = apiServer.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	connector.publicBase = "http://" + connector.server.Address()

	payload := map[string]any{
		"destination": "line-bot", "events": []map[string]any{{
			"type": "message", "mode": "active", "webhookEventId": "line-event-1", "timestamp": int64(1785772800000), "replyToken": "reply-token-1",
			"source": map[string]any{"type": "group", "userId": "42", "groupId": "group-1"},
			"message": map[string]any{
				"id": "line-message-1", "type": "text", "text": "@豆包 帮我看看",
				"mention": map[string]any{"mentionees": []map[string]any{{"index": 0, "length": 3, "type": "user", "userId": "line-bot", "isSelf": true}}},
			},
		}},
	}
	body, _ := json.Marshal(payload)
	request, _ := http.NewRequest(http.MethodPost, "http://"+connector.server.Address()+connector.webhookPath(), bytes.NewReader(body))
	mac := hmac.New(sha256.New, []byte("LINE-SECRET"))
	_, _ = mac.Write(body)
	request.Header.Set("X-Line-Signature", base64.StdEncoding.EncodeToString(mac.Sum(nil)))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("LINE webhook status = %d", response.StatusCode)
	}

	eventID := runtime.platformPseudonym("event", "line-primary:line-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	plain, err := runtime.decrypt(inputCipher)
	if err != nil || !isAdmin || strings.TrimSpace(string(plain)) != "帮我看看" {
		t.Fatalf("LINE run = admin=%v input=%q err=%v", isAdmin, plain, err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.TargetID != "group-1" || route.ChannelID != "reply-token-1" || route.GuildID != "line-bot" {
		t.Fatalf("LINE route = %+v: %v", route, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-line-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png", MimeType: "image/png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	select {
	case call := <-sent:
		messages, _ := call["messages"].([]any)
		if call["_path"] != "/v2/bot/message/reply" || call["replyToken"] != "reply-token-1" || len(messages) != 2 {
			t.Fatalf("LINE reply = %+v", call)
		}
		imageMessage, _ := messages[1].(map[string]any)
		mediaURL := fmt.Sprint(imageMessage["originalContentUrl"])
		mediaResponse, fetchErr := http.Get(mediaURL)
		if fetchErr != nil {
			t.Fatal(fetchErr)
		}
		mediaBody, _ := io.ReadAll(mediaResponse.Body)
		mediaResponse.Body.Close()
		if mediaResponse.StatusCode != http.StatusOK || string(mediaBody) != "png-data" {
			t.Fatalf("LINE media proxy = %d %q", mediaResponse.StatusCode, mediaBody)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("LINE reply was not sent")
	}

	badRequest, _ := http.NewRequest(http.MethodPost, "http://"+connector.server.Address()+connector.webhookPath(), bytes.NewReader(body))
	badRequest.Header.Set("X-Line-Signature", "bad")
	badResponse, err := http.DefaultClient.Do(badRequest)
	if err != nil {
		t.Fatal(err)
	}
	badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("LINE invalid signature status = %d", badResponse.StatusCode)
	}
}

func TestQQOfficialWebhookChallengeSignedInboundDedupeAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
		"mode": "active", "captureUnaddressedGroups": true,
	})
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "initialProbability": 1.0, "afterReplyProbability": 1.0,
		"proactiveChatEnabled": true, "messageQualityEnabled": true, "replyDensityEnabled": false,
	})
	t.Setenv("ERDAI_QQ_WEBHOOK_TEST_SECRET", "qq-webhook-secret")
	t.Setenv("ERDAI_QQ_WEBHOOK_TEST_PATH", "qq-webhook-path")
	connector, err := newQQOfficialWebhookConnector(runtime, mgmtPlatform{
		ID: "qq-webhook-primary", Type: qqOfficialWebhookTransport,
		Settings: map[string]any{
			"appid": "1024", "callback_server_host": "127.0.0.1", "port": float64(6196),
			"admin_openids": "admin-openid", "enable_group_c2c": true, "enable_guild_direct_message": true,
		},
		CredentialRefs: map[string]any{
			"secret": "ERDAI_QQ_WEBHOOK_TEST_SECRET", "webhook_uuid": "ERDAI_QQ_WEBHOOK_TEST_PATH",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	fakeAPI := &fakeQQOfficialAPI{}
	connector.qq.api = fakeAPI
	connector.port = 0
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	endpoint := "http://" + connector.server.Address() + connector.webhookPath()

	challengeBody, _ := json.Marshal(map[string]any{
		"op": 13, "d": map[string]any{"event_ts": "1785772800", "plain_token": "plain-token"},
	})
	challengeResponse := postQQWebhook(t, endpoint, challengeBody, "", "")
	if challengeResponse.StatusCode != http.StatusOK {
		challengeResponse.Body.Close()
		t.Fatalf("QQ webhook challenge status = %d", challengeResponse.StatusCode)
	}
	var challenge map[string]string
	if err = json.NewDecoder(challengeResponse.Body).Decode(&challenge); err != nil {
		challengeResponse.Body.Close()
		t.Fatal(err)
	}
	challengeResponse.Body.Close()
	privateKey, _ := qqWebhookPrivateKey("qq-webhook-secret")
	challengeSignature, err := hex.DecodeString(challenge["signature"])
	if err != nil || challenge["plain_token"] != "plain-token" ||
		!ed25519.Verify(privateKey.Public().(ed25519.PublicKey), []byte("1785772800plain-token"), challengeSignature) {
		t.Fatalf("QQ webhook challenge response = %+v: %v", challenge, err)
	}

	groupBody, _ := json.Marshal(map[string]any{
		"id": "webhook-event-group", "op": 0, "t": "GROUP_AT_MESSAGE_CREATE",
		"d": map[string]any{
			"id": "webhook-group-message", "group_openid": "group-openid", "content": "豆包，看看这个",
			"timestamp": "2026-08-04T00:00:00+00:00",
			"author":    map[string]any{"member_openid": "admin-openid", "username": "管理员"},
		},
	})
	timestamp := "1785772801"
	signature := signQQWebhookTestPayload(t, "qq-webhook-secret", timestamp, groupBody)
	groupResponse := postQQWebhook(t, endpoint, groupBody, timestamp, signature)
	if groupResponse.StatusCode != http.StatusOK {
		groupResponse.Body.Close()
		t.Fatalf("QQ webhook group status = %d", groupResponse.StatusCode)
	}
	groupResponse.Body.Close()
	groupEventID := runtime.platformPseudonym("event", "qq-webhook-primary:webhook-group-message")
	waitForPlatformRun(t, runtime, groupEventID)
	var replyHandle, transport string
	var isAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, transport, is_admin FROM agent_runs WHERE event_id = ?`, groupEventID).
		Scan(&replyHandle, &transport, &isAdmin); err != nil {
		t.Fatal(err)
	}
	groupRoute, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || transport != qqOfficialWebhookTransport || !isAdmin || groupRoute.Kind != "group" || groupRoute.TargetID != "group-openid" {
		t.Fatalf("QQ webhook group route = %+v transport=%q admin=%v: %v", groupRoute, transport, isAdmin, err)
	}

	duplicateResponse := postQQWebhook(t, endpoint, groupBody, timestamp, signature)
	duplicateResponse.Body.Close()
	var duplicateRuns int
	if err = runtime.db.QueryRow(`SELECT count(*) FROM agent_runs WHERE event_id = ?`, groupEventID).Scan(&duplicateRuns); err != nil || duplicateRuns != 1 {
		t.Fatalf("QQ webhook duplicate runs = %d: %v", duplicateRuns, err)
	}

	plainBody, _ := json.Marshal(map[string]any{
		"id": "webhook-event-plain", "op": 0, "t": "GROUP_MESSAGE_CREATE",
		"d": map[string]any{
			"id": "webhook-plain-message", "group_openid": "group-openid", "content": "这个方案靠谱吗？",
			"author": map[string]any{"member_openid": "member-openid", "username": "群友"},
		},
	})
	plainSignature := signQQWebhookTestPayload(t, "qq-webhook-secret", timestamp, plainBody)
	plainResponse := postQQWebhook(t, endpoint, plainBody, timestamp, plainSignature)
	plainResponse.Body.Close()
	plainEventID := runtime.platformPseudonym("event", "qq-webhook-primary:webhook-plain-message")
	waitForPlatformRun(t, runtime, plainEventID)
	var plainReply string
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, plainEventID).Scan(&plainReply); err != nil {
		t.Fatal(err)
	}
	plainRoute, err := runtime.platformRoute(context.Background(), plainReply)
	if err != nil || plainRoute.Kind != "group" {
		t.Fatalf("QQ webhook plain route = %+v: %v", plainRoute, err)
	}

	c2cBody, _ := json.Marshal(map[string]any{
		"id": "webhook-event-c2c", "op": 0, "t": "C2C_MESSAGE_CREATE",
		"d": map[string]any{
			"id": "webhook-c2c-message", "content": "/帮助",
			"author": map[string]any{"user_openid": "friend-openid", "username": "朋友"},
		},
	})
	c2cSignature := signQQWebhookTestPayload(t, "qq-webhook-secret", timestamp, c2cBody)
	c2cResponse := postQQWebhook(t, endpoint, c2cBody, timestamp, c2cSignature)
	c2cResponse.Body.Close()
	c2cEventID := runtime.platformPseudonym("event", "qq-webhook-primary:webhook-c2c-message")
	waitForPlatformRun(t, runtime, c2cEventID)
	var c2cReply string
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, c2cEventID).Scan(&c2cReply); err != nil {
		t.Fatal(err)
	}
	c2cRoute, err := runtime.platformRoute(context.Background(), c2cReply)
	if err != nil || c2cRoute.Kind != "c2c" || c2cRoute.TargetID != "friend-openid" {
		t.Fatalf("QQ webhook C2C route = %+v: %v", c2cRoute, err)
	}
	if err = connector.Deliver(context.Background(), c2cRoute, leasedTransportDelivery{
		ReplyHandle: c2cReply, Message: transportDeliveryMessage{Text: "看到了。"},
	}); err != nil {
		t.Fatal(err)
	}
	if len(fakeAPI.c2cMessages) != 1 {
		t.Fatalf("QQ webhook C2C sends = %d", len(fakeAPI.c2cMessages))
	}

	invalidResponse := postQQWebhook(t, endpoint, groupBody, timestamp, "bad-signature")
	invalidResponse.Body.Close()
	if invalidResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("QQ webhook invalid signature status = %d", invalidResponse.StatusCode)
	}
}

func TestWeComEncryptedWebhookAppKFMediaAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	type apiCall struct {
		Path    string
		Payload map[string]any
	}
	calls := make(chan apiCall, 8)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/gettoken":
			_, _ = w.Write([]byte(`{"errcode":0,"access_token":"WECOM-TOKEN","expires_in":7200}`))
		case "/cgi-bin/message/send", "/cgi-bin/kf/send_msg":
			if r.URL.Query().Get("access_token") != "WECOM-TOKEN" {
				t.Errorf("WeCom access token = %q", r.URL.Query().Get("access_token"))
			}
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			calls <- apiCall{Path: r.URL.Path, Payload: payload}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		case "/cgi-bin/media/upload":
			if err := r.ParseMultipartForm(2 << 20); err != nil || r.MultipartForm.File["media"] == nil {
				t.Errorf("WeCom upload form: %v", err)
			}
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"WECOM-MEDIA"}`))
		case "/cgi-bin/media/get":
			w.Header().Set("Content-Type", "audio/amr")
			_, _ = w.Write([]byte("wecom-media-bytes"))
		case "/cgi-bin/kf/sync_msg":
			_, _ = w.Write([]byte(`{"errcode":0,"has_more":0,"msg_list":[{"msgid":"kf-message-1","msgtype":"text","open_kfid":"open-kf-1","external_userid":"external-1","send_time":1785772800,"text":{"content":"客服消息"}}]}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer apiServer.Close()
	aesKey := strings.TrimRight(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)), "=")
	t.Setenv("ERDAI_WECOM_TEST_SECRET", "wecom-secret")
	t.Setenv("ERDAI_WECOM_TEST_TOKEN", "wecom-callback-token")
	t.Setenv("ERDAI_WECOM_TEST_AES", aesKey)
	t.Setenv("ERDAI_WECOM_TEST_WEBHOOK", "wecom-hook")
	connector, err := newWecomConnector(runtime, mgmtPlatform{
		ID: "wecom-primary", Type: wecomTransport,
		Settings: map[string]any{
			"corpid": "corp-1", "agent_id": "100001", "api_base_url": apiServer.URL + "/cgi-bin",
			"callback_server_host": "127.0.0.1", "port": float64(6195), "wecom_public_base_url": "", "admin_ids": "admin-user",
		},
		CredentialRefs: map[string]any{
			"secret": "ERDAI_WECOM_TEST_SECRET", "token": "ERDAI_WECOM_TEST_TOKEN",
			"encoding_aes_key": "ERDAI_WECOM_TEST_AES", "webhook_uuid": "ERDAI_WECOM_TEST_WEBHOOK",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.port = 0
	connector.client = apiServer.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	connector.publicBase = "http://" + connector.server.Address()
	endpoint := "http://" + connector.server.Address() + connector.webhookPath()
	timestamp, nonce := "1785772800", "nonce-one"

	echoEnvelope, echoSignature := encryptedWechatTestEnvelope(t, connector.crypto, []byte("echo-ok"), timestamp, nonce)
	echoEncrypted, _ := wechatParseEncryptedBody(echoEnvelope)
	echoRequest, _ := http.NewRequest(http.MethodGet, endpoint+"?echostr="+url.QueryEscape(echoEncrypted)+"&msg_signature="+echoSignature+"&timestamp="+timestamp+"&nonce="+nonce, nil)
	echoResponse, err := http.DefaultClient.Do(echoRequest)
	if err != nil {
		t.Fatal(err)
	}
	echoBody, _ := io.ReadAll(echoResponse.Body)
	echoResponse.Body.Close()
	if echoResponse.StatusCode != http.StatusOK || string(echoBody) != "echo-ok" {
		t.Fatalf("WeCom verify = %d %q", echoResponse.StatusCode, echoBody)
	}

	appXML := []byte(`<xml><ToUserName><![CDATA[corp-1]]></ToUserName><FromUserName><![CDATA[admin-user]]></FromUserName><CreateTime>1785772800</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[帮我看看]]></Content><MsgId>wecom-app-1</MsgId><AgentID>100001</AgentID></xml>`)
	appEnvelope, appSignature := encryptedWechatTestEnvelope(t, connector.crypto, appXML, timestamp, nonce)
	appResponse := postWechatTestEnvelope(t, endpoint, appEnvelope, appSignature, timestamp, nonce)
	appResponse.Body.Close()
	if appResponse.StatusCode != http.StatusOK {
		t.Fatalf("WeCom app webhook status = %d", appResponse.StatusCode)
	}
	appEventID := runtime.platformPseudonym("event", "wecom-primary:wecom-app-1")
	waitForPlatformRun(t, runtime, appEventID)
	var appReply string
	var appAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, appEventID).Scan(&appReply, &appAdmin); err != nil {
		t.Fatal(err)
	}
	appRoute, err := runtime.platformRoute(context.Background(), appReply)
	if err != nil || !appAdmin || appRoute.Kind != "app" || appRoute.TargetID != "admin-user" || appRoute.GuildID != "100001" {
		t.Fatalf("WeCom app route = %+v admin=%v: %v", appRoute, appAdmin, err)
	}

	voiceXML := []byte(`<xml><ToUserName><![CDATA[corp-1]]></ToUserName><FromUserName><![CDATA[user-voice]]></FromUserName><CreateTime>1785772801</CreateTime><MsgType><![CDATA[voice]]></MsgType><MediaId><![CDATA[VOICE-MEDIA]]></MediaId><MsgId>wecom-voice-1</MsgId><AgentID>100001</AgentID></xml>`)
	voiceEnvelope, voiceSignature := encryptedWechatTestEnvelope(t, connector.crypto, voiceXML, timestamp, nonce)
	voiceResponse := postWechatTestEnvelope(t, endpoint, voiceEnvelope, voiceSignature, timestamp, nonce)
	voiceResponse.Body.Close()
	voiceEventID := runtime.platformPseudonym("event", "wecom-primary:wecom-voice-1")
	waitForPlatformRun(t, runtime, voiceEventID)
	_, voiceAttachments := connector.parseInboundContent("voice", "", "", "VOICE-MEDIA", "")
	if len(voiceAttachments) != 1 || !strings.Contains(voiceAttachments[0].SourceURL, "/wecom-media/") {
		t.Fatalf("WeCom voice attachments = %+v", voiceAttachments)
	}
	mediaResponse, err := http.Get(voiceAttachments[0].SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	mediaBody, _ := io.ReadAll(mediaResponse.Body)
	mediaResponse.Body.Close()
	if mediaResponse.StatusCode != http.StatusOK || string(mediaBody) != "wecom-media-bytes" {
		t.Fatalf("WeCom media proxy = %d %q", mediaResponse.StatusCode, mediaBody)
	}

	kfXML := []byte(`<xml><ToUserName><![CDATA[corp-1]]></ToUserName><FromUserName><![CDATA[sys]]></FromUserName><CreateTime>1785772802</CreateTime><MsgType><![CDATA[event]]></MsgType><Event><![CDATA[kf_msg_or_event]]></Event><Token><![CDATA[sync-token]]></Token><OpenKfId><![CDATA[open-kf-1]]></OpenKfId></xml>`)
	kfEnvelope, kfSignature := encryptedWechatTestEnvelope(t, connector.crypto, kfXML, timestamp, nonce)
	kfResponse := postWechatTestEnvelope(t, endpoint, kfEnvelope, kfSignature, timestamp, nonce)
	kfResponse.Body.Close()
	kfEventID := runtime.platformPseudonym("event", "wecom-primary:kf-message-1")
	waitForPlatformRun(t, runtime, kfEventID)
	var kfReply string
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, kfEventID).Scan(&kfReply); err != nil {
		t.Fatal(err)
	}
	kfRoute, err := runtime.platformRoute(context.Background(), kfReply)
	if err != nil || kfRoute.Kind != "kf" || kfRoute.TargetID != "external-1" || kfRoute.GuildID != "open-kf-1" {
		t.Fatalf("WeCom KF route = %+v: %v", kfRoute, err)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-wecom-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), appRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if err = connector.Deliver(context.Background(), kfRoute, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "客服回复"}}); err != nil {
		t.Fatal(err)
	}
	seenApp, seenKF, seenImage := false, false, false
	for index := 0; index < 3; index++ {
		select {
		case call := <-calls:
			seenApp = seenApp || (call.Path == "/cgi-bin/message/send" && call.Payload["msgtype"] == "text")
			seenImage = seenImage || (call.Path == "/cgi-bin/message/send" && call.Payload["msgtype"] == "image")
			seenKF = seenKF || (call.Path == "/cgi-bin/kf/send_msg" && call.Payload["open_kfid"] == "open-kf-1")
		case <-time.After(3 * time.Second):
			t.Fatal("WeCom outbound API calls were not completed")
		}
	}
	if !seenApp || !seenImage || !seenKF {
		t.Fatalf("WeCom sends app=%v image=%v kf=%v", seenApp, seenImage, seenKF)
	}
}

func TestWeixinOfficialAccountEncryptedInboundActiveMediaAndPassiveReply(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	type apiCall struct {
		Path    string
		Payload map[string]any
	}
	calls := make(chan apiCall, 4)
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cgi-bin/token":
			_, _ = w.Write([]byte(`{"access_token":"WX-TOKEN","expires_in":7200}`))
		case "/cgi-bin/message/custom/send":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			calls <- apiCall{Path: r.URL.Path, Payload: payload}
			_, _ = w.Write([]byte(`{"errcode":0,"errmsg":"ok"}`))
		case "/cgi-bin/media/upload":
			_, _ = w.Write([]byte(`{"type":"image","media_id":"WX-MEDIA","created_at":1785772800}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer apiServer.Close()
	aesKey := strings.TrimRight(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)), "=")
	t.Setenv("ERDAI_WXOA_TEST_SECRET", "wx-secret")
	t.Setenv("ERDAI_WXOA_TEST_TOKEN", "wx-callback-token")
	t.Setenv("ERDAI_WXOA_TEST_AES", aesKey)
	t.Setenv("ERDAI_WXOA_TEST_WEBHOOK", "wx-hook")
	platform := mgmtPlatform{
		ID: "wxoa-primary", Type: weixinOfficialAccountTransport,
		Settings: map[string]any{
			"appid": "wx-app-1", "api_base_url": apiServer.URL + "/cgi-bin", "active_send_mode": true,
			"callback_server_host": "127.0.0.1", "port": float64(6194), "admin_ids": "wx-user",
		},
		CredentialRefs: map[string]any{
			"secret": "ERDAI_WXOA_TEST_SECRET", "token": "ERDAI_WXOA_TEST_TOKEN",
			"encoding_aes_key": "ERDAI_WXOA_TEST_AES", "webhook_uuid": "ERDAI_WXOA_TEST_WEBHOOK",
		},
	}
	connector, err := newWeixinOfficialAccountConnector(runtime, platform)
	if err != nil {
		t.Fatal(err)
	}
	connector.port = 0
	connector.client = apiServer.Client()
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	endpoint := "http://" + connector.server.Address() + connector.webhookPath()
	timestamp, nonce := "1785772800", "wx-nonce"
	plainSignature := wechatSignature("wx-callback-token", timestamp, nonce)
	verifyResponse, err := http.Get(endpoint + "?signature=" + plainSignature + "&timestamp=" + timestamp + "&nonce=" + nonce + "&echostr=verified")
	if err != nil {
		t.Fatal(err)
	}
	verifyBody, _ := io.ReadAll(verifyResponse.Body)
	verifyResponse.Body.Close()
	if verifyResponse.StatusCode != http.StatusOK || string(verifyBody) != "verified" {
		t.Fatalf("Weixin verify = %d %q", verifyResponse.StatusCode, verifyBody)
	}

	inboundXML := []byte(`<xml><ToUserName><![CDATA[wx-app-1]]></ToUserName><FromUserName><![CDATA[wx-user]]></FromUserName><CreateTime>1785772800</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[帮我查一下]]></Content><MsgId>wx-message-1</MsgId></xml>`)
	inboundEnvelope, inboundSignature := encryptedWechatTestEnvelope(t, connector.crypto, inboundXML, timestamp, nonce)
	inboundResponse := postWechatTestEnvelope(t, endpoint, inboundEnvelope, inboundSignature, timestamp, nonce)
	inboundBody, _ := io.ReadAll(inboundResponse.Body)
	inboundResponse.Body.Close()
	if inboundResponse.StatusCode != http.StatusOK || string(inboundBody) != "success" {
		t.Fatalf("Weixin active webhook = %d %q", inboundResponse.StatusCode, inboundBody)
	}
	eventID := runtime.platformPseudonym("event", "wxoa-primary:wx-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || !isAdmin || route.Kind != "active" || route.TargetID != "wx-user" {
		t.Fatalf("Weixin route = %+v admin=%v: %v", route, isAdmin, err)
	}
	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-wxoa-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "查到了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	seenText, seenImage := false, false
	for index := 0; index < 2; index++ {
		select {
		case call := <-calls:
			seenText = seenText || call.Payload["msgtype"] == "text"
			seenImage = seenImage || call.Payload["msgtype"] == "image"
		case <-time.After(3 * time.Second):
			t.Fatal("Weixin active sends were not completed")
		}
	}
	if !seenText || !seenImage {
		t.Fatalf("Weixin sends text=%v image=%v", seenText, seenImage)
	}

	passivePlatform := platform
	passivePlatform.ID = "wxoa-passive"
	passivePlatform.Settings = map[string]any{
		"appid": "wx-app-1", "api_base_url": apiServer.URL + "/cgi-bin", "active_send_mode": false,
		"callback_server_host": "127.0.0.1", "port": float64(6194), "passive_reply_placeholder": "稍等。",
	}
	passive, err := newWeixinOfficialAccountConnector(runtime, passivePlatform)
	if err != nil {
		t.Fatal(err)
	}
	passive.port = 0
	passive.client = apiServer.Client()
	if err = passive.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer passive.Close()
	passiveXML := []byte(`<xml><ToUserName><![CDATA[wx-app-1]]></ToUserName><FromUserName><![CDATA[wx-passive-user]]></FromUserName><CreateTime>1785772801</CreateTime><MsgType><![CDATA[text]]></MsgType><Content><![CDATA[在吗]]></Content><MsgId>wx-passive-message</MsgId></xml>`)
	passiveEnvelope, passiveSignature := encryptedWechatTestEnvelope(t, passive.crypto, passiveXML, timestamp, nonce)
	passiveRequest := httptest.NewRequest(http.MethodPost,
		passive.webhookPath()+"?msg_signature="+url.QueryEscape(passiveSignature)+"&timestamp="+timestamp+"&nonce="+nonce,
		bytes.NewReader(passiveEnvelope))
	passiveRecorder := httptest.NewRecorder()
	passiveDone := make(chan struct{})
	go func() {
		defer close(passiveDone)
		passive.handleWebhook(passiveRecorder, passiveRequest)
	}()
	passiveEventID := runtime.platformPseudonym("event", "wxoa-passive:wx-passive-message")
	waitForPlatformRun(t, runtime, passiveEventID)
	var passiveReplyHandle string
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, passiveEventID).Scan(&passiveReplyHandle); err != nil {
		t.Fatal(err)
	}
	passiveRoute, err := runtime.platformRoute(context.Background(), passiveReplyHandle)
	if err != nil {
		t.Fatal(err)
	}
	if err = passive.Deliver(context.Background(), passiveRoute, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "在。什么事？"}}); err != nil {
		t.Fatal(err)
	}
	<-passiveDone
	passiveResponseBody := passiveRecorder.Body.Bytes()
	if passiveRecorder.Code != http.StatusOK || len(passiveResponseBody) == 0 {
		t.Fatalf("Weixin passive response = %d %q", passiveRecorder.Code, passiveResponseBody)
	}
	var encryptedReply struct {
		Encrypt      string `xml:"Encrypt"`
		MsgSignature string `xml:"MsgSignature"`
	}
	if err = xml.Unmarshal(passiveResponseBody, &encryptedReply); err != nil {
		t.Fatal(err)
	}
	decryptedReply, err := passive.crypto.decryptMessage(encryptedReply.Encrypt, encryptedReply.MsgSignature, timestamp, nonce)
	if err != nil || !strings.Contains(string(decryptedReply), "在。什么事？") {
		t.Fatalf("Weixin passive reply = %q: %v", decryptedReply, err)
	}

	badResponse := postWechatTestEnvelope(t, endpoint, inboundEnvelope, "bad", timestamp, nonce)
	badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Weixin invalid signature status = %d", badResponse.StatusCode)
	}
}

func TestSlackSocketWebhookInboundMediaAndExternalUploadOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	type apiCall struct {
		Path    string
		Payload map[string]any
	}
	calls := make(chan apiCall, 8)
	acknowledged := make(chan string, 1)
	allowSocketEvent := make(chan struct{})
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	var apiServer *httptest.Server
	apiServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth.test":
			_, _ = w.Write([]byte(`{"ok":true,"user_id":"SLACK-BOT"}`))
		case "/api/apps.connections.open":
			_, _ = w.Write([]byte(`{"ok":true,"url":"ws` + strings.TrimPrefix(apiServer.URL, "http") + `/socket"}`))
		case "/socket":
			connection, err := upgrader.Upgrade(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer connection.Close()
			<-allowSocketEvent
			_ = connection.WriteJSON(map[string]any{
				"type": "events_api", "envelope_id": "slack-envelope-1",
				"payload": map[string]any{
					"event_id": "slack-event-1", "event": map[string]any{
						"type": "app_mention", "user": "U-ADMIN", "channel": "C-ONE", "channel_type": "channel",
						"text": "<@SLACK-BOT> 帮我看看", "ts": "1785772800.100", "client_msg_id": "slack-message-1",
						"files": []map[string]any{{"id": "F-ONE", "name": "photo.png", "mimetype": "image/png", "url_private_download": apiServer.URL + "/private/file"}},
					},
				},
			})
			var ack map[string]any
			if err = connection.ReadJSON(&ack); err == nil {
				acknowledged <- fmt.Sprint(ack["envelope_id"])
			}
			for err == nil {
				err = connection.ReadJSON(&ack)
			}
		case "/api/chat.postMessage", "/api/files.completeUploadExternal":
			var payload map[string]any
			_ = json.NewDecoder(r.Body).Decode(&payload)
			calls <- apiCall{Path: r.URL.Path, Payload: payload}
			_, _ = w.Write([]byte(`{"ok":true,"ts":"1785772801.1"}`))
		case "/api/files.getUploadURLExternal":
			_, _ = w.Write([]byte(`{"ok":true,"upload_url":"` + apiServer.URL + `/upload","file_id":"F-UPLOAD"}`))
		case "/upload":
			body, _ := io.ReadAll(r.Body)
			if string(body) != "png-data" {
				t.Errorf("Slack uploaded bytes = %q", body)
			}
			w.WriteHeader(http.StatusOK)
		case "/private/file":
			if r.Header.Get("Authorization") != "Bearer xoxb-test" {
				t.Errorf("Slack file authorization = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("slack-private-file"))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer apiServer.Close()
	t.Setenv("ERDAI_SLACK_TEST_BOT", "xoxb-test")
	t.Setenv("ERDAI_SLACK_TEST_APP", "xapp-test")
	t.Setenv("ERDAI_SLACK_TEST_SIGNING", "slack-signing-secret")
	t.Setenv("ERDAI_SLACK_TEST_WEBHOOK", "slack-hook")
	socketConnector, err := newSlackConnector(runtime, mgmtPlatform{
		ID: "slack-socket", Type: slackTransport,
		Settings: map[string]any{
			"slack_connection_mode": "socket", "slack_api_base_url": apiServer.URL + "/api",
			"slack_public_base_url": "http://placeholder", "slack_webhook_host": "127.0.0.1", "slack_webhook_port": float64(6197),
			"admin_ids": "U-ADMIN",
		},
		CredentialRefs: map[string]any{"bot_token": "ERDAI_SLACK_TEST_BOT", "app_token": "ERDAI_SLACK_TEST_APP"},
	})
	if err != nil {
		t.Fatal(err)
	}
	socketConnector.port = 0
	socketConnector.client = apiServer.Client()
	if err = socketConnector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer socketConnector.Close()
	socketConnector.publicBase = "http://" + socketConnector.server.Address()
	close(allowSocketEvent)
	select {
	case ack := <-acknowledged:
		if ack != "slack-envelope-1" {
			t.Fatalf("Slack socket ACK = %q", ack)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Slack socket envelope was not acknowledged")
	}
	socketEventID := runtime.platformPseudonym("event", "slack-socket:slack-message-1")
	waitForPlatformRun(t, runtime, socketEventID)
	var socketReply string
	var socketAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, socketEventID).Scan(&socketReply, &socketAdmin); err != nil {
		t.Fatal(err)
	}
	socketRoute, err := runtime.platformRoute(context.Background(), socketReply)
	if err != nil || !socketAdmin || socketRoute.TargetID != "C-ONE" {
		t.Fatalf("Slack socket route = %+v admin=%v: %v", socketRoute, socketAdmin, err)
	}
	mediaAttachments := socketConnector.parseFiles([]slackFile{{ID: "F-TWO", Name: "photo.png", MimeType: "image/png", DownloadURL: apiServer.URL + "/private/file"}})
	if len(mediaAttachments) != 1 || !strings.Contains(mediaAttachments[0].SourceURL, "/slack-media/") {
		t.Fatalf("Slack media attachments = %+v", mediaAttachments)
	}
	mediaResponse, err := http.Get(mediaAttachments[0].SourceURL)
	if err != nil {
		t.Fatal(err)
	}
	mediaBody, _ := io.ReadAll(mediaResponse.Body)
	mediaResponse.Body.Close()
	if string(mediaBody) != "slack-private-file" {
		t.Fatalf("Slack media proxy = %q", mediaBody)
	}

	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	media, err := os.CreateTemp(mediaMountRoot, "erdai-slack-*.png")
	if err != nil {
		t.Fatal(err)
	}
	mediaPath := media.Name()
	defer os.Remove(mediaPath)
	_, _ = media.Write([]byte("png-data"))
	_ = media.Close()
	if err = socketConnector.Deliver(context.Background(), socketRoute, leasedTransportDelivery{Message: transportDeliveryMessage{
		Text: "看过了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaPath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	seenText, seenFile := false, false
	for index := 0; index < 2; index++ {
		select {
		case call := <-calls:
			seenText = seenText || call.Path == "/api/chat.postMessage"
			seenFile = seenFile || call.Path == "/api/files.completeUploadExternal"
		case <-time.After(3 * time.Second):
			t.Fatal("Slack outbound calls were not completed")
		}
	}
	if !seenText || !seenFile {
		t.Fatalf("Slack sends text=%v file=%v", seenText, seenFile)
	}

	webhookConnector, err := newSlackConnector(runtime, mgmtPlatform{
		ID: "slack-webhook", Type: slackTransport,
		Settings: map[string]any{
			"slack_connection_mode": "webhook", "slack_api_base_url": apiServer.URL + "/api",
			"slack_webhook_host": "127.0.0.1", "slack_webhook_port": float64(6197), "slack_webhook_path": "/events",
		},
		CredentialRefs: map[string]any{
			"bot_token": "ERDAI_SLACK_TEST_BOT", "signing_secret": "ERDAI_SLACK_TEST_SIGNING", "webhook_uuid": "ERDAI_SLACK_TEST_WEBHOOK",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	webhookConnector.port = 0
	webhookConnector.client = apiServer.Client()
	if err = webhookConnector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer webhookConnector.Close()
	webhookEndpoint := "http://" + webhookConnector.server.Address() + webhookConnector.webhookPath()
	webhookBody, _ := json.Marshal(map[string]any{
		"type": "event_callback", "event_id": "slack-event-webhook",
		"event": map[string]any{
			"type": "message", "user": "U-WEB", "channel": "D-ONE", "channel_type": "im",
			"text": "在吗", "ts": "1785772802.1", "client_msg_id": "slack-message-webhook",
		},
	})
	requestTimestamp := strconv.FormatInt(time.Now().Unix(), 10)
	mac := hmac.New(sha256.New, []byte("slack-signing-secret"))
	_, _ = mac.Write([]byte("v0:" + requestTimestamp + ":"))
	_, _ = mac.Write(webhookBody)
	webhookRequest, _ := http.NewRequest(http.MethodPost, webhookEndpoint, bytes.NewReader(webhookBody))
	webhookRequest.Header.Set("X-Slack-Request-Timestamp", requestTimestamp)
	webhookRequest.Header.Set("X-Slack-Signature", "v0="+hex.EncodeToString(mac.Sum(nil)))
	webhookResponse, err := http.DefaultClient.Do(webhookRequest)
	if err != nil {
		t.Fatal(err)
	}
	webhookResponse.Body.Close()
	if webhookResponse.StatusCode != http.StatusOK {
		t.Fatalf("Slack webhook status = %d", webhookResponse.StatusCode)
	}
	webhookEventID := runtime.platformPseudonym("event", "slack-webhook:slack-message-webhook")
	waitForPlatformRun(t, runtime, webhookEventID)

	invalidRequest, _ := http.NewRequest(http.MethodPost, webhookEndpoint, bytes.NewReader(webhookBody))
	invalidRequest.Header.Set("X-Slack-Request-Timestamp", requestTimestamp)
	invalidRequest.Header.Set("X-Slack-Signature", "v0=bad")
	invalidResponse, err := http.DefaultClient.Do(invalidRequest)
	if err != nil {
		t.Fatal(err)
	}
	invalidResponse.Body.Close()
	if invalidResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("Slack invalid signature status = %d", invalidResponse.StatusCode)
	}
}

func encryptedWechatTestEnvelope(t *testing.T, crypto *wechatWebhookCrypto, plaintext []byte, timestamp, nonce string) ([]byte, string) {
	t.Helper()
	encoded, err := crypto.encryptMessage(plaintext, timestamp, nonce)
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := wechatParseEncryptedBody([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	return []byte(encoded), wechatSignature(crypto.token, timestamp, nonce, encrypted)
}

func postWechatTestEnvelope(t *testing.T, endpoint string, body []byte, signature, timestamp, nonce string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost,
		endpoint+"?msg_signature="+url.QueryEscape(signature)+"&timestamp="+url.QueryEscape(timestamp)+"&nonce="+url.QueryEscape(nonce), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func signQQWebhookTestPayload(t *testing.T, secret, timestamp string, body []byte) string {
	t.Helper()
	privateKey, err := qqWebhookPrivateKey(secret)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(ed25519.Sign(privateKey, append([]byte(timestamp), body...)))
}

func postQQWebhook(t *testing.T, endpoint string, body []byte, timestamp, signature string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if timestamp != "" {
		request.Header.Set(qqWebhookTimestampHeader, timestamp)
	}
	if signature != "" {
		request.Header.Set(qqWebhookSignatureHeader, signature)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func waitForPlatformRun(t *testing.T, runtime *AgentRuntime, eventID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var count int
		if err := runtime.db.QueryRow("SELECT count(*) FROM agent_runs WHERE event_id = ?", eventID).Scan(&count); err == nil && count == 1 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("platform run %s was not created", eventID)
}
