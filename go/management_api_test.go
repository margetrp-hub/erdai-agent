package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	managementAdminToken   = "management-admin-token-at-least-32-bytes"
	managementRuntimeToken = "management-runtime-token-at-least-32-bytes"
)

func newManagementRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.Exec(`
		PRAGMA foreign_keys = ON;
		PRAGMA user_version = 13;
		CREATE TABLE model_endpoints (
			id TEXT PRIMARY KEY, provider TEXT NOT NULL, model TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)), capabilities_json TEXT NOT NULL,
			input_cost_per_million REAL NOT NULL CHECK (input_cost_per_million >= 0),
			output_cost_per_million REAL NOT NULL CHECK (output_cost_per_million >= 0),
			pricing_source TEXT NOT NULL DEFAULT '', pricing_checked_at TEXT NOT NULL DEFAULT '',
			pricing_currency TEXT NOT NULL DEFAULT 'USD',
			quality_score REAL NOT NULL CHECK (quality_score BETWEEN 0 AND 1), priority REAL NOT NULL,
			max_context_tokens INTEGER NOT NULL CHECK (max_context_tokens >= 0),
			execution_kind TEXT NOT NULL, adapter_ref TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			UNIQUE(provider, model)
		);
		CREATE TABLE model_health (
			endpoint_id TEXT PRIMARY KEY REFERENCES model_endpoints(id) ON DELETE CASCADE,
			healthy INTEGER NOT NULL, latency_ms INTEGER, error_rate REAL NOT NULL,
			consecutive_failures INTEGER NOT NULL, status_message TEXT NOT NULL DEFAULT '',
			checked_at TEXT NOT NULL
		);
		CREATE TABLE model_health_samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT, endpoint_id TEXT NOT NULL,
			healthy INTEGER NOT NULL, latency_ms INTEGER, error_rate REAL NOT NULL,
			status_message TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL
		);
		CREATE TABLE provider_connections (
			id TEXT PRIMARY KEY, provider TEXT NOT NULL UNIQUE, protocol TEXT NOT NULL,
			api_base TEXT NOT NULL, credential_ref TEXT NOT NULL DEFAULT '', pricing_url TEXT NOT NULL DEFAULT '', timeout_seconds INTEGER NOT NULL,
			enabled INTEGER NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE model_endpoint_connections (
			endpoint_id TEXT PRIMARY KEY REFERENCES model_endpoints(id) ON DELETE CASCADE,
			connection_id TEXT NOT NULL REFERENCES provider_connections(id) ON DELETE RESTRICT,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE routing_control (id INTEGER PRIMARY KEY, mode TEXT NOT NULL, updated_at TEXT NOT NULL);
		INSERT INTO routing_control VALUES (1, 'auto', '2026-08-02T00:00:00Z');
		CREATE TABLE routing_lane_locks (
			lane TEXT PRIMARY KEY, endpoint_id TEXT NOT NULL REFERENCES model_endpoints(id) ON DELETE CASCADE,
			updated_at TEXT NOT NULL
		);
		CREATE TABLE routing_lane_profiles (
			lane TEXT PRIMARY KEY, required_capabilities_json TEXT NOT NULL,
			preferred_capabilities_json TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO routing_lane_profiles VALUES ('chat', '["chat"]', '[]', '2026-08-02T00:00:00Z');
		INSERT INTO routing_lane_profiles VALUES ('image', '["image_generation"]', '[]', '2026-08-02T00:00:00Z');
		CREATE TABLE tools (
			id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, description TEXT NOT NULL,
			capabilities_json TEXT NOT NULL, risk_level INTEGER NOT NULL, enabled INTEGER NOT NULL,
			config_json TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE skills (id TEXT PRIMARY KEY);
		CREATE TABLE mcp_servers (id TEXT PRIMARY KEY);
		CREATE TABLE personas (id TEXT PRIMARY KEY);
		CREATE TABLE worldbook_entries (id TEXT PRIMARY KEY);
		CREATE TABLE knowledge_documents (id TEXT PRIMARY KEY);
		CREATE TABLE runs (id TEXT PRIMARY KEY);
		CREATE TABLE deliveries (id TEXT PRIMARY KEY);
		CREATE TABLE runtime_config (id INTEGER PRIMARY KEY);
		CREATE TABLE admin_directives (id TEXT PRIMARY KEY);
		CREATE TABLE knowledge_candidates (id TEXT PRIMARY KEY);
		CREATE TABLE transport_events (event_id TEXT PRIMARY KEY);
		CREATE TABLE agent_runs (id TEXT PRIMARY KEY);
		CREATE TABLE agent_deliveries (id TEXT PRIMARY KEY);
		CREATE TABLE run_stage_events (id INTEGER PRIMARY KEY);
		CREATE TABLE agent_transport_events (event_id TEXT PRIMARY KEY);
		CREATE TABLE integration_settings (
			id TEXT PRIMARY KEY, config_json TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE agent_plugins (
			id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL,
			version TEXT NOT NULL, author TEXT NOT NULL, source TEXT NOT NULL,
			enabled INTEGER NOT NULL, manifest_json TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO integration_settings VALUES (
			'channel_runtime',
			'{"mode":"off","agentCoreUrl":"http://erdai-agent-core:6280","consumerId":"erdai-runtime","captureUnaddressedGroups":false,"requestTimeoutSeconds":5,"deliveryPollSeconds":2,"fallbackOnCoreError":false,"compatibilitySyncEnabled":true}',
			'2026-08-02T00:00:00Z'
		);
		INSERT INTO integration_settings VALUES (
			'qq_official',
			'{"enabled":true,"groupC2C":true,"guildDirectMessage":true,"credentialConfigured":false,"platformId":"Doubao","appid":"","credentialRefs":{}}',
			'2026-08-02T00:00:00Z'
		);
		INSERT INTO integration_settings VALUES ('provider_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('message_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('group_chat_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('companion_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('runtime_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('grok_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('memory_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('ops_policy', '{}', '2026-08-02T00:00:00Z');
		INSERT INTO integration_settings VALUES ('image_policy', '{}', '2026-08-02T00:00:00Z');
		CREATE TABLE platform_integrations (
			id TEXT PRIMARY KEY, platform_type TEXT NOT NULL, display_name TEXT NOT NULL,
			enabled INTEGER NOT NULL, credential_configured INTEGER NOT NULL,
			settings_json TEXT NOT NULL, credential_refs_json TEXT NOT NULL,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE audit_events (
			id INTEGER PRIMARY KEY AUTOINCREMENT, actor TEXT NOT NULL, action TEXT NOT NULL,
			target_type TEXT NOT NULL, target_id TEXT, details_json TEXT NOT NULL,
			created_at TEXT NOT NULL
		);
		CREATE TABLE shadow_interactions (
			id INTEGER PRIMARY KEY AUTOINCREMENT, transport TEXT NOT NULL,
			conversation_hash TEXT NOT NULL, sender_hash TEXT NOT NULL,
			message_length INTEGER NOT NULL, has_image INTEGER NOT NULL, has_audio INTEGER NOT NULL,
			legacy_model TEXT NOT NULL, lane TEXT NOT NULL,
			selected_endpoint_id TEXT REFERENCES model_endpoints(id) ON DELETE SET NULL,
			route_json TEXT NOT NULL, created_at TEXT NOT NULL
		);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return &AgentRuntime{
		configStore: &coreConfigStore{db: db},
		adminToken:  managementAdminToken, runtimeToken: managementRuntimeToken,
	}
}

func managementRequest(t *testing.T, runtime *AgentRuntime, method, path string, payload any, auth string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if payload != nil {
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(method, path, &body)
	request.Header.Set("Content-Type", "application/json")
	switch auth {
	case "admin":
		request.Header.Set(adminTokenHeader, managementAdminToken)
	case "runtime":
		request.Header.Set("Authorization", "Bearer "+managementRuntimeToken)
	}
	recorder := httptest.NewRecorder()
	if !runtime.handleNativeManagement(recorder, request, request.URL.Path) {
		t.Fatalf("management route was not handled: %s", path)
	}
	return recorder
}

func managementData(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %d response: %v: %s", recorder.Code, err, recorder.Body.String())
	}
}

func TestConfigLayerManifestIsExplicitAndReadOnly(t *testing.T) {
	runtime := newManagementRuntime(t)
	recorder := managementRequest(t, runtime, http.MethodGet, "/api/v1/config/layers", nil, "admin")
	if recorder.Code != http.StatusOK {
		t.Fatalf("expected config layer manifest, got %d: %s", recorder.Code, recorder.Body.String())
	}
	var payload struct {
		Data struct {
			Version    string   `json:"version"`
			MergeRule  string   `json:"mergeRule"`
			Precedence []string `json:"precedence"`
			Layers     []struct {
				ID       string            `json:"id"`
				Scope    string            `json:"scope"`
				Consumes []string          `json:"consumes"`
				Fields   map[string]string `json:"fields"`
			} `json:"layers"`
			Controls []struct {
				ID             string `json:"id"`
				CanonicalField string `json:"canonicalField"`
				Rule           string `json:"rule"`
			} `json:"controls"`
		} `json:"data"`
	}
	managementData(t, recorder, &payload)
	if payload.Data.Version == "" || payload.Data.MergeRule == "" || len(payload.Data.Precedence) != 6 || len(payload.Data.Layers) != 6 {
		t.Fatalf("incomplete config layer manifest: %#v", payload.Data)
	}
	seen := map[string]bool{}
	for _, layer := range payload.Data.Layers {
		if layer.ID == "" || layer.Scope == "" || len(layer.Consumes) == 0 || len(layer.Fields) == 0 {
			t.Fatalf("layer lacks ownership or consumption data: %#v", layer)
		}
		seen[layer.ID] = true
	}
	for _, id := range []string{"public_config", "public_policy", "role_config", "role_policy", "instance_config", "instance_policy"} {
		if !seen[id] {
			t.Fatalf("missing config layer %q", id)
		}
	}
	if len(payload.Data.Controls) < 5 {
		t.Fatalf("config control manifest is incomplete: %#v", payload.Data.Controls)
	}
	controlIDs := map[string]bool{}
	for _, control := range payload.Data.Controls {
		if control.ID == "" || control.CanonicalField == "" || control.Rule == "" {
			t.Fatalf("config control lacks canonical field or rule: %#v", control)
		}
		controlIDs[control.ID] = true
	}
	for _, id := range []string{"group_participation", "transport_takeover", "search_activation", "media_generation", "group_moderation"} {
		if !controlIDs[id] {
			t.Fatalf("missing config control %q", id)
		}
	}
	if managementRequest(t, runtime, http.MethodPut, "/api/v1/config/layers", map[string]any{}, "admin").Code != http.StatusMethodNotAllowed {
		t.Fatal("config layer manifest must remain read-only")
	}
}

func TestAgentRuntimeRoutesNativeManagement(t *testing.T) {
	runtime := newManagementRuntime(t)
	request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
	recorder := httptest.NewRecorder()
	if !runtime.Handle(recorder, request) {
		t.Fatal("overview fell through to the legacy upstream")
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("native overview = %d: %s", recorder.Code, recorder.Body.String())
	}
}

func TestManagementAuthorizationAndStrictFields(t *testing.T) {
	runtime := newManagementRuntime(t)
	unauthorized := managementRequest(t, runtime, http.MethodPost, "/api/v1/tools", map[string]any{"name": "search"}, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized mutation = %d: %s", unauthorized.Code, unauthorized.Body.String())
	}
	unknown := managementRequest(t, runtime, http.MethodPost, "/api/v1/tools", map[string]any{
		"name": "search", "apiKey": "must-not-be-stored",
	}, "admin")
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unsupported tool fields: apiKey") {
		t.Fatalf("unknown field response = %d: %s", unknown.Code, unknown.Body.String())
	}
	wrongAuthority := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/channel_runtime", map[string]any{"mode": "shadow"}, "runtime")
	if wrongAuthority.Code != http.StatusUnauthorized {
		t.Fatalf("runtime integration mutation = %d", wrongAuthority.Code)
	}
}

func TestManagementIntegrationAndPlatformCRUD(t *testing.T) {
	runtime := newManagementRuntime(t)
	opsPolicy := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/ops_policy", map[string]any{
		"enabled": true, "statusUrl": "https://ohlaoo.com/ops-bot/group-status",
		"statusTitle": "Synai996 分组检测", "credentialRef": "ERDAI_OPS_TOKEN",
		"requestTimeoutSeconds": 10, "commandAliases": []string{"/渠道", "/分组"},
		"timelinePoints": 10, "groupMultipliers": map[string]any{"Codex_GPT": 0.15},
		"showMultiplierNote": true, "radarEnabled": true,
		"radarUrl":            "https://codex-radar.roixw.com/api/model-ratings?history=14",
		"radarCommandAliases": []string{"/雷达"}, "radarMinimumSamples": 5,
		"radarFamilyOrder":         []string{"GPT-5.6 Sol"},
		"radarRecommendationOrder": []string{"日常开发"},
		"radarRecommendations":     map[string]any{"日常开发": "GPT-5.6 Sol"},
	}, "admin")
	if opsPolicy.Code != http.StatusOK || !strings.Contains(opsPolicy.Body.String(), `"radarMinimumSamples":5`) ||
		!strings.Contains(opsPolicy.Body.String(), `"Codex_GPT":0.15`) {
		t.Fatalf("OPS policy update = %d: %s", opsPolicy.Code, opsPolicy.Body.String())
	}
	invalidAlias := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/ops_policy", map[string]any{
		"commandAliases": []string{"渠道"},
	}, "admin")
	if invalidAlias.Code != http.StatusBadRequest {
		t.Fatalf("OPS policy accepted non-slash alias: %s", invalidAlias.Body.String())
	}
	updated := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/channel_runtime", map[string]any{
		"mode": "shadow", "captureUnaddressedGroups": true, "deliveryPollSeconds": 0.5,
	}, "admin")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"mode":"shadow"`) ||
		!strings.Contains(updated.Body.String(), `"deliveryPollSeconds":0.5`) {
		t.Fatalf("integration update = %d: %s", updated.Code, updated.Body.String())
	}
	publicCore := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/channel_runtime", map[string]any{
		"agentCoreUrl": "https://public.example/core",
	}, "admin")
	if publicCore.Code != http.StatusBadRequest {
		t.Fatalf("public Core URL accepted: %s", publicCore.Body.String())
	}
	catalog := managementRequest(t, runtime, http.MethodGet, "/api/v1/platforms/catalog", nil, "")
	var catalogBody struct {
		Data []mgmtPlatformCatalogItem `json:"data"`
	}
	managementData(t, catalog, &catalogBody)
	webchatFound, telegramUserFound := false, false
	for _, item := range catalogBody.Data {
		webchatFound = webchatFound || (item.Type == "webchat" && !item.HasDefaultTemplate)
		telegramUserFound = telegramUserFound || item.Type == "telegram_user"
	}
	if catalog.Code != http.StatusOK || len(catalogBody.Data) != 19 || !webchatFound || !telegramUserFound {
		t.Fatalf("platform catalog = %+v", catalogBody.Data)
	}
	telegramUser := managementRequest(t, runtime, http.MethodPost, "/api/v1/platforms", map[string]any{
		"id": "telegram-user-test", "type": "telegram_user", "displayName": "Telegram User Test",
		"enabled": false, "settings": map[string]any{"telegram_user_api_id": 123456, "telegram_user_receive_groups": true},
		"credentialRefs": map[string]any{"api_hash": "ERDAI_TELEGRAM_USER_API_HASH"},
	}, "admin")
	if telegramUser.Code != http.StatusCreated || !strings.Contains(telegramUser.Body.String(), `"telegram_user_api_id":123456`) {
		t.Fatalf("telegram user platform create = %d: %s", telegramUser.Code, telegramUser.Body.String())
	}
	telegramUserDetails := managementRequest(t, runtime, http.MethodGet, "/api/v1/platforms/telegram-user-test", nil, "")
	if telegramUserDetails.Code != http.StatusOK || !strings.Contains(telegramUserDetails.Body.String(), `"api_hash":"ERDAI_TELEGRAM_USER_API_HASH"`) {
		t.Fatalf("telegram user platform get = %d: %s", telegramUserDetails.Code, telegramUserDetails.Body.String())
	}
	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/platforms", map[string]any{
		"id": "telegram-primary", "type": "telegram", "displayName": "Primary Telegram",
		"enabled": true, "credentialConfigured": true,
		"settings":       map[string]any{"start_message": "Ready", "telegram_command_register": true},
		"credentialRefs": map[string]any{"telegram_token": "ERDAI_TELEGRAM_PRIMARY_TOKEN"},
	}, "admin")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"telegram_file_base_url":"https://api.telegram.org/file/bot"`) {
		t.Fatalf("platform create = %d: %s", created.Code, created.Body.String())
	}
	updatedPlatform := managementRequest(t, runtime, http.MethodPut, "/api/v1/platforms/telegram-primary", map[string]any{
		"settings": map[string]any{"start_message": "Updated"},
	}, "admin")
	if updatedPlatform.Code != http.StatusOK || !strings.Contains(updatedPlatform.Body.String(), `"start_message":"Updated"`) {
		t.Fatalf("platform update = %d: %s", updatedPlatform.Code, updatedPlatform.Body.String())
	}
	registry := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/channel_platforms", map[string]any{
		"instances": []map[string]any{
			{"id": "telegram-primary", "type": "telegram", "displayName": "Telegram Updated", "enabled": true},
			{"id": "discord-primary", "type": "discord", "displayName": "Discord Primary", "enabled": false},
		},
	}, "admin")
	if registry.Code != http.StatusOK || !strings.Contains(registry.Body.String(), `"runtimeVersion":"`+erdaiRuntimeVersion+`"`) || !strings.Contains(registry.Body.String(), `"id":"discord-primary"`) {
		t.Fatalf("platform registry update = %d: %s", registry.Code, registry.Body.String())
	}
	catalogMutation := managementRequest(t, runtime, http.MethodPut, "/api/v1/integrations/channel_platforms", map[string]any{
		"runtimeVersion": "changed", "instances": []any{},
	}, "admin")
	if catalogMutation.Code != http.StatusBadRequest {
		t.Fatalf("platform registry catalog mutation = %d: %s", catalogMutation.Code, catalogMutation.Body.String())
	}
	literalSecret := managementRequest(t, runtime, http.MethodPost, "/api/v1/platforms", map[string]any{
		"id": "telegram-unsafe", "type": "telegram",
		"credentialRefs": map[string]any{"telegram_token": "123456:literal-token"},
	}, "admin")
	if literalSecret.Code != http.StatusBadRequest {
		t.Fatalf("literal platform secret accepted: %s", literalSecret.Body.String())
	}
	legacyDelete := managementRequest(t, runtime, http.MethodDelete, "/api/v1/platforms/qq_official", nil, "admin")
	if legacyDelete.Code != http.StatusBadRequest {
		t.Fatalf("legacy QQ deletion = %d", legacyDelete.Code)
	}
	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/platforms/telegram-primary", nil, "admin")
	if deleted.Code != http.StatusOK {
		t.Fatalf("platform delete = %d: %s", deleted.Code, deleted.Body.String())
	}
	deleted = managementRequest(t, runtime, http.MethodDelete, "/api/v1/platforms/telegram-user-test", nil, "admin")
	if deleted.Code != http.StatusOK {
		t.Fatalf("telegram user platform delete = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestManagementModelsHealthAndRouting(t *testing.T) {
	runtime := newManagementRuntime(t)
	for _, endpoint := range []map[string]any{
		{"id": "preferred", "provider": "test", "model": "preferred", "capabilities": []string{"chat"}, "qualityScore": 1.0, "maxContextTokens": 100000},
		{"id": "locked", "provider": "test", "model": "locked", "capabilities": []string{"chat"}, "qualityScore": 0.2, "maxContextTokens": 100000},
		{"id": "video", "provider": "test", "model": "video", "capabilities": []string{"video_generation"}, "executionKind": "media", "adapterRef": "grok_generate_video", "inputCostPerMillion": 0.0, "outputCostPerMillion": 0.0},
	} {
		id := endpoint["id"].(string)
		delete(endpoint, "id")
		response := managementRequest(t, runtime, http.MethodPut, "/api/v1/model-endpoints/"+id, endpoint, "admin")
		if response.Code != http.StatusOK {
			t.Fatalf("model %s create = %d: %s", id, response.Code, response.Body.String())
		}
	}
	var zeroPriceSource, zeroPriceCheckedAt string
	if err := runtime.configStore.db.QueryRow(`SELECT pricing_source, pricing_checked_at FROM model_endpoints WHERE id = 'video'`).Scan(&zeroPriceSource, &zeroPriceCheckedAt); err != nil {
		t.Fatal(err)
	}
	if zeroPriceSource != "" || zeroPriceCheckedAt != "" {
		t.Fatalf("zero price marked as configured: source=%q checkedAt=%q", zeroPriceSource, zeroPriceCheckedAt)
	}
	health := managementRequest(t, runtime, http.MethodPut, "/api/v1/model-health/preferred", map[string]any{
		"status": "healthy", "latencyMs": 100, "errorRate": 0.01,
		"statusMessage": "must be cleared for healthy endpoints",
		"checkedAt":     time.Now().UTC().Format(time.RFC3339Nano),
	}, "runtime")
	if health.Code != http.StatusOK || strings.Contains(health.Body.String(), "must be cleared") {
		t.Fatalf("runtime health update = %d: %s", health.Code, health.Body.String())
	}
	unavailable := managementRequest(t, runtime, http.MethodPut, "/api/v1/model-health/video", map[string]any{
		"status": "unhealthy", "statusMessage": "无可用账号", "latencyMs": nil,
		"errorRate": 1.0, "checkedAt": time.Now().UTC().Format(time.RFC3339Nano),
	}, "runtime")
	if unavailable.Code != http.StatusOK || !strings.Contains(unavailable.Body.String(), `"statusMessage":"无可用账号"`) {
		t.Fatalf("unavailable health update = %d: %s", unavailable.Code, unavailable.Body.String())
	}
	unknownCapability := managementRequest(t, runtime, http.MethodPut, "/api/v1/model-endpoints/bad", map[string]any{
		"provider": "test", "model": "bad", "capabilities": []string{"telepathy"},
	}, "admin")
	if unknownCapability.Code != http.StatusBadRequest {
		t.Fatalf("unknown model capability accepted: %s", unknownCapability.Body.String())
	}
	control := managementRequest(t, runtime, http.MethodPut, "/api/v1/routing/control", map[string]any{
		"mode": "manual", "locks": map[string]string{"chat": "locked"},
	}, "admin")
	if control.Code != http.StatusOK {
		t.Fatalf("routing control = %d: %s", control.Code, control.Body.String())
	}
	simulated := managementRequest(t, runtime, http.MethodPost, "/api/v1/routing/simulate", map[string]any{"lane": "chat"}, "admin")
	if simulated.Code != http.StatusOK || !strings.Contains(simulated.Body.String(), `"id":"locked"`) || !strings.Contains(simulated.Body.String(), `"operatorMode":"manual"`) {
		t.Fatalf("manual route = %d: %s", simulated.Code, simulated.Body.String())
	}
	missingLock := managementRequest(t, runtime, http.MethodPut, "/api/v1/routing/control", map[string]any{
		"mode": "manual", "locks": map[string]string{"chat": "missing"},
	}, "admin")
	if missingLock.Code != http.StatusBadRequest {
		t.Fatalf("missing routing lock accepted: %s", missingLock.Body.String())
	}
}

func TestManagementToolValidationOverviewAndRedactedAudit(t *testing.T) {
	runtime := newManagementRuntime(t)
	sensitiveDescription := "lookup-with-private-context"
	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/tools", map[string]any{
		"id": "ops-status", "name": "ops_status", "description": sensitiveDescription,
		"capabilities": []string{"tool_calling", "web_search"}, "riskLevel": 1,
		"adapterRef": "core:ops_status", "allowedAuthorities": []string{"member", "admin"},
		"approvalMode": "confirm", "timeoutSeconds": 12,
		"inputSchema": map[string]any{"type": "object"},
	}, "admin")
	if created.Code != http.StatusCreated {
		t.Fatalf("tool create = %d: %s", created.Code, created.Body.String())
	}
	highRisk := managementRequest(t, runtime, http.MethodPost, "/api/v1/tools", map[string]any{
		"name": "unsafe_tool", "riskLevel": 3,
		"allowedAuthorities": []string{"member", "admin"}, "approvalMode": "confirm",
	}, "admin")
	if highRisk.Code != http.StatusBadRequest || !strings.Contains(highRisk.Body.String(), "cannot be granted to members") {
		t.Fatalf("high-risk member tool = %d: %s", highRisk.Code, highRisk.Body.String())
	}
	var detailsJSON string
	if err := runtime.configStore.db.QueryRow(`
		SELECT details_json FROM audit_events
		WHERE target_type = 'tool' AND target_id = 'ops-status' ORDER BY id LIMIT 1
	`).Scan(&detailsJSON); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(detailsJSON, sensitiveDescription) || !strings.Contains(detailsJSON, "description") {
		t.Fatalf("audit details leaked value or omitted field: %s", detailsJSON)
	}
	overview := managementRequest(t, runtime, http.MethodGet, "/api/v1/overview", nil, "")
	if overview.Code != http.StatusOK || !strings.Contains(overview.Body.String(), `"tools":1`) || !strings.Contains(overview.Body.String(), `"schemaVersion":13`) {
		t.Fatalf("overview = %d: %s", overview.Code, overview.Body.String())
	}
	audit := managementRequest(t, runtime, http.MethodGet, "/api/v1/audit?limit=10", nil, "")
	if audit.Code != http.StatusOK || strings.Contains(audit.Body.String(), sensitiveDescription) {
		t.Fatalf("audit API = %d: %s", audit.Code, audit.Body.String())
	}
}

func TestManagementShadowUsesRuntimeTokenWithoutRawPersistence(t *testing.T) {
	runtime := newManagementRuntime(t)
	model := managementRequest(t, runtime, http.MethodPut, "/api/v1/model-endpoints/chat", map[string]any{
		"provider": "test", "model": "chat", "capabilities": []string{"chat"},
	}, "admin")
	if model.Code != http.StatusOK {
		t.Fatalf("model create = %d: %s", model.Code, model.Body.String())
	}
	message := "Please search today's AI news"
	recorded := managementRequest(t, runtime, http.MethodPost, "/api/v1/shadow/interactions", map[string]any{
		"transport": "qq", "conversationRef": "group-123", "senderRef": "user-456",
		"message": message, "lane": "chat",
	}, "runtime")
	if recorded.Code != http.StatusCreated {
		t.Fatalf("shadow record = %d: %s", recorded.Code, recorded.Body.String())
	}
	var conversationHash, senderHash, routeJSON string
	var messageLength int
	if err := runtime.configStore.db.QueryRow(`
		SELECT conversation_hash, sender_hash, message_length, route_json
		FROM shadow_interactions LIMIT 1
	`).Scan(&conversationHash, &senderHash, &messageLength, &routeJSON); err != nil {
		t.Fatal(err)
	}
	persisted := strings.Join([]string{conversationHash, senderHash, routeJSON}, " ")
	if strings.Contains(persisted, "group-123") || strings.Contains(persisted, "user-456") || strings.Contains(persisted, message) {
		t.Fatalf("shadow persistence retained raw input: %s", persisted)
	}
	if conversationHash == "group-123" || senderHash == "user-456" || messageLength != len([]rune(message)) {
		t.Fatalf("shadow metadata = %q %q %d", conversationHash, senderHash, messageLength)
	}
	listed := managementRequest(t, runtime, http.MethodGet, "/api/v1/shadow/interactions", nil, "")
	if listed.Code != http.StatusOK || strings.Contains(listed.Body.String(), "group-123") || strings.Contains(listed.Body.String(), message) {
		t.Fatalf("shadow list leaked raw input: %s", listed.Body.String())
	}
}
