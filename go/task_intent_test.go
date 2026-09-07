package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func admitTestTaskIntent(t *testing.T, a *AgentRuntime, run runRecord, message string) *TaskIntent {
	t.Helper()
	input, err := a.encrypt([]byte(message))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := a.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.Exec(`INSERT INTO agent_runs (id,event_id,message_id,reply_to_message_id,transport,transport_instance,
		conversation_ref,thread_key,sender_ref,agent_instance_id,memory_namespace,persona_id,reply_handle,input_cipher,state,created_at,updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,'queued',?,?)`, run.ID, run.ID, run.MessageID, run.ReplyToMessageID, run.Transport, run.TransportInstance,
		run.ConversationRef, run.ThreadKey, run.SenderRef, run.AgentInstanceID, run.MemoryNamespace, run.PersonaID, "reply", input, now, now)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := a.admitTaskIntentTx(context.Background(), tx, run, message)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return intent
}

func intentTestRun(id string) runRecord {
	return runRecord{ID: id, MessageID: id + "-message", Transport: "qq_official", TransportInstance: "qq-test",
		ConversationRef: "test-group", ThreadKey: "test-thread", SenderRef: "member-a", AgentInstanceID: "agent-a", MemoryNamespace: "agent-a", PersonaID: "persona-a"}
}

func newTaskIntentTestRuntime(t *testing.T) *AgentRuntime {
	a := newDormantRuntime(t)
	a.lifecycle = context.Background()
	return a
}

func TestTaskIntentRevisionCancelsStaleMediaAndLeases(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("intent-first")
	initial := admitTestTaskIntent(t, a, first, "给我拍一张自拍")
	if initial == nil || initial.Revision != 1 {
		t.Fatalf("initial = %#v", initial)
	}
	if err := a.enqueueAgentReply(first, agentReply{Attachments: []agentAttachment{{Kind: "image", Name: "first.png", LocalPath: "/test/first.png", MimeType: "image/png"}}}, ""); err != nil {
		t.Fatal(err)
	}
	leased, err := a.leaseTransportDeliveries(context.Background(), "test-consumer", 1, 30)
	if err != nil || len(leased) != 1 {
		t.Fatalf("leased=%#v err=%v", leased, err)
	}
	second := intentTestRun("intent-second")
	correction := admitTestTaskIntent(t, a, second, "不要总是紫色，穿着短一点")
	if correction.TaskID != initial.TaskID || correction.Revision != 2 || !strings.Contains(correction.prompt(), "短一点") {
		t.Fatalf("correction=%#v", correction)
	}
	if err = a.ensureMediaTaskCurrent(context.Background(), first); !errors.Is(err, errTaskSuperseded) {
		t.Fatalf("old guard=%v", err)
	}
	if err = a.ensureDeliveryTaskCurrent(context.Background(), leased[0].ID); !errors.Is(err, errTaskSuperseded) {
		t.Fatalf("send guard=%v", err)
	}
	if err = a.enqueueAgentReply(first, agentReply{Text: "old result"}, ""); !errors.Is(err, errStaleTerminalReply) {
		t.Fatalf("enqueue=%v", err)
	}
	if err = a.ensureMediaTaskCurrent(context.Background(), second); err != nil {
		t.Fatal(err)
	}
}

func TestTaskIntentIsolationReplyTargetAndNewRequest(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("intent-isolation-first")
	initial := admitTestTaskIntent(t, a, first, "给我拍一张红色衣服的自拍")
	if initial == nil {
		t.Fatal("explicit selfie request did not create task intent")
	}
	for _, dimension := range []string{"sender", "persona", "instance", "thread", "group"} {
		run := intentTestRun("intent-other-" + dimension)
		switch dimension {
		case "sender":
			run.SenderRef = "other"
		case "persona":
			run.PersonaID = "other"
		case "instance":
			run.AgentInstanceID = "other"
		case "thread":
			run.ThreadKey = "other"
		case "group":
			run.ConversationRef = "other"
		}
		intent := admitTestTaskIntent(t, a, run, "继续")
		if intent.Clarification == "" || intent.PreviousRunID != "" {
			t.Fatalf("%s leaked %#v", dimension, intent)
		}
	}
	freshRun := intentTestRun("intent-new-request")
	fresh := admitTestTaskIntent(t, a, freshRun, "生成一个海边视频")
	if fresh == nil {
		t.Fatal("explicit video request did not create task intent")
	}
	if fresh.TaskID == initial.TaskID || len(fresh.Constraints) != 0 || strings.Contains(fresh.prompt(), "红色") {
		t.Fatalf("new inherited constraints: %#v", fresh)
	}
	reply := intentTestRun("intent-reply-target")
	reply.ReplyToMessageID = first.MessageID
	target := admitTestTaskIntent(t, a, reply, "重来")
	if target.TaskID != initial.TaskID || target.PreviousRunID != first.ID {
		t.Fatalf("reply target=%#v", target)
	}
	if err := a.ensureMediaTaskCurrent(context.Background(), freshRun); err != nil {
		t.Fatalf("unrelated task cancelled: %v", err)
	}
	for index, text := range []string{"新任务，把logo颜色改成绿色", "另外，把背景换成学校"} {
		intent := admitTestTaskIntent(t, a, intentTestRun("explicit-new-"+itoa(index)), text)
		if intent == nil || intent.Action != "new" || intent.PreviousRunID != "" || len(intent.Constraints) > 0 {
			t.Fatalf("new marker inherited: %#v", intent)
		}
	}
}

func TestTaskIntentStopExpiryAndOrdinaryChat(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	chat := admitTestTaskIntent(t, a, intentTestRun("intent-chat"), "你好")
	if chat != nil {
		t.Fatalf("chat created task=%#v", chat)
	}
	first := intentTestRun("intent-stop-first")
	admitTestTaskIntent(t, a, first, "生成一段视频")
	stop := intentTestRun("intent-stop")
	intent := admitTestTaskIntent(t, a, stop, "等下")
	if intent.Action != "stop" || intent.PreviousRunID != first.ID {
		t.Fatalf("stop=%#v", intent)
	}
	_, reply, err := a.prepareTaskIntent(context.Background(), stop, "等下")
	if err != nil || reply == nil || !strings.Contains(reply.Text, "停止") {
		t.Fatalf("stop reply=%#v err=%v", reply, err)
	}
	if _, err = a.db.Exec("UPDATE agent_runs SET created_at=? WHERE task_scope_key=?", time.Now().Add(-7*time.Hour).UTC().Format(time.RFC3339Nano), taskScopeKey(first)); err != nil {
		t.Fatal(err)
	}
	expired := admitTestTaskIntent(t, a, intentTestRun("intent-expired"), "继续")
	if expired.Clarification == "" {
		t.Fatalf("expired task reused=%#v", expired)
	}
}

func TestTaskIntentTemporaryConstraintsAreNotStableMemory(t *testing.T) {
	for _, text := range []string{"这次不要紫色", "短一点", "不要总是紫色", "这张我喜欢红色", "短一点，我喜欢红色"} {
		if !transientTaskConstraint(text) {
			t.Errorf("temporary constraint captured: %s", text)
		}
	}
	for _, text := range []string{"以后记住我喜欢蓝色", "我喜欢游泳"} {
		if transientTaskConstraint(text) {
			t.Errorf("explicit preference suppressed: %s", text)
		}
	}
}

func TestTaskIntentPersistentMediaKeysIgnoreVolatileCallID(t *testing.T) {
	one := chatToolCall{ID: "call-first"}
	one.Function.Name = "grok_generate_image"
	one.Function.Arguments = `{"prompt":"same request"}`
	two := one
	two.ID = "call-after-restart"
	a, _ := json.Marshal(persistentToolInput(one))
	b, _ := json.Marshal(persistentToolInput(two))
	if string(a) != string(b) {
		t.Fatalf("unstable media key: %s != %s", a, b)
	}
	one.Function.Name = "search_web"
	two.Function.Name = "search_web"
	a, _ = json.Marshal(persistentToolInput(one))
	b, _ = json.Marshal(persistentToolInput(two))
	if string(a) == string(b) {
		t.Fatal("intentional nonmedia calls unexpectedly merged")
	}
}

func TestTaskIntentUnknownMediaReceiptSurvivesRestartAndRetry(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	run := intentTestRun("intent-unknown")
	admitTestTaskIntent(t, a, run, "生成一张图片")
	id, err := a.beginTaskStep(run.ID, "", "tool", "grok_generate_image", 0, map[string]string{"prompt": "test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = recoverInterruptedRuntime(context.Background(), a.db); err != nil {
		t.Fatal(err)
	}
	if err = a.uncertainTaskOperation(id); !errors.Is(err, errTaskExecutionUncertain) {
		t.Fatalf("unknown receipt=%v", err)
	}
	called := false
	_, err = a.executePersistentOperation(run, "generate_image", map[string]string{"prompt": "paraphrased after upgrade"}, func() (toolResult, error) { called = true; return toolResult{}, nil })
	if !errors.Is(err, errTaskExecutionUncertain) || called {
		t.Fatalf("legacy receipt bypassed: called=%v err=%v", called, err)
	}
	if _, err = a.db.Exec("UPDATE agent_runs SET state='failed' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.retryTask(context.Background(), run.ID); err == nil {
		t.Fatal("unknown provider execution was allowed to retry")
	}
	if err = a.finishTaskStep("missing-row", "succeeded", "", toolResult{}); err == nil {
		t.Fatal("lost receipt reported as persisted")
	}
}

func TestTaskIntentOutboundReplyUsesOriginalTaskAfterNewerRequest(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("outbound-original")
	initial := admitTestTaskIntent(t, a, first, "给我拍一张自拍")
	if err := a.enqueueAgentReply(first, agentReply{Text: "done"}, ""); err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	if err := a.db.QueryRow("SELECT id FROM agent_deliveries WHERE run_id=?", first.ID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := a.recordTaskOutboundMessage(context.Background(), deliveryID, "text", "bot-photo-message"); err != nil {
		t.Fatal(err)
	}
	connector := &qqOfficialConnector{runtime: a, platform: mgmtPlatform{ID: first.TransportInstance}}
	if !connector.isOutboundMessage("bot-photo-message") {
		t.Fatal("new connector lost durable outbound receipt")
	}
	other := &qqOfficialConnector{runtime: a, platform: mgmtPlatform{ID: "other-connector"}}
	if other.isOutboundMessage("bot-photo-message") {
		t.Fatal("outbound receipt leaked across connectors")
	}
	if _, err := a.db.Exec("UPDATE platform_sent_delivery_parts SET sent_at=? WHERE delivery_id=?", time.Now().UTC().Add(-7*time.Hour).Format(time.RFC3339Nano), deliveryID); err != nil {
		t.Fatal(err)
	}
	if connector.isOutboundMessage("bot-photo-message") {
		t.Fatal("expired outbound receipt remained wakeable")
	}
	if _, err := a.db.Exec("UPDATE platform_sent_delivery_parts SET sent_at=? WHERE delivery_id=?", time.Now().UTC().Format(time.RFC3339Nano), deliveryID); err != nil {
		t.Fatal(err)
	}
	admitTestTaskIntent(t, a, intentTestRun("outbound-newer"), "生成一个海边视频")
	reply := intentTestRun("outbound-reply")
	reply.ReplyToMessageID = "bot-photo-message"
	intent := admitTestTaskIntent(t, a, reply, "短一点")
	if intent.TaskID != initial.TaskID || intent.PreviousRunID != first.ID {
		t.Fatalf("outbound target=%#v", intent)
	}
}

func TestTaskIntentModelParseIsOnceAndPreservesLatestConstraint(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"choices": []map[string]any{{"message": map[string]string{"content": `{"constraints":["衣服短一点"],"forbidden":[],"clarification":""}`}}}})
	}))
	defer server.Close()
	if _, err := a.configStore.db.Exec("UPDATE model_endpoints SET enabled=0"); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, a.configStore.db, "task-parser", "task-parser", []string{"chat", "reasoning"}, "llm", "")
	bindTestModelConnection(t, a.configStore.db, "task-parser", server.URL)
	a.client = server.Client()
	first := intentTestRun("parse-first")
	first.PersonaID = "doubao"
	admitTestTaskIntent(t, a, first, "给我拍一张自拍")
	second := intentTestRun("parse-second")
	second.PersonaID = "doubao"
	admitTestTaskIntent(t, a, second, "短一点，这次不要紫色")
	for range 2 {
		prompt, reply, err := a.prepareTaskIntent(context.Background(), second, "短一点，这次不要紫色")
		if err != nil || reply != nil || !strings.Contains(prompt, "这次不要紫色") {
			t.Fatalf("prompt=%s reply=%#v err=%v", prompt, reply, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("parse calls=%d want1", calls.Load())
	}
	_, found, err := a.taskIntentForRun(context.Background(), "not-a-task")
	if err != nil || found {
		t.Fatalf("plain missing=%v %v", found, err)
	}
}

func TestTaskIntentContextCancellationIsScoped(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first, releaseFirst := a.taskRunContext(context.Background(), "cancel-one")
	defer releaseFirst()
	second, releaseSecond := a.taskRunContext(context.Background(), "cancel-two")
	defer releaseSecond()
	a.cancelTaskRunContext("cancel-one")
	if first.Err() != context.Canceled || second.Err() != nil {
		t.Fatalf("first=%v second=%v", first.Err(), second.Err())
	}
	release, ok := a.claimTaskOperation("operation")
	if !ok {
		t.Fatal("first claim failed")
	}
	if _, ok = a.claimTaskOperation("operation"); ok {
		t.Fatal("duplicate operation admitted")
	}
	release()
	release, ok = a.claimTaskOperation("operation")
	if !ok {
		t.Fatal("released operation retained")
	}
	release()
}

func TestTaskIntentContinuePendingVideoNeverCreatesNewRevision(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("continue-video-first")
	initial := admitTestTaskIntent(t, a, first, "生成一段自拍视频")
	if _, err := a.db.Exec("UPDATE agent_runs SET state='running' WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	followup := intentTestRun("continue-video-followup")
	intent := admitTestTaskIntent(t, a, followup, "继续")
	if intent.Revision != initial.Revision || intent.TaskID != initial.TaskID {
		t.Fatalf("continue changed revision=%#v", intent)
	}
	if err := a.ensureMediaTaskCurrent(context.Background(), first); err != nil {
		t.Fatalf("accepted video cancelled: %v", err)
	}
	_, reply, err := a.prepareTaskIntent(context.Background(), followup, "继续")
	if err != nil || reply == nil || !strings.Contains(reply.Text, "没有重新生成") {
		t.Fatalf("continue reply=%#v err=%v", reply, err)
	}
	if id := persistentOperationID(first.ID, "grok_generate_image", 9, []byte(`{"prompt":"paraphrased"}`)); id != persistentOperationID(first.ID, "generate_image", 0, []byte(`{"prompt":"original"}`)) {
		t.Fatal("provider paraphrase or alias changed costly operation id")
	}
	if _, err = a.db.Exec("UPDATE agent_runs SET state='delivered' WHERE id=?", first.ID); err != nil {
		t.Fatal(err)
	}
	_, reply, err = a.prepareTaskIntent(context.Background(), followup, "继续")
	if err != nil || reply == nil {
		t.Fatalf("completed video replayed: %#v %v", reply, err)
	}
	textRun := intentTestRun("continue-text-first")
	admitTestTaskIntent(t, a, textRun, "详细分析这个方案")
	if _, err = a.db.Exec("UPDATE agent_runs SET state='delivered' WHERE id=?", textRun.ID); err != nil {
		t.Fatal(err)
	}
	textFollowup := intentTestRun("continue-text-followup")
	textIntent := admitTestTaskIntent(t, a, textFollowup, "继续")
	if textIntent.Revision != 1 {
		t.Fatalf("text continue newrevision=%#v", textIntent)
	}
	prompt, reply, err := a.prepareTaskIntent(context.Background(), textFollowup, "继续")
	if err != nil || reply != nil || !strings.Contains(prompt, "详细分析这个方案") {
		t.Fatalf("text continuation=%q %#v %v", prompt, reply, err)
	}
}

func TestTaskIntentExplicitFeedbackDoesNotRegenerateOrInferAcceptance(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("feedback-first")
	initial := admitTestTaskIntent(t, a, first, "给我拍一张自拍")
	for index, text := range []string{"这次对了", "不满意"} {
		run := intentTestRun("feedback-" + itoa(index))
		intent := admitTestTaskIntent(t, a, run, text)
		if intent.Revision != initial.Revision || intent.PreviousRunID != first.ID {
			t.Fatalf("feedback changed task=%#v", intent)
		}
		if err := a.ensureMediaTaskCurrent(context.Background(), first); err != nil {
			t.Fatalf("feedback cancelled task: %v", err)
		}
		_, reply, err := a.prepareTaskIntent(context.Background(), run, text)
		if err != nil || reply == nil {
			t.Fatalf("feedback should not enter provider path: %#v %v", reply, err)
		}
	}
	for _, text := range []string{"好", "好的", "嗯", "收到"} {
		if action := taskIntentAction(text); action == "accepted" {
			t.Fatalf("ambiguous text accepted: %s", text)
		}
	}
}

func TestTaskIntentRetentionRedactsTerminalTextWithoutReplaying(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	cutoff := now.Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	for _, state := range []string{"delivered", "failed", "cancelled", "running", "queued", "responding"} {
		run := intentTestRun("retention-" + state)
		admitTestTaskIntent(t, a, run, "生成一张红色衣服的自拍")
		if _, err := a.db.Exec("UPDATE agent_runs SET state=?,updated_at=? WHERE id=?", state, old, run.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec("UPDATE agent_task_steps SET updated_at=? WHERE id=?", old, run.ID+":intent"); err != nil {
			t.Fatal(err)
		}
	}
	fresh := intentTestRun("retention-fresh")
	admitTestTaskIntent(t, a, fresh, "生成一张红色衣服的自拍")
	if _, err := a.db.Exec("UPDATE agent_runs SET state='delivered' WHERE id=?", fresh.ID); err != nil {
		t.Fatal(err)
	}
	unknownReceipt, err := a.encrypt([]byte(`{"phase":"creating","task":{"id":""}}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec(`INSERT INTO agent_task_steps
		(id,run_id,step_index,kind,name,status,output_cipher,created_at,updated_at)
		VALUES ('retention-unknown','retention-failed',1,'tool','media_generation:video','running',?,?,?)`, unknownReceipt, old, old); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = a.pruneTaskIntentMetadata(ctx, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	for _, state := range []string{"delivered", "failed", "cancelled", "running", "queued", "responding", "fresh"} {
		run := intentTestRun("retention-" + state)
		intent, found, readErr := a.taskIntentForRun(ctx, run.ID)
		if readErr != nil || !found {
			t.Fatalf("%s retained tombstone: found=%v err=%v", state, found, readErr)
		}
		terminal := state == "delivered" || state == "failed" || state == "cancelled"
		if intent.Retired != terminal {
			t.Fatalf("%s retired=%v", state, intent.Retired)
		}
		if !terminal {
			if intent.UserRequest == "" {
				t.Fatalf("%s live/recent text removed", state)
			}
			continue
		}
		if intent.TaskID != run.ID || intent.Revision != 1 || intent.UserRequest != "" || intent.Goal != "" || len(intent.Constraints) != 0 || intent.SourceMessageID != "" {
			t.Fatalf("%s incomplete text retirement: %#v", state, intent)
		}
		if err = a.ensureMediaTaskCurrent(ctx, run); err == nil {
			t.Fatalf("%s retired task can execute", state)
		}
		var input []byte
		if err = a.db.QueryRow("SELECT input_cipher FROM agent_runs WHERE id=?", run.ID).Scan(&input); err != nil || len(input) != 0 {
			t.Fatalf("%s input retained: len=%d err=%v", state, len(input), err)
		}
		if err = a.retryTask(ctx, run.ID); err == nil {
			t.Fatalf("%s retired task can retry", state)
		}
	}
	var retainedReceipt []byte
	if err = a.db.QueryRow("SELECT output_cipher FROM agent_task_steps WHERE id='retention-unknown'").Scan(&retainedReceipt); err != nil || string(retainedReceipt) != string(unknownReceipt) {
		t.Fatalf("unknown receipt changed: %v", err)
	}
}

func TestTaskIntentSupersededFinishKeepsCancelledState(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	first := intentTestRun("superseded-finish-first")
	admitTestTaskIntent(t, a, first, "生成一张自拍")
	admitTestTaskIntent(t, a, intentTestRun("superseded-finish-correction"), "衣服短一点")
	if err := a.finishRunWithoutDelivery(first, "task_revision_superseded"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := a.db.QueryRow("SELECT state FROM agent_runs WHERE id=?", first.ID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("superseded task state=%q err=%v", state, err)
	}
}
