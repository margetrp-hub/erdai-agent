package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeMCPBridgeDiscoversAndCallsOnlyAfterExplicitConfirmation(t *testing.T) {
	t.Setenv("MCP_TEST_AUTH", "test-auth-value")
	server := newSuccessfulNativeMCPServer(t, nil, nil)
	defer server.Close()
	store := newNativeMCPConfigStore(t, nativeMCPTestConfig{
		Endpoint: endpointUsingLocalhost(t, server.URL),
		EnvHeaderRefs: map[string]string{
			"X-Test-Auth": "MCP_TEST_AUTH",
		},
	})
	defer store.Close()
	runtime := &AgentRuntime{configStore: store, client: nativeMCPTestHTTPClient(t, server)}
	policy := nativeMCPTestPolicy()

	definitions, routes := runtime.discoverCoreMCPTools(
		context.Background(), policy, false, "ordinary chat",
	)
	if len(definitions) != 0 || len(routes) != 0 {
		t.Fatal("unconfirmed MCP server was exposed")
	}
	definitions, routes = runtime.discoverCoreMCPTools(
		context.Background(), policy, false, "Use Context7 MCP documentation",
	)
	if len(definitions) != 1 || len(routes) != 1 {
		t.Fatalf("MCP discovery = %d definitions, %d routes", len(definitions), len(routes))
	}
	if _, exposed := routes["docs_hidden-tool"]; exposed {
		t.Fatal("tool outside the allow-list was exposed")
	}
	result := runtime.callCoreMCP(
		context.Background(), routes["docs_query-docs"],
		`{"libraryId":"/golang/go","query":"ReadHeaderTimeout"}`,
	)
	if !strings.Contains(result.Content, `"ok":true`) ||
		!strings.Contains(result.Content, "ReadHeaderTimeout documentation") {
		t.Fatalf("MCP result = %s", result.Content)
	}
	var successfulCalls int
	if err := store.db.QueryRow(`
		SELECT count(*) FROM audit_events
		WHERE action = 'call_succeeded' AND target_id = 'context7-docs:query-docs'
	`).Scan(&successfulCalls); err != nil || successfulCalls != 1 {
		t.Fatalf("MCP audit count = %d, err=%v", successfulCalls, err)
	}
}

func TestConfirmedMCPSelectionRequiresItsOwnNameOrTool(t *testing.T) {
	server := runtimeMCPServer{
		ID: "microsoft-learn", Name: "Microsoft Learn", ToolPrefix: "mslearn_",
		AllowedTools: []string{"microsoft_docs_search"}, ApprovalMode: "confirm",
	}
	if approved, allowed := mcpServerPermitted(server, false, "Use Context7 MCP documentation"); approved || allowed {
		t.Fatal("unrelated MCP name exposed Microsoft Learn")
	}
	if approved, allowed := mcpServerPermitted(server, false, "Use Microsoft Learn to check docs"); !approved || !allowed {
		t.Fatal("named MCP server was not exposed")
	}
	if approved, allowed := mcpServerPermitted(server, false, "Call microsoft_docs_search"); !approved || !allowed {
		t.Fatal("named MCP tool was not exposed")
	}
}

func TestNativeContext7MCPRunsThroughAgentLoopAndOutbox(t *testing.T) {
	t.Setenv("MCP_TEST_AUTH", "test-auth-value")
	var modelCalls atomic.Int32
	var mcpCalls atomic.Int32
	mcpStarted := make(chan struct{}, 1)
	releaseMCP := make(chan struct{})

	mcpServer := newSuccessfulNativeMCPServer(t, func() {
		mcpCalls.Add(1)
		mcpStarted <- struct{}{}
		<-releaseMCP
	}, nil)
	defer mcpServer.Close()

	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		call := modelCalls.Add(1)
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if call == 1 {
			tools, _ := input["tools"].([]any)
			if !strings.Contains(string(mustJSON(t, tools)), "docs_query-docs") {
				t.Fatalf("model tools = %+v", tools)
			}
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]any{
					"role": "assistant", "content": "",
					"tool_calls": []any{toolCallResponse(
						"mcp-1", "docs_query-docs",
						`{"libraryId":"/golang/go","query":"Server.ReadHeaderTimeout"}`,
					)},
				},
			}}})
			return
		}
		messages, _ := input["messages"].([]any)
		encoded := string(mustJSON(t, messages))
		if !strings.Contains(encoded, `"role":"tool"`) ||
			!strings.Contains(encoded, `\"ok\":true`) ||
			!strings.Contains(encoded, "ReadHeaderTimeout documentation") {
			t.Fatalf("tool result was not returned to model: %s", encoded)
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"role": "assistant", "content": "ReadHeaderTimeout limits request-header read time.",
			}}},
		})
	}))
	defer provider.Close()

	store := newNativeMCPConfigStore(t, nativeMCPTestConfig{
		Endpoint: endpointUsingLocalhost(t, mcpServer.URL),
		EnvHeaderRefs: map[string]string{
			"X-Test-Auth": "MCP_TEST_AUTH",
		},
		ProviderAPIBase: provider.URL + "/v1",
	})
	configPath := nativeMCPStorePath(t, store)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)),
		HTTPClient:    nativeMCPTestHTTPClient(t, provider),
	})
	if err != nil {
		store.Close()
		t.Fatal(err)
	}
	defer runtime.Close()
	defer func() {
		select {
		case <-releaseMCP:
		default:
			close(releaseMCP)
		}
	}()

	event := testTransportEvent(
		"mcp-loop-event",
		"Use Context7 MCP to query the /golang/go documentation.",
		true,
	)
	response := runtimeRequest(t, runtime, "/api/v1/transport/events", event, "mcp-loop-event")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"owned"`) {
		t.Fatalf("accept response = %d: %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)

	select {
	case <-mcpStarted:
	case <-time.After(15 * time.Second):
		t.Fatal("agent loop did not call native Context7 MCP")
	}
	var progressCount int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_deliveries
		WHERE run_id = ? AND phase = 'progress'`, accepted.Data.RunID).Scan(&progressCount); err != nil {
		t.Fatal(err)
	}
	if progressCount != 0 {
		t.Fatalf("short MCP task created %d progress deliveries", progressCount)
	}
	close(releaseMCP)
	waitForDelivery(t, runtime, accepted.Data.RunID)
	leaseAndAckOne(t, runtime, "terminal")

	var state string
	if err := runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = ?", accepted.Data.RunID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" || modelCalls.Load() != 2 || mcpCalls.Load() != 1 {
		t.Fatalf("state=%s modelCalls=%d mcpCalls=%d", state, modelCalls.Load(), mcpCalls.Load())
	}
}

type nativeMCPTestConfig struct {
	Endpoint        string
	EnvHeaderRefs   map[string]string
	SecretRef       string
	ApprovalMode    string
	Authorities     []string
	AllowedTools    []string
	TimeoutSeconds  int
	ProviderAPIBase string
}

func newNativeMCPConfigStore(t *testing.T, input nativeMCPTestConfig) *coreConfigStore {
	t.Helper()
	_, database := newTestCoreConfig(t)
	if input.ApprovalMode == "" {
		input.ApprovalMode = "confirm"
	}
	if input.Authorities == nil {
		input.Authorities = []string{"member", "admin"}
	}
	if input.AllowedTools == nil {
		input.AllowedTools = []string{"query-docs"}
	}
	if input.TimeoutSeconds == 0 {
		input.TimeoutSeconds = 5
	}
	config, _ := json.Marshal(map[string]any{
		"secretRef": input.SecretRef, "envHeaderRefs": input.EnvHeaderRefs,
		"allowedTools": input.AllowedTools, "allowedAuthorities": input.Authorities,
		"approvalMode": input.ApprovalMode, "timeoutSeconds": input.TimeoutSeconds,
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := database.Exec(`
		DELETE FROM tools;
		DELETE FROM mcp_servers;
		INSERT INTO mcp_servers (
			id, name, transport, endpoint, command, args_json, tool_prefix, enabled,
			config_json, created_at, updated_at
		) VALUES ('context7-docs', 'Context7', 'http', ?, '', '[]', 'docs_', 1, ?, ?, ?)
	`, input.Endpoint, string(config), now, now)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if input.ProviderAPIBase != "" {
		setTestIntegration(t, database, "provider_policy", map[string]string{
			"apiBase": input.ProviderAPIBase, "defaultModel": "fake-model",
		})
		insertTestEndpoint(t, database, "mcp-chat", "fake-model", []string{"chat"}, "llm", "openai")
	}
	return &coreConfigStore{db: database}
}

func nativeMCPStorePath(t *testing.T, store *coreConfigStore) string {
	t.Helper()
	var sequence int
	var name, path string
	if err := store.db.QueryRow("PRAGMA database_list").Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	return path
}

func nativeMCPTestPolicy() runtimeToolPolicy {
	return runtimeToolPolicy{Authority: "member", Tools: []runtimeTool{}, MCPServers: []runtimeMCPServer{{
		ID: "context7-docs", Name: "Context7", Transport: "http", ToolPrefix: "docs_",
		ApprovalMode: "confirm", AllowedTools: []string{"query-docs"}, TimeoutSeconds: 5,
	}}}
}

func newSuccessfulNativeMCPServer(
	t *testing.T,
	onToolCall func(),
	onRequest func(map[string]any),
) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Test-Auth") != "test-auth-value" {
			t.Fatal("environment-backed MCP header missing")
		}
		var input map[string]any
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Fatal(err)
		}
		if onRequest != nil {
			onRequest(input)
		}
		method, _ := input["method"].(string)
		if method != "initialize" && r.Header.Get("Mcp-Session-Id") != "session-test" {
			t.Fatal("MCP session header missing")
		}
		switch method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "session-test")
			writeNativeMCPResult(t, w, input["id"], map[string]any{
				"protocolVersion": nativeMCPProtocolVersion,
				"serverInfo":      map[string]string{"name": "test-mcp", "version": "1"},
			}, true)
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			writeNativeMCPResult(t, w, input["id"], map[string]any{
				"tools": []any{
					map[string]any{
						"name": "query-docs", "description": "Query documentation",
						"inputSchema": map[string]any{"type": "object"},
					},
					map[string]any{
						"name": "hidden-tool", "description": "Must stay hidden",
						"inputSchema": map[string]any{"type": "object"},
					},
				},
			}, false)
		case "tools/call":
			params, _ := input["params"].(map[string]any)
			if params["name"] != "query-docs" {
				t.Fatalf("tool call = %+v", params)
			}
			if onToolCall != nil {
				onToolCall()
			}
			writeNativeMCPResult(t, w, input["id"], map[string]any{
				"content": []any{map[string]string{
					"type": "text", "text": "ReadHeaderTimeout documentation",
				}},
			}, false)
		default:
			t.Fatalf("unexpected MCP method %q", method)
		}
	}))
}

func writeNativeMCPResult(t *testing.T, w http.ResponseWriter, id, result any, eventStream bool) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": id, "result": result,
	})
	if err != nil {
		t.Fatal(err)
	}
	if eventStream {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(payload)
}

func endpointUsingLocalhost(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Host = "localhost:" + parsed.Port()
	parsed.Path = "/mcp"
	return parsed.String()
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
