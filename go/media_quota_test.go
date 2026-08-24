package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMediaQuotaReservationIsAtomic(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 3, 3); err != nil {
		t.Fatal(err)
	}

	const attempts = 24
	start := make(chan struct{})
	reservations := make(chan *mediaQuotaReservation, attempts)
	errorsSeen := make(chan error, attempts)
	var workers sync.WaitGroup
	for range attempts {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			reservation, err := runtime.mediaQuota.reserve(context.Background(), "same-sender", mediaKindImage)
			if err == nil {
				reservations <- reservation
				return
			}
			var exceeded *mediaQuotaExceededError
			if !errors.As(err, &exceeded) {
				errorsSeen <- err
			}
		}()
	}
	close(start)
	workers.Wait()
	close(reservations)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("unexpected reservation error: %v", err)
	}

	granted := 0
	for reservation := range reservations {
		granted++
		if err := reservation.commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if granted != 3 {
		t.Fatalf("granted reservations = %d, want 3", granted)
	}
	used, reserved := mediaQuotaUsage(t, runtime, "same-sender", mediaKindImage)
	if used != 3 || reserved != 0 {
		t.Fatalf("usage = used %d, reserved %d", used, reserved)
	}
}

func TestMediaQuotaReleasesFailureAndCountsSuccess(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	run := runRecord{SenderRef: "failure-sender"}

	_, err := runtime.executeQuotaMedia(context.Background(), run, mediaKindImage, func() (toolResult, error) {
		return toolResult{}, errors.New("provider failed")
	})
	if err == nil {
		t.Fatal("failed generation returned no error")
	}
	used, reserved := mediaQuotaUsage(t, runtime, run.SenderRef, mediaKindImage)
	if used != 0 || reserved != 0 {
		t.Fatalf("failed usage = used %d, reserved %d", used, reserved)
	}

	_, err = runtime.executeQuotaMedia(context.Background(), run, mediaKindImage, func() (toolResult, error) {
		return toolResult{Content: `{"ok":true}`}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	used, reserved = mediaQuotaUsage(t, runtime, run.SenderRef, mediaKindImage)
	if used != 1 || reserved != 0 {
		t.Fatalf("successful usage = used %d, reserved %d", used, reserved)
	}
	if _, err := runtime.mediaQuota.reserve(context.Background(), run.SenderRef, mediaKindImage); !isQuotaExceeded(err, mediaKindImage) {
		t.Fatalf("second generation error = %v", err)
	}
}

func TestMediaQuotaUsesShanghaiDayAndKeepsKindsIndependent(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	current := time.Date(2026, 8, 2, 15, 59, 0, 0, time.UTC)
	runtime.mediaQuota.now = func() time.Time { return current }

	image, err := runtime.mediaQuota.reserve(context.Background(), "day-sender", mediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := image.commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.mediaQuota.reserve(context.Background(), "day-sender", mediaKindImage); !isQuotaExceeded(err, mediaKindImage) {
		t.Fatalf("same-day image error = %v", err)
	}
	video, err := runtime.mediaQuota.reserve(context.Background(), "day-sender", mediaKindVideo)
	if err != nil {
		t.Fatalf("video quota was not independent: %v", err)
	}
	if err := video.commit(context.Background()); err != nil {
		t.Fatal(err)
	}

	current = time.Date(2026, 8, 2, 16, 1, 0, 0, time.UTC)
	nextDayImage, err := runtime.mediaQuota.reserve(context.Background(), "day-sender", mediaKindImage)
	if err != nil {
		t.Fatalf("new Shanghai day did not reset quota: %v", err)
	}
	if nextDayImage.day != "2026-08-03" {
		t.Fatalf("quota day = %s", nextDayImage.day)
	}
	if err := nextDayImage.release(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMediaQuotaAdminAPIIsStrictAndUsesGatewayBoundary(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	defaults, err := runtime.mediaQuota.config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if defaults.ImageDailyLimit != 3 || defaults.VideoDailyLimit != 3 ||
		!defaults.TrustedAdminBypass || len(defaults.Whitelist) != 0 {
		t.Fatalf("default quota config = %+v", defaults)
	}
	imageReply := mediaQuotaReply(mediaKindImage)
	videoReply := mediaQuotaReply(mediaKindVideo)
	if !isMediaQuotaReply(mediaKindImage, imageReply) || !isMediaQuotaReply(mediaKindVideo, videoReply) ||
		len([]rune(imageReply)) > 24 || len([]rune(videoReply)) > 24 {
		t.Fatal("quota replies must be distinct and no longer than 20 characters")
	}

	unauthorized := mediaQuotaAdminRequest(runtime, http.MethodGet, nil, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	unknown := mediaQuotaAdminRequest(runtime, http.MethodPut, map[string]any{
		"imageDailyLimit": 4, "videoDailyLimit": 2, "extra": true,
	}, "admin-test-token")
	if unknown.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status = %d: %s", unknown.Code, unknown.Body.String())
	}
	outOfRange := mediaQuotaAdminRequest(runtime, http.MethodPut, map[string]any{
		"imageDailyLimit": 101, "videoDailyLimit": 2,
	}, "admin-test-token")
	if outOfRange.Code != http.StatusBadRequest {
		t.Fatalf("range status = %d: %s", outOfRange.Code, outOfRange.Body.String())
	}

	gateway := NewGateway("admin-test-token")
	gateway.runtime = runtime
	body, _ := json.Marshal(map[string]any{
		"imageDailyLimit": 4, "videoDailyLimit": 2, "trustedAdminBypass": true,
		"whitelist": []map[string]string{{"label": "owner", "senderRef": "sender-owner"}},
	})
	request := httptest.NewRequest(http.MethodPut, "/api/v1/runtime/media-quotas", bytes.NewReader(body))
	request.Header.Set(adminTokenHeader, "attacker-controlled")
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("gateway attacker update = %d: %s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "/api/v1/runtime/media-quotas", bytes.NewReader(body))
	request.Header.Set(adminTokenHeader, "admin-test-token")
	recorder = httptest.NewRecorder()
	gateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("gateway update = %d: %s", recorder.Code, recorder.Body.String())
	}

	get := mediaQuotaAdminRequest(runtime, http.MethodGet, nil, "admin-test-token")
	var response struct {
		Data mediaQuotaConfig `json:"data"`
	}
	if err := json.NewDecoder(get.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Data.ImageDailyLimit != 4 || response.Data.VideoDailyLimit != 2 ||
		response.Data.Timezone != "Asia/Shanghai" || !response.Data.TrustedAdminBypass ||
		len(response.Data.Whitelist) != 1 || response.Data.Whitelist[0].SenderRef != "sender-owner" {
		t.Fatalf("quota config = %+v", response.Data)
	}
}

func TestDirectImageRouteReturnsQuotaReplyBeforeGeneration(t *testing.T) {
	configPath, configDB := newTestCoreConfig(t)
	insertTestEndpoint(t, configDB, "image-quota", "image", []string{"image_generation"}, "media", "generate_image")
	_ = configDB.Close()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 0, 3); err != nil {
		t.Fatal(err)
	}

	reply, err := runtime.generate(context.Background(), runRecord{
		ID: "direct-run", SenderRef: "direct-sender", ConversationRef: "group",
	}, "画一张图")
	if err != nil {
		t.Fatal(err)
	}
	if !isMediaQuotaReply(mediaKindImage, reply.Text) || len(reply.Attachments) != 0 {
		t.Fatalf("quota reply = %+v", reply)
	}
}

func TestAgentToolLoopReturnsExactVideoQuotaReply(t *testing.T) {
	var modelCalls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		modelCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant", "content": "", "tool_calls": []any{
					toolCallResponse("video-1", "grok_generate_video", `{"prompt":"city at night"}`),
				},
			},
		}}})
	}))
	defer provider.Close()
	runtime := newQuotaTestRuntime(t, provider.Client())
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 3, 0); err != nil {
		t.Fatal(err)
	}
	run := insertQuotaTestRun(t, runtime, "tool-run", "tool-sender")
	policy := runtimeToolPolicy{Authority: "member", Tools: []runtimeTool{{
		Name: "grok_generate_video", AdapterRef: "core:grok_generate_video",
		RiskLevel: 0, ApprovalMode: "auto", TimeoutSeconds: 30,
		InputSchema: map[string]any{"type": "object"},
	}}}
	reply, err := runtime.runAgentLoop(
		context.Background(), run, "make a video", "system", "model", provider.URL,
		policy, runtimeMessagePolicy{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !isMediaQuotaReply(mediaKindVideo, reply.Text) || len(reply.Attachments) != 0 {
		t.Fatalf("tool quota reply = %+v", reply)
	}
	if modelCalls.Load() != 1 {
		t.Fatalf("model calls = %d, want 1", modelCalls.Load())
	}
}

func TestMediaQuotaAdminBypassesDailyLimit(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := runtime.executeQuotaMedia(context.Background(), runRecord{IsAdmin: true, SenderRef: "admin"}, mediaKindImage, func() (toolResult, error) {
		called = true
		return toolResult{Content: `{"ok":true}`}, nil
	})
	if err != nil || !called || result.Content == "" {
		t.Fatalf("admin bypass result=%+v err=%v called=%v", result, err, called)
	}
	if _, err := runtime.mediaQuota.reserve(context.Background(), "admin", mediaKindImage); !isQuotaExceeded(err, mediaKindImage) {
		t.Fatalf("admin bypass should not consume quota: %v", err)
	}
}

func TestMediaQuotaWhitelistBypassesDailyLimit(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updatePolicy(context.Background(), 0, 0, true, []mediaQuotaWhitelistEntry{{
		Label: "owner", SenderRef: "sender-owner",
	}}); err != nil {
		t.Fatal(err)
	}
	called := false
	result, err := runtime.executeQuotaMedia(context.Background(), runRecord{
		SenderRef: "sender-owner",
	}, mediaKindImage, func() (toolResult, error) {
		called = true
		return toolResult{Content: `{"ok":true}`}, nil
	})
	if err != nil || !called || result.Content == "" {
		t.Fatalf("whitelist bypass result=%+v err=%v called=%v", result, err, called)
	}
	if _, err := runtime.mediaQuota.reserve(context.Background(), "sender-owner", mediaKindImage); !isQuotaExceeded(err, mediaKindImage) {
		t.Fatalf("whitelist bypass should not consume quota: %v", err)
	}
}

func mediaQuotaUsage(t *testing.T, runtime *AgentRuntime, sender string, kind mediaKind) (int, int) {
	t.Helper()
	day := runtime.mediaQuota.now().In(shanghaiTime).Format("2006-01-02")
	var used, reserved int
	err := runtime.db.QueryRow(`
		SELECT used_count, reserved_count FROM media_quota_usage
		WHERE subject_digest = ? AND usage_day = ? AND media_kind = ?
	`, runtime.mediaQuota.digestSender(sender), day, string(kind)).Scan(&used, &reserved)
	if err != nil {
		t.Fatal(err)
	}
	return used, reserved
}

func isQuotaExceeded(err error, kind mediaKind) bool {
	var exceeded *mediaQuotaExceededError
	return errors.As(err, &exceeded) && exceeded.Kind == kind
}

func mediaQuotaAdminRequest(runtime *AgentRuntime, method string, payload any, token string) *httptest.ResponseRecorder {
	var body bytes.Buffer
	if payload != nil {
		_ = json.NewEncoder(&body).Encode(payload)
	}
	request := httptest.NewRequest(method, "/api/v1/runtime/media-quotas", &body)
	if token != "" {
		request.Header.Set(adminTokenHeader, token)
	}
	recorder := httptest.NewRecorder()
	runtime.Handle(recorder, request)
	return recorder
}

func newQuotaTestRuntime(t *testing.T, client *http.Client) *AgentRuntime {
	t.Helper()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath:       filepath.Join(t.TempDir(), "runtime.sqlite3"),
		ConfigDatabasePath: newTestCoreConfigPath(t),
		AdminToken:         "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32)),
		HTTPClient:    client,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func insertQuotaTestRun(t *testing.T, runtime *AgentRuntime, id, sender string) runRecord {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	run := runRecord{
		ID: id, EventID: id + "-event", ReplyHandle: id + "-reply",
		ConversationRef: "group", SenderRef: sender, State: "running",
	}
	_, err := runtime.db.Exec(`
		INSERT INTO agent_runs (
			id, event_id, reply_handle, conversation_ref, sender_ref,
			input_cipher, is_admin, state, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, NULL, 0, 'running', ?, ?)
	`, run.ID, run.EventID, run.ReplyHandle, run.ConversationRef, run.SenderRef, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func TestMediaQuotaStoresOnlySenderDigest(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	sender := "plain-identity-must-not-be-stored"
	reservation, err := runtime.mediaQuota.reserve(context.Background(), sender, mediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := reservation.release(context.Background()); err != nil {
		t.Fatal(err)
	}
	var digest []byte
	if err := runtime.db.QueryRow("SELECT subject_digest FROM media_quota_usage").Scan(&digest); err != nil {
		t.Fatal(err)
	}
	if len(digest) != sha256.Size || bytes.Contains(digest, []byte(sender)) || strings.Contains(string(digest), sender) {
		t.Fatalf("stored sender digest is invalid")
	}
}

func TestMediaQuotaSeparatesAgentInstances(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.mediaQuota.updateConfig(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	first := runRecord{AgentInstanceID: "doubao-qq", SenderRef: "same-member"}
	second := runRecord{AgentInstanceID: "xiaoman-qq", SenderRef: "same-member"}
	left, err := runtime.mediaQuota.reserveForRun(context.Background(), first, mediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	if err := left.commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	right, err := runtime.mediaQuota.reserveForRun(context.Background(), second, mediaKindImage)
	if err != nil {
		t.Fatal("different agent instances must have independent quotas: ", err)
	}
	if err := right.release(context.Background()); err != nil {
		t.Fatal(err)
	}
}
