package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPlainChatCapsCompletionBudget(t *testing.T) {
	requests := make(chan map[string]any, 1)
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		requests <- request
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"role": "assistant", "content": "在。"},
		}}})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	reply, err := runtime.runAgentLoopWithTargets(
		context.Background(), runRecord{}, "在吗", "自然接话。",
		[]runtimeProviderTarget{{EndpointID: "chat", Model: "chat-model", APIBase: service.URL}},
		runtimeToolPolicy{Authority: "member"}, runtimeMessagePolicy{},
	)
	if err != nil || reply.Text != "在。" {
		t.Fatalf("reply = %+v, err=%v", reply, err)
	}
	request := <-requests
	if got := int(request["max_tokens"].(float64)); got != defaultChatCompletionMaxToken {
		t.Fatalf("max_tokens = %d", got)
	}
}

func TestPlainChatBoundsProviderFallbacks(t *testing.T) {
	targets := []runtimeProviderTarget{
		{EndpointID: "one", TimeoutSeconds: 120, ProviderRetries: 3},
		{EndpointID: "two", TimeoutSeconds: 30, ProviderRetries: 2},
		{EndpointID: "three", TimeoutSeconds: 10, ProviderRetries: 1},
	}
	bounded := boundedProviderTargets("在吗", false, targets)
	if len(bounded) != 3 {
		t.Fatalf("bounded target count = %d", len(bounded))
	}
	for _, target := range bounded {
		if target.TimeoutSeconds != 8 || target.ProviderRetries != 0 {
			t.Fatalf("unbounded target = %+v", target)
		}
	}
	if targets[0].TimeoutSeconds != 120 || targets[0].ProviderRetries != 3 {
		t.Fatal("source targets were mutated")
	}
	if got := boundedProviderTargets("搜索今天的 AI 新闻", false, targets); len(got) != len(targets) {
		t.Fatalf("tool request was bounded: %d", len(got))
	}
}

func TestFinalizerBlocksUnbackedMediaPromise(t *testing.T) {
	runtime := &AgentRuntime{}
	reply := runtime.finalizeAgentReplyKey(
		context.Background(), "发张自拍", "", "", "", nil, 0, nil,
		runtimeMessagePolicy{}, agentReply{Text: "等会拍好发你照片。"}, false,
	)
	if replyMakesUnbackedMediaPromise(reply.Text) || reply.Text != "这次还没真做出来，先别等。" {
		t.Fatalf("unguarded reply = %q", reply.Text)
	}
	withAttachment := runtime.finalizeAgentReplyKey(
		context.Background(), "发张自拍", "", "", "", nil, 0, nil,
		runtimeMessagePolicy{}, agentReply{Text: "拍好发你。", Attachments: []agentAttachment{{Kind: "image"}}}, false,
	)
	if withAttachment.Text != "拍好发你。" {
		t.Fatalf("real media completion was rewritten: %q", withAttachment.Text)
	}
	if replyMakesUnbackedMediaPromise("照片稍后更新说明。") {
		t.Fatal("ordinary media statement was treated as a promise")
	}
}

func TestProgressMessageIgnoresUnknownShortTool(t *testing.T) {
	call := chatToolCall{}
	call.Function.Name = "calculator"
	if got := progressMessage(runtimeMessagePolicy{}, []chatToolCall{call}, "算一下"); got != "" {
		t.Fatalf("short tool progress = %q", got)
	}
}

func TestAgentToolLoopRunsBuiltInsAndPersistsImageAttachment(t *testing.T) {
	var modelCalls atomic.Int32
	var toolResults atomic.Int32
	var xaiCalls atomic.Int32
	var observedToolResults atomic.Value
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{1}, 32)...)
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			call := modelCalls.Add(1)
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if call == 1 {
				tools, _ := request["tools"].([]any)
				encodedTools := string(mustJSON(t, tools))
				for _, name := range []string{"grok_web_search", "grok_generate_image", "query_ops_status"} {
					if !strings.Contains(encodedTools, name) {
						t.Fatalf("model tools missing %s: %s", name, encodedTools)
					}
				}
				writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
					"message": map[string]any{
						"role": "assistant", "content": "",
						"tool_calls": []any{
							toolCallResponse("search-1", "grok_web_search", `{"query":"today's AI news"}`),
							toolCallResponse("search-2", "grok_web_search", `{"query":"today's AI news"}`),
							toolCallResponse("image-1", "grok_generate_image", `{"prompt":"a red square"}`),
							toolCallResponse("ops-1", "query_ops_status", `{}`),
						},
					},
				}}})
				return
			}
			messages, _ := request["messages"].([]any)
			observed := make([]string, 0, 3)
			for _, item := range messages {
				message, _ := item.(map[string]any)
				if message["role"] == "tool" {
					content := stringValue(message["content"])
					observed = append(observed, stringValue(message["tool_call_id"])+":"+content)
					if strings.Contains(content, `"ok":true`) {
						toolResults.Add(1)
					}
				}
			}
			observedToolResults.Store(observed)
			writeJSON(w, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "弄好了，图也在下面。"}}},
			})
		case "/grok/responses":
			xaiCalls.Add(1)
			if r.Header.Get("Authorization") != "Bearer grok-test-key" {
				t.Fatal("Grok credential missing")
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"output": []any{map[string]any{
					"type": "message",
					"content": []any{map[string]any{
						"type": "output_text", "text": "AI news is current.",
						"annotations": []any{map[string]string{"type": "url_citation", "url": "https://example.test/ai", "title": "AI update"}},
					}},
				}},
				"usage": map[string]int{"input_tokens": 10, "output_tokens": 5, "total_tokens": 15},
			})
		case "/grok/images/generations":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			if request["response_format"] != "b64_json" {
				t.Fatalf("Grok response format = %v", request["response_format"])
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			})
		case "/ops/status":
			if r.URL.Query().Get("token") != "ops-test-token" {
				t.Fatal("OPS token missing")
			}
			if r.Header.Get("User-Agent") != "ErDai-Agent-OPS/"+erdaiRuntimeVersion {
				t.Fatalf("OPS user agent = %q", r.Header.Get("User-Agent"))
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{
				map[string]any{"name": "B", "rate_multiplier": 0.8, "timeline": []any{map[string]string{"status": "down"}}},
				map[string]any{"name": "A", "rate_multiplier": 0.4, "timeline": []any{map[string]string{"status": "operational"}}},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "provider_policy", map[string]any{
		"apiBase": service.URL + "/v1", "defaultModel": "fake-model",
	})
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "searchConnectionId": "xai-test", "searchModel": "search", "imageModel": "image",
	})
	if _, err := configDB.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('xai-test', 'xai-test', 'xai_responses', ?, 'ERDAI_TEST_XAI_KEY', 20, 1, '2026-08-07T00:00:00Z', '2026-08-07T00:00:00Z')`, service.URL+"/grok"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERDAI_TEST_XAI_KEY", "grok-test-key")
	setTestIntegration(t, configDB, "ops_policy", map[string]any{
		"enabled": true, "statusUrl": service.URL + "/ops/status", "timelinePoints": 10,
		"groupMultipliers": map[string]float64{}, "showMultiplierNote": true,
	})
	if _, err := configDB.Exec("DELETE FROM tools; DELETE FROM skills"); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, configDB, "tool-chat", "fake-model", []string{"chat"}, "llm", "openai")
	insertTestTool(t, configDB, "search", "grok_web_search", "core:grok_web_search")
	insertTestTool(t, configDB, "image", "grok_generate_image", "core:grok_generate_image")
	insertTestTool(t, configDB, "ops", "query_ops_status", "core:query_ops_status")
	insertTestSkill(t, configDB, "tool-loop-test", []string{"grok_web_search", "grok_generate_image", "query_ops_status"})
	_ = configDB.Close()

	mediaDir := filepath.Join(t.TempDir(), "media")
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key", OpsToken: "ops-test-token",
		SearchBaseURL: service.URL + "/search?format=rss",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)),
		MediaDir:      mediaDir, HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	response := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("tools-event", "search, draw, and check OPS", true), "tools-event")
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d: %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	waitForDelivery(t, runtime, accepted.Data.RunID)
	if modelCalls.Load() != 2 || toolResults.Load() != 4 || xaiCalls.Load() != 1 {
		t.Fatalf("model calls = %d, tool results = %d, xai calls = %d, observed = %v", modelCalls.Load(), toolResults.Load(), xaiCalls.Load(), observedToolResults.Load())
	}
	var payloadJSON string
	if err := runtime.db.QueryRow("SELECT payload_json FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'", accepted.Data.RunID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text        string            `json:"text"`
		Attachments []agentAttachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != "弄好了，图也在下面。" || len(payload.Attachments) != 1 {
		t.Fatalf("delivery payload = %+v", payload)
	}
	attachment := payload.Attachments[0]
	if !strings.HasPrefix(attachment.LocalPath, mediaMountRoot+"/image_") || strings.Contains(attachment.LocalPath, "..") {
		t.Fatalf("attachment path = %q", attachment.LocalPath)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, filepath.Base(attachment.LocalPath))); err != nil {
		t.Fatal(err)
	}
}

func TestClearOfficeRequestForcesToolAndEnqueuesDocument(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		choice, _ := request["tool_choice"].(map[string]any)
		function, _ := choice["function"].(map[string]any)
		if function["name"] != "create_office_document" {
			t.Fatalf("Office tool choice = %+v", request["tool_choice"])
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant", "content": "",
				"tool_calls": []any{toolCallResponse("office-1", "create_office_document", `{"format":"docx","title":"豆包","content":"豆包","filename":"豆包"}`)},
			},
		}}})
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "provider_policy", map[string]any{
		"apiBase": service.URL + "/v1", "defaultModel": "fake-tool-model",
	})
	if _, err := configDB.Exec("DELETE FROM tools; DELETE FROM skills"); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, configDB, "office-tool-chat", "fake-tool-model", []string{"chat", "tool_calling"}, "llm", "openai")
	insertTestTool(t, configDB, "create-office", "create_office_document", "core:create_office_document")
	insertTestSkill(t, configDB, "office-create-test", []string{"create_office_document"})
	_ = configDB.Close()

	mediaDir := filepath.Join(t.TempDir(), "media")
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{11}, 32)),
		MediaDir:      mediaDir, HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	response := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent(
		"office-event", "帮我做一个word，里面就放豆包两个字", true,
	), "office-event")
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d: %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	waitForDelivery(t, runtime, accepted.Data.RunID)
	if calls.Load() != 1 {
		t.Fatalf("model calls = %d, want one forced tool call", calls.Load())
	}
	var payloadJSON string
	if err = runtime.db.QueryRow("SELECT payload_json FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'", accepted.Data.RunID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text        string            `json:"text"`
		Attachments []agentAttachment `json:"attachments"`
	}
	if err = json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != "file" ||
		!strings.HasSuffix(payload.Attachments[0].Name, ".docx") {
		t.Fatalf("Office delivery payload = %+v", payload)
	}
	if _, err = os.Stat(filepath.Join(mediaDir, filepath.Base(payload.Attachments[0].LocalPath))); err != nil {
		t.Fatal(err)
	}
}

func TestGrokImageFallsBackToStandardProviderWhenGrokIsUnavailable(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{7}, 32)...)
	var grokCalls, standardCalls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/grok/images/generations":
			grokCalls.Add(1)
			http.Error(w, "provider credential rejected", http.StatusForbidden)
		case "/standard/images/generations":
			standardCalls.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["response_format"] != "b64_json" {
				t.Fatalf("standard response format = %v", payload["response_format"])
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "imageModel": "grok-imagine-image", "imageEditModel": "grok-imagine-edit",
	})
	setTestIntegration(t, configDB, "image_policy", map[string]any{
		"enabled": true, "model": "gpt-image-2",
	})
	setTestIntegration(t, configDB, "provider_policy", map[string]any{
		"apiBase": service.URL + "/standard", "defaultModel": "chat-model",
	})
	_ = configDB.Close()

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key", ImageAPIKey: "image-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir:      filepath.Join(t.TempDir(), "media"), HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	result, err := runtime.generateImage(context.Background(), "a green square", true)
	if err != nil || len(result.Attachments) != 1 || grokCalls.Load() != 1 || standardCalls.Load() != 1 {
		t.Fatalf("fallback result = %+v, calls = %d/%d, err = %v", result, grokCalls.Load(), standardCalls.Load(), err)
	}
}

func TestImageProviderAttemptTimeoutKeepsFallbackBudget(t *testing.T) {
	if got := imageProviderAttemptTimeout(600, true); got != defaultGrokImageAttemptTimeout {
		t.Fatalf("Grok attempt timeout = %s, want %s", got, defaultGrokImageAttemptTimeout)
	}
	if got := imageProviderAttemptTimeout(600, false); got != defaultImageAttemptTimeout {
		t.Fatalf("standard image attempt timeout = %s, want %s", got, defaultImageAttemptTimeout)
	}
	if got := imageProviderAttemptTimeout(3, true); got != 3*time.Second {
		t.Fatalf("configured attempt timeout = %s, want 3s", got)
	}
}

func TestFitImageProviderPromptUsesUTF8ByteLimitAndKeepsConstraints(t *testing.T) {
	prompt := "人物核心：" + strings.Repeat("年轻甜美、五官稳定；", 500) + "\n末尾约束：现实摄影、明确成年。"
	got := fitImageProviderPrompt(prompt)
	if len([]byte(got)) > maxImagePromptBytes {
		t.Fatalf("prompt bytes = %d, want <= %d", len([]byte(got)), maxImagePromptBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("prompt is not valid UTF-8")
	}
	if !strings.HasPrefix(got, "人物核心：") || !strings.Contains(got, "末尾约束：现实摄影、明确成年。") {
		t.Fatalf("prompt lost required head or tail: %q", got)
	}
}

func TestImageTaskTimeoutUsesImagePolicy(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "image_policy", map[string]any{
		"enabled": true, "model": "image-model", "timeoutSeconds": 90,
	})
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir: filepath.Join(t.TempDir(), "media"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := runtime.imageTaskTimeout(context.Background(), 30); got != 90*time.Second {
		t.Fatalf("image task timeout = %s, want 90s", got)
	}
}

func TestImageTaskTimeoutCapsLongPolicyWithoutRestoringShortGate(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "image_policy", map[string]any{
		"enabled": true, "model": "image-model", "timeoutSeconds": 1800,
	})
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir: filepath.Join(t.TempDir(), "media"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if got := runtime.imageTaskTimeout(context.Background(), 30); got != 15*time.Minute {
		t.Fatalf("image task timeout = %s, want 15m", got)
	}
}

func TestGrokSelfImageUsesActivePersonaAvatarReference(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	const avatar = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"
	var edits, generations atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/grok/images/edits":
			edits.Add(1)
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			image, _ := payload["image"].(map[string]any)
			if image["url"] != avatar || payload["model"] != "grok-imagine-edit" {
				t.Fatalf("reference payload = %+v", payload)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			})
		case "/grok/images/generations":
			generations.Add(1)
			http.Error(w, "unexpected text-only generation", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "imageModel": "grok-imagine-image", "imageEditModel": "grok-imagine-edit",
	})
	if _, err = configDB.Exec("UPDATE personas SET avatar_data_uri = ? WHERE id = 'doubao'", avatar); err != nil {
		t.Fatal(err)
	}
	_ = configDB.Close()

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir:      filepath.Join(t.TempDir(), "media"), HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	result, err := runtime.generateImage(context.Background(), "来一张你的自拍", true)
	if err != nil || len(result.Attachments) != 1 || edits.Load() != 1 || generations.Load() != 0 {
		t.Fatalf("reference result = %+v, calls = %d/%d, err = %v", result, edits.Load(), generations.Load(), err)
	}
}

func TestInboundImageEditUsesDurableAttachmentAsReference(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	var edits atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/grok/images/edits" {
			http.NotFound(w, r)
			return
		}
		edits.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		image, _ := payload["image"].(map[string]any)
		reference, _ := image["url"].(string)
		if !strings.HasPrefix(reference, "data:image/png;base64,") || payload["model"] != "grok-imagine-edit" {
			t.Fatalf("edit payload = %+v", payload)
		}
		if !strings.Contains(stringArgument(payload, "prompt"), "把我的头像优化一下") {
			t.Fatalf("edit prompt = %v", payload["prompt"])
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
		})
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "imageModel": "grok-imagine-image", "imageEditModel": "grok-imagine-edit",
	})
	_ = configDB.Close()
	mediaDir := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(mediaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mediaDir, "avatar.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir:      mediaDir, HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 3, 3); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`
		INSERT INTO agent_runs
			(id, event_id, reply_handle, conversation_ref, sender_ref, persona_id, state, created_at, updated_at)
		VALUES ('avatar-edit-run', 'avatar-edit-event', 'avatar-edit-reply', 'group-one', 'sender-one', 'doubao', 'running', ?, ?)
	`, now, now); err != nil {
		t.Fatal(err)
	}
	reply, err := runtime.generate(context.Background(), runRecord{
		ID: "avatar-edit-run", EventID: "avatar-edit-event", ReplyHandle: "avatar-edit-reply",
		SenderRef: "sender-one", ConversationRef: "group-one", PersonaID: "doubao",
		Attachments: []transportAttachment{{ID: "avatar", Kind: "image", LocalPath: "avatar.png", MimeType: "image/png"}},
	}, "把我的头像优化一下")
	if err != nil || len(reply.Attachments) != 1 || edits.Load() != 1 {
		t.Fatalf("edit reply = %+v calls=%d err=%v", reply, edits.Load(), err)
	}
}

func TestGrokSelfImageFallsBackWhenReferenceEditIsUnavailable(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	const avatar = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"
	var edits, generations atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		switch r.URL.Path {
		case "/grok/images/edits":
			edits.Add(1)
			if payload["model"] != "grok-imagine-edit" {
				t.Fatalf("edit model = %v", payload["model"])
			}
			http.Error(w, "model unavailable", http.StatusNotFound)
		case "/grok/images/generations":
			generations.Add(1)
			if payload["model"] != "grok-imagine-image" || payload["image"] != nil {
				t.Fatalf("generation payload = %+v", payload)
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "grok_policy", map[string]any{
		"enabled": true, "apiBase": service.URL + "/grok", "imageModel": "grok-imagine-image", "imageEditModel": "grok-imagine-edit",
	})
	if _, err = configDB.Exec("UPDATE personas SET avatar_data_uri = ? WHERE id = 'doubao'", avatar); err != nil {
		t.Fatal(err)
	}
	_ = configDB.Close()

	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
		MediaDir:      filepath.Join(t.TempDir(), "media"), HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	result, err := runtime.generateImage(context.Background(), "来一张你的自拍", true)
	if err != nil || len(result.Attachments) != 1 || edits.Load() != 1 || generations.Load() != 1 {
		t.Fatalf("fallback result = %+v, calls = %d/%d, err = %v", result, edits.Load(), generations.Load(), err)
	}
}

func TestDownloadImageRewritesPrivateProviderLoopbackURL(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte{9}, 32)...)
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/images/result.png" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(png)
	}))
	defer service.Close()
	serviceURL, err := url.Parse(service.URL)
	if err != nil {
		t.Fatal(err)
	}
	dialer := &net.Dialer{}
	client := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == "grok2api-local:"+serviceURL.Port() {
			address = serviceURL.Host
		}
		return dialer.DialContext(ctx, network, address)
	}}}
	runtime := &AgentRuntime{client: client}
	providerBase := "http://grok2api-local:" + serviceURL.Port() + "/v1"
	rawURL := "http://127.0.0.1:8000/images/result.png"
	image, err := runtime.downloadImage(context.Background(), rawURL, providerBase)
	if err != nil || !bytes.Equal(image, png) {
		t.Fatalf("loopback image download = %d bytes, err = %v", len(image), err)
	}
}

func TestMediaRouteGeneratesImageWithoutCallingChatProvider(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	type capturedImageRequest struct {
		Authorization string
		Payload       map[string]any
	}
	imageRequest := make(chan capturedImageRequest, 1)
	releaseImage := make(chan struct{})
	var chatCalls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/chat/completions":
			chatCalls.Add(1)
			http.Error(w, "chat provider must not be called", http.StatusInternalServerError)
		case "/v1/images/generations":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			imageRequest <- capturedImageRequest{
				Authorization: r.Header.Get("Authorization"), Payload: payload,
			}
			<-releaseImage
			writeJSON(w, http.StatusOK, map[string]any{
				"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(png)}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()

	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "image_policy", map[string]any{
		"enabled": true, "model": "gpt-image-2",
	})
	setTestIntegration(t, configDB, "provider_policy", map[string]any{
		"apiBase": service.URL + "/v1", "defaultModel": "must-not-be-used",
	})
	setTestIntegration(t, configDB, "message_policy", map[string]any{
		"toolProgressImageMessages":   []string{"我去画，马上给你。"},
		"toolCompletionImageMessages": []string{"画好了，给你。"},
	})
	insertTestEndpoint(t, configDB, "image-route", "gpt-image-2", []string{"image_generation"}, "media", "generate_image")
	_ = configDB.Close()

	mediaDir := filepath.Join(t.TempDir(), "media")
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", ImageAPIKey: "image-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{4}, 32)),
		MediaDir:      mediaDir, HTTPClient: service.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	defer func() {
		select {
		case <-releaseImage:
		default:
			close(releaseImage)
		}
	}()

	message := "画一张白底绿色圆形图标"
	response := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("media-event", message, true), "media-event")
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d: %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)

	var captured capturedImageRequest
	select {
	case captured = <-imageRequest:
	case <-time.After(3 * time.Second):
		t.Fatal("media route did not call image provider")
	}
	if captured.Authorization != "Bearer image-test-key" {
		t.Fatal("image provider credential missing")
	}
	if captured.Payload["model"] != "gpt-image-2" || captured.Payload["prompt"] != message {
		t.Fatalf("image request = %+v", captured.Payload)
	}

	lease := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/lease", map[string]any{
		"consumerId": "test", "limit": 1, "leaseSeconds": 30,
	}, "")
	var progress struct {
		Data []struct {
			ID      string `json:"id"`
			Phase   string `json:"phase"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
		} `json:"data"`
	}
	decodeRecorder(t, lease, &progress)
	if len(progress.Data) != 1 || progress.Data[0].Phase != "progress" || progress.Data[0].Message.Text != "我去画，马上给你。" {
		t.Fatalf("progress delivery = %+v", progress.Data)
	}
	ack := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/"+progress.Data[0].ID+"/ack", map[string]any{}, "")
	if ack.Code != http.StatusOK {
		t.Fatalf("progress ack = %d: %s", ack.Code, ack.Body.String())
	}

	close(releaseImage)
	waitForDelivery(t, runtime, accepted.Data.RunID)
	if chatCalls.Load() != 0 {
		t.Fatalf("chat provider calls = %d", chatCalls.Load())
	}
	var payloadJSON string
	if err := runtime.db.QueryRow(
		"SELECT payload_json FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'",
		accepted.Data.RunID,
	).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text        string            `json:"text"`
		Attachments []agentAttachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Text != "画好了，给你。" || len(payload.Attachments) != 1 {
		t.Fatalf("terminal payload = %+v", payload)
	}
	attachment := payload.Attachments[0]
	if !strings.HasPrefix(attachment.LocalPath, mediaMountRoot+"/image_") {
		t.Fatalf("attachment path = %q", attachment.LocalPath)
	}
	if _, err := os.Stat(filepath.Join(mediaDir, filepath.Base(attachment.LocalPath))); err != nil {
		t.Fatal(err)
	}
	leaseAndAckOne(t, runtime, "terminal")
	var state string
	if err := runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = ?", accepted.Data.RunID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("run state = %s", state)
	}
}

func TestToolExecutionFailsClosedForApprovalAndAuthority(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	call := chatToolCall{ID: "call-1", Type: "function"}
	call.Function.Name = "grok_web_search"
	call.Function.Arguments = `{"query":"private"}`

	policy := runtimeToolPolicy{Authority: "member", Tools: []runtimeTool{{
		Name: "grok_web_search", AdapterRef: "core:grok_web_search", RiskLevel: 1, ApprovalMode: "confirm",
	}}}
	result := runtime.executeToolCall(context.Background(), runRecord{}, "ordinary chat", policy, nil, call)
	if !strings.Contains(result.Content, "approval_required") {
		t.Fatalf("approval result = %s", result.Content)
	}
	if definitions := authorizedToolDefinitions(policy, false, "ordinary chat", false); len(definitions) != 0 {
		t.Fatalf("approval-required tool was exposed to the model")
	}

	policy.Tools[0].RiskLevel = 0
	policy.Tools[0].ApprovalMode = "auto"
	adminRun := runRecord{IsAdmin: true}
	result = runtime.executeToolCall(context.Background(), adminRun, "ordinary chat", policy, nil, call)
	if !strings.Contains(result.Content, "authority_mismatch") {
		t.Fatalf("authority result = %s", result.Content)
	}
	if definitions := authorizedToolDefinitions(policy, true, "ordinary chat", false); len(definitions) != 0 {
		t.Fatalf("authority mismatch exposed %d tools", len(definitions))
	}
}

func TestExplicitSameTurnConfirmationOnlyExposesRequestedImageTool(t *testing.T) {
	policy := runtimeToolPolicy{Authority: "member", Tools: []runtimeTool{{
		Name: "generate_image", AdapterRef: "core:generate_image", RiskLevel: 1,
		ApprovalMode: "confirm", InputSchema: map[string]any{"type": "object"},
	}}}
	if definitions := authorizedToolDefinitions(policy, false, "随便聊聊", false); len(definitions) != 0 {
		t.Fatalf("ordinary chat exposed %d confirmed tools", len(definitions))
	}
	if definitions := authorizedToolDefinitions(policy, false, "帮我画一张海边日落", false); len(definitions) != 1 {
		t.Fatalf("explicit image request exposed %d tools", len(definitions))
	}
	photoRequest := "那你能给我一张你的自拍吗"
	if definitions := authorizedToolDefinitions(policy, false, photoRequest, false); len(definitions) != 1 {
		t.Fatalf("selfie request exposed %d tools", len(definitions))
	}
	call := chatToolCall{}
	call.Function.Name = "generate_image"
	messagePolicy := runtimeMessagePolicy{
		ToolProgressImageMessages: []string{"我去画。"},
		ToolProgressPhotoMessages: []string{"那你等我一会呀，我去拍一张。"},
	}
	if progress := progressMessage(messagePolicy, []chatToolCall{call}, photoRequest); progress != "那你等我一会呀，我去拍一张。" {
		t.Fatalf("selfie progress = %q", progress)
	}
	if !containsSensitiveMemory("记住我的 API key 是 sk-secret") {
		t.Fatal("sensitive memory marker was not rejected")
	}
}

func TestSearchBaseURLDefaultsAndRejectsUnsafeURLs(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if runtime.searchBaseURL != defaultSearchBaseURL {
		t.Fatalf("search base = %q", runtime.searchBaseURL)
	}
	for _, value := range []string{
		"http://search.example.test/rss",
		"https://user:secret@search.example.test/rss",
		"https://search.example.test/rss#fragment",
	} {
		if _, err := secureSearchBase(value); err == nil {
			t.Fatalf("unsafe search base accepted: %s", value)
		}
	}
	if value, err := secureServiceBase("http://grok2api-local:8000/v1"); err != nil ||
		value != "http://grok2api-local:8000/v1" {
		t.Fatalf("private Grok service base = %q, %v", value, err)
	}
	if _, err := secureServiceBase("http://public.example.test/v1"); err == nil {
		t.Fatal("public HTTP tool service was accepted")
	}
}

func TestProgressDeliveryAckDoesNotFinishRun(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run := runRecord{ID: "run-progress", EventID: "event-progress", ReplyHandle: "reply-progress"}
	_, err := runtime.db.Exec(`
		INSERT INTO agent_runs (
			id, event_id, reply_handle, conversation_ref, sender_ref, input_cipher,
			is_admin, state, created_at, updated_at
		) VALUES (?, ?, ?, 'group', 'sender', NULL, 0, 'running', ?, ?)
	`, run.ID, run.EventID, run.ReplyHandle, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.enqueueDelivery(run, agentReply{Text: "still working"}, "progress", ""); err != nil {
		t.Fatal(err)
	}
	leaseAndAckOne(t, runtime, "progress")
	var state string
	if err := runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = ?", run.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "running" {
		t.Fatalf("progress ack finished run: %s", state)
	}

	if err := runtime.enqueueDelivery(run, agentReply{Text: "done"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	leaseAndAckOne(t, runtime, "terminal")
	if err := runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = ?", run.ID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("terminal ack state = %s", state)
	}
}

func toolCallResponse(id, name, arguments string) map[string]any {
	return map[string]any{
		"id": id, "type": "function", "function": map[string]string{"name": name, "arguments": arguments},
	}
}

func fakeRuntimeTool(name, adapter string) map[string]any {
	return map[string]any{
		"id": name, "name": name, "description": name, "riskLevel": 0,
		"adapterRef": adapter, "approvalMode": "auto", "timeoutSeconds": 10,
		"inputSchema": map[string]any{"type": "object", "additionalProperties": true},
	}
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func waitForDelivery(t *testing.T, runtime *AgentRuntime, runID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var count int
		if err := runtime.db.QueryRow("SELECT count(*) FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'", runID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("delivery was not enqueued")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func leaseAndAckOne(t *testing.T, runtime *AgentRuntime, phase string) {
	t.Helper()
	lease := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/lease", map[string]any{
		"consumerId": "test", "limit": 1, "leaseSeconds": 30,
	}, "")
	var body struct {
		Data []struct {
			ID    string `json:"id"`
			Phase string `json:"phase"`
		} `json:"data"`
	}
	decodeRecorder(t, lease, &body)
	if len(body.Data) != 1 || body.Data[0].Phase != phase {
		t.Fatalf("leased %s = %+v", phase, body.Data)
	}
	ack := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/"+body.Data[0].ID+"/ack", map[string]any{}, "")
	if ack.Code != http.StatusOK {
		t.Fatalf("ack %s = %d: %s", phase, ack.Code, ack.Body.String())
	}
}
