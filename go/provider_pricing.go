package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type providerPrice struct {
	Model    string
	Input    float64
	Output   float64
	Currency string
}

func (a *AgentRuntime) handleProviderPricingSync(w http.ResponseWriter, r *http.Request, path string) error {
	if r.Method != http.MethodPost {
		return mgmtMethodNotAllowed()
	}
	id, err := parseCorePathID(strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/provider-connections/"), "/pricing-sync"))
	if err != nil {
		return err
	}
	var provider, pricingURL, credentialRef string
	var timeoutSeconds int
	err = a.configStore.db.QueryRow(`SELECT provider, pricing_url, credential_ref, timeout_seconds
		FROM provider_connections WHERE id = ? AND enabled = 1`, id).
		Scan(&provider, &pricingURL, &credentialRef, &timeoutSeconds)
	if errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("provider connection")
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(pricingURL) == "" {
		return coreInvalid("pricingUrl is not configured")
	}
	endpoint, err := secureServiceBase(pricingURL)
	if err != nil {
		return coreInvalid("pricingUrl is invalid")
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	if key := getenv(credentialRef); key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	response, err := a.client.Do(request)
	if err != nil {
		return fmt.Errorf("fetch pricing: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("pricing source returned HTTP %d", response.StatusCode)
	}
	prices, err := decodeProviderPrices(body)
	if err != nil {
		return coreInvalid(err.Error())
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.configStore.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	updated := 0
	for _, price := range prices {
		result, updateErr := tx.Exec(`UPDATE model_endpoints SET input_cost_per_million = ?,
			output_cost_per_million = ?, pricing_source = ?, pricing_checked_at = ?,
			pricing_currency = ?, updated_at = ? WHERE provider = ? AND model = ?`,
			price.Input, price.Output, endpoint, now, price.Currency, now, provider, price.Model)
		if updateErr != nil {
			return updateErr
		}
		changed, _ := result.RowsAffected()
		updated += int(changed)
	}
	if updated == 0 {
		return coreInvalid("pricing source contained no matching model")
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"provider": provider, "updatedModels": updated, "checkedAt": now})
	return nil
}

func decodeProviderPrices(body []byte) ([]providerPrice, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("pricing source is not valid JSON")
	}
	items := pricingItems(root)
	prices := make([]providerPrice, 0, len(items))
	for _, item := range items {
		model := firstPricingString(item, "id", "model", "name")
		if model == "" {
			continue
		}
		input, inputOK := firstPricingNumber(item, "input_cost_per_million", "inputCostPerMillion", "input_per_million")
		output, outputOK := firstPricingNumber(item, "output_cost_per_million", "outputCostPerMillion", "output_per_million")
		if nested, ok := item["pricing"].(map[string]any); ok {
			if !inputOK {
				if value, found := firstPricingNumber(nested, "prompt", "input"); found {
					input, inputOK = value*1_000_000, true
				}
			}
			if !outputOK {
				if value, found := firstPricingNumber(nested, "completion", "output"); found {
					output, outputOK = value*1_000_000, true
				}
			}
		}
		if !inputOK && !outputOK {
			continue
		}
		currency := strings.ToUpper(firstPricingString(item, "currency"))
		if currency == "" {
			currency = "USD"
		}
		prices = append(prices, providerPrice{Model: model, Input: input, Output: output, Currency: currency})
	}
	if len(prices) == 0 {
		return nil, fmt.Errorf("pricing source contained no recognized model prices")
	}
	return prices, nil
}

func pricingItems(root any) []map[string]any {
	if object, ok := root.(map[string]any); ok {
		for _, key := range []string{"data", "models", "items"} {
			if values, ok := object[key].([]any); ok {
				return pricingObjectItems(values)
			}
		}
		return []map[string]any{object}
	}
	if values, ok := root.([]any); ok {
		return pricingObjectItems(values)
	}
	return nil
}

func pricingObjectItems(values []any) []map[string]any {
	items := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if item, ok := value.(map[string]any); ok {
			items = append(items, item)
		}
	}
	return items
}

func firstPricingString(item map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := item[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstPricingNumber(item map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch value := item[key].(type) {
		case float64:
			if value >= 0 {
				return value, true
			}
		case string:
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err == nil && parsed >= 0 {
				return parsed, true
			}
		}
	}
	return 0, false
}
