package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	nativeMCPProtocolVersion = "2025-06-18"
	nativeMCPMaxResponse     = 2 * 1024 * 1024
	nativeMCPMaxPages        = 10
	nativeMCPMaxTools        = 1000
)

var nativeMCPBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("ff00::/8"),
}

type nativeMCPError struct {
	Code string
	Err  error
}

func (e *nativeMCPError) Error() string {
	if e.Err == nil {
		return e.Code
	}
	return e.Code + ": " + e.Err.Error()
}

func (e *nativeMCPError) Unwrap() error { return e.Err }

func nativeMCPFailure(code string, err error) error {
	return &nativeMCPError{Code: code, Err: err}
}

func nativeMCPErrorCode(err error) string {
	var target *nativeMCPError
	if errors.As(err, &target) {
		return target.Code
	}
	return "mcp_error"
}

type nativeMCPResolver interface {
	LookupIPAddr(context.Context, string) ([]net.IPAddr, error)
}

type nativeMCPServerConfig struct {
	ID                 string
	Name               string
	Transport          string
	Endpoint           string
	ToolPrefix         string
	Enabled            bool
	SecretRef          string
	EnvHeaderRefs      map[string]string
	AllowedTools       []string
	AllowedAuthorities []string
	ApprovalMode       string
	Timeout            time.Duration
	Command            string
	Args               []string
}

type nativeMCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type nativeMCPClient struct {
	endpoint        *url.URL
	httpClient      *http.Client
	headers         http.Header
	sessionID       string
	protocolVersion string
	serverInfo      map[string]any
	nextID          int64
}

type nativeMCPEnvelope struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code int `json:"code"`
	} `json:"error"`
}

func (s *coreConfigStore) nativeMCPServer(id string) (nativeMCPServerConfig, error) {
	if s == nil || s.db == nil {
		return nativeMCPServerConfig{}, nativeMCPFailure("config_unavailable", nil)
	}
	var server nativeMCPServerConfig
	var enabled int
	var configJSON string
	var argsJSON string
	err := s.db.QueryRow(`
		SELECT id, name, transport, endpoint, command, args_json, tool_prefix, enabled, config_json
		FROM mcp_servers WHERE id = ?
	`, strings.TrimSpace(id)).Scan(
		&server.ID, &server.Name, &server.Transport, &server.Endpoint, &server.Command, &argsJSON,
		&server.ToolPrefix, &enabled, &configJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nativeMCPServerConfig{}, nativeMCPFailure("server_not_found", nil)
	}
	if err != nil {
		return nativeMCPServerConfig{}, nativeMCPFailure("config_read_failed", err)
	}
	server.Enabled = enabled == 1
	server.Args = decodeJSONStringList(argsJSON)
	var config struct {
		SecretRef          string            `json:"secretRef"`
		EnvHeaderRefs      map[string]string `json:"envHeaderRefs"`
		AllowedTools       []string          `json:"allowedTools"`
		AllowedAuthorities []string          `json:"allowedAuthorities"`
		ApprovalMode       string            `json:"approvalMode"`
		TimeoutSeconds     int               `json:"timeoutSeconds"`
	}
	if err = json.Unmarshal([]byte(configJSON), &config); err != nil {
		return nativeMCPServerConfig{}, nativeMCPFailure("invalid_config", nil)
	}
	server.SecretRef = strings.TrimSpace(config.SecretRef)
	server.EnvHeaderRefs = config.EnvHeaderRefs
	server.AllowedTools = normalizedNativeMCPList(config.AllowedTools)
	server.AllowedAuthorities = normalizedNativeMCPList(config.AllowedAuthorities)
	server.ApprovalMode = strings.TrimSpace(config.ApprovalMode)
	if len(server.AllowedAuthorities) == 0 {
		server.AllowedAuthorities = []string{"admin"}
	}
	if server.ApprovalMode == "" {
		server.ApprovalMode = "admin_only"
	}
	if config.TimeoutSeconds == 0 {
		config.TimeoutSeconds = 30
	}
	if config.TimeoutSeconds < 1 || config.TimeoutSeconds > 300 {
		return nativeMCPServerConfig{}, nativeMCPFailure("invalid_timeout", nil)
	}
	server.Timeout = time.Duration(config.TimeoutSeconds) * time.Second
	return server, nil
}

func normalizedNativeMCPList(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func authorizeNativeMCP(server nativeMCPServerConfig, authority string, approved bool, toolName string) error {
	if !server.Enabled || (server.Transport != "http" && server.Transport != "sse" && server.Transport != "stdio") {
		return nativeMCPFailure("server_unavailable", nil)
	}
	if !nativeMCPListContains(server.AllowedAuthorities, authority) {
		return nativeMCPFailure("authority_mismatch", nil)
	}
	switch server.ApprovalMode {
	case "auto":
		if authority == "member" {
			return nativeMCPFailure("approval_required", nil)
		}
	case "confirm":
		if !approved {
			return nativeMCPFailure("approval_required", nil)
		}
	case "admin_only":
		if authority != "admin" {
			return nativeMCPFailure("authority_mismatch", nil)
		}
	default:
		return nativeMCPFailure("invalid_approval_mode", nil)
	}
	if toolName != "" && !nativeMCPListContains(server.AllowedTools, toolName) {
		return nativeMCPFailure("tool_not_allowed", nil)
	}
	return nil
}

func nativeMCPListContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func effectiveNativeMCPTimeout(server nativeMCPServerConfig, policy runtimeMCPServer) time.Duration {
	timeout := server.Timeout
	policyTimeout := time.Duration(policy.TimeoutSeconds) * time.Second
	if policyTimeout > 0 && (timeout <= 0 || policyTimeout < timeout) {
		timeout = policyTimeout
	}
	if timeout <= 0 || timeout > 300*time.Second {
		timeout = 30 * time.Second
	}
	return timeout
}

func nativeMCPHeaders(server nativeMCPServerConfig) (http.Header, error) {
	if len(server.EnvHeaderRefs) > 32 {
		return nil, nativeMCPFailure("invalid_header_config", nil)
	}
	headers := make(http.Header, len(server.EnvHeaderRefs)+1)
	for name, reference := range server.EnvHeaderRefs {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		reference = strings.TrimSpace(reference)
		if !validNativeMCPHeaderName(canonical) || reservedNativeMCPHeader(canonical) ||
			!validNativeMCPEnvironmentReference(reference) {
			return nil, nativeMCPFailure("invalid_header_config", nil)
		}
		if _, exists := headers[canonical]; exists {
			return nil, nativeMCPFailure("invalid_header_config", nil)
		}
		value, ok := os.LookupEnv(reference)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, nativeMCPFailure("missing_secret", nil)
		}
		if len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, nativeMCPFailure("invalid_secret", nil)
		}
		headers.Set(canonical, value)
	}
	if server.SecretRef != "" {
		if !validNativeMCPEnvironmentReference(server.SecretRef) || headers.Get("Authorization") != "" {
			return nil, nativeMCPFailure("invalid_header_config", nil)
		}
		value, ok := os.LookupEnv(server.SecretRef)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, nativeMCPFailure("missing_secret", nil)
		}
		if len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
			return nil, nativeMCPFailure("invalid_secret", nil)
		}
		headers.Set("Authorization", "Bearer "+strings.TrimSpace(value))
	}
	return headers, nil
}

func validNativeMCPEnvironmentReference(value string) bool {
	if len(value) < 3 || len(value) > 120 || value[0] < 'A' || value[0] > 'Z' {
		return false
	}
	for _, char := range value[1:] {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return false
		}
	}
	return true
}

func validNativeMCPHeaderName(value string) bool {
	if value == "" {
		return false
	}
	const separators = "()<>@,;:\\\"/[]?={} \t"
	for _, char := range value {
		if char <= 31 || char >= 127 || strings.ContainsRune(separators, char) {
			return false
		}
	}
	return true
}

func reservedNativeMCPHeader(value string) bool {
	switch strings.ToLower(value) {
	case "accept", "connection", "content-length", "content-type", "host",
		"mcp-protocol-version", "mcp-session-id", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "x-erdai-admin-token":
		return true
	default:
		return false
	}
}

func validateNativeMCPEndpoint(ctx context.Context, raw string, resolver nativeMCPResolver) (*url.URL, error) {
	endpoint, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || endpoint.Host == "" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.Fragment != "" {
		return nil, nativeMCPFailure("invalid_endpoint", nil)
	}
	host := strings.TrimSuffix(strings.ToLower(endpoint.Hostname()), ".")
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && host == "localhost") {
		return nil, nativeMCPFailure("insecure_endpoint", nil)
	}
	if _, err = resolveNativeMCPHost(ctx, resolver, host); err != nil {
		return nil, err
	}
	return endpoint, nil
}

func resolveNativeMCPHost(ctx context.Context, resolver nativeMCPResolver, host string) ([]net.IPAddr, error) {
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	var addresses []net.IPAddr
	if parsed := net.ParseIP(host); parsed != nil {
		addresses = []net.IPAddr{{IP: parsed}}
	} else {
		var err error
		addresses, err = resolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, nativeMCPFailure("dns_failed", err)
		}
	}
	if len(addresses) == 0 {
		return nil, nativeMCPFailure("dns_failed", nil)
	}
	for _, address := range addresses {
		if !allowedNativeMCPAddress(host, address.IP) {
			return nil, nativeMCPFailure("blocked_address", nil)
		}
	}
	return addresses, nil
}

func allowedNativeMCPAddress(host string, ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if host == "localhost" {
		return address.IsLoopback()
	}
	if !address.IsGlobalUnicast() {
		return false
	}
	for _, prefix := range nativeMCPBlockedPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

func nativeMCPDialContext(resolver nativeMCPResolver) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, nativeMCPFailure("invalid_endpoint", err)
		}
		addresses, err := resolveNativeMCPHost(ctx, resolver, host)
		if err != nil {
			return nil, err
		}
		dialer := &net.Dialer{KeepAlive: 30 * time.Second}
		var lastErr error
		for _, candidate := range addresses {
			connection, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(candidate.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			lastErr = dialErr
		}
		return nil, nativeMCPFailure("connection_failed", lastErr)
	}
}

func newNativeMCPClient(
	ctx context.Context,
	base *http.Client,
	server nativeMCPServerConfig,
	timeout time.Duration,
	resolver nativeMCPResolver,
) (*nativeMCPClient, error) {
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	endpoint, err := validateNativeMCPEndpoint(ctx, server.Endpoint, resolver)
	if err != nil {
		return nil, err
	}
	headers, err := nativeMCPHeaders(server)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if base != nil && base.Transport != nil {
		configured, ok := base.Transport.(*http.Transport)
		if !ok {
			return nil, nativeMCPFailure("unsupported_http_transport", nil)
		}
		transport = configured.Clone()
	}
	transport.Proxy = nil
	transport.DialContext = nativeMCPDialContext(resolver)
	transport.DialTLSContext = nil
	transport.ResponseHeaderTimeout = timeout
	if transport.TLSHandshakeTimeout <= 0 || transport.TLSHandshakeTimeout > timeout {
		transport.TLSHandshakeTimeout = timeout
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return nativeMCPFailure("redirect_blocked", nil)
		},
	}
	return &nativeMCPClient{
		endpoint: endpoint, httpClient: client, headers: headers,
		protocolVersion: nativeMCPProtocolVersion,
	}, nil
}

func (c *nativeMCPClient) connect(ctx context.Context) error {
	result, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": nativeMCPProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name": "erdai-agent-core", "version": erdaiRuntimeVersion,
		},
	})
	if err != nil {
		return err
	}
	var initialized struct {
		ProtocolVersion string         `json:"protocolVersion"`
		ServerInfo      map[string]any `json:"serverInfo"`
	}
	if json.Unmarshal(result, &initialized) != nil || initialized.ProtocolVersion != nativeMCPProtocolVersion {
		return nativeMCPFailure("unsupported_protocol", nil)
	}
	c.protocolVersion = initialized.ProtocolVersion
	c.serverInfo = initialized.ServerInfo
	return c.notify(ctx, "notifications/initialized", nil)
}

func (c *nativeMCPClient) listTools(ctx context.Context) ([]nativeMCPTool, error) {
	tools := make([]nativeMCPTool, 0)
	cursor := ""
	for page := 0; page < nativeMCPMaxPages; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		result, err := c.rpc(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var response struct {
			Tools      []nativeMCPTool `json:"tools"`
			NextCursor string          `json:"nextCursor"`
		}
		if json.Unmarshal(result, &response) != nil || response.Tools == nil {
			return nil, nativeMCPFailure("invalid_response", nil)
		}
		for _, tool := range response.Tools {
			tool.Name = strings.TrimSpace(tool.Name)
			if tool.Name == "" {
				continue
			}
			if tool.InputSchema == nil {
				tool.InputSchema = map[string]any{"type": "object"}
			}
			tools = append(tools, tool)
			if len(tools) >= nativeMCPMaxTools {
				return tools, nil
			}
		}
		cursor = strings.TrimSpace(response.NextCursor)
		if cursor == "" {
			return tools, nil
		}
	}
	return nil, nativeMCPFailure("pagination_limit", nil)
}

func (c *nativeMCPClient) callTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	return c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func (c *nativeMCPClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := c.nextID
	body := map[string]any{"jsonrpc": "2.0", "id": id, "method": method}
	if params != nil {
		body["params"] = params
	}
	return c.post(ctx, body, strconv.FormatInt(id, 10), false)
}

func (c *nativeMCPClient) notify(ctx context.Context, method string, params any) error {
	body := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		body["params"] = params
	}
	_, err := c.post(ctx, body, "", true)
	return err
}

func (c *nativeMCPClient) post(
	ctx context.Context,
	payload map[string]any,
	expectedID string,
	notification bool,
) (json.RawMessage, error) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, nativeMCPFailure("request_encode_failed", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return nil, nativeMCPFailure("invalid_endpoint", err)
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Accept", "application/json, text/event-stream")
	request.Header.Set("Content-Type", "application/json")
	if c.sessionID != "" {
		request.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	if c.protocolVersion != "" {
		request.Header.Set("Mcp-Protocol-Version", c.protocolVersion)
	}
	response, err := c.httpClient.Do(request)
	if err != nil {
		var networkError net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
			(errors.As(err, &networkError) && networkError.Timeout()) {
			return nil, nativeMCPFailure("timeout", err)
		}
		var target *nativeMCPError
		if errors.As(err, &target) {
			return nil, target
		}
		return nil, nativeMCPFailure("connection_failed", err)
	}
	defer response.Body.Close()
	if sessionID := strings.TrimSpace(response.Header.Get("Mcp-Session-Id")); sessionID != "" {
		if c.sessionID != "" && c.sessionID != sessionID {
			return nil, nativeMCPFailure("invalid_session", nil)
		}
		c.sessionID = sessionID
	}
	if response.ContentLength > nativeMCPMaxResponse {
		return nil, nativeMCPFailure("response_too_large", nil)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, nativeMCPMaxResponse+1))
	if err != nil {
		return nil, nativeMCPFailure("response_read_failed", err)
	}
	if len(body) > nativeMCPMaxResponse {
		return nil, nativeMCPFailure("response_too_large", nil)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, nativeMCPFailure("upstream_http_error", fmt.Errorf("HTTP %d", response.StatusCode))
	}
	if notification {
		return nil, nil
	}
	if response.StatusCode == http.StatusAccepted || response.StatusCode == http.StatusNoContent || len(bytes.TrimSpace(body)) == 0 {
		return nil, nativeMCPFailure("invalid_response", nil)
	}
	return decodeNativeMCPResponse(body, response.Header.Get("Content-Type"), expectedID)
}

func decodeNativeMCPResponse(body []byte, contentType, expectedID string) (json.RawMessage, error) {
	candidates := [][]byte{body}
	if strings.Contains(strings.ToLower(contentType), "text/event-stream") {
		candidates = nativeMCPSSEData(body)
	}
	for _, candidate := range candidates {
		var envelope nativeMCPEnvelope
		if json.Unmarshal(candidate, &envelope) != nil || envelope.JSONRPC != "2.0" {
			continue
		}
		if string(bytes.TrimSpace(envelope.ID)) != expectedID {
			continue
		}
		if envelope.Error != nil {
			return nil, nativeMCPFailure("rpc_error", fmt.Errorf("code %d", envelope.Error.Code))
		}
		if envelope.Result == nil {
			return nil, nativeMCPFailure("invalid_response", nil)
		}
		return envelope.Result, nil
	}
	return nil, nativeMCPFailure("invalid_response", nil)
}

func nativeMCPSSEData(body []byte) [][]byte {
	normalized := strings.ReplaceAll(string(body), "\r\n", "\n")
	blocks := strings.Split(normalized, "\n\n")
	result := make([][]byte, 0, len(blocks))
	for _, block := range blocks {
		lines := strings.Split(block, "\n")
		data := make([]string, 0, len(lines))
		for _, line := range lines {
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if len(data) > 0 {
			result = append(result, []byte(strings.Join(data, "\n")))
		}
	}
	return result
}

func (a *AgentRuntime) discoverNativeMCPTools(
	ctx context.Context,
	policy runtimeMCPServer,
	authority string,
	approved bool,
) ([]nativeMCPTool, error) {
	if a.configStore == nil {
		return nil, nativeMCPFailure("config_unavailable", nil)
	}
	server, err := a.configStore.nativeMCPServer(policy.ID)
	if err != nil {
		return nil, err
	}
	if err = authorizeNativeMCP(server, authority, approved, ""); err != nil {
		return nil, err
	}
	timeout := effectiveNativeMCPTimeout(server, policy)
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := newNativeMCPTransportClient(operationContext, a.client, server, timeout, net.DefaultResolver)
	if err != nil {
		a.configStore.recordNativeMCPAudit("discover_failed", "mcp_server", server.ID, nativeMCPErrorCode(err))
		return nil, err
	}
	defer client.Close()
	if err = client.connect(operationContext); err != nil {
		a.configStore.recordNativeMCPAudit("discover_failed", "mcp_server", server.ID, nativeMCPErrorCode(err))
		return nil, err
	}
	tools, err := client.listTools(operationContext)
	if err != nil {
		a.configStore.recordNativeMCPAudit("discover_failed", "mcp_server", server.ID, nativeMCPErrorCode(err))
		return nil, err
	}
	allowed := make([]nativeMCPTool, 0, len(tools))
	for _, tool := range tools {
		if nativeMCPListContains(server.AllowedTools, tool.Name) &&
			nativeMCPListContains(policy.AllowedTools, tool.Name) {
			allowed = append(allowed, tool)
		}
	}
	a.configStore.recordNativeMCPAudit("discover", "mcp_server", server.ID, "")
	return allowed, nil
}

func (a *AgentRuntime) callNativeMCPTool(
	ctx context.Context,
	route mcpBridgeRoute,
	arguments map[string]any,
) (json.RawMessage, error) {
	if a.configStore == nil {
		return nil, nativeMCPFailure("config_unavailable", nil)
	}
	server, err := a.configStore.nativeMCPServer(route.ServerID)
	if err != nil {
		return nil, err
	}
	if err = authorizeNativeMCP(server, route.Authority, route.Approved, route.ToolName); err != nil {
		a.configStore.recordNativeMCPAudit("call_failed", "mcp_tool", server.ID+":"+route.ToolName, nativeMCPErrorCode(err))
		return nil, err
	}
	timeout := effectiveNativeMCPTimeout(server, runtimeMCPServer{
		TimeoutSeconds: route.TimeoutSeconds,
	})
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := newNativeMCPTransportClient(operationContext, a.client, server, timeout, net.DefaultResolver)
	if client != nil {
		defer client.Close()
	}
	if err == nil {
		err = client.connect(operationContext)
	}
	var result json.RawMessage
	if err == nil {
		result, err = client.callTool(operationContext, route.ToolName, arguments)
	}
	if err != nil {
		a.configStore.recordNativeMCPAudit("call_failed", "mcp_tool", server.ID+":"+route.ToolName, nativeMCPErrorCode(err))
		return nil, err
	}
	a.configStore.recordNativeMCPAudit("call_succeeded", "mcp_tool", server.ID+":"+route.ToolName, "")
	return result, nil
}

func (s *coreConfigStore) recordNativeMCPAudit(action, targetType, targetID, code string) {
	if s == nil || s.db == nil {
		return
	}
	details, _ := json.Marshal(map[string]string{"errorCode": code})
	_, _ = s.db.Exec(`
		INSERT INTO audit_events (actor, action, target_type, target_id, details_json, created_at)
		VALUES ('runtime', ?, ?, ?, ?, ?)
	`, action, targetType, targetID, string(details), time.Now().UTC().Format(time.RFC3339Nano))
}
