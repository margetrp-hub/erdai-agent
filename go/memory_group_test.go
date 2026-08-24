package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMemoryGroupSchemaIsIdempotentAndTextIsEncrypted(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("second schema initialization failed: %v", err)
	}

	const scope = "qq:group:123456"
	const memoryText = "the launch phrase is blue lantern"
	if _, created, err := store.AddMemory(ctx, scope, memoryText); err != nil || !created {
		t.Fatalf("add memory: created=%v err=%v", created, err)
	}
	const eventText = "never store this group text in plaintext"
	if _, created, err := store.ObserveGroupEvent(ctx, GroupEventInput{
		Conversation: scope,
		Sender:       "qq:user:987654",
		Role:         "user",
		Text:         eventText,
		OccurredAt:   store.now(),
	}, time.Hour); err != nil || !created {
		t.Fatalf("observe group event: created=%v err=%v", created, err)
	}

	var memoryCipher, scopeDigest []byte
	if err := store.runtime.db.QueryRow("SELECT content_cipher, scope_digest FROM agent_memories").Scan(&memoryCipher, &scopeDigest); err != nil {
		t.Fatal(err)
	}
	var eventCipher, conversationDigest, senderDigest []byte
	if err := store.runtime.db.QueryRow(`
		SELECT text_cipher, conversation_digest, sender_digest FROM conversation_events
	`).Scan(&eventCipher, &conversationDigest, &senderDigest); err != nil {
		t.Fatal(err)
	}
	for name, stored := range map[string][]byte{
		"memory cipher":       memoryCipher,
		"event cipher":        eventCipher,
		"scope digest":        scopeDigest,
		"conversation digest": conversationDigest,
		"sender digest":       senderDigest,
	} {
		if bytes.Contains(stored, []byte(memoryText)) || bytes.Contains(stored, []byte(eventText)) ||
			bytes.Contains(stored, []byte(scope)) || bytes.Contains(stored, []byte("987654")) {
			t.Fatalf("%s contains plaintext: %q", name, stored)
		}
	}
	if bytes.Equal(memoryCipher, []byte(memoryText)) || bytes.Equal(eventCipher, []byte(eventText)) {
		t.Fatal("text was stored without encryption")
	}
}

func TestMemorySearchRanksMetadataAndTracksAccess(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	ctx := context.Background()
	store.now = func() time.Time { return time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC) }
	if _, created, err := store.AddMemoryWithMetadata(ctx, "user-one", "用户喜欢茉莉花茶", MemoryMetadata{
		Source: "auto_capture", Kind: "preference", Confidence: 0.9, Importance: 0.9,
	}); err != nil || !created {
		t.Fatalf("add preference: created=%v err=%v", created, err)
	}
	if _, created, err := store.AddMemoryWithMetadata(ctx, "user-one", "用户偶尔喝红茶", MemoryMetadata{
		Source: "manual", Kind: "fact", Confidence: 0.5, Importance: 0.2,
	}); err != nil || !created {
		t.Fatalf("add generic memory: created=%v err=%v", created, err)
	}
	expires := store.now().Add(-time.Minute)
	if _, created, err := store.AddMemoryWithMetadata(ctx, "user-one", "用户以前喜欢茉莉花茶", MemoryMetadata{
		Source: "auto_capture", Kind: "preference", Confidence: 1, Importance: 1, ExpiresAt: &expires,
	}); err != nil || !created {
		t.Fatalf("add expired memory: created=%v err=%v", created, err)
	}
	memories, err := store.SearchMemories(ctx, "user-one", "茉莉花茶", 5)
	if err != nil || len(memories) != 1 || memories[0].Kind != "preference" {
		t.Fatalf("ranked memories = %+v, err=%v", memories, err)
	}
	var accessCount int
	if err = store.runtime.db.QueryRow("SELECT access_count FROM agent_memories WHERE id = ?", memories[0].ID).Scan(&accessCount); err != nil || accessCount != 1 {
		t.Fatalf("access count = %d: %v", accessCount, err)
	}
}

func TestRelationshipObservationIsIdempotentAndStagesInteraction(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	ctx := context.Background()
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	state, inserted, err := store.ObserveRelationship(ctx, "event-1", "group-one", "sender-one", true, now)
	if err != nil || !inserted || state.InteractionCount != 1 || state.Stage != "普通群友" {
		t.Fatalf("first relationship = %+v inserted=%v err=%v", state, inserted, err)
	}
	duplicate, inserted, err := store.ObserveRelationship(ctx, "event-1", "group-one", "sender-one", true, now)
	if err != nil || inserted || duplicate.InteractionCount != 1 {
		t.Fatalf("duplicate relationship = %+v inserted=%v err=%v", duplicate, inserted, err)
	}
	for index := 2; index <= 10; index++ {
		state, inserted, err = store.ObserveRelationship(ctx, fmt.Sprintf("event-%d", index), "group-one", "sender-one", false, now.Add(time.Duration(index)*time.Minute))
		if err != nil || !inserted {
			t.Fatalf("relationship event %d: inserted=%v err=%v", index, inserted, err)
		}
	}
	if state.InteractionCount != 10 || state.Stage != "熟悉群友" {
		t.Fatalf("mature relationship = %+v", state)
	}
}

func TestRelationshipPulseUsesRealEventsMemoryAndIdempotentReplyFeedback(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	store := runtime.memory
	ctx := context.Background()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return base.Add(10 * 24 * time.Hour) }
	conversation := personaConversationRef("doubao", "group-pulse")
	for index := 0; index < 8; index++ {
		_, inserted, err := store.ObserveRelationship(
			ctx, fmt.Sprintf("pulse-event-%d", index), conversation, "member-pulse",
			index%2 == 0, base.Add(time.Duration(index)*24*time.Hour),
		)
		if err != nil || !inserted {
			t.Fatalf("observe pulse event %d: inserted=%v err=%v", index, inserted, err)
		}
	}
	if err := store.ObserveRelationshipReply(ctx, "delivery-pulse", conversation, "member-pulse", base.Add(8*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.ObserveRelationshipReply(ctx, "delivery-pulse", conversation, "member-pulse", base.Add(9*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.AddMemoryWithMetadata(ctx, personaMemoryScope("doubao", "user", "member-pulse"), "喜欢夜间散步", MemoryMetadata{
		Source: "test", Kind: "preference", Confidence: 0.95, Importance: 0.85,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SearchMemories(ctx, personaMemoryScope("doubao", "user", "member-pulse"), "夜间散步", 3); err != nil {
		t.Fatal(err)
	}
	policy := runtimeMemoryPolicy{
		RelationshipPulseEnabled: true, OutputFeedbackEnabled: true,
		MemoryResonanceEnabled: true, CircadianAwarenessEnabled: true,
		LongingEnabled: true, PulseMinInteractions: 5, RhythmWindowEvents: 60,
		TimezoneOffsetMinutes: 480,
	}
	state, found, err := store.RelationshipWithPulse(ctx, conversation, "member-pulse", "doubao", policy)
	if err != nil || !found || state.Pulse == nil {
		t.Fatalf("pulse state = %+v found=%v err=%v", state, found, err)
	}
	if state.ReplyCount != 1 {
		t.Fatalf("duplicate reply feedback counted %d times", state.ReplyCount)
	}
	if state.Pulse.ReplyCount != 1 {
		t.Fatalf("reply feedback was not exposed to pulse evidence: %+v", state.Pulse)
	}
	if !state.Pulse.Ready || state.Pulse.MemoryResonance <= 0 || state.Pulse.RoutineExpectation <= 0 || state.Pulse.Longing <= 0 {
		t.Fatalf("derived pulse = %+v", state.Pulse)
	}
	if state.Pulse.MemoryCount != 1 || state.Pulse.TypicalGapHours < 23 || state.Pulse.TypicalGapHours > 25 {
		t.Fatalf("pulse evidence = %+v", state.Pulse)
	}
}

func TestRelationshipIdentityBackfillUsesMatchingEventMetadata(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	ctx := context.Background()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	_, inserted, err := store.ObserveGroupEvent(ctx, GroupEventInput{
		ID: "backfill-event", Conversation: "group-one", Sender: "member-one",
		SenderDisplayName: "小明", PersonaID: "doubao", Role: "user", Text: "在吗", OccurredAt: now,
	}, 24*time.Hour)
	if err != nil || !inserted {
		t.Fatalf("observe group event: inserted=%v err=%v", inserted, err)
	}
	conversationDigest := store.digest("conversation", personaConversationRef("doubao", "group-one"))
	senderDigest := store.digest("sender", "member-one")
	if _, err = store.runtime.db.Exec(`
		INSERT INTO relationship_events (event_id, conversation_digest, sender_digest, occurred_at) VALUES (?, ?, ?, ?)
	`, "backfill-event", conversationDigest, senderDigest, formatStoreTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.runtime.db.Exec(`
		INSERT INTO member_relationship_state (
			conversation_digest, sender_digest, relationship_id, interaction_count,
			addressed_count, last_interaction_at, updated_at
		) VALUES (?, ?, 'rel_backfill', 4, 2, ?, ?)
	`, conversationDigest, senderDigest, formatStoreTime(now), formatStoreTime(now)); err != nil {
		t.Fatal(err)
	}
	if err = store.backfillRelationshipIdentities(ctx); err != nil {
		t.Fatal(err)
	}
	items, total, err := store.ListRelationships(ctx, "doubao", 10, 0)
	if err != nil || total != 1 || len(items) != 1 || items[0].SenderDisplayName != "小明" {
		t.Fatalf("backfilled relationships = %+v total=%d err=%v", items, total, err)
	}
}

func TestMemoriesAreScopedSearchableAndForgettable(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	mustInitMemoryGroupSchema(t, store)

	alpha, created, err := store.AddMemory(ctx, "group-alpha", "Prefers jasmine tea")
	if err != nil || !created {
		t.Fatalf("add alpha memory: created=%v err=%v", created, err)
	}
	if _, created, err := store.AddMemory(ctx, "group-beta", "Prefers jasmine tea"); err != nil || !created {
		t.Fatalf("same text in another scope: created=%v err=%v", created, err)
	}
	if _, _, err := store.AddMemory(ctx, "group-alpha", "Only alpha knows this detail"); err != nil {
		t.Fatal(err)
	}

	alphaMatches, err := store.SearchMemories(ctx, "group-alpha", "JASMINE", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(alphaMatches) != 1 || alphaMatches[0].UntrustedContent != "Prefers jasmine tea" {
		t.Fatalf("unexpected alpha search: %#v", alphaMatches)
	}
	betaList, err := store.ListMemories(ctx, "group-beta", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(betaList) != 1 || strings.Contains(betaList[0].UntrustedContent, "alpha") {
		t.Fatalf("scope leaked into beta: %#v", betaList)
	}

	if deleted, err := store.ForgetMemory(ctx, "group-beta", alpha.ID); err != nil || deleted {
		t.Fatalf("wrong scope deleted alpha memory: deleted=%v err=%v", deleted, err)
	}
	if deleted, err := store.ForgetMemory(ctx, "group-alpha", alpha.ID); err != nil || !deleted {
		t.Fatalf("forget alpha memory: deleted=%v err=%v", deleted, err)
	}
}

func TestRecentGroupEventsPruneTTLOrderLimitAndTrackAck(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 8, 2, 6, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }

	events := []struct {
		text       string
		occurredAt time.Time
		ttl        time.Duration
	}{
		{text: "expired", occurredAt: now.Add(-2 * time.Hour), ttl: time.Minute},
		{text: "first", occurredAt: now.Add(-3 * time.Minute), ttl: time.Hour},
		{text: "second", occurredAt: now.Add(-2 * time.Minute), ttl: time.Hour},
		{text: "third", occurredAt: now.Add(-time.Minute), ttl: time.Hour},
	}
	for _, event := range events {
		if _, created, err := store.ObserveGroupEvent(ctx, GroupEventInput{
			Conversation: "group-1",
			Sender:       "user-1",
			Role:         "user",
			Text:         event.text,
			OccurredAt:   event.occurredAt,
		}, event.ttl); err != nil || !created {
			t.Fatalf("observe %q: created=%v err=%v", event.text, created, err)
		}
	}
	if _, _, err := store.ObserveGroupEvent(ctx, GroupEventInput{
		Conversation: "group-2", Sender: "user-2", Role: "user", Text: "other group", OccurredAt: now,
	}, time.Hour); err != nil {
		t.Fatal(err)
	}

	recent, err := store.RecentGroupEvents(ctx, "group-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 || recent[0].UntrustedText != "second" || recent[1].UntrustedText != "third" {
		t.Fatalf("unexpected recent events: %#v", recent)
	}
	var expiredCount int
	if err := store.runtime.db.QueryRow("SELECT count(*) FROM conversation_events WHERE text_cipher IS NOT NULL").Scan(&expiredCount); err != nil {
		t.Fatal(err)
	}
	if expiredCount != 4 {
		t.Fatalf("expected one expired event pruned, remaining=%d", expiredCount)
	}

	firstAck := now.Add(-30 * time.Second)
	if err := store.MarkBotReplyAck(ctx, "group-1", firstAck); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkBotReplyAck(ctx, "group-1", firstAck.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	ack, found, err := store.LastBotReplyAck(ctx, "group-1")
	if err != nil || !found || !ack.Equal(firstAck) {
		t.Fatalf("ack moved backwards: ack=%v found=%v err=%v", ack, found, err)
	}
	newerAck := now.Add(time.Second)
	if err := store.MarkBotReplyAck(ctx, "group-1", newerAck); err != nil {
		t.Fatal(err)
	}
	ack, found, err = store.LastBotReplyAck(ctx, "group-1")
	if err != nil || !found || !ack.Equal(newerAck) {
		t.Fatalf("newer ack missing: ack=%v found=%v err=%v", ack, found, err)
	}
}

func TestSearchGroupEventsRecallsRelevantColdHistory(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for index, text := range []string{
		"今天中午吃面", "部署方案决定使用蓝绿发布", "晚上看看电影", "蓝绿发布先跑影子流量",
	} {
		if _, created, err := store.ObserveGroupEvent(ctx, GroupEventInput{
			Conversation: "group-1", Sender: "user-1", Role: "user", Text: text,
			OccurredAt: now.Add(time.Duration(index-20) * time.Hour),
		}, 365*24*time.Hour); err != nil || !created {
			t.Fatalf("observe event %d: created=%v err=%v", index, created, err)
		}
	}
	hits, err := store.SearchGroupEvents(ctx, "group-1", "回顾一下之前的蓝绿发布方案", 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || !strings.Contains(hits[0].UntrustedText, "蓝绿发布") ||
		!strings.Contains(hits[1].UntrustedText, "蓝绿发布") {
		t.Fatalf("cold history hits = %#v", hits)
	}
	if unrelated, err := store.SearchGroupEvents(ctx, "group-1", "你还记得吗", 100, 3); err != nil || len(unrelated) != 0 {
		t.Fatalf("empty recall query = %#v, err=%v", unrelated, err)
	}
}

func TestRecentGroupEventsSupportsLargeColdScan(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	mustInitMemoryGroupSchema(t, store)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for index := 0; index < 150; index++ {
		if _, _, err := store.ObserveGroupEvent(ctx, GroupEventInput{
			Conversation: "group-1", Sender: "user-1", Role: "user",
			Text: fmt.Sprintf("message-%03d", index), OccurredAt: now.Add(time.Duration(index) * time.Second),
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	events, err := store.RecentGroupEvents(ctx, "group-1", 150)
	if err != nil || len(events) != 150 {
		t.Fatalf("large cold scan = %d, err=%v", len(events), err)
	}
}

func TestPersonaConversationHistoryDoesNotCrossRoleCards(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for index, item := range []struct {
		persona string
		text    string
	}{
		{persona: "doubao", text: "豆包记得蓝色雨伞"},
		{persona: "second-role", text: "另一个角色记得红色帽子"},
	} {
		if _, _, err := store.ObserveGroupEvent(ctx, GroupEventInput{
			ID: "persona-event-" + item.persona, Conversation: "group-one", Sender: "member-one",
			PersonaID: item.persona, Role: "user", Text: item.text,
			OccurredAt: now.Add(time.Duration(index) * time.Second),
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	doubao, err := store.RecentPersonaGroupEvents(ctx, "group-one", "doubao", 10)
	if err != nil || len(doubao) != 1 || doubao[0].UntrustedText != "豆包记得蓝色雨伞" {
		t.Fatalf("doubao history = %+v: %v", doubao, err)
	}
	second, err := store.SearchPersonaGroupEvents(ctx, "group-one", "second-role", "回顾红色帽子", 100, 5)
	if err != nil || len(second) != 1 || second[0].UntrustedText != "另一个角色记得红色帽子" {
		t.Fatalf("second role history = %+v: %v", second, err)
	}
	if leaked, err := store.SearchPersonaGroupEvents(ctx, "group-one", "doubao", "回顾红色帽子", 100, 5); err != nil || len(leaked) != 0 {
		t.Fatalf("cross-role history leak = %+v: %v", leaked, err)
	}
}

func TestEpisodeSummaryPersistsPerPersonaAndThread(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	mustInitMemoryGroupSchema(t, store)
	ctx := context.Background()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	for index := 0; index < 2; index++ {
		if _, _, err := store.ObserveGroupEvent(ctx, GroupEventInput{
			ID: fmt.Sprintf("episode-event-%d", index), Conversation: "group-one", Sender: "member-one",
			PersonaID: "doubao", ThreadKey: "thread-a", Role: "user",
			Text: fmt.Sprintf("讨论部署步骤 %d", index), OccurredAt: now.Add(time.Duration(index) * time.Minute),
		}, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MaybeRefreshEpisodeSummary(ctx, "group-one", "doubao", "thread-a", 2, 2, time.Hour); err != nil {
		t.Fatal(err)
	}
	episodes, err := store.RecentPersonaEpisodes(ctx, "group-one", "doubao", 3)
	if err != nil || len(episodes) != 1 || episodes[0].ThreadKey != "thread-a" || !strings.Contains(episodes[0].Summary, "部署步骤") {
		t.Fatalf("episode summary = %+v: %v", episodes, err)
	}
	other, err := store.RecentPersonaEpisodes(ctx, "group-one", "second-role", 3)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-persona episode leak = %+v: %v", other, err)
	}
}

func TestLegacyMigrationIsIdempotent(t *testing.T) {
	store := newTestMemoryGroupStore(t)
	ctx := context.Background()
	mustInitMemoryGroupSchema(t, store)

	first, created, err := store.ImportLegacyMemory(ctx, "group-1", "embedded-memory", "42", "legacy detail")
	if err != nil || !created {
		t.Fatalf("first memory import: created=%v err=%v", created, err)
	}
	second, created, err := store.ImportLegacyMemory(ctx, "group-1", "embedded-memory", "42", "legacy detail")
	if err != nil || created || second.ID != first.ID {
		t.Fatalf("duplicate memory import: first=%s second=%s created=%v err=%v", first.ID, second.ID, created, err)
	}
	if _, _, err := store.ImportLegacyMemory(ctx, "group-2", "embedded-memory", "42", "legacy detail"); err == nil {
		t.Fatal("legacy identity was allowed to cross scopes")
	}

	input := GroupEventInput{
		Conversation: "group-1",
		Sender:       "user-1",
		Role:         "user",
		Text:         "legacy group message",
		OccurredAt:   store.now(),
		LegacySource: "embedded-companion",
		LegacyID:     "73",
	}
	event1, created, err := store.ObserveGroupEvent(ctx, input, time.Hour)
	if err != nil || !created {
		t.Fatalf("first event import: created=%v err=%v", created, err)
	}
	input.ID = "a-different-generated-id"
	event2, created, err := store.ObserveGroupEvent(ctx, input, time.Hour)
	if err != nil || created || event2.ID != event1.ID {
		t.Fatalf("duplicate event import: first=%s second=%s created=%v err=%v", event1.ID, event2.ID, created, err)
	}

	var memories, events int
	if err := store.runtime.db.QueryRow("SELECT count(*) FROM agent_memories").Scan(&memories); err != nil {
		t.Fatal(err)
	}
	if err := store.runtime.db.QueryRow("SELECT count(*) FROM conversation_events").Scan(&events); err != nil {
		t.Fatal(err)
	}
	if memories != 1 || events != 1 {
		t.Fatalf("migration duplicated rows: memories=%d events=%d", memories, events)
	}
}

func newTestMemoryGroupStore(t *testing.T) *MemoryGroupStore {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	block, err := aes.NewCipher(bytes.Repeat([]byte{0x41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewMemoryGroupStore(&AgentRuntime{db: db, aead: aead}, bytes.Repeat([]byte{0x52}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func mustInitMemoryGroupSchema(t *testing.T, store *MemoryGroupStore) {
	t.Helper()
	if err := store.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
}
