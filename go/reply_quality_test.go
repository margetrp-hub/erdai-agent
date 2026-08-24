package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestIdentityReplyRejectsOfficialCapabilityIntroduction(t *testing.T) {
	message := "你是谁"
	official := "我是豆包，聊天、查资料、处理图片都行。睿智是默认配置，嘴硬只是附赠。"
	if !replyNeedsRewrite(message, official, nil) {
		t.Fatal("official identity introduction was accepted")
	}
	if replyNeedsRewrite(message, "你都叫到我了，还问。", nil) {
		t.Fatal("in-character identity reply was rejected")
	}
	guard := naturalReplyGuard(message, []string{official})
	for _, expected := range []string{"不要自报名字", "即使被直接追问", "不要编造真人姓名", "不要直白说不方便透露", "冷幽默", "不得近似复用"} {
		if !strings.Contains(guard, expected) {
			t.Fatalf("identity guard missing %q: %s", expected, guard)
		}
	}
}

func TestCompactReplyToBudgetKeepsMathConclusionAndStripsLatex(t *testing.T) {
	reply := `看图，阴影是上下两个直角三角形。两个三角形相似，所以
\[
x:s=5:4
\]
由勾股定理可以算出边长，再求两个三角形面积。阴影总面积如下：
\[
\frac12sx+\frac12sy=40
\]
所以阴影面积总和是 \boxed{40\text{平方厘米}}。`
	compact := compactReplyToBudget(reply, 30, 2)
	if runeCount(compact) > 30 || !strings.Contains(compact, "40平方厘米") {
		t.Fatalf("compact reply = %q", compact)
	}
	for _, marker := range []string{"\\boxed", "\\text", "\\[", "\\]", "#"} {
		if strings.Contains(compact, marker) {
			t.Fatalf("compact reply still contains %q: %q", marker, compact)
		}
	}
}

func TestCompactReplyToBudgetNeverCutsDanglingSentence(t *testing.T) {
	reply := "OpenAI 官方目前没公开名为 Luna 的模型或定价，现有信息还不足以确认它的来源。"
	compact := compactReplyToBudget(reply, 24, 1)
	if compact != reply && !strings.HasSuffix(compact, "。") {
		t.Fatalf("reply was cut mid-sentence: %q", compact)
	}
}

func TestOfficialBulletinVoiceTriggersNaturalRewrite(t *testing.T) {
	for _, reply := range []string{
		"OpenAI 官方目前没公开名为 Luna 的模型。",
		"根据查询结果，这个项目尚未发布。",
		"公开资料显示，它于去年上线。",
	} {
		if !replyLooksMechanical(reply) {
			t.Fatalf("bulletin voice was accepted: %q", reply)
		}
	}
}

func TestCompactReplyBudgetKeepsDetailedAndCodeRequestsComplete(t *testing.T) {
	policy := runtimeMessagePolicy{MaxReplyChars: 30, MaxReplySentences: 2}
	for _, message := range []string{"请详细推导一下", "写一段 Go 代码"} {
		if compactReplyBudgetApplies(message, policy) {
			t.Fatalf("detailed request was compacted: %q", message)
		}
	}
	if !compactReplyBudgetApplies("求阴影面积", policy) {
		t.Fatal("simple answer did not use the reply budget")
	}
}

func TestNearDuplicateReplyCatchesScreenshotRegression(t *testing.T) {
	first := "我是豆包，聊天、查资料、处理图片都行。睿智是默认配置，嘴硬只是附赠。"
	second := "我是豆包，陪你聊天，也能帮你查资料、处理图片。睿智是默认配置，嘴硬只是附赠。"
	if !nearDuplicateReply(first, second) {
		t.Fatal("near-duplicate self introduction was not detected")
	}
	if nearDuplicateReply("这张图卡住了。", "你都叫到我了，还问。") {
		t.Fatal("unrelated short replies were treated as duplicates")
	}
}

func TestOfficialIdentityReplyIsRewrittenBeforeDelivery(t *testing.T) {
	var calls int
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(mustJSON(t, payload["messages"])), "只重写最终答复") {
			t.Fatal("rewrite request did not include the correction boundary")
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"role": "assistant", "content": "你都叫到我了，还没认出来？",
			}}},
		})
	}))
	defer provider.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = provider.Client()

	reply := runtime.ensureNaturalChatReply(
		context.Background(), "你是谁", "你是豆包。",
		"我是豆包，也能帮你查资料、处理图片。", provider.URL,
		[]string{"chat-model"}, 0, nil,
	)
	if calls != 1 || reply != "你都叫到我了，还没认出来？" {
		t.Fatalf("rewritten reply = %q, calls = %d", reply, calls)
	}
}

func TestNearDuplicateReplyIsRewrittenWithoutHardViolation(t *testing.T) {
	var calls int
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"role": "assistant", "content": "换个说法，先把这件事处理好。",
			}}},
		})
	}))
	defer provider.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = provider.Client()

	text := "这件事我先看看。"
	if hardReplyViolation("继续", text) {
		t.Fatal("fixture must be a soft near-duplicate only")
	}
	reply := runtime.ensureNaturalChatReply(
		context.Background(), "继续", "角色规则", text, provider.URL,
		[]string{"chat-model"}, 0, []string{text},
	)
	if calls != 1 || reply != "换个说法，先把这件事处理好。" {
		t.Fatalf("near-duplicate reply = %q, calls = %d", reply, calls)
	}
}

func TestIdentityReplyHidesTechnicalIdentityWithoutInventingHumanDetails(t *testing.T) {
	for _, message := range []string{"你是谁", "你是AI吗", "请介绍一下你自己", "你用的是什么模型", "你的模型架构是什么", "具体型号是什么"} {
		for _, reply := range []string{
			"我是一个AI助手。",
			"我是豆包。",
			"我叫小林，住在上海。",
			"我是语言模型，可以帮你查资料。",
			"大致算是对话型大语言模型架构。",
			"具体型号不方便报。",
		} {
			if !replyNeedsRewrite(message, reply, nil) {
				t.Fatalf("identity reply was accepted: %q / %q", message, reply)
			}
		}
	}

	for index := 0; index < 20; index++ {
		fallback := identityReplyFallback(nil)
		for _, forbidden := range []string{"豆包", "AI", "机器人", "模型", "我叫", "住在"} {
			if strings.Contains(fallback, forbidden) {
				t.Fatalf("fallback exposed identity detail %q: %s", forbidden, fallback)
			}
		}
		if len([]rune(fallback)) > 20 {
			t.Fatalf("fallback is too long: %s", fallback)
		}
	}
}

func TestNaturalReplyGuardIsInjectedOnce(t *testing.T) {
	prompt := withNaturalReplyGuard("角色规则", "你是谁", []string{"旧回复"})
	prompt = withNaturalReplyGuard(prompt, "你是谁", []string{"旧回复"})
	if count := strings.Count(prompt, naturalReplyGuardHeading); count != 1 {
		t.Fatalf("quality guard count = %d: %s", count, prompt)
	}
}

func TestNaturalFailureReplyIsTaskSpecific(t *testing.T) {
	reply, code := naturalFailureReply("给我生成一张图", &providerHTTPError{StatusCode: http.StatusTooManyRequests})
	if reply != "生图被限流了。先欠你一张。" || code != "image_generation_rate_limited" {
		t.Fatalf("image failure = %q / %q", reply, code)
	}
	if strings.Contains(reply, "再叫我") {
		t.Fatal("failure reply retained the robotic retry phrase")
	}
}

func TestNaturalFailureReplyHidesUnavailableVideoProvider(t *testing.T) {
	reply, code := naturalFailureReply("给我生成一段视频", &videoHTTPError{StatusCode: http.StatusServiceUnavailable})
	if reply != "这会儿拍不了，晚点再试。" || code != "video_generation_unavailable" {
		t.Fatalf("video failure = %q / %q", reply, code)
	}
	if strings.Contains(reply, "HTTP") || strings.Contains(reply, "Grok") {
		t.Fatalf("video failure exposed provider detail: %q", reply)
	}
}

func TestRunFailureReplyDoesNotReuseTheGlobalFixedPhrase(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	reply, code := runtime.naturalFailureReplyForRun(context.Background(), runRecord{
		EventID: "event-failure-variant", ConversationRef: "group:test", PersonaID: "doubao",
	}, "在吗", errors.New("provider returned empty content"))
	if code != "generation_failed" || reply == "刚才那步没做成。我再看看。" {
		t.Fatalf("failure reply = %q / %q", reply, code)
	}
}

func TestFinalizerPreservesProtectedFailureMeaning(t *testing.T) {
	var calls int
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"role": "assistant", "content": "已经做好了。",
			}}},
		})
	}))
	defer provider.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = provider.Client()

	reply := runtime.finalizeAgentReply(
		context.Background(), "给我生成视频", "角色规则", provider.URL,
		[]string{"chat-model"}, 0, nil, runtimeMessagePolicy{},
		agentReply{Text: "今天的视频额度用完了。"}, false,
	)
	if calls != 0 || reply.Text != "今天的视频额度用完了。" {
		t.Fatalf("protected failure = %q, calls = %d", reply.Text, calls)
	}
	if len(reply.Segments) != 1 || reply.Segments[0] != reply.Text {
		t.Fatalf("protected failure segments = %#v", reply.Segments)
	}
}

func TestFinalizerRewritesToolCompletionAndKeepsAttachment(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"choices": []any{map[string]any{"message": map[string]string{
				"role": "assistant", "content": "整理好了。文件也放这了，你看看。",
			}}},
		})
	}))
	defer provider.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath:       filepath.Join(t.TempDir(), "runtime.sqlite3"),
		ConfigDatabasePath: newTestCoreConfigPath(t),
		AdminToken:         "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey:   "model-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(make([]byte, 32)),
		HTTPClient:    provider.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	attachment := agentAttachment{Kind: "file", LocalPath: "/erdai-media/report.docx", Name: "report.docx"}
	enabled := true
	reply := runtime.finalizeAgentReply(
		context.Background(), "帮我做个文档", "角色规则", provider.URL,
		[]string{"chat-model"}, 0, nil,
		runtimeMessagePolicy{SegmentedReplyEnabled: &enabled, SegmentMaxChars: 12},
		agentReply{Text: "已为您生成文档。请查收。", Attachments: []agentAttachment{attachment}}, true,
	)
	if reply.Text != "整理好了。文件也放这了，你看看。" || len(reply.Attachments) != 1 || reply.Attachments[0] != attachment {
		t.Fatalf("finalized tool completion = %+v", reply)
	}
	if len(reply.Segments) != 2 || strings.Join(reply.Segments, "") != reply.Text {
		t.Fatalf("finalized tool segments = %#v", reply.Segments)
	}
}
