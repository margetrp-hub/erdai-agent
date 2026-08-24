package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	contentBoundaryActionModel   = "model"
	contentBoundaryActionRefuse  = "refuse"
	contentBoundaryActionCounter = "counter"
	contentBoundaryActionIgnore  = "ignore"
)

type contentBoundaryPolicy struct {
	Enabled                   bool     `json:"enabled"`
	SexualAction              string   `json:"sexualAction"`
	ViolenceAction            string   `json:"violenceAction"`
	AbuseAction               string   `json:"abuseAction"`
	ProvocationAction         string   `json:"provocationAction"`
	SexualTriggers            []string `json:"sexualTriggers"`
	ViolenceTriggers          []string `json:"violenceTriggers"`
	AbuseTriggers             []string `json:"abuseTriggers"`
	ProvocationTriggers       []string `json:"provocationTriggers"`
	SexualContextExceptions   []string `json:"sexualContextExceptions"`
	ViolenceContextExceptions []string `json:"violenceContextExceptions"`
	AbuseContextExceptions    []string `json:"abuseContextExceptions"`
	SexualReplies             []string `json:"sexualReplies"`
	ViolenceReplies           []string `json:"violenceReplies"`
	AbuseReplies              []string `json:"abuseReplies"`
	ProvocationReplies        []string `json:"provocationReplies"`
	ModelInstruction          string   `json:"modelInstruction"`
}

type contentBoundaryDecision struct {
	Category string
	Action   string
	Replies  []string
}

func decodeContentBoundaryPolicy(raw []byte) (contentBoundaryPolicy, error) {
	var policy contentBoundaryPolicy
	if err := json.Unmarshal(raw, &policy); err != nil {
		return policy, err
	}
	if !validContentBoundaryAction(policy.SexualAction) ||
		!validContentBoundaryAction(policy.ViolenceAction) ||
		!validContentBoundaryAction(policy.AbuseAction) ||
		!validContentBoundaryAction(policy.ProvocationAction) {
		return policy, errors.New("content boundary policy contains an invalid action")
	}
	return policy, nil
}

func validContentBoundaryAction(action string) bool {
	switch strings.TrimSpace(action) {
	case contentBoundaryActionModel, contentBoundaryActionRefuse,
		contentBoundaryActionCounter, contentBoundaryActionIgnore:
		return true
	default:
		return false
	}
}

func normalizeBoundaryText(value string) string {
	var builder strings.Builder
	for _, current := range strings.ToLower(strings.TrimSpace(value)) {
		if unicode.IsLetter(current) || unicode.IsNumber(current) {
			builder.WriteRune(current)
		}
	}
	return builder.String()
}

func boundaryContainsAny(message string, values []string) bool {
	for _, value := range values {
		value = normalizeBoundaryText(value)
		if value != "" && strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func contentBoundaryMatch(message string, triggers, exceptions []string) bool {
	normalized := normalizeBoundaryText(message)
	return normalized != "" && boundaryContainsAny(normalized, triggers) &&
		!boundaryContainsAny(normalized, exceptions)
}

func evaluateContentBoundary(policy contentBoundaryPolicy, message string) (contentBoundaryDecision, bool) {
	if !policy.Enabled {
		return contentBoundaryDecision{}, false
	}
	checks := []struct {
		category   string
		action     string
		triggers   []string
		exceptions []string
		replies    []string
	}{
		{"sexual", policy.SexualAction, policy.SexualTriggers, policy.SexualContextExceptions, policy.SexualReplies},
		{"violence", policy.ViolenceAction, policy.ViolenceTriggers, policy.ViolenceContextExceptions, policy.ViolenceReplies},
		{"abuse", policy.AbuseAction, policy.AbuseTriggers, policy.AbuseContextExceptions, policy.AbuseReplies},
		{"provocation", policy.ProvocationAction, policy.ProvocationTriggers, nil, policy.ProvocationReplies},
	}
	for _, check := range checks {
		if contentBoundaryMatch(message, check.triggers, check.exceptions) {
			return contentBoundaryDecision{
				Category: check.category, Action: check.action, Replies: check.replies,
			}, true
		}
	}
	return contentBoundaryDecision{}, false
}

func chooseBoundaryReply(eventID, message string, decision contentBoundaryDecision) string {
	if len(decision.Replies) == 0 {
		if decision.Action == contentBoundaryActionCounter {
			return "有事说事。别靠嘴脏撑场面。"
		}
		return "这话题不接。换一个。"
	}
	digest := sha256.Sum256([]byte(eventID + "\x00" + decision.Category + "\x00" + message))
	index := binary.BigEndian.Uint64(digest[:8]) % uint64(len(decision.Replies))
	return strings.TrimSpace(decision.Replies[index])
}

func compileContentBoundaryPrompt(policy contentBoundaryPolicy) string {
	if !policy.Enabled {
		return ""
	}
	instruction := strings.TrimSpace(policy.ModelInstruction)
	if instruction == "" {
		instruction = "遇到色情、伤害、严重辱骂或恶意挑衅时先守住边界；不迎合、不升级冲突，不攻击家人、外貌、疾病、贫困或受保护特征。"
	}
	return fmt.Sprintf("## 1A. 内容与互动边界\n%s\nCore 已在模型调用前执行拒绝、忽略和短句反击；不得把被拦截内容改写后继续完成。", instruction)
}

func (s *coreConfigStore) contentBoundaryPolicy() (contentBoundaryPolicy, error) {
	raw, err := s.integrationRaw("content_boundary_policy")
	if err != nil {
		return contentBoundaryPolicy{}, err
	}
	return decodeContentBoundaryPolicy(raw)
}
