package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestActivePersonaVideoPromptCarriesVisualIdentity(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	profile := personaRuntimeProfile{
		PersonaID:            "doubao",
		VisualPromptOverride: "动作轻而连贯：整理耳边头发，侧身后回看镜头。",
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.configStore.db.Exec(`INSERT INTO persona_runtime_profiles(persona_id, profile_json, updated_at)
		VALUES ('doubao', ?, ?) ON CONFLICT(persona_id) DO UPDATE SET profile_json=excluded.profile_json, updated_at=excluded.updated_at`,
		string(encoded), time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	prompt := runtime.activePersonaVideoPrompt(context.Background(), "在海边散步")
	if !strings.Contains(prompt, "角色外观基准") || !strings.Contains(prompt, "用户场景要求：在海边散步") ||
		!strings.Contains(prompt, "当前角色视觉覆盖") || !strings.Contains(prompt, "整理耳边头发") ||
		!strings.Contains(prompt, "参考视频只借鉴动作、镜头、光线、服装和氛围") ||
		!strings.Contains(prompt, "一个主要动作") {
		t.Fatalf("persona video prompt missing identity context: %s", prompt)
	}
}
