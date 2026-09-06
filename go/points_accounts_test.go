package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func newPointsRuntime(t *testing.T) *AgentRuntime {
	t.Helper()
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "points.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return &AgentRuntime{db: store.db, configStore: store, adminToken: managementAdminToken}
}

func seedPointsAccount(t *testing.T, a *AgentRuntime, instance, sender string, points int64) (runRecord, string) {
	t.Helper()
	run := runRecord{Transport: "aiocqhttp", TransportInstance: instance, SenderRef: sender}
	id, err := a.pointsIdentityAccount(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	if points != 0 {
		_, err = a.db.Exec(`INSERT INTO agent_points_ledger VALUES (?, 'aiocqhttp', ?, ?, 'adjustment', ?, 'test-seed', 'test fixture', ?)`,
			id+"_seed", instance, sender, points, time.Now().UTC().Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	return run, id
}

func TestPointsMigrationPreservesLedgerAndCheckin(t *testing.T) {
	a := newPointsRuntime(t)
	today := shanghaiDate(time.Now())
	_, err := a.db.Exec(`INSERT INTO agent_affiliate_bindings VALUES ('aiocqhttp','old','qq-1','ABCD','now','now');
		INSERT INTO agent_points_ledger VALUES ('old-award','aiocqhttp','old','qq-1','adjustment',100,'invite:ABCD:1','original','now');
		INSERT INTO agent_points_ledger VALUES ('old-signin','aiocqhttp','old','qq-1','check_in',10,?,'original','now');
		PRAGMA user_version = 84;`, "check-in:"+today)
	if err != nil {
		t.Fatal(err)
	}
	if err = migrateCoreConfig(a.db); err != nil {
		t.Fatal(err)
	}
	if err = migrateCoreConfig(a.db); err != nil {
		t.Fatal(err)
	}
	run := runRecord{Transport: "aiocqhttp", TransportInstance: "old", SenderRef: "qq-1"}
	awarded, balance, err := a.recordDailyCheckIn(context.Background(), run, affiliatePolicy{})
	if err != nil || awarded || balance != 110 {
		t.Fatalf("migration duplicated check-in: awarded=%v balance=%d err=%v", awarded, balance, err)
	}
	var count, owners int
	if err = a.db.QueryRow(`SELECT count(*) FROM agent_points_ledger`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("legacy ledger changed: %d %v", count, err)
	}
	if err = a.db.QueryRow(`SELECT count(*) FROM agent_affiliate_owners`).Scan(&owners); err != nil || owners != 0 {
		t.Fatalf("migration claimed an owner: %d %v", owners, err)
	}
}

func TestPointsMergeSharesBalanceAndDailyLimit(t *testing.T) {
	a := newPointsRuntime(t)
	ctx := context.Background()
	first, source := seedPointsAccount(t, a, "bot-a", "same-user", 100)
	second, target := seedPointsAccount(t, a, "bot-b", "same-user", 200)
	if source == target {
		t.Fatal("unverified identities were automatically merged")
	}
	if _, _, err := a.recordDailyCheckIn(ctx, first, affiliatePolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.mergePointsAccounts(ctx, source, target, ""); err == nil {
		t.Fatal("merge without attestation succeeded")
	}
	if _, err := a.mergePointsAccounts(ctx, source, target, "both test identities verified"); err != nil {
		t.Fatal(err)
	}
	for _, run := range []runRecord{first, second} {
		awarded, balance, err := a.recordDailyCheckIn(ctx, run, affiliatePolicy{})
		if err != nil || awarded || balance != 310 {
			t.Fatalf("shared daily limit failed: awarded=%v balance=%d err=%v", awarded, balance, err)
		}
	}
	if id, err := a.mergePointsAccounts(ctx, source, target, "same retry"); err != nil || id != target {
		t.Fatalf("merge replay: %s %v", id, err)
	}
}

func TestPointsMergeRejectsDifferentVerifiedOwners(t *testing.T) {
	a := newPointsRuntime(t)
	_, first := seedPointsAccount(t, a, "a", "first", 100)
	_, second := seedPointsAccount(t, a, "b", "second", 200)
	_, err := a.db.Exec(`INSERT INTO agent_affiliate_owners VALUES ('AAAA','aiocqhttp','a','first','now','verified');
		INSERT INTO agent_affiliate_owners VALUES ('BBBB','aiocqhttp','b','second','now','verified')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.mergePointsAccounts(context.Background(), first, second, "wrong claim"); err == nil {
		t.Fatal("different verified site owners merged")
	}
}

func TestPointsOrderConcurrentDebitReplayAndRefund(t *testing.T) {
	a := newPointsRuntime(t)
	run, id := seedPointsAccount(t, a, "a", "user", 250)
	ctx := context.Background()
	input := pointsOrder{AccountID: id, Source: "lottery-test", ExternalRef: "exchange-1", Kind: "lottery", Points: 100, Note: "test reservation"}
	var wg sync.WaitGroup
	results := make(chan pointsOrder, 12)
	errors := make(chan error, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			order, err := a.reservePointsOrder(ctx, input)
			results <- order
			errors <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	var original pointsOrder
	for order := range results {
		if original.ID != "" && order.ID != original.ID {
			t.Fatal("idempotent retries created multiple orders")
		}
		original = order
	}
	if balance, err := a.pointsLedgerBalance(ctx, run); err != nil || balance != 150 {
		t.Fatalf("replayed debit: %d %v", balance, err)
	}
	input.Points = 101
	if _, err := a.reservePointsOrder(ctx, input); err == nil {
		t.Fatal("idempotency key accepted different amount")
	}
	input.ExternalRef, input.Points = "too-much", 151
	if _, err := a.reservePointsOrder(ctx, input); err == nil {
		t.Fatal("overdraft accepted")
	}
	for range 3 {
		if _, err := a.resolvePointsOrder(ctx, original.ID, "refunded", "confirmed no ticket issued"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.resolvePointsOrder(ctx, original.ID, "committed", "late success"); err == nil {
		t.Fatal("refunded transaction later committed")
	}
	if balance, err := a.pointsLedgerBalance(ctx, run); err != nil || balance != 250 {
		t.Fatalf("refund was not exact: %d %v", balance, err)
	}
}

func TestPointsParallelOrdersCannotOverspend(t *testing.T) {
	a := newPointsRuntime(t)
	run, id := seedPointsAccount(t, a, "a", "user", 100)
	results := make(chan error, 8)
	for n := range 8 {
		go func() {
			_, err := a.reservePointsOrder(context.Background(), pointsOrder{AccountID: id, Source: "test", ExternalRef: fmt.Sprint(n), Kind: "redemption", Points: 60, Note: "fixture"})
			results <- err
		}()
	}
	succeeded := 0
	for range 8 {
		if err := <-results; err == nil {
			succeeded++
		}
	}
	if balance, err := a.pointsLedgerBalance(context.Background(), run); err != nil || balance != 40 || succeeded != 1 {
		t.Fatalf("overspend: balance=%d succeeded=%d err=%v", balance, succeeded, err)
	}
}

func TestPointsOrderRollsBackOnLedgerFailure(t *testing.T) {
	a := newPointsRuntime(t)
	run, id := seedPointsAccount(t, a, "a", "user", 200)
	_, err := a.db.Exec(`CREATE TRIGGER fail_points BEFORE INSERT ON agent_points_ledger BEGIN SELECT RAISE(ABORT, 'fault injection'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.reservePointsOrder(context.Background(), pointsOrder{AccountID: id, Source: "test", ExternalRef: "fault", Kind: "lottery", Points: 100, Note: "fixture"})
	if err == nil {
		t.Fatal("ledger failure ignored")
	}
	var count int
	if err = a.db.QueryRow(`SELECT count(*) FROM agent_points_orders`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("orphan order persisted: %d %v", count, err)
	}
	if balance, err := a.pointsLedgerBalance(context.Background(), run); err != nil || balance != 200 {
		t.Fatalf("failed debit changed balance: %d %v", balance, err)
	}
}

func TestPointsManagementAuthPaginationAndMerge(t *testing.T) {
	a := newPointsRuntime(t)
	_, source := seedPointsAccount(t, a, "a", "user", 100)
	_, target := seedPointsAccount(t, a, "b", "user", 200)
	for _, path := range []string{"/api/v1/points/accounts", "/api/v1/points/accounts/" + source, "/api/v1/points/orders"} {
		for _, token := range []string{"", managementRuntimeToken} {
			r := httptest.NewRequest(http.MethodGet, path, nil)
			r.Header.Set(adminTokenHeader, token)
			w := httptest.NewRecorder()
			if !a.handleNativeManagement(w, r, path) || w.Code != http.StatusUnauthorized {
				t.Fatalf("private points read permitted: %s %d", path, w.Code)
			}
		}
	}
	for _, query := range []string{"?limit=0", "?limit=201", "?offset=-1"} {
		if err := a.handlePointsManagement(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/points/accounts"+query, nil), "/api/v1/points/accounts"); err == nil {
			t.Fatalf("bad page accepted: %s", query)
		}
	}
	input, _ := json.Marshal(map[string]any{"targetAccountId": target, "identityConfirmed": true, "evidence": "test-only identity proof"})
	path := "/api/v1/points/accounts/" + source + "/merge"
	r := httptest.NewRequest("POST", path, bytes.NewReader(input))
	r.Header.Set(adminTokenHeader, managementAdminToken)
	w := httptest.NewRecorder()
	if !a.handleNativeManagement(w, r, path) || w.Code != 200 {
		t.Fatalf("admin merge: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	if err := a.readPointsAccount(w, httptest.NewRequest("GET", "/", nil), source, 1, 0); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Data struct {
			ID      string `json:"id"`
			Balance int    `json:"balance"`
			HasMore bool   `json:"hasMore"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Data.ID != target || response.Data.Balance != 300 || !response.Data.HasMore {
		t.Fatalf("merged detail/pagination: %s %v", w.Body.String(), err)
	}
}
