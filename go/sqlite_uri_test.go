package main

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadOnlySQLiteURIPreservesLocalPaths(t *testing.T) {
	paths := []string{
		filepath.Join(t.TempDir(), "space # percent% question? & plus+.sqlite3"),
		filepath.Join("relative directory", "nested", "..", "database.sqlite3"),
	}
	if runtime.GOOS == "windows" {
		paths = append(paths, `C:\data directory\reserved #50% & +.sqlite3`)
	} else {
		paths = append(paths, "/var/lib/erdai/data #50% ? & +.sqlite3", `/var/lib/erdai/literal\backslash.sqlite3`)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			dsn, err := readOnlySQLiteURI(path)
			if err != nil {
				t.Fatal(err)
			}
			parsed, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("parse URI %q: %v", dsn, err)
			}
			if parsed.Scheme != "file" || parsed.Host != "" || parsed.Opaque != "" || parsed.Fragment != "" ||
				parsed.Query().Get("mode") != "ro" || len(parsed.Query()) != 1 {
				t.Fatalf("local path became an authority or changed readonly options: %q", dsn)
			}
			absolute, err := filepath.Abs(filepath.Clean(path))
			if err != nil {
				t.Fatal(err)
			}
			want := filepath.ToSlash(absolute)
			if runtime.GOOS == "windows" {
				want = "/" + want
			}
			if parsed.Path != want || !strings.HasPrefix(dsn, "file:///") {
				t.Fatalf("URI %q decoded to %q, want native path %q", dsn, parsed.Path, want)
			}
		})
	}
}

func TestReadOnlySQLiteURIReadAndAttachStayReadOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space #50% & +.sqlite3")
	seedSQLiteURITestDatabase(t, path)
	dsn, err := readOnlySQLiteURI(path)
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var value string
	if err = database.QueryRow("SELECT value FROM uri_value").Scan(&value); err != nil || value != "original" {
		t.Fatalf("read special path: value=%q err=%v", value, err)
	}
	if _, err = database.Exec("INSERT INTO uri_value(value) VALUES ('unexpected')"); err == nil {
		t.Fatal("read-only URI permitted a write")
	}
	target, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	target.SetMaxOpenConns(1)
	if _, err = target.Exec("ATTACH DATABASE ? AS source_database", dsn); err != nil {
		t.Fatalf("attach special path: %v", err)
	}
	if err = target.QueryRow("SELECT value FROM source_database.uri_value").Scan(&value); err != nil || value != "original" {
		t.Fatalf("read attached special path: value=%q err=%v", value, err)
	}
	if _, err = target.Exec("INSERT INTO source_database.uri_value(value) VALUES ('unexpected')"); err == nil {
		t.Fatal("attached read-only URI permitted a write")
	}
}

func TestSQLiteReadOnlyConsumersHandleSpecialPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "space #50% & +.sqlite3")
	seedSQLiteURITestDatabase(t, path)
	if err := checkSQLiteIntegrity(path); err != nil {
		t.Fatalf("integrity check failed for special path: %v", err)
	}
	database, err := openLocalMCPDatabase(path)
	if err != nil {
		t.Fatalf("MCP database failed for special path: %v", err)
	}
	defer database.Close()
	if _, err = database.Exec("INSERT INTO uri_value(value) VALUES ('unexpected')"); err == nil {
		t.Fatal("MCP database lost readonly enforcement")
	}
}

func TestReadOnlySQLiteURIPOSIXReservedFilename(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permits question marks and literal backslashes in filenames")
	}
	directory := t.TempDir()
	original := filepath.Join(directory, "original.sqlite3")
	path := filepath.Join(directory, `question?mode=rw#50%\literal.sqlite3`)
	seedSQLiteURITestDatabase(t, original)
	if err := os.Rename(original, path); err != nil {
		t.Fatal(err)
	}
	if err := checkSQLiteIntegrity(path); err != nil {
		t.Fatalf("POSIX reserved characters changed the database path: %v", err)
	}
	if _, err := os.Stat(original); !os.IsNotExist(err) {
		t.Fatalf("integrity check opened an incorrect replacement path: %v", err)
	}
}

func TestSQLiteReadOnlyConsumersRejectMissingAndCorruptDatabases(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing #50%.sqlite3")
	if err := checkSQLiteIntegrity(missing); err == nil {
		t.Fatal("missing database passed integrity check")
	}
	if database, err := openLocalMCPDatabase(missing); err == nil {
		database.Close()
		t.Fatal("MCP opened a missing database")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("readonly consumer created missing database: %v", err)
	}
	corrupt := filepath.Join(t.TempDir(), "corrupt #50%.sqlite3")
	if err := os.WriteFile(corrupt, []byte("not a SQLite database"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := checkSQLiteIntegrity(corrupt); err == nil {
		t.Fatal("corrupt database passed integrity check")
	}
}

func seedSQLiteURITestDatabase(t *testing.T, path string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = database.Exec("CREATE TABLE uri_value (value TEXT NOT NULL); INSERT INTO uri_value VALUES ('original')"); err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err = database.Close(); err != nil {
		t.Fatal(err)
	}
}
