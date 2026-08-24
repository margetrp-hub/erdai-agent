package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func newRealtimeTestRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath:       filepath.Join(t.TempDir(), "runtime.sqlite3"),
		ConfigDatabasePath: newTestCoreConfigPath(t),
		AdminToken:         "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{31}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime
}

func TestRealtimePairingIsSingleUseAndStoresOnlyTokenDigest(t *testing.T) {
	runtime := newRealtimeTestRuntime(t)
	code, _, err := runtime.realtime.createPairingCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	device, token, err := runtime.realtime.pairDevice(context.Background(), code, "桌面设备")
	if err != nil {
		t.Fatal(err)
	}
	if device.ID == "" || token == "" {
		t.Fatal("pairing did not return a device and token")
	}
	var stored []byte
	if err = runtime.db.QueryRow("SELECT token_hash FROM realtime_devices WHERE id = ?", device.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if string(stored) == token || strings.Contains(string(stored), token) {
		t.Fatal("device token was stored in plaintext")
	}
	if _, _, err = runtime.realtime.pairDevice(context.Background(), code, "另一台设备"); err == nil {
		t.Fatal("pairing code was accepted twice")
	}
}

func TestRealtimeExpiredPairingCodeIsRejected(t *testing.T) {
	runtime := newRealtimeTestRuntime(t)
	code, _, err := runtime.realtime.createPairingCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	digest := realtimeDigest("pair", code)
	if _, err = runtime.db.Exec("UPDATE realtime_pairing_codes SET expires_at = ? WHERE code_hash = ?",
		time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), digest[:]); err != nil {
		t.Fatal(err)
	}
	if _, _, err = runtime.realtime.pairDevice(context.Background(), code, "过期设备"); err == nil {
		t.Fatal("expired pairing code was accepted")
	}
}

func TestRealtimeSessionResumeSequenceAndDeliveryAck(t *testing.T) {
	runtime := newRealtimeTestRuntime(t)
	gateway := NewGateway("")
	gateway.runtime = runtime
	server := httptest.NewServer(gateway)
	defer server.Close()

	code, _, err := runtime.realtime.createPairingCode(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pairBody, _ := json.Marshal(map[string]string{"code": code, "name": "测试桌面"})
	response, err := http.Post(server.URL+"/api/v2/realtime/pair", "application/json", bytes.NewReader(pairBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var paired struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if response.StatusCode != http.StatusCreated || json.NewDecoder(response.Body).Decode(&paired) != nil || paired.Data.Token == "" {
		t.Fatalf("pairing response status=%d", response.StatusCode)
	}

	dial := func(sessionID string) (*websocket.Conn, realtimeEnvelope) {
		t.Helper()
		wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/api/v2/realtime"
		if sessionID != "" {
			wsURL += "?sessionId=" + url.QueryEscape(sessionID)
		}
		dialer := websocket.Dialer{Subprotocols: []string{
			realtimeProtocol,
			realtimeTokenProtocol + base64.RawURLEncoding.EncodeToString([]byte(paired.Data.Token)),
		}}
		conn, _, dialErr := dialer.Dial(wsURL, nil)
		if dialErr != nil {
			t.Fatal(dialErr)
		}
		var ready realtimeEnvelope
		if readErr := conn.ReadJSON(&ready); readErr != nil {
			t.Fatal(readErr)
		}
		if ready.Type != "session.ready" {
			t.Fatalf("first event type = %q", ready.Type)
		}
		return conn, ready
	}

	first, ready := dial("")
	sessionID := ready.SessionID
	if sessionID == "" {
		t.Fatal("session id is empty")
	}
	runtime.realtime.mu.Lock()
	client := runtime.realtime.clients[sessionID]
	runtime.realtime.mu.Unlock()
	if client == nil {
		t.Fatal("connected realtime client was not registered")
	}
	advanced, err := client.claimSequence(context.Background(), 7)
	if err != nil || !advanced {
		t.Fatalf("claim sequence: advanced=%v err=%v", advanced, err)
	}
	advanced, err = client.claimSequence(context.Background(), 7)
	if err != nil || advanced {
		t.Fatalf("duplicate sequence: advanced=%v err=%v", advanced, err)
	}
	_ = first.Close()

	second, resumed := dial(sessionID)
	defer second.Close()
	if resumed.SessionID != sessionID {
		t.Fatalf("resumed session = %q, want %q", resumed.SessionID, sessionID)
	}

	delivery := leasedTransportDelivery{ID: "delivery-realtime", RunID: "run-realtime", Phase: "terminal"}
	delivery.Message.Text = "已经处理好了。"
	done := make(chan error, 1)
	go func() {
		done <- runtime.realtime.Deliver(context.Background(), platformReplyRoute{
			ConnectorID: realtimeConnectorID, Transport: "realtime", Kind: "private", TargetID: sessionID,
		}, delivery)
	}()
	var committed realtimeEnvelope
	for committed.Type != "response.commit" {
		if err = second.ReadJSON(&committed); err != nil {
			t.Fatal(err)
		}
	}
	ackPayload, _ := json.Marshal(map[string]string{"deliveryId": delivery.ID})
	if err = second.WriteJSON(realtimeEnvelope{
		Version: 1, EventID: "ack-one", SessionID: sessionID, Sequence: 8,
		Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Type: "delivery.ack", Payload: ackPayload,
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delivery did not complete after client acknowledgement")
	}
}

func TestRealtimeWebSocketRejectsUnknownToken(t *testing.T) {
	runtime := newRealtimeTestRuntime(t)
	gateway := NewGateway("")
	gateway.runtime = runtime
	server := httptest.NewServer(gateway)
	defer server.Close()
	dialer := websocket.Dialer{Subprotocols: []string{
		realtimeProtocol,
		realtimeTokenProtocol + base64.RawURLEncoding.EncodeToString([]byte("unknown-device-token-value")),
	}}
	conn, response, err := dialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/api/v2/realtime", nil)
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unknown token response=%v err=%v", response, err)
	}
}
