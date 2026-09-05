package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestOptimizationProviderBindingAndCredentialIsolation(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	t.Setenv("ERDAI_OPT_GOOD", "good-key")
	t.Setenv("ERDAI_OPT_MISSING", "")
	for _, value := range []struct {
		id, provider, credential string
		enabled                  int
	}{
		{"bound-disabled", "disabled-provider", "ERDAI_OPT_GOOD", 0},
		{"bound-missing-key", "missing-provider", "ERDAI_OPT_MISSING", 1},
		{"bound-good", "test", "ERDAI_OPT_GOOD", 1},
	} {
		_, err := runtime.configStore.db.Exec(`INSERT INTO provider_connections
			(id, provider, api_base, credential_ref, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, 'now', 'now')`,
			value.id, value.provider, "https://"+value.id+".test/v1", value.credential, value.enabled)
		if err != nil {
			t.Fatal(err)
		}
		insertTestEndpoint(t, runtime.configStore.db, value.id, value.id, []string{"chat"}, "llm", "openai")
		if _, err := runtime.configStore.db.Exec(`INSERT INTO model_endpoint_connections VALUES (?, ?, 'now')`, value.id, value.id); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"bound-disabled", "bound-missing-key"} {
		bad := nativeRouteCandidate{Endpoint: nativeModelEndpoint{ID: id, Provider: "test", Model: id, ExecutionKind: "llm"}}
		route := nativeRouteDecision{Lane: "chat", Selected: &bad}
		if targets, err := runtime.providerRouteTargets(route, providerPolicyConfig{}, []string{"legacy"}, "https://legacy.test", "legacy-key"); err == nil || len(targets) != 0 {
			t.Fatalf("invalid binding reused credentials: %+v, %v", targets, err)
		}
		route.Fallbacks = []nativeRouteCandidate{{Endpoint: nativeModelEndpoint{ID: "bound-good", Provider: "test", Model: "good", ExecutionKind: "llm"}}}
		targets, err := runtime.providerRouteTargets(route, providerPolicyConfig{}, nil, "https://legacy.test", "legacy-key")
		if err != nil || len(targets) != 1 || targets[0].APIKey != "good-key" || targets[0].EndpointID != "bound-good" {
			t.Fatalf("eligible fallback lost: %+v, %v", targets, err)
		}
	}
	if _, found, err := runtime.providerConnectionForEndpoint("bound-disabled", "test"); err == nil || found {
		t.Fatalf("disabled binding changed provider: found=%v err=%v", found, err)
	}
	runtime.modelAPIKey = "injected-default-only"
	if runtime.providerCredential("ERDAI_OPT_MISSING") != "" || runtime.providerCredential("") != "" {
		t.Fatal("an unrelated or empty credential reference inherited the model key")
	}
}

func TestOptimizationSchema84DoesNotApproveLegacyBindings(t *testing.T) {
	path, db := newTestCoreConfig(t)
	_, err := db.Exec(`DROP TABLE agent_affiliate_owners;
		INSERT INTO agent_affiliate_bindings VALUES ('qq_official','qq','legacy','ABCD','now','now');
		INSERT INTO agent_points_ledger VALUES ('legacy-points','qq_official','qq','legacy','adjustment',200,'invite:ABCD:2','existing','now');
		PRAGMA user_version = 83;`)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var owners, points, bindings, version int
	if err = store.db.QueryRow(`SELECT count(*) FROM agent_affiliate_owners`).Scan(&owners); err != nil || owners != 0 {
		t.Fatalf("legacy ownership auto-approved: %d %v", owners, err)
	}
	if err = store.db.QueryRow(`SELECT points FROM agent_points_ledger WHERE id='legacy-points'`).Scan(&points); err != nil || points != 200 {
		t.Fatalf("legacy points changed: %d %v", points, err)
	}
	if err = store.db.QueryRow(`SELECT count(*) FROM agent_affiliate_bindings WHERE sender_ref='legacy'`).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("legacy binding lost: %d %v", bindings, err)
	}
	if err = store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != 84 {
		t.Fatalf("schema version: %d %v", version, err)
	}
}

func TestOptimizationPurpleReplacementDirectives(t *testing.T) {
	for _, prompt := range []string{"不要红色改成紫色", "不要长裙换成紫色短裙", "不要红色，穿紫色衣服", "not red, use purple", "don't wear red but wear purple", "不要蓝色而是紫色衣服"} {
		if !videoPurpleRequested(prompt) {
			t.Errorf("positive replacement rejected: %s", prompt)
		}
	}
	for _, prompt := range []string{"不要改成紫色", "不要换成紫色", "不要红色和紫色", "do not use purple", "don't wear purple", "穿紫色衣服，但不要紫色", "别用紫色"} {
		if videoPurpleRequested(prompt) {
			t.Errorf("negation lost: %s", prompt)
		}
	}
}

func TestOptimizationSearchQueryIsolationAndAtomicBudget(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := seedSearchRun(t, runtime, "opt-search")
	for _, query := range []string{"A", "B"} {
		if _, _, handled, err := runtime.beginSearchRun(&run, query); handled || err != nil {
			t.Fatalf("distinct query rejected: %v %v", handled, err)
		}
		if err := runtime.finishSearchRunSuccess(run.ID, query, query+" result", []searchSource{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, query := range []string{"A", "B"} {
		text, _, handled, err := runtime.beginSearchRun(&run, query)
		if !handled || err != nil || text != query+" result" {
			t.Fatalf("query reused unrelated result: %q %v %v", text, handled, err)
		}
	}
	if _, _, handled, err := runtime.beginSearchRun(&run, "failed"); handled || err != nil {
		t.Fatal(err)
	}
	if err := runtime.finishSearchRunFailure(run.ID, "failed", errors.New("source down")); err != nil {
		t.Fatal(err)
	}
	if _, _, handled, err := runtime.beginSearchRun(&run, "fourth"); !handled || err == nil || !strings.Contains(err.Error(), "budget") {
		t.Fatalf("budget not enforced: %v %v", handled, err)
	}
	if text, _, _, err := runtime.beginSearchRun(&run, "A"); err != nil || text != "A result" {
		t.Fatalf("full budget hid cached evidence: %s %v", text, err)
	}
	concurrent := seedSearchRun(t, runtime, "opt-concurrent-search")
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, _, _, _ = runtime.beginSearchRun(&concurrent, fmt.Sprint(i)) }(i)
	}
	wg.Wait()
	var count int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_search_queries WHERE run_id = ?`, concurrent.ID).Scan(&count); err != nil || count != maxSearchQueriesPerRun {
		t.Fatalf("concurrent budget=%d %v", count, err)
	}
}

func TestOptimizationAffiliateOwnershipGateAndSourceDeduplication(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken
	ctx := context.Background()
	run := runRecord{Transport: "qq_official", TransportInstance: "qq", SenderRef: "owner"}
	if _, err := runtime.db.Exec(`INSERT INTO agent_affiliate_bindings VALUES
		('qq_official','qq','owner','ABCD','now','now'), ('qq_official','another','copy','ABCD','now','now');
		INSERT INTO agent_points_ledger VALUES ('old-award','qq_official','another','copy','adjustment',200,'invite:ABCD:2','legacy','now')`); err != nil {
		t.Fatal(err)
	}
	policy := affiliatePolicy{PointsPerPaidInvitee: 100}
	if err := runtime.creditInvitePoints(ctx, run, affiliateSummary{Code: "ABCD", PaidInviteeCount: 5}, policy); !errors.Is(err, errAffiliateOwnershipRequired) {
		t.Fatalf("public code awarded points: %v", err)
	}
	account, err := runtime.pointsAccount(ctx, run, policy)
	if err != nil || !errors.Is(account.SyncErr, errAffiliateOwnershipRequired) {
		t.Fatalf("missing verification was hidden: %+v %v", account, err)
	}
	if _, _, err := runtime.recordDailyCheckIn(ctx, run, policy); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"transport": "qq_official", "transportInstance": "qq", "senderRef": "owner", "affiliateCode": "ABCD", "ownershipConfirmed": true, "evidence": "test: checked site account and QQ identity"}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		if got := managementRequest(t, runtime, method, "/api/v1/affiliate/ownership", payload, "runtime"); got.Code != http.StatusUnauthorized {
			t.Fatalf("runtime authorized ownership operation: %d", got.Code)
		}
	}
	for i := 0; i < 2; i++ {
		if got := managementRequest(t, runtime, http.MethodPost, "/api/v1/affiliate/ownership", payload, "admin"); got.Code != http.StatusOK {
			t.Fatalf("approval: %d %s", got.Code, got.Body.String())
		}
	}
	if err := runtime.creditInvitePoints(ctx, run, affiliateSummary{Code: "ABCD", PaidInviteeCount: 5}, policy); err != nil {
		t.Fatal(err)
	}
	if points, err := runtime.pointsLedgerBalance(ctx, run); err != nil || points != 310 {
		t.Fatalf("historical source was credited again: %d %v", points, err)
	}
	copyRun := run
	copyRun.TransportInstance = "another"
	copyRun.SenderRef = "copy"
	payload["transportInstance"] = "another"
	payload["senderRef"] = "copy"
	if got := managementRequest(t, runtime, http.MethodPost, "/api/v1/affiliate/ownership", payload, "admin"); got.Code != http.StatusConflict {
		t.Fatalf("second owner accepted: %d %s", got.Code, got.Body.String())
	}
	if err := runtime.creditInvitePoints(ctx, copyRun, affiliateSummary{Code: "ABCD", PaidInviteeCount: 6}, policy); !errors.Is(err, errAffiliateOwnershipRequired) {
		t.Fatalf("copy received new points: %v", err)
	}
	if points, err := runtime.pointsLedgerBalance(ctx, copyRun); err != nil || points != 200 {
		t.Fatalf("legacy balance changed: %d %v", points, err)
	}
	got := managementRequest(t, runtime, http.MethodGet, "/api/v1/affiliate/ownership", nil, "admin")
	if got.Code != 200 || !strings.Contains(got.Body.String(), "conflict") || !strings.Contains(got.Body.String(), "verified") {
		t.Fatalf("ownership readback: %d %s", got.Code, got.Body.String())
	}
}

type optimizationBlockingConnector struct {
	started chan string
	release chan struct{}
}

func (c *optimizationBlockingConnector) ID() string                  { return "opt-connector" }
func (c *optimizationBlockingConnector) Type() string                { return "test" }
func (c *optimizationBlockingConnector) Start(context.Context) error { return nil }
func (c *optimizationBlockingConnector) Close() error                { return nil }
func (c *optimizationBlockingConnector) Health() platformConnectorHealth {
	return platformConnectorHealth{ID: c.ID()}
}
func (c *optimizationBlockingConnector) Deliver(ctx context.Context, _ platformReplyRoute, delivery leasedTransportDelivery) error {
	c.started <- delivery.Message.Text
	if delivery.Message.Text == "slow" {
		select {
		case <-c.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func TestOptimizationSlowDeliveryDoesNotBlockOtherConversation(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	c := &optimizationBlockingConnector{started: make(chan string, 10), release: make(chan struct{})}
	ctx := context.Background()
	for i, item := range []struct{ group, text string }{{"one", "slow"}, {"one", "after-slow"}, {"two", "fast"}} {
		run := insertHonestyTestRun(t, runtime, fmt.Sprintf("opt-delivery-%d", i), item.group, "sender", "group", "running", time.Now().Add(-time.Minute))
		handle, err := runtime.rememberPlatformRoute(ctx, run.EventID, platformReplyRoute{ConnectorID: c.ID(), Transport: "qq_official", Kind: "group", TargetID: item.group})
		if err != nil {
			t.Fatal(err)
		}
		run.ReplyHandle = handle
		if err := runtime.enqueueDelivery(run, agentReply{Text: item.text}, "terminal", ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runtime.db.Exec(`UPDATE agent_deliveries SET next_attempt_at = NULL`); err != nil {
		t.Fatal(err)
	}
	m := &platformConnectorManager{runtime: runtime, connectors: map[string]platformConnector{c.ID(): c}, poll: 10 * time.Millisecond}
	m.Start(ctx)
	defer m.Close()
	seen := map[string]bool{}
	for len(seen) < 2 {
		select {
		case text := <-c.started:
			if text == "after-slow" {
				t.Fatal("conversation was reordered")
			}
			seen[text] = true
		case <-time.After(5 * time.Second):
			t.Fatal("slow attachment blocked another conversation")
		}
	}
	if !seen["slow"] || !seen["fast"] {
		t.Fatalf("unexpected sends: %+v", seen)
	}
	close(c.release)
	select {
	case text := <-c.started:
		if text != "after-slow" {
			t.Fatalf("duplicate send: %s", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("same-conversation successor was not released")
	}
}
