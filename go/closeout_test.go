package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDecodeProviderPricesSupportsCatalogAndPerTokenPricing(t *testing.T) {
	prices, err := decodeProviderPrices([]byte(`{"data":[
		{"id":"chat-a","input_cost_per_million":2.5,"output_cost_per_million":7.5},
		{"id":"chat-b","pricing":{"prompt":"0.000001","completion":"0.000003"}}
	]}`))
	if err != nil || len(prices) != 2 {
		t.Fatalf("prices = %+v, err=%v", prices, err)
	}
	if prices[0].Input != 2.5 || prices[0].Output != 7.5 || prices[1].Input != 1 || prices[1].Output != 3 {
		t.Fatalf("decoded prices = %+v", prices)
	}
}

func TestProgressMessageConfigurationIsConsumed(t *testing.T) {
	disabled := false
	call := chatToolCall{}
	call.Function.Name = "grok_web_search"
	if got := progressMessage(runtimeMessagePolicy{
		ToolProgressEnabled: &disabled, ToolProgressSearchMessages: []string{"我去看看。"},
	}, []chatToolCall{call}, "查一下"); got != "" {
		t.Fatalf("disabled progress = %q", got)
	}
	if _, err := mgmtValidateIntegration("message_policy", map[string]any{}, map[string]any{"showToolUseStatus": true}); err == nil {
		t.Fatal("removed display-only field was accepted")
	}
}

func TestSearchProgressRequiresExplicitOptIn(t *testing.T) {
	call := chatToolCall{}
	call.Function.Name = "grok_web_search"
	policy := runtimeMessagePolicy{ToolProgressSearchMessages: []string{"Searching."}}
	if got := progressMessage(policy, []chatToolCall{call}, "find news"); got != "" {
		t.Fatalf("default search progress = %q", got)
	}
	enabled := true
	policy.ToolProgressSearchEnabled = &enabled
	if got := progressMessage(policy, []chatToolCall{call}, "find news"); got != "Searching." {
		t.Fatalf("enabled search progress = %q", got)
	}
}

func TestMediaGCDeletesOnlyExpiredUnreferencedFiles(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	mediaDir := t.TempDir()
	runtime.mediaDir = mediaDir
	oldPath := filepath.Join(mediaDir, "old.png")
	protectedPath := filepath.Join(mediaDir, "protected.png")
	for _, path := range []string{oldPath, protectedPath} {
		if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)
	_ = os.Chtimes(oldPath, oldTime, oldTime)
	_ = os.Chtimes(protectedPath, oldTime, oldTime)
	attachments, _ := json.Marshal([]transportAttachment{{ID: "image", Kind: "image", LocalPath: mediaMountRoot + "/protected.png"}})
	ciphertext, err := runtime.encrypt(attachments)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.db.Exec(`INSERT INTO agent_recent_attachments
		(transport, conversation_ref, attachments_cipher, expires_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"qq_official", "group-one", ciphertext, now.Add(time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	deleted := runtime.runMediaGC(now, documentPolicy{MediaRetentionHours: 24})
	if deleted != 1 {
		t.Fatalf("deleted = %d", deleted)
	}
	if _, err = os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired file still exists: %v", err)
	}
	if _, err = os.Stat(protectedPath); err != nil {
		t.Fatalf("protected file was removed: %v", err)
	}
}

func TestMediaGCDryRunAndActiveReferencesUseSameCandidateScan(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	mediaDir := t.TempDir()
	runtime.mediaDir = mediaDir
	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)
	stalePath := filepath.Join(mediaDir, "stale.png")
	activePath := filepath.Join(mediaDir, "active.png")
	deliveryPath := filepath.Join(mediaDir, "delivery.png")
	for _, path := range []string{stalePath, activePath, deliveryPath} {
		if err := os.WriteFile(path, []byte("1234567"), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(path, oldTime, oldTime)
	}
	stamp := now.Format(time.RFC3339Nano)
	_, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, reply_handle, conversation_ref, sender_ref, state, created_at, updated_at)
		VALUES ('run-active', 'event-active', 'reply', 'group', 'sender', 'running', ?, ?)`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.db.Exec(`INSERT INTO agent_task_steps
		(id, run_id, step_index, kind, name, status, created_at, updated_at)
		VALUES ('step-active', 'run-active', 0, 'tool', 'image', 'running', ?, ?)`, stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.db.Exec(`INSERT INTO agent_task_artifacts
		(run_id, step_id, kind, local_path, created_at)
		VALUES ('run-active', 'step-active', 'image', ?, ?)`, mediaMountRoot+"/active.png", oldTime.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(transportDeliveryMessage{Attachments: []agentAttachment{{Kind: "image", LocalPath: mediaMountRoot + "/delivery.png"}}})
	_, err = runtime.db.Exec(`INSERT INTO agent_deliveries
		(id, run_id, reply_handle, payload_json, status, created_at, updated_at)
		VALUES ('delivery-active', 'run-active', 'reply', ?, 'pending', ?, ?)`, string(payload), stamp, stamp)
	if err != nil {
		t.Fatal(err)
	}

	dry, err := runtime.runMediaGCAudited(now, documentPolicy{MediaRetentionHours: 24}, true)
	if err != nil {
		t.Fatal(err)
	}
	if dry.CandidateFiles != 1 || dry.CandidateBytes != 7 || dry.DeletedFiles != 0 || dry.ProtectedActiveTask != 1 || dry.ProtectedDelivery != 1 {
		t.Fatalf("dry report = %+v", dry)
	}
	for _, path := range []string{stalePath, activePath, deliveryPath} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("dry-run changed %s: %v", path, err)
		}
	}

	actual, err := runtime.runMediaGCAudited(now, documentPolicy{MediaRetentionHours: 24}, false)
	if err != nil {
		t.Fatal(err)
	}
	if actual.CandidateFiles != dry.CandidateFiles || actual.CandidateBytes != dry.CandidateBytes || actual.DeletedFiles != 1 || actual.DeletedBytes != 7 {
		t.Fatalf("actual report = %+v, dry = %+v", actual, dry)
	}
	if _, err = os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale file still exists: %v", err)
	}
	for _, path := range []string{activePath, deliveryPath} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("protected file was removed: %s: %v", path, err)
		}
	}
}

func TestMediaGCStopsWhenReferenceScanFails(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	mediaDir := t.TempDir()
	runtime.mediaDir = mediaDir
	path := filepath.Join(mediaDir, "must-stay.png")
	if err := os.WriteFile(path, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	oldTime := now.Add(-48 * time.Hour)
	_ = os.Chtimes(path, oldTime, oldTime)
	stamp := now.Format(time.RFC3339Nano)
	_, err := runtime.db.Exec(`INSERT INTO agent_recent_attachments
		(transport, conversation_ref, attachments_cipher, expires_at, updated_at)
		VALUES ('qq_official', 'corrupt', X'00', ?, ?)`, now.Add(time.Hour).Format(time.RFC3339Nano), stamp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.runMediaGCAudited(now, documentPolicy{MediaRetentionHours: 24}, false); err == nil {
		t.Fatal("corrupt reference did not stop media GC")
	}
	if _, err = os.Stat(path); err != nil {
		t.Fatalf("media was removed after reference scan failed: %v", err)
	}
}

func TestTransportV2DecisionIsAudited(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	event := testTransportEvent("audit-v2", "hello", true)
	event["schemaVersion"] = 2
	event["message"] = map[string]any{"id": "message-v2", "text": "hello", "attachments": []any{}}
	response := runtimeRequest(t, runtime, "/api/v1/transport/events", event, "audit-v2")
	if response.Code != 202 {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}
	var disposition, reason, messageID string
	if err := runtime.db.QueryRow(`SELECT disposition, reason, message_id FROM agent_transport_events WHERE event_id = ?`, "audit-v2").
		Scan(&disposition, &reason, &messageID); err != nil {
		t.Fatal(err)
	}
	if disposition != "owned" || reason != "go_core_owned" || messageID != "message-v2" {
		t.Fatalf("audit = disposition=%q reason=%q message=%q", disposition, reason, messageID)
	}
}

func TestComplexMessageThresholdIsLoaded(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "companion_policy", map[string]any{
		"enabled": true, "enableModelRouting": true, "complexMessageChars": 260,
		"contextMessagesPerPrompt": 40, "contextTokenBudget": 6000,
		"summaryIntervalMessages": 12, "summaryWindowMessages": 12, "topicTtlHours": 6,
		"maxMessagesPerGroup": 20000, "messageRetentionHours": 8760,
		"coldRecallEnabled": true, "coldRecallScanMessages": 5000, "coldRecallMaxMessages": 12,
	})
	if policy := runtime.companionContextPolicy(context.Background()); policy.ComplexMessageChars != 260 {
		t.Fatalf("complex threshold = %d", policy.ComplexMessageChars)
	}
}
