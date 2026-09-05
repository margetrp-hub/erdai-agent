package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tencent-connect/botgo/dto"
	"github.com/tencent-connect/botgo/openapi/options"
)

func TestRepairCommonQuestionUsesHealthyChatRoute(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.configStore.db.Exec(`UPDATE model_endpoints SET enabled = 0`); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, runtime.configStore.db, "healthy-chat", "healthy-model", []string{"chat"}, "llm", "openai")
	for _, message := range []string{"正常人的智力是多少？", "帮我查一下今天的新闻"} {
		prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{Transport: "qq_official", Message: message})
		if err != nil || prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != "healthy-chat" {
			t.Fatalf("route for %q = %+v, %v", message, prepared.RouteDecision, err)
		}
	}
	_, err := runtime.providerRouteTargets(nativeRouteDecision{Lane: "chat"}, providerPolicyConfig{}, []string{"broken-legacy"}, "https://broken.test", "key")
	if err == nil {
		t.Fatal("routed failure fell through to an untracked legacy endpoint")
	}
}

func TestRepair521FailureIsNotAnEmptyCatchphrase(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	reply, code := runtime.naturalFailureReplyForRun(context.Background(), runRecord{PersonaID: "doubao"},
		"正常人的智力是多少？", &providerHTTPError{StatusCode: 521})
	if code != "provider_unavailable" || !strings.Contains(reply, "线路") || strings.Contains(reply, "HTTP") || strings.Contains(reply, "没接住") {
		t.Fatalf("failure = %q / %q", reply, code)
	}
}

func TestRepairPurpleNegationsAndLibraryPriority(t *testing.T) {
	for _, prompt := range []string{"不要紫色衣服", "别穿紫色裙子", "不要再穿紫色上衣", "不要穿紫色衣服", "一直都是紫色衣服", "no purple dress", "don't wear purple", "穿紫色衣服，但不要紫色"} {
		if videoPurpleRequested(prompt) || strings.Contains(videoOutfit(prompt, 0), "紫色") {
			t.Errorf("negative request selected purple: %q", prompt)
		}
	}
	for _, prompt := range []string{"换成紫色", "穿紫色衣服", "purple dress"} {
		if !videoPurpleRequested(prompt) {
			t.Errorf("positive purple request rejected: %q", prompt)
		}
	}
	now := time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"doubao", "xiaoman"} {
		persona := &nativeActivePersona{ID: id, VisualDescription: "共享外观库主脸", OutfitLength: "short"}
		prompt := personaVideoPromptAt("穿紫色长裙，在窗边拍个视频", persona, now, defaultImageVisualDirectorPolicy(), 0)
		if !strings.Contains(prompt, "允许使用紫色") || strings.Contains(prompt, "禁止复用紫色") || strings.Contains(prompt, "非窗边") || strings.Contains(prompt, shortOutfitInstruction()) {
			t.Fatalf("user instruction contradicted for %s: %s", id, prompt)
		}
		persona.VisualDescription = ""
		if !personaPrefersShortOutfit("不要紫色衣服", persona) {
			t.Fatal("color-only feedback erased the library length preference")
		}
		negativeVideo := personaVideoPromptAt("do not use purple clothes", persona, now, defaultImageVisualDirectorPolicy(), 0)
		if strings.Contains(negativeVideo, "允许使用紫色") {
			t.Fatal("video normalization erased an English negation")
		}
		imagePrompt := personaImagePromptAt("来一张你的自拍", persona, now, defaultImageVisualDirectorPolicy(), 0)
		if !strings.Contains(imagePrompt, "参考图") || !strings.Contains(imagePrompt, shortOutfitInstruction()) {
			t.Fatalf("reference-only library lost identity/length constraints: %s", imagePrompt)
		}
		persona.OutfitLength = "auto"
		persona.VisualDescription = "旧描述中写了膝盖以上"
		if personaPrefersShortOutfit("来个视频", persona) {
			t.Fatal("structured library setting lost to stale description")
		}
	}
}

func repairTestDelivery(t *testing.T, runtime *AgentRuntime, reply agentReply) leasedTransportDelivery {
	t.Helper()
	run := insertHonestyTestRun(t, runtime, "repair-run", "repair-group", "sender", "group", "running", time.Now().Add(-time.Minute))
	if err := runtime.enqueueDelivery(run, reply, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.db.Exec(`UPDATE agent_deliveries SET next_attempt_at = NULL`); err != nil {
		t.Fatal(err)
	}
	values, err := runtime.leaseTransportDeliveries(context.Background(), "consumer", 1, 30)
	if err != nil || len(values) != 1 {
		t.Fatalf("lease = %+v, %v", values, err)
	}
	return values[0]
}

func TestRepairStaleDeliveryReceipts(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	delivery := repairTestDelivery(t, runtime, agentReply{Text: "done"})
	oldReceipt := deliveryLeaseReceipt{LeaseOwner: delivery.LeaseOwner, Attempts: delivery.Attempts}
	if _, err := runtime.db.Exec(`UPDATE agent_deliveries SET lease_expires_at = ? WHERE id = ?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), delivery.ID); err != nil {
		t.Fatal(err)
	}
	if err := runtime.ackTransportDelivery(ctx, delivery.ID, oldReceipt); err == nil {
		t.Fatal("expired lease ACK was accepted")
	}
	values, err := runtime.leaseTransportDeliveries(ctx, "consumer", 1, 30)
	if err != nil || len(values) != 1 || values[0].Attempts != 2 {
		t.Fatalf("re-lease = %+v, %v", values, err)
	}
	if err := runtime.ackTransportDelivery(ctx, delivery.ID, oldReceipt); err == nil {
		t.Fatal("stale ACK was accepted")
	}
	if _, err := runtime.failTransportDelivery(ctx, delivery.ID, false, "late failure", oldReceipt); err == nil {
		t.Fatal("stale failure was accepted")
	}
	if err := runtime.ackTransportDelivery(ctx, delivery.ID); err == nil {
		t.Fatal("receipt-less ACK was accepted after re-lease")
	}
	current := deliveryLeaseReceipt{LeaseOwner: values[0].LeaseOwner, Attempts: values[0].Attempts}
	response := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/"+delivery.ID+"/ack", current, "")
	if response.Code != http.StatusOK {
		t.Fatalf("current HTTP ACK = %d: %s", response.Code, response.Body.String())
	}
	if _, err := runtime.failTransportDelivery(ctx, delivery.ID, true, "late failure", current); err == nil {
		t.Fatal("delivered status regressed")
	}
	if err := runtime.ackTransportDelivery(ctx, delivery.ID, current); err != nil {
		t.Fatalf("duplicate ACK was not idempotent: %v", err)
	}
	var state, status string
	if err := runtime.db.QueryRow(`SELECT r.state, d.status FROM agent_runs r JOIN agent_deliveries d ON d.run_id = r.id WHERE d.id = ?`, delivery.ID).Scan(&state, &status); err != nil || state != "delivered" || status != "delivered" {
		t.Fatalf("terminal states = %s/%s, %v", state, status, err)
	}
}

func TestRepairCancelledDeliveryCannotBeAcknowledged(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	delivery := repairTestDelivery(t, runtime, agentReply{Text: "done"})
	runtime.cancelRun(httptest.NewRecorder(), delivery.RunID)
	receipt := deliveryLeaseReceipt{LeaseOwner: delivery.LeaseOwner, Attempts: delivery.Attempts}
	if err := runtime.ackTransportDelivery(context.Background(), delivery.ID, receipt); err == nil {
		t.Fatal("cancelled delivery was acknowledged")
	}
	response := runtimeRequest(t, runtime, "/api/v1/transport/deliveries/"+delivery.ID+"/fail", map[string]any{
		"retryable": true, "reason": "late", "leaseOwner": receipt.LeaseOwner, "attempts": receipt.Attempts,
	}, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("cancelled HTTP fail = %d: %s", response.Code, response.Body.String())
	}
}

type repairQQAPI struct {
	fakeQQOfficialAPI
	failUpload bool
}

func (f *repairQQAPI) PostGroupMessage(ctx context.Context, target string, message dto.APIMessage, opts ...options.Option) (*dto.Message, error) {
	if _, ok := message.(qqRichMediaUpload); ok && f.failUpload {
		f.failUpload = false
		return nil, errors.New("temporary upload failure")
	}
	return f.fakeQQOfficialAPI.PostGroupMessage(ctx, target, message, opts...)
}

func TestRepairQQPartialSendResumesAfterConnectorRestart(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if err := os.MkdirAll(mediaMountRoot, 0700); err != nil {
		t.Fatal(err)
	}
	file, err := os.CreateTemp(mediaMountRoot, "repair-qq-*.png")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(file.Name()) })
	if _, err := file.Write([]byte("image")); err != nil {
		t.Fatal(err)
	}
	_ = file.Close()
	delivery := repairTestDelivery(t, runtime, agentReply{Text: "done", Attachments: []agentAttachment{{Kind: "image", LocalPath: file.Name()}}})
	ctx := context.Background()
	route := platformReplyRoute{ConnectorID: "qq_official", Transport: qqOfficialTransport, Kind: "group", TargetID: "group", MessageID: "message"}
	delivery.ReplyHandle, err = runtime.rememberPlatformRoute(ctx, "repair-event", route)
	if err != nil {
		t.Fatal(err)
	}
	api := &repairQQAPI{failUpload: true}
	connector := &qqOfficialConnector{runtime: runtime, api: api}
	if err := connector.Deliver(ctx, route, delivery); err == nil {
		t.Fatal("expected attachment failure")
	}
	connector = &qqOfficialConnector{runtime: runtime, api: api}
	if err := connector.Deliver(ctx, route, delivery); err != nil {
		t.Fatal(err)
	}
	if err := connector.Deliver(ctx, route, delivery); err != nil {
		t.Fatal(err)
	}
	if len(api.groupMessages) != 3 {
		t.Fatalf("successful calls = %d, want text + upload + attachment, without duplicates", len(api.groupMessages))
	}
}

func TestRepairInvitePointsAreDurableIdempotentAndIsolated(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	ctx := context.Background()
	run := runRecord{Transport: "qq_official", TransportInstance: "qq", SenderRef: "member"}
	policy := affiliatePolicy{PointsPerPaidInvitee: 100, SummaryURL: "https://affiliate.test/summary"}
	if _, err := runtime.db.Exec(`INSERT INTO agent_affiliate_bindings VALUES ('qq_official', 'qq', 'member', 'ABCD', 'now', 'now');
		INSERT INTO agent_affiliate_owners VALUES ('ABCD', 'qq_official', 'qq', 'member', 'now', 'test ownership evidence')`); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 12; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := runtime.creditInvitePoints(ctx, run, affiliateSummary{Code: "ABCD", PaidInviteeCount: 2}, policy); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	for _, count := range []int64{1, 4, 3, 4} {
		if err := runtime.creditInvitePoints(ctx, run, affiliateSummary{Code: "ABCD", PaidInviteeCount: count}, policy); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := runtime.recordDailyCheckIn(ctx, run, policy); err != nil {
		t.Fatal(err)
	}
	runtime.opsToken = "test-token"
	runtime.client = &http.Client{Transport: affiliateRoundTripper(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}
	account, err := runtime.pointsAccount(ctx, run, policy)
	if err != nil || account.SyncErr == nil || account.TotalPoints != 410 || account.InvitePoints != 400 || account.LocalPoints != 10 {
		t.Fatalf("offline account = %+v, %v", account, err)
	}
	other := run
	other.TransportInstance = "another-qq"
	if points, err := runtime.pointsLedgerBalance(ctx, other); err != nil || points != 0 {
		t.Fatalf("points crossed account boundary: %d, %v", points, err)
	}
	var count int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_points_ledger`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("ledger rows = %d, %v", count, err)
	}
}

func TestRepairHealthSamplesRetentionIsBounded(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	for _, item := range []struct {
		age   time.Duration
		count int
	}{{15 * 24 * time.Hour, 1005}, {time.Hour, 2}} {
		_, err := runtime.configStore.db.Exec(`WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x < ?)
			INSERT INTO model_health_samples(endpoint_id, healthy, latency_ms, checked_at) SELECT 'retention', 1, 1, ? FROM n`, item.count, now.Add(-item.age).Format(time.RFC3339Nano))
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []int{7, 2} {
		if err := runtime.configStore.pruneProviderHealthSamples(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := runtime.configStore.db.QueryRow(`SELECT count(*) FROM model_health_samples WHERE endpoint_id = 'retention'`).Scan(&count); err != nil || count != want {
			t.Fatalf("remaining = %d, want %d: %v", count, want, err)
		}
	}
}

func TestRepairLibraryOutfitSettingFollowsBinding(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	store := runtime.configStore
	for _, length := range []string{"auto", "short", "long"} {
		name := fmt.Sprintf("library-%s", length)
		library, err := store.createAppearanceLibrary("default", appearanceLibraryPayload{Name: &name, OutfitLength: &length})
		if err != nil || library.OutfitLength != length {
			t.Fatalf("library = %+v, %v", library, err)
		}
		if _, err := store.db.Exec(`UPDATE persona_appearance_libraries SET library_id = ? WHERE persona_id = 'doubao'`, library.ID); err != nil {
			t.Fatal(err)
		}
		persona := runtime.personaForRun(runRecord{PersonaID: "doubao"}, "来张自拍")
		if persona == nil || persona.OutfitLength != length {
			t.Fatalf("bound persona = %+v", persona)
		}
	}
}

func TestRepairSchema83MigrationPreservesDataAndPreferences(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`ALTER TABLE appearance_libraries DROP COLUMN outfit_length;
		INSERT INTO agent_points_ledger VALUES ('existing-check-in', 'qq_official', 'qq', 'member', 'check_in', 10, 'check-in:2026-09-05', 'existing', '2026-09-05T00:00:00Z');
		PRAGMA user_version = 82;`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.appearanceLibraryOutfitLength("xiaoman"); got != "short" {
		t.Fatalf("migrated outfit length = %q", got)
	}
	var points int
	if err := store.db.QueryRow(`SELECT points FROM agent_points_ledger WHERE id = 'existing-check-in'`).Scan(&points); err != nil || points != 10 {
		t.Fatalf("migration lost points: %d, %v", points, err)
	}
	if _, err := store.db.Exec(`UPDATE appearance_libraries SET outfit_length = 'long' WHERE id = 'persona-appearance-xiaoman'`); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.appearanceLibraryOutfitLength("xiaoman"); got != "long" {
		t.Fatalf("reopening overwrote user preference: %q", got)
	}
}
