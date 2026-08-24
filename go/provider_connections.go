package main

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"
)

type providerConnectionView struct {
	ID              string               `json:"id"`
	Provider        string               `json:"provider"`
	Protocol        string               `json:"protocol"`
	APIBase         string               `json:"apiBase"`
	CredentialRef   string               `json:"credentialRef"`
	PricingURL      string               `json:"pricingUrl"`
	CredentialReady bool                 `json:"credentialConfigured"`
	TimeoutSeconds  int                  `json:"timeoutSeconds"`
	Enabled         bool                 `json:"enabled"`
	Usage           providerUsageSummary `json:"usage"`
	CreatedAt       string               `json:"createdAt"`
	UpdatedAt       string               `json:"updatedAt"`
}

type providerConnectionPayload struct {
	Provider       *string `json:"provider"`
	Protocol       *string `json:"protocol"`
	APIBase        *string `json:"apiBase"`
	CredentialRef  *string `json:"credentialRef"`
	PricingURL     *string `json:"pricingUrl"`
	TimeoutSeconds *int    `json:"timeoutSeconds"`
	Enabled        *bool   `json:"enabled"`
}

func (s *coreConfigStore) listProviderConnections() ([]providerConnectionView, error) {
	rows, err := s.db.Query(`SELECT id, provider, protocol, api_base, credential_ref, pricing_url,
		timeout_seconds, enabled, created_at, updated_at FROM provider_connections ORDER BY provider`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []providerConnectionView{}
	for rows.Next() {
		var value providerConnectionView
		var enabled int
		if err := rows.Scan(&value.ID, &value.Provider, &value.Protocol, &value.APIBase,
			&value.CredentialRef, &value.PricingURL, &value.TimeoutSeconds, &enabled, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.Enabled = enabled == 1
		value.CredentialReady = strings.TrimSpace(value.CredentialRef) != "" && strings.TrimSpace(getenv(value.CredentialRef)) != ""
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range values {
		usage, err := providerUsageForConnection(s.db, values[index].ID)
		if err != nil {
			return nil, err
		}
		values[index].Usage = usage
	}
	return values, nil
}

func getenv(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	value, _ := os.LookupEnv(name)
	return strings.TrimSpace(value)
}

func (s *coreConfigStore) handleProviderConnections(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/provider-connections" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		values, err := s.listProviderConnections()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	if !strings.HasPrefix(path, "/api/v1/provider-connections/") {
		return mgmtNotFound("provider connection")
	}
	rest := strings.TrimPrefix(path, "/api/v1/provider-connections/")
	if strings.HasSuffix(rest, "/test") || strings.HasSuffix(rest, "/pricing-sync") {
		return mgmtNotFound("provider connection action")
	}
	id, err := parseCorePathID(rest)
	if err != nil {
		return err
	}
	if r.Method == http.MethodDelete {
		result, err := s.db.Exec("DELETE FROM provider_connections WHERE id = ?", id)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed == 0 {
			return mgmtNotFound("provider connection")
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	}
	if r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	var input providerConnectionPayload
	if _, err := decodeCoreObject(r, coreFieldSet("provider", "protocol", "apiBase", "credentialRef", "pricingUrl", "timeoutSeconds", "enabled"), "provider connection", &input); err != nil {
		return err
	}
	var current providerConnectionView
	var enabled int
	err = s.db.QueryRow(`SELECT id, provider, protocol, api_base, credential_ref, pricing_url, timeout_seconds,
		enabled, created_at, updated_at FROM provider_connections WHERE id = ?`, id).Scan(
		&current.ID, &current.Provider, &current.Protocol, &current.APIBase, &current.CredentialRef,
		&current.PricingURL, &current.TimeoutSeconds, &enabled, &current.CreatedAt, &current.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		current = providerConnectionView{ID: id, Protocol: "openai_chat_completion", TimeoutSeconds: 120, Enabled: true}
	} else if err != nil {
		return err
	} else {
		current.Enabled = enabled == 1
	}
	if input.Provider != nil {
		current.Provider = strings.TrimSpace(*input.Provider)
	}
	if input.Protocol != nil {
		current.Protocol = strings.TrimSpace(*input.Protocol)
	}
	if input.APIBase != nil {
		current.APIBase = strings.TrimRight(strings.TrimSpace(*input.APIBase), "/")
	}
	if input.CredentialRef != nil {
		current.CredentialRef = strings.TrimSpace(*input.CredentialRef)
	}
	if input.PricingURL != nil {
		current.PricingURL = strings.TrimSpace(*input.PricingURL)
	}
	if input.TimeoutSeconds != nil {
		current.TimeoutSeconds = *input.TimeoutSeconds
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if current.Provider == "" || current.APIBase == "" || current.TimeoutSeconds < 1 || current.TimeoutSeconds > 600 {
		return coreInvalid("provider, apiBase and timeoutSeconds are required; timeout must be 1-600 seconds")
	}
	switch current.Protocol {
	case "openai_chat_completion", "openai_chat_rerank", "openai_compatible", "openai_embeddings", "cohere_rerank", "xai_responses":
	default:
		return coreInvalid("protocol is not supported")
	}
	if current.APIBase, err = secureServiceBase(current.APIBase); err != nil {
		return coreInvalid("apiBase must use HTTPS or an approved private HTTP host")
	}
	if current.CredentialRef != "" && !validRuntimeCredentialReference(current.CredentialRef) {
		return coreInvalid("credentialRef is invalid")
	}
	if current.PricingURL != "" {
		if current.PricingURL, err = secureServiceBase(current.PricingURL); err != nil {
			return coreInvalid("pricingUrl must use HTTPS or an approved private HTTP host")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, pricing_url, timeout_seconds, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, protocol=excluded.protocol,
		api_base=excluded.api_base, credential_ref=excluded.credential_ref, pricing_url=excluded.pricing_url,
		timeout_seconds=excluded.timeout_seconds, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		id, current.Provider, current.Protocol, current.APIBase, current.CredentialRef, current.PricingURL,
		current.TimeoutSeconds, boolInt(current.Enabled), coalesceTime(current.CreatedAt, now), now)
	if err != nil {
		return mgmtConstraintError(err, "provider connection already exists")
	}
	current.UpdatedAt = now
	current.CreatedAt = coalesceTime(current.CreatedAt, now)
	current.CredentialReady = getenv(current.CredentialRef) != ""
	mgmtWriteData(w, http.StatusOK, current)
	return nil
}

func (a *AgentRuntime) handleProviderConnectionTest(w http.ResponseWriter, r *http.Request, path string) error {
	if r.Method != http.MethodPost {
		return mgmtMethodNotAllowed()
	}
	rest := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/provider-connections/"), "/test")
	id, err := parseCorePathID(rest)
	if err != nil {
		return err
	}
	var provider string
	if err = a.configStore.db.QueryRow("SELECT provider FROM provider_connections WHERE id = ? AND enabled = 1", id).Scan(&provider); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return mgmtNotFound("provider connection")
		}
		return err
	}
	var endpointID, model, executionKind, capabilities string
	if err = a.configStore.db.QueryRow(`SELECT endpoint.id, endpoint.model,
		endpoint.execution_kind, endpoint.capabilities_json
		FROM model_endpoint_connections binding
		JOIN model_endpoints endpoint ON endpoint.id = binding.endpoint_id
		WHERE binding.connection_id = ? AND endpoint.enabled = 1
		ORDER BY CASE WHEN endpoint.execution_kind = 'media' THEN 1 ELSE 0 END,
			endpoint.priority DESC, endpoint.id LIMIT 1`, id).
		Scan(&endpointID, &model, &executionKind, &capabilities); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return coreInvalid("provider connection has no enabled endpoint")
		}
		return err
	}
	_ = a.checkOneProvider(r.Context(), endpointID, provider, model, executionKind, capabilities)
	var routingHealthy, checkedHealthy int
	var latency, failures int
	var message, checkedAt string
	if err = a.configStore.db.QueryRow(`SELECT healthy, latency_ms, consecutive_failures, status_message, checked_at
		FROM model_health WHERE endpoint_id = ?`, endpointID).Scan(&routingHealthy, &latency, &failures, &message, &checkedAt); err != nil {
		return err
	}
	if err = a.configStore.db.QueryRow(`SELECT healthy FROM model_health_samples
		WHERE endpoint_id = ? ORDER BY id DESC LIMIT 1`, endpointID).Scan(&checkedHealthy); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"endpointId": endpointID, "healthy": checkedHealthy == 1,
		"routingEligible": routingHealthy == 1, "latencyMs": latency, "consecutiveFailures": failures,
		"statusMessage": message, "checkedAt": checkedAt})
	return nil
}

func coalesceTime(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
