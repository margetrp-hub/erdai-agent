package main

import (
	"strings"
	"time"
)

func (a *AgentRuntime) executeObservedMedia(run runRecord, kind mediaKind, execute func() (toolResult, error)) (toolResult, error) {
	started := time.Now().UTC()
	result, err := execute()
	a.recordMediaTaskOutcome(run.ID, kind, started, result, err)
	return result, err
}

func (a *AgentRuntime) recordMediaTaskOutcome(runID string, kind mediaKind, started time.Time, result toolResult, taskErr error) {
	if a == nil || a.db == nil || kind != mediaKindImage && kind != mediaKindVideo {
		return
	}
	completed := time.Now().UTC()
	endpointID := ""
	if strings.TrimSpace(runID) != "" {
		_ = a.db.QueryRow("SELECT selected_endpoint_id FROM agent_runs WHERE id = ?", runID).Scan(&endpointID)
	}
	succeeded := taskErr == nil && len(result.Attachments) > 0
	failureClass := ""
	if !succeeded {
		failureClass = classifyProviderFailure(taskErr)
		if failureClass == "" {
			failureClass = "invalid_output"
		}
	}
	var successAt, failureAt any
	if succeeded {
		successAt = completed.Format(time.RFC3339Nano)
	} else {
		failureAt = completed.Format(time.RFC3339Nano)
	}
	_, _ = a.db.Exec(`INSERT INTO media_task_health (
		media_kind, success_count, failure_count, consecutive_failures,
		last_started_at, last_success_at, last_failure_at, last_failure_class,
		last_endpoint_id, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(media_kind) DO UPDATE SET
		success_count = media_task_health.success_count + excluded.success_count,
		failure_count = media_task_health.failure_count + excluded.failure_count,
		consecutive_failures = CASE WHEN excluded.success_count = 1 THEN 0
			ELSE media_task_health.consecutive_failures + 1 END,
		last_started_at = excluded.last_started_at,
		last_success_at = COALESCE(excluded.last_success_at, media_task_health.last_success_at),
		last_failure_at = COALESCE(excluded.last_failure_at, media_task_health.last_failure_at),
		last_failure_class = CASE WHEN excluded.failure_count = 1 THEN excluded.last_failure_class ELSE '' END,
		last_endpoint_id = excluded.last_endpoint_id,
		updated_at = excluded.updated_at`, string(kind), boolInt(succeeded), boolInt(!succeeded),
		boolInt(!succeeded), started.Format(time.RFC3339Nano), successAt, failureAt,
		failureClass, strings.TrimSpace(endpointID), completed.Format(time.RFC3339Nano))
	_ = a.recordRunStage(runID, "media_task_result", started, map[string]any{
		"kind": kind, "endpointId": strings.TrimSpace(endpointID), "succeeded": succeeded,
		"attachmentCount": len(result.Attachments), "failureClass": failureClass,
	})
}
