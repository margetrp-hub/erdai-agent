package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCapabilityManifestLifecycleAndBinding(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	runtime.adminToken = managementAdminToken

	created := managementRequest(t, runtime, http.MethodPost, "/api/v1/capabilities/manifests", map[string]any{
		"id": "calendar-tool", "version": "1.0.0", "kind": "tool", "source": "external",
		"manifest": map[string]any{"adapter": "calendar", "capabilities": []string{"calendar.read"}},
	}, "admin")
	if created.Code != http.StatusCreated || !strings.Contains(created.Body.String(), `"state":"staged"`) || !strings.Contains(created.Body.String(), `"runtimeReady":false`) {
		t.Fatalf("create capability = %d: %s", created.Code, created.Body.String())
	}
	var createdBody struct {
		Data capabilityManifest `json:"data"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createdBody); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(createdBody.Data.Digest, "sha256:") {
		t.Fatalf("digest = %q", createdBody.Data.Digest)
	}

	badDigest := managementRequest(t, runtime, http.MethodPut, "/api/v1/capabilities/manifests/calendar-tool?version=1.0.0", map[string]any{
		"digest": "sha256:bad",
	}, "admin")
	if badDigest.Code != http.StatusBadRequest {
		t.Fatalf("bad digest status = %d: %s", badDigest.Code, badDigest.Body.String())
	}

	bound := managementRequest(t, runtime, http.MethodPost, "/api/v1/capabilities/manifests/calendar-tool/bindings?version=1.0.0", map[string]any{
		"workspaceId": "workspace-a", "grantedBy": "admin",
	}, "admin")
	if bound.Code != http.StatusCreated || !strings.Contains(bound.Body.String(), `"workspaceId":"workspace-a"`) {
		t.Fatalf("bind capability = %d: %s", bound.Code, bound.Body.String())
	}
	allowed, err := runtime.configStore.capabilityAllowed("calendar-tool", "1.0.0", "", "workspace-a")
	if err != nil || !allowed {
		t.Fatalf("capability allowed = %v, err=%v", allowed, err)
	}

	active := managementRequest(t, runtime, http.MethodPut, "/api/v1/capabilities/manifests/calendar-tool?version=1.0.0", map[string]any{
		"state": "active", "manifest": map[string]any{"adapter": "calendar", "capabilities": []string{"calendar.read"}},
	}, "admin")
	if active.Code != http.StatusOK || !strings.Contains(active.Body.String(), `"state":"active"`) {
		t.Fatalf("activate capability = %d: %s", active.Code, active.Body.String())
	}

	listed := managementRequest(t, runtime, http.MethodGet, "/api/v1/capabilities/manifests?kind=tool&state=active", nil, "")
	if listed.Code != http.StatusOK || !strings.Contains(listed.Body.String(), `"id":"calendar-tool"`) {
		t.Fatalf("list capability = %d: %s", listed.Code, listed.Body.String())
	}

	paused := managementRequest(t, runtime, http.MethodPut, "/api/v1/capabilities/manifests/calendar-tool?version=1.0.0", map[string]any{"state": "paused"}, "admin")
	if paused.Code != http.StatusOK || !strings.Contains(paused.Body.String(), `"state":"paused"`) {
		t.Fatalf("pause capability = %d: %s", paused.Code, paused.Body.String())
	}
	retired := managementRequest(t, runtime, http.MethodPut, "/api/v1/capabilities/manifests/calendar-tool?version=1.0.0", map[string]any{"state": "retired"}, "admin")
	if retired.Code != http.StatusOK {
		t.Fatalf("retire capability = %d: %s", retired.Code, retired.Body.String())
	}
	deleted := managementRequest(t, runtime, http.MethodDelete, "/api/v1/capabilities/manifests/calendar-tool?version=1.0.0", nil, "admin")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete capability = %d: %s", deleted.Code, deleted.Body.String())
	}
}

func TestCapabilityManifestRejectsUnboundAndCrossWorkspaceBotBinding(t *testing.T) {
	store := newAgentInstanceStore(t)
	defer store.db.Close()
	if err := ensureCapabilityManifestSchema(store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO agent_instances(id, display_name, persona_id, memory_namespace, overrides_json, enabled, created_at, updated_at)
		VALUES ('private-bot-bot-a', 'Bot A', 'persona-test', 'private-bot:bot-a', '{}', 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO private_bots(id, owner_ref, workspace_id, display_name, persona_id, agent_instance_id, memory_namespace, visibility, status, source_bot_id, created_at, updated_at)
		VALUES ('bot-a', 'owner-a', 'workspace-a', 'Bot A', 'persona-test', 'private-bot-bot-a', 'private-bot:bot-a', 'private', 'active', '', 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO private_bot_acl(bot_id, subject_ref, role, enabled, created_at, updated_at) VALUES ('bot-a', 'owner-a', 'owner', 1, 'now', 'now')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createCapabilityManifest(capabilityManifestPayload{ID: stringPtr("bot-tool"), Version: stringPtr("1.0.0"), Kind: stringPtr("tool"), Manifest: mapPtr(map[string]any{"adapter": "builtin"})}); err != nil {
		t.Fatal(err)
	}
	_, err := store.createCapabilityBinding("bot-tool", "1.0.0", capabilityBindingPayload{BotID: stringPtr("bot-a"), WorkspaceID: stringPtr("workspace-b"), GrantedBy: stringPtr("admin")})
	if err == nil || !strings.Contains(err.Error(), "workspaceId does not match") {
		t.Fatalf("cross workspace binding err = %v", err)
	}
}

func stringPtr(value string) *string              { return &value }
func mapPtr(value map[string]any) *map[string]any { return &value }
