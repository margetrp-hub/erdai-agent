package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
)

const groupModerationCapabilityID = "group_moderation"

type agentInstanceCapability struct {
	InstanceID   string          `json:"instanceId"`
	CapabilityID string          `json:"capabilityId"`
	Enabled      bool            `json:"enabled"`
	Config       json.RawMessage `json:"config"`
	CreatedAt    string          `json:"createdAt"`
	UpdatedAt    string          `json:"updatedAt"`
}

type agentInstanceCapabilityPayload struct {
	Enabled *bool            `json:"enabled"`
	Config  *json.RawMessage `json:"config"`
}

type groupModerationConfig struct {
	Mode                string   `json:"mode"`
	ExecutorConnectorID string   `json:"executorConnectorId"`
	GroupIDs            []string `json:"groupIds"`
	ExemptAdmins        bool     `json:"exemptAdmins"`
	MinimumScore        int      `json:"minimumScore"`
	CommercialMarkers   []string `json:"commercialMarkers,omitempty"`
	ServiceMarkers      []string `json:"serviceMarkers,omitempty"`
	AllowedSenderIDs    []string `json:"allowedSenderIds,omitempty"`
}

type groupModerationDecision struct {
	Matched         bool
	Mode            string
	OwnerInstanceID string
	Score           int
	Reasons         []string
}

var groupModerationContactPattern = regexp.MustCompile(`(?i)(https?://|www\.|(vx|v信|微信|qq)\s*[:：]?\s*[a-z0-9_-]{4,}|[a-z0-9.-]+\.(com|cn|net|org|cc|cfd|top|xyz)(/|\b))`)

var agentInstanceCapabilityFields = coreFieldSet("enabled", "config")

func (s *coreConfigStore) handleAgentInstanceCapabilities(w http.ResponseWriter, r *http.Request, path string) error {
	const prefix = "/api/v1/agent-instance-capabilities"
	if path == prefix {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		items, err := s.listAgentInstanceCapabilities(strings.TrimSpace(r.URL.Query().Get("instanceId")))
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
		return nil
	}
	rest := strings.TrimPrefix(path, prefix+"/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return mgmtNotFound("agent instance capability")
	}
	instanceID, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	capabilityID, err := parseCorePathID(parts[1])
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		item, found, err := s.agentInstanceCapability(instanceID, capabilityID)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("agent instance capability")
		}
		mgmtWriteData(w, http.StatusOK, item)
		return nil
	case http.MethodPut:
		var input agentInstanceCapabilityPayload
		if _, err = decodeCoreObject(r, agentInstanceCapabilityFields, "agent instance capability", &input); err != nil {
			return err
		}
		item, err := s.putAgentInstanceCapability(instanceID, capabilityID, input)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, item)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec(`DELETE FROM agent_instance_capabilities WHERE instance_id = ? AND capability_id = ?`, instanceID, capabilityID)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "agent instance capability"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"instanceId": instanceID, "capabilityId": capabilityID, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) listAgentInstanceCapabilities(instanceID string) ([]agentInstanceCapability, error) {
	query := `SELECT instance_id, capability_id, enabled, config_json, created_at, updated_at FROM agent_instance_capabilities`
	args := []any{}
	if instanceID != "" {
		query += ` WHERE instance_id = ?`
		args = append(args, instanceID)
	}
	query += ` ORDER BY instance_id, capability_id`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []agentInstanceCapability{}
	for rows.Next() {
		var item agentInstanceCapability
		var enabled int
		var config string
		if err = rows.Scan(&item.InstanceID, &item.CapabilityID, &enabled, &config, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled, item.Config = enabled == 1, json.RawMessage(config)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) agentInstanceCapability(instanceID, capabilityID string) (agentInstanceCapability, bool, error) {
	var item agentInstanceCapability
	var enabled int
	var config string
	err := s.db.QueryRow(`SELECT instance_id, capability_id, enabled, config_json, created_at, updated_at
		FROM agent_instance_capabilities WHERE instance_id = ? AND capability_id = ?`, instanceID, capabilityID).
		Scan(&item.InstanceID, &item.CapabilityID, &enabled, &config, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	item.Enabled, item.Config = enabled == 1, json.RawMessage(config)
	return item, true, nil
}

func (s *coreConfigStore) putAgentInstanceCapability(instanceID, capabilityID string, input agentInstanceCapabilityPayload) (agentInstanceCapability, error) {
	var exists int
	if err := s.db.QueryRow(`SELECT count(*) FROM agent_instances WHERE id = ?`, instanceID).Scan(&exists); err != nil {
		return agentInstanceCapability{}, err
	}
	if exists == 0 {
		return agentInstanceCapability{}, mgmtNotFound("agent instance")
	}
	current, found, err := s.agentInstanceCapability(instanceID, capabilityID)
	if err != nil {
		return current, err
	}
	if !found {
		current = agentInstanceCapability{InstanceID: instanceID, CapabilityID: capabilityID, Config: json.RawMessage("{}")}
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.Config != nil {
		config, err := validAgentInstanceJSON(*input.Config)
		if err != nil {
			return current, err
		}
		current.Config = json.RawMessage(config)
	}
	if capabilityID == groupModerationCapabilityID && current.Enabled {
		var config groupModerationConfig
		if err := json.Unmarshal(current.Config, &config); err != nil {
			return current, coreInvalid("group moderation config is invalid")
		}
		if err := validateGroupModerationConfig(config); err != nil {
			return current, err
		}
	}
	now := mgmtNow()
	if current.CreatedAt == "" {
		current.CreatedAt = now
	}
	current.UpdatedAt = now
	_, err = s.db.Exec(`INSERT INTO agent_instance_capabilities(instance_id, capability_id, enabled, config_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(instance_id, capability_id) DO UPDATE SET
		enabled=excluded.enabled, config_json=excluded.config_json, updated_at=excluded.updated_at`,
		current.InstanceID, current.CapabilityID, boolInt(current.Enabled), string(current.Config), current.CreatedAt, current.UpdatedAt)
	return current, err
}

func (a *AgentRuntime) evaluateOwnedGroupModeration(ctx context.Context, connectorID, transport, conversationRef, groupID, senderID, senderName, message string, senderIsAdmin bool) (groupModerationDecision, error) {
	resolved, err := a.configStore.resolveAgentInstance(connectorID, transport, conversationRef)
	if err != nil || !resolved.Matched || !resolved.Enabled {
		return groupModerationDecision{}, err
	}
	item, found, err := a.configStore.agentInstanceCapability(resolved.InstanceID, groupModerationCapabilityID)
	if err != nil || !found || !item.Enabled {
		return groupModerationDecision{}, err
	}
	var config groupModerationConfig
	if json.Unmarshal(item.Config, &config) != nil || config.ExemptAdmins && senderIsAdmin || containsExact(config.AllowedSenderIDs, senderID) || !matchesModerationGroup(config.GroupIDs, groupID) {
		return groupModerationDecision{}, nil
	}
	score, reasons := scoreGroupAdvertisement(senderName, message, config)
	if score < config.MinimumScore {
		return groupModerationDecision{}, nil
	}
	return groupModerationDecision{Matched: true, Mode: config.Mode, OwnerInstanceID: resolved.InstanceID, Score: score, Reasons: reasons}, nil
}

func validateGroupModerationConfig(config groupModerationConfig) error {
	mode := strings.TrimSpace(config.Mode)
	if mode != "audit" && mode != "enforce" {
		return coreInvalid("group moderation mode must be audit or enforce")
	}
	if strings.TrimSpace(config.ExecutorConnectorID) == "" {
		return coreInvalid("executorConnectorId is required")
	}
	if config.MinimumScore < 2 || config.MinimumScore > 6 {
		return coreInvalid("minimumScore must be between 2 and 6")
	}
	return nil
}

func (a *AgentRuntime) evaluateGroupModeration(ctx context.Context, connectorID, groupID, senderID, senderName, message string, senderIsAdmin bool) (groupModerationDecision, error) {
	rows, err := a.configStore.db.QueryContext(ctx, `SELECT capability.instance_id, capability.config_json
		FROM agent_instance_capabilities capability JOIN agent_instances instance ON instance.id = capability.instance_id
		WHERE capability.capability_id = ? AND capability.enabled = 1 AND instance.enabled = 1`, groupModerationCapabilityID)
	if err != nil {
		return groupModerationDecision{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var owner, raw string
		if err = rows.Scan(&owner, &raw); err != nil {
			return groupModerationDecision{}, err
		}
		var config groupModerationConfig
		if json.Unmarshal([]byte(raw), &config) != nil || strings.TrimSpace(config.ExecutorConnectorID) != connectorID {
			continue
		}
		if config.ExemptAdmins && senderIsAdmin || containsExact(config.AllowedSenderIDs, senderID) || !matchesModerationGroup(config.GroupIDs, groupID) {
			continue
		}
		score, reasons := scoreGroupAdvertisement(senderName, message, config)
		if score >= config.MinimumScore {
			return groupModerationDecision{Matched: true, Mode: config.Mode, OwnerInstanceID: owner, Score: score, Reasons: reasons}, nil
		}
	}
	return groupModerationDecision{}, rows.Err()
}

func matchesModerationGroup(groups []string, groupID string) bool {
	return len(groups) == 0 || containsExact(groups, "*") || containsExact(groups, groupID)
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
}

func scoreGroupAdvertisement(senderName, message string, config groupModerationConfig) (int, []string) {
	combined := strings.ToLower(strings.TrimSpace(senderName + " " + message))
	commercial := config.CommercialMarkers
	if len(commercial) == 0 {
		commercial = []string{"充值", "代充", "低价", "优惠", "返利", "折扣", "推广", "注册", "接单", "客服", "渠道"}
	}
	service := config.ServiceMarkers
	if len(service) == 0 {
		service = []string{"24小时", "全天", "长期", "为你服务", "联系我", "加我", "私聊", "扫码", "点击", "链接"}
	}
	score := 0
	reasons := []string{}
	if groupModerationContactPattern.MatchString(combined) {
		score++
		reasons = append(reasons, "contact_or_link")
	}
	if containsAnyFold(combined, commercial) {
		score++
		reasons = append(reasons, "commercial_offer")
	}
	if containsAnyFold(combined, service) {
		score++
		reasons = append(reasons, "promotion_language")
	}
	sort.Strings(reasons)
	return score, reasons
}

func containsAnyFold(text string, values []string) bool {
	for _, value := range values {
		if value = strings.ToLower(strings.TrimSpace(value)); value != "" && strings.Contains(text, value) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) recordGroupModerationAudit(decision groupModerationDecision, action, connectorID, groupID, messageID, errorCode string) {
	if a == nil || a.configStore == nil || a.configStore.db == nil {
		return
	}
	details, _ := json.Marshal(map[string]any{
		"connectorId": connectorID,
		"groupRef":    a.platformPseudonym("group", connectorID+":"+groupID),
		"score":       decision.Score,
		"reasons":     decision.Reasons,
		"errorCode":   errorCode,
	})
	_, _ = a.configStore.db.Exec(`INSERT INTO audit_events(actor, action, target_type, target_id, details_json, created_at)
		VALUES (?, ?, 'group_message', ?, ?, ?)`, "agent:"+decision.OwnerInstanceID, action,
		a.platformPseudonym("message", connectorID+":"+messageID), string(details), time.Now().UTC().Format(time.RFC3339Nano))
}
