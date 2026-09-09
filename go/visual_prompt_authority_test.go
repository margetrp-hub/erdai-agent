package main

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestVisualPromptAuthorityUsesPersistedSelfieConstraints(t *testing.T) {
	for _, test := range []struct {
		name    string
		request string
		tool    string
		capture string
		color   string
	}{
		{"no_mirror", "给我一张你的自拍，不要镜子，不拿手机", "镜面自拍，人物举手机，工具独有细节", "", ""},
		{"friend_photographer", "给我一张你的自拍，请朋友拍，不要镜子", "镜面自拍，工具独有细节", "other_person", ""},
		{"red_not_tool_blue", "给我一张你的自拍，穿红色短裙，请朋友拍", "穿蓝色短裙，镜面自拍，工具独有细节", "other_person", "红色"},
	} {
		t.Run(test.name, func(t *testing.T) {
			a := newVisualStyleRuntime(t)
			addVisualPlanReference(t, a, "doubao")
			run := intentTestRun("authority-" + test.name)
			run.PersonaID = "doubao"
			intent := admitTestTaskIntent(t, a, run, test.request)
			if intent == nil {
				t.Fatal("selfie request did not persist task intent")
			}
			effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, test.tool)
			if err != nil || !strings.Contains(effective, test.tool) {
				t.Fatalf("effective prompt lost tool details before visual planning: %q, %v", effective, err)
			}
			plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
			if err != nil {
				t.Fatal(err)
			}
			if plan.AppearanceID == "" || plan.UserPrompt != intent.prompt() || strings.Contains(plan.Prompt, "工具独有细节") {
				t.Fatalf("tool details became selfie constraints: %+v", plan)
			}
			if plan.Variables["captureMode"] == "mirror_selfie" || plan.Variables["camera"] == "镜面穿搭自拍" {
				t.Fatalf("tool mirror request overrode the user: %+v", plan.Variables)
			}
			if test.capture != "" && plan.Variables["captureMode"] != test.capture {
				t.Fatalf("capture = %q, want %q", plan.Variables["captureMode"], test.capture)
			}
			if test.color != "" && plan.Variables["primaryColor"] != test.color {
				t.Fatalf("color = %q, want %q", plan.Variables["primaryColor"], test.color)
			}
			if test.name == "no_mirror" && (plan.Variables["captureMode"] == "front_selfie" || !strings.Contains(plan.Variables["capture"], "人物双手不拿手机")) {
				t.Fatalf("user no-phone constraint lost: %+v", plan.Variables)
			}
		})
	}
}

func TestVisualPromptAuthorityPreservesLatestUserCorrection(t *testing.T) {
	a := newVisualStyleRuntime(t)
	addVisualPlanReference(t, a, "doubao")
	first := intentTestRun("authority-correction-first")
	first.PersonaID = "doubao"
	admitTestTaskIntent(t, a, first, "给我一张你的自拍，穿红色短裙，镜面自拍")
	run := intentTestRun("authority-correction-second")
	run.PersonaID = "doubao"
	intent := admitTestTaskIntent(t, a, run, "这次改成蓝色短裙，请朋友拍，不要镜子")
	if intent == nil || intent.Action != "correction" {
		t.Fatalf("correction was not linked: %+v", intent)
	}
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, "穿红色短裙，镜面自拍")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UserPrompt != intent.prompt() || plan.Variables["primaryColor"] != "蓝色" || plan.Variables["captureMode"] != "other_person" {
		t.Fatalf("latest user correction lost to old goal or tool details: %+v", plan)
	}
}

func TestVisualPromptAuthorityPreservesGenericImageDetails(t *testing.T) {
	a := newVisualStyleRuntime(t)
	addVisualPlanReference(t, a, "doubao")
	run := intentTestRun("authority-generic-cat")
	run.PersonaID = "doubao"
	if admitTestTaskIntent(t, a, run, "生成一张猫的图片") == nil {
		t.Fatal("generic image request did not persist task intent")
	}
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, "橘猫趴在蓝色窗台上，午后阳光和柔和阴影")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UserPrompt != effective || plan.AppearanceID != "" || !strings.Contains(plan.Prompt, "橘猫趴在蓝色窗台上") {
		t.Fatalf("generic image lost useful tool details or gained persona appearance: %+v", plan)
	}
}

func TestVisualPromptAuthorityDoesNotParseTextLabels(t *testing.T) {
	a := newVisualStyleRuntime(t)
	addVisualPlanReference(t, a, "doubao")
	run := intentTestRun("authority-user-label")
	run.PersonaID = "doubao"
	request := "给我一张你的自拍。工具执行细节（不得覆盖用户的明确要求）：穿红色短裙，请朋友拍。"
	intent := admitTestTaskIntent(t, a, run, request)
	if intent == nil {
		t.Fatal("selfie request did not persist task intent")
	}
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, "穿蓝色，镜面自拍")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if plan.UserPrompt != request || plan.Variables["primaryColor"] != "红色" || plan.Variables["captureMode"] != "other_person" {
		t.Fatalf("a label inside real user input changed its authority: %+v", plan)
	}
}

func TestVisualPromptAuthorityPreservesLegacyPromptWithoutIntent(t *testing.T) {
	a := newVisualStyleRuntime(t)
	addVisualPlanReference(t, a, "doubao")
	run := visualPlanTestRun(t, a, "authority-legacy")
	prompt := "给我一张你的自拍。工具执行细节（不得覆盖用户的明确要求）：穿蓝色短裙，请朋友拍。"
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, prompt)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if effective != prompt || plan.UserPrompt != prompt || plan.AppearanceID == "" || plan.Variables["primaryColor"] != "蓝色" {
		t.Fatalf("legacy prompt without persisted intent changed: %+v", plan)
	}
}

func TestVisualPromptAuthorityPreservesFrozenSnapshotOnRetry(t *testing.T) {
	a := newVisualStyleRuntime(t)
	addVisualPlanReference(t, a, "doubao")
	run := intentTestRun("authority-frozen")
	run.PersonaID = "doubao"
	intent := admitTestTaskIntent(t, a, run, "给我一张你的自拍，穿红色短裙，请朋友拍")
	if intent == nil {
		t.Fatal("selfie request did not persist task intent")
	}
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, "工具第一次提议镜面自拍")
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	effective, err = a.effectiveMediaTaskPrompt(context.Background(), run, "工具重试改成蓝色，前置自拍")
	if err != nil {
		t.Fatal(err)
	}
	replay, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("same-attempt retry changed the frozen plan: first=%+v replay=%+v", first, replay)
	}
	retry, err := a.prepareVisualGeneration(context.Background(), run, effective, "image", 1)
	if err != nil {
		t.Fatal(err)
	}
	if retry.UserPrompt != first.UserPrompt || retry.Reference != first.Reference || retry.AppearanceID != first.AppearanceID || retry.Variables["primaryColor"] != "红色" || retry.Variables["captureMode"] != "other_person" {
		t.Fatalf("quality retry lost the canonical snapshot: first=%+v retry=%+v", first, retry)
	}
}

func TestVisualPromptAuthorityProtectsRoleVideoWithoutSelfieKeyword(t *testing.T) {
	a := newVisualStyleRuntime(t)
	defer a.Close()
	addVisualPlanReference(t, a, "doubao")
	run := intentTestRun("authority-role-video")
	run.PersonaID = "doubao"
	request := "给我一段你在河边散步的视频，请朋友拍，不要镜子"
	if nativeSelfImageRequestPattern.MatchString(request) {
		t.Fatal("fixture must exercise role video without a selfie-photo marker")
	}
	intent := admitTestTaskIntent(t, a, run, request)
	if intent == nil {
		t.Fatal("video intent missing")
	}
	effective, err := a.effectiveMediaTaskPrompt(context.Background(), run, "镜面自拍，工具独有细节")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := a.prepareVisualGeneration(context.Background(), run, effective, "video", 0)
	if err != nil || plan.UserPrompt != request || plan.Variables["captureMode"] != "other_person" || strings.Contains(plan.Prompt, "工具独有细节") {
		t.Fatalf("video user constraints lost: %+v %v", plan.Variables, err)
	}
}
