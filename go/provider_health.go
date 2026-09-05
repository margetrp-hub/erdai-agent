package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

func (a *AgentRuntime) startProviderHealthWorker(ctx context.Context) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.checkProviderHealth(ctx)
			}
		}
	}()
}

func (a *AgentRuntime) checkProviderHealth(ctx context.Context) {
	if a == nil || a.configStore == nil {
		return
	}
	if err := a.configStore.pruneProviderHealthSamples(ctx, time.Now()); err != nil {
		log.Printf("provider health retention failed: %v", err)
	}
	rows, err := a.configStore.db.Query(`SELECT endpoint.id, endpoint.provider, endpoint.model,
		endpoint.execution_kind, endpoint.capabilities_json
		FROM model_endpoints endpoint WHERE endpoint.enabled = 1`)
	if err != nil {
		return
	}
	type endpoint struct{ id, provider, model, executionKind, capabilities string }
	endpoints := []endpoint{}
	for rows.Next() {
		var value endpoint
		if rows.Scan(&value.id, &value.provider, &value.model, &value.executionKind, &value.capabilities) != nil {
			continue
		}
		endpoints = append(endpoints, value)
	}
	rowErr := rows.Err()
	closeErr := rows.Close()
	if rowErr != nil || closeErr != nil {
		return
	}
	for _, value := range endpoints {
		_ = a.checkOneProvider(ctx, value.id, value.provider, value.model, value.executionKind, value.capabilities)
	}
}

func (a *AgentRuntime) checkOneProvider(parent context.Context, endpointID, provider, model, executionKind, capabilities string) error {
	connection, ok, err := a.providerConnectionForEndpoint(endpointID, provider)
	if err != nil {
		return a.recordProviderHealth(endpointID, false, 0, err.Error())
	}
	if !ok || strings.TrimSpace(connection.APIBase) == "" {
		return a.recordProviderHealth(endpointID, false, 0, "provider connection is not configured")
	}
	key := a.providerCredential(connection.CredentialRef)
	if strings.TrimSpace(key) == "" {
		return a.recordProviderHealth(endpointID, false, 0, "provider credential is not configured")
	}
	timeout := time.Duration(connection.TimeoutSeconds) * time.Second
	if timeout <= 0 || timeout > 8*time.Second {
		timeout = 8 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var configuredCapabilities, protocol string
	_ = a.configStore.db.QueryRow(`SELECT e.capabilities_json, c.protocol
		FROM model_endpoints e JOIN model_endpoint_connections binding ON binding.endpoint_id = e.id
		JOIN provider_connections c ON c.id = binding.connection_id WHERE e.id = ?`, endpointID).
		Scan(&configuredCapabilities, &protocol)
	if configuredCapabilities != "" {
		capabilities = configuredCapabilities
	}
	apiPath := "/chat/completions"
	method := http.MethodPost
	requestPayload := map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "ping"}}, "max_tokens": 1, "stream": false}
	if protocol == "xai_responses" {
		// Health must not execute a billable web search. /models verifies
		// authentication and endpoint reachability without tool latency.
		apiPath = "/models"
		method = http.MethodGet
		requestPayload = nil
	} else if executionKind == "media" || nativeMCPListContains(decodeJSONStringList(capabilities), "web_search") {
		apiPath = "/models"
		method = http.MethodGet
		requestPayload = nil
	} else if nativeMCPListContains(decodeJSONStringList(capabilities), "embedding") {
		apiPath = "/embeddings"
		requestPayload = map[string]any{"model": model, "input": []string{"ping"}, "encoding_format": "float"}
	} else if nativeMCPListContains(decodeJSONStringList(capabilities), "rerank") && protocol == "cohere_rerank" {
		apiPath = "/rerank"
		requestPayload = map[string]any{"model": model, "query": "ping", "documents": []string{"ping"}, "top_n": 1}
	} else if nativeMCPListContains(decodeJSONStringList(capabilities), "rerank") && protocol == "openai_chat_rerank" {
		requestPayload = map[string]any{"model": model, "messages": []map[string]string{{"role": "user", "content": "Return {}"}}, "max_tokens": 8, "stream": false}
	}
	var body io.Reader
	if requestPayload != nil {
		payload, _ := json.Marshal(requestPayload)
		body = strings.NewReader(string(payload))
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(connection.APIBase, "/")+apiPath, body)
	if err != nil {
		return a.recordProviderHealth(endpointID, false, 0, "invalid provider URL")
	}
	request.Header.Set("Authorization", "Bearer "+key)
	if requestPayload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	started := time.Now()
	response, err := a.client.Do(request)
	latency := int(time.Since(started).Milliseconds())
	if err != nil {
		return a.recordProviderHealth(endpointID, false, latency, err.Error())
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return a.recordProviderHealth(endpointID, false, latency, fmt.Sprintf("provider returned HTTP %d", response.StatusCode))
	}
	if apiPath == "/models" && strings.TrimSpace(model) != "" {
		var catalog struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 2<<20)).Decode(&catalog); err != nil {
			return a.recordProviderHealth(endpointID, false, latency, "provider model catalog is invalid")
		}
		found := false
		for _, item := range catalog.Data {
			if strings.EqualFold(strings.TrimSpace(item.ID), strings.TrimSpace(model)) {
				found = true
				break
			}
		}
		if !found {
			return a.recordProviderHealth(endpointID, false, latency, "configured model is not exposed by provider")
		}
	}
	return a.recordProviderHealth(endpointID, true, latency, "")
}

func (a *AgentRuntime) recordProviderHealth(endpointID string, healthy bool, latency int, message string) error {
	if a == nil || a.configStore == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	var failures, currentHealthy int
	if err := a.configStore.db.QueryRow("SELECT healthy, consecutive_failures FROM model_health WHERE endpoint_id = ?", endpointID).
		Scan(&currentHealthy, &failures); err != nil {
		currentHealthy = 1
		failures = 0
	}
	routingHealthy := currentHealthy == 1
	if healthy {
		failures = 0
		if !routingHealthy {
			var previousHealthy int
			if err := a.configStore.db.QueryRow(`SELECT healthy FROM model_health_samples
				WHERE endpoint_id = ? ORDER BY id DESC LIMIT 1`, endpointID).Scan(&previousHealthy); err == nil && previousHealthy == 1 {
				routingHealthy = true
			}
		}
	} else {
		failures++
		if failures >= 3 {
			routingHealthy = false
		}
	}
	errorRate := 0.0
	if !healthy {
		errorRate = 1
	}
	_, err := a.configStore.db.Exec(`INSERT INTO model_health
		(endpoint_id, healthy, latency_ms, error_rate, consecutive_failures, status_message, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(endpoint_id) DO UPDATE SET healthy=excluded.healthy, latency_ms=excluded.latency_ms,
		error_rate=excluded.error_rate, consecutive_failures=excluded.consecutive_failures,
		status_message=excluded.status_message, checked_at=excluded.checked_at`, endpointID, boolInt(routingHealthy), latency, errorRate, failures, message, now)
	if err != nil {
		return err
	}
	_, err = a.configStore.db.Exec(`INSERT INTO model_health_samples
		(endpoint_id, healthy, latency_ms, error_rate, status_message, checked_at)
		VALUES (?, ?, ?, ?, ?, ?)`, endpointID, boolInt(healthy), latency, errorRate, message, now)
	return err
}

func (s *coreConfigStore) pruneProviderHealthSamples(ctx context.Context, now time.Time) error {
	cutoff := now.UTC().Add(-14 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := s.db.ExecContext(ctx, `DELETE FROM model_health_samples WHERE id IN
		(SELECT id FROM model_health_samples WHERE checked_at < ? ORDER BY checked_at, id LIMIT 1000)`, cutoff)
	return err
}
