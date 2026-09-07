package main

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"
)

var visualConstraintNegations = regexp.MustCompile(`(?i)不要|不想|不用|别|不|禁止|避免|拒绝|\b(?:no|not|without|avoid|don't)\s+`)

// Identify constrained dimensions, including exclusions. This normalized text
// is never sent to a provider or treated as a positive user instruction.
func visualConstraintSubjects(prompt string) string {
	return visualConstraintNegations.ReplaceAllString(prompt, "")
}

func explicitVisualContinuity(prompt string) bool {
	for _, clause := range visualConstraintClauses(prompt) {
		for _, marker := range []string{"同一场景", "同一个场景", "同个场景", "同一地点", "同个地方", "同一个地方", "接着拍", "刚才那张", "同一套", "同一件衣服", "衣服不换", "不换衣服", "保持场景", "场景不换"} {
			if index := strings.Index(clause, marker); index >= 0 {
				prefix := strings.TrimSpace(clause[:index])
				if !visualClauseNegated(prefix) && !visualNegationSuffix.MatchString(prefix) {
					return true
				}
			}
		}
	}
	return false
}

func applyVisualContinuity(current *visualGenerationPlan, previous *visualGenerationPlan) {
	if previous == nil || !explicitVisualContinuity(current.UserPrompt) {
		return
	}
	prompt := strings.ToLower(current.UserPrompt)
	constraints := visualConstraintSubjects(prompt)
	keys := []string{}
	if !visualSceneSpecified(constraints) && !videoHasAny(prompt, "换场景", "换个场景", "换一个场景", "换地点", "换个地方", "change location", "different scene") {
		keys = append(keys, "scene", "light")
		if !visualLifestyleActionSpecified(constraints) {
			keys = append(keys, "activity", "action")
		}
	}
	outfitRequest := strings.NewReplacer("同一件衣服", "", "同一套衣服", "", "同一套", "", "衣服不换", "", "不换衣服", "").Replace(prompt)
	if !visualPlanOutfitSpecified(outfitRequest) && !visualOutfitLengthSpecified(outfitRequest) && current.OutfitLength == previous.OutfitLength {
		keys = append(keys, "outfit")
		color, forbidden := explicitVisualColor(prompt)
		if color == "" && !forbidden[previous.Variables["primaryColor"]] {
			keys = append(keys, "primaryColor")
		}
	}
	for _, key := range keys {
		if value := previous.Variables[key]; value != "" {
			if key == "scene" && visualLifestyleActionSpecified(constraints) {
				for _, moment := range visualLifestyleMoments {
					if value == moment.scene {
						value = moment.place
						break
					}
				}
			}
			current.Variables[key] = value
		}
	}
	if len(keys) > 0 {
		current.Variables["continuity"] = "沿用同会话近期已交付媒体的场景或衣着计划；当前明确变更优先，不声称画面已被逐像素核实"
	}
}

// Only delivered, QA-selected media in the persisted run's exact scope can
// establish continuity. A saved plan alone is not evidence of a produced image.
func (a *AgentRuntime) continuingVisualPlan(ctx context.Context, run runRecord, current visualGenerationPlan, now time.Time) (*visualGenerationPlan, error) {
	if !explicitVisualContinuity(current.UserPrompt) || current.AppearanceID == "" || current.ReferenceDigest == "" || a == nil || a.db == nil {
		return nil, nil
	}
	rows, err := a.db.QueryContext(ctx, `SELECT p.output_cipher,q.output_cipher,f.kind,f.local_path,d.payload_json
		FROM agent_runs c JOIN agent_runs r ON r.id<>c.id
		AND r.transport=c.transport AND r.transport_instance=c.transport_instance
		AND r.agent_instance_id=c.agent_instance_id AND r.memory_namespace=c.memory_namespace
		AND r.conversation_ref=c.conversation_ref AND r.sender_ref=c.sender_ref
		AND r.persona_id=c.persona_id AND r.thread_key=c.thread_key
		JOIN agent_deliveries d ON d.run_id=r.id AND d.status='delivered' AND d.phase='terminal'
		JOIN agent_task_steps p ON p.run_id=r.id AND p.kind='tool' AND p.name=? AND p.status='succeeded'
		JOIN agent_task_steps q ON q.run_id=r.id AND q.kind='tool' AND q.status='succeeded'
		JOIN agent_task_artifacts f ON f.run_id=r.id AND f.step_id=q.id AND f.kind IN ('image','video')
		WHERE c.id=? AND r.state='delivered' AND q.name='media_quality:'||f.kind
		AND julianday(d.updated_at) BETWEEN julianday(?) AND julianday(?)
		AND julianday(r.created_at)<=julianday(c.created_at)
		AND (?='' OR EXISTS (SELECT 1 FROM platform_sent_delivery_parts sent WHERE sent.delivery_id=d.id AND sent.message_id=?))
		ORDER BY julianday(d.updated_at) DESC,p.created_at DESC LIMIT 32`, visualPlanName(current.AppearanceID), run.ID,
		now.Add(-30*time.Minute).UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano), run.ReplyToMessageID, run.ReplyToMessageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var planCipher, qaCipher []byte
		var kind, path, payload string
		if err = rows.Scan(&planCipher, &qaCipher, &kind, &path, &payload); err != nil {
			return nil, err
		}
		var plan visualGenerationPlan
		var qa mediaQualityReceipt
		var delivered transportDeliveryMessage
		if len(planCipher) == 0 || len(qaCipher) == 0 {
			continue
		}
		plain, decodeErr := a.decrypt(planCipher)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err = json.Unmarshal(plain, &plan); err != nil {
			return nil, err
		}
		plain, decodeErr = a.decrypt(qaCipher)
		if decodeErr != nil {
			return nil, decodeErr
		}
		if err = json.Unmarshal(plain, &qa); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(payload), &delivered); err != nil {
			return nil, err
		}
		selected := qa.SelectedAttempt
		if plan.AppearanceID != current.AppearanceID || plan.AppearanceRevision != current.AppearanceRevision ||
			plan.BindingRevision != current.BindingRevision || plan.ReferenceDigest != current.ReferenceDigest ||
			plan.UserPrompt == "" || plan.Prompt == "" || plan.OperationID == "" || plan.OperationID != qa.OperationID ||
			plan.MediaType != kind || qa.MediaType != kind || selected != plan.Attempt || selected < 0 || selected >= len(qa.Attempts) || selected >= len(qa.Results) {
			continue
		}
		if qa.Attempts[selected].Attempt != selected || qa.Attempts[selected].GenerationStatus != "completed" {
			continue
		}
		concrete := true
		for _, key := range []string{"scene", "outfit", "camera"} {
			value := strings.TrimSpace(plan.Variables[key])
			if value == "" || containsAnyText(value, []string{"用户明确", "按用户", "expired"}) {
				concrete = false
			}
		}
		if !concrete || path == "" {
			continue
		}
		for _, generated := range qa.Results[selected].Attachments {
			for _, sent := range delivered.Attachments {
				if generated.Kind == kind && sent.Kind == kind && generated.LocalPath == path && sent.LocalPath == path {
					return &plan, nil
				}
			}
		}
	}
	return nil, rows.Err()
}
