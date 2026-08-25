package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A 403 from a credential pair must not walk fallbacks that share the same
// credentials: the request short-fails on the first rejection.
func TestChatCompletionSameCredentialForbiddenShortFails(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "account unavailable"})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	targets := []runtimeProviderTarget{
		{EndpointID: "primary", Model: "primary-model", APIBase: service.URL + "/shared", APIKey: "same-key"},
		{EndpointID: "fallback", Model: "fallback-model", APIBase: service.URL + "/shared", APIKey: "same-key"},
	}
	_, _, err := runtime.chatCompletionWithTargets(context.Background(), map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, nil)
	if err == nil || calls.Load() != 1 {
		t.Fatalf("same-credential forbidden: calls=%d err=%v", calls.Load(), err)
	}
	if classifyProviderFailure(err) != failureClassCredential {
		t.Fatalf("failure class = %q", classifyProviderFailure(err))
	}
}

// A second distinct credential pair also rejecting ends the request: at most
// two credential pairs are ever tried on authorization failures.
func TestChatCompletionSecondCredentialForbiddenStops(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeJSON(w, http.StatusForbidden, map[string]any{"error": "denied"})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	targets := []runtimeProviderTarget{
		{EndpointID: "a1", Model: "a-model", APIBase: service.URL + "/a", APIKey: "key-a"},
		{EndpointID: "a2", Model: "a-model-2", APIBase: service.URL + "/a", APIKey: "key-a"},
		{EndpointID: "b1", Model: "b-model", APIBase: service.URL + "/b", APIKey: "key-b"},
	}
	_, _, err := runtime.chatCompletionWithTargets(context.Background(), map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, nil)
	if err == nil || calls.Load() != 2 {
		t.Fatalf("second credential forbidden: calls=%d err=%v", calls.Load(), err)
	}
}

// The fallback chain respects the caller's deadline: attempts are clipped to
// the remaining budget and no new attempt starts once the budget is gone.
func TestChatCompletionRespectsDeadlineBudget(t *testing.T) {
	var calls atomic.Int32
	service := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		time.Sleep(3 * time.Second)
		writeJSON(w, http.StatusOK, map[string]any{"choices": []any{map[string]any{
			"message": map[string]string{"role": "assistant", "content": "太晚了。"},
		}}})
	}))
	defer service.Close()
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	runtime.client = service.Client()
	targets := []runtimeProviderTarget{
		{EndpointID: "slow-1", Model: "slow-1", APIBase: service.URL + "/one", TimeoutSeconds: 30},
		{EndpointID: "slow-2", Model: "slow-2", APIBase: service.URL + "/two", TimeoutSeconds: 30},
		{EndpointID: "slow-3", Model: "slow-3", APIBase: service.URL + "/three", TimeoutSeconds: 30},
	}
	deadlineContext, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := runtime.chatCompletionWithTargets(deadlineContext, map[string]any{
		"messages": []map[string]string{{"role": "user", "content": "test"}},
	}, targets, 0, nil)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("expected a deadline failure")
	}
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("fallback chain ran past the deadline budget: %v", elapsed)
	}
	if calls.Load() > 2 {
		t.Fatalf("started %d attempts with a 1.2s budget", calls.Load())
	}
}

func TestClassifyProviderFailureMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{&providerHTTPError{StatusCode: 401}, failureClassCredential},
		{&providerHTTPError{StatusCode: 403}, failureClassCredential},
		{&providerHTTPError{StatusCode: 429}, failureClassRateLimit},
		{&providerHTTPError{StatusCode: 400}, failureClassContent},
		{&providerHTTPError{StatusCode: 502}, failureClassUpstreamDown},
		{context.DeadlineExceeded, failureClassTimeout},
	}
	for _, testCase := range cases {
		if got := classifyProviderFailure(testCase.err); got != testCase.want {
			t.Fatalf("classify(%v) = %q, want %q", testCase.err, got, testCase.want)
		}
	}
	if classifyProviderFailure(nil) != "" {
		t.Fatal("nil error must classify to empty")
	}
}

func TestNaturalFailureReplyCredentialClassFirst(t *testing.T) {
	text, code := naturalFailureReply("随便聊聊", &providerHTTPError{StatusCode: 403})
	if code != "provider_credential_rejected" || strings.TrimSpace(text) == "" {
		t.Fatalf("credential failure reply = %q code=%q", text, code)
	}
	if _, code := naturalFailureReply("给我画一张图", &providerHTTPError{StatusCode: 403}); code != "provider_credential_rejected" {
		t.Fatalf("image-lane credential code = %q", code)
	}
}

func TestFailureNoticeAllowedTargeting(t *testing.T) {
	silent := []runRecord{
		{OwnershipReason: "trigger_keyword"},
		{OwnershipReason: "group_participation"},
		{OwnershipReason: "group_participation_local_fallback"},
		{OwnershipReason: "trigger_keyword_local_fallback"},
	}
	for _, run := range silent {
		if failureNoticeAllowed(run) {
			t.Fatalf("ownership %q must fail silently", run.OwnershipReason)
		}
	}
	spoken := []runRecord{
		{IsWake: true, OwnershipReason: "group_participation"},
		{IsMentionBot: true, OwnershipReason: "trigger_keyword"},
		{OwnershipReason: "direct_continuation"},
		{OwnershipReason: "direct_address"},
		{OwnershipReason: "attachment_continuation"},
	}
	for _, run := range spoken {
		if !failureNoticeAllowed(run) {
			t.Fatalf("run %+v must deliver a failure notice", run)
		}
	}
}

func insertHonestyTestRun(t *testing.T, runtime *AgentRuntime, id, conversation, sender, kind, state string, createdAt time.Time) runRecord {
	t.Helper()
	formatted := createdAt.UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, reply_handle, conversation_ref, conversation_kind, sender_ref, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, "event-"+id, "reply-"+id, conversation, kind, sender, state, formatted, formatted); err != nil {
		t.Fatal(err)
	}
	return runRecord{
		ID: id, EventID: "event-" + id, ReplyHandle: "reply-" + id,
		Transport: "qq_official", TransportInstance: "",
		ConversationRef: conversation, ConversationKind: kind, SenderRef: sender,
		AgentInstanceID: "legacy-default", MemoryNamespace: "legacy-default",
		State: state, CreatedAt: formatted,
	}
}

// A conversation runs serially: while one run is 'running', a sibling queued
// run in the same conversation is not claimable, but another conversation is.
func TestProcessNextSerializesConversation(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	insertHonestyTestRun(t, runtime, "busy-run", "group-serial", "sender-a", "group", "running", now.Add(-2*time.Second))
	insertHonestyTestRun(t, runtime, "blocked-run", "group-serial", "sender-b", "group", "queued", now.Add(-time.Second))
	insertHonestyTestRun(t, runtime, "free-run", "group-other", "sender-c", "group", "queued", now)

	claim := func() string {
		staleGuard := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
		var id string
		err := runtime.db.QueryRow(`SELECT id FROM agent_runs WHERE state = 'queued'
			AND NOT EXISTS (
				SELECT 1 FROM agent_runs busy
				WHERE busy.conversation_ref = agent_runs.conversation_ref
				  AND busy.transport = agent_runs.transport
				  AND busy.transport_instance = agent_runs.transport_instance
				  AND busy.state = 'running'
				  AND busy.id <> agent_runs.id
				  AND busy.updated_at >= ?
				  AND NOT EXISTS (
					SELECT 1 FROM agent_deliveries progress
					WHERE progress.run_id = busy.id AND progress.phase = 'progress'
					  AND progress.status <> 'cancelled'
				  )
			)
			ORDER BY created_at, rowid LIMIT 1`, staleGuard).Scan(&id)
		if err != nil {
			return ""
		}
		return id
	}
	if got := claim(); got != "free-run" {
		t.Fatalf("claim with busy conversation = %q, want free-run", got)
	}
	// A busy run that already announced progress is a long media task: it
	// releases the conversation lane instead of freezing the group's chat.
	if _, err := runtime.db.Exec(`INSERT INTO agent_deliveries
		(id, run_id, reply_handle, payload_json, phase, status, created_at, updated_at)
		VALUES ('busy-progress', 'busy-run', 'reply-busy-run', '{}', 'progress', 'delivered', ?, ?)`,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if got := claim(); got != "blocked-run" {
		t.Fatalf("claim with media-busy conversation = %q, want blocked-run", got)
	}
	if _, err := runtime.db.Exec(`DELETE FROM agent_deliveries WHERE id = 'busy-progress'`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.db.Exec(`UPDATE agent_runs SET state = 'responding' WHERE id = 'busy-run'`); err != nil {
		t.Fatal(err)
	}
	if got := claim(); got != "blocked-run" {
		t.Fatalf("claim after busy run finished = %q, want blocked-run", got)
	}
}

// A crashed run stuck in 'running' past the staleness guard stops blocking
// its conversation.
func TestProcessNextStaleRunningDoesNotBlock(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	stale := insertHonestyTestRun(t, runtime, "stale-run", "group-stale", "sender-a", "group", "running", now.Add(-time.Hour))
	if _, err := runtime.db.Exec(`UPDATE agent_runs SET updated_at = ? WHERE id = ?`,
		now.Add(-time.Hour).Format(time.RFC3339Nano), stale.ID); err != nil {
		t.Fatal(err)
	}
	insertHonestyTestRun(t, runtime, "waiting-run", "group-stale", "sender-b", "group", "queued", now)
	staleGuard := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339Nano)
	var id string
	if err := runtime.db.QueryRow(`SELECT id FROM agent_runs WHERE state = 'queued'
		AND NOT EXISTS (
			SELECT 1 FROM agent_runs busy
			WHERE busy.conversation_ref = agent_runs.conversation_ref
			  AND busy.transport = agent_runs.transport
			  AND busy.transport_instance = agent_runs.transport_instance
			  AND busy.state = 'running'
			  AND busy.id <> agent_runs.id
			  AND busy.updated_at >= ?
		)
		ORDER BY created_at, rowid LIMIT 1`, staleGuard).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != "waiting-run" {
		t.Fatalf("stale running run still blocks: claimed %q", id)
	}
}

// A terminal reply whose run is older than a newer already-delivered terminal
// in the same conversation+sender is rejected instead of enqueued.
func TestEnqueueDeliveryRejectsStaleTerminal(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC()
	older := insertHonestyTestRun(t, runtime, "older-run", "group-stale-term", "sender-a", "group", "running", now.Add(-30*time.Second))
	newer := insertHonestyTestRun(t, runtime, "newer-run", "group-stale-term", "sender-a", "group", "responding", now.Add(-5*time.Second))
	if err := runtime.enqueueDelivery(newer, agentReply{Text: "先答后问的这条。"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.db.Exec(`UPDATE agent_deliveries SET status = 'delivered' WHERE run_id = 'newer-run'`); err != nil {
		t.Fatal(err)
	}
	err := runtime.enqueueDelivery(older, agentReply{Text: "迟到的旧回答。"}, "terminal", "")
	if err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale terminal enqueue error = %v", err)
	}
	var count int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_deliveries WHERE run_id = 'older-run'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("stale terminal still wrote %d deliveries", count)
	}
}

// Progress deliveries are deduplicated per run, and the first delivery stamps
// first_response_ms exactly once.
func TestEnqueueDeliveryProgressDedupeAndFirstResponse(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertHonestyTestRun(t, runtime, "progress-run", "group-progress", "sender-a", "group", "running", time.Now().UTC().Add(-1500*time.Millisecond))
	if err := runtime.enqueueDelivery(run, agentReply{Text: "我去弄。"}, "progress", ""); err != nil {
		t.Fatal(err)
	}
	if err := runtime.enqueueDelivery(run, agentReply{Text: "再说一遍我去弄。"}, "progress", ""); err != nil {
		t.Fatal(err)
	}
	var progressCount int
	if err := runtime.db.QueryRow(`SELECT count(*) FROM agent_deliveries
		WHERE run_id = 'progress-run' AND phase = 'progress'`).Scan(&progressCount); err != nil {
		t.Fatal(err)
	}
	if progressCount != 1 {
		t.Fatalf("progress rows = %d, want 1", progressCount)
	}
	var firstResponse int64
	if err := runtime.db.QueryRow(`SELECT first_response_ms FROM agent_runs WHERE id = 'progress-run'`).Scan(&firstResponse); err != nil {
		t.Fatal(err)
	}
	if firstResponse < 1000 || firstResponse > 30000 {
		t.Fatalf("first_response_ms = %d", firstResponse)
	}
}

// A terminal chat reply that promises media with no task behind it is
// rewritten at the outbox choke point.
func TestEnqueueDeliveryRewritesUnbackedPromise(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertHonestyTestRun(t, runtime, "promise-run", "group-promise", "sender-a", "group", "running", time.Now().UTC())
	if err := runtime.enqueueDelivery(run, agentReply{Text: "等我一下，马上给你看。"}, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	var payload string
	if err := runtime.db.QueryRow(`SELECT payload_json FROM agent_deliveries
		WHERE run_id = 'promise-run' AND phase = 'terminal'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(payload, "马上给你看") || !strings.Contains(payload, "这次还没真做出来") {
		t.Fatalf("unbacked promise survived: %s", payload)
	}
}

// Later text segments of one reply are scheduled with a typing rhythm instead
// of becoming eligible instantly.
func TestEnqueueDeliveryPacesSegments(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertHonestyTestRun(t, runtime, "paced-run", "group-paced", "sender-a", "group", "running", time.Now().UTC())
	reply := agentReply{Text: "第一段。第二段。", Segments: []string{"第一段。", "第二段。"}}
	if err := runtime.enqueueDelivery(run, reply, "terminal", ""); err != nil {
		t.Fatal(err)
	}
	rows, err := runtime.db.Query(`SELECT next_attempt_at FROM agent_deliveries
		WHERE run_id = 'paced-run' ORDER BY created_at, rowid`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	values := []any{}
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		values = append(values, value)
	}
	if len(values) != 2 || values[0] != nil || values[1] == nil {
		t.Fatalf("segment scheduling = %#v", values)
	}
}

func TestUnbackedMediaPromisePatternVariants(t *testing.T) {
	positive := []string{
		"等我一下，马上给你看。", "我重新来一张。", "马上安排。",
		"这就去弄。", "稍等，马上发你。", "等我画好。",
	}
	for _, text := range positive {
		if !replyMakesUnbackedMediaPromise(text) {
			t.Fatalf("promise not detected: %q", text)
		}
	}
	negative := []string{
		"照片稍后更新说明。", "游戏再来一局。", "这就是答案。",
		"重新来过的人生也一样。", "马上到家了。",
	}
	for _, text := range negative {
		if replyMakesUnbackedMediaPromise(text) {
			t.Fatalf("false positive promise: %q", text)
		}
	}
}

func TestRepeatedReplySkeletonDetection(t *testing.T) {
	if !repeatedReplySkeleton("行，这次也一样。", []string{"行，先这样。", "换个话题。"}) {
		t.Fatal("stall opener repeat must trigger")
	}
	if repeatedReplySkeleton("行，这次也一样。", []string{"这个可以。", "换个话题。"}) {
		t.Fatal("first stall use must not trigger")
	}
	if repeatedReplySkeleton("好像不太行。", []string{"好，先这样。"}) {
		t.Fatal("好像 must not match the 好 stall opener")
	}
	if !repeatedReplySkeleton("今晚吃啥这个话题聊过了。", []string{"今晚吃啥又来了。", "今晚吃啥没完了。"}) {
		t.Fatal("substantive opener repeated twice must trigger")
	}
	if repeatedReplySkeleton("今晚吃啥？", []string{"今晚吃啥又来了。"}) {
		t.Fatal("substantive opener needs two prior uses")
	}
}

func TestFilterRelevantSearchSourcesRegression(t *testing.T) {
	cases := []struct {
		query    string
		relevant searchSource
		noise    searchSource
	}{
		{"上海 明天 天气", searchSource{Title: "上海明天天气预报", Snippet: "多云转晴"}, searchSource{Title: "英超联赛战报", Snippet: "曼城获胜"}},
		{"golang 泛型 教程", searchSource{Title: "Golang 泛型入门教程", Snippet: "type parameter"}, searchSource{Title: "红烧肉做法", Snippet: "五花肉"}},
		{"苹果 发布会 时间", searchSource{Title: "苹果秋季发布会时间公布", Snippet: "9 月"}, searchSource{Title: "橙子价格上涨", Snippet: "水果批发"}},
		{"高考 分数线 查询", searchSource{Title: "各省高考分数线查询入口", Snippet: "一本线"}, searchSource{Title: "考研英语经验", Snippet: "背单词"}},
		{"人民币 美元 汇率", searchSource{Title: "人民币兑美元汇率中间价", Snippet: "7.1"}, searchSource{Title: "黄金金价走势", Snippet: "克价"}},
		{"北京 演唱会 门票", searchSource{Title: "北京站演唱会门票开售", Snippet: "大麦"}, searchSource{Title: "上海车展新车", Snippet: "新能源"}},
		{"感冒 咳嗽 怎么办", searchSource{Title: "感冒咳嗽的家庭处理", Snippet: "多喝水"}, searchSource{Title: "篮球训练计划", Snippet: "投篮"}},
		{"世界杯 赛程", searchSource{Title: "世界杯完整赛程表", Snippet: "小组赛"}, searchSource{Title: "股市收盘点评", Snippet: "大盘"}},
		{"chatgpt 使用 技巧", searchSource{Title: "ChatGPT 使用技巧合集", Snippet: "提示词"}, searchSource{Title: "钓鱼装备推荐", Snippet: "鱼竿"}},
		{"电动车 续航 排行", searchSource{Title: "电动车续航排行对比", Snippet: "CLTC"}, searchSource{Title: "咖啡豆烘焙", Snippet: "手冲"}},
	}
	for _, testCase := range cases {
		filtered := filterRelevantSearchSources(testCase.query, []searchSource{testCase.noise, testCase.relevant})
		if len(filtered) != 1 || filtered[0].Title != testCase.relevant.Title {
			t.Fatalf("query %q filtered = %#v", testCase.query, filtered)
		}
	}
	if filtered := filterRelevantSearchSources("上海 明天 天气", []searchSource{{Title: "英超联赛战报", Snippet: "曼城获胜"}}); len(filtered) != 0 {
		t.Fatalf("irrelevant-only sources must filter to empty, got %#v", filtered)
	}
}
