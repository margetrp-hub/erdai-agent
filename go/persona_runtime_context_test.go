package main

import (
	"context"
	"strings"
	"testing"
)

func TestStableMemoryExtractionIsConservative(t *testing.T) {
	values := extractStableMemories("以后叫我老王，我喜欢茉莉花茶。")
	if len(values) != 2 {
		t.Fatalf("stable memories = %+v", values)
	}
	kinds := map[string]bool{}
	for _, value := range values {
		kinds[value.Kind] = true
	}
	if !kinds["address"] || !kinds["preference"] {
		t.Fatalf("stable memory kinds = %+v", values)
	}
	if values := extractStableMemories("可能明天会喝茶吧"); len(values) != 0 {
		t.Fatalf("temporary statement was captured: %+v", values)
	}
	values = extractStableMemories("我平时喝无糖美式，我最近在做群聊整理")
	if len(values) != 2 {
		t.Fatalf("natural stable statements were not captured: %+v", values)
	}
}

func TestConversationEmotionUsesMessageEvidence(t *testing.T) {
	for message, expected := range map[string]string{
		"今天真的很难过": "难过",
		"这到底怎么回事": "困惑",
		"好耶，终于好了": "开心",
		"正常问个问题":  "平静",
	} {
		if actual := detectConversationEmotion(message); actual != expected {
			t.Fatalf("emotion for %q = %q, want %q", message, actual, expected)
		}
	}
}

func TestImaginaryMemoryContextIsNotPromotedToFact(t *testing.T) {
	for _, message := range []string{
		"我梦到自己喜欢住在海边",
		"假如我平时喝黑咖啡呢",
		"角色扮演里我叫小满",
	} {
		if !imaginaryMemoryContext(message) {
			t.Fatalf("imaginary context was not isolated: %s", message)
		}
	}
	if imaginaryMemoryContext("我平时喝无糖拿铁") {
		t.Fatal("ordinary stable preference was treated as imaginary")
	}
}

func TestRelationshipPulsePromptUsesQualitativeBoundaries(t *testing.T) {
	prompt := relationshipPulsePrompt(RelationshipPulse{
		Ready: true, MemoryResonance: 72, RoutineExpectation: 68,
		Longing: 55, Sharing: 64, OutputReflow: 20, PreferredHour: 21,
	})
	for _, expected := range []string{"自然接住", "不要声称监控作息", "不催促", "降低追问密度"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("pulse prompt %q misses %q", prompt, expected)
		}
	}
	if strings.Contains(prompt, "72") || strings.Contains(prompt, "55") {
		t.Fatalf("pulse prompt leaked internal scores: %s", prompt)
	}
}

func TestMemoryPulsePolicyFieldsAreConsumed(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	_, err := runtime.configStore.db.Exec(`UPDATE integration_settings SET config_json = ? WHERE id = 'memory_policy'`, `{
		"enabled":true,"autoCapture":true,"retrievalLimit":7,"maxMemoriesPerScope":900,
		"allowGroupSharedMemory":false,"relationshipPulseEnabled":false,
		"outputFeedbackEnabled":false,"memoryResonanceEnabled":false,
		"circadianAwarenessEnabled":false,"longingEnabled":false,
		"dreamMemoryIsolation":false,"pulseMinInteractions":9,
		"rhythmWindowEvents":44,"timezoneOffsetMinutes":60
	}`)
	if err != nil {
		t.Fatal(err)
	}
	policy := runtime.memoryPolicy(context.Background())
	if policy.RelationshipPulseEnabled || policy.OutputFeedbackEnabled || policy.DreamMemoryIsolation {
		t.Fatalf("disabled memory pulse fields were ignored: %+v", policy)
	}
	if policy.PulseMinInteractions != 9 || policy.RhythmWindowEvents != 44 || policy.TimezoneOffsetMinutes != 60 {
		t.Fatalf("memory pulse numeric fields were ignored: %+v", policy)
	}
}
