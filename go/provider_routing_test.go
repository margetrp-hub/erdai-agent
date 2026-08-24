package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProviderConnectionCRUDUsesCredentialReferenceWithoutSecret(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_TEST_PROVIDER_KEY", "secret-value-must-not-leak")
	payload := map[string]any{
		"provider": "test-provider", "protocol": "openai_chat_completion",
		"apiBase": "https://provider.example/v1", "credentialRef": "ERDAI_TEST_PROVIDER_KEY",
		"timeoutSeconds": 9, "enabled": true,
	}
	request := httptest.NewRequest(http.MethodPut, "/api/v1/provider-connections/test-connection", strings.NewReader(string(mustJSON(t, payload))))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	if err := runtime.configStore.handleProviderConnections(recorder, request, request.URL.Path); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "secret-value-must-not-leak") ||
		!strings.Contains(recorder.Body.String(), `"credentialConfigured":true`) {
		t.Fatalf("provider connection response = %d %s", recorder.Code, recorder.Body.String())
	}
	values, err := runtime.configStore.listProviderConnections()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, value := range values {
		if value.ID == "test-connection" {
			found = value.CredentialRef == "ERDAI_TEST_PROVIDER_KEY" && value.CredentialReady
		}
	}
	if !found {
		t.Fatalf("provider connection was not persisted: %+v", values)
	}
}

func TestProviderConnectionTestSupportsMediaOnlyConnection(t *testing.T) {
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" {
			t.Fatalf("media health request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer media-test-secret" {
			t.Fatalf("media health authorization = %q", r.Header.Get("Authorization"))
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"id": "video-model"}}})
	}))
	defer service.Close()

	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	t.Setenv("ERDAI_MEDIA_TEST_KEY", "media-test-secret")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.configStore.db.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('media-only-connection', 'media-test', 'openai_compatible', ?,
			'ERDAI_MEDIA_TEST_KEY', 5, 1, ?, ?)`, service.URL+"/v1", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoints
		(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
		 quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
		VALUES ('media-only-endpoint', 'media-test', 'video-model', 1, '["video_generation"]',
			0, 0, 1, 1, 0, 'media', 'grok_generate_video', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoint_connections
		(endpoint_id, connection_id, updated_at)
		VALUES ('media-only-endpoint', 'media-only-connection', ?)`, now); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost,
		"/api/v1/provider-connections/media-only-connection/test", nil)
	recorder := httptest.NewRecorder()
	if err := runtime.handleProviderConnectionTest(recorder, request, request.URL.Path); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK ||
		!strings.Contains(recorder.Body.String(), `"endpointId":"media-only-endpoint"`) ||
		!strings.Contains(recorder.Body.String(), `"healthy":true`) {
		t.Fatalf("media connection test = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestProviderHealthRejectsMissingConfiguredModel(t *testing.T) {
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"id": "different-model"}}})
	}))
	defer service.Close()

	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	t.Setenv("ERDAI_MISSING_MODEL_KEY", "missing-model-secret")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.configStore.db.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('missing-model-connection', 'missing-model-provider', 'xai_responses', ?,
			'ERDAI_MISSING_MODEL_KEY', 5, 1, ?, ?)`, service.URL+"/v1", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoints
		(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
		 quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
		VALUES ('missing-model-endpoint', 'missing-model-provider', 'expected-model', 1, '["web_search"]',
			0, 0, 1, 1, 0, 'tool', 'grok_web_search', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoint_connections
		(endpoint_id, connection_id, updated_at) VALUES ('missing-model-endpoint', 'missing-model-connection', ?)`, now); err != nil {
		t.Fatal(err)
	}

	if err := runtime.checkOneProvider(context.Background(), "missing-model-endpoint", "missing-model-provider",
		"expected-model", "tool", `["web_search"]`); err != nil {
		t.Fatal(err)
	}
	var healthy int
	var message string
	if err := runtime.configStore.db.QueryRow(`SELECT healthy, status_message FROM model_health
		WHERE endpoint_id = 'missing-model-endpoint'`).Scan(&healthy, &message); err != nil {
		t.Fatal(err)
	}
	if healthy != 1 || !strings.Contains(message, "not exposed") {
		t.Fatalf("first failed probe health=%d message=%q", healthy, message)
	}
}

func TestProviderRouteTargetsUseBoundConnectionsAndCrossProviderFallback(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := calls.Add(1)
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		switch r.URL.Path {
		case "/primary/v1/chat/completions":
			if call != 1 || r.Header.Get("Authorization") != "Bearer primary-secret" || payload["model"] != "primary-model" {
				t.Errorf("primary request = call:%d auth:%q payload:%+v", call, r.Header.Get("Authorization"), payload)
			}
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": ""},
			}}})
		case "/fallback/v1/chat/completions":
			if r.Header.Get("Authorization") != "Bearer fallback-secret" || payload["model"] != "fallback-model" {
				t.Errorf("fallback request auth:%q payload:%+v", r.Header.Get("Authorization"), payload)
			}
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "接住了。"},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	t.Setenv("ERDAI_PRIMARY_TEST_KEY", "primary-secret")
	t.Setenv("ERDAI_FALLBACK_TEST_KEY", "fallback-secret")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, value := range []struct{ id, provider, path, key string }{
		{"primary-connection", "primary-provider", "/primary/v1", "ERDAI_PRIMARY_TEST_KEY"},
		{"fallback-connection", "fallback-provider", "/fallback/v1", "ERDAI_FALLBACK_TEST_KEY"},
	} {
		if _, err := runtime.configStore.db.Exec(`INSERT INTO provider_connections
			(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
			VALUES (?, ?, 'openai_chat_completion', ?, ?, 5, 1, ?, ?)`,
			value.id, value.provider, service.URL+value.path, value.key, now, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []struct{ id, provider, model, connection string }{
		{"primary-endpoint", "primary-provider", "primary-model", "primary-connection"},
		{"fallback-endpoint", "fallback-provider", "fallback-model", "fallback-connection"},
	} {
		if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoints
			(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
			 quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
			VALUES (?, ?, ?, 1, '["chat"]', 0, 0, 1, 1, 1000, 'llm', 'openai', ?, ?)`,
			value.id, value.provider, value.model, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoint_connections
			(endpoint_id, connection_id, updated_at) VALUES (?, ?, ?)`, value.id, value.connection, now); err != nil {
			t.Fatal(err)
		}
	}
	route := nativeRouteDecision{
		Selected:  &nativeRouteCandidate{Endpoint: nativeModelEndpoint{ID: "primary-endpoint", Provider: "primary-provider", Model: "primary-model", ExecutionKind: "llm"}},
		Fallbacks: []nativeRouteCandidate{{Endpoint: nativeModelEndpoint{ID: "fallback-endpoint", Provider: "fallback-provider", Model: "fallback-model", ExecutionKind: "llm"}}},
	}
	targets, err := runtime.providerRouteTargets(route, providerPolicyConfig{}, nil, "", "")
	if err != nil || len(targets) != 2 {
		t.Fatalf("provider targets = %+v, err=%v", targets, err)
	}
	var attempts int
	completion, used, err := runtime.chatCompletionWithTargets(context.Background(), map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, func(runtimeProviderTarget, time.Duration, error) { attempts++ })
	if err != nil || used != 1 || attempts != 2 || len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "接住了。" {
		t.Fatalf("fallback completion = %+v, used=%d attempts=%d err=%v", completion, used, attempts, err)
	}
}

func TestProviderRouteTargetsFallBackAfterPerTargetTimeout(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch r.URL.Path {
		case "/slow/chat/completions":
			time.Sleep(1500 * time.Millisecond)
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "太晚了。"},
			}}})
		case "/fallback/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "接住了。"},
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	targets := []runtimeProviderTarget{
		{EndpointID: "slow", Model: "slow-model", APIBase: service.URL + "/slow", TimeoutSeconds: 1},
		{EndpointID: "fallback", Model: "fallback-model", APIBase: service.URL + "/fallback", TimeoutSeconds: 2},
	}
	outcomes := []string{}
	completion, used, err := runtime.chatCompletionWithTargets(context.Background(), map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, func(_ runtimeProviderTarget, _ time.Duration, attemptErr error) {
		outcomes = append(outcomes, providerAttemptOutcome(attemptErr))
	})
	if err != nil || used != 1 || calls.Load() != 2 || completion.Choices[0].Message.Content != "接住了。" {
		t.Fatalf("timeout fallback = %+v, used=%d calls=%d err=%v", completion, used, calls.Load(), err)
	}
	if len(outcomes) != 2 || outcomes[0] != "timeout" || outcomes[1] != "succeeded" {
		t.Fatalf("attempt outcomes = %#v", outcomes)
	}
}

func TestProviderRouteTargetsFallBackAfterForbidden(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path == "/primary/chat/completions" {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "account unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"role": "assistant", "content": "接住了。"},
		}}})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	targets := []runtimeProviderTarget{
		{EndpointID: "primary", Model: "primary", APIBase: service.URL + "/primary"},
		{EndpointID: "fallback", Model: "fallback", APIBase: service.URL + "/fallback"},
	}
	completion, used, err := runtime.chatCompletionWithTargets(context.Background(), map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, nil)
	if err != nil || used != 1 || calls.Load() != 2 || completion.Choices[0].Message.Content != "接住了。" {
		t.Fatalf("forbidden fallback = %+v, used=%d calls=%d err=%v", completion, used, calls.Load(), err)
	}
}

func TestProviderHealthRequiresThreeFailuresAndTwoSuccesses(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoints
		(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
		 quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
		VALUES ('health-endpoint', 'health-provider', 'health-model', 1, '["chat"]', 0, 0, 1, 1, 1000, 'llm', 'openai', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	assertHealth := func(wantHealthy, outcome bool, wantFailures int) {
		t.Helper()
		if err := runtime.recordProviderHealth("health-endpoint", outcome, 10, "test"); err != nil {
			t.Fatal(err)
		}
		var healthy, failures int
		if err := runtime.configStore.db.QueryRow(`SELECT healthy, consecutive_failures FROM model_health
			WHERE endpoint_id = 'health-endpoint'`).Scan(&healthy, &failures); err != nil {
			t.Fatal(err)
		}
		if (healthy == 1) != wantHealthy || failures != wantFailures {
			t.Fatalf("health = %d failures=%d, want healthy=%v failures=%d", healthy, failures, wantHealthy, wantFailures)
		}
	}
	assertHealth(true, false, 1)
	assertHealth(true, false, 2)
	assertHealth(false, false, 3)
	assertHealth(false, true, 0)
	assertHealth(true, true, 0)
}

func TestCompanionModelRoutingToggleControlsFallbacks(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	insertTestEndpoint(t, runtime.configStore.db, "route-primary", "route-primary-model", []string{"chat"}, "llm", "openai")
	insertTestEndpoint(t, runtime.configStore.db, "route-fallback", "route-fallback-model", []string{"chat"}, "llm", "openai")
	setTestIntegration(t, runtime.configStore.db, "companion_policy", map[string]any{
		"enableModelRouting": true, "chatModel": "route-primary",
	})
	prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{
		Transport: "qq_official", ConversationRef: "group-routing", Message: "你好",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != "route-primary" || len(prepared.RouteDecision.Fallbacks) == 0 {
		t.Fatalf("automatic route = %+v", prepared.RouteDecision)
	}
	setTestIntegration(t, runtime.configStore.db, "companion_policy", map[string]any{
		"enableModelRouting": false, "chatModel": "route-primary",
	})
	prepared, err = runtime.configStore.prepareRuntime(corePreparePayload{
		Transport: "qq_official", ConversationRef: "group-routing", Message: "你好",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != "route-primary" || len(prepared.RouteDecision.Fallbacks) != 0 {
		t.Fatalf("pinned route = %+v", prepared.RouteDecision)
	}
}

func TestPersonaModelPreferenceDoesNotPinAutomaticRouting(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertTestEndpoint(t, runtime.configStore.db, "persona-unhealthy", "persona-unhealthy-model", []string{"chat"}, "llm", "openai")
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_health
		(endpoint_id, healthy, latency_ms, error_rate, consecutive_failures, status_message, checked_at)
		VALUES ('persona-unhealthy', 0, 10000, 1, 3, 'timeout', ?)`, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
		VALUES ('doubao', '{"personaId":"doubao","chatEndpointId":"persona-unhealthy"}', ?)
		ON CONFLICT(persona_id) DO UPDATE SET profile_json=excluded.profile_json, updated_at=excluded.updated_at`, now); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, runtime.configStore.db, "companion_policy", map[string]any{
		"enableModelRouting": true,
	})
	prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{
		Transport: "qq_official", ConversationRef: "group-persona-routing", Message: "你好",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID == "persona-unhealthy" {
		t.Fatalf("persona preference bypassed automatic health routing: %+v", prepared.RouteDecision)
	}
	if strings.Contains(prepared.RouteDecision.Explanation, "pinned endpoint") {
		t.Fatalf("persona preference unexpectedly pinned route: %s", prepared.RouteDecision.Explanation)
	}
}

func TestPersonaTaskModelDoesNotReplaceSearchToolRoute(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insertTestEndpoint(t, runtime.configStore.db, "persona-task", "persona-task-model", []string{"reasoning"}, "llm", "openai")
	if _, err := runtime.configStore.db.Exec(`INSERT INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
		VALUES ('doubao', '{"personaId":"doubao","taskEndpointId":"persona-task"}', ?)
		ON CONFLICT(persona_id) DO UPDATE SET profile_json=excluded.profile_json, updated_at=excluded.updated_at`, now); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, runtime.configStore.db, "companion_policy", map[string]any{
		"enableModelRouting": true,
	})
	prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{
		Transport: "qq_official", ConversationRef: "group-search-routing", Message: "帮我搜索今天的 AI 新闻",
	})
	if err != nil {
		t.Fatal(err)
	}
	selected := prepared.RouteDecision.Selected
	if prepared.RouteDecision.Lane != "search" || selected == nil ||
		selected.Endpoint.ExecutionKind != "tool" || normalizeAdapterRef(selected.Endpoint.AdapterRef) != "grok_web_search" {
		t.Fatalf("persona task model replaced search route: %+v", prepared.RouteDecision)
	}
}

func TestChatRoutingRejectsToolEndpointsThatAdvertiseChat(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	insertTestEndpoint(t, runtime.configStore.db, "chat-shaped-tool", "chat-shaped-tool-model", []string{"chat"}, "tool", "grok_web_search")
	route, err := runtime.configStore.simulateNativeRoute("chat")
	if err != nil {
		t.Fatal(err)
	}
	if route.Selected == nil || route.Selected.Endpoint.ExecutionKind != "llm" {
		t.Fatalf("chat selected non-LLM endpoint: %+v", route)
	}
	for _, fallback := range route.Fallbacks {
		if fallback.Endpoint.ExecutionKind != "llm" {
			t.Fatalf("chat retained non-LLM fallback: %+v", fallback)
		}
	}
}

func TestRunTimelineListsRouteAndStageDurations(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, transport, reply_handle, conversation_ref, sender_ref, persona_id,
		 selected_endpoint_id, selected_model, route_reason, provider_calls, total_duration_ms,
		 state, created_at, updated_at)
		VALUES ('run-timeline', 'event-timeline', 'qq_official', 'reply', 'group', 'member', 'persona-test',
		 'endpoint-test', 'model-test', 'configured route', 2, 321, 'delivered', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := runtime.recordRunStage("run-timeline", "model_completed", time.Now().Add(-25*time.Millisecond), map[string]any{"model": "model-test"}); err != nil {
		t.Fatal(err)
	}
	list := httptest.NewRecorder()
	if err := runtime.handleRunTimeline(list, httptest.NewRequest(http.MethodGet, "/api/v1/runs", nil), "/api/v1/runs"); err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{`"selectedEndpointId":"endpoint-test"`, `"selectedModel":"model-test"`, `"providerCalls":2`, `"totalDurationMs":321`} {
		if !strings.Contains(list.Body.String(), marker) {
			t.Fatalf("run list missing %s: %s", marker, list.Body.String())
		}
	}
	timeline := httptest.NewRecorder()
	if err := runtime.handleRunTimeline(timeline, httptest.NewRequest(http.MethodGet, "/api/v1/runs/run-timeline", nil), "/api/v1/runs/run-timeline"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(timeline.Body.String(), `"stage":"model_completed"`) || !strings.Contains(timeline.Body.String(), `"durationMs":`) {
		t.Fatalf("run timeline = %s", timeline.Body.String())
	}
}

func TestConfiguredMaxAgentStepsStopsToolLoop(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "tool_calls": []any{
				toolCallResponse("call", "unavailable_tool", `{}`),
			}},
		}}})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, transport, reply_handle, conversation_ref, sender_ref, state, created_at, updated_at)
		VALUES ('step-test', 'step-event', 'qq_official', 'reply', 'group', 'member', 'running', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.runAgentLoopWithModelsKey(
		context.Background(), runRecord{ID: "step-test", ConversationRef: "group", SenderRef: "member"},
		"test", "system", []string{"test-model"}, 0, service.URL, "key",
		runtimeToolPolicy{Authority: "member", MaxAgentSteps: 2}, runtimeMessagePolicy{},
	)
	if err == nil || !strings.Contains(err.Error(), "step limit") || calls.Load() != 2 {
		t.Fatalf("max steps result = calls:%d err:%v", calls.Load(), err)
	}
}

func TestStreamingCompletionAggregatesContentAndRecordsFirstToken(t *testing.T) {
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(5 * time.Millisecond)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"你\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"好\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	var completion chatCompletion
	if err := runtime.postProviderJSON(context.Background(), service.URL, "test-key", map[string]any{"stream": true}, &completion); err != nil {
		t.Fatal(err)
	}
	if len(completion.Choices) != 1 || completion.Choices[0].Message.Content != "你好" || completion.FirstTokenMS < 1 {
		t.Fatalf("stream completion = %+v", completion)
	}
}
