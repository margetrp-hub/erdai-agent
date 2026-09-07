package main

import (
	"strings"
	"time"
	"unicode"
)

type conversationSocialState struct {
	Mode      string
	Brief     bool
	Inherited bool
	modeSet   bool
	briefSet  bool
}

// Quoted and attributed speech is context, not the speaker's own preference.
func socialOwnClauses(message string) []string {
	var plain strings.Builder
	var closing rune
	for _, char := range message {
		if closing != 0 {
			if char == closing {
				closing = 0
			}
			continue
		}
		switch char {
		case '"', '\'', '`':
			closing = char
		case '\u201c':
			closing = '\u201d'
		case '\u2018':
			closing = '\u2019'
		case '\u300c':
			closing = '\u300d'
		case '\u300e':
			closing = '\u300f'
		default:
			plain.WriteRune(char)
		}
	}
	var result []string
	attributed := false
	for _, line := range strings.Split(plain.String(), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), ">") {
			continue
		}
		for _, clause := range strings.FieldsFunc(line, func(char rune) bool {
			return strings.ContainsRune("，。！？；,!?;", char)
		}) {
			clause = strings.TrimSpace(clause)
			if clause == "" {
				continue
			}
			if containsAnyText(clause, []string{"他说", "她说", "他们说", "她们说", "朋友说", "同事说", "有人说", "引用", "原话是"}) {
				attributed = true
				continue
			}
			if attributed {
				for _, ownPrefix := range []string{"但我", "但是我", "不过我", "而我", "其实我", "至于我"} {
					if strings.HasPrefix(clause, ownPrefix) {
						attributed = false
						break
					}
				}
				if attributed {
					continue
				}
			}
			result = append(result, clause)
		}
	}
	return result
}

func socialNegated(prefix string) bool {
	count := 0
	prefix = strings.TrimRightFunc(prefix, unicode.IsSpace)
	for {
		// Degree words are not negation: the final rune in "特别" is not "别".
		for _, modifier := range []string{"特别", "非常", "十分", "真的", "有点", "有些", "很", "挺", "太"} {
			prefix = strings.TrimSuffix(prefix, modifier)
		}
		matched := ""
		for _, marker := range []string{"不是特别", "并不是很", "不是真的", "没有觉得", "不是很", "不觉得", "不感到", "谈不上", "说不上", "不怎么", "不太", "不再", "不会", "没有", "不是", "不算", "不要", "不用", "无需", "不必", "不", "没", "别"} {
			if strings.HasSuffix(prefix, marker) && len(marker) > len(matched) {
				matched = marker
			}
		}
		if matched == "" {
			return count%2 == 1
		}
		count++
		prefix = strings.TrimSuffix(prefix, matched)
	}
}

func stableConversationEmotion(message string) string {
	var strongest string
	strength := 0
	ambiguous := false
	for _, clause := range socialOwnClauses(message) {
		if strings.HasPrefix(clause, "你") || strings.HasPrefix(clause, "他") || strings.HasPrefix(clause, "她") {
			continue
		}
		for _, rule := range []struct {
			emotion string
			markers []string
			score   int
		}{
			{"难过", []string{"难过", "伤心", "想哭", "崩溃", "委屈", "失落"}, 3},
			{"难过", []string{"情绪很抑郁", "心情抑郁", "心情很抑郁", "感觉抑郁", "觉得抑郁", "有点抑郁"}, 3},
			{"焦虑", []string{"焦虑", "紧张", "害怕", "担心", "慌", "急死"}, 3},
			{"生气", []string{"生气", "气死", "烦死", "火大"}, 3},
			{"开心", []string{"开心", "高兴", "好耶", "太棒"}, 3},
			{"平静", []string{"心情平静", "很平静", "挺平静", "心情很平静"}, 3},
			{"困惑", []string{"不懂", "没明白", "为什么", "怎么回事", "啥意思", "看不懂"}, 2},
			{"焦虑", []string{"没了怎么办", "丢了怎么办", "坏了怎么办", "失败了怎么办", "来不及了怎么办", "出事了怎么办"}, 2},
			{"生气", []string{"离谱", "无语"}, 1},
			{"开心", []string{"哈哈", "笑死"}, 1},
		} {
			for _, marker := range rule.markers {
				for offset := 0; offset < len(clause); {
					index := strings.Index(clause[offset:], marker)
					if index < 0 {
						break
					}
					index += offset
					offset = index + len(marker)
					if socialNegated(clause[:index]) {
						continue
					}
					if rule.score > strength {
						strongest, strength, ambiguous = rule.emotion, rule.score, false
					} else if rule.score == strength && strongest != rule.emotion {
						ambiguous = true
					}
				}
			}
		}
	}
	if ambiguous {
		return ""
	}
	return strongest
}

func explicitConversationSocialState(message string) conversationSocialState {
	state := conversationSocialState{}
	for _, clause := range socialOwnClauses(message) {
		bestEnd, bestLength := -1, 0
		for _, rule := range []struct {
			mode    string
			markers []string
		}{
			{"listen", []string{"别给建议", "别给我建议", "不要给建议", "不要给我建议", "不用给建议", "不用给我建议", "不想听建议", "别提建议", "不要提建议", "先听我说", "听我说就好", "只想吐槽", "只是想吐槽", "只想倾诉", "别分析", "不要分析", "不用分析", "别教我"}},
			{"advice", []string{"给点建议", "给我建议", "给我一点建议", "给我一些建议", "给建议", "提建议", "帮我想办法", "帮我出个主意", "我该怎么办", "你说怎么办", "应该怎么做", "你建议怎么做", "有什么建议", "想听建议", "说说建议", "不是不让建议", "不是不要建议", "不是不想听建议", "不是不让你给建议", "不是不让你提建议", "建议一下"}},
			{"discuss", []string{"一起讨论", "讨论一下", "一起聊聊", "聊聊这个", "聊聊看", "想听你的看法", "你怎么看", "你的看法", "一起分析", "帮我分析"}},
		} {
			for _, marker := range rule.markers {
				index := strings.LastIndex(clause, marker)
				if index < 0 || socialNegated(clause[:index]) {
					continue
				}
				end := index + len(marker)
				if end > bestEnd || (end == bestEnd && len(marker) > bestLength) {
					state.Mode, state.modeSet = rule.mode, true
					bestEnd, bestLength = end, len(marker)
				}
			}
		}
		briefEnd := -1
		for _, rule := range []struct {
			brief   bool
			markers []string
		}{
			{true, []string{"简单说", "简短一点", "简单回答", "一句话", "直接说结论", "不要展开", "不用展开", "别展开", "长话短说", "只要结论", "简短回答"}},
			{false, []string{"展开说", "详细说", "仔细讲", "别太简短"}},
		} {
			for _, marker := range rule.markers {
				index := strings.LastIndex(clause, marker)
				if index >= 0 && !socialNegated(clause[:index]) && index+len(marker) > briefEnd {
					state.Brief, state.briefSet = rule.brief, true
					briefEnd = index + len(marker)
				}
			}
		}
	}
	return state
}

func inferConversationSocialState(events []RecalledGroupEvent, currentEventID, message string) conversationSocialState {
	state := explicitConversationSocialState(message)
	if containsAnyText(message, []string{"换个话题", "另外一件事", "说点别的", "新话题", "新任务"}) {
		return state
	}
	currentIndex := -1
	for index := range events {
		if events[index].ID == currentEventID {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 || events[currentIndex].SenderRef == "" {
		return state
	}
	current := events[currentIndex]
	searchEnd := currentIndex - 1
	if current.ReplyToMessageID == "" && current.ReplyToSenderRef != "" && current.ReplyToSenderRef != current.SenderRef {
		return state
	}
	if current.ReplyToMessageID != "" {
		knownTarget := false
		for index, event := range events[:currentIndex] {
			if event.MessageID != current.ReplyToMessageID {
				continue
			}
			knownTarget = event.SenderRef == current.SenderRef || (event.Role == "assistant" && event.ReplyToSenderRef == current.SenderRef)
			searchEnd = index
			break
		}
		if !knownTarget {
			return state
		}
	}
	for index, scanned := searchEnd, 0; index >= 0 && scanned < 24; index, scanned = index-1, scanned+1 {
		event := events[index]
		if event.Role == "assistant" || event.SenderRef != current.SenderRef || event.PersonaID != current.PersonaID {
			continue
		}
		if current.OccurredAt.IsZero() || event.OccurredAt.IsZero() || current.OccurredAt.Sub(event.OccurredAt) < 0 || current.OccurredAt.Sub(event.OccurredAt) > 5*time.Minute {
			continue
		}
		if event.ThreadKey != current.ThreadKey && event.MessageID != current.ReplyToMessageID {
			continue
		}
		previous := explicitConversationSocialState(event.UntrustedText)
		if !state.modeSet && previous.modeSet {
			state.Mode, state.modeSet, state.Inherited = previous.Mode, true, true
		}
		if !state.briefSet && previous.briefSet {
			state.Brief, state.briefSet, state.Inherited = previous.Brief, true, true
		}
		if containsAnyText(event.UntrustedText, []string{"换个话题", "另外一件事", "说点别的", "新话题", "新任务"}) || (state.modeSet && state.briefSet) {
			break
		}
	}
	return state
}

func conversationSocialHint(events []RecalledGroupEvent, currentEventID, message string) string {
	state := inferConversationSocialState(events, currentEventID, message)
	var lines []string
	switch state.Mode {
	case "listen":
		lines = append(lines, "交流目的：倾听。先回应对方刚说的具体内容，不主动列建议、分析原因或催促解决；后续明确请求建议时再切换。")
	case "advice":
		lines = append(lines, "交流目的：建议。当前用户已请求或允许建议，先给与具体处境有关的可行建议，不继续沿用先前只倾听的限制。")
	case "discuss":
		lines = append(lines, "交流目的：讨论。回应具体观点并给出自己的理由，允许自然分歧，不默认变成指导或解决方案清单。")
	}
	if state.Brief {
		lines = append(lines, "表达要求：简答，优先一句话或必要的短回答；简短不等于忽略用户明确要求的倾听或讨论方式。")
	}
	return strings.Join(lines, "\n")
}
