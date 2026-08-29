package main

import (
	"context"
	"strings"
	"testing"
)

func TestPersonaMediaPromptUsesRunPersonaInsteadOfGlobalPersona(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()

	run := runRecord{PersonaID: "xiaoman", AgentInstanceID: "xiaoman-qq"}
	imagePrompt := runtime.personaImagePromptForRun(context.Background(), run, "给我一张你的全身自拍")
	videoPrompt := runtime.personaVideoPromptForRun(context.Background(), run, "在街边回头看镜头")
	for kind, prompt := range map[string]string{"image": imagePrompt, "video": videoPrompt} {
		if !strings.Contains(prompt, "黑色长卷发或自然大波浪") || !strings.Contains(prompt, "比豆包更热、更鲜活") {
			t.Fatalf("%s prompt did not load xiaoman identity: %s", kind, prompt)
		}
		if strings.Contains(prompt, "小巧柔和的鹅蛋脸") || strings.Contains(prompt, "身形纤细") {
			t.Fatalf("%s prompt leaked doubao identity: %s", kind, prompt)
		}
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
