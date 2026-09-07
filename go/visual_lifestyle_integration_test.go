package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVisualPlanLifestyleDefaultCompositionAndVariation(t *testing.T) {
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, shanghaiTime)
	scenes, outfits := map[string]bool{}, map[string]bool{}
	for seed := uint64(0); seed < 100; seed++ {
		values := allocateVisualVariables("来张自拍", now, seed, defaultImageVisualDirectorPolicy(), "short", nil)
		if !strings.Contains(values["outfit"], "膝上") || values["activity"] == "" || values["light"] == "" {
			t.Fatalf("missing everyday outfit/activity/light: %+v", values)
		}
		if videoHasAny(values["camera"], "全身", "穿搭", "朋友") || videoHasAny(values["scene"], "展览", "舞台", "商场", "工作室") {
			t.Fatalf("unsolicited staged portrait: %+v", values)
		}
		scenes[values["scene"]], outfits[values["outfit"]] = true, true
	}
	if len(scenes) < 4 || len(outfits) < 3 {
		t.Fatalf("one fixed scene or uniform: %d scenes / %d outfits", len(scenes), len(outfits))
	}
}

func TestVisualPlanLifestyleExplicitChoicesAndUserLocation(t *testing.T) {
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, shanghaiTime)
	policy := defaultImageVisualDirectorPolicy()
	plain := allocateVisualVariables("来张自拍", now, 19, policy, "short", nil)
	for _, request := range []string{"我在办公室摸鱼，来张自拍", "我刚到办公室想看你的自拍", "我在办公室你在干嘛，给我自拍"} {
		user := allocateVisualVariables(request, now, 19, policy, "short", nil)
		if visualSceneSpecified(request) || !reflect.DeepEqual(plain, user) {
			t.Fatalf("user location became a persona scene for %q: %+v vs %+v", request, plain, user)
		}
	}
	explicit := allocateVisualVariables("你在海边穿红色长裙拍全身自拍", now, 19, policy, "short", nil)
	if explicit["primaryColor"] != "红色" || explicit["camera"] != "全身穿搭照" ||
		!strings.Contains(explicit["scene"], "用户明确") || !strings.Contains(explicit["outfit"], "用户明确") {
		t.Fatalf("explicit request lost to everyday defaults: %+v", explicit)
	}
	if videoHasAny(explicit["action"]+explicit["activity"], "餐具", "托腮", "书页") {
		t.Fatalf("unrelated domestic action imposed on the beach: %+v", explicit)
	}
	policy.SelfieTypes = []string{"全身生活照", "朋友视角抓拍"}
	custom := allocateVisualVariables("来张自拍", now, 19, policy, "long", nil)
	if custom["camera"] != "全身生活照" && custom["camera"] != "朋友视角抓拍" {
		t.Fatalf("custom camera policy overwritten: %+v", custom)
	}
	policy.Enabled = false
	if values := allocateVisualVariables("来张自拍", now, 19, policy, "short", nil); len(values) != 0 {
		t.Fatalf("disabled director still allocated variables: %+v", values)
	}
}

func TestVisualPlanLifestyleExplicitPostureDoesNotMixActivities(t *testing.T) {
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, shanghaiTime)
	for _, prompt := range []string{"坐着给我拍段自拍视频", "站着拍全身自拍", "躺着拍自拍", "走着拍自拍视频"} {
		for seed := uint64(0); seed < 100; seed++ {
			values := allocateVisualVariables(prompt, now, seed, defaultImageVisualDirectorPolicy(), "short", nil)
			if videoHasAny(values["scene"]+values["activity"], "散步", "沙发", "坐下来", "翻书", "收拾餐具", "厨房", "托腮") ||
				!strings.Contains(values["action"], "用户明确动作") {
				t.Fatalf("explicit posture conflicts with random context: %s %+v", prompt, values)
			}
		}
	}
}

func TestVisualPlanLifestyleTightBudgetKeepsLibraryLength(t *testing.T) {
	for _, length := range []string{"short", "long"} {
		plan := visualGenerationPlan{MediaType: "video", AppearanceID: "selected", Identity: "固定成年角色", UserPrompt: "自拍视频", OutfitLength: length}
		base, err := compileVisualGenerationPrompt(plan, "")
		if err != nil {
			t.Fatal(err)
		}
		mandatoryBytes := strings.Index(base, "\n用户未指定长度时")
		if mandatoryBytes < 0 {
			t.Fatal("library length section missing")
		}
		plan.UserPrompt += strings.Repeat("x", maxVideoPromptBytes-300-mandatoryBytes)
		prompt, err := compileVisualGenerationPrompt(plan, "")
		want := "用户未指定长度时外观库默认服装长度=" + length + "；明确长度要求优先。"
		if err != nil || len(prompt) > maxVideoPromptBytes || !strings.Contains(prompt, want) {
			t.Fatalf("length lost to lifestyle description: %s / %v", length, err)
		}
	}
}

func TestVisualPlanLifestyleContinuesDeliveredPlanAndFreezesReplay(t *testing.T) {
	a := newIdleRuntime(t)
	defer a.Close()
	addVisualPlanReference(t, a, "doubao")
	ctx := context.Background()
	run := visualPlanTestRun(t, a, "current-life")
	snapshot, err := a.resolveVisualAppearance(ctx, run, "同一场景接着拍自拍视频", "video")
	if err != nil {
		t.Fatal(err)
	}
	prior := visualGenerationPlan{OperationID: "prior-life-operation", MediaType: "image", Attempt: 1,
		AppearanceID: snapshot.AppearanceID, AppearanceRevision: snapshot.Revision, BindingRevision: snapshot.BindingRevision,
		ReferenceDigest: snapshot.ReferenceDigest, OutfitLength: snapshot.OutfitLength, UserPrompt: "来张自拍", Prompt: "test compiled plan",
		Variables: map[string]string{"scene": "桌边短暂休息", "outfit": "短款上衣配短裙", "camera": "近景自拍", "primaryColor": "绿色", "activity": "桌边休息片刻", "action": "自然看向手机"}}
	priorRun := runRecord{ID: "prior-life"}
	if err = a.db.QueryRow(`SELECT transport,transport_instance,agent_instance_id,memory_namespace,conversation_ref,sender_ref,persona_id,thread_key FROM agent_runs WHERE id=?`, run.ID).
		Scan(&priorRun.Transport, &priorRun.TransportInstance, &priorRun.AgentInstanceID, &priorRun.MemoryNamespace, &priorRun.ConversationRef, &priorRun.SenderRef, &priorRun.PersonaID, &priorRun.ThreadKey); err != nil {
		t.Fatal(err)
	}
	visualContinuitySource(t, a, priorRun, prior, time.Now().Add(-time.Minute))
	plan, err := a.prepareVisualGeneration(ctx, run, "同一场景接着拍自拍视频", "video", 0)
	if err != nil || plan.Variables["scene"] != prior.Variables["scene"] || plan.Variables["outfit"] != prior.Variables["outfit"] || plan.Variables["continuity"] == "" {
		t.Fatalf("actual generation path failed continuity: %+v / %v", plan.Variables, err)
	}
	if _, err = a.db.Exec("DELETE FROM agent_deliveries WHERE run_id='prior-life'"); err != nil {
		t.Fatal(err)
	}
	replay, err := a.prepareVisualGeneration(ctx, run, "同一场景接着拍自拍视频", "video", 0)
	if err != nil || replay.Prompt != plan.Prompt || replay.Seed != plan.Seed {
		t.Fatalf("transport replay changed plan: %v", err)
	}
}

func TestVisualPlanLifestyleCompilerUsesActualImageAndVideoPath(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		plan := visualGenerationPlan{MediaType: kind, AppearanceID: "selected-library", Identity: "固定同一位成年角色的脸与发型",
			UserPrompt: "来一张你的自拍", Variables: map[string]string{"activity": "翻书间隙", "light": "已有室内环境光"}}
		if kind == "video" {
			plan.UserPrompt = "给我拍段自拍视频"
		}
		compiled, err := compileVisualGenerationPrompt(plan, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{plan.UserPrompt, plan.Identity, "自然肤质", "activity=翻书间隙", "light=已有室内环境光"} {
			if !strings.Contains(compiled, want) {
				t.Fatalf("%s actual prompt omitted %q: %s", kind, want, compiled)
			}
		}
		if kind == "video" && (!strings.Contains(compiled, "单镜头") || !strings.Contains(compiled, "短暂停顿")) {
			t.Fatalf("video fell back to a photo-only prompt: %s", compiled)
		}
		plan.UserPrompt = "拍一段全身舞蹈视频，电影感"
		compiled, err = compileVisualGenerationPrompt(plan, "")
		if err != nil || strings.Contains(compiled, "不安排表演") {
			t.Fatalf("explicit performance overridden: %s / %v", compiled, err)
		}
	}
	generic := visualGenerationPlan{MediaType: "image", UserPrompt: "画一张产品结构图"}
	if prompt, err := compileVisualGenerationPrompt(generic, ""); err != nil || prompt != generic.UserPrompt {
		t.Fatalf("non-persona image affected: %q / %v", prompt, err)
	}
}

func TestVisualPlanLifestyleVideoBudgetKeepsMotionBeforeOptionalDetail(t *testing.T) {
	plan := visualGenerationPlan{MediaType: "video", AppearanceID: "test", Identity: "明确成年、固定同一张脸",
		UserPrompt: "不要紫色，坐着拍段视频", Variables: map[string]string{"scene": strings.Repeat("生活细节", 1500)}}
	prompt, err := compileVisualGenerationPrompt(plan, "修正脸型")
	if err != nil || len(prompt) > maxVideoPromptBytes || !strings.Contains(prompt, "单镜头") ||
		!strings.Contains(prompt, plan.UserPrompt) || !strings.Contains(prompt, "修正脸型") {
		t.Fatalf("mandatory request or motion lost to decorative detail: %v", err)
	}
}

func TestVisualLifestyleVideoExplicitAspectRatioWinsOverSelfie(t *testing.T) {
	for _, sample := range []struct{ prompt, ratio string }{
		{"横屏自拍视频，16:9", "16:9"}, {"方形自拍视频，1:1", "1:1"},
		{"自拍视频，4:3", "4:3"}, {"自拍视频，3:4", "3:4"}, {"普通自拍视频", "9:16"},
	} {
		if actual := videoGenerationOptionsForPrompt(sample.prompt).AspectRatio; actual != sample.ratio {
			t.Fatalf("%q: got %s, want %s", sample.prompt, actual, sample.ratio)
		}
	}
}

func TestNativePrepareLifestyleContextDoesNotAuthorizeGeneration(t *testing.T) {
	prompt := compileNativeSystemPrompt(nativeRuntimeConfig{}, contentBoundaryPolicy{}, nil,
		&nativePersona{ID: "doubao"}, nil, nil, nil, nil, "", "", "", "")
	for _, want := range []string{"本会话中明确属于角色", "不把用户自述所在地", "不自动授权付费生成", "不得为了生活感虚构生成或投递成功"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("planning prompt omitted %q", want)
		}
	}
}
