package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagedMemoriesAreEditableAndIsolatedByPersona(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := runtime.configStore.db.Exec(`
		INSERT INTO personas (
			id, namespace, name, description, personality, scenario, first_message,
			system_prompt, post_history_instructions, message_example,
			alternate_greetings_json, tags_json, creator, character_version,
			source_format, source_version, avatar_data_uri, visual_description,
			created_at, updated_at
		) VALUES ('second-role', 'default', 'Second role', '', '', '', '', '', '', '',
			'[]', '[]', '', '', 'native', 'test', '', '', ?, ?)
	`, now, now)
	if err != nil {
		t.Fatal(err)
	}
	admin := NewGateway(runtime.adminToken)
	admin.runtime = runtime

	created := managedMemoryRequest(t, admin, http.MethodPost, "/api/v1/memories", map[string]any{
		"personaId": "doubao", "scopeKind": "user", "scopeReference": "member-one",
		"content": "喜欢茉莉花茶", "kind": "preference", "confidence": 0.9, "importance": 0.8,
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create memory = %d: %s", created.Code, created.Body.String())
	}
	var createBody struct {
		Data RecalledMemory `json:"data"`
	}
	decodeRecorder(t, created, &createBody)
	if createBody.Data.PersonaID != "doubao" || createBody.Data.ScopeReference != "member-one" {
		t.Fatalf("created memory = %+v", createBody.Data)
	}
	if _, _, err = runtime.memory.AddMemory(context.Background(), personaMemoryScope("second-role", "user", "member-one"), "喜欢黑咖啡"); err != nil {
		t.Fatal(err)
	}

	listed := managedMemoryRequest(t, admin, http.MethodGet, "/api/v1/memories?personaId=doubao&scopeKind=user", nil)
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), "喜欢茉莉花茶") || strings.Contains(listed.Body.String(), "喜欢黑咖啡") {
		t.Fatalf("persona memory list = %d: %s", listed.Code, listed.Body.String())
	}

	updated := managedMemoryRequest(t, admin, http.MethodPut, "/api/v1/memories/"+createBody.Data.ID, map[string]any{
		"personaId": "doubao", "scopeKind": "user", "scopeReference": "member-one",
		"content": "只喝无糖茉莉花茶", "kind": "preference", "confidence": 1, "importance": 0.9,
	})
	if updated.Code != http.StatusOK {
		t.Fatalf("update memory = %d: %s", updated.Code, updated.Body.String())
	}
	matches, err := runtime.memory.SearchMemories(context.Background(), personaMemoryScope("doubao", "user", "member-one"), "无糖", 5)
	if err != nil || len(matches) != 1 || matches[0].UntrustedContent != "只喝无糖茉莉花茶" {
		t.Fatalf("corrected memory = %+v: %v", matches, err)
	}
	other, err := runtime.memory.SearchMemories(context.Background(), personaMemoryScope("second-role", "user", "member-one"), "无糖", 5)
	if err != nil || len(other) != 0 {
		t.Fatalf("cross-persona memory leak = %+v: %v", other, err)
	}

	var referenceCipher []byte
	if err = runtime.db.QueryRow("SELECT scope_ref_cipher FROM agent_memories WHERE id = ?", createBody.Data.ID).Scan(&referenceCipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(referenceCipher, []byte("member-one")) {
		t.Fatal("memory scope reference stored in plaintext")
	}

	deleted := managedMemoryRequest(t, admin, http.MethodDelete,
		"/api/v1/memories/"+createBody.Data.ID+"?personaId=doubao&scopeKind=user&scopeReference=member-one", nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete memory = %d: %s", deleted.Code, deleted.Body.String())
	}
	matches, err = runtime.memory.ListMemories(context.Background(), personaMemoryScope("doubao", "user", "member-one"), 5)
	if err != nil || len(matches) != 0 {
		t.Fatalf("deleted memory remains = %+v: %v", matches, err)
	}
}

func TestManagedMemoriesAreUnavailableOnCoreListener(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	core := NewGateway("")
	core.runtime = runtime
	response := httptest.NewRecorder()
	core.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/memories?personaId=doubao", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("core memory route = %d: %s", response.Code, response.Body.String())
	}
}

func TestManagedRelationshipsAutoEvolveAndCanBeLockedOrReset(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	admin := NewGateway(runtime.adminToken)
	admin.runtime = runtime
	ctx := context.Background()
	now := time.Now().UTC()
	for index := 0; index < 12; index++ {
		_, inserted, err := runtime.memory.ObserveRelationship(
			ctx, "relationship-event-"+strconv.Itoa(index), personaConversationRef("doubao", "group-one"), "member-one",
			index < 5, now.Add(time.Duration(index)*time.Minute), RelationshipIdentity{
				PersonaID: "doubao", ConversationRef: "group-one", SenderRef: "member-one", SenderDisplayName: "小明",
			},
		)
		if err != nil || !inserted {
			t.Fatalf("observe relationship %d: inserted=%v err=%v", index, inserted, err)
		}
	}

	listed := managedMemoryRequest(t, admin, http.MethodGet, "/api/v1/relationships?personaId=doubao&limit=20", nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("list relationships = %d: %s", listed.Code, listed.Body.String())
	}
	var listBody struct {
		Data struct {
			Items []ManagedRelationship `json:"items"`
		} `json:"data"`
	}
	decodeRecorder(t, listed, &listBody)
	if len(listBody.Data.Items) != 1 || listBody.Data.Items[0].SenderDisplayName != "小明" || listBody.Data.Items[0].State.Intimacy <= 0 {
		t.Fatalf("managed relationships = %+v", listBody.Data.Items)
	}
	id := listBody.Data.Items[0].ID
	var senderRefCipher, senderDisplayCipher []byte
	if err := runtime.db.QueryRow(`SELECT sender_ref_cipher, sender_display_cipher FROM member_relationship_state WHERE relationship_id = ?`, id).Scan(&senderRefCipher, &senderDisplayCipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(senderRefCipher, []byte("member-one")) || bytes.Contains(senderDisplayCipher, []byte("小明")) {
		t.Fatal("relationship identity stored in plaintext")
	}
	locked := managedMemoryRequest(t, admin, http.MethodPut, "/api/v1/relationships/"+id, map[string]any{"intimacy": 92, "locked": true})
	if locked.Code != http.StatusOK || !strings.Contains(locked.Body.String(), `"stage":"亲近的人"`) {
		t.Fatalf("lock relationship = %d: %s", locked.Code, locked.Body.String())
	}
	state, found, err := runtime.memory.Relationship(ctx, personaConversationRef("doubao", "group-one"), "member-one")
	if err != nil || !found || !state.IntimacyLocked || state.Intimacy != 92 {
		t.Fatalf("locked relationship state = %+v found=%v err=%v", state, found, err)
	}
	unlocked := managedMemoryRequest(t, admin, http.MethodPut, "/api/v1/relationships/"+id, map[string]any{"locked": false})
	if unlocked.Code != http.StatusOK || strings.Contains(unlocked.Body.String(), `"intimacyLocked":true`) {
		t.Fatalf("unlock relationship = %d: %s", unlocked.Code, unlocked.Body.String())
	}
	deleted := managedMemoryRequest(t, admin, http.MethodDelete, "/api/v1/relationships/"+id, nil)
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete relationship = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func managedMemoryRequest(t *testing.T, gateway *Gateway, method, path string, payload any) *httptest.ResponseRecorder {
	t.Helper()
	var body *bytes.Reader
	if payload == nil {
		body = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(encoded)
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set(adminTokenHeader, gateway.adminToken)
	request.Header.Set("content-type", "application/json")
	response := httptest.NewRecorder()
	gateway.ServeHTTP(response, request)
	return response
}
