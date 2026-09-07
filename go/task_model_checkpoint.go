package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const taskModelCheckpointVersion = 1

var errTaskModelCheckpoint = errors.New("task model checkpoint is incompatible or unreadable; submit a new task")
var errTaskPlanPersistence = errors.New("task execution record could not be persisted or restored")

type taskModelInput struct {
	Version  int            `json:"version"`
	Contract string         `json:"contract"`
	History  string         `json:"history"`
	Request  map[string]any `json:"request"`
}

type taskModelOutput struct {
	Version    int            `json:"version"`
	Completion chatCompletion `json:"completion"`
	EndpointID string         `json:"endpointId"`
	Model      string         `json:"model"`
	APIBase    string         `json:"apiBase"`
}

func taskModelDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (a *AgentRuntime) persistentModelRun(ctx context.Context, runID string) (bool, error) {
	var exists int
	err := a.db.QueryRowContext(ctx, "SELECT 1 FROM agent_runs WHERE id=?", runID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Read before beginTaskStep: its upsert intentionally resets the attempt state.
// The existing step index is the model cursor; original tool IDs retain receipts.
func (a *AgentRuntime) prepareTaskModelStep(ctx context.Context, run runRecord, step int, model string,
	input taskModelInput, targets []runtimeProviderTarget,
) (string, chatCompletion, int, bool, error) {
	if err := a.ensureMediaTaskCurrent(ctx, run); err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id,status,input_cipher,output_cipher FROM agent_task_steps
		WHERE run_id=? AND kind='model' AND step_index=? ORDER BY rowid LIMIT 2`, run.ID, step)
	if err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	var id, status string
	var inputCipher, outputCipher []byte
	count := 0
	for rows.Next() {
		count++
		if err = rows.Scan(&id, &status, &inputCipher, &outputCipher); err != nil {
			break
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	if count == 0 {
		id, err = a.beginTaskStep(run.ID, "", "model", model, step, input)
		return id, chatCompletion{}, 0, false, err
	}
	if count != 1 {
		return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
	}
	plain, err := a.decrypt(inputCipher)
	var saved taskModelInput
	if err != nil || json.Unmarshal(plain, &saved) != nil || saved.Version != taskModelCheckpointVersion ||
		saved.Contract != input.Contract {
		return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
	}
	if status == "succeeded" {
		var output taskModelOutput
		plain, err = a.decrypt(outputCipher)
		if err != nil || json.Unmarshal(plain, &output) != nil || output.Version != taskModelCheckpointVersion ||
			saved.History != input.History || len(output.Completion.Choices) == 0 {
			return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
		}
		for index, target := range targets {
			if target.EndpointID == output.EndpointID && target.Model == output.Model && target.APIBase == output.APIBase {
				return id, output.Completion, index, true, nil
			}
		}
		return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
	}
	if status != "pending" && status != "failed" && status != "running" {
		return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := a.db.ExecContext(ctx, `UPDATE agent_task_steps SET status='running', input_cipher=?,
		output_cipher=NULL, attempts=attempts+1, started_at=?, finished_at=NULL, error_code='', updated_at=?
		WHERE id=? AND status=?`, ciphertext, now, now, id, status)
	if err != nil {
		return "", chatCompletion{}, 0, false, err
	}
	if affected, err := result.RowsAffected(); err != nil || affected != 1 {
		return "", chatCompletion{}, 0, false, errTaskModelCheckpoint
	}
	return id, chatCompletion{}, 0, false, nil
}

func (a *AgentRuntime) finishTaskModelStep(ctx context.Context, run runRecord, id string,
	completion chatCompletion, target runtimeProviderTarget,
) error {
	if err := a.ensureMediaTaskCurrent(ctx, run); err != nil {
		return err
	}
	if err := a.finishTaskStep(id, "succeeded", "", taskModelOutput{
		Version: taskModelCheckpointVersion, Completion: completion,
		EndpointID: target.EndpointID, Model: target.Model, APIBase: target.APIBase,
	}); err != nil {
		return fmt.Errorf("%w: %v", errTaskPlanPersistence, err)
	}
	return nil
}
