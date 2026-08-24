package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func seedSearchRun(t *testing.T, runtime *AgentRuntime, id string) runRecord {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, reply_handle, conversation_ref, sender_ref, persona_id, state, created_at, updated_at)
		VALUES (?, ?, 'reply', 'group:one', 'member-a', 'doubao', 'running', ?, ?)`, id, "event-"+id, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return runRecord{ID: id, ConversationRef: "group:one", SenderRef: "member-a", PersonaID: "doubao", ThreadKey: "thread-a"}
}

func TestSearchRunReusesTheFirstResult(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	run := seedSearchRun(t, runtime, "run-search-cache")
	if _, _, handled, err := runtime.beginSearchRun(&run, "first query"); err != nil || handled {
		t.Fatalf("first reservation handled=%v err=%v", handled, err)
	}
	wantSources := []searchSource{{Title: "Result", URL: "https://example.com/result"}}
	runtime.finishSearchRunSuccess(run.ID, "short answer", wantSources)
	text, sources, handled, err := runtime.beginSearchRun(&run, "different query")
	if err != nil || !handled || text != "short answer" || len(sources) != 1 || sources[0].URL != wantSources[0].URL {
		t.Fatalf("cached search handled=%v text=%q sources=%+v err=%v", handled, text, sources, err)
	}
	var attempts int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_search_runs WHERE run_id = ?", run.ID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("search attempts=%d err=%v", attempts, err)
	}
}

func TestSearchRunReusesTheFirstFailure(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	run := seedSearchRun(t, runtime, "run-search-failure")
	if _, _, handled, err := runtime.beginSearchRun(&run, "query"); err != nil || handled {
		t.Fatalf("first reservation handled=%v err=%v", handled, err)
	}
	wantErr := errors.New("provider unavailable")
	runtime.finishSearchRunFailure(run.ID, wantErr)
	_, _, handled, err := runtime.beginSearchRun(&run, "query again")
	if !handled || err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("cached failure handled=%v err=%v", handled, err)
	}
}

func TestSearchEntityIsScopedAndDoesNotOverrideImageContext(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	run := runRecord{
		AgentInstanceID: "instance-a", Transport: "qq", TransportInstance: "qq-a",
		ConversationRef: "group:one", SenderRef: "member-a", PersonaID: "doubao", ThreadKey: "thread-a",
	}
	runtime.recordSearchEntity(run, "\u5e2e\u6211\u67e5\u4e00\u4e0b OpenAI", []searchSource{{Title: "OpenAI", URL: "https://example.com/openai"}})
	query := "\u5b83\u662f\u8c01"
	if got := runtime.expandSearchQuery(&run, query); got != "openai "+query {
		t.Fatalf("same scope expansion=%q", got)
	}
	for _, changed := range []runRecord{
		{AgentInstanceID: "instance-b", Transport: run.Transport, TransportInstance: run.TransportInstance, ConversationRef: run.ConversationRef, SenderRef: run.SenderRef, PersonaID: run.PersonaID, ThreadKey: run.ThreadKey},
		{AgentInstanceID: run.AgentInstanceID, Transport: run.Transport, TransportInstance: "qq-b", ConversationRef: run.ConversationRef, SenderRef: run.SenderRef, PersonaID: run.PersonaID, ThreadKey: run.ThreadKey},
		{AgentInstanceID: run.AgentInstanceID, Transport: "telegram", TransportInstance: run.TransportInstance, ConversationRef: run.ConversationRef, SenderRef: run.SenderRef, PersonaID: run.PersonaID, ThreadKey: run.ThreadKey},
		{ConversationRef: run.ConversationRef, SenderRef: "member-b", PersonaID: run.PersonaID, ThreadKey: run.ThreadKey},
		{ConversationRef: run.ConversationRef, SenderRef: run.SenderRef, PersonaID: run.PersonaID, ThreadKey: "thread-b"},
		{ConversationRef: run.ConversationRef, SenderRef: run.SenderRef, PersonaID: "other", ThreadKey: run.ThreadKey},
	} {
		if got := runtime.expandSearchQuery(&changed, query); got != query {
			t.Fatalf("entity crossed scope: %+v -> %q", changed, got)
		}
	}
	withImage := run
	withImage.Attachments = []transportAttachment{{Kind: "image", ID: "current-image"}}
	if got := runtime.expandSearchQuery(&withImage, query); got != query {
		t.Fatalf("old entity overrode current image context: %q", got)
	}
}
