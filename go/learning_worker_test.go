package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGrokLearningCreatesReviewableCandidateWhenDue(t *testing.T) {
	var grokCalls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/grok/responses":
			grokCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer grok-learning-key" {
				t.Fatal("Grok credential missing")
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"output": []any{map[string]any{
					"type": "message",
					"content": []any{map[string]any{
						"type": "output_text", "text": "近期结论，仍需管理员核对。",
						"annotations": []any{map[string]string{"type": "url_citation", "url": "https://example.test/chinese-voice", "title": "中文互联网口语观察"}},
					}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "searchConnectionId": "xai-learning", "searchModel": "grok-test",
		"learningWorkerEnabled": true, "learningPollSeconds": 600,
	})
	if _, err := configDB.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('xai-learning', 'xai-learning', 'xai_responses', ?, 'ERDAI_TEST_XAI_LEARNING_KEY', 20, 1, '2026-08-07T00:00:00Z', '2026-08-07T00:00:00Z')`, service.URL+"/grok"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERDAI_TEST_XAI_LEARNING_KEY", "grok-learning-key")
	if _, err := configDB.Exec(`
		UPDATE runtime_config SET learning_enabled = 1,
			learning_topics_json = '["中文互联网口语"]', learning_interval_hours = 24,
			last_collected_at = NULL WHERE id = 1
	`); err != nil {
		t.Fatal(err)
	}
	_ = configDB.Close()

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-learning-key",
		SearchBaseURL: service.URL + "/search?format=rss",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)),
		HTTPClient:    service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	now := time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC)
	created, err := runtime.collectLearningCandidates(context.Background(), now)
	if err != nil || created != 1 {
		t.Fatalf("collection = %d, err=%v", created, err)
	}
	var status, title, content, sourceURI, tags string
	if err = runtime.configStore.db.QueryRow(`
		SELECT status, title, content, source_uri, tags_json
		FROM knowledge_candidates LIMIT 1
	`).Scan(&status, &title, &content, &sourceURI, &tags); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || !strings.Contains(title, "中文互联网口语") ||
		!strings.Contains(content, "管理员核对") || sourceURI != "https://example.test/chinese-voice" ||
		!strings.Contains(tags, "auto-learning") {
		t.Fatalf("candidate = %q %q %q %q %q", status, title, content, sourceURI, tags)
	}
	var collectedAt string
	if err = runtime.configStore.db.QueryRow("SELECT last_collected_at FROM runtime_config WHERE id = 1").Scan(&collectedAt); err != nil {
		t.Fatal(err)
	}
	if collectedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("last collected = %q", collectedAt)
	}
	var auditActor string
	if err = runtime.configStore.db.QueryRow(`
		SELECT actor FROM audit_events WHERE target_type = 'knowledge_candidate' LIMIT 1
	`).Scan(&auditActor); err != nil || auditActor != "system:grok-learning" {
		t.Fatalf("audit actor = %q, err=%v", auditActor, err)
	}

	created, err = runtime.collectLearningCandidates(context.Background(), now.Add(2*time.Hour))
	if err != nil || created != 0 || grokCalls.Load() != 1 {
		t.Fatalf("early repeat = %d, err=%v, calls=%d", created, err, grokCalls.Load())
	}
}

func TestGrokLearningHonorsBothEnablementSwitches(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "learningWorkerEnabled": false, "learningPollSeconds": 1,
	})
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-learning-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{5}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.learningPollInterval(context.Background()) != minimumLearningPoll {
		t.Fatal("learning poll interval was not clamped")
	}
	created, err := runtime.collectLearningCandidates(context.Background(), time.Now().UTC())
	if err != nil || created != 0 {
		t.Fatalf("disabled collection = %d, err=%v", created, err)
	}
}
