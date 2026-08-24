package main

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProviderUsageRecordsTokensAndSnapshotCost(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	_, err := runtime.configStore.db.Exec(`
		UPDATE model_endpoints SET input_cost_per_million = 2, output_cost_per_million = 4
		WHERE id = 'ohlaoo-gpt-5.6-luna'
	`)
	if err != nil {
		t.Fatal(err)
	}
	runtime.recordProviderUsage("run-usage", runtimeProviderTarget{
		EndpointID: "ohlaoo-gpt-5.6-luna", Provider: "ohlaoo", Model: "gpt-5.6-luna",
	}, chatUsage{PromptTokens: 1000, CompletionTokens: 3000, TotalTokens: 4000})

	usage, err := providerUsageForEndpoint(runtime.configStore.db, "ohlaoo-gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Calls != 1 || usage.PromptTokens != 1000 || usage.CompletionTokens != 3000 ||
		usage.TotalTokens != 4000 || !usage.TokenDataAvailable || !usage.PricingConfigured {
		t.Fatalf("usage = %+v", usage)
	}
	if math.Abs(usage.EstimatedCost-0.014) > 0.0000001 {
		t.Fatalf("estimated cost = %f", usage.EstimatedCost)
	}
}

func TestProviderUsageReportsConfiguredPricingBeforeFirstCall(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	_, err := runtime.configStore.db.Exec(`
		UPDATE model_endpoints SET input_cost_per_million = 1.5, output_cost_per_million = 3
		WHERE id = 'ohlaoo-gpt-5.6-luna'
	`)
	if err != nil {
		t.Fatal(err)
	}
	usage, err := providerUsageForEndpoint(runtime.configStore.db, "ohlaoo-gpt-5.6-luna")
	if err != nil {
		t.Fatal(err)
	}
	if usage.Calls != 0 || !usage.PricingConfigured {
		t.Fatalf("usage = %+v", usage)
	}
}

func TestProviderUsageAggregatesByConnectionBinding(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	connectionID := "usage-test-connection"
	now := "2026-08-07T00:00:00Z"
	if _, err := runtime.configStore.db.Exec(`INSERT INTO provider_connections
		(id, provider, api_base, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		connectionID, "usage-test-provider", "https://example.invalid/v1", now, now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoint_connections
		(endpoint_id, connection_id, updated_at) VALUES (?, ?, ?)`,
		"ohlaoo-gpt-5.6-sol", connectionID, now); err != nil {
		t.Fatal(err)
	}

	runtime.recordProviderUsage("run-connection-usage", runtimeProviderTarget{
		EndpointID: "ohlaoo-gpt-5.6-sol", Provider: "ohlaoo", Model: "gpt-5.6-sol",
	}, chatUsage{PromptTokens: 120, CompletionTokens: 30, TotalTokens: 150})

	usage, err := providerUsageForConnection(runtime.configStore.db, connectionID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.Calls != 1 || usage.TotalTokens != 150 || !usage.TokenDataAvailable {
		t.Fatalf("connection usage = %+v", usage)
	}
}

func TestChatUsageAcceptsInputOutputTokenNames(t *testing.T) {
	input, output, total := (chatUsage{InputTokens: 12, OutputTokens: 8}).normalized()
	if input != 12 || output != 8 || total != 20 {
		t.Fatalf("normalized usage = %d/%d/%d", input, output, total)
	}
}

func TestProviderUsageWindowSeparatesTokensFromIncompletePricing(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	if err := ensureProviderUsageTable(runtime.configStore.db); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.configStore.db.Exec(`INSERT INTO model_usage_events (
		id, provider, model, prompt_tokens, completion_tokens, total_tokens,
		input_cost_per_million, output_cost_per_million, estimated_cost, created_at
	) VALUES
		('usage-unpriced', 'grok2api', 'grok-chat-fast', 100, 50, 150, 0, 0, 0, ?),
		('usage-priced', 'openai', 'gpt-test', 200, 100, 300, 2, 4, 0.0008, ?),
		('usage-old', 'openai', 'gpt-old', 999, 1, 1000, 2, 4, 0.002, ?)`,
		now.Add(-time.Hour).Format(time.RFC3339Nano),
		now.Add(-2*time.Hour).Format(time.RFC3339Nano),
		now.Add(-48*time.Hour).Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}

	value, err := providerUsageForWindow(runtime.configStore.db, 24, now)
	if err != nil {
		t.Fatal(err)
	}
	if value.Calls != 2 || value.TotalTokens != 450 || value.UnpricedCalls != 1 || value.PricingComplete {
		t.Fatalf("window usage = %+v", value)
	}
	if len(value.ByModel) != 2 || value.ByModel[0].Model != "gpt-test" || value.ByModel[1].Model != "grok-chat-fast" {
		t.Fatalf("by model = %+v", value.ByModel)
	}
}

func TestProviderUsageStatsEndpointValidatesWindow(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	request := httptest.NewRequest(http.MethodGet, "/api/v1/usage/stats?hours=0", nil)
	recorder := httptest.NewRecorder()
	err := runtime.configStore.handleUsageStats(recorder, request)
	if err == nil || !strings.Contains(err.Error(), "hours must be") {
		t.Fatalf("error = %v", err)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/usage/stats?hours=24", nil)
	recorder = httptest.NewRecorder()
	if err := runtime.configStore.handleUsageStats(recorder, request); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data providerUsageWindowSummary `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.WindowHours != 24 || response.Data.ByModel == nil {
		t.Fatalf("response = %+v", response.Data)
	}
}
