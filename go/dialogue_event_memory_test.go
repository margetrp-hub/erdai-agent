package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestExtractDialogueEventRequiresOwnNearTermStatement(t *testing.T) {
	observed := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC) // 09:00 Asia/Shanghai
	event, ok := extractDialogueEvent("我明天要去面试", observed, 480)
	if !ok || event.Key != "面试" || event.Status != "active" {
		t.Fatalf("event = %#v, ok=%v", event, ok)
	}
	if got := event.DueAt.In(time.FixedZone("CST", 8*60*60)); got.Day() != 10 || got.Hour() != 9 {
		t.Fatalf("due at = %s, want Sep 10 09:00 CST", got)
	}
	for _, message := range []string{
		`他说“我明天要去面试”`,
		"我明天不去面试",
		"我的面试没有取消",
		"我明天面试还没结束",
		"我明天要去面试吗？",
		"明天朋友面试",
	} {
		if _, ok := extractDialogueEvent(message, observed, 480); ok {
			t.Fatalf("captured non-own/non-statement event: %q", message)
		}
	}
}

func TestDialogueEventCaptureRescheduleAndCancel(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := personaMemoryScope("doubao", "user", "alice")
	active, ok := extractDialogueEvent("我明天要去面试", now, 480)
	if !ok || store.CaptureDialogueEvent(context.Background(), scope, active, "event-1") != nil {
		t.Fatal("initial event capture failed")
	}
	rescheduled, ok := extractDialogueEvent("我把面试改到后天", now, 480)
	if !ok || store.CaptureDialogueEvent(context.Background(), scope, rescheduled, "event-2") != nil {
		t.Fatal("rescheduled event capture failed")
	}
	events, err := store.ActiveDialogueEvents(context.Background(), scope)
	if err != nil || len(events) != 1 || events[0].DueAt.Day() != 11 {
		t.Fatalf("rescheduled events = %#v, err=%v", events, err)
	}
	cancelled, ok := extractDialogueEvent("我面试取消了", now, 480)
	if !ok || cancelled.Status != "cancelled" || store.CaptureDialogueEvent(context.Background(), scope, cancelled, "event-3") != nil {
		t.Fatal("cancellation capture failed")
	}
	events, err = store.ActiveDialogueEvents(context.Background(), scope)
	if err != nil || len(events) != 0 {
		t.Fatalf("cancelled event still active: %#v, err=%v", events, err)
	}
	completed, ok := extractDialogueEvent("我的面试已经结束了", now, 480)
	if !ok || completed.Status != "completed" || !completed.DueAt.IsZero() {
		t.Fatalf("completion event = %#v, ok=%v", completed, ok)
	}
}

func TestDialogueEventExpiresWithoutOutboundAction(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := personaMemoryScope("doubao", "user", "alice")
	event, ok := extractDialogueEvent("我今晚要开会", now, 480)
	if !ok || store.CaptureDialogueEvent(context.Background(), scope, event, "event-1") != nil {
		t.Fatal("event capture failed")
	}
	store.now = func() time.Time { return now.Add(72 * time.Hour) }
	if events, err := store.ActiveDialogueEvents(context.Background(), scope); err != nil || len(events) != 0 {
		t.Fatalf("expired event recalled: %#v, err=%v", events, err)
	}
}

func TestDialogueEventPromptIsConsumedOnce(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	scope := personaMemoryScope("doubao", "event", "instance-user|conversation:group")
	event, ok := extractDialogueEvent("我明天要面试", now, 480)
	if !ok || store.CaptureDialogueEvent(context.Background(), scope, event, "event-1") != nil {
		t.Fatal("event capture failed")
	}
	if sameTurn, err := store.DialogueEventsForPrompt(context.Background(), scope, "event-1", "我回来了"); err != nil || len(sameTurn) != 0 {
		t.Fatalf("same-turn event prompt = %#v, err=%v", sameTurn, err)
	}
	store.now = func() time.Time { return now.Add(24 * time.Hour) }
	first, err := store.DialogueEventsForPrompt(context.Background(), scope, "different-event", "我回来了")
	if err != nil || len(first) != 1 {
		t.Fatalf("first prompt events = %#v, err=%v", first, err)
	}
	second, err := store.DialogueEventsForPrompt(context.Background(), scope, "different-event", "我回来了")
	if err != nil || len(second) != 0 {
		t.Fatalf("repeated prompt events = %#v, err=%v", second, err)
	}
}

func TestCaptureStableMemoryFeedsDueEventIntoPersonaContext(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	runtime.memory.now = func() time.Time { return now }
	run := memoryLedgerTestRun("event-context", "qq-event")
	run.EventID = "event-context-1"
	run.CreatedAt = now.Format(time.RFC3339Nano)
	run.PersonaID = "doubao"
	run.SenderRef = "alice"
	run.ConversationRef = "group-event"
	run.MemoryNamespace = "doubao-qq"
	runtime.captureStableMemory(context.Background(), run, "我明天要面试")
	if context := runtime.personaContext(context.Background(), run, "我明天要面试"); len(context.RecentMessages) != 0 {
		t.Fatalf("same-turn event leaked into context: %#v", context.RecentMessages)
	}
	run.EventID = "event-context-2"
	now = now.Add(24 * time.Hour)
	runtime.memory.now = func() time.Time { return now }
	run.CreatedAt = now.Format(time.RFC3339Nano)
	context := runtime.personaContext(context.Background(), run, "我回来了")
	if len(context.RecentMessages) == 0 || !strings.Contains(context.RecentMessages[len(context.RecentMessages)-1], "面试") {
		t.Fatalf("due event missing from persona context: %#v", context.RecentMessages)
	}
}

func TestForgetMemoryClearsOnlyScopedDialogueEvent(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Date(2026, 9, 9, 1, 0, 0, 0, time.UTC)
	runtime.memory.now = func() time.Time { return now }
	run := memoryLedgerTestRun("event-forget", "qq-event")
	run.PersonaID, run.SenderRef, run.ConversationRef, run.MemoryNamespace = "doubao", "alice", "group-event", "doubao-qq"
	event, ok := extractDialogueEvent("我明天要面试", now, 480)
	if !ok || runtime.memory.CaptureDialogueEvent(context.Background(), dialogueEventScope(run), event, "event-forget-1") != nil {
		t.Fatal("event capture failed")
	}
	result, err := runtime.forgetMemory(context.Background(), run, "面试")
	if err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Deleted int `json:"deleted"`
	}
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil || payload.Deleted != 1 {
		t.Fatalf("forget result = %s, err=%v", result.Content, err)
	}
	if events, err := runtime.memory.ActiveDialogueEvents(context.Background(), dialogueEventScope(run)); err != nil || len(events) != 0 {
		t.Fatalf("forgotten event remains: %#v, err=%v", events, err)
	}
}
