package main

import (
	"strings"
	"testing"
	"time"
)

func TestDialogueSocialEmotionEvidenceIsStable(t *testing.T) {
	for _, test := range []struct{ message, emotion string }{
		{"我不难过", ""}, {"我没有觉得难过", ""}, {"我不是很难过", ""},
		{"哈哈其实挺难过", "难过"}, {"难过，但是哈哈", "难过"},
		{"我又开心又难过", ""}, {"我不是不难过", "难过"},
		{"他说他很难过", ""}, {"她说：\"我很难过\"", ""},
		{"他说昨天下雨，很难过", ""},
		{"他说：我昨天下雨很难过，我今天也难过", ""},
		{"他说难过，但我挺开心", "开心"}, {"“哈哈”其实我挺难过", "难过"},
		{"> 我很难过\n我现在心情很平静", "平静"}, {"正常问个问题", ""},
		{"今天真的很难过", "难过"}, {"你很难过吗", ""},
		{"这到底怎么回事", "困惑"}, {"好耶，终于好了", "开心"},
		{"我现在特别想哭", "难过"}, {"今天特别开心", "开心"},
		{"我不是特别开心", ""}, {"我特别不难过", ""}, {"别难过", ""},
		{"最近情绪很抑郁", "难过"}, {"我不觉得抑郁", ""}, {"抑郁症是什么", ""},
		{"文件没了怎么办", "焦虑"}, {"早餐该怎么办", ""}, {"这个应该怎么办", ""},
	} {
		t.Run(test.message, func(t *testing.T) {
			for iteration := 0; iteration < 100; iteration++ {
				if got := detectConversationEmotion(test.message); got != test.emotion {
					t.Fatalf("emotion = %q, want %q", got, test.emotion)
				}
			}
		})
	}
}

func TestDialogueSocialExplicitModeAndQuotedSpeech(t *testing.T) {
	for _, test := range []struct {
		message, mode string
		brief         bool
	}{
		{"别给建议", "listen", false}, {"先听我说，别给建议", "listen", false}, {"别给我建议", "listen", false},
		{"不要给我建议", "listen", false}, {"能给我一点建议吗", "advice", false},
		{"不是不让建议", "advice", false}, {"不是不让你给建议", "advice", false},
		{"先别给建议，现在给我建议吧", "advice", false},
		{"给我建议，算了先听我说", "listen", false},
		{"他说别给建议", "", false}, {"朋友说：‘别给建议’", "", false},
		{"他说今天很烦，别给建议", "", false}, {"他说今天很烦，别给建议，但我想听建议", "advice", false},
		{"他说：我今天很烦，我只想吐槽", "", false},
		{"引用：别给建议", "", false}, {"> 别给建议\n你怎么看", "discuss", false},
		{"先听我说，简单回答", "listen", true}, {"一句话回答", "", true},
		{"别展开说", "", true}, {"简单说，还是详细说吧", "", false},
		{"我没有什么建议", "", false}, {"我不想听建议", "listen", false},
	} {
		t.Run(test.message, func(t *testing.T) {
			got := explicitConversationSocialState(test.message)
			if got.Mode != test.mode || got.Brief != test.brief {
				t.Fatalf("mode = %+v, want mode %q brief %v", got, test.mode, test.brief)
			}
		})
	}
}

func TestDialogueSocialModeContinuationIsScopedAndReleasable(t *testing.T) {
	now := time.Now().UTC()
	base := []RecalledGroupEvent{
		{ID: "one", MessageID: "m-one", SenderRef: "a", PersonaID: "persona", Role: "user", UntrustedText: "先听我说，别给建议", OccurredAt: now.Add(-time.Minute)},
		{ID: "other", MessageID: "m-other", SenderRef: "b", PersonaID: "persona", Role: "user", UntrustedText: "给我建议", OccurredAt: now.Add(-30 * time.Second)},
		{ID: "current", MessageID: "m-current", SenderRef: "a", PersonaID: "persona", Role: "user", UntrustedText: "今天又遇到了同样的事", OccurredAt: now},
	}
	for _, test := range []struct {
		name, mode string
		modify     func([]RecalledGroupEvent)
	}{
		{"same speaker continues", "listen", func(_ []RecalledGroupEvent) {}},
		{"new advice releases restriction", "advice", func(events []RecalledGroupEvent) { events[2].UntrustedText = "那你建议怎么做" }},
		{"other sender has own preference", "advice", func(events []RecalledGroupEvent) { events[2].SenderRef = "b" }},
		{"unknown sender", "", func(events []RecalledGroupEvent) { events[2].SenderRef = "" }},
		{"other persona", "", func(events []RecalledGroupEvent) { events[2].PersonaID = "other" }},
		{"new thread", "", func(events []RecalledGroupEvent) { events[2].ThreadKey = "different" }},
		{"reply to another user", "", func(events []RecalledGroupEvent) { events[2].ReplyToMessageID = "m-other" }},
		{"unknown reply target", "", func(events []RecalledGroupEvent) { events[2].ReplyToMessageID = "missing" }},
		{"reply to another sender without message id", "", func(events []RecalledGroupEvent) { events[2].ReplyToSenderRef = "b" }},
		{"old preference expires", "", func(events []RecalledGroupEvent) { events[0].OccurredAt = now.Add(-6 * time.Minute) }},
		{"new topic resets", "", func(events []RecalledGroupEvent) { events[2].UntrustedText = "换个话题，今天吃什么" }},
		{"quoted old preference not adopted", "", func(events []RecalledGroupEvent) { events[0].UntrustedText = "她说：‘别给建议’" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			events := append([]RecalledGroupEvent(nil), base...)
			test.modify(events)
			got := inferConversationSocialState(events, "current", events[2].UntrustedText)
			if got.Mode != test.mode {
				t.Fatalf("mode = %+v, want %q", got, test.mode)
			}
		})
	}
	if hint := conversationSocialHint(base, "current", "那你建议怎么做"); !strings.Contains(hint, "已请求或允许建议") || strings.Contains(hint, "交流目的：倾听") {
		t.Fatalf("new advice request retained listening restriction: %q", hint)
	}
	if got := conversationSocialHint(nil, "missing", "正常问个问题"); got != "" {
		t.Fatalf("unknown input got a hard social label: %q", got)
	}
	olderThread := append([]RecalledGroupEvent(nil), base...)
	olderThread[1].SenderRef = "a"
	olderThread[2].ReplyToMessageID = "m-one"
	if got := inferConversationSocialState(olderThread, "current", olderThread[2].UntrustedText); got.Mode != "listen" {
		t.Fatalf("explicit reply to an older message adopted later preference: %+v", got)
	}
}
