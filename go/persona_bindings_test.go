package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPersonaBindingSelectsConversationSpecificRole(t *testing.T) {
	path, db := newTestCoreConfig(t)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := db.Exec(`
		INSERT INTO personas (
			id, namespace, name, description, personality, scenario, first_message,
			system_prompt, post_history_instructions, message_example,
			alternate_greetings_json, tags_json, creator, character_version,
			source_format, source_version, avatar_data_uri, visual_description,
			created_at, updated_at
		) VALUES ('second-role', 'default', '第二角色', '', '', '', '', '', '', '', '[]', '[]', '', '', 'native', 'test', '', '', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
		INSERT INTO persona_bindings (
			id, persona_id, transport, conversation_ref, priority, enabled, created_at, updated_at
		) VALUES ('qq-group-two', 'second-role', 'qq_official', 'group-two', 100, 1, ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fallback := "doubao"
	selected, err := store.resolvePersonaID("qq_official", "group-two", &fallback)
	if err != nil || selected == nil || *selected != "second-role" {
		t.Fatalf("selected persona = %v: %v", selected, err)
	}
	selected, err = store.resolvePersonaID("qq_official", "other-group", &fallback)
	if err != nil || selected == nil || *selected != "doubao" {
		t.Fatalf("fallback persona = %v: %v", selected, err)
	}
}

func TestPersonaBindingSelectsConnectorInstanceBeforeGlobalBinding(t *testing.T) {
	path, db := newTestCoreConfig(t)
	defer db.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range []struct{ id, name string }{{"xiaoman-test", "小满测试"}, {"luna-test", "Luna测试"}} {
		_, err := db.Exec(`
			INSERT INTO personas (
				id, namespace, name, description, personality, scenario, first_message,
				system_prompt, post_history_instructions, message_example,
				alternate_greetings_json, tags_json, creator, character_version,
				source_format, source_version, avatar_data_uri, visual_description,
				created_at, updated_at
			) VALUES (?, 'default', ?, '', '', '', '', '', '', '', '[]', '[]', '', '', 'native', 'test', '', '', ?, ?)
		`, row.id, row.name, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []struct{ id, persona, instance string }{
		{"qq-doubao-default", "doubao", "qq-doubao"},
		{"qq-xiaoman-default", "xiaoman-test", "qq-xiaoman"},
		{"qq-xiaoman-group", "luna-test", "qq-xiaoman"},
	} {
		conversation := "*"
		if row.id == "qq-xiaoman-group" {
			conversation = "group-special"
		}
		_, err := db.Exec(`
			INSERT INTO persona_bindings (
				id, persona_id, transport, transport_instance, conversation_ref,
				priority, enabled, created_at, updated_at
			) VALUES (?, ?, 'qq_official', ?, ?, 100, 1, ?, ?)
		`, row.id, row.persona, row.instance, conversation, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	fallback := "doubao"
	selected, err := store.resolvePersonaIDForInstance("qq-xiaoman", "qq_official", "group-special", &fallback)
	if err != nil || selected == nil || *selected != "luna-test" {
		t.Fatalf("conversation-specific instance persona = %v: %v", selected, err)
	}
	selected, err = store.resolvePersonaIDForInstance("qq-xiaoman", "qq_official", "other-group", &fallback)
	if err != nil || selected == nil || *selected != "xiaoman-test" {
		t.Fatalf("instance-wide persona = %v: %v", selected, err)
	}
	selected, err = store.resolvePersonaIDForInstance("qq-doubao", "qq_official", "other-group", &fallback)
	if err != nil || selected == nil || *selected != "doubao" {
		t.Fatalf("second instance leaked persona = %v: %v", selected, err)
	}
}

func TestPersonaBindingManagementRequiresAdminListener(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/persona-bindings", nil)
	response := httptest.NewRecorder()
	core := NewGateway("")
	core.runtime = runtime
	core.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("core binding route status = %d", response.Code)
	}
}
