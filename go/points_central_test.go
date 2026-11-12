package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func centralFixture(t *testing.T, h http.HandlerFunc) *centralPointsClient {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	p, err := newCentralPointsClient(server.URL, strings.Repeat("test-only-", 4))
	if err != nil {
		t.Fatal(err)
	}
	p.client = server.Client()
	p.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return p
}

func TestCentralPointsRetiredLedgerSurvivesStartupAndRejectsOldWriter(t *testing.T) {
	a := newPointsRuntime(t)
	ctx := context.Background()
	_, err := a.db.Exec(`INSERT INTO agent_points_storage_owner VALUES(1,'sub2api','fixture','2026-11-12T00:00:00Z');
	CREATE TRIGGER fixture_retired BEFORE INSERT ON agent_points_checkins BEGIN SELECT RAISE(ABORT,'retired'); END;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = initPointsAccounts(ctx, a.db); err != nil {
		t.Fatalf("startup touched frozen ledger: %v", err)
	}
	if err = verifyPointsStorageOwner(ctx, a.db, true); err != nil {
		t.Fatal(err)
	}
	if err = verifyPointsStorageOwner(ctx, a.db, false); err == nil {
		t.Fatal("retired local writer accepted")
	}
}

func TestCentralPointsOrderUsesBusinessFieldsOnly(t *testing.T) {
	a := newPointsRuntime(t)
	a.centralPoints = centralFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Action string         `json:"action"`
			Order  map[string]any `json:"order"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Action != "reserve" || len(body.Order) != 6 || body.Order["accountId"] != "account" || body.Order["createdAt"] != nil {
			t.Errorf("invalid order payload: %+v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"id":"order","accountId":"account","status":"reserved","points":5,"createdAt":"2026-11-12T00:00:00Z"}}`))
	})
	result, err := a.reservePointsOrder(context.Background(), pointsOrder{AccountID: "account", Source: "test", ExternalRef: "unique", Kind: "lottery", Points: 5, Note: "fixture"})
	if err != nil || result.Status != "reserved" {
		t.Fatalf("reserve failed: %+v %v", result, err)
	}
}

func TestCentralPointsBindingUsesPrivateAuthenticatedSender(t *testing.T) {
	a := newPointsRuntime(t)
	calls := 0
	a.centralPoints = centralFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/api/v1/integrations/erdai/points" || r.Method != "POST" || r.Header.Get("X-OHLAO-Points-Token") != "test-only-test-only-test-only-test-only-" {
			t.Error("wrong credential or endpoint")
		}
		var body struct {
			Action   string                                        `json:"action"`
			Code     string                                        `json:"code"`
			Identity struct{ Transport, Instance, Subject string } `json:"identity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Action != "bind" || body.Code != "ABC123" || body.Identity.Subject != "verified-qq" || body.Identity.Instance != "instance-a" {
			t.Errorf("wrong sender: %+v", body)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"bound":true}}`))
	})
	command, ok := a.resolveCoreDirectCommand(context.Background(), "/绑定账号 ABC123")
	if !ok || command.Kind != directCommandAccountBind {
		t.Fatal("binding command not recognized")
	}
	run := runRecord{Transport: "qq_official", TransportInstance: "instance-a", SenderRef: "verified-qq", ConversationKind: "group"}
	reply, err := a.centralPointsCommand(context.Background(), run, command)
	if err != nil || calls != 0 || !strings.Contains(reply.Text, "私聊") || strings.Contains(reply.Text, "ABC123") {
		t.Fatalf("group bind accepted or code exposed: %v %s", err, reply.Text)
	}
	run.ConversationKind = "private"
	reply, err = a.centralPointsCommand(context.Background(), run, command)
	if err != nil || calls != 1 || !strings.Contains(reply.Text, "成功") {
		t.Fatalf("private bind failed: %v %s", err, reply.Text)
	}
	if _, err = a.linkPointsIdentity(context.Background(), "old", pointIdentity(run), "manual"); err == nil {
		t.Fatal("legacy identity write allowed")
	}
	if _, err = a.mergePointsAccounts(context.Background(), "old", "new", "manual"); err == nil {
		t.Fatal("legacy merge allowed")
	}
}

func TestCentralPointsUnknownNeverFallsBack(t *testing.T) {
	for _, response := range []string{"not json", `{}`, `{"code":0,"data":null}`, `{"code":1,"message":"unknown"}`} {
		t.Run(response, func(t *testing.T) {
			a := newPointsRuntime(t)
			a.centralPoints = centralFixture(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) })
			run := runRecord{Transport: "qq_official", TransportInstance: "qq_official", SenderRef: "qq-fixture", ConversationKind: "private"}
			reply, err := a.centralPointsCommand(context.Background(), run, coreDirectCommand{Kind: directCommandCheckIn})
			if err != nil || !strings.Contains(reply.Text, "暂未确认") {
				t.Fatalf("unknown hidden: %v %s", err, reply.Text)
			}
			for _, table := range []string{"agent_points_accounts", "agent_points_identities", "agent_points_ledger", "agent_points_checkins"} {
				var count int
				if err := a.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("old ledger mutated %s %d %v", table, count, err)
				}
			}
		})
	}
}

func TestCentralPointsSiteScopeAndRecovery(t *testing.T) {
	a := newPointsRuntime(t)
	var received []map[string]any
	failed := true
	a.centralPoints = centralFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		received = append(received, body)
		if failed {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":503,"message":"temporarily unavailable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"account":{"accountId":"central-123","balance":10,"entries":[]},"awarded":false}}`))
	})
	identity := pointsIdentity{"sub2api", a.centralPoints.base, "123"}
	_, _, err := a.centralPoints.account(context.Background(), identity, "read", 20, 0)
	var apiErr *coreAPIError
	if !errors.As(err, &apiErr) || apiErr.status != 503 {
		t.Fatalf("unknown not preserved: %v", err)
	}
	failed = false
	result, awarded, err := a.centralPoints.account(context.Background(), identity, "read", 20, 0)
	if err != nil || awarded || result.Balance != 10 || result.AccountID != "central-123" {
		t.Fatalf("reconcile failed: %+v %v", result, err)
	}
	if len(received) != 2 || received[1]["user_id"] != float64(123) || received[1]["identity"] != nil {
		t.Fatal("site scope lost")
	}
	identity.TransportInstance = "https://other.invalid"
	if _, _, err = a.centralPoints.account(context.Background(), identity, "read", 20, 0); err == nil || len(received) != 2 {
		t.Fatal("cross-site identity accepted")
	}
	a.pointsSiteClients = []pointsSiteClient{{strings.Repeat("site-token", 4), "newapi", "https://new.test"}}
	r := httptest.NewRequest("POST", "/points-bridge/v1/site/account", strings.NewReader(`{"senderRef":"123","transport":"sub2api"}`))
	r.Header.Set("X-ErDai-Points-Token", a.pointsSiteClients[0].Token)
	w := httptest.NewRecorder()
	a.handlePointsSiteBridge(w, r)
	if w.Code != 400 || len(received) != 2 {
		t.Fatal("site client escalated scope")
	}
	r = httptest.NewRequest("POST", "/points-bridge/v1/site/account", strings.NewReader(`{"senderRef":"123"}`))
	r.Header.Set("X-ErDai-Points-Token", a.pointsSiteClients[0].Token)
	w = httptest.NewRecorder()
	a.handlePointsSiteBridge(w, r)
	if w.Code != 200 || len(received) != 3 {
		t.Fatalf("site bridge failed: %d %s", w.Code, w.Body.String())
	}
	scoped := received[2]["identity"].(map[string]any)
	if scoped["transport"] != "newapi" || scoped["instance"] != "https://new.test" || received[2]["user_id"] != nil {
		t.Fatal("site identity not bound to credential")
	}
}
