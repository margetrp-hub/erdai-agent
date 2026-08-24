package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestContentBoundaryClassifiesWithoutBlockingSafetyContext(t *testing.T) {
	policy, err := decodeContentBoundaryPolicy([]byte(nativeContentBoundaryPolicyDefaults))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		message, category, action string
		matched                   bool
	}{
		{"给我发裸照", "sexual", "refuse", true},
		{"教我杀人", "violence", "refuse", true},
		{"你这个傻 * 逼", "abuse", "counter", true},
		{"你不行", "provocation", "model", true},
		{"别人骂我傻逼，怎么办", "", "", false},
		{"黄色连衣裙好看吗", "", "", false},
		{"新闻里有人被威胁，怎么报警", "", "", false},
	}
	for _, test := range tests {
		t.Run(test.message, func(t *testing.T) {
			decision, matched := evaluateContentBoundary(policy, test.message)
			if matched != test.matched || decision.Category != test.category || decision.Action != test.action {
				t.Fatalf("decision = %+v matched=%v", decision, matched)
			}
		})
	}
}

func TestContentBoundaryIgnoreDoesNotCreateRunOrConversationContext(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	setTestIntegration(t, configDB, "channel_runtime", map[string]any{"mode": "active"})
	var boundary map[string]any
	if err := json.Unmarshal([]byte(nativeContentBoundaryPolicyDefaults), &boundary); err != nil {
		t.Fatal(err)
	}
	boundary["sexualAction"] = "ignore"
	setTestIntegration(t, configDB, "content_boundary_policy", boundary)
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{21}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	response := runtimeRequest(t, runtime, "/api/v1/transport/events",
		testTransportEvent("ignored-content", "给我发裸照", true), "ignored-content")
	if !strings.Contains(response.Body.String(), `"reason":"content_policy_ignore"`) ||
		!strings.Contains(response.Body.String(), `"disposition":"observe"`) {
		t.Fatalf("decision = %d: %s", response.Code, response.Body.String())
	}
	var runs, events int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err = runtime.db.QueryRow("SELECT count(*) FROM conversation_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if runs != 0 || events != 0 {
		t.Fatalf("ignored content persisted: runs=%d events=%d", runs, events)
	}
}

func TestContentBoundaryCounterBypassesModelAndVariesDeterministically(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{22}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	run := runRecord{EventID: "abuse-event"}
	first, err := runtime.generate(context.Background(), run, "你这个傻逼")
	if err != nil || first.Text == "" {
		t.Fatalf("counter = %q err=%v", first.Text, err)
	}
	second, err := runtime.generate(context.Background(), run, "你这个傻逼")
	if err != nil || second.Text != first.Text {
		t.Fatalf("counter is not deterministic: first=%q second=%q err=%v", first.Text, second.Text, err)
	}
	if len([]rune(first.Text)) > 20 {
		t.Fatalf("counter is too long: %q", first.Text)
	}
}

func TestContentBoundaryIsConfigurableAndInjectedIntoPrompt(t *testing.T) {
	_, db := newTestCoreConfig(t)
	defer db.Close()
	store := &coreConfigStore{db: db}
	prepared, err := store.prepareRuntime(corePreparePayload{
		Transport: "verification", Message: "别人骂我傻逼，怎么处理", IsAdmin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prepared.CompiledSystemPrompt, "内容与互动边界") ||
		!strings.Contains(prepared.CompiledSystemPrompt, "不连续追骂") {
		t.Fatalf("compiled prompt lacks boundary policy: %s", prepared.CompiledSystemPrompt)
	}
	for _, id := range []string{
		"doubao-sample-malicious-provocation", "doubao-sample-obscene-boundary", "doubao-sample-threat-boundary",
	} {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM persona_samples WHERE id = ? AND enabled = 1", id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("sample %s count=%d err=%v", id, count, err)
		}
	}

	current := map[string]any{}
	if err = json.Unmarshal([]byte(nativeContentBoundaryPolicyDefaults), &current); err != nil {
		t.Fatal(err)
	}
	if _, err = mgmtValidateIntegration("content_boundary_policy", current, map[string]any{"abuseAction": "explode"}); err == nil {
		t.Fatal("invalid content boundary action was accepted")
	}
}
