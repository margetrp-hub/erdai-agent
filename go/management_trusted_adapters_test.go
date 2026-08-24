package main

import (
	"net/http"
	"strings"
	"testing"
)

func createExternalPluginForAdapter(t *testing.T, runtime *AgentRuntime, id string) {
	t.Helper()
	response := managementRequest(t, runtime, http.MethodPost, "/api/v1/plugins", map[string]any{
		"id": id, "name": "外部能力 " + id,
		"manifest": map[string]any{"category": "extension", "capabilities": []string{"测试能力"}},
	}, "admin")
	if response.Code != http.StatusCreated {
		t.Fatalf("create external plugin %s = %d: %s", id, response.Code, response.Body.String())
	}
}

func TestManagementTrustedAdapterLifecycleAndPluginOwnership(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken
	createExternalPluginForAdapter(t, runtime, "external-calendar")

	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/trusted-adapters", map[string]any{
		"id": "calendar-core", "name": "日历 Core 适配器", "pluginId": "external-calendar",
		"integrationId": "memory_policy", "version": "1.0.0",
		"permissions": []string{"runtime.toggle", "health.read", "config.read"}, "enabled": true,
	}, "admin")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"state":"ready"`) {
		t.Fatalf("create trusted adapter = %d: %s", created.Code, created.Body.String())
	}

	listed := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins", nil, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"external-calendar"`) ||
		!strings.Contains(listed.Body.String(), `"integrationId":"memory_policy"`) ||
		!strings.Contains(listed.Body.String(), `"toggleMode":"adapter_policy_field"`) ||
		!strings.Contains(listed.Body.String(), `"adapter":{"id":"calendar-core"`) {
		t.Fatalf("trusted plugin ownership = %d: %s", listed.Code, listed.Body.String())
	}

	health := managementRequest(t, runtime, http.MethodGet, "/api/v1/trusted-adapters/calendar-core/health", nil, "")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"state":"ready"`) || !strings.Contains(health.Body.String(), `"history":[`) {
		t.Fatalf("trusted adapter health = %d: %s", health.Code, health.Body.String())
	}

	disabled := managementRequest(t, runtime, http.MethodPut, "/api/v1/trusted-adapters/calendar-core", map[string]any{"enabled": false}, "admin")
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"state":"disabled"`) {
		t.Fatalf("disable trusted adapter = %d: %s", disabled.Code, disabled.Body.String())
	}
	listed = managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins", nil, "")
	if !strings.Contains(listed.Body.String(), `"id":"external-calendar"`) || !strings.Contains(listed.Body.String(), `"state":"registered"`) {
		t.Fatalf("disabled adapter plugin state = %s", listed.Body.String())
	}

	reenabled := managementRequest(t, runtime, http.MethodPut, "/api/v1/trusted-adapters/calendar-core", map[string]any{"enabled": true}, "admin")
	if reenabled.Code != http.StatusOK {
		t.Fatalf("enable trusted adapter = %d: %s", reenabled.Code, reenabled.Body.String())
	}
	toggled := managementRequest(t, runtime, http.MethodPut, "/api/v1/plugins/external-calendar", map[string]any{"enabled": false}, "admin")
	if toggled.Code != http.StatusOK || !strings.Contains(toggled.Body.String(), `"state":"disabled"`) {
		t.Fatalf("adapter-owned runtime toggle = %d: %s", toggled.Code, toggled.Body.String())
	}

	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/trusted-adapters/calendar-core", nil, "admin")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete trusted adapter = %d: %s", deleted.Code, deleted.Body.String())
	}
	listed = managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins", nil, "")
	if !strings.Contains(listed.Body.String(), `"id":"external-calendar"`) || !strings.Contains(listed.Body.String(), `"state":"registered"`) {
		t.Fatalf("plugin after adapter deletion = %s", listed.Body.String())
	}
}

func TestManagementTrustedAdaptersRejectUntrustedOwnership(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken
	createExternalPluginForAdapter(t, runtime, "external-invalid")

	tests := []struct {
		name    string
		payload map[string]any
	}{
		{
			name: "builtin plugin",
			payload: map[string]any{"id": "builtin-adapter", "name": "内置适配器", "pluginId": "affiliate-invite",
				"integrationId": "affiliate_policy", "permissions": []string{"health.read"}},
		},
		{
			name: "unknown permission",
			payload: map[string]any{"id": "unknown-permission", "name": "未知权限", "pluginId": "external-invalid",
				"integrationId": "memory_policy", "permissions": []string{"health.read", "process.exec"}},
		},
		{
			name: "missing health permission",
			payload: map[string]any{"id": "missing-health", "name": "缺少健康权限", "pluginId": "external-invalid",
				"integrationId": "memory_policy", "permissions": []string{"config.read"}},
		},
		{
			name: "non-toggleable integration",
			payload: map[string]any{"id": "message-toggle", "name": "消息开关", "pluginId": "external-invalid",
				"integrationId": "message_policy", "permissions": []string{"health.read", "runtime.toggle"}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := managementRequest(t, runtime, http.MethodPost, "/api/v1/trusted-adapters", test.payload, "admin")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("untrusted adapter accepted = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}
