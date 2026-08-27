package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDialogueReasoningConsumesPreviousQuestion(t *testing.T) {
	now := time.Now().UTC()
	events := []RecalledGroupEvent{
		{ID: "assistant-question", Role: "assistant", UntrustedText: "他是现实中的人物吗？", OccurredAt: now.Add(-time.Second)},
		{ID: "current", Role: "user", SenderRef: "member-one", UntrustedText: "是的", OccurredAt: now},
	}
	state := inferDialogueReasoningState(events, "current", "是的")
	if state.Action != "answer_previous_question" || state.PendingQuestion == "" {
		t.Fatalf("state = %#v", state)
	}
	hint := dialogueReasoningHint(events, "current", "是的")
	if !strings.Contains(hint, "先消费当前答案") || !strings.Contains(hint, "不要重复复述上一问") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestDialogueReasoningRecognizesCorrection(t *testing.T) {
	now := time.Now().UTC()
	events := []RecalledGroupEvent{
		{ID: "assistant", Role: "assistant", UntrustedText: "那我去搜索这个人。", OccurredAt: now.Add(-time.Second)},
		{ID: "current", Role: "user", SenderRef: "member-one", UntrustedText: "不对，我说的是看图继续猜", OccurredAt: now},
	}
	state := inferDialogueReasoningState(events, "current", "不对，我说的是看图继续猜")
	if state.Action != "correct_previous_reply" {
		t.Fatalf("action = %q", state.Action)
	}
}

func TestDialogueReasoningCollapsesRepeatedDirectPings(t *testing.T) {
	now := time.Now().UTC()
	events := []RecalledGroupEvent{
		{ID: "ping-one", Role: "user", SenderRef: "member-one", UntrustedText: "@豆包 包？", OccurredAt: now.Add(-8 * time.Second)},
		{ID: "ping-two", Role: "user", SenderRef: "member-one", UntrustedText: "@豆包 包？", OccurredAt: now.Add(-4 * time.Second)},
		{ID: "current", Role: "user", SenderRef: "member-one", UntrustedText: "@豆包 怎么不理我", OccurredAt: now},
	}
	state := inferDialogueReasoningState(events, "current", "@豆包 怎么不理我")
	if state.Action != "direct_ping" || state.RepeatedBurst != 3 {
		t.Fatalf("state = %#v", state)
	}
	if hint := dialogueReasoningHint(events, "current", "@豆包 怎么不理我"); !strings.Contains(hint, "视为同一轮催促") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestCoalesceQueuedWakeRunsCancelsOnlyStalePings(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	insert := func(id, message string) {
		ciphertext, err := runtime.encrypt([]byte(message))
		if err != nil {
			t.Fatal(err)
		}
		_, err = runtime.db.Exec(`INSERT INTO agent_runs (
			id, event_id, message_id, reply_to_message_id, thread_key, transport, reply_handle,
			conversation_ref, conversation_kind, sender_ref, persona_id, input_cipher,
			attachments_cipher, is_admin, is_wake, is_mention_bot, ownership_reason, state,
			created_at, updated_at
		) VALUES (?, ?, '', '', '', 'qq_official', 'reply-one', 'group-one', 'group',
			'member-one', 'doubao', ?, NULL, 0, 1, 1, 'wake_required', 'queued', ?, ?)`,
			id, id, ciphertext, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("old-ping", "@豆包 包？")
	insert("real-task", "@豆包 帮我分析这张图")
	event := transportEvent{}
	event.Conversation.Key = "group-one"
	event.Conversation.Kind = "group"
	event.Sender.Key = "member-one"
	event.Flags.IsWake = true
	// Default policy is concurrentMode=smart + smartMaxBatchSize=3: the stale
	// ping is dropped and the substantive sibling folds into the new run.
	merged, count, err := runtime.coalesceQueuedWakeRuns(context.Background(), event, "doubao", "@豆包 怎么不理我")
	if err != nil || count != 2 {
		t.Fatalf("coalesced = %d err=%v", count, err)
	}
	if len(merged) != 1 || merged[0] != "@豆包 帮我分析这张图" {
		t.Fatalf("merged burst = %#v", merged)
	}
	var pingState, taskState string
	if err = runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = 'old-ping'").Scan(&pingState); err != nil {
		t.Fatal(err)
	}
	if err = runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = 'real-task'").Scan(&taskState); err != nil {
		t.Fatal(err)
	}
	if pingState != "cancelled" || taskState != "cancelled" {
		t.Fatalf("states = ping:%s task:%s", pingState, taskState)
	}
}

func TestCoalesceLeavesSubstantiveSiblingWhenSmartMergeOff(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "concurrentMode": "off", "smartMaxBatchSize": 3,
	})
	now := time.Now().UTC().Format(time.RFC3339Nano)
	ciphertext, err := runtime.encrypt([]byte("@豆包 帮我分析这张图"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec(`INSERT INTO agent_runs (
		id, event_id, message_id, reply_to_message_id, thread_key, transport, reply_handle,
		conversation_ref, conversation_kind, sender_ref, persona_id, input_cipher,
		attachments_cipher, is_admin, is_wake, is_mention_bot, ownership_reason, state,
		created_at, updated_at
	) VALUES ('off-task', 'off-task', '', '', '', 'qq_official', 'reply-one', 'group-one', 'group',
		'member-one', 'doubao', ?, NULL, 0, 1, 1, 'wake_required', 'queued', ?, ?)`,
		ciphertext, now, now); err != nil {
		t.Fatal(err)
	}
	event := transportEvent{}
	event.Conversation.Key = "group-one"
	event.Conversation.Kind = "group"
	event.Sender.Key = "member-one"
	event.Flags.IsWake = true
	merged, count, err := runtime.coalesceQueuedWakeRuns(context.Background(), event, "doubao", "@豆包 再帮我看看这个")
	if err != nil || count != 0 || len(merged) != 0 {
		t.Fatalf("off-mode coalesce = %d merged=%#v err=%v", count, merged, err)
	}
	var state string
	if err = runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = 'off-task'").Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "queued" {
		t.Fatalf("off-mode task state = %s", state)
	}
}

func TestTransportThreadKeyIsStableAndInheritsReplies(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	event := transportEvent{Transport: "qq_official", TransportInstance: "qq-one"}
	event.Conversation.Key = "group-one"
	event.Conversation.Kind = "group"
	event.Sender.Key = "member-one"
	threadKey := runtime.deriveTransportThreadKey(context.Background(), event)
	if threadKey == "" || threadKey != runtime.deriveTransportThreadKey(context.Background(), event) {
		t.Fatalf("unstable thread key: %q", threadKey)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_transport_events (
		event_id, idempotency_key, transport, transport_instance, conversation_ref,
		conversation_kind, sender_ref, message_id, thread_key, created_at
	) VALUES ('root-event', 'root-event', 'qq_official', 'qq-one', 'group-one',
		'group', 'member-one', 'root-message', ?, ?)`, threadKey, now); err != nil {
		t.Fatal(err)
	}
	reply := event
	reply.Sender.Key = "member-two"
	reply.Message.ReplyTo = &transportReplyReference{MessageID: "root-message"}
	if got := runtime.deriveTransportThreadKey(context.Background(), reply); got != threadKey {
		t.Fatalf("reply thread = %q, want %q", got, threadKey)
	}
}

func TestNewerDialogueSupersedesOlderTextReply(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"smartMergeWaitSeconds": 2.0,
	})
	base := time.Now().UTC()
	insert := func(id, state string, at time.Time) {
		_, err := runtime.db.Exec(`INSERT INTO agent_runs (
			id, event_id, message_id, thread_key, transport, transport_instance,
			reply_handle, conversation_ref, conversation_kind, sender_ref,
			agent_instance_id, persona_id, state, ownership_reason, created_at, updated_at
		) VALUES (?, ?, ?, 'dialogue-one', 'qq_official', 'qq-one', ?, 'group-one',
			'group', 'member-one', 'doubao-qq', 'doubao', ?, 'wake_required', ?, ?)`,
			id, id+"-event", id+"-message", id+"-reply", state,
			at.Format(time.RFC3339Nano), at.Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("older", "running", base)
	insert("newer", "queued", base.Add(1500*time.Millisecond))
	superseded, err := runtime.supersededByNewerDialogue(context.Background(), runRecord{
		ID: "older", ConversationKind: "group", OwnershipReason: "wake_required",
	}, agentReply{Text: "旧回复"})
	if err != nil || !superseded {
		t.Fatalf("superseded = %v, err = %v", superseded, err)
	}
	if err = runtime.finishRunWithoutDelivery(runRecord{ID: "older"}, "superseded_by_newer_dialogue"); err != nil {
		t.Fatal(err)
	}
	var state, code string
	if err = runtime.db.QueryRow(`SELECT state, error_code FROM agent_runs WHERE id = 'older'`).Scan(&state, &code); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || code != "superseded_by_newer_dialogue" {
		t.Fatalf("older run = %s/%s", state, code)
	}
}

func TestProactiveOwnershipReasonsAreFailureSilent(t *testing.T) {
	for _, reason := range []string{"group_participation", "group_participation_local_fallback", "trigger_keyword_local_fallback"} {
		if !isProactiveOwnershipReason(reason) {
			t.Fatalf("reason %q should suppress failure delivery", reason)
		}
	}
	if isProactiveOwnershipReason("wake_required") {
		t.Fatal("direct wake failure must remain visible")
	}
}
