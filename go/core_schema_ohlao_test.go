package main

import (
	"strings"
	"testing"
)

func TestCoreSchemaSeedsOhlaoKnowledgeAndGenericConversationSkill(t *testing.T) {
	_, db := newTestCoreConfig(t)
	defer db.Close()
	var source, content string
	if err := db.QueryRow(`SELECT source_uri, content FROM knowledge_documents WHERE id = 'ohlao-sub2api-overview'`).Scan(&source, &content); err != nil {
		t.Fatal(err)
	}
	if source != "https://ohlao.cfd/" || !strings.Contains(content, "OpenAI SDK") {
		t.Fatalf("Ohlao knowledge source=%q content=%q", source, content)
	}
	var requiredTools string
	if err := db.QueryRow(`SELECT required_tools_json FROM skills WHERE id = 'knowledge-gap-search'`).Scan(&requiredTools); err != nil {
		t.Fatal(err)
	}
	if requiredTools != `["grok_web_search"]` {
		t.Fatalf("knowledge gap tools = %s", requiredTools)
	}
}
