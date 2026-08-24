package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"
)

func insertVectorTestDocument(t *testing.T, store *coreConfigStore, id, title, content string) {
	t.Helper()
	hash := sha256.Sum256([]byte(content))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := store.db.Exec(`
		INSERT INTO knowledge_documents (
			id, namespace, title, source_uri, content, content_hash, metadata_json,
			created_at, updated_at
		) VALUES (?, 'default', ?, '', ?, ?, '{}', ?, ?)
	`, id, title, content, hex.EncodeToString(hash[:]), now, now); err != nil {
		t.Fatal(err)
	}
}

func TestHybridKnowledgeSearchBuildsAndRefreshesVectors(t *testing.T) {
	store, err := openCoreConfigStore(t.TempDir() + "/core.sqlite3")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	insertVectorTestDocument(t, store, "quantum", "量子计算", "量子纠错可以降低量子比特噪声。")
	insertVectorTestDocument(t, store, "recipe", "厨房菜谱", "番茄炒蛋需要番茄和鸡蛋。")

	for _, mode := range []string{"keyword", "vector", "hybrid"} {
		setTestIntegration(t, store.db, "retrieval_policy", map[string]any{
			"enabled": true, "mode": mode, "vectorAlgorithm": "local_hash", "dimensions": 128,
			"keywordWeight": 0.45, "vectorWeight": 0.55, "minimumSimilarity": 0, "topK": 1,
		})
		items, searchErr := store.searchHybridKnowledge("default", "量子纠错", 0)
		if searchErr != nil {
			t.Fatalf("%s search: %v", mode, searchErr)
		}
		if len(items) != 1 || items[0].ID != "quantum" {
			t.Fatalf("%s search = %+v", mode, items)
		}
	}

	var vectorHash string
	var dimensions int
	if err = store.db.QueryRow(`
		SELECT content_hash, dimensions FROM knowledge_vectors WHERE document_id = 'quantum'
	`).Scan(&vectorHash, &dimensions); err != nil || dimensions != 128 {
		t.Fatalf("stored vector = hash %q dimensions %d: %v", vectorHash, dimensions, err)
	}
	newContent := "分布式数据库使用一致性协议。"
	newHash := sha256.Sum256([]byte(newContent))
	if _, err = store.db.Exec(`
		UPDATE knowledge_documents SET title = '数据库', content = ?, content_hash = ?
		WHERE id = 'quantum'
	`, newContent, hex.EncodeToString(newHash[:])); err != nil {
		t.Fatal(err)
	}
	if _, err = store.searchHybridKnowledge("default", "分布式数据库", 0); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`
		SELECT content_hash FROM knowledge_vectors WHERE document_id = 'quantum'
	`).Scan(&vectorHash); err != nil || vectorHash != hex.EncodeToString(newHash[:]) {
		t.Fatalf("refreshed vector hash = %q: %v", vectorHash, err)
	}
}

func TestRetrievalAndDocumentPoliciesValidateAndPersist(t *testing.T) {
	path, db := newTestCoreConfig(t)
	runtime := &AgentRuntime{
		configStore: &coreConfigStore{db: db},
		adminToken:  testAdminToken, runtimeToken: testRuntimeToken,
	}
	defer runtime.configStore.Close()
	_ = path

	invalid := nativeConfigRequest(t, runtime, http.MethodPut, "/api/v1/integrations/retrieval_policy", map[string]any{
		"dimensions": 63,
	}, "admin")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid dimensions = %d: %s", invalid.Code, invalid.Body.String())
	}
	invalid = nativeConfigRequest(t, runtime, http.MethodPut, "/api/v1/integrations/retrieval_policy", map[string]any{
		"keywordWeight": 0, "vectorWeight": 0,
	}, "admin")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("zero weights = %d: %s", invalid.Code, invalid.Body.String())
	}
	valid := nativeConfigRequest(t, runtime, http.MethodPut, "/api/v1/integrations/retrieval_policy", map[string]any{
		"mode": "vector", "dimensions": 512, "topK": 7,
	}, "admin")
	if valid.Code != http.StatusOK {
		t.Fatalf("valid retrieval policy = %d: %s", valid.Code, valid.Body.String())
	}
	policy := runtime.configStore.retrievalPolicy()
	if policy.Mode != "vector" || policy.Dimensions != 512 || policy.TopK != 7 {
		t.Fatalf("saved retrieval policy = %+v", policy)
	}

	invalid = nativeConfigRequest(t, runtime, http.MethodPut, "/api/v1/integrations/document_policy", map[string]any{
		"maxFileMb": 101,
	}, "admin")
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid document policy = %d: %s", invalid.Code, invalid.Body.String())
	}
	valid = nativeConfigRequest(t, runtime, http.MethodPut, "/api/v1/integrations/document_policy", map[string]any{
		"allowPptx": false, "maxExtractChars": 32000,
	}, "admin")
	if valid.Code != http.StatusOK {
		t.Fatalf("valid document policy = %d: %s", valid.Code, valid.Body.String())
	}
	document := runtime.documentPolicy()
	if document.AllowPptx || document.MaxExtractChars != 32000 {
		t.Fatalf("saved document policy = %+v", document)
	}
}
