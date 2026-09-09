package main

import "time"

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
