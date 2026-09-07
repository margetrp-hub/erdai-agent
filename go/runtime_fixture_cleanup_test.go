package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeCloseReleasesDatabaseFile(t *testing.T) {
	for i := range 5 {
		if !t.Run(fmt.Sprintf("close-%d", i), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runtime.sqlite3")
			agent, err := NewAgentRuntime(RuntimeConfig{
				DatabasePath: path, ConfigDatabasePath: newTestCoreConfigPath(t),
				AdminToken: "admin-test-token", RuntimeToken: testRuntimeToken,
				ModelAPIKey:   "model-test-key",
				EncryptionKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)),
			})
			if err != nil {
				t.Fatal(err)
			}
			assertRuntimeClosedFile(t, agent, path)
		}) {
			break
		}
	}
}

func TestRuntimeFixtureCleanupReleasesDatabaseConnections(t *testing.T) {
	var runtime *AgentRuntime
	t.Run("fixture", func(t *testing.T) {
		runtime = newIdleRuntime(t)
	})
	if runtime == nil {
		t.Fatal("fixture did not start")
	}
	// Cleanup must complete before the parent continues, not at process exit.
	defer runtime.Close()
	if err := runtime.db.Ping(); err == nil {
		t.Error("runtime fixture left its database open after the test")
	}
	if got := runtime.db.Stats().OpenConnections; got != 0 {
		t.Errorf("runtime fixture left %d database connections open", got)
	}
	if err := runtime.configStore.db.Ping(); err == nil {
		t.Error("runtime fixture left its configuration database open after the test")
	}
	if got := runtime.configStore.db.Stats().OpenConnections; got != 0 {
		t.Errorf("runtime fixture left %d configuration connections open", got)
	}
}

func assertRuntimeClosedFile(t *testing.T, agent *AgentRuntime, path string) {
	t.Helper()
	if err := agent.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		stats := agent.db.Stats()
		stack := make([]byte, 128*1024)
		n := runtime.Stack(stack, true)
		t.Fatalf("closed runtime retained database: %v; stats=%+v\n%s", err, stats, stack[:n])
	}
}
