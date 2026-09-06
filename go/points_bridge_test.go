package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestPointsIdentityLinkAndReadOnlyBridge(t *testing.T) {
	a := newPointsRuntime(t)
	a.runtimeToken = managementRuntimeToken
	a.pointsReadToken = strings.Repeat("points-reader-", 4)
	ctx := context.Background()
	_, account := seedPointsAccount(t, a, "bot-a", "qq-user", 170)
	_, other := seedPointsAccount(t, a, "bot-b", "other-user", 20)
	identity := pointsIdentity{"sub2api", "https://site.test", "123"}
	path := "/points-bridge/v1/account?" + url.Values{"transport": {identity.Transport}, "transportInstance": {identity.TransportInstance}, "senderRef": {identity.SenderRef}}.Encode()
	read := func(token, method, target string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, nil)
		r.Header.Set("X-ErDai-Points-Token", token)
		w := httptest.NewRecorder()
		if !a.handlePointsReadBridge(w, r) {
			t.Fatal("bridge not handled")
		}
		return w
	}
	for _, token := range []string{"", managementRuntimeToken, managementAdminToken} {
		if w := read(token, "GET", path); w.Code != 401 {
			t.Fatalf("read credential boundary: %d", w.Code)
		}
	}
	if w := read(a.pointsReadToken, "GET", path); w.Code != 409 {
		t.Fatalf("unverified identity: %d", w.Code)
	}
	if _, err := a.linkPointsIdentity(ctx, account, identity, ""); err == nil {
		t.Fatal("unattested link accepted")
	}
	for range 2 {
		if id, err := a.linkPointsIdentity(ctx, account, identity, "test-only proof"); err != nil || id != account {
			t.Fatalf("link: %s %v", id, err)
		}
	}
	if _, err := a.linkPointsIdentity(ctx, other, identity, "conflicting proof"); err == nil {
		t.Fatal("linked identity reassigned")
	}
	if id, err := a.pointsIdentityAccount(ctx, identity.run()); err != nil || id != account {
		t.Fatalf("identity mapping lost: %s %v", id, err)
	}
	var count int
	if err := a.db.QueryRow(`SELECT count(*) FROM agent_points_accounts`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("orphan account: %d %v", count, err)
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM agent_points_identity_links`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("link audit: %d %v", count, err)
	}
	w := read(a.pointsReadToken, "GET", path)
	var payload struct {
		Data struct {
			AccountID string `json:"accountId"`
			Balance   int    `json:"balance"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil || w.Code != 200 || payload.Data.AccountID != account || payload.Data.Balance != 170 || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("bridge read: %d %s %v", w.Code, w.Body.String(), err)
	}
	if w = read(a.pointsReadToken, "POST", path); w.Code != 404 {
		t.Fatalf("bridge permits writes: %d", w.Code)
	}
	r := httptest.NewRequest("POST", "/api/v1/points/orders", strings.NewReader(`{}`))
	r.Header.Set(adminTokenHeader, a.pointsReadToken)
	w = httptest.NewRecorder()
	if !a.handleNativeManagement(w, r, r.URL.Path) || w.Code != 401 {
		t.Fatalf("read token permits management: %d", w.Code)
	}
	a.pointsReadToken = managementAdminToken
	if w = read(a.pointsReadToken, "GET", path); w.Code != 401 {
		t.Fatal("reused admin credential accepted")
	}
}

func TestPointsOrderPersistsAcrossRestartAndFailedRefund(t *testing.T) {
	a := newPointsRuntime(t)
	ctx := context.Background()
	run, account := seedPointsAccount(t, a, "bot-a", "qq-user", 170)
	input := pointsOrder{AccountID: account, Source: "test", ExternalRef: "persistent-order", Kind: "lottery", Points: 100, Note: "test reservation"}
	order, err := a.reservePointsOrder(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	var seq int
	var name, path string
	if err = a.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	if err = a.configStore.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	a.db, a.configStore = store.db, store
	replay, err := a.reservePointsOrder(ctx, input)
	if err != nil || replay.ID != order.ID || replay.Status != "reserved" {
		t.Fatalf("restart replay: %+v %v", replay, err)
	}
	_, err = a.db.Exec(`CREATE TRIGGER fail_refund BEFORE INSERT ON agent_points_ledger WHEN NEW.reference_key LIKE '%:refund' BEGIN SELECT RAISE(ABORT, 'refund fault'); END`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.resolvePointsOrder(ctx, order.ID, "refunded", "not issued"); err == nil {
		t.Fatal("refund fault ignored")
	}
	if balance, err := a.pointsLedgerBalance(ctx, run); err != nil || balance != 70 {
		t.Fatalf("failed refund changed balance: %d %v", balance, err)
	}
	var status string
	if err = a.db.QueryRow(`SELECT status FROM agent_points_orders WHERE id = ?`, order.ID).Scan(&status); err != nil || status != "reserved" {
		t.Fatalf("failed refund finalized order: %s %v", status, err)
	}
	if _, err = a.db.Exec(`DROP TRIGGER fail_refund`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.resolvePointsOrder(ctx, order.ID, "refunded", "not issued"); err != nil {
		t.Fatal(err)
	}
	if balance, err := a.pointsLedgerBalance(ctx, run); err != nil || balance != 170 {
		t.Fatalf("refund retry: %d %v", balance, err)
	}
}

func TestPointsMergedIdentityCreditsInvitesOnce(t *testing.T) {
	a := newPointsRuntime(t)
	ctx := context.Background()
	first, source := seedPointsAccount(t, a, "bot-a", "same-user", 0)
	second, target := seedPointsAccount(t, a, "bot-b", "same-user", 0)
	_, err := a.db.Exec(`INSERT INTO agent_affiliate_bindings VALUES ('aiocqhttp','bot-a','same-user','ABCD','now','now');
		INSERT INTO agent_affiliate_owners VALUES ('ABCD','aiocqhttp','bot-a','same-user','now','verified')`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.mergePointsAccounts(ctx, source, target, "test proof"); err != nil {
		t.Fatal(err)
	}
	for _, run := range []runRecord{first, second, first, second} {
		if code, err := a.boundAffiliateCode(ctx, run); err != nil || code != "ABCD" {
			t.Fatalf("alias binding: %s %v", code, err)
		}
		if err = a.creditInvitePoints(ctx, run, affiliateSummary{Code: "ABCD", PaidInviteeCount: 2}, affiliatePolicy{}); err != nil {
			t.Fatal(err)
		}
	}
	if balance, err := a.pointsLedgerBalance(ctx, second); err != nil || balance != 200 {
		t.Fatalf("duplicate invitation award: %d %v", balance, err)
	}
}
