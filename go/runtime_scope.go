package main

import (
	"strconv"
	"strings"
)

const legacyAgentInstanceID = "legacy-default"

// runtimeScope is the canonical isolation boundary for one inbound interaction.
type runtimeScope struct {
	AgentInstanceID   string
	MemoryNamespace   string
	Transport         string
	TransportInstance string
	ConversationRef   string
	ThreadKey         string
	SenderRef         string
}

func (s runtimeScope) normalized() runtimeScope {
	s.AgentInstanceID = strings.TrimSpace(s.AgentInstanceID)
	if s.AgentInstanceID == "" {
		s.AgentInstanceID = legacyAgentInstanceID
	}
	s.MemoryNamespace = strings.TrimSpace(s.MemoryNamespace)
	if s.MemoryNamespace == "" {
		s.MemoryNamespace = s.AgentInstanceID
	}
	s.Transport = strings.ToLower(strings.TrimSpace(s.Transport))
	s.TransportInstance = strings.TrimSpace(s.TransportInstance)
	s.ConversationRef = strings.TrimSpace(s.ConversationRef)
	s.ThreadKey = strings.TrimSpace(s.ThreadKey)
	s.SenderRef = strings.TrimSpace(s.SenderRef)
	return s
}

func (s runtimeScope) conversationKey() string {
	s = s.normalized()
	return joinScopeParts(
		"memory", s.MemoryNamespace,
		"transport", s.Transport,
		"connector", s.TransportInstance,
		"conversation", s.ConversationRef,
		"thread", s.ThreadKey,
	)
}

func (s runtimeScope) senderKey() string {
	s = s.normalized()
	return s.conversationKey() + joinScopeParts("sender", s.SenderRef)
}

func (s runtimeScope) userKey() string {
	s = s.normalized()
	return joinScopeParts(
		"memory", s.MemoryNamespace,
		"transport", s.Transport,
		"connector", s.TransportInstance,
		"sender", s.SenderRef,
	)
}

func (s runtimeScope) memoryKey(personaID, kind, reference string) string {
	s = s.normalized()
	return s.conversationKey() + joinScopeParts(
		"persona", strings.TrimSpace(personaID),
		"kind", strings.TrimSpace(kind),
		"reference", strings.TrimSpace(reference),
	)
}

// Legacy traffic keeps its historic keys so the first schema rollout does not
// make existing conversations appear empty. Explicit instances are isolated.
func (s runtimeScope) memoryConversationRef() string {
	s = s.normalized()
	if s.MemoryNamespace == legacyAgentInstanceID {
		return s.ConversationRef
	}
	return s.conversationKey()
}

func (s runtimeScope) memorySenderRef() string {
	s = s.normalized()
	if s.MemoryNamespace == legacyAgentInstanceID {
		return s.SenderRef
	}
	return s.senderKey()
}

func (s runtimeScope) userMemoryRef() string {
	s = s.normalized()
	if s.MemoryNamespace == legacyAgentInstanceID {
		return s.SenderRef
	}
	return s.userKey()
}

func (s runtimeScope) groupMemoryRef() string {
	return s.memoryConversationRef()
}

func runtimeScopeFromRun(run runRecord) runtimeScope {
	return runtimeScope{
		AgentInstanceID:   run.AgentInstanceID,
		MemoryNamespace:   run.MemoryNamespace,
		Transport:         run.Transport,
		TransportInstance: run.TransportInstance,
		ConversationRef:   run.ConversationRef,
		ThreadKey:         run.ThreadKey,
		SenderRef:         run.SenderRef,
	}.normalized()
}

func runtimeScopeFromEvent(event transportEvent, agentInstanceID, memoryNamespace string) runtimeScope {
	return runtimeScope{
		AgentInstanceID:   agentInstanceID,
		MemoryNamespace:   memoryNamespace,
		Transport:         event.Transport,
		TransportInstance: event.TransportInstance,
		ConversationRef:   event.Conversation.Key,
		ThreadKey:         event.Conversation.ThreadKey,
		SenderRef:         event.Sender.Key,
	}.normalized()
}

func (a *AgentRuntime) resolvedRuntimeScope(event transportEvent) (runtimeScope, error) {
	target, err := a.configStore.resolveAgentInstance(
		event.TransportInstance, event.Transport, event.Conversation.Key,
	)
	if err != nil {
		return runtimeScope{}, err
	}
	return runtimeScopeFromEvent(event, target.InstanceID, target.MemoryNamespace), nil
}

func joinScopeParts(parts ...string) string {
	var value strings.Builder
	for _, part := range parts {
		value.WriteString(strconv.Itoa(len(part)))
		value.WriteByte(':')
		value.WriteString(part)
		value.WriteByte('|')
	}
	return value.String()
}
