package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type taskStepView struct {
	ID           string `json:"id"`
	RunID        string `json:"runId"`
	ParentStepID string `json:"parentStepId,omitempty"`
	StepIndex    int    `json:"stepIndex"`
	Kind         string `json:"kind"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	Attempts     int    `json:"attempts"`
	ErrorCode    string `json:"errorCode,omitempty"`
	StartedAt    string `json:"startedAt,omitempty"`
	FinishedAt   string `json:"finishedAt,omitempty"`
	UpdatedAt    string `json:"updatedAt"`
}

type taskArtifactView struct {
	StepID    string `json:"stepId"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	MimeType  string `json:"mimeType"`
	CreatedAt string `json:"createdAt"`
}

func (a *AgentRuntime) taskContext() context.Context {
	if a.lifecycle != nil {
		return a.lifecycle
	}
	return context.Background()
}

func taskStepID(runID, kind string, step int, name, signature string) string {
	digest := sha256.Sum256([]byte(runID + "\x00" + kind + "\x00" + name + "\x00" + signature))
	return runID + ":" + kind + ":" + string(rune('a'+step%26)) + ":" + hex.EncodeToString(digest[:6])
}

func (a *AgentRuntime) beginTaskStep(runID, parentID, kind, name string, step int, input any) (string, error) {
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	signature := string(encoded)
	id := taskStepID(runID, kind, step, name, signature)
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.ExecContext(a.taskContext(), `INSERT INTO agent_task_steps
		(id, run_id, parent_step_id, step_index, kind, name, status, input_cipher,
		 attempts, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'running', ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET status='running', attempts=agent_task_steps.attempts+1,
		started_at=excluded.started_at, finished_at=NULL, error_code='', updated_at=excluded.updated_at`,
		id, runID, nullable(parentID), step, kind, name, ciphertext, now, now, now)
	return id, err
}

func (a *AgentRuntime) finishTaskStep(id, status, errorCode string, output any) error {
	var ciphertext []byte
	if output != nil {
		encoded, err := json.Marshal(output)
		if err != nil {
			return err
		}
		ciphertext, err = a.encrypt(encoded)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(a.taskContext(), `UPDATE agent_task_steps SET status=?, output_cipher=?, error_code=?,
		finished_at=?, updated_at=? WHERE id=?`, status, ciphertext, errorCode, now, now, id)
	return err
}

func (a *AgentRuntime) cachedTaskToolResult(id string) (toolResult, bool) {
	var ciphertext []byte
	err := a.db.QueryRow("SELECT output_cipher FROM agent_task_steps WHERE id = ? AND status = 'succeeded'", id).Scan(&ciphertext)
	if err != nil || len(ciphertext) == 0 {
		return toolResult{}, false
	}
	plain, err := a.decrypt(ciphertext)
	if err != nil {
		return toolResult{}, false
	}
	var result toolResult
	return result, json.Unmarshal(plain, &result) == nil
}

func (a *AgentRuntime) taskGraphRunExists(runID string) bool {
	var exists int
	return a.db.QueryRow("SELECT 1 FROM agent_runs WHERE id = ?", runID).Scan(&exists) == nil
}

func (a *AgentRuntime) executePersistentToolCall(
	ctx context.Context,
	run runRecord,
	message string,
	policy runtimeToolPolicy,
	mcpRoutes map[string]mcpBridgeRoute,
	step int,
	parentID string,
	call chatToolCall,
) toolResult {
	if !a.taskGraphRunExists(run.ID) {
		return a.executeToolCall(ctx, run, message, policy, mcpRoutes, call)
	}
	input := map[string]string{"callId": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments}
	encoded, _ := json.Marshal(input)
	id := taskStepID(run.ID, "tool", step, call.Function.Name, string(encoded))
	if result, found := a.cachedTaskToolResult(id); found {
		return result
	}
	id, err := a.beginTaskStep(run.ID, parentID, "tool", call.Function.Name, step, input)
	if err != nil {
		body, _ := json.Marshal(map[string]any{"ok": false, "error": "task_persistence_failed"})
		return toolResult{Content: string(body)}
	}
	result := a.executeToolCall(ctx, run, message, policy, mcpRoutes, call)
	status, errorCode := "succeeded", ""
	var response map[string]any
	if json.Unmarshal([]byte(result.Content), &response) == nil && response["ok"] == false {
		status = "failed"
		errorCode, _ = response["error"].(string)
	}
	_ = a.finishTaskStep(id, status, errorCode, result)
	for _, artifact := range result.Attachments {
		_, _ = a.db.Exec(`INSERT OR IGNORE INTO agent_task_artifacts
			(run_id, step_id, kind, name, local_path, mime_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			run.ID, id, artifact.Kind, artifact.Name, artifact.LocalPath, artifact.MimeType,
			time.Now().UTC().Format(time.RFC3339Nano))
		_ = a.recordRunStage(run.ID, "media_attached", time.Now(), map[string]any{
			"kind": artifact.Kind, "name": artifact.Name,
		})
	}
	return result
}

func (a *AgentRuntime) executePersistentOperation(
	run runRecord,
	name string,
	input any,
	operation func() (toolResult, error),
) (toolResult, error) {
	if !a.taskGraphRunExists(run.ID) {
		return operation()
	}
	encoded, _ := json.Marshal(input)
	id := taskStepID(run.ID, "tool", 0, name, string(encoded))
	if result, found := a.cachedTaskToolResult(id); found {
		return result, nil
	}
	id, err := a.beginTaskStep(run.ID, "", "tool", name, 0, input)
	if err != nil {
		return toolResult{}, err
	}
	result, operationErr := operation()
	if operationErr != nil {
		_ = a.finishTaskStep(id, "failed", "tool_execution_failed", nil)
		return result, operationErr
	}
	if err = a.finishTaskStep(id, "succeeded", "", result); err != nil {
		return toolResult{}, err
	}
	for _, artifact := range result.Attachments {
		_, _ = a.db.Exec(`INSERT OR IGNORE INTO agent_task_artifacts
			(run_id, step_id, kind, name, local_path, mime_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			run.ID, id, artifact.Kind, artifact.Name, artifact.LocalPath, artifact.MimeType,
			time.Now().UTC().Format(time.RFC3339Nano))
		_ = a.recordRunStage(run.ID, "media_attached", time.Now(), map[string]any{
			"kind": artifact.Kind, "name": artifact.Name,
		})
	}
	return result, nil
}

func (a *AgentRuntime) taskGraph(ctx context.Context, runID string) (map[string]any, error) {
	var exists int
	if err := a.db.QueryRowContext(ctx, "SELECT count(*) FROM agent_runs WHERE id = ?", runID).Scan(&exists); err != nil {
		return nil, err
	}
	if exists == 0 {
		return nil, mgmtNotFound("task")
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id, run_id, COALESCE(parent_step_id, ''), step_index,
		kind, name, status, attempts, error_code, COALESCE(started_at, ''),
		COALESCE(finished_at, ''), updated_at FROM agent_task_steps WHERE run_id = ?
		ORDER BY step_index, created_at`, runID)
	if err != nil {
		return nil, err
	}
	steps := []taskStepView{}
	for rows.Next() {
		var step taskStepView
		if err = rows.Scan(&step.ID, &step.RunID, &step.ParentStepID, &step.StepIndex,
			&step.Kind, &step.Name, &step.Status, &step.Attempts, &step.ErrorCode,
			&step.StartedAt, &step.FinishedAt, &step.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		steps = append(steps, step)
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	artifacts := []taskArtifactView{}
	rows, err = a.db.QueryContext(ctx, `SELECT step_id, kind, name, mime_type, created_at
		FROM agent_task_artifacts WHERE run_id = ? ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var artifact taskArtifactView
		if err = rows.Scan(&artifact.StepID, &artifact.Kind, &artifact.Name, &artifact.MimeType, &artifact.CreatedAt); err != nil {
			return nil, err
		}
		artifacts = append(artifacts, artifact)
	}
	return map[string]any{"runId": runID, "steps": steps, "artifacts": artifacts}, rows.Err()
}

func (a *AgentRuntime) handleTaskGraphManagement(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/tasks" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		rows, err := a.db.QueryContext(r.Context(), `SELECT run.id, run.state, run.transport,
			count(step.id), run.created_at, run.updated_at FROM agent_runs run
			LEFT JOIN agent_task_steps step ON step.run_id = run.id
			GROUP BY run.id ORDER BY run.created_at DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, state, transport, createdAt, updatedAt string
			var stepCount int
			if err = rows.Scan(&id, &state, &transport, &stepCount, &createdAt, &updatedAt); err != nil {
				return err
			}
			items = append(items, map[string]any{"id": id, "state": state, "transport": transport,
				"stepCount": stepCount, "createdAt": createdAt, "updatedAt": updatedAt})
		}
		mgmtWriteData(w, http.StatusOK, items)
		return rows.Err()
	}
	if strings.HasSuffix(path, "/retry") {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/tasks/"), "/retry")
		if id == "" || strings.Contains(id, "/") {
			return mgmtNotFound("task")
		}
		if err := a.retryTask(r.Context(), id); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusAccepted, map[string]any{"runId": id, "state": "queued"})
		return nil
	}
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	id := strings.TrimPrefix(path, "/api/v1/tasks/")
	if id == "" || strings.Contains(id, "/") {
		return mgmtNotFound("task")
	}
	value, err := a.taskGraph(r.Context(), id)
	if err == nil {
		mgmtWriteData(w, http.StatusOK, value)
	}
	return err
}

func (a *AgentRuntime) retryTask(ctx context.Context, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return mgmtNotFound("task")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	var input []byte
	if err = tx.QueryRowContext(ctx, `SELECT state, input_cipher FROM agent_runs WHERE id = ?`, runID).
		Scan(&state, &input); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("task")
	} else if err != nil {
		return err
	}
	if len(input) == 0 {
		return coreInvalid("task input is no longer available")
	}
	if state != "failed" && state != "cancelled" {
		return coreInvalid("only failed or cancelled tasks can be retried")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET state = 'queued', error_code = NULL, updated_at = ? WHERE id = ?`, now, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_task_steps SET status = 'pending', output_cipher = NULL,
		error_code = '', started_at = NULL, finished_at = NULL, updated_at = ?
		WHERE run_id = ? AND status IN ('failed', 'cancelled', 'running', 'pending')`, now, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_deliveries
		WHERE run_id = ? AND phase = 'terminal' AND status != 'delivered'`, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_search_queries
		WHERE run_id = ? AND status != 'succeeded'`, runID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	_ = a.recordRunStage(runID, "task_retry_requested", time.Now(), map[string]any{"previousState": state})
	a.signalWorker()
	return nil
}
