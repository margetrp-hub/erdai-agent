package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

const mediaVerificationFreshness = 7 * 24 * time.Hour

type mediaCapabilityStatus struct {
	Kind                string `json:"kind"`
	Status              string `json:"status"`
	Reason              string `json:"reason"`
	ProbeHealthy        *bool  `json:"probeHealthy,omitempty"`
	ProbeCheckedAt      string `json:"probeCheckedAt,omitempty"`
	SuccessCount        int    `json:"successCount"`
	FailureCount        int    `json:"failureCount"`
	ConsecutiveFailures int    `json:"consecutiveFailures"`
	LastAttemptAt       string `json:"lastAttemptAt,omitempty"`
	LastSuccessAt       string `json:"lastSuccessAt,omitempty"`
	LastFailureAt       string `json:"lastFailureAt,omitempty"`
	LastArtifactAt      string `json:"lastArtifactAt,omitempty"`
	LastFailureClass    string `json:"lastFailureClass,omitempty"`
}

type retrievalObservability struct {
	Enabled                bool   `json:"enabled"`
	Mode                   string `json:"mode"`
	VectorAlgorithm        string `json:"vectorAlgorithm"`
	EmbeddingEndpointID    string `json:"embeddingEndpointId,omitempty"`
	DocumentCount          int    `json:"documentCount"`
	ChunkCount             int    `json:"chunkCount"`
	EmbeddingCount         int    `json:"embeddingCount"`
	QueryCount24H          int    `json:"queryCount24h"`
	EmbeddingQueryCount24H int    `json:"embeddingQueryCount24h"`
	FallbackCount24H       int    `json:"fallbackCount24h"`
	LastQueryAt            string `json:"lastQueryAt,omitempty"`
}

type memoryObservability struct {
	StoredCount    int    `json:"storedCount"`
	AccessedCount  int    `json:"accessedCount"`
	RecallCount24H int    `json:"recallCount24h"`
	LastWriteAt    string `json:"lastWriteAt,omitempty"`
	LastAccessAt   string `json:"lastAccessAt,omitempty"`
	LastRecallAt   string `json:"lastRecallAt,omitempty"`
}

func (a *AgentRuntime) handleManagementObservability(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	image, err := a.mediaCapabilityStatus(mediaKindImage)
	if err != nil {
		return err
	}
	video, err := a.mediaCapabilityStatus(mediaKindVideo)
	if err != nil {
		return err
	}
	retrieval, err := a.retrievalObservability()
	if err != nil {
		return err
	}
	memory, err := a.memoryObservability()
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{
		"media":     map[string]any{"image": image, "video": video},
		"retrieval": retrieval,
		"memory":    memory,
		"checkedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	return nil
}

func (a *AgentRuntime) mediaCapabilityStatus(kind mediaKind) (mediaCapabilityStatus, error) {
	value := mediaCapabilityStatus{Kind: string(kind), Status: "unverified"}
	capability := "image_generation"
	names := []string{"grok_generate_image", "generate_image"}
	if kind == mediaKindVideo {
		capability = "video_generation"
		names = []string{"grok_generate_video"}
	}
	var probeHealthy sql.NullInt64
	var probeChecked sql.NullString
	err := a.configStore.db.QueryRow(`SELECT min(h.healthy), max(h.checked_at)
		FROM model_endpoints e LEFT JOIN model_health h ON h.endpoint_id = e.id
		WHERE e.enabled = 1 AND instr(e.capabilities_json, ?) > 0`, `"`+capability+`"`).Scan(&probeHealthy, &probeChecked)
	if err != nil {
		return value, err
	}
	if probeHealthy.Valid {
		healthy := probeHealthy.Int64 == 1
		value.ProbeHealthy = &healthy
	}
	value.ProbeCheckedAt = probeChecked.String

	var lastStarted, lastSuccess, lastFailure sql.NullString
	err = a.db.QueryRow(`SELECT success_count, failure_count, consecutive_failures,
		last_started_at, last_success_at, last_failure_at, last_failure_class
		FROM media_task_health WHERE media_kind = ?`, string(kind)).Scan(
		&value.SuccessCount, &value.FailureCount, &value.ConsecutiveFailures,
		&lastStarted, &lastSuccess, &lastFailure, &value.LastFailureClass)
	if errors.Is(err, sql.ErrNoRows) {
		placeholders := "?"
		args := []any{names[0]}
		for _, name := range names[1:] {
			placeholders += ",?"
			args = append(args, name)
		}
		err = a.db.QueryRow(`SELECT
			coalesce(sum(CASE WHEN status = 'succeeded' THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN status = 'failed' THEN 1 ELSE 0 END), 0),
			max(COALESCE(finished_at, started_at)),
			max(CASE WHEN status = 'succeeded' THEN finished_at END),
			max(CASE WHEN status = 'failed' THEN finished_at END)
			FROM agent_task_steps WHERE kind = 'tool' AND name IN (`+placeholders+`)`, args...).Scan(
			&value.SuccessCount, &value.FailureCount, &lastStarted, &lastSuccess, &lastFailure)
		if err == nil && lastFailure.Valid {
			_ = a.db.QueryRow(`SELECT COALESCE(NULLIF(run.failure_class, ''), NULLIF(step.error_code, ''), 'unknown')
				FROM agent_task_steps step JOIN agent_runs run ON run.id = step.run_id
				WHERE step.kind = 'tool' AND step.status = 'failed' AND step.name IN (`+placeholders+`)
				ORDER BY step.finished_at DESC LIMIT 1`, args...).Scan(&value.LastFailureClass)
		}
	} else if err != nil {
		return value, err
	}
	if err != nil {
		return value, err
	}
	value.LastAttemptAt = lastStarted.String
	value.LastSuccessAt = lastSuccess.String
	value.LastFailureAt = lastFailure.String
	var lastArtifact sql.NullString
	_ = a.db.QueryRow("SELECT max(created_at) FROM agent_task_artifacts WHERE kind = ?", string(kind)).Scan(&lastArtifact)
	value.LastArtifactAt = lastArtifact.String

	lastAttempt := parseObservedTime(value.LastAttemptAt)
	lastSuccessTime := parseObservedTime(value.LastSuccessAt)
	lastFailureTime := parseObservedTime(value.LastFailureAt)
	artifactTime := parseObservedTime(value.LastArtifactAt)
	switch {
	case value.ProbeHealthy != nil && !*value.ProbeHealthy:
		value.Status, value.Reason = "degraded", "端点探针失败"
	case lastFailureTime.After(lastSuccessTime):
		value.Status, value.Reason = "degraded", "最近一次真实任务失败"
	case lastAttempt.IsZero():
		value.Reason = "尚无真实任务记录"
	case time.Since(lastAttempt) > mediaVerificationFreshness:
		value.Reason = "真实任务记录已超过 7 天"
	case !lastSuccessTime.IsZero() && !artifactTime.Before(lastSuccessTime):
		value.Status, value.Reason = "available", "近期真实任务已生成附件"
	default:
		value.Reason = "缺少近期成品附件证据"
	}
	return value, nil
}

func (a *AgentRuntime) retrievalObservability() (retrievalObservability, error) {
	policy := a.configStore.retrievalPolicy()
	value := retrievalObservability{
		Enabled: policy.Enabled, Mode: policy.Mode, VectorAlgorithm: policy.VectorAlgorithm,
		EmbeddingEndpointID: policy.EmbeddingEndpoint,
	}
	for target, query := range map[*int]string{
		&value.DocumentCount:  "SELECT count(*) FROM knowledge_documents",
		&value.ChunkCount:     "SELECT count(*) FROM knowledge_chunks",
		&value.EmbeddingCount: "SELECT count(*) FROM knowledge_chunk_embeddings",
	} {
		if err := a.configStore.db.QueryRow(query).Scan(target); err != nil {
			return value, err
		}
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	var lastQuery sql.NullString
	if err := a.db.QueryRow(`SELECT count(*), max(completed_at) FROM run_stage_events
		WHERE stage = 'retrieval_query' AND completed_at >= ?`, cutoff).Scan(&value.QueryCount24H, &lastQuery); err != nil {
		return value, err
	}
	value.LastQueryAt = lastQuery.String
	if err := a.db.QueryRow(`SELECT count(*) FROM run_stage_events
		WHERE stage = 'embedding_query' AND completed_at >= ?`, cutoff).Scan(&value.EmbeddingQueryCount24H); err != nil {
		return value, err
	}
	if err := a.db.QueryRow(`SELECT count(*) FROM run_stage_events
		WHERE stage = 'retrieval_query' AND completed_at >= ?
		AND json_extract(details_json, '$.vectorFallback') = 1`, cutoff).Scan(&value.FallbackCount24H); err != nil {
		return value, err
	}
	return value, nil
}

func (a *AgentRuntime) memoryObservability() (memoryObservability, error) {
	value := memoryObservability{}
	var lastWrite, lastAccess, lastRecall sql.NullString
	now := formatStoreTime(time.Now().UTC())
	if err := a.db.QueryRow(`SELECT count(*),
		coalesce(sum(CASE WHEN access_count > 0 THEN 1 ELSE 0 END), 0),
		max(updated_at), max(last_accessed_at) FROM agent_memories
		WHERE expires_at IS NULL OR expires_at > ?`, now).Scan(
		&value.StoredCount, &value.AccessedCount, &lastWrite, &lastAccess); err != nil {
		return value, err
	}
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	if err := a.db.QueryRow(`SELECT count(*), max(completed_at) FROM run_stage_events
		WHERE stage = 'memory_recall' AND completed_at >= ?`, cutoff).Scan(&value.RecallCount24H, &lastRecall); err != nil {
		return value, err
	}
	value.LastWriteAt, value.LastAccessAt, value.LastRecallAt = lastWrite.String, lastAccess.String, lastRecall.String
	return value, nil
}

func parseObservedTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	return parsed
}
