package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type personaRuntimeProfile struct {
	PersonaID               string   `json:"personaId"`
	ChatEndpointID          string   `json:"chatEndpointId,omitempty"`
	TaskEndpointID          string   `json:"taskEndpointId,omitempty"`
	DecisionEndpointID      string   `json:"decisionEndpointId,omitempty"`
	AllowedToolIDs          []string `json:"allowedToolIds,omitempty"`
	DeniedToolIDs           []string `json:"deniedToolIds,omitempty"`
	ProactiveEnabled        *bool    `json:"proactiveEnabled,omitempty"`
	ParticipationMode       string   `json:"participationMode,omitempty"`
	InitialReplyProbability *float64 `json:"initialReplyProbability,omitempty"`
	AfterReplyProbability   *float64 `json:"afterReplyProbability,omitempty"`
	ParticipationStyle      string   `json:"participationStyle,omitempty"`
	UnaddressedMode         string   `json:"unaddressedMode,omitempty"`
	AddressKeywords         []string `json:"addressKeywords,omitempty"`
	MaxReplyChars           *int     `json:"maxReplyChars,omitempty"`
	MaxReplySentences       *int     `json:"maxReplySentences,omitempty"`
	MemoryPolicy            string   `json:"memoryPolicy,omitempty"`
	SearchMode              string   `json:"searchMode,omitempty"`
	SearchReplyStyle        string   `json:"searchReplyStyle,omitempty"`
	VisualPromptOverride    string   `json:"visualPromptOverride,omitempty"`
	ExpressionPrompt        string   `json:"expressionPrompt,omitempty"`
	UpdatedAt               string   `json:"updatedAt"`
}

func personaRuntimeEndpoint(profile personaRuntimeProfile, lane string) string {
	switch lane {
	case "chat":
		return strings.TrimSpace(profile.ChatEndpointID)
	case "task", "search", "vision", "document":
		return strings.TrimSpace(profile.TaskEndpointID)
	case "decision":
		return strings.TrimSpace(profile.DecisionEndpointID)
	default:
		return ""
	}
}

func personaRuntimeModelLane(lane, message string, complexThreshold int) string {
	if lane == "chat" && len([]rune(strings.TrimSpace(message))) >= complexThreshold {
		return "task"
	}
	return lane
}

func applyPersonaRuntimeTools(policy runtimeToolPolicy, profile personaRuntimeProfile) runtimeToolPolicy {
	allowed := map[string]bool{}
	denied := map[string]bool{}
	for _, id := range profile.AllowedToolIDs {
		allowed[strings.TrimSpace(id)] = true
	}
	for _, id := range profile.DeniedToolIDs {
		denied[strings.TrimSpace(id)] = true
	}
	if len(allowed) == 0 && len(denied) == 0 {
		return policy
	}
	filtered := make([]runtimeTool, 0, len(policy.Tools))
	for _, tool := range policy.Tools {
		if denied[tool.ID] || denied[tool.Name] {
			continue
		}
		if len(allowed) > 0 && !allowed[tool.ID] && !allowed[tool.Name] {
			continue
		}
		filtered = append(filtered, tool)
	}
	policy.Tools = filtered
	return policy
}

func applyPersonaSearchMode(policy runtimeToolPolicy, profile personaRuntimeProfile, message string) runtimeToolPolicy {
	if profile.SearchMode != "explicit_only" || explicitSearchCommandIntent(strings.ToLower(strings.TrimSpace(message))) {
		return policy
	}
	filtered := make([]runtimeTool, 0, len(policy.Tools))
	for _, tool := range policy.Tools {
		if normalizeAdapterRef(tool.AdapterRef) == "grok_web_search" {
			continue
		}
		filtered = append(filtered, tool)
	}
	policy.Tools = filtered
	return policy
}

type personaRuntimeProfilePayload struct {
	ChatEndpointID          *string   `json:"chatEndpointId"`
	TaskEndpointID          *string   `json:"taskEndpointId"`
	DecisionEndpointID      *string   `json:"decisionEndpointId"`
	AllowedToolIDs          *[]string `json:"allowedToolIds"`
	DeniedToolIDs           *[]string `json:"deniedToolIds"`
	ProactiveEnabled        *bool     `json:"proactiveEnabled"`
	ParticipationMode       *string   `json:"participationMode"`
	InitialReplyProbability *float64  `json:"initialReplyProbability"`
	AfterReplyProbability   *float64  `json:"afterReplyProbability"`
	ParticipationStyle      *string   `json:"participationStyle"`
	UnaddressedMode         *string   `json:"unaddressedMode"`
	AddressKeywords         *[]string `json:"addressKeywords"`
	MaxReplyChars           *int      `json:"maxReplyChars"`
	MaxReplySentences       *int      `json:"maxReplySentences"`
	MemoryPolicy            *string   `json:"memoryPolicy"`
	SearchMode              *string   `json:"searchMode"`
	SearchReplyStyle        *string   `json:"searchReplyStyle"`
	VisualPromptOverride    *string   `json:"visualPromptOverride"`
	ExpressionPrompt        *string   `json:"expressionPrompt"`
}

var personaRuntimeProfileFields = coreFieldSet(
	"chatEndpointId", "taskEndpointId", "decisionEndpointId", "allowedToolIds", "deniedToolIds",
	"proactiveEnabled", "participationMode", "initialReplyProbability", "afterReplyProbability", "participationStyle", "unaddressedMode", "addressKeywords",
	"maxReplyChars", "maxReplySentences", "memoryPolicy", "searchMode", "searchReplyStyle", "visualPromptOverride", "expressionPrompt",
)

func (s *coreConfigStore) personaRuntimeProfile(personaID string) (personaRuntimeProfile, error) {
	personaID = strings.TrimSpace(personaID)
	value := personaRuntimeProfile{PersonaID: personaID}
	var raw, updated string
	err := s.db.QueryRow(`SELECT profile_json, updated_at FROM persona_runtime_profiles WHERE persona_id = ?`, personaID).Scan(&raw, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return value, err
	}
	value.PersonaID, value.UpdatedAt = personaID, updated
	return value, nil
}

func (s *coreConfigStore) effectivePersonaRuntimeProfile(personaID, instanceID string) (personaRuntimeProfile, error) {
	profile, err := s.personaRuntimeProfile(personaID)
	if err != nil {
		return profile, err
	}
	return s.agentInstanceRuntimeProfile(instanceID, profile)
}

func personaRuntimeAllowsAnyTool(profile personaRuntimeProfile, toolIDs ...string) bool {
	allowed := map[string]bool{}
	denied := map[string]bool{}
	for _, id := range profile.AllowedToolIDs {
		allowed[strings.TrimSpace(id)] = true
	}
	for _, id := range profile.DeniedToolIDs {
		denied[strings.TrimSpace(id)] = true
	}
	for _, id := range toolIDs {
		if denied[strings.TrimSpace(id)] {
			return false
		}
	}
	if len(allowed) == 0 {
		return true
	}
	for _, id := range toolIDs {
		if allowed[strings.TrimSpace(id)] {
			return true
		}
	}
	return false
}

func (s *coreConfigStore) personaRuntimePrompt(personaID *string, instanceID string) string {
	if personaID == nil {
		return ""
	}
	profile, err := s.effectivePersonaRuntimeProfile(*personaID, instanceID)
	if err != nil {
		return ""
	}
	parts := []string{}
	if profile.ExpressionPrompt != "" {
		parts = append(parts, "表达档案："+profile.ExpressionPrompt)
	}
	if profile.MaxReplyChars != nil && *profile.MaxReplyChars > 0 {
		parts = append(parts, "本角色默认回复不超过"+itoa(*profile.MaxReplyChars)+"字。")
	}
	if profile.MaxReplySentences != nil && *profile.MaxReplySentences > 0 {
		parts = append(parts, "本角色默认最多"+itoa(*profile.MaxReplySentences)+"句。")
	}
	if profile.MemoryPolicy != "" {
		parts = append(parts, "记忆策略："+profile.MemoryPolicy)
	}
	if profile.SearchMode != "" {
		parts = append(parts, "联网触发："+profile.SearchMode)
	}
	if profile.SearchReplyStyle != "" {
		parts = append(parts, "联网表达："+searchReplyInstruction(profile.SearchReplyStyle))
	}
	if profile.ParticipationStyle == "social" {
		parts = append(parts, "群聊定位：像普通群友一样社交。普通知识问句不是邀请，不主动讲课或给完整百科答案；没有自然反应就保持安静，被明确问到也可以坦率说不知道。")
		parts = append(parts, "需要联网时，搜索结果只是你心里的参考，不要原样播报资料、标题、百科名称或搜索过程。先用自己的口吻接住话，再用一两句说结论；不确定就自然承认，不要变成客服、百科或检索报告。")
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n\n## 当前角色运行档案\n" + strings.Join(parts, "\n")
}

func searchReplyInstruction(style string) string {
	switch strings.TrimSpace(style) {
	case "natural":
		return "先用当前角色的口吻接住话题，再把查到的结论说成自然短句；不要播报资料、标题、URL 或搜索过程。"
	case "concise":
		return "只保留最关键的结论，默认一两句；不要列结果、复述问题或加客服式开场。"
	case "source_first":
		return "先说结论，再在用户明确要出处时补来源；默认不附 URL，不把搜索结果原样转发。"
	default:
		return style
	}
}

func itoa(value int) string {
	return strconv.Itoa(value)
}

func (s *coreConfigStore) handlePersonaRuntimeRequest(w http.ResponseWriter, r *http.Request, path string) error {
	prefix := "/api/v1/personas/runtime-profiles"
	if path == prefix {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		rows, err := s.db.Query(`SELECT p.id, p.name, COALESCE(v.profile_json, '{}'), COALESCE(v.updated_at, '')
			FROM personas p LEFT JOIN persona_runtime_profiles v ON v.persona_id = p.id ORDER BY p.name`)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id, name, raw, updated string
			if err := rows.Scan(&id, &name, &raw, &updated); err != nil {
				return err
			}
			var profile map[string]any
			_ = json.Unmarshal([]byte(raw), &profile)
			items = append(items, map[string]any{"personaId": id, "name": name, "profile": profile, "updatedAt": updated})
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
		return nil
	}
	id, err := parseCorePathID(strings.TrimPrefix(path, prefix+"/"))
	if err != nil {
		return err
	}
	if r.Method == http.MethodGet {
		current, profileErr := s.personaRuntimeProfile(id)
		if profileErr != nil {
			return profileErr
		}
		mgmtWriteData(w, http.StatusOK, current)
		return nil
	}
	if r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	var payload personaRuntimeProfilePayload
	fields, err := decodeCoreObject(r, personaRuntimeProfileFields, "persona runtime profile", &payload)
	if err != nil {
		return err
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", id).Scan(&exists); err != nil {
		return err
	}
	if exists == 0 {
		return mgmtNotFound("persona")
	}
	current, err := s.personaRuntimeProfile(id)
	if err != nil {
		return err
	}
	applyPersonaRuntimeProfilePayload(&current, payload, fields)
	if current.MaxReplyChars != nil && (*current.MaxReplyChars < 20 || *current.MaxReplyChars > 1000) {
		return coreInvalid("maxReplyChars must be between 20 and 1000")
	}
	if current.MaxReplySentences != nil && (*current.MaxReplySentences < 1 || *current.MaxReplySentences > 6) {
		return coreInvalid("maxReplySentences must be between 1 and 6")
	}
	if !validOptionalProbability(current.InitialReplyProbability) || !validOptionalProbability(current.AfterReplyProbability) {
		return coreInvalid("reply probabilities must be between 0 and 1")
	}
	if !validParticipationStyle(current.ParticipationStyle) {
		return coreInvalid("participationStyle must be balanced, social, or service")
	}
	if !validParticipationMode(current.ParticipationMode) {
		return coreInvalid("participationMode must be addressed_only, adaptive, or social")
	}
	if !validUnaddressedMode(current.UnaddressedMode) {
		return coreInvalid("unaddressedMode must be off, rare, or adaptive")
	}
	if !validSearchMode(current.SearchMode) || !validSearchReplyStyle(current.SearchReplyStyle) {
		return coreInvalid("searchMode or searchReplyStyle is invalid")
	}
	encoded, _ := json.Marshal(current)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO persona_runtime_profiles(persona_id, profile_json, updated_at) VALUES (?, ?, ?)
		ON CONFLICT(persona_id) DO UPDATE SET profile_json=excluded.profile_json, updated_at=excluded.updated_at`, id, string(encoded), now)
	if err != nil {
		return err
	}
	current.UpdatedAt = now
	mgmtWriteData(w, http.StatusOK, current)
	return nil
}

func applyPersonaRuntimeProfilePayload(current *personaRuntimeProfile, payload personaRuntimeProfilePayload, fields map[string]json.RawMessage) {
	canonicalModeSet := payload.ParticipationMode != nil
	if payload.ChatEndpointID != nil {
		current.ChatEndpointID = strings.TrimSpace(*payload.ChatEndpointID)
	}
	if payload.TaskEndpointID != nil {
		current.TaskEndpointID = strings.TrimSpace(*payload.TaskEndpointID)
	}
	if payload.DecisionEndpointID != nil {
		current.DecisionEndpointID = strings.TrimSpace(*payload.DecisionEndpointID)
	}
	if payload.AllowedToolIDs != nil {
		current.AllowedToolIDs = cleanRuntimeIDs(*payload.AllowedToolIDs)
	}
	if payload.DeniedToolIDs != nil {
		current.DeniedToolIDs = cleanRuntimeIDs(*payload.DeniedToolIDs)
	}
	if !canonicalModeSet && payload.ProactiveEnabled != nil {
		current.ProactiveEnabled = payload.ProactiveEnabled
	}
	if payload.ParticipationMode != nil {
		current.ParticipationMode = strings.TrimSpace(*payload.ParticipationMode)
		// The canonical mode is the only editable switch. Keep legacy fields
		// coherent for older clients, or clear them when inheriting the public
		// policy.
		switch current.ParticipationMode {
		case "addressed_only":
			value := false
			current.ProactiveEnabled = &value
			current.UnaddressedMode = "off"
			current.ParticipationStyle = "service"
		case "adaptive":
			value := true
			current.ProactiveEnabled = &value
			current.UnaddressedMode = "adaptive"
			current.ParticipationStyle = "balanced"
		case "social":
			value := true
			current.ProactiveEnabled = &value
			current.UnaddressedMode = "adaptive"
			current.ParticipationStyle = "social"
		case "":
			current.ProactiveEnabled = nil
			current.UnaddressedMode = ""
			current.ParticipationStyle = ""
		}
	}
	if payload.InitialReplyProbability != nil {
		current.InitialReplyProbability = payload.InitialReplyProbability
	}
	if payload.AfterReplyProbability != nil {
		current.AfterReplyProbability = payload.AfterReplyProbability
	}
	if !canonicalModeSet && payload.ParticipationStyle != nil {
		current.ParticipationStyle = strings.TrimSpace(*payload.ParticipationStyle)
	}
	if !canonicalModeSet && payload.UnaddressedMode != nil {
		current.UnaddressedMode = strings.TrimSpace(*payload.UnaddressedMode)
	}
	if payload.AddressKeywords != nil {
		current.AddressKeywords = cleanRuntimeIDs(*payload.AddressKeywords)
	}
	if payload.MaxReplyChars != nil {
		current.MaxReplyChars = payload.MaxReplyChars
	}
	if payload.MaxReplySentences != nil {
		current.MaxReplySentences = payload.MaxReplySentences
	}
	if payload.MemoryPolicy != nil {
		current.MemoryPolicy = strings.TrimSpace(*payload.MemoryPolicy)
	}
	if payload.SearchMode != nil {
		current.SearchMode = strings.TrimSpace(*payload.SearchMode)
	}
	if payload.SearchReplyStyle != nil {
		current.SearchReplyStyle = strings.TrimSpace(*payload.SearchReplyStyle)
	}
	if payload.VisualPromptOverride != nil {
		current.VisualPromptOverride = strings.TrimSpace(*payload.VisualPromptOverride)
	}
	if payload.ExpressionPrompt != nil {
		current.ExpressionPrompt = strings.TrimSpace(*payload.ExpressionPrompt)
	}
	_ = fields
}

func validOptionalProbability(value *float64) bool {
	return value == nil || (*value >= 0 && *value <= 1)
}

func validParticipationStyle(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "balanced", "social", "service":
		return true
	default:
		return false
	}
}

func validParticipationMode(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "addressed_only", "adaptive", "social":
		return true
	default:
		return false
	}
}

func validUnaddressedMode(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "off", "rare", "adaptive":
		return true
	default:
		return false
	}
}

func validSearchMode(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "adaptive", "explicit_only":
		return true
	default:
		return false
	}
}

func validSearchReplyStyle(value string) bool {
	switch strings.TrimSpace(value) {
	case "", "natural", "concise", "source_first":
		return true
	default:
		return false
	}
}

func cleanRuntimeIDs(values []string) []string {
	result := []string{}
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			result, seen[value] = append(result, value), true
		}
	}
	return result
}

func (a *AgentRuntime) trustedRoleSwitch(run runRecord) bool {
	if !run.IsAdmin {
		return false
	}
	trusted := []string{}
	if raw := strings.TrimSpace(os.Getenv("ERDAI_ROLE_SWITCH_OPENIDS")); raw != "" {
		trusted = append(trusted, strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' })...)
	}
	if a.configStore != nil {
		if raw, err := a.configStore.integrationRaw("qq_official"); err == nil {
			var policy struct {
				AdminOpenIDs []string `json:"adminOpenIds"`
			}
			if json.Unmarshal(raw, &policy) == nil {
				trusted = append(trusted, policy.AdminOpenIDs...)
			}
		}
	}
	if len(trusted) == 0 {
		trusted = append(trusted, strings.FieldsFunc(strings.TrimSpace(os.Getenv("ERDAI_QQ_ADMIN_OPENIDS")), func(r rune) bool { return r == ',' || r == ';' || r == ' ' || r == '\n' || r == '\t' })...)
	}
	for _, value := range trusted {
		if strings.TrimSpace(value) == strings.TrimSpace(run.SenderRef) {
			return true
		}
	}
	return false
}

func roleCommand(message string) (string, string, bool) {
	message = strings.TrimSpace(message)
	switch {
	case message == "/角色":
		return "current", "", true
	case message == "/角色列表":
		return "list", "", true
	case strings.HasPrefix(message, "/切换角色 "):
		return "switch", strings.TrimSpace(strings.TrimPrefix(message, "/切换角色 ")), true
	case message == "/撤销角色切换":
		return "revert", "", true
	default:
		return "", "", false
	}
}

func (a *AgentRuntime) roleCommandReply(run runRecord, message string) (agentReply, bool, error) {
	kind, name, ok := roleCommand(message)
	if !ok {
		return agentReply{}, false, nil
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return agentReply{}, true, err
	}
	switch kind {
	case "current":
		id := "未设置"
		if config.ActivePersonaID != nil {
			id = *config.ActivePersonaID
		}
		return agentReply{Text: "当前角色：" + id}, true, nil
	case "list":
		rows, err := a.configStore.db.Query("SELECT name FROM personas ORDER BY name")
		if err != nil {
			return agentReply{}, true, err
		}
		defer rows.Close()
		names := []string{}
		for rows.Next() {
			var value string
			if rows.Scan(&value) == nil {
				names = append(names, value)
			}
		}
		return agentReply{Text: "可用角色：" + strings.Join(names, "、")}, true, nil
	}
	if !a.trustedRoleSwitch(run) {
		return agentReply{Text: "这个命令只有可信管理员能用。"}, true, nil
	}
	if kind == "revert" {
		var oldID string
		if err := a.configStore.db.QueryRow("SELECT old_persona_id FROM persona_switch_history WHERE old_persona_id <> '' ORDER BY created_at DESC LIMIT 1").Scan(&oldID); err != nil {
			return agentReply{Text: "没有可回退的角色切换。"}, true, nil
		}
		name = oldID
	}
	var personaID, personaName string
	if err := a.configStore.db.QueryRow("SELECT id, name FROM personas WHERE id = ? OR name = ?", name, name).Scan(&personaID, &personaName); err != nil {
		return agentReply{Text: "没找到这个角色。"}, true, nil
	}
	oldID := ""
	if config.ActivePersonaID != nil {
		oldID = *config.ActivePersonaID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := a.configStore.db.Begin()
	if err != nil {
		return agentReply{}, true, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE runtime_config SET active_persona_id = ?, updated_at = ? WHERE id = 1", personaID, now); err != nil {
		return agentReply{}, true, err
	}
	id, _ := randomID("role_switch")
	if _, err = tx.Exec("INSERT INTO persona_switch_history(id, old_persona_id, new_persona_id, actor_ref, source, reverted_from_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)", id, oldID, personaID, run.SenderRef, "qq_command", "", now); err != nil {
		return agentReply{}, true, err
	}
	if err = tx.Commit(); err != nil {
		return agentReply{}, true, err
	}
	return agentReply{Text: "已切换到「" + personaName + "」。"}, true, nil
}
