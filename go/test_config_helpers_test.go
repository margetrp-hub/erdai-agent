package main

import (
	"database/sql"
	"encoding/json"
	"testing"
	"time"
)

func newTestCoreConfig(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path, fixture := createCoreConfigFixture(t)
	if err := migrateCoreConfig(fixture); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, fixture, "channel_runtime", map[string]any{
		"mode": "active", "agentCoreUrl": "http://erdai-core:6282",
	})
	return path, fixture
}

func newTestCoreConfigPath(t *testing.T) string {
	t.Helper()
	path, db := newTestCoreConfig(t)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func setTestIntegration(t *testing.T, db *sql.DB, id string, value any) {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO integration_settings (id, config_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET config_json = excluded.config_json, updated_at = excluded.updated_at
	`, id, string(encoded), time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
}

func setTestActiveParticipationMode(t *testing.T, db *sql.DB, mode string) {
	t.Helper()
	if mode != "addressed_only" && mode != "adaptive" && mode != "social" {
		t.Fatalf("invalid test participation mode %q", mode)
	}
	if _, err := db.Exec(`UPDATE persona_runtime_profiles
		SET profile_json = json_set(profile_json, '$.participationMode', ?),
			updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
		WHERE persona_id = COALESCE((SELECT NULLIF(active_persona_id, '') FROM runtime_config WHERE id = 1), 'doubao')`, mode); err != nil {
		t.Fatal(err)
	}
}

func insertTestEndpoint(
	t *testing.T,
	db *sql.DB,
	id string,
	model string,
	capabilities []string,
	executionKind string,
	adapterRef string,
) {
	t.Helper()
	encoded, err := json.Marshal(capabilities)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		INSERT INTO model_endpoints (
			id, provider, model, enabled, capabilities_json, input_cost_per_million,
			output_cost_per_million, quality_score, priority, max_context_tokens,
			execution_kind, adapter_ref, created_at, updated_at
		) VALUES (?, 'test', ?, 1, ?, 0, 0, 1, 100, 100000, ?, ?, ?, ?)
	`, id, model, string(encoded), executionKind, adapterRef, now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func bindTestModelConnection(t *testing.T, db *sql.DB, endpointID, apiBase string) {
	t.Helper()
	if err := migrateCoreConfig(db); err != nil {
		t.Fatal(err)
	}
	id := "test-bound-" + endpointID
	if _, err := db.Exec(`INSERT INTO provider_connections
		(id, provider, api_base, credential_ref, created_at, updated_at)
		VALUES (?, ?, ?, 'ERDAI_MODEL_API_KEY', 'now', 'now')`, id, id, apiBase); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_endpoint_connections VALUES (?, ?, 'now')
		ON CONFLICT(endpoint_id) DO UPDATE SET connection_id = excluded.connection_id`, endpointID, id); err != nil {
		t.Fatal(err)
	}
}

func insertTestTool(t *testing.T, db *sql.DB, id, name, adapterRef string) {
	t.Helper()
	config, err := json.Marshal(map[string]any{
		"adapterRef": adapterRef, "allowedAuthorities": []string{"member", "admin"},
		"approvalMode": "auto", "timeoutSeconds": 30,
		"inputSchema": map[string]any{"type": "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.Exec(`
		INSERT INTO tools (
			id, name, description, capabilities_json, risk_level, enabled,
			config_json, created_at, updated_at
		) VALUES (?, ?, ?, '[]', 0, 1, ?, ?, ?)
	`, id, name, name, string(config), now, now)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTestSkill(t *testing.T, db *sql.DB, id string, requiredTools []string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`
		INSERT INTO skills (
			id, name, description, instructions, enabled, activation_mode,
			triggers_json, attachment_kinds_json, required_tools_json,
			required_mcp_servers_json, allowed_authorities_json, persona_ids_json,
			priority, created_at, updated_at
		) VALUES (?, ?, '', 'test skill', 1, 'always', '[]', '[]', ?, '[]',
			'["member","admin"]', '[]', 100, ?, ?)
	`, id, id, mgmtJSON(requiredTools), now, now)
	if err != nil {
		t.Fatal(err)
	}
}
