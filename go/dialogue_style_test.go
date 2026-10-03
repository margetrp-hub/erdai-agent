package main

import (
	"strings"
	"testing"
	"time"
)

func TestConversationStyleHintUsesRecentHumanRhythm(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	events := []RecalledGroupEvent{
		{ID: "one", Role: "user", OccurredAt: now.Add(-9 * time.Minute), UntrustedText: "哈哈，绝了"},
		{ID: "two", Role: "user", OccurredAt: now.Add(-7 * time.Minute), UntrustedText: "你看这个呀"},
		{ID: "three", Role: "assistant", OccurredAt: now.Add(-5 * time.Minute), UntrustedText: "我看到了"},
		{ID: "four", Role: "user", OccurredAt: now.Add(-2 * time.Minute), UntrustedText: "真的嘛？"},
		{ID: "current", Role: "user", OccurredAt: now, UntrustedText: "接着说"},
	}
	hint := conversationStyleHint(events, "current")
	for _, phrase := range []string{"群聊节奏参考", "短句", "口语和轻松语气较多", "不复制群友原句"} {
		if !strings.Contains(hint, phrase) {
			t.Fatalf("hint missing %q: %s", phrase, hint)
		}
	}
}

func TestConversationStyleHintNeedsEnoughRecentHumanMessages(t *testing.T) {
	now := time.Now().UTC()
	events := []RecalledGroupEvent{
		{ID: "old", Role: "user", OccurredAt: now.Add(-time.Hour), UntrustedText: "哈哈哈哈"},
		{ID: "current", Role: "user", OccurredAt: now, UntrustedText: "继续"},
	}
	if got := conversationStyleHint(events, "current"); got != "" {
		t.Fatalf("insufficient recent messages produced a hint: %s", got)
	}
}
