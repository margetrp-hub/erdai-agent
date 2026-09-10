package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestVisualPlanPhoneRatioReachesProviderDespiteDefaultStyle(t *testing.T) {
	captured := make(chan map[string]any, 1)
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/gpt/images/edits":
			http.Error(w, "rejected before execution", http.StatusUnauthorized)
		case "/grok/images/edits":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
			}
			select {
			case captured <- payload:
			default:
				t.Error("unexpected extra generation")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"b64_json": strings.SplitN(testVideoPersonaAvatar, ",", 2)[1]}}})
		default:
			t.Error("unexpected route: " + r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer service.Close()
	runtime := newGPTImageEditRuntime(t, service, true)
	addVisualPlanReference(t, runtime, "doubao")
	setVisualStyleProfile(t, runtime.configStore, "doubao", `{"visualPromptOverride":"默认竖拍3:4，身份参考图9:16"}`)
	run := visualPlanTestRun(t, runtime, "phone-ratio-provider")
	result, err := runtime.generateImageForRun(t.Context(), run, "给个自拍，4:3横屏，朋友帮拍", true)
	if err != nil || len(result.Attachments) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	var payload map[string]any
	select {
	case payload = <-captured:
	default:
		t.Fatal("provider was not called")
	}
	gotRatio, _ := payload["aspect_ratio"].(string)
	gotPrompt, _ := payload["prompt"].(string)
	if gotRatio != "4:3" || !strings.Contains(gotPrompt, "本次照片比例：4:3") {
		t.Fatalf("provider ratio=%q; explicit ratio instruction present=%v", gotRatio, strings.Contains(gotPrompt, "本次照片比例：4:3"))
	}
}

func TestImageAspectRatioUsesNativePhonePhotoFraming(t *testing.T) {
	for _, sample := range []struct {
		name, prompt, want string
	}{
		{"plain-selfie", "给我一张你的自拍", "3:4"},
		{"phone-life-photo", "给我一张你的手机生活照", "3:4"},
		{"vertical-wording", "给我一张你的照片，竖拍", "3:4"},
		{"full-width-colon", "给我一张你的照片，3：4", "3:4"},
		{"explicit-story", "给我一张你的照片，9:16", "9:16"},
		{"explicit-landscape", "给我一张你的照片，16：9", "16:9"},
		{"landscape-wording", "给我一张你的照片，横屏", "16:9"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			if got := imageAspectRatioForPrompt(sample.prompt); got != sample.want {
				t.Fatalf("image aspect ratio = %q, want %q for %q", got, sample.want, sample.prompt)
			}
		})
	}
}

func TestImageAspectRatioHonorsLatestExplicitCorrection(t *testing.T) {
	for _, sample := range []struct {
		prompt, want string
	}{
		{"不要9:16，改成3:4", "3:4"},
		{"9：16不要，换成4：3", "4:3"},
		{"不要横屏，改成竖拍", "3:4"},
	} {
		if got := imageAspectRatioForPrompt(sample.prompt); got != sample.want {
			t.Fatalf("latest image ratio correction = %q, want %q for %q", got, sample.want, sample.prompt)
		}
	}
}

func TestImageAspectRatioKeepsExactRatioAndTextualCorrectionOrder(t *testing.T) {
	for _, sample := range []struct {
		prompt, want string
	}{
		{"9:16竖拍", "9:16"},
		{"竖拍9:16", "9:16"},
		{"9：16 portrait", "9:16"},
		{"4:3横屏", "4:3"},
		{"9:16 3:4", "3:4"},
		{"3:4 9:16", "9:16"},
		{"9:16改成3:4", "3:4"},
		{"3:4改成9:16竖拍", "9:16"},
		{"不要9:16用3:4", "3:4"},
		{"不要9:16，改成3:4竖拍", "3:4"},
		{"9:16不要，改成4:3横屏", "4:3"},
		{"9:16，改成横屏", "16:9"},
	} {
		t.Run(sample.prompt, func(t *testing.T) {
			if got := imageAspectRatioForPrompt(sample.prompt); got != sample.want {
				t.Fatalf("image aspect ratio = %q, want %q for %q", got, sample.want, sample.prompt)
			}
		})
	}
}

func TestPersonaImagePromptDoesNotBiasEveryPhonePhotoToFrontCamera(t *testing.T) {
	persona := &nativeActivePersona{
		ID:                "doubao",
		VisualDescription: "明确成年的年轻女性，现实手机摄影。",
	}
	for _, prompt := range []string{
		"给我一张你的自拍，让朋友拍",
		"给我一张你的生活照，定时拍",
		"给我一张你的镜面自拍",
	} {
		got := personaImagePromptAt(prompt, persona, time.Date(2026, 9, 10, 12, 0, 0, 0, time.FixedZone("CST", 8*60*60)), defaultImageVisualDirectorPolicy(), 7)
		if strings.Contains(got, "手机前置镜头") {
			t.Fatalf("phone photo prompt still forced front camera for %q: %s", prompt, got)
		}
		if !strings.Contains(got, "手机原生相机的3:4比例") || !strings.Contains(got, "不预设为前置自拍") {
			t.Fatalf("phone photo prompt lost ordinary-camera guidance for %q: %s", prompt, got)
		}
	}
}
