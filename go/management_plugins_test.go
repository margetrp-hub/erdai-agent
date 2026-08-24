package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestManagementPluginsListHealthAndToggleIntegration(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken

	listed := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins", nil, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"sub2api-channel-monitor"`) || !strings.Contains(listed.Body.String(), `"id":"affiliate-invite"`) || !strings.Contains(listed.Body.String(), `"id":"knowledge-retrieval"`) || !strings.Contains(listed.Body.String(), `"id":"tools-and-mcp"`) {
		t.Fatalf("plugin list = %d: %s", listed.Code, listed.Body.String())
	}
	messagePlugin, found, err := runtime.configStore.mgmtPlugin("message-experience")
	if err != nil || !found {
		t.Fatalf("message plugin lookup: found=%v err=%v", found, err)
	}
	if messagePlugin.Toggleable || messagePlugin.ToggleMode != "readonly" || messagePlugin.ConfigView != "integrations" {
		t.Fatalf("message plugin contract = %#v", messagePlugin)
	}
	messageToggle := managementRequest(t, runtime, http.MethodPut, "/api/v1/plugins/message-experience", map[string]any{"enabled": false}, "admin")
	if messageToggle.Code != http.StatusBadRequest {
		t.Fatalf("message plugin fake toggle = %d: %s", messageToggle.Code, messageToggle.Body.String())
	}
	var messageRaw string
	if err := runtime.configStore.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'message_policy'").Scan(&messageRaw); err != nil {
		t.Fatal(err)
	}
	messageConfig := map[string]any{}
	if err := json.Unmarshal([]byte(messageRaw), &messageConfig); err != nil {
		t.Fatal(err)
	}
	if _, exists := messageConfig["enabled"]; exists {
		t.Fatalf("message policy acquired fake enabled field: %s", messageRaw)
	}

	disabled := managementRequest(t, runtime, http.MethodPut, "/api/v1/plugins/affiliate-invite", map[string]any{"enabled": false}, "admin")
	if disabled.Code != http.StatusOK || !strings.Contains(disabled.Body.String(), `"state":"disabled"`) {
		t.Fatalf("disable plugin = %d: %s", disabled.Code, disabled.Body.String())
	}
	var enabledValue bool
	if err := runtime.configStore.db.QueryRow("SELECT json_extract(config_json, '$.enabled') FROM integration_settings WHERE id = 'affiliate_policy'").Scan(&enabledValue); err != nil {
		t.Fatal(err)
	}
	if enabledValue {
		t.Fatal("affiliate integration remained enabled after plugin disable")
	}

	health := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins/affiliate-invite/health", nil, "")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"pluginId":"affiliate-invite"`) {
		t.Fatalf("plugin health = %d: %s", health.Code, health.Body.String())
	}

	enabled := managementRequest(t, runtime, http.MethodPut, "/api/v1/plugins/affiliate-invite", map[string]any{"enabled": true}, "admin")
	if enabled.Code != http.StatusOK || !strings.Contains(enabled.Body.String(), `"enabled":true`) {
		t.Fatalf("enable plugin = %d: %s", enabled.Code, enabled.Body.String())
	}
	readyHealth := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins/affiliate-invite/health", nil, "")
	if readyHealth.Code != http.StatusOK || !strings.Contains(readyHealth.Body.String(), `"state":"ready"`) || strings.Contains(readyHealth.Body.String(), `"state":"healthy"`) {
		t.Fatalf("affiliate readiness = %d: %s", readyHealth.Code, readyHealth.Body.String())
	}
}

func TestManagementPluginsCanRegisterAndRemoveExternalManifest(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken

	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/plugins", map[string]any{
		"id":          "external-calendar",
		"name":        "日程助手",
		"description": "登记一个未来由 Core 适配器实现的能力包。",
		"version":     "0.1.0",
		"author":      "二呆扩展",
		"manifest": map[string]any{
			"category":     "extension",
			"capabilities": []string{"日程查询"},
			"commands":     []string{"/日程"},
		},
	}, "admin")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"source":"external"`) || !strings.Contains(created.Body.String(), `"state":"registered"`) {
		t.Fatalf("create external plugin = %d: %s", created.Code, created.Body.String())
	}

	health := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins/external-calendar/health", nil, "")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"state":"registered"`) {
		t.Fatalf("external plugin health = %d: %s", health.Code, health.Body.String())
	}

	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/plugins/external-calendar", nil, "admin")
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("delete external plugin = %d: %s", deleted.Code, deleted.Body.String())
	}
	builtinDelete := managementRequest(t, runtime, http.MethodDelete, "/api/v1/plugins/affiliate-invite", nil, "admin")
	if builtinDelete.Code != http.StatusBadRequest {
		t.Fatalf("builtin plugin delete = %d: %s", builtinDelete.Code, builtinDelete.Body.String())
	}
}

func TestManagementPluginsRejectExternalRuntimeOwnership(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken

	for name, payload := range map[string]map[string]any{
		"top-level integration": {
			"id": "external-top-level", "name": "外部顶层绑定", "integrationId": "affiliate_policy",
		},
		"nested integration": {
			"id": "external-nested", "name": "外部嵌套绑定",
			"manifest": map[string]any{"category": "extension", "adapter": map[string]any{"integrationId": "affiliate_policy"}},
		},
		"runtime health": {
			"id": "external-health", "name": "外部健康声明",
			"manifest": map[string]any{"category": "extension", "healthMode": "live"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			response := managementRequest(t, runtime, http.MethodPost, "/api/v1/plugins", payload, "admin")
			if response.Code != http.StatusBadRequest {
				t.Fatalf("external ownership accepted = %d: %s", response.Code, response.Body.String())
			}
		})
	}
}

func TestManagementPluginHealthReportsDependencyDegradation(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken

	disabled := managementRequest(t, runtime, http.MethodPut, "/api/v1/plugins/companion-context", map[string]any{"enabled": false}, "admin")
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable dependency = %d: %s", disabled.Code, disabled.Body.String())
	}
	health := managementRequest(t, runtime, http.MethodGet, "/api/v1/plugins/group-conversation/health", nil, "")
	if health.Code != http.StatusOK || !strings.Contains(health.Body.String(), `"state":"degraded"`) || !strings.Contains(health.Body.String(), `"companion-context":"disabled"`) {
		t.Fatalf("dependency health = %d: %s", health.Code, health.Body.String())
	}
}
