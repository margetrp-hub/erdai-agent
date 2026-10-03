package main

import (
	"strings"
	"time"
)

// dialogueOutputFeedback is derived from the bounded conversation timeline.
// It deliberately records only explicit signals tied to this member's latest
// assistant turn; ambient group reactions must not change their relationship
// or make the agent learn the wrong person's preference.
type dialogueOutputFeedback struct {
	QuestionsObserved int
	QuestionsAnswered int
	Corrections       int
	Accepted          int
}

// This is conversational evidence, not delivery feedback. Give an unanswered
// question time to receive an answer and require a known recipient. Missing
// history is unknown rather than negative feedback.
func dialogueQuestionFeedback(events []RecalledGroupEvent, sender string, now time.Time) (asked, answered int) {
	if sender == "" {
		return
	}
	seen := map[string]bool{}
	for index, question := range events {
		if question.Role != "assistant" || question.ID == "" || seen[question.ID] || !dialogueHasQuestion(question.UntrustedText) || question.OccurredAt.IsZero() || question.OccurredAt.After(now) || now.Sub(question.OccurredAt) > 24*time.Hour {
			continue
		}
		seen[question.ID] = true
		target, ambiguous := dialogueAssistantTarget(events, index)
		if ambiguous || target != sender {
			continue
		}
		end := index + 1
		for end < len(events) && !events[end].OccurredAt.After(question.OccurredAt.Add(10*time.Minute)) {
			end++
		}
		answer := dialogueQuestionAnswer(events, index, end, target, RecalledGroupEvent{SenderRef: sender, PersonaID: question.PersonaID})
		if answer.ID == "" && now.Sub(question.OccurredAt) < 10*time.Minute {
			continue
		}
		asked++
		if answer.ID != "" {
			answered++
		}
	}
	return
}

func dialogueOutputFeedbackSummary(events []RecalledGroupEvent, sender string, now time.Time) dialogueOutputFeedback {
	asked, answered := dialogueQuestionFeedback(events, sender, now)
	result := dialogueOutputFeedback{QuestionsObserved: asked, QuestionsAnswered: answered}
	if sender == "" {
		return result
	}
	for index, event := range events {
		if event.Role != "user" || event.SenderRef != sender || event.ID == "" || event.OccurredAt.IsZero() || event.OccurredAt.After(now) || now.Sub(event.OccurredAt) > 24*time.Hour {
			continue
		}
		thread := resolveDialogueThread(events, event.ID)
		if thread.PreviousAssistant.ID == "" {
			continue
		}
		if looksLikeCorrection(event.UntrustedText, thread.PreviousAssistant.UntrustedText) {
			result.Corrections++
			continue
		}
		if dialogueExplicitAcceptance(event.UntrustedText) && dialogueAssistantTargetMatches(events, index, sender) {
			result.Accepted++
		}
	}
	return result
}

func dialogueAssistantTargetMatches(events []RecalledGroupEvent, userIndex int, sender string) bool {
	for index := userIndex - 1; index >= 0; index-- {
		event := events[index]
		if event.Role != "assistant" {
			continue
		}
		// A short answer such as “可以了” is not praise for the bot when
		// the preceding assistant turn was itself a question. It belongs to
		// the question feedback path above.
		if dialogueHasQuestion(event.UntrustedText) {
			return false
		}
		target, ambiguous := dialogueAssistantTarget(events, index)
		return !ambiguous && target != "" && target == sender
	}
	return false
}

func dialogueExplicitAcceptance(message string) bool {
	switch strings.Trim(strings.TrimSpace(message), "。！!？? ") {
	case "对", "对了", "这次对了", "这个对了", "这张对了", "满意", "这次满意", "可以", "可以了", "行", "行了", "没问题", "不错", "挺好", "谢谢", "感谢", "收到", "明白", "懂了":
		return true
	default:
		return false
	}
}
