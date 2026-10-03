package main

import (
	"strings"
	"testing"
)

func TestGroupParticipationDecisionPromptBalancesSpeechAndSilence(t *testing.T) {
	prompt := groupParticipationDecisionSystemPrompt()
	for _, phrase := range []string{
		"默认保持安静",
		"对当前情绪或玩笑作自然反应",
		"自然接话可以只是短短一句反应或态度",
		"单纯因为问题可回答",
		`"action":"reply|ignore"`,
	} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("decision prompt missing %q: %s", phrase, prompt)
		}
	}
}
