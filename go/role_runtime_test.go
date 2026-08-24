package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPersonaRuntimeEndpointAndToolPolicy(t *testing.T) {
	profile := personaRuntimeProfile{
		ChatEndpointID: "chat-fast",
		TaskEndpointID: "task-deep",
		AllowedToolIDs: []string{"search"},
		DeniedToolIDs:  []string{"danger"},
	}
	if got := personaRuntimeEndpoint(profile, "chat"); got != "chat-fast" {
		t.Fatalf("chat endpoint = %q", got)
	}
	if got := personaRuntimeEndpoint(profile, "task"); got != "task-deep" {
		t.Fatalf("task endpoint = %q", got)
	}
	policy := applyPersonaRuntimeTools(runtimeToolPolicy{Tools: []runtimeTool{
		{ID: "search", Name: "web"}, {ID: "danger", Name: "shell"}, {ID: "other", Name: "other"},
	}}, profile)
	if len(policy.Tools) != 1 || policy.Tools[0].ID != "search" {
		t.Fatalf("filtered tools = %+v", policy.Tools)
	}
}

func TestPersonaRuntimeToolAliasesRespectInstanceDenials(t *testing.T) {
	profile := personaRuntimeProfile{DeniedToolIDs: []string{"ops-status", "query_ops_status", "ops_status", "radar"}}
	if personaRuntimeAllowsAnyTool(profile, "ops-status", "query_ops_status", "ops_status") {
		t.Fatal("OPS aliases must be disabled together")
	}
	if personaRuntimeAllowsAnyTool(profile, "ops-status", "radar", "query_radar") {
		t.Fatal("radar aliases must be disabled together")
	}
	if !personaRuntimeAllowsAnyTool(personaRuntimeProfile{}, "ops-status") {
		t.Fatal("an unrestricted profile must inherit the Core capability")
	}
}

func TestPersonaRuntimeModelLaneUsesTaskForComplexChat(t *testing.T) {
	if got := personaRuntimeModelLane("chat", "short message", 100); got != "chat" {
		t.Fatalf("short chat lane = %q", got)
	}
	message := "This is a deliberately long request that needs comparison, risk analysis, a staged plan, rollback checks, and acceptance criteria."
	if got := personaRuntimeModelLane("chat", message, 100); got != "task" {
		t.Fatalf("complex chat lane = %q", got)
	}
	if got := personaRuntimeModelLane("vision", message, 100); got != "vision" {
		t.Fatalf("vision lane = %q", got)
	}
}

func TestPersonaRuntimeReplyBudgetFeedsMessagePolicy(t *testing.T) {
	chars, sentences := 30, 1
	raw, err := resolvedRuntimeMessagePolicy(
		json.RawMessage(`{"segmentedReplyEnabled":false,"segmentMaxChars":24}`),
		nativeRuntimeConfig{MaxReplyChars: 40, MaxReplySentences: 2},
		personaRuntimeProfile{MaxReplyChars: &chars, MaxReplySentences: &sentences},
	)
	if err != nil {
		t.Fatal(err)
	}
	var policy runtimeMessagePolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		t.Fatal(err)
	}
	if policy.MaxReplyChars != 30 || policy.MaxReplySentences != 1 || policy.SegmentMaxChars != 24 {
		t.Fatalf("resolved message policy = %+v", policy)
	}
}

func TestPersonaSearchModeAndReplyStyleAreExplicit(t *testing.T) {
	profile := personaRuntimeProfile{SearchMode: "explicit_only", SearchReplyStyle: "natural"}
	policy := runtimeToolPolicy{Tools: []runtimeTool{
		{ID: "search", AdapterRef: "grok_web_search"},
		{ID: "memory", AdapterRef: "memory_recall"},
	}}
	if got := applyPersonaSearchMode(policy, profile, "你是谁？"); len(got.Tools) != 1 || got.Tools[0].ID != "memory" {
		t.Fatalf("social message must suppress search: %+v", got.Tools)
	}
	if got := applyPersonaSearchMode(policy, profile, "帮我查一下最新公告"); len(got.Tools) != 2 {
		t.Fatalf("explicit search must retain search tool: %+v", got.Tools)
	}
	if got := searchReplyInstruction(profile.SearchReplyStyle); !strings.Contains(got, "当前角色的口吻") {
		t.Fatalf("natural search style lost its human-expression contract: %q", got)
	}
}
