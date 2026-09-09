package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

func TestMediaQualityParserRejectsIncompleteOrAmbiguousOutput(t *testing.T) {
	valid := `{"status":"passed","identityIssues":[],"constraintIssues":[],"qualityIssues":[]}`
	for _, raw := range []string{
		valid + valid,
		valid + "\n" + valid,
		valid + " extra explanation",
		"Review result: " + valid,
		"```json\n" + valid,
		"```json\n" + valid + "\n```\nextra explanation",
		"```json\n" + valid + "\n```\n```json\n" + valid + "\n```",
		"```javascript\n" + valid + "\n```",
		"```json\n" + valid + valid + "\n```",
		"```json\n" + valid + " extra explanation\n```",
		"```json\n{\"status\":\"passed\",\"unexpected\":true}\n```",
		"```json\n{\"status\":\"passed\",\"identityIssues\":\"none\"}\n```",
		`{"status":"passed","identityIssues":[`,
		`{"status":"passed","identityIssues":"none"}`,
	} {
		if got := parseQualityAssessment(raw); got.Status != "unverified" || got.Reason != "invalid_assessment" {
			t.Fatalf("ambiguous or incomplete assessment accepted: %+v", got)
		}
	}
}

func TestMediaQualityParserAcceptsOneWholeJSONFence(t *testing.T) {
	for _, status := range []string{"passed", "failed"} {
		for _, fence := range []struct {
			name   string
			label  string
			ending string
		}{
			{"json", "json", "\n"},
			{"unlabeled", "", "\n"},
			{"json-crlf", "json", "\r\n"},
			{"unlabeled-crlf", "", "\r\n"},
		} {
			t.Run(status+"/"+fence.name, func(t *testing.T) {
				issues := "[]"
				if status == "failed" {
					issues = `["wrong outfit color"]`
				}
				raw := fmt.Sprintf(`{"status":%q,"identityIssues":[],"constraintIssues":%s,"qualityIssues":[]}`, status, issues)
				wrapped := " \n```" + fence.label + fence.ending + raw + fence.ending + "```\n "
				got := parseQualityAssessment(wrapped)
				if got.Status != status || got.Reason != "" || status == "failed" && (len(got.ConstraintIssues) != 1 || got.ConstraintIssues[0] != "wrong outfit color") {
					t.Fatalf("complete fenced assessment changed: %+v", got)
				}
			})
		}
	}
	if got := parseQualityAssessment("```json\n{\"status\":\"passed\",\"qualityIssues\":[\"wrong face\"]}\n```"); got.Status != "unverified" || got.Reason != "inconsistent_assessment" {
		t.Fatalf("fence bypassed status consistency: %+v", got)
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

func TestMediaQualityTargetHonorsExplicitRouteAndSkipsUnusableAutomaticRoutes(t *testing.T) {
	for _, scenario := range []string{"disabled-endpoint", "not-vision", "disabled-connection", "missing-credential", "unsupported-protocol"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			if _, err := runtime.configStore.db.Exec("UPDATE model_endpoints SET enabled=0"); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"quality-bad", "quality-good"} {
				insertTestEndpoint(t, runtime.configStore.db, id, id, []string{"vision", "chat"}, "llm", "openai")
				bindTestModelConnection(t, runtime.configStore.db, id, "https://quality.example.test/v1")
			}
			queries := map[string]string{
				"disabled-endpoint":    "UPDATE model_endpoints SET enabled=0 WHERE id='quality-bad'",
				"not-vision":           "UPDATE model_endpoints SET capabilities_json='[\"chat\"]' WHERE id='quality-bad'",
				"disabled-connection":  "UPDATE provider_connections SET enabled=0 WHERE id='test-bound-quality-bad'",
				"missing-credential":   "UPDATE provider_connections SET credential_ref='ERDAI_UNCONFIGURED_QUALITY_TEST_KEY' WHERE id='test-bound-quality-bad'",
				"unsupported-protocol": "UPDATE provider_connections SET protocol='anthropic_messages' WHERE id='test-bound-quality-bad'",
			}
			if _, err := runtime.configStore.db.Exec(queries[scenario]); err != nil {
				t.Fatal(err)
			}
			if target, err := runtime.mediaQualityTarget(t.Context(), ""); err != nil || target.EndpointID != "quality-good" {
				t.Fatalf("automatic selection did not skip unavailable route: endpoint=%s err=%v", target.EndpointID, err)
			}
			if target, err := runtime.mediaQualityTarget(t.Context(), "quality-bad"); err == nil || target.EndpointID != "" {
				t.Fatalf("explicit unavailable endpoint silently fell back: endpoint=%s err=%v", target.EndpointID, err)
			}
		})
	}
}

type qualityRoundTripper func(*http.Request) (*http.Response, error)

func (transport qualityRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestMediaQualityVideoPhaseBudgetsAndParentCancellation(t *testing.T) {
	for _, scenario := range []string{"independent-budgets", "parent-deadline", "parent-cancel"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			insertTestEndpoint(t, runtime.configStore.db, "quality-test", "vision-test", []string{"vision"}, "llm", "openai")
			bindTestModelConnection(t, runtime.configStore.db, "quality-test", "https://quality.example.test")
			t.Setenv("ERDAI_MEDIA_CHECK_URL", "http://127.0.0.1:19272")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if scenario == "parent-deadline" {
				var cancelDeadline context.CancelFunc
				ctx, cancelDeadline = context.WithTimeout(ctx, 5*time.Second)
				defer cancelDeadline()
			}
			var probeDeadline, visionDeadline time.Time
			calls := 0
			runtime.client = &http.Client{Transport: qualityRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				deadline, found := r.Context().Deadline()
				if !found {
					t.Fatal("quality phase has no deadline")
				}
				var body any
				if r.URL.Path == "/inspect" {
					probeDeadline = deadline
					if scenario == "parent-cancel" {
						cancel()
						return nil, r.Context().Err()
					}
					options := videoGenerationOptionsForPrompt("")
					var width, height int
					_, _ = fmt.Sscanf(options.AspectRatio, "%d:%d", &width, &height)
					frames := make([]string, 8)
					for index := range frames {
						frames[index] = "data:image/jpeg;base64,dGVzdA=="
					}
					body = mediaCheckResponse{Valid: true, Duration: float64(options.Duration), Width: width, Height: height, Frames: frames}
				} else {
					visionDeadline = deadline
					body = map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": `{"status":"passed","identityIssues":[],"constraintIssues":[],"qualityIssues":[]}`}}}}
				}
				encoded, _ := json.Marshal(body)
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(encoded)))}, nil
			})}
			assessment, err := runtime.assessMediaQuality(ctx, runRecord{}, mediaQualityRequest{MediaType: "video"},
				toolResult{Attachments: []agentAttachment{{Kind: "video", Name: "test.mp4", LocalPath: "/erdai-media/test.mp4"}}},
				mediaQualityPolicy{EndpointID: "quality-test"})
			if scenario == "parent-cancel" {
				if !errors.Is(err, context.Canceled) || calls != 1 {
					t.Fatalf("parent cancellation proceeded to vision or delivery: calls=%d err=%v", calls, err)
				}
				return
			}
			if err != nil || assessment.Status != "passed" || assessment.EndpointID != "quality-test" || assessment.CheckedAt == "" || calls != 2 {
				t.Fatalf("assessment=%+v calls=%d err=%v", assessment, calls, err)
			}
			if scenario == "parent-deadline" {
				parentDeadline, _ := ctx.Deadline()
				if !probeDeadline.Equal(parentDeadline) || !visionDeadline.Equal(parentDeadline) {
					t.Fatal("quality phases exceeded the parent deadline")
				}
			} else if !visionDeadline.After(probeDeadline.Add(4 * time.Second)) {
				t.Fatal("local extraction and visual assessment still share a deadline")
			}
		})
	}
}

func TestMediaQualityUnavailableAssessmentKeepsSafeRouteEvidence(t *testing.T) {
	for _, scenario := range []string{"http", "timeout", "empty-response", "missing-route"} {
		t.Run(scenario, func(t *testing.T) {
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			insertTestEndpoint(t, runtime.configStore.db, "quality-test", "vision-test", []string{"vision"}, "llm", "openai")
			bindTestModelConnection(t, runtime.configStore.db, "quality-test", "https://quality.example.test")
			if scenario == "missing-route" {
				if _, err := runtime.configStore.db.Exec("UPDATE model_endpoints SET enabled=0 WHERE id='quality-test'"); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			runtime.client = &http.Client{Transport: qualityRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if scenario == "timeout" {
					return nil, context.DeadlineExceeded
				}
				code, body := http.StatusServiceUnavailable, "private-upstream-error-body"
				if scenario == "empty-response" {
					code, body = http.StatusOK, `{"choices":[]}`
				}
				return &http.Response{StatusCode: code, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			assessment, err := runtime.assessMediaQuality(t.Context(), runRecord{}, mediaQualityRequest{MediaType: "image"},
				qualityTestArtifact(t, runtime, "unavailable.png"), mediaQualityPolicy{EndpointID: "quality-test"})
			want := map[string]string{"http": "vision_check_http_503", "timeout": "vision_check_timeout", "empty-response": "vision_check_invalid_response", "missing-route": "vision_route_unavailable"}[scenario]
			if err != nil || assessment.Status != "unverified" || assessment.Reason != want || assessment.EndpointID != "quality-test" || assessment.CheckedAt == "" || assessment.ElapsedMS < 0 {
				t.Fatalf("missing failure evidence: assessment=%+v err=%v", assessment, err)
			}
			if scenario == "missing-route" && calls != 0 {
				t.Fatal("disabled quality endpoint was invoked")
			}
			encoded, _ := json.Marshal(assessment)
			if strings.Contains(string(encoded), "private-upstream-error-body") {
				t.Fatal("upstream body leaked into quality evidence")
			}
		})
	}
}

func TestMediaQualityUsesLowReasoningOnlyForSupportedModels(t *testing.T) {
	for _, model := range []string{"grok-4.5", "grok-4.6", "grok-4.20", "gpt-5.6-terra", "grok-4.5-preview"} {
		t.Run(model, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				if model == "grok-4.5" || model == "grok-4.6" {
					if payload["reasoning_effort"] != "low" {
						t.Error("latency-sensitive visual check did not select low reasoning")
					}
				} else if _, found := payload["reasoning_effort"]; found {
					t.Error("unsupported model received a reasoning parameter")
				}
				if payload["model"] != model || payload["max_tokens"] != float64(1000) {
					t.Error("quality model or output budget changed")
				}
				writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{"message": map[string]string{
					"content": `{"status":"passed","identityIssues":[],"constraintIssues":[],"qualityIssues":[]}`,
				}}}})
			}))
			defer server.Close()
			runtime := newIdleRuntime(t)
			defer runtime.Close()
			runtime.client = server.Client()
			insertTestEndpoint(t, runtime.configStore.db, "quality-test", model, []string{"vision"}, "llm", "openai")
			bindTestModelConnection(t, runtime.configStore.db, "quality-test", server.URL)
			assessment, err := runtime.assessMediaQuality(t.Context(), runRecord{}, mediaQualityRequest{MediaType: "image"},
				qualityTestArtifact(t, runtime, "quality.png"), mediaQualityPolicy{EndpointID: "quality-test"})
			if err != nil || assessment.Status != "passed" || calls.Load() != 1 {
				t.Fatalf("visual assessment failed: assessment=%+v calls=%d err=%v", assessment, calls.Load(), err)
			}
		})
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
				} else if !result.PreserveUserMessage || status == "failed" && !strings.Contains(result.UserMessage, "质量核验仍未通过") ||
					status == "unverified" && (!strings.Contains(result.UserMessage, "这次没能完成画面检查") || strings.Contains(result.UserMessage, "尚未")) {
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
	parent, finish := context.WithTimeout(t.Context(), 10*time.Second)
	defer finish()
	var creates, polls atomic.Int32
	firstPoll := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			creates.Add(1)
			writeJSON(w, 200, map[string]string{"id": "persisted-video", "status": "queued"})
		case strings.HasSuffix(r.URL.Path, "/content"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write(testMP4())
		default:
			if polls.Add(1) == 1 {
				close(firstPoll)
				select {
				case <-r.Context().Done():
				case <-parent.Done():
				}
				return
			}
			writeJSON(w, 200, map[string]string{"id": "persisted-video", "status": "completed"})
		}
	}))
	defer server.Close()
	runtime := newVideoRuntime(t, videoTestConfig(t, server.URL, 10), server.Client(), t.TempDir(), time.Millisecond, 1)
	defer runtime.Close()
	run := visualPlanTestRun(t, runtime, "video-resume")
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	firstResult := make(chan error, 1)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		_, err := runtime.generateVideoAttempt(ctx, run, "video", "video", testVideoPersonaAvatar, "persisted-op", 0)
		firstResult <- err
	}()
	defer func() {
		cancel()
		select {
		case <-firstDone:
		case <-parent.Done():
		}
	}()
	// The first GET happens only after the accepted provider task is persisted.
	select {
	case <-firstPoll:
	case err := <-firstResult:
		t.Fatalf("video attempt ended before first poll: creates=%d err=%v", creates.Load(), err)
	case <-parent.Done():
		t.Fatalf("video attempt did not reach first poll: %v", parent.Err())
	}
	cancel()
	select {
	case err := <-firstResult:
		if !errors.Is(err, context.Canceled) || creates.Load() != 1 || polls.Load() != 1 {
			t.Fatalf("cancel accepted task: creates=%d polls=%d err=%v", creates.Load(), polls.Load(), err)
		}
	case <-parent.Done():
		t.Fatalf("cancelled video attempt did not stop: %v", parent.Err())
	}
	result, err := runtime.generateVideoAttempt(parent, run, "video", "video", testVideoPersonaAvatar, "persisted-op", 0)
	if err != nil || creates.Load() != 1 || polls.Load() != 2 || len(result.Attachments) != 1 {
		t.Fatalf("resume: creates=%d polls=%d result=%+v err=%v", creates.Load(), polls.Load(), result, err)
	}
}
