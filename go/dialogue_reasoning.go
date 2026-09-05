package main

import (
	"strings"
	"time"
	"unicode"
)

// dialogueReasoningState is a small, deterministic bridge between the event
// timeline and the generation model. It does not answer the user; it tells the
// model what the latest message is doing in the conversation.
type dialogueReasoningState struct {
	Action              string
	PreviousAssistant   string
	PendingQuestion     string
	RepeatedBurst       int
	SameSpeakerFollowup bool
}

func inferDialogueReasoningState(events []RecalledGroupEvent, currentEventID, message string) dialogueReasoningState {
	message = strings.TrimSpace(message)
	state := dialogueReasoningState{Action: "background_chat"}
	if message == "" {
		return state
	}
	currentIndex := len(events)
	for index := range events {
		if events[index].ID == currentEventID {
			currentIndex = index
			break
		}
	}
	if currentIndex <= 0 {
		state.Action = classifyDialogueAction(message, "", false)
		return state
	}
	current := RecalledGroupEvent{ID: currentEventID}
	if currentIndex < len(events) {
		current = events[currentIndex]
	}
	var previousUser RecalledGroupEvent
	for index := currentIndex - 1; index >= 0; index-- {
		event := events[index]
		if state.PreviousAssistant == "" && event.Role == "assistant" {
			state.PreviousAssistant = strings.TrimSpace(event.UntrustedText)
			if strings.ContainsAny(state.PreviousAssistant, "?？") {
				state.PendingQuestion = truncateRunes(state.PreviousAssistant, 180)
			}
		}
		if previousUser.ID == "" && event.Role != "assistant" && event.SenderRef == current.SenderRef {
			previousUser = event
		}
		if state.PreviousAssistant != "" && previousUser.ID != "" {
			break
		}
	}
	state.SameSpeakerFollowup = previousUser.ID != ""
	state.RepeatedBurst = repeatedSpeakerBurst(events, currentIndex, current.SenderRef, current.OccurredAt)
	state.Action = classifyDialogueAction(message, state.PreviousAssistant, state.PendingQuestion != "")
	if state.PendingQuestion != "" && state.Action == "answer_previous_question" {
		state.SameSpeakerFollowup = true
	}
	return state
}

func classifyDialogueAction(message, previousAssistant string, hasPendingQuestion bool) string {
	message = strings.TrimSpace(message)
	if looksLikeCorrection(message, previousAssistant) {
		return "correct_previous_reply"
	}
	if hasPendingQuestion && !looksLikeNewRequest(message) && !looksLikeDirectPing(message) {
		return "answer_previous_question"
	}
	if looksLikeDirectPing(message) {
		return "direct_ping"
	}
	if looksLikeNewRequest(message) {
		return "new_request"
	}
	if previousAssistant != "" && containsAnyText(message, []string{"继续", "然后", "那就", "所以", "刚才", "你说", "行", "好"}) {
		return "continue_previous_topic"
	}
	if isLowInformationReaction(message) {
		return "reaction"
	}
	return "background_chat"
}

func dialogueReasoningHint(events []RecalledGroupEvent, currentEventID, message string) string {
	state := inferDialogueReasoningState(events, currentEventID, message)
	if state.PreviousAssistant == "" && state.RepeatedBurst < 2 && state.Action == "background_chat" {
		return ""
	}
	var lines []string
	lines = append(lines, "互动动作："+state.Action)
	if state.PendingQuestion != "" {
		lines = append(lines, "上一条角色消息带有未完成的问题：\""+state.PendingQuestion+"\"")
	}
	if state.RepeatedBurst >= 2 {
		lines = append(lines, "同一成员在短时间内重复发送了相近消息；把它们视为同一轮催促，只处理一次，不连续刷屏")
	}
	if state.SameSpeakerFollowup {
		lines = append(lines, "当前消息来自上一轮互动的同一成员，优先承接上文，不要凭空换题")
	}
	switch state.Action {
	case "answer_previous_question":
		lines = append(lines, "先消费当前答案，再推进原任务；不要重复复述上一问")
	case "correct_previous_reply":
		lines = append(lines, "这是对上一轮的纠正；提取本次新增约束并覆盖旧假设，按新信息重算，不要只道歉、辩护或重复承诺；能验证时先执行再回答")
	case "direct_ping":
		lines = append(lines, "这是叫角色或催促，不等于新问题；简短回应当前状态，必要时接回最近未完成任务")
	case "reaction":
		lines = append(lines, "这更像反应或低信息消息；除非能补充当前话题，否则可以保持安静")
	case "background_chat":
		lines = append(lines, "当前没有明确任务；只在能补充关键内容时接话，不要把陈述改写成客服问答")
	}
	return strings.Join(lines, "\n")
}

func looksLikeCorrection(message, previousAssistant string) bool {
	if containsAnyText(message, []string{
		"不对", "不是这个意思", "你理解错", "我说的是", "不是让你", "重来", "都说了", "又错了", "根本没",
	}) {
		return true
	}
	if strings.TrimSpace(previousAssistant) == "" {
		return false
	}
	return containsAnyText(message, []string{
		"没用", "不好用", "还是不行", "还是没", "怎么还是", "怎么又", "没改", "没听懂", "听不懂", "没按", "跟之前一样", "没有变化",
	})
}

func repeatedSpeakerBurst(events []RecalledGroupEvent, currentIndex int, sender string, currentAt time.Time) int {
	count := 0
	for index := currentIndex; index >= 0 && count < 6; index-- {
		event := events[index]
		if event.Role == "assistant" {
			break
		}
		if !currentAt.IsZero() && !event.OccurredAt.IsZero() && currentAt.Sub(event.OccurredAt) > 90*time.Second {
			break
		}
		if event.SenderRef == sender && looksLikeDirectPing(event.UntrustedText) {
			count++
		}
	}
	return count
}

func looksLikeDirectPing(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || len([]rune(message)) > 24 {
		return false
	}
	if containsAnyText(message, []string{"在吗", "在不在", "在不", "有人吗", "回我", "怎么不理", "怎么不回", "包?", "包？", "豆包?", "豆包？"}) {
		return true
	}
	letters := 0
	for _, value := range message {
		if unicode.IsLetter(value) || unicode.IsNumber(value) {
			letters++
		}
	}
	return strings.Contains(message, "@") && letters <= 8
}

func isLowInformationReaction(message string) bool {
	message = strings.TrimSpace(message)
	if message == "" || len([]rune(message)) > 12 {
		return false
	}
	return containsAnyText(message, []string{"哈哈", "笑死", "好家伙", "绝了", "确实", "6", "666", "收到", "懂了", "行吧"})
}

func looksLikeNewRequest(message string) bool {
	return strings.ContainsAny(message, "?？") || containsAnyText(message, []string{
		"帮我", "帮忙", "查一下", "搜一下", "看看", "解释", "分析", "生成", "做一个", "怎么", "为什么", "能不能",
	})
}
