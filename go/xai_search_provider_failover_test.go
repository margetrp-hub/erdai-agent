package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeSearchAttemptTimeoutReservesFallbackBudget(t *testing.T) {
	local := xaiSearchConnection{providerConnectionConfig: providerConnectionConfig{
		ID: "connection-local", APIBase: "http://grok2api-local:8000/v1",
	}}
	paid := xaiSearchConnection{providerConnectionConfig: providerConnectionConfig{
		ID: "connection-paid", APIBase: "https://fast.example/v1",
	}}
	if got := nativeSearchAttemptTimeout(local, 30*time.Second); got != 4*time.Second {
		t.Fatalf("local attempt = %s", got)
	}
	if got := nativeSearchAttemptTimeout(paid, 30*time.Second); got != 10*time.Second {
		t.Fatalf("paid attempt = %s", got)
	}
	if got := nativeSearchAttemptTimeout(paid, 4*time.Second); got != 4*time.Second {
		t.Fatalf("remaining budget = %s", got)
	}
}

func TestNativeSearchFailsOverAcrossEnabledConnections(t *testing.T) {
	var firstCalls, secondCalls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/first/responses":
			firstCalls.Add(1)
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case "/second/responses":
			secondCalls.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if stream, ok := request["stream"].(bool); !ok || stream {
				t.Fatalf("native search stream = %v", request["stream"])
			}
			writeJSON(w, http.StatusOK, map[string]any{"output": []any{map[string]any{
				"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "北京天气今天晴朗。"}},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, db := newTestCoreConfig(t)
	setTestIntegration(t, db, "grok_policy", map[string]any{
		"enabled": true, "searchConnectionIds": []string{"search-first", "search-second"},
		"searchModel": "grok-4.5", "searchSummaryMaxChars": 120,
	})
	for _, row := range []struct{ id, base, key string }{
		{"search-first", service.URL + "/first", "ERDAI_TEST_SEARCH_FIRST"},
		{"search-second", service.URL + "/second", "ERDAI_TEST_SEARCH_SECOND"},
	} {
		if _, err := db.Exec(`INSERT INTO provider_connections
			(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
			VALUES (?, ?, 'xai_responses', ?, ?, 2, 1, '2026-08-09T00:00:00Z', '2026-08-09T00:00:00Z')`, row.id, row.id, row.base, row.key); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Close()
	t.Setenv("ERDAI_TEST_SEARCH_FIRST", "first-key")
	t.Setenv("ERDAI_TEST_SEARCH_SECOND", "second-key")

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
		HTTPClient:    service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	text, _, handled, err := runtime.grokNativeResearchForRun(context.Background(), nil, "北京天气", "保持简短。")
	if err != nil || !handled || text != "北京天气今天晴朗。" {
		t.Fatalf("search failover = %q handled=%v err=%v", text, handled, err)
	}
	if firstCalls.Load() != 1 || secondCalls.Load() != 1 {
		t.Fatalf("search calls first=%d second=%d", firstCalls.Load(), secondCalls.Load())
	}
}
