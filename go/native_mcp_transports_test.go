package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
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
	script := `while IFS= read -r line; do case "$line" in *initialize*) printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":"2025-06-18","serverInfo":{"name":"stdio-test"}}}' ;; *tools/list*) printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"tools":[{"name":"echo"}]}}' ;; *tools/call*) printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"content":[{"type":"text","text":"ok"}]}}' ;; esac; done`
	t.Setenv("ERDAI_MCP_STDIO_ALLOWLIST", "/bin/sh")
	client, err := newNativeMCPStdioClient(nativeMCPServerConfig{Transport: "stdio", Command: "/bin/sh", Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = client.connect(ctx); err != nil {
		t.Fatal(err)
	}
	tools, err := client.listTools(ctx)
	if err != nil || len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("stdio tools = %#v, err=%v", tools, err)
	}
	if _, err = client.callTool(ctx, "echo", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	if err = client.Close(); err != nil && !strings.Contains(err.Error(), "signal") {
		t.Fatal(err)
	}
	if nativeMCPStdioCommandAllowed("/tmp/not-allowlisted") {
		t.Fatal("unallowlisted stdio command was accepted")
	}
}
