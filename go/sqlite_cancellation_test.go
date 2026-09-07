package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSQLiteQueryCancellationReleasesStatementsAndFile(t *testing.T) {
	ctx, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	path := filepath.Join(t.TempDir(), "cancellation.sqlite3")
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	conn, err := database.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	var mode string
	if err := conn.QueryRowContext(ctx, "PRAGMA journal_mode=WAL").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("enable WAL: mode=%q err=%v", mode, err)
	}
	// Long column names widen result metadata construction without expensive query execution.
	columns := make([]string, 256)
	for index := range columns {
		columns[index] = fmt.Sprintf("c%03d_%s INTEGER DEFAULT 0", index, strings.Repeat("metadata", 64))
	}
	if _, err := conn.ExecContext(ctx, "CREATE TABLE cancellation_probe ("+strings.Join(columns, ",")+")"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "INSERT INTO cancellation_probe DEFAULT VALUES"); err != nil {
		t.Fatal(err)
	}
	const query = "SELECT * FROM cancellation_probe"

	// An active result must prevent a checkpoint on this same connection. This
	// public-SQL probe catches native statement leaks on POSIX as well as Windows.
	calibration, err := conn.QueryContext(ctx, query)
	if err != nil {
		t.Fatal(err)
	}
	busy, _, _, checkpointErr := sqliteCancellationCheckpoint(ctx, conn)
	closeErr := calibration.Close()
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	if checkpointErr == nil && busy == 0 {
		t.Fatal("checkpoint probe did not detect deliberately open result rows")
	}
	if busy, _, _, err := sqliteCancellationCheckpoint(ctx, conn); err != nil || busy != 0 {
		t.Fatalf("checkpoint did not recover after closing calibration rows: busy=%d err=%v", busy, err)
	}

	preCanceled, cancel := context.WithCancel(ctx)
	cancel()
	rows, err := conn.QueryContext(preCanceled, query)
	if rows != nil {
		_ = rows.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled query did not honor cancellation: %v", err)
	}
	canceled := 1
	succeeded := 0
	const attempts = 250
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			t.Fatalf("cancellation regression exceeded its bounded context at attempt %d: %v", attempt, err)
		}
		queryCtx, cancelQuery := context.WithCancel(ctx)
		cancellationDone := make(chan struct{})
		go func(yields int) {
			defer close(cancellationDone)
			for index := 0; index < yields; index++ {
				runtime.Gosched()
			}
			cancelQuery()
		}((attempt % 32) * 16)
		rows, queryErr := conn.QueryContext(queryCtx, query)
		<-cancellationDone
		if rows != nil {
			if err := rows.Close(); err != nil && !sqliteCancellationExpectedError(err) {
				t.Fatalf("close returned rows at attempt %d: %v", attempt, err)
			}
		}
		if queryErr == nil {
			if rows == nil {
				t.Fatalf("successful query returned no rows handle at attempt %d", attempt)
			}
			succeeded++
		} else if sqliteCancellationExpectedError(queryErr) {
			canceled++
		} else {
			t.Fatalf("unexpected cancellation query error at attempt %d: %v", attempt, queryErr)
		}
		busy, logFrames, checkpointed, err := sqliteCancellationCheckpoint(ctx, conn)
		if err != nil || busy != 0 {
			t.Fatalf("canceled query retained native rows at attempt %d (canceled=%d succeeded=%d): checkpoint busy=%d log=%d checkpointed=%d err=%v", attempt, canceled, succeeded, busy, logFrames, checkpointed, err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("closed database retained its file after cancellation: %v; stats=%+v", err, database.Stats())
	}
	t.Logf("completed %d cancellation interleavings: canceled=%d succeeded=%d; checkpoints clean and closed file removed", attempts, canceled, succeeded)
}

func sqliteCancellationCheckpoint(ctx context.Context, conn *sql.Conn) (busy, logFrames, checkpointed int, err error) {
	err = conn.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logFrames, &checkpointed)
	return
}

func sqliteCancellationExpectedError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var sqliteError interface{ Code() int }
	return errors.As(err, &sqliteError) && sqliteError.Code()&0xff == 9 // SQLITE_INTERRUPT
}
