package main

import (
	"strings"
	"testing"
	"time"
)

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
		if !strings.Contains(got, "手机原生相机的竖拍3:4比例") || !strings.Contains(got, "不预设为前置自拍") {
			t.Fatalf("phone photo prompt lost ordinary-camera guidance for %q: %s", prompt, got)
		}
	}
}
