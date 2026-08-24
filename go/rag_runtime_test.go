package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSyncKnowledgeChunksRebuildsWhenPolicyChangesButCountMatches(t *testing.T) {
	store, err := openCoreConfigStore(t.TempDir() + "/core.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	content := strings.Repeat("甲", 590) + "。" + strings.Repeat("乙", 62)
	hash := sha256.Sum256([]byte(content))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = store.db.Exec(`INSERT INTO knowledge_documents
		(id, namespace, title, source_uri, content, content_hash, metadata_json, created_at, updated_at)
		VALUES ('chunk-policy', 'default', '策略切换', '', ?, ?, '{}', ?, ?)`,
		content, hex.EncodeToString(hash[:]), now, now); err != nil {
		t.Fatal(err)
	}
	runtime := &AgentRuntime{configStore: store}
	first := defaultRetrievalPolicy()
	first.ChunkSize, first.ChunkOverlap = 600, 100
	if err = runtime.syncKnowledgeChunks(context.Background(), "default", first); err != nil {
		t.Fatal(err)
	}
	var firstLength, firstCount int
	if err = store.db.QueryRow(`SELECT length(content),
		(SELECT count(*) FROM knowledge_chunks WHERE document_id = 'chunk-policy')
		FROM knowledge_chunks WHERE document_id = 'chunk-policy' AND ordinal = 0`).Scan(&firstLength, &firstCount); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ChunkSize, second.ChunkOverlap = 360, 60
	if err = runtime.syncKnowledgeChunks(context.Background(), "default", second); err != nil {
		t.Fatal(err)
	}
	var secondLength, secondCount int
	if err = store.db.QueryRow(`SELECT length(content),
		(SELECT count(*) FROM knowledge_chunks WHERE document_id = 'chunk-policy')
		FROM knowledge_chunks WHERE document_id = 'chunk-policy' AND ordinal = 0`).Scan(&secondLength, &secondCount); err != nil {
		t.Fatal(err)
	}
	if firstCount != secondCount || firstCount != 2 {
		t.Fatalf("chunk counts = %d -> %d, want the same two chunks", firstCount, secondCount)
	}
	if firstLength == secondLength || secondLength > second.ChunkSize {
		t.Fatalf("first chunk length = %d -> %d, want rebuilt at <= %d", firstLength, secondLength, second.ChunkSize)
	}
}

func TestSplitKnowledgeContentHonorsBoundariesAndOverlap(t *testing.T) {
	parts := splitKnowledgeContent("甲乙丙丁。戊己庚辛！壬癸子丑？寅卯辰巳。", 8, 2)
	if len(parts) < 3 {
		t.Fatalf("chunks = %#v", parts)
	}
	for index, part := range parts {
		if len([]rune(part)) > 8 {
			t.Fatalf("chunk %d exceeds size: %q", index, part)
		}
	}
	previous := []rune(parts[0])
	if len(previous) < 2 || !strings.Contains(parts[1], string(previous[len(previous)-2:])) {
		t.Fatalf("overlap was not retained: %#v", parts)
	}
}

func TestRuntimeKnowledgeUsesRemoteEmbeddingRerankAndCache(t *testing.T) {
	var embeddingCalls atomic.Int32
	var rerankCalls atomic.Int32
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/embeddings":
			embeddingCalls.Add(1)
			var payload struct {
				Input []string `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			data := make([]map[string]any, len(payload.Input))
			for index, input := range payload.Input {
				vector := []float64{0, 1}
				if strings.Contains(input, "alpha") {
					vector = []float64{1, 0}
				}
				data[index] = map[string]any{"index": index, "embedding": vector}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		case "/rerank":
			rerankCalls.Add(1)
			var payload struct {
				Documents []string `json:"documents"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			results := make([]map[string]any, len(payload.Documents))
			for index := range payload.Documents {
				results[index] = map[string]any{"index": index, "relevance_score": float64(len(payload.Documents) - index)}
			}
			writeJSON(w, http.StatusOK, map[string]any{"results": results})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	t.Setenv("ERDAI_RAG_EMBED_KEY", "embedding-secret")
	t.Setenv("ERDAI_RAG_RERANK_KEY", "rerank-secret")
	store, err := openCoreConfigStore(t.TempDir() + "/core.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	content := "alpha knowledge is authoritative. beta context is secondary. gamma context remains available."
	hash := sha256.Sum256([]byte(content))
	if _, err = store.db.Exec(`INSERT INTO knowledge_documents
		(id, namespace, title, source_uri, content, content_hash, metadata_json, created_at, updated_at)
		VALUES ('doc-rag', 'default', 'alpha guide', 'https://source.example/alpha', ?, ?, '{}', ?, ?)`,
		content, hex.EncodeToString(hash[:]), now, now); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ id, provider, protocol, model, capability, key string }{
		{"embed-endpoint", "embed-provider", "openai_embeddings", "embed-model", "embedding", "ERDAI_RAG_EMBED_KEY"},
		{"rerank-endpoint", "rerank-provider", "cohere_rerank", "rerank-model", "rerank", "ERDAI_RAG_RERANK_KEY"},
	} {
		connectionID := item.id + "-connection"
		if _, err = store.db.Exec(`INSERT INTO provider_connections
			(id, provider, protocol, api_base, credential_ref, timeout_seconds, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 5, 1, ?, ?)`, connectionID, item.provider, item.protocol,
			provider.URL, item.key, now, now); err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`INSERT INTO model_endpoints
			(id, provider, model, enabled, capabilities_json, input_cost_per_million, output_cost_per_million,
			 quality_score, priority, max_context_tokens, execution_kind, adapter_ref, created_at, updated_at)
			VALUES (?, ?, ?, 1, ?, 0, 0, 1, 1, 1000, 'llm', '', ?, ?)`, item.id, item.provider,
			item.model, "[\""+item.capability+"\"]", now, now); err != nil {
			t.Fatal(err)
		}
		if _, err = store.db.Exec(`INSERT INTO model_endpoint_connections
			(endpoint_id, connection_id, updated_at) VALUES (?, ?, ?)`, item.id, connectionID, now); err != nil {
			t.Fatal(err)
		}
	}
	setTestIntegration(t, store.db, "retrieval_policy", map[string]any{
		"enabled": true, "mode": "hybrid", "vectorAlgorithm": "remote_embedding",
		"embeddingEndpointId": "embed-endpoint", "rerankEndpointId": "rerank-endpoint",
		"dimensions": 2, "minimumSimilarity": 0, "topK": 1, "candidateK": 10,
		"chunkSize": 32, "chunkOverlap": 4,
	})
	runtime := &AgentRuntime{configStore: store, client: provider.Client()}
	items, err := runtime.searchRuntimeKnowledge(t.Context(), "default", "alpha")
	if err != nil || len(items) != 1 || items[0].ID == "" {
		t.Fatalf("remote RAG = %#v, err=%v", items, err)
	}
	firstEmbeddings, firstReranks := embeddingCalls.Load(), rerankCalls.Load()
	if firstEmbeddings < 2 || firstReranks != 1 {
		t.Fatalf("remote calls = embeddings:%d rerank:%d", firstEmbeddings, firstReranks)
	}
	if _, err = runtime.searchRuntimeKnowledge(t.Context(), "default", "alpha"); err != nil {
		t.Fatal(err)
	}
	if embeddingCalls.Load() != firstEmbeddings+1 {
		t.Fatalf("cached chunk embeddings were recomputed: %d -> %d", firstEmbeddings, embeddingCalls.Load())
	}
}
