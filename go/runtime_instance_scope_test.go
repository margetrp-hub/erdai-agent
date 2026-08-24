package main

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func TestRecentAttachmentScopeMigrationMovesLegacyRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE agent_recent_attachments (
		transport TEXT NOT NULL, conversation_ref TEXT NOT NULL, attachments_cipher BLOB NOT NULL,
		expires_at TEXT NOT NULL, updated_at TEXT NOT NULL, PRIMARY KEY (transport, conversation_ref)
	); INSERT INTO agent_recent_attachments VALUES ('qq', 'conversation-1', X'01', 'future', 'now')`); err != nil {
		t.Fatal(err)
	}
	if err = migrateRecentAttachmentScopes(db); err != nil {
		t.Fatal(err)
	}
	var instanceID, transportInstance, conversation string
	var cipher []byte
	if err = db.QueryRow(`SELECT agent_instance_id, transport_instance, conversation_ref, attachments_cipher
		FROM agent_recent_attachments`).Scan(&instanceID, &transportInstance, &conversation, &cipher); err != nil {
		t.Fatal(err)
	}
	if instanceID != "legacy-default" || transportInstance != "legacy-default" || conversation != "conversation-1" || len(cipher) != 1 {
		t.Fatalf("migrated attachment scope = %q/%q/%q/%x", instanceID, transportInstance, conversation, cipher)
	}
}

func TestSearchEntityLegacyColumnsDefaultToLegacyScope(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE agent_search_entities (
		id INTEGER PRIMARY KEY AUTOINCREMENT, conversation_ref TEXT NOT NULL, sender_ref TEXT NOT NULL,
		thread_key TEXT NOT NULL DEFAULT '', entity_hint TEXT NOT NULL, query TEXT NOT NULL,
		sources_json TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL
	); INSERT INTO agent_search_entities(conversation_ref, sender_ref, entity_hint, query, created_at)
		VALUES ('conversation-1', 'sender-1', 'entity', '', 'now')`); err != nil {
		t.Fatal(err)
	}
	for _, column := range []struct{ name, definition string }{
		{"agent_instance_id", "TEXT NOT NULL DEFAULT 'legacy-default'"},
		{"transport", "TEXT NOT NULL DEFAULT 'legacy'"},
		{"transport_instance", "TEXT NOT NULL DEFAULT 'legacy-default'"},
	} {
		if err = ensureRuntimeColumn(db, "agent_search_entities", column.name, column.definition); err != nil {
			t.Fatal(err)
		}
	}
	var instanceID, transport, transportInstance string
	if err = db.QueryRow("SELECT agent_instance_id, transport, transport_instance FROM agent_search_entities").Scan(&instanceID, &transport, &transportInstance); err != nil {
		t.Fatal(err)
	}
	if instanceID != "legacy-default" || transport != "legacy" || transportInstance != "legacy-default" {
		t.Fatalf("legacy search scope = %q/%q/%q", instanceID, transport, transportInstance)
	}
}
