package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckSQLiteIntegrity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "healthy.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("CREATE TABLE check_value (id INTEGER PRIMARY KEY)"); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	if err = checkSQLiteIntegrity(path); err != nil {
		t.Fatalf("healthy database failed integrity check: %v", err)
	}
}

func TestCheckSQLiteIntegrityDoesNotCreateMissingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.sqlite3")
	if err := checkSQLiteIntegrity(path); err == nil {
		t.Fatal("missing database passed integrity check")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("read-only integrity check created the database: %v", err)
	}
}

func TestHandleCLIRejectsUnknownArguments(t *testing.T) {
	handled, err := handleCLI([]string{"--unknown"})
	if !handled || err == nil {
		t.Fatalf("handled=%v err=%v", handled, err)
	}
}
