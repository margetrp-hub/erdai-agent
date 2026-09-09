package main

import (
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestVisualCaptureExplicitMethodsAndExclusions(t *testing.T) {
	for _, test := range []struct {
		prompt, mode string
		forbidden    []string
	}{
		{"给我一张别人拍的手机生活照", "other_person", nil},
		{"让朋友拍你的全身照，不拿手机，不要镜子", "other_person", nil},
		{"这次要前置近景自拍", "front_selfie", nil},
		{"用定时拍全身", "timer", nil},
		{"照镜自拍，半身就好", "mirror_selfie", nil},
		{"请对镜拍一张", "mirror_selfie", nil},
		{"自拍，不要镜子", "", []string{"mirror_selfie"}},
		{"给我自拍，不拿手机", "", []string{"front_selfie", "mirror_selfie"}},
		{"来张不照镜的自拍", "", []string{"mirror_selfie"}},
		{"手机随手拍，不要手持自拍，不要镜拍", "", []string{"front_selfie", "mirror_selfie"}},
		{"photo taken by a friend, no mirror, no phone", "other_person", nil},
		{"front camera selfie", "front_selfie", nil},
		{"timer photo, no mirror", "timer", nil},
		{"mirror selfie", "mirror_selfie", nil},
		{"selfie without holding a phone", "", []string{"front_selfie", "mirror_selfie"}},
	} {
		t.Run(test.prompt, func(t *testing.T) {
			for seed := uint64(0); seed < 12; seed++ {
				values := map[string]string{"camera": "半身生活照", "scene": "家中全身镜前", "action": "自然看向手机"}
				applyVisualCapture(test.prompt, seed, nil, values)
				if test.mode != "" && values["captureMode"] != test.mode || slices.Contains(test.forbidden, values["captureMode"]) {
					t.Fatalf("capture ignored request: %q %+v", test.prompt, values)
				}
				if values["captureMode"] != "mirror_selfie" && values["scene"] == "家中全身镜前" {
					t.Fatal("default mirror scene bypassed capture selection")
				}
				if test.mode == "other_person" && !strings.Contains(values["capture"], "人物不是拍摄者") {
					t.Fatal("other-person request lacked an explicit non-selfie instruction")
				}
			}
		})
	}
}

func TestVisualCaptureRotatesCompatibleViewsWithoutLockingGenericSelfie(t *testing.T) {
	for _, frame := range []string{"近景自拍", "半身生活照", "全身生活照"} {
		history := []visualGenerationPlan{}
		seen := map[string]bool{}
		for seed := uint64(0); seed < 40; seed++ {
			values := map[string]string{"camera": frame, "scene": "家中客厅", "action": "自然停下"}
			applyVisualCapture("发张你的自拍，普通手机质感", seed, history, values)
			mode := values["captureMode"]
			if frame == "全身生活照" && mode == "front_selfie" {
				t.Fatal("full-body default selected arm-length front selfie")
			}
			for _, previous := range history[:min(2, len(history))] {
				if mode == previous.Variables["captureMode"] {
					t.Fatal("capture repeated one of the previous two modes despite available alternatives")
				}
			}
			seen[mode] = true
			history = append([]visualGenerationPlan{{Variables: values}}, history...)
		}
		if !seen["other_person"] || !seen["timer"] || !seen["mirror_selfie"] || frame != "全身生活照" && !seen["front_selfie"] {
			t.Fatalf("generic phone/selfie prompt was locked to one method: %s %+v", frame, seen)
		}
	}
	history := []visualGenerationPlan{{Variables: map[string]string{"captureMode": "other_person"}}, {Variables: map[string]string{"captureMode": "timer"}}}
	values := map[string]string{"camera": "全身生活照"}
	applyVisualCapture("不要镜子，不拿手机", 5, history, values)
	if values["captureMode"] != "timer" {
		t.Fatalf("relaxing recent-mode exclusion crossed explicit prohibitions: %+v", values)
	}
	applyVisualCapture("这次还是让朋友拍", 5, history, values)
	if values["captureMode"] != "other_person" {
		t.Fatal("history overrode explicit repeated shooting method")
	}
}

func TestVisualCaptureDefaultMirrorRequiresCompatibleScene(t *testing.T) {
	for _, scene := range []string{"河畔步道", "咖啡店外摆", "城市街角", "阳台边短暂停留", "户外的普通环境"} {
		for seed := uint64(0); seed < 32; seed++ {
			values := map[string]string{"camera": "全身生活照", "scene": scene}
			applyVisualCapture("发张手机生活照", seed, nil, values)
			if values["captureMode"] == "mirror_selfie" {
				t.Fatalf("default invented an outdoor mirror: %s", scene)
			}
		}
	}
	values := map[string]string{"camera": "全身生活照", "scene": "河畔步道"}
	applyVisualCapture("在这里照镜自拍", 7, nil, values)
	if values["captureMode"] != "mirror_selfie" {
		t.Fatal("scene heuristic overrode explicit mirror request")
	}
}

func TestVisualCaptureReconcilePreservesPropsAndExplicitMirrorLocation(t *testing.T) {
	values := map[string]string{"camera": "近景自拍", "captureMode": "other_person", "scene": "家中全身镜前", "action": "人物拿手机看消息"}
	const prompt = "在镜子前让朋友拍，我要看你手里拿手机刷消息"
	reconcileVisualCapture(prompt, values)
	if values["action"] != "人物拿手机看消息" || !strings.Contains(values["capture"], "仅为生活道具") || !strings.Contains(values["scene"], "用户明确的镜前地点") || strings.Contains(values["camera"], "自拍") {
		t.Fatalf("capture conflated a phone prop or explicit location with taking a selfie: %+v", values)
	}
	before := map[string]string{}
	for key, value := range values {
		before[key] = value
	}
	reconcileVisualCapture(prompt, values)
	if !reflect.DeepEqual(values, before) {
		t.Fatal("post-continuity capture reconciliation is not idempotent")
	}
	values["scene"], values["action"] = "家中全身镜前", "按指定取景保留完整身体、穿搭或镜面关系，自然看向手机"
	reconcileVisualCapture("别人拍，不要镜子，不拿手机", values)
	if values["scene"] == "家中全身镜前" || strings.Contains(values["action"], "看向手机") || strings.Contains(values["action"], "镜面关系") {
		t.Fatalf("continuity reintroduced mirror/phone-camera defaults: %+v", values)
	}
}

func TestVisualCaptureFramingKeepsOnlyComposition(t *testing.T) {
	for _, test := range []struct{ prompt, kind string }{
		{"半身对镜自拍", "半身生活照"}, {"朋友拍的近景生活照", "近景自拍"}, {"全身前置自拍", "全身穿搭照"},
		{"给我别人拍的照片", ""}, {"镜子前自拍", ""}, {"普通手机质感抓拍", ""},
	} {
		if got := explicitSelfieType(visualCaptureFramingText(test.prompt)); got != test.kind {
			t.Fatalf("capture consumed framing %q: %q", test.prompt, got)
		}
	}
}

func TestVisualCaptureRunsWithAndWithoutStyleAndPreservesFraming(t *testing.T) {
	now := time.Now()
	style := &visualStyleDefaults{SelfieTypes: []string{"全身生活照"}, Scenes: []string{"家中全身镜前"}, Outfits: []string{"短衬衫配短裤"}}
	for _, defaults := range []*visualStyleDefaults{nil, {}, style} {
		for _, prompt := range []string{"让朋友拍你的近景生活照，不要镜子", "让朋友拍你的半身照，不拿手机", "让朋友拍你的全身照"} {
			values := allocateStyledVisualVariables(prompt, now, 9, defaultImageVisualDirectorPolicy(), "short", nil, defaults)
			if values["captureMode"] != "other_person" || values["capture"] == "" {
				t.Fatalf("real styled allocator did not apply capture: %+v", values)
			}
			for _, frame := range []string{"近景", "半身", "全身"} {
				if strings.Contains(prompt, frame) && !strings.Contains(values["camera"], frame) {
					t.Fatalf("capture changed explicit framing: %q %+v", prompt, values)
				}
			}
		}
	}
}

func TestVisualCapturePhoneVisibilityDoesNotForbidFrontSelfie(t *testing.T) {
	for _, prompt := range []string{"前置近景自拍，手机不入镜", "手持自拍，不要出现手机", "front camera selfie, phone out of frame", "front camera selfie, no phone in frame"} {
		values := map[string]string{"camera": "近景自拍", "scene": "家中客厅"}
		applyVisualCapture(prompt, 3, nil, values)
		if values["captureMode"] != "front_selfie" || strings.Contains(values["capture"], "双手不拿手机") || !strings.Contains(values["capture"], "保持画面外") {
			t.Fatalf("phone visibility was interpreted as no handheld camera: %s %+v", prompt, values)
		}
	}
	for _, prompt := range []string{"前置近景自拍，不拿手机", "前置自拍，不要手机", "front camera selfie, without holding a phone"} {
		values := map[string]string{"camera": "近景自拍", "scene": "家中客厅"}
		applyVisualCapture(prompt, 3, nil, values)
		if values["captureMode"] == "front_selfie" || values["captureMode"] == "mirror_selfie" || !strings.Contains(values["capture"], "双手不拿手机") {
			t.Fatalf("no-holding request allowed a handheld camera: %s %+v", prompt, values)
		}
	}
	for seed := uint64(0); seed < 16; seed++ {
		values := map[string]string{"camera": "近景自拍", "scene": "家中客厅"}
		applyVisualCapture("自拍，手机不入镜", seed, nil, values)
		if values["captureMode"] == "mirror_selfie" {
			t.Fatal("phone-out-of-frame request selected a visible mirror phone")
		}
	}
}

func TestVisualCaptureRevalidatesFinalSceneAfterContinuity(t *testing.T) {
	history := []visualGenerationPlan{{Variables: map[string]string{"captureMode": "other_person"}}, {Variables: map[string]string{"captureMode": "timer"}}}
	previous := visualGenerationPlan{Variables: map[string]string{"scene": "河畔步道", "outfit": "原来的短衬衫"}}
	for _, test := range []struct{ prompt, want string }{
		{"同一场景接着拍", "timer"},
		{"同一场景接着拍，不要定时拍摄", "other_person"},
		{"同一场景接着拍，不要定时拍摄，不要别人拍", "user_allowed"},
		{"同一场景接着照镜自拍", "mirror_selfie"},
	} {
		current := visualGenerationPlan{UserPrompt: test.prompt, Variables: map[string]string{"camera": "全身生活照", "scene": "家中客厅"}}
		applyVisualCapture(test.prompt, 7, history, current.Variables)
		if current.Variables["captureMode"] != "mirror_selfie" {
			t.Fatalf("fixture did not select indoor mirror capture: %+v", current.Variables)
		}
		applyVisualContinuity(&current, &previous)
		if current.Variables["scene"] != "河畔步道" {
			t.Fatal("fixture did not restore the outdoor continuity scene")
		}
		reconcileVisualCapture(test.prompt, current.Variables)
		if current.Variables["captureMode"] != test.want || current.Variables["scene"] != "河畔步道" {
			t.Fatalf("final-scene reconciliation overrode scene or capture constraints: %s %+v", test.prompt, current.Variables)
		}
		reconcileVisualCapture(test.prompt, current.Variables)
		if current.Variables["captureMode"] != test.want {
			t.Fatal("final-scene reconciliation was not stable")
		}
	}
}
