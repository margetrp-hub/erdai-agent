package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestPointsSiteBridgeScopesAndDailyLedger(t *testing.T) {
	a := newPointsRuntime(t)
	a.runtimeToken = managementRuntimeToken
	a.pointsReadToken = strings.Repeat("read-only", 5)
	a.pointsSiteClients = []pointsSiteClient{
		{strings.Repeat("site-a", 8), "newapi", "https://new.test"},
		{strings.Repeat("site-b", 8), "sub2api", "https://api.test"},
	}
	if _, err := a.db.Exec(`UPDATE integration_settings SET config_json = '{"enabled":true,"checkInPoints":10}' WHERE id = 'affiliate_policy'`); err != nil {
		t.Fatal(err)
	}
	request := func(token, method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/points-bridge/v1/site/"+path, strings.NewReader(body))
		r.Header.Set("X-ErDai-Points-Token", token)
		w := httptest.NewRecorder()
		a.handlePointsReadBridge(w, r)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) pointsSiteAccount {
		t.Helper()
		var payload struct {
			Data pointsSiteAccount `json:"data"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &payload) != nil {
			t.Fatalf("response: %d %s", w.Code, w.Body.String())
		}
		return payload.Data
	}
	token := a.pointsSiteClients[0].Token
	for _, bad := range []string{"", a.pointsReadToken, a.adminToken, a.runtimeToken} {
		if w := request(bad, "POST", "account", `{"senderRef":"123"}`); w.Code != 401 {
			t.Fatalf("credential accepted: %d", w.Code)
		}
	}
	if w := request(token, "GET", "account?senderRef=123", ""); w.Code != 409 {
		t.Fatalf("GET created account: %d", w.Code)
	}
	for _, body := range []string{`{"senderRef":"123","points":1280}`, `{"senderRef":"123","transport":"sub2api"}`, `{"senderRef":"0123"}`, `{"senderRef":"0"}`, `{"senderRef":"123"} {}`} {
		if w := request(token, "POST", "account", body); w.Code != 400 {
			t.Fatalf("invalid identity accepted: %d", w.Code)
		}
	}
	first := decode(request(token, "POST", "account", `{"senderRef":"123"}`))
	replay := decode(request(token, "POST", "account", `{"senderRef":"123"}`))
	other := decode(request(a.pointsSiteClients[1].Token, "POST", "account", `{"senderRef":"123"}`))
	if first.Balance != 0 || len(first.Entries) != 0 || replay.AccountID != first.AccountID || other.AccountID == first.AccountID {
		t.Fatal("provisioning imported points or crossed site boundaries")
	}
	var wg sync.WaitGroup
	results := make(chan int, 8)
	for range 8 {
		wg.Go(func() { results <- request(token, "POST", "checkin", `{"senderRef":"123"}`).Code })
	}
	wg.Wait()
	close(results)
	for status := range results {
		if status != 200 {
			t.Fatalf("checkin failed: %d", status)
		}
	}
	account := decode(request(token, "GET", "account?senderRef=123&limit=1", ""))
	if account.Balance != 10 || !account.CheckedIn || !account.CheckInEnabled || len(account.Entries) != 1 || account.Entries[0].Type != "check_in" || account.HasMore {
		t.Fatalf("daily ledger: %+v", account)
	}
	qq, qqAccount := seedPointsAccount(t, a, "private-bot", "private-qq-number", 100)
	if _, err := a.mergePointsAccounts(context.Background(), first.AccountID, qqAccount, "test fixture verified identity"); err != nil {
		t.Fatal(err)
	}
	if awarded, balance, err := a.recordDailyCheckIn(context.Background(), qq, affiliatePolicy{}); err != nil || awarded || balance != 110 {
		t.Fatalf("shared check-in: %v %d %v", awarded, balance, err)
	}
	w := request(token, "GET", "account?senderRef=123&limit=1", "")
	account = decode(w)
	if !account.Linked || !account.HasMore || account.Balance != 110 || strings.Contains(w.Body.String(), "private-") || strings.Contains(w.Body.String(), "test fixture") {
		t.Fatalf("shared ledger privacy/pagination: %s", w.Body.String())
	}
	if next := decode(request(token, "GET", "account?senderRef=123&limit=1&offset=1", "")); next.HasMore || len(next.Entries) != 1 || next.Entries[0].ID == account.Entries[0].ID {
		t.Fatal("ledger page lost entries")
	}
	if _, err := a.db.Exec(`UPDATE integration_settings SET config_json = '{"enabled":false}' WHERE id = 'affiliate_policy'`); err != nil {
		t.Fatal(err)
	}
	if w := request(token, "POST", "checkin", `{"senderRef":"123"}`); w.Code != 409 {
		t.Fatalf("disabled checkin: %d", w.Code)
	}
	if decode(request(token, "GET", "account?senderRef=123", "")).CheckInEnabled {
		t.Fatal("disabled checkin reported enabled")
	}
	for _, path := range []string{"orders", "merge", "draw", "exchange"} {
		if w := request(token, "POST", path, `{}`); w.Code != 404 {
			t.Fatalf("site credential exceeded scope: %s", path)
		}
	}
	r := httptest.NewRequest("POST", "/api/v1/points/orders", strings.NewReader(`{}`))
	r.Header.Set(adminTokenHeader, token)
	w = httptest.NewRecorder()
	if !a.handleNativeManagement(w, r, r.URL.Path) || w.Code != 401 {
		t.Fatal("site credential permits management")
	}
}

func TestPointsSiteClientConfiguration(t *testing.T) {
	token := strings.Repeat("scoped", 8)
	valid := pointsSiteClient{token, "newapi", "https://new.test"}
	for _, clients := range [][]pointsSiteClient{{valid}, {valid, valid}, {{"short", "newapi", "https://new.test"}}, {{token, "aiocqhttp", "https://new.test"}}, {{token, "newapi", "http://new.test"}}, {{token, "newapi", "https://new.test/path"}}} {
		raw, _ := json.Marshal(clients)
		_, err := parsePointsSiteClients(RuntimeConfig{PointsSiteClients: string(raw)})
		wantValid := len(clients) == 1 && clients[0] == valid
		if (err == nil) != wantValid {
			t.Fatalf("config validation mismatch: valid=%v error=%v", wantValid, err)
		}
	}
	raw, _ := json.Marshal([]pointsSiteClient{valid})
	if _, err := parsePointsSiteClients(RuntimeConfig{PointsSiteClients: string(raw), AdminToken: token}); err == nil {
		t.Fatal("admin token reused")
	}
}
