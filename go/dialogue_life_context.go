package main

import (
	"strings"
	"time"
)

// dialogueLifeContextTTL deliberately stays short.  This is a continuity hint
// for the next media turn, not a durable fact about a real person.
const dialogueLifeContextTTL = 10 * time.Minute

type dialogueLifeContext struct {
	Scene      string
	Activity   string
	Action     string
	SourceID   string
	OccurredAt time.Time
}

// recentAssistantLifeContext extracts an established fictional scene from a
// recent assistant message. The caller scopes retrieval by runtime instance's
// memory conversation and persona; this helper additionally checks the target
// member and thread. A later own user turn invalidates the old moment, except
// for the current media request itself.
func recentAssistantLifeContext(events []RecalledGroupEvent, personaID, senderRef, threadKey, currentEventID string, now time.Time) (dialogueLifeContext, bool) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	for index := len(events) - 1; index >= 0; index-- {
		event := events[index]
		if event.ID == currentEventID {
			continue
		}
		if event.Role == "user" && event.SenderRef == senderRef && (threadKey == "" || event.ThreadKey == threadKey) {
			return dialogueLifeContext{}, false
		}
		if event.Role != "assistant" || (personaID != "" && event.PersonaID != personaID) {
			continue
		}
		if senderRef != "" && event.ReplyToSenderRef != senderRef {
			continue
		}
		if threadKey != "" && event.ThreadKey != threadKey {
			continue
		}
		occurred := event.OccurredAt.UTC()
		if occurred.IsZero() || occurred.After(now) || now.Sub(occurred) > dialogueLifeContextTTL {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(event.UntrustedText))
		// The latest scoped assistant turn owns the current fictional moment.
		// If it has no affirmative scene, a prior scene must not leak through a
		// later topic change.
		if text == "" || len([]rune(text)) > 500 || containsAnyText(text, []string{"生成失败", "尚未完成", "稍后更新", "无法发送"}) {
			return dialogueLifeContext{}, false
		}
		for _, clause := range socialOwnClauses(text) {
			if !dialogueLifeClauseAffirmative(clause) {
				continue
			}
			for _, moment := range visualLifestyleMoments {
				if !containsAnyText(clause, strings.Split(moment.markers, "|")) {
					continue
				}
				return dialogueLifeContext{
					Scene: moment.scene, Activity: moment.activity, Action: moment.action,
					SourceID: event.ID, OccurredAt: occurred,
				}, true
			}
		}
		return dialogueLifeContext{}, false
	}
	return dialogueLifeContext{}, false
}

func dialogueLifeClauseAffirmative(clause string) bool {
	clause = strings.TrimSpace(clause)
	if clause == "" || strings.ContainsAny(clause, "?？") || strings.Contains(clause, "吗") || visualClauseNegated(clause) {
		return false
	}
	if strings.HasPrefix(clause, "你") || strings.HasPrefix(clause, "他") || strings.HasPrefix(clause, "她") {
		return false
	}
	return strings.ContainsAny(clause, "我") || strings.HasPrefix(clause, "在") ||
		containsAnyText(clause, []string{"正在", "已经", "刚刚", "就在", "这会儿", "此刻"})
}

// dialogueLifeContextRelevant requires a conversational bridge. A plain
// repeated "来张自拍" must keep normal scene rotation; words such as "刚才"
// or "顺便" make the latest textual scene relevant to this media request.
func dialogueLifeContextRelevant(prompt string, context dialogueLifeContext) bool {
	if context.Scene == "" || context.OccurredAt.IsZero() {
		return false
	}
	prompt = strings.ToLower(strings.TrimSpace(prompt))
	if containsAnyText(prompt, []string{"换个话题", "换一个话题", "另外一件事", "说点别的", "新话题", "新任务"}) {
		return false
	}
	return prompt != "" && containsAnyText(prompt, []string{"刚才", "现在", "这里", "顺便", "接着", "就在", "此刻", "刚刚"})
}

// applyDialogueLifeContext only fills unspecified visual variables. Explicit
// scene or outfit requests always win, and no identity or media source is
// copied from the dialogue text.
func applyDialogueLifeContext(plan *visualGenerationPlan, context dialogueLifeContext) {
	if plan == nil || !dialogueLifeContextRelevant(plan.UserPrompt, context) || plan.Variables == nil {
		return
	}
	constraints := visualConstraintSubjects(plan.UserPrompt)
	if !visualSceneSpecified(constraints) && !visualLifestyleActionSpecified(constraints) {
		plan.Variables["scene"] = context.Scene
		plan.Variables["activity"] = context.Activity
		plan.Variables["action"] = context.Action
		plan.Variables["dialogueLifeContext"] = "承接同一会话最近十分钟内已建立的角色虚构情境；当前明确要求优先"
	}
}
