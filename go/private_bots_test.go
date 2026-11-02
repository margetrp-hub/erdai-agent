package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// privateBotRequest exercises the management handler directly so these tests
// cover the owner boundary and durable state transitions without requiring a
// running HTTP server or an administrator token.
func privateBotRequest(t *testing.T, store *coreConfigStore, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	// The production router passes URL.Path separately from RawQuery. Keep
	// owner/workspace selectors available through req.URL.Query().
	if err := store.handlePrivateBotRequest(rec, req, req.URL.Path); err != nil {
		writeCoreAPIError(rec, err)
	}
	return rec
}

type privateBotEnvelope struct {
	Bot     privateBot `json:"bot"`
	Created bool       `json:"created"`
}

func decodePrivateBotEnvelope(t *testing.T, rec *httptest.ResponseRecorder) privateBotEnvelope {
	t.Helper()
	if !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("response is not JSON: %d %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data privateBotEnvelope `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func createPrivateBotForTest(t *testing.T, store *coreConfigStore, owner, id, name, idem string) privateBotEnvelope {
	t.Helper()
	body := `{"ownerRef":"` + owner + `","id":"` + id + `","displayName":"` + name + `","personaId":"persona-test"`
	if idem != "" {
		body += `,"idempotencyKey":"` + idem + `"`
	}
	body += `}`
	rec := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create private bot = %d: %s", rec.Code, rec.Body.String())
	}
	envelope := decodePrivateBotEnvelope(t, rec)
	if !envelope.Created || envelope.Bot.ID != id {
		t.Fatalf("create response = %+v", envelope)
	}
	return envelope
}

func TestPrivateBotCopyIsIdempotentAndDoesNotShareRuntimeInstance(t *testing.T) {
	store := newAgentInstanceStore(t)
	source := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "source-create")
	copyBody := `{"ownerRef":"owner-a","displayName":"豆包副本","idempotencyKey":"copy-1"}`
	first := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+source.Bot.ID+"/copy", copyBody)
	if first.Code != http.StatusCreated {
		t.Fatalf("copy = %d: %s", first.Code, first.Body.String())
	}
	copy := decodePrivateBotEnvelope(t, first)
	if copy.Bot.ID == source.Bot.ID || copy.Bot.AgentInstanceID == source.Bot.AgentInstanceID || copy.Bot.MemoryNamespace == source.Bot.MemoryNamespace {
		t.Fatalf("copy shares mutable runtime scope: source=%+v copy=%+v", source.Bot, copy.Bot)
	}
	second := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+source.Bot.ID+"/copy", copyBody)
	if second.Code != http.StatusOK {
		t.Fatalf("idempotent copy = %d: %s", second.Code, second.Body.String())
	}
	replayed := decodePrivateBotEnvelope(t, second)
	if replayed.Created || replayed.Bot.ID != copy.Bot.ID {
		t.Fatalf("idempotent copy replay = %+v", replayed)
	}
	var count int
	if err := store.db.QueryRow(`SELECT count(*) FROM private_bots WHERE owner_ref='owner-a'`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("private bot count=%d err=%v, want 2", count, err)
	}
}

func TestPrivateBotOwnerBoundaryAndSessionSelection(t *testing.T) {
	store := newAgentInstanceStore(t)
	a := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "")
	b := createPrivateBotForTest(t, store, "owner-a", "bot-b", "小满", "")

	// A different owner cannot read or mutate a private Bot, even when it
	// knows the identifier. This protects memories, credentials and routes.
	read := privateBotRequest(t, store, http.MethodGet, "/api/v1/private-bots/"+a.Bot.ID+"?ownerRef=owner-b", "")
	if read.Code != http.StatusForbidden {
		t.Fatalf("cross-owner read = %d: %s", read.Code, read.Body.String())
	}
	update := privateBotRequest(t, store, http.MethodPut, "/api/v1/private-bots/"+a.Bot.ID+"?ownerRef=owner-b", `{"status":"paused"}`)
	if update.Code != http.StatusForbidden {
		t.Fatalf("cross-owner update = %d: %s", update.Code, update.Body.String())
	}

	selectPath := "/api/v1/private-bots/" + a.Bot.ID + "/select"
	selection := `{"ownerRef":"owner-a","workspaceId":"ws-1","transport":"qq","transportInstance":"qq-main","conversationRef":"group-1"}`
	selected := privateBotRequest(t, store, http.MethodPost, selectPath, selection)
	if selected.Code != http.StatusOK {
		t.Fatalf("select first bot = %d: %s", selected.Code, selected.Body.String())
	}
	selected = privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+b.Bot.ID+"/select", selection)
	if selected.Code != http.StatusOK {
		t.Fatalf("select second bot = %d: %s", selected.Code, selected.Body.String())
	}
	var activeBot string
	if err := store.db.QueryRow(`SELECT bot_id FROM private_bot_sessions WHERE owner_ref='owner-a' AND workspace_id='ws-1' AND transport='qq' AND transport_instance='qq-main' AND conversation_ref='group-1' AND active=1`).Scan(&activeBot); err != nil {
		t.Fatal(err)
	}
	if activeBot != b.Bot.ID {
		t.Fatalf("active session bot=%q, want %q", activeBot, b.Bot.ID)
	}
	var activeCount int
	if err := store.db.QueryRow(`SELECT count(*) FROM private_bot_sessions WHERE owner_ref='owner-a' AND workspace_id='ws-1' AND transport='qq' AND transport_instance='qq-main' AND conversation_ref='group-1' AND active=1`).Scan(&activeCount); err != nil || activeCount != 1 {
		t.Fatalf("active session count=%d err=%v", activeCount, err)
	}
	resolved, err := store.resolveAgentInstance("qq-main", "qq", "group-1")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Matched || !resolved.Enabled || resolved.InstanceID != b.Bot.AgentInstanceID || resolved.PersonaID != b.Bot.PersonaID || resolved.MemoryNamespace != b.Bot.MemoryNamespace {
		t.Fatalf("runtime resolution crossed the selected Bot boundary: %+v", resolved)
	}
}

func TestPrivateBotSessionSelectionRejectsForeignOwnerScope(t *testing.T) {
	store := newAgentInstanceStore(t)
	a := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "")
	b := createPrivateBotForTest(t, store, "owner-b", "bot-b", "小满", "")
	scope := `{"ownerRef":"owner-a","workspaceId":"ws-1","transport":"qq","transportInstance":"qq-main","conversationRef":"group-1"}`
	first := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+a.Bot.ID+"/select", scope)
	if first.Code != http.StatusOK {
		t.Fatalf("select first owner = %d: %s", first.Code, first.Body.String())
	}
	foreign := strings.Replace(scope, `"ownerRef":"owner-a"`, `"ownerRef":"owner-b"`, 1)
	second := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+b.Bot.ID+"/select", foreign)
	if second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "private_bot_scope_conflict") {
		t.Fatalf("foreign owner scope selection = %d: %s", second.Code, second.Body.String())
	}
	var activeBot string
	if err := store.db.QueryRow(`SELECT bot_id FROM private_bot_sessions WHERE active=1 AND transport='qq' AND transport_instance='qq-main' AND conversation_ref='group-1'`).Scan(&activeBot); err != nil {
		t.Fatal(err)
	}
	if activeBot != a.Bot.ID {
		t.Fatalf("foreign selection changed active bot=%q, want %q", activeBot, a.Bot.ID)
	}
}

func TestPrivateBotPauseAndActivityRecoveryStates(t *testing.T) {
	store := newAgentInstanceStore(t)
	bot := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "")
	paused := privateBotRequest(t, store, http.MethodPut, "/api/v1/private-bots/"+bot.Bot.ID+"?ownerRef=owner-a", `{"status":"paused"}`)
	if paused.Code != http.StatusOK {
		t.Fatalf("pause bot = %d: %s", paused.Code, paused.Body.String())
	}
	selection := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/select", `{"ownerRef":"owner-a","transport":"qq","conversationRef":"group-1"}`)
	if selection.Code != http.StatusConflict || !strings.Contains(selection.Body.String(), "bot_not_active") {
		t.Fatalf("paused bot selected = %d: %s", selection.Code, selection.Body.String())
	}
	active := privateBotRequest(t, store, http.MethodPut, "/api/v1/private-bots/"+bot.Bot.ID+"?ownerRef=owner-a", `{"status":"active"}`)
	if active.Code != http.StatusOK {
		t.Fatalf("resume bot = %d: %s", active.Code, active.Body.String())
	}
	activity := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/activities?ownerRef=owner-a", `{"state":"waiting","kind":"message","inputSummary":"hello"}`)
	if activity.Code != http.StatusCreated {
		t.Fatalf("create activity = %d: %s", activity.Code, activity.Body.String())
	}
	var created struct {
		Data privateBotActivity `json:"data"`
	}
	if err := json.Unmarshal(activity.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Data.State != "waiting" {
		t.Fatalf("activity = %+v", created.Data)
	}
	transition := func(state, code string) *httptest.ResponseRecorder {
		body := `{"state":"` + state + `"}`
		if code != "" {
			body = `{"state":"` + state + `","errorCode":"` + code + `"}`
		}
		return privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/activities/"+created.Data.ID+"?ownerRef=owner-a", body)
	}
	blocked := transition("blocked", "unknown_external")
	if blocked.Code != http.StatusOK || !strings.Contains(blocked.Body.String(), `"state":"blocked"`) || !strings.Contains(blocked.Body.String(), "unknown_external") {
		t.Fatalf("blocked activity = %d: %s", blocked.Code, blocked.Body.String())
	}
	retrying := transition("retrying", "")
	if retrying.Code != http.StatusOK || !strings.Contains(retrying.Body.String(), `"state":"retrying"`) {
		t.Fatalf("retrying activity = %d: %s", retrying.Code, retrying.Body.String())
	}
	completed := transition("completed", "")
	if completed.Code != http.StatusOK || !strings.Contains(completed.Body.String(), `"state":"completed"`) {
		t.Fatalf("completed activity = %d: %s", completed.Code, completed.Body.String())
	}
	terminal := transition("retrying", "")
	if terminal.Code != http.StatusConflict || !strings.Contains(terminal.Body.String(), "activity_terminal") {
		t.Fatalf("terminal activity retry = %d: %s", terminal.Code, terminal.Body.String())
	}
	var attempts int
	if err := store.db.QueryRow(`SELECT attempts FROM private_bot_activities WHERE id=?`, created.Data.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("activity attempts=%d err=%v, want 1", attempts, err)
	}
	failed := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/activities?ownerRef=owner-a", `{"state":"waiting","kind":"message","inputSummary":"retry me"}`)
	if failed.Code != http.StatusCreated {
		t.Fatalf("create retry activity = %d: %s", failed.Code, failed.Body.String())
	}
	var retryCreated struct {
		Data privateBotActivity `json:"data"`
	}
	if err := json.Unmarshal(failed.Body.Bytes(), &retryCreated); err != nil {
		t.Fatal(err)
	}
	failedTransition := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/activities/"+retryCreated.Data.ID+"?ownerRef=owner-a", `{"state":"failed","errorCode":"provider_timeout"}`)
	if failedTransition.Code != http.StatusOK {
		t.Fatalf("failed activity = %d: %s", failedTransition.Code, failedTransition.Body.String())
	}
	retryTransition := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+bot.Bot.ID+"/activities/"+retryCreated.Data.ID+"?ownerRef=owner-a", `{"state":"retrying"}`)
	if retryTransition.Code != http.StatusOK || !strings.Contains(retryTransition.Body.String(), `"state":"retrying"`) {
		t.Fatalf("failed activity recovery = %d: %s", retryTransition.Code, retryTransition.Body.String())
	}
}

func TestPrivateBotTasksExposeDurableRuntimeStateAndRecoveryURL(t *testing.T) {
	store := newAgentInstanceStore(t)
	bot := createPrivateBotForTest(t, store, "owner-a", "bot-task", "豆包任务", "")
	now := "2026-11-02T00:00:00Z"
	if _, err := store.db.Exec(`
		CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, agent_instance_id TEXT NOT NULL, task_id TEXT NOT NULL DEFAULT '',
			task_revision INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL, error_code TEXT,
			input_cipher BLOB, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		CREATE TABLE agent_task_steps (id TEXT PRIMARY KEY, run_id TEXT NOT NULL);
		INSERT INTO agent_runs(id, agent_instance_id, task_id, task_revision, state, error_code, input_cipher, created_at, updated_at)
		VALUES ('run-failed', ?, 'task-1', 2, 'failed', 'provider_timeout', X'01', ?, ?),
		       ('run-running', ?, 'task-2', 1, 'running', '', NULL, ?, ?),
		       ('run-other', 'private-other', 'task-3', 1, 'failed', 'x', X'01', ?, ?);
		INSERT INTO agent_task_steps(id, run_id) VALUES ('step-failed', 'run-failed'), ('step-failed-2', 'run-failed'), ('step-running', 'run-running')
	`, bot.Bot.AgentInstanceID, now, now, bot.Bot.AgentInstanceID, now, now, now, now); err != nil {
		t.Fatal(err)
	}
	rec := privateBotRequest(t, store, http.MethodGet, "/api/v1/private-bots/"+bot.Bot.ID+"/tasks?ownerRef=owner-a", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list private bot tasks = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Items []privateBotTask `json:"items"`
			Total int              `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Total != 2 || len(envelope.Data.Items) != 2 {
		t.Fatalf("tasks = %+v", envelope.Data)
	}
	byID := map[string]privateBotTask{}
	for _, item := range envelope.Data.Items {
		byID[item.RunID] = item
	}
	failed, ok := byID["run-failed"]
	if !ok {
		t.Fatalf("failed task missing = %+v", envelope.Data.Items)
	}
	if !failed.Retryable || failed.RetryURL != "/api/v1/tasks/run-failed/retry" || failed.StepCount != 2 || failed.ErrorCode != "provider_timeout" {
		t.Fatalf("failed task recovery projection = %+v", failed)
	}
	running, ok := byID["run-running"]
	if !ok || running.Retryable || running.RetryURL != "" {
		t.Fatalf("running task unexpectedly retryable = %+v", running)
	}
}

func TestPrivateBotRoleSwitchDoesNotMutateGlobalActivePersona(t *testing.T) {
	store := newAgentInstanceStore(t)
	if _, err := store.db.Exec(`INSERT INTO personas(id, name, created_at, updated_at) VALUES ('persona-alt', 'Alternate persona', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO runtime_config(id, active_persona_id, updated_at) VALUES (1, 'persona-test', 'now')`); err != nil {
		t.Fatal(err)
	}
	bot := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "")
	t.Setenv("ERDAI_ROLE_SWITCH_OPENIDS", "admin-1")
	runtime := &AgentRuntime{configStore: store}
	run := runRecord{AgentInstanceID: bot.Bot.AgentInstanceID, PersonaID: bot.Bot.PersonaID, SenderRef: "admin-1", IsAdmin: true}
	reply, handled, err := runtime.roleCommandReply(run, "/切换角色 persona-alt")
	if err != nil || !handled || !strings.Contains(reply.Text, "Alternate persona") {
		t.Fatalf("private bot role switch = handled:%v reply:%q err:%v", handled, reply.Text, err)
	}
	var global, botPersona string
	if err := store.db.QueryRow(`SELECT active_persona_id FROM runtime_config WHERE id=1`).Scan(&global); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT persona_id FROM private_bots WHERE id=?`, bot.Bot.ID).Scan(&botPersona); err != nil {
		t.Fatal(err)
	}
	if global != "persona-test" || botPersona != "persona-alt" {
		t.Fatalf("role switch leaked across ownership boundary: global=%q bot=%q", global, botPersona)
	}
}

func TestPrivateBotTemplateAndSharedCollectionBoundaries(t *testing.T) {
	store := newAgentInstanceStore(t)
	template := createPrivateBotForTest(t, store, "owner-a", "bot-template", "模板", "")
	if response := privateBotRequest(t, store, http.MethodPut, "/api/v1/private-bots/"+template.Bot.ID+"?ownerRef=owner-a", `{"visibility":"template"}`); response.Code != http.StatusOK {
		t.Fatalf("mark template = %d: %s", response.Code, response.Body.String())
	}
	// A template is readable and copyable by another owner, while the copied
	// Bot receives a private runtime instance and namespace.
	otherRead := privateBotRequest(t, store, http.MethodGet, "/api/v1/private-bots/"+template.Bot.ID+"?ownerRef=owner-b", "")
	if otherRead.Code != http.StatusOK {
		t.Fatalf("template read = %d: %s", otherRead.Code, otherRead.Body.String())
	}
	copy := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+template.Bot.ID+"/copy", `{"ownerRef":"owner-b","displayName":"我的副本","idempotencyKey":"template-copy"}`)
	if copy.Code != http.StatusCreated || !strings.Contains(copy.Body.String(), `"sourceBotId":"bot-template"`) {
		t.Fatalf("template copy = %d: %s", copy.Code, copy.Body.String())
	}

	shared := createPrivateBotForTest(t, store, "owner-a", "bot-shared", "共享", "")
	if response := privateBotRequest(t, store, http.MethodPut, "/api/v1/private-bots/"+shared.Bot.ID+"?ownerRef=owner-a", `{"visibility":"shared"}`); response.Code != http.StatusOK {
		t.Fatalf("mark shared = %d: %s", response.Code, response.Body.String())
	}
	grant := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+shared.Bot.ID+"/acl?ownerRef=owner-a", `{"subjectRef":"owner-b","role":"viewer"}`)
	if grant.Code != http.StatusOK {
		t.Fatalf("grant shared ACL = %d: %s", grant.Code, grant.Body.String())
	}
	list := privateBotRequest(t, store, http.MethodGet, "/api/v1/private-bots?ownerRef=owner-b&workspaceId=default", "")
	if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), `"id":"bot-shared"`) {
		t.Fatalf("shared collection = %d: %s", list.Code, list.Body.String())
	}
	aclRead := privateBotRequest(t, store, http.MethodGet, "/api/v1/private-bots/"+shared.Bot.ID+"/acl?ownerRef=owner-b", "")
	if aclRead.Code != http.StatusOK || !strings.Contains(aclRead.Body.String(), `"subjectRef":"owner-b"`) {
		t.Fatalf("shared ACL read = %d: %s", aclRead.Code, aclRead.Body.String())
	}
}

func TestPrivateBotActivityRejectsForeignSessionAndOversizedSummary(t *testing.T) {
	store := newAgentInstanceStore(t)
	a := createPrivateBotForTest(t, store, "owner-a", "bot-a", "豆包", "")
	b := createPrivateBotForTest(t, store, "owner-a", "bot-b", "小满", "")
	selection := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+a.Bot.ID+"/select", `{"ownerRef":"owner-a","transport":"qq","conversationRef":"group-a"}`)
	if selection.Code != http.StatusOK {
		t.Fatalf("create session = %d: %s", selection.Code, selection.Body.String())
	}
	var session struct {
		Data privateBotSession `json:"data"`
	}
	if err := json.Unmarshal(selection.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	foreign := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+b.Bot.ID+"/activities?ownerRef=owner-a", `{"sessionId":"`+session.Data.ID+`","inputSummary":"hello"}`)
	if foreign.Code != http.StatusBadRequest || !strings.Contains(foreign.Body.String(), "does not belong") {
		t.Fatalf("foreign session activity = %d: %s", foreign.Code, foreign.Body.String())
	}
	oversized := `{"inputSummary":"` + strings.Repeat("x", 2001) + `"}`
	tooLarge := privateBotRequest(t, store, http.MethodPost, "/api/v1/private-bots/"+a.Bot.ID+"/activities?ownerRef=owner-a", oversized)
	if tooLarge.Code != http.StatusBadRequest || !strings.Contains(tooLarge.Body.String(), "inputSummary") {
		t.Fatalf("oversized activity = %d: %s", tooLarge.Code, tooLarge.Body.String())
	}
}
