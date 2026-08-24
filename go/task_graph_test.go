package main

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"
)

func TestPersistentTaskGraphCachesSuccessfulOperation(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, transport, reply_handle, conversation_ref, sender_ref, input_cipher,
		 is_admin, state, created_at, updated_at) VALUES ('task-run', 'task-event', 'test', 'reply', 'group', 'member', ?, 0, 'running', ?, ?)`, []byte("input"), now, now); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	operation := func() (toolResult, error) {
		calls.Add(1)
		return toolResult{Content: `{"ok":true,"result":"done"}`}, nil
	}
	run := runRecord{ID: "task-run"}
	first, err := runtime.executePersistentOperation(run, "echo", map[string]string{"value": "one"}, operation)
	if err != nil || first.Content == "" {
		t.Fatalf("first operation = %#v, err=%v", first, err)
	}
	second, err := runtime.executePersistentOperation(run, "echo", map[string]string{"value": "one"}, operation)
	if err != nil || second.Content != first.Content || calls.Load() != 1 {
		t.Fatalf("cached operation = %#v, err=%v, calls=%d", second, err, calls.Load())
	}
	var count int
	if err = runtime.db.QueryRow("SELECT count(*) FROM agent_task_steps WHERE run_id = 'task-run'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("task step count = %d, err=%v", count, err)
	}
	var output []byte
	if err = runtime.db.QueryRow("SELECT output_cipher FROM agent_task_steps WHERE run_id = 'task-run'").Scan(&output); err != nil || len(output) == 0 {
		t.Fatalf("encrypted task output missing: %v", err)
	}
	var decoded toolResult
	plain, decryptErr := runtime.decrypt(output)
	if decryptErr != nil || json.Unmarshal(plain, &decoded) != nil || decoded.Content != first.Content {
		t.Fatalf("task output could not be recovered: %v", decryptErr)
	}
}

func TestPersistentTaskGraphRetryRequeuesFailedRun(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	input, err := runtime.encrypt([]byte("retry this task"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec(`INSERT INTO agent_runs
		(id, event_id, transport, reply_handle, conversation_ref, sender_ref, input_cipher,
		 is_admin, state, error_code, created_at, updated_at)
		VALUES ('retry-run', 'retry-event', 'test', 'reply', 'group', 'member', ?, 0, 'failed', 'tool_failed', ?, ?)`, input, now, now); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec(`INSERT INTO agent_task_steps
		(id, run_id, step_index, kind, name, status, attempts, error_code, created_at, updated_at)
		VALUES ('retry-step', 'retry-run', 0, 'tool', 'echo', 'failed', 1, 'tool_failed', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err = runtime.retryTask(context.Background(), "retry-run"); err != nil {
		t.Fatal(err)
	}
	var state, errorCode, stepStatus string
	if err = runtime.db.QueryRow("SELECT state, COALESCE(error_code, '') FROM agent_runs WHERE id = 'retry-run'").
		Scan(&state, &errorCode); err != nil {
		t.Fatal(err)
	}
	if err = runtime.db.QueryRow("SELECT status FROM agent_task_steps WHERE id = 'retry-step'").Scan(&stepStatus); err != nil {
		t.Fatal(err)
	}
	if (state != "queued" && state != "running") || errorCode != "" || (stepStatus != "pending" && stepStatus != "running") {
		t.Fatalf("retry state = %q, error = %q, step = %q", state, errorCode, stepStatus)
	}
}
