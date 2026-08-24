package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type providerUsageSummary struct {
	Calls              int64   `json:"calls"`
	PromptTokens       int64   `json:"promptTokens"`
	CompletionTokens   int64   `json:"completionTokens"`
	TotalTokens        int64   `json:"totalTokens"`
	EstimatedCost      float64 `json:"estimatedCost"`
	LastUsedAt         string  `json:"lastUsedAt,omitempty"`
	Source             string  `json:"source"`
	TokenDataAvailable bool    `json:"tokenDataAvailable"`
	PricingConfigured  bool    `json:"pricingConfigured"`
}

type providerUsageModelSummary struct {
	Provider         string  `json:"provider"`
	Model            string  `json:"model"`
	Calls            int64   `json:"calls"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	TotalTokens      int64   `json:"totalTokens"`
	EstimatedCost    float64 `json:"estimatedCost"`
	UnpricedCalls    int64   `json:"unpricedCalls"`
	PricingComplete  bool    `json:"pricingComplete"`
}

type providerUsageWindowSummary struct {
	WindowHours        int                         `json:"windowHours"`
	Calls              int64                       `json:"calls"`
	PromptTokens       int64                       `json:"promptTokens"`
	CompletionTokens   int64                       `json:"completionTokens"`
	TotalTokens        int64                       `json:"totalTokens"`
	EstimatedCost      float64                     `json:"estimatedCost"`
	LastUsedAt         string                      `json:"lastUsedAt,omitempty"`
	Source             string                      `json:"source"`
	TokenDataAvailable bool                        `json:"tokenDataAvailable"`
	UnpricedCalls      int64                       `json:"unpricedCalls"`
	PricingComplete    bool                        `json:"pricingComplete"`
	ByModel            []providerUsageModelSummary `json:"byModel"`
}

func ensureProviderUsageTable(db *sql.DB) error {
	if db == nil {
		return errors.New("provider usage database is nil")
	}
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS model_usage_events (
			id TEXT PRIMARY KEY,
			run_id TEXT NOT NULL DEFAULT '',
			endpoint_id TEXT NOT NULL DEFAULT '',
			provider TEXT NOT NULL DEFAULT '',
			model TEXT NOT NULL DEFAULT '',
			prompt_tokens INTEGER NOT NULL DEFAULT 0,
			completion_tokens INTEGER NOT NULL DEFAULT 0,
			total_tokens INTEGER NOT NULL DEFAULT 0,
			input_cost_per_million REAL NOT NULL DEFAULT 0,
			output_cost_per_million REAL NOT NULL DEFAULT 0,
			estimated_cost REAL NOT NULL DEFAULT 0,
			source TEXT NOT NULL DEFAULT 'runtime_observed',
			created_at TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_model_usage_endpoint_created
			ON model_usage_events(endpoint_id, created_at);
		CREATE INDEX IF NOT EXISTS idx_model_usage_provider_created
			ON model_usage_events(provider, created_at);
	`)
	return err
}

func (s *coreConfigStore) handleUsageStats(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	hours := 24
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 336 {
			return coreInvalid("hours must be an integer between 1 and 336")
		}
		hours = parsed
	}
	value, err := providerUsageForWindow(s.db, hours, time.Now().UTC())
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func providerUsageForWindow(db *sql.DB, hours int, now time.Time) (providerUsageWindowSummary, error) {
	value := providerUsageWindowSummary{
		WindowHours: hours,
		Source:      "runtime_observed",
		ByModel:     []providerUsageModelSummary{},
	}
	if err := ensureProviderUsageTable(db); err != nil {
		return value, err
	}
	since := now.Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
	var lastUsed sql.NullString
	var tokenData int
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(estimated_cost), 0), MAX(created_at),
		COALESCE(MAX(CASE WHEN total_tokens > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN total_tokens > 0 AND input_cost_per_million = 0
			AND output_cost_per_million = 0 THEN 1 ELSE 0 END), 0)
		FROM model_usage_events WHERE created_at >= ?`, since).Scan(
		&value.Calls, &value.PromptTokens, &value.CompletionTokens, &value.TotalTokens,
		&value.EstimatedCost, &lastUsed, &tokenData, &value.UnpricedCalls)
	if err != nil {
		return value, err
	}
	value.LastUsedAt = lastUsed.String
	value.TokenDataAvailable = tokenData == 1
	value.PricingComplete = value.Calls == 0 || value.UnpricedCalls == 0

	rows, err := db.Query(`SELECT provider, model, COUNT(*), COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(estimated_cost), 0),
		COALESCE(SUM(CASE WHEN total_tokens > 0 AND input_cost_per_million = 0
			AND output_cost_per_million = 0 THEN 1 ELSE 0 END), 0)
		FROM model_usage_events WHERE created_at >= ?
		GROUP BY provider, model ORDER BY SUM(total_tokens) DESC, provider, model`, since)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for rows.Next() {
		var item providerUsageModelSummary
		if err := rows.Scan(&item.Provider, &item.Model, &item.Calls, &item.PromptTokens,
			&item.CompletionTokens, &item.TotalTokens, &item.EstimatedCost, &item.UnpricedCalls); err != nil {
			return value, err
		}
		item.PricingComplete = item.UnpricedCalls == 0
		value.ByModel = append(value.ByModel, item)
	}
	return value, rows.Err()
}

func (a *AgentRuntime) recordProviderUsage(runID string, target runtimeProviderTarget, usage chatUsage) {
	if a == nil || a.db == nil {
		return
	}
	usageDB := a.db
	if a.configStore != nil && a.configStore.db != nil {
		usageDB = a.configStore.db
	}
	if err := ensureProviderUsageTable(usageDB); err != nil {
		return
	}
	inputTokens, outputTokens, totalTokens := usage.normalized()
	var provider, model string
	provider, model = target.Provider, target.Model
	inputRate, outputRate := 0.0, 0.0
	if target.EndpointID != "" {
		pricingDB := a.db
		if a.configStore != nil && a.configStore.db != nil {
			pricingDB = a.configStore.db
		}
		_ = pricingDB.QueryRow(`SELECT provider, model, input_cost_per_million, output_cost_per_million
			FROM model_endpoints WHERE id = ?`, target.EndpointID).
			Scan(&provider, &model, &inputRate, &outputRate)
	}
	if provider == "" {
		provider = "unknown"
	}
	if model == "" {
		model = "unknown"
	}
	estimatedCost := float64(inputTokens)/1_000_000*inputRate + float64(outputTokens)/1_000_000*outputRate
	id, err := randomID("usage")
	if err != nil {
		return
	}
	_, _ = usageDB.Exec(`INSERT INTO model_usage_events (
		id, run_id, endpoint_id, provider, model, prompt_tokens, completion_tokens,
		total_tokens, input_cost_per_million, output_cost_per_million, estimated_cost,
		source, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'runtime_observed', ?)`,
		id, runID, target.EndpointID, provider, model,
		inputTokens, outputTokens, totalTokens, inputRate, outputRate, estimatedCost,
		time.Now().UTC().Format(time.RFC3339Nano))
}

func providerUsageForEndpoint(db *sql.DB, endpointID string) (providerUsageSummary, error) {
	value, err := providerUsageQuery(db, "endpoint_id = ?", endpointID)
	if err != nil {
		return value, err
	}
	var inputRate, outputRate float64
	if err := db.QueryRow(`SELECT input_cost_per_million, output_cost_per_million
		FROM model_endpoints WHERE id = ?`, endpointID).Scan(&inputRate, &outputRate); err == nil {
		value.PricingConfigured = value.PricingConfigured || inputRate > 0 || outputRate > 0
	}
	return value, nil
}

func providerUsageForProvider(db *sql.DB, provider string) (providerUsageSummary, error) {
	value, err := providerUsageQuery(db, "provider = ?", provider)
	if err != nil {
		return value, err
	}
	var inputRate, outputRate float64
	if err := db.QueryRow(`SELECT COALESCE(MAX(input_cost_per_million), 0),
		COALESCE(MAX(output_cost_per_million), 0) FROM model_endpoints WHERE provider = ?`, provider).
		Scan(&inputRate, &outputRate); err == nil {
		value.PricingConfigured = value.PricingConfigured || inputRate > 0 || outputRate > 0
	}
	return value, nil
}

func providerUsageForConnection(db *sql.DB, connectionID string) (providerUsageSummary, error) {
	value, err := providerUsageQuery(db, `endpoint_id IN (
		SELECT endpoint_id FROM model_endpoint_connections WHERE connection_id = ?
	)`, connectionID)
	if err != nil {
		return value, err
	}
	var inputRate, outputRate float64
	if err := db.QueryRow(`SELECT COALESCE(MAX(endpoint.input_cost_per_million), 0),
		COALESCE(MAX(endpoint.output_cost_per_million), 0)
		FROM model_endpoints endpoint
		JOIN model_endpoint_connections binding ON binding.endpoint_id = endpoint.id
		WHERE binding.connection_id = ?`, connectionID).Scan(&inputRate, &outputRate); err == nil {
		value.PricingConfigured = value.PricingConfigured || inputRate > 0 || outputRate > 0
	}
	return value, nil
}

func providerUsageQuery(db *sql.DB, predicate string, arg string) (providerUsageSummary, error) {
	var value providerUsageSummary
	if err := ensureProviderUsageTable(db); err != nil {
		return value, err
	}
	var lastUsed sql.NullString
	var tokenData, pricing int
	err := db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(prompt_tokens), 0),
		COALESCE(SUM(completion_tokens), 0), COALESCE(SUM(total_tokens), 0),
		COALESCE(SUM(estimated_cost), 0), MAX(created_at),
		COALESCE(MAX(CASE WHEN total_tokens > 0 THEN 1 ELSE 0 END), 0),
		COALESCE(MAX(CASE WHEN input_cost_per_million > 0 OR output_cost_per_million > 0 THEN 1 ELSE 0 END), 0)
		FROM model_usage_events WHERE `+predicate, arg).Scan(
		&value.Calls, &value.PromptTokens, &value.CompletionTokens, &value.TotalTokens,
		&value.EstimatedCost, &lastUsed, &tokenData, &pricing)
	if err != nil {
		return value, err
	}
	value.LastUsedAt = lastUsed.String
	value.Source = "runtime_observed"
	value.TokenDataAvailable = tokenData == 1
	value.PricingConfigured = pricing == 1
	return value, nil
}
