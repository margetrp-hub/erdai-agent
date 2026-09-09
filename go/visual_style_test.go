package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestVisualStyleDefaultsFullBodyAndVariation(t *testing.T) {
	style := &visualStyleDefaults{
		SelfieTypes: []string{"全身生活照", "全身穿搭照"},
		Outfits:     []string{"短款吊带配高腰短裙和敞开外套", "修身短上衣配牛仔短裤", "露肩短上衣配短裙和短靴"},
		Scenes:      []string{"城市街角", "咖啡店外摆", "河畔步道"},
	}
	history := []visualGenerationPlan{}
	for seed := uint64(0); seed < 40; seed++ {
		values := allocateStyledVisualVariables("来张你的自拍", time.Now(), seed, defaultImageVisualDirectorPolicy(), "short", history, style)
		if !strings.Contains(values["camera"], "全身") || !strings.Contains(values["action"], "双脚完整") {
			t.Fatalf("full-body default not applied: %+v", values)
		}
		if len(history) > 0 && (values["scene"] == history[0].Variables["scene"] || values["outfit"] == history[0].Variables["outfit"]) {
			t.Fatalf("repeated scene or outfit: %+v", values)
		}
		if strings.Contains(values["action"], "书页") || strings.Contains(values["outfit"], "棉质") {
			t.Fatalf("generic lifestyle choice leaked into styled defaults: %+v", values)
		}
		history = []visualGenerationPlan{{Variables: values}}
	}
}

func TestVisualStyleCurrentRequestWins(t *testing.T) {
	style := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Outfits: []string{"紫色短款吊带配短裙"}, Scenes: []string{"城市街角"}}
	for _, prompt := range []string{
		"来张你的近景自拍，穿长裤，在家里看书",
		"来张自拍，不要全身，不穿短裙，不要户外，别拿书",
		"selfie, no full body; wear pants; indoors",
	} {
		values := allocateStyledVisualVariables(prompt, time.Now(), 1, defaultImageVisualDirectorPolicy(), "short", nil, style)
		if values["camera"] == "全身生活照" || strings.Contains(values["outfit"], "吊带") || values["scene"] == "城市街角" {
			t.Fatalf("style overrode current constraints %q: %+v", prompt, values)
		}
	}
	values := allocateStyledVisualVariables("来张自拍，这次不要紫色", time.Now(), 1, defaultImageVisualDirectorPolicy(), "short", nil, style)
	if strings.Contains(values["outfit"], "紫色") || values["primaryColor"] == "紫色" {
		t.Fatalf("excluded color reintroduced by wardrobe pool: %+v", values)
	}
}

func TestVisualStyleFramingAndWeatherConstraints(t *testing.T) {
	style := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Scenes: []string{"雨夜街角路灯旁"}}
	for _, prompt := range []string{"给我近景自拍，穿裙子", "穿裙子的近景自拍", "给我半身穿搭照", "来张半身，鞋子穿运动鞋"} {
		values := allocateStyledVisualVariables(prompt, time.Now(), 3, defaultImageVisualDirectorPolicy(), "short", nil, style)
		if strings.Contains(values["camera"], "全身") {
			t.Fatalf("clothing overrode explicit framing %q: %+v", prompt, values)
		}
	}
	for _, prompt := range []string{"这次不要雨夜，来张自拍", "来张你的白天自拍", "selfie, no rain"} {
		values := allocateStyledVisualVariables(prompt, time.Now(), 3, defaultImageVisualDirectorPolicy(), "short", nil, style)
		if values["scene"] == style.Scenes[0] || !strings.Contains(values["scene"], "本次") {
			t.Fatalf("default weather/time overrode request %q: %+v", prompt, values)
		}
	}
}

func TestVisualStyleDoesNotInferRoleLocationFromUser(t *testing.T) {
	style := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Scenes: []string{"城市街角"}}
	values := allocateStyledVisualVariables("我在办公室，来张你的自拍", time.Now(), 7, defaultImageVisualDirectorPolicy(), "short", nil, style)
	if values["scene"] != "城市街角" {
		t.Fatalf("user location became character location: %+v", values)
	}
	values = allocateStyledVisualVariables("来张你坐着看书的自拍", time.Now(), 7, defaultImageVisualDirectorPolicy(), "short", nil, style)
	if values["scene"] == "城市街角" || !strings.Contains(values["action"], "用户明确") {
		t.Fatalf("default moved an explicit activity: %+v", values)
	}
}

func TestVisualStyleAbsentAndDirectorDisabled(t *testing.T) {
	now := time.Now()
	policy := defaultImageVisualDirectorPolicy()
	want := allocateVisualVariables("来张自拍", now, 17, policy, "short", nil)
	for _, style := range []*visualStyleDefaults{nil, {}} {
		got := allocateStyledVisualVariables("来张自拍", now, 17, policy, "short", nil, style)
		if got["captureMode"] == "" || got["capture"] == "" {
			t.Fatal("unconfigured style omitted independent capture selection")
		}
		for _, key := range []string{"outfit", "scene", "primaryColor", "makeup", "mood", "light", "time", "season"} {
			if !reflect.DeepEqual(got[key], want[key]) {
				t.Fatalf("unconfigured non-capture default changed: %s %q != %q", key, got[key], want[key])
			}
		}
	}
	policy.Enabled = false
	got := allocateStyledVisualVariables("来张自拍", now, 17, policy, "short", nil, &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Scenes: []string{"城市街角"}})
	if len(got) != 0 {
		t.Fatalf("disabled director was re-enabled: %+v", got)
	}
}

func TestVisualStylePoseMatchesFramingAndPreservesActionConstraints(t *testing.T) {
	for _, camera := range []string{"坐姿生活照", "近景自拍", "半身生活照", "全身生活照"} {
		style := &visualStyleDefaults{SelfieTypes: []string{camera}, Scenes: []string{"家中客厅"}}
		seen := map[string]bool{}
		history := []visualGenerationPlan{}
		for seed := uint64(0); seed < 16; seed++ {
			values := allocateStyledVisualVariables("来张生活照", time.Now(), seed, defaultImageVisualDirectorPolicy(), "short", history, style)
			if camera == "坐姿生活照" && !strings.Contains(values["action"], "坐稳") {
				t.Fatalf("sitting frame used an incompatible pose: %+v", values)
			}
			if len(history) > 0 && values["action"] == history[0].Variables["action"] {
				t.Fatalf("pose repeated despite other compatible poses: %+v", values)
			}
			seen[values["action"]] = true
			history = []visualGenerationPlan{{Variables: values}}
		}
		if len(seen) < 2 {
			t.Fatalf("pose stayed fixed for %s", camera)
		}
		for _, prompt := range []string{"坐着看书的生活照", "来张生活照，不要站着，也不要整理衣服", "生活照，不要侧身", "自拍，不要整理衣摆", "让朋友拍，手里拿手机"} {
			values := allocateStyledVisualVariables(prompt, time.Now(), 3, defaultImageVisualDirectorPolicy(), "short", nil, style)
			if !strings.Contains(values["action"], "明确") {
				t.Fatalf("default pose overrode an action constraint: %+v", values)
			}
		}
	}
}

func TestVisualStyleMirrorSceneSourceStillPreventsRepeats(t *testing.T) {
	style := &visualStyleDefaults{SelfieTypes: []string{"半身生活照"}, Scenes: []string{"家中全身镜前", "河畔步道"}}
	history := []visualGenerationPlan{}
	for seed := uint64(0); seed < 24; seed++ {
		values := allocateStyledVisualVariables("让朋友拍张生活照", time.Now(), seed, defaultImageVisualDirectorPolicy(), "short", history, style)
		if len(history) > 0 && values["scene"] == history[0].Variables["scene"] {
			t.Fatal("normalized mirror scene defeated scene rotation")
		}
		if values["scene"] != "河畔步道" && values["sourceScene"] != "家中全身镜前" {
			t.Fatalf("mirror scene origin lost: %+v", values)
		}
		history = []visualGenerationPlan{{Variables: values}}
	}
}
