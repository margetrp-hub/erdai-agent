package main

import "strings"

// inferDialogueProtocolHint turns a small, recognizable conversation protocol
// into an explicit action hint. The model still writes the reply, but it no
// longer has to rediscover that a short "是的" answers its previous question.
func inferDialogueProtocolHint(events []RecalledGroupEvent, currentEventID, message string) string {
	message = strings.TrimSpace(message)
	if message == "" || strings.ContainsAny(message, "?？") {
		return ""
	}
	currentIndex := len(events)
	for index := range events {
		if events[index].ID == currentEventID {
			currentIndex = index
			break
		}
	}
	if currentIndex <= 0 {
		return ""
	}
	windowStart := currentIndex - 12
	if windowStart < 0 {
		windowStart = 0
	}
	window := events[windowStart:currentIndex]
	if !guessingGameContext(window) {
		return ""
	}
	question := ""
	for index := len(window) - 1; index >= 0; index-- {
		candidate := strings.TrimSpace(window[index].UntrustedText)
		if window[index].Role == "assistant" && isSubstantiveGuessingQuestion(candidate) {
			question = truncateRunes(candidate, 120)
			break
		}
	}
	if question == "" || !looksLikeBinaryAnswer(message, question) {
		return ""
	}
	answer := "补充了线索"
	if containsAnyText(message, []string{"不是", "不对", "否", "没有", "并非"}) {
		answer = "否定"
	} else if containsAnyText(message, []string{"是的", "是", "对的", "对", "没错", "确实"}) {
		answer = "肯定"
	}
	return "当前在玩只能用是/不是回答的猜人游戏。上一轮角色问的是：\"" + question + "\"。当前消息是对上一问的" + answer + "，不要重复已经确认的判断，也不要说‘继续说呗’或只问‘下一个问题？’；直接提出一个新的、能继续缩小范围的判断问题。"
}

func clearlyContinuesRecentAssistant(events []RecalledGroupEvent, currentEventID, message string) bool {
	message = strings.TrimSpace(message)
	if message == "" {
		return false
	}
	if inferDialogueProtocolHint(events, currentEventID, message) != "" {
		return true
	}
	currentIndex := len(events)
	for index := range events {
		if events[index].ID == currentEventID {
			currentIndex = index
			break
		}
	}
	for index := currentIndex - 1; index >= 0; index-- {
		if events[index].Role != "assistant" {
			continue
		}
		previous := strings.TrimSpace(events[index].UntrustedText)
		if previous == "" {
			return false
		}
		if strings.ContainsAny(previous, "?？") {
			if len([]rune(message)) <= 24 || strings.ContainsAny(message, "?？") {
				return true
			}
		}
		return len([]rune(message)) <= 40 && containsAnyText(message, []string{
			"继续", "然后", "那就", "所以", "但是", "可是", "刚才", "你说",
			"为什么", "怎么", "啥意思", "不对", "没错", "是的", "不是",
		})
	}
	return false
}

func guessingGameContext(events []RecalledGroupEvent) bool {
	for _, event := range events {
		text := strings.ToLower(strings.TrimSpace(event.UntrustedText))
		if containsAnyText(text, []string{
			"只能回答是或者不是", "只能回答是或不是", "只能说是或不是",
			"你来提问", "你提问", "问关于这个人", "猜出来", "猜出这个人",
			"猜猜这个人", "猜一下这个人", "几个问题能猜",
		}) {
			return true
		}
	}
	return false
}

func isSubstantiveGuessingQuestion(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || !strings.ContainsAny(text, "?？") {
		return false
	}
	return !containsAnyText(text, []string{"继续说", "继续猜", "下一个问题", "告诉我", "怎么了"})
}

func looksLikeBinaryAnswer(message, question string) bool {
	message = strings.TrimSpace(message)
	if len([]rune(message)) > 24 {
		return false
	}
	if containsAnyText(message, []string{"是的", "不是", "不对", "没错", "确实", "没有", "并非", "对的"}) {
		return true
	}
	if len([]rune(message)) <= 8 && containsAnyText(message, []string{"是", "否", "对", "不"}) {
		return true
	}
	// Some group members answer in a natural fragment such as “是真人”。
	questionWords := []rune(strings.TrimSpace(question))
	for _, marker := range []string{"真人", "男生", "女生", "成年人", "中国人", "认识"} {
		if strings.Contains(question, marker) && strings.Contains(message, marker) && len(questionWords) <= 80 {
			return containsAnyText(message, []string{"是", "不是", "对", "不"})
		}
	}
	return false
}
