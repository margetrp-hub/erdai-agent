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

func TestGroupParticipationUsesEffectiveDecisionEndpoint(t *testing.T) {
	for _, test := range []struct {
		name               string
		global             string
		role               string
		template           string
		instance           string
		disabledEndpoint   string
		disabledConnection string
		decision           string
		globalModel        string
		wantReasoning      string
		keyword            bool
		wantModel          string
		wantOwned          bool
		wantReason         string
	}{
		{name: "global default", global: "global", wantModel: "global", wantOwned: true, wantReason: "group_participation"},
		{name: "role overrides disabled global", global: "global", role: "role", disabledEndpoint: "global", wantModel: "role", wantOwned: true, wantReason: "group_participation"},
		{name: "template overrides role", global: "global", role: "role", template: "template", wantModel: "template", wantOwned: true, wantReason: "group_participation"},
		{name: "instance overrides template and role", global: "global", role: "role", template: "template", instance: "instance", disabledEndpoint: "global", wantModel: "instance", wantOwned: true, wantReason: "group_participation"},
		{name: "instance alone still runs rejecting gate", instance: "instance", decision: `{"action":"ignore"}`, wantModel: "instance", wantReason: "model_declined"},
		{name: "keyword uses instance endpoint", global: "global", instance: "instance", disabledEndpoint: "global", keyword: true, wantModel: "instance", wantOwned: true, wantReason: "trigger_keyword"},
		{name: "disabled instance cannot fall back", global: "global", instance: "instance", disabledEndpoint: "instance", wantReason: "group_participation_decision_unavailable"},
		{name: "missing instance cannot fall back", global: "global", instance: "missing", wantReason: "group_participation_decision_unavailable"},
		{name: "disabled bound connection cannot fall back", global: "global", instance: "instance", disabledConnection: "instance", wantReason: "group_participation_decision_unavailable"},
		{name: "invalid decision remains closed", global: "global", instance: "instance", decision: "sure, go ahead", wantModel: "instance", wantReason: "group_participation_decision_unavailable"},
		{name: "chat endpoint does not replace disabled gate", global: "global", disabledEndpoint: "global", wantReason: "group_participation_decision_unavailable"},
		{name: "grok 4.5 uses low reasoning", global: "global", globalModel: "grok-4.5", wantModel: "grok-4.5", wantReasoning: "low", wantOwned: true, wantReason: "group_participation"},
		{name: "grok 4.6 uses low reasoning", global: "global", globalModel: "grok-4.6", wantModel: "grok-4.6", wantReasoning: "low", wantOwned: true, wantReason: "group_participation"},
		{name: "older grok omits reasoning option", global: "global", globalModel: "grok-4.20-0309-non-reasoning", wantModel: "grok-4.20-0309-non-reasoning", wantOwned: true, wantReason: "group_participation"},
		{name: "other model omits reasoning option", global: "global", globalModel: "gpt-5.6-terra", wantModel: "gpt-5.6-terra", wantOwned: true, wantReason: "group_participation"},
		{name: "unverified grok suffix omits reasoning option", global: "global", globalModel: "grok-4.5-preview", wantModel: "grok-4.5-preview", wantOwned: true, wantReason: "group_participation"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calledModels := make(chan string, 4)
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload struct {
					Model       string   `json:"model"`
					MaxTokens   int      `json:"max_tokens"`
					Temperature *float64 `json:"temperature"`
					Reasoning   *string  `json:"reasoning_effort"`
				}
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Errorf("decode decision request: %v", err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer model-test-key" {
					t.Errorf("decision used unexpected route or credential")
				}
				if payload.MaxTokens != 160 || payload.Temperature == nil || *payload.Temperature != 0 {
					t.Errorf("decision output is not bounded: %+v", payload)
				}
				if test.wantReasoning == "" {
					if payload.Reasoning != nil {
						t.Errorf("unsupported decision model received reasoning_effort")
					}
				} else if payload.Reasoning == nil || *payload.Reasoning != test.wantReasoning {
					t.Errorf("decision model did not receive its bounded reasoning setting")
				}
				calledModels <- payload.Model
				decision := test.decision
				if decision == "" {
					decision = `{"action":"reply"}`
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]string{"content": decision}}},
				})
			}))
			defer provider.Close()
			runtime := newIdleRuntime(t)
			runtime.client = provider.Client()
			for _, id := range []string{"global", "role", "template", "instance", "chat"} {
				model := id
				if id == "global" && test.globalModel != "" {
					model = test.globalModel
				}
				insertTestEndpoint(t, runtime.configStore.db, id, model, []string{"chat"}, "llm", "openai_chat")
				bindTestModelConnection(t, runtime.configStore.db, id, provider.URL+"/v1")
			}
			if _, err := runtime.configStore.db.Exec(`UPDATE model_endpoints SET enabled = 0 WHERE id = ?`, test.disabledEndpoint); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.configStore.db.Exec(`UPDATE provider_connections SET enabled = 0 WHERE id = ?`, "test-bound-"+test.disabledConnection); err != nil {
				t.Fatal(err)
			}
			profile, _ := json.Marshal(personaRuntimeProfile{
				ParticipationMode: "social", ChatEndpointID: "chat", DecisionEndpointID: test.role,
			})
			template, _ := json.Marshal(personaRuntimeProfile{DecisionEndpointID: test.template})
			instance, _ := json.Marshal(personaRuntimeProfile{DecisionEndpointID: test.instance})
			if _, err := runtime.configStore.db.Exec(`
				INSERT OR REPLACE INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
				VALUES ('doubao', ?, 'now')`, string(profile)); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.configStore.db.Exec(`
				INSERT INTO agent_policy_templates (id, name, config_json, created_at, updated_at)
				VALUES ('decision-template', 'Decision test', ?, 'now', 'now')`, string(template)); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.configStore.db.Exec(`
				INSERT INTO agent_instances (id, display_name, persona_id, policy_template_id, overrides_json, created_at, updated_at)
				VALUES ('decision-instance', 'Decision test', 'doubao', 'decision-template', ?, 'now', 'now')`, string(instance)); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.configStore.db.Exec(`
				INSERT INTO agent_instance_connectors (instance_id, connector_id, created_at, updated_at)
				VALUES ('decision-instance', 'instance-one', 'now', 'now');
				INSERT INTO agent_instance_routes (id, instance_id, connector_id, transport, created_at, updated_at)
				VALUES ('decision-route', 'decision-instance', 'instance-one', 'qq_official', 'now', 'now');
			`); err != nil {
				t.Fatal(err)
			}
			setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
				"enabled": true, "participationMode": "social", "initialProbability": 1.0,
				"afterReplyProbability": 1.0, "replyDensityEnabled": false,
				"decisionProviderId": test.global, "decisionTimeoutSeconds": 2,
				"triggerKeywords": []string{"豆包"}, "keywordSmartMode": true,
			})
			message := "今天群里真的很热闹"
			if test.keyword {
				message = "刚才有人提到豆包"
			}
			var event transportEvent
			raw, _ := json.Marshal(testTransportEvent("decision-route-event", message, false))
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			scope, err := runtime.resolvedRuntimeScope(event)
			if err != nil || scope.AgentInstanceID != "decision-instance" {
				t.Fatalf("decision fixture resolved scope = %+v, err %v", scope, err)
			}
			owned, reason, err := runtime.shouldOwnUnaddressedGroup(context.Background(), event, message)
			if err != nil || owned != test.wantOwned || reason != test.wantReason {
				t.Fatalf("decision = owned %v, reason %q, err %v; want owned %v, reason %q", owned, reason, err, test.wantOwned, test.wantReason)
			}
			if test.wantModel != "" {
				select {
				case model := <-calledModels:
					if model != test.wantModel {
						t.Fatalf("decision model = %q, want %q", model, test.wantModel)
					}
				default:
					t.Fatal("configured decision model was not called")
				}
			}
			if len(calledModels) != 0 {
				t.Fatalf("unexpected decision or fallback calls: %d", len(calledModels))
			}
		})
	}
}

func TestGroupParticipationDoesNotBypassUnreadableRuntimeProfile(t *testing.T) {
	runtime := newIdleRuntime(t)
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "participationMode": "social", "initialProbability": 1.0,
	})
	if _, err := runtime.configStore.db.Exec(`
		INSERT OR REPLACE INTO persona_runtime_profiles (persona_id, profile_json, updated_at)
		VALUES ('doubao', '{invalid', 'now')`); err != nil {
		t.Fatal(err)
	}
	var event transportEvent
	raw, _ := json.Marshal(testTransportEvent("decision-invalid-profile", "今天群里真的很热闹", false))
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	owned, reason, err := runtime.shouldOwnUnaddressedGroup(context.Background(), event, event.Message.Text)
	if err == nil || owned || reason != "group_participation_profile_failed" {
		t.Fatalf("unreadable profile bypassed gate: owned %v, reason %q, err %v", owned, reason, err)
	}
}

func TestRuntimeNonDirectKeywordHonorsProactiveAdmission(t *testing.T) {
	for _, scenario := range []struct{ name, reason string }{
		{"busy", "proactive_run_in_flight"},
		{"cooldown", "proactive_cooldown"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			var decisionCalls atomic.Int32
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				decisionCalls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]string{"content": `{"action":"reply"}`}}},
				})
			}))
			defer provider.Close()
			runtime := newDormantRuntime(t)
			runtime.client = provider.Client()
			insertTestEndpoint(t, runtime.configStore.db, "keyword-decision", "keyword-model", []string{"chat"}, "llm", "openai_chat")
			bindTestModelConnection(t, runtime.configStore.db, "keyword-decision", provider.URL+"/v1")
			setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
				"mode": "active", "captureUnaddressedGroups": true,
			})
			setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
				"enabled": true, "participationMode": "social", "initialProbability": 0.0,
				"afterReplyProbability": 0.0, "replyDensityEnabled": false,
				"triggerKeywords": []string{"豆包"}, "keywordSmartMode": true,
				"decisionProviderId": "keyword-decision", "decisionTimeoutSeconds": 2,
				"concurrentMode": "smart", "suppressProactiveWhileBusy": true,
				"proactiveCooldownSeconds": 75,
			})
			setTestActiveParticipationMode(t, runtime.configStore.db, "social")
			first := runtimeRequest(t, runtime, "/api/v1/transport/events",
				testTransportEvent("keyword-first", "刚才有人提到豆包", false), "keyword-first")
			if first.Code != http.StatusAccepted || !strings.Contains(first.Body.String(), `"disposition":"owned"`) {
				t.Fatalf("first keyword was not owned: %d %s", first.Code, first.Body.String())
			}
			var firstRun, firstReason string
			if err := runtime.db.QueryRow(`SELECT id, ownership_reason FROM agent_runs WHERE event_id='keyword-first'`).Scan(&firstRun, &firstReason); err != nil {
				t.Fatal(err)
			}
			if firstReason != "trigger_keyword" {
				t.Fatalf("non-direct keyword was misclassified as %q", firstReason)
			}
			if scenario.name == "cooldown" {
				if _, err := runtime.db.Exec(`UPDATE agent_runs SET state='delivered' WHERE id=?`, firstRun); err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC().Format(time.RFC3339Nano)
				if _, err := runtime.db.Exec(`INSERT INTO agent_deliveries
					(id,run_id,reply_handle,payload_json,phase,status,created_at,updated_at)
					VALUES ('keyword-delivered',?,'reply-one','{}','terminal','delivered',?,?)`, firstRun, now, now); err != nil {
					t.Fatal(err)
				}
			}
			second := runtimeRequest(t, runtime, "/api/v1/transport/events",
				testTransportEvent("keyword-second", "他们又说起豆包这件事了", false), "keyword-second")
			if second.Code != http.StatusAccepted || !strings.Contains(second.Body.String(), `"disposition":"observe"`) ||
				!strings.Contains(second.Body.String(), `"reason":"`+scenario.reason+`"`) {
				t.Fatalf("repeated keyword bypassed admission: %d %s", second.Code, second.Body.String())
			}
			var auditReason string
			if err := runtime.db.QueryRow(`SELECT reason FROM agent_transport_events WHERE event_id='keyword-second'`).Scan(&auditReason); err != nil || auditReason != scenario.reason {
				t.Fatalf("keyword suppression audit = %q, err %v", auditReason, err)
			}
			direct := runtimeRequest(t, runtime, "/api/v1/transport/events",
				testTransportEvent("keyword-direct", "豆包，过来一下", false), "keyword-direct")
			if direct.Code != http.StatusAccepted || !strings.Contains(direct.Body.String(), `"disposition":"owned"`) {
				t.Fatalf("direct address was suppressed: %d %s", direct.Code, direct.Body.String())
			}
			var directReason string
			if err := runtime.db.QueryRow(`SELECT ownership_reason FROM agent_runs WHERE event_id='keyword-direct'`).Scan(&directReason); err != nil || directReason != "direct_address" {
				t.Fatalf("direct address reason = %q, err %v", directReason, err)
			}
			if decisionCalls.Load() != 2 {
				t.Fatalf("decision calls = %d; want two non-direct decisions and no direct-address decision", decisionCalls.Load())
			}
		})
	}
}
