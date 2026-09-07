package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestVideoLifestyleAspectRatioLastPositiveAndNegation(t *testing.T) {
	for _, sample := range []struct{ prompt, ratio string }{
		{"横屏自拍视频，不要竖屏", "16:9"},
		{"不要竖屏，横屏自拍视频", "16:9"},
		{"不要9:16，改成16:9", "16:9"},
		{"16:9，不要9:16", "16:9"},
		{"不要横屏，竖屏自拍视频", "9:16"},
		{"竖屏自拍视频，不要横屏", "9:16"},
		{"9:16，改成16:9", "16:9"},
		{"横屏，改成竖版", "9:16"},
		{"竖屏，横屏，最后方形", "1:1"},
		{"横屏，方形，不要方形", "16:9"},
		{"不要横屏，横屏", "16:9"},
		{"不要竖屏要横屏", "16:9"},
		{"不要9:16竖屏，要横屏", "16:9"},
		{"竖屏不要，横屏", "16:9"},
		{"不要竖屏或横屏", "1:1"},
		{"拍成 4 ： 3，不要 3 : 4", "4:3"},
		{"4:3，改成3:4", "3:4"},
		{"landscape selfie video, not portrait", "16:9"},
		{"not portrait; use landscape", "16:9"},
		{"no 9:16, use 16:9", "16:9"},
		{"16:9, not 9:16", "16:9"},
		{"horizontal instead of vertical", "16:9"},
		{"vertical, not horizontal", "9:16"},
		{"portrait but horizontal", "16:9"},
		{"landscape but vertical", "9:16"},
		{"not landscape or portrait; square", "1:1"},
		{"avoid square, use 4 : 3", "4:3"},
		{"3:4 but 1:1", "1:1"},
		{"普通自拍视频", "9:16"},
		{"自拍视频，不要竖屏", "16:9"},
		{"普通视频", "16:9"},
		{"普通视频，不要横屏", "9:16"},
	} {
		t.Run(sample.prompt, func(t *testing.T) {
			options := videoGenerationOptionsForPrompt(sample.prompt)
			if options.AspectRatio != sample.ratio || options.Duration != 6 || options.Resolution != "720p" {
				t.Fatalf("options=%+v, want ratio=%s with unchanged duration/resolution", options, sample.ratio)
			}
		})
	}
}

func TestVideoLifestyleAspectRatioCompiledPromptAndRequest(t *testing.T) {
	plan := visualGenerationPlan{MediaType: "video", AppearanceID: "appearance", Identity: "明确成年，固定同一张脸，portrait 1080p",
		UserPrompt: "给我横屏自拍视频，不要竖屏，480p", OutfitLength: "short",
		Variables: map[string]string{"scene": "桌边休息", "camera": "近景自拍"}}
	prompt, err := compileVisualGenerationPrompt(plan, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "portrait 1080p") || !strings.Contains(prompt, "单镜头") {
		t.Fatal("compiled fixture lost conflicting identity or lifestyle instructions")
	}
	request := map[string]any{}
	if err := applyVideoGenerationOptions(request, plan.UserPrompt, "grok-imagine-video"); err != nil {
		t.Fatal(err)
	}
	if request["aspect_ratio"] != "16:9" || request["resolution"] != "480p" || request["duration"] != 6 {
		t.Fatalf("compiled prompt request options=%+v", request)
	}
}

func TestVideoLifestyleAspectRatioProviderRequestUsesUserPrompt(t *testing.T) {
	captured := make(chan map[string]any, 1)
	var creates atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			creates.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			select {
			case captured <- body:
			default:
				t.Error("unexpected repeat video generation")
			}
			writeJSON(w, http.StatusOK, map[string]string{"id": "options-video", "status": "completed"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/options-video/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4())
		default:
			t.Errorf("unexpected mock provider request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	t.Setenv("ERDAI_MEDIA_CHECK_URL", "")
	runtime := newVideoRuntime(t, videoTestConfig(t, provider.URL+"/grok", 10), provider.Client(), t.TempDir(), time.Millisecond, 1)
	defer runtime.Close()
	setTestIntegration(t, runtime.configStore.db, "image_policy", map[string]any{"enabled": true, "mediaQualityEnabled": false})
	updated, err := runtime.configStore.db.Exec(`UPDATE appearance_libraries SET visual_description=?
		WHERE id=(SELECT library_id FROM persona_appearance_libraries WHERE persona_id='doubao')`,
		"明确成年，固定同一张脸，portrait 1080p")
	if err != nil {
		t.Fatal(err)
	}
	if count, err := updated.RowsAffected(); err != nil || count != 1 {
		t.Fatalf("appearance fixture update: rows=%d err=%v", count, err)
	}
	run := visualPlanTestRun(t, runtime, "video-lifestyle-options")
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	const userPrompt = "给我横屏自拍视频，不要竖屏，480p"
	result, err := runtime.generateVideo(ctx, run, userPrompt)
	if err != nil || len(result.Attachments) != 1 || result.Attachments[0].Kind != "video" {
		t.Fatalf("local video generation result=%+v err=%v", result, err)
	}
	if creates.Load() != 1 {
		t.Fatalf("create calls=%d, want exactly one local mock request", creates.Load())
	}
	select {
	case body := <-captured:
		prompt, _ := body["prompt"].(string)
		if body["aspect_ratio"] != "16:9" || body["resolution"] != "480p" || body["duration"] != float64(6) {
			t.Fatalf("identity contaminated actual provider options: aspect=%v resolution=%v duration=%v", body["aspect_ratio"], body["resolution"], body["duration"])
		}
		for _, marker := range []string{userPrompt, "portrait 1080p", "单镜头"} {
			if !strings.Contains(prompt, marker) {
				t.Fatalf("actual provider prompt omitted %q", marker)
			}
		}
	default:
		t.Fatal("generation did not reach the local provider")
	}
}

func TestVideoLifestyleAspectRatioAllExcludedDoesNotSubmitOptions(t *testing.T) {
	request := map[string]any{}
	err := applyVideoGenerationOptions(request, "不要16:9，不要9:16，不要1:1，不要4:3，不要3:4", "grok-imagine-video")
	if err == nil || len(request) != 0 {
		t.Fatalf("unsupported exclusions assigned provider options: request=%+v err=%v", request, err)
	}
}
