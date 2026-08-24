package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocalMCPStdioRoundTrip(t *testing.T) {
	input := strings.NewReader(
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"+
			`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`+"\n"+
			`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"time_now","arguments":{}}}`+"\n",
	)
	var output bytes.Buffer
	if err := serveLocalMCPStdio(input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var initialize, list, call map[string]any
	if err := decoder.Decode(&initialize); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&list); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&call); err != nil {
		t.Fatal(err)
	}
	if initialize["id"].(float64) != 1 || initialize["result"] == nil {
		t.Fatalf("initialize = %#v", initialize)
	}
	result, ok := list["result"].(map[string]any)
	if !ok || len(result["tools"].([]any)) != 3 {
		t.Fatalf("tools/list = %#v", list)
	}
	if call["id"].(float64) != 3 || call["result"] == nil {
		t.Fatalf("tools/call = %#v", call)
	}
}

func TestLocalMCPSSERoundTrip(t *testing.T) {
	hub := newLocalMCPHub(nil)
	server := httptest.NewServer(httpHandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !hub.handleHTTP(w, r, cleanPath(r.URL.Path), true) {
			httpNotFound(w)
		}
	}))
	defer server.Close()
	endpoint := strings.Replace(server.URL, "127.0.0.1", "localhost", 1) + "/internal/mcp/sse"
	client, err := newNativeMCPTransportClient(context.Background(), nil, nativeMCPServerConfig{
		ID: "local", Name: "local", Transport: "sse", Endpoint: endpoint,
		Enabled: true, AllowedAuthorities: []string{"admin"}, ApprovalMode: "auto", Timeout: 5 * time.Second,
	}, 5*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err = client.connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	tools, err := client.listTools(context.Background())
	if err != nil || len(tools) != 3 {
		t.Fatalf("tools = %#v, err=%v", tools, err)
	}
	result, err := client.callTool(context.Background(), "time_now", map[string]any{})
	if err != nil || !bytes.Contains(result, []byte("time")) {
		t.Fatalf("call = %s, err=%v", result, err)
	}
}

type httpHandlerFunc func(http.ResponseWriter, *http.Request)

func (handler httpHandlerFunc) ServeHTTP(w http.ResponseWriter, r *http.Request) { handler(w, r) }

func httpNotFound(w http.ResponseWriter) {
	w.WriteHeader(404)
	_, _ = io.WriteString(w, "not found")
}
