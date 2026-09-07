package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMemoryCognitionPreferenceUpdateAndReplay(t *testing.T) {
	a := newIdleRuntime(t)
	defer a.Close()
	ctx := context.Background()
	run := memoryLedgerTestRun("cognition", "qq")
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	first := time.Now().UTC().Add(-time.Hour)
	run.EventID, run.CreatedAt = "preference-first", first.Format(time.RFC3339Nano)
	a.captureStableMemory(ctx, run, "我喜欢咖啡")
	run.EventID, run.CreatedAt = "preference-change", first.Add(time.Minute).Format(time.RFC3339Nano)
	a.captureStableMemory(ctx, run, "我不喜欢咖啡")
	assertCurrentMemory := func(want string) {
		t.Helper()
		memories, err := a.memory.ListMemories(ctx, scope, 20)
		if err != nil || len(memories) != 1 || memories[0].UntrustedContent != want {
			t.Fatalf("current preference = %+v, err=%v, want %q", memories, err, want)
		}
	}
	assertCurrentMemory("我不喜欢咖啡")
	// A delayed replay must not restore the superseded preference.
	run.EventID, run.CreatedAt = "preference-first", first.Format(time.RFC3339Nano)
	a.captureStableMemory(ctx, run, "我喜欢咖啡")
	assertCurrentMemory("我不喜欢咖啡")
	run.EventID, run.CreatedAt = "preference-return", first.Add(2*time.Minute).Format(time.RFC3339Nano)
	a.captureStableMemory(ctx, run, "我喜欢咖啡")
	assertCurrentMemory("我喜欢咖啡")
	var count int
	if err := a.db.QueryRow("SELECT count(*) FROM agent_memories").Scan(&count); err != nil || count != 2 {
		t.Fatalf("historical rows = %d, err=%v", count, err)
	}
}

func TestMemoryCognitionTemporaryQuotedAndScopedFacts(t *testing.T) {
	a := newIdleRuntime(t)
	defer a.Close()
	ctx := context.Background()
	run := memoryLedgerTestRun("cognition", "qq")
	a.captureStableMemory(ctx, run, "我喜欢咖啡")
	for _, message := range []string{
		"今天我不喜欢咖啡", "我今天不喜欢咖啡", "我不喜欢咖啡，只限今天",
		"他说：我不喜欢咖啡", "朋友说，我不喜欢咖啡", "引用：\"我不喜欢咖啡\"",
		"假如我不喜欢咖啡呢", "我不喜欢咖啡吗？", "我不喜欢咖啡才怪",
	} {
		if values := extractStableMemories(message); len(values) != 0 {
			t.Errorf("temporary, quoted or uncertain text captured: %q -> %+v", message, values)
		}
	}
	other := run
	other.SenderRef = "other-member"
	a.captureStableMemory(ctx, other, "我不喜欢咖啡")
	for _, sample := range []struct {
		run  runRecord
		want string
	}{{run, "我喜欢咖啡"}, {other, "我不喜欢咖啡"}} {
		scope := personaMemoryScope(sample.run.PersonaID, "user", runtimeScopeFromRun(sample.run).userMemoryRef())
		got, err := a.memory.ListMemories(ctx, scope, 20)
		if err != nil || len(got) != 1 || got[0].UntrustedContent != sample.want {
			t.Fatalf("scope got %+v err=%v", got, err)
		}
	}
}

func TestMemoryCognitionAddressUpdatesWithoutErasingOtherLikes(t *testing.T) {
	a := newIdleRuntime(t)
	defer a.Close()
	run := memoryLedgerTestRun("cognition", "qq")
	a.captureStableMemory(context.Background(), run, "以后叫我老王，我喜欢咖啡，我喜欢茶")
	run.EventID = "new-address"
	a.captureStableMemory(context.Background(), run, "以后叫我小王")
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	got, err := a.memory.ListMemories(context.Background(), scope, 20)
	if err != nil || len(got) != 3 {
		t.Fatalf("address/likes = %+v, err=%v", got, err)
	}
	for _, memory := range got {
		if strings.Contains(memory.UntrustedContent, "老王") {
			t.Fatal("superseded address recalled")
		}
	}
}

func TestMemoryCognitionSameMessageCorrectionUsesLastStatement(t *testing.T) {
	for _, sample := range []struct{ message, want string }{
		{"我喜欢咖啡，不对，我不喜欢咖啡", "我不喜欢咖啡"},
		{"我不喜欢咖啡，不对，我喜欢咖啡", "我喜欢咖啡"},
		{"我喜欢咖啡，我不喜欢咖啡，不对，我喜欢咖啡", "我喜欢咖啡"},
		{"我喜欢喝咖啡，我已经不再喜欢咖啡", "我已经不再喜欢咖啡"},
	} {
		t.Run(sample.message, func(t *testing.T) {
			a := newIdleRuntime(t)
			defer a.Close()
			run := memoryLedgerTestRun("cognition", "qq")
			a.captureStableMemory(context.Background(), run, sample.message)
			scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
			got, err := a.memory.ListMemories(context.Background(), scope, 20)
			if err != nil || len(got) != 1 || got[0].UntrustedContent != sample.want {
				t.Fatalf("self-correction = %+v err=%v, want %q", got, err, sample.want)
			}
		})
	}
}

func TestMemoryCognitionLegacyFactsAndManualEdits(t *testing.T) {
	a := newIdleRuntime(t)
	defer a.Close()
	ctx := context.Background()
	run := memoryLedgerTestRun("cognition", "qq")
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	legacy, _, err := a.memory.AddMemoryWithMetadata(ctx, scope, "我喜欢咖啡", MemoryMetadata{Source: "auto_capture", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	manual, _, err := a.memory.AddMemoryWithMetadata(ctx, scope, "我喜欢茶", MemoryMetadata{Source: "manual", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.db.Exec("UPDATE agent_memories SET created_at=? WHERE id=?", formatStoreTime(time.Now().UTC().Add(-time.Hour)), legacy.ID); err != nil {
		t.Fatal(err)
	}
	run.EventID = "update-legacy"
	a.captureStableMemory(ctx, run, "我不喜欢咖啡，我喜欢茶")
	got, err := a.memory.ListMemories(ctx, scope, 20)
	if err != nil || len(got) != 2 {
		t.Fatalf("legacy update = %+v err=%v", got, err)
	}
	var revised RecalledMemory
	for _, memory := range got {
		if memory.ID == legacy.ID {
			t.Fatal("legacy automatic fact remained active")
		}
		if memory.ID == manual.ID && memory.Source != "manual" {
			t.Fatal("automatic capture replaced manual provenance")
		}
		if memory.UntrustedContent == "我不喜欢咖啡" {
			revised = memory
		}
	}
	if revised.ID == "" {
		t.Fatal("updated preference missing")
	}
	if _, changed, err := a.memory.UpdateMemory(ctx, scope, revised.ID, "我喜欢清茶", MemoryMetadata{Source: "manual", Kind: "preference"}); err != nil || !changed {
		t.Fatalf("manual correction changed=%v err=%v", changed, err)
	}
	var clean int
	if err := a.db.QueryRow(`SELECT count(*) FROM agent_memories WHERE id=? AND fact_key_digest IS NULL
		AND source_event_digest IS NULL AND observed_at IS NULL AND source='manual'`, revised.ID).Scan(&clean); err != nil || clean != 1 {
		t.Fatalf("manual correction retained obsolete fact provenance: count=%d err=%v", clean, err)
	}
}
