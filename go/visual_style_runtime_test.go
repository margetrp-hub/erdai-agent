package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func newVisualStyleRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	runtime := newDormantRuntime(t)
	// Keep workers stopped while permitting the real task-step persistence path.
	runtime.lifecycle, runtime.cancel = context.WithCancel(context.Background())
	return runtime
}

func visualStyleProfileRequest(t *testing.T, store *coreConfigStore, personaID, body string) *httptest.ResponseRecorder {
	t.Helper()
	path := "/api/v1/personas/runtime-profiles/" + personaID
	request := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	response := httptest.NewRecorder()
	if err := store.handlePersonaRuntimeRequest(response, request, path); err != nil {
		writeCoreAPIError(response, err)
	}
	return response
}

func setVisualStyleProfile(t *testing.T, store *coreConfigStore, personaID, body string) {
	t.Helper()
	response := visualStyleProfileRequest(t, store, personaID, body)
	if response.Code != http.StatusOK {
		t.Fatalf("set visual style profile: %d %s", response.Code, response.Body.String())
	}
}

func createVisualStyleInstance(t *testing.T, store *coreConfigStore, instanceID, personaID string, overrides any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"id": instanceID, "displayName": instanceID, "personaId": personaID, "overrides": overrides})
	if err != nil {
		t.Fatal(err)
	}
	response := agentInstanceRequest(t, store, http.MethodPost, "/api/v1/agent-instances", string(body))
	if response.Code != http.StatusCreated {
		t.Fatalf("create styled instance: %d %s", response.Code, response.Body.String())
	}
}

func updateVisualStyleInstance(t *testing.T, store *coreConfigStore, instanceID string, overrides any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"overrides": overrides})
	if err != nil {
		t.Fatal(err)
	}
	response := agentInstanceRequest(t, store, http.MethodPut, "/api/v1/agent-instances/"+instanceID, string(body))
	if response.Code != http.StatusOK {
		t.Fatalf("update styled instance: %d %s", response.Code, response.Body.String())
	}
}

func visualStyleRun(t *testing.T, runtime *AgentRuntime, runID, personaID, instanceID string) runRecord {
	t.Helper()
	run := visualPlanTestRun(t, runtime, runID)
	run.PersonaID, run.AgentInstanceID = personaID, instanceID
	if _, err := runtime.db.Exec("UPDATE agent_runs SET persona_id=?,agent_instance_id=? WHERE id=?", personaID, instanceID, runID); err != nil {
		t.Fatal(err)
	}
	return run
}

func TestVisualStyleManagementValidationAndInheritance(t *testing.T) {
	store := newAgentInstanceStore(t)
	setVisualStyleProfile(t, store, "persona-test", `{"visualPromptOverride":" 默认真实相机风格 ","visualStyle":{"selfieTypes":[" 全身生活照 ","","全身生活照"],"outfits":[" 短吊带加外套 ","短吊带加外套"],"scenes":[" 雨夜街角 "," ","雨夜街角"]}}`)
	profile, err := store.personaRuntimeProfile("persona-test")
	want := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Outfits: []string{"短吊带加外套"}, Scenes: []string{"雨夜街角"}}
	if err != nil || !reflect.DeepEqual(profile.VisualStyle, want) || profile.VisualPromptOverride != "默认真实相机风格" {
		t.Fatalf("normalized role style = %+v, error=%v", profile, err)
	}
	setVisualStyleProfile(t, store, "persona-test", `{"expressionPrompt":"保持自然聊天"}`)
	profile, err = store.personaRuntimeProfile("persona-test")
	if err != nil || !reflect.DeepEqual(profile.VisualStyle, want) {
		t.Fatalf("unrelated update lost role style: %+v %v", profile, err)
	}
	createVisualStyleInstance(t, store, "styled", "persona-test", map[string]any{"visualStyle": map[string]any{"scenes": []string{" 公园小路 ", "公园小路"}}, "extension": map[string]any{"keep": true}})
	profile, err = store.effectivePersonaRuntimeProfile("persona-test", "styled")
	if err != nil || !reflect.DeepEqual(profile.VisualStyle, &visualStyleDefaults{Scenes: []string{"公园小路"}}) || profile.VisualPromptOverride != "默认真实相机风格" {
		t.Fatalf("instance did not replace style object and inherit prose: %+v %v", profile, err)
	}
	instance, found, err := store.agentInstance("styled")
	if err != nil || !found || !strings.Contains(string(instance.Overrides), `"extension":{"keep":true}`) || strings.Contains(string(instance.Overrides), " 公园小路 ") {
		t.Fatalf("instance style normalization changed unrelated config: %s %v", instance.Overrides, err)
	}
	updateVisualStyleInstance(t, store, "styled", map[string]any{"visualStyle": map[string]any{}})
	profile, err = store.effectivePersonaRuntimeProfile("persona-test", "styled")
	if err != nil || profile.VisualStyle == nil || len(profile.VisualStyle.Scenes) != 0 || len(profile.VisualStyle.SelfieTypes) != 0 {
		t.Fatalf("explicit empty style did not suppress inherited pools: %+v %v", profile, err)
	}
	updateVisualStyleInstance(t, store, "styled", map[string]any{"visualStyle": nil})
	profile, err = store.effectivePersonaRuntimeProfile("persona-test", "styled")
	if err != nil || !reflect.DeepEqual(profile.VisualStyle, want) {
		t.Fatalf("null instance style did not restore inheritance: %+v %v", profile, err)
	}
	setVisualStyleProfile(t, store, "persona-test", `{"visualStyle":null}`)
	profile, err = store.personaRuntimeProfile("persona-test")
	if err != nil || profile.VisualStyle != nil {
		t.Fatalf("null role style did not clear defaults: %+v %v", profile, err)
	}

	for _, test := range []struct {
		name  string
		style any
	}{
		{"too_many_outfits", map[string]any{"outfits": make([]string, 21)}},
		{"too_many_scenes", map[string]any{"scenes": make([]string, 21)}},
		{"too_many_cameras", map[string]any{"selfieTypes": make([]string, 21)}},
		{"outfit_too_long", map[string]any{"outfits": []string{strings.Repeat("衣", 161)}}},
		{"scene_too_long", map[string]any{"scenes": []string{strings.Repeat("景", 161)}}},
		{"camera_too_long", map[string]any{"selfieTypes": []string{strings.Repeat("机", 25)}}},
		{"unknown_identity_field", map[string]any{"identity": "different face"}},
		{"wrong_list_type", map[string]any{"outfits": "short"}},
		{"wrong_item_type", map[string]any{"scenes": []any{1}}},
		{"not_an_object", []string{"street"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"visualStyle": test.style})
			response := visualStyleProfileRequest(t, store, "persona-test", string(body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid role style accepted: %d %s", response.Code, response.Body.String())
			}
			body, _ = json.Marshal(map[string]any{"overrides": map[string]any{"visualStyle": test.style}})
			response = agentInstanceRequest(t, store, http.MethodPut, "/api/v1/agent-instances/styled", string(body))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("invalid instance style accepted: %d %s", response.Code, response.Body.String())
			}
		})
	}
	boundary := strings.Repeat("景", 160)
	body, _ := json.Marshal(map[string]any{"visualStyle": map[string]any{"scenes": []string{boundary}}})
	setVisualStyleProfile(t, store, "persona-test", string(body))
}

func TestVisualStylePrepareScopesDefaultsWithoutChangingAppearance(t *testing.T) {
	runtime := newVisualStyleRuntime(t)
	defer runtime.Close()
	store := runtime.configStore
	addVisualPlanReference(t, runtime, "doubao")
	var libraryID, libraryBefore string
	if err := store.db.QueryRow("SELECT library_id FROM persona_appearance_libraries WHERE persona_id='doubao'").Scan(&libraryID); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow("SELECT visual_description || outfit_length || updated_at FROM appearance_libraries WHERE id=?", libraryID).Scan(&libraryBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec("UPDATE persona_appearance_libraries SET library_id=? WHERE persona_id='xiaoman'", libraryID); err != nil {
		t.Fatal(err)
	}
	setVisualStyleProfile(t, store, "doubao", `{"visualPromptOverride":"角色默认：自然相机质感","visualStyle":{"selfieTypes":["半身生活照"],"outfits":["日常衬衫"],"scenes":["公园小路"]}}`)
	instanceStyle := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Outfits: []string{"短吊带、短裙与薄外套"}, Scenes: []string{"雨夜街角路灯旁"}}
	createVisualStyleInstance(t, store, "styled-main", "doubao", map[string]any{"visualPromptOverride": "实例默认：真实相机细节，日常轻松", "visualStyle": instanceStyle})
	createVisualStyleInstance(t, store, "styled-secondary", "doubao", map[string]any{})
	createVisualStyleInstance(t, store, "unstyled-other-role", "xiaoman", map[string]any{})
	ctx := context.Background()
	var referenceDigest, identity string
	for _, test := range []struct {
		instance    string
		persona     string
		stylePrompt string
		camera      string
		scene       string
	}{
		{"styled-main", "doubao", "实例默认：真实相机细节，日常轻松", "全身生活照", "雨夜街角路灯旁"},
		{"styled-secondary", "doubao", "角色默认：自然相机质感", "半身生活照", "公园小路"},
		{"unstyled-other-role", "xiaoman", "", "", ""},
	} {
		t.Run(test.instance, func(t *testing.T) {
			run := visualStyleRun(t, runtime, "style-scope-"+test.instance, test.persona, test.instance)
			plan, err := runtime.prepareVisualGeneration(ctx, run, "给我一张你的自拍", "image", 0)
			if err != nil {
				t.Fatal(err)
			}
			if plan.AppearanceID != libraryID || plan.StylePrompt != test.stylePrompt || plan.UserPrompt != "给我一张你的自拍" {
				t.Fatalf("wrong scope/identity or defaults promoted to user request: %+v", plan)
			}
			if referenceDigest == "" {
				referenceDigest, identity = plan.ReferenceDigest, plan.Identity
			}
			if plan.ReferenceDigest != referenceDigest || plan.Identity != identity {
				t.Fatal("style/role changed identity from the selected shared library")
			}
			if test.stylePrompt == "" {
				if plan.Style != nil || strings.Contains(plan.Prompt, "雨夜街角路灯旁") || strings.Contains(plan.Prompt, "独立默认风格") {
					t.Fatal("instance defaults leaked to another role")
				}
			} else if !strings.Contains(plan.Variables["camera"], test.camera) || plan.Variables["scene"] != test.scene || !strings.Contains(plan.Prompt, test.stylePrompt) {
				t.Fatalf("effective style missed the actual prepare path: %+v", plan)
			}
		})
	}
	run := visualStyleRun(t, runtime, "style-ordinary-image", "doubao", "styled-main")
	ordinary, err := runtime.prepareVisualGeneration(ctx, run, "画一张森林风景", "image", 0)
	if err != nil || ordinary.AppearanceID != "" || ordinary.Style != nil || ordinary.StylePrompt != "" || ordinary.Prompt != "画一张森林风景" {
		t.Fatalf("non-selfie acquired personal style: %+v %v", ordinary, err)
	}
	run = visualStyleRun(t, runtime, "style-video", "doubao", "styled-main")
	video, err := runtime.prepareVisualGeneration(ctx, run, "给我一段你的生活视频", "video", 0)
	if err != nil || !reflect.DeepEqual(video.Style, instanceStyle) || video.StylePrompt != "实例默认：真实相机细节，日常轻松" || !strings.Contains(video.Variables["camera"], "全身") {
		t.Fatalf("video omitted effective style: %+v %v", video, err)
	}
	var libraryAfter string
	if err := store.db.QueryRow("SELECT visual_description || outfit_length || updated_at FROM appearance_libraries WHERE id=?", libraryID).Scan(&libraryAfter); err != nil || libraryBefore != libraryAfter {
		t.Fatalf("style config modified appearance library: %v", err)
	}
}

func TestVisualStylePlanPersistsRetryAndSupersedesChangedDefaults(t *testing.T) {
	runtime := newVisualStyleRuntime(t)
	defer runtime.Close()
	store := runtime.configStore
	addVisualPlanReference(t, runtime, "doubao")
	style := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Outfits: []string{"短吊带加外套"}, Scenes: []string{"雨夜街角", "傍晚河边"}}
	overrides := map[string]any{"visualStyle": style, "visualPromptOverride": "自然相机质感"}
	createVisualStyleInstance(t, store, "styled-retry", "doubao", overrides)
	ctx := context.Background()
	run := visualStyleRun(t, runtime, "styled-retry-task", "doubao", "styled-retry")
	first, err := runtime.prepareVisualGeneration(ctx, run, "给我一张你的自拍", "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := runtime.prepareVisualGeneration(ctx, run, "工具换了一种说法的自拍", "image", 0)
	if err != nil || !reflect.DeepEqual(first, retry) {
		t.Fatalf("transport retry did not restore the frozen style/plan: %v", err)
	}
	repair, err := runtime.prepareVisualGeneration(ctx, run, "给我一张你的自拍", "image", 1)
	if err != nil || !reflect.DeepEqual(repair.Style, style) || repair.StylePrompt != first.StylePrompt || repair.UserPrompt != first.UserPrompt || repair.ReferenceDigest != first.ReferenceDigest {
		t.Fatalf("repair did not preserve style and identity snapshot: %+v %v", repair, err)
	}
	details, err := runtime.visualPlanTaskDetails(ctx, run.ID)
	if err != nil || len(details) != 2 {
		t.Fatalf("persisted plans: %+v %v", details, err)
	}
	for _, plan := range details {
		if !reflect.DeepEqual(plan.Style, style) || plan.StylePrompt != first.StylePrompt {
			t.Fatal("persisted visual plan omitted its style snapshot")
		}
	}
	setVisualStyleProfile(t, store, "doubao", `{"expressionPrompt":"另一个表达习惯","visualStyle":{"scenes":["白天公园"]},"visualPromptOverride":"角色新默认"}`)
	if err := runtime.validateVisualGeneration(ctx, run, first); err != nil {
		t.Fatalf("overridden role/unrelated expression change cancelled instance style: %v", err)
	}
	overrides["visualPromptOverride"] = "新的拍摄默认"
	updateVisualStyleInstance(t, store, "styled-retry", overrides)
	for _, attempt := range []int{0, 1} {
		if _, err := runtime.prepareVisualGeneration(ctx, run, first.UserPrompt, "image", attempt); !errors.Is(err, errTaskSuperseded) {
			t.Fatalf("changed style prose replayed stale attempt %d: %v", attempt, err)
		}
	}
	newRun := visualStyleRun(t, runtime, "styled-new-task", "doubao", "styled-retry")
	newPlan, err := runtime.prepareVisualGeneration(ctx, newRun, "给我一张你的自拍", "image", 0)
	if err != nil || newPlan.StylePrompt != "新的拍摄默认" {
		t.Fatalf("fresh request failed to use new style: %+v %v", newPlan, err)
	}
	style.Scenes = []string{"晴天步行街"}
	updateVisualStyleInstance(t, store, "styled-retry", overrides)
	if err := runtime.validateVisualGeneration(ctx, newRun, newPlan); !errors.Is(err, errTaskSuperseded) {
		t.Fatalf("changed structured style did not supersede old task: %v", err)
	}
	if err := runtime.ensureRunVisualAppearanceCurrent(ctx, newRun.ID); !errors.Is(err, errTaskSuperseded) {
		t.Fatalf("delivery validation missed changed style: %v", err)
	}
}

func TestVisualStyleCompilerPreservesDefaultsWithoutPromotingThem(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			plan := visualGenerationPlan{MediaType: kind, AppearanceID: "selected", Identity: "原身份：成年、同一张脸与体态", UserPrompt: "这次不要雨夜，不穿短裙，只要近景自拍", StylePrompt: "默认雨夜街头、短款穿搭、全身摄影", Style: &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}}, Variables: map[string]string{"camera": "近景自拍", "scene": "按用户要求"}}
			prompt, err := compileVisualGenerationPrompt(plan, "脸型跟参考保持一致")
			if err != nil {
				t.Fatal(err)
			}
			defaultIndex := strings.Index(prompt, "独立默认风格")
			if defaultIndex < 0 || strings.Contains(prompt[:defaultIndex], plan.StylePrompt) || !strings.Contains(prompt[:defaultIndex], plan.UserPrompt) || !strings.Contains(prompt[:defaultIndex], plan.Identity) {
				t.Fatal("defaults replaced user intent or were presented as identity")
			}
			for _, required := range []string{plan.StylePrompt, "不得改变当前外观库的脸、年龄或体态", "用户本次要求及否定约束优先", "camera=近景自拍", "脸型跟参考保持一致"} {
				if !strings.Contains(prompt, required) {
					t.Fatalf("required prompt section missing: %s", required)
				}
			}
			plan.StylePrompt = strings.Repeat("默认细节", maxImagePromptBytes)
			if _, err := compileVisualGenerationPrompt(plan, ""); err == nil {
				t.Fatal("oversized configured style was silently truncated")
			}
			plan.StylePrompt = ""
			plan.Variables["camera"] = strings.Repeat("完整构图", maxImagePromptBytes)
			if _, err := compileVisualGenerationPrompt(plan, ""); err == nil {
				t.Fatal("selected configured composition was silently truncated")
			}
		})
	}
}
