package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

const mgmtLegacyQQPlatformID = "qq_official"

var mgmtEnvironmentRefPattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{2,119}$`)

type mgmtIntegration struct {
	ID        string         `json:"id"`
	Config    map[string]any `json:"config"`
	UpdatedAt *string        `json:"updatedAt"`
}

var mgmtIntegrationFields = map[string]map[string]struct{}{
	"channel_runtime": coreFieldSet(
		"mode", "captureUnaddressedGroups", "deliveryPollSeconds",
	),
	"qq_official": coreFieldSet(
		"enabled", "groupC2C", "guildDirectMessage", "credentialConfigured",
		"platformId", "appid", "adminOpenIds", "credentialRefs",
	),
	"provider_policy": coreFieldSet(
		"providerId", "providerType", "apiBase", "credentialRef", "defaultModel",
		"fallbackModels", "streaming", "webSearch", "providerRetries", "maxAgentSteps",
		"toolCallTimeoutSeconds", "credentialConfigured",
	),
	"message_policy": coreFieldSet(
		"segmentedReplyEnabled",
		"segmentMinChars", "segmentMaxChars", "maxReplySegments", "segmentMinDelaySeconds",
		"segmentMaxDelaySeconds", "toolProgressEnabled", "toolProgressSearchEnabled", "toolProgressSearchMessages",
		"toolProgressImageMessages", "toolProgressPhotoMessages", "toolCompletionImageMessages", "toolProgressVideoMessages",
		"toolCompletionVideoMessages", "toolProgressDocumentMessages", "toolCompletionDocumentMessages",
	),
	"content_boundary_policy": coreFieldSet(
		"enabled", "sexualAction", "violenceAction", "abuseAction", "provocationAction",
		"sexualTriggers", "violenceTriggers", "abuseTriggers", "provocationTriggers",
		"sexualContextExceptions", "violenceContextExceptions", "abuseContextExceptions",
		"sexualReplies", "violenceReplies", "abuseReplies", "provocationReplies",
		"modelInstruction",
	),
	"group_chat_policy": coreFieldSet(
		"enabled", "enabledGroups", "initialProbability", "afterReplyProbability",
		"probabilityDurationSeconds", "decisionProviderId", "decisionIncludePersona",
		"decisionTimeoutSeconds", "decisionPromptMode", "decisionExtraPrompt",
		"replyPromptMode", "replyExtraPrompt", "atLinkMaxMessages", "atLinkMaxSeconds",
		"concurrentMode", "smartBatchHintEnabled", "smartMergeWaitSeconds",
		"smartMaxBatchSize", "smartClaimDelaySeconds", "concurrentWaitMaxLoops",
		"concurrentWaitIntervalSeconds", "groupWaitWindowEnabled", "maxContextMessages",
		"includeTimestamp", "includeSenderInfo", "triggerKeywords", "keywordSmartMode",
		"commandPrefixes", "messageQualityEnabled", "questionBoost", "waterReduce",
		"replyDensityEnabled", "replyDensityWindowSeconds", "replyDensityMaxReplies",
		"replyDensitySoftLimitRatio", "replyDensityAiHint", "ignoreAtOthers",
		"ignoreAtOthersMode", "ignoreAtAll", "duplicateFilterEnabled", "participationMode", "proactiveChatEnabled",
		"ignoreLowValueMedia", "lowValueMediaMarkers", "lowValueMinTextChars",
		"typingSimulatorEnabled", "typingSpeedCharsPerSecond", "typingMaxDelaySeconds",
		"imageProcessingEnabled", "imageScope", "imageProviderId", "imagePrompt",
		"imageTimeoutSeconds", "maxImagesPerMessage", "imageCacheEnabled",
		"proactiveCooldownSeconds", "suppressProactiveWhileBusy",
	),
	"companion_policy": coreFieldSet(
		"enabled", "enabledGroups", "enableModelRouting", "chatModel", "taskModel",
		"complexMessageChars", "collectTopicState",
		"summaryIntervalMessages", "summaryWindowMessages",
		"topicTtlHours",
		"contextMessagesPerPrompt", "contextTokenBudget", "maxMessagesPerGroup", "messageRetentionHours",
		"coldRecallEnabled", "coldRecallScanMessages", "coldRecallMaxMessages",
	),
	"grok_policy": coreFieldSet(
		"enabled", "apiBase", "credentialRef", "searchConnectionId", "searchSummaryEndpointId", "searchModel", "imageModel", "imageEditModel", "videoModel",
		"searchConnectionIds", "mediaConnectionIds",
		"searchSummaryMaxChars", "searchMaxSources", "searchIncludeSourceLinks",
		"videoTimeoutSeconds", "ttsEnabled", "ttsApiBase", "ttsCredentialRef",
		"ttsVoiceId", "ttsLanguage", "ttsPersonaIds", "ttsAlways",
		"ttsTriggerKeywords", "ttsMaxChars", "ttsTimeoutSeconds",
		"learningWorkerEnabled", "learningPollSeconds",
	),
	"memory_policy": coreFieldSet(
		"enabled", "autoCapture", "retrievalLimit", "maxMemoriesPerScope",
		"allowGroupSharedMemory", "relationshipPulseEnabled", "outputFeedbackEnabled",
		"memoryResonanceEnabled", "circadianAwarenessEnabled", "longingEnabled",
		"dreamMemoryIsolation", "pulseMinInteractions", "rhythmWindowEvents",
		"timezoneOffsetMinutes",
	),
	"retrieval_policy": coreFieldSet(
		"enabled", "mode", "vectorAlgorithm", "dimensions", "keywordWeight",
		"vectorWeight", "minimumSimilarity", "topK", "candidateK", "chunkSize", "chunkOverlap",
		"embeddingEndpointId", "rerankEndpointId",
	),
	"document_policy": coreFieldSet(
		"enabled", "imageUnderstandingEnabled", "allowText", "allowDocx",
		"allowPdf", "allowPptx", "allowXlsx", "maxFileMb", "maxExtractChars",
		"extractionTimeoutSeconds", "recentAttachmentTtlSeconds", "recentAttachmentMax",
		"recentAttachmentContextMax",
		"mediaRetentionHours", "mediaGCIntervalMinutes",
	),
	"ops_policy": coreFieldSet(
		"enabled", "statusUrl", "statusTitle", "credentialRef", "requestTimeoutSeconds",
		"cardPageUrl", "cardBrowserUrl", "cardCaptureTimeoutSeconds",
		"commandAliases", "timelinePoints", "evaluationWindowMinutes", "evaluationPollSeconds",
		"groupMultipliers", "showMultiplierNote",
		"radarEnabled", "radarUrl", "radarCommandAliases", "radarMinimumSamples",
		"radarFamilyOrder", "radarRecommendationOrder", "radarRecommendations",
	),
	"affiliate_policy": coreFieldSet(
		"enabled", "summaryUrl", "registerBaseUrl", "credentialRef", "requestTimeoutSeconds",
		"pointsPerPaidInvitee", "bindAliases", "linkAliases", "pointsAliases",
	),
	"image_policy": coreFieldSet(
		"enabled", "providerId", "model", "credentialRef", "defaultImageCount",
		"maxImageCount", "maxImagesPerMessage", "timeoutSeconds", "maxRetryAttempts",
		"maxConcurrentTasks", "maxQueuedTasks", "rateLimitSeconds", "dailyLimitEnabled",
		"dailyLimitCount", "maxImageSizeMb", "promptAuditEnabled", "promptAuditProviderId",
		"historyEnabled", "historyLimit", "historyRetentionDays",
		"visualDirectorEnabled", "visualUseTimeContext", "visualTimezone", "selfieTypes",
	),
}

func mgmtDecodeObjectJSON(raw string) map[string]any {
	value := map[string]any{}
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func (s *coreConfigStore) mgmtStoredIntegration(id string) (mgmtIntegration, bool, error) {
	var value mgmtIntegration
	var configJSON, updatedAt string
	err := s.db.QueryRow(`
		SELECT id, config_json, updated_at FROM integration_settings WHERE id = ?
	`, id).Scan(&value.ID, &configJSON, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	value.Config = mgmtDecodeObjectJSON(configJSON)
	value.UpdatedAt = &updatedAt
	return value, true, nil
}

func (s *coreConfigStore) mgmtIntegration(id string) (mgmtIntegration, bool, error) {
	if id == "channel_platforms" {
		value, err := s.mgmtPlatformRegistry()
		return value, err == nil, err
	}
	if _, ok := mgmtIntegrationFields[id]; !ok {
		return mgmtIntegration{}, false, nil
	}
	return s.mgmtStoredIntegration(id)
}

func (s *coreConfigStore) mgmtIntegrations() ([]mgmtIntegration, error) {
	rows, err := s.db.Query("SELECT id, config_json, updated_at FROM integration_settings ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtIntegration{}
	for rows.Next() {
		var value mgmtIntegration
		var configJSON, updatedAt string
		if err = rows.Scan(&value.ID, &configJSON, &updatedAt); err != nil {
			return nil, err
		}
		value.Config = mgmtDecodeObjectJSON(configJSON)
		value.UpdatedAt = &updatedAt
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	registry, err := s.mgmtPlatformRegistry()
	if err != nil {
		return nil, err
	}
	values = append(values, registry)
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

func mgmtPrivateHost(host string) bool {
	host = strings.Trim(strings.ToLower(host), "[]")
	for _, allowed := range []string{
		"localhost", "erdai-agent-core", "erdai-core", "erdai-embedding", "erdai-monitor-browser", "grok2api-local",
	} {
		if host == allowed {
			return true
		}
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func mgmtHTTPURL(raw, name string, privateOnly, securePublic, allowQuery bool) (string, error) {
	raw, err := normalizeCoreText(raw, name, 500, true)
	if err != nil {
		return "", err
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", coreInvalid(name + " must be an HTTP URL without credentials")
	}
	if parsed.RawQuery != "" && !allowQuery {
		return "", coreInvalid(name + " cannot contain a query")
	}
	if parsed.Fragment != "" {
		return "", coreInvalid(name + " cannot contain a fragment")
	}
	private := mgmtPrivateHost(parsed.Hostname())
	if privateOnly && !private {
		return "", coreInvalid(name + " must use a private Core host")
	}
	if securePublic && parsed.Scheme != "https" && !private {
		return "", coreInvalid(name + " must use HTTPS or a private host")
	}
	return strings.TrimRight(raw, "/"), nil
}

func mgmtEnvironmentReference(value, name string) (string, error) {
	value, err := normalizeCoreText(value, name, 120, true)
	if err != nil {
		return "", err
	}
	if !mgmtEnvironmentRefPattern.MatchString(value) {
		return "", coreInvalid(name + " must reference an environment variable")
	}
	return value, nil
}

func mgmtFiniteNumber(value any, name string) (float64, error) {
	number, ok := value.(float64)
	if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, coreInvalid(name + " must be a finite number")
	}
	return number, nil
}

func mgmtStringArray(value any, name string, maxItems, maxLength int) ([]string, error) {
	raw, ok := value.([]any)
	if !ok || len(raw) > maxItems {
		return nil, coreInvalid(name + " is invalid")
	}
	stringsValue := make([]string, 0, len(raw))
	for _, item := range raw {
		text, ok := item.(string)
		if !ok {
			return nil, coreInvalid(name + " must contain strings")
		}
		stringsValue = append(stringsValue, text)
	}
	return normalizeCoreStrings(stringsValue, name, maxItems, maxLength)
}

func mgmtSameJSONKind(value, current any, name string) error {
	switch current.(type) {
	case bool:
		if _, ok := value.(bool); !ok {
			return coreInvalid(name + " must be a boolean")
		}
	case string:
		if _, ok := value.(string); !ok {
			return coreInvalid(name + " must be a string")
		}
	case float64:
		if _, err := mgmtFiniteNumber(value, name); err != nil {
			return err
		}
	case []any:
		if _, ok := value.([]any); !ok {
			return coreInvalid(name + " must be an array")
		}
	case map[string]any:
		if _, ok := value.(map[string]any); !ok {
			return coreInvalid(name + " must be an object")
		}
	}
	return nil
}

func mgmtValidateIntegration(id string, current, input map[string]any) (map[string]any, error) {
	allowed := mgmtIntegrationFields[id]
	if allowed == nil {
		return nil, mgmtNotFound("integration")
	}
	for field := range input {
		if _, ok := allowed[field]; !ok {
			return nil, coreInvalid("unsupported " + strings.ReplaceAll(id, "_", " ") + " fields: " + field)
		}
	}
	next := make(map[string]any, len(current)+len(input))
	for field, value := range current {
		next[field] = value
	}
	for field, value := range input {
		if currentValue, ok := current[field]; ok {
			if err := mgmtSameJSONKind(value, currentValue, field); err != nil {
				return nil, err
			}
		}
		switch field {
		case "credentialRef":
			reference, ok := value.(string)
			if !ok {
				return nil, coreInvalid("credentialRef must be a string")
			}
			normalized, err := mgmtEnvironmentReference(reference, "credentialRef")
			if err != nil {
				return nil, err
			}
			value = normalized
		case "credentialRefs":
			references, ok := value.(map[string]any)
			if !ok {
				return nil, coreInvalid("credentialRefs must be an object")
			}
			if id != "qq_official" {
				return nil, coreInvalid("credentialRefs is not supported")
			}
			normalized := map[string]any{}
			for slot, raw := range references {
				if slot != "secret" {
					return nil, coreInvalid("unsupported qq_official credential references: " + slot)
				}
				reference, ok := raw.(string)
				if !ok {
					return nil, coreInvalid(slot + " must reference an environment variable")
				}
				reference, err := mgmtEnvironmentReference(reference, slot)
				if err != nil {
					return nil, err
				}
				normalized[slot] = reference
			}
			value = normalized
		case "apiBase", "statusUrl", "radarUrl", "cardPageUrl", "cardBrowserUrl":
			raw, ok := value.(string)
			if !ok {
				return nil, coreInvalid(field + " must be a string")
			}
			if field == "cardPageUrl" && strings.TrimSpace(raw) == "" {
				value = ""
				break
			}
			allowQuery := id == "ops_policy" && (field == "statusUrl" || field == "radarUrl" || field == "cardPageUrl")
			privateOnly := id == "ops_policy" && field == "cardBrowserUrl"
			normalized, err := mgmtHTTPURL(raw, field, privateOnly, id == "grok_policy" || id == "ops_policy", allowQuery)
			if err != nil {
				return nil, err
			}
			value = normalized
		case "commandAliases", "radarCommandAliases":
			aliases, err := mgmtStringArray(value, field, 20, 40)
			if err != nil {
				return nil, err
			}
			for _, alias := range aliases {
				if !strings.HasPrefix(alias, "/") {
					return nil, coreInvalid(field + " must use slash-prefixed commands")
				}
			}
			value = aliases
		case "groupMultipliers":
			record, ok := value.(map[string]any)
			if !ok || len(record) > 100 {
				return nil, coreInvalid("groupMultipliers must be an object")
			}
			for key, raw := range record {
				if _, err := normalizeCoreText(key, "groupMultipliers key", 120, true); err != nil {
					return nil, err
				}
				number, err := mgmtFiniteNumber(raw, "groupMultipliers."+key)
				if err != nil || number < 0 || number > 100 {
					return nil, coreInvalid("groupMultipliers." + key + " must be between 0 and 100")
				}
			}
		case "evaluationWindowMinutes":
			number, err := mgmtFiniteNumber(value, field)
			if err != nil || number < 1 || number > 60 || number != float64(int(number)) {
				return nil, coreInvalid("evaluationWindowMinutes must be an integer between 1 and 60")
			}
		case "evaluationPollSeconds":
			number, err := mgmtFiniteNumber(value, field)
			if err != nil || number < 15 || number > 300 || number != float64(int(number)) {
				return nil, coreInvalid("evaluationPollSeconds must be an integer between 15 and 300")
			}
		case "cardCaptureTimeoutSeconds":
			number, err := mgmtFiniteNumber(value, field)
			if err != nil || number < 10 || number > 90 || number != float64(int(number)) {
				return nil, coreInvalid("cardCaptureTimeoutSeconds must be an integer between 10 and 90")
			}
		case "radarRecommendations":
			record, ok := value.(map[string]any)
			if !ok || len(record) > 20 {
				return nil, coreInvalid("radarRecommendations must be an object")
			}
			for key, raw := range record {
				if _, err := normalizeCoreText(key, "radarRecommendations key", 80, true); err != nil {
					return nil, err
				}
				family, ok := raw.(string)
				if !ok {
					return nil, coreInvalid("radarRecommendations values must be strings")
				}
				if _, err := normalizeCoreText(family, "radarRecommendations value", 120, true); err != nil {
					return nil, err
				}
			}
		case "radarMinimumSamples":
			number, err := mgmtFiniteNumber(value, field)
			if err != nil || number < 1 || number > 10000 {
				return nil, coreInvalid("radarMinimumSamples must be between 1 and 10000")
			}
			value = number
		}
		switch typed := value.(type) {
		case string:
			normalized, normalizeErr := normalizeCoreText(typed, field, 10000, false)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			value = normalized
		case []any:
			normalized, normalizeErr := mgmtStringArray(typed, field, 100, 500)
			if normalizeErr != nil {
				return nil, normalizeErr
			}
			value = normalized
		case float64:
			if math.IsNaN(typed) || math.IsInf(typed, 0) || typed < 0 || typed > 1_000_000_000 {
				return nil, coreInvalid(field + " is out of range")
			}
		}
		next[field] = value
	}
	if mode, ok := next["mode"].(string); id == "channel_runtime" && ok && mode != "off" && mode != "shadow" && mode != "active" {
		return nil, coreInvalid("mode is not supported")
	}
	if id == "group_chat_policy" {
		if rawMode, ok := input["participationMode"]; ok {
			mode, modeOK := rawMode.(string)
			if !modeOK || !validParticipationMode(mode) || strings.TrimSpace(mode) == "" {
				return nil, coreInvalid("participationMode must be addressed_only, adaptive, or social")
			}
			next["participationMode"] = strings.TrimSpace(mode)
			next["proactiveChatEnabled"] = mode != "addressed_only"
		} else if rawEnabled, ok := input["proactiveChatEnabled"]; ok {
			if enabled, enabledOK := rawEnabled.(bool); enabledOK {
				next["participationMode"] = map[bool]string{true: "adaptive", false: "addressed_only"}[enabled]
			}
		}
		for _, field := range []string{"initialProbability", "afterReplyProbability", "questionBoost", "waterReduce", "replyDensitySoftLimitRatio"} {
			if value, ok := next[field]; ok {
				number, err := mgmtFiniteNumber(value, field)
				if err != nil || number < 0 || number > 1 {
					return nil, coreInvalid(field + " must be between 0 and 1")
				}
			}
		}
		if value, ok := next["lowValueMinTextChars"]; ok {
			number, err := mgmtFiniteNumber(value, "lowValueMinTextChars")
			if err != nil || number < 0 || number > 20 || math.Trunc(number) != number {
				return nil, coreInvalid("lowValueMinTextChars must be an integer between 0 and 20")
			}
		}
	}
	if id == "message_policy" {
		minimum, minOK := next["segmentMinChars"].(float64)
		maximum, maxOK := next["segmentMaxChars"].(float64)
		if minOK && maxOK && minimum > maximum {
			return nil, coreInvalid("segmentMinChars cannot exceed segmentMaxChars")
		}
		minDelay, minDelayOK := next["segmentMinDelaySeconds"].(float64)
		maxDelay, maxDelayOK := next["segmentMaxDelaySeconds"].(float64)
		if minDelayOK && maxDelayOK && minDelay > maxDelay {
			return nil, coreInvalid("segmentMinDelaySeconds cannot exceed segmentMaxDelaySeconds")
		}
	}
	if id == "content_boundary_policy" {
		for _, field := range []string{"sexualAction", "violenceAction", "abuseAction", "provocationAction"} {
			action, ok := next[field].(string)
			if !ok || !validContentBoundaryAction(action) {
				return nil, coreInvalid(field + " must be model, refuse, counter, or ignore")
			}
		}
		for _, field := range []string{"sexualReplies", "violenceReplies", "abuseReplies", "provocationReplies"} {
			values, ok := next[field].([]any)
			if !ok || len(values) == 0 {
				return nil, coreInvalid(field + " requires at least one reply")
			}
		}
	}
	if id == "image_policy" {
		defaultCount, defaultOK := next["defaultImageCount"].(float64)
		maximumCount, maxOK := next["maxImageCount"].(float64)
		if defaultOK && maxOK && defaultCount > maximumCount {
			return nil, coreInvalid("defaultImageCount cannot exceed maxImageCount")
		}
	}
	if id == "companion_policy" {
		for _, limit := range []struct {
			field        string
			minimum, max float64
		}{
			{"contextMessagesPerPrompt", 6, 200},
			{"contextTokenBudget", 512, 100000},
			{"maxMessagesPerGroup", 100, 100000},
			{"messageRetentionHours", 1, 43800},
			{"coldRecallScanMessages", 100, 20000},
			{"coldRecallMaxMessages", 1, 30},
			{"summaryIntervalMessages", 2, 200},
			{"summaryWindowMessages", 2, 200},
			{"topicTtlHours", 1, 720},
		} {
			value, err := mgmtFiniteNumber(next[limit.field], limit.field)
			if err != nil || math.Trunc(value) != value || value < limit.minimum || value > limit.max {
				return nil, coreInvalid(limit.field + " is out of range")
			}
		}
	}
	if id == "memory_policy" {
		for _, limit := range []struct {
			field        string
			minimum, max float64
		}{
			{"retrievalLimit", 1, 50}, {"maxMemoriesPerScope", 1, 100000},
			{"pulseMinInteractions", 3, 100}, {"rhythmWindowEvents", 10, 500},
			{"timezoneOffsetMinutes", -720, 840},
		} {
			value, err := mgmtFiniteNumber(next[limit.field], limit.field)
			if err != nil || math.Trunc(value) != value || value < limit.minimum || value > limit.max {
				return nil, coreInvalid(limit.field + " is out of range")
			}
		}
	}
	if id == "retrieval_policy" {
		mode, _ := next["mode"].(string)
		if mode != "keyword" && mode != "vector" && mode != "hybrid" {
			return nil, coreInvalid("retrieval mode is not supported")
		}
		algorithm, _ := next["vectorAlgorithm"].(string)
		if algorithm != "local_hash" && algorithm != "remote_embedding" {
			return nil, coreInvalid("vectorAlgorithm is not supported")
		}
		for _, field := range []string{"keywordWeight", "vectorWeight", "minimumSimilarity"} {
			value, err := mgmtFiniteNumber(next[field], field)
			if err != nil || value < 0 || value > 1 {
				return nil, coreInvalid(field + " must be between 0 and 1")
			}
		}
		keywordWeight, _ := mgmtFiniteNumber(next["keywordWeight"], "keywordWeight")
		vectorWeight, _ := mgmtFiniteNumber(next["vectorWeight"], "vectorWeight")
		if keywordWeight+vectorWeight == 0 {
			return nil, coreInvalid("at least one retrieval weight must be greater than zero")
		}
		for _, field := range []string{"dimensions", "topK"} {
			value, err := mgmtFiniteNumber(next[field], field)
			if err != nil || math.Trunc(value) != value {
				return nil, coreInvalid(field + " must be an integer")
			}
			if field == "dimensions" && (value < 64 || value > 2048) {
				return nil, coreInvalid("dimensions must be between 64 and 2048")
			}
			if field == "topK" && (value < 1 || value > 20) {
				return nil, coreInvalid("topK must be between 1 and 20")
			}
		}
	}
	if id == "document_policy" {
		for _, limit := range []struct {
			field        string
			minimum, max float64
		}{
			{"maxFileMb", 1, 100}, {"maxExtractChars", 1000, 200000},
			{"extractionTimeoutSeconds", 1, 300},
			{"recentAttachmentTtlSeconds", 0, 31536000}, {"recentAttachmentMax", 1, 5000},
			{"recentAttachmentContextMax", 1, 50},
			{"mediaRetentionHours", 24, 43800}, {"mediaGCIntervalMinutes", 15, 1440},
		} {
			value, err := mgmtFiniteNumber(next[limit.field], limit.field)
			if err != nil || math.Trunc(value) != value || value < limit.minimum || value > limit.max {
				return nil, coreInvalid(limit.field + " is out of range")
			}
		}
	}
	return next, nil
}

func (s *coreConfigStore) mgmtUpdateIntegration(r *http.Request, id string) (mgmtIntegration, bool, error) {
	if id == "channel_platforms" {
		value, err := s.mgmtUpdatePlatformRegistry(r)
		return value, true, err
	}
	current, found, err := s.mgmtStoredIntegration(id)
	if err != nil || !found {
		return current, found, err
	}
	allowed := mgmtIntegrationFields[id]
	if allowed == nil {
		return current, false, nil
	}
	var input map[string]any
	fields, err := decodeCoreObject(r, allowed, strings.ReplaceAll(id, "_", " "), &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	next, err := mgmtValidateIntegration(id, current.Config, input)
	if err != nil {
		return current, true, err
	}
	updatedAt := mgmtNow()
	_, err = s.db.Exec("UPDATE integration_settings SET config_json = ?, updated_at = ? WHERE id = ?", mgmtJSON(next), updatedAt, id)
	if err != nil {
		return current, true, err
	}
	if err = s.mgmtAudit("update", "integration", id, mgmtFieldNames(fields)); err != nil {
		return current, true, err
	}
	value, _, err := s.mgmtStoredIntegration(id)
	return value, true, err
}

func (s *coreConfigStore) handleManagementIntegrations(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/integrations" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		values, err := s.mgmtIntegrations()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	id, err := mgmtPathID(path, "/api/v1/integrations/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtIntegration(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("integration")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateIntegration(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("integration")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type mgmtPlatformTemplate struct {
	Type               string
	DisplayName        string
	SupportsStreaming  bool
	SupportsWebhook    bool
	HasDefaultTemplate bool
	Defaults           map[string]any
	CredentialFields   []string
	Options            map[string][]string
}

var mgmtPlatformTemplates = []mgmtPlatformTemplate{
	{Type: "aiocqhttp", DisplayName: "OneBot v11", Defaults: map[string]any{"ws_reverse_host": "0.0.0.0", "ws_reverse_port": float64(6199), "admin_ids": ""}, CredentialFields: []string{"ws_reverse_token"}},
	{Type: "dingtalk", DisplayName: "DingTalk", SupportsStreaming: true, Defaults: map[string]any{"client_id": "", "card_template_id": "", "dingtalk_api_base_url": "https://api.dingtalk.com", "dingtalk_oapi_base_url": "https://oapi.dingtalk.com", "dingtalk_public_base_url": "", "dingtalk_media_host": "0.0.0.0", "dingtalk_media_port": float64(6202), "admin_ids": ""}, CredentialFields: []string{"client_secret"}},
	{Type: "discord", DisplayName: "Discord", Defaults: map[string]any{"discord_proxy": "", "discord_command_register": true, "discord_activity_name": "", "discord_allow_bot_messages": false, "discord_api_base_url": "https://discord.com/api/v10", "discord_gateway_url": "", "admin_ids": ""}, CredentialFields: []string{"discord_token"}},
	{Type: "kook", DisplayName: "KOOK", SupportsStreaming: true, Defaults: map[string]any{"kook_api_base_url": "https://www.kookapp.cn/api/v3", "kook_gateway_url": "", "kook_reconnect_delay": float64(1), "kook_max_reconnect_delay": float64(60), "kook_max_retry_delay": float64(60), "kook_heartbeat_interval": float64(30), "kook_heartbeat_timeout": float64(6), "kook_max_heartbeat_failures": float64(3), "kook_max_consecutive_failures": float64(5), "admin_ids": ""}, CredentialFields: []string{"kook_bot_token"}},
	{Type: "lark", DisplayName: "Lark", SupportsStreaming: true, SupportsWebhook: true, Defaults: map[string]any{"app_id": "", "domain": "https://open.feishu.cn", "lark_connection_mode": "socket", "lark_public_base_url": "", "lark_webhook_host": "0.0.0.0", "lark_webhook_port": float64(6201), "lark_webhook_path": "/erdai-lark-webhook/callback", "admin_ids": ""}, CredentialFields: []string{"app_secret", "webhook_uuid", "lark_encrypt_key", "lark_verification_token"}, Options: map[string][]string{"lark_connection_mode": {"socket", "webhook"}}},
	{Type: "line", DisplayName: "LINE", SupportsWebhook: true, Defaults: map[string]any{"unified_webhook_mode": true, "callback_server_host": "0.0.0.0", "port": float64(6193), "line_api_base_url": "https://api.line.me", "line_data_base_url": "https://api-data.line.me", "line_public_base_url": "", "line_video_preview_url": "", "admin_ids": ""}, CredentialFields: []string{"channel_access_token", "channel_secret", "webhook_uuid"}},
	{Type: "mattermost", DisplayName: "Mattermost", Defaults: map[string]any{"mattermost_url": "https://chat.example.com", "mattermost_reconnect_delay": float64(5), "admin_ids": ""}, CredentialFields: []string{"mattermost_bot_token"}},
	{Type: "misskey", DisplayName: "Misskey", Defaults: map[string]any{"misskey_instance_url": "https://misskey.example", "max_message_length": float64(3000), "misskey_default_visibility": "public", "misskey_local_only": false, "misskey_enable_chat": true, "misskey_allow_insecure_downloads": false, "misskey_download_timeout": float64(15), "misskey_download_chunk_size": float64(65536), "misskey_max_download_bytes": nil, "misskey_enable_file_upload": true, "misskey_upload_concurrency": float64(3), "misskey_upload_folder": "", "admin_ids": ""}, CredentialFields: []string{"misskey_token"}, Options: map[string][]string{"misskey_default_visibility": {"public", "home", "followers"}}},
	{Type: "qq_official", DisplayName: "QQ Official", SupportsStreaming: true, Defaults: map[string]any{"appid": "", "admin_openids": "", "enable_group_c2c": true, "enable_guild_direct_message": true}, CredentialFields: []string{"secret"}},
	{Type: "qq_official_webhook", DisplayName: "QQ Official Webhook", SupportsStreaming: true, SupportsWebhook: true, Defaults: map[string]any{"appid": "", "is_sandbox": false, "unified_webhook_mode": true, "callback_server_host": "0.0.0.0", "port": float64(6196), "admin_openids": "", "enable_group_c2c": true, "enable_guild_direct_message": true}, CredentialFields: []string{"secret", "webhook_uuid"}},
	{Type: "satori", DisplayName: "Satori", Defaults: map[string]any{"satori_api_base_url": "http://localhost:5140/satori/v1", "satori_endpoint": "ws://localhost:5140/satori/v1/events", "satori_auto_reconnect": true, "satori_heartbeat_interval": float64(10), "satori_reconnect_delay": float64(5), "admin_ids": ""}, CredentialFields: []string{"satori_token"}},
	{Type: "slack", DisplayName: "Slack", SupportsStreaming: true, SupportsWebhook: true, Defaults: map[string]any{"slack_connection_mode": "socket", "slack_api_base_url": "https://slack.com/api", "slack_public_base_url": "", "unified_webhook_mode": true, "slack_webhook_host": "0.0.0.0", "slack_webhook_port": float64(6197), "slack_webhook_path": "/erdai-slack-webhook/callback", "admin_ids": ""}, CredentialFields: []string{"bot_token", "app_token", "signing_secret", "webhook_uuid"}, Options: map[string][]string{"slack_connection_mode": {"socket", "webhook"}}},
	{Type: "telegram", DisplayName: "Telegram", SupportsStreaming: true, Defaults: map[string]any{"start_message": "你好，我是豆包。", "telegram_api_base_url": "https://api.telegram.org/bot", "telegram_file_base_url": "https://api.telegram.org/file/bot", "telegram_public_base_url": "", "telegram_media_host": "0.0.0.0", "telegram_media_port": float64(6204), "telegram_command_register": true, "telegram_command_auto_refresh": true, "telegram_command_register_interval": float64(300), "telegram_polling_restart_delay": float64(5), "admin_ids": ""}, CredentialFields: []string{"telegram_token"}},
	{Type: "telegram_user", DisplayName: "Telegram User (MTProto)", SupportsStreaming: true, Defaults: map[string]any{"telegram_user_api_id": float64(0), "telegram_user_phone": "", "telegram_user_session_dir": "/app/data/telegram-sessions", "telegram_user_device_model": "ErDai Agent", "telegram_user_system_version": "linux", "telegram_user_app_version": "0.9.4", "telegram_user_lang_code": "zh-hans", "telegram_user_receive_groups": true, "telegram_user_receive_private": true, "telegram_user_download_media": true, "telegram_user_proactive_enabled": false, "telegram_user_allow_chats": "", "admin_ids": ""}, CredentialFields: []string{"api_hash"}},
	{Type: "webchat", DisplayName: "WebChat", SupportsStreaming: true, HasDefaultTemplate: false, Defaults: map[string]any{"webchat_link_path": "", "webchat_present_type": "fullscreen", "webchat_host": "127.0.0.1", "webchat_port": float64(6200), "webchat_api_path": "/api/v1/webchat", "admin_ids": ""}, CredentialFields: []string{"webchat_token"}, Options: map[string][]string{"webchat_present_type": {"fullscreen", "embedded"}}},
	{Type: "wecom", DisplayName: "WeCom", SupportsWebhook: true, Defaults: map[string]any{"corpid": "", "agent_id": "", "kf_name": "", "api_base_url": "https://qyapi.weixin.qq.com/cgi-bin/", "wecom_public_base_url": "", "unified_webhook_mode": true, "callback_server_host": "0.0.0.0", "port": float64(6195), "admin_ids": ""}, CredentialFields: []string{"secret", "token", "encoding_aes_key", "webhook_uuid"}},
	{Type: "wecom_ai_bot", DisplayName: "WeCom AI Bot", SupportsStreaming: true, SupportsWebhook: true, Defaults: map[string]any{"wecom_ai_bot_connection_mode": "long_connection", "wecom_ai_bot_name": "", "wecomaibot_ws_bot_id": "", "wecomaibot_init_respond_text": "", "wecomaibot_friend_message_welcome_text": "", "only_use_webhook_url_to_send": false, "wecomaibot_ws_url": "wss://openws.work.weixin.qq.com", "wecomaibot_heartbeat_interval": float64(30), "wecom_ai_public_base_url": "", "unified_webhook_mode": true, "callback_server_host": "0.0.0.0", "port": float64(6198), "admin_ids": ""}, CredentialFields: []string{"wecomaibot_ws_secret", "wecomaibot_token", "wecomaibot_encoding_aes_key", "msg_push_webhook_url", "webhook_uuid"}, Options: map[string][]string{"wecom_ai_bot_connection_mode": {"long_connection", "webhook"}}},
	{Type: "weixin_oc", DisplayName: "Weixin Personal", Defaults: map[string]any{"weixin_oc_base_url": "https://ilinkai.weixin.qq.com", "weixin_oc_cdn_base_url": "https://novac2c.cdn.weixin.qq.com/c2c", "weixin_oc_bot_type": "3", "weixin_oc_qr_poll_interval": float64(1), "weixin_oc_long_poll_timeout_ms": float64(35000), "weixin_oc_api_timeout_ms": float64(120000), "weixin_oc_public_base_url": "", "weixin_oc_media_host": "127.0.0.1", "weixin_oc_media_port": float64(6203), "admin_ids": ""}, CredentialFields: []string{}},
	{Type: "weixin_official_account", DisplayName: "Weixin Official Account", SupportsWebhook: true, Defaults: map[string]any{"appid": "", "api_base_url": "https://api.weixin.qq.com/cgi-bin/", "weixin_public_base_url": "", "unified_webhook_mode": true, "callback_server_host": "0.0.0.0", "port": float64(6194), "active_send_mode": false, "passive_reply_placeholder": "我还在想。等下再回一句，我把结果给你。", "admin_ids": ""}, CredentialFields: []string{"secret", "token", "encoding_aes_key", "webhook_uuid"}},
}

func init() {
	for index := range mgmtPlatformTemplates {
		if mgmtPlatformTemplates[index].Type != "webchat" {
			mgmtPlatformTemplates[index].HasDefaultTemplate = true
		}
	}
}

func mgmtPlatformTemplateFor(platformType string) (mgmtPlatformTemplate, bool) {
	for _, template := range mgmtPlatformTemplates {
		if template.Type == platformType {
			return template, true
		}
	}
	return mgmtPlatformTemplate{}, false
}

type mgmtPlatformCatalogItem struct {
	Type               string              `json:"type"`
	DisplayName        string              `json:"displayName"`
	SupportsStreaming  bool                `json:"supportsStreaming"`
	SupportsWebhook    bool                `json:"supportsWebhook"`
	HasDefaultTemplate bool                `json:"hasDefaultTemplate"`
	SettingFields      []string            `json:"settingFields"`
	SettingDefaults    map[string]any      `json:"settingDefaults"`
	CredentialFields   []string            `json:"credentialFields"`
	SettingOptions     map[string][]string `json:"settingOptions"`
}

func mgmtPlatformCatalog() []mgmtPlatformCatalogItem {
	values := make([]mgmtPlatformCatalogItem, 0, len(mgmtPlatformTemplates))
	for _, template := range mgmtPlatformTemplates {
		fields := make([]string, 0, len(template.Defaults))
		defaults := map[string]any{}
		for field, value := range template.Defaults {
			fields = append(fields, field)
			defaults[field] = value
		}
		sort.Strings(fields)
		credentials := append([]string{}, template.CredentialFields...)
		options := map[string][]string{}
		for field, values := range template.Options {
			options[field] = append([]string{}, values...)
		}
		values = append(values, mgmtPlatformCatalogItem{
			Type: template.Type, DisplayName: template.DisplayName,
			SupportsStreaming: template.SupportsStreaming, SupportsWebhook: template.SupportsWebhook,
			HasDefaultTemplate: template.HasDefaultTemplate, SettingFields: fields,
			SettingDefaults: defaults, CredentialFields: credentials, SettingOptions: options,
		})
	}
	return values
}

type mgmtPlatform struct {
	ID                   string         `json:"id"`
	Type                 string         `json:"type"`
	DisplayName          string         `json:"displayName"`
	Enabled              bool           `json:"enabled"`
	CredentialConfigured bool           `json:"credentialConfigured"`
	Settings             map[string]any `json:"settings"`
	CredentialRefs       map[string]any `json:"credentialRefs"`
	CompatibilitySource  *string        `json:"compatibilitySource"`
	CreatedAt            *string        `json:"createdAt"`
	UpdatedAt            *string        `json:"updatedAt"`
}

func scanMgmtPlatform(scanner interface{ Scan(...any) error }) (mgmtPlatform, error) {
	var value mgmtPlatform
	var enabled, credentialConfigured int
	var settingsJSON, credentialRefsJSON, createdAt, updatedAt string
	err := scanner.Scan(
		&value.ID, &value.Type, &value.DisplayName, &enabled, &credentialConfigured,
		&settingsJSON, &credentialRefsJSON, &createdAt, &updatedAt,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.CredentialConfigured = credentialConfigured == 1
	value.Settings = mgmtDecodeObjectJSON(settingsJSON)
	value.CredentialRefs = mgmtDecodeObjectJSON(credentialRefsJSON)
	value.CreatedAt = &createdAt
	value.UpdatedAt = &updatedAt
	return value, nil
}

func (s *coreConfigStore) mgmtLegacyQQ() (mgmtPlatform, bool, error) {
	integration, found, err := s.mgmtStoredIntegration(mgmtLegacyQQPlatformID)
	if err != nil || !found {
		return mgmtPlatform{}, found, err
	}
	compatibility := "integration_settings.qq_official"
	displayName, _ := integration.Config["platformId"].(string)
	enabled, _ := integration.Config["enabled"].(bool)
	credentialConfigured, _ := integration.Config["credentialConfigured"].(bool)
	groupC2C, _ := integration.Config["groupC2C"].(bool)
	guildDM, _ := integration.Config["guildDirectMessage"].(bool)
	appid, _ := integration.Config["appid"].(string)
	adminOpenIDs, _ := integration.Config["adminOpenIds"].(string)
	credentialRefs, _ := integration.Config["credentialRefs"].(map[string]any)
	if credentialRefs == nil {
		credentialRefs = map[string]any{}
	}
	return mgmtPlatform{
		ID: mgmtLegacyQQPlatformID, Type: "qq_official", DisplayName: displayName,
		Enabled: enabled, CredentialConfigured: credentialConfigured,
		Settings:       map[string]any{"appid": appid, "admin_openids": adminOpenIDs, "enable_group_c2c": groupC2C, "enable_guild_direct_message": guildDM},
		CredentialRefs: credentialRefs, CompatibilitySource: &compatibility,
		UpdatedAt: integration.UpdatedAt,
	}, true, nil
}

func (s *coreConfigStore) mgmtPlatform(id string) (mgmtPlatform, bool, error) {
	id, err := mgmtIdentifier(id, "id")
	if err != nil {
		return mgmtPlatform{}, false, err
	}
	if id == mgmtLegacyQQPlatformID {
		return s.mgmtLegacyQQ()
	}
	value, err := scanMgmtPlatform(s.db.QueryRow(`
		SELECT id, platform_type, display_name, enabled, credential_configured,
			settings_json, credential_refs_json, created_at, updated_at
		FROM platform_integrations WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtPlatforms() ([]mgmtPlatform, error) {
	rows, err := s.db.Query(`
		SELECT id, platform_type, display_name, enabled, credential_configured,
			settings_json, credential_refs_json, created_at, updated_at
		FROM platform_integrations ORDER BY id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtPlatform{}
	for rows.Next() {
		value, err := scanMgmtPlatform(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if legacy, found, err := s.mgmtLegacyQQ(); err != nil {
		return nil, err
	} else if found {
		values = append(values, legacy)
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

type mgmtPlatformPayload struct {
	ID                   *string         `json:"id"`
	Type                 *string         `json:"type"`
	DisplayName          *string         `json:"displayName"`
	Enabled              *bool           `json:"enabled"`
	CredentialConfigured *bool           `json:"credentialConfigured"`
	Settings             *map[string]any `json:"settings"`
	CredentialRefs       *map[string]any `json:"credentialRefs"`
}

var (
	mgmtPlatformCreateFields = coreFieldSet("id", "type", "displayName", "enabled", "credentialConfigured", "settings", "credentialRefs")
	mgmtPlatformUpdateFields = coreFieldSet("displayName", "enabled", "credentialConfigured", "settings", "credentialRefs")
)

func mgmtPlatformSettings(template mgmtPlatformTemplate, input *map[string]any, current map[string]any) (map[string]any, error) {
	next := map[string]any{}
	for field, value := range current {
		next[field] = value
	}
	if input == nil {
		return next, nil
	}
	if *input == nil {
		return nil, coreInvalid("settings must be an object")
	}
	for field, raw := range *input {
		defaultValue, ok := template.Defaults[field]
		if !ok {
			return nil, coreInvalid("unsupported " + template.Type + " settings: " + field)
		}
		if defaultValue == nil {
			if raw != nil {
				number, err := mgmtFiniteNumber(raw, field)
				if err != nil || number < 0 || math.Trunc(number) != number {
					return nil, coreInvalid(field + " must be null or a non-negative integer")
				}
			}
		} else if err := mgmtSameJSONKind(raw, defaultValue, field); err != nil {
			return nil, err
		}
		if number, ok := raw.(float64); ok && (number < 0 || number > 1_000_000_000) {
			return nil, coreInvalid(field + " is out of range")
		}
		if text, ok := raw.(string); ok {
			text = mgmtBoundedText(text, 1000)
			if text != "" && (strings.HasSuffix(field, "url") || strings.HasSuffix(field, "endpoint") || strings.HasSuffix(field, "proxy") || strings.HasSuffix(field, "domain")) {
				parsed, err := url.ParseRequestURI(text)
				if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
					return nil, coreInvalid(field + " must be a URL without credentials")
				}
				allowed := map[string]bool{"http": true, "https": true, "ws": true, "wss": true}
				if strings.HasSuffix(field, "proxy") {
					allowed["socks"] = true
					allowed["socks5"] = true
				}
				if !allowed[parsed.Scheme] {
					return nil, coreInvalid(field + " must be a URL without credentials")
				}
			}
			raw = text
		}
		if options := template.Options[field]; len(options) > 0 {
			text, ok := raw.(string)
			matched := false
			for _, option := range options {
				matched = matched || text == option
			}
			if !ok || !matched {
				return nil, coreInvalid(field + " is not supported")
			}
		}
		next[field] = raw
	}
	return next, nil
}

func mgmtPlatformCredentialRefs(template mgmtPlatformTemplate, input *map[string]any, current map[string]any) (map[string]any, error) {
	next := map[string]any{}
	for field, value := range current {
		next[field] = value
	}
	if input == nil {
		return next, nil
	}
	if *input == nil {
		return nil, coreInvalid("credentialRefs must be an object")
	}
	allowed := map[string]struct{}{}
	for _, field := range template.CredentialFields {
		allowed[field] = struct{}{}
	}
	for field, raw := range *input {
		if _, ok := allowed[field]; !ok {
			return nil, coreInvalid("unsupported " + template.Type + " credential references: " + field)
		}
		text, ok := raw.(string)
		if !ok {
			return nil, coreInvalid(field + " must reference an environment variable")
		}
		text, err := mgmtEnvironmentReference(text, field)
		if err != nil {
			return nil, err
		}
		next[field] = text
	}
	return next, nil
}

func mgmtPlatformValues(input mgmtPlatformPayload, current *mgmtPlatform) (mgmtPlatform, error) {
	value := mgmtPlatform{Settings: map[string]any{}, CredentialRefs: map[string]any{}}
	if current != nil {
		value = *current
	}
	var template mgmtPlatformTemplate
	var ok bool
	if current == nil {
		if input.Type == nil {
			return value, coreInvalid("type is required")
		}
		value.Type = strings.TrimSpace(*input.Type)
		template, ok = mgmtPlatformTemplateFor(value.Type)
		if !ok {
			return value, coreInvalid("platform type is not supported")
		}
		value.DisplayName = template.DisplayName
		for field, defaultValue := range template.Defaults {
			value.Settings[field] = defaultValue
		}
	} else {
		template, ok = mgmtPlatformTemplateFor(value.Type)
		if !ok {
			return value, coreInvalid("platform type is not supported")
		}
	}
	var err error
	if input.DisplayName != nil {
		value.DisplayName, err = normalizeCoreText(*input.DisplayName, "displayName", 120, true)
		if err != nil {
			return value, err
		}
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if input.CredentialConfigured != nil {
		value.CredentialConfigured = *input.CredentialConfigured
	}
	value.Settings, err = mgmtPlatformSettings(template, input.Settings, value.Settings)
	if err != nil {
		return value, err
	}
	value.CredentialRefs, err = mgmtPlatformCredentialRefs(template, input.CredentialRefs, value.CredentialRefs)
	return value, err
}

func (s *coreConfigStore) mgmtCreatePlatform(r *http.Request) (mgmtPlatform, error) {
	var input mgmtPlatformPayload
	fields, err := decodeCoreObject(r, mgmtPlatformCreateFields, "platform integration", &input)
	if err != nil {
		return mgmtPlatform{}, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return mgmtPlatform{}, err
	}
	id := ""
	if input.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = mgmtIdentifier(*input.ID, "id")
	}
	if err != nil {
		return mgmtPlatform{}, err
	}
	if _, found, err := s.mgmtPlatform(id); err != nil {
		return mgmtPlatform{}, err
	} else if found {
		return mgmtPlatform{}, coreInvalid("platform integration id already exists")
	}
	value, err := mgmtPlatformValues(input, nil)
	if err != nil {
		return mgmtPlatform{}, err
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO platform_integrations (
			id, platform_type, display_name, enabled, credential_configured,
			settings_json, credential_refs_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.Type, value.DisplayName, boolInt(value.Enabled), boolInt(value.CredentialConfigured),
		mgmtJSON(value.Settings), mgmtJSON(value.CredentialRefs), now, now)
	if err != nil {
		return mgmtPlatform{}, mgmtConstraintError(err, "platform integration id already exists")
	}
	if err = s.mgmtAudit("create", "platform_integration", id, mgmtFieldNames(fields)); err != nil {
		return mgmtPlatform{}, err
	}
	value, _, err = s.mgmtPlatform(id)
	return value, err
}

func (s *coreConfigStore) mgmtUpdatePlatform(r *http.Request, id string) (mgmtPlatform, bool, error) {
	current, found, err := s.mgmtPlatform(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtPlatformPayload
	fields, err := decodeCoreObject(r, mgmtPlatformUpdateFields, "platform integration", &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	value, err := mgmtPlatformValues(input, &current)
	if err != nil {
		return current, true, err
	}
	updatedAt := mgmtNow()
	if id == mgmtLegacyQQPlatformID {
		config := map[string]any{
			"enabled": value.Enabled, "groupC2C": value.Settings["enable_group_c2c"],
			"guildDirectMessage":   value.Settings["enable_guild_direct_message"],
			"credentialConfigured": value.CredentialConfigured, "platformId": value.DisplayName,
			"appid": value.Settings["appid"], "adminOpenIds": value.Settings["admin_openids"],
			"credentialRefs": value.CredentialRefs,
		}
		_, err = s.db.Exec("UPDATE integration_settings SET config_json = ?, updated_at = ? WHERE id = ?", mgmtJSON(config), updatedAt, id)
		if err == nil {
			err = s.mgmtAudit("update", "integration", id, mgmtFieldNames(fields))
		}
	} else {
		_, err = s.db.Exec(`
			UPDATE platform_integrations SET display_name = ?, enabled = ?, credential_configured = ?,
				settings_json = ?, credential_refs_json = ?, updated_at = ? WHERE id = ?
		`, value.DisplayName, boolInt(value.Enabled), boolInt(value.CredentialConfigured),
			mgmtJSON(value.Settings), mgmtJSON(value.CredentialRefs), updatedAt, id)
		if err == nil {
			err = s.mgmtAudit("update", "platform_integration", id, mgmtFieldNames(fields))
		}
	}
	if err != nil {
		return current, true, err
	}
	value, _, err = s.mgmtPlatform(id)
	return value, true, err
}

func (s *coreConfigStore) mgmtPlatformRegistry() (mgmtIntegration, error) {
	instances, err := s.mgmtPlatforms()
	if err != nil {
		return mgmtIntegration{}, err
	}
	var updatedAt *string
	for _, instance := range instances {
		if instance.UpdatedAt != nil && (updatedAt == nil || *instance.UpdatedAt > *updatedAt) {
			value := *instance.UpdatedAt
			updatedAt = &value
		}
	}
	serialized, _ := json.Marshal(instances)
	var instanceValues []any
	_ = json.Unmarshal(serialized, &instanceValues)
	catalogJSON, _ := json.Marshal(mgmtPlatformCatalog())
	var catalogValues []any
	_ = json.Unmarshal(catalogJSON, &catalogValues)
	return mgmtIntegration{
		ID: "channel_platforms", UpdatedAt: updatedAt,
		Config: map[string]any{"runtimeVersion": erdaiRuntimeVersion, "catalog": catalogValues, "instances": instanceValues},
	}, nil
}

func (s *coreConfigStore) mgmtUpdatePlatformRegistry(r *http.Request) (mgmtIntegration, error) {
	var body struct {
		Instances []json.RawMessage `json:"instances"`
	}
	fields, err := decodeCoreObject(r, coreFieldSet("instances"), "agent platforms", &body)
	if err != nil {
		return mgmtIntegration{}, err
	}
	if _, ok := fields["instances"]; !ok || body.Instances == nil || len(body.Instances) > 64 {
		return mgmtIntegration{}, coreInvalid("instances must be an array with at most 64 items")
	}
	type registryItem struct {
		raw   json.RawMessage
		id    string
		found bool
	}
	items := make([]registryItem, 0, len(body.Instances))
	seen := map[string]struct{}{}
	for _, raw := range body.Instances {
		var input mgmtPlatformPayload
		var fieldMap map[string]json.RawMessage
		if json.Unmarshal(raw, &fieldMap) != nil || fieldMap == nil || json.Unmarshal(raw, &input) != nil {
			return mgmtIntegration{}, coreInvalid("platform integration must be an object")
		}
		for field := range fieldMap {
			if _, ok := mgmtPlatformCreateFields[field]; !ok {
				return mgmtIntegration{}, coreInvalid("unsupported platform integration fields: " + field)
			}
		}
		if err = mgmtRejectNullFields(fieldMap); err != nil {
			return mgmtIntegration{}, err
		}
		if input.ID == nil || input.Type == nil {
			return mgmtIntegration{}, coreInvalid("platform integration id and type are required")
		}
		id, err := mgmtIdentifier(*input.ID, "id")
		if err != nil {
			return mgmtIntegration{}, err
		}
		if _, duplicate := seen[id]; duplicate {
			return mgmtIntegration{}, coreInvalid("platform integration ids must be unique")
		}
		seen[id] = struct{}{}
		current, found, err := s.mgmtPlatform(id)
		if err != nil {
			return mgmtIntegration{}, err
		}
		if found && current.Type != strings.TrimSpace(*input.Type) {
			return mgmtIntegration{}, coreInvalid("platform integration type cannot be changed")
		}
		if found {
			if _, err = mgmtPlatformValues(input, &current); err != nil {
				return mgmtIntegration{}, err
			}
		} else if _, err = mgmtPlatformValues(input, nil); err != nil {
			return mgmtIntegration{}, err
		}
		items = append(items, registryItem{raw: raw, id: id, found: found})
	}
	for _, item := range items {
		if item.found {
			changes := map[string]any{}
			if json.Unmarshal(item.raw, &changes) != nil {
				return mgmtIntegration{}, coreInvalid("platform integration must be an object")
			}
			delete(changes, "id")
			delete(changes, "type")
			requestBody := mgmtJSON(changes)
			request, _ := http.NewRequest(http.MethodPut, "/", strings.NewReader(requestBody))
			if _, _, err = s.mgmtUpdatePlatform(request, item.id); err != nil {
				return mgmtIntegration{}, err
			}
		} else {
			request, _ := http.NewRequest(http.MethodPost, "/", strings.NewReader(string(item.raw)))
			if _, err = s.mgmtCreatePlatform(request); err != nil {
				return mgmtIntegration{}, err
			}
		}
	}
	return s.mgmtPlatformRegistry()
}

func (s *coreConfigStore) handleManagementPlatforms(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/platforms/catalog" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		mgmtWriteData(w, http.StatusOK, mgmtPlatformCatalog())
		return nil
	}
	if path == "/api/v1/platforms" {
		switch r.Method {
		case http.MethodGet:
			values, err := s.mgmtPlatforms()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreatePlatform(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/platforms/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtPlatform(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("platform integration")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdatePlatform(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("platform integration")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		id, err = mgmtIdentifier(id, "id")
		if err != nil {
			return err
		}
		if id == mgmtLegacyQQPlatformID {
			return coreInvalid("legacy QQ integration cannot be deleted")
		}
		result, err := s.db.Exec("DELETE FROM platform_integrations WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "platform integration"); err != nil {
			return err
		}
		if err = s.mgmtAudit("delete", "platform_integration", id, nil); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}
