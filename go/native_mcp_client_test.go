package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNativeMCPEndpointRejectsSSRFAddresses(t *testing.T) {
	tests := map[string]string{
		"metadata":      "https://169.254.169.254/mcp",
		"private_10":    "https://10.0.0.1/mcp",
		"private_172":   "https://172.16.0.1/mcp",
		"private_192":   "https://192.168.1.1/mcp",
		"loopback_ipv4": "https://127.0.0.1/mcp",
		"loopback_ipv6": "https://[::1]/mcp",
	}
	for name, endpoint := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := validateNativeMCPEndpoint(context.Background(), endpoint, net.DefaultResolver)
			requireNativeMCPError(t, err, "blocked_address")
		})
	}
}

func TestNativeMCPDialRejectsDNSRebinding(t *testing.T) {
	resolver := &sequenceNativeMCPResolver{answers: [][]net.IPAddr{
		{{IP: net.ParseIP("8.8.8.8")}},
		{{IP: net.ParseIP("127.0.0.1")}},
	}}
	client, err := newNativeMCPClient(
		context.Background(), nil,
		nativeMCPServerConfig{Endpoint: "https://mcp.example.test/mcp"},
		time.Second, resolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.rpc(context.Background(), "tools/list", map[string]any{})
	requireNativeMCPError(t, err, "blocked_address")
	if resolver.callCount() != 2 {
		t.Fatalf("DNS lookups = %d, want 2", resolver.callCount())
	}
}

func TestNativeMCPRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(bytes.Repeat([]byte("x"), nativeMCPMaxResponse+1))
	}))
	defer server.Close()

	client := newNativeMCPTestClient(t, server, 2*time.Second)
	_, err := client.rpc(context.Background(), "tools/list", map[string]any{})
	requireNativeMCPError(t, err, "response_too_large")
}

func TestNativeMCPTimeouts(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
			writeNativeMCPResult(t, w, float64(1), map[string]any{}, false)
		}
	}))
	defer server.Close()

	t.Run("client_timeout", func(t *testing.T) {
		client := newNativeMCPTestClient(t, server, 100*time.Millisecond)
		_, err := client.rpc(context.Background(), "tools/list", map[string]any{})
		requireNativeMCPError(t, err, "timeout")
	})

	t.Run("context_timeout", func(t *testing.T) {
		client := newNativeMCPTestClient(t, server, 2*time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_, err := client.rpc(ctx, "tools/list", map[string]any{})
		requireNativeMCPError(t, err, "timeout")
	})
}

func TestNativeMCPPreparedPolicyCapsCallTimeout(t *testing.T) {
	if got := effectiveNativeMCPTimeout(
		nativeMCPServerConfig{Timeout: 5 * time.Second},
		runtimeMCPServer{TimeoutSeconds: 1},
	); got != time.Second {
		t.Fatalf("effective timeout = %s, want 1s", got)
	}
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	store := newNativeMCPConfigStore(t, nativeMCPTestConfig{
		Endpoint: endpointUsingLocalhost(t, server.URL), TimeoutSeconds: 5,
	})
	defer store.Close()
	runtime := &AgentRuntime{configStore: store, client: nativeMCPTestHTTPClient(t, server)}

	started := time.Now()
	_, err := runtime.callNativeMCPTool(context.Background(), mcpBridgeRoute{
		ServerID: "context7-docs", ToolName: "query-docs", Authority: "member",
		Approved: true, TimeoutSeconds: 1,
	}, map[string]any{})
	requireNativeMCPError(t, err, "timeout")
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("prepared one-second timeout took %s", elapsed)
	}
}

func TestNativeMCPRejectsProtocolErrors(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		contentType string
		want        string
	}{
		{
			name: "rpc_error", want: "rpc_error",
			body: `{"jsonrpc":"2.0","id":1,"error":{"code":-32603,"message":"do not leak me"}}`,
		},
		{name: "wrong_id", want: "invalid_response", body: `{"jsonrpc":"2.0","id":2,"result":{}}`},
		{name: "invalid_body", want: "invalid_response", body: `{not-json`},
		{name: "wrong_version", want: "invalid_response", body: `{"jsonrpc":"1.0","id":1,"result":{}}`},
		{name: "missing_result", want: "invalid_response", body: `{"jsonrpc":"2.0","id":1}`},
		{
			name: "invalid_sse", want: "invalid_response", contentType: "text/event-stream",
			body: "event: message\ndata: {not-json}\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := decodeNativeMCPResponse([]byte(test.body), test.contentType, "1")
			requireNativeMCPError(t, err, test.want)
			if err != nil && bytes.Contains([]byte(err.Error()), []byte("do not leak me")) {
				t.Fatal("upstream RPC message leaked into the local error")
			}
		})
	}
}

func TestNativeMCPRejectsSessionChange(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Mcp-Session-Id", "changed-session")
		writeNativeMCPResult(t, w, float64(1), map[string]any{}, false)
	}))
	defer server.Close()
	client := newNativeMCPTestClient(t, server, time.Second)
	client.sessionID = "original-session"
	_, err := client.rpc(context.Background(), "tools/list", map[string]any{})
	requireNativeMCPError(t, err, "invalid_session")
}

func TestNativeMCPRejectsRedirects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/redirected", http.StatusFound)
	}))
	defer server.Close()
	client := newNativeMCPTestClient(t, server, time.Second)
	_, err := client.rpc(context.Background(), "tools/list", map[string]any{})
	requireNativeMCPError(t, err, "redirect_blocked")
}

func TestNativeMCPHeaderReferencesAreRestricted(t *testing.T) {
	t.Setenv("MCP_HEADER_OK", "header-value")
	t.Setenv("MCP_SECRET_OK", "secret-value")
	t.Setenv("MCP_HEADER_EMPTY", "")
	t.Setenv("MCP_HEADER_INVALID", "bad\nvalue")

	tests := []struct {
		name   string
		server nativeMCPServerConfig
		want   string
	}{
		{
			name: "invalid_reference", want: "invalid_header_config",
			server: nativeMCPServerConfig{EnvHeaderRefs: map[string]string{"X-Token": "lowercase"}},
		},
		{
			name: "reserved_header", want: "invalid_header_config",
			server: nativeMCPServerConfig{EnvHeaderRefs: map[string]string{"Host": "MCP_HEADER_OK"}},
		},
		{
			name: "duplicate_canonical_header", want: "invalid_header_config",
			server: nativeMCPServerConfig{EnvHeaderRefs: map[string]string{
				"X-Token": "MCP_HEADER_OK", "x-token": "MCP_HEADER_OK",
			}},
		},
		{
			name: "missing_header_secret", want: "missing_secret",
			server: nativeMCPServerConfig{EnvHeaderRefs: map[string]string{"X-Token": "MCP_HEADER_EMPTY"}},
		},
		{
			name: "invalid_header_secret", want: "invalid_secret",
			server: nativeMCPServerConfig{EnvHeaderRefs: map[string]string{"X-Token": "MCP_HEADER_INVALID"}},
		},
		{
			name: "missing_bearer_secret", want: "missing_secret",
			server: nativeMCPServerConfig{SecretRef: "MCP_HEADER_EMPTY"},
		},
		{
			name: "bearer_conflicts_with_authorization", want: "invalid_header_config",
			server: nativeMCPServerConfig{
				SecretRef:     "MCP_SECRET_OK",
				EnvHeaderRefs: map[string]string{"Authorization": "MCP_HEADER_OK"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := nativeMCPHeaders(test.server)
			requireNativeMCPError(t, err, test.want)
		})
	}

	headers, err := nativeMCPHeaders(nativeMCPServerConfig{
		SecretRef:     "MCP_SECRET_OK",
		EnvHeaderRefs: map[string]string{"X-Test-Auth": "MCP_HEADER_OK"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if headers.Get("Authorization") != "Bearer secret-value" || headers.Get("X-Test-Auth") != "header-value" {
		t.Fatalf("valid environment headers were not constructed")
	}
}

func TestNativeMCPAuthorizationIsRevalidated(t *testing.T) {
	base := nativeMCPServerConfig{
		Enabled: true, Transport: "http", ApprovalMode: "confirm",
		AllowedAuthorities: []string{"member", "admin"}, AllowedTools: []string{"query-docs"},
	}
	tests := []struct {
		name      string
		server    nativeMCPServerConfig
		authority string
		approved  bool
		tool      string
		want      string
	}{
		{name: "confirmed", server: base, authority: "member", approved: true, tool: "query-docs"},
		{name: "confirmation_missing", server: base, authority: "member", tool: "query-docs", want: "approval_required"},
		{name: "authority_changed", server: base, authority: "guest", approved: true, tool: "query-docs", want: "authority_mismatch"},
		{name: "tool_removed", server: base, authority: "member", approved: true, tool: "hidden", want: "tool_not_allowed"},
		{name: "disabled", server: withNativeMCPEnabled(base, false), authority: "member", approved: true, tool: "query-docs", want: "server_unavailable"},
		{name: "stdio", server: withNativeMCPTransport(base, "stdio"), authority: "member", approved: true, tool: "query-docs"},
		{name: "transport_changed", server: withNativeMCPTransport(base, "pipe"), authority: "member", approved: true, tool: "query-docs", want: "server_unavailable"},
		{name: "member_auto_forbidden", server: withNativeMCPApproval(base, "auto"), authority: "member", approved: true, tool: "query-docs", want: "approval_required"},
		{name: "member_admin_only", server: withNativeMCPApproval(base, "admin_only"), authority: "member", approved: true, tool: "query-docs", want: "authority_mismatch"},
		{name: "invalid_mode", server: withNativeMCPApproval(base, "unknown"), authority: "admin", approved: true, tool: "query-docs", want: "invalid_approval_mode"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := authorizeNativeMCP(test.server, test.authority, test.approved, test.tool)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			requireNativeMCPError(t, err, test.want)
		})
	}
}

func TestNativeMCPCallReloadsAllowList(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	store := newNativeMCPConfigStore(t, nativeMCPTestConfig{
		Endpoint: endpointUsingLocalhost(t, server.URL),
	})
	defer store.Close()
	config, _ := json.Marshal(map[string]any{
		"allowedTools":       []string{"different-tool"},
		"allowedAuthorities": []string{"member", "admin"},
		"approvalMode":       "confirm", "timeoutSeconds": 5,
	})
	if _, err := store.db.Exec(
		"UPDATE mcp_servers SET config_json = ? WHERE id = 'context7-docs'", string(config),
	); err != nil {
		t.Fatal(err)
	}
	runtime := &AgentRuntime{configStore: store, client: server.Client()}
	_, err := runtime.callNativeMCPTool(context.Background(), mcpBridgeRoute{
		ServerID: "context7-docs", ToolName: "query-docs", Authority: "member",
		Approved: true, TimeoutSeconds: 5,
	}, map[string]any{})
	requireNativeMCPError(t, err, "tool_not_allowed")
	if requests.Load() != 0 {
		t.Fatalf("unauthorized call reached MCP server %d times", requests.Load())
	}
}

type sequenceNativeMCPResolver struct {
	mu      sync.Mutex
	answers [][]net.IPAddr
	calls   int
}

func (r *sequenceNativeMCPResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	index := r.calls
	r.calls++
	if index >= len(r.answers) {
		index = len(r.answers) - 1
	}
	return r.answers[index], nil
}

func (r *sequenceNativeMCPResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func newNativeMCPTestClient(t *testing.T, server *httptest.Server, timeout time.Duration) *nativeMCPClient {
	t.Helper()
	client, err := newNativeMCPClient(
		context.Background(), nativeMCPTestHTTPClient(t, server),
		nativeMCPServerConfig{Endpoint: endpointUsingLocalhost(t, server.URL)},
		timeout, net.DefaultResolver,
	)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func nativeMCPTestHTTPClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	transport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatal("test server transport is not *http.Transport")
	}
	transport = transport.Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = new(tls.Config)
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.InsecureSkipVerify = true // Test-only certificate.
	return &http.Client{Transport: transport}
}

func requireNativeMCPError(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %s", want)
	}
	if got := nativeMCPErrorCode(err); got != want {
		t.Fatalf("error code = %s, want %s: %v", got, want, err)
	}
}

func withNativeMCPEnabled(server nativeMCPServerConfig, enabled bool) nativeMCPServerConfig {
	server.Enabled = enabled
	return server
}

func withNativeMCPTransport(server nativeMCPServerConfig, transport string) nativeMCPServerConfig {
	server.Transport = transport
	return server
}

func withNativeMCPApproval(server nativeMCPServerConfig, approval string) nativeMCPServerConfig {
	server.ApprovalMode = approval
	return server
}
