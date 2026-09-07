package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func dialogueQuestionReplay() []RecalledGroupEvent {
	return []RecalledGroupEvent{
		{ID: "setup-a", Role: "user", SenderRef: "a", PersonaID: "doubao", MessageID: "setup-a", ThreadKey: "thread-a", UntrustedText: "你来提问，我只能回答是或者不是"},
		{ID: "question-a", Role: "assistant", PersonaID: "doubao", MessageID: "bot-question-a", ThreadKey: "thread-a", ReplyToMessageID: "setup-a", ReplyToSenderRef: "a", UntrustedText: "是真人吗？"},
		{ID: "aside-b", Role: "user", SenderRef: "b", PersonaID: "doubao", ThreadKey: "thread-a", UntrustedText: "是的"},
		{ID: "question-b", Role: "assistant", PersonaID: "doubao", MessageID: "bot-question-b", ThreadKey: "thread-b", ReplyToSenderRef: "b", UntrustedText: "今晚吃什么？"},
		{ID: "answer-a", Role: "user", SenderRef: "a", PersonaID: "doubao", ThreadKey: "thread-a", UntrustedText: "是真人"},
	}
}

func TestDialogueQuestionKeepsTargetAcrossOtherMemberInterjection(t *testing.T) {
	events := dialogueQuestionReplay()
	state := inferDialogueReasoningState(events, "answer-a", "是真人")
	if state.Action != "answer_previous_question" || state.PendingQuestion != "是真人吗？" {
		t.Fatalf("answer attached to another member's question: %#v", state)
	}
	if hint := inferDialogueProtocolHint(events, "answer-a", "是真人"); !strings.Contains(hint, "真人吗") || strings.Contains(hint, "今晚吃什么") {
		t.Fatalf("wrong guessing question: %s", hint)
	}
}

func TestDialogueQuestionOtherMemberCannotAnswerOrGainContinuation(t *testing.T) {
	events := dialogueQuestionReplay()[:3]
	state := inferDialogueReasoningState(events, "aside-b", "是的")
	if state.PendingQuestion != "" || state.Action == "answer_previous_question" || state.SameSpeakerFollowup {
		t.Fatalf("another member inherited the pending question: %#v", state)
	}
	if clearlyContinuesRecentAssistant(events, "aside-b", "是的") || inferDialogueProtocolHint(events, "aside-b", "是的") != "" {
		t.Fatal("another member gained direct continuation ownership")
	}
}

func TestDialogueQuestionAnswerConsumedOnlyOnce(t *testing.T) {
	events := dialogueQuestionReplay()
	events = append(events, RecalledGroupEvent{ID: "after-answer", Role: "user", SenderRef: "a", PersonaID: "doubao", ThreadKey: "thread-a", UntrustedText: "是的"})
	state := inferDialogueReasoningState(events, "after-answer", "是的")
	if state.PendingQuestion != "" || state.Action == "answer_previous_question" {
		t.Fatalf("answered question became pending again: %#v", state)
	}
	if clearlyContinuesRecentAssistant(events, "after-answer", "是的") {
		t.Fatal("duplicate answer was promoted to a new direct continuation")
	}
	if hint := dialogueReasoningHint(events, "after-answer", "是的"); !strings.Contains(hint, "已回答") {
		t.Fatalf("answered-state hint missing: %s", hint)
	}
}

func TestDialogueQuestionExplicitReplyOverridesRecentQuestion(t *testing.T) {
	events := dialogueQuestionReplay()[:2]
	events = append(events,
		RecalledGroupEvent{ID: "new-question", Role: "assistant", PersonaID: "doubao", MessageID: "new-question", ReplyToSenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是男生吗？"},
		RecalledGroupEvent{ID: "answer", Role: "user", SenderRef: "a", PersonaID: "doubao", ThreadKey: "thread-a", ReplyToMessageID: "bot-question-a", UntrustedText: "是的"})
	state := inferDialogueReasoningState(events, "answer", "是的")
	if state.PendingQuestion != "是真人吗？" {
		t.Fatalf("explicit reply lost precedence: %#v", state)
	}
	for _, reply := range []string{"unknown-message", "setup-a"} {
		events[len(events)-1].ReplyToMessageID = reply
		if state = inferDialogueReasoningState(events, "answer", "是的"); state.PendingQuestion != "" {
			t.Fatalf("reply to %q fell back to an unrelated question: %#v", reply, state)
		}
	}
}

func TestDialogueQuestionExplicitReplyDoesNotOverrideTargetOrPersona(t *testing.T) {
	events := dialogueQuestionReplay()[:3]
	events[2].ReplyToMessageID = "bot-question-a"
	if state := inferDialogueReasoningState(events, "aside-b", "是的"); state.PendingQuestion != "" {
		t.Fatalf("explicit reply consumed someone else's answer: %#v", state)
	}
	events[2].SenderRef = "a"
	events[2].PersonaID = "xiaoman"
	if state := inferDialogueReasoningState(events, "aside-b", "是的"); state.PendingQuestion != "" {
		t.Fatalf("question crossed persona boundary: %#v", state)
	}
}

func TestDialogueQuestionLegacyMetadataIsConservative(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "ask", Role: "user", SenderRef: "a", UntrustedText: "猜猜我想的人"},
		{ID: "question", Role: "assistant", UntrustedText: "是真人吗？"},
		{ID: "b", Role: "user", SenderRef: "b", UntrustedText: "是的"},
		{ID: "a", Role: "user", SenderRef: "a", UntrustedText: "不是"},
	}
	if state := inferDialogueReasoningState(events, "b", "是的"); state.PendingQuestion != "" {
		t.Fatalf("legacy question inherited by another member: %#v", state)
	}
	if state := inferDialogueReasoningState(events, "a", "不是"); state.PendingQuestion == "" {
		t.Fatalf("legacy original member's answer was lost: %#v", state)
	}
}

func TestDialogueQuestionResolvesTargetFromOriginalMessage(t *testing.T) {
	events := dialogueQuestionReplay()
	events[1].ReplyToSenderRef = ""
	if state := inferDialogueReasoningState(events, "answer-a", "是真人"); state.PendingQuestion != "是真人吗？" {
		t.Fatalf("source message target was not recovered: %#v", state)
	}
	events[1].ReplyToMessageID = "unknown-source"
	if state := inferDialogueReasoningState(events, "answer-a", "是真人"); state.PendingQuestion != "" {
		t.Fatalf("unknown explicit source fell back to a guessed target: %#v", state)
	}
}

func TestDialogueQuestionNewerAnswerDoesNotConsumeOlderExplicitQuestion(t *testing.T) {
	events := dialogueQuestionReplay()[:2]
	events = append(events,
		RecalledGroupEvent{ID: "new-question", Role: "assistant", PersonaID: "doubao", MessageID: "new-question", ReplyToSenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是男生吗？"},
		RecalledGroupEvent{ID: "new-answer", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是的"},
		RecalledGroupEvent{ID: "old-answer", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", ReplyToMessageID: "bot-question-a", UntrustedText: "不是真人"})
	if state := inferDialogueReasoningState(events, "old-answer", "不是真人"); state.PendingQuestion != "是真人吗？" {
		t.Fatalf("newer answer consumed an older, explicitly quoted question: %#v", state)
	}
}

func TestDialogueQuestionReactionDoesNotConsumePendingAnswer(t *testing.T) {
	events := dialogueQuestionReplay()[:2]
	events = append(events,
		RecalledGroupEvent{ID: "reaction", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "哈哈"},
		RecalledGroupEvent{ID: "answer", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是的"})
	if state := inferDialogueReasoningState(events, "reaction", "哈哈"); state.Action == "answer_previous_question" {
		t.Fatalf("reaction was taken as an answer: %#v", state)
	}
	if state := inferDialogueReasoningState(events, "answer", "是的"); state.PendingQuestion != "是真人吗？" {
		t.Fatalf("reaction consumed the pending answer: %#v", state)
	}
}

func TestDialogueQuestionNumericAnswersAreNotReactions(t *testing.T) {
	for _, answer := range []string{"6点", "26岁", "确实"} {
		events := dialogueQuestionReplay()[:2]
		events = append(events,
			RecalledGroupEvent{ID: "answer", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: answer},
			RecalledGroupEvent{ID: "later", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是的"})
		if state := inferDialogueReasoningState(events, "answer", answer); state.Action != "answer_previous_question" {
			t.Fatalf("numeric answer %q was misclassified: %#v", answer, state)
		}
		if state := inferDialogueReasoningState(events, "later", "是的"); state.PendingQuestion != "" || state.AnsweredQuestion == "" {
			t.Fatalf("numeric answer %q was not consumed: %#v", answer, state)
		}
	}
}

func TestDialogueQuestionGuessingGameContextDoesNotCrossMember(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "game-b", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "b", UntrustedText: "你来提问，我只能回答是或者不是"},
		{ID: "question-a", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "a", UntrustedText: "你今天还去吗？"},
		{ID: "answer-a", Role: "user", PersonaID: "doubao", SenderRef: "a", UntrustedText: "是的"},
	}
	if hint := inferDialogueProtocolHint(events, "answer-a", "是的"); hint != "" {
		t.Fatalf("B's game hijacked A's ordinary question: %s", hint)
	}
}

func TestDialogueQuestionCorrectionUpdatesAnswerWithoutReopeningQuestion(t *testing.T) {
	events := dialogueQuestionReplay()
	events = append(events,
		RecalledGroupEvent{ID: "correction", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "刚才说错了，不是真人"},
		RecalledGroupEvent{ID: "followup", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "继续"})
	state := resolveDialogueThread(events, "followup")
	if state.PendingQuestion.ID != "" || state.AnsweredQuestion.ID != "question-a" || state.PreviousAnswer.ID != "correction" {
		t.Fatalf("explicit answer correction was lost or reopened the question: %#v", state)
	}
	events[len(events)-2].UntrustedText = "今晚挺热闹"
	if state = resolveDialogueThread(events, "followup"); state.PreviousAnswer.ID != "answer-a" {
		t.Fatalf("arbitrary followup replaced the recorded answer: %#v", state)
	}
	events[len(events)-2].ReplyToMessageID = "bot-question-a"
	events[len(events)-2].UntrustedText = "不是"
	if state = resolveDialogueThread(events, "followup"); state.PreviousAnswer.ID != "correction" {
		t.Fatalf("explicitly quoted answer update was lost: %#v", state)
	}
}

func TestDialogueQuestionCorrectionOfDifferentAssistantReplyDoesNotRewriteAnswer(t *testing.T) {
	events := dialogueQuestionReplay()
	events = append(events,
		RecalledGroupEvent{ID: "other-reply", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "a", ThreadKey: "thread-a", UntrustedText: "今天的雷达已经恢复了。"},
		RecalledGroupEvent{ID: "other-correction", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "不对，还是没用"},
		RecalledGroupEvent{ID: "followup", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "继续"})
	if state := resolveDialogueThread(events, "followup"); state.PreviousAnswer.ID != "answer-a" {
		t.Fatalf("correction to another assistant reply rewrote the old answer: %#v", state)
	}
}

func TestDialogueQuestionSenderOnlyReplyToAnotherMemberCannotConsume(t *testing.T) {
	events := dialogueQuestionReplay()[:2]
	events[1].SenderRef = "agent"
	events = append(events,
		RecalledGroupEvent{ID: "reply-b", Role: "user", PersonaID: "doubao", SenderRef: "a", ReplyToSenderRef: "b", ThreadKey: "thread-a", UntrustedText: "好"},
		RecalledGroupEvent{ID: "answer-a", Role: "user", PersonaID: "doubao", SenderRef: "a", ThreadKey: "thread-a", UntrustedText: "是的"})
	if state := inferDialogueReasoningState(events, "reply-b", "好"); state.PendingQuestion != "" || clearlyContinuesRecentAssistant(events, "reply-b", "好") {
		t.Fatalf("sender-only reply to B inherited the bot question: %#v", state)
	}
	if state := inferDialogueReasoningState(events, "answer-a", "是的"); state.PendingQuestion != "是真人吗？" {
		t.Fatalf("sender-only reply to B consumed A's pending question: %#v", state)
	}
}

func TestDialogueQuestionDeliveryACKPersistsReplyIdentity(t *testing.T) {
	runtime := newTaskIntentTestRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	run := intentTestRun("dialogue-ack")
	admitTestTaskIntent(t, runtime, run, "今晚聊聊")
	if err := runtime.enqueueAgentReply(run, agentReply{Text: "你今晚几点回来？"}, ""); err != nil {
		t.Fatal(err)
	}
	var deliveryID string
	if err := runtime.db.QueryRow(`SELECT id FROM agent_deliveries WHERE run_id=?`, run.ID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.recordTaskOutboundMessage(ctx, deliveryID, "text", "bot-question-message"); err != nil {
		t.Fatal(err)
	}
	leased, err := runtime.leaseTransportDeliveries(ctx, "dialogue-ack-test", 1, 30)
	if err != nil || len(leased) != 1 {
		t.Fatalf("lease = %#v err=%v", leased, err)
	}
	if err := runtime.ackTransportDelivery(ctx, deliveryID, deliveryLeaseReceipt{LeaseOwner: leased[0].LeaseOwner, Attempts: leased[0].Attempts}); err != nil {
		t.Fatal(err)
	}
	scope := runtimeScopeFromRun(run)
	events, err := runtime.memory.RecentPersonaGroupEvents(ctx, scope.memoryConversationRef(), run.PersonaID, 12)
	if err != nil || len(events) != 1 {
		t.Fatalf("ACK events = %#v err=%v", events, err)
	}
	question := events[0]
	if question.MessageID != "bot-question-message" || question.ThreadKey != run.ThreadKey ||
		question.ReplyToSenderRef != scope.memorySenderRef() || question.ReplyToMessageID != run.MessageID {
		t.Fatalf("delivery ACK lost scoped reply identity: %#v", question)
	}
	if _, _, err := runtime.memory.ObserveGroupEvent(ctx, GroupEventInput{
		ID: "ack-answer", Conversation: scope.memoryConversationRef(), Sender: scope.memorySenderRef(), PersonaID: run.PersonaID,
		Role: "user", Text: "6点", ThreadKey: run.ThreadKey, ReplyTo: &transportReplyReference{MessageID: "bot-question-message"},
		OccurredAt: time.Now().UTC().Add(time.Second),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	events, err = runtime.memory.RecentPersonaGroupEvents(ctx, scope.memoryConversationRef(), run.PersonaID, 12)
	if err != nil {
		t.Fatal(err)
	}
	if state := inferDialogueReasoningState(events, "ack-answer", "6点"); state.Action != "answer_previous_question" || state.PendingQuestion != question.UntrustedText {
		t.Fatalf("persisted outbound reply identity did not resolve the answer: %#v", state)
	}
}

func TestGroupQuestionContinuationSurvivesReplyToAnotherMember(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	setTestIntegration(t, runtime.configStore.db, "channel_runtime", map[string]any{
		"mode": "active", "captureUnaddressedGroups": true,
	})
	setTestIntegration(t, runtime.configStore.db, "group_chat_policy", map[string]any{
		"enabled": true, "proactiveChatEnabled": true,
		"initialProbability": 0.0, "afterReplyProbability": 0.0,
		"probabilityDurationSeconds": 180, "decisionProviderId": "missing-decision-model",
		"messageQualityEnabled": true, "replyDensityEnabled": false,
	})
	now := time.Now().UTC()
	stamp := now.Format(time.RFC3339Nano)
	if err := runtime.memory.MarkBotReplyAck(ctx, "group-one", now); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id,event_id,reply_handle,conversation_ref,sender_ref,persona_id,state,created_at,updated_at)
		VALUES ('latest-b','latest-b','latest-b','group-one','sender-two','doubao','delivered',?,?);
		INSERT INTO agent_deliveries (id,run_id,reply_handle,payload_json,phase,status,created_at,updated_at)
		VALUES ('latest-b-delivery','latest-b','latest-b','{}','terminal','delivered',?,?)`, stamp, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	for index, input := range []GroupEventInput{
		{ID: "setup-a", Sender: "sender-one", Role: "user", Text: "你来提问，我只能回答是或者不是", MessageID: "setup-a"},
		{ID: "question-a", Sender: "agent", Role: "assistant", Text: "是真人吗？", MessageID: "question-a", ReplyTo: &transportReplyReference{MessageID: "setup-a", SenderKey: "sender-one"}},
		{ID: "aside-b", Sender: "sender-two", Role: "user", Text: "我回来了"},
		{ID: "question-b", Sender: "agent", Role: "assistant", Text: "今晚吃什么？", MessageID: "question-b", ReplyTo: &transportReplyReference{SenderKey: "sender-two"}},
	} {
		input.Conversation, input.PersonaID = "group-one", "doubao"
		input.OccurredAt = now.Add(time.Duration(index-5) * time.Second)
		if _, _, err := runtime.memory.ObserveGroupEvent(ctx, input, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	response := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("answer-a", "是真人", false), "answer-a")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"owned"`) {
		t.Fatalf("A's pending answer lost ownership after reply to B: %d %s", response.Code, response.Body.String())
	}
	var reason string
	if err := runtime.db.QueryRow(`SELECT ownership_reason FROM agent_runs WHERE event_id='answer-a'`).Scan(&reason); err != nil || reason != "direct_continuation" {
		t.Fatalf("A's answer was not owned through the existing continuation gate: reason=%q err=%v", reason, err)
	}
	response = runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("answer-a-again", "是的", false), "answer-a-again")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"observe"`) {
		t.Fatalf("persisted answer was consumed again: %d %s", response.Code, response.Body.String())
	}
	other := testTransportEvent("answer-c", "是的", false)
	other["sender"].(map[string]string)["key"] = "sender-three"
	response = runtimeRequest(t, runtime, "/api/v1/transport/events", other, "answer-c")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"observe"`) {
		t.Fatalf("non-target inherited question ownership: %d %s", response.Code, response.Body.String())
	}
	otherGroup := testTransportEvent("answer-other-group", "是真人", false)
	otherGroup["conversation"].(map[string]string)["key"] = "group-two"
	response = runtimeRequest(t, runtime, "/api/v1/transport/events", otherGroup, "answer-other-group")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"observe"`) {
		t.Fatalf("question ownership crossed conversations: %d %s", response.Code, response.Body.String())
	}
	oldThread := transportEvent{Transport: "qq_official", TransportInstance: "instance-one"}
	oldThread.Conversation.Key, oldThread.Sender.Key = "group-one", "sender-four"
	if _, _, err := runtime.memory.ObserveGroupEvent(ctx, GroupEventInput{
		ID: "old-question-d", Conversation: "group-one", Sender: "agent", PersonaID: "doubao", Role: "assistant",
		Text: "你还继续参加吗？", ReplyTo: &transportReplyReference{SenderKey: "sender-four"}, OccurredAt: now.Add(-time.Hour),
		ThreadKey: runtime.deriveTransportThreadKey(ctx, oldThread),
	}, 2*time.Hour); err != nil {
		t.Fatal(err)
	}
	stale := testTransportEvent("answer-old-d", "是的", false)
	stale["sender"].(map[string]string)["key"] = "sender-four"
	response = runtimeRequest(t, runtime, "/api/v1/transport/events", stale, "answer-old-d")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"disposition":"observe"`) {
		t.Fatalf("B's fresh ACK revived an expired question to D: %d %s", response.Code, response.Body.String())
	}
}
