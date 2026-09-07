package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type memorySemanticMock struct {
	mu      sync.Mutex
	inputs  [][]string
	respond func(http.ResponseWriter, *http.Request, []string)
}

func (mock *memorySemanticMock) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/embeddings" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("Authorization") != "Bearer memory-test-key" {
		http.Error(w, "missing test authentication", http.StatusUnauthorized)
		return
	}
	var payload struct {
		Input []string `json:"input"`
	}
	if json.NewDecoder(r.Body).Decode(&payload) != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	mock.mu.Lock()
	mock.inputs = append(mock.inputs, append([]string(nil), payload.Input...))
	mock.mu.Unlock()
	if mock.respond != nil {
		mock.respond(w, r, payload.Input)
		return
	}
	memorySemanticReply(w, payload.Input)
}

func memorySemanticReply(w http.ResponseWriter, inputs []string) {
	data := make([]map[string]any, len(inputs))
	for index := range inputs {
		data[index] = map[string]any{"index": index, "embedding": []float64{1, 0}}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

func (mock *memorySemanticMock) batches() [][]string {
	mock.mu.Lock()
	defer mock.mu.Unlock()
	result := make([][]string, len(mock.inputs))
	for index, input := range mock.inputs {
		result[index] = append([]string(nil), input...)
	}
	return result
}

func newMemorySemanticFixture(t *testing.T, optIn bool, handler func(http.ResponseWriter, *http.Request, []string)) (*MemoryGroupStore, *memorySemanticMock) {
	t.Helper()
	mock := &memorySemanticMock{respond: handler}
	provider := httptest.NewTLSServer(mock)
	t.Cleanup(provider.Close)
	t.Setenv("ERDAI_RAG_EMBED_KEY", "memory-test-key")
	config, err := openCoreConfigStore(t.TempDir() + "/core.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = config.Close() })
	memory := newTestMemoryGroupStore(t)
	memory.runtime.db = config.db
	memory.runtime.configStore = config
	memory.runtime.client = provider.Client()
	mustInitMemoryGroupSchema(t, memory)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = config.db.Exec(`INSERT INTO provider_connections
		(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
		VALUES ('memory-connection', 'memory-provider', 'openai_embeddings', ?, 'ERDAI_RAG_EMBED_KEY', 5, 1, ?, ?)`, provider.URL, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = config.db.Exec(`INSERT INTO model_endpoints
		(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
		quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
		VALUES ('memory-endpoint', 'memory-provider', 'memory-embedding', 1, '["embedding"]', 0, 0, 1, 1, 1000, 'llm', '', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = config.db.Exec(`INSERT INTO model_endpoint_connections(endpoint_id,connection_id,updated_at)
		VALUES ('memory-endpoint','memory-connection',?)`, now); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, config.db, "retrieval_policy", map[string]any{
		"enabled": true, "mode": "hybrid", "vectorAlgorithm": "remote_embedding",
		"embeddingEndpointId": "memory-endpoint", "dimensions": 2, "minimumSimilarity": 0,
	})
	if optIn {
		setTestIntegration(t, config.db, "memory_policy", map[string]any{"enabled": true, "semanticRecallEnabled": true})
	}
	return memory, mock
}

func addSemanticMemory(t *testing.T, memory *MemoryGroupStore, scope, content string) RecalledMemory {
	t.Helper()
	item, _, err := memory.AddMemory(t.Context(), scope, content)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestMemorySemanticOptInDefaultAndEnabledRouteRequired(t *testing.T) {
	memory, mock := newMemorySemanticFixture(t, false, nil)
	scope := personaMemoryScope("doubao", "user", "alice")
	addSemanticMemory(t, memory, scope, "我习惯早上喝黑咖啡")
	if memory.runtime.memoryPolicy(t.Context()).SemanticRecallEnabled {
		t.Fatal("personal semantic recall must default to disabled")
	}
	if items, err := memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5); err != nil || len(items) != 0 {
		t.Fatalf("default keyword-only search = %#v, %v", items, err)
	}
	if len(mock.batches()) != 0 {
		t.Fatal("default policy sent personal memory to a provider")
	}
	setTestIntegration(t, memory.runtime.configStore.db, "memory_policy", map[string]any{"enabled": false, "semanticRecallEnabled": true})
	if items, err := memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5); err != nil || len(items) != 0 || len(mock.batches()) != 0 {
		t.Fatalf("disabled memory policy sent data despite opt-in: items=%#v, err=%v, requests=%d", items, err, len(mock.batches()))
	}
	setTestIntegration(t, memory.runtime.configStore.db, "memory_policy", map[string]any{"enabled": true, "semanticRecallEnabled": true})
	if _, err := memory.runtime.configStore.db.Exec("UPDATE model_endpoints SET enabled=0 WHERE id='memory-endpoint'"); err != nil {
		t.Fatal(err)
	}
	if items, err := memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5); err != nil || len(items) != 0 || len(mock.batches()) != 0 {
		t.Fatalf("disabled endpoint was used: items=%#v, err=%v, requests=%d", items, err, len(mock.batches()))
	}
}

func TestMemorySemanticParaphraseCacheEncryptionAndDeleteCascade(t *testing.T) {
	memory, mock := newMemorySemanticFixture(t, true, nil)
	scope := personaMemoryScope("doubao", "user", "alice")
	content, query := "我习惯早上喝黑咖啡", "晨间饮品偏好"
	for _, char := range query {
		if strings.ContainsRune(content, char) {
			t.Fatal("fixture must not share literal characters")
		}
	}
	item := addSemanticMemory(t, memory, scope, content)
	for iteration := 0; iteration < 2; iteration++ {
		items, err := memory.SearchMemories(t.Context(), scope, query, 5)
		if err != nil || len(items) != 1 || items[0].ID != item.ID {
			t.Fatalf("paraphrase recall %d = %#v, %v", iteration, items, err)
		}
	}
	batches := mock.batches()
	if len(batches) != 2 || !slices.Equal(batches[0], []string{query, content}) || !slices.Equal(batches[1], []string{query}) {
		t.Fatalf("cache did not avoid repeating memory content: %#v", batches)
	}
	var cipher []byte
	if err := memory.runtime.db.QueryRow("SELECT vector_cipher FROM agent_memory_embeddings WHERE memory_id=?", item.ID).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	plain, err := memory.runtime.decrypt(cipher)
	if err != nil || string(plain) != "[1,0]" || json.Valid(cipher) {
		t.Fatalf("embedding cache was not encrypted: decryptErr=%v", err)
	}
	if forgotten, err := memory.ForgetMemory(t.Context(), scope, item.ID); err != nil || !forgotten {
		t.Fatalf("forget = %v, %v", forgotten, err)
	}
	var count int
	if err := memory.runtime.db.QueryRow("SELECT count(*) FROM agent_memory_embeddings WHERE memory_id=?", item.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("forgotten memory retained cached embedding: %d, %v", count, err)
	}
}

func TestMemorySemanticScopeAndSensitiveInputBoundaries(t *testing.T) {
	memory, mock := newMemorySemanticFixture(t, true, nil)
	scope := personaMemoryScope("doubao", "user", "alice")
	allowed := addSemanticMemory(t, memory, scope, "我习惯早上喝黑咖啡")
	addSemanticMemory(t, memory, personaMemoryScope("doubao", "user", "bob"), "另一位用户独有的饮食习惯")
	addSemanticMemory(t, memory, personaMemoryScope("xiaoman", "user", "alice"), "另一角色独有的偏好")
	addSemanticMemory(t, memory, scope, "密码是模拟测试专用值")
	items, err := memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5)
	if err != nil || len(items) != 1 || items[0].ID != allowed.ID {
		t.Fatalf("scope-limited recall = %#v, %v", items, err)
	}
	batches := mock.batches()
	if len(batches) != 1 || !slices.Equal(batches[0], []string{"晨间饮品偏好", allowed.UntrustedContent}) {
		t.Fatalf("provider received other scope or sensitive content: %#v", batches)
	}
	if _, err = memory.SearchMemories(t.Context(), scope, "我的密码是什么", 5); err != nil {
		t.Fatal(err)
	}
	if len(mock.batches()) != 1 {
		t.Fatal("sensitive query was sent to embedding provider")
	}
}

func TestMemorySemanticProviderFailureKeepsLiteralRecallWithoutInventing(t *testing.T) {
	memory, mock := newMemorySemanticFixture(t, true, func(w http.ResponseWriter, _ *http.Request, _ []string) {
		http.Error(w, "mock unavailable", http.StatusServiceUnavailable)
	})
	scope := personaMemoryScope("doubao", "user", "alice")
	item := addSemanticMemory(t, memory, scope, "我习惯早上喝黑咖啡")
	items, err := memory.SearchMemories(t.Context(), scope, "黑咖啡", 5)
	if err != nil || len(items) != 1 || items[0].ID != item.ID {
		t.Fatalf("provider error lost literal evidence: %#v, %v", items, err)
	}
	items, err = memory.SearchMemories(t.Context(), scope, "晨间饮品偏好", 5)
	if err != nil || len(items) != 0 {
		t.Fatalf("provider error invented a nonliteral match: %#v, %v", items, err)
	}
	if len(mock.batches()) != 2 {
		t.Fatalf("expected exactly two bounded attempts, got %d", len(mock.batches()))
	}
}

func TestMemorySemanticForgetOrSupersedeDuringEmbeddingCannotReviveMemory(t *testing.T) {
	for _, operation := range []string{"forget", "supersede"} {
		t.Run(operation, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var enterOnce, releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			memory, _ := newMemorySemanticFixture(t, true, func(w http.ResponseWriter, r *http.Request, inputs []string) {
				enterOnce.Do(func() { close(entered) })
				select {
				case <-release:
					memorySemanticReply(w, inputs)
				case <-r.Context().Done():
				}
			})
			t.Cleanup(unblock)
			scope := personaMemoryScope("doubao", "user", "alice")
			first := stableMemoryCandidate{Content: "用户希望被称为老板", Kind: "address", Importance: 0.9}
			if err := memory.captureMemoryFact(t.Context(), scope, first, "event-old", time.Now().Add(-time.Minute)); err != nil {
				t.Fatal(err)
			}
			original, err := memory.SearchMemories(t.Context(), scope, "", 5)
			if err != nil || len(original) != 1 {
				t.Fatalf("initial memory = %#v, %v", original, err)
			}
			type searchResult struct {
				items []RecalledMemory
				err   error
			}
			done := make(chan searchResult, 1)
			go func() {
				items, searchErr := memory.SearchMemories(t.Context(), scope, "尊号", 5)
				done <- searchResult{items, searchErr}
			}()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("embedding mock was not reached")
			}
			if operation == "forget" {
				if forgotten, forgetErr := memory.ForgetMemory(t.Context(), scope, original[0].ID); forgetErr != nil || !forgotten {
					t.Fatalf("concurrent forget = %v, %v", forgotten, forgetErr)
				}
			} else {
				if err := memory.captureMemoryFact(t.Context(), scope, stableMemoryCandidate{Content: "用户希望被称为阿明", Kind: "address", Importance: 0.9}, "event-new", time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			unblock()
			select {
			case result := <-done:
				if result.err != nil || len(result.items) != 0 {
					t.Fatalf("stale memory returned after %s: %#v, %v", operation, result.items, result.err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("search did not finish after embedding was released")
			}
			var count int
			if err := memory.runtime.db.QueryRow("SELECT count(*) FROM agent_memory_embeddings WHERE memory_id=?", original[0].ID).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale cache revived after %s: %d, %v", operation, count, err)
			}
			if operation == "supersede" {
				current, err := memory.SearchMemories(t.Context(), scope, "", 5)
				if err != nil || len(current) != 1 || current[0].UntrustedContent != "用户希望被称为阿明" {
					t.Fatalf("supersede lost new fact: %#v, %v", current, err)
				}
			}
		})
	}
}

type memorySemanticDeadlineTransport struct {
	base      http.RoundTripper
	remaining chan time.Duration
}

func (transport memorySemanticDeadlineTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	deadline, present := request.Context().Deadline()
	if !present {
		transport.remaining <- -1
	} else {
		transport.remaining <- time.Until(deadline)
	}
	return transport.base.RoundTrip(request)
}

func TestMemorySemanticDeadlineAndParentCancellation(t *testing.T) {
	for _, parentTimeout := range []time.Duration{5 * time.Second, 80 * time.Millisecond} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			memory, mock := newMemorySemanticFixture(t, true, func(_ http.ResponseWriter, r *http.Request, _ []string) {
				<-r.Context().Done()
			})
			remaining := make(chan time.Duration, 1)
			memory.runtime.client.Transport = memorySemanticDeadlineTransport{base: memory.runtime.client.Transport, remaining: remaining}
			scope := personaMemoryScope("doubao", "user", "alice")
			addSemanticMemory(t, memory, scope, "我习惯早上喝黑咖啡")
			ctx, cancel := context.WithTimeout(t.Context(), parentTimeout)
			defer cancel()
			started := time.Now()
			items, _ := memory.SearchMemories(ctx, scope, "晨间饮品偏好", 5)
			if len(items) != 0 {
				t.Fatalf("timed-out provider invented evidence: %#v", items)
			}
			limit := min(parentTimeout, 1500*time.Millisecond)
			select {
			case available := <-remaining:
				if available <= 0 || available > limit {
					t.Fatalf("request deadline %v exceeded %v or missing", available, limit)
				}
			default:
				t.Fatal("request did not pass through deadline observer")
			}
			if elapsed := time.Since(started); elapsed > limit+750*time.Millisecond {
				t.Fatalf("cancellation exceeded bounded scheduling tolerance: %v", elapsed)
			}
			if len(mock.batches()) != 1 {
				t.Fatalf("deadline caused extra provider attempts: %d", len(mock.batches()))
			}
		})
	}
}
