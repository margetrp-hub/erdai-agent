package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"database/sql"
	"testing"
)

func TestModelCheckpointRecoveryFromReopenedDatabase(t *testing.T) {
	f := newModelCheckpointFixture(t)
	if _, err := f.execute(f.policy, runtimeMessagePolicy{}); err == nil {
		t.Fatal("injected continuation failure did not stop the first runtime")
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 2 || plans != 1 {
		t.Fatalf("initial work: calls=%d plans=%d", calls, plans)
	}
	oldRuntime := f.runtime
	var databasePath, configurationPath, savedStepID string
	var savedPlan []byte
	if err := oldRuntime.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil || databasePath == "" {
		t.Fatalf("runtime database path=%q err=%v", databasePath, err)
	}
	if err := oldRuntime.configStore.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&configurationPath); err != nil || configurationPath == "" {
		t.Fatalf("configuration database path=%q err=%v", configurationPath, err)
	}
	if err := oldRuntime.db.QueryRow(`SELECT id,output_cipher FROM agent_task_steps
		WHERE run_id=? AND kind='model' AND status='succeeded'`, f.run.ID).Scan(&savedStepID, &savedPlan); err != nil {
		t.Fatal(err)
	}
	client := oldRuntime.client
	if err := oldRuntime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := oldRuntime.db.Ping(); err == nil {
		t.Fatal("old runtime database connection remained open")
	}
	if err := oldRuntime.configStore.db.Ping(); err == nil {
		t.Fatal("old configuration database connection remained open")
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	configDB, err := sql.Open("sqlite", configurationPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = configDB.Close() })
	configDB.SetMaxOpenConns(1)
	if _, err = configDB.Exec(`PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000`); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{9}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	// Rebuild only this test's runtime dependencies without starting workers.
	// The database connections, locks, caches, encryption and memory store are new.
	fresh := &AgentRuntime{
		db: db, configStore: &coreConfigStore{db: configDB}, aead: aead, client: client,
		lifecycle: ctx, cancel: cancel, modelAPIKey: "model-test-key",
	}
	t.Cleanup(func() {
		if err := fresh.Close(); err != nil {
			t.Errorf("close reopened runtime: %v", err)
		}
	})
	identityKey := sha256.Sum256(append([]byte("erdai-identity-v1:"), key...))
	fresh.identitySecret = identityKey[:]
	fresh.memory, err = NewMemoryGroupStore(fresh, identityKey[:])
	if err != nil {
		t.Fatal(err)
	}
	if fresh == oldRuntime || fresh.db == oldRuntime.db || fresh.configStore.db == oldRuntime.configStore.db || fresh.taskOperations != nil {
		t.Fatal("reopened runtime reused the original instance or connection state")
	}
	if err := recoverInterruptedRuntime(ctx, db); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM agent_runs WHERE id=?`, f.run.ID).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("reopened recovery state=%q err=%v", state, err)
	}
	f.runtime = fresh
	f.allowCompletion.Store(true)
	reply, err := f.execute(f.policy, runtimeMessagePolicy{})
	if err != nil || reply.Text != "Done." {
		t.Fatalf("reopened recovery reply=%+v err=%v", reply, err)
	}
	if calls, plans := f.modelCalls.Load(), f.plans.Load(); calls != 3 || plans != 1 {
		t.Errorf("reopened recovery repeated model plan: calls=%d plans=%d", calls, plans)
	}
	if got := f.lastToolCallID.Load(); got != "recall-1" {
		t.Errorf("reopened recovery lost original tool call ID: %v", got)
	}
	var restoredPlan []byte
	var modelAttempts, executions, toolAttempts int
	if err := db.QueryRow(`SELECT output_cipher,attempts FROM agent_task_steps WHERE id=? AND status='succeeded'`, savedStepID).Scan(&restoredPlan, &modelAttempts); err != nil || modelAttempts != 1 || !bytes.Equal(savedPlan, restoredPlan) {
		t.Errorf("reopened recovery replaced saved plan: attempts=%d equal=%v err=%v", modelAttempts, bytes.Equal(savedPlan, restoredPlan), err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM run_stage_events WHERE run_id=? AND stage='memory_recall'`, f.run.ID).Scan(&executions); err != nil || executions != 1 {
		t.Errorf("reopened recovery repeated tool: executions=%d err=%v", executions, err)
	}
	if err := db.QueryRow(`SELECT COALESCE(sum(attempts),0) FROM agent_task_steps WHERE run_id=? AND kind='tool' AND status='succeeded'`, f.run.ID).Scan(&toolAttempts); err != nil || toolAttempts != 1 {
		t.Errorf("reopened successful tool attempts=%d err=%v", toolAttempts, err)
	}
}
