package main

import (
	"context"
	"database/sql"
	"time"
)

// Run after legacy database import, before starting workers. Moving receipts
// and resetting interrupted work in one transaction prevents old reservations
// from being resurrected on the next restart.
func recoverInterruptedRuntime(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `INSERT OR IGNORE INTO agent_search_queries
		(run_id, query_hash, status, result_cipher, sources_cipher, error_message, created_at, updated_at)
		SELECT run_id, query_hash, status, result_cipher, sources_cipher, error_message, created_at, updated_at
		FROM agent_search_runs`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_search_runs`); err != nil {
		return err
	}
	// Completed caches and failures remain deduplicated within an attempt.
	// Only an explicit task retry may clear a completed failure.
	if _, err = tx.ExecContext(ctx, `DELETE FROM agent_search_queries WHERE status = 'running'`); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `UPDATE agent_runs SET state = 'queued', updated_at = ?
		WHERE state = 'running' AND input_cipher IS NOT NULL`, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE agent_task_steps SET status = 'pending', updated_at = ?
		WHERE status = 'running'`, now); err != nil {
		return err
	}
	return tx.Commit()
}
