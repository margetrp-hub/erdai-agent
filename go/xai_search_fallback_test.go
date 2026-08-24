package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeSearchTimeoutFallsBackToSummarizedRSS(t *testing.T) {
	var rssCalls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/xai/responses":
			select {
			case <-r.Context().Done():
			case <-time.After(2 * time.Second):
			}
		case "/search":
			rssCalls.Add(1)
			w.Header().Set("Content-Type", "application/rss+xml")
			_, _ = w.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><item><title>xAI latest update</title><link>https://x.ai/news</link><description>xAI released a current product update.</description></item></channel></rss>`))
		case "/xai/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"content": "xAI released a current product update."},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, db := newTestCoreConfig(t)
	setTestIntegration(t, db, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "credentialRef": "ERDAI_TEST_GROK_FALLBACK_KEY",
		"searchConnectionId": "xai-fallback", "searchModel": "grok-4.5", "searchSummaryMaxChars": 120,
	})
	if _, err := db.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('xai-fallback', 'xai-fallback', 'xai_responses', ?, 'ERDAI_TEST_XAI_FALLBACK_KEY', 1, 1,
		'2026-08-08T00:00:00Z', '2026-08-08T00:00:00Z')`, service.URL+"/xai"); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	t.Setenv("ERDAI_TEST_XAI_FALLBACK_KEY", "xai-test-key")
	t.Setenv("ERDAI_TEST_GROK_FALLBACK_KEY", "grok-test-key")

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		SearchBaseURL: service.URL + "/search?format=rss", HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	text, sources, err := runtime.grokResearch(context.Background(), "xAI latest update")
	if err != nil || text == "" || len(sources) != 1 {
		t.Fatalf("native search result = %q %+v err=%v", text, sources, err)
	}
	if rssCalls.Load() != 1 {
		t.Fatalf("RSS fallback calls = %d", rssCalls.Load())
	}
}
