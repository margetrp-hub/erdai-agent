package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOptimizationArtifactPreviewIsAdminOnlyAndConfined(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.adminToken, runtime.runtimeToken = managementAdminToken, managementRuntimeToken
	run := insertQuotaTestRun(t, runtime, "artifact-preview", "member")
	runtime.mediaDir = t.TempDir()
	if err := os.WriteFile(filepath.Join(runtime.mediaDir, "result.png"), []byte("test-image-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime.mediaDir, "active.svg"), []byte("test-active-content"), 0600); err != nil {
		t.Fatal(err)
	}
	step, err := runtime.beginTaskStep(run.ID, "", "tool", "generate_image", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		name, path, mime string
		want             int
	}{
		{"image", mediaMountRoot + "/result.png", "image/png", 200},
		{"escape", mediaMountRoot + "/../private.png", "image/png", 404},
		{"absolute", "/etc/passwd", "image/png", 404},
		{"active-document", mediaMountRoot + "/active.svg", "image/svg+xml", 404},
		{"missing", mediaMountRoot + "/missing.png", "image/png", 404},
	} {
		if err = runtime.persistTaskArtifacts(run.ID, step, []agentAttachment{{Kind: "image", Name: item.name, LocalPath: item.path, MimeType: item.mime}}); err != nil {
			t.Fatal(err)
		}
		var id int64
		if err = runtime.db.QueryRow("SELECT id FROM agent_task_artifacts WHERE run_id=? AND name=?", run.ID, item.name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/tasks/" + run.ID + "/artifacts/" + strconv.FormatInt(id, 10)
		for _, auth := range []string{"", "runtime"} {
			response := managementRequest(t, runtime, http.MethodGet, path, nil, auth)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("artifact accepted auth=%q: %d", auth, response.Code)
			}
		}
		response := managementRequest(t, runtime, http.MethodGet, path, nil, "admin")
		if response.Code != item.want {
			t.Fatalf("%s got=%d want=%d body=%s", item.name, response.Code, item.want, response.Body.String())
		}
		if item.want == 200 && (response.Body.String() != "test-image-content" || response.Header().Get("Cache-Control") != "private, no-store") {
			t.Fatal("artifact content/cache headers incorrect")
		}
		wrong := managementRequest(t, runtime, http.MethodGet, strings.Replace(path, run.ID, "another-run", 1), nil, "admin")
		if wrong.Code != http.StatusNotFound {
			t.Fatal("artifact crossed task boundary")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("private-external-image"), 0600); err != nil {
		t.Fatal(err)
	}
	linkPath := createEscapingMediaLink(t, runtime.mediaDir, outside)
	if data, err := os.ReadFile(linkPath); err != nil || string(data) != "private-external-image" {
		t.Fatalf("escape fixture is not a real readable link: %q, %v", data, err)
	}
	linkRelative, err := filepath.Rel(runtime.mediaDir, linkPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.persistTaskArtifacts(run.ID, step, []agentAttachment{{Kind: "image", Name: "symlink", LocalPath: mediaMountRoot + "/" + filepath.ToSlash(linkRelative), MimeType: "image/png"}}); err != nil {
		t.Fatal(err)
	}
	var id int64
	if err = runtime.db.QueryRow("SELECT id FROM agent_task_artifacts WHERE name='symlink'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	response := managementRequest(t, runtime, http.MethodGet, "/api/v1/tasks/"+run.ID+"/artifacts/"+strconv.FormatInt(id, 10), nil, "admin")
	if response.Code != 404 {
		t.Fatal("artifact symlink escaped media root")
	}
}

func TestOptimizationVisualHistoryDoesNotExposeUserRequests(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.adminToken, runtime.runtimeToken = managementAdminToken, managementRuntimeToken
	run := insertQuotaTestRun(t, runtime, "visual-history", "member")
	for i := 0; i < 10; i++ {
		plan := visualGenerationPlan{Version: 1, AppearanceID: "library", UserPrompt: "private-user-request", Prompt: "private-provider-prompt", Identity: "private-identity", Variables: map[string]string{"primaryColor": "red"}, Attempt: i}
		if err := runtime.writeVisualRecord(run, visualPlanName("library"), i, map[string]int{"attempt": i}, plan); err != nil {
			t.Fatal(err)
		}
	}
	path := "/api/v1/observability/visual-history?libraryId=library"
	if response := managementRequest(t, runtime, http.MethodGet, path, nil, "runtime"); response.Code != 401 {
		t.Fatal("runtime exposed private history")
	}
	response := managementRequest(t, runtime, http.MethodGet, path, nil, "admin")
	var payload struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &payload) != nil || len(payload.Data.Items) != 8 {
		t.Fatalf("history not bounded: %s", response.Body.String())
	}
	if strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "reference") {
		t.Fatal("private prompts/references leaked into history")
	}
	other := managementRequest(t, runtime, http.MethodGet, strings.Replace(path, "libraryId=library", "libraryId=other", 1), nil, "admin")
	if !strings.Contains(other.Body.String(), `"items":[]`) {
		t.Fatal("history crossed appearance library")
	}
}

func TestOptimizationArtifactPreviewDeduplicatesQualityAndToolReceipts(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "preview-dedup", "member")
	for _, name := range []string{"media_quality:image", "generate_image"} {
		step, err := runtime.beginTaskStep(run.ID, "", "tool", name, 0, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err = runtime.persistTaskArtifacts(run.ID, step, []agentAttachment{{Kind: "image", Name: "image.png", LocalPath: mediaMountRoot + "/image.png", MimeType: "image/png"}}); err != nil {
			t.Fatal(err)
		}
	}
	graph, err := runtime.taskGraph(context.Background(), run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(graph["artifacts"].([]taskArtifactView)) != 1 {
		t.Fatal("same actual artifact appeared twice")
	}
}

func TestOptimizationFeedbackIdempotencyAndSourceSeparation(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "feedback-run", "feedback-member")
	ctx := context.Background()
	var group sync.WaitGroup
	for i := 0; i < 8; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			if err := runtime.recordTaskFeedback(ctx, run.ID, "message-1", "correction", "conversation"); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
	if err := runtime.recordTaskFeedback(ctx, run.ID, "message-1", "accepted", "conversation"); err == nil {
		t.Fatal("same event changed its feedback outcome")
	}
	if err := runtime.recordTaskFeedback(ctx, run.ID, "message-1", "accepted", "admin"); err != nil {
		t.Fatal(err)
	}
	values, err := runtime.taskFeedbackEvents(ctx, run.ID)
	if err != nil || len(values) != 2 {
		t.Fatalf("feedback = %+v, %v", values, err)
	}
	stats, err := runtime.taskQualityObservability(ctx)
	if err != nil || stats.Corrections != 1 || stats.Accepted != 0 || stats.AdminFeedback != 1 || stats.Passed != 0 {
		t.Fatalf("feedback stats = %+v, %v", stats, err)
	}
}

func TestOptimizationFeedbackAcceptsOpaquePlatformEventIDs(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "opaque-feedback", "member")
	for i := 0; i < 2; i++ {
		if err := runtime.recordTaskFeedback(context.Background(), run.ID, "qq:message/+opaque=", "accepted", "conversation"); err != nil {
			t.Fatal(err)
		}
	}
	events, err := runtime.taskFeedbackEvents(context.Background(), run.ID)
	if err != nil || len(events) != 1 || events[0].Source != "conversation" {
		t.Fatalf("opaque platform feedback: %+v %v", events, err)
	}
}

func TestOptimizationFeedbackAPIRequiresAdminAndValidEvent(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.adminToken, runtime.runtimeToken = managementAdminToken, managementRuntimeToken
	run := insertQuotaTestRun(t, runtime, "feedback-api", "feedback-member")
	path := "/api/v1/tasks/" + run.ID
	for _, auth := range []string{"", "runtime"} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			target := path
			if method == http.MethodPost {
				target += "/feedback"
			}
			response := managementRequest(t, runtime, method, target, map[string]any{"eventId": "one", "kind": "accepted"}, auth)
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s accepted %q: %d", method, target, auth, response.Code)
			}
		}
	}
	for _, input := range []map[string]any{
		{"eventId": "", "kind": "accepted"}, {"eventId": "event one", "kind": "accepted"},
		{"eventId": "one", "kind": "silent_acceptance"}, {"eventId": "one", "kind": "accepted", "source": "conversation"},
	} {
		response := managementRequest(t, runtime, http.MethodPost, path+"/feedback", input, "admin")
		if response.Code != http.StatusBadRequest {
			t.Fatalf("invalid input %+v accepted: %d %s", input, response.Code, response.Body.String())
		}
	}
	response := managementRequest(t, runtime, http.MethodPost, path+"/feedback", map[string]any{"eventId": "one", "kind": "accepted"}, "admin")
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("feedback response = %d %s", response.Code, response.Body.String())
	}
}

func TestOptimizationQualityMetricsDoNotConvertDeliveryIntoApproval(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "quality-metrics", "feedback-member")
	ctx := context.Background()
	record := mediaQualityReceipt{mediaQualityReport: mediaQualityReport{
		Version: 1, MediaType: "image", Status: "unverified", SelectedAttempt: 1,
		Attempts: []mediaQualityAttempt{{Attempt: 0, GenerationStatus: "completed"}, {Attempt: 1, GenerationStatus: "completed"}},
	}, Results: []toolResult{{Content: "private-receipt-content"}}}
	stepID, err := runtime.beginTaskStep(run.ID, "", "tool", "media_quality:image", 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.finishTaskStep(stepID, "succeeded", "", record); err != nil {
		t.Fatal(err)
	}
	stats, err := runtime.taskQualityObservability(ctx)
	if err != nil || stats.QualityOperations != 1 || stats.Unverified != 1 || stats.GenerationRounds != 2 || stats.Passed != 0 || stats.Accepted != 0 {
		t.Fatalf("quality stats = %+v, %v", stats, err)
	}
	details, err := runtime.taskOptimizationDetails(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(details)
	if strings.Contains(string(encoded), "private-receipt-content") || strings.Contains(string(encoded), `"results"`) {
		t.Fatal("private receipt exposed by task details")
	}
}

func TestOptimizationFeedbackRetentionPreservesActiveTasks(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	for _, state := range []string{"running", "delivered"} {
		run := insertQuotaTestRun(t, runtime, "retention-"+state, "member")
		if err := runtime.recordTaskFeedback(ctx, run.ID, "one", "accepted", "conversation"); err != nil {
			t.Fatal(err)
		}
		if _, err := runtime.db.Exec("UPDATE agent_runs SET state=? WHERE id=?", state, run.ID); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().UTC().Add(-31 * 24 * time.Hour).Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec("UPDATE agent_task_steps SET updated_at=? WHERE name LIKE 'task_feedback:%'", old); err != nil {
		t.Fatal(err)
	}
	if err := runtime.pruneTaskOptimizationMetadata(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	active, err := runtime.taskFeedbackEvents(ctx, "retention-running")
	if err != nil || len(active) != 1 {
		t.Fatalf("active feedback removed: %+v %v", active, err)
	}
	terminal, err := runtime.taskFeedbackEvents(ctx, "retention-delivered")
	if err != nil || len(terminal) != 0 {
		t.Fatalf("expired feedback retained: %+v %v", terminal, err)
	}
}

func TestOptimizationPolicyStrictTypes(t *testing.T) {
	for _, key := range []string{"mediaQualityEnabled", "visualVariationEnabled"} {
		if _, err := mgmtValidateIntegration("image_policy", map[string]any{}, map[string]any{key: "false"}); err == nil {
			t.Fatalf("accepted string for %s", key)
		}
		if _, err := mgmtValidateIntegration("image_policy", map[string]any{}, map[string]any{key: false}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := mgmtValidateIntegration("companion_policy", map[string]any{}, map[string]any{"taskUnderstandingEnabled": 1}); err == nil {
		t.Fatal("accepted nonboolean task flag")
	}
}
