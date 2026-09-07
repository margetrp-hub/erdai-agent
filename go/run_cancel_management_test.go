package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestManagementRunCancelHandlerRequiresAdminAndCancelsExecution(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	a.adminToken, a.runtimeToken = managementAdminToken, managementRuntimeToken
	run := insertHonestyTestRun(t, a, "management-cancel", "test-group", "member-a", "group", "running", time.Now())
	if err := a.enqueueDelivery(run, agentReply{Text: "result"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec("UPDATE agent_deliveries SET status='sending',lease_owner='test-worker' WHERE run_id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	stepID, err := a.beginTaskStep(run.ID, "", "tool", "media_generation:video", 0, map[string]string{"requestId": "accepted-request"})
	if err != nil {
		t.Fatal(err)
	}
	runContext, releaseRun := a.taskRunContext(context.Background(), run.ID)
	defer releaseRun()
	otherContext, releaseOther := a.taskRunContext(context.Background(), "other-run")
	defer releaseOther()
	path := "/api/v1/runs/" + run.ID + "/cancel"
	request := func(method, route, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, route, nil)
		if token == "runtime" {
			r.Header.Set("Authorization", "Bearer "+managementRuntimeToken)
		} else if token != "" {
			r.Header.Set(adminTokenHeader, token)
		}
		w := httptest.NewRecorder()
		if !a.Handle(w, r) {
			t.Fatalf("route was not handled: %s %s", method, route)
		}
		return w
	}
	for _, token := range []string{"", "runtime", "wrong-admin"} {
		if w := request(http.MethodPost, path, token); w.Code != http.StatusUnauthorized {
			t.Fatalf("unauthorized cancel status=%d body=%s", w.Code, w.Body.String())
		}
		if runContext.Err() != nil {
			t.Fatal("unauthorized request cancelled execution")
		}
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead, http.MethodOptions} {
		if w := request(method, path, managementAdminToken); w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("method=%s status=%d body=%s", method, w.Code, w.Body.String())
		}
	}
	for _, route := range []string{
		"/api/v1/runs//cancel", "/api/v1/runs/" + run.ID + "/nested/cancel",
		path + "/", "/api/v1/runs/../runs/" + run.ID + "/cancel",
		"/api/v1/runs/%20" + run.ID + "%20/cancel",
	} {
		if w := request(http.MethodPost, route, managementAdminToken); w.Code != http.StatusNotFound {
			t.Fatalf("malformed route=%s status=%d body=%s", route, w.Code, w.Body.String())
		}
	}
	if w := request(http.MethodPost, path+"/extra", managementAdminToken); w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("extra path was accepted: %d", w.Code)
	}
	if w := request(http.MethodPost, "/api/v1/runs/missing-run/cancel", managementAdminToken); w.Code != http.StatusNotFound {
		t.Fatalf("unknown run status=%d body=%s", w.Code, w.Body.String())
	}
	var state string
	if err = a.db.QueryRow("SELECT state FROM agent_runs WHERE id=?", run.ID).Scan(&state); err != nil || state != "responding" || runContext.Err() != nil {
		t.Fatalf("invalid request changed run state=%s context=%v err=%v", state, runContext.Err(), err)
	}
	w := request(http.MethodPost, path, managementAdminToken)
	if w.Code != http.StatusOK {
		t.Fatalf("admin cancel status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			ID        string `json:"id"`
			State     string `json:"state"`
			Cancelled bool   `json:"cancelled"`
		} `json:"data"`
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.ID != run.ID || response.Data.State != "cancelled" || !response.Data.Cancelled {
		t.Fatalf("cancel response=%s err=%v", w.Body.String(), err)
	}
	if !errors.Is(runContext.Err(), context.Canceled) || otherContext.Err() != nil {
		t.Fatalf("context cancellation scope: target=%v other=%v", runContext.Err(), otherContext.Err())
	}
	var input []byte
	if err = a.db.QueryRow("SELECT state,input_cipher FROM agent_runs WHERE id=?", run.ID).Scan(&state, &input); err != nil || state != "cancelled" || len(input) != 0 {
		t.Fatalf("run state=%s input bytes=%d err=%v", state, len(input), err)
	}
	var deliveryState, leaseOwner string
	if err = a.db.QueryRow("SELECT status,COALESCE(lease_owner,'') FROM agent_deliveries WHERE run_id=?", run.ID).Scan(&deliveryState, &leaseOwner); err != nil || deliveryState != "cancelled" || leaseOwner != "" {
		t.Fatalf("outbox state=%s lease=%s err=%v", deliveryState, leaseOwner, err)
	}
	if err = a.db.QueryRow("SELECT status FROM agent_task_steps WHERE id=?", stepID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("step state=%s err=%v", state, err)
	}
	if w = request(http.MethodPost, path, managementAdminToken); w.Code != http.StatusOK {
		t.Fatalf("repeat cancellation not idempotent: %d %s", w.Code, w.Body.String())
	}
}

func TestManagementRunCancelHandlerLeavesTerminalRunsUnchanged(t *testing.T) {
	a := newTaskIntentTestRuntime(t)
	defer a.Close()
	a.adminToken = managementAdminToken
	for _, state := range []string{"delivered", "failed"} {
		run := insertHonestyTestRun(t, a, "terminal-cancel-"+state, "test-group", "member-a", "group", state, time.Now())
		r := httptest.NewRequest(http.MethodPost, "/api/v1/runs/"+run.ID+"/cancel", nil)
		r.Header.Set(adminTokenHeader, managementAdminToken)
		w := httptest.NewRecorder()
		if !a.Handle(w, r) || w.Code != http.StatusOK {
			t.Fatalf("terminal=%s status=%d body=%s", state, w.Code, w.Body.String())
		}
		var payload struct {
			Data struct {
				State     string `json:"state"`
				Cancelled bool   `json:"cancelled"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || payload.Data.Cancelled || payload.Data.State != state {
			t.Fatalf("terminal=%s response=%s err=%v", state, w.Body.String(), err)
		}
	}
}
