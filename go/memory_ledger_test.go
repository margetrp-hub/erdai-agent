package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func memoryLedgerTestRun(instance, transportInstance string) runRecord {
	return runRecord{
		ID: "run-" + instance, EventID: "event-" + instance,
		Transport: "qq_official", TransportInstance: transportInstance,
		ConversationRef: "group-ledger", ConversationKind: "group",
		SenderRef: "member-ledger", AgentInstanceID: instance, MemoryNamespace: instance,
		PersonaID: "doubao",
	}
}

// The memory ledger closed loop: observe → auto-capture → scoped recall →
// same-scope dedupe → expiry filter → prune actually deletes.
func TestMemoryLedgerClosedLoop(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	runA := memoryLedgerTestRun("instance-a", "qq-a")

	if _, _, err := runtime.memory.ObserveGroupEvent(ctx, GroupEventInput{
		ID: "ledger-event", Conversation: "group-ledger", Sender: "member-ledger",
		PersonaID: "doubao", Role: "user", Text: "以后叫我老王，我喜欢茉莉花茶",
		OccurredAt: time.Now().UTC(),
	}, time.Hour); err != nil {
		t.Fatal(err)
	}
	runtime.captureStableMemory(ctx, runA, "以后叫我老王，我喜欢茉莉花茶")
	var captured int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_memories
		WHERE source = 'auto_capture'`).Scan(&captured); err != nil {
		t.Fatal(err)
	}
	if captured == 0 {
		t.Fatal("auto capture stored nothing")
	}

	// Same text captured again in the same scope must not duplicate.
	runtime.captureStableMemory(ctx, runA, "以后叫我老王，我喜欢茉莉花茶")
	var afterRepeat int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_memories
		WHERE source = 'auto_capture'`).Scan(&afterRepeat); err != nil {
		t.Fatal(err)
	}
	if afterRepeat != captured {
		t.Fatalf("same-scope duplicate stored: %d -> %d", captured, afterRepeat)
	}

	// Recall sees it in the owning instance and not in a sibling instance.
	recalled, err := runtime.recallMemory(ctx, runA, "茉莉花茶")
	if err != nil || !strings.Contains(recalled.Content, "茉莉花茶") {
		t.Fatalf("owner recall = %+v err=%v", recalled, err)
	}
	runB := memoryLedgerTestRun("instance-b", "qq-b")
	other, err := runtime.recallMemory(ctx, runB, "茉莉花茶")
	if err == nil && strings.Contains(other.Content, "茉莉花茶") {
		t.Fatalf("memory leaked across instances: %+v", other)
	}

	// Expired rows disappear from recall, and prune physically deletes them.
	if _, err := runtime.db.Exec(`UPDATE agent_memories SET expires_at = ?`,
		time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	expired, err := runtime.recallMemory(ctx, runA, "茉莉花茶")
	if err == nil && strings.Contains(expired.Content, "茉莉花茶") {
		t.Fatalf("expired memory still recalled: %+v", expired)
	}
	if err := runtime.memory.PruneExpiredMemories(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_memories`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("prune left %d expired rows", remaining)
	}
}
