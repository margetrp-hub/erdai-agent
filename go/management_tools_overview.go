package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type mgmtToolConfig struct {
	AdapterRef         string         `json:"adapterRef"`
	AllowedAuthorities []string       `json:"allowedAuthorities"`
	ApprovalMode       string         `json:"approvalMode"`
	TimeoutSeconds     int            `json:"timeoutSeconds"`
	InputSchema        map[string]any `json:"inputSchema"`
}

type mgmtTool struct {
	ID                 string         `json:"id"`
	Name               string         `json:"name"`
	Description        string         `json:"description"`
	Capabilities       []string       `json:"capabilities"`
	RiskLevel          int            `json:"riskLevel"`
	Enabled            bool           `json:"enabled"`
	AdapterRef         string         `json:"adapterRef"`
	AllowedAuthorities []string       `json:"allowedAuthorities"`
	ApprovalMode       string         `json:"approvalMode"`
	TimeoutSeconds     int            `json:"timeoutSeconds"`
	InputSchema        map[string]any `json:"inputSchema"`
	CreatedAt          string         `json:"createdAt"`
	UpdatedAt          string         `json:"updatedAt"`
}

func scanMgmtTool(scanner interface{ Scan(...any) error }) (mgmtTool, error) {
	var value mgmtTool
	var capabilitiesJSON, configJSON string
	var enabled int
	err := scanner.Scan(
		&value.ID, &value.Name, &value.Description, &capabilitiesJSON,
		&value.RiskLevel, &enabled, &configJSON, &value.CreatedAt, &value.UpdatedAt,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.Capabilities = decodeJSONStringList(capabilitiesJSON)
	config := mgmtToolConfig{
		AllowedAuthorities: []string{"admin"}, ApprovalMode: "admin_only",
		TimeoutSeconds: 30, InputSchema: map[string]any{},
	}
	if json.Unmarshal([]byte(configJSON), &config) != nil {
		config = mgmtToolConfig{
			AllowedAuthorities: []string{"admin"}, ApprovalMode: "admin_only",
			TimeoutSeconds: 30, InputSchema: map[string]any{},
		}
	}
	if config.AllowedAuthorities == nil {
		config.AllowedAuthorities = []string{"admin"}
	}
	if config.InputSchema == nil {
		config.InputSchema = map[string]any{}
	}
	value.AdapterRef = config.AdapterRef
	value.AllowedAuthorities = config.AllowedAuthorities
	value.ApprovalMode = config.ApprovalMode
	value.TimeoutSeconds = config.TimeoutSeconds
	value.InputSchema = config.InputSchema
	return value, nil
}

func (s *coreConfigStore) mgmtTool(id string) (mgmtTool, bool, error) {
	value, err := scanMgmtTool(s.db.QueryRow(`
		SELECT id, name, description, capabilities_json, risk_level, enabled,
			config_json, created_at, updated_at FROM tools WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtTools() ([]mgmtTool, error) {
	rows, err := s.db.Query(`
		SELECT id, name, description, capabilities_json, risk_level, enabled,
			config_json, created_at, updated_at FROM tools ORDER BY name, id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtTool{}
	for rows.Next() {
		value, err := scanMgmtTool(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type mgmtToolPayload struct {
	ID                 *string         `json:"id"`
	Name               *string         `json:"name"`
	Description        *string         `json:"description"`
	Capabilities       *[]string       `json:"capabilities"`
	RiskLevel          *int            `json:"riskLevel"`
	Enabled            *bool           `json:"enabled"`
	AdapterRef         *string         `json:"adapterRef"`
	AllowedAuthorities *[]string       `json:"allowedAuthorities"`
	ApprovalMode       *string         `json:"approvalMode"`
	TimeoutSeconds     *int            `json:"timeoutSeconds"`
	InputSchema        *map[string]any `json:"inputSchema"`
}

var (
	mgmtToolCreateFields = coreFieldSet(
		"id", "name", "description", "capabilities", "riskLevel", "enabled",
		"adapterRef", "allowedAuthorities", "approvalMode", "timeoutSeconds", "inputSchema",
	)
	mgmtToolUpdateFields = coreFieldSet(
		"name", "description", "capabilities", "riskLevel", "enabled",
		"adapterRef", "allowedAuthorities", "approvalMode", "timeoutSeconds", "inputSchema",
	)
)

func mgmtFieldNames(fields map[string]json.RawMessage) []string {
	values := make([]string, 0, len(fields))
	for field := range fields {
		values = append(values, field)
	}
	sort.Strings(values)
	return values
}

func mgmtRejectNullFields(fields map[string]json.RawMessage) error {
	for field, raw := range fields {
		if strings.TrimSpace(string(raw)) == "null" {
			return coreInvalid(field + " must not be null")
		}
	}
	return nil
}

func mgmtAuthorities(values []string) ([]string, error) {
	values, err := normalizeCoreStrings(values, "allowedAuthorities", 2, 20)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if value != "member" && value != "admin" {
			return nil, coreInvalid("allowedAuthorities contains unsupported values: " + value)
		}
	}
	if len(values) == 0 {
		return nil, coreInvalid("allowedAuthorities must not be empty")
	}
	return values, nil
}

func mgmtContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func mgmtToolValues(input mgmtToolPayload, current *mgmtTool) (mgmtTool, error) {
	value := mgmtTool{
		Enabled: true, RiskLevel: 0, Capabilities: []string{},
		AllowedAuthorities: []string{"member", "admin"}, ApprovalMode: "auto",
		TimeoutSeconds: 30, InputSchema: map[string]any{},
	}
	if current != nil {
		value = *current
	}
	var err error
	if input.Name != nil {
		value.Name, err = mgmtIdentifier(*input.Name, "name")
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("name is required")
	}
	if input.Description != nil {
		value.Description, err = normalizeCoreText(*input.Description, "description", 2000, false)
		if err != nil {
			return value, err
		}
	}
	if input.Capabilities != nil {
		value.Capabilities, err = mgmtCapabilities(*input.Capabilities)
		if err != nil {
			return value, err
		}
	}
	if input.RiskLevel != nil {
		value.RiskLevel = *input.RiskLevel
	}
	if value.RiskLevel < 0 || value.RiskLevel > 3 {
		return value, coreInvalid("riskLevel must be an integer between 0 and 3")
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if input.AdapterRef != nil {
		value.AdapterRef, err = normalizeCoreText(*input.AdapterRef, "adapterRef", 240, false)
		if err != nil {
			return value, err
		}
	}
	if input.AllowedAuthorities != nil {
		value.AllowedAuthorities, err = mgmtAuthorities(*input.AllowedAuthorities)
		if err != nil {
			return value, err
		}
	} else if current == nil && value.RiskLevel >= 2 {
		value.AllowedAuthorities = []string{"admin"}
	}
	if input.ApprovalMode != nil {
		value.ApprovalMode, err = normalizeCoreText(*input.ApprovalMode, "approvalMode", 20, true)
		if err != nil {
			return value, err
		}
	} else if current == nil && value.RiskLevel >= 2 {
		value.ApprovalMode = "admin_only"
	}
	if value.ApprovalMode != "auto" && value.ApprovalMode != "confirm" && value.ApprovalMode != "admin_only" {
		return value, coreInvalid("approvalMode is not supported")
	}
	if value.RiskLevel >= 2 && mgmtContains(value.AllowedAuthorities, "member") {
		return value, coreInvalid("risk level 2 or 3 tools cannot be granted to members")
	}
	if value.ApprovalMode == "admin_only" && mgmtContains(value.AllowedAuthorities, "member") {
		return value, coreInvalid("admin_only tools cannot be granted to members")
	}
	if input.TimeoutSeconds != nil {
		value.TimeoutSeconds = *input.TimeoutSeconds
	}
	if value.TimeoutSeconds < 1 || value.TimeoutSeconds > 300 {
		return value, coreInvalid("timeoutSeconds must be an integer between 1 and 300")
	}
	if input.InputSchema != nil {
		if *input.InputSchema == nil {
			return value, coreInvalid("inputSchema must be an object")
		}
		if len(mgmtJSON(*input.InputSchema)) > 50000 {
			return value, coreInvalid("inputSchema is too large")
		}
		value.InputSchema = *input.InputSchema
	}
	return value, nil
}

func (s *coreConfigStore) mgmtCreateTool(r *http.Request) (mgmtTool, error) {
	var input mgmtToolPayload
	fields, err := decodeCoreObject(r, mgmtToolCreateFields, "tool", &input)
	if err != nil {
		return mgmtTool{}, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return mgmtTool{}, err
	}
	id := ""
	if input.ID != nil {
		id, err = mgmtIdentifier(*input.ID, "id")
	} else {
		id, err = newCoreUUID()
	}
	if err != nil {
		return mgmtTool{}, err
	}
	value, err := mgmtToolValues(input, nil)
	if err != nil {
		return mgmtTool{}, err
	}
	now := mgmtNow()
	config := mgmtToolConfig{
		AdapterRef: value.AdapterRef, AllowedAuthorities: value.AllowedAuthorities,
		ApprovalMode: value.ApprovalMode, TimeoutSeconds: value.TimeoutSeconds,
		InputSchema: value.InputSchema,
	}
	_, err = s.db.Exec(`
		INSERT INTO tools (
			id, name, description, capabilities_json, risk_level, enabled,
			config_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.Name, value.Description, mgmtJSON(value.Capabilities), value.RiskLevel,
		boolInt(value.Enabled), mgmtJSON(config), now, now)
	if err != nil {
		return mgmtTool{}, mgmtConstraintError(err, "tool id or name already exists")
	}
	if err = s.mgmtAudit("create", "tool", id, mgmtFieldNames(fields)); err != nil {
		return mgmtTool{}, err
	}
	value, _, err = s.mgmtTool(id)
	return value, err
}

func (s *coreConfigStore) mgmtUpdateTool(r *http.Request, id string) (mgmtTool, bool, error) {
	current, found, err := s.mgmtTool(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtToolPayload
	fields, err := decodeCoreObject(r, mgmtToolUpdateFields, "tool", &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	value, err := mgmtToolValues(input, &current)
	if err != nil {
		return current, true, err
	}
	config := mgmtToolConfig{
		AdapterRef: value.AdapterRef, AllowedAuthorities: value.AllowedAuthorities,
		ApprovalMode: value.ApprovalMode, TimeoutSeconds: value.TimeoutSeconds,
		InputSchema: value.InputSchema,
	}
	_, err = s.db.Exec(`
		UPDATE tools SET name = ?, description = ?, capabilities_json = ?, risk_level = ?,
			enabled = ?, config_json = ?, updated_at = ? WHERE id = ?
	`, value.Name, value.Description, mgmtJSON(value.Capabilities), value.RiskLevel,
		boolInt(value.Enabled), mgmtJSON(config), mgmtNow(), id)
	if err != nil {
		return current, true, mgmtConstraintError(err, "tool id or name already exists")
	}
	if err = s.mgmtAudit("update", "tool", id, mgmtFieldNames(fields)); err != nil {
		return current, true, err
	}
	value, _, err = s.mgmtTool(id)
	return value, true, err
}

func (s *coreConfigStore) handleManagementTools(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/tools" {
		switch r.Method {
		case http.MethodGet:
			values, err := s.mgmtTools()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateTool(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/tools/")
	if err != nil {
		return err
	}
	id, err = mgmtIdentifier(id, "id")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtTool(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("tool")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateTool(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("tool")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM tools WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "tool"); err != nil {
			return err
		}
		if err = s.mgmtAudit("delete", "tool", id, nil); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) handleManagementOverview(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	tables := map[string]string{
		"model_endpoints": "model_endpoints", "personas": "personas", "worldbook_entries": "worldbook_entries",
		"knowledge_documents": "knowledge_documents", "tools": "tools", "skills": "skills", "mcp_servers": "mcp_servers",
		"plugins": "agent_plugins",
		"runs":    "agent_runs", "deliveries": "agent_deliveries", "run_stage_events": "run_stage_events",
		"audit_events": "audit_events", "shadow_interactions": "shadow_interactions", "routing_control": "routing_control",
		"routing_lane_locks": "routing_lane_locks", "runtime_config": "runtime_config",
		"admin_directives": "admin_directives", "knowledge_candidates": "knowledge_candidates",
		"routing_lane_profiles": "routing_lane_profiles", "integration_settings": "integration_settings",
		"platform_integrations": "platform_integrations", "transport_events": "agent_transport_events",
	}
	counts := make(map[string]int64, len(tables))
	for key, table := range tables {
		var count int64
		if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
			return err
		}
		counts[key] = count
	}
	var schemaVersion int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&schemaVersion); err != nil {
		return err
	}
	models := struct {
		Configured int64 `json:"configured"`
		Healthy    int64 `json:"healthy"`
		Unhealthy  int64 `json:"unhealthy"`
		Unknown    int64 `json:"unknown"`
	}{}
	if err := s.db.QueryRow(`
		SELECT count(*),
			coalesce(sum(CASE WHEN h.healthy = 1 THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN h.healthy = 0 THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN h.endpoint_id IS NULL THEN 1 ELSE 0 END), 0)
		FROM model_endpoints e LEFT JOIN model_health h ON h.endpoint_id = e.id
		WHERE e.enabled = 1
	`).Scan(&models.Configured, &models.Healthy, &models.Unhealthy, &models.Unknown); err != nil {
		return err
	}
	routing, err := s.mgmtRoutingControl()
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{
		"schemaVersion": schemaVersion,
		"counts":        counts,
		"models":        models,
		"routing": map[string]any{
			"mode": routing.Mode, "updatedAt": routing.UpdatedAt,
			"lockedLaneCount": len(routing.Locks), "locks": routing.Locks,
		},
	})
	return nil
}

type mgmtAuditEvent struct {
	ID         int64   `json:"id"`
	Actor      string  `json:"actor"`
	Action     string  `json:"action"`
	TargetType string  `json:"targetType"`
	TargetID   *string `json:"targetId"`
	CreatedAt  string  `json:"createdAt"`
}

func (s *coreConfigStore) handleManagementAudit(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(`
		SELECT id, actor, action, target_type, target_id, created_at
		FROM audit_events ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		return err
	}
	defer rows.Close()
	values := []mgmtAuditEvent{}
	for rows.Next() {
		var value mgmtAuditEvent
		var targetID sql.NullString
		if err = rows.Scan(&value.ID, &value.Actor, &value.Action, &value.TargetType, &targetID, &value.CreatedAt); err != nil {
			return err
		}
		if targetID.Valid {
			value.TargetID = &targetID.String
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, values)
	return nil
}

type mgmtShadowPayload struct {
	Transport       string   `json:"transport"`
	ConversationRef string   `json:"conversationRef"`
	SenderRef       string   `json:"senderRef"`
	Message         string   `json:"message"`
	HasImage        bool     `json:"hasImage"`
	HasAudio        bool     `json:"hasAudio"`
	LegacyModel     string   `json:"legacyModel"`
	Lane            string   `json:"lane"`
	MaxHealthAgeMS  *float64 `json:"maxHealthAgeMs"`
}

type mgmtShadowCreated struct {
	ID        int64                  `json:"id"`
	Lane      string                 `json:"lane"`
	Selected  *nativeRouteCandidate  `json:"selected"`
	Fallbacks []nativeRouteCandidate `json:"fallbacks"`
	Rejected  []nativeRouteRejection `json:"rejected"`
	CreatedAt string                 `json:"createdAt"`
}

type mgmtShadowRecord struct {
	ID                 int64                `json:"id"`
	Transport          string               `json:"transport"`
	ConversationHash   string               `json:"conversationHash"`
	SenderHash         string               `json:"senderHash"`
	MessageLength      int                  `json:"messageLength"`
	HasImage           bool                 `json:"hasImage"`
	HasAudio           bool                 `json:"hasAudio"`
	LegacyModel        string               `json:"legacyModel"`
	Lane               string               `json:"lane"`
	SelectedEndpointID *string              `json:"selectedEndpointId"`
	Route              *nativeRouteDecision `json:"route"`
	CreatedAt          string               `json:"createdAt"`
}

func mgmtBoundedText(value string, maximum int) string {
	value = strings.Map(func(r rune) rune {
		if r <= 31 || r == 127 {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > maximum {
		value = string(runes[:maximum])
	}
	return value
}

func mgmtPseudonym(kind, value string) string {
	digest := sha256.Sum256([]byte(kind + ":" + value))
	return hex.EncodeToString(digest[:])[:24]
}

func (s *coreConfigStore) mgmtRecordShadow(r *http.Request) (mgmtShadowCreated, error) {
	var input mgmtShadowPayload
	_, err := decodeCoreObject(r, coreFieldSet(
		"transport", "conversationRef", "senderRef", "message", "hasImage", "hasAudio",
		"legacyModel", "lane", "maxHealthAgeMs",
	), "shadow interaction", &input)
	if err != nil {
		return mgmtShadowCreated{}, err
	}
	input.Transport = mgmtBoundedText(input.Transport, 40)
	input.ConversationRef = mgmtBoundedText(input.ConversationRef, 500)
	input.SenderRef = mgmtBoundedText(input.SenderRef, 500)
	input.Message = mgmtBoundedText(input.Message, 4000)
	input.LegacyModel = mgmtBoundedText(input.LegacyModel, 200)
	if input.Transport == "" || input.ConversationRef == "" {
		return mgmtShadowCreated{}, coreInvalid("shadow event transport and conversationRef are required")
	}
	lane := strings.TrimSpace(input.Lane)
	if lane == "" {
		lane = inferNativeLane(input.Message, input.HasImage, input.HasAudio)
	}
	if _, ok := defaultLaneCapabilities[lane]; !ok {
		return mgmtShadowCreated{}, coreInvalid("unknown routing lane: " + lane)
	}
	maxHealthAge := input.MaxHealthAgeMS
	if maxHealthAge == nil {
		defaultAge := float64(15 * 60 * 1000)
		maxHealthAge = &defaultAge
	}
	route, err := s.mgmtSimulateRouteInput(mgmtRoutePayload{Lane: lane, MaxHealthAgeMS: maxHealthAge})
	if err != nil {
		return mgmtShadowCreated{}, err
	}
	createdAt := mgmtNow()
	var selectedID any
	if route.Selected != nil {
		selectedID = route.Selected.Endpoint.ID
	}
	result, err := s.db.Exec(`
		INSERT INTO shadow_interactions (
			transport, conversation_hash, sender_hash, message_length, has_image, has_audio,
			legacy_model, lane, selected_endpoint_id, route_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, input.Transport, mgmtPseudonym("conversation", input.ConversationRef),
		mgmtPseudonym("sender", input.SenderRef), utf8.RuneCountInString(input.Message),
		boolInt(input.HasImage), boolInt(input.HasAudio), input.LegacyModel, lane,
		selectedID, mgmtJSON(route), createdAt)
	if err != nil {
		return mgmtShadowCreated{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return mgmtShadowCreated{}, err
	}
	return mgmtShadowCreated{
		ID: id, Lane: lane, Selected: route.Selected, Fallbacks: route.Fallbacks,
		Rejected: route.Rejected, CreatedAt: createdAt,
	}, nil
}

func (s *coreConfigStore) mgmtShadowList(r *http.Request) ([]mgmtShadowRecord, error) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.db.Query(`
		SELECT id, transport, conversation_hash, sender_hash, message_length,
			has_image, has_audio, legacy_model, lane, selected_endpoint_id,
			route_json, created_at
		FROM shadow_interactions ORDER BY id DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtShadowRecord{}
	for rows.Next() {
		var value mgmtShadowRecord
		var hasImage, hasAudio int
		var selectedID sql.NullString
		var routeJSON string
		if err = rows.Scan(
			&value.ID, &value.Transport, &value.ConversationHash, &value.SenderHash,
			&value.MessageLength, &hasImage, &hasAudio, &value.LegacyModel, &value.Lane,
			&selectedID, &routeJSON, &value.CreatedAt,
		); err != nil {
			return nil, err
		}
		value.HasImage = hasImage == 1
		value.HasAudio = hasAudio == 1
		if selectedID.Valid {
			value.SelectedEndpointID = &selectedID.String
		}
		var route nativeRouteDecision
		if json.Unmarshal([]byte(routeJSON), &route) == nil {
			value.Route = &route
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *coreConfigStore) handleManagementShadow(w http.ResponseWriter, r *http.Request) error {
	switch r.Method {
	case http.MethodGet:
		values, err := s.mgmtShadowList(r)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	case http.MethodPost:
		value, err := s.mgmtRecordShadow(r)
		if err == nil {
			mgmtWriteData(w, http.StatusCreated, value)
		}
		return err
	default:
		return mgmtMethodNotAllowed()
	}
}
