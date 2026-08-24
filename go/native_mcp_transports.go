package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type nativeMCPTransportClient interface {
	connect(context.Context) error
	listTools(context.Context) ([]nativeMCPTool, error)
	callTool(context.Context, string, map[string]any) (json.RawMessage, error)
	protocolDetails() (string, map[string]any)
	Close() error
}

func (c *nativeMCPClient) Close() error { return nil }

func (c *nativeMCPClient) protocolDetails() (string, map[string]any) {
	return c.protocolVersion, c.serverInfo
}

func newNativeMCPTransportClient(
	ctx context.Context,
	base *http.Client,
	server nativeMCPServerConfig,
	timeout time.Duration,
	resolver nativeMCPResolver,
) (nativeMCPTransportClient, error) {
	switch server.Transport {
	case "http":
		return newNativeMCPClient(ctx, base, server, timeout, resolver)
	case "sse":
		httpClient, err := newNativeMCPClient(ctx, base, server, timeout, resolver)
		if err != nil {
			return nil, err
		}
		return &nativeMCPLegacySSEClient{
			streamEndpoint: httpClient.endpoint, httpClient: httpClient.httpClient,
			headers: httpClient.headers, events: make(chan []byte, 32),
			errors: make(chan error, 1), endpointReady: make(chan struct{}),
		}, nil
	case "stdio":
		return newNativeMCPStdioClient(server)
	default:
		return nil, nativeMCPFailure("unsupported_transport", nil)
	}
}

func nativeMCPStdioCommandAllowed(command string) bool {
	command = filepath.Clean(strings.TrimSpace(command))
	if command == "." || !filepath.IsAbs(command) {
		return false
	}
	for _, allowed := range filepath.SplitList(os.Getenv("ERDAI_MCP_STDIO_ALLOWLIST")) {
		if strings.EqualFold(filepath.Clean(strings.TrimSpace(allowed)), command) {
			return true
		}
	}
	return false
}

type nativeMCPStdioClient struct {
	server          nativeMCPServerConfig
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	scan            *bufio.Scanner
	protocolVersion string
	serverInfo      map[string]any
	nextID          int64
	mu              sync.Mutex
}

func newNativeMCPStdioClient(server nativeMCPServerConfig) (*nativeMCPStdioClient, error) {
	if !nativeMCPStdioCommandAllowed(server.Command) {
		return nil, nativeMCPFailure("stdio_command_not_allowed", nil)
	}
	return &nativeMCPStdioClient{server: server}, nil
}

func (c *nativeMCPStdioClient) start(ctx context.Context) error {
	if c.cmd != nil {
		return nil
	}
	c.cmd = exec.CommandContext(ctx, c.server.Command, c.server.Args...)
	environment := []string{}
	for _, name := range []string{
		"PATH", "HOME", "LANG", "LC_ALL", "TMPDIR", "TZ", "ERDAI_CONFIG_DATABASE",
	} {
		if value, found := os.LookupEnv(name); found {
			environment = append(environment, name+"="+value)
		}
	}
	if c.server.SecretRef != "" {
		value := getenv(c.server.SecretRef)
		if value == "" {
			return nativeMCPFailure("missing_secret", nil)
		}
		environment = append(environment, c.server.SecretRef+"="+value)
	}
	c.cmd.Env = environment
	stdout, err := c.cmd.StdoutPipe()
	if err != nil {
		return nativeMCPFailure("stdio_start_failed", err)
	}
	c.stdin, err = c.cmd.StdinPipe()
	if err != nil {
		return nativeMCPFailure("stdio_start_failed", err)
	}
	c.cmd.Stderr = io.Discard
	if err = c.cmd.Start(); err != nil {
		return nativeMCPFailure("stdio_start_failed", err)
	}
	c.scan = bufio.NewScanner(stdout)
	c.scan.Buffer(make([]byte, 64*1024), nativeMCPMaxResponse)
	return nil
}

func (c *nativeMCPStdioClient) connect(ctx context.Context) error {
	if err := c.start(ctx); err != nil {
		return err
	}
	result, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": nativeMCPProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "erdai-agent-core", "version": erdaiRuntimeVersion},
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

func (c *nativeMCPStdioClient) protocolDetails() (string, map[string]any) {
	return c.protocolVersion, c.serverInfo
}

func (c *nativeMCPStdioClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	payload := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.write(payload); err != nil {
		return nil, err
	}
	for {
		if ctx.Err() != nil {
			return nil, nativeMCPFailure("timeout", ctx.Err())
		}
		if !c.scan.Scan() {
			return nil, nativeMCPFailure("stdio_closed", c.scan.Err())
		}
		line := bytes.TrimSpace(c.scan.Bytes())
		if len(line) == 0 {
			continue
		}
		result, err := decodeNativeMCPResponse(line, "application/json", id)
		if nativeMCPErrorCode(err) == "invalid_response" {
			continue
		}
		return result, err
	}
}

func (c *nativeMCPStdioClient) notify(_ context.Context, method string, params any) error {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		payload["params"] = params
	}
	return c.write(payload)
}

func (c *nativeMCPStdioClient) write(payload map[string]any) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nativeMCPFailure("request_encode_failed", err)
	}
	encoded = append(encoded, '\n')
	if _, err = c.stdin.Write(encoded); err != nil {
		return nativeMCPFailure("stdio_write_failed", err)
	}
	return nil
}

func (c *nativeMCPStdioClient) listTools(ctx context.Context) ([]nativeMCPTool, error) {
	result, err := c.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Tools []nativeMCPTool `json:"tools"`
	}
	if json.Unmarshal(result, &response) != nil || response.Tools == nil {
		return nil, nativeMCPFailure("invalid_response", nil)
	}
	return response.Tools, nil
}

func (c *nativeMCPStdioClient) callTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	return c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func (c *nativeMCPStdioClient) Close() error {
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	_ = c.cmd.Process.Kill()
	return c.cmd.Wait()
}

type nativeMCPLegacySSEClient struct {
	streamEndpoint  *url.URL
	postEndpoint    *url.URL
	httpClient      *http.Client
	headers         http.Header
	protocolVersion string
	serverInfo      map[string]any
	response        *http.Response
	events          chan []byte
	errors          chan error
	endpointReady   chan struct{}
	readyOnce       sync.Once
	nextID          int64
}

func (c *nativeMCPLegacySSEClient) connect(ctx context.Context) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.streamEndpoint.String(), nil)
	if err != nil {
		return nativeMCPFailure("invalid_endpoint", err)
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Accept", "text/event-stream")
	c.response, err = c.httpClient.Do(request)
	if err != nil {
		return nativeMCPFailure("connection_failed", err)
	}
	if c.response.StatusCode < 200 || c.response.StatusCode >= 300 ||
		!strings.Contains(strings.ToLower(c.response.Header.Get("Content-Type")), "text/event-stream") {
		c.response.Body.Close()
		return nativeMCPFailure("invalid_sse_response", nil)
	}
	go c.readEvents()
	select {
	case <-ctx.Done():
		return nativeMCPFailure("timeout", ctx.Err())
	case err = <-c.errors:
		return err
	case <-c.endpointReady:
	}
	result, err := c.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": nativeMCPProtocolVersion, "capabilities": map[string]any{},
		"clientInfo": map[string]string{"name": "erdai-agent-core", "version": erdaiRuntimeVersion},
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

func (c *nativeMCPLegacySSEClient) protocolDetails() (string, map[string]any) {
	return c.protocolVersion, c.serverInfo
}

func (c *nativeMCPLegacySSEClient) readEvents() {
	scanner := bufio.NewScanner(c.response.Body)
	scanner.Buffer(make([]byte, 64*1024), nativeMCPMaxResponse)
	eventType := ""
	data := []string{}
	flush := func() {
		joined := strings.Join(data, "\n")
		if eventType == "endpoint" {
			if endpoint, err := c.resolvePostEndpoint(joined); err == nil {
				c.postEndpoint = endpoint
				c.readyOnce.Do(func() { close(c.endpointReady) })
			} else {
				select {
				case c.errors <- err:
				default:
				}
			}
		} else if joined != "" {
			select {
			case c.events <- []byte(joined):
			default:
			}
		}
		eventType, data = "", data[:0]
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "event:") {
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		}
		if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	flush()
	select {
	case c.errors <- nativeMCPFailure("sse_closed", scanner.Err()):
	default:
	}
}

func (c *nativeMCPLegacySSEClient) resolvePostEndpoint(raw string) (*url.URL, error) {
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, nativeMCPFailure("invalid_sse_endpoint", err)
	}
	endpoint := c.streamEndpoint.ResolveReference(reference)
	if endpoint.Scheme != c.streamEndpoint.Scheme || !strings.EqualFold(endpoint.Host, c.streamEndpoint.Host) || endpoint.User != nil {
		return nil, nativeMCPFailure("invalid_sse_endpoint", nil)
	}
	return endpoint, nil
}

func (c *nativeMCPLegacySSEClient) post(ctx context.Context, payload map[string]any) error {
	encoded, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.postEndpoint.String(), bytes.NewReader(encoded))
	if err != nil {
		return nativeMCPFailure("invalid_sse_endpoint", err)
	}
	for name, values := range c.headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nativeMCPFailure("connection_failed", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64*1024))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nativeMCPFailure("upstream_http_error", fmt.Errorf("HTTP %d", response.StatusCode))
	}
	return nil
}

func (c *nativeMCPLegacySSEClient) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	payload := map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method}
	if params != nil {
		payload["params"] = params
	}
	if err := c.post(ctx, payload); err != nil {
		return nil, err
	}
	for {
		select {
		case <-ctx.Done():
			return nil, nativeMCPFailure("timeout", ctx.Err())
		case err := <-c.errors:
			return nil, err
		case event := <-c.events:
			result, err := decodeNativeMCPResponse(event, "application/json", id)
			if nativeMCPErrorCode(err) == "invalid_response" {
				continue
			}
			return result, err
		}
	}
}

func (c *nativeMCPLegacySSEClient) notify(ctx context.Context, method string, params any) error {
	payload := map[string]any{"jsonrpc": "2.0", "method": method}
	if params != nil {
		payload["params"] = params
	}
	return c.post(ctx, payload)
}

func (c *nativeMCPLegacySSEClient) listTools(ctx context.Context) ([]nativeMCPTool, error) {
	result, err := c.rpc(ctx, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	var response struct {
		Tools []nativeMCPTool `json:"tools"`
	}
	if json.Unmarshal(result, &response) != nil || response.Tools == nil {
		return nil, nativeMCPFailure("invalid_response", nil)
	}
	return response.Tools, nil
}

func (c *nativeMCPLegacySSEClient) callTool(ctx context.Context, name string, arguments map[string]any) (json.RawMessage, error) {
	return c.rpc(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments})
}

func (c *nativeMCPLegacySSEClient) Close() error {
	if c.response != nil {
		return c.response.Body.Close()
	}
	return nil
}
