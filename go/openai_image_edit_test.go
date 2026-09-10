package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newGPTImageEditRuntime(t *testing.T, service *httptest.Server, fallback bool) *AgentRuntime {
	t.Helper()
	runtime := newIdleRuntime(t)
	runtime.client = service.Client()
	runtime.mediaDir = t.TempDir()
	t.Setenv("ERDAI_MODEL_API_KEY", "image-test-key")
	if _, err := runtime.configStore.db.Exec(`UPDATE model_endpoints SET enabled = 0 WHERE instr(capabilities_json, '"image_generation"') > 0`); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, runtime.configStore.db, "gpt-edit", "gpt-image-2.5", []string{"image_generation"}, "media", "grok_generate_image")
	bindTestModelConnection(t, runtime.configStore.db, "gpt-edit", service.URL+"/gpt")
	connections := []string{"test-bound-gpt-edit"}
	if fallback {
		insertTestEndpoint(t, runtime.configStore.db, "grok-edit", "grok-imagine-image-lite", []string{"image_generation"}, "media", "grok_generate_image")
		bindTestModelConnection(t, runtime.configStore.db, "grok-edit", service.URL+"/grok")
		connections = append(connections, "test-bound-grok-edit")
	}
	setTestIntegration(t, runtime.configStore.db, "image_policy", map[string]any{"enabled": true})
	setTestIntegration(t, runtime.configStore.db, "grok_policy", map[string]any{
		"enabled": true, "mediaConnectionIds": connections,
		"imageEditModel": "grok-imagine-edit", "apiBase": service.URL + "/unused",
	})
	return runtime
}

func TestGPTImageEditSelectedRouteUsesMultipartReference(t *testing.T) {
	const prompt = "用户明确要求：朋友帮我拍，不照镜、不裁脚；9:16，保留既定人物。"
	reference, err := base64.StdEncoding.DecodeString(strings.SplitN(testVideoPersonaAvatar, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/gpt/images/edits" ||
			r.Header.Get("Authorization") != "Bearer image-test-key" || r.Header.Get("Accept") != "application/json" {
			t.Error("incorrect image route, method or authentication")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := r.ParseMultipartForm(1024 * 1024); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer r.MultipartForm.RemoveAll()
		if len(r.MultipartForm.Value) != 3 || r.FormValue("model") != "gpt-image-2.5" || r.FormValue("prompt") != prompt || r.FormValue("n") != "1" {
			t.Error("model, authoritative prompt or single-image contract changed; unexpected optional fields")
		}
		files := r.MultipartForm.File["image"]
		if len(r.MultipartForm.File) != 1 || len(files) != 1 || files[0].Filename != "reference.png" || files[0].Header.Get("Content-Type") != "image/png" {
			t.Error("expected exactly one PNG image part")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, err := files[0].Open()
		if err != nil {
			t.Error(err)
			return
		}
		data, err := io.ReadAll(file)
		_ = file.Close()
		if err != nil || !bytes.Equal(data, reference) {
			t.Error("reference image bytes changed")
		}
		// A mislabeled JSON response must behave just like other provider calls.
		w.Header().Set("Content-Type", "text/event-stream")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(reference)}}})
	}))
	defer service.Close()
	runtime := newGPTImageEditRuntime(t, service, false)
	result, err := runtime.generateImageOnce(t.Context(), prompt, true, testVideoPersonaAvatar)
	if err != nil || len(result.Attachments) != 1 || calls.Load() != 1 {
		t.Fatalf("edit result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
}

func TestGPTImageEditPreservesDefiniteRejectionFallback(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var gptCalls, grokCalls atomic.Int32
			service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/gpt/images/edits":
					gptCalls.Add(1)
					http.Error(w, "test rejection", status)
				case "/grok/images/edits":
					grokCalls.Add(1)
					var payload struct {
						Model string `json:"model"`
						Image struct {
							URL string `json:"url"`
						} `json:"image"`
						AspectRatio    string `json:"aspect_ratio"`
						ResponseFormat string `json:"response_format"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil ||
						payload.Model != "grok-imagine-edit" || payload.Image.URL != testVideoPersonaAvatar ||
						payload.AspectRatio != "3:4" || payload.ResponseFormat != "b64_json" {
						t.Error("Grok fallback lost its JSON contract, edit model or reference")
					}
					writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"b64_json": strings.SplitN(testVideoPersonaAvatar, ",", 2)[1]}}})
				default:
					t.Error("unexpected image route or text-only fallback")
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer service.Close()
			runtime := newGPTImageEditRuntime(t, service, true)
			result, err := runtime.generateImageOnce(t.Context(), "来一张你的自拍", true, testVideoPersonaAvatar)
			definite := status == http.StatusUnauthorized || status == http.StatusTooManyRequests
			if definite {
				if err != nil || len(result.Attachments) != 1 || grokCalls.Load() != 1 {
					t.Fatalf("definite rejection did not preserve reference fallback: err=%v calls=%d", err, grokCalls.Load())
				}
			} else {
				var response *providerHTTPError
				if !errors.As(err, &response) || response.StatusCode != status || grokCalls.Load() != 0 {
					t.Fatalf("ambiguous rejection retried or lost HTTP status: err=%v calls=%d", err, grokCalls.Load())
				}
			}
			if gptCalls.Load() != 1 {
				t.Fatalf("GPT edit attempts=%d", gptCalls.Load())
			}
		})
	}
}

func TestGPTImageEditRejectsInvalidReferenceBeforeProvider(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer service.Close()
	runtime := newGPTImageEditRuntime(t, service, true)
	for _, test := range []struct{ name, reference string }{
		{"external-url", "https://example.com/reference.png"},
		{"invalid-base64", "data:image/png;base64,invalid!"},
		{"empty-image", "data:image/png;base64,"},
		{"mismatched-content-type", strings.Replace(testVideoPersonaAvatar, "image/png", "image/jpeg", 1)},
		{"oversized", "data:image/png;base64," + strings.Repeat("A", base64.StdEncoding.EncodedLen(maxImageBytes)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := runtime.generateImageOnce(t.Context(), "保留参考图中的人物", true, test.reference); err == nil {
				t.Fatal("invalid reference accepted")
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid reference reached provider or fallback: calls=%d", calls.Load())
	}
}

func TestGPTImageEditCancellationDoesNotFallBack(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		if calls.Add(1) == 1 {
			close(started)
		}
		<-r.Context().Done()
	}))
	defer service.Close()
	runtime := newGPTImageEditRuntime(t, service, true)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := runtime.generateImageOnce(ctx, "保留参考图中的人物", true, testVideoPersonaAvatar)
		done <- err
	}()
	select {
	case <-started:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("edit did not start")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || calls.Load() != 1 {
			t.Fatalf("cancel error=%v calls=%d", err, calls.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("edit ignored parent cancellation")
	}
}

func TestGPTImageEditAcceptsBase64ResponseLargerThanToolLimit(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, maxToolBody))
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"b64_json": encoded}}})
	}))
	defer service.Close()
	runtime := &AgentRuntime{client: service.Client()}
	var response struct {
		Data []struct {
			Base64 string `json:"b64_json"`
		} `json:"data"`
	}
	err := runtime.postGPTImageEdit(t.Context(), service.URL, "test-key", "gpt-image-2.5", "保留人物", testVideoPersonaAvatar, &response)
	if err != nil || len(response.Data) != 1 || response.Data[0].Base64 != encoded {
		t.Fatalf("valid image response was truncated at the text tool limit: %v", err)
	}
}

func TestGPTImageGenerationPreservesJSONAndAcceptsLargeImage(t *testing.T) {
	const prompt = "一张普通城市街角的手机照片，9:16，不添加人物。"
	image, err := base64.StdEncoding.DecodeString(strings.SplitN(testVideoPersonaAvatar, ",", 2)[1])
	if err != nil {
		t.Fatal(err)
	}
	image = append(image, make([]byte, maxToolBody)...)
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/gpt/images/generations" || r.Header.Get("Content-Type") != "application/json" ||
			r.Header.Get("Authorization") != "Bearer image-test-key" {
			t.Error("generation did not use its authenticated JSON endpoint")
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || len(payload) != 3 ||
			payload["model"] != "gpt-image-2.5" || payload["prompt"] != prompt || payload["n"] != float64(1) {
			t.Error("generation changed model/prompt/count or included unsupported Grok/size fields")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": []any{map[string]string{"b64_json": base64.StdEncoding.EncodeToString(image)}}})
	}))
	defer service.Close()
	runtime := newGPTImageEditRuntime(t, service, false)
	result, err := runtime.generateImageOnce(t.Context(), prompt, true, "")
	if err != nil || len(result.Attachments) != 1 || calls.Load() != 1 {
		t.Fatalf("large generation result=%+v calls=%d err=%v", result, calls.Load(), err)
	}
	stored, _, err := runtime.readLocalMedia(result.Attachments[0].LocalPath, maxImageBytes)
	if err != nil || !bytes.Equal(stored, image) {
		t.Fatalf("generated image was truncated or not stored: %v", err)
	}
}
