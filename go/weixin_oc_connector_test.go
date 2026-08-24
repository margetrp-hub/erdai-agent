package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestWeixinOCCoreLifecyclePersistenceMediaAndOutbound(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	inboundPlain := []byte("weixin-image")
	inboundKey := []byte("0123456789abcdef")
	inboundCipher := encryptWeixinOCMedia(inboundPlain, inboundKey)
	var uploadedPlain []byte
	var uploadKey []byte
	var sentPayload map[string]any
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			if r.URL.Query().Get("bot_type") != "3" {
				t.Errorf("bot_type = %q", r.URL.Query().Get("bot_type"))
			}
			_, _ = w.Write([]byte(`{"qrcode":"qr-session","qrcode_img_content":"weixin://login/value"}`))
		case "/ilink/bot/get_qrcode_status":
			if r.Header.Get("iLink-App-ClientVersion") != "1" {
				t.Errorf("missing QR client version header")
			}
			_, _ = w.Write([]byte(`{"status":"confirmed","bot_token":"secret-login-token","ilink_bot_id":"bot-account","baseurl":"` + server.URL + `"}`))
		case "/ilink/bot/getupdates":
			assertWeixinOCAuthorization(t, r)
			_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"get_updates_buf":"sync-next","msgs":[{"from_user_id":"user-42","context_token":"ctx-42","message_id":"wx-message-1","create_time_ms":1785772800000,"item_list":[{"type":1,"text_item":{"text":"帮我看看"}},{"type":2,"image_item":{"media":{"encrypt_query_param":"download-one","aes_key":"` + base64.StdEncoding.EncodeToString(inboundKey) + `"}}}]}]}`))
		case "/c2c/download":
			if r.URL.Query().Get("encrypted_query_param") != "download-one" {
				t.Errorf("download param = %q", r.URL.Query().Get("encrypted_query_param"))
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(inboundCipher)
		case "/ilink/bot/getuploadurl":
			assertWeixinOCAuthorization(t, r)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			keyHex, _ := payload["aeskey"].(string)
			uploadKey, _ = hex.DecodeString(keyHex)
			_, _ = w.Write([]byte(`{"ret":0,"errcode":0,"upload_full_url":"` + server.URL + `/cdn/upload"}`))
		case "/cdn/upload":
			ciphertext, _ := io.ReadAll(r.Body)
			var err error
			uploadedPlain, err = decryptWeixinOCMedia(ciphertext, uploadKey)
			if err != nil {
				t.Error(err)
			}
			w.Header().Set("x-encrypted-param", "uploaded-one")
			_, _ = w.Write([]byte(`{}`))
		case "/ilink/bot/sendmessage":
			assertWeixinOCAuthorization(t, r)
			if err := json.NewDecoder(r.Body).Decode(&sentPayload); err != nil {
				t.Error(err)
			}
			_, _ = w.Write([]byte(`{"ret":0,"errcode":0}`))
		default:
			http.Error(w, "unexpected path "+r.URL.Path, http.StatusNotFound)
		}
	}))
	defer server.Close()
	platform := mgmtPlatform{ID: "weixin-personal", Type: weixinOCTransport, Settings: map[string]any{
		"weixin_oc_base_url": server.URL, "weixin_oc_cdn_base_url": server.URL + "/c2c",
		"weixin_oc_bot_type": "3", "weixin_oc_long_poll_timeout_ms": float64(5000),
		"weixin_oc_api_timeout_ms": float64(5000), "weixin_oc_public_base_url": "http://core.test",
		"admin_ids": "user-42",
	}, CredentialRefs: map[string]any{}}
	connector, err := newWeixinOCConnector(runtime, platform)
	if err != nil {
		t.Fatal(err)
	}
	connector.client = server.Client()
	if err = connector.loginOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	health := connector.Health()
	if health.Status != "waiting_for_login" || health.Details["qrAvailable"] != true || connector.QRCodeValue() != "weixin://login/value" {
		t.Fatalf("QR health = %+v", health)
	}
	runtime.platformManager = &platformConnectorManager{connectors: map[string]platformConnector{connector.ID(): connector}}
	qrRequest := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/weixin-personal/login-qr", nil)
	qrResponse := httptest.NewRecorder()
	if !runtime.Handle(qrResponse, qrRequest) || qrResponse.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous QR endpoint = handled status=%d", qrResponse.Code)
	}
	qrRequest = httptest.NewRequest(http.MethodGet, "/api/v1/platforms/weixin-personal/login-qr", nil)
	qrRequest.Header.Set(adminTokenHeader, "admin-test-token")
	qrResponse = httptest.NewRecorder()
	if !runtime.Handle(qrResponse, qrRequest) || qrResponse.Code != http.StatusOK || qrResponse.Header().Get("Content-Type") != "image/png" || !bytes.HasPrefix(qrResponse.Body.Bytes(), []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("QR endpoint = handled status=%d type=%q bytes=%x", qrResponse.Code, qrResponse.Header().Get("Content-Type"), qrResponse.Body.Bytes()[:min(8, qrResponse.Body.Len())])
	}
	gateway := NewGateway("admin-test-token")
	gateway.runtime = runtime
	loginBody, _ := json.Marshal(map[string]string{"token": "admin-test-token"})
	loginRequest := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(loginBody))
	loginRequest.Header.Set("Content-Type", "application/json")
	loginResponse := httptest.NewRecorder()
	gateway.ServeHTTP(loginResponse, loginRequest)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("QR gateway login = %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	gatewayQRRequest := httptest.NewRequest(http.MethodGet, "/api/v1/platforms/weixin-personal/login-qr", nil)
	gatewayQRRequest.AddCookie(loginResponse.Result().Cookies()[0])
	gatewayQRResponse := httptest.NewRecorder()
	gateway.ServeHTTP(gatewayQRResponse, gatewayQRRequest)
	if gatewayQRResponse.Code != http.StatusOK || gatewayQRResponse.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("QR gateway endpoint = %d type=%q", gatewayQRResponse.Code, gatewayQRResponse.Header().Get("Content-Type"))
	}
	if err = connector.loginOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if connector.Health().Details["configured"] != true {
		t.Fatalf("confirmed health = %+v", connector.Health())
	}
	var stateCipher []byte
	if err = runtime.configStore.db.QueryRow(`SELECT state_cipher FROM platform_connector_state WHERE connector_id = ?`, platform.ID).Scan(&stateCipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stateCipher, []byte("secret-login-token")) {
		t.Fatal("login token was stored as plaintext")
	}
	if err = connector.pollUpdates(context.Background()); err != nil {
		t.Fatal(err)
	}
	eventID := runtime.platformPseudonym("event", "weixin-personal:wx-message-1")
	waitForPlatformRun(t, runtime, eventID)
	var replyHandle string
	var isAdmin bool
	var inputCipher []byte
	if err = runtime.db.QueryRow(`SELECT reply_handle, is_admin, input_cipher FROM agent_runs WHERE event_id = ?`, eventID).Scan(&replyHandle, &isAdmin, &inputCipher); err != nil {
		t.Fatal(err)
	}
	input, err := runtime.decrypt(inputCipher)
	if err != nil || !isAdmin || !strings.Contains(string(input), "帮我看看") || !strings.Contains(string(input), "[图片]") {
		t.Fatalf("inbound admin=%v input=%q err=%v", isAdmin, input, err)
	}
	route, err := runtime.platformRoute(context.Background(), replyHandle)
	if err != nil || route.Kind != "private" || route.TargetID != "user-42" {
		t.Fatalf("route = %+v: %v", route, err)
	}
	connector.mu.RLock()
	var mediaID string
	for id := range connector.media {
		mediaID = id
	}
	connector.mu.RUnlock()
	mediaRequest := httptest.NewRequest(http.MethodGet, "/weixin-oc-media/"+mediaID, nil)
	mediaResponse := httptest.NewRecorder()
	connector.handleMedia(mediaResponse, mediaRequest)
	if mediaResponse.Code != http.StatusOK || mediaResponse.Body.String() != string(inboundPlain) {
		t.Fatalf("media response = %d %q", mediaResponse.Code, mediaResponse.Body.String())
	}
	if err = os.MkdirAll(mediaMountRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(mediaMountRoot, "erdai-weixin-*.png")
	if err != nil {
		t.Fatal(err)
	}
	filePath := file.Name()
	defer os.Remove(filePath)
	_, _ = file.Write([]byte("outbound-image"))
	_ = file.Close()
	if err = connector.Deliver(context.Background(), route, leasedTransportDelivery{ID: "weixin-delivery", Message: transportDeliveryMessage{
		Text: "看好了", Attachments: []agentAttachment{{Kind: "image", LocalPath: filePath, Name: "result.png"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if string(uploadedPlain) != "outbound-image" {
		t.Fatalf("uploaded plaintext = %q", uploadedPlain)
	}
	message := anyMap(sentPayload["msg"])
	if message["to_user_id"] != "user-42" || message["context_token"] != "ctx-42" || len(anyObjectSlice(message["item_list"])) != 2 {
		t.Fatalf("sendmessage payload = %+v", sentPayload)
	}
	reloaded, err := newWeixinOCConnector(runtime, platform)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.account.Token != "secret-login-token" || reloaded.account.SyncBuffer != "sync-next" || reloaded.account.ContextTokens["user-42"] != "ctx-42" {
		t.Fatalf("reloaded state = %+v", reloaded.account)
	}
}

func TestWeixinOCMediaKeyFormatsAndFactory(t *testing.T) {
	raw := []byte("0123456789abcdef")
	for _, encoded := range []string{
		base64.StdEncoding.EncodeToString(raw),
		base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(raw))),
	} {
		key, err := parseWeixinOCMediaKey(encoded)
		if err != nil || !bytes.Equal(key, raw) {
			t.Fatalf("parsed key = %x: %v", key, err)
		}
	}
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connector, err := newPlatformConnector(runtime, mgmtPlatform{ID: "weixin-factory", Type: weixinOCTransport, Settings: map[string]any{}, CredentialRefs: map[string]any{}})
	if err != nil || connector.Type() != weixinOCTransport {
		t.Fatalf("factory connector = %T: %v", connector, err)
	}
}

func assertWeixinOCAuthorization(t *testing.T, r *http.Request) {
	t.Helper()
	if r.Header.Get("Authorization") != "Bearer secret-login-token" || r.Header.Get("AuthorizationType") != "ilink_bot_token" || r.Header.Get("X-WECHAT-UIN") == "" {
		t.Errorf("authorization headers = %+v", r.Header)
	}
}
