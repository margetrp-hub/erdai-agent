package main

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func newAgentInstanceStore(t *testing.T) *coreConfigStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("PRAGMA foreign_keys = ON"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(nativeCoreTables); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(nativeCoreIndexes); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO personas(id, name, created_at, updated_at) VALUES ('persona-test', 'Test persona', 'now', 'now')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &coreConfigStore{db: db}
}

func agentInstanceRequest(t *testing.T, store *coreConfigStore, method, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := store.handleAgentInstanceRequest(rec, req, path); err != nil {
		writeCoreAPIError(rec, err)
	}
	return rec
}

func TestAgentInstanceCRUDAndResolutionDoesNotFallThroughWhenDisabled(t *testing.T) {
	store := newAgentInstanceStore(t)
	policy := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-policy-templates", `{"id":"policy-test","name":"Test policy","config":{"toolAllowlist":["chat"]}}`)
	if policy.Code != http.StatusCreated {
		t.Fatalf("create policy = %d: %s", policy.Code, policy.Body.String())
	}
	instance := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", `{"id":"instance-test","displayName":"Test instance","personaId":"persona-test","policyTemplateId":"policy-test"}`)
	if instance.Code != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", instance.Code, instance.Body.String())
	}
	connector := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances/instance-test/connectors", `{"connectorId":"qq-test"}`)
	if connector.Code != http.StatusCreated {
		t.Fatalf("create connector = %d: %s", connector.Code, connector.Body.String())
	}
	route := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instance-routes", `{"id":"route-test","instanceId":"instance-test","connectorId":"qq-test","transport":"aiocqhttp"}`)
	if route.Code != http.StatusCreated {
		t.Fatalf("create route = %d: %s", route.Code, route.Body.String())
	}
	resolved, err := store.resolveAgentInstance("qq-test", "aiocqhttp", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Matched || !resolved.Enabled || resolved.InstanceID != "instance-test" || resolved.PersonaID != "persona-test" || resolved.MemoryNamespace != "instance-test" {
		t.Fatalf("resolved instance = %+v", resolved)
	}
	disabled := agentInstanceRequest(t, store, http.MethodPut, "/api/v1/agent-instances/instance-test", `{"enabled":false}`)
	if disabled.Code != http.StatusOK {
		t.Fatalf("disable instance = %d: %s", disabled.Code, disabled.Body.String())
	}
	resolved, err = store.resolveAgentInstance("qq-test", "aiocqhttp", "conversation-1")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Matched || resolved.Enabled {
		t.Fatalf("disabled instance was not retained as a match: %+v", resolved)
	}
}

func TestAgentInstanceRouteRejectsUnknownConnector(t *testing.T) {
	store := newAgentInstanceStore(t)
	instance := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", `{"id":"instance-test","displayName":"Test instance","personaId":"persona-test"}`)
	if instance.Code != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", instance.Code, instance.Body.String())
	}
	route := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instance-routes", `{"id":"route-test","instanceId":"instance-test","connectorId":"missing"}`)
	if route.Code != http.StatusBadRequest || !strings.Contains(route.Body.String(), "connector") {
		t.Fatalf("unknown connector route = %d: %s", route.Code, route.Body.String())
	}
}

func TestAgentInstancePolicyIsConsumedAndCanOnlyTightenProfile(t *testing.T) {
	store := newAgentInstanceStore(t)
	policy := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-policy-templates", `{"id":"policy-test","name":"Tight policy","config":{"chatEndpointId":"chat-instance","allowedToolIds":["chat"],"deniedToolIds":["search"],"maxReplyChars":20,"maxReplySentences":1,"memoryPolicy":"instance-only"}}`)
	if policy.Code != http.StatusCreated {
		t.Fatalf("create policy = %d: %s", policy.Code, policy.Body.String())
	}
	instance := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", `{"id":"instance-test","displayName":"Test instance","personaId":"persona-test","policyTemplateId":"policy-test"}`)
	if instance.Code != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", instance.Code, instance.Body.String())
	}
	proactive := true
	chars, sentences := 100, 3
	got, err := store.agentInstanceRuntimeProfile("instance-test", personaRuntimeProfile{
		ChatEndpointID:    "chat-global",
		AllowedToolIDs:    []string{"chat", "search"},
		ProactiveEnabled:  &proactive,
		MaxReplyChars:     &chars,
		MaxReplySentences: &sentences,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ChatEndpointID != "chat-instance" || len(got.AllowedToolIDs) != 1 || got.AllowedToolIDs[0] != "chat" {
		t.Fatalf("instance policy did not apply endpoint/tool restriction: %+v", got)
	}
	if got.MaxReplyChars == nil || *got.MaxReplyChars != 20 || got.MaxReplySentences == nil || *got.MaxReplySentences != 1 {
		t.Fatalf("instance policy did not tighten reply limits: %+v", got)
	}
	if got.MemoryPolicy != "instance-only" || got.ProactiveEnabled == nil || !*got.ProactiveEnabled {
		t.Fatalf("instance policy did not consume supported fields: %+v", got)
	}
}

func TestAgentInstanceParticipationPolicyOverridesRole(t *testing.T) {
	store := newAgentInstanceStore(t)
	policy := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-policy-templates", `{"id":"policy-social","name":"Social policy","config":{"proactiveEnabled":true,"initialReplyProbability":0.08,"afterReplyProbability":0.26,"participationStyle":"social","expressionPrompt":"只说自然短句"}}`)
	if policy.Code != http.StatusCreated {
		t.Fatalf("create policy = %d: %s", policy.Code, policy.Body.String())
	}
	instance := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", `{"id":"instance-social","displayName":"Social instance","personaId":"persona-test","policyTemplateId":"policy-social"}`)
	if instance.Code != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", instance.Code, instance.Body.String())
	}
	disabled := false
	got, err := store.agentInstanceRuntimeProfile("instance-social", personaRuntimeProfile{ProactiveEnabled: &disabled})
	if err != nil {
		t.Fatal(err)
	}
	if got.ProactiveEnabled == nil || !*got.ProactiveEnabled || got.InitialReplyProbability == nil || *got.InitialReplyProbability != 0.08 || got.AfterReplyProbability == nil || *got.AfterReplyProbability != 0.26 || got.ParticipationStyle != "social" {
		t.Fatalf("participation override = %+v", got)
	}
	if prompt := store.personaRuntimePrompt(ptrString("persona-test"), "instance-social"); !strings.Contains(prompt, "普通知识问句不是邀请") || !strings.Contains(prompt, "只说自然短句") {
		t.Fatalf("instance profile prompt = %q", prompt)
	}
}

func TestGroupModerationCapabilityCRUDAndHighConfidenceDetection(t *testing.T) {
	store := newAgentInstanceStore(t)
	instance := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", `{"id":"doubao-instance","displayName":"Doubao","personaId":"persona-test"}`)
	if instance.Code != http.StatusCreated {
		t.Fatalf("create instance = %d: %s", instance.Code, instance.Body.String())
	}
	req := httptest.NewRequest(http.MethodPut, "/api/v1/agent-instance-capabilities/doubao-instance/group_moderation", strings.NewReader(`{"enabled":true,"config":{"mode":"enforce","executorConnectorId":"onebot-executor","groupIds":["88"],"exemptAdmins":true,"minimumScore":3}}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	if err := store.handleAgentInstanceCapabilities(rec, req, req.URL.Path); err != nil {
		writeCoreAPIError(rec, err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("put capability = %d: %s", rec.Code, rec.Body.String())
	}
	runtime := &AgentRuntime{db: store.db, configStore: store, identitySecret: []byte("test-identity-secret")}
	decision, err := runtime.evaluateGroupModeration(context.Background(), "onebot-executor", "88", "member", "https://ad.example/register", "24小时低价充值，为你服务", false)
	if err != nil || !decision.Matched || decision.Mode != "enforce" || decision.OwnerInstanceID != "doubao-instance" {
		t.Fatalf("ad decision = %+v, err=%v", decision, err)
	}
	benign, err := runtime.evaluateGroupModeration(context.Background(), "onebot-executor", "88", "member", "普通群友", "这个文档链接挺有用", false)
	if err != nil || benign.Matched {
		t.Fatalf("benign decision = %+v, err=%v", benign, err)
	}
	admin, err := runtime.evaluateGroupModeration(context.Background(), "onebot-executor", "88", "admin", "https://ad.example", "24小时充值服务", true)
	if err != nil || admin.Matched {
		t.Fatalf("admin decision = %+v, err=%v", admin, err)
	}
}

func ptrString(value string) *string { return &value }
