package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestGatewayAdminUsernamePasswordLogin(t *testing.T) {
	gateway := NewGatewayFromRootWithCredentials("legacy-admin-token-that-is-at-least-32-bytes", "admin", hashAdminPassword("correct horse"), "web")
	server := httptest.NewServer(gateway)
	defer server.Close()
	payload, _ := json.Marshal(map[string]string{"username": "admin", "password": "correct horse"})
	response, err := http.Post(server.URL+"/auth/login", "application/json", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("password login = %d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) != 1 || strings.TrimSpace(cookies[0].Value) == "" {
		t.Fatalf("password login did not create a session cookie: %#v", cookies)
	}
	request := httptest.NewRequest(http.MethodGet, "/auth/session", nil)
	request.AddCookie(cookies[0])
	recorder := httptest.NewRecorder()
	gateway.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"authenticated":true`) {
		t.Fatalf("password session = %d: %s", recorder.Code, recorder.Body.String())
	}
}
