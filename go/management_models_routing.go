package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

var (
	mgmtKnownCapabilities = map[string]struct{}{
		"chat": {}, "reasoning": {}, "vision": {}, "tool_calling": {},
		"json_output": {}, "long_context": {}, "web_search": {},
		"image_generation": {}, "video_generation": {}, "embedding": {}, "rerank": {}, "code": {},
	}
	mgmtIdentifierPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	mgmtUTCTimePattern    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$`)
)

func mgmtNow() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

func mgmtIdentifier(value, name string) (string, error) {
	value, err := normalizeCoreText(value, name, 120, true)
	if err != nil {
		return "", err
	}
	if !mgmtIdentifierPattern.MatchString(value) {
		return "", coreInvalid(name + " may contain only letters, numbers, dot, underscore, and dash")
	}
	return value, nil
}

func mgmtCapabilities(values []string) ([]string, error) {
	values, err := normalizeCoreStrings(values, "capabilities", 32, 120)
	if err != nil {
		return nil, err
	}
	unknown := make([]string, 0)
	for _, value := range values {
		if _, ok := mgmtKnownCapabilities[value]; !ok {
			unknown = append(unknown, value)
		}
	}
	if len(unknown) > 0 {
		return nil, coreInvalid("unknown capabilities: " + strings.Join(unknown, ", "))
	}
	sort.Strings(values)
	return values, nil
}

func mgmtJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *coreConfigStore) mgmtAudit(action, targetType, targetID string, fields []string) error {
	fields = append([]string{}, fields...)
	sort.Strings(fields)
	_, err := s.db.Exec(`
		INSERT INTO audit_events (actor, action, target_type, target_id, details_json, created_at)
		VALUES ('admin', ?, ?, ?, ?, ?)
	`, action, targetType, targetID, mgmtJSON(map[string]any{"fields": fields}), mgmtNow())
	return err
}

type mgmtModelHealth struct {
	EndpointID          string  `json:"endpointId,omitempty"`
	Status              string  `json:"status"`
	StatusMessage       string  `json:"statusMessage,omitempty"`
	Healthy             *bool   `json:"healthy,omitempty"`
	LatencyMS           *int    `json:"latencyMs"`
	ErrorRate           float64 `json:"errorRate"`
	ConsecutiveFailures int     `json:"consecutiveFailures"`
	CheckedAt           string  `json:"checkedAt"`
}

type mgmtModelEndpoint struct {
	ID                   string               `json:"id"`
	Provider             string               `json:"provider"`
	Model                string               `json:"model"`
	Enabled              bool                 `json:"enabled"`
	Capabilities         []string             `json:"capabilities"`
	InputCostPerMillion  float64              `json:"inputCostPerMillion"`
	OutputCostPerMillion float64              `json:"outputCostPerMillion"`
	PricingSource        string               `json:"pricingSource"`
	PricingCheckedAt     string               `json:"pricingCheckedAt"`
	PricingCurrency      string               `json:"pricingCurrency"`
	QualityScore         float64              `json:"qualityScore"`
	Priority             float64              `json:"priority"`
	MaxContextTokens     int                  `json:"maxContextTokens"`
	ExecutionKind        string               `json:"executionKind"`
	AdapterRef           string               `json:"adapterRef"`
	ConnectionID         string               `json:"connectionId,omitempty"`
	Health               *mgmtModelHealth     `json:"health"`
	Usage                providerUsageSummary `json:"usage"`
	CreatedAt            string               `json:"createdAt"`
	UpdatedAt            string               `json:"updatedAt"`
}

func scanMgmtModel(scanner interface{ Scan(...any) error }) (mgmtModelEndpoint, error) {
	var value mgmtModelEndpoint
	var enabled int
	var capabilities string
	var statusMessage string
	var healthy, latency, failures sql.NullInt64
	var errorRate sql.NullFloat64
	var checkedAt sql.NullString
	err := scanner.Scan(
		&value.ID, &value.Provider, &value.Model, &enabled, &capabilities,
		&value.InputCostPerMillion, &value.OutputCostPerMillion, &value.QualityScore,
		&value.Priority, &value.MaxContextTokens, &value.ExecutionKind, &value.AdapterRef,
		&value.PricingSource, &value.PricingCheckedAt, &value.PricingCurrency,
		&value.CreatedAt, &value.UpdatedAt, &healthy, &latency, &errorRate, &failures,
		&statusMessage, &checkedAt,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.Capabilities = decodeJSONStringList(capabilities)
	if healthy.Valid {
		healthyBool := healthy.Int64 == 1
		health := &mgmtModelHealth{
			Status: "unhealthy", Healthy: &healthyBool, ErrorRate: errorRate.Float64,
			ConsecutiveFailures: int(failures.Int64), StatusMessage: statusMessage,
			CheckedAt: checkedAt.String,
		}
		if healthyBool {
			health.Status = "healthy"
		}
		if latency.Valid {
			latencyValue := int(latency.Int64)
			health.LatencyMS = &latencyValue
		}
		value.Health = health
	}
	return value, nil
}

const mgmtModelSelect = `
	SELECT e.id, e.provider, e.model, e.enabled, e.capabilities_json,
		e.input_cost_per_million, e.output_cost_per_million, e.quality_score,
		e.priority, e.max_context_tokens, e.execution_kind, e.adapter_ref,
		e.pricing_source, e.pricing_checked_at, e.pricing_currency,
		e.created_at, e.updated_at, h.healthy, h.latency_ms, h.error_rate,
		h.consecutive_failures, COALESCE(h.status_message, ''), h.checked_at
	FROM model_endpoints e LEFT JOIN model_health h ON h.endpoint_id = e.id`

func (s *coreConfigStore) mgmtModels() ([]mgmtModelEndpoint, error) {
	rows, err := s.db.Query(mgmtModelSelect + " ORDER BY e.id")
	if err != nil {
		return nil, err
	}
	values := []mgmtModelEndpoint{}
	for rows.Next() {
		value, err := scanMgmtModel(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for index := range values {
		_ = s.db.QueryRow("SELECT connection_id FROM model_endpoint_connections WHERE endpoint_id = ?", values[index].ID).Scan(&values[index].ConnectionID)
		usage, usageErr := providerUsageForEndpoint(s.db, values[index].ID)
		if usageErr != nil {
			return nil, usageErr
		}
		values[index].Usage = usage
	}
	return values, nil
}

func (s *coreConfigStore) mgmtModel(id string) (mgmtModelEndpoint, bool, error) {
	value, err := scanMgmtModel(s.db.QueryRow(mgmtModelSelect+" WHERE e.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err == nil {
		_ = s.db.QueryRow("SELECT connection_id FROM model_endpoint_connections WHERE endpoint_id = ?", id).Scan(&value.ConnectionID)
		value.Usage, err = providerUsageForEndpoint(s.db, id)
	}
	return value, err == nil, err
}

type mgmtModelPayload struct {
	Provider             string   `json:"provider"`
	Model                string   `json:"model"`
	Enabled              *bool    `json:"enabled"`
	Capabilities         []string `json:"capabilities"`
	InputCostPerMillion  *float64 `json:"inputCostPerMillion"`
	OutputCostPerMillion *float64 `json:"outputCostPerMillion"`
	QualityScore         *float64 `json:"qualityScore"`
	Priority             *float64 `json:"priority"`
	MaxContextTokens     *int     `json:"maxContextTokens"`
	ExecutionKind        string   `json:"executionKind"`
	AdapterRef           string   `json:"adapterRef"`
	ConnectionID         string   `json:"connectionId"`
}

var mgmtModelFields = coreFieldSet(
	"provider", "model", "enabled", "capabilities", "inputCostPerMillion",
	"outputCostPerMillion", "qualityScore", "priority", "maxContextTokens",
	"executionKind", "adapterRef", "connectionId",
)

func (s *coreConfigStore) mgmtUpsertModel(r *http.Request, id string) (mgmtModelEndpoint, error) {
	var input mgmtModelPayload
	_, err := decodeCoreObject(r, mgmtModelFields, "model endpoint", &input)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	provider, err := normalizeCoreText(input.Provider, "provider", 160, true)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	model, err := normalizeCoreText(input.Model, "model", 240, true)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	capabilities, err := mgmtCapabilities(input.Capabilities)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	inputCost, outputCost, quality, priority, context := 0.0, 0.0, 0.5, 0.0, 0
	if input.InputCostPerMillion != nil {
		inputCost = *input.InputCostPerMillion
	}
	if input.OutputCostPerMillion != nil {
		outputCost = *input.OutputCostPerMillion
	}
	if input.QualityScore != nil {
		quality = *input.QualityScore
	}
	if input.Priority != nil {
		priority = *input.Priority
	}
	if input.MaxContextTokens != nil {
		context = *input.MaxContextTokens
	}
	for name, value := range map[string]float64{
		"inputCostPerMillion": inputCost, "outputCostPerMillion": outputCost,
		"qualityScore": quality, "priority": priority,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return mgmtModelEndpoint{}, coreInvalid(name + " must be a finite number")
		}
	}
	if inputCost < 0 || outputCost < 0 || context < 0 {
		return mgmtModelEndpoint{}, coreInvalid("model costs and maxContextTokens must be non-negative")
	}
	if quality < 0 || quality > 1 {
		return mgmtModelEndpoint{}, coreInvalid("qualityScore must be between 0 and 1")
	}
	executionKind := strings.TrimSpace(input.ExecutionKind)
	if executionKind == "" {
		executionKind = "llm"
	}
	if executionKind != "llm" && executionKind != "tool" && executionKind != "media" {
		return mgmtModelEndpoint{}, coreInvalid("executionKind must be llm, tool or media")
	}
	adapterRef, err := normalizeCoreText(input.AdapterRef, "adapterRef", 240, false)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	connectionID, err := normalizeCoreText(input.ConnectionID, "connectionId", 240, false)
	if err != nil {
		return mgmtModelEndpoint{}, err
	}
	if connectionID != "" {
		var connectionProvider string
		if err = s.db.QueryRow("SELECT provider FROM provider_connections WHERE id = ? AND enabled = 1", connectionID).Scan(&connectionProvider); errors.Is(err, sql.ErrNoRows) {
			return mgmtModelEndpoint{}, coreInvalid("provider connection does not exist or is disabled")
		} else if err != nil {
			return mgmtModelEndpoint{}, err
		}
		if connectionProvider != provider {
			return mgmtModelEndpoint{}, coreInvalid("model provider must match its provider connection")
		}
	}
	now := mgmtNow()
	pricingSource, pricingCheckedAt := "", ""
	if input.InputCostPerMillion != nil || input.OutputCostPerMillion != nil {
		pricingSource, pricingCheckedAt = "manual", now
	}
	_, err = s.db.Exec(`
		INSERT INTO model_endpoints (
			id, provider, model, enabled, capabilities_json, input_cost_per_million,
			output_cost_per_million, quality_score, priority, max_context_tokens,
			execution_kind, adapter_ref, pricing_source, pricing_checked_at, pricing_currency,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'USD', ?, ?)
		ON CONFLICT(id) DO UPDATE SET provider = excluded.provider, model = excluded.model,
			enabled = excluded.enabled, capabilities_json = excluded.capabilities_json,
			input_cost_per_million = excluded.input_cost_per_million,
			output_cost_per_million = excluded.output_cost_per_million,
			quality_score = excluded.quality_score, priority = excluded.priority,
			max_context_tokens = excluded.max_context_tokens,
			execution_kind = excluded.execution_kind, adapter_ref = excluded.adapter_ref,
			pricing_source = CASE WHEN excluded.pricing_source != '' THEN excluded.pricing_source ELSE model_endpoints.pricing_source END,
			pricing_checked_at = CASE WHEN excluded.pricing_checked_at != '' THEN excluded.pricing_checked_at ELSE model_endpoints.pricing_checked_at END,
			updated_at = excluded.updated_at
	`, id, provider, model, boolInt(enabled), mgmtJSON(capabilities), inputCost, outputCost,
		quality, priority, context, executionKind, adapterRef, pricingSource, pricingCheckedAt, now, now)
	if err != nil {
		return mgmtModelEndpoint{}, mgmtConstraintError(err, "model endpoint id or provider/model already exists")
	}
	if inputCost == 0 && outputCost == 0 {
		if _, err = s.db.Exec(`UPDATE model_endpoints SET pricing_source = '', pricing_checked_at = '' WHERE id = ?`, id); err != nil {
			return mgmtModelEndpoint{}, err
		}
	}
	if connectionID != "" {
		if _, err = s.db.Exec(`INSERT INTO model_endpoint_connections (endpoint_id, connection_id, updated_at)
			VALUES (?, ?, ?) ON CONFLICT(endpoint_id) DO UPDATE SET connection_id=excluded.connection_id, updated_at=excluded.updated_at`, id, connectionID, now); err != nil {
			return mgmtModelEndpoint{}, mgmtConstraintError(err, "provider connection does not exist")
		}
	} else {
		_, err = s.db.Exec("DELETE FROM model_endpoint_connections WHERE endpoint_id = ?", id)
		if err != nil {
			return mgmtModelEndpoint{}, err
		}
	}
	value, _, err := s.mgmtModel(id)
	return value, err
}

func (s *coreConfigStore) handleManagementModels(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/model-endpoints" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		values, err := s.mgmtModels()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	id, err := mgmtPathID(path, "/api/v1/model-endpoints/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtModel(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("model endpoint")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, err := s.mgmtUpsertModel(r, id)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM model_endpoints WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "model endpoint"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func scanMgmtHealth(scanner interface{ Scan(...any) error }) (mgmtModelHealth, error) {
	var value mgmtModelHealth
	var healthy int
	var latency sql.NullInt64
	err := scanner.Scan(
		&value.EndpointID, &healthy, &latency, &value.ErrorRate,
		&value.ConsecutiveFailures, &value.StatusMessage, &value.CheckedAt,
	)
	if err != nil {
		return value, err
	}
	value.Status = "unhealthy"
	if healthy == 1 {
		value.Status = "healthy"
	}
	if latency.Valid {
		latencyValue := int(latency.Int64)
		value.LatencyMS = &latencyValue
	}
	return value, nil
}

func (s *coreConfigStore) mgmtHealthList() ([]mgmtModelHealth, error) {
	rows, err := s.db.Query(`
		SELECT endpoint_id, healthy, latency_ms, error_rate, consecutive_failures,
			status_message, checked_at
		FROM model_health ORDER BY endpoint_id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtModelHealth{}
	for rows.Next() {
		value, err := scanMgmtHealth(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type mgmtHealthPayload struct {
	Status        string   `json:"status"`
	StatusMessage string   `json:"statusMessage"`
	LatencyMS     *int     `json:"latencyMs"`
	ErrorRate     *float64 `json:"errorRate"`
	CheckedAt     string   `json:"checkedAt"`
}

func (s *coreConfigStore) handleManagementHealth(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/model-health" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		values, err := s.mgmtHealthList()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	if strings.HasSuffix(path, "/history") {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		id, err := mgmtPathID(strings.TrimSuffix(path, "/history"), "/api/v1/model-health/")
		if err != nil {
			return err
		}
		rows, err := s.db.Query(`SELECT id, endpoint_id, healthy, latency_ms, error_rate, status_message, checked_at
			FROM model_health_samples WHERE endpoint_id = ? ORDER BY id DESC LIMIT 200`, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		values := []map[string]any{}
		for rows.Next() {
			var sampleID int64
			var endpointID, message, checkedAt string
			var healthy int
			var latency sql.NullInt64
			var errorRate float64
			if err := rows.Scan(&sampleID, &endpointID, &healthy, &latency, &errorRate, &message, &checkedAt); err != nil {
				return err
			}
			values = append(values, map[string]any{
				"id": sampleID, "endpointId": endpointID, "healthy": healthy == 1,
				"latencyMs": nullableInt(latency), "errorRate": errorRate,
				"statusMessage": message, "checkedAt": checkedAt,
			})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, values)
		return nil
	}
	if r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	id, err := mgmtPathID(path, "/api/v1/model-health/")
	if err != nil {
		return err
	}
	var input mgmtHealthPayload
	fields, err := decodeCoreObject(
		r, coreFieldSet("status", "statusMessage", "latencyMs", "errorRate", "checkedAt"),
		"model health", &input,
	)
	if err != nil {
		return err
	}
	for _, required := range []string{"status", "latencyMs", "errorRate", "checkedAt"} {
		if _, ok := fields[required]; !ok {
			return coreInvalid("status, latencyMs, errorRate and checkedAt are required")
		}
	}
	if input.Status != "healthy" && input.Status != "unhealthy" {
		return coreInvalid("status must be healthy or unhealthy")
	}
	input.StatusMessage, err = normalizeCoreText(input.StatusMessage, "statusMessage", 120, false)
	if err != nil {
		return err
	}
	if input.Status == "healthy" {
		input.StatusMessage = ""
	}
	if input.LatencyMS != nil && *input.LatencyMS < 0 {
		return coreInvalid("latencyMs must be a non-negative integer or null")
	}
	if input.ErrorRate == nil || math.IsNaN(*input.ErrorRate) || math.IsInf(*input.ErrorRate, 0) || *input.ErrorRate < 0 || *input.ErrorRate > 1 {
		return coreInvalid("errorRate must be a number between 0 and 1")
	}
	if !mgmtUTCTimePattern.MatchString(input.CheckedAt) {
		return coreInvalid("checkedAt must be an ISO-8601 UTC timestamp")
	}
	checkedAt, err := time.Parse(time.RFC3339Nano, input.CheckedAt)
	if err != nil {
		return coreInvalid("checkedAt must be an ISO-8601 UTC timestamp")
	}
	var exists int
	if err = s.db.QueryRow("SELECT 1 FROM model_endpoints WHERE id = ?", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("model endpoint")
	} else if err != nil {
		return err
	}
	checkedAtValue := checkedAt.UTC().Format("2006-01-02T15:04:05.000Z")
	_, err = s.db.Exec(`INSERT INTO model_health_samples
		(endpoint_id, healthy, latency_ms, error_rate, status_message, checked_at)
		VALUES (?, ?, ?, ?, ?, ?)`, id, boolInt(input.Status == "healthy"), input.LatencyMS,
		*input.ErrorRate, input.StatusMessage, checkedAtValue)
	if err != nil {
		return err
	}
	value := mgmtModelHealth{
		EndpointID: id, Status: input.Status, StatusMessage: input.StatusMessage,
		LatencyMS: input.LatencyMS, ErrorRate: *input.ErrorRate, CheckedAt: checkedAtValue,
	}
	healthy := input.Status == "healthy"
	value.Healthy = &healthy
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func nullableInt(value sql.NullInt64) any {
	if !value.Valid {
		return nil
	}
	return value.Int64
}

type mgmtRoutingControl struct {
	Mode      string            `json:"mode"`
	Locks     map[string]string `json:"locks"`
	UpdatedAt string            `json:"updatedAt"`
}

func (s *coreConfigStore) mgmtRoutingControl() (mgmtRoutingControl, error) {
	value := mgmtRoutingControl{Locks: map[string]string{}}
	if err := s.db.QueryRow("SELECT mode, updated_at FROM routing_control WHERE id = 1").Scan(&value.Mode, &value.UpdatedAt); err != nil {
		return value, err
	}
	rows, err := s.db.Query("SELECT lane, endpoint_id FROM routing_lane_locks ORDER BY lane")
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var lane, endpointID string
		if err = rows.Scan(&lane, &endpointID); err != nil {
			return value, err
		}
		value.Locks[lane] = endpointID
	}
	return value, rows.Err()
}

type mgmtRoutingControlPayload struct {
	Mode  string            `json:"mode"`
	Locks map[string]string `json:"locks"`
}

func (s *coreConfigStore) mgmtSetRoutingControl(r *http.Request) (mgmtRoutingControl, error) {
	var input mgmtRoutingControlPayload
	fields, err := decodeCoreObject(r, coreFieldSet("mode", "locks"), "routing control", &input)
	if err != nil {
		return mgmtRoutingControl{}, err
	}
	if _, ok := fields["mode"]; !ok || (input.Mode != "auto" && input.Mode != "manual") {
		return mgmtRoutingControl{}, coreInvalid("routing mode must be auto or manual")
	}
	if _, ok := fields["locks"]; !ok || input.Locks == nil {
		return mgmtRoutingControl{}, coreInvalid("routing locks must be an object")
	}
	for lane, endpointID := range input.Locks {
		if _, ok := defaultLaneCapabilities[lane]; !ok {
			return mgmtRoutingControl{}, coreInvalid("unknown routing lane: " + lane)
		}
		endpointID = strings.TrimSpace(endpointID)
		if endpointID == "" {
			return mgmtRoutingControl{}, coreInvalid("routing lock for " + lane + " requires an endpoint id")
		}
		var exists int
		if err = s.db.QueryRow("SELECT 1 FROM model_endpoints WHERE id = ?", endpointID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return mgmtRoutingControl{}, coreInvalid("routing lock endpoint does not exist: " + endpointID)
		} else if err != nil {
			return mgmtRoutingControl{}, err
		}
		input.Locks[lane] = endpointID
	}
	tx, err := s.db.Begin()
	if err != nil {
		return mgmtRoutingControl{}, err
	}
	defer tx.Rollback()
	now := mgmtNow()
	if _, err = tx.Exec("UPDATE routing_control SET mode = ?, updated_at = ? WHERE id = 1", input.Mode, now); err != nil {
		return mgmtRoutingControl{}, err
	}
	if _, err = tx.Exec("DELETE FROM routing_lane_locks"); err != nil {
		return mgmtRoutingControl{}, err
	}
	lanes := make([]string, 0, len(input.Locks))
	for lane := range input.Locks {
		lanes = append(lanes, lane)
	}
	sort.Strings(lanes)
	for _, lane := range lanes {
		if _, err = tx.Exec(`
			INSERT INTO routing_lane_locks (lane, endpoint_id, updated_at) VALUES (?, ?, ?)
		`, lane, input.Locks[lane], now); err != nil {
			return mgmtRoutingControl{}, err
		}
	}
	if err = tx.Commit(); err != nil {
		return mgmtRoutingControl{}, err
	}
	return s.mgmtRoutingControl()
}

type mgmtLaneProfile struct {
	Lane                  string   `json:"lane"`
	RequiredCapabilities  []string `json:"requiredCapabilities"`
	PreferredCapabilities []string `json:"preferredCapabilities"`
	UpdatedAt             string   `json:"updatedAt"`
}

func (s *coreConfigStore) mgmtLaneProfiles() ([]mgmtLaneProfile, error) {
	rows, err := s.db.Query(`
		SELECT lane, required_capabilities_json, preferred_capabilities_json, updated_at
		FROM routing_lane_profiles ORDER BY lane
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtLaneProfile{}
	for rows.Next() {
		var value mgmtLaneProfile
		var required, preferred string
		if err = rows.Scan(&value.Lane, &required, &preferred, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.RequiredCapabilities = decodeJSONStringList(required)
		value.PreferredCapabilities = decodeJSONStringList(preferred)
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *coreConfigStore) mgmtSetLaneProfile(r *http.Request) (mgmtLaneProfile, error) {
	var input mgmtLaneProfile
	_, err := decodeCoreObject(
		r, coreFieldSet("lane", "requiredCapabilities", "preferredCapabilities"),
		"routing lane profile", &input,
	)
	if err != nil {
		return mgmtLaneProfile{}, err
	}
	input.Lane = strings.TrimSpace(input.Lane)
	if _, ok := defaultLaneCapabilities[input.Lane]; !ok {
		return mgmtLaneProfile{}, coreInvalid("unknown routing lane: " + input.Lane)
	}
	if input.RequiredCapabilities, err = mgmtCapabilities(input.RequiredCapabilities); err != nil {
		return mgmtLaneProfile{}, err
	}
	if input.PreferredCapabilities, err = mgmtCapabilities(input.PreferredCapabilities); err != nil {
		return mgmtLaneProfile{}, err
	}
	input.UpdatedAt = mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO routing_lane_profiles (lane, required_capabilities_json, preferred_capabilities_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(lane) DO UPDATE SET required_capabilities_json = excluded.required_capabilities_json,
			preferred_capabilities_json = excluded.preferred_capabilities_json, updated_at = excluded.updated_at
	`, input.Lane, mgmtJSON(input.RequiredCapabilities), mgmtJSON(input.PreferredCapabilities), input.UpdatedAt)
	return input, err
}

type mgmtRoutePayload struct {
	Lane                         string   `json:"lane"`
	RequiredCapabilities         []string `json:"requiredCapabilities"`
	PreferredCapabilities        []string `json:"preferredCapabilities"`
	MinimumContextTokens         *int     `json:"minimumContextTokens"`
	MaximumLatencyMS             *float64 `json:"maximumLatencyMs"`
	MaximumBlendedCostPerMillion *float64 `json:"maximumBlendedCostPerMillion"`
	MaxHealthAgeMS               *float64 `json:"maxHealthAgeMs"`
}

func (s *coreConfigStore) mgmtSimulateRoute(r *http.Request) (nativeRouteDecision, error) {
	var input mgmtRoutePayload
	_, err := decodeCoreObject(r, coreFieldSet(
		"lane", "requiredCapabilities", "preferredCapabilities", "minimumContextTokens",
		"maximumLatencyMs", "maximumBlendedCostPerMillion", "maxHealthAgeMs",
	), "routing simulation", &input)
	if err != nil {
		return nativeRouteDecision{}, err
	}
	return s.mgmtSimulateRouteInput(input)
}

func (s *coreConfigStore) mgmtSimulateRouteInput(input mgmtRoutePayload) (nativeRouteDecision, error) {
	lane := strings.TrimSpace(input.Lane)
	if lane == "" {
		lane = "chat"
	}
	if _, ok := defaultLaneCapabilities[lane]; !ok {
		return nativeRouteDecision{}, coreInvalid("unknown routing lane: " + lane)
	}
	baseRequired, basePreferred, err := s.laneCapabilities(lane)
	if err != nil {
		return nativeRouteDecision{}, err
	}
	extraRequired, err := mgmtCapabilities(input.RequiredCapabilities)
	if err != nil {
		return nativeRouteDecision{}, err
	}
	extraPreferred, err := mgmtCapabilities(input.PreferredCapabilities)
	if err != nil {
		return nativeRouteDecision{}, err
	}
	minimumContext := 0
	if input.MinimumContextTokens != nil {
		minimumContext = *input.MinimumContextTokens
	}
	if minimumContext < 0 {
		return nativeRouteDecision{}, coreInvalid("minimumContextTokens must be a non-negative number")
	}
	for name, value := range map[string]*float64{
		"maximumLatencyMs":             input.MaximumLatencyMS,
		"maximumBlendedCostPerMillion": input.MaximumBlendedCostPerMillion,
		"maxHealthAgeMs":               input.MaxHealthAgeMS,
	} {
		if value != nil && (math.IsNaN(*value) || math.IsInf(*value, 0) || *value < 0) {
			return nativeRouteDecision{}, coreInvalid(name + " must be a non-negative number")
		}
	}
	required := uniqueNativeStrings(baseRequired, extraRequired)
	preferred := uniqueNativeStrings(basePreferred, extraPreferred)
	decision := nativeRouteDecision{
		Lane: lane,
		Constraints: nativeRouteConstraints{
			RequiredCapabilities: required, PreferredCapabilities: preferred,
			MinimumContextTokens: minimumContext, MaximumLatencyMS: input.MaximumLatencyMS,
			MaximumBlendedCostPerMillion: input.MaximumBlendedCostPerMillion,
			MaxHealthAgeMS:               input.MaxHealthAgeMS,
		},
		Fallbacks: []nativeRouteCandidate{}, Rejected: []nativeRouteRejection{},
	}
	control, err := s.mgmtRoutingControl()
	if err != nil {
		return decision, err
	}
	decision.OperatorMode = control.Mode
	rows, err := s.db.Query(`
		SELECT e.id, e.provider, e.model, e.capabilities_json, e.input_cost_per_million,
			e.output_cost_per_million, e.quality_score, e.priority, e.max_context_tokens,
			e.execution_kind, e.adapter_ref, h.healthy, h.latency_ms, h.error_rate, h.checked_at
		FROM model_endpoints e LEFT JOIN model_health h ON h.endpoint_id = e.id
		WHERE e.enabled = 1 ORDER BY e.id
	`)
	if err != nil {
		return decision, err
	}
	defer rows.Close()
	candidates := []nativeRouteCandidate{}
	now := time.Now().UTC()
	for rows.Next() {
		endpoint, _, err := scanNativeEndpoint(rows)
		if err != nil {
			return decision, err
		}
		reasons := []string{}
		missing := []string{}
		for _, capability := range required {
			if !containsNativeString(endpoint.Capabilities, capability) {
				missing = append(missing, capability)
			}
		}
		if len(missing) > 0 {
			reasons = append(reasons, "missing capabilities: "+strings.Join(missing, ", "))
		}
		if endpoint.Health == "unhealthy" {
			reasons = append(reasons, "endpoint is unhealthy")
		}
		if endpoint.HealthCheckedAt != nil {
			if checkedAt, parseErr := time.Parse(time.RFC3339Nano, *endpoint.HealthCheckedAt); parseErr == nil && time.Since(checkedAt) > 5*time.Minute {
				reasons = append(reasons, "health record is stale: older than 5m")
			}
		}
		if minimumContext > 0 && endpoint.MaxContextTokens < minimumContext {
			reasons = append(reasons, fmt.Sprintf("context %d < %d", endpoint.MaxContextTokens, minimumContext))
		}
		if input.MaximumLatencyMS != nil && endpoint.LatencyMS != nil && float64(*endpoint.LatencyMS) > *input.MaximumLatencyMS {
			reasons = append(reasons, fmt.Sprintf("latency %dms > %gms", *endpoint.LatencyMS, *input.MaximumLatencyMS))
		}
		blendedCost := (endpoint.InputCostPerMillion + endpoint.OutputCostPerMillion) / 2
		if input.MaximumBlendedCostPerMillion != nil && blendedCost > *input.MaximumBlendedCostPerMillion {
			reasons = append(reasons, fmt.Sprintf("blended cost %g > %g", blendedCost, *input.MaximumBlendedCostPerMillion))
		}
		if input.MaxHealthAgeMS != nil && endpoint.HealthCheckedAt != nil {
			checkedAt, parseErr := time.Parse(time.RFC3339Nano, *endpoint.HealthCheckedAt)
			if parseErr != nil {
				reasons = append(reasons, "health record has an invalid checkedAt timestamp")
			} else {
				age := math.Max(0, float64(now.Sub(checkedAt)/time.Millisecond))
				if age > *input.MaxHealthAgeMS {
					reasons = append(reasons, fmt.Sprintf("health record is stale: %gms > %gms", age, *input.MaxHealthAgeMS))
				}
			}
		}
		if len(reasons) > 0 {
			decision.Rejected = append(decision.Rejected, nativeRouteRejection{Endpoint: endpoint, Reasons: reasons})
		} else {
			candidates = append(candidates, nativeRouteCandidate{Endpoint: endpoint, Score: scoreNativeEndpoint(endpoint, preferred)})
		}
	}
	if err = rows.Err(); err != nil {
		return decision, err
	}
	if err = rows.Close(); err != nil {
		return decision, err
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score.Total == candidates[j].Score.Total {
			return candidates[i].Endpoint.ID < candidates[j].Endpoint.ID
		}
		return candidates[i].Score.Total > candidates[j].Score.Total
	})
	if control.Mode == "manual" {
		lockedID := control.Locks[lane]
		if lockedID == "" {
			return decision, coreInvalid("manual routing has no endpoint lock for lane: " + lane)
		}
		for _, candidate := range candidates {
			if candidate.Endpoint.ID == lockedID {
				selected := candidate
				decision.Selected = &selected
				decision.Explanation = fmt.Sprintf("Selected manually locked endpoint %s for lane %s; automatic scoring and fallback were bypassed.", lockedID, lane)
				return decision, nil
			}
		}
		for _, rejected := range decision.Rejected {
			if rejected.Endpoint.ID == lockedID {
				return decision, coreInvalid(fmt.Sprintf("manual endpoint %s rejected for lane %s: %s", lockedID, lane, strings.Join(rejected.Reasons, "; ")))
			}
		}
		var enabled int
		if err = s.db.QueryRow("SELECT enabled FROM model_endpoints WHERE id = ?", lockedID).Scan(&enabled); errors.Is(err, sql.ErrNoRows) {
			return decision, coreInvalid(fmt.Sprintf("manual endpoint %s rejected for lane %s: endpoint does not exist", lockedID, lane))
		} else if err != nil {
			return decision, err
		}
		return decision, coreInvalid(fmt.Sprintf("manual endpoint %s rejected for lane %s: endpoint is disabled", lockedID, lane))
	}
	if len(candidates) > 0 {
		selected := candidates[0]
		decision.Selected = &selected
		decision.Fallbacks = append(decision.Fallbacks, candidates[1:]...)
		decision.Explanation = fmt.Sprintf(
			"Selected %s from %d eligible endpoint(s) by quality, reliability, latency, cost, priority and preferred-capability score.",
			selected.Endpoint.ID, len(candidates),
		)
	} else {
		decision.Explanation = fmt.Sprintf("No endpoint satisfies all hard constraints; %d endpoint(s) rejected.", len(decision.Rejected))
	}
	return decision, nil
}

func (s *coreConfigStore) handleManagementRouting(w http.ResponseWriter, r *http.Request, path string) error {
	switch path {
	case "/api/v1/routing/control":
		if r.Method == http.MethodGet {
			value, err := s.mgmtRoutingControl()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		}
		if r.Method == http.MethodPut {
			value, err := s.mgmtSetRoutingControl(r)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		}
	case "/api/v1/routing/lanes":
		if r.Method == http.MethodGet {
			values, err := s.mgmtLaneProfiles()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		}
		if r.Method == http.MethodPut {
			value, err := s.mgmtSetLaneProfile(r)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		}
	case "/api/v1/routing/simulate":
		if r.Method == http.MethodPost {
			value, err := s.mgmtSimulateRoute(r)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		}
	}
	return mgmtMethodNotAllowed()
}
