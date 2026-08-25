package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestManagedEnvFileWritesBackupAndEscapesValue(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "runtime.env")
	original := "# keep this comment\nERDAI_MODEL_API_KEY=old-value\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	if err := updateManagedEnvFile(path, "ERDAI_MODEL_API_KEY", "value with spaces'and$symbols"); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "ERDAI_MODEL_API_KEY='value with spaces\\'and$symbols'") || !strings.Contains(string(updated), "# keep this comment") {
		t.Fatalf("updated runtime.env = %q", string(updated))
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != original {
		t.Fatalf("backup = %q", string(backup))
	}
}

func TestManagedCredentialNameRejectsRuntimeBoundaries(t *testing.T) {
	for _, name := range []string{"ERDAI_ADMIN_TOKEN", "ERDAI_RUN_ENCRYPTION_KEY", "ERDAI_CONFIG_DATABASE", "ERDAI_RUNTIME_ENV_PATH"} {
		if managedCredentialNameAllowed(name) {
			t.Fatalf("dangerous credential name accepted: %s", name)
		}
	}
	for _, name := range []string{"ERDAI_QQ_SECRET", "ERDAI_MODEL_API_KEY", "ASTRBOT_TELEGRAM_TOKEN"} {
		if !managedCredentialNameAllowed(name) {
			t.Fatalf("managed credential name rejected: %s", name)
		}
	}
}

func TestLoadManagedCredentialsFileAppliesQuotedValue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "managed-credentials.env")
	if err := os.WriteFile(path, []byte("ERDAI_QQ_SECRET='value with spaces\\'and$symbols'\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERDAI_QQ_SECRET", "")
	if err := loadManagedCredentialsFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("ERDAI_QQ_SECRET"); got != "value with spaces'and$symbols" {
		t.Fatalf("loaded managed credential = %q", got)
	}
}

func TestRequiredManagedCredentialCannotBeCleared(t *testing.T) {
	if !managedCredentialRequired("ERDAI_MODEL_API_KEY") || managedCredentialRequired("ERDAI_QQ_SECRET") {
		t.Fatal("required credential classification is incorrect")
	}
}

func TestCollectManagedCredentialRefsFromPlatformConfig(t *testing.T) {
	names := map[string]struct{}{}
	collectManagedCredentialRefs(map[string]any{
		"token":  "ASTRBOT_TELEGRAM_TOKEN",
		"nested": []any{"ERDAI_QQ_SECRET", "not-a-secret"},
	}, names)
	want := map[string]struct{}{"ASTRBOT_TELEGRAM_TOKEN": {}, "ERDAI_QQ_SECRET": {}}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("credential refs = %#v, want %#v", names, want)
	}
}
