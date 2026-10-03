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

func TestDialogueOutputFeedbackStaysWithTheAddressedMember(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	events := []RecalledGroupEvent{
		{ID: "reply", MessageID: "reply-message", Role: "assistant", PersonaID: "doubao", SenderRef: "bot", ReplyToSenderRef: "alice", ThreadKey: "topic", UntrustedText: "我已经按你说的改好了。", OccurredAt: now.Add(-20 * time.Minute)},
		{ID: "alice-correction", Role: "user", PersonaID: "doubao", SenderRef: "alice", ReplyToMessageID: "reply-message", ThreadKey: "topic", UntrustedText: "不对，先停一下", OccurredAt: now.Add(-19 * time.Minute)},
		{ID: "bob-acceptance", Role: "user", PersonaID: "doubao", SenderRef: "bob", ReplyToMessageID: "reply-message", ThreadKey: "topic", UntrustedText: "满意", OccurredAt: now.Add(-18 * time.Minute)},
		{ID: "alice-acceptance", Role: "user", PersonaID: "doubao", SenderRef: "alice", ReplyToMessageID: "reply-message", ThreadKey: "topic", UntrustedText: "可以了", OccurredAt: now.Add(-17 * time.Minute)},
	}
	feedback := dialogueOutputFeedbackSummary(events, "alice", now)
	if feedback.Corrections != 1 || feedback.Accepted != 1 {
		t.Fatalf("feedback = %+v", feedback)
	}
	if other := dialogueOutputFeedbackSummary(events, "bob", now); other.Corrections != 0 || other.Accepted != 0 {
		t.Fatalf("other member feedback leaked: %+v", other)
	}
}

func TestDialogueAnswerIsNotCountedAsAcceptance(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	events := []RecalledGroupEvent{
		{ID: "question", MessageID: "question-message", Role: "assistant", PersonaID: "doubao", SenderRef: "bot", ReplyToSenderRef: "alice", ThreadKey: "topic", UntrustedText: "要不要继续？", OccurredAt: now.Add(-10 * time.Minute)},
		{ID: "alice-answer", Role: "user", PersonaID: "doubao", SenderRef: "alice", ReplyToMessageID: "question-message", ThreadKey: "topic", UntrustedText: "可以了", OccurredAt: now.Add(-9 * time.Minute)},
	}
	feedback := dialogueOutputFeedbackSummary(events, "alice", now)
	if feedback.Accepted != 0 {
		t.Fatalf("question answer was counted as acceptance: %+v", feedback)
	}
}

func TestDialogueExplicitAcceptanceAvoidsGenericShortReplies(t *testing.T) {
	for _, message := range []string{"对了", "这次满意", "可以了", "谢谢"} {
		if !dialogueExplicitAcceptance(message) {
			t.Fatalf("explicit acceptance not recognized: %q", message)
		}
	}
	for _, message := range []string{"好", "好烦", "行吧，先这样", "确实"} {
		if dialogueExplicitAcceptance(message) {
			t.Fatalf("generic reply treated as acceptance: %q", message)
		}
	}
}
