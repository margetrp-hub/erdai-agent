package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type modelCheckpointFixture struct {
	runtime         *AgentRuntime
	run             runRecord
	policy          runtimeToolPolicy
	targets         []runtimeProviderTarget
	modelCalls      atomic.Int32
	plans           atomic.Int32
	allowCompletion atomic.Bool
	emptyFirstReply atomic.Bool
	duplicateTools  atomic.Bool
	lastToolCallID  atomic.Value
	lastToolCallIDs atomic.Value
}

func newModelCheckpointFixture(t *testing.T) *modelCheckpointFixture {
	t.Helper()
	f := &modelCheckpointFixture{runtime: newTaskIntentTestRuntime(t)}
	f.run = insertHonestyTestRun(t, f.runtime, "checkpoint-run", "checkpoint-group", "member-a", "group", "running", time.Now())
	input, err := f.runtime.encrypt([]byte("Recall my favorite drink."))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.runtime.db.Exec(`UPDATE agent_runs SET input_cipher=? WHERE id=?`, input, f.run.ID); err != nil {
		t.Fatal(err)
	}
	f.policy = runtimeToolPolicy{Authority: "member", MaxAgentSteps: 3, Tools: []runtimeTool{{
		ID: "recall", Name: "memory_search", AdapterRef: "memory_recall", ApprovalMode: "auto",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{
			"query": map[string]string{"type": "string"},
		}},
	}}}
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.modelCalls.Add(1)
		if f.emptyFirstReply.CompareAndSwap(true, false) {
			writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": ""},
			}}})
			return
		}
		var request struct {
			Messages []struct {
				Role       string `json:"role"`
				ToolCallID string `json:"tool_call_id"`
				Content    any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var toolCallIDs []string
		for _, message := range request.Messages {
			if message.Role != "tool" {
				continue
			}
			toolCallIDs = append(toolCallIDs, message.ToolCallID)
		}
		if len(toolCallIDs) > 0 {
			f.lastToolCallID.Store(toolCallIDs[0])
			f.lastToolCallIDs.Store(toolCallIDs)
			if !f.allowCompletion.Load() {
				http.Error(w, "injected continuation failure", http.StatusServiceUnavailable)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"usage": map[string]int{
				"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			}, "choices": []any{map[string]any{
				"message": map[string]string{"role": "assistant", "content": "Done."},
			}}})
			return
		}
		// Replanning receives a different ID, so a tool-only cache cannot hide it.
		callID := fmt.Sprintf("recall-%d", f.plans.Add(1))
		calls := []any{toolCallResponse(callID, "memory_search", `{"query":"favorite drink"}`)}
		if f.duplicateTools.Load() {
			calls = append(calls, toolCallResponse(callID+"-second", "memory_search", `{"query":"favorite drink"}`))
		}
		writeJSON(w, http.StatusOK, map[string]any{"usage": map[string]int{
			"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
		}, "choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "tool_calls": calls},
		}}})
	}))
	t.Cleanup(service.Close)
	f.runtime.client = service.Client()
	f.targets = []runtimeProviderTarget{{EndpointID: "checkpoint-model", Model: "checkpoint-model", APIBase: service.URL}}
	return f
}

func (f *modelCheckpointFixture) execute(policy runtimeToolPolicy, messages runtimeMessagePolicy) (agentReply, error) {
	return f.runtime.runAgentLoopWithTargets(context.Background(), f.run, "Recall my favorite drink.", "", f.targets, policy, messages)
}

func (f *modelCheckpointFixture) interruptAndRecover(t *testing.T) {
	t.Helper()
	if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
		t.Fatal("injected continuation failure did not fail the loop")
	}
	if got := f.modelCalls.Load(); got != 2 {
		t.Fatalf("initial model calls=%d, want plan and failed continuation", got)
	}
	var completed int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='model' AND status='succeeded'`, f.run.ID).Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("successful model checkpoints=%d err=%v", completed, err)
	}
	if err := recoverInterruptedRuntime(context.Background(), f.runtime.db); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := f.runtime.db.QueryRow(`SELECT state FROM agent_runs WHERE id=?`, f.run.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("recovered state=%q err=%v", state, err)
	}
	f.allowCompletion.Store(true)
}

func TestModelCheckpointPersistenceFailureStopsToolsAndProgress(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.allowCompletion.Store(true)
	if _, err := f.runtime.db.Exec(`CREATE TRIGGER reject_model_completion BEFORE UPDATE ON agent_task_steps
		WHEN NEW.kind='model' AND NEW.status='succeeded'
		BEGIN SELECT RAISE(FAIL, 'injected model checkpoint failure'); END`); err != nil {
		t.Fatal(err)
	}
	enabled := true
	_, err := f.execute(f.policy, runtimeMessagePolicy{
		ToolProgressEnabled: &enabled, ToolProgressSearchEnabled: &enabled,
		ToolProgressSearchMessages: []string{"Checking memory."},
	})
	if err == nil {
		t.Error("checkpoint write failure was ignored")
	}
	if got := f.modelCalls.Load(); got != 1 {
		t.Errorf("model calls=%d, want only the unpersisted plan", got)
	}
	for name, query := range map[string]string{
		"tool steps":      `SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='tool'`,
		"tool executions": `SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`,
		"deliveries":      `SELECT count(*) FROM agent_deliveries WHERE run_id=?`,
	} {
		var count int
		if err := f.runtime.db.QueryRow(query, f.run.ID).Scan(&count); err != nil || count != 0 {
			t.Errorf("%s after failed plan=%d err=%v", name, count, err)
		}
	}
}

func TestModelCheckpointRecoveryReusesPlanAndSuccessfulTool(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.interruptAndRecover(t)
	reply, err := f.execute(f.policy, runtimeMessagePolicy{})
	if err != nil || reply.Text != "Done." {
		t.Fatalf("resume reply=%+v err=%v", reply, err)
	}
	if got := f.modelCalls.Load(); got != 3 {
		t.Errorf("model calls=%d, want original plan, failed continuation, resumed continuation", got)
	}
	if got := f.plans.Load(); got != 1 {
		t.Errorf("completed plan was requested again: plans=%d", got)
	}
	if got := f.lastToolCallID.Load(); got != "recall-1" {
		t.Errorf("restored tool call ID=%v, want original recall-1", got)
	}
	var executions, attempts int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
		t.Errorf("memory tool executions=%d err=%v", executions, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool' AND status='succeeded'`, f.run.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Errorf("successful tool attempts=%d err=%v", attempts, err)
	}
}

func TestModelCheckpointRecoveryRejectsChangedAuthorityAndStaleTask(t *testing.T) {
	for _, scenario := range []string{"tool_policy_changed", "cancelled", "superseded_revision"} {
		t.Run(scenario, func(t *testing.T) {
			f := newModelCheckpointFixture(t)
			f.interruptAndRecover(t)
			policy := f.policy
			switch scenario {
			case "tool_policy_changed":
				policy.Tools = nil
			case "cancelled":
				if _, err := f.runtime.db.Exec(`UPDATE agent_runs SET state='cancelled' WHERE id=?`, f.run.ID); err != nil {
					t.Fatal(err)
				}
			case "superseded_revision":
				newer := insertHonestyTestRun(t, f.runtime, "newer-run", f.run.ConversationRef, f.run.SenderRef, "group", "queued", time.Now())
				if _, err := f.runtime.db.Exec(`UPDATE agent_runs SET task_id='shared-task', task_scope_key='shared-scope', task_revision=CASE WHEN id=? THEN 2 ELSE 1 END WHERE id IN (?,?)`, newer.ID, f.run.ID, newer.ID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.execute(policy, runtimeMessagePolicy{}); err == nil {
				t.Error("incompatible or stale checkpoint resumed without error")
			}
			if got := f.modelCalls.Load(); got != 2 {
				t.Errorf("model called after rejected resume: calls=%d", got)
			}
			var count int
			if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&count); err != nil || count != 1 {
				t.Errorf("tool executions after rejected resume=%d err=%v", count, err)
			}
		})
	}
}

func TestModelCheckpointCorruptionDoesNotReplan(t *testing.T) {
	for _, scenario := range []string{"invalid_ciphertext", "invalid_json", "missing_output"} {
		t.Run(scenario, func(t *testing.T) {
			f := newModelCheckpointFixture(t)
			f.interruptAndRecover(t)
			var ciphertext []byte
			switch scenario {
			case "invalid_ciphertext":
				ciphertext = []byte("not-encrypted")
			case "invalid_json":
				var err error
				ciphertext, err = f.runtime.encrypt([]byte("not-json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.runtime.db.Exec(`UPDATE agent_task_steps SET output_cipher=? WHERE run_id=? AND kind='model' AND status='succeeded'`, ciphertext, f.run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
				t.Error("corrupt successful model checkpoint was ignored")
			}
			if got := f.modelCalls.Load(); got != 2 {
				t.Errorf("corrupt checkpoint caused replanning: model calls=%d", got)
			}
		})
	}
}

func TestModelCheckpointCancellationAfterSaveStopsBeforeTool(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.allowCompletion.Store(true)
	if _, err := f.runtime.db.Exec(`CREATE TRIGGER cancel_after_model_completion AFTER UPDATE ON agent_task_steps
		WHEN NEW.kind='model' AND NEW.status='succeeded' AND NEW.step_index=0
		BEGIN UPDATE agent_runs SET state='cancelled' WHERE id=NEW.run_id; END`); err != nil {
		t.Fatal(err)
	}
	enabled := true
	policy := runtimeMessagePolicy{ToolProgressEnabled: &enabled, ToolProgressSearchEnabled: &enabled,
		ToolProgressSearchMessages: []string{"Checking memory."}}
	if _, err := f.execute(f.policy, policy); err == nil {
		t.Error("cancellation after saving the plan did not stop execution")
	}
	if got := f.modelCalls.Load(); got != 1 {
		t.Errorf("model calls before cancellation=%d, want 1", got)
	}
	var completed, tools, deliveries int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='model' AND status='succeeded'`, f.run.ID).Scan(&completed); err != nil || completed != 1 {
		t.Fatalf("saved model checkpoint count=%d err=%v", completed, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='tool'`, f.run.ID).Scan(&tools); err != nil || tools != 0 {
		t.Errorf("tool steps after cancellation=%d err=%v", tools, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_deliveries WHERE run_id=?`, f.run.ID).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Errorf("deliveries after cancellation=%d err=%v", deliveries, err)
	}
	if _, err := f.runtime.db.Exec(`DROP TRIGGER cancel_after_model_completion`); err != nil {
		t.Fatal(err)
	}
	// The fixture permits continuation; production cancellation is not undone here.
	if _, err := f.runtime.db.Exec(`UPDATE agent_runs SET state='running' WHERE id=?`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	reply, err := f.execute(f.policy, policy)
	if err != nil || reply.Text != "Done." {
		t.Fatalf("saved-plan continuation reply=%+v err=%v", reply, err)
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
		t.Errorf("saved plan was repeated: calls=%d plans=%d", calls, plans)
	}
	var executions int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
		t.Errorf("continued tool executions=%d err=%v", executions, err)
	}
}

func TestModelCheckpointCompletedRunDoesNotRepeatCallsOrUsage(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.allowCompletion.Store(true)
	for attempt := 0; attempt < 2; attempt++ {
		reply, err := f.execute(f.policy, runtimeMessagePolicy{})
		if err != nil || reply.Text != "Done." {
			t.Fatalf("attempt %d reply=%+v err=%v", attempt, reply, err)
		}
		if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
			t.Errorf("attempt %d repeated completion: calls=%d plans=%d", attempt, calls, plans)
		}
		var providerCalls, usageEvents, tokens, toolAttempts int
		if err := f.runtime.db.QueryRow(`SELECT provider_calls FROM agent_runs WHERE id=?`, f.run.ID).Scan(&providerCalls); err != nil || providerCalls != 2 {
			t.Errorf("attempt %d provider calls=%d err=%v", attempt, providerCalls, err)
		}
		if err := f.runtime.configStore.db.QueryRow(`SELECT count(*), COALESCE(sum(total_tokens),0) FROM model_usage_events WHERE run_id=?`, f.run.ID).Scan(&usageEvents, &tokens); err != nil || usageEvents != 2 || tokens != 30 {
			t.Errorf("attempt %d usage events=%d tokens=%d err=%v", attempt, usageEvents, tokens, err)
		}
		if err := f.runtime.db.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool' AND status='succeeded'`, f.run.ID).Scan(&toolAttempts); err != nil || toolAttempts != 1 {
			t.Errorf("attempt %d successful tool attempts=%d err=%v", attempt, toolAttempts, err)
		}
	}
}

func TestModelCheckpointCorruptToolReceiptStopsRecovery(t *testing.T) {
	for _, scenario := range []string{"invalid_ciphertext", "invalid_json", "missing_output"} {
		t.Run(scenario, func(t *testing.T) {
			f := newModelCheckpointFixture(t)
			f.interruptAndRecover(t)
			var ciphertext []byte
			switch scenario {
			case "invalid_ciphertext":
				ciphertext = []byte("not-encrypted")
			case "invalid_json":
				var err error
				ciphertext, err = f.runtime.encrypt([]byte("not-json"))
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := f.runtime.db.Exec(`UPDATE agent_task_steps SET output_cipher=? WHERE run_id=? AND kind='tool' AND status='succeeded'`, ciphertext, f.run.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
				t.Error("corrupt successful tool receipt was ignored")
			}
			if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
				t.Errorf("corrupt tool receipt triggered a model: calls=%d plans=%d", calls, plans)
			}
			var executions, attempts int
			if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
				t.Errorf("corrupt receipt repeated tool: executions=%d err=%v", executions, err)
			}
			if err := f.runtime.db.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool'`, f.run.ID).Scan(&attempts); err != nil || attempts != 1 {
				t.Errorf("corrupt tool receipt reset attempts: attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestModelCheckpointDistinctToolCallsKeepSeparateReceipts(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.duplicateTools.Store(true)
	f.interruptAndRecover(t)
	reply, err := f.execute(f.policy, runtimeMessagePolicy{})
	if err != nil || reply.Text != "Done." {
		t.Fatalf("two-call recovery reply=%+v err=%v", reply, err)
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 3 || plans != 1 {
		t.Errorf("two-call recovery repeated plan: calls=%d plans=%d", calls, plans)
	}
	callIDs, ok := f.lastToolCallIDs.Load().([]string)
	if !ok || len(callIDs) != 2 || callIDs[0] != "recall-1" || callIDs[1] != "recall-1-second" {
		t.Errorf("restored distinct tool calls=%v", callIDs)
	}
	var executions, receipts, attempts int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 2 {
		t.Errorf("distinct tool executions=%d err=%v", executions, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*), COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool' AND status='succeeded'`, f.run.ID).Scan(&receipts, &attempts); err != nil || receipts != 2 || attempts != 2 {
		t.Errorf("distinct receipts=%d attempts=%d err=%v", receipts, attempts, err)
	}
}

func TestModelCheckpointDynamicPromptChangeKeepsCompatibleAppearance(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.run.PersonaID = "doubao"
	if _, err := f.runtime.db.Exec(`UPDATE agent_runs SET persona_id=? WHERE id=?`, f.run.PersonaID, f.run.ID); err != nil {
		t.Fatal(err)
	}
	var libraryID, libraryVersion, bindingVersion string
	if err := f.runtime.configStore.db.QueryRow(`SELECT l.id,l.updated_at,p.updated_at
		FROM persona_appearance_libraries p JOIN appearance_libraries l ON l.id=p.library_id
		WHERE p.persona_id=?`, f.run.PersonaID).Scan(&libraryID, &libraryVersion, &bindingVersion); err != nil {
		t.Fatal(err)
	}
	f.interruptAndRecover(t)
	for _, dynamicPrompt := range []string{
		"Current time is 12:01. The conversation mood is relaxed.",
		"Current time is 12:02. A new unrelated conversation message arrived.",
	} {
		reply, err := f.runtime.runAgentLoopWithTargets(context.Background(), f.run,
			"Recall my favorite drink.", dynamicPrompt, f.targets, f.policy, runtimeMessagePolicy{})
		if err != nil || reply.Text != "Done." {
			t.Fatalf("dynamic prompt invalidated compatible checkpoint: reply=%+v err=%v", reply, err)
		}
		if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 3 || plans != 1 {
			t.Errorf("dynamic prompt repeated paid work: calls=%d plans=%d", calls, plans)
		}
		var executions int
		if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
			t.Errorf("dynamic prompt repeated tool: executions=%d err=%v", executions, err)
		}
	}
	var currentLibraryID, currentLibraryVersion, currentBindingVersion string
	if err := f.runtime.configStore.db.QueryRow(`SELECT l.id,l.updated_at,p.updated_at
		FROM persona_appearance_libraries p JOIN appearance_libraries l ON l.id=p.library_id
		WHERE p.persona_id=?`, f.run.PersonaID).Scan(&currentLibraryID, &currentLibraryVersion, &currentBindingVersion); err != nil {
		t.Fatal(err)
	}
	if currentLibraryID != libraryID || currentLibraryVersion != libraryVersion || currentBindingVersion != bindingVersion {
		t.Error("resuming a plan changed the appearance library or binding")
	}
}

func TestModelCheckpointRecoveryRejectsChangedRuntimeConfiguration(t *testing.T) {
	for _, scenario := range []string{"protected_rules", "content_boundary", "persona", "appearance_version", "appearance_binding_version"} {
		t.Run(scenario, func(t *testing.T) {
			f := newModelCheckpointFixture(t)
			f.run.PersonaID = "doubao"
			if _, err := f.runtime.db.Exec(`UPDATE agent_runs SET persona_id=? WHERE id=?`, f.run.PersonaID, f.run.ID); err != nil {
				t.Fatal(err)
			}
			f.interruptAndRecover(t)
			query := ""
			switch scenario {
			case "protected_rules":
				query = `UPDATE runtime_config SET protected_rules=protected_rules || ' Require current task authorization.' WHERE id=1`
			case "content_boundary":
				boundary, err := f.runtime.configStore.contentBoundaryPolicy()
				if err != nil {
					t.Fatal(err)
				}
				boundary.ModelInstruction += " Recheck current content constraints."
				setTestIntegration(t, f.runtime.configStore.db, "content_boundary_policy", boundary)
			case "persona":
				query = `UPDATE personas SET personality=personality || ' Keep all replies concise.' WHERE id='doubao'`
			case "appearance_version":
				query = `UPDATE appearance_libraries SET updated_at=updated_at || '-new-version'
					WHERE id=(SELECT library_id FROM persona_appearance_libraries WHERE persona_id='doubao')`
			case "appearance_binding_version":
				query = `UPDATE persona_appearance_libraries SET updated_at=updated_at || '-new-binding' WHERE persona_id='doubao'`
			}
			if query != "" {
				result, err := f.runtime.configStore.db.Exec(query)
				if err != nil {
					t.Fatal(err)
				}
				if count, err := result.RowsAffected(); err != nil || count != 1 {
					t.Fatalf("configuration fixture changed rows=%d err=%v", count, err)
				}
			}
			if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
				t.Error("changed authoritative configuration resumed an old plan")
			}
			if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
				t.Errorf("configuration change triggered new model work: calls=%d plans=%d", calls, plans)
			}
			var executions, attempts int
			if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
				t.Errorf("configuration change repeated tool: executions=%d err=%v", executions, err)
			}
			if err := f.runtime.db.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool'`, f.run.ID).Scan(&attempts); err != nil || attempts != 1 {
				t.Errorf("configuration change reset tool receipt: attempts=%d err=%v", attempts, err)
			}
		})
	}
}

func TestModelCheckpointRecoveryRejectsReducedStepBudget(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.interruptAndRecover(t)
	policy := f.policy
	policy.MaxAgentSteps = 1
	if _, err := f.execute(policy, runtimeMessagePolicy{}); err == nil {
		t.Error("reduced execution budget accepted an old plan")
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
		t.Errorf("reduced budget repeated model work: calls=%d plans=%d", calls, plans)
	}
	var restored, executions int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='model_restored'`, f.run.ID).Scan(&restored); err != nil || restored != 0 {
		t.Errorf("reduced budget restored a plan: restored=%d err=%v", restored, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
		t.Errorf("reduced budget repeated tool: executions=%d err=%v", executions, err)
	}
}

func TestModelCheckpointEmptyResponseCanRetryValidCompletion(t *testing.T) {
	f := newModelCheckpointFixture(t)
	f.emptyFirstReply.Store(true)
	f.allowCompletion.Store(true)
	if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
		t.Error("empty response was accepted")
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 1 || plans != 0 {
		t.Fatalf("unexpected initial empty response work: calls=%d plans=%d", calls, plans)
	}
	var succeeded, failed, tools int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='model' AND status='succeeded'`, f.run.ID).Scan(&succeeded); err != nil || succeeded != 0 {
		t.Errorf("empty response persisted as reusable success: succeeded=%d err=%v", succeeded, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='model' AND status='failed'`, f.run.ID).Scan(&failed); err != nil || failed != 1 {
		t.Errorf("empty response not retained as retryable failure: failed=%d err=%v", failed, err)
	}
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM agent_task_steps WHERE run_id=? AND kind='tool'`, f.run.ID).Scan(&tools); err != nil || tools != 0 {
		t.Errorf("empty response executed a tool: tools=%d err=%v", tools, err)
	}
	reply, err := f.execute(f.policy, runtimeMessagePolicy{})
	if err != nil || reply.Text != "Done." {
		t.Fatalf("valid retry remained stuck on empty response: reply=%+v err=%v", reply, err)
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 3 || plans != 1 {
		t.Errorf("unexpected valid retry work: calls=%d plans=%d", calls, plans)
	}
	var executions int
	if err := f.runtime.db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
		t.Errorf("valid retry tool executions=%d err=%v", executions, err)
	}
}
