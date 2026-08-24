package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestTTSRequestRequiresVoiceIntentUnlessAlwaysEnabled(t *testing.T) {
	policy := grokTTSPolicy{
		TriggerKeywords: []string{"\u53d1\u8bed\u97f3", "tts"},
	}
	if ttsRequested("\u4f60\u597d\u5417", policy) {
		t.Fatal("ordinary chat unexpectedly requested TTS")
	}
	if !ttsRequested("\u8bf7\u53d1\u8bed\u97f3\u56de\u590d", policy) {
		t.Fatal("voice request was not recognized")
	}
	policy.Always = true
	if !ttsRequested("\u4f60\u597d\u5417", policy) {
		t.Fatal("always-enabled TTS did not trigger")
	}
}

func TestMaybeAttachPersonaSpeechStoresMP3AndUsesOfficialPayload(t *testing.T) {
	var received struct {
		Text     string `json:"text"`
		VoiceID  string `json:"voice_id"`
		Language string `json:"language"`
	}
	audio := []byte("ID3\x04\x00test-audio")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tts" {
			t.Fatalf("TTS request = %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer tts-test-key" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(audio)
	}))
	defer server.Close()

	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = server.Client()
	runtime.grokAPIKey = "tts-test-key"
	runtime.mediaDir = filepath.Join(t.TempDir(), "media")
	setTestIntegration(t, runtime.configStore.db, "grok_policy", map[string]any{
		"ttsEnabled": true, "ttsApiBase": server.URL + "/v1",
		"ttsCredentialRef": "ERDAI_GROK_API_KEY", "ttsVoiceId": "eve",
		"ttsLanguage": "zh", "ttsPersonaIds": []string{"xiaoman"},
		"ttsTriggerKeywords": []string{"\u53d1\u8bed\u97f3"}, "ttsMaxChars": 240,
	})

	reply := runtime.maybeAttachPersonaSpeech(
		context.Background(),
		runRecord{ID: "run-tts", EventID: "event-tts", PersonaID: "xiaoman"},
		"\u8bf7\u53d1\u8bed\u97f3",
		agentReply{Text: "\u4f60\u597d\uff0c\u6211\u662f\u5c0f\u6ee1\u3002"},
	)
	if len(reply.Attachments) != 1 {
		t.Fatalf("attachments = %+v", reply.Attachments)
	}
	attachment := reply.Attachments[0]
	if attachment.Kind != "audio" || attachment.MimeType != "audio/mpeg" ||
		!bytes.HasSuffix([]byte(attachment.LocalPath), []byte(".mp3")) {
		t.Fatalf("audio attachment = %+v", attachment)
	}
	if received.Text == "" || received.VoiceID != "eve" || received.Language != "zh" {
		t.Fatalf("TTS payload = %+v", received)
	}
	stored, err := os.ReadFile(filepath.Join(runtime.mediaDir, filepath.Base(attachment.LocalPath)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, audio) {
		t.Fatalf("stored audio = %q", stored)
	}
}
