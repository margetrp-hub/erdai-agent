package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestNativeMCPLegacySSETransportRoundTrip(t *testing.T) {
	var streamMu sync.Mutex
	var stream http.ResponseWriter
	var flush http.Flusher
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			streamMu.Lock()
			stream, flush = w, w.(http.Flusher)
			_, _ = w.Write([]byte("event: endpoint\ndata: /message\n\n"))
			flush.Flush()
			streamMu.Unlock()
			<-r.Context().Done()
			return
		}
		var request struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{}}
		switch request.Method {
		case "initialize":
			response["result"] = map[string]any{"protocolVersion": nativeMCPProtocolVersion, "serverInfo": map[string]any{"name": "sse-test"}}
		case "tools/list":
			response["result"] = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "echo"}}}
		case "tools/call":
			response["result"] = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		}
		encoded, _ := json.Marshal(response)
		streamMu.Lock()
		if stream != nil {
			_, _ = stream.Write([]byte("event: message\ndata: " + string(encoded) + "\n\n"))
			flush.Flush()
		}
		streamMu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	endpoint, _ := url.Parse(server.URL + "/sse")
	client := &nativeMCPLegacySSEClient{
		streamEndpoint: endpoint, httpClient: server.Client(), headers: make(http.Header),
		events: make(chan []byte, 8), errors: make(chan error, 1), endpointReady: make(chan struct{}),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.listTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("SSE tools = %#v, err=%v", tools, err)
	}
	if _, err = client.callTool(ctx, "echo", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
}

func TestNativeMCPStdioTransportIsAllowlistedAndControlled(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config := nativeMCPServerConfig{
		Transport: "stdio", Command: executable,
		Args: []string{"-test.run=^TestNativeMCPStdioHelperProcess$", "--", "erdai-mcp-stdio-test-helper"},
	}
	t.Setenv("ERDAI_MCP_STDIO_ALLOWLIST", "")
	if _, err = newNativeMCPStdioClient(config); nativeMCPErrorCode(err) != "stdio_command_not_allowed" {
		t.Fatalf("empty allowlist accepted stdio command: %v", err)
	}
	t.Setenv("ERDAI_MCP_STDIO_ALLOWLIST", executable)
	client, err := newNativeMCPStdioClient(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if client.cmd != nil && client.cmd.ProcessState == nil {
			_ = client.Close()
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = client.connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.listTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("stdio tools = %#v, err=%v", tools, err)
	}
	result, err := client.callTool(ctx, "echo", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err = json.Unmarshal(result, &response); err != nil || len(response.Content) != 1 || response.Content[0].Type != "text" || response.Content[0].Text != "ok" {
		t.Fatalf("stdio tool result = %s, err=%v", result, err)
	}
	// Close deliberately kills the process; Windows reports an exit status,
	// while Unix reports a signal. Both must still be reaped by Wait.
	var exitErr *exec.ExitError
	if err = client.Close(); err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("stdio close failed: %v", err)
	}
	state := client.cmd.ProcessState
	if state == nil || state.Pid() != client.cmd.Process.Pid {
		t.Fatal("stdio subprocess did not return its own reaped state")
	}
	status, ok := state.Sys().(syscall.WaitStatus)
	if !ok || (!status.Exited() && !status.Signaled()) {
		t.Fatalf("stdio subprocess has no terminal wait status: %v", state)
	}
	if exitErr != nil && exitErr.ProcessState != state {
		t.Fatal("stdio close error does not describe the reaped subprocess")
	}
	if nativeMCPStdioCommandAllowed(filepath.Base(executable)) {
		t.Fatal("relative stdio command was accepted")
	}
	unlisted := filepath.Join(t.TempDir(), "not-allowlisted")
	if nativeMCPStdioCommandAllowed(unlisted) {
		t.Fatal("unallowlisted stdio command was accepted")
	}
	if _, err = newNativeMCPStdioClient(nativeMCPServerConfig{Transport: "stdio", Command: unlisted}); nativeMCPErrorCode(err) != "stdio_command_not_allowed" {
		t.Fatalf("unallowlisted client was created: %v", err)
	}
}

func TestNativeMCPStdioHelperProcess(t *testing.T) {
	if os.Args[len(os.Args)-1] != "erdai-mcp-stdio-test-helper" {
		return
	}
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for {
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&request); err != nil {
			if errors.Is(err, io.EOF) {
				os.Exit(0)
			}
			os.Exit(2)
		}
		if len(request.ID) == 0 {
			continue
		}
		var result any
		switch request.Method {
		case "initialize":
			result = map[string]any{"protocolVersion": nativeMCPProtocolVersion, "serverInfo": map[string]any{"name": "stdio-test"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo"}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "ok"}}}
		default:
			os.Exit(3)
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(4)
		}
	}
}
