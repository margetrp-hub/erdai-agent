package main

import (
	"strings"
	"testing"
	"time"
)

func TestDialogueQuestionFeedbackUsesOwnedUserAnswers(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	question := RecalledGroupEvent{ID: "q1", MessageID: "m1", Role: "assistant", PersonaID: "doubao", SenderRef: "bot", ReplyToSenderRef: "alice", ThreadKey: "topic", UntrustedText: "今天想喝什么？", OccurredAt: now.Add(-20 * time.Minute)}
	answer := RecalledGroupEvent{ID: "u1", Role: "user", PersonaID: "doubao", SenderRef: "alice", ThreadKey: "topic", UntrustedText: "冰美式", OccurredAt: now.Add(-19 * time.Minute)}
	for _, tc := range []struct {
		name   string
		mutate func(*RecalledGroupEvent)
		want   int
	}{
		{"same member", func(*RecalledGroupEvent) {}, 1},
		{"other member", func(e *RecalledGroupEvent) { e.SenderRef = "bob" }, 0},
		{"other persona", func(e *RecalledGroupEvent) { e.PersonaID = "xiaoman" }, 0},
		{"new topic", func(e *RecalledGroupEvent) { e.ThreadKey = "new" }, 0},
		{"new task", func(e *RecalledGroupEvent) { e.UntrustedText = "帮我查一下天气" }, 0},
		{"late unquoted message", func(e *RecalledGroupEvent) { e.OccurredAt = now.Add(-time.Minute) }, 0},
		{"delivery is not answer", func(e *RecalledGroupEvent) { e.Role = "assistant" }, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := answer
			tc.mutate(&e)
			asked, answered := dialogueQuestionFeedback([]RecalledGroupEvent{question, e}, "alice", now)
			if asked != 1 || answered != tc.want {
				t.Fatalf("asked=%d answered=%d", asked, answered)
			}
		})
	}
	question.OccurredAt = now.Add(-time.Minute)
	if asked, answered := dialogueQuestionFeedback([]RecalledGroupEvent{question}, "alice", now); asked != 0 || answered != 0 {
		t.Fatalf("new pending question prematurely counted: %d/%d", answered, asked)
	}
}

func TestRelationshipUnknownFeedbackDoesNotSuppressQuestions(t *testing.T) {
	if prompt := relationshipPulsePrompt(RelationshipPulse{Ready: true}); strings.Contains(prompt, "降低追问") {
		t.Fatalf("missing evidence treated as rejection: %s", prompt)
	}
}

func TestRelationshipOneSidedAddressingCannotBecomeClose(t *testing.T) {
	now := time.Now()
	if score := relationshipIntimacy(1000, 1000, 0, now, now); score >= 38 {
		t.Fatalf("one-sided traffic promoted relationship: %.1f", score)
	}
}
