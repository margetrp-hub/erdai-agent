package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMediaCapabilityStatusUsesRealTaskOutcomeAndArtifact(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "media-health-run", "media-health-sender")

	runtime.recordMediaTaskOutcome(run.ID, mediaKindImage, time.Now().Add(-time.Second), toolResult{}, errors.New("provider failed"))
	failed, err := runtime.mediaCapabilityStatus(mediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "degraded" || failed.FailureCount != 1 || failed.LastFailureClass == "" {
		t.Fatalf("failed media status = %+v", failed)
	}

	result, err := runtime.executePersistentOperation(run, "grok_generate_image", map[string]string{"prompt": "test"}, func() (toolResult, error) {
		return runtime.executeObservedMedia(run, mediaKindImage, func() (toolResult, error) {
			return toolResult{Attachments: []agentAttachment{{Kind: "image", Name: "test.png", LocalPath: "/tmp/test.png", MimeType: "image/png"}}}, nil
		})
	})
	if err != nil || len(result.Attachments) != 1 {
		t.Fatalf("successful media result = %+v, err=%v", result, err)
	}
	available, err := runtime.mediaCapabilityStatus(mediaKindImage)
	if err != nil {
		t.Fatal(err)
	}
	if available.Status != "available" || available.SuccessCount != 1 || available.FailureCount != 1 || available.LastArtifactAt == "" {
		t.Fatalf("available media status = %+v", available)
	}
}

func TestRuntimeKnowledgeRecordsUserRetrievalMetrics(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	run := insertQuotaTestRun(t, runtime, "rag-observability-run", "rag-observability-sender")
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.configStore.db.Exec(`INSERT INTO knowledge_documents
		(id, namespace, title, source_uri, content, content_hash, metadata_json, created_at, updated_at)
		VALUES ('observability-doc', 'default', 'Alpha', '', 'alpha knowledge', 'hash-alpha', '{}', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	setTestIntegration(t, runtime.configStore.db, "retrieval_policy", map[string]any{
		"enabled": true, "mode": "hybrid", "vectorAlgorithm": "local_hash",
		"dimensions": 64, "minimumSimilarity": 0, "topK": 5, "candidateK": 10,
		"chunkSize": 200, "chunkOverlap": 20,
	})
	items, err := runtime.searchRuntimeKnowledgeForRun(context.Background(), run.ID, "default", "alpha")
	if err != nil || len(items) == 0 {
		t.Fatalf("knowledge search = %+v, err=%v", items, err)
	}
	var retrievalStages, embeddingStages int
	if err = runtime.db.QueryRow("SELECT count(*) FROM run_stage_events WHERE run_id = ? AND stage = 'retrieval_query'", run.ID).Scan(&retrievalStages); err != nil {
		t.Fatal(err)
	}
	if err = runtime.db.QueryRow("SELECT count(*) FROM run_stage_events WHERE run_id = ? AND stage = 'embedding_query'", run.ID).Scan(&embeddingStages); err != nil {
		t.Fatal(err)
	}
	if retrievalStages != 1 || embeddingStages != 0 {
		t.Fatalf("observability stages = retrieval:%d embedding:%d", retrievalStages, embeddingStages)
	}
	observability, err := runtime.retrievalObservability()
	if err != nil {
		t.Fatal(err)
	}
	if observability.QueryCount24H != 1 || observability.ChunkCount == 0 {
		t.Fatalf("retrieval observability = %+v", observability)
	}
}
