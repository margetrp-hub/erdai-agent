package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHumanTypingDelayScalesAndClamps(t *testing.T) {
	policy := runtimeMessagePolicy{}
	short := humanTypingDelayMillis("在吗", "在。", policy)
	if short < 700 || short > 2200 {
		t.Fatalf("short reply delay = %dms", short)
	}
	long := humanTypingDelayMillis(
		"帮我看看这段代码为什么会崩,我已经试了三种办法都不行,真的要疯了",
		"先别乱改。把完整报错和你最后一次改动发来,我从现象开始拆,别自己瞎猜原因。",
		policy,
	)
	if long <= short {
		t.Fatalf("long reply not slower: long=%d short=%d", long, short)
	}
	if long > 5000 {
		t.Fatalf("delay exceeds default cap: %d", long)
	}
	disabled := false
	if got := humanTypingDelayMillis("在吗", "在。", runtimeMessagePolicy{HumanPacingEnabled: &disabled}); got != 0 {
		t.Fatalf("disabled pacing still delayed: %d", got)
	}
	if got := humanTypingDelayMillis("在吗", "", policy); got != 0 {
		t.Fatalf("empty reply must not delay: %d", got)
	}
	capped := runtimeMessagePolicy{HumanPacingMaxSeconds: 2}
	if got := humanTypingDelayMillis(strings.Repeat("问", 200), strings.Repeat("答", 200), capped); got > 2400 {
		t.Fatalf("configured cap not applied: %d", got)
	}
}

func TestFinalizeAgentReplySetsTypingDelayOnlyForTextReplies(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	policy := runtimeMessagePolicy{MaxReplyChars: 100, MaxReplySentences: 5}
	textReply := runtime.finalizeAgentReplyKey(
		context.Background(), "今晚吃什么好", "", "", "", nil, 0, nil, policy,
		agentReply{Text: "随便你,反正你最后都会点烧烤。"}, false,
	)
	if textReply.TypingDelayMS <= 0 {
		t.Fatalf("text reply got no typing delay: %+v", textReply)
	}
	mediaReply := runtime.finalizeAgentReplyKey(
		context.Background(), "画一张图", "", "", "", nil, 0, nil, policy,
		agentReply{Text: "弄好了。", Attachments: []agentAttachment{{Kind: "image", LocalPath: "/media/x.png"}}}, false,
	)
	if mediaReply.TypingDelayMS != 0 {
		t.Fatalf("media reply must not be delayed: %+v", mediaReply)
	}
}

func TestEnqueueDeliveryAppliesTypingDelay(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertHonestyTestRun(t, runtime, "typed-run", "group-typed", "sender-a", "group", "running", time.Now().UTC())
	reply := agentReply{Text: "这句要等一下再发。", TypingDelayMS: 2500}
	if err := runtime.enqueueDelivery(run, reply, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	var nextAttempt any
	if err := runtime.db.QueryRow(`SELECT next_attempt_at FROM agent_deliveries
		WHERE run_id = 'typed-run' AND phase = 'terminal'`).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	if nextAttempt == nil {
		t.Fatal("typing delay did not schedule next_attempt_at")
	}
	// A failure notice never waits, even if the reply carries a delay.
	failed := insertHonestyTestRun(t, runtime, "failed-run", "group-typed", "sender-b", "group", "running", time.Now().UTC())
	if err := runtime.enqueueDelivery(failed, agentReply{Text: "这次没成。", TypingDelayMS: 2500}, "terminal", "generation_failed"); err != nil {
		t.Fatal(err)
	}
	if err := runtime.db.QueryRow(`SELECT next_attempt_at FROM agent_deliveries
		WHERE run_id = 'failed-run' AND phase = 'terminal'`).Scan(&nextAttempt); err != nil {
		t.Fatal(err)
	}
	if nextAttempt != nil {
		t.Fatalf("failure notice was delayed: %v", nextAttempt)
	}
}

func TestBotMoodCueDetectionAndStore(t *testing.T) {
	if detectInboundBotMood("豆包你真棒,谢谢") != botMoodCheerful {
		t.Fatal("praise cue missed")
	}
	if detectInboundBotMood("豆包你是不是傻") != botMoodTeased {
		t.Fatal("tease cue missed")
	}
	if detectInboundBotMood("今晚吃什么") != botMoodNeutral {
		t.Fatal("neutral message produced a mood")
	}
	if detectInboundBotMood(strings.Repeat("夸你厉害", 30)) != botMoodNeutral {
		t.Fatal("long message must not set mood")
	}

	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	if err := runtime.memory.ObserveBotMoodCue(ctx, "doubao|group-mood", botMoodCheerful); err != nil {
		t.Fatal(err)
	}
	if got := runtime.memory.BotMood(ctx, "doubao|group-mood"); got != botMoodCheerful {
		t.Fatalf("stored mood = %q", got)
	}
	if got := runtime.memory.BotMood(ctx, "doubao|other-group"); got != "" {
		t.Fatalf("mood leaked across conversations: %q", got)
	}
	// Expired mood decays to neutral.
	if _, err := runtime.db.Exec(`UPDATE conversation_state SET bot_mood_updated_at = ?`,
		time.Now().UTC().Add(-2*time.Hour).Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got := runtime.memory.BotMood(ctx, "doubao|group-mood"); got != "" {
		t.Fatalf("expired mood still returned: %q", got)
	}
}

func TestFailureDeflatesMoodFilter(t *testing.T) {
	if failureDeflatesMood("") || failureDeflatesMood("coalesced_by_newer_dialogue") ||
		failureDeflatesMood("superseded_by_newer_dialogue") || failureDeflatesMood("stale_terminal_discarded") {
		t.Fatal("scheduling outcomes must not deflate mood")
	}
	if !failureDeflatesMood("generation_failed") || !failureDeflatesMood("video_generation_failed") {
		t.Fatal("real failures must deflate mood")
	}
}

func TestTimeOfDayLabelBuckets(t *testing.T) {
	cases := map[int]string{3: "深夜", 6: "清晨", 10: "上午", 13: "中午", 16: "下午", 20: "晚上", 23: "深夜"}
	for hour, want := range cases {
		moment := time.Date(2026, 8, 27, hour, 30, 0, 0, time.Local)
		if got := timeOfDayLabel(moment); got != want {
			t.Fatalf("hour %d = %q, want %q", hour, got, want)
		}
	}
}

func TestCompileDynamicMoodLine(t *testing.T) {
	if compileDynamicMoodLine("", "") != "" {
		t.Fatal("empty inputs must produce no line")
	}
	line := compileDynamicMoodLine(botMoodTeased, "深夜")
	if !strings.Contains(line, "深夜") || !strings.Contains(line, "不服气") {
		t.Fatalf("mood line = %q", line)
	}
	if !strings.Contains(compileNativeSystemPrompt(
		nativeRuntimeConfig{MaxReplySentences: 2, MaxReplyChars: 40}, contentBoundaryPolicy{}, nil, nil, nil, nil, nil, nil,
		"", "", "", line,
	), "深夜") {
		t.Fatal("mood line did not reach the compiled prompt")
	}
}
