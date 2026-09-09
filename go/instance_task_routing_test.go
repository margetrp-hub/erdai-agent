package main

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestPrepareInstanceTaskEndpointCoversTextLanesAndPreservesSpecializedRoutes(t *testing.T) {
	_, db := newTestCoreConfig(t)
	defer db.Close()
	store := &coreConfigStore{db: db}
	if _, err := db.Exec(`UPDATE model_endpoints SET enabled=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE persona_runtime_profiles SET profile_json='{}' WHERE persona_id IN ('doubao','xiaoman')`); err != nil {
		t.Fatal(err)
	}
	insertTestEndpoint(t, db, "global-chat", "global-chat-model", []string{"chat"}, "llm", "openai")
	// Match the deployed capability declarations: neither endpoint explicitly
	// advertises code, and only the existing global endpoint advertises vision.
	insertTestEndpoint(t, db, "global-task", "grok-4.5", []string{"chat", "long_context", "reasoning", "tool_calling", "vision"}, "llm", "openai")
	insertTestEndpoint(t, db, "instance-task", "grok-4.6", []string{"chat", "long_context", "reasoning", "tool_calling"}, "llm", "openai")
	insertTestEndpoint(t, db, "image-route", "image-model", []string{"image_generation"}, "media", "grok_generate_image")
	insertTestEndpoint(t, db, "video-route", "video-model", []string{"video_generation"}, "media", "grok_generate_video")
	insertTestEndpoint(t, db, "search-route", "search-model", []string{"web_search"}, "tool", "grok_web_search")
	setTestIntegration(t, db, "companion_policy", map[string]any{
		"enableModelRouting": true, "chatModel": "global-chat", "taskModel": "global-task",
	})
	create := func(path string, payload map[string]any) {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		response := agentInstanceRequest(t, store, http.MethodPost, path, string(body))
		if response.Code != http.StatusCreated {
			t.Fatalf("fixture %s: %d %s", path, response.Code, response.Body.String())
		}
	}
	for id, personaID := range map[string]string{"task-doubao": "doubao", "task-doubao-secondary": "doubao", "task-xiaoman": "xiaoman"} {
		overrides := map[string]any{}
		if id == "task-doubao" {
			overrides["taskEndpointId"] = "instance-task"
		}
		create("/api/v1/agent-instances", map[string]any{"id": id, "displayName": id, "personaId": personaID, "overrides": overrides})
		create("/api/v1/agent-instances/"+id+"/connectors", map[string]any{"connectorId": id + "-connector"})
		create("/api/v1/agent-instance-routes", map[string]any{"id": id + "-route", "instanceId": id, "connectorId": id + "-connector", "transport": "qq_official"})
		if _, err := db.Exec(`INSERT INTO persona_bindings
			(id,persona_id,transport,transport_instance,conversation_ref,created_at,updated_at)
			VALUES (?,?,'qq_official',?,'*','now','now')`, id+"-persona", personaID, id+"-connector"); err != nil {
			t.Fatal(err)
		}
	}
	for _, scenario := range []struct {
		name, message, lane, selected string
		hasImage, hasDocument         bool
	}{
		{name: "csv-to-json", message: "写一个 Python 脚本，把 CSV 转成 JSON。", lane: "tools", selected: "instance-task"},
		{name: "code", message: "写个函数计算斐波那契数列。", lane: "code", selected: "instance-task"},
		{name: "reasoning", message: "比较这两个方案的优缺点。", lane: "reasoning", selected: "instance-task"},
		{name: "document-tools", message: "总结附件内容。", hasDocument: true, lane: "tools", selected: "instance-task"},
		{name: "chat", message: "在吗", lane: "chat", selected: "global-chat"},
		{name: "vision", message: "这张图片是什么？", hasImage: true, lane: "vision", selected: "global-task"},
		{name: "image-generation", message: "给我一张自拍", lane: "image", selected: "image-route"},
		{name: "video-generation", message: "帮我生成一段视频", lane: "video", selected: "video-route"},
		{name: "search", message: "帮我搜索今天的 AI 新闻", lane: "search", selected: "search-route"},
	} {
		for _, instanceID := range []string{"task-doubao", "task-doubao-secondary", "task-xiaoman"} {
			t.Run(scenario.name+"/"+instanceID, func(t *testing.T) {
				prepared, err := store.prepareRuntime(corePreparePayload{
					Transport: "qq_official", TransportInstance: instanceID + "-connector", ConversationRef: "task-routing-group",
					Message: scenario.message, HasImage: scenario.hasImage, HasDocument: scenario.hasDocument,
				})
				if err != nil {
					t.Fatal(err)
				}
				want := scenario.selected
				if want == "instance-task" && instanceID != "task-doubao" {
					want = "global-task"
				}
				if prepared.Lane != scenario.lane || prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != want {
					t.Fatalf("lane=%s route=%+v want lane=%s endpoint=%s", prepared.Lane, prepared.RouteDecision, scenario.lane, want)
				}
				wantPersona := "doubao"
				if instanceID == "task-xiaoman" {
					wantPersona = "xiaoman"
				}
				if prepared.ActivePersona == nil || prepared.ActivePersona.ID != wantPersona {
					t.Fatalf("instance %s did not resolve expected persona %s", instanceID, wantPersona)
				}
			})
		}
	}
	t.Run("pinned-task-still-requires-vision", func(t *testing.T) {
		setTestIntegration(t, db, "companion_policy", map[string]any{
			"enableModelRouting": false, "chatModel": "global-chat", "taskModel": "global-task",
		})
		prepared, err := store.prepareRuntime(corePreparePayload{
			Transport: "qq_official", TransportInstance: "task-doubao-connector", ConversationRef: "task-routing-group",
			Message: "看看这张图片", HasImage: true,
		})
		if err != nil || prepared.RouteDecision.Selected == nil || prepared.RouteDecision.Selected.Endpoint.ID != "global-task" {
			t.Fatalf("text-only task preference replaced required vision capability: route=%+v err=%v", prepared.RouteDecision, err)
		}
	})
}
