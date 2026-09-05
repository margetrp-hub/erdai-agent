package main

import (
	"errors"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

func (a *AgentRuntime) handleNativeManagement(w http.ResponseWriter, r *http.Request, path string) bool {
	if a.configStore == nil || !isNativeManagementPath(path) {
		return false
	}
	sensitiveRead := r.Method == http.MethodGet && (path == "/api/v1/affiliate/ownership" ||
		strings.HasPrefix(path, "/api/v1/platforms/") && (strings.HasSuffix(path, "/login-qr") || strings.HasSuffix(path, "/telegram-user/auth/status")))
	if sensitiveRead && !tokenMatches(r.Header.Get(adminTokenHeader), a.adminToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"code": "unauthorized", "message": "administrator service token required"},
		})
		return true
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
		adminOK := tokenMatches(r.Header.Get(adminTokenHeader), a.adminToken)
		runtimeMutation := r.Method == http.MethodPost && path == "/api/v1/shadow/interactions" ||
			r.Method == http.MethodPost && path == "/api/v1/runtime/knowledge-candidates" ||
			r.Method == http.MethodPut && strings.HasPrefix(path, "/api/v1/model-health/")
		if !adminOK && !(runtimeMutation && a.authorized(r)) {
			message := "administrator service token required"
			if runtimeMutation {
				message = "runtime service token required"
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{"code": "unauthorized", "message": message},
			})
			return true
		}
	}

	err := a.configStore.dispatchNativeManagement(a, w, r, path)
	if err != nil {
		writeCoreAPIError(w, err)
	}
	return true
}

func isNativeManagementPath(path string) bool {
	for _, exact := range []string{
		"/api/v1/affiliate/ownership",
		"/api/v1/overview", "/api/v1/observability", "/api/v1/audit", "/api/v1/shadow/interactions",
		"/api/v1/installation/status", "/api/v1/update/check", "/api/v1/update/status", "/api/v1/update/request",
		"/api/v1/credentials",
		"/api/v1/config/layers",
		"/api/v1/integrations", "/api/v1/model-endpoints", "/api/v1/model-health",
		"/api/v1/agent-policy-templates", "/api/v1/agent-instances", "/api/v1/agent-instance-routes", "/api/v1/agent-instance-capabilities",
		"/api/v1/provider-connections", "/api/v1/runs", "/api/v1/usage/stats",
		"/api/v1/provider-drivers",
		"/api/v1/routing/control", "/api/v1/routing/lanes", "/api/v1/routing/simulate",
		"/api/v1/tools", "/api/v1/skills", "/api/v1/platforms", "/api/v1/platforms/catalog", "/api/v1/platforms/runtime-status",
		"/api/v1/plugins", "/api/v1/trusted-adapters",
		"/api/v1/capabilities", "/api/v1/knowledge/bases", "/api/v1/knowledge/documents",
		"/api/v1/memories", "/api/v1/relationships",
		"/api/v1/knowledge/search-preview", "/api/v1/runtime/directives",
		"/api/v1/runtime/knowledge-candidates", "/api/v1/mcp/servers",
		"/api/v1/devices", "/api/v1/realtime/pairing-codes", "/api/v1/realtime/sessions",
		"/api/v1/tasks",
		"/api/v1/maintenance/media-gc",
	} {
		if path == exact {
			return true
		}
	}
	for _, prefix := range []string{
		"/api/v1/integrations/", "/api/v1/model-endpoints/", "/api/v1/model-health/",
		"/api/v1/credentials/",
		"/api/v1/agent-policy-templates/", "/api/v1/agent-instances/", "/api/v1/agent-instance-routes/", "/api/v1/agent-instance-capabilities/",
		"/api/v1/provider-connections/", "/api/v1/runs/",
		"/api/v1/tools/", "/api/v1/skills/", "/api/v1/platforms/",
		"/api/v1/plugins/", "/api/v1/trusted-adapters/",
		"/api/v1/knowledge/bases/",
		"/api/v1/knowledge/documents/", "/api/v1/runtime/directives/",
		"/api/v1/memories/", "/api/v1/relationships/",
		"/api/v1/runtime/knowledge-candidates/", "/api/v1/mcp/servers/",
		"/api/v1/devices/", "/api/v1/realtime/",
		"/api/v1/tasks/",
	} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (s *coreConfigStore) dispatchNativeManagement(a *AgentRuntime, w http.ResponseWriter, r *http.Request, path string) error {
	switch {
	case path == "/api/v1/affiliate/ownership":
		return a.handleAffiliateOwnership(w, r)
	case path == "/api/v1/installation/status":
		return a.handleManagementInstallation(w, r)
	case path == "/api/v1/update/check":
		return a.handleManagementUpdateCheck(w, r)
	case path == "/api/v1/update/status":
		return a.handleManagementUpdateStatus(w, r)
	case path == "/api/v1/update/request":
		return a.handleManagementUpdateRequest(w, r)
	case path == "/api/v1/credentials" || strings.HasPrefix(path, "/api/v1/credentials/"):
		return a.handleManagedCredentials(w, r, path)
	case path == "/api/v1/overview":
		return s.handleManagementOverview(w, r)
	case path == "/api/v1/observability":
		return a.handleManagementObservability(w, r)
	case path == "/api/v1/config/layers":
		return s.handleConfigLayers(w, r)
	case path == "/api/v1/audit":
		return s.handleManagementAudit(w, r)
	case path == "/api/v1/shadow/interactions":
		return s.handleManagementShadow(w, r)
	case path == "/api/v1/integrations" || strings.HasPrefix(path, "/api/v1/integrations/"):
		return s.handleManagementIntegrations(w, r, path)
	case path == "/api/v1/model-endpoints" || strings.HasPrefix(path, "/api/v1/model-endpoints/"):
		return s.handleManagementModels(w, r, path)
	case path == "/api/v1/model-health" || strings.HasPrefix(path, "/api/v1/model-health/"):
		return s.handleManagementHealth(w, r, path)
	case path == "/api/v1/provider-connections" || strings.HasPrefix(path, "/api/v1/provider-connections/"):
		if strings.HasSuffix(path, "/test") {
			return a.handleProviderConnectionTest(w, r, path)
		}
		if strings.HasSuffix(path, "/pricing-sync") {
			return a.handleProviderPricingSync(w, r, path)
		}
		return s.handleProviderConnections(w, r, path)
	case path == "/api/v1/agent-policy-templates" || strings.HasPrefix(path, "/api/v1/agent-policy-templates/") ||
		path == "/api/v1/agent-instances" || strings.HasPrefix(path, "/api/v1/agent-instances/") ||
		path == "/api/v1/agent-instance-routes" || strings.HasPrefix(path, "/api/v1/agent-instance-routes/"):
		return s.handleAgentInstanceRequest(w, r, path)
	case path == "/api/v1/agent-instance-capabilities" || strings.HasPrefix(path, "/api/v1/agent-instance-capabilities/"):
		return s.handleAgentInstanceCapabilities(w, r, path)
	case path == "/api/v1/provider-drivers":
		return s.handleProviderDrivers(w, r, path)
	case path == "/api/v1/runs" || strings.HasPrefix(path, "/api/v1/runs/"):
		return a.handleRunTimeline(w, r, path)
	case path == "/api/v1/usage/stats":
		return s.handleUsageStats(w, r)
	case path == "/api/v1/routing/control" || path == "/api/v1/routing/lanes" || path == "/api/v1/routing/simulate":
		return s.handleManagementRouting(w, r, path)
	case path == "/api/v1/tools" || strings.HasPrefix(path, "/api/v1/tools/"):
		return s.handleManagementTools(w, r, path)
	case path == "/api/v1/skills" || strings.HasPrefix(path, "/api/v1/skills/"):
		return s.handleManagementSkills(w, r, path)
	case path == "/api/v1/plugins" || strings.HasPrefix(path, "/api/v1/plugins/"):
		return s.handleManagementPlugins(a, w, r, path)
	case path == "/api/v1/trusted-adapters" || strings.HasPrefix(path, "/api/v1/trusted-adapters/"):
		return s.handleManagementTrustedAdapters(w, r, path)
	case path == "/api/v1/platforms/runtime-status":
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		mgmtWriteData(w, http.StatusOK, a.platformManager.Health())
		return nil
	case strings.HasPrefix(path, "/api/v1/platforms/") && strings.Contains(path, "/telegram-user/auth/"):
		return a.handleTelegramUserAuth(w, r, path)
	case strings.HasPrefix(path, "/api/v1/platforms/") && strings.HasSuffix(path, "/login-qr"):
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/platforms/"), "/login-qr")
		if id == "" || strings.Contains(id, "/") {
			return coreInvalid("platform id is invalid")
		}
		value, found := a.platformManager.WeixinOCQRCode(id)
		if !found {
			return mgmtNotFound("login QR code")
		}
		png, err := qrcode.Encode(value, qrcode.Medium, 256)
		if err != nil {
			return err
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(png)
		return nil
	case path == "/api/v1/platforms/catalog" || path == "/api/v1/platforms" || strings.HasPrefix(path, "/api/v1/platforms/"):
		return s.handleManagementPlatforms(w, r, path)
	case path == "/api/v1/capabilities":
		return s.handleManagementCapabilities(w, r)
	case path == "/api/v1/knowledge/bases" || strings.HasPrefix(path, "/api/v1/knowledge/bases/"):
		return s.handleKnowledgeBases(w, r, path)
	case path == "/api/v1/memories" || strings.HasPrefix(path, "/api/v1/memories/"):
		return a.handleManagementMemories(w, r, path)
	case path == "/api/v1/relationships" || strings.HasPrefix(path, "/api/v1/relationships/"):
		return a.handleManagementRelationships(w, r, path)
	case path == "/api/v1/knowledge/search-preview" || path == "/api/v1/knowledge/documents" || strings.HasPrefix(path, "/api/v1/knowledge/documents/"):
		return s.handleManagementKnowledge(w, r, path)
	case path == "/api/v1/runtime/directives" || strings.HasPrefix(path, "/api/v1/runtime/directives/"):
		return s.handleManagementDirectives(w, r, path)
	case path == "/api/v1/runtime/knowledge-candidates" || strings.HasPrefix(path, "/api/v1/runtime/knowledge-candidates/"):
		return s.handleManagementKnowledgeCandidates(w, r, path)
	case path == "/api/v1/mcp/servers" || strings.HasPrefix(path, "/api/v1/mcp/servers/"):
		return a.handleManagementMCP(w, r, path)
	case path == "/api/v1/devices" || strings.HasPrefix(path, "/api/v1/devices/") ||
		path == "/api/v1/realtime/pairing-codes" || path == "/api/v1/realtime/sessions":
		return a.handleRealtimeManagement(w, r, path)
	case path == "/api/v1/tasks" || strings.HasPrefix(path, "/api/v1/tasks/"):
		return a.handleTaskGraphManagement(w, r, path)
	case path == "/api/v1/maintenance/media-gc":
		return a.handleMediaGCManagement(w, r)
	default:
		return mgmtNotFound("route")
	}
}

func mgmtMethodNotAllowed() error {
	return &coreAPIError{
		status: http.StatusMethodNotAllowed, code: "method_not_allowed", message: "method not allowed",
	}
}

func mgmtNotFound(resource string) error {
	return &coreAPIError{
		status: http.StatusNotFound, code: "not_found", message: resource + " not found",
	}
}

func mgmtPathID(path, prefix string) (string, error) {
	if !strings.HasPrefix(path, prefix) {
		return "", mgmtNotFound("route")
	}
	return parseCorePathID(strings.TrimPrefix(path, prefix))
}

func mgmtWriteData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, map[string]any{"data": data})
}

func mgmtConstraintError(err error, message string) error {
	if err == nil {
		return nil
	}
	if strings.Contains(strings.ToLower(err.Error()), "constraint") ||
		strings.Contains(strings.ToLower(err.Error()), "unique") {
		return coreInvalid(message)
	}
	return err
}

func mgmtDeleteResult(result interface{ RowsAffected() (int64, error) }, resource string) (bool, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed == 0 {
		return false, mgmtNotFound(resource)
	}
	return true, nil
}

func mgmtIsNotFound(err error) bool {
	var apiError *coreAPIError
	return errors.As(err, &apiError) && apiError.status == http.StatusNotFound
}
