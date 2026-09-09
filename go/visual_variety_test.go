package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestVisualPlanFramingVariesWithoutOverridingExplicitChoice(t *testing.T) {
	pool := []string{"全身生活照", "全身穿搭照", "镜面穿搭自拍", "半身生活照", "近景自拍"}
	previous := visualGenerationPlan{Variables: map[string]string{"camera": "全身生活照"}}
	for seed := uint64(0); seed < 80; seed++ {
		value := seed
		if got := visualCameraChoice("来张你的照片", &value, pool, previous); visualFramingKey(got) == "full" {
			t.Fatalf("adjacent synonymous full-body composition repeated: %s", got)
		}
		value = seed
		if got := visualCameraChoice("给我你的全身照", &value, pool, previous); visualFramingKey(got) != "full" {
			t.Fatalf("explicit repeated full-body composition lost: %s", got)
		}
		value = seed
		if got := visualCameraChoice("半身对镜自拍", &value, pool, previous); visualFramingKey(got) != "half" {
			t.Fatalf("capture method replaced explicit framing: %s", got)
		}
	}
}

func TestVisualPlanScopedHistoryKeepsOwnRecentReservations(t *testing.T) {
	runtime := newVisualStyleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	name := visualPlanName("shared-appearance")
	current := visualStyleRun(t, runtime, "variety-current", "doubao", "doubao-qq")
	previous := visualStyleRun(t, runtime, "variety-own", "doubao", "doubao-qq")
	want := visualGenerationPlan{OperationID: "own-recent", Variables: map[string]string{"camera": "全身生活照", "captureMode": "other_person"}}
	if err := runtime.writeVisualRecord(previous, name, 0, map[string]string{"id": previous.ID}, want); err != nil {
		t.Fatal(err)
	}
	for index, changed := range []string{"agent_instance_id", "persona_id", "transport", "transport_instance", "memory_namespace", "conversation_ref", "sender_ref", "thread_key", "failed", "cancelled", "error"} {
		run := visualStyleRun(t, runtime, fmt.Sprintf("variety-unrelated-%d", index), "doubao", "doubao-qq")
		if changed == "failed" || changed == "cancelled" || changed == "error" {
			if _, err := runtime.db.Exec("UPDATE agent_runs SET state=? WHERE id=?", changed, run.ID); err != nil {
				t.Fatal(err)
			}
		} else {
			// Column identifiers come only from the fixed test list above.
			if _, err := runtime.db.Exec("UPDATE agent_runs SET "+changed+"=? WHERE id=?", "different", run.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := runtime.writeVisualRecord(run, name, 0, map[string]string{"id": run.ID}, visualGenerationPlan{OperationID: run.ID}); err != nil {
			t.Fatal(err)
		}
	}
	history, err := runtime.recentVisualPlans(ctx, name, 8, current)
	if err != nil || len(history) != 1 || !reflect.DeepEqual(history[0], want) {
		t.Fatalf("another scope or failed attempt displaced own history: %+v %v", history, err)
	}
	adminHistory, err := runtime.recentVisualPlans(ctx, name, 8)
	if err != nil || len(adminHistory) != 8 {
		t.Fatalf("admin library-wide history was narrowed: %d %v", len(adminHistory), err)
	}
}

func TestVisualPlanCaptureSurvivesCompilationAndContinuity(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		plan := visualGenerationPlan{MediaType: kind, AppearanceID: "adult-identity", Identity: "同一位成年角色",
			UserPrompt: "同一场景，朋友帮你拍，不要镜子", Variables: map[string]string{
				"captureMode": "other_person", "capture": "朋友在画外拍摄，人物不持拍摄设备、镜子不入镜",
				"scene": "城市街角", "camera": "全身生活照", "action": "自然放松", "activity": "日常散步"}}
		previous := visualGenerationPlan{Variables: map[string]string{"scene": "旧地点", "action": "对镜举手机", "activity": "举着手机自拍"}}
		applyVisualContinuity(&plan, &previous)
		if plan.Variables["action"] != "自然放松" || plan.Variables["activity"] != "日常散步" {
			t.Fatal("continuity brought back old capture props")
		}
		compiled, err := compileVisualGenerationPrompt(plan, "")
		if err != nil || !strings.Contains(compiled, "capture="+plan.Variables["capture"]) || strings.Contains(compiled, "captureMode=") {
			t.Fatalf("capture requirements omitted or internal enum leaked: %v", err)
		}
		plan.Variables["capture"] = strings.Repeat("不得丢失的拍摄约束", maxImagePromptBytes)
		if _, err := compileVisualGenerationPrompt(plan, ""); err == nil {
			t.Fatal("oversized capture requirements silently truncated")
		}
	}
}

func TestVisualPlanNewRequestsVaryAndRetriesKeepCapture(t *testing.T) {
	runtime := newVisualStyleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "doubao")
	style := &visualStyleDefaults{
		SelfieTypes: []string{"全身生活照", "半身生活照", "近景自拍"},
		Outfits:     []string{"吊带配短裙", "衬衫配短裤", "短款连衣裙"},
		Scenes:      []string{"河边", "咖啡店外摆", "客厅"},
	}
	for _, instance := range []string{"variety-a", "variety-b"} {
		createVisualStyleInstance(t, runtime.configStore, instance, "doubao", map[string]any{"visualStyle": style})
	}
	previous := map[string]visualGenerationPlan{}
	for index := 0; index < 12; index++ {
		for _, instance := range []string{"variety-a", "variety-b"} {
			run := visualStyleRun(t, runtime, fmt.Sprintf("variety-%s-%d", instance, index), "doubao", instance)
			plan, err := runtime.prepareVisualGeneration(context.Background(), run, "来张你的自拍", "image", 0)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Variables["captureMode"] == "" || !strings.Contains(plan.Prompt, "capture="+plan.Variables["capture"]) {
				t.Fatal("real prepare did not carry the selected capture mode")
			}
			if old, found := previous[instance]; found {
				if old.OperationID == plan.OperationID || old.Seed == plan.Seed ||
					old.Variables["outfit"] == plan.Variables["outfit"] || old.Variables["scene"] == plan.Variables["scene"] ||
					visualFramingKey(old.Variables["camera"]) == visualFramingKey(plan.Variables["camera"]) {
					t.Fatalf("new request repeated its own latest plan despite interleaving another instance: %+v", plan.Variables)
				}
			}
			retry, err := runtime.prepareVisualGeneration(context.Background(), run, "工具重复请求不同说法的自拍", "image", 0)
			if err != nil || !reflect.DeepEqual(plan, retry) {
				t.Fatalf("same task retry changed capture or composition: %v", err)
			}
			previous[instance] = plan
		}
	}
}
