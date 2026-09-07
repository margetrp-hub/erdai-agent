package main

import "strings"

type dialogueThreadState struct {
	PreviousAssistant   RecalledGroupEvent
	PendingQuestion     RecalledGroupEvent
	AnsweredQuestion    RecalledGroupEvent
	PreviousAnswer      RecalledGroupEvent
	QuestionTarget      string
	SameSpeakerFollowup bool
}

func dialogueCurrentIndex(events []RecalledGroupEvent, currentEventID string) int {
	for index := range events {
		if events[index].ID == currentEventID {
			return index
		}
	}
	return len(events)
}

// Rebuild the latest question's ownership and answer state from the existing
// bounded event window. A shared thread is not evidence of a shared recipient.
func resolveDialogueThread(events []RecalledGroupEvent, currentEventID string) dialogueThreadState {
	var state dialogueThreadState
	currentIndex := dialogueCurrentIndex(events, currentEventID)
	current := RecalledGroupEvent{ID: currentEventID}
	if currentIndex < len(events) {
		current = events[currentIndex]
	}
	for index := currentIndex - 1; index >= 0; index-- {
		candidate := events[index]
		if candidate.Role != "assistant" || !dialogueSamePersona(candidate, current) {
			continue
		}
		explicit := strings.TrimSpace(current.ReplyToMessageID) != ""
		if explicit && !dialogueReplyMatches(current, candidate) {
			continue
		}
		if !explicit && current.ReplyToSenderRef != "" && current.ReplyToSenderRef != candidate.SenderRef {
			continue
		}
		target, ambiguous := dialogueAssistantTarget(events, index)
		if ambiguous || (target != "" && target != current.SenderRef) {
			continue
		}
		if !explicit && !dialogueSameThread(candidate, current) {
			continue
		}
		// Older events without a recipient are usable only when no other
		// identifiable member has intervened. Never guess across an interjection.
		if target == "" && dialogueOtherMemberSince(events, index+1, currentIndex, current) {
			continue
		}
		if state.PreviousAssistant.ID == "" {
			state.PreviousAssistant = candidate
			state.SameSpeakerFollowup = target != "" && target == current.SenderRef
		}
		if !dialogueHasQuestion(candidate.UntrustedText) {
			if explicit {
				return state
			}
			continue
		}
		state.QuestionTarget = target
		if answer := dialogueQuestionAnswer(events, index, currentIndex, target, current); answer.ID != "" {
			state.AnsweredQuestion, state.PreviousAnswer = candidate, answer
		} else {
			state.PendingQuestion = candidate
		}
		return state
	}
	return state
}

func dialogueAssistantTarget(events []RecalledGroupEvent, assistantIndex int) (string, bool) {
	assistant := events[assistantIndex]
	if target := strings.TrimSpace(assistant.ReplyToSenderRef); target != "" {
		return target, false
	}
	if assistant.ReplyToMessageID != "" {
		for index := assistantIndex - 1; index >= 0; index-- {
			previous := events[index]
			if previous.Role == "user" && dialogueSamePersona(previous, assistant) && dialogueReplyMatches(assistant, previous) {
				return previous.SenderRef, false
			}
		}
		return "", true
	}
	// Legacy rows have no reply target. Accept a single identifiable speaker
	// before this assistant turn; mixed-speaker old turns remain unassigned.
	target := ""
	for index := assistantIndex - 1; index >= 0; index-- {
		previous := events[index]
		if !dialogueSamePersona(previous, assistant) || !dialogueSameThread(previous, assistant) {
			continue
		}
		if previous.Role == "assistant" {
			break
		}
		if previous.Role != "user" || previous.SenderRef == "" {
			continue
		}
		if target != "" && target != previous.SenderRef {
			return "", true
		}
		target = previous.SenderRef
	}
	return target, false
}

func dialogueQuestionAnswer(events []RecalledGroupEvent, questionIndex, currentIndex int, target string, current RecalledGroupEvent) RecalledGroupEvent {
	question := events[questionIndex]
	var recorded RecalledGroupEvent
	latestAssistantIndex := questionIndex
	for index := questionIndex + 1; index < currentIndex; index++ {
		answer := events[index]
		if answer.Role == "assistant" && dialogueSamePersona(question, answer) && dialogueSameThread(question, answer) {
			owner, ambiguous := dialogueAssistantTarget(events, index)
			if !ambiguous && owner == target {
				latestAssistantIndex = index
			}
		}
		if answer.Role != "user" || !dialogueSamePersona(question, answer) || (target != "" && answer.SenderRef != target) {
			continue
		}
		if target == "" && answer.SenderRef != current.SenderRef {
			continue
		}
		if answer.ReplyToMessageID != "" {
			if !dialogueReplyMatches(answer, question) {
				continue
			}
		} else {
			if answer.ReplyToSenderRef != "" && answer.ReplyToSenderRef != question.SenderRef {
				continue
			}
			if !dialogueSameThread(question, answer) {
				continue
			}
			// An unquoted answer belongs to the most recent question for that
			// member, not an older question later reached by an explicit reply.
			newerQuestion := false
			for later := questionIndex + 1; later < index; later++ {
				if events[later].Role != "assistant" || !dialogueSamePersona(question, events[later]) || !dialogueHasQuestion(events[later].UntrustedText) {
					continue
				}
				owner, ambiguous := dialogueAssistantTarget(events, later)
				if !ambiguous && owner == target && dialogueSameThread(events[later], answer) {
					newerQuestion = true
				}
			}
			if newerQuestion {
				continue
			}
		}
		if !dialogueLooksLikeAnswer(answer.UntrustedText) {
			continue
		}
		// Keep ordinary followups from replacing a completed answer. An
		// explicit quote can revise that answer later; an unquoted correction
		// applies only before a newer assistant turn changes what is being corrected.
		if recorded.ID == "" || dialogueReplyMatches(answer, question) ||
			(latestAssistantIndex == questionIndex && looksLikeCorrection(answer.UntrustedText, recorded.UntrustedText)) {
			recorded = answer
		}
	}
	return recorded
}

func dialogueLooksLikeAnswer(message string) bool {
	message = strings.TrimSpace(message)
	return message != "" && !looksLikeNewRequest(message) && !looksLikeDirectPing(message) &&
		(message == "确实" || !isLowInformationReaction(message))
}

func dialogueHasQuestion(message string) bool {
	return strings.ContainsAny(message, "?？") && !containsAnyText(message, []string{"继续说", "继续猜", "下一个问题", "怎么了"})
}

func dialogueSamePersona(left, right RecalledGroupEvent) bool {
	return left.PersonaID == "" || right.PersonaID == "" || left.PersonaID == right.PersonaID
}

func dialogueSameThread(left, right RecalledGroupEvent) bool {
	return left.ThreadKey == "" || right.ThreadKey == "" || left.ThreadKey == right.ThreadKey
}

func dialogueReplyMatches(reply, original RecalledGroupEvent) bool {
	return reply.ReplyToMessageID != "" && (reply.ReplyToMessageID == original.MessageID || reply.ReplyToMessageID == original.ID)
}

func dialogueOtherMemberSince(events []RecalledGroupEvent, start, end int, current RecalledGroupEvent) bool {
	for _, event := range events[start:end] {
		if event.Role == "user" && dialogueSamePersona(event, current) && event.SenderRef != "" && event.SenderRef != current.SenderRef {
			return true
		}
	}
	return false
}
