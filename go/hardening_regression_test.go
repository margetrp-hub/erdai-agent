package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func TestHardeningSearchRecoveryAfterRestart(t *testing.T) {
	cfg := RuntimeConfig{
		DatabasePath:       filepath.Join(t.TempDir(), "runtime.sqlite3"),
		ConfigDatabasePath: newTestCoreConfigPath(t),
		AdminToken:         "admin-test-token", RuntimeToken: testRuntimeToken,
		EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
	}
	a, err := NewAgentRuntime(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.cancel()
	a.workers.Wait()
	run := seedSearchRun(t, a, "hardening-interrupted-search")
	for _, query := range []string{"running", "success", "failure"} {
		if _, _, handled, err := a.beginSearchRun(&run, query); err != nil || handled {
			t.Fatalf("reserve %s: handled=%v err=%v", query, handled, err)
		}
	}
	if err := a.finishSearchRunSuccess(run.ID, "success", "cached", []searchSource{}); err != nil {
		t.Fatal(err)
	}
	if err := a.finishSearchRunFailure(run.ID, "failure", errors.New("outage")); err != nil {
		t.Fatal(err)
	}
	// A migrated legacy reservation must not return on a second restart.
	if _, err := a.db.Exec(`INSERT INTO agent_search_runs
		SELECT * FROM agent_search_queries WHERE query_hash = ?`, searchQueryHash("running")); err != nil {
		t.Fatal(err)
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	for restart := 0; restart < 2; restart++ {
		b, err := NewAgentRuntime(cfg)
		if err != nil {
			t.Fatal(err)
		}
		b.cancel()
		b.workers.Wait()
		if _, _, handled, err := b.beginSearchRun(&run, "running"); handled || err != nil {
			t.Fatalf("restart %d blocked interrupted query: handled=%v err=%v", restart, handled, err)
		}
		if text, _, handled, err := b.beginSearchRun(&run, "success"); !handled || err != nil || text != "cached" {
			t.Fatalf("lost successful cache: handled=%v text=%s err=%v", handled, text, err)
		}
		if _, _, handled, err := b.beginSearchRun(&run, "failure"); !handled || err == nil || err.Error() != "outage" {
			t.Fatalf("restart incorrectly cleared completed failure: handled=%v err=%v", handled, err)
		}
		var legacyCount int
		if err := b.db.QueryRow(`SELECT count(*) FROM agent_search_runs`).Scan(&legacyCount); err != nil || legacyCount != 0 {
			t.Fatalf("legacy receipts were not drained: count=%d err=%v", legacyCount, err)
		}
		if err := b.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHardeningExplicitRetryPreservesSuccessAndClearsFailure(t *testing.T) {
	a := newDormantRuntime(t)
	defer a.Close()
	run := seedSearchRun(t, a, "hardening-search-retry")
	input, err := a.encrypt([]byte("search test query"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE agent_runs SET state = 'failed', input_cipher = ? WHERE id = ?`, input, run.ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"success", "failed", "interrupted"} {
		if _, _, handled, err := a.beginSearchRun(&run, query); err != nil || handled {
			t.Fatalf("reserve: %v %v", handled, err)
		}
	}
	if err := a.finishSearchRunSuccess(run.ID, "success", "cached", []searchSource{}); err != nil {
		t.Fatal(err)
	}
	if err := a.finishSearchRunFailure(run.ID, "failed", errors.New("temporary outage")); err != nil {
		t.Fatal(err)
	}
	if err := a.retryTask(context.Background(), run.ID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{"failed", "interrupted"} {
		if _, _, handled, err := a.beginSearchRun(&run, query); handled || err != nil {
			t.Fatalf("explicit retry blocked %s: handled=%v err=%v", query, handled, err)
		}
	}
	if text, _, handled, err := a.beginSearchRun(&run, "success"); !handled || err != nil || text != "cached" {
		t.Fatalf("successful cache changed: %v %s %v", handled, text, err)
	}
	if err := a.finishSearchRunSuccess(run.ID, "never-reserved", "text", []searchSource{}); err == nil {
		t.Fatal("missing reservation reported successful persistence")
	}
	if err := a.finishSearchRunFailure(run.ID, "success", errors.New("late failure")); err == nil {
		t.Fatal("completed receipt accepted late failure")
	}
}

func TestHardeningDeliveryFIFOWithVariableTimestampPrecision(t *testing.T) {
	a := newDormantRuntime(t)
	defer a.Close()
	base := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	// FIFO must survive variable timestamp precision and a wall-clock rollback.
	for i, stamp := range []time.Time{base, base.Add(100 * time.Millisecond), base.Add(-time.Second)} {
		run := insertHonestyTestRun(t, a, fmt.Sprintf("hardening-order-%d", i), "one", "sender", "group", "succeeded", stamp)
		if err := a.enqueueDelivery(run, agentReply{Text: fmt.Sprintf("message-%d", i)}, "terminal", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec(`UPDATE agent_deliveries SET created_at = ?, next_attempt_at = NULL WHERE run_id = ?`,
			stamp.Format(time.RFC3339Nano), run.ID); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3; i++ {
		deliveries, err := a.leaseTransportDeliveries(context.Background(), "hardening", 3, 30)
		if err != nil || len(deliveries) != 1 {
			t.Fatalf("lease: %+v %v", deliveries, err)
		}
		d := deliveries[0]
		if d.RunID != fmt.Sprintf("hardening-order-%d", i) {
			t.Fatalf("FIFO reordered: run=%s", d.RunID)
		}
		if err := a.ackTransportDelivery(context.Background(), d.ID, deliveryLeaseReceipt{d.LeaseOwner, d.Attempts}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHardeningOwnershipPaginationAndFilters(t *testing.T) {
	a := newDormantRuntime(t)
	defer a.Close()
	for i := 0; i < 201; i++ {
		if _, err := a.db.Exec(`INSERT INTO agent_affiliate_bindings VALUES ('qq_official','qq',?,'ABCD',?,'now')`,
			fmt.Sprintf("sender-%03d", i), fmt.Sprintf("%03d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.db.Exec(`INSERT INTO agent_affiliate_owners VALUES ('ABCD','qq_official','qq','sender-200','now','test')`); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		query string
		count int
		first string
		more  bool
	}{
		{"", 200, "sender-000", true},
		{"?offset=200&limit=200", 1, "sender-200", false},
		{"?offset=201", 0, "", false},
		{"?status=verified", 1, "sender-200", false},
		{"?status=pending", 0, "", false},
		{"?status=conflict&limit=1", 1, "sender-000", true},
		{"?affiliateCode=abcd&senderRef=sender-200&transport=qq_official&transportInstance=qq", 1, "sender-200", false},
		{"?senderRef=%27%20OR%201%3D1--", 0, "", false},
	} {
		w := httptest.NewRecorder()
		if err := a.handleAffiliateOwnership(w, httptest.NewRequest("GET", "/api/v1/affiliate/ownership"+test.query, nil)); err != nil {
			t.Fatal(err)
		}
		var response struct {
			Data []struct {
				SenderRef string `json:"senderRef"`
			} `json:"data"`
			Truncated  bool `json:"truncated"`
			NextOffset *int `json:"nextOffset"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != test.count || response.Truncated != test.more || (test.more != (response.NextOffset != nil)) {
			t.Fatalf("%s: %+v", test.query, response)
		}
		if test.count > 0 && response.Data[0].SenderRef != test.first {
			t.Fatalf("%s: first=%s", test.query, response.Data[0].SenderRef)
		}
	}
	for _, query := range []string{"?offset=-1", "?offset=x", "?limit=0", "?limit=201", "?status=anything"} {
		if err := a.handleAffiliateOwnership(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/v1/affiliate/ownership"+query, nil)); err == nil {
			t.Fatalf("invalid filter accepted: %s", query)
		}
	}
}

func TestHardeningPurpleNegation(t *testing.T) {
	for _, prompt := range []string{"不是紫色衣服，是红色衣服", "并非紫色上衣", "不要穿紫色的衣服，改成白色", "不要改成紫色", "do not use purple"} {
		if videoPurpleRequested(prompt) || !visualPurpleRejected(prompt) {
			t.Errorf("negative instruction became purple request: %q", prompt)
		}
	}
	for _, prompt := range []string{"不是红色衣服，换成紫色衣服", "不要红色改成紫色", "不要长裙换成紫色短裙"} {
		if !videoPurpleRequested(prompt) {
			t.Errorf("positive replacement rejected: %q", prompt)
		}
	}
}

func TestHardeningDeliveryReceiptReadFailureDoesNotSend(t *testing.T) {
	a := newDormantRuntime(t)
	defer a.Close()
	ctx := context.Background()
	if sent, err := a.platformDeliveryWasSent(ctx, "not-sent"); sent || err != nil {
		t.Fatalf("missing receipt: sent=%v err=%v", sent, err)
	}
	c := &optimizationBlockingConnector{started: make(chan string, 10), release: make(chan struct{})}
	run := insertHonestyTestRun(t, a, "hardening-read-failure", "one", "sender", "group", "running", time.Now().Add(-time.Minute))
	handle, err := a.rememberPlatformRoute(ctx, run.EventID, platformReplyRoute{ConnectorID: c.ID(), Transport: "qq_official", Kind: "group", TargetID: "one"})
	if err != nil {
		t.Fatal(err)
	}
	run.ReplyHandle = handle
	if err := a.enqueueDelivery(run, agentReply{Text: "must not send"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE agent_deliveries SET next_attempt_at = NULL; DROP TABLE platform_sent_deliveries`); err != nil {
		t.Fatal(err)
	}
	m := &platformConnectorManager{runtime: a, connectors: map[string]platformConnector{c.ID(): c}}
	m.deliverPending(ctx)
	if _, err := a.platformDeliveryWasSent(ctx, "not-sent"); err == nil {
		t.Fatal("database failure treated as missing receipt")
	}
	select {
	case <-c.started:
		t.Fatal("sent without knowing whether delivery was already sent")
	default:
	}
}
