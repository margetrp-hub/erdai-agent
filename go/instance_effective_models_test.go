package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInstanceEffectiveModelsExplainsRuntimePrecedence(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	store := runtime.configStore
	for _, id := range []string{"public-chat", "public-task", "public-decision", "role", "template", "instance"} {
		insertTestEndpoint(t, store.db, id, "model-"+id, []string{"chat", "reasoning", "tool_calling"}, "llm", "openai")
	}
	setTestIntegration(t, store.db, "companion_policy", map[string]any{
		"enableModelRouting": true, "chatModel": "public-chat", "taskModel": "public-task",
	})
	setTestIntegration(t, store.db, "group_chat_policy", map[string]any{"decisionProviderId": "public-decision"})
	if _, err := store.db.Exec(`INSERT OR REPLACE INTO persona_runtime_profiles(persona_id,profile_json,updated_at)
		VALUES ('doubao','{"chatEndpointId":"role","taskEndpointId":"role","decisionEndpointId":"role"}','now')`); err != nil {
		t.Fatal(err)
	}
	create := func(path, body string) {
		t.Helper()
		response := agentInstanceRequest(t, store, http.MethodPost, path, body)
		if response.Code != http.StatusCreated {
			t.Fatalf("fixture %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	create("/api/v1/agent-policy-templates", `{"id":"model-layers","name":"Model layers","config":{"chatEndpointId":"template","taskEndpointId":"template","decisionEndpointId":"template"}}`)
	create("/api/v1/agent-instances", `{"id":"model-instance","displayName":"Model instance","personaId":"doubao","policyTemplateId":"model-layers","overrides":{"chatEndpointId":"instance","decisionEndpointId":"instance","privateNote":"never-return-this"}}`)
	create("/api/v1/agent-instances/model-instance/connectors", `{"connectorId":"model-connector"}`)
	create("/api/v1/agent-instance-routes", `{"id":"model-route","instanceId":"model-instance","connectorId":"model-connector","transport":"qq_official"}`)
	if _, err := store.db.Exec(`INSERT INTO persona_bindings
		(id,persona_id,transport,transport_instance,conversation_ref,created_at,updated_at)
		VALUES ('model-persona','doubao','qq_official','model-connector','*','now','now')`); err != nil {
		t.Fatal(err)
	}
	const path = "/api/v1/agent-instances/model-instance/effective-models"
	gateway := &Gateway{runtime: runtime, adminToken: managementAdminToken}
	request := func(method, route, token string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, route, nil)
		if token == "admin" {
			req.Header.Set(adminTokenHeader, managementAdminToken)
		}
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		return response
	}
	read := func() instanceEffectiveModels {
		t.Helper()
		response := request(http.MethodGet, path, "admin")
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-return-this") {
			t.Fatalf("effective models response: %d %s", response.Code, response.Body.String())
		}
		var envelope struct {
			Data instanceEffectiveModels `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	value := read()
	if value.InstanceID != "model-instance" || value.PersonaID != "doubao" || !value.AutomaticRouting || len(value.Models) != 3 {
		t.Fatalf("unexpected model provenance: %+v", value)
	}
	for index, expected := range []string{"instance", "template", "instance"} {
		item := value.Models[index]
		if item.Source != expected || item.Preference != expected || item.Mode != "configured_preference" || item.Endpoint == nil || item.Endpoint.Model != "model-"+expected {
			t.Fatalf("wrong effective %s model: %+v", item.Lane, item)
		}
		profile, err := store.effectivePersonaRuntimeProfile("doubao", "model-instance")
		if err != nil || personaRuntimeEndpoint(profile, item.Lane) != item.Preference {
			t.Fatalf("provenance differs from runtime profile: %+v err=%v", item, err)
		}
		if len(item.Layers) != 4 || !item.Layers[0].Overridden || !item.Layers[1].Overridden {
			t.Fatalf("hidden overridden public/role defaults: %+v", item.Layers)
		}
	}
	for index, message := range []string{"你好", "比较这两个方案的优缺点。"} {
		prepared, err := store.prepareRuntime(corePreparePayload{
			Transport: "qq_official", TransportInstance: "model-connector", ConversationRef: "models-group", Message: message,
		})
		if err != nil || prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != value.Models[index].Endpoint.ID {
			t.Fatalf("reported preference differs from an eligible runtime route: %+v err=%v", prepared.RouteDecision, err)
		}
	}
	// A disabled template is still explained, but must not override the role.
	if _, err := store.db.Exec(`UPDATE agent_policy_templates SET enabled=0 WHERE id='model-layers';
		UPDATE agent_instances SET overrides_json='{}' WHERE id='model-instance'`); err != nil {
		t.Fatal(err)
	}
	for _, item := range read().Models {
		if item.Source != "persona" || item.Preference != "role" || item.Layers[2].Enabled || item.Layers[2].Overridden {
			t.Fatalf("disabled template influenced effective model: %+v", item)
		}
	}
	if _, err := store.db.Exec(`UPDATE persona_runtime_profiles SET profile_json='{}' WHERE persona_id='doubao'`); err != nil {
		t.Fatal(err)
	}
	for index, expected := range []string{"public-chat", "public-task", "public-decision"} {
		item := read().Models[index]
		if item.Source != "public" || item.Preference != expected {
			t.Fatalf("missing public default: %+v", item)
		}
	}
	setTestIntegration(t, store.db, "companion_policy", map[string]any{"enableModelRouting": false})
	setTestIntegration(t, store.db, "group_chat_policy", map[string]any{})
	value = read()
	if value.AutomaticRouting {
		t.Fatal("disabled automatic routing was not reported")
	}
	for index, mode := range []string{"public_default", "public_default", "heuristic"} {
		if item := value.Models[index]; item.Mode != mode || item.Preference != "" || item.Endpoint != nil {
			t.Fatalf("empty preference invented a model: %+v", item)
		}
	}
	for _, scenario := range []struct {
		method, route, token string
		status               int
	}{
		{http.MethodGet, path, "", http.StatusUnauthorized},
		{http.MethodPut, path, "admin", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/v1/agent-instances/absent/effective-models", "admin", http.StatusNotFound},
	} {
		response := request(scenario.method, scenario.route, scenario.token)
		if response.Code != scenario.status {
			t.Fatalf("%s %s status=%d want=%d", scenario.method, scenario.route, response.Code, scenario.status)
		}
	}
}

func TestInstanceModelMetadataUsesLaneSpecificLookup(t *testing.T) {
	_, db := newTestCoreConfig(t)
	defer db.Close()
	store := &coreConfigStore{db: db}
	insertTestEndpoint(t, db, "lookup-id", "shared-model-name", []string{"chat"}, "llm", "openai")
	for _, lane := range []string{"chat", "task", "decision"} {
		endpoint, err := store.instancePreferenceEndpoint(lane, "shared-model-name")
		if err != nil || (lane == "decision") != (endpoint == nil) {
			t.Fatalf("%s lookup diverged from runtime semantics: %+v err=%v", lane, endpoint, err)
		}
	}
	if _, err := db.Exec(`UPDATE model_endpoints SET enabled=0 WHERE id='lookup-id'`); err != nil {
		t.Fatal(err)
	}
	endpoint, err := store.instancePreferenceEndpoint("decision", "lookup-id")
	if err != nil || endpoint == nil || endpoint.Enabled {
		t.Fatalf("disabled configured endpoint should stay visible without claiming availability: %+v err=%v", endpoint, err)
	}
}
