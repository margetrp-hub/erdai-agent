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
		!strings.Contains(prompt, "一个主要动作") || !strings.Contains(prompt, "室内咖啡店生活照") ||
		!strings.Contains(prompt, "轻薄自然的侧分刘海") {
		t.Fatalf("persona video prompt missing identity context: %s", prompt)
	}
}

func TestPersonaVideoPromptHonorsOutfitAndStyleFeedback(t *testing.T) {
	persona := &nativeActivePersona{
		ID:                "xiaoman",
		VisualDescription: "明确成年的中国女性，黑色长卷发，真实摄影质感。",
	}
	now := time.Date(2026, time.August, 31, 18, 0, 0, 0, time.FixedZone("CST", 8*60*60))
	prompt := personaVideoPromptAt("换套别的颜色的，老是紫色，来个韩流风的跳舞视频", persona, now, defaultImageVisualDirectorPolicy(), 2)
	for _, expected := range []string{
		"参考图与上一张成片只锁定脸部、发型、年龄感和体态",
		"至少更换场景与服装颜色或款式",
		"外观只按当前选中的外观库和参考图确定",
		"视频类型=韩流/K-pop舞蹈短视频",
		"换装优先级=必须更换颜色和款式",
		"不得沿用参考图或上一条成片的紫色衣服、同一套衣服",
		"用户场景要求：换套别的颜色的，老是紫色，来个韩流风的跳舞视频",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("video prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestPersonaVideoPromptAppliesXiaomanShortOutfitLibrary(t *testing.T) {
	persona := &nativeActivePersona{
		ID:                "xiaoman",
		VisualDescription: canonicalXiaomanVisualDescription,
	}
	prompt := personaVideoPromptAt("来个韩流风的跳舞视频", persona,
		time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC), defaultImageVisualDirectorPolicy(), 13)
	for _, expected := range []string{
		"当前外观库的服装长度优先级：短款、膝上",
		"服装=白色修身无袖上衣配银灰色高腰短裙",
		"裙摆或裤脚明确在膝盖以上",
	} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("xiaoman short outfit prompt missing %q: %s", expected, prompt)
		}
	}
	if strings.Contains(prompt, "工装长裤") || strings.Contains(prompt, "宽腿运动裤") {
		t.Fatalf("xiaoman short outfit prompt retained long bottoms: %s", prompt)
	}
}

func TestPersonaVideoPromptAddsNonExplicitSexyVariation(t *testing.T) {
	persona := &nativeActivePersona{
		ID:                "xiaoman",
		VisualDescription: "明确成年的中国女性，真实摄影质感。",
	}
	prompt := personaVideoPromptAt("发个你穿的性感一点的跳舞视频给我", persona,
		time.Date(2026, time.August, 31, 16, 0, 0, 0, time.UTC), defaultImageVisualDirectorPolicy(), 1)
	for _, expected := range []string{"服装=", "性感表达=明确成年", "非裸露、非色情", "服装变化=本次使用新的非紫色造型"} {
		if !strings.Contains(prompt, expected) {
			t.Fatalf("sexy video prompt missing %q: %s", expected, prompt)
		}
	}
}

func TestVideoOutfitVariesAcrossGenerationSeeds(t *testing.T) {
	first := videoOutfit("性感一点的跳舞视频", 0)
	second := videoOutfit("性感一点的跳舞视频", 1)
	if first == second {
		t.Fatalf("video outfit did not vary: %q", first)
	}
	if strings.Contains(first, "紫色") || strings.Contains(second, "紫色") {
		t.Fatalf("default sexy outfits still use purple: %q / %q", first, second)
	}
}
