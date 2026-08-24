package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type agentPolicyTemplate struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Config      json.RawMessage `json:"config"`
	Version     int             `json:"version"`
	Enabled     bool            `json:"enabled"`
	CreatedAt   string          `json:"createdAt"`
	UpdatedAt   string          `json:"updatedAt"`
}

type agentPolicyTemplatePayload struct {
	ID          *string          `json:"id"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Config      *json.RawMessage `json:"config"`
	Version     *int             `json:"version"`
	Enabled     *bool            `json:"enabled"`
}

type agentInstance struct {
	ID               string          `json:"id"`
	DisplayName      string          `json:"displayName"`
	PersonaID        string          `json:"personaId"`
	PolicyTemplateID string          `json:"policyTemplateId,omitempty"`
	MemoryNamespace  string          `json:"memoryNamespace"`
	Overrides        json.RawMessage `json:"overrides"`
	Enabled          bool            `json:"enabled"`
	CreatedAt        string          `json:"createdAt"`
	UpdatedAt        string          `json:"updatedAt"`
}

type agentInstancePayload struct {
	ID               *string          `json:"id"`
	DisplayName      *string          `json:"displayName"`
	PersonaID        *string          `json:"personaId"`
	PolicyTemplateID *string          `json:"policyTemplateId"`
	MemoryNamespace  *string          `json:"memoryNamespace"`
	Overrides        *json.RawMessage `json:"overrides"`
	Enabled          *bool            `json:"enabled"`
}

type agentInstanceConnector struct {
	InstanceID  string `json:"instanceId"`
	ConnectorID string `json:"connectorId"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"createdAt"`
	UpdatedAt   string `json:"updatedAt"`
}

type agentInstanceConnectorPayload struct {
	ConnectorID *string `json:"connectorId"`
	Enabled     *bool   `json:"enabled"`
	Priority    *int    `json:"priority"`
}

type agentInstanceRoute struct {
	ID              string `json:"id"`
	InstanceID      string `json:"instanceId"`
	ConnectorID     string `json:"connectorId"`
	Transport       string `json:"transport"`
	ConversationRef string `json:"conversationRef"`
	Priority        int    `json:"priority"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type agentInstanceRoutePayload struct {
	ID              *string `json:"id"`
	InstanceID      *string `json:"instanceId"`
	ConnectorID     *string `json:"connectorId"`
	Transport       *string `json:"transport"`
	ConversationRef *string `json:"conversationRef"`
	Priority        *int    `json:"priority"`
	Enabled         *bool   `json:"enabled"`
}

// agentInstanceRuntimeProfile applies only the supported runtime keys from an
// instance policy. Unknown JSON remains stored for forward compatibility, but
// it cannot silently change behavior.
func (s *coreConfigStore) agentInstanceRuntimeProfile(instanceID string, base personaRuntimeProfile) (personaRuntimeProfile, error) {
	instanceID = strings.TrimSpace(instanceID)
	if instanceID == "" || instanceID == legacyAgentInstanceID {
		return base, nil
	}
	var templateID, overrides string
	if err := s.db.QueryRow(`SELECT COALESCE(policy_template_id, ''), overrides_json FROM agent_instances WHERE id = ?`, instanceID).
		Scan(&templateID, &overrides); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return base, nil
		}
		return base, err
	}
	values := make([]json.RawMessage, 0, 2)
	if templateID != "" {
		var enabled int
		var config string
		if err := s.db.QueryRow(`SELECT config_json, enabled FROM agent_policy_templates WHERE id = ?`, templateID).
			Scan(&config, &enabled); err == nil && enabled == 1 {
			values = append(values, json.RawMessage(config))
		}
	}
	if strings.TrimSpace(overrides) != "" && strings.TrimSpace(overrides) != "{}" {
		values = append(values, json.RawMessage(overrides))
	}
	for _, raw := range values {
		var override personaRuntimeProfile
		if err := json.Unmarshal(raw, &override); err != nil {
			return base, err
		}
		base = mergeAgentRuntimeProfile(base, override)
	}
	return base, nil
}

func mergeAgentRuntimeProfile(base, override personaRuntimeProfile) personaRuntimeProfile {
	canonicalMode := strings.TrimSpace(override.ParticipationMode)
	canonicalModeSet := validParticipationMode(canonicalMode) && canonicalMode != ""
	if value := strings.TrimSpace(override.ChatEndpointID); value != "" {
		base.ChatEndpointID = value
	}
	if value := strings.TrimSpace(override.TaskEndpointID); value != "" {
		base.TaskEndpointID = value
	}
	if value := strings.TrimSpace(override.DecisionEndpointID); value != "" {
		base.DecisionEndpointID = value
	}
	if len(override.AllowedToolIDs) > 0 {
		if len(base.AllowedToolIDs) == 0 {
			base.AllowedToolIDs = append([]string(nil), override.AllowedToolIDs...)
		} else {
			allowed := map[string]bool{}
			for _, id := range override.AllowedToolIDs {
				allowed[strings.TrimSpace(id)] = true
			}
			filtered := make([]string, 0, len(base.AllowedToolIDs))
			for _, id := range base.AllowedToolIDs {
				if allowed[strings.TrimSpace(id)] {
					filtered = append(filtered, id)
				}
			}
			base.AllowedToolIDs = filtered
		}
	}
	base.DeniedToolIDs = appendUniqueStrings(base.DeniedToolIDs, override.DeniedToolIDs...)
	if !canonicalModeSet && override.ProactiveEnabled != nil {
		value := *override.ProactiveEnabled
		base.ProactiveEnabled = &value
	}
	if canonicalModeSet {
		base.ParticipationMode = canonicalMode
	}
	if validOptionalProbability(override.InitialReplyProbability) && override.InitialReplyProbability != nil {
		value := *override.InitialReplyProbability
		base.InitialReplyProbability = &value
	}
	if validOptionalProbability(override.AfterReplyProbability) && override.AfterReplyProbability != nil {
		value := *override.AfterReplyProbability
		base.AfterReplyProbability = &value
	}
	if !canonicalModeSet {
		if value := strings.TrimSpace(override.ParticipationStyle); validParticipationStyle(value) && value != "" {
			base.ParticipationStyle = value
		}
	}
	if !canonicalModeSet {
		if value := strings.TrimSpace(override.UnaddressedMode); validUnaddressedMode(value) && value != "" {
			base.UnaddressedMode = value
		}
	}
	if canonicalModeSet {
		value := canonicalMode != "addressed_only"
		base.ProactiveEnabled = &value
		base.UnaddressedMode = "adaptive"
		base.ParticipationStyle = "balanced"
		switch canonicalMode {
		case "addressed_only":
			base.UnaddressedMode = "off"
			base.ParticipationStyle = "service"
		case "social":
			base.ParticipationStyle = "social"
		}
	}
	if len(override.AddressKeywords) > 0 {
		base.AddressKeywords = cleanRuntimeIDs(override.AddressKeywords)
	}
	if override.MaxReplyChars != nil && (base.MaxReplyChars == nil || *override.MaxReplyChars < *base.MaxReplyChars) {
		value := *override.MaxReplyChars
		base.MaxReplyChars = &value
	}
	if override.MaxReplySentences != nil && (base.MaxReplySentences == nil || *override.MaxReplySentences < *base.MaxReplySentences) {
		value := *override.MaxReplySentences
		base.MaxReplySentences = &value
	}
	if value := strings.TrimSpace(override.MemoryPolicy); value != "" {
		base.MemoryPolicy = value
	}
	if value := strings.TrimSpace(override.SearchMode); validSearchMode(value) && value != "" {
		base.SearchMode = value
	}
	if value := strings.TrimSpace(override.SearchReplyStyle); validSearchReplyStyle(value) && value != "" {
		base.SearchReplyStyle = value
	}
	if value := strings.TrimSpace(override.VisualPromptOverride); value != "" {
		base.VisualPromptOverride = value
	}
	if value := strings.TrimSpace(override.ExpressionPrompt); value != "" {
		base.ExpressionPrompt = value
	}
	return base
}

func appendUniqueStrings(values []string, additions ...string) []string {
	seen := map[string]bool{}
	result := make([]string, 0, len(values)+len(additions))
	for _, value := range append(values, additions...) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result
}

var agentPolicyTemplateFields = coreFieldSet("id", "name", "description", "config", "version", "enabled")
var agentInstanceFields = coreFieldSet("id", "displayName", "personaId", "policyTemplateId", "memoryNamespace", "overrides", "enabled")
var agentInstanceConnectorFields = coreFieldSet("connectorId", "enabled", "priority")
var agentInstanceRouteFields = coreFieldSet("id", "instanceId", "connectorId", "transport", "conversationRef", "priority", "enabled")

func validAgentInstanceJSON(value json.RawMessage) (string, error) {
	if value == nil || len(value) == 0 {
		return "{}", nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return "", coreInvalid("config must be a JSON object")
	}
	var runtimeProfile personaRuntimeProfile
	if err := json.Unmarshal(value, &runtimeProfile); err != nil {
		return "", coreInvalid("runtime config contains an invalid value")
	}
	if !validOptionalProbability(runtimeProfile.InitialReplyProbability) || !validOptionalProbability(runtimeProfile.AfterReplyProbability) {
		return "", coreInvalid("reply probabilities must be between 0 and 1")
	}
	if !validParticipationStyle(runtimeProfile.ParticipationStyle) {
		return "", coreInvalid("participationStyle must be balanced, social, or service")
	}
	if !validParticipationMode(runtimeProfile.ParticipationMode) {
		return "", coreInvalid("participationMode must be addressed_only, adaptive, or social")
	}
	if !validUnaddressedMode(runtimeProfile.UnaddressedMode) {
		return "", coreInvalid("unaddressedMode must be off, rare, or adaptive")
	}
	if !validSearchMode(runtimeProfile.SearchMode) || !validSearchReplyStyle(runtimeProfile.SearchReplyStyle) {
		return "", coreInvalid("searchMode or searchReplyStyle is invalid")
	}
	encoded, _ := json.Marshal(object)
	return string(encoded), nil
}

func normalizeAgentInstanceText(value, name string, maximum int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if (required && value == "") || len(value) > maximum || strings.Contains(value, "\x00") {
		return "", coreInvalid(name + " is invalid")
	}
	return value, nil
}

func (s *coreConfigStore) handleAgentInstanceRequest(w http.ResponseWriter, r *http.Request, path string) error {
	switch {
	case path == "/api/v1/agent-policy-templates" || strings.HasPrefix(path, "/api/v1/agent-policy-templates/"):
		return s.handleAgentPolicyTemplates(w, r, path)
	case path == "/api/v1/agent-instances" || strings.HasPrefix(path, "/api/v1/agent-instances/"):
		return s.handleAgentInstances(w, r, path)
	case path == "/api/v1/agent-instance-routes" || strings.HasPrefix(path, "/api/v1/agent-instance-routes/"):
		return s.handleAgentInstanceRoutes(w, r, path)
	default:
		return mgmtNotFound("agent instance route")
	}
}

func (s *coreConfigStore) handleAgentPolicyTemplates(w http.ResponseWriter, r *http.Request, path string) error {
	const prefix = "/api/v1/agent-policy-templates"
	if path == prefix {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAgentPolicyTemplates()
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
			return nil
		case http.MethodPost:
			var input agentPolicyTemplatePayload
			if _, err := decodeCoreObject(r, agentPolicyTemplateFields, "agent policy template", &input); err != nil {
				return err
			}
			if input.ID == nil {
				return coreInvalid("id is required")
			}
			value, err := s.putAgentPolicyTemplate(strings.TrimSpace(*input.ID), input, true)
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusCreated, value)
			return nil
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, prefix+"/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.agentPolicyTemplate(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("agent policy template")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		var input agentPolicyTemplatePayload
		if _, err := decodeCoreObject(r, agentPolicyTemplateFields, "agent policy template", &input); err != nil {
			return err
		}
		value, err := s.putAgentPolicyTemplate(id, input, false)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM agent_policy_templates WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "agent policy template"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) listAgentPolicyTemplates() ([]agentPolicyTemplate, error) {
	rows, err := s.db.Query(`SELECT id, name, description, config_json, version, enabled, created_at, updated_at
		FROM agent_policy_templates ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []agentPolicyTemplate{}
	for rows.Next() {
		var item agentPolicyTemplate
		var enabled int
		var config string
		if err = rows.Scan(&item.ID, &item.Name, &item.Description, &config, &item.Version, &enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Config, item.Enabled = json.RawMessage(config), enabled == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) agentPolicyTemplate(id string) (agentPolicyTemplate, bool, error) {
	var item agentPolicyTemplate
	var enabled int
	var config string
	err := s.db.QueryRow(`SELECT id, name, description, config_json, version, enabled, created_at, updated_at
		FROM agent_policy_templates WHERE id = ?`, id).Scan(&item.ID, &item.Name, &item.Description, &config, &item.Version, &enabled, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	item.Config, item.Enabled = json.RawMessage(config), enabled == 1
	return item, true, nil
}

func (s *coreConfigStore) putAgentPolicyTemplate(id string, input agentPolicyTemplatePayload, create bool) (agentPolicyTemplate, error) {
	id, err := normalizeAgentInstanceText(id, "id", 120, true)
	if err != nil {
		return agentPolicyTemplate{}, err
	}
	current, found, err := s.agentPolicyTemplate(id)
	if err != nil {
		return current, err
	}
	if create && found {
		return current, coreInvalid("agent policy template already exists")
	}
	if !create && !found {
		return current, mgmtNotFound("agent policy template")
	}
	if create && !found {
		current.Version = 1
		current.Enabled = true
	}
	if input.Name != nil {
		current.Name = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		current.Description = strings.TrimSpace(*input.Description)
	}
	if input.Config != nil {
		raw, err := validAgentInstanceJSON(*input.Config)
		if err != nil {
			return current, err
		}
		current.Config = json.RawMessage(raw)
	}
	if input.Version != nil {
		current.Version = *input.Version
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if current.Name, err = normalizeAgentInstanceText(current.Name, "name", 160, true); err != nil {
		return current, err
	}
	if current.Description, err = normalizeAgentInstanceText(current.Description, "description", 2000, false); err != nil {
		return current, err
	}
	if current.Version < 1 {
		return current, coreInvalid("version must be at least 1")
	}
	if current.Config == nil {
		current.Config = json.RawMessage("{}")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current.ID = id
	current.CreatedAt = coalesceTime(current.CreatedAt, now)
	current.UpdatedAt = now
	_, err = s.db.Exec(`INSERT INTO agent_policy_templates(id, name, description, config_json, version, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description,
		config_json=excluded.config_json, version=excluded.version, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		current.ID, current.Name, current.Description, string(current.Config), current.Version, boolInt(current.Enabled), current.CreatedAt, current.UpdatedAt)
	if err != nil {
		return current, mgmtConstraintError(err, "agent policy template name already exists")
	}
	return current, nil
}

func (s *coreConfigStore) handleAgentInstances(w http.ResponseWriter, r *http.Request, path string) error {
	const prefix = "/api/v1/agent-instances"
	if path == prefix {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAgentInstances()
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
			return nil
		case http.MethodPost:
			var input agentInstancePayload
			if _, err := decodeCoreObject(r, agentInstanceFields, "agent instance", &input); err != nil {
				return err
			}
			if input.ID == nil {
				return coreInvalid("id is required")
			}
			value, err := s.putAgentInstance(strings.TrimSpace(*input.ID), input, true)
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusCreated, value)
			return nil
		default:
			return mgmtMethodNotAllowed()
		}
	}
	rest := strings.TrimPrefix(path, prefix+"/")
	parts := strings.Split(rest, "/")
	if len(parts) >= 2 && parts[1] == "connectors" {
		return s.handleAgentInstanceConnectors(w, r, parts)
	}
	if len(parts) != 1 {
		return mgmtNotFound("agent instance")
	}
	id, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.agentInstance(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("agent instance")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		var input agentInstancePayload
		if _, err := decodeCoreObject(r, agentInstanceFields, "agent instance", &input); err != nil {
			return err
		}
		value, err := s.putAgentInstance(id, input, false)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM agent_instances WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "agent instance"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) listAgentInstances() ([]agentInstance, error) {
	rows, err := s.db.Query(`SELECT id, display_name, persona_id, COALESCE(policy_template_id, ''), memory_namespace, overrides_json, enabled, created_at, updated_at
		FROM agent_instances ORDER BY display_name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []agentInstance{}
	for rows.Next() {
		item, err := scanAgentInstance(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type agentInstanceScanner interface{ Scan(...any) error }

func scanAgentInstance(row agentInstanceScanner) (agentInstance, error) {
	var item agentInstance
	var enabled int
	var overrides string
	err := row.Scan(&item.ID, &item.DisplayName, &item.PersonaID, &item.PolicyTemplateID, &item.MemoryNamespace, &overrides, &enabled, &item.CreatedAt, &item.UpdatedAt)
	item.Overrides, item.Enabled = json.RawMessage(overrides), enabled == 1
	return item, err
}

func (s *coreConfigStore) agentInstance(id string) (agentInstance, bool, error) {
	item, err := scanAgentInstance(s.db.QueryRow(`SELECT id, display_name, persona_id, COALESCE(policy_template_id, ''), memory_namespace, overrides_json, enabled, created_at, updated_at FROM agent_instances WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	return item, true, nil
}

func (s *coreConfigStore) putAgentInstance(id string, input agentInstancePayload, create bool) (agentInstance, error) {
	id, err := normalizeAgentInstanceText(id, "id", 120, true)
	if err != nil {
		return agentInstance{}, err
	}
	current, found, err := s.agentInstance(id)
	if err != nil {
		return current, err
	}
	if create && found {
		return current, coreInvalid("agent instance already exists")
	}
	if !create && !found {
		return current, mgmtNotFound("agent instance")
	}
	if create && !found {
		current.Enabled = true
		current.MemoryNamespace = id
	}
	if input.DisplayName != nil {
		current.DisplayName = strings.TrimSpace(*input.DisplayName)
	}
	if input.PersonaID != nil {
		current.PersonaID = strings.TrimSpace(*input.PersonaID)
	}
	if input.PolicyTemplateID != nil {
		current.PolicyTemplateID = strings.TrimSpace(*input.PolicyTemplateID)
	}
	if input.MemoryNamespace != nil {
		current.MemoryNamespace = strings.TrimSpace(*input.MemoryNamespace)
	}
	if input.Overrides != nil {
		raw, err := validAgentInstanceJSON(*input.Overrides)
		if err != nil {
			return current, err
		}
		current.Overrides = json.RawMessage(raw)
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if current.DisplayName, err = normalizeAgentInstanceText(current.DisplayName, "displayName", 160, true); err != nil {
		return current, err
	}
	if current.PersonaID, err = normalizeAgentInstanceText(current.PersonaID, "personaId", 120, true); err != nil {
		return current, err
	}
	if current.MemoryNamespace, err = normalizeAgentInstanceText(current.MemoryNamespace, "memoryNamespace", 160, true); err != nil {
		return current, err
	}
	if current.PolicyTemplateID != "" {
		var exists int
		if err = s.db.QueryRow("SELECT count(*) FROM agent_policy_templates WHERE id = ?", current.PolicyTemplateID).Scan(&exists); err != nil {
			return current, err
		}
		if exists != 1 {
			return current, coreInvalid("policy template does not exist")
		}
	}
	var personaExists int
	if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", current.PersonaID).Scan(&personaExists); err != nil {
		return current, err
	}
	if personaExists != 1 {
		return current, coreInvalid("persona does not exist")
	}
	if current.Overrides == nil {
		current.Overrides = json.RawMessage("{}")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current.ID = id
	current.CreatedAt = coalesceTime(current.CreatedAt, now)
	current.UpdatedAt = now
	var templateID any = current.PolicyTemplateID
	if current.PolicyTemplateID == "" {
		templateID = nil
	}
	_, err = s.db.Exec(`INSERT INTO agent_instances(id, display_name, persona_id, policy_template_id, memory_namespace, overrides_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET display_name=excluded.display_name, persona_id=excluded.persona_id, policy_template_id=excluded.policy_template_id, memory_namespace=excluded.memory_namespace, overrides_json=excluded.overrides_json, enabled=excluded.enabled, updated_at=excluded.updated_at`, current.ID, current.DisplayName, current.PersonaID, templateID, current.MemoryNamespace, string(current.Overrides), boolInt(current.Enabled), current.CreatedAt, current.UpdatedAt)
	return current, err
}

func (s *coreConfigStore) handleAgentInstanceConnectors(w http.ResponseWriter, r *http.Request, parts []string) error {
	if len(parts) < 2 || len(parts) > 3 {
		return mgmtNotFound("agent instance connector")
	}
	instanceID, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	if _, found, err := s.agentInstance(instanceID); err != nil {
		return err
	} else if !found {
		return mgmtNotFound("agent instance")
	}
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAgentInstanceConnectors(instanceID)
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
			return nil
		case http.MethodPost:
			var input agentInstanceConnectorPayload
			if _, err := decodeCoreObject(r, agentInstanceConnectorFields, "agent instance connector", &input); err != nil {
				return err
			}
			if input.ConnectorID == nil {
				return coreInvalid("connectorId is required")
			}
			value, err := s.putAgentInstanceConnector(instanceID, strings.TrimSpace(*input.ConnectorID), input, true)
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusCreated, value)
			return nil
		default:
			return mgmtMethodNotAllowed()
		}
	}
	connectorID, err := parseCorePathID(parts[2])
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		var input agentInstanceConnectorPayload
		if _, err := decodeCoreObject(r, agentInstanceConnectorFields, "agent instance connector", &input); err != nil {
			return err
		}
		value, err := s.putAgentInstanceConnector(instanceID, connectorID, input, false)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM agent_instance_connectors WHERE instance_id = ? AND connector_id = ?", instanceID, connectorID)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "agent instance connector"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"connectorId": connectorID, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) listAgentInstanceConnectors(instanceID string) ([]agentInstanceConnector, error) {
	rows, err := s.db.Query(`SELECT instance_id, connector_id, enabled, priority, created_at, updated_at FROM agent_instance_connectors WHERE instance_id = ? ORDER BY priority DESC, connector_id`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []agentInstanceConnector{}
	for rows.Next() {
		var item agentInstanceConnector
		var enabled int
		if err = rows.Scan(&item.InstanceID, &item.ConnectorID, &enabled, &item.Priority, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Enabled = enabled == 1
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) putAgentInstanceConnector(instanceID, connectorID string, input agentInstanceConnectorPayload, create bool) (agentInstanceConnector, error) {
	connectorID, err := normalizeAgentInstanceText(connectorID, "connectorId", 120, true)
	if err != nil {
		return agentInstanceConnector{}, err
	}
	var current agentInstanceConnector
	var enabled int
	err = s.db.QueryRow(`SELECT instance_id, connector_id, enabled, priority, created_at, updated_at FROM agent_instance_connectors WHERE instance_id = ? AND connector_id = ?`, instanceID, connectorID).Scan(&current.InstanceID, &current.ConnectorID, &enabled, &current.Priority, &current.CreatedAt, &current.UpdatedAt)
	found := err == nil
	if !found && !errors.Is(err, sql.ErrNoRows) {
		return current, err
	}
	if create && found {
		return current, coreInvalid("agent instance connector already exists")
	}
	if !create && !found {
		return current, mgmtNotFound("agent instance connector")
	}
	if found {
		current.Enabled = enabled == 1
	} else {
		current.InstanceID, current.ConnectorID, current.Enabled, current.Priority = instanceID, connectorID, true, 100
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if input.Priority != nil {
		current.Priority = *input.Priority
	}
	if current.Priority < -10000 || current.Priority > 10000 {
		return current, coreInvalid("priority is invalid")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current.CreatedAt, current.UpdatedAt = coalesceTime(current.CreatedAt, now), now
	_, err = s.db.Exec(`INSERT INTO agent_instance_connectors(instance_id, connector_id, enabled, priority, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(instance_id, connector_id) DO UPDATE SET enabled=excluded.enabled, priority=excluded.priority, updated_at=excluded.updated_at`, current.InstanceID, current.ConnectorID, boolInt(current.Enabled), current.Priority, current.CreatedAt, current.UpdatedAt)
	return current, err
}

func (s *coreConfigStore) handleAgentInstanceRoutes(w http.ResponseWriter, r *http.Request, path string) error {
	const prefix = "/api/v1/agent-instance-routes"
	if path == prefix {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAgentInstanceRoutes()
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
			return nil
		case http.MethodPost:
			var input agentInstanceRoutePayload
			if _, err := decodeCoreObject(r, agentInstanceRouteFields, "agent instance route", &input); err != nil {
				return err
			}
			if input.ID == nil {
				return coreInvalid("id is required")
			}
			value, err := s.putAgentInstanceRoute(strings.TrimSpace(*input.ID), input, true)
			if err != nil {
				return err
			}
			mgmtWriteData(w, http.StatusCreated, value)
			return nil
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, prefix+"/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.agentInstanceRoute(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("agent instance route")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		var input agentInstanceRoutePayload
		if _, err := decodeCoreObject(r, agentInstanceRouteFields, "agent instance route", &input); err != nil {
			return err
		}
		value, err := s.putAgentInstanceRoute(id, input, false)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM agent_instance_routes WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "agent instance route"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) listAgentInstanceRoutes() ([]agentInstanceRoute, error) {
	rows, err := s.db.Query(`SELECT id, instance_id, connector_id, transport, conversation_ref, priority, enabled, created_at, updated_at FROM agent_instance_routes ORDER BY priority DESC, updated_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []agentInstanceRoute{}
	for rows.Next() {
		item, err := scanAgentInstanceRoute(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type agentInstanceRouteScanner interface{ Scan(...any) error }

func scanAgentInstanceRoute(row agentInstanceRouteScanner) (agentInstanceRoute, error) {
	var item agentInstanceRoute
	var enabled int
	err := row.Scan(&item.ID, &item.InstanceID, &item.ConnectorID, &item.Transport, &item.ConversationRef, &item.Priority, &enabled, &item.CreatedAt, &item.UpdatedAt)
	item.Enabled = enabled == 1
	return item, err
}

func (s *coreConfigStore) agentInstanceRoute(id string) (agentInstanceRoute, bool, error) {
	item, err := scanAgentInstanceRoute(s.db.QueryRow(`SELECT id, instance_id, connector_id, transport, conversation_ref, priority, enabled, created_at, updated_at FROM agent_instance_routes WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return item, false, nil
	}
	if err != nil {
		return item, false, err
	}
	return item, true, nil
}

func (s *coreConfigStore) putAgentInstanceRoute(id string, input agentInstanceRoutePayload, create bool) (agentInstanceRoute, error) {
	id, err := normalizeAgentInstanceText(id, "id", 120, true)
	if err != nil {
		return agentInstanceRoute{}, err
	}
	current, found, err := s.agentInstanceRoute(id)
	if err != nil {
		return current, err
	}
	if create && found {
		return current, coreInvalid("agent instance route already exists")
	}
	if !create && !found {
		return current, mgmtNotFound("agent instance route")
	}
	if create && !found {
		current.Enabled = true
	}
	if input.InstanceID != nil {
		current.InstanceID = strings.TrimSpace(*input.InstanceID)
	}
	if input.ConnectorID != nil {
		current.ConnectorID = strings.TrimSpace(*input.ConnectorID)
	}
	if input.Transport != nil {
		current.Transport = strings.ToLower(strings.TrimSpace(*input.Transport))
	}
	if input.ConversationRef != nil {
		current.ConversationRef = strings.TrimSpace(*input.ConversationRef)
	}
	if input.Priority != nil {
		current.Priority = *input.Priority
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	if current.InstanceID, err = normalizeAgentInstanceText(current.InstanceID, "instanceId", 120, true); err != nil {
		return current, err
	}
	if current.ConnectorID == "" {
		current.ConnectorID = "*"
	}
	if current.Transport == "" {
		current.Transport = "*"
	}
	if current.ConversationRef == "" {
		current.ConversationRef = "*"
	}
	if _, err = normalizeAgentInstanceText(current.ConnectorID, "connectorId", 120, true); err != nil {
		return current, err
	}
	if _, err = normalizeAgentInstanceText(current.Transport, "transport", 80, true); err != nil {
		return current, err
	}
	if _, err = normalizeAgentInstanceText(current.ConversationRef, "conversationRef", 240, true); err != nil {
		return current, err
	}
	if current.Priority < -10000 || current.Priority > 10000 {
		return current, coreInvalid("priority is invalid")
	}
	var instanceExists int
	if err = s.db.QueryRow("SELECT count(*) FROM agent_instances WHERE id = ?", current.InstanceID).Scan(&instanceExists); err != nil {
		return current, err
	}
	if instanceExists != 1 {
		return current, coreInvalid("agent instance does not exist")
	}
	if current.ConnectorID != "*" {
		var connectorExists int
		if err = s.db.QueryRow("SELECT count(*) FROM agent_instance_connectors WHERE instance_id = ? AND connector_id = ?", current.InstanceID, current.ConnectorID).Scan(&connectorExists); err != nil {
			return current, err
		}
		if connectorExists != 1 {
			return current, coreInvalid("agent instance connector does not exist")
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	current.ID, current.CreatedAt, current.UpdatedAt = id, coalesceTime(current.CreatedAt, now), now
	_, err = s.db.Exec(`INSERT INTO agent_instance_routes(id, instance_id, connector_id, transport, conversation_ref, priority, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET instance_id=excluded.instance_id, connector_id=excluded.connector_id, transport=excluded.transport, conversation_ref=excluded.conversation_ref, priority=excluded.priority, enabled=excluded.enabled, updated_at=excluded.updated_at`, current.ID, current.InstanceID, current.ConnectorID, current.Transport, current.ConversationRef, current.Priority, boolInt(current.Enabled), current.CreatedAt, current.UpdatedAt)
	if err != nil {
		return current, mgmtConstraintError(err, "agent instance route already exists")
	}
	return current, nil
}

type resolvedAgentInstance struct {
	InstanceID, PersonaID string
	MemoryNamespace       string
	Enabled               bool
	Matched               bool
}

func (s *coreConfigStore) resolveAgentInstance(transportInstance, transport, conversation string) (resolvedAgentInstance, error) {
	transportInstance, transport, conversation = strings.TrimSpace(transportInstance), strings.ToLower(strings.TrimSpace(transport)), strings.TrimSpace(conversation)
	var value resolvedAgentInstance
	var enabled int
	err := s.db.QueryRow(`SELECT instance.id, instance.persona_id, instance.memory_namespace, instance.enabled
		FROM agent_instance_routes route
		JOIN agent_instances instance ON instance.id = route.instance_id
		JOIN agent_instance_connectors connector ON connector.instance_id = instance.id AND connector.connector_id = ? AND connector.enabled = 1
		WHERE route.enabled = 1 AND route.connector_id IN (?, '*') AND route.transport IN (?, '*') AND route.conversation_ref IN (?, '*')
		ORDER BY (route.connector_id = ?) DESC, (route.conversation_ref = ?) DESC, (route.transport = ?) DESC, route.priority DESC, connector.priority DESC, route.updated_at DESC
		LIMIT 1`, transportInstance, transportInstance, transport, conversation, transportInstance, conversation, transport).Scan(&value.InstanceID, &value.PersonaID, &value.MemoryNamespace, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	value.Matched, value.Enabled = true, enabled == 1
	return value, nil
}
