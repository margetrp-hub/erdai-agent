package main

import (
	"context"
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

type decisionErrorTransport struct{}

func (decisionErrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private-network-error-with-secret")
}

func TestGroupParticipationDecisionObservability(t *testing.T) {
	for _, test := range []struct {
		name, setup, decision, response, wantCode, wantResult string
		status                                                int
		wantCall, wantAllowed                                 bool
	}{
		{name: "reply", decision: `{"action":"reply"}`, wantCall: true, wantAllowed: true, wantResult: "reply"},
		{name: "ignore", decision: `{"action":"ignore"}`, wantCall: true, wantResult: "ignore"},
		{name: "legacy-reply", decision: "REPLY", wantCall: true, wantAllowed: true, wantResult: "reply"},
		{name: "legacy-ignore", decision: "IGNORE", wantCall: true, wantResult: "ignore"},
		{name: "http-auth", status: 401, wantCall: true, wantCode: "provider_http"},
		{name: "http-server", status: 503, wantCall: true, wantCode: "provider_http"},
		{name: "timeout", setup: "timeout", wantCall: true, wantCode: "timeout"},
		{name: "cancelled", setup: "cancelled", wantCode: "cancelled"},
		{name: "invalid-outer-json", response: "private-response-content", wantCall: true, wantCode: "invalid_response"},
		{name: "invalid-decision-json", decision: "private-decision-content", wantCall: true, wantCode: "invalid_response"},
		{name: "unknown-action", decision: `{"action":"private-unknown-action"}`, wantCall: true, wantCode: "invalid_response"},
		{name: "missing-action", decision: `{}`, wantCall: true, wantCode: "invalid_response"},
		{name: "no-choices", response: `{"choices":[]}`, wantCall: true, wantCode: "invalid_response"},
		{name: "disabled-route", setup: "route", wantCode: "route"},
		{name: "disabled-connection", setup: "connection", wantCode: "connection"},
		{name: "missing-credential", setup: "credential", wantCode: "credential"},
		{name: "missing-context", setup: "context", wantCode: "context"},
		{name: "network-error", setup: "network", wantCode: "connection"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if test.setup == "timeout" {
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
					return
				}
				if test.status != 0 {
					http.Error(w, "private-provider-response-with-secret", test.status)
					return
				}
				if test.response != "" {
					_, _ = w.Write([]byte(test.response))
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"choices": []map[string]any{{"message": map[string]string{"content": test.decision}}},
				})
			}))
			defer provider.Close()
			runtime := newIdleRuntime(t)
			runtime.client = provider.Client()
			insertTestEndpoint(t, runtime.configStore.db, "observed-decision", "decision-test", []string{"chat"}, "llm", "openai_chat")
			bindTestModelConnection(t, runtime.configStore.db, "observed-decision", provider.URL+"/v1")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch test.setup {
			case "route":
				if _, err := runtime.configStore.db.Exec("UPDATE model_endpoints SET enabled = 0 WHERE id = 'observed-decision'"); err != nil {
					t.Fatal(err)
				}
			case "connection":
				if _, err := runtime.configStore.db.Exec("UPDATE provider_connections SET enabled = 0 WHERE id = 'test-bound-observed-decision'"); err != nil {
					t.Fatal(err)
				}
			case "credential":
				if _, err := runtime.configStore.db.Exec("UPDATE provider_connections SET credential_ref = '' WHERE id = 'test-bound-observed-decision'"); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				cancel()
			case "context":
				memory := runtime.memory
				runtime.memory = nil
				defer func() { runtime.memory = memory }()
			case "network":
				runtime.client = &http.Client{Transport: decisionErrorTransport{}}
			}
			var event transportEvent
			raw, _ := json.Marshal(testTransportEvent("private-event", "private-message", false))
			if err := json.Unmarshal(raw, &event); err != nil {
				t.Fatal(err)
			}
			event.Sender.Key = "private-sender"
			event.Conversation.Key = "private-conversation"
			allowed, err := runtime.modelAllowsGroupParticipation(ctx, event, event.Message.Text,
				groupParticipationPolicy{DecisionProviderID: "observed-decision", DecisionTimeoutSeconds: 1})
			if allowed != test.wantAllowed || (err != nil) != (test.wantCode != "") {
				t.Fatalf("decision = %v, %v; want allowed %v, failure %q", allowed, err, test.wantAllowed, test.wantCode)
			}
			if (calls.Load() > 0) != test.wantCall || calls.Load() > 1 {
				t.Fatalf("provider calls = %d, want call %v", calls.Load(), test.wantCall)
			}
			var count int
			if err := runtime.configStore.db.QueryRow("SELECT count(*) FROM audit_events WHERE action = ?", groupParticipationDecisionAudit).Scan(&count); err != nil || count != 1 {
				t.Fatalf("decision audit count = %d, err %v", count, err)
			}
			var actor, targetType, targetID, details string
			if err := runtime.configStore.db.QueryRow(`SELECT actor, target_type, COALESCE(target_id, ''), details_json
				FROM audit_events WHERE action = ?`, groupParticipationDecisionAudit).Scan(&actor, &targetType, &targetID, &details); err != nil {
				t.Fatal(err)
			}
			var observation groupParticipationDecisionObservation
			if err := json.Unmarshal([]byte(details), &observation); err != nil {
				t.Fatal(err)
			}
			wantResult := test.wantResult
			if test.wantCode != "" {
				wantResult = "error"
			}
			if observation.EndpointID != "observed-decision" || observation.Result != wantResult || observation.ErrorCode != test.wantCode || observation.DurationMS < 0 {
				t.Fatalf("decision observation = %+v", observation)
			}
			if test.setup == "timeout" && observation.DurationMS < 900 {
				t.Fatalf("timeout duration omitted: %d", observation.DurationMS)
			}
			var fields map[string]any
			if err := json.Unmarshal([]byte(details), &fields); err != nil || len(fields) != 4 {
				t.Fatalf("audit should contain only four fields: %s, %v", details, err)
			}
			if actor != "runtime" || targetType != "group_participation" || targetID != "" {
				t.Fatalf("audit unexpectedly links user context: %q/%q/%q", actor, targetType, targetID)
			}
			for _, secret := range []string{"private-", "model-test-key", provider.URL, "Authorization", "prompt"} {
				if strings.Contains(details, secret) {
					t.Fatalf("audit leaked sensitive content %q", secret)
				}
			}
		})
	}
}

func TestGroupParticipationObservabilityAggregatesOnlyLast24Hours(t *testing.T) {
	runtime := newIdleRuntime(t)
	empty, err := runtime.groupParticipationObservability(context.Background())
	if err != nil || empty.Attempts != 0 || empty.LastAttemptAt != "" || empty.FailuresByCode == nil {
		t.Fatalf("empty observability = %+v, %v", empty, err)
	}
	now := time.Now().UTC()
	lastAttempt := now.Add(-time.Minute).Format(time.RFC3339Nano)
	for _, event := range []struct {
		action, details string
		at              time.Time
	}{
		{groupParticipationDecisionAudit, `{"result":"reply","durationMs":100}`, now.Add(-5 * time.Hour)},
		{groupParticipationDecisionAudit, `{"result":"ignore","durationMs":300}`, now.Add(-4 * time.Hour)},
		{groupParticipationDecisionAudit, `{"result":"error","errorCode":"timeout","durationMs":800}`, now.Add(-3 * time.Hour)},
		{groupParticipationDecisionAudit, `{"result":"error","errorCode":"provider_http","durationMs":400}`, now.Add(-2 * time.Hour)},
		{groupParticipationDecisionAudit, `{"result":"error","errorCode":"private-error-code","durationMs":-10}`, now.Add(-time.Minute)},
		{groupParticipationDecisionAudit, `{"result":"reply","durationMs":9999}`, now.Add(-25 * time.Hour)},
		{groupParticipationDecisionAudit, `{"result":"reply","durationMs":9999}`, now.Add(time.Hour)},
		{"other.action", `{"result":"reply","durationMs":9999}`, now.Add(-time.Minute)},
		{groupParticipationDecisionAudit, `{invalid`, now.Add(-time.Minute)},
		{groupParticipationDecisionAudit, `{"result":"private-unknown-result","durationMs":9999}`, now.Add(-time.Minute)},
	} {
		if _, err := runtime.configStore.db.Exec(`INSERT INTO audit_events
			(actor, action, target_type, details_json, created_at) VALUES ('runtime', ?, 'group_participation', ?, ?)`,
			event.action, event.details, event.at.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	value, err := runtime.groupParticipationObservability(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if value.Attempts != 5 || value.Replies != 1 || value.Ignored != 1 || value.Errors != 3 || value.AvgDurationMS != 320 || value.LastAttemptAt != lastAttempt {
		t.Fatalf("24-hour observability = %+v", value)
	}
	if len(value.FailuresByCode) != 3 || value.FailuresByCode["timeout"] != 1 || value.FailuresByCode["provider_http"] != 1 || value.FailuresByCode["context"] != 1 {
		t.Fatalf("unsafe or incorrect error codes: %+v", value.FailuresByCode)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/observability", nil)
	if err := runtime.handleManagementObservability(recorder, request); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Data struct {
			GroupParticipation groupParticipationObservability `json:"groupParticipation"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if recorder.Code != http.StatusOK || envelope.Data.GroupParticipation.Attempts != 5 || strings.Contains(recorder.Body.String(), "private-") {
		t.Fatalf("management observability response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestGroupParticipationDecisionAuditSanitizesIdentifiers(t *testing.T) {
	runtime := newIdleRuntime(t)
	runtime.recordGroupParticipationDecision(context.Background(), "https://private-host/?key=private-key", time.Now(), false, "private-error")
	var details string
	if err := runtime.configStore.db.QueryRow("SELECT details_json FROM audit_events WHERE action = ?", groupParticipationDecisionAudit).Scan(&details); err != nil {
		t.Fatal(err)
	}
	var observation groupParticipationDecisionObservation
	if err := json.Unmarshal([]byte(details), &observation); err != nil {
		t.Fatal(err)
	}
	if observation.EndpointID != "" || observation.ErrorCode != "context" || strings.Contains(details, "private-") {
		t.Fatalf("unsafe decision audit: %s", details)
	}
}
