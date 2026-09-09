package main

import (
	"context"
	"testing"
	"time"
)

func TestRecentAssistantLifeContextIsScopedAndShortLived(t *testing.T) {
	now := time.Date(2026, 9, 9, 20, 0, 0, 0, time.UTC)
	events := []RecalledGroupEvent{
		{ID: "user-scene", Role: "user", SenderRef: "member-a", PersonaID: "doubao", ThreadKey: "thread-a", UntrustedText: "我在厨房", OccurredAt: now.Add(-2 * time.Minute)},
		{ID: "stale", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "我在书店翻书", OccurredAt: now.Add(-11 * time.Minute)},
		{ID: "wrong-persona", Role: "assistant", PersonaID: "other", ReplyToSenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "我在厨房做饭", OccurredAt: now.Add(-1 * time.Minute)},
		{ID: "wrong-member", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "member-b", ThreadKey: "thread-a", UntrustedText: "我在沙发边休息", OccurredAt: now.Add(-1 * time.Minute)},
		{ID: "wrong-thread", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "member-a", ThreadKey: "thread-b", UntrustedText: "我在阳台边", OccurredAt: now.Add(-1 * time.Minute)},
		{ID: "question", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "你在咖啡店吗？", OccurredAt: now.Add(-1 * time.Minute)},
		{ID: "current", Role: "assistant", PersonaID: "doubao", ReplyToSenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "我刚在厨房备餐，顺手拍了一张", OccurredAt: now.Add(-2 * time.Minute)},
	}
	context, ok := recentAssistantLifeContext(events, "doubao", "member-a", "thread-a", "current-request", now)
	if !ok || context.SourceID != "current" || context.Scene != "厨房操作台旁" {
		t.Fatalf("unexpected recent life context: %+v, ok=%v", context, ok)
	}
}

func TestRecentAssistantLifeContextStopsAtLaterUserTopic(t *testing.T) {
	now := time.Now().UTC()
	events := []RecalledGroupEvent{
		{ID: "scene", Role: "assistant", PersonaID: "doubao", SenderRef: "agent", ReplyToSenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "我在厨房备餐", OccurredAt: now.Add(-2 * time.Minute)},
		{ID: "topic", Role: "user", PersonaID: "doubao", SenderRef: "member-a", ThreadKey: "thread-a", UntrustedText: "换个话题，今天吃什么", OccurredAt: now.Add(-1 * time.Minute)},
	}
	if _, ok := recentAssistantLifeContext(events, "doubao", "member-a", "thread-a", "media-request", now); ok {
		t.Fatal("later user topic resurrected an older fictional scene")
	}
}

func TestDialogueLifeContextNeedsBridgeAndRespectsExplicitScene(t *testing.T) {
	context := dialogueLifeContext{Scene: "沙发边放松片刻", Activity: "坐下来歇一会儿", Action: "靠着坐稳", OccurredAt: time.Now().UTC()}
	plan := visualGenerationPlan{UserPrompt: "现在顺便来张自拍", Variables: map[string]string{"scene": "随机场景", "activity": "随机活动", "action": "随机动作"}}
	applyDialogueLifeContext(&plan, context)
	if plan.Variables["scene"] != context.Scene || plan.Variables["activity"] != context.Activity || plan.Variables["action"] != context.Action {
		t.Fatalf("recent conversational scene was not continued: %+v", plan.Variables)
	}
	if plan.Variables["dialogueLifeContext"] == "" {
		t.Fatal("continuity marker missing")
	}

	plain := visualGenerationPlan{UserPrompt: "来张自拍", Variables: map[string]string{"scene": "随机场景"}}
	applyDialogueLifeContext(&plain, context)
	if plain.Variables["scene"] != "随机场景" {
		t.Fatalf("generic repeated selfie became sticky: %+v", plain.Variables)
	}

	explicit := visualGenerationPlan{UserPrompt: "现在在海边来张自拍", Variables: map[string]string{"scene": "海边"}}
	applyDialogueLifeContext(&explicit, context)
	if explicit.Variables["scene"] != "海边" {
		t.Fatalf("explicit scene was overridden: %+v", explicit.Variables)
	}
}

func TestDialogueLifeClauseRejectsQuestionsAndNegatedScenes(t *testing.T) {
	for _, clause := range []string{"你在咖啡店吗", "别去厨房", "你觉得咖啡店怎么样"} {
		if dialogueLifeClauseAffirmative(clause) {
			t.Fatalf("non-affirmative scene accepted: %q", clause)
		}
	}
	for _, clause := range []string{"我在咖啡店等你", "正在厨房备餐", "在沙发边歇一会儿"} {
		if !dialogueLifeClauseAffirmative(clause) {
			t.Fatalf("affirmative scene rejected: %q", clause)
		}
	}
}

func TestPrepareVisualGenerationContinuesScopedAssistantLifeContext(t *testing.T) {
	runtime := newVisualStyleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "doubao")
	run := visualPlanTestRun(t, runtime, "life-context-prepare")
	run.PersonaID, run.ConversationRef, run.SenderRef, run.ThreadKey = "doubao", "conversation", "member-a", "thread-a"
	if _, err := runtime.db.Exec("UPDATE agent_runs SET persona_id=?,conversation_ref=?,sender_ref=?,thread_key=? WHERE id=?", run.PersonaID, run.ConversationRef, run.SenderRef, run.ThreadKey, run.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.memory.ObserveGroupEvent(context.Background(), GroupEventInput{
		ID: "life-context-assistant", Conversation: "conversation", Sender: "agent", PersonaID: "doubao",
		Role: "assistant", Text: "我在厨房操作台旁歇一会儿。", ThreadKey: run.ThreadKey,
		ReplyTo: &transportReplyReference{SenderKey: run.SenderRef}, OccurredAt: time.Now().UTC().Add(-2 * time.Minute),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	events, err := runtime.memory.RecentPersonaGroupEvents(context.Background(), "conversation", "doubao", 24)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := recentAssistantLifeContext(events, "doubao", "member-a", "thread-a", run.EventID, time.Now().UTC()); !ok {
		t.Fatalf("fixture assistant event was not eligible: %+v", events)
	}
	plan, err := runtime.prepareVisualGeneration(context.Background(), run, "现在顺便来张自拍", "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Variables["scene"] != "厨房操作台旁" || plan.Variables["dialogueLifeContext"] == "" {
		t.Fatalf("scoped assistant scene was not applied: %+v", plan.Variables)
	}
}
