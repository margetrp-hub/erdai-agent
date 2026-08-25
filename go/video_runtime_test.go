package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFitVideoProviderPromptKeepsUTF8AndProviderLimit(t *testing.T) {
	input := strings.Repeat("角色外观：年轻、可爱、自然；", 500) + "\n用户场景：雨夜街边的全身自拍。"
	got := fitVideoProviderPrompt(input)
	if len([]byte(got)) > maxVideoPromptBytes {
		t.Fatalf("video prompt bytes = %d, limit = %d", len([]byte(got)), maxVideoPromptBytes)
	}
	if !strings.Contains(got, "用户场景") {
		t.Fatalf("video prompt lost the requested scene: %q", got[len(got)-min(len(got), 80):])
	}
	if !strings.Contains(got, "角色外观") {
		t.Fatal("video prompt lost the character profile")
	}
	if !utf8.ValidString(got) {
		t.Fatal("video prompt is not valid UTF-8")
	}
}

func TestVideoRouteSurvivesTransientPollsAndDeliversMP4(t *testing.T) {
	mp4 := testMP4()
	var pollCalls atomic.Int32
	var requestHeadersMu sync.Mutex
	requestIDs := []string{}
	authorizations := []string{}
	recordHeaders := func(r *http.Request) {
		requestHeadersMu.Lock()
		defer requestHeadersMu.Unlock()
		requestIDs = append(requestIDs, r.Header.Get("X-Client-Request-ID"))
		authorizations = append(authorizations, r.Header.Get("Authorization"))
	}
	finalPollReached := make(chan struct{})
	releaseFinalPoll := make(chan struct{})
	var finalPollOnce sync.Once
	// releaseFinal is idempotent and also runs on test failure, so a blocked
	// provider handler can never deadlock the deferred provider.Close.
	var releaseOnce sync.Once
	releaseFinal := func() { releaseOnce.Do(func() { close(releaseFinalPoll) }) }
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			recordHeaders(r)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid JSON", http.StatusBadRequest)
				return
			}
			prompt, _ := body["prompt"].(string)
			references, _ := body["reference_images"].([]any)
			if body["model"] != "grok-imagine-video" || !strings.Contains(prompt, "做一段海边日落视频") ||
				!strings.Contains(prompt, "角色外观基准") {
				t.Errorf("create body = %+v", body)
			}
			if len(references) != 1 || references[0].(map[string]any)["url"] != testVideoPersonaAvatar {
				t.Errorf("video reference body = %+v", body)
			}
			writeJSON(w, http.StatusOK, map[string]any{"id": "video-task", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/video-task":
			recordHeaders(r)
			if pollCalls.Add(1) <= defaultVideoPollMaxTransientFailures {
				http.NotFound(w, r)
				return
			}
			finalPollOnce.Do(func() { close(finalPollReached) })
			<-releaseFinalPoll
			writeJSON(w, http.StatusOK, map[string]any{
				"data": map[string]any{
					"task_id": "video-task", "status": "completed", "video": map[string]string{"url": provider.URL + "/asset.mp4"},
				},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/asset.mp4":
			recordHeaders(r)
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", "24")
			_, _ = w.Write(mp4)
		case strings.Contains(r.URL.Path, "chat/completions"):
			t.Error("video media route called the chat provider")
			http.Error(w, "unexpected chat call", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	defer releaseFinal()

	configPath := videoTestConfig(t, provider.URL+"/grok", 10)

	mediaDir := filepath.Join(t.TempDir(), "media")
	runtime := newVideoRuntime(t, configPath, provider.Client(), mediaDir, time.Microsecond, 450)
	defer runtime.Close()

	response := runtimeRequest(
		t,
		runtime,
		"/api/v1/transport/events",
		testTransportEvent("video-event", "做一段海边日落视频", true),
		"video-event",
	)
	if response.Code != http.StatusAccepted {
		t.Fatalf("accept status = %d: %s", response.Code, response.Body.String())
	}
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	select {
	case <-finalPollReached:
	case <-time.After(10 * time.Second):
		t.Fatal("video polling did not survive 450 transient responses")
	}

	lease := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/lease", map[string]any{
		"consumerId": "video-test", "limit": 1, "leaseSeconds": 30,
	}, "")
	var progress struct {
		Data []struct {
			ID      string `json:"id"`
			Phase   string `json:"phase"`
			Message struct {
				Text string `json:"text"`
			} `json:"message"`
		} `json:"data"`
	}
	decodeRecorder(t, lease, &progress)
	// The progress text may come from the persona runtime reply pool or the
	// configured message policy; the contract is one natural progress message
	// with no task IDs or system phrasing, announced only after the provider
	// accepted the task.
	if len(progress.Data) != 1 || progress.Data[0].Phase != "progress" ||
		strings.TrimSpace(progress.Data[0].Message.Text) == "" ||
		strings.Contains(progress.Data[0].Message.Text, "任务") ||
		strings.Contains(progress.Data[0].Message.Text, "ID") {
		t.Fatalf("progress delivery = %+v", progress.Data)
	}
	ack := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/"+progress.Data[0].ID+"/ack", map[string]any{}, "")
	if ack.Code != http.StatusOK {
		t.Fatalf("progress ack = %d: %s", ack.Code, ack.Body.String())
	}
	releaseFinal()
	waitForDelivery(t, runtime, accepted.Data.RunID)

	var payloadJSON string
	if err := runtime.db.QueryRow(
		"SELECT payload_json FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'",
		accepted.Data.RunID,
	).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var terminal struct {
		Text        string            `json:"text"`
		Attachments []agentAttachment `json:"attachments"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &terminal); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(terminal.Text) == "" || strings.Contains(terminal.Text, "任务") ||
		len(terminal.Attachments) != 1 {
		t.Fatalf("terminal delivery = %+v", terminal)
	}
	attachment := terminal.Attachments[0]
	if attachment.Kind != "video" || attachment.MimeType != "video/mp4" ||
		!strings.HasPrefix(attachment.LocalPath, mediaMountRoot+"/video_") ||
		!strings.HasSuffix(attachment.LocalPath, ".mp4") || strings.Contains(attachment.LocalPath, "..") {
		t.Fatalf("video attachment = %+v", attachment)
	}
	stored, err := os.ReadFile(filepath.Join(mediaDir, filepath.Base(attachment.LocalPath)))
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) < 12 || !bytes.Equal(stored[4:8], []byte("ftyp")) {
		t.Fatal("stored attachment is not an MP4")
	}

	requestHeadersMu.Lock()
	defer requestHeadersMu.Unlock()
	if len(requestIDs) != 453 {
		t.Fatalf("provider request count = %d", len(requestIDs))
	}
	expectedRequestID := stableVideoRequestID(runRecord{EventID: "video-event"})
	for index := range requestIDs {
		if requestIDs[index] != expectedRequestID || requestIDs[index] == "" {
			t.Fatalf("request ID %d = %q", index, requestIDs[index])
		}
		if authorizations[index] != "Bearer grok-video-test-key" {
			t.Fatalf("authorization %d missing", index)
		}
	}
	leaseAndAckOne(t, runtime, "terminal")
	var state string
	if err := runtime.db.QueryRow("SELECT state FROM agent_runs WHERE id = ?", accepted.Data.RunID).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "delivered" {
		t.Fatalf("run state = %s", state)
	}
}

func TestVideoCreateRetriesTransientGatewayFailure(t *testing.T) {
	var createCalls atomic.Int32
	var provider *httptest.Server
	provider = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			if createCalls.Add(1) < 3 {
				http.Error(w, "temporary gateway failure", http.StatusBadGateway)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"request_id": "retry-task"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/retry-task":
			writeJSON(w, http.StatusOK, map[string]any{
				"status": "done", "video": map[string]any{"url": provider.URL + "/asset.mp4"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/asset.mp4":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4())
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	configPath := videoTestConfig(t, provider.URL+"/grok", 10)
	runtime := newVideoRuntime(
		t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"),
		time.Millisecond, 1,
	)
	defer runtime.Close()

	result, err := runtime.generateVideo(context.Background(), runRecord{EventID: "retry-event"}, "做一段视频")
	if err != nil {
		t.Fatal(err)
	}
	if createCalls.Load() != 3 || len(result.Attachments) != 1 {
		t.Fatalf("create calls = %d, attachments = %d", createCalls.Load(), len(result.Attachments))
	}
}

func TestVideoProviderNotFoundReturnsUnavailableReply(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "unexpected request", http.StatusInternalServerError)
	}))
	defer provider.Close()

	configPath := videoTestConfig(t, provider.URL+"/grok", 10)
	runtime := newVideoRuntime(
		t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"),
		time.Millisecond, 1,
	)
	defer runtime.Close()

	response := runtimeRequest(
		t, runtime, "/api/v1/transport/events",
		testTransportEvent("video-not-found", "生成一个视频", true), "video-not-found",
	)
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	waitForDeliveryWithin(t, runtime, accepted.Data.RunID, 3*time.Second)

	var payloadJSON string
	if err := runtime.db.QueryRow(`
		SELECT payload_json FROM agent_deliveries
		WHERE run_id = ? AND phase = 'terminal'
	`, accepted.Data.RunID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(payload.Text) == "" || strings.Contains(payloadJSON, "刚才没处理好") {
		t.Fatalf("video unavailable delivery = %s", payloadJSON)
	}
}

func TestUnavailableVideoRouteDoesNotCallProvider(t *testing.T) {
	var providerCalls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls.Add(1)
		http.Error(w, "must not be called", http.StatusInternalServerError)
	}))
	defer provider.Close()

	configPath, configDB := newTestCoreConfig(t)
	_, err := configDB.Exec(`
		INSERT INTO model_health (
			endpoint_id, healthy, latency_ms, error_rate, consecutive_failures,
			status_message, checked_at
		) VALUES ('grok-imagine-video', 0, NULL, 1, 1, '无可用账号', ?)
	`, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	if err = configDB.Close(); err != nil {
		t.Fatal(err)
	}

	runtime := newVideoRuntime(
		t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"),
		time.Millisecond, 1,
	)
	defer runtime.Close()
	response := runtimeRequest(
		t, runtime, "/api/v1/transport/events",
		testTransportEvent("video-no-account", "生视频，做一个日落短片", true), "video-no-account",
	)
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	waitForDeliveryWithin(t, runtime, accepted.Data.RunID, 3*time.Second)
	var payloadJSON string
	if err = runtime.db.QueryRow(`
		SELECT payload_json FROM agent_deliveries
		WHERE run_id = ? AND phase = 'terminal'
	`, accepted.Data.RunID).Scan(&payloadJSON); err != nil {
		t.Fatal(err)
	}
	var payload struct {
		Text string `json:"text"`
	}
	if err = json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(payload.Text) == "" || providerCalls.Load() != 0 {
		t.Fatalf("unavailable route delivery = %s, provider calls = %d", payloadJSON, providerCalls.Load())
	}
}

func TestVideoTimeoutCreatesFailedTerminalDelivery(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			writeJSON(w, http.StatusOK, map[string]any{"id": "slow-task", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/slow-task":
			writeJSON(w, http.StatusOK, map[string]any{"id": "slow-task", "status": "running"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	configPath := videoTestConfig(t, provider.URL+"/grok", 1)
	runtime := newVideoRuntime(t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"), 5*time.Millisecond, 10)
	defer runtime.Close()

	response := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("timeout-video", "做个视频", true), "timeout-video")
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, response, &accepted)
	waitForDeliveryWithin(t, runtime, accepted.Data.RunID, 15*time.Second)
	var state, errorCode string
	if err := runtime.db.QueryRow("SELECT state, error_code FROM agent_runs WHERE id = ?", accepted.Data.RunID).Scan(&state, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || errorCode != "video_generation_timeout" {
		t.Fatalf("timeout state = %s, error = %s", state, errorCode)
	}
	leaseAndAckOne(t, runtime, "terminal")
}

func TestVideoWorkerDoesNotBlockChatAndCloseCancelsPolling(t *testing.T) {
	pollStarted := make(chan struct{})
	var pollOnce sync.Once
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			writeJSON(w, http.StatusOK, map[string]any{"id": "blocked-task", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/blocked-task":
			pollOnce.Do(func() { close(pollStarted) })
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions":
			writeJSON(w, http.StatusOK, map[string]any{
				"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "在。"}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	configPath := videoTestConfig(t, provider.URL+"/grok", 1800)
	runtime := newVideoRuntime(t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"), time.Millisecond, 450)

	videoResponse := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("blocking-video", "做个视频", true), "blocking-video")
	if videoResponse.Code != http.StatusAccepted {
		t.Fatalf("video accept = %d", videoResponse.Code)
	}
	select {
	case <-pollStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("video polling did not start")
	}

	chatResponse := runtimeRequest(t, runtime, "/api/v1/transport/events", testTransportEvent("parallel-chat", "在吗", true), "parallel-chat")
	var accepted struct {
		Data struct {
			RunID string `json:"runId"`
		} `json:"data"`
	}
	decodeRecorder(t, chatResponse, &accepted)
	waitForDeliveryWithin(t, runtime, accepted.Data.RunID, 15*time.Second)

	heldConnection, err := runtime.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer heldConnection.Close()

	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = heldConnection.Close()
		<-closed
		t.Fatal("Close did not cancel the active video worker")
	}
}

func TestVideoDownloadRejectsUnsafeAndInvalidAssets(t *testing.T) {
	runtime := &AgentRuntime{
		client: http.DefaultClient, grokAPIKey: "test-key", mediaDir: filepath.Join(t.TempDir(), "media"),
	}
	if _, err := runtime.downloadAndStoreVideo(context.Background(), "https://provider.example", "http://asset.example/video.mp4", "request-id"); err == nil {
		t.Fatal("HTTP video URL was accepted")
	}
	if resolved, err := resolveVideoAssetURL("http://grok2api-local:8000/v1", "/files/video.mp4"); err != nil ||
		resolved.String() != "http://grok2api-local:8000/files/video.mp4" {
		t.Fatalf("private Grok video URL = %v, %v", resolved, err)
	}

	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Header().Set("Content-Length", "268435457")
		_, _ = w.Write(testMP4())
	}))
	defer provider.Close()
	runtime.client = provider.Client()
	if _, err := runtime.downloadAndStoreVideo(context.Background(), provider.URL, provider.URL+"/large.mp4", "request-id"); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("oversized video error = %v", err)
	}
}

func TestVideoDownloadAcceptsMissingContentTypeWhenMP4MagicIsValid(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(testMP4())
	}))
	defer provider.Close()
	runtime := newIdleRuntime(t)
	runtime.grokAPIKey = "test-key"
	runtime.mediaDir = filepath.Join(t.TempDir(), "media")
	runtime.client = provider.Client()
	attachment, err := runtime.downloadAndStoreVideo(
		context.Background(), provider.URL, provider.URL+"/video.mp4", "request-id",
	)
	if err != nil {
		t.Fatal(err)
	}
	if attachment.Kind != "video" || attachment.MimeType != "video/mp4" {
		t.Fatalf("video attachment = %+v", attachment)
	}
}

func TestVideoRouteFallsBackToProviderContentForLocalAssetURL(t *testing.T) {
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/grok/videos/generations":
			writeJSON(w, http.StatusOK, map[string]any{"id": "local-url-task", "status": "queued"})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/local-url-task":
			writeJSON(w, http.StatusOK, map[string]any{
				"id": "local-url-task", "status": "completed",
				"video": map[string]string{"url": "http://127.0.0.1:18081/videos/local-url-task/content"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/grok/videos/local-url-task/content":
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4())
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()

	configPath := videoTestConfig(t, provider.URL+"/grok", 10)
	runtime := newVideoRuntime(t, configPath, provider.Client(), filepath.Join(t.TempDir(), "media"), time.Microsecond, 1)
	defer runtime.Close()

	result, err := runtime.generateVideo(context.Background(), runRecord{EventID: "local-url-event"}, "生成视频：街边回头")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attachments) != 1 || result.Attachments[0].Kind != "video" {
		t.Fatalf("video attachments = %+v", result.Attachments)
	}
}

func TestVideoPromptOptionsSupportAspectRatioAndResolution(t *testing.T) {
	cases := []struct {
		prompt, aspect, resolution string
	}{
		{"\u751f\u62109:16\u7ad6\u5c4f\u89c6\u9891 480p", "9:16", "480p"},
		{"\u505a\u4e00\u4e2a\u65b9\u5f62\u89c6\u9891 720p", "1:1", "720p"},
		{"\u505a\u4e00\u4e2a\u6a2a\u5c4f\u89c6\u9891", "16:9", "720p"},
	}
	for _, testCase := range cases {
		t.Run(testCase.prompt, func(t *testing.T) {
			request := map[string]any{}
			if err := applyVideoGenerationOptions(request, testCase.prompt, "grok-imagine-video"); err != nil {
				t.Fatal(err)
			}
			if request["aspect_ratio"] != testCase.aspect ||
				request["resolution"] != testCase.resolution ||
				request["duration"] != 6 {
				t.Fatalf("video options = %+v", request)
			}
		})
	}
}

func TestVideo1080pRequiresVideo15Model(t *testing.T) {
	request := map[string]any{}
	if err := applyVideoGenerationOptions(request, "\u751f\u62101080p\u89c6\u9891", "grok-imagine-video"); err == nil {
		t.Fatal("1080p was accepted by the base video model")
	}
	if err := applyVideoGenerationOptions(request, "\u751f\u62101080p\u89c6\u9891", "grok-imagine-video-1.5-preview"); err != nil {
		t.Fatal(err)
	}
	if request["resolution"] != "1080p" {
		t.Fatalf("1080p options = %+v", request)
	}
}

func TestMediaGenerationHonorsUnifiedImagePolicy(t *testing.T) {
	configPath, db := newTestCoreConfig(t)
	setTestIntegration(t, db, "image_policy", map[string]any{"enabled": false})
	setTestIntegration(t, db, "grok_policy", map[string]any{
		"enabled": true, "apiBase": "http://127.0.0.1:1/v1", "videoModel": "grok-imagine-video",
	})
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath:       filepath.Join(t.TempDir(), "runtime.sqlite3"),
		ConfigDatabasePath: configPath,
		AdminToken:         "admin-test-token",
		RuntimeToken:       testRuntimeToken,
		ModelAPIKey:        "model-test-key",
		GrokAPIKey:         "grok-test-key",
		EncryptionKey:      base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if _, err := runtime.generateImageOnce(context.Background(), "test", true, ""); err == nil || !strings.Contains(err.Error(), "image generation is disabled") {
		t.Fatalf("image gate error = %v", err)
	}
	if _, err := runtime.generateVideo(context.Background(), runRecord{EventID: "media-gate"}, "test"); err == nil || !strings.Contains(err.Error(), "media generation is disabled") {
		t.Fatalf("video gate error = %v", err)
	}
}

func videoTestConfig(t *testing.T, apiBase string, timeoutSeconds int) string {
	t.Helper()
	path, db := newTestCoreConfig(t)
	setTestIntegration(t, db, "image_policy", map[string]any{
		"enabled": true,
	})
	setTestIntegration(t, db, "grok_policy", map[string]any{
		"enabled": true, "apiBase": apiBase, "videoModel": "grok-imagine-video",
		"videoTimeoutSeconds": timeoutSeconds,
	})
	setTestIntegration(t, db, "provider_policy", map[string]any{
		"apiBase": strings.TrimSuffix(apiBase, "/grok") + "/v1", "defaultModel": "chat-model",
	})
	setTestIntegration(t, db, "message_policy", map[string]any{
		"toolProgressVideoMessages":   []string{"我开始做这段视频。"},
		"toolCompletionVideoMessages": []string{"视频好了，给你。"},
	})
	insertTestEndpoint(t, db, "video-route", "grok-imagine-video", []string{"video_generation"}, "media", "core:grok_generate_video")
	insertTestEndpoint(t, db, "chat-route", "chat-model", []string{"chat"}, "llm", "openai")
	if _, err := db.Exec("UPDATE personas SET avatar_data_uri = ? WHERE id = 'doubao'", testVideoPersonaAvatar); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

const testVideoPersonaAvatar = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAAB"

func newVideoRuntime(
	t *testing.T,
	configPath string,
	client *http.Client,
	mediaDir string,
	pollInterval time.Duration,
	maxTransientFailures int,
) *AgentRuntime {
	t.Helper()
	runtime, err := NewAgentRuntime(RuntimeConfig{
		DatabasePath: filepath.Join(t.TempDir(), "runtime.sqlite3"), ConfigDatabasePath: configPath,
		AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
		ModelAPIKey: "model-test-key", GrokAPIKey: "grok-video-test-key",
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		MediaDir:      mediaDir, HTTPClient: client,
		VideoPollInterval: pollInterval, VideoPollMaxTransientFailures: maxTransientFailures,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func waitForDeliveryWithin(t *testing.T, runtime *AgentRuntime, runID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var count int
		if err := runtime.db.QueryRow("SELECT count(*) FROM agent_deliveries WHERE run_id = ? AND phase = 'terminal'", runID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("terminal delivery was not enqueued")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func testMP4() []byte {
	return []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
}
