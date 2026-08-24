package main

import (
	"bufio"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const localMCPServerName = "erdai-local-tools"

type localMCPRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type localMCPSession struct {
	events chan []byte
}

type localMCPHub struct {
	db       *sql.DB
	mu       sync.Mutex
	sessions map[string]*localMCPSession
}

func newLocalMCPHub(db *sql.DB) *localMCPHub {
	return &localMCPHub{db: db, sessions: map[string]*localMCPSession{}}
}

func localMCPTools() []nativeMCPTool {
	return []nativeMCPTool{
		{
			Name: "knowledge_search", Description: "Search the configured local knowledge base.",
			InputSchema: map[string]any{
				"type": "object", "required": []string{"query"},
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"namespace": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 10},
				},
			},
		},
		{
			Name: "task_status", Description: "Read durable task and step status by run ID.",
			InputSchema: map[string]any{
				"type": "object", "required": []string{"runId"},
				"properties": map[string]any{"runId": map[string]any{"type": "string"}},
			},
		},
		{
			Name: "time_now", Description: "Return the server time.",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
		},
	}
}

func localMCPResponse(id json.RawMessage, result any) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
}

func localMCPError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{
		"jsonrpc": "2.0", "id": id,
		"error": map[string]any{"code": code, "message": message},
	}
}

func localMCPToolResult(value any, toolErr error) map[string]any {
	if toolErr != nil {
		return map[string]any{
			"content": []map[string]string{{"type": "text", "text": toolErr.Error()}},
			"isError": true,
		}
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		encoded = []byte(`{"error":"result_encode_failed"}`)
	}
	return map[string]any{
		"content": []map[string]string{{"type": "text", "text": string(encoded)}},
		"isError": false,
	}
}

func dispatchLocalMCP(db *sql.DB, request localMCPRequest) map[string]any {
	if request.JSONRPC != "2.0" || strings.TrimSpace(request.Method) == "" {
		return localMCPError(request.ID, -32600, "invalid request")
	}
	if request.Method == "notifications/initialized" {
		return nil
	}
	switch request.Method {
	case "initialize":
		return localMCPResponse(request.ID, map[string]any{
			"protocolVersion": nativeMCPProtocolVersion,
			"capabilities": map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo": map[string]string{"name": localMCPServerName, "version": erdaiRuntimeVersion},
		})
	case "tools/list":
		return localMCPResponse(request.ID, map[string]any{"tools": localMCPTools()})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if json.Unmarshal(request.Params, &params) != nil {
			return localMCPError(request.ID, -32602, "invalid tool arguments")
		}
		value, err := executeLocalMCPTool(db, params.Name, params.Arguments)
		return localMCPResponse(request.ID, localMCPToolResult(value, err))
	default:
		return localMCPError(request.ID, -32601, "method not found")
	}
}

func executeLocalMCPTool(db *sql.DB, name string, arguments map[string]any) (any, error) {
	switch strings.TrimSpace(name) {
	case "time_now":
		return map[string]string{"time": time.Now().Format(time.RFC3339)}, nil
	case "knowledge_search":
		return localMCPKnowledgeSearch(db, arguments)
	case "task_status":
		return localMCPTaskStatus(db, arguments)
	default:
		return nil, errors.New("tool is not available")
	}
}

func localMCPKnowledgeSearch(db *sql.DB, arguments map[string]any) (any, error) {
	if db == nil {
		return nil, errors.New("knowledge database is unavailable")
	}
	query, _ := arguments["query"].(string)
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 500 {
		return nil, errors.New("query is required and must not exceed 500 characters")
	}
	namespace, _ := arguments["namespace"].(string)
	namespace = strings.TrimSpace(namespace)
	if namespace == "" {
		_ = db.QueryRow("SELECT knowledge_namespace FROM runtime_config WHERE id = 1").Scan(&namespace)
	}
	if namespace == "" {
		namespace = "default"
	}
	limit := 5
	if raw, ok := arguments["limit"].(float64); ok && raw >= 1 && raw <= 10 {
		limit = int(raw)
	}
	pattern := "%" + strings.ReplaceAll(strings.ReplaceAll(query, "%", "\\%"), "_", "\\_") + "%"
	rows, err := db.Query(`SELECT id, title, source_uri, substr(content, 1, 500)
		FROM knowledge_documents WHERE namespace = ? AND
		(title LIKE ? ESCAPE '\' OR content LIKE ? ESCAPE '\') ORDER BY updated_at DESC LIMIT ?`,
		namespace, pattern, pattern, limit)
	if err != nil {
		return nil, errors.New("knowledge search failed")
	}
	defer rows.Close()
	items := []map[string]string{}
	for rows.Next() {
		var id, title, source, content string
		if err = rows.Scan(&id, &title, &source, &content); err != nil {
			return nil, errors.New("knowledge search failed")
		}
		items = append(items, map[string]string{
			"id": id, "title": title, "source": source, "content": content,
		})
	}
	return map[string]any{"namespace": namespace, "matches": items}, rows.Err()
}

func localMCPTaskStatus(db *sql.DB, arguments map[string]any) (any, error) {
	if db == nil {
		return nil, errors.New("task database is unavailable")
	}
	runID, _ := arguments["runId"].(string)
	runID = strings.TrimSpace(runID)
	if runID == "" || len(runID) > 160 {
		return nil, errors.New("runId is required")
	}
	var state, transport, updatedAt string
	if err := db.QueryRow(`SELECT state, transport, updated_at FROM agent_runs WHERE id = ?`, runID).
		Scan(&state, &transport, &updatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("task was not found")
		}
		return nil, errors.New("task status failed")
	}
	rows, err := db.Query(`SELECT step_index, kind, name, status, attempts, error_code
		FROM agent_task_steps WHERE run_id = ? ORDER BY step_index, created_at`, runID)
	if err != nil {
		return nil, errors.New("task status failed")
	}
	defer rows.Close()
	steps := []map[string]any{}
	for rows.Next() {
		var index, attempts int
		var kind, stepName, status, errorCode string
		if err = rows.Scan(&index, &kind, &stepName, &status, &attempts, &errorCode); err != nil {
			return nil, errors.New("task status failed")
		}
		steps = append(steps, map[string]any{
			"index": index, "kind": kind, "name": stepName, "status": status,
			"attempts": attempts, "errorCode": errorCode,
		})
	}
	return map[string]any{
		"runId": runID, "state": state, "transport": transport,
		"updatedAt": updatedAt, "steps": steps,
	}, rows.Err()
}

func serveLocalMCPStdio(input io.Reader, output io.Writer) error {
	database, err := openLocalMCPDatabase(strings.TrimSpace(os.Getenv("ERDAI_CONFIG_DATABASE")))
	if err != nil {
		return err
	}
	if database != nil {
		defer database.Close()
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64*1024), nativeMCPMaxResponse)
	encoder := json.NewEncoder(output)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var request localMCPRequest
		if json.Unmarshal(line, &request) != nil {
			if err = encoder.Encode(localMCPError(nil, -32700, "parse error")); err != nil {
				return err
			}
			continue
		}
		if response := dispatchLocalMCP(database, request); response != nil {
			if err = encoder.Encode(response); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}

func openLocalMCPDatabase(path string) (*sql.DB, error) {
	if path == "" {
		return nil, nil
	}
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro"}).String()
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open MCP database: %w", err)
	}
	if err = database.Ping(); err != nil {
		database.Close()
		return nil, fmt.Errorf("open MCP database: %w", err)
	}
	return database, nil
}

func (h *localMCPHub) handleHTTP(w http.ResponseWriter, r *http.Request, path string, authorized bool) bool {
	if path != "/internal/mcp/sse" && path != "/internal/mcp/messages" {
		return false
	}
	if !authorized {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "runtime token required"})
		return true
	}
	if path == "/internal/mcp/sse" {
		h.serveSSE(w, r)
		return true
	}
	h.receiveSSEMessage(w, r)
	return true
}

func (h *localMCPHub) serveSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming unsupported"})
		return
	}
	sessionID, err := randomID("mcp")
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "session failed"})
		return
	}
	session := &localMCPSession{events: make(chan []byte, 16)}
	h.mu.Lock()
	h.sessions[sessionID] = session
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.sessions, sessionID)
		h.mu.Unlock()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprintf(w, "event: endpoint\ndata: /internal/mcp/messages?session=%s\n\n", sessionID)
	flusher.Flush()
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case payload := <-session.events:
			_, _ = fmt.Fprintf(w, "event: message\ndata: %s\n\n", payload)
			flusher.Flush()
		case <-keepalive.C:
			_, _ = io.WriteString(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func (h *localMCPHub) receiveSSEMessage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("session"))
	h.mu.Lock()
	session := h.sessions[sessionID]
	h.mu.Unlock()
	if session == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "session not found"})
		return
	}
	var request localMCPRequest
	decoder := json.NewDecoder(io.LimitReader(r.Body, nativeMCPMaxResponse+1))
	if decoder.Decode(&request) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	response := dispatchLocalMCP(h.db, request)
	if response != nil {
		encoded, err := json.Marshal(response)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "response failed"})
			return
		}
		select {
		case session.events <- encoded:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "session busy"})
			return
		}
	}
	w.WriteHeader(http.StatusAccepted)
}
