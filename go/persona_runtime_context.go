package main

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type runtimeMemoryPolicy struct {
	Enabled                   bool `json:"enabled"`
	AutoCapture               bool `json:"autoCapture"`
	RetrievalLimit            int  `json:"retrievalLimit"`
	MaxMemoriesPerScope       int  `json:"maxMemoriesPerScope"`
	AllowGroupSharedMemory    bool `json:"allowGroupSharedMemory"`
	RelationshipPulseEnabled  bool `json:"relationshipPulseEnabled"`
	OutputFeedbackEnabled     bool `json:"outputFeedbackEnabled"`
	MemoryResonanceEnabled    bool `json:"memoryResonanceEnabled"`
	CircadianAwarenessEnabled bool `json:"circadianAwarenessEnabled"`
	LongingEnabled            bool `json:"longingEnabled"`
	DreamMemoryIsolation      bool `json:"dreamMemoryIsolation"`
	PulseMinInteractions      int  `json:"pulseMinInteractions"`
	RhythmWindowEvents        int  `json:"rhythmWindowEvents"`
	TimezoneOffsetMinutes     int  `json:"timezoneOffsetMinutes"`
}

type runtimeCompanionContextPolicy struct {
	Enabled                  bool   `json:"enabled"`
	EnableModelRouting       bool   `json:"enableModelRouting"`
	ChatModel                string `json:"chatModel"`
	TaskModel                string `json:"taskModel"`
	ComplexMessageChars      int    `json:"complexMessageChars"`
	CollectTopicState        bool   `json:"collectTopicState"`
	ContextMessagesPerPrompt int    `json:"contextMessagesPerPrompt"`
	ContextTokenBudget       int    `json:"contextTokenBudget"`
	SummaryIntervalMessages  int    `json:"summaryIntervalMessages"`
	SummaryWindowMessages    int    `json:"summaryWindowMessages"`
	TopicTtlHours            int    `json:"topicTtlHours"`
	MaxMessagesPerGroup      int    `json:"maxMessagesPerGroup"`
	MessageRetentionHours    int    `json:"messageRetentionHours"`
	ColdRecallEnabled        bool   `json:"coldRecallEnabled"`
	ColdRecallScanMessages   int    `json:"coldRecallScanMessages"`
	ColdRecallMaxMessages    int    `json:"coldRecallMaxMessages"`
}

type personaRuntimeContext struct {
	RelationshipStage string
	RelationshipPulse string
	DetectedEmotion   string
	RecentMessages    []string
}

var stableMemoryPatterns = []struct {
	expression *regexp.Regexp
	kind       string
	importance float64
}{
	{regexp.MustCompile(`(?:^|[，。！？\s])我(?:很|比较|最)?喜欢([^，。！？\n]{1,40})`), "preference", 0.75},
	{regexp.MustCompile(`(?:^|[，。！？\s])我不喜欢([^，。！？\n]{1,40})`), "preference", 0.75},
	{regexp.MustCompile(`(?:^|[，。！？\s])我(?:平时|一般|通常)喝([^，。！？\n]{1,30})`), "preference", 0.72},
	{regexp.MustCompile(`(?:^|[，。！？\s])我(?:平时|一般|通常)用([^，。！？\n]{1,30})`), "preference", 0.72},
	{regexp.MustCompile(`(?:^|[，。！？\s])我习惯([^，。！？\n]{1,40})`), "habit", 0.72},
	{regexp.MustCompile(`(?:^|[，。！？\s])我最近在做([^，。！？\n]{1,50})`), "project", 0.68},
	{regexp.MustCompile(`(?:^|[，。！？\s])我叫([^，。！？\n]{1,20})`), "address", 0.9},
	{regexp.MustCompile(`(?:以后|下次)(?:你)?(?:就|请)?叫我([^，。！？\n]{1,20})`), "address", 0.9},
	{regexp.MustCompile(`(?:^|[，。！？\s])我是([^，。！？\n]{1,30})`), "identity", 0.65},
}

func (a *AgentRuntime) memoryPolicy(ctx context.Context) runtimeMemoryPolicy {
	policy := runtimeMemoryPolicy{
		Enabled: true, AutoCapture: true, RetrievalLimit: 12, MaxMemoriesPerScope: 5000,
		RelationshipPulseEnabled: true, OutputFeedbackEnabled: true,
		MemoryResonanceEnabled: true, CircadianAwarenessEnabled: true,
		LongingEnabled: true, DreamMemoryIsolation: true,
		PulseMinInteractions: 5, RhythmWindowEvents: 60, TimezoneOffsetMinutes: 480,
	}
	configured := policy
	if err := a.integrationConfig(ctx, "memory_policy", &configured); err != nil {
		return policy
	}
	if configured.RetrievalLimit <= 0 {
		configured.RetrievalLimit = 12
	}
	if configured.RetrievalLimit > 50 {
		configured.RetrievalLimit = 50
	}
	if configured.MaxMemoriesPerScope <= 0 {
		configured.MaxMemoriesPerScope = 5000
	}
	if configured.MaxMemoriesPerScope > 100000 {
		configured.MaxMemoriesPerScope = 100000
	}
	if configured.PulseMinInteractions < 3 || configured.PulseMinInteractions > 100 {
		configured.PulseMinInteractions = 5
	}
	if configured.RhythmWindowEvents < 10 || configured.RhythmWindowEvents > 500 {
		configured.RhythmWindowEvents = 60
	}
	if configured.TimezoneOffsetMinutes < -720 || configured.TimezoneOffsetMinutes > 840 {
		configured.TimezoneOffsetMinutes = 480
	}
	return configured
}

func (a *AgentRuntime) companionContextPolicy(ctx context.Context) runtimeCompanionContextPolicy {
	policy := runtimeCompanionContextPolicy{
		Enabled:                  true,
		EnableModelRouting:       true,
		CollectTopicState:        true,
		ContextMessagesPerPrompt: 40,
		ComplexMessageChars:      100,
		ContextTokenBudget:       6000,
		SummaryIntervalMessages:  12,
		SummaryWindowMessages:    12,
		TopicTtlHours:            6,
		MaxMessagesPerGroup:      20000,
		MessageRetentionHours:    365 * 24,
		ColdRecallEnabled:        true,
		ColdRecallScanMessages:   5000,
		ColdRecallMaxMessages:    12,
	}
	if err := a.integrationConfig(ctx, "companion_policy", &policy); err != nil {
		return policy
	}
	if policy.ContextMessagesPerPrompt < 6 || policy.ContextMessagesPerPrompt > 200 {
		policy.ContextMessagesPerPrompt = 40
	}
	if policy.ComplexMessageChars < 40 || policy.ComplexMessageChars > 2000 {
		policy.ComplexMessageChars = 100
	}
	if policy.ContextTokenBudget < 512 || policy.ContextTokenBudget > 100000 {
		policy.ContextTokenBudget = 6000
	}
	if policy.SummaryIntervalMessages < 2 || policy.SummaryIntervalMessages > 200 {
		policy.SummaryIntervalMessages = 12
	}
	if policy.SummaryWindowMessages < 2 || policy.SummaryWindowMessages > 200 {
		policy.SummaryWindowMessages = 12
	}
	if policy.TopicTtlHours < 1 || policy.TopicTtlHours > 720 {
		policy.TopicTtlHours = 6
	}
	if policy.MaxMessagesPerGroup < 100 || policy.MaxMessagesPerGroup > 100000 {
		policy.MaxMessagesPerGroup = 20000
	}
	if policy.MessageRetentionHours < 1 || policy.MessageRetentionHours > 5*365*24 {
		policy.MessageRetentionHours = 365 * 24
	}
	if policy.ColdRecallScanMessages < 100 || policy.ColdRecallScanMessages > 20000 {
		policy.ColdRecallScanMessages = 5000
	}
	if policy.ColdRecallMaxMessages < 1 || policy.ColdRecallMaxMessages > 30 {
		policy.ColdRecallMaxMessages = 12
	}
	return policy
}

func (a *AgentRuntime) personaContext(ctx context.Context, run runRecord, message string) personaRuntimeContext {
	result := personaRuntimeContext{RelationshipStage: "新群友", DetectedEmotion: detectConversationEmotion(message)}
	if a.memory == nil {
		return result
	}
	scope := runtimeScopeFromRun(run)
	memoryConversation := scope.memoryConversationRef()
	memorySender := scope.memorySenderRef()
	policy := a.memoryPolicy(ctx)
	if state, found, err := a.memory.RelationshipWithPulse(ctx, personaConversationRef(run.PersonaID, memoryConversation), memorySender, run.PersonaID, policy); err == nil && found {
		result.RelationshipStage = fmt.Sprintf("%s（亲密度 %.0f/100）", state.Stage, state.Intimacy)
		if state.Pulse != nil && state.Pulse.Ready {
			result.RelationshipPulse = relationshipPulsePrompt(*state.Pulse)
		}
	}
	contextPolicy := a.companionContextPolicy(ctx)
	recent, err := a.memory.RecentPersonaGroupEvents(ctx, memoryConversation, run.PersonaID, contextPolicy.ContextMessagesPerPrompt+1)
	if err != nil {
		return result
	}
	recent = selectThreadContext(recent, run.EventID, contextPolicy.ContextMessagesPerPrompt)
	for _, event := range recent {
		if event.ID == run.EventID {
			continue
		}
		text := truncateRunes(strings.TrimSpace(event.UntrustedText), 500)
		if text != "" {
			result.RecentMessages = append(result.RecentMessages, conversationEventLine(event, text))
		}
	}
	if len(result.RecentMessages) > contextPolicy.ContextMessagesPerPrompt {
		result.RecentMessages = result.RecentMessages[len(result.RecentMessages)-contextPolicy.ContextMessagesPerPrompt:]
	}
	return result
}

func selectThreadContext(events []RecalledGroupEvent, currentEventID string, limit int) []RecalledGroupEvent {
	if len(events) == 0 || limit <= 0 {
		return nil
	}
	currentIndex := -1
	for index := range events {
		if events[index].ID == currentEventID {
			currentIndex = index
			break
		}
	}
	if currentIndex < 0 {
		if len(events) > limit {
			return append([]RecalledGroupEvent(nil), events[len(events)-limit:]...)
		}
		return append([]RecalledGroupEvent(nil), events...)
	}
	current := events[currentIndex]
	connectedIDs := map[string]struct{}{}
	for _, value := range []string{current.MessageID, current.ReplyToMessageID, current.ThreadKey} {
		if value != "" {
			connectedIDs[value] = struct{}{}
		}
	}
	selected := make([]RecalledGroupEvent, 0, min(limit, 16))
	ambient := 0
	for index := currentIndex; index >= 0 && len(selected) < limit; index-- {
		event := events[index]
		if current.PersonaID != "" && event.PersonaID != "" && event.PersonaID != current.PersonaID {
			continue
		}
		age := current.OccurredAt.Sub(event.OccurredAt)
		if age < 0 {
			age = -age
		}
		_, messageConnected := connectedIDs[event.MessageID]
		_, replyConnected := connectedIDs[event.ReplyToMessageID]
		_, threadConnected := connectedIDs[event.ThreadKey]
		sameSpeaker := event.SenderRef != "" && event.SenderRef == current.SenderRef
		assistant := event.Role == "assistant"
		include := event.ID == current.ID || messageConnected || replyConnected || threadConnected
		include = include || (age <= 5*time.Minute && (sameSpeaker || assistant))
		if !include && age <= 2*time.Minute && ambient < 3 {
			include = true
			ambient++
		}
		if !include {
			continue
		}
		selected = append(selected, event)
		for _, value := range []string{event.MessageID, event.ReplyToMessageID, event.ThreadKey} {
			if value != "" {
				connectedIDs[value] = struct{}{}
			}
		}
	}
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	return selected
}

func conversationEventLine(event RecalledGroupEvent, text string) string {
	label := strings.TrimSpace(event.SenderDisplayName)
	if event.Role == "assistant" {
		label = "当前角色"
	} else if label == "" {
		label = strings.TrimSpace(event.SenderRef)
	}
	if label == "" {
		label = event.Role
	}
	line := label + "：" + text
	if quoted := strings.TrimSpace(event.UntrustedQuotedText); quoted != "" {
		line += "（回复：" + truncateRunes(quoted, 120) + "）"
	}
	return line
}

func detectConversationEmotion(message string) string {
	normalized := strings.ToLower(strings.TrimSpace(message))
	for emotion, markers := range map[string][]string{
		"难过": {"难过", "伤心", "想哭", "崩溃", "委屈", "失落", "抑郁"},
		"焦虑": {"焦虑", "紧张", "害怕", "担心", "慌", "怎么办", "急死"},
		"生气": {"生气", "气死", "烦死", "离谱", "无语", "火大"},
		"开心": {"开心", "高兴", "好耶", "哈哈", "笑死", "太棒"},
		"困惑": {"不懂", "没明白", "为什么", "怎么回事", "啥意思", "看不懂"},
	} {
		for _, marker := range markers {
			if strings.Contains(normalized, marker) {
				return emotion
			}
		}
	}
	return "平静"
}

func (a *AgentRuntime) captureStableMemory(ctx context.Context, run runRecord, message string) {
	policy := a.memoryPolicy(ctx)
	contextPolicy := a.companionContextPolicy(ctx)
	if a.memory == nil || !policy.Enabled || !policy.AutoCapture ||
		!contextPolicy.Enabled || !contextPolicy.CollectTopicState || containsSensitiveMemory(message) {
		return
	}
	if policy.DreamMemoryIsolation && imaginaryMemoryContext(message) {
		return
	}
	for _, candidate := range extractStableMemories(message) {
		memoryScope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
		_, _, err := a.memory.AddMemoryWithMetadata(ctx, memoryScope, candidate.Content, MemoryMetadata{
			Source: "auto_capture", Kind: candidate.Kind,
			Confidence: 0.88, Importance: candidate.Importance,
		})
		if err == nil {
			_ = a.memory.TrimScope(ctx, memoryScope, policy.MaxMemoriesPerScope)
		}
	}
}

type stableMemoryCandidate struct {
	Content    string
	Kind       string
	Importance float64
}

func extractStableMemories(message string) []stableMemoryCandidate {
	message = strings.TrimSpace(message)
	if message == "" || len([]rune(message)) > 300 {
		return nil
	}
	result := []stableMemoryCandidate{}
	seen := map[string]struct{}{}
	for _, pattern := range stableMemoryPatterns {
		for _, match := range pattern.expression.FindAllStringSubmatch(message, -1) {
			if len(match) != 2 {
				continue
			}
			content := strings.TrimSpace(strings.Trim(match[0], "，。！？ \t\r\n"))
			if pattern.kind == "address" {
				address := strings.TrimSpace(match[1])
				if address == "" || isAddressQuestion(address) {
					continue
				}
				content = "用户希望被称为" + address
			}
			if content == "" || len([]rune(content)) > 80 || containsSensitiveMemory(content) {
				continue
			}
			if _, exists := seen[content]; exists {
				continue
			}
			seen[content] = struct{}{}
			result = append(result, stableMemoryCandidate{
				Content: content, Kind: pattern.kind, Importance: pattern.importance,
			})
		}
	}
	return result
}

func isAddressQuestion(address string) bool {
	address = strings.TrimSpace(address)
	for _, prefix := range []string{"什么", "啥", "谁", "哪个", "哪一个", "哪种", "怎么"} {
		if strings.HasPrefix(address, prefix) {
			return true
		}
	}
	return false
}

func addressRecallAnswerHint(query string, memories []RecalledMemory) string {
	hasAddress := false
	for _, memory := range memories {
		if memory.Kind == "address" && strings.TrimSpace(memory.UntrustedContent) != "" {
			hasAddress = true
			break
		}
	}
	if !hasAddress {
		return ""
	}
	if addressRecallQuestion(query) {
		return "Core 回答要求：这是称呼回忆问题。直接说出已记录的称呼，不重复首次设置时的确认或接受话术。"
	}
	return ""
}

func addressRecallQuestion(query string) bool {
	query = strings.TrimSpace(query)
	for _, marker := range []string{"叫我什么", "怎么称呼我", "如何称呼我", "我的称呼"} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func marshalRecentMessages(values []string) json.RawMessage {
	encoded, _ := json.Marshal(values)
	return encoded
}

func relationshipObservationTime(raw string) time.Time {
	value, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Now().UTC()
	}
	return value.UTC()
}
