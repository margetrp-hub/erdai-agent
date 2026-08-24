package main

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestSkillsCRUDMatchingAndToolGating(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	if _, err := runtime.configStore.db.Exec("DELETE FROM skills"); err != nil {
		t.Fatal(err)
	}
	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/skills", map[string]any{
		"id": "office-read-test", "name": "Office 阅读测试",
		"description": "read files", "instructions": "先读附件，再回答。",
		"enabled": true, "activationMode": "any", "triggers": []string{"附件"},
		"attachmentKinds": []string{"file"}, "requiredTools": []string{"read_document"},
		"requiredMcpServers": []string{"microsoft-learn"},
		"allowedAuthorities": []string{"member", "admin"}, "personaIds": []string{"doubao"},
		"priority": 90,
	}, "admin")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"requiredTools":["read_document"]`) {
		t.Fatalf("create skill = %d: %s", created.Code, created.Body.String())
	}
	unauthorized := managementRequest(t, runtime, http.MethodPut, "/api/v1/skills/office-read-test", map[string]any{"enabled": false}, "")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized skill update = %d", unauthorized.Code)
	}

	matched, total, err := runtime.configStore.matchedRuntimeSkills("member", "doubao", "看看附件", []string{"file"})
	if err != nil || total != 1 || len(matched) != 1 {
		t.Fatalf("matched skills = %+v total=%d err=%v", matched, total, err)
	}
	unmatched, _, err := runtime.configStore.matchedRuntimeSkills("member", "other", "看看附件", []string{"file"})
	if err != nil || len(unmatched) != 0 {
		t.Fatalf("persona-scoped skill leaked: %+v err=%v", unmatched, err)
	}

	policy := runtimeToolPolicy{
		Tools:      []runtimeTool{{ID: "read-document", Name: "read_document"}, {ID: "search", Name: "grok_web_search"}},
		MCPServers: []runtimeMCPServer{{ID: "microsoft-learn"}, {ID: "context7-docs"}},
	}
	filtered := filterRuntimeToolPolicy(policy, matched, total)
	if len(filtered.Tools) != 1 || filtered.Tools[0].Name != "read_document" || len(filtered.MCPServers) != 1 || filtered.MCPServers[0].ID != "microsoft-learn" {
		t.Fatalf("filtered policy = %+v", filtered)
	}

	updated := managementRequest(t, runtime, http.MethodPut, "/api/v1/skills/office-read-test", map[string]any{
		"activationMode": "always", "triggers": []string{}, "attachmentKinds": []string{},
	}, "admin")
	if updated.Code != http.StatusOK || !strings.Contains(updated.Body.String(), `"activationMode":"always"`) {
		t.Fatalf("update skill = %d: %s", updated.Code, updated.Body.String())
	}
	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/skills/office-read-test", nil, "admin")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete skill = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestPrepareInjectsOnlyTriggeredSkills(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	if _, err := runtime.configStore.db.Exec("DELETE FROM skills"); err != nil {
		t.Fatal(err)
	}
	insertTestSkill(t, runtime.configStore.db, "always-test", []string{})
	prepared, err := runtime.configStore.prepareRuntime(corePreparePayload{Message: "普通聊天"})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.Skills) != 1 || !strings.Contains(prepared.CompiledSystemPrompt, "test skill") {
		t.Fatalf("prepared skills = %+v prompt=%s", prepared.Skills, prepared.CompiledSystemPrompt)
	}
}

func TestRuntimeSkillCatalogLoadsOnlyTopInstructionBodies(t *testing.T) {
	runtime := newCompleteManagementRuntime(t)
	if _, err := runtime.configStore.db.Exec("DELETE FROM skills"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("always-%02d", i)
		now := mgmtNow()
		_, err := runtime.configStore.db.Exec(`INSERT INTO skills (
			id, name, description, instructions, enabled, activation_mode, triggers_json,
			attachment_kinds_json, required_tools_json, required_mcp_servers_json,
			allowed_authorities_json, persona_ids_json, priority, created_at, updated_at
		) VALUES (?, ?, '', ?, 1, 'always', '[]', '[]', '[]', '[]', '["member"]', '[]', ?, ?, ?)`,
			id, id, "instructions-"+id, 100-i, now, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	catalog, enabled, err := runtime.configStore.matchedRuntimeSkillCatalog("member", "", "hello", nil)
	if err != nil || enabled != 8 || len(catalog) != 8 {
		t.Fatalf("catalog = %d enabled=%d err=%v", len(catalog), enabled, err)
	}
	selected := selectRuntimeSkillCatalog(catalog, 6)
	if len(selected) != 6 || selected[0].ID != "always-00" || selected[5].ID != "always-05" {
		t.Fatalf("selected catalog = %+v", selected)
	}
	loaded, err := runtime.configStore.loadRuntimeSkillDetails([]string{selected[0].ID, selected[5].ID})
	if err != nil || len(loaded) != 2 || loaded[0].Instructions == "" || loaded[1].Instructions == "" {
		t.Fatalf("loaded details = %+v err=%v", loaded, err)
	}
}

func TestOfficeSkillMatchesNaturalDocumentCreationPhrases(t *testing.T) {
	skill := mgmtSkill{
		Enabled: true, ActivationMode: "any", AllowedAuthorities: []string{"member"},
		Triggers: []string{"做个Word"}, RequiredTools: []string{"create_office_document"},
	}
	if !skillMatches(skill, "member", "doubao", "帮我做一个word，里面放豆包", nil) {
		t.Fatal("Office creation skill did not match a natural document request")
	}
	if skillMatches(skill, "member", "doubao", "帮我看看这个word", nil) {
		t.Fatal("Office creation skill matched a document reading request")
	}
}
