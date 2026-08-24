package main

import "testing"

func TestRuntimeScopeSeparatesInstancesAndConnectors(t *testing.T) {
	base := runtimeScope{
		AgentInstanceID: "doubao-qq", Transport: "qq_official",
		TransportInstance: "qq-doubao", ConversationRef: "group-1", ThreadKey: "topic-1",
	}
	otherInstance := base
	otherInstance.AgentInstanceID = "xiaoman-qq"
	otherConnector := base
	otherConnector.TransportInstance = "qq-xiaoman"

	if base.conversationKey() == otherInstance.conversationKey() {
		t.Fatal("agent instances must not share a conversation scope")
	}
	if base.conversationKey() == otherConnector.conversationKey() {
		t.Fatal("connector instances must not share a conversation scope")
	}
}

func TestRuntimeScopeSeparatesSendersAndMemories(t *testing.T) {
	base := runtimeScope{
		AgentInstanceID: "doubao-qq", Transport: "qq_official",
		TransportInstance: "qq-doubao", ConversationRef: "group-1", SenderRef: "member-a",
	}
	otherSender := base
	otherSender.SenderRef = "member-b"

	if base.senderKey() == otherSender.senderKey() {
		t.Fatal("members must not share a sender scope")
	}
	if base.memoryKey("doubao", "member", "member-a") == base.memoryKey("xiaoman", "member", "member-a") {
		t.Fatal("personas must not share a memory scope")
	}
}

func TestRuntimeScopeUsesLegacyDefaultAndLengthPrefixes(t *testing.T) {
	legacy := runtimeScope{Transport: " QQ_OFFICIAL ", ConversationRef: "group|one"}
	canonical := runtimeScope{AgentInstanceID: legacyAgentInstanceID, Transport: "qq_official", ConversationRef: "group|one"}
	if legacy.conversationKey() != canonical.conversationKey() {
		t.Fatal("empty instance must map to the legacy default scope")
	}

	a := runtimeScope{AgentInstanceID: "a|b", TransportInstance: "c"}
	b := runtimeScope{AgentInstanceID: "a", TransportInstance: "b|c"}
	if a.conversationKey() == b.conversationKey() {
		t.Fatal("scope delimiters must not allow key collisions")
	}
}

func TestRuntimeScopeMemoryCompatibilityAndIsolation(t *testing.T) {
	legacy := runtimeScope{
		AgentInstanceID: legacyAgentInstanceID, Transport: "qq_official",
		TransportInstance: "qq-main", ConversationRef: "group-1", SenderRef: "user-1",
	}
	if got := legacy.memoryConversationRef(); got != "group-1" {
		t.Fatalf("legacy conversation key = %q", got)
	}
	if got := legacy.userMemoryRef(); got != "user-1" {
		t.Fatalf("legacy user key = %q", got)
	}
	migrated := legacy
	migrated.AgentInstanceID = "doubao-qq"
	migrated.MemoryNamespace = legacyAgentInstanceID
	if got := migrated.memoryConversationRef(); got != "group-1" {
		t.Fatalf("migrated legacy namespace lost conversation history: %q", got)
	}
	if got := migrated.userMemoryRef(); got != "user-1" {
		t.Fatalf("migrated legacy namespace lost user history: %q", got)
	}

	doubao := legacy
	doubao.AgentInstanceID = "doubao-qq"
	xiaoman := legacy
	xiaoman.AgentInstanceID = "xiaoman-qq"
	if doubao.memoryConversationRef() == xiaoman.memoryConversationRef() {
		t.Fatal("different instances share a conversation memory key")
	}
	if doubao.userMemoryRef() == xiaoman.userMemoryRef() {
		t.Fatal("different instances share a user memory key")
	}
}
