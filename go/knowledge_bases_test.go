package main

import (
	"path/filepath"
	"testing"
)

func TestKnowledgeBaseLayerResolver(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.ensureKnowledgeBaseNamespace("ops"); err != nil {
		t.Fatal(err)
	}
	if err = store.ensureKnowledgeBaseNamespace("persona:doubao"); err != nil {
		t.Fatal(err)
	}
	config, err := store.runtimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.KnowledgeNamespace = "ops"
	selections, err := store.knowledgeNamespacesForRun(config, "doubao", "doubao-qq")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, selection := range selections {
		seen[selection.Namespace] = true
	}
	for _, namespace := range []string{"default", "ops", "persona:doubao"} {
		if !seen[namespace] {
			t.Fatalf("expected %q in layered selection: %#v", namespace, selections)
		}
	}
}

func TestKnowledgeBaseAutoIncludeIsConsumed(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.ensureKnowledgeBaseNamespace("ops"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE knowledge_bases SET auto_include = 0 WHERE namespace = 'ops'`); err != nil {
		t.Fatal(err)
	}
	config, err := store.runtimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	config.KnowledgeNamespace = "default"
	selections, err := store.knowledgeNamespacesForRun(config, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range selections {
		if selection.Namespace == "ops" {
			t.Fatalf("disabled auto-include namespace was selected: %#v", selections)
		}
	}
	config.KnowledgeNamespace = "ops"
	selections, err = store.knowledgeNamespacesForRun(config, "", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, selection := range selections {
		if selection.Namespace == "ops" {
			return
		}
	}
	t.Fatalf("explicit namespace was not selected: %#v", selections)
}
