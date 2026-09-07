package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDoubaoParticipationExampleOverridesOnlySelectedInstance(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "examples", "roles", "doubao.runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]json.RawMessage
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	participation := map[string]any{
		"participationMode": "social", "participationStyle": "social", "unaddressedMode": "adaptive",
		"proactiveEnabled": true, "initialReplyProbability": 0.18, "afterReplyProbability": 0.36,
	}
	for key, want := range participation {
		var got any
		if err := json.Unmarshal(example[key], &got); err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("example %s=%v want=%v err=%v", key, got, want, err)
		}
	}
	store := newAgentInstanceStore(t)
	if _, err := store.db.Exec(`INSERT INTO personas (id,name,created_at,updated_at)
		VALUES ('doubao','Doubao','now','now'),('xiaoman','Xiaoman','now','now')`); err != nil {
		t.Fatal(err)
	}
	legacyRole := `{"participationMode":"addressed_only","proactiveEnabled":false,"participationStyle":"service","unaddressedMode":"off","initialReplyProbability":0,"afterReplyProbability":0,"searchMode":"adaptive","searchReplyStyle":"concise","maxReplyChars":80,"maxReplySentences":3,"memoryPolicy":"isolated","allowedToolIds":["chat","search"]}`
	for _, personaID := range []string{"doubao", "xiaoman"} {
		if _, err := store.db.Exec(`INSERT INTO persona_runtime_profiles (persona_id,profile_json,updated_at) VALUES (?,?,'now')`, personaID, legacyRole); err != nil {
			t.Fatal(err)
		}
	}
	response := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-policy-templates", `{
		"id":"legacy-quiet","name":"Legacy quiet policy","config":{
			"participationMode":"addressed_only","proactiveEnabled":false,"participationStyle":"service","unaddressedMode":"off",
			"initialReplyProbability":0,"afterReplyProbability":0,"maxReplyChars":64,"maxReplySentences":2,"deniedToolIds":["admin"]
		}}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create legacy template: %d %s", response.Code, response.Body.String())
	}
	instancePersonas := map[string]string{"doubao-qq": "doubao", "doubao-secondary": "doubao", "xiaoman-qq": "xiaoman"}
	for id, personaID := range instancePersonas {
		body, err := json.Marshal(map[string]any{
			"id": id, "displayName": id, "personaId": personaID, "policyTemplateId": "legacy-quiet",
			"overrides": map[string]any{
				"participationMode": "addressed_only", "proactiveEnabled": false, "unaddressedMode": "off",
				"initialReplyProbability": 0, "afterReplyProbability": 0,
				"chatEndpointId": "instance-chat", "taskEndpointId": "instance-task",
				"allowedToolIds": []string{"chat"}, "deniedToolIds": []string{"delete"},
				"memoryPolicy": "instance-memory", "maxReplyChars": 48, "maxReplySentences": 1,
				"expressionPrompt":  "Preserve this instance expression.",
				"operatorExtension": map[string]any{"label": "preserve", "enabled": true},
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		response := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", string(body))
		if response.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", id, response.Code, response.Body.String())
		}
	}
	if _, err := store.db.Exec(`UPDATE agent_instances SET overrides_json=json_set(overrides_json,
		'$.participationMode','social','$.proactiveEnabled',json('true'),'$.unaddressedMode','adaptive',
		'$.initialReplyProbability',0.08,'$.afterReplyProbability',0.24) WHERE id='xiaoman-qq'`); err != nil {
		t.Fatal(err)
	}
	beforeProfiles := map[string]personaRuntimeProfile{}
	beforeOverrides := map[string]string{}
	for id, personaID := range instancePersonas {
		profile, err := store.effectivePersonaRuntimeProfile(personaID, id)
		if err != nil {
			t.Fatal(err)
		}
		mode, initial, after := "addressed_only", 0.0, 0.0
		if personaID == "xiaoman" {
			mode, initial, after = "social", 0.08, 0.24
		}
		if effectiveParticipationMode(groupParticipationPolicy{}, profile) != mode ||
			profile.InitialReplyProbability == nil || *profile.InitialReplyProbability != initial ||
			profile.AfterReplyProbability == nil || *profile.AfterReplyProbability != after {
			t.Fatalf("unexpected initial participation fixture: %s %+v", id, profile)
		}
		beforeProfiles[id] = profile
		var overrides string
		if err := store.db.QueryRow(`SELECT overrides_json FROM agent_instances WHERE id=?`, id).Scan(&overrides); err != nil {
			t.Fatal(err)
		}
		beforeOverrides[id] = overrides
	}
	var templateBefore string
	if err := store.db.QueryRow(`SELECT config_json FROM agent_policy_templates WHERE id='legacy-quiet'`).Scan(&templateBefore); err != nil {
		t.Fatal(err)
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal([]byte(beforeOverrides["doubao-qq"]), &merged); err != nil {
		t.Fatal(err)
	}
	// Match the deployment workflow: merge only participation keys into readback.
	for key := range participation {
		merged[key] = example[key]
	}
	payload, err := json.Marshal(map[string]any{"overrides": merged})
	if err != nil {
		t.Fatal(err)
	}
	response = agentInstanceRequest(t, store, http.MethodPut, "/api/v1/agent-instances/doubao-qq", string(payload))
	if response.Code != http.StatusOK {
		t.Fatalf("update participation: %d %s", response.Code, response.Body.String())
	}
	for id, personaID := range instancePersonas {
		got, err := store.effectivePersonaRuntimeProfile(personaID, id)
		if err != nil {
			t.Fatal(err)
		}
		var overrides string
		if err := store.db.QueryRow(`SELECT overrides_json FROM agent_instances WHERE id=?`, id).Scan(&overrides); err != nil {
			t.Fatal(err)
		}
		if id != "doubao-qq" {
			if !reflect.DeepEqual(got, beforeProfiles[id]) || overrides != beforeOverrides[id] {
				t.Errorf("unselected instance changed: %s", id)
			}
			continue
		}
		if effectiveParticipationMode(groupParticipationPolicy{ParticipationMode: "addressed_only"}, got) != "social" ||
			got.ParticipationStyle != "social" || got.UnaddressedMode != "adaptive" ||
			got.ProactiveEnabled == nil || !*got.ProactiveEnabled ||
			got.InitialReplyProbability == nil || *got.InitialReplyProbability != 0.18 ||
			got.AfterReplyProbability == nil || *got.AfterReplyProbability != 0.36 {
			t.Errorf("example did not override all legacy participation gates: %+v", got)
		}
		before := beforeProfiles[id]
		got.ParticipationMode, got.ParticipationStyle, got.UnaddressedMode = before.ParticipationMode, before.ParticipationStyle, before.UnaddressedMode
		got.ProactiveEnabled, got.InitialReplyProbability, got.AfterReplyProbability = before.ProactiveEnabled, before.InitialReplyProbability, before.AfterReplyProbability
		if !reflect.DeepEqual(got, before) {
			t.Errorf("non-participation effective settings changed: before=%+v after=%+v", before, got)
		}
		var stored map[string]json.RawMessage
		if err := json.Unmarshal([]byte(overrides), &stored); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(stored, merged) {
			t.Error("PUT discarded or changed preserved instance overrides")
		}
	}
	var templateAfter string
	if err := store.db.QueryRow(`SELECT config_json FROM agent_policy_templates WHERE id='legacy-quiet'`).Scan(&templateAfter); err != nil || templateAfter != templateBefore {
		t.Errorf("shared template changed: err=%v", err)
	}
	for _, personaID := range []string{"doubao", "xiaoman"} {
		var profile string
		if err := store.db.QueryRow(`SELECT profile_json FROM persona_runtime_profiles WHERE persona_id=?`, personaID).Scan(&profile); err != nil || profile != legacyRole {
			t.Errorf("base persona profile changed: %s err=%v", personaID, err)
		}
	}
}
