package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func newCompleteManagementRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &AgentRuntime{
		configStore: store, adminToken: managementAdminToken,
		runtimeToken: managementRuntimeToken, client: http.DefaultClient,
	}
}

func TestManagementKnowledgeCapabilitiesAndSearchPreview(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	capabilities := managementRequest(t, runtime, http.MethodGet, "/api/v1/capabilities", nil, "")
	if capabilities.Code != http.StatusOK || !strings.Contains(capabilities.Body.String(), `"id":"vision"`) ||
		!strings.Contains(capabilities.Body.String(), `"required":["chat"]`) {
		t.Fatalf("capabilities = %d: %s", capabilities.Code, capabilities.Body.String())
	}

	unauthorized := managementRequest(t, runtime, http.MethodPost, "/api/v1/knowledge/documents", map[string]any{
		"title": "denied", "content": "denied",
	}, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized knowledge create = %d", unauthorized.Code)
	}
	for _, document := range []map[string]any{
		{
			"id": "ops-green-red", "namespace": "ops", "title": "Group status display",
			"content":   "Display healthy groups with green icons and unhealthy groups with red icons.",
			"sourceUri": "https://example.test/ops", "metadata": map[string]any{"owner": "ops"},
		},
		{
			"id": "ops-alerting", "namespace": "ops", "title": "Alerting",
			"content": "Alerts must include a timestamp and affected group.",
		},
		{
			"id": "private-green-red", "namespace": "private", "title": "Private status",
			"content": "Display healthy groups while private details remain hidden.",
		},
	} {
		created := managementRequest(t, runtime, http.MethodPost, "/api/v1/knowledge/documents", document, "admin")
		if created.Code != http.StatusCreated {
			t.Fatalf("knowledge create = %d: %s", created.Code, created.Body.String())
		}
	}

	page := managementRequest(t, runtime, http.MethodGet, "/api/v1/knowledge/documents?namespace=ops&limit=1", nil, "")
	var pageBody struct {
		Data struct {
			Items []map[string]any `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	managementData(t, page, &pageBody)
	if page.Code != http.StatusOK || pageBody.Data.Total != 2 || len(pageBody.Data.Items) != 1 {
		t.Fatalf("knowledge page = %d: %s", page.Code, page.Body.String())
	}
	if _, present := pageBody.Data.Items[0]["content"]; present {
		t.Fatalf("knowledge list exposed content: %s", page.Body.String())
	}

	hidden := managementRequest(t, runtime, http.MethodGet, "/api/v1/knowledge/documents/ops-green-red?namespace=private", nil, "")
	if hidden.Code != http.StatusNotFound {
		t.Fatalf("cross-namespace document = %d", hidden.Code)
	}
	preview := managementRequest(t, runtime, http.MethodPost, "/api/v1/knowledge/search-preview", map[string]any{
		"namespace": "ops", "query": "healthy groups", "limit": 10,
	}, "admin")
	if preview.Code != http.StatusOK || !strings.Contains(preview.Body.String(), `"id":"ops-green-red"`) ||
		strings.Contains(preview.Body.String(), `"id":"private-green-red"`) {
		t.Fatalf("knowledge preview = %d: %s", preview.Code, preview.Body.String())
	}
	updated := managementRequest(t, runtime, http.MethodPut, "/api/v1/knowledge/documents/ops-green-red?namespace=ops", map[string]any{
		"title": "Group status legend", "metadata": map[string]any{"owner": "platform"},
	}, "admin")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"owner":"platform"`) {
		t.Fatalf("knowledge update = %d: %s", updated.Code, updated.Body.String())
	}
	unknown := managementRequest(t, runtime, http.MethodPost, "/api/v1/knowledge/documents", map[string]any{
		"title": "unsafe", "content": "unsafe", "apiKey": "must-not-be-stored",
	}, "admin")
	if unknown.Code != http.StatusBadRequest || !strings.Contains(unknown.Body.String(), "unsupported knowledge document fields: apiKey") {
		t.Fatalf("knowledge field whitelist = %d: %s", unknown.Code, unknown.Body.String())
	}
	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/knowledge/documents/ops-green-red?namespace=ops", nil, "admin")
	if deleted.Code != http.StatusOK || !strings.Contains(deleted.Body.String(), `"deleted":true`) {
		t.Fatalf("knowledge delete = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestManagementDirectivesAndKnowledgeCandidateReview(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	directive := managementRequest(t, runtime, http.MethodPost, "/api/v1/runtime/directives", map[string]any{
		"id": "owner-rule", "content": "Administrator instructions outrank member requests.",
	}, "admin")
	if directive.Code != http.StatusCreated || !strings.Contains(directive.Body.String(), `"createdByAuthority":"admin"`) {
		t.Fatalf("directive create = %d: %s", directive.Code, directive.Body.String())
	}
	updatedDirective := managementRequest(t, runtime, http.MethodPut, "/api/v1/runtime/directives/owner-rule", map[string]any{
		"enabled": false,
	}, "admin")
	if updatedDirective.Code != http.StatusOK || !strings.Contains(updatedDirective.Body.String(), `"enabled":false`) {
		t.Fatalf("directive update = %d: %s", updatedDirective.Code, updatedDirective.Body.String())
	}

	candidate := managementRequest(t, runtime, http.MethodPost, "/api/v1/runtime/knowledge-candidates", map[string]any{
		"id": "api-candidate", "title": "API candidate",
		"content": "Only approved content is published.", "tags": []string{"go", "core"},
	}, "runtime")
	if candidate.Code != http.StatusCreated || !strings.Contains(candidate.Body.String(), `"status":"pending"`) {
		t.Fatalf("runtime candidate create = %d: %s", candidate.Code, candidate.Body.String())
	}
	runtimeReview := managementRequest(t, runtime, http.MethodPost, "/api/v1/runtime/knowledge-candidates/api-candidate/review", map[string]any{
		"decision": "approved", "authority": "admin",
	}, "runtime")
	if runtimeReview.Code != http.StatusUnauthorized {
		t.Fatalf("runtime token reviewed candidate = %d: %s", runtimeReview.Code, runtimeReview.Body.String())
	}
	memberReview := managementRequest(t, runtime, http.MethodPost, "/api/v1/runtime/knowledge-candidates/api-candidate/review", map[string]any{
		"decision": "approved", "authority": "member",
	}, "admin")
	if memberReview.Code != http.StatusBadRequest {
		t.Fatalf("member authority review = %d: %s", memberReview.Code, memberReview.Body.String())
	}
	approved := managementRequest(t, runtime, http.MethodPost, "/api/v1/runtime/knowledge-candidates/api-candidate/review", map[string]any{
		"decision": "approved", "authority": "admin", "knowledgeNamespace": "learned",
	}, "admin")
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"status":"approved"`) ||
		!strings.Contains(approved.Body.String(), `"id":"candidate-api-candidate"`) {
		t.Fatalf("candidate approval = %d: %s", approved.Code, approved.Body.String())
	}
	published := managementRequest(t, runtime, http.MethodGet, "/api/v1/knowledge/documents/candidate-api-candidate?namespace=learned", nil, "")
	if published.Code != http.StatusOK || !strings.Contains(published.Body.String(), `"candidateId":"api-candidate"`) {
		t.Fatalf("published candidate = %d: %s", published.Code, published.Body.String())
	}
	reviewedEdit := managementRequest(t, runtime, http.MethodPut, "/api/v1/runtime/knowledge-candidates/api-candidate", map[string]any{
		"title": "changed",
	}, "admin")
	if reviewedEdit.Code != http.StatusBadRequest {
		t.Fatalf("reviewed candidate edit = %d: %s", reviewedEdit.Code, reviewedEdit.Body.String())
	}
	deletedDirective := managementRequest(t, runtime, http.MethodDelete, "/api/v1/runtime/directives/owner-rule", nil, "admin")
	if deletedDirective.Code != http.StatusOK {
		t.Fatalf("directive delete = %d: %s", deletedDirective.Code, deletedDirective.Body.String())
	}
}

func TestManagementMCPCRUDDiscoverAndCall(t *testing.T) {
	mcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode MCP request: %v", err)
			return
		}
		method, _ := request["method"].(string)
		if method == "notifications/initialized" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		response := map[string]any{"jsonrpc": "2.0", "id": request["id"]}
		switch method {
		case "initialize":
			response["result"] = map[string]any{
				"protocolVersion": nativeMCPProtocolVersion,
				"serverInfo":      map[string]any{"name": "fixture", "version": "1"},
			}
		case "tools/list":
			response["result"] = map[string]any{"tools": []map[string]any{
				{"name": "search", "description": "Search", "inputSchema": map[string]any{"type": "object"}},
				{"name": "delete_everything", "description": "Denied", "inputSchema": map[string]any{"type": "object"}},
			}}
		case "tools/call":
			response["result"] = map[string]any{
				"content": []map[string]any{{"type": "text", "text": "result"}}, "isError": false,
			}
		default:
			t.Errorf("unexpected MCP method %q", method)
			return
		}
		_ = json.NewEncoder(w).Encode(response)
	}))
	defer mcp.Close()
	endpoint := strings.Replace(mcp.URL, "127.0.0.1", "localhost", 1) + "/mcp"
	runtime := newCompleteManagementRuntime(t)
	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers", map[string]any{
		"id": "fixture-mcp", "name": "Fixture MCP", "transport": "http",
		"endpoint": endpoint + "/", "toolPrefix": "fixture", "enabled": true,
		"allowedTools": []string{"search"}, "allowedAuthorities": []string{"member", "admin"},
		"approvalMode": "confirm", "timeoutSeconds": 5,
	}, "admin")
	if created.Code != http.StatusCreated || strings.Contains(created.Body.String(), endpoint+"/") {
		t.Fatalf("MCP create = %d: %s", created.Code, created.Body.String())
	}
	literalSecret := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers", map[string]any{
		"name": "Unsafe", "transport": "http", "endpoint": "https://mcp.example.test",
		"secretRef": "sk-literal-secret",
	}, "admin")
	if literalSecret.Code != http.StatusBadRequest || !strings.Contains(literalSecret.Body.String(), "environment variable name") {
		t.Fatalf("literal MCP secret = %d: %s", literalSecret.Code, literalSecret.Body.String())
	}
	discovered := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers/fixture-mcp/discover", map[string]any{}, "admin")
	if discovered.Code != http.StatusOK || !strings.Contains(discovered.Body.String(), `"name":"fixture"`) ||
		!strings.Contains(discovered.Body.String(), `"name":"delete_everything","description":"Denied","inputSchema":{"type":"object"},"allowed":false`) {
		t.Fatalf("MCP discovery = %d: %s", discovered.Code, discovered.Body.String())
	}
	denied := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers/fixture-mcp/call", map[string]any{
		"toolName": "delete_everything", "arguments": map[string]any{}, "authority": "admin", "approved": true,
	}, "admin")
	if denied.Code != http.StatusBadRequest || !strings.Contains(denied.Body.String(), "allow-list") {
		t.Fatalf("MCP allow-list = %d: %s", denied.Code, denied.Body.String())
	}
	unconfirmed := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers/fixture-mcp/call", map[string]any{
		"toolName": "search", "arguments": map[string]any{}, "authority": "member", "approved": false,
	}, "admin")
	if unconfirmed.Code != http.StatusBadRequest || !strings.Contains(unconfirmed.Body.String(), "confirmation") {
		t.Fatalf("MCP confirmation = %d: %s", unconfirmed.Code, unconfirmed.Body.String())
	}
	called := managementRequest(t, runtime, http.MethodPost, "/api/v1/mcp/servers/fixture-mcp/call", map[string]any{
		"toolName": "search", "arguments": map[string]any{"query": "Luker"},
		"authority": "member", "approved": true,
	}, "admin")
	if called.Code != http.StatusOK || !strings.Contains(called.Body.String(), `"text":"result"`) {
		t.Fatalf("MCP call = %d: %s", called.Code, called.Body.String())
	}
	updated := managementRequest(t, runtime, http.MethodPut, "/api/v1/mcp/servers/fixture-mcp", map[string]any{
		"enabled": false, "timeoutSeconds": 40,
	}, "admin")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"timeoutSeconds":40`) {
		t.Fatalf("MCP update = %d: %s", updated.Code, updated.Body.String())
	}
	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/mcp/servers/fixture-mcp", nil, "admin")
	if deleted.Code != http.StatusOK {
		t.Fatalf("MCP delete = %d: %s", deleted.Code, deleted.Body.String())
	}
}
