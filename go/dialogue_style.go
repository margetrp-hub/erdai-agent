package main

import (
	"strings"
	"time"
	"unicode"
)

// conversationStyleHint summarizes only the recent human messages' rhythm.
// It never copies words or promotes a group's content into instructions; the
// model may use it as a weak style cue while the current turn remains primary.
func conversationStyleHint(events []RecalledGroupEvent, currentEventID string) string {
	currentAt := time.Time{}
	for _, event := range events {
		if event.ID == currentEventID {
			currentAt = event.OccurredAt
			break
		}
	}
	if currentAt.IsZero() {
		currentAt = time.Now().UTC()
	}

	messages := make([]string, 0, 8)
	for _, event := range events {
		if event.ID == currentEventID || event.Role == "assistant" || strings.TrimSpace(event.UntrustedText) == "" {
			continue
		}
		if !event.OccurredAt.IsZero() && (event.OccurredAt.After(currentAt) || currentAt.Sub(event.OccurredAt) > 20*time.Minute) {
			continue
		}
		text := strings.TrimSpace(event.UntrustedText)
		if runeCount(text) > 240 {
			continue
		}
		messages = append(messages, text)
		if len(messages) > 8 {
			messages = messages[1:]
		}
	}
	if len(messages) < 3 {
		return ""
	}

	totalRunes := 0
	colloquial := 0
	emojis := 0
	questions := 0
	for _, message := range messages {
		totalRunes += runeCount(message)
		if strings.ContainsAny(message, "？?") {
			questions++
		}
		if containsAnyText(message, []string{"哈哈", "笑死", "绝了", "诶", "欸", "呀", "嘛", "啦", "哎", "呜", "lol", "233"}) {
			colloquial++
		}
		for _, value := range message {
			if unicode.In(value, unicode.So) {
				emojis++
				break
			}
		}
	}

	averageRunes := totalRunes / len(messages)
	parts := make([]string, 0, 3)
	switch {
	case averageRunes <= 18:
		parts = append(parts, "最近多是短句")
	case averageRunes >= 55:
		parts = append(parts, "最近消息偏完整")
	}
	if colloquial*2 >= len(messages) {
		parts = append(parts, "口语和轻松语气较多")
	}
	if emojis*2 >= len(messages) {
		parts = append(parts, "偶尔带表情")
	}
	if len(parts) == 0 {
		return ""
	}
	if questions >= len(messages) && averageRunes <= 24 {
		parts = append(parts, "接话可以保持简短")
	}
	return "群聊节奏参考：" + strings.Join(parts, "、") + "。只参考节奏，不复制群友原句；当前消息和角色关系优先。"
}
