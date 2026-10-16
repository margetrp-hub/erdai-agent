package main

import (
	"net/http"
	"testing"
)

func TestModelEnableChangePreservesPricingProvenance(t *testing.T) {
	runtime := newManagementRuntime(t)
	path := "/api/v1/model-endpoints/edit-price-test"
	payload := map[string]any{
		"provider": "test", "model": "edit-price-test", "enabled": true,
		"capabilities": []string{"chat", "vision"}, "priority": 7, "maxContextTokens": 64000,
		"inputCostPerMillion": 2.5, "outputCostPerMillion": 8.0,
	}
	created := managementRequest(t, runtime, http.MethodPut, path, payload, "admin")
	if created.Code != http.StatusOK {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	const checkedAt = "2026-10-15T12:00:00Z"
	if _, err := runtime.configStore.db.Exec(`UPDATE model_endpoints SET pricing_source = 'provider_feed', pricing_checked_at = ? WHERE id = 'edit-price-test'`, checkedAt); err != nil {
		t.Fatal(err)
	}
	payload["enabled"] = false
	updated := managementRequest(t, runtime, http.MethodPut, path, payload, "admin")
	if updated.Code != http.StatusOK {
		t.Fatalf("toggle = %d: %s", updated.Code, updated.Body.String())
	}
	model, found, err := runtime.configStore.mgmtModel("edit-price-test")
	if err != nil || !found || model.Enabled || model.PricingSource != "provider_feed" || model.PricingCheckedAt != checkedAt || model.InputCostPerMillion != 2.5 || model.OutputCostPerMillion != 8 || model.Priority != 7 || model.MaxContextTokens != 64000 {
		t.Fatalf("toggle changed configuration: %+v found=%v err=%v", model, found, err)
	}
	payload["inputCostPerMillion"] = 3.0
	updated = managementRequest(t, runtime, http.MethodPut, path, payload, "admin")
	if updated.Code != http.StatusOK {
		t.Fatalf("price edit = %d: %s", updated.Code, updated.Body.String())
	}
	model, _, err = runtime.configStore.mgmtModel("edit-price-test")
	if err != nil || model.InputCostPerMillion != 3 || model.PricingSource != "manual" || model.PricingCheckedAt == checkedAt {
		t.Fatalf("explicit price edit not recorded: %+v err=%v", model, err)
	}
}
