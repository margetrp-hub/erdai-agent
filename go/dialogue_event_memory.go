package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// dialogueEventRecord is a short-lived, user-scoped event. It is deliberately
// kept separate from durable facts: it is a follow-up hint, never an identity
// claim or a scheduled outbound action.
type dialogueEventRecord struct {
	Key         string     `json:"key"`
	Text        string     `json:"text"`
	Status      string     `json:"status"`
	DueAt       time.Time  `json:"dueAt,omitempty"`
	ObservedAt  time.Time  `json:"observedAt"`
	SourceEvent string     `json:"sourceEvent"`
	PromptedAt  *time.Time `json:"promptedAt,omitempty"`
}

const dialogueEventKind = "dialogue_event"

var dialogueEventKeywords = []string{
	"面试", "考试", "搬家", "旅行", "出差", "看医生", "体检", "约会", "见面", "生日", "开会", "发布",
}

// extractDialogueEvent only captures explicit first-person near-term events.
// Quoted/attributed clauses are removed by socialOwnClauses before parsing.
func extractDialogueEvent(message string, observedAt time.Time, timezoneOffsetMinutes int) (dialogueEventRecord, bool) {
	message = strings.TrimSpace(message)
	if message == "" || len([]rune(message)) > 300 || strings.ContainsAny(message, "?？") {
		return dialogueEventRecord{}, false
	}
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	var best dialogueEventRecord
	for _, clause := range socialOwnClauses(message) {
		clause = strings.TrimSpace(clause)
		if clause == "" || !strings.Contains(clause, "我") {
			continue
		}
		if containsAnyText(clause, []string{"我朋友", "我的朋友", "我同事", "我的同事", "我同学", "我的同学", "我爸", "我妈", "我哥", "我姐", "我老公", "我老婆"}) {
			continue
		}
		keyword := ""
		for _, candidate := range dialogueEventKeywords {
			if strings.Contains(clause, candidate) {
				keyword = candidate
				break
			}
		}
		if keyword == "" {
			continue
		}
		status := "active"
		for _, marker := range []string{"取消", "不用去了", "没去成"} {
			if index := strings.Index(clause, marker); index >= 0 && !dialogueEventNegated(clause[:index]) {
				status = "cancelled"
				break
			}
		}
		if status == "active" {
			for _, marker := range []string{"已经结束", "完成了", "结束了"} {
				if index := strings.Index(clause, marker); index >= 0 && !dialogueEventNegated(clause[:index]) {
					status = "completed"
					break
				}
			}
		}
		keywordIndex := strings.Index(clause, keyword)
		if status == "active" && dialogueEventNegated(clause[:keywordIndex]) {
			continue
		}
		if status == "active" && containsAnyText(clause, []string{"还没结束", "尚未结束", "没有结束", "未结束"}) {
			continue
		}
		marker := dialogueEventTimeMarker(clause)
		if marker == "" && status == "active" {
			continue
		}
		due, ok := dialogueEventDueAt(marker, observedAt, timezoneOffsetMinutes)
		if !ok && status == "active" {
			continue
		}
		best = dialogueEventRecord{
			Key: keyword, Text: strings.TrimSpace(clause), Status: status,
			DueAt: due, ObservedAt: observedAt.UTC(),
		}
		break
	}
	return best, best.Key != ""
}

func dialogueEventNegated(prefix string) bool {
	if socialNegated(prefix) {
		return true
	}
	return containsAnyText(prefix, []string{"不去", "不参加", "不做", "没去", "没有", "还没", "没", "未能", "别去", "不用"})
}

func dialogueEventTimeMarker(clause string) string {
	best, bestIndex := "", -1
	for _, marker := range []string{"后天", "明天", "今晚", "今天", "下周", "这周末", "周一", "周二", "周三", "周四", "周五", "周六", "周日", "周天"} {
		if index := strings.LastIndex(clause, marker); index > bestIndex {
			best, bestIndex = marker, index
		}
	}
	return best
}

func dialogueEventDueAt(marker string, observedAt time.Time, timezoneOffsetMinutes int) (time.Time, bool) {
	if timezoneOffsetMinutes < -720 || timezoneOffsetMinutes > 840 {
		timezoneOffsetMinutes = 480
	}
	loc := time.FixedZone("dialogue", timezoneOffsetMinutes*60)
	local := observedAt.In(loc)
	due := time.Date(local.Year(), local.Month(), local.Day(), 9, 0, 0, 0, loc)
	switch marker {
	case "今天":
		if due.Before(local) {
			due = local.Add(time.Hour)
		}
	case "今晚":
		due = time.Date(local.Year(), local.Month(), local.Day(), 20, 0, 0, 0, loc)
		if due.Before(local) {
			due = due.Add(24 * time.Hour)
		}
	case "明天":
		due = due.AddDate(0, 0, 1)
	case "后天":
		due = due.AddDate(0, 0, 2)
	case "下周", "这周末":
		due = due.AddDate(0, 0, 7)
		if marker == "这周末" {
			days := (int(time.Saturday) - int(local.Weekday()) + 7) % 7
			if days == 0 {
				days = 7
			}
			due = time.Date(local.Year(), local.Month(), local.Day()+days, 9, 0, 0, 0, loc)
		}
	default:
		weekdays := map[string]time.Weekday{"周一": time.Monday, "周二": time.Tuesday, "周三": time.Wednesday, "周四": time.Thursday, "周五": time.Friday, "周六": time.Saturday, "周日": time.Sunday, "周天": time.Sunday}
		weekday, found := weekdays[marker]
		if !found {
			return time.Time{}, false
		}
		days := (int(weekday) - int(local.Weekday()) + 7) % 7
		if days == 0 {
			days = 7
		}
		due = due.AddDate(0, 0, days)
	}
	return due.UTC(), true
}

func encodeDialogueEvent(record dialogueEventRecord) (string, error) {
	if record.Key == "" || record.Text == "" || record.Status == "" {
		return "", errors.New("dialogue event fields are required")
	}
	encoded, err := json.Marshal(record)
	return string(encoded), err
}

func decodeDialogueEvent(memory RecalledMemory) (dialogueEventRecord, bool) {
	if memory.Kind != dialogueEventKind {
		return dialogueEventRecord{}, false
	}
	var record dialogueEventRecord
	if json.Unmarshal([]byte(memory.UntrustedContent), &record) != nil || record.Key == "" {
		return dialogueEventRecord{}, false
	}
	return record, true
}

// CaptureDialogueEvent stores or updates one event in the existing encrypted
// memory ledger. Callers should pass dialogueEventScope for full isolation.
func (s *MemoryGroupStore) CaptureDialogueEvent(ctx context.Context, scope string, record dialogueEventRecord, sourceEvent string) error {
	if strings.TrimSpace(scope) == "" || record.Key == "" {
		return errors.New("dialogue event scope and key are required")
	}
	record.SourceEvent = strings.TrimSpace(sourceEvent)
	if record.ObservedAt.IsZero() {
		record.ObservedAt = s.now().UTC()
	}
	if record.Status == "active" {
		record.DueAt = record.DueAt.UTC()
	}
	memories, err := s.ListMemories(ctx, scope, maxMemoryQueryLimit)
	if err != nil {
		return err
	}
	for _, memory := range memories {
		old, ok := decodeDialogueEvent(memory)
		if !ok || old.Key != record.Key || memory.ExpiresAt != nil && !memory.ExpiresAt.After(s.now().UTC()) {
			continue
		}
		if record.DueAt.IsZero() {
			record.DueAt = old.DueAt
		}
		break
	}
	if record.DueAt.IsZero() {
		return nil
	}
	expires := record.DueAt.Add(48 * time.Hour)
	if record.Status != "active" {
		expires = s.now().UTC().Add(24 * time.Hour)
	}
	payload, err := encodeDialogueEvent(record)
	if err != nil {
		return err
	}
	for _, memory := range memories {
		old, ok := decodeDialogueEvent(memory)
		if !ok || old.Key != record.Key || memory.ExpiresAt != nil && !memory.ExpiresAt.After(s.now().UTC()) {
			continue
		}
		_, _, err = s.UpdateMemory(ctx, scope, memory.ID, payload, MemoryMetadata{
			Source: "auto_event", Kind: dialogueEventKind, Confidence: 0.82, Importance: 0.65, ExpiresAt: &expires,
		})
		return err
	}
	_, _, err = s.AddMemoryWithMetadata(ctx, scope, payload, MemoryMetadata{
		Source: "auto_event", Kind: dialogueEventKind, Confidence: 0.82, Importance: 0.65, ExpiresAt: &expires,
	})
	return err
}

func (s *MemoryGroupStore) ActiveDialogueEvents(ctx context.Context, scope string) ([]dialogueEventRecord, error) {
	memories, err := s.ListMemories(ctx, scope, maxMemoryQueryLimit)
	if err != nil {
		return nil, err
	}
	result := make([]dialogueEventRecord, 0)
	now := s.now().UTC()
	for _, memory := range memories {
		record, ok := decodeDialogueEvent(memory)
		if !ok || record.Status != "active" || memory.ExpiresAt != nil && !memory.ExpiresAt.After(now) {
			continue
		}
		result = append(result, record)
	}
	return result, nil
}

func (s *MemoryGroupStore) DialogueEventsForPrompt(ctx context.Context, scope, currentEventID, message string) ([]dialogueEventRecord, error) {
	events, err := s.ActiveDialogueEvents(ctx, scope)
	if err != nil {
		return nil, err
	}
	result := make([]dialogueEventRecord, 0, len(events))
	for _, event := range events {
		if strings.TrimSpace(currentEventID) != "" && event.SourceEvent == currentEventID {
			continue
		}
		if event.DueAt.After(s.now().UTC()) {
			continue
		}
		if !dialogueEventReturnAllowsHint(message, event.Key) {
			continue
		}
		if event.PromptedAt != nil {
			continue
		}
		now := s.now().UTC()
		event.PromptedAt = &now
		payload, encodeErr := encodeDialogueEvent(event)
		if encodeErr != nil {
			continue
		}
		memories, listErr := s.ListMemories(ctx, scope, maxMemoryQueryLimit)
		if listErr != nil {
			continue
		}
		for _, memory := range memories {
			old, ok := decodeDialogueEvent(memory)
			if ok && old.Key == event.Key {
				_, _, _ = s.UpdateMemory(ctx, scope, memory.ID, payload, MemoryMetadata{
					Source: "auto_event", Kind: dialogueEventKind, Confidence: 0.82, Importance: 0.65, ExpiresAt: memory.ExpiresAt,
				})
				result = append(result, event)
				break
			}
		}
	}
	return result, nil
}

func dialogueEventReturnAllowsHint(message, key string) bool {
	message = strings.TrimSpace(message)
	if key != "" && strings.Contains(message, key) {
		return true
	}
	if len([]rune(message)) > 20 {
		return false
	}
	return containsAnyText(message, []string{"回来了", "在吗", "早上好", "晚上好", "你好", "嗨"})
}

func dialogueEventScope(run runRecord) string {
	scope := runtimeScopeFromRun(run)
	// Keep events isolated to the same persona, instance, user and conversation.
	reference := scope.userMemoryRef() + "|conversation:" + scope.memoryConversationRef()
	return personaMemoryScope(run.PersonaID, "event", reference)
}

func dialogueEventPromptLine(event dialogueEventRecord) string {
	if event.Status != "active" || event.Text == "" {
		return ""
	}
	return "对方之前提过一件近期的事，当前话题相关时自然跟进：" + event.Text
}
