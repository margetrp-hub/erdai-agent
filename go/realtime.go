package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	realtimeConnectorID       = "realtime-gateway"
	realtimeProtocol          = "erdai.realtime.v1"
	realtimeTokenProtocol     = "erdai.token."
	realtimePairingTTL        = 10 * time.Minute
	realtimeDeliveryAckWait   = 20 * time.Second
	realtimeMaxTextRunes      = 4000
	realtimeMaxWebSocketBytes = 128 * 1024
)

const realtimeSchema = `
CREATE TABLE IF NOT EXISTS realtime_devices (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  token_hash BLOB NOT NULL UNIQUE,
  status TEXT NOT NULL CHECK (status IN ('trusted', 'revoked')),
  created_at TEXT NOT NULL,
  last_seen_at TEXT,
  revoked_at TEXT
);
CREATE TABLE IF NOT EXISTS realtime_pairing_codes (
  code_hash BLOB PRIMARY KEY,
  expires_at TEXT NOT NULL,
  used_at TEXT,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS realtime_sessions (
  id TEXT PRIMARY KEY,
  device_id TEXT NOT NULL REFERENCES realtime_devices(id) ON DELETE CASCADE,
  persona_id TEXT NOT NULL DEFAULT '',
  conversation_ref TEXT NOT NULL,
  state TEXT NOT NULL DEFAULT 'idle',
  presence TEXT NOT NULL DEFAULT 'online',
  last_client_sequence INTEGER NOT NULL DEFAULT 0,
  last_server_sequence INTEGER NOT NULL DEFAULT 0,
  connected_at TEXT,
  disconnected_at TEXT,
  updated_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS realtime_sessions_device_idx
  ON realtime_sessions(device_id, updated_at);
`

type realtimeHub struct {
	runtime *AgentRuntime
	mu      sync.Mutex
	clients map[string]*realtimeClient
	pending map[string]chan struct{}
	closed  bool
}

type realtimeClient struct {
	hub       *realtimeHub
	conn      *websocket.Conn
	deviceID  string
	device    string
	sessionID string
	writeMu   sync.Mutex
}

type realtimeEnvelope struct {
	Version   int             `json:"version"`
	EventID   string          `json:"eventId"`
	SessionID string          `json:"sessionId,omitempty"`
	DeviceID  string          `json:"deviceId,omitempty"`
	Sequence  int64           `json:"sequence"`
	Timestamp string          `json:"timestamp"`
	PersonaID string          `json:"personaId,omitempty"`
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

type realtimeDevice struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	Online     bool    `json:"online"`
	CreatedAt  string  `json:"createdAt"`
	LastSeenAt *string `json:"lastSeenAt"`
	RevokedAt  *string `json:"revokedAt"`
}

type realtimeSession struct {
	ID                 string  `json:"id"`
	DeviceID           string  `json:"deviceId"`
	DeviceName         string  `json:"deviceName"`
	PersonaID          string  `json:"personaId"`
	ConversationRef    string  `json:"conversationRef"`
	State              string  `json:"state"`
	Presence           string  `json:"presence"`
	Online             bool    `json:"online"`
	LastClientSequence int64   `json:"lastClientSequence"`
	LastServerSequence int64   `json:"lastServerSequence"`
	ConnectedAt        *string `json:"connectedAt"`
	DisconnectedAt     *string `json:"disconnectedAt"`
	UpdatedAt          string  `json:"updatedAt"`
}

func newRealtimeHub(runtime *AgentRuntime) (*realtimeHub, error) {
	if runtime == nil || runtime.db == nil {
		return nil, errors.New("runtime database is required")
	}
	if _, err := runtime.db.Exec(realtimeSchema); err != nil {
		return nil, err
	}
	return &realtimeHub{
		runtime: runtime,
		clients: map[string]*realtimeClient{},
		pending: map[string]chan struct{}{},
	}, nil
}

func (h *realtimeHub) ID() string                  { return realtimeConnectorID }
func (h *realtimeHub) Type() string                { return "realtime" }
func (h *realtimeHub) Start(context.Context) error { return nil }

func (h *realtimeHub) Close() error {
	h.mu.Lock()
	h.closed = true
	clients := make([]*realtimeClient, 0, len(h.clients))
	for _, client := range h.clients {
		clients = append(clients, client)
	}
	h.clients = map[string]*realtimeClient{}
	h.mu.Unlock()
	for _, client := range clients {
		_ = client.conn.Close()
	}
	return nil
}

func (h *realtimeHub) Health() platformConnectorHealth {
	h.mu.Lock()
	connected := len(h.clients)
	closed := h.closed
	h.mu.Unlock()
	status := "connected"
	if closed {
		status = "stopped"
	}
	return platformConnectorHealth{
		ID: realtimeConnectorID, Type: "realtime", Status: status,
		Details: map[string]any{"connectedSessions": connected},
	}
}

func (h *realtimeHub) Deliver(ctx context.Context, route platformReplyRoute, delivery leasedTransportDelivery) error {
	if route.ConnectorID != realtimeConnectorID || route.TargetID == "" {
		return &platformDeliveryError{Retryable: false, Reason: "invalid_realtime_route"}
	}
	h.mu.Lock()
	client := h.clients[route.TargetID]
	if client == nil {
		h.mu.Unlock()
		return &platformDeliveryError{Retryable: true, Reason: "realtime_session_offline"}
	}
	ack := make(chan struct{})
	h.pending[delivery.ID] = ack
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.pending, delivery.ID)
		h.mu.Unlock()
	}()

	payload, err := json.Marshal(map[string]any{
		"deliveryId":  delivery.ID,
		"runId":       delivery.RunID,
		"phase":       delivery.Phase,
		"text":        delivery.Message.Text,
		"attachments": delivery.Message.Attachments,
	})
	if err != nil {
		return err
	}
	if err = client.send("response.commit", payload); err != nil {
		return &platformDeliveryError{Retryable: true, Reason: "realtime_write_failed", Cause: err}
	}
	timer := time.NewTimer(realtimeDeliveryAckWait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return &platformDeliveryError{Retryable: true, Reason: "realtime_delivery_cancelled", Cause: ctx.Err()}
	case <-ack:
		_ = client.sendState("idle", delivery.RunID)
		return nil
	case <-timer.C:
		return &platformDeliveryError{Retryable: true, Reason: "realtime_ack_timeout"}
	}
}

func (h *realtimeHub) handlePublic(w http.ResponseWriter, r *http.Request, path string) bool {
	switch {
	case path == "/api/v2/realtime/pair" && r.Method == http.MethodPost:
		h.handlePair(w, r)
		return true
	case path == "/api/v2/realtime" && r.Method == http.MethodGet:
		h.handleWebSocket(w, r)
		return true
	case path == "/api/v2/realtime/pair" || path == "/api/v2/realtime":
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
		})
		return true
	default:
		return false
	}
}

func (h *realtimeHub) handlePair(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
		Name string `json:"name"`
	}
	if err := decodeJSONBody(r, &input); err != nil {
		runtimeError(w, http.StatusBadRequest, "invalid_pairing_request")
		return
	}
	device, token, err := h.pairDevice(r.Context(), input.Code, input.Name)
	if err != nil {
		var runtimeErr *transportRuntimeError
		if errors.As(err, &runtimeErr) {
			runtimeError(w, runtimeErr.status, runtimeErr.code)
			return
		}
		runtimeError(w, http.StatusInternalServerError, "pairing_failed")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{"device": device, "token": token, "protocol": realtimeProtocol},
	})
}

func (h *realtimeHub) pairDevice(ctx context.Context, code, name string) (realtimeDevice, string, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	if len(code) < 8 || len(code) > 64 || name == "" || len([]rune(name)) > 80 {
		return realtimeDevice{}, "", newTransportRuntimeError(http.StatusBadRequest, "invalid_pairing_request", nil)
	}
	now := time.Now().UTC()
	tx, err := h.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return realtimeDevice{}, "", err
	}
	defer tx.Rollback()
	var expiresAt string
	digest := realtimeDigest("pair", code)
	err = tx.QueryRowContext(ctx, `SELECT expires_at FROM realtime_pairing_codes
		WHERE code_hash = ? AND used_at IS NULL`, digest[:]).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return realtimeDevice{}, "", newTransportRuntimeError(http.StatusUnauthorized, "pairing_code_invalid", err)
	}
	if err != nil {
		return realtimeDevice{}, "", err
	}
	expires, err := time.Parse(time.RFC3339Nano, expiresAt)
	if err != nil || !expires.After(now) {
		return realtimeDevice{}, "", newTransportRuntimeError(http.StatusUnauthorized, "pairing_code_expired", err)
	}
	deviceID, err := randomID("device")
	if err != nil {
		return realtimeDevice{}, "", err
	}
	token, err := randomID("erdai_device")
	if err != nil {
		return realtimeDevice{}, "", err
	}
	tokenDigest := realtimeDigest("token", token)
	nowText := now.Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO realtime_devices
		(id, name, token_hash, status, created_at) VALUES (?, ?, ?, 'trusted', ?)`,
		deviceID, name, tokenDigest[:], nowText); err != nil {
		return realtimeDevice{}, "", err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE realtime_pairing_codes SET used_at = ?
		WHERE code_hash = ? AND used_at IS NULL`, nowText, digest[:]); err != nil {
		return realtimeDevice{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return realtimeDevice{}, "", err
	}
	return realtimeDevice{ID: deviceID, Name: name, Status: "trusted", CreatedAt: nowText}, token, nil
}

func (h *realtimeHub) createPairingCode(ctx context.Context) (string, string, error) {
	rawID, err := randomID("pair")
	if err != nil {
		return "", "", err
	}
	code := strings.ToUpper(strings.TrimPrefix(rawID, "pair_"))
	now := time.Now().UTC()
	expires := now.Add(realtimePairingTTL)
	digest := realtimeDigest("pair", code)
	_, err = h.runtime.db.ExecContext(ctx, `INSERT INTO realtime_pairing_codes
		(code_hash, expires_at, used_at, created_at) VALUES (?, ?, NULL, ?)`,
		digest[:], expires.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return "", "", err
	}
	return code, expires.Format(time.RFC3339Nano), nil
}

func realtimeDigest(kind, value string) [32]byte {
	return sha256.Sum256([]byte("erdai-realtime-v1:" + kind + ":" + value))
}

func realtimeTokenFromProtocols(r *http.Request) string {
	for _, protocol := range websocket.Subprotocols(r) {
		if strings.HasPrefix(protocol, realtimeTokenProtocol) {
			raw := strings.TrimPrefix(protocol, realtimeTokenProtocol)
			decoded, err := base64.RawURLEncoding.DecodeString(raw)
			if err == nil {
				return string(decoded)
			}
		}
	}
	return ""
}

func (h *realtimeHub) authenticateDevice(ctx context.Context, token string) (realtimeDevice, error) {
	if len(token) < 24 || len(token) > 256 {
		return realtimeDevice{}, errors.New("device token is invalid")
	}
	digest := realtimeDigest("token", token)
	var device realtimeDevice
	err := h.runtime.db.QueryRowContext(ctx, `SELECT id, name, status, created_at,
		last_seen_at, revoked_at FROM realtime_devices WHERE token_hash = ? AND status = 'trusted'`, digest[:]).
		Scan(&device.ID, &device.Name, &device.Status, &device.CreatedAt, &device.LastSeenAt, &device.RevokedAt)
	return device, err
}

func (h *realtimeHub) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	device, err := h.authenticateDevice(r.Context(), realtimeTokenFromProtocols(r))
	if err != nil {
		runtimeError(w, http.StatusUnauthorized, "device_authentication_failed")
		return
	}
	sessionID := strings.TrimSpace(r.URL.Query().Get("sessionId"))
	session, err := h.openSession(r.Context(), device, sessionID)
	if err != nil {
		runtimeError(w, http.StatusBadRequest, "session_open_failed")
		return
	}
	upgrader := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096,
		CheckOrigin:  func(request *http.Request) bool { return realtimeOriginAllowed(request) },
		Subprotocols: []string{realtimeProtocol},
	}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadLimit(realtimeMaxWebSocketBytes)
	client := &realtimeClient{
		hub: h, conn: conn, deviceID: device.ID, device: device.Name, sessionID: session.ID,
	}
	h.register(client)
	defer h.unregister(client)
	readyPayload, _ := json.Marshal(map[string]any{
		"sessionId": session.ID, "deviceId": device.ID, "deviceName": device.Name,
		"state": session.State, "lastClientSequence": session.LastClientSequence,
		"lastServerSequence": session.LastServerSequence,
	})
	if err = client.send("session.ready", readyPayload); err != nil {
		return
	}
	for {
		var event realtimeEnvelope
		if err = conn.ReadJSON(&event); err != nil {
			return
		}
		if err = client.handleEvent(r.Context(), event); err != nil {
			payload, _ := json.Marshal(map[string]string{"message": err.Error()})
			_ = client.send("error", payload)
		}
	}
}

func realtimeOriginAllowed(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	return origin == "https://"+r.Host || origin == "http://"+r.Host
}

func (h *realtimeHub) openSession(ctx context.Context, device realtimeDevice, requestedID string) (realtimeSession, error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if requestedID != "" {
		var session realtimeSession
		err := h.runtime.db.QueryRowContext(ctx, `SELECT id, device_id, persona_id, conversation_ref,
			state, presence, last_client_sequence, last_server_sequence, connected_at,
			disconnected_at, updated_at FROM realtime_sessions WHERE id = ? AND device_id = ?`,
			requestedID, device.ID).Scan(&session.ID, &session.DeviceID, &session.PersonaID,
			&session.ConversationRef, &session.State, &session.Presence,
			&session.LastClientSequence, &session.LastServerSequence, &session.ConnectedAt,
			&session.DisconnectedAt, &session.UpdatedAt)
		if err == nil {
			_, err = h.runtime.db.ExecContext(ctx, `UPDATE realtime_sessions SET presence = 'online',
				connected_at = ?, disconnected_at = NULL, updated_at = ? WHERE id = ?`, now, now, session.ID)
			session.Presence, session.ConnectedAt, session.DisconnectedAt, session.UpdatedAt = "online", &now, nil, now
			return session, err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return realtimeSession{}, err
		}
	}
	sessionID, err := randomID("session")
	if err != nil {
		return realtimeSession{}, err
	}
	personaID, err := h.runtime.activePersonaID("realtime", "desktop:"+device.ID)
	if err != nil {
		return realtimeSession{}, err
	}
	session := realtimeSession{
		ID: sessionID, DeviceID: device.ID, DeviceName: device.Name,
		PersonaID: personaID, ConversationRef: "desktop:" + device.ID,
		State: "idle", Presence: "online", ConnectedAt: &now, UpdatedAt: now,
	}
	_, err = h.runtime.db.ExecContext(ctx, `INSERT INTO realtime_sessions
		(id, device_id, persona_id, conversation_ref, state, presence, connected_at, updated_at)
		VALUES (?, ?, ?, ?, 'idle', 'online', ?, ?)`,
		session.ID, session.DeviceID, session.PersonaID, session.ConversationRef, now, now)
	return session, err
}

func (h *realtimeHub) register(client *realtimeClient) {
	h.mu.Lock()
	previous := h.clients[client.sessionID]
	h.clients[client.sessionID] = client
	h.mu.Unlock()
	if previous != nil && previous != client {
		_ = previous.conn.Close()
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = h.runtime.db.Exec(`UPDATE realtime_devices SET last_seen_at = ? WHERE id = ?`, now, client.deviceID)
}

func (h *realtimeHub) unregister(client *realtimeClient) {
	h.mu.Lock()
	if h.clients[client.sessionID] == client {
		delete(h.clients, client.sessionID)
	}
	h.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, _ = h.runtime.db.Exec(`UPDATE realtime_sessions SET presence = 'offline', disconnected_at = ?,
		updated_at = ? WHERE id = ?`, now, now, client.sessionID)
	_ = client.conn.Close()
}

func (c *realtimeClient) handleEvent(ctx context.Context, event realtimeEnvelope) error {
	if event.Version != 1 || strings.TrimSpace(event.EventID) == "" || event.Sequence < 1 {
		return errors.New("invalid realtime event")
	}
	if event.SessionID != "" && event.SessionID != c.sessionID {
		return errors.New("session mismatch")
	}
	switch event.Type {
	case "ping":
		return c.send("pong", nil)
	case "delivery.ack":
		var input struct {
			DeliveryID string `json:"deliveryId"`
		}
		if json.Unmarshal(event.Payload, &input) != nil || input.DeliveryID == "" {
			return errors.New("delivery acknowledgement is invalid")
		}
		c.hub.ack(input.DeliveryID)
		return c.advanceSequence(ctx, event.Sequence, "online", "idle")
	case "presence.update":
		var input struct {
			Presence string `json:"presence"`
		}
		if json.Unmarshal(event.Payload, &input) != nil ||
			(input.Presence != "online" && input.Presence != "away" && input.Presence != "do_not_disturb") {
			return errors.New("presence is invalid")
		}
		return c.advanceSequence(ctx, event.Sequence, input.Presence, "")
	case "speech.finished":
		return c.advanceSequence(ctx, event.Sequence, "online", "idle")
	case "input.text", "input.transcript":
		var input struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(event.Payload, &input) != nil {
			return errors.New("text payload is invalid")
		}
		input.Text = strings.TrimSpace(input.Text)
		if input.Text == "" || len([]rune(input.Text)) > realtimeMaxTextRunes {
			return errors.New("text is empty or too long")
		}
		advanced, err := c.claimSequence(ctx, event.Sequence)
		if err != nil {
			return err
		}
		if !advanced {
			payload, _ := json.Marshal(map[string]any{"eventId": event.EventID, "duplicate": true})
			return c.send("input.accepted", payload)
		}
		return c.acceptText(ctx, event.EventID, input.Text)
	default:
		return fmt.Errorf("unsupported realtime event type %q", event.Type)
	}
}

func (c *realtimeClient) acceptText(ctx context.Context, eventID, text string) error {
	internalEventID := "realtime:" + c.deviceID + ":" + eventID
	replyHandle, err := c.hub.runtime.rememberPlatformRoute(ctx, internalEventID, platformReplyRoute{
		ConnectorID: realtimeConnectorID, Transport: "realtime", Kind: "private",
		TargetID: c.sessionID, MessageID: eventID,
	})
	if err != nil {
		return err
	}
	event := transportEvent{SchemaVersion: 2, EventID: internalEventID, Transport: "realtime",
		TransportInstance: realtimeConnectorID, ReplyHandle: replyHandle,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)}
	event.Conversation.Key = "desktop:" + c.deviceID
	event.Conversation.Kind = "private"
	event.Conversation.ThreadKey = c.sessionID
	event.Sender.Key = c.deviceID
	event.Sender.DisplayName = c.device
	event.Message.ID = eventID
	event.Message.Text = text
	event.Message.Attachments = []transportAttachment{}
	event.Flags.IsWake = true
	event.Privacy.Transient = []string{"sender.displayName", "message.text"}
	decision, err := c.hub.runtime.acceptTrustedTransportEvent(ctx, event, internalEventID)
	if err != nil {
		c.hub.runtime.forgetPlatformRoute(ctx, internalEventID)
		return err
	}
	payload, _ := json.Marshal(map[string]any{
		"eventId": eventID, "runId": decision["runId"], "disposition": decision["disposition"],
	})
	if err = c.send("input.accepted", payload); err != nil {
		return err
	}
	if decision["disposition"] == "owned" {
		return c.sendState("thinking", fmt.Sprint(decision["runId"]))
	}
	return c.sendState("idle", "")
}

func (c *realtimeClient) claimSequence(ctx context.Context, sequence int64) (bool, error) {
	result, err := c.hub.runtime.db.ExecContext(ctx, `UPDATE realtime_sessions
		SET last_client_sequence = ?, updated_at = ?
		WHERE id = ? AND device_id = ? AND last_client_sequence < ?`,
		sequence, time.Now().UTC().Format(time.RFC3339Nano), c.sessionID, c.deviceID, sequence)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (c *realtimeClient) advanceSequence(ctx context.Context, sequence int64, presence, state string) error {
	advanced, err := c.claimSequence(ctx, sequence)
	if err != nil || !advanced {
		return err
	}
	if presence != "" {
		_, err = c.hub.runtime.db.ExecContext(ctx, `UPDATE realtime_sessions SET presence = ?,
			updated_at = ? WHERE id = ?`, presence, time.Now().UTC().Format(time.RFC3339Nano), c.sessionID)
	}
	if err == nil && state != "" {
		_, err = c.hub.runtime.db.ExecContext(ctx, `UPDATE realtime_sessions SET state = ?,
			updated_at = ? WHERE id = ?`, state, time.Now().UTC().Format(time.RFC3339Nano), c.sessionID)
	}
	return err
}

func (c *realtimeClient) nextServerSequence() (int64, error) {
	tx, err := c.hub.runtime.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var current int64
	if err = tx.QueryRow(`SELECT last_server_sequence FROM realtime_sessions WHERE id = ?`, c.sessionID).Scan(&current); err != nil {
		return 0, err
	}
	current++
	if _, err = tx.Exec(`UPDATE realtime_sessions SET last_server_sequence = ?, updated_at = ? WHERE id = ?`,
		current, time.Now().UTC().Format(time.RFC3339Nano), c.sessionID); err != nil {
		return 0, err
	}
	return current, tx.Commit()
}

func (c *realtimeClient) send(eventType string, payload []byte) error {
	sequence, err := c.nextServerSequence()
	if err != nil {
		return err
	}
	eventID, err := randomID("rt")
	if err != nil {
		return err
	}
	event := realtimeEnvelope{
		Version: 1, EventID: eventID, SessionID: c.sessionID, DeviceID: c.deviceID,
		Sequence: sequence, Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
		Type: eventType, Payload: payload,
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.conn.WriteJSON(event)
}

func (c *realtimeClient) sendState(state, runID string) error {
	if state != "idle" && state != "thinking" && state != "speaking" && state != "tool_running" && state != "error" {
		return errors.New("agent state is invalid")
	}
	_, _ = c.hub.runtime.db.Exec(`UPDATE realtime_sessions SET state = ?, updated_at = ? WHERE id = ?`,
		state, time.Now().UTC().Format(time.RFC3339Nano), c.sessionID)
	payload, _ := json.Marshal(map[string]string{"state": state, "runId": runID})
	return c.send("agent.state", payload)
}

func (h *realtimeHub) ack(deliveryID string) {
	h.mu.Lock()
	pending := h.pending[deliveryID]
	h.mu.Unlock()
	if pending == nil {
		return
	}
	select {
	case pending <- struct{}{}:
	default:
	}
}

func (h *realtimeHub) listDevices(ctx context.Context) ([]realtimeDevice, error) {
	rows, err := h.runtime.db.QueryContext(ctx, `SELECT id, name, status, created_at,
		last_seen_at, revoked_at FROM realtime_devices ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []realtimeDevice{}
	for rows.Next() {
		var value realtimeDevice
		if err = rows.Scan(&value.ID, &value.Name, &value.Status, &value.CreatedAt,
			&value.LastSeenAt, &value.RevokedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	h.mu.Lock()
	for index := range values {
		for _, client := range h.clients {
			if client.deviceID == values[index].ID {
				values[index].Online = true
				break
			}
		}
	}
	h.mu.Unlock()
	return values, rows.Err()
}

func (h *realtimeHub) listSessions(ctx context.Context) ([]realtimeSession, error) {
	rows, err := h.runtime.db.QueryContext(ctx, `SELECT session.id, session.device_id, device.name,
		session.persona_id, session.conversation_ref, session.state, session.presence,
		session.last_client_sequence, session.last_server_sequence, session.connected_at,
		session.disconnected_at, session.updated_at
		FROM realtime_sessions session JOIN realtime_devices device ON device.id = session.device_id
		ORDER BY session.updated_at DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []realtimeSession{}
	for rows.Next() {
		var value realtimeSession
		if err = rows.Scan(&value.ID, &value.DeviceID, &value.DeviceName, &value.PersonaID,
			&value.ConversationRef, &value.State, &value.Presence, &value.LastClientSequence,
			&value.LastServerSequence, &value.ConnectedAt, &value.DisconnectedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	h.mu.Lock()
	for index := range values {
		values[index].Online = h.clients[values[index].ID] != nil
	}
	h.mu.Unlock()
	return values, rows.Err()
}

func (h *realtimeHub) revokeDevice(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" || strings.Contains(id, "/") {
		return coreInvalid("device id is invalid")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := h.runtime.db.ExecContext(ctx, `UPDATE realtime_devices SET status = 'revoked',
		revoked_at = ? WHERE id = ? AND status = 'trusted'`, now, id)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return mgmtNotFound("device")
	}
	h.mu.Lock()
	clients := []*realtimeClient{}
	for _, client := range h.clients {
		if client.deviceID == id {
			clients = append(clients, client)
		}
	}
	h.mu.Unlock()
	for _, client := range clients {
		_ = client.conn.Close()
	}
	return nil
}

func (a *AgentRuntime) handleRealtimeManagement(w http.ResponseWriter, r *http.Request, path string) error {
	if a.realtime == nil {
		return errors.New("realtime gateway is unavailable")
	}
	switch {
	case path == "/api/v1/devices" && r.Method == http.MethodGet:
		values, err := a.realtime.listDevices(r.Context())
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, values)
		return nil
	case strings.HasPrefix(path, "/api/v1/devices/") && r.Method == http.MethodDelete:
		id, err := mgmtPathID(path, "/api/v1/devices/")
		if err != nil {
			return err
		}
		if err = a.realtime.revokeDevice(r.Context(), id); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]string{"id": id, "status": "revoked"})
		return nil
	case path == "/api/v1/realtime/pairing-codes" && r.Method == http.MethodPost:
		code, expiresAt, err := a.realtime.createPairingCode(r.Context())
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusCreated, map[string]string{"code": code, "expiresAt": expiresAt})
		return nil
	case path == "/api/v1/realtime/sessions" && r.Method == http.MethodGet:
		values, err := a.realtime.listSessions(r.Context())
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, values)
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}
