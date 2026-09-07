package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
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
	ID         int64  `json:"id"`
	ContentURL string `json:"contentUrl,omitempty"`
	StepID     string `json:"stepId"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	MimeType   string `json:"mimeType"`
	CreatedAt  string `json:"createdAt"`
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
	result, err := a.db.ExecContext(a.taskContext(), `UPDATE agent_task_steps SET status=?, output_cipher=?, error_code=?,
		finished_at=?, updated_at=? WHERE id=?`, status, ciphertext, errorCode, now, now, id)
	if err == nil {
		if affected, countErr := result.RowsAffected(); countErr != nil {
			return countErr
		} else if affected != 1 {
			return errors.New("task receipt row is missing")
		}
	}
	return err
}

var errTaskExecutionUncertain = errors.New("media task execution is uncertain; automatic retry is blocked")

func costlyTaskOperation(name string) bool {
	return name == "generate_image" || name == "grok_generate_image" || name == "grok_generate_video"
}

func persistentOperationID(runID, name string, step int, encoded []byte) string {
	if costlyTaskOperation(name) {
		kind := "image"
		if name == "grok_generate_video" {
			kind = "video"
		}
		return runID + ":media:" + kind
	}
	return taskStepID(runID, "tool", step, name, string(encoded))
}

func (a *AgentRuntime) existingCostlyOperationID(runID, name, fallback string) (string, error) {
	if !costlyTaskOperation(name) {
		return fallback, nil
	}
	query := `SELECT id FROM agent_task_steps WHERE run_id=? AND name IN ('generate_image','grok_generate_image') ORDER BY rowid LIMIT 1`
	if name == "grok_generate_video" {
		query = `SELECT id FROM agent_task_steps WHERE run_id=? AND name='grok_generate_video' ORDER BY rowid LIMIT 1`
	}
	var id string
	err := a.db.QueryRowContext(a.taskContext(), query, runID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	return id, err
}

func (a *AgentRuntime) beginPersistentOperation(runID, parentID, name string, step int, input any) (string, error) {
	if !costlyTaskOperation(name) {
		return a.beginTaskStep(runID, parentID, "tool", name, step, input)
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", err
	}
	id := persistentOperationID(runID, name, step, encoded)
	id, err = a.existingCostlyOperationID(runID, name, id)
	if err != nil {
		return "", err
	}
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return "", err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = a.db.ExecContext(a.taskContext(), `INSERT INTO agent_task_steps
		(id,run_id,parent_step_id,step_index,kind,name,status,input_cipher,attempts,started_at,created_at,updated_at)
		VALUES (?,?,?,?,'tool',?,'running',?,1,?,?,?) ON CONFLICT(id) DO UPDATE
		SET status='running',attempts=agent_task_steps.attempts+1,updated_at=excluded.updated_at`,
		id, runID, nullable(parentID), step, name, ciphertext, now, now, now)
	return id, err
}

func (a *AgentRuntime) claimTaskOperation(id string) (func(), bool) {
	a.taskOperationMu.Lock()
	defer a.taskOperationMu.Unlock()
	if a.taskOperations == nil {
		a.taskOperations = map[string]bool{}
	}
	if a.taskOperations[id] {
		return func() {}, false
	}
	a.taskOperations[id] = true
	return func() { a.taskOperationMu.Lock(); delete(a.taskOperations, id); a.taskOperationMu.Unlock() }, true
}

func persistentToolInput(call chatToolCall) map[string]string {
	input := map[string]string{"name": call.Function.Name, "arguments": call.Function.Arguments}
	if !costlyTaskOperation(call.Function.Name) {
		input["callId"] = call.ID
	} else {
		var arguments any
		if json.Unmarshal([]byte(call.Function.Arguments), &arguments) == nil {
			if normalized, err := json.Marshal(arguments); err == nil {
				input["arguments"] = string(normalized)
			}
		}
	}
	return input
}

func (a *AgentRuntime) uncertainTaskOperation(id string) error {
	var status, code string
	err := a.db.QueryRowContext(a.taskContext(), "SELECT status,error_code FROM agent_task_steps WHERE id=?", id).Scan(&status, &code)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "succeeded" {
		return errors.New("completed media receipt is unreadable; retry is blocked")
	}
	if status == "running" || code == "task_execution_uncertain" {
		var checkpoint int
		if err = a.db.QueryRowContext(a.taskContext(), `SELECT count(*) FROM agent_task_steps checkpoint
			WHERE checkpoint.run_id=(SELECT run_id FROM agent_task_steps WHERE id=?)
			AND (checkpoint.name LIKE 'media_quality:%' OR checkpoint.name LIKE 'media_generation:%')
			AND length(checkpoint.output_cipher)>0`, id).Scan(&checkpoint); err != nil {
			return err
		}
		// Inner media checkpoint handlers either resume the accepted task or
		// reject an unknown acceptance; neither blindly creates another task.
		if checkpoint == 0 {
			return errTaskExecutionUncertain
		}
	}
	return nil
}

func (a *AgentRuntime) persistTaskArtifacts(runID, stepID string, artifacts []agentAttachment) error {
	for _, artifact := range artifacts {
		if _, err := a.db.ExecContext(a.taskContext(), `INSERT OR IGNORE INTO agent_task_artifacts
			(run_id, step_id, kind, name, local_path, mime_type, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			runID, stepID, artifact.Kind, artifact.Name, artifact.LocalPath, artifact.MimeType,
			time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
		_ = a.recordRunStage(runID, "media_attached", time.Now(), map[string]any{"kind": artifact.Kind, "name": artifact.Name})
	}
	return nil
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
	if costlyTaskOperation(call.Function.Name) {
		if err := a.ensureToolTaskIntent(ctx, run, message); err != nil {
			return taskPersistenceFailure()
		}
	}
	if err := a.ensureMediaTaskCurrent(ctx, run); err != nil {
		return toolResult{Content: `{"ok":false,"error":"task_revision_superseded"}`}
	}
	if !a.taskGraphRunExists(run.ID) {
		return a.executeToolCall(ctx, run, message, policy, mcpRoutes, call)
	}
	input := persistentToolInput(call)
	if costlyTaskOperation(call.Function.Name) {
		step = 0
	}
	encoded, _ := json.Marshal(input)
	id := persistentOperationID(run.ID, call.Function.Name, step, encoded)
	var lookupErr error
	id, lookupErr = a.existingCostlyOperationID(run.ID, call.Function.Name, id)
	if lookupErr != nil {
		return taskPersistenceFailure()
	}
	if costlyTaskOperation(call.Function.Name) {
		release, claimed := a.claimTaskOperation(id)
		if !claimed {
			return toolResult{Content: `{"ok":false,"error":"task_execution_in_progress"}`}
		}
		defer release()
	}
	if result, found := a.cachedTaskToolResult(id); found {
		if err := a.persistTaskArtifacts(run.ID, id, result.Attachments); err != nil {
			return taskPersistenceFailure()
		}
		return result
	}
	if costlyTaskOperation(call.Function.Name) {
		if err := a.uncertainTaskOperation(id); err != nil {
			return toolResult{Content: `{"ok":false,"error":"task_execution_uncertain"}`}
		}
	}
	id, err := a.beginPersistentOperation(run.ID, parentID, call.Function.Name, step, input)
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
	if err = a.finishTaskStep(id, status, errorCode, result); err != nil {
		return taskPersistenceFailure()
	}
	if err = a.persistTaskArtifacts(run.ID, id, result.Attachments); err != nil {
		return taskPersistenceFailure()
	}
	return result
}

func (a *AgentRuntime) executePersistentOperation(
	run runRecord,
	name string,
	input any,
	operation func() (toolResult, error),
) (toolResult, error) {
	if err := a.ensureMediaTaskCurrent(a.taskContext(), run); err != nil {
		return toolResult{}, err
	}
	if !a.taskGraphRunExists(run.ID) {
		return operation()
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return toolResult{}, err
	}
	id := persistentOperationID(run.ID, name, 0, encoded)
	id, err = a.existingCostlyOperationID(run.ID, name, id)
	if err != nil {
		return toolResult{}, err
	}
	if costlyTaskOperation(name) {
		release, claimed := a.claimTaskOperation(id)
		if !claimed {
			return toolResult{}, errTaskExecutionUncertain
		}
		defer release()
	}
	if result, found := a.cachedTaskToolResult(id); found {
		return result, a.persistTaskArtifacts(run.ID, id, result.Attachments)
	}
	if costlyTaskOperation(name) {
		if err := a.uncertainTaskOperation(id); err != nil {
			return toolResult{}, err
		}
	}
	id, err = a.beginPersistentOperation(run.ID, "", name, 0, input)
	if err != nil {
		return toolResult{}, err
	}
	result, operationErr := operation()
	if operationErr != nil {
		if receiptErr := a.finishTaskStep(id, "failed", "tool_execution_failed", nil); receiptErr != nil {
			return toolResult{}, errors.Join(operationErr, receiptErr)
		}
		return result, operationErr
	}
	if err = a.finishTaskStep(id, "succeeded", "", result); err != nil {
		return toolResult{}, err
	}
	return result, a.persistTaskArtifacts(run.ID, id, result.Attachments)
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
	rows, err = a.db.QueryContext(ctx, `SELECT id, step_id, kind, name, mime_type, created_at
		FROM agent_task_artifacts WHERE id IN (
			SELECT min(id) FROM agent_task_artifacts WHERE run_id = ? GROUP BY kind, local_path
		) ORDER BY id`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var artifact taskArtifactView
		if err = rows.Scan(&artifact.ID, &artifact.StepID, &artifact.Kind, &artifact.Name, &artifact.MimeType, &artifact.CreatedAt); err != nil {
			return nil, err
		}
		if artifact.Kind == "image" || artifact.Kind == "video" {
			artifact.ContentURL = "/api/v1/tasks/" + url.PathEscape(runID) + "/artifacts/" + strconv.FormatInt(artifact.ID, 10)
		}
		artifacts = append(artifacts, artifact)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	optimization, err := a.taskOptimizationDetails(ctx, runID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"runId": runID, "steps": steps, "artifacts": artifacts, "optimization": optimization}, nil
}

func (a *AgentRuntime) handleTaskGraphManagement(w http.ResponseWriter, r *http.Request, path string) error {
	if parts := strings.Split(strings.TrimPrefix(path, "/api/v1/tasks/"), "/"); len(parts) == 3 && parts[0] != "" && parts[1] == "artifacts" {
		return a.handleTaskArtifact(w, r, parts[0], parts[2])
	}
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
	if strings.HasSuffix(path, "/feedback") {
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/api/v1/tasks/"), "/feedback")
		if id == "" || strings.Contains(id, "/") {
			return mgmtNotFound("task")
		}
		return a.handleTaskFeedback(w, r, id)
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
	var retired bool
	if err = tx.QueryRowContext(ctx, `SELECT state, input_cipher, EXISTS(SELECT 1 FROM agent_task_steps s
		WHERE s.id=r.id || ':intent' AND s.error_code='task_intent_expired') FROM agent_runs r WHERE id = ?`, runID).
		Scan(&state, &input, &retired); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("task")
	} else if err != nil {
		return err
	}
	if retired {
		return coreInvalid("task intent retention expired; submit a new task")
	}
	if len(input) == 0 {
		return coreInvalid("task input is no longer available")
	}
	if state != "failed" && state != "cancelled" {
		return coreInvalid("only failed or cancelled tasks can be retried")
	}
	var uncertain int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_task_steps WHERE run_id=? AND
		(error_code='task_execution_uncertain' OR (status='running' AND name IN ('generate_image','grok_generate_image','grok_generate_video')))`, runID).Scan(&uncertain); err != nil {
		return err
	}
	if uncertain > 0 {
		var checkpoint int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_task_steps WHERE run_id=?
			AND (name LIKE 'media_quality:%' OR name LIKE 'media_generation:%') AND length(output_cipher)>0`, runID).Scan(&checkpoint); err != nil {
			return err
		}
		if checkpoint == 0 {
			return coreInvalid("media task outcome is uncertain; automatic retry is blocked")
		}
	}
	var superseded int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM agent_runs r WHERE id=? AND task_id<>'' AND EXISTS
		(SELECT 1 FROM agent_runs newer WHERE newer.task_id=r.task_id AND newer.task_scope_key=r.task_scope_key AND newer.task_revision>r.task_revision)`, runID).Scan(&superseded); err != nil {
		return err
	}
	if superseded > 0 {
		return coreInvalid("a newer task revision exists")
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET state = 'queued', error_code = NULL, updated_at = ? WHERE id = ?`, now, runID); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_task_steps SET status = 'pending', output_cipher = NULL,
		error_code = '', started_at = NULL, finished_at = NULL, updated_at = ?
		WHERE run_id = ? AND status IN ('failed', 'cancelled', 'running', 'pending')
		AND name NOT LIKE 'media_quality:%' AND name NOT LIKE 'media_generation:%'`, now, runID); err != nil {
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
