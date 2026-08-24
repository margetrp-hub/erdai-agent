package main

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func unifiedRuntimeConfig(databasePath, configPath, legacyPath string) RuntimeConfig {
	return RuntimeConfig{
		DatabasePath:              databasePath,
		ConfigDatabasePath:        configPath,
		LegacyRuntimeDatabasePath: legacyPath,
		AdminToken:                "admin-test-token",
		RuntimeToken:              testRuntimeToken,
		ModelAPIKey:               "model-test-key",
		EncryptionKey:             base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32)),
	}
}

func seedLegacyRuntimeRows(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	statements := []string{
		`INSERT INTO agent_runs (id, event_id, transport, reply_handle, conversation_ref, sender_ref, input_cipher, state, created_at, updated_at)
		 VALUES ('run-1', 'event-1', 'qq_official', 'reply-1', 'group-1', 'member-1', X'01', 'running', ?, ?)`,
		`INSERT INTO agent_deliveries (id, run_id, reply_handle, payload_json, status, created_at, updated_at)
		 VALUES ('delivery-1', 'run-1', 'reply-1', '{}', 'sent', ?, ?)`,
		`INSERT INTO platform_reply_routes (reply_handle, event_id, route_cipher, created_at, updated_at)
		 VALUES ('reply-1', 'event-1', X'02', ?, ?)`,
		`INSERT INTO platform_sent_deliveries (delivery_id, sent_at) VALUES ('delivery-1', ?)`,
		`INSERT INTO agent_memories (id, scope_digest, content_cipher, content_digest, created_at, updated_at)
		 VALUES ('memory-1', X'03', X'04', X'05', ?, ?)`,
		`INSERT INTO conversation_events (id, conversation_digest, sender_digest, role, text_cipher, occurred_at, expires_at)
		 VALUES ('conversation-event-1', X'06', X'07', 'user', X'08', ?, ?)`,
		`INSERT INTO conversation_state (conversation_digest, last_bot_reply_ack_at, updated_at)
		 VALUES (X'06', ?, ?)`,
		`INSERT INTO relationship_events (event_id, conversation_digest, sender_digest, occurred_at)
		 VALUES ('relationship-event-1', X'06', X'07', ?)`,
		`INSERT INTO member_relationship_state (conversation_digest, sender_digest, interaction_count, addressed_count, last_interaction_at, updated_at)
		 VALUES (X'06', X'07', 2, 1, ?, ?)`,
		`UPDATE media_quota_config SET image_daily_limit = 7, video_daily_limit = 5, updated_at = ? WHERE singleton = 1`,
		`INSERT INTO media_quota_usage (subject_digest, usage_day, media_kind, used_count, reserved_count, updated_at)
		 VALUES (X'09', '2026-08-05', 'image', 1, 1, ?)`,
	}
	for _, statement := range statements {
		arguments := make([]any, 0, 2)
		for index := 0; index < len(statement); index++ {
			if statement[index] == '?' {
				arguments = append(arguments, now)
			}
		}
		if _, err = db.Exec(statement, arguments...); err != nil {
			t.Fatalf("seed legacy runtime row: %v\n%s", err, statement)
		}
	}
}

func TestLegacyRuntimeDatabaseMergesIntoCoreOnce(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "erdai-runtime.sqlite3")
	legacyConfigPath := filepath.Join(directory, "legacy-core.sqlite3")
	legacy, err := NewAgentRuntime(unifiedRuntimeConfig(legacyPath, legacyConfigPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	seedLegacyRuntimeRows(t, legacyPath)

	unifiedPath := filepath.Join(directory, "erdai-agent-core.sqlite3")
	store, err := openCoreConfigStore(unifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewAgentRuntime(unifiedRuntimeConfig(unifiedPath, unifiedPath, legacyPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = runtime.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", unifiedPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, table := range unifiedRuntimeTables {
		var count int
		if err = db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("unified %s count = %d, err = %v", table, count, err)
		}
	}
	var coreTables, marker, imageLimit, videoLimit, reserved int
	if err = db.QueryRow("SELECT count(*) FROM personas").Scan(&coreTables); err != nil || coreTables == 0 {
		t.Fatalf("Core tables unavailable from unified database: count=%d err=%v", coreTables, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM runtime_database_migrations WHERE id = ?", unifiedRuntimeMigrationID).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("migration marker = %d, err = %v", marker, err)
	}
	if err = db.QueryRow("SELECT image_daily_limit, video_daily_limit FROM media_quota_config WHERE singleton = 1").Scan(&imageLimit, &videoLimit); err != nil || imageLimit != 7 || videoLimit != 5 {
		t.Fatalf("media quota config = %d/%d, err = %v", imageLimit, videoLimit, err)
	}
	if err = db.QueryRow("SELECT reserved_count FROM media_quota_usage").Scan(&reserved); err != nil || reserved != 0 {
		t.Fatalf("imported media reservation = %d, err = %v", reserved, err)
	}

	reopened, err := NewAgentRuntime(unifiedRuntimeConfig(unifiedPath, unifiedPath, legacyPath))
	if err != nil {
		t.Fatal(err)
	}
	if err = reopened.Close(); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err = db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("idempotent reopen changed runs to %d: %v", runs, err)
	}
}

func TestLegacyRuntimeDatabaseMapsReorderedProductionColumns(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "legacy-reordered.sqlite3")
	legacy, err := sql.Open("sqlite", legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = legacy.Exec(`
		CREATE TABLE agent_runs (
			id TEXT PRIMARY KEY, event_id TEXT NOT NULL UNIQUE, reply_handle TEXT NOT NULL,
			conversation_ref TEXT NOT NULL, sender_ref TEXT NOT NULL, input_cipher BLOB,
			state TEXT NOT NULL, error_code TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL,
			transport TEXT NOT NULL DEFAULT 'qq_official'
		);
		CREATE TABLE agent_deliveries (
			id TEXT PRIMARY KEY, run_id TEXT NOT NULL, reply_handle TEXT NOT NULL,
			payload_json TEXT NOT NULL, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO agent_runs
			(id, event_id, reply_handle, conversation_ref, sender_ref, input_cipher, state, created_at, updated_at, transport)
		VALUES ('run-old', 'event-old', 'reply-old', 'group-old', '', X'0102', 'delivered',
			'2026-08-05T00:00:00Z', '2026-08-05T00:00:00Z', 'qq_official');
		INSERT INTO agent_deliveries
			(id, run_id, reply_handle, payload_json, status, created_at, updated_at)
		VALUES ('delivery-old', 'run-old', 'reply-old', '{}', 'sent',
			'2026-08-05T00:00:00Z', '2026-08-05T00:00:00Z');
	`)
	if closeErr := legacy.Close(); err != nil || closeErr != nil {
		t.Fatalf("seed reordered legacy database: exec=%v close=%v", err, closeErr)
	}

	unifiedPath := filepath.Join(directory, "erdai-agent-core.sqlite3")
	runtime, err := NewAgentRuntime(unifiedRuntimeConfig(unifiedPath, unifiedPath, legacyPath))
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()

	var transport, replyHandle, conversationRef, senderRef, phase string
	var isAdmin int
	var input []byte
	if err = runtime.db.QueryRow(`SELECT transport, reply_handle, conversation_ref, sender_ref, input_cipher, is_admin
		FROM agent_runs WHERE id = 'run-old'`).Scan(
		&transport, &replyHandle, &conversationRef, &senderRef, &input, &isAdmin,
	); err != nil {
		t.Fatal(err)
	}
	if transport != "qq_official" || replyHandle != "reply-old" || conversationRef != "group-old" ||
		senderRef != "legacy:unknown" || !bytes.Equal(input, []byte{1, 2}) || isAdmin != 0 {
		t.Fatalf("mapped run = transport=%q reply=%q conversation=%q sender=%q input=%x admin=%d",
			transport, replyHandle, conversationRef, senderRef, input, isAdmin)
	}
	if err = runtime.db.QueryRow("SELECT phase FROM agent_deliveries WHERE id = 'delivery-old'").Scan(&phase); err != nil {
		t.Fatal(err)
	}
	if phase != "terminal" {
		t.Fatalf("delivery phase = %q", phase)
	}
}

func TestLegacyRuntimeDatabaseRejectsNonemptyUnifiedTarget(t *testing.T) {
	directory := t.TempDir()
	legacyPath := filepath.Join(directory, "legacy.sqlite3")
	targetPath := filepath.Join(directory, "core.sqlite3")
	legacyConfigPath := filepath.Join(directory, "legacy-config.sqlite3")

	legacy, err := NewAgentRuntime(unifiedRuntimeConfig(legacyPath, legacyConfigPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	if err = legacy.Close(); err != nil {
		t.Fatal(err)
	}
	seedLegacyRuntimeRows(t, legacyPath)

	target, err := NewAgentRuntime(unifiedRuntimeConfig(targetPath, targetPath, ""))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = target.db.Exec(`INSERT INTO agent_runs
		(id, event_id, reply_handle, conversation_ref, sender_ref, state, created_at, updated_at)
		VALUES ('target-run', 'target-event', 'target-reply', 'target-group', 'target-member', 'delivered', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err = target.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err = NewAgentRuntime(unifiedRuntimeConfig(targetPath, targetPath, legacyPath)); err == nil {
		t.Fatal("migration accepted a nonempty unified Runtime target")
	}
	db, openErr := sql.Open("sqlite", targetPath)
	if openErr != nil {
		t.Fatal(openErr)
	}
	defer db.Close()
	var runs, marker int
	if err = db.QueryRow("SELECT count(*) FROM agent_runs").Scan(&runs); err != nil || runs != 1 {
		t.Fatalf("target data changed after rejected migration: runs=%d err=%v", runs, err)
	}
	if err = db.QueryRow("SELECT count(*) FROM runtime_database_migrations WHERE id = ?", unifiedRuntimeMigrationID).Scan(&marker); err != nil || marker != 0 {
		t.Fatalf("rejected migration marker = %d, err=%v", marker, err)
	}
}
