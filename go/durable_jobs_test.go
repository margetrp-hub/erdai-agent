package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDurableJobWorkerStateMachine(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.db = runtime.configStore.db
	runtime.aead = durableJobTestAEAD(t)
	job, existing, err := runtime.createDurableJob(context.Background(), durableJobCreatePayload{Goal: "执行一次测试任务", MaxAttempts: 2})
	if err != nil || existing {
		t.Fatalf("create job: %#v existing=%v", err, existing)
	}
	claimed, ok, err := runtime.claimDurableJob(context.Background(), "test-worker")
	if err != nil || !ok || claimed.Status != durableJobLeased || claimed.Attempts != 1 {
		t.Fatalf("claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if err = runtime.setDurableJobRunning(context.Background(), job.ID, "test-worker"); err != nil {
		t.Fatal(err)
	}
	if err = runtime.finishDurableJob(context.Background(), claimed, "test-worker", runRecord{}, agentReply{Text: "完成"}, nil); err != nil {
		t.Fatal(err)
	}
	finished, err := runtime.durableJobRecord(context.Background(), job.ID)
	if err != nil || finished.Status != durableJobSucceeded || finished.ResultText != "完成" {
		t.Fatalf("success = %#v err=%v", finished, err)
	}

	retryJob, _, err := runtime.createDurableJob(context.Background(), durableJobCreatePayload{Goal: "需要重试", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	retryClaim, ok, err := runtime.claimDurableJob(context.Background(), "test-worker")
	if err != nil || !ok {
		t.Fatalf("retry claim = %#v ok=%v err=%v", retryClaim, ok, err)
	}
	if err = runtime.setDurableJobRunning(context.Background(), retryJob.ID, "test-worker"); err != nil {
		t.Fatal(err)
	}
	if err = runtime.finishDurableJob(context.Background(), retryClaim, "test-worker", runRecord{}, agentReply{}, errors.New("provider unavailable")); err != nil {
		t.Fatal(err)
	}
	retrying, err := runtime.durableJobRecord(context.Background(), retryJob.ID)
	if err != nil || retrying.Status != durableJobRetrying || retrying.NextRunAt == "" {
		t.Fatalf("retry = %#v err=%v", retrying, err)
	}
}

func TestDurableJobRecurringPlanRequeuesAfterSuccess(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.db = runtime.configStore.db
	runtime.aead = durableJobTestAEAD(t)
	job, _, err := runtime.createDurableJob(context.Background(), durableJobCreatePayload{
		Goal: "每天检查渠道", Plan: map[string]any{"kind": "recurring", "interval": "daily"}, MaxAttempts: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := runtime.claimDurableJob(context.Background(), "test-worker")
	if err != nil || !ok {
		t.Fatalf("claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	if err = runtime.setDurableJobRunning(context.Background(), job.ID, "test-worker"); err != nil {
		t.Fatal(err)
	}
	if err = runtime.finishDurableJob(context.Background(), claimed, "test-worker", runRecord{}, agentReply{Text: "本轮完成"}, nil); err != nil {
		t.Fatal(err)
	}
	requeued, err := runtime.durableJobRecord(context.Background(), job.ID)
	if err != nil || requeued.Status != durableJobPlanned || requeued.NextRunAt == "" || requeued.FinishedAt != "" {
		t.Fatalf("recurring result = %#v err=%v", requeued, err)
	}
}

func decodeManagementData(t *testing.T, response *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func decodeManagementList(t *testing.T, response *httptest.ResponseRecorder) []any {
	t.Helper()
	var envelope struct {
		Data []any `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func durableJobTestAEAD(t *testing.T) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return aead
}

func TestDurableJobLifecycleAndIdempotency(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.aead = durableJobTestAEAD(t)
	payload := map[string]any{
		"ownerId": "owner-1", "workspaceId": "workspace-1", "botId": "doubao",
		"title": "渠道巡检", "goal": "检查渠道状态并在异常时提醒我",
		"plan":           map[string]any{"kind": "monitor", "interval": "daily"},
		"idempotencyKey": "job-once-1",
	}
	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/durable-tasks", payload, "admin")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	value := decodeManagementData(t, created)
	id, _ := value["id"].(string)
	if id == "" || value["status"] != durableJobQueued || value["goal"] != payload["goal"] {
		t.Fatalf("created = %#v", value)
	}
	duplicate := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs", payload, "admin")
	if duplicate.Code != http.StatusOK || decodeManagementData(t, duplicate)["id"] != id {
		t.Fatalf("idempotency = %d: %s", duplicate.Code, duplicate.Body.String())
	}
	paused := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+id+"/pause", nil, "admin")
	if paused.Code != http.StatusAccepted || decodeManagementData(t, paused)["status"] != durableJobPaused {
		t.Fatalf("pause = %d: %s", paused.Code, paused.Body.String())
	}
	resumed := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+id+"/resume", nil, "admin")
	if resumed.Code != http.StatusAccepted || decodeManagementData(t, resumed)["status"] != durableJobQueued {
		t.Fatalf("resume = %d: %s", resumed.Code, resumed.Body.String())
	}
	cancelled := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+id+"/cancel", nil, "admin")
	if cancelled.Code != http.StatusAccepted || decodeManagementData(t, cancelled)["status"] != durableJobCancelled {
		t.Fatalf("cancel = %d: %s", cancelled.Code, cancelled.Body.String())
	}
	retried := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+id+"/retry", nil, "admin")
	if retried.Code != http.StatusAccepted || decodeManagementData(t, retried)["status"] != durableJobRetrying {
		t.Fatalf("retry = %d: %s", retried.Code, retried.Body.String())
	}
	events := managementRequest(t, runtime, http.MethodGet, "/api/v1/jobs/"+id+"/events", nil, "admin")
	if events.Code != http.StatusOK {
		t.Fatalf("events = %d: %s", events.Code, events.Body.String())
	}
	if len(decodeManagementList(t, events)) < 5 {
		t.Fatalf("events missing transitions: %s", events.Body.String())
	}
}

func TestDurableJobApprovalAndReconciler(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.aead = durableJobTestAEAD(t)
	approval := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs", map[string]any{
		"goal": "删除旧缓存", "requiresApproval": true,
	}, "admin")
	if approval.Code != http.StatusCreated || decodeManagementData(t, approval)["status"] != durableJobWaitingApproval {
		t.Fatalf("approval = %d: %s", approval.Code, approval.Body.String())
	}
	approvalID, _ := decodeManagementData(t, approval)["id"].(string)
	blockedResume := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+approvalID+"/resume", nil, "admin")
	if blockedResume.Code != http.StatusBadRequest {
		t.Fatalf("approval resume should be rejected, got %d: %s", blockedResume.Code, blockedResume.Body.String())
	}
	approved := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs/"+approvalID+"/approve", nil, "admin")
	approvedValue := decodeManagementData(t, approved)
	if approved.Code != http.StatusAccepted || approvedValue["status"] != durableJobQueued || approvedValue["requiresApproval"] != false {
		t.Fatalf("approve = %d: %s", approved.Code, approved.Body.String())
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	due := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs", map[string]any{
		"goal": "生成日报", "nextRunAt": past, "startImmediately": false,
	}, "admin")
	if due.Code != http.StatusCreated || decodeManagementData(t, due)["status"] != durableJobPlanned {
		t.Fatalf("planned = %d: %s", due.Code, due.Body.String())
	}
	dueID, _ := decodeManagementData(t, due)["id"].(string)
	runtime.reconcileDurableJobs(context.Background())
	value, err := runtime.durableJobRecord(context.Background(), dueID)
	if err != nil {
		t.Fatal(err)
	}
	if value.Status != durableJobQueued {
		t.Fatalf("reconciled status = %q", value.Status)
	}
	autoGate := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs", map[string]any{
		"goal": "部署新版本到生产环境",
	}, "admin")
	if autoGate.Code != http.StatusCreated {
		t.Fatalf("automatic approval gate = %d: %s", autoGate.Code, autoGate.Body.String())
	}
	autoGateValue := decodeManagementData(t, autoGate)
	if autoGateValue["status"] != durableJobWaitingApproval || autoGateValue["requiresApproval"] != true {
		t.Fatalf("high-risk goal was not gated: %#v", autoGateValue)
	}
	deferredGate := managementRequest(t, runtime, http.MethodPost, "/api/v1/jobs", map[string]any{
		"goal": "清理服务器旧日志", "startImmediately": false,
	}, "admin")
	deferredGateValue := decodeManagementData(t, deferredGate)
	if deferredGate.Code != http.StatusCreated || deferredGateValue["status"] != durableJobWaitingApproval || deferredGateValue["requiresApproval"] != true {
		t.Fatalf("deferred high-risk goal was not gated: %#v", deferredGateValue)
	}
}
