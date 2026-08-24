package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	larktypes "github.com/larksuite/oapi-sdk-go/v3/channel/types"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/open-dingtalk/dingtalk-stream-sdk-go/chatbot"
)

func TestWebchatHTTPInboundEncryptedCursorOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_WEBCHAT_TEST_TOKEN", "webchat-secret")
	connector, err := newWebchatConnector(runtime, mgmtPlatform{
		ID: "webchat-primary", Type: webchatTransport,
		Settings:       map[string]any{"webchat_host": "127.0.0.1", "webchat_port": float64(6200), "webchat_api_path": "/api/v1/webchat", "admin_ids": "admin-user"},
		CredentialRefs: map[string]any{"webchat_token": "ERDAI_WEBCHAT_TEST_TOKEN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.port = 0
	if err = connector.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	endpoint := "http://" + connector.server.Address() + connector.path + "/conversations/conversation-one/messages"
	unauthorized, _ := http.Post(endpoint, "application/json", strings.NewReader(`{}`))
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.StatusCode)
	}
	payload := `{"messageId":"web-message-1","senderId":"admin-user","senderName":"管理","text":"帮我看看","attachments":[{"kind":"image","sourceUrl":"https://img.example/in.png","name":"in.png"}]}`
	request, _ := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer webchat-secret")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		t.Fatalf("inbound status = %d", response.StatusCode)
	}
	eventID := runtime.platformPseudonym("event", "webchat-primary:web-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || !isAdmin || route.TargetID != "conversation-one" {
		t.Fatalf("route = %+v admin=%v: %v", route, isAdmin, err)
	}
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{ID: "web-delivery-1", Message: transportDeliveryMessage{Text: "看好了"}}); err != nil {
		t.Fatal(err)
	}
	eventsRequest, _ := http.NewRequest(http.MethodGet, "http://"+connector.server.Address()+connector.path+"/conversations/conversation-one/events?after=0", nil)
	eventsRequest.Header.Set("Authorization", "Bearer webchat-secret")
	eventsResponse, err := http.DefaultClient.Do(eventsRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer eventsResponse.Body.Close()
	var events struct {
		Items []webchatStoredMessage `json:"items"`
	}
	if err = json.NewDecoder(eventsResponse.Body).Decode(&events); err != nil || len(events.Items) != 1 || events.Items[0].Text != "看好了" || events.Items[0].Sequence < 1 {
		t.Fatalf("events = %+v: %v", events, err)
	}
	var stored []byte
	if err = runtime.db.QueryRow(`SELECT message_cipher FROM webchat_outbound_messages WHERE delivery_id = ?`, "web-delivery-1").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "看好了") {
		t.Fatal("WebChat outbound message was stored as plaintext")
	}
}

func TestLarkOfficialEventChannelMediaAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_LARK_TEST_SECRET", "lark-secret")
	connector, err := newLarkConnector(runtime, mgmtPlatform{
		ID: "lark-primary", Type: larkTransport,
		Settings:       map[string]any{"app_id": "cli-test", "lark_connection_mode": "socket", "lark_public_base_url": "http://core.test", "admin_ids": "ou-admin"},
		CredentialRefs: map[string]any{"app_secret": "ERDAI_LARK_TEST_SECRET"},
	})
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeLarkChannel{bot: &larktypes.BotIdentity{OpenID: "ou-bot"}, download: []byte("lark-file")}
	connector.channel = fake
	var event larkim.P2MessageReceiveV1
	raw := `{"schema":"2.0","header":{"event_id":"lark-event-1","create_time":"1785772800000","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"ou-admin"},"sender_type":"user"},"message":{"message_id":"om-lark-1","chat_id":"oc-chat-1","chat_type":"p2p","message_type":"image","content":"{\"image_key\":\"img-key\"}","create_time":"1785772800000"}}}`
	if err = json.Unmarshal([]byte(raw), &event); err != nil {
		t.Fatal(err)
	}
	if err = connector.handleP2Message(context.Background(), &event); err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "lark-primary:om-lark-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || !isAdmin || route.Kind != "private" || route.TargetID != "oc-chat-1" {
		t.Fatalf("route = %+v admin=%v: %v", route, isAdmin, err)
	}
	attachments := connector.registerResources([]larktypes.Resource{{Type: "image", FileKey: "img-key", FileName: "photo.png"}})
	mediaID := strings.TrimPrefix(attachments[0].SourceURL, "http://core.test/lark-media/")
	mediaRequest := httptest.NewRequest(http.MethodGet, "/lark-media/"+mediaID, nil)
	mediaResponse := httptest.NewRecorder()
	connector.handleMedia(mediaResponse, mediaRequest)
	if mediaResponse.Code != http.StatusOK || mediaResponse.Body.String() != "lark-file" {
		t.Fatalf("media response = %d %q", mediaResponse.Code, mediaResponse.Body.String())
	}
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "看好了"}}); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent) != 1 || fake.sent[0].ChatID != "oc-chat-1" || fake.sent[0].ReplyMessageID != "om-lark-1" || fake.sent[0].Text != "看好了" {
		t.Fatalf("Lark sends = %+v", fake.sent)
	}
}

func TestDingTalkOfficialCallbackRESTMediaAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_DING_TEST_SECRET", "ding-secret")
	var calls []string
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_, _ = w.Write([]byte(`{"accessToken":"ding-token","expireIn":7200}`))
		case "/media/upload":
			if r.URL.Query().Get("access_token") != "ding-token" {
				t.Errorf("DingTalk media token = %q", r.URL.Query().Get("access_token"))
			}
			_, _ = w.Write([]byte(`{"errcode":0,"media_id":"media-one"}`))
		case "/v1.0/robot/groupMessages/send":
			if r.Header.Get("x-acs-dingtalk-access-token") != "ding-token" {
				t.Errorf("DingTalk REST token missing")
			}
			_, _ = w.Write([]byte(`{}`))
		case "/v1.0/robot/messageFiles/download":
			_, _ = w.Write([]byte(`{"downloadUrl":"` + server.URL + `/download/file"}`))
		case "/download/file":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("ding-image"))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	connector, err := newDingTalkConnector(runtime, mgmtPlatform{
		ID: "ding-primary", Type: dingtalkTransport,
		Settings:       map[string]any{"client_id": "ding-app", "dingtalk_api_base_url": server.URL, "dingtalk_oapi_base_url": server.URL, "dingtalk_public_base_url": "http://core.test", "admin_ids": "staff-admin"},
		CredentialRefs: map[string]any{"client_secret": "ERDAI_DING_TEST_SECRET"},
	})
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	_, err = connector.handleMessage(context.Background(), &chatbot.BotCallbackDataModel{
		ConversationId: "cid-group", ConversationType: "2", MsgId: "ding-message-1",
		SenderId: "sender-admin", SenderStaffId: "staff-admin", SenderNick: "管理",
		IsInAtList: true, RobotCode: "robot-one", Msgtype: "picture",
		Text:    chatbot.BotCallbackDataTextModel{Content: "帮我看看"},
		Content: map[string]any{"downloadCode": "download-code", "fileName": "in.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "ding-primary:ding-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	if err = runtime.db.QueryRow(`SELECT reply_handle FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle); err != nil {
		t.Fatal(err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.Kind != "group" || route.TargetID != "cid-group" || route.GuildID != "robot-one" {
		t.Fatalf("route = %+v: %v", route, err)
	}
	connector.mediaMu.RLock()
	var mediaID string
	for id := range connector.media {
		mediaID = id
	}
	connector.mediaMu.RUnlock()
	mediaRequest := httptest.NewRequest(http.MethodGet, "/dingtalk-media/"+mediaID, nil)
	mediaResponse := httptest.NewRecorder()
	connector.handleMedia(mediaResponse, mediaRequest)
	if mediaResponse.Code != http.StatusOK || mediaResponse.Body.String() != "ding-image" {
		t.Fatalf("media response = %d %q", mediaResponse.Code, mediaResponse.Body.String())
	}
	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(mediaMountRoot, "erdai-ding-*.png")
	if err != nil {
		t.Fatal(err)
	}
	filePath := file.Name()
	defer os.Remove(filePath)
	_, _ = file.Write([]byte("outbound"))
	_ = file.Close()
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "看好了", Attachments: []agentAttachment{{Kind: "image", LocalPath: filePath}}}}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "/v1.0/oauth2/accessToken,/v1.0/robot/messageFiles/download,/download/file,/v1.0/robot/groupMessages/send,/media/upload,/v1.0/robot/groupMessages/send" {
		t.Fatalf("DingTalk calls = %v", calls)
	}
}

func TestWeComAIBotLongConnectionInboundAndOutboxResponse(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_WECOM_AI_TEST_SECRET", "wecom-secret")
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	responded := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		var subscribe map[string]any
		if err = conn.ReadJSON(&subscribe); err != nil {
			return
		}
		if subscribe["cmd"] != "aibot_subscribe" {
			t.Errorf("subscribe = %+v", subscribe)
		}
		_ = conn.WriteJSON(map[string]any{"errcode": 0})
		_ = conn.WriteJSON(map[string]any{
			"cmd": "aibot_msg_callback", "headers": map[string]any{"req_id": "req-inbound"},
			"body": map[string]any{"msgid": "wecom-ai-message-1", "chattype": "group", "chatid": "group-one", "from": map[string]any{"userid": "user-admin"}, "msgtype": "text", "text": map[string]any{"content": "@豆包 帮我看看"}},
		})
		var response map[string]any
		if err = conn.ReadJSON(&response); err != nil {
			return
		}
		responded <- response
		headers := anyMap(response["headers"])
		_ = conn.WriteJSON(map[string]any{"errcode": 0, "headers": map[string]any{"req_id": headers["req_id"]}})
		for {
			var ignored map[string]any
			if conn.ReadJSON(&ignored) != nil {
				return
			}
		}
	}))
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	connector, err := newWecomAIBotConnector(runtime, mgmtPlatform{
		ID: "wecom-ai-primary", Type: wecomAIBotTransport,
		Settings:       map[string]any{"wecom_ai_bot_connection_mode": "long_connection", "wecomaibot_ws_bot_id": "bot-one", "wecom_ai_bot_name": "豆包", "wecomaibot_ws_url": wsURL, "admin_ids": "user-admin"},
		CredentialRefs: map[string]any{"wecomaibot_ws_secret": "ERDAI_WECOM_AI_TEST_SECRET"},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- connector.longConnectionOnce(ctx) }()
	eventID := runtime.platformPseudonym("event", "wecom-ai-primary:wecom-ai-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin); err != nil {
		t.Fatal(err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || !isAdmin || route.Kind != "long_connection" || route.TargetID != "req-inbound" {
		t.Fatalf("route = %+v admin=%v: %v", route, isAdmin, err)
	}
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{Message: transportDeliveryMessage{Text: "看好了"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case response := <-responded:
		if response["cmd"] != "aibot_respond_msg" || anyMap(anyMap(response["body"])["stream"])["content"] != "看好了" {
			t.Fatalf("WeCom AI response = %+v", response)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WeCom AI response was not sent")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("WeCom AI connection did not stop")
	}
}

type fakeLarkChannel struct {
	mu       sync.Mutex
	bot      *larktypes.BotIdentity
	download []byte
	sent     []*larktypes.SendInput
}

func (f *fakeLarkChannel) Send(_ context.Context, input *larktypes.SendInput) (*larktypes.SendResult, error) {
	f.mu.Lock()
	copy := *input
	f.sent = append(f.sent, &copy)
	f.mu.Unlock()
	return &larktypes.SendResult{MessageID: "sent-one"}, nil
}
func (*fakeLarkChannel) OnMessage(func(context.Context, *larktypes.NormalizedMessage) error)  {}
func (*fakeLarkChannel) OnReaction(func(context.Context, *larktypes.ReactionEvent) error)     {}
func (*fakeLarkChannel) OnComment(func(context.Context, *larktypes.CommentEvent) error)       {}
func (*fakeLarkChannel) OnBotAdded(func(context.Context, *larktypes.BotAddedEvent) error)     {}
func (*fakeLarkChannel) OnCardAction(func(context.Context, *larktypes.CardActionEvent) error) {}
func (*fakeLarkChannel) OnReject(func(context.Context, *larktypes.RejectEvent) error)         {}
func (f *fakeLarkChannel) DownloadFile(context.Context, string, string) ([]byte, error) {
	return append([]byte{}, f.download...), nil
}
func (*fakeLarkChannel) OnReady(func())              {}
func (*fakeLarkChannel) OnError(func(error))         {}
func (*fakeLarkChannel) OnReconnecting(func())       {}
func (*fakeLarkChannel) OnReconnected(func())        {}
func (*fakeLarkChannel) OnDisconnected(func())       {}
func (*fakeLarkChannel) Start(context.Context) error { return nil }
func (*fakeLarkChannel) Stream(context.Context, *larktypes.SendInput) (larktypes.StreamController, error) {
	return nil, nil
}
func (*fakeLarkChannel) UpdatePolicy(larktypes.PolicyConfig)                     {}
func (*fakeLarkChannel) GetPolicy() larktypes.PolicyConfig                       { return larktypes.PolicyConfig{} }
func (f *fakeLarkChannel) GetBotIdentity(context.Context) *larktypes.BotIdentity { return f.bot }
func (*fakeLarkChannel) Stop(context.Context) error                              { return nil }
