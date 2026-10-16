package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectRoutesRespectPreparedToolPermissions(t *testing.T) {
	for _, media := range []struct {
		name, adapter, message, capability string
		edit                               bool
	}{
		{"grok-image", "grok_generate_image", "画一张窗边的小猫", "image_generation", false},
		{"natural-image", "grok_generate_image", "给张照片", "image_generation", false},
		{"image", "generate_image", "画一张窗边的小猫", "image_generation", false},
		{"edit", "grok_generate_image", "把我的头像优化一下", "image_generation", true},
		{"video", "grok_generate_video", "生成一段海边日落视频", "video_generation", false},
		{"search", "grok_web_search", "帮我搜索北京天气", "web_search", false},
	} {
		for _, mode := range []string{"allowed", "tool-disabled", "instance-denied", "instance-allowlist", "approval-required"} {
			t.Run(media.name+"/"+mode, func(t *testing.T) {
				var providerCalls atomic.Int32
				var provider *httptest.Server
				provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodPost && r.URL.Path == "/search/responses":
						providerCalls.Add(1)
						writeJSON(w, http.StatusOK, map[string]any{"output": []any{map[string]any{
							"type": "message", "content": []any{map[string]any{"type": "output_text", "text": "北京天气今天晴朗。"}},
						}}})
					case r.Method == http.MethodPost && (strings.HasSuffix(r.URL.Path, "/images/generations") || strings.HasSuffix(r.URL.Path, "/images/edits")):
						providerCalls.Add(1)
						writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{
							"b64_json": strings.TrimPrefix(testVideoPersonaAvatar, "data:image/png;base64,"),
						}}})
					case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
						providerCalls.Add(1)
						writeJSON(w, http.StatusOK, map[string]string{"id": "permission-video", "status": "queued"})
					case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/permission-video":
						writeJSON(w, http.StatusOK, map[string]any{"status": "done", "video": map[string]string{"url": provider.URL + "/asset.mp4"}})
					case r.Method == http.MethodGet && r.URL.Path == "/asset.mp4":
						w.Header().Set("Content-Type", "video/mp4")
						_, _ = w.Write(testMP4())
					default:
						providerCalls.Add(1)
						http.Error(w, "unexpected provider request", http.StatusBadRequest)
					}
				}))
				t.Cleanup(provider.Close)
				configPath, db := newTestCoreConfig(t)
				if _, err := db.Exec("UPDATE model_endpoints SET enabled=0; UPDATE persona_runtime_profiles SET profile_json='{}'"); err != nil {
					t.Fatal(err)
				}
				executionKind := "media"
				if media.name == "search" {
					executionKind = "tool"
				}
				insertTestEndpoint(t, db, "permission-media", "permission-model", []string{media.capability}, executionKind, media.adapter)
				setTestIntegration(t, db, "image_policy", map[string]any{"enabled": true, "model": "image-model", "mediaQualityEnabled": false})
				setTestIntegration(t, db, "provider_policy", map[string]any{"apiBase": provider.URL + "/v1", "defaultModel": "chat-model"})
				setTestIntegration(t, db, "grok_policy", map[string]any{
					"enabled": true, "apiBase": provider.URL + "/grok", "imageModel": "grok-imagine-image", "imageEditModel": "grok-imagine-edit",
					"videoModel": "grok-imagine-video", "videoTimeoutSeconds": 10,
				})
				if media.name == "search" {
					t.Setenv("ERDAI_TEST_DIRECT_SEARCH_KEY", "search-test-key")
					if _, err := db.Exec(`INSERT INTO provider_connections
						(id,provider,protocol,api_base,credential_ref,timeout_seconds,enabled,created_at,updated_at)
						VALUES ('permission-search','permission-search','xai_responses',?,'ERDAI_TEST_DIRECT_SEARCH_KEY',2,1,'now','now')`, provider.URL+"/search"); err != nil {
						t.Fatal(err)
					}
					setTestIntegration(t, db, "grok_policy", map[string]any{
						"enabled": true, "searchConnectionIds": []string{"permission-search"}, "searchModel": "grok-4.5", "searchSummaryMaxChars": 120,
					})
				}
				store := &coreConfigStore{db: db}
				for _, request := range []struct{ path, body string }{
					{"/api/v1/agent-instances", `{"id":"media-instance","displayName":"Media instance","personaId":"doubao"}`},
					{"/api/v1/agent-instances/media-instance/connectors", `{"connectorId":"media-connector"}`},
					{"/api/v1/agent-instance-routes", `{"id":"media-route","instanceId":"media-instance","connectorId":"media-connector","transport":"qq_official"}`},
				} {
					response := agentInstanceRequest(t, store, http.MethodPost, request.path, request.body)
					if response.Code != http.StatusCreated {
						t.Fatalf("fixture %s: %d %s", request.path, response.Code, response.Body.String())
					}
				}
				if err := db.Close(); err != nil {
					t.Fatal(err)
				}
				mediaDir := filepath.Join(t.TempDir(), "media")
				runtime, err := NewAgentRuntime(RuntimeConfig{
					DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
					AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
					ModelAPIKey: "model-test-key", ImageAPIKey: "image-test-key", GrokAPIKey: "grok-test-key",
					EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)),
					MediaDir:      mediaDir, HTTPClient: provider.Client(), VideoPollInterval: time.Millisecond,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = runtime.Close() })
				if media.capability == "video_generation" {
					addVisualPlanReference(t, runtime, "doubao")
				}
				if _, err := runtime.configStore.db.Exec("UPDATE tools SET enabled=1 WHERE name=?", media.adapter); err != nil {
					t.Fatal(err)
				}
				switch mode {
				case "tool-disabled":
					_, err = runtime.configStore.db.Exec("UPDATE tools SET enabled=0 WHERE name=?", media.adapter)
				case "instance-denied":
					profile, _ := json.Marshal(map[string]any{"deniedToolIds": []string{media.adapter}})
					_, err = runtime.configStore.db.Exec("UPDATE agent_instances SET overrides_json=? WHERE id='media-instance'", string(profile))
				case "instance-allowlist":
					_, err = runtime.configStore.db.Exec(`UPDATE agent_instances SET overrides_json='{"allowedToolIds":["ops-status"]}' WHERE id='media-instance'`)
				case "approval-required":
					_, err = runtime.configStore.db.Exec("UPDATE tools SET risk_level=3 WHERE name=?", media.adapter)
				}
				if err != nil {
					t.Fatal(err)
				}
				run := insertQuotaTestRun(t, runtime, "media-permission-run", "media-sender")
				run.Transport, run.TransportInstance, run.AgentInstanceID, run.PersonaID = "qq_official", "media-connector", "media-instance", "doubao"
				if _, err := runtime.db.Exec("UPDATE agent_runs SET transport=?, transport_instance=?, agent_instance_id=?, persona_id=? WHERE id=?", run.Transport, run.TransportInstance, run.AgentInstanceID, run.PersonaID, run.ID); err != nil {
					t.Fatal(err)
				}
				if media.edit {
					if err := os.MkdirAll(mediaDir, 0o700); err != nil {
						t.Fatal(err)
					}
					png, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(testVideoPersonaAvatar, "data:image/png;base64,"))
					if err := os.WriteFile(filepath.Join(mediaDir, "avatar.png"), png, 0o600); err != nil {
						t.Fatal(err)
					}
					run.Attachments = []transportAttachment{{ID: "avatar", Kind: "image", LocalPath: "avatar.png", MimeType: "image/png"}}
				}
				reply, err := runtime.generate(context.Background(), run, media.message)
				if err != nil {
					t.Fatalf("generate failed: %v", err)
				}
				if mode == "allowed" {
					wantAttachments := 1
					if media.name == "search" {
						wantAttachments = 0
						if !strings.Contains(reply.Text, "北京天气") {
							t.Fatalf("allowed search returned no result: %+v", reply)
						}
					}
					if providerCalls.Load() != 1 || len(reply.Attachments) != wantAttachments {
						t.Fatalf("allowed media did not execute: calls=%d reply=%+v", providerCalls.Load(), reply)
					}
					return
				}
				if providerCalls.Load() != 0 || len(reply.Attachments) != 0 || !strings.Contains(reply.Text, "未开启") {
					t.Fatalf("restricted media executed or hid the denial: calls=%d reply=%+v", providerCalls.Load(), reply)
				}
				for _, query := range []string{"SELECT count(*) FROM media_quota_usage", "SELECT count(*) FROM agent_deliveries"} {
					var count int
					if err := runtime.db.QueryRow(query).Scan(&count); err != nil || count != 0 {
						t.Fatalf("denial created quota/progress state: query=%s count=%d err=%v", query, count, err)
					}
				}
			})
		}
	}
}
