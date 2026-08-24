package main

import "testing"

func TestGuessingGameHintTurnsBinaryAnswerIntoNextQuestion(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "setup", Role: "user", UntrustedText: "你来提问关于这个人的问题，我只能回答是或者不是"},
		{ID: "question", Role: "assistant", UntrustedText: "行，那我先问：是真人吗？"},
		{ID: "answer", Role: "user", UntrustedText: "是的"},
	}
	hint := inferDialogueProtocolHint(events, "answer", "是的")
	if hint == "" {
		t.Fatal("binary answer did not produce a protocol hint")
	}
	for _, expected := range []string{"猜人游戏", "上一轮角色问的是", "肯定", "不要重复已经确认", "新的"} {
		if !containsAnyText(hint, []string{expected}) {
			t.Fatalf("hint missing %q: %s", expected, hint)
		}
	}
}

func TestDirectContinuationRequiresMessageRelation(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "question", Role: "assistant", UntrustedText: "你今天还去吗？"},
		{ID: "answer", Role: "user", UntrustedText: "不去了"},
	}
	if !clearlyContinuesRecentAssistant(events, "answer", "不去了") {
		t.Fatal("short answer to the previous question was not treated as a continuation")
	}
	events = []RecalledGroupEvent{
		{ID: "reply", Role: "assistant", UntrustedText: "这事先这样。"},
		{ID: "unrelated", Role: "user", UntrustedText: "今晚群里好热闹"},
	}
	if clearlyContinuesRecentAssistant(events, "unrelated", "今晚群里好热闹") {
		t.Fatal("unrelated group message inherited direct continuation ownership")
	}
}

func TestGuessingGameHintHandlesNaturalFragmentAnswer(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "setup", Role: "user", UntrustedText: "你问几个问题能猜出来"},
		{ID: "question", Role: "assistant", UntrustedText: "是真人吗？"},
		{ID: "answer", Role: "user", UntrustedText: "是真人"},
	}
	if hint := inferDialogueProtocolHint(events, "answer", "是真人"); hint == "" {
		t.Fatal("natural fragment answer did not produce a protocol hint")
	}
}

func TestGuessingGameHintDoesNotHijackOrdinaryChat(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "question", Role: "assistant", UntrustedText: "今天吃什么？"},
		{ID: "answer", Role: "user", UntrustedText: "是的"},
	}
	if hint := inferDialogueProtocolHint(events, "answer", "是的"); hint != "" {
		t.Fatalf("ordinary chat was treated as guessing game: %s", hint)
	}
}

func TestGuessingGameHintSkipsGenericContinuationQuestion(t *testing.T) {
	events := []RecalledGroupEvent{
		{ID: "setup", Role: "user", UntrustedText: "只能回答是或者不是，你来提问"},
		{ID: "question", Role: "assistant", UntrustedText: "是真人吗？"},
		{ID: "answer", Role: "user", UntrustedText: "是的"},
		{ID: "generic", Role: "assistant", UntrustedText: "嗯？继续说呗。"},
		{ID: "answer2", Role: "user", UntrustedText: "是真人"},
	}
	hint := inferDialogueProtocolHint(events, "answer2", "是真人")
	if hint == "" || !containsAnyText(hint, []string{"真人吗"}) {
		t.Fatalf("substantive question was not recovered: %s", hint)
	}
}
