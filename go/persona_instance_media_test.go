package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPersonaMediaPromptUsesRunPersonaInsteadOfGlobalPersona(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	run := runRecord{PersonaID: "xiaoman", AgentInstanceID: "xiaoman-qq"}
	imagePrompt := runtime.personaImagePromptForRun(context.Background(), run, "给我一张你的全身自拍")
	videoPrompt := runtime.personaVideoPromptForRun(context.Background(), run, "在街边回头看镜头")
	for kind, prompt := range map[string]string{"image": imagePrompt, "video": videoPrompt} {
		if !strings.Contains(prompt, "黑色长卷发或自然大波浪") || !strings.Contains(prompt, "比豆包更热、更鲜活") ||
			!strings.Contains(prompt, "裙摆或裤脚明确在膝盖以上") || !strings.Contains(prompt, "膝上短款") ||
			!strings.Contains(prompt, "至少更换场景与服装颜色或款式") || !strings.Contains(prompt, "非窗边场景") {
			t.Fatalf("%s prompt did not load xiaoman identity: %s", kind, prompt)
		}
		if strings.Contains(prompt, "小巧柔和的鹅蛋脸") || strings.Contains(prompt, "身形纤细") {
			t.Fatalf("%s prompt leaked doubao identity: %s", kind, prompt)
		}
	}
}

func TestRuntimePreparePrefersRunPersonaSnapshotOverCurrentActivePersona(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	if _, err := runtime.configStore.db.Exec("UPDATE runtime_config SET active_persona_id = 'doubao' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{
		Transport: "qq_official", TransportInstance: "instance-one", ConversationRef: "group-one",
		Message: "给我一张你的自拍", personaID: "xiaoman",
	})
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ActivePersona == nil || prepared.ActivePersona.ID != "xiaoman" {
		t.Fatalf("prepared persona = %+v", prepared.ActivePersona)
	}
	if !strings.Contains(prepared.ActivePersona.VisualDescription, "不得读取或复用豆包") ||
		strings.Contains(prepared.ActivePersona.VisualDescription, "小巧柔和的鹅蛋脸") {
		t.Fatalf("prepared visual identity = %s", prepared.ActivePersona.VisualDescription)
	}
}

func TestPersonaAppearanceLibrarySwitchChangesSameRoleVisualPrompt(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	const alternateLibraryID = "doubao-appearance-summer"
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.configStore.db.Exec(`INSERT INTO appearance_libraries
		(id, namespace, name, description, visual_description, source_persona_id, enabled, created_at, updated_at)
		VALUES (?, 'default', '豆包夏日外观库', '同一角色的另一套外观',
			'同一个豆包角色，夏日红色短袖和短发造型；保持原角色身份。', 'doubao', 1, ?, ?)`, alternateLibraryID, now, now); err != nil {
		t.Fatal(err)
	}

	run := runRecord{PersonaID: "doubao"}
	basePrompt := runtime.personaImagePromptForRun(context.Background(), run, "给我一张你的自拍")
	if !strings.Contains(basePrompt, "室内咖啡店生活照") {
		t.Fatalf("default appearance prompt = %s", basePrompt)
	}

	request := httptest.NewRequest(http.MethodPut, "/api/v1/personas/doubao/appearance-library?namespace=default",
		strings.NewReader(`{"libraryId":"`+alternateLibraryID+`"}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	if err := runtime.configStore.handlePersonaRequest(response, request, request.URL.Path); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK {
		t.Fatalf("appearance library switch status = %d: %s", response.Code, response.Body.String())
	}

	switchedPrompt := runtime.personaImagePromptForRun(context.Background(), run, "给我一张你的自拍")
	if !strings.Contains(switchedPrompt, "夏日红色短袖和短发造型") || strings.Contains(switchedPrompt, "室内咖啡店生活照") {
		t.Fatalf("switched appearance prompt = %s", switchedPrompt)
	}
}

func TestPersonaMediaPromptAvoidsStockPhotoLanguage(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	prompt := runtime.personaImagePromptForRun(context.Background(), runRecord{PersonaID: "xiaoman", AgentInstanceID: "xiaoman-qq"}, "给我一张你的自拍")
	for _, phrase := range []string{"随手拍到", "不要每次正面居中", "禁止棚拍", "真实皮肤纹理"} {
		if !strings.Contains(prompt, phrase) {
			t.Fatalf("missing natural photo constraint %q: %s", phrase, prompt)
		}
	}
}

func TestPersonaAvatarReferenceUsesExplicitPersona(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	if _, err := runtime.configStore.db.Exec(`UPDATE personas SET avatar_data_uri = CASE id
		WHEN 'doubao' THEN 'data:image/png;base64,ZG91YmFv'
		WHEN 'xiaoman' THEN 'data:image/png;base64,eGlhb21hbg=='
		ELSE avatar_data_uri END WHERE id IN ('doubao', 'xiaoman')`); err != nil {
		t.Fatal(err)
	}
	active := runtime.personaAvatarDataURI(context.Background(), "", "自拍", false)
	xiaoman := runtime.personaAvatarDataURI(context.Background(), "xiaoman", "自拍", false)
	if active != "data:image/png;base64,ZG91YmFv" {
		t.Fatalf("global persona avatar = %q", active)
	}
	if xiaoman != "data:image/png;base64,eGlhb21hbg==" {
		t.Fatalf("xiaoman persona avatar = %q", xiaoman)
	}
}

func TestServicePersonaDoesNotReceiveOrdinaryQuestionBoost(t *testing.T) {
	assessment := groupSocialAssessment{Intent: "求助", IsQuestion: true}
	if boost := groupParticipationIntentBoost("service", assessment, 0.35); boost != 0 {
		t.Fatalf("service persona ordinary question boost = %v", boost)
	}
	if boost := groupParticipationIntentBoost("social", assessment, 0.35); boost != 0 {
		t.Fatalf("social persona ordinary question boost = %v", boost)
	}
	if boost := groupParticipationIntentBoost("balanced", assessment, 0.35); boost < 0.449 || boost > 0.451 {
		t.Fatalf("balanced persona question boost = %v", boost)
	}
}
