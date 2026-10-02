package main

import (
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestVisualLifestyleEnabledPreservesExplicitStyles(t *testing.T) {
	for _, prompt := range []string{"来张你的自拍", "你穿短裙拍全身", "不要电影感，随手拍", "不要舞台也不要棚拍", "not cinematic; no dancing", "别用商业写真风格"} {
		if !visualLifestyleEnabled(prompt) {
			t.Fatalf("lifestyle disabled for %q", prompt)
		}
	}
	for _, prompt := range []string{"跳舞给我看", "来个K-pop舞蹈", "在舞台表演", "要棚拍商业写真", "拍张写真", "时尚大片", "电影感视频", "cinematic portrait", "不要电影感，但是来个舞台舞蹈"} {
		if visualLifestyleEnabled(prompt) {
			t.Fatalf("explicit style overridden for %q", prompt)
		}
	}
}

func TestVisualLifestyleInstructionKeepsScopeAndSmallMotion(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		value := visualLifestyleInstruction(kind)
		if utf8.RuneCountInString(value) > 520 {
			t.Fatalf("%s instruction exceeds compact budget", kind)
		}
		for _, marker := range []string{"未指定", "要求优先", "虚构情境", "真实人类事实", "用户说的", "按选定构图", "自然肤质", "脸"} {
			if !strings.Contains(value, marker) {
				t.Fatalf("%s missing %q", kind, marker)
			}
		}
	}
	video := visualLifestyleInstruction("video")
	for _, marker := range []string{"单镜头", "一个主要小动作", "短暂停顿", "不安排表演、转场、慢动作或换装", "明确要求舞蹈时照办"} {
		if !strings.Contains(video, marker) {
			t.Fatalf("video missing %q", marker)
		}
	}
	image := visualLifestyleInstruction("image")
	for _, marker := range []string{"拍摄缘由和手头小事", "桌面杂物", "前置自拍、朋友随手拍、定时拍、镜面拍", "轻微构图偏移", "局部光线不均", "棚拍、海报、壁纸或商业宣传图"} {
		if !strings.Contains(image, marker) {
			t.Fatalf("image missing concrete lifestyle marker %q", marker)
		}
	}
}

func TestVisualLifestyleVariablesCoherentExplicitMoments(t *testing.T) {
	now := time.Date(2026, 12, 1, 14, 0, 0, 0, time.UTC)
	for _, test := range []struct{ prompt, scene, action string }{
		{"你在桌边托腮，笔记本放旁边", "桌边", "明确动作与姿势"},
		{"你在沙发上", "沙发边放松片刻", "靠着坐稳"},
		{"厨房备餐时拍你", "厨房操作台旁", "明确动作与姿势"},
		{"在阳台拍你的照片", "阳台边短暂停留", "轻轻转头"},
		{"给我拍一张你的公园散步照", "楼下步道", "明确动作与姿势"},
		{"你在窗边阅读", "窗边", "明确动作与姿势"},
		{"你在海边拍手机近照", "用户明确指定的场景", "短暂停下"},
	} {
		values := visualLifestyleVariables(test.prompt, now, 7, "short", test.scene)
		if values["scene"] != test.scene || !strings.Contains(values["action"], test.action) {
			t.Fatalf("incoherent %q: %+v", test.prompt, values)
		}
		if len(values) != 8 || values["activity"] == "" || values["cameraAngle"] == "" || !strings.Contains(values["outfit"], "膝上") {
			t.Fatalf("missing lifestyle variables: %+v", values)
		}
		if strings.Contains(values["outfit"], "大衣") || strings.Contains(values["scene"], "雨") {
			t.Fatalf("invented weather or heavy indoor clothing: %+v", values)
		}
	}
}

func TestVisualLifestyleVariablesIgnoreUserLocation(t *testing.T) {
	now := time.Date(2026, 9, 7, 23, 0, 0, 0, time.UTC)
	for seed := uint64(0); seed < 30; seed++ {
		plain := visualLifestyleVariables("来张你的自拍", now, seed, "long", "")
		for _, prompt := range []string{"我在办公室，来张你的自拍", "我们在厨房，来张你的自拍", "我在办公室拍材料，来张自拍", "我在办公室想看你的自拍", "我坐在沙发上想看你的自拍", "我刚到办公室想看你的自拍", "我在办公室你在干嘛，给我自拍", "我在厨房做饭想看你的自拍", "不要去阳台，来张你的自拍"} {
			got := visualLifestyleVariables(prompt, now, seed, "long", "")
			if !reflect.DeepEqual(plain, got) {
				t.Fatalf("user location became role location: %q %+v", prompt, got)
			}
		}
		if !strings.Contains(plain["outfit"], "长裙或长裤") || strings.Contains(plain["light"], "自然环境光") {
			t.Fatalf("length/night context lost: %+v", plain)
		}
	}
}

func TestVisualLifestyleRoleLocationSeparatesMixedSubjects(t *testing.T) {
	for _, clause := range []string{"我刚到办公室想看你的自拍", "我在办公室你在干嘛", "我在厨房做饭想看你的自拍"} {
		if got := visualRoleLocationClause(clause); got != "" {
			t.Fatalf("user situation survived: %q => %q", clause, got)
		}
	}
	for _, test := range []struct{ input, want string }{
		{"我在办公室但你在海边自拍", "你在海边自拍"},
		{"你在海边自拍我刚到办公室", "你在海边自拍"},
		{"我坐在办公室想看你站着自拍", "你站着自拍"},
		{"我不想看你在厨房", "不要你在厨房"},
	} {
		if got := visualRoleLocationClause(test.input); got != test.want {
			t.Fatalf("role clause = %q, want %q", got, test.want)
		}
	}
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	got := visualLifestyleVariables("我在办公室但你在海边自拍", now, 4, "short", "")
	want := visualLifestyleVariables("你在海边自拍", now, 4, "short", "")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed subject changed role scene: %+v / %+v", got, want)
	}
}

func TestVisualLifestyleActionSpecifiedSeparatesSubjects(t *testing.T) {
	for _, prompt := range []string{"我坐在办公室想看你的自拍", "我正在厨房做饭，给我自拍", "我在办公室你在干嘛", "不要站着，随手拍", "来张车站自拍", "runtime portrait"} {
		if visualLifestyleActionSpecified(prompt) {
			t.Fatalf("non-role action selected: %q", prompt)
		}
	}
	for _, prompt := range []string{"你站着拍", "坐着自拍", "躺着看手机", "你走几步", "走路自拍", "你在桌边托腮", "我坐在办公室想看你站着自拍", "我在厨房，但你在海边散步", "you are sitting", "拿起杯子"} {
		if !visualLifestyleActionSpecified(prompt) {
			t.Fatalf("explicit role action lost: %q", prompt)
		}
	}
}

func TestVisualLifestyleExplicitPostureDoesNotPickConflictingMoment(t *testing.T) {
	now := time.Date(2026, 12, 1, 23, 0, 0, 0, time.UTC)
	for seed := uint64(0); seed < 20; seed++ {
		for _, prompt := range []string{"坐着自拍", "站着自拍", "躺着看手机", "你走几步", "走路自拍"} {
			value := visualLifestyleVariables(prompt, now, seed, "long", "")
			if value["scene"] != "与本次明确动作相符的环境，不额外指定地点" ||
				value["action"] != "遵循本次明确动作与姿势，不追加其他动作" ||
				value["activity"] != "接续本次明确动作，不新增另一项活动" || strings.Contains(value["light"], "已有室内") {
				t.Fatalf("default moment conflicts with %q: %+v", prompt, value)
			}
		}
	}
	value := visualLifestyleVariables("你站在沙发边自拍", now, 4, "short", "")
	if value["scene"] != "沙发边" || strings.Contains(value["action"], "坐") || strings.Contains(value["activity"], "坐") {
		t.Fatalf("explicit standing scene replaced by sitting: %+v", value)
	}
}

func TestVisualLifestyleUserLocationClause(t *testing.T) {
	for _, clause := range []string{"我在办公室想看你的自拍", "我们正在厨房", "我坐在沙发上", "I am in the kitchen"} {
		if !visualUserLocationClause(clause) {
			t.Fatalf("user location not recognized: %q", clause)
		}
	}
	for _, clause := range []string{"给我拍你在办公室的照片", "我想看你在桌边托腮", "厨房备餐时拍你", "你在阳台"} {
		if visualUserLocationClause(clause) {
			t.Fatalf("explicit role location rejected: %q", clause)
		}
	}
}

func TestVisualLifestyleVariablesVarietyAndPreviousScene(t *testing.T) {
	seen := map[string]bool{}
	previous := ""
	for seed := uint64(0); seed < 120; seed++ {
		now := time.Date(2026, 9, 7, int(seed%24), 0, 0, 0, time.UTC)
		values := visualLifestyleVariables("随手拍", now, seed, "", previous)
		if values["scene"] == previous {
			t.Fatalf("default scene repeated: %+v", values)
		}
		if strings.Contains(values["scene"], "咖啡") || strings.Contains(values["scene"], "棚") || strings.Contains(values["action"], "杯") {
			t.Fatalf("fixed studio/cup default: %+v", values)
		}
		seen[values["scene"]], previous = true, values["scene"]
	}
	if len(seen) < 6 {
		t.Fatalf("too few daily moments: %+v", seen)
	}
}

func TestVisualLifestyleCameraAngleVariesAndRespectsExplicitRequest(t *testing.T) {
	seen := map[string]bool{}
	previous := ""
	now := time.Date(2026, 9, 7, 14, 0, 0, 0, time.UTC)
	for seed := uint64(0); seed < 80; seed++ {
		values := allocateVisualVariables("来张你的手机生活照", now, seed, defaultImageVisualDirectorPolicy(), "short", func() []visualGenerationPlan {
			if previous == "" {
				return nil
			}
			return []visualGenerationPlan{{Variables: map[string]string{"cameraAngle": previous}}}
		}())
		if values["cameraAngle"] == "" || values["cameraAngle"] == previous {
			t.Fatalf("phone camera angle repeated or missing: previous=%q values=%+v", previous, values)
		}
		seen[values["cameraAngle"]] = true
		previous = values["cameraAngle"]
	}
	if len(seen) < 4 {
		t.Fatalf("too few phone camera angles: %+v", seen)
	}
	for _, prompt := range []string{"给我一张侧前方平视的生活照", "来张低机位自拍", "从上面俯拍一张"} {
		value := visualLifestyleVariables(prompt, now, 7, "short", "")
		if !strings.Contains(value["cameraAngle"], "用户明确指定") {
			t.Fatalf("explicit camera angle was replaced for %q: %+v", prompt, value)
		}
	}
}

func TestVisualLifestyleExpandedDailyMomentsAppear(t *testing.T) {
	seen := map[string]bool{}
	previous := ""
	for seed := uint64(0); seed < 240; seed++ {
		now := time.Date(2026, 9, 7, int(seed%24), 0, 0, 0, time.UTC)
		value := visualLifestyleVariables("随手拍", now, seed, "short", previous)
		seen[value["scene"]] = true
		previous = value["scene"]
	}
	for _, marker := range []string{"床边整理小物", "便利店门口等人", "电梯口等电梯", "窗边照料绿植"} {
		if !seen[marker] {
			t.Fatalf("expanded daily moment never selected: %q; seen=%+v", marker, seen)
		}
	}
}
