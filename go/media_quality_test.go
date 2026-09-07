package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func qualityTestArtifact(t *testing.T, runtime *AgentRuntime, name string) toolResult {
	t.Helper()
	if runtime.mediaDir == "" {
		runtime.mediaDir = t.TempDir()
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(testVideoPersonaAvatar, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(runtime.mediaDir, name), data, 0600); err != nil {
		t.Fatal(err)
	}
	return toolResult{Content: `{"ok":true,"result":"image_generated"}`, Attachments: []agentAttachment{{Kind: "image", Name: name, LocalPath: mediaMountRoot + "/" + name, MimeType: "image/png"}}}
}

func TestMediaQualityParserAndSelection(t *testing.T) {
	for _, raw := range []string{`{}`, `{"status":"passed","identityIssues":["wrong face"]}`, `{"status":"failed"}`, `{"status":"passed"} ignored`, `{"status":"passed","override":true}`} {
		if got := parseQualityAssessment(raw); got.Status != "unverified" {
			t.Fatalf("invalid assessment accepted: %s", raw)
		}
	}
	if got := parseQualityAssessment(`{"status":"passed","identityIssues":[],"constraintIssues":[],"qualityIssues":[]}`); got.Status != "passed" {
		t.Fatalf("valid assessment: %+v", got)
	}
	first := qualityAssessment{IdentityIssues: []string{"wrong identity"}}
	second := qualityAssessment{ConstraintIssues: []string{"color", "outfit"}, QualityIssues: []string{"anatomy"}}
	if closerQualityAttempt(first, second) != 1 || closerQualityAttempt(second, first) != 0 || closerQualityAttempt(second, second) != 1 {
		t.Fatal("lexicographic quality selection failed")
	}
}

func TestMediaQualityTwoAttemptsAndPersistedSelection(t *testing.T) {
	for _, secondStatus := range []string{"failed", "unverified", "passed"} {
		t.Run(secondStatus, func(t *testing.T) {
			var checks atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				encoded, _ := json.Marshal(payload)
				if !strings.Contains(string(encoded), `image_url`) || !strings.Contains(string(encoded), "this time no purple") {
					t.Error("quality did not inspect actual media and request")
				}
				assessment := `{"status":"failed","identityIssues":[],"constraintIssues":[],"qualityIssues":["slight composition issue"]}`
				if checks.Add(1) == 2 {
					assessment = fmt.Sprintf(`{"status":%q,"identityIssues":[],"constraintIssues":[],"qualityIssues":[]}`, secondStatus)
					if secondStatus == "failed" {
						assessment = `{"status":"failed","identityIssues":["wrong identity"],"constraintIssues":[],"qualityIssues":[]}`
					}
				}
				writeJSON(w, 200, map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": assessment}}}})
			}))
			defer server.Close()
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			runtime.client = server.Client()
			insertTestEndpoint(t, runtime.configStore.db, "quality-test", "vision-test", []string{"vision", "chat"}, "llm", "openai")
			bindTestModelConnection(t, runtime.configStore.db, "quality-test", server.URL)
			setTestIntegration(t, runtime.configStore.db, "image_policy", map[string]any{"enabled": true, "mediaQualityEndpointId": "quality-test"})
			run := visualPlanTestRun(t, runtime, "quality-"+secondStatus)
			var generates int
			generate := func(ctx context.Context, attempt int, correction string) (toolResult, error) {
				generates++
				if attempt == 1 && !strings.Contains(correction, "composition issue") {
					t.Error("repair lacks observed defect")
				}
				return qualityTestArtifact(t, runtime, fmt.Sprintf("artifact-%d.png", attempt)), nil
			}
			request := mediaQualityRequest{MediaType: "image", Prompt: "this time no purple", Reference: testVideoPersonaAvatar, OperationID: "quality-op"}
			result, err := runtime.executeMediaQuality(context.Background(), run, request, generate)
			want := "artifact-1.png"
			if secondStatus == "failed" {
				want = "artifact-0.png"
			}
			if err != nil || len(result.Attachments) != 1 || result.Attachments[0].Name != want || generates != 2 || checks.Load() != 2 {
				t.Fatalf("selection=%+v err=%v generations=%d checks=%d", result, err, generates, checks.Load())
			}
			replay, replayErr := runtime.executeMediaQuality(context.Background(), run, request, generate)
			if replayErr != nil || generates != 2 || checks.Load() != 2 || replay.UserMessage != result.UserMessage || replay.Content != result.Content {
				t.Fatalf("recovery regenerated: %v %d %d", err, generates, checks.Load())
			}
			if secondStatus != "passed" && (!result.PreserveUserMessage || result.UserMessage == "") {
				t.Fatal("non-passing quality was not disclosed to the user")
			}
			reports, err := runtime.mediaQualityTaskDetails(context.Background(), run.ID)
			if err != nil || len(reports) != 1 || len(reports[0].Attempts) != 2 {
				t.Fatalf("report=%+v %v", reports, err)
			}
		})
	}
}

func TestMediaQualityUnavailableSendsAndUnknownGenerationDoesNotRepeat(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := visualPlanTestRun(t, runtime, "quality-unavailable")
	setTestIntegration(t, runtime.configStore.db, "image_policy", map[string]any{"enabled": true, "mediaQualityEndpointId": "not-enabled"})
	count := 0
	request := mediaQualityRequest{MediaType: "image", Prompt: "test", OperationID: "unavailable"}
	generate := func(context.Context, int, string) (toolResult, error) {
		count++
		return qualityTestArtifact(t, runtime, "unverified.png"), nil
	}
	if _, err := runtime.executeMediaQuality(context.Background(), run, request, generate); err != nil || count != 1 {
		t.Fatalf("unavailable QA blocked media: %v", err)
	}
	reports, err := runtime.mediaQualityTaskDetails(context.Background(), run.ID)
	if err != nil || reports[0].Status != "unverified" {
		t.Fatalf("unverified status missing: %+v %v", reports, err)
	}
	var id string
	if err = runtime.db.QueryRow("SELECT id FROM agent_task_steps WHERE run_id=? AND name='media_quality:image'", run.ID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	unknown := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{SelectedAttempt: -1, Attempts: []mediaQualityAttempt{{Attempt: 0, GenerationStatus: "started", Assessment: unverifiedQuality("pending")}}}}
	if err = runtime.saveMediaReceipt(context.Background(), id, "running", unknown); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.executeMediaQuality(context.Background(), run, request, generate); err == nil || !strings.Contains(err.Error(), "uncertain") || count != 1 {
		t.Fatalf("uncertain image repeated: %v count=%d", err, count)
	}
}

func TestMediaQualityCancellationIsNotFailOpen(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runtime.executeMediaQuality(ctx, runRecord{}, mediaQualityRequest{MediaType: "image"}, func(context.Context, int, string) (toolResult, error) {
		t.Fatal("cancelled request generated media")
		return toolResult{}, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestMediaQualityCorrectionGenerationFailureRetainsFirstArtifact(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		t.Run(kind, func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			run := visualPlanTestRun(t, runtime, "correction-unavailable-"+kind)
			request := mediaQualityRequest{MediaType: kind, OperationID: "correction-unavailable"}
			first := qualityTestArtifact(t, runtime, "first.png")
			if kind == "video" {
				first.Attachments[0] = agentAttachment{Kind: "video", Name: "first.mp4", LocalPath: mediaMountRoot + "/first.mp4", MimeType: "video/mp4"}
			}
			// Resume after the first artifact passed integrity checks but failed
			// visual constraints, immediately before the correction submission.
			id, err := runtime.beginTaskStep(run.ID, "", "tool", "media_quality:"+kind, 0, map[string]string{"mediaType": kind})
			if err != nil {
				t.Fatal(err)
			}
			receipt := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{Version: 1, MediaType: kind,
				OperationID: request.OperationID, Status: "pending", SelectedAttempt: -1,
				Attempts: []mediaQualityAttempt{{Attempt: 0, GenerationStatus: "completed", ArtifactNames: []string{first.Attachments[0].Name},
					Assessment: qualityAssessment{Status: "failed", ConstraintIssues: []string{"wrong outfit color"}}}}}, Results: []toolResult{first}}
			if err = runtime.saveMediaReceipt(context.Background(), id, "running", receipt); err != nil {
				t.Fatal(err)
			}
			calls := 0
			generate := func(_ context.Context, attempt int, correction string) (toolResult, error) {
				calls++
				if attempt != 1 || !strings.Contains(correction, "wrong outfit color") {
					t.Fatalf("unexpected generation attempt=%d correction=%q", attempt, correction)
				}
				if kind == "video" {
					return toolResult{}, &videoHTTPError{StatusCode: http.StatusBadGateway}
				}
				return toolResult{}, &providerHTTPError{StatusCode: http.StatusGatewayTimeout}
			}
			for repeat := 0; repeat < 2; repeat++ {
				result, err := runtime.executeMediaQuality(context.Background(), run, request, generate)
				if err != nil || len(result.Attachments) != 1 || result.Attachments[0].Name != first.Attachments[0].Name || calls != 1 ||
					!result.PreserveUserMessage || !strings.Contains(result.UserMessage, "质量核验仍未通过") {
					t.Fatalf("result=%+v err=%v calls=%d", result, err, calls)
				}
			}
			reports, err := runtime.mediaQualityTaskDetails(context.Background(), run.ID)
			if err != nil || len(reports) != 1 || reports[0].Status != "failed" || reports[0].SelectedAttempt != 0 ||
				reports[0].SelectionReason != "correction_generation_failed" || len(reports[0].Attempts) != 2 ||
				reports[0].Attempts[1].GenerationStatus != "failed" || reports[0].Attempts[1].Assessment.Reason != "correction_generation_unavailable" {
				t.Fatalf("report=%+v err=%v", reports, err)
			}
		})
	}
}

func TestMediaQualitySelectedResultDisclosesStateWithoutDuplicating(t *testing.T) {
	for _, kind := range []string{"image", "video"} {
		for _, status := range []string{"passed", "failed", "unverified"} {
			t.Run(kind+"/"+status, func(t *testing.T) {
				receipt := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{MediaType: kind, Status: status,
					SelectedAttempt: 0, SelectionReason: status}, Results: []toolResult{{Content: `{"ok":true,"result":"original_result"}`, UserMessage: "original completion"}}}
				result := selectedMediaQualityResult(receipt)
				var content struct {
					Result       string `json:"result"`
					MediaQuality struct {
						Status string `json:"status"`
					} `json:"mediaQuality"`
				}
				if err := json.Unmarshal([]byte(result.Content), &content); err != nil || content.Result != "original_result" || content.MediaQuality.Status != status {
					t.Fatalf("quality content missing: %s err=%v", result.Content, err)
				}
				if status == "passed" {
					if result.UserMessage != "original completion" || result.PreserveUserMessage {
						t.Fatal("passing quality replaced normal completion")
					}
				} else if !result.PreserveUserMessage || !strings.Contains(result.UserMessage, "质量核验") {
					t.Fatal("quality warning was not preserved")
				}
				receipt.Results[0] = result
				again := selectedMediaQualityResult(receipt)
				if again.UserMessage != result.UserMessage || again.Content != result.Content {
					t.Fatal("replay duplicated the quality warning")
				}
			})
		}
	}
}

func TestMediaQualityCorrectionFailureClassificationPreservesHardFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"provider-timeout", context.DeadlineExceeded, true},
		{"provider-connection", &net.OpError{Op: "read", Net: "tcp", Err: errors.New("connection lost")}, true},
		{"provider-rate-limit", &providerHTTPError{StatusCode: 429}, true},
		{"provider-uncertain-acceptance", &providerHTTPError{StatusCode: 502}, true},
		{"video-timeout", &videoHTTPError{StatusCode: 504}, true},
		{"cancelled", context.Canceled, false},
		{"superseded", errTaskSuperseded, false},
		{"image-content-refusal", &providerHTTPError{StatusCode: 400, Message: "content refused"}, false},
		{"video-content-refusal", &videoHTTPError{StatusCode: 403}, false},
		{"integrity", errors.New("media integrity check failed"), false},
		{"unknown-provider-failure", errors.New("video provider reported failure"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mediaCorrectionGenerationUnavailable(context.Background(), tc.err); got != tc.want {
				t.Fatalf("recoverable=%v want=%v err=%v", got, tc.want, tc.err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if mediaCorrectionGenerationUnavailable(ctx, &providerHTTPError{StatusCode: 504}) {
		t.Fatal("cancelled outer request delivered the earlier artifact")
	}
}

func TestMediaQualityVideoWorkerInvalidHardFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"valid": false}) }))
	defer server.Close()
	t.Setenv("ERDAI_MEDIA_CHECK_URL", server.URL)
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	_, err := runtime.assessMediaQuality(context.Background(), runRecord{}, mediaQualityRequest{MediaType: "video"}, toolResult{Attachments: []agentAttachment{{Kind: "video", Name: "test.mp4", LocalPath: "/erdai-media/test.mp4"}}}, mediaQualityPolicy{})
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("corrupt video delivered: %v", err)
	}
	run := visualPlanTestRun(t, runtime, "invalid-video-retry")
	request := mediaQualityRequest{MediaType: "video", OperationID: "invalid-video"}
	generated := 0
	generate := func(context.Context, int, string) (toolResult, error) {
		generated++
		return toolResult{Attachments: []agentAttachment{{Kind: "video", Name: "test.mp4", LocalPath: "/erdai-media/test.mp4"}}}, nil
	}
	if _, err = runtime.executeMediaQuality(context.Background(), run, request, generate); err == nil {
		t.Fatal("invalid video selected")
	}
	if _, err = runtime.executeMediaQuality(context.Background(), run, request, generate); err == nil || generated != 1 {
		t.Fatalf("invalid video became unverified on resume: err=%v generated=%d", err, generated)
	}
}

func TestMediaQualityPrunePreservesResultAndIssueCounts(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := visualPlanTestRun(t, runtime, "quality-prune")
	id, err := runtime.beginTaskStep(run.ID, "", "tool", "media_quality:image", 0, map[string]string{"prompt": "private words"})
	if err != nil {
		t.Fatal(err)
	}
	receipt := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{SelectedAttempt: 0, Attempts: []mediaQualityAttempt{{Assessment: qualityAssessment{Status: "failed", IdentityIssues: []string{"private identity notes"}}}}}, Results: []toolResult{{Content: "preserve-result"}}}
	if err = runtime.saveMediaReceipt(context.Background(), id, "succeeded", receipt); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec("UPDATE agent_runs SET state='delivered' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	if err = runtime.pruneMediaQualityMetadata(context.Background(), time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	var got mediaQualityReceipt
	if _, err = runtime.loadMediaReceipt(context.Background(), id, &got); err != nil || got.Results[0].Content != "preserve-result" || got.Attempts[0].Assessment.IdentityIssues[0] != "expired" {
		t.Fatalf("prune receipt=%+v err=%v", got, err)
	}
}

func TestMediaQualityVideoCreateUncertainIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.Error(w, "gateway failed", 502) }))
	defer server.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	_, err := runtime.createVideoTask(context.Background(), server.URL, map[string]string{"prompt": "test"}, "test-request", "test-key")
	if err == nil || calls.Load() != 1 || !uncertainVideoCreateError(err) {
		t.Fatalf("uncertain retry calls=%d err=%v", calls.Load(), err)
	}
	if stableVideoAttemptRequestID(runRecord{ID: "run"}, "operation", 0) == stableVideoAttemptRequestID(runRecord{ID: "run"}, "operation", 1) {
		t.Fatal("repair shares provider idempotency key")
	}
}

func TestMediaQualityReceiptFailureNeverRepeatsImageGeneration(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := visualPlanTestRun(t, runtime, "receipt-failure")
	request := mediaQualityRequest{MediaType: "image", OperationID: "receipt-failure"}
	calls := 0
	generate := func(context.Context, int, string) (toolResult, error) {
		calls++
		_, err := runtime.db.Exec(`CREATE TRIGGER reject_quality_receipt BEFORE UPDATE OF output_cipher ON agent_task_steps
			WHEN NEW.name='media_quality:image' BEGIN SELECT RAISE(FAIL,'test receipt storage failure'); END`)
		if err != nil {
			t.Fatal(err)
		}
		return qualityTestArtifact(t, runtime, "receipt-failure.png"), nil
	}
	if _, err := runtime.executeMediaQuality(context.Background(), run, request, generate); err == nil {
		t.Fatal("receipt write failure ignored")
	}
	if _, err := runtime.db.Exec("DROP TRIGGER reject_quality_receipt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.executeMediaQuality(context.Background(), run, request, generate); err == nil || !strings.Contains(err.Error(), "uncertain") || calls != 1 {
		t.Fatalf("uncertain generation repeated: calls=%d err=%v", calls, err)
	}
}

func TestMediaQualityVideoAcceptedTaskResumesWithoutCreate(t *testing.T) {
	var creates atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			creates.Add(1)
			writeJSON(w, 200, map[string]string{"id": "persisted-video", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4())
		default:
			writeJSON(w, 200, map[string]string{"id": "persisted-video", "status": "completed"})
		}
	}))
	defer server.Close()
	runtime := newVideoRuntime(t, videoTestConfig(t, server.URL, 10), server.Client(), t.TempDir(), time.Hour, 1)
	defer runtime.Close()
	run := visualPlanTestRun(t, runtime, "video-resume")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_, err := runtime.generateVideoAttempt(ctx, run, "video", "video", testVideoPersonaAvatar, "persisted-op", 0)
	if err == nil || creates.Load() != 1 {
		t.Fatalf("initial accepted task: creates=%d err=%v", creates.Load(), err)
	}
	runtime.videoPollInterval = time.Millisecond
	result, err := runtime.generateVideoAttempt(context.Background(), run, "video", "video", testVideoPersonaAvatar, "persisted-op", 0)
	if err != nil || creates.Load() != 1 || len(result.Attachments) != 1 {
		t.Fatalf("resume: creates=%d result=%+v err=%v", creates.Load(), result, err)
	}
}
