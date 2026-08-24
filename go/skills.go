package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

type runtimeSkill struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
}

type mgmtSkill struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Instructions       string   `json:"instructions"`
	Enabled            bool     `json:"enabled"`
	ActivationMode     string   `json:"activationMode"`
	Triggers           []string `json:"triggers"`
	AttachmentKinds    []string `json:"attachmentKinds"`
	RequiredTools      []string `json:"requiredTools"`
	RequiredMCPServers []string `json:"requiredMcpServers"`
	AllowedAuthorities []string `json:"allowedAuthorities"`
	PersonaIDs         []string `json:"personaIds"`
	Priority           int      `json:"priority"`
	CreatedAt          string   `json:"createdAt"`
	UpdatedAt          string   `json:"updatedAt"`
}

// skillCatalogEntry is the cheap part of a skill. Runtime matching should not
// read or carry the instruction body until a candidate has actually won.
type skillCatalogEntry struct {
	ID                 string   `json:"id"`
	Name               string   `json:"name"`
	Description        string   `json:"description"`
	Enabled            bool     `json:"enabled"`
	ActivationMode     string   `json:"activationMode"`
	Triggers           []string `json:"triggers"`
	AttachmentKinds    []string `json:"attachmentKinds"`
	RequiredTools      []string `json:"requiredTools"`
	RequiredMCPServers []string `json:"requiredMcpServers"`
	AllowedAuthorities []string `json:"allowedAuthorities"`
	PersonaIDs         []string `json:"personaIds"`
	Priority           int      `json:"priority"`
}

type mgmtSkillPayload struct {
	ID                 *string   `json:"id"`
	Name               *string   `json:"name"`
	Description        *string   `json:"description"`
	Instructions       *string   `json:"instructions"`
	Enabled            *bool     `json:"enabled"`
	ActivationMode     *string   `json:"activationMode"`
	Triggers           *[]string `json:"triggers"`
	AttachmentKinds    *[]string `json:"attachmentKinds"`
	RequiredTools      *[]string `json:"requiredTools"`
	RequiredMCPServers *[]string `json:"requiredMcpServers"`
	AllowedAuthorities *[]string `json:"allowedAuthorities"`
	PersonaIDs         *[]string `json:"personaIds"`
	Priority           *int      `json:"priority"`
}

var (
	mgmtSkillCreateFields = coreFieldSet(
		"id", "name", "description", "instructions", "enabled", "activationMode",
		"triggers", "attachmentKinds", "requiredTools", "requiredMcpServers",
		"allowedAuthorities", "personaIds", "priority",
	)
	mgmtSkillUpdateFields = coreFieldSet(
		"name", "description", "instructions", "enabled", "activationMode",
		"triggers", "attachmentKinds", "requiredTools", "requiredMcpServers",
		"allowedAuthorities", "personaIds", "priority",
	)
)

const mgmtSkillSelect = `
	SELECT id, name, description, instructions, enabled, activation_mode,
		triggers_json, attachment_kinds_json, required_tools_json,
		required_mcp_servers_json, allowed_authorities_json, persona_ids_json,
		priority, created_at, updated_at FROM skills`

const skillCatalogSelect = `
	SELECT id, name, description, enabled, activation_mode,
		triggers_json, attachment_kinds_json, required_tools_json,
		required_mcp_servers_json, allowed_authorities_json, persona_ids_json,
		priority FROM skills`

func scanMgmtSkill(scanner interface{ Scan(...any) error }) (mgmtSkill, error) {
	var value mgmtSkill
	var enabled int
	var triggers, attachments, tools, mcp, authorities, personas string
	err := scanner.Scan(
		&value.ID, &value.Name, &value.Description, &value.Instructions, &enabled,
		&value.ActivationMode, &triggers, &attachments, &tools, &mcp, &authorities,
		&personas, &value.Priority, &value.CreatedAt, &value.UpdatedAt,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.Triggers = decodeJSONStringList(triggers)
	value.AttachmentKinds = decodeJSONStringList(attachments)
	value.RequiredTools = decodeJSONStringList(tools)
	value.RequiredMCPServers = decodeJSONStringList(mcp)
	value.AllowedAuthorities = decodeJSONStringList(authorities)
	value.PersonaIDs = decodeJSONStringList(personas)
	return value, nil
}

func scanSkillCatalogEntry(scanner interface{ Scan(...any) error }) (skillCatalogEntry, error) {
	var value skillCatalogEntry
	var enabled int
	var triggers, attachments, tools, mcp, authorities, personas string
	err := scanner.Scan(
		&value.ID, &value.Name, &value.Description, &enabled, &value.ActivationMode,
		&triggers, &attachments, &tools, &mcp, &authorities, &personas, &value.Priority,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.Triggers = decodeJSONStringList(triggers)
	value.AttachmentKinds = decodeJSONStringList(attachments)
	value.RequiredTools = decodeJSONStringList(tools)
	value.RequiredMCPServers = decodeJSONStringList(mcp)
	value.AllowedAuthorities = decodeJSONStringList(authorities)
	value.PersonaIDs = decodeJSONStringList(personas)
	return value, nil
}

func (value skillCatalogEntry) mgmtSkill() mgmtSkill {
	return mgmtSkill{
		ID: value.ID, Name: value.Name, Description: value.Description,
		Enabled: value.Enabled, ActivationMode: value.ActivationMode,
		Triggers: value.Triggers, AttachmentKinds: value.AttachmentKinds,
		RequiredTools: value.RequiredTools, RequiredMCPServers: value.RequiredMCPServers,
		AllowedAuthorities: value.AllowedAuthorities, PersonaIDs: value.PersonaIDs,
		Priority: value.Priority,
	}
}

func (s *coreConfigStore) mgmtSkill(id string) (mgmtSkill, bool, error) {
	value, err := scanMgmtSkill(s.db.QueryRow(mgmtSkillSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtSkills() ([]mgmtSkill, error) {
	rows, err := s.db.Query(mgmtSkillSelect + " ORDER BY priority DESC, name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtSkill{}
	for rows.Next() {
		value, scanErr := scanMgmtSkill(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *coreConfigStore) skillCatalog(query string, limit int) ([]skillCatalogEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.Query(skillCatalogSelect + " ORDER BY priority DESC, name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	needle := strings.ToLower(strings.TrimSpace(query))
	values := []skillCatalogEntry{}
	for rows.Next() {
		value, scanErr := scanSkillCatalogEntry(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if needle != "" && !strings.Contains(strings.ToLower(value.ID+" "+value.Name+" "+value.Description+" "+strings.Join(value.Triggers, " ")), needle) {
			continue
		}
		values = append(values, value)
		if len(values) >= limit {
			break
		}
	}
	return values, rows.Err()
}

func normalizeSkillList(values []string, name string, maxItems, maxLength int) ([]string, error) {
	return normalizeCoreStrings(values, name, maxItems, maxLength)
}

func skillAttachmentKinds(values []string) ([]string, error) {
	values, err := normalizeSkillList(values, "attachmentKinds", 4, 20)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if value != "image" && value != "audio" && value != "video" && value != "file" {
			return nil, coreInvalid("attachmentKinds contains unsupported values: " + value)
		}
	}
	return values, nil
}

func mgmtSkillValues(input mgmtSkillPayload, current *mgmtSkill) (mgmtSkill, error) {
	value := mgmtSkill{
		Enabled: true, ActivationMode: "any", AllowedAuthorities: []string{"member", "admin"},
		Triggers: []string{}, AttachmentKinds: []string{}, RequiredTools: []string{},
		RequiredMCPServers: []string{}, PersonaIDs: []string{},
	}
	if current != nil {
		value = *current
	}
	var err error
	if input.Name != nil {
		value.Name, err = normalizeCoreText(*input.Name, "name", 120, true)
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("name is required")
	}
	if input.Description != nil {
		value.Description, err = normalizeCoreText(*input.Description, "description", 2000, false)
		if err != nil {
			return value, err
		}
	}
	if input.Instructions != nil {
		value.Instructions, err = normalizeCoreText(*input.Instructions, "instructions", 12000, true)
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("instructions are required")
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if input.ActivationMode != nil {
		value.ActivationMode, err = normalizeCoreText(*input.ActivationMode, "activationMode", 20, true)
		if err != nil {
			return value, err
		}
	}
	if value.ActivationMode != "any" && value.ActivationMode != "all" && value.ActivationMode != "always" {
		return value, coreInvalid("activationMode is not supported")
	}
	if input.Triggers != nil {
		value.Triggers, err = normalizeSkillList(*input.Triggers, "triggers", 80, 120)
		if err != nil {
			return value, err
		}
	}
	if input.AttachmentKinds != nil {
		value.AttachmentKinds, err = skillAttachmentKinds(*input.AttachmentKinds)
		if err != nil {
			return value, err
		}
	}
	if input.RequiredTools != nil {
		value.RequiredTools, err = normalizeSkillList(*input.RequiredTools, "requiredTools", 40, 120)
		if err != nil {
			return value, err
		}
	}
	if input.RequiredMCPServers != nil {
		value.RequiredMCPServers, err = normalizeSkillList(*input.RequiredMCPServers, "requiredMcpServers", 40, 120)
		if err != nil {
			return value, err
		}
	}
	if input.AllowedAuthorities != nil {
		value.AllowedAuthorities, err = mgmtAuthorities(*input.AllowedAuthorities)
		if err != nil {
			return value, err
		}
	}
	if input.PersonaIDs != nil {
		value.PersonaIDs, err = normalizeSkillList(*input.PersonaIDs, "personaIds", 40, 120)
		if err != nil {
			return value, err
		}
	}
	if input.Priority != nil {
		value.Priority = *input.Priority
	}
	if value.Priority < -1000 || value.Priority > 1000 {
		return value, coreInvalid("priority must be between -1000 and 1000")
	}
	if value.ActivationMode != "always" && len(value.Triggers) == 0 && len(value.AttachmentKinds) == 0 {
		return value, coreInvalid("a non-always skill needs triggers or attachmentKinds")
	}
	return value, nil
}

func (s *coreConfigStore) mgmtCreateSkill(r *http.Request) (mgmtSkill, error) {
	var input mgmtSkillPayload
	fields, err := decodeCoreObject(r, mgmtSkillCreateFields, "skill", &input)
	if err != nil {
		return mgmtSkill{}, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return mgmtSkill{}, err
	}
	id := ""
	if input.ID != nil {
		id, err = mgmtIdentifier(*input.ID, "id")
	} else {
		id, err = newCoreUUID()
	}
	if err != nil {
		return mgmtSkill{}, err
	}
	value, err := mgmtSkillValues(input, nil)
	if err != nil {
		return mgmtSkill{}, err
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO skills (
			id, name, description, instructions, enabled, activation_mode, triggers_json,
			attachment_kinds_json, required_tools_json, required_mcp_servers_json,
			allowed_authorities_json, persona_ids_json, priority, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.Name, value.Description, value.Instructions, boolInt(value.Enabled),
		value.ActivationMode, mgmtJSON(value.Triggers), mgmtJSON(value.AttachmentKinds),
		mgmtJSON(value.RequiredTools), mgmtJSON(value.RequiredMCPServers),
		mgmtJSON(value.AllowedAuthorities), mgmtJSON(value.PersonaIDs), value.Priority, now, now)
	if err != nil {
		return mgmtSkill{}, mgmtConstraintError(err, "skill id or name already exists")
	}
	if err = s.mgmtAudit("create", "skill", id, mgmtFieldNames(fields)); err != nil {
		return mgmtSkill{}, err
	}
	value, _, err = s.mgmtSkill(id)
	return value, err
}

func (s *coreConfigStore) mgmtUpdateSkill(r *http.Request, id string) (mgmtSkill, bool, error) {
	current, found, err := s.mgmtSkill(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtSkillPayload
	fields, err := decodeCoreObject(r, mgmtSkillUpdateFields, "skill", &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	value, err := mgmtSkillValues(input, &current)
	if err != nil {
		return current, true, err
	}
	_, err = s.db.Exec(`
		UPDATE skills SET name = ?, description = ?, instructions = ?, enabled = ?,
			activation_mode = ?, triggers_json = ?, attachment_kinds_json = ?,
			required_tools_json = ?, required_mcp_servers_json = ?,
			allowed_authorities_json = ?, persona_ids_json = ?, priority = ?, updated_at = ?
		WHERE id = ?
	`, value.Name, value.Description, value.Instructions, boolInt(value.Enabled),
		value.ActivationMode, mgmtJSON(value.Triggers), mgmtJSON(value.AttachmentKinds),
		mgmtJSON(value.RequiredTools), mgmtJSON(value.RequiredMCPServers),
		mgmtJSON(value.AllowedAuthorities), mgmtJSON(value.PersonaIDs), value.Priority, mgmtNow(), id)
	if err != nil {
		return current, true, mgmtConstraintError(err, "skill id or name already exists")
	}
	if err = s.mgmtAudit("update", "skill", id, mgmtFieldNames(fields)); err != nil {
		return current, true, err
	}
	value, _, err = s.mgmtSkill(id)
	return value, true, err
}

func (s *coreConfigStore) handleManagementSkills(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/skills/catalog" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		values, err := s.skillCatalog(r.URL.Query().Get("q"), limit)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	if path == "/api/v1/skills" {
		switch r.Method {
		case http.MethodGet:
			limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
			values, err := s.skillCatalog(r.URL.Query().Get("q"), limit)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateSkill(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/skills/")
	if err != nil {
		return err
	}
	id, err = mgmtIdentifier(id, "id")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtSkill(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("skill")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateSkill(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("skill")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM skills WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "skill"); err != nil {
			return err
		}
		if err = s.mgmtAudit("delete", "skill", id, nil); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func skillListContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func skillMatches(value mgmtSkill, authority, personaID, message string, attachments []string) bool {
	if !value.Enabled || !skillListContains(value.AllowedAuthorities, authority) {
		return false
	}
	if len(value.PersonaIDs) > 0 && !skillListContains(value.PersonaIDs, personaID) {
		return false
	}
	if value.ActivationMode == "always" {
		return true
	}
	if skillListContains(value.RequiredTools, "create_office_document") && officeDocumentRequestIntent(message) {
		return true
	}
	normalized := strings.ToLower(message)
	triggerHits := 0
	for _, trigger := range value.Triggers {
		if strings.Contains(normalized, strings.ToLower(trigger)) {
			triggerHits++
		}
	}
	attachmentHits := 0
	for _, kind := range value.AttachmentKinds {
		if skillListContains(attachments, kind) {
			attachmentHits++
		}
	}
	if value.ActivationMode == "all" {
		return (len(value.Triggers) == 0 || triggerHits == len(value.Triggers)) &&
			(len(value.AttachmentKinds) == 0 || attachmentHits == len(value.AttachmentKinds))
	}
	return triggerHits > 0 || attachmentHits > 0
}

func (s *coreConfigStore) matchedRuntimeSkills(
	authority, personaID, message string, attachments []string,
) ([]mgmtSkill, int, error) {
	rows, err := s.db.Query(mgmtSkillSelect + " WHERE enabled = 1 ORDER BY priority DESC, id")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	matched := []mgmtSkill{}
	enabled := 0
	for rows.Next() {
		value, scanErr := scanMgmtSkill(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		enabled++
		if skillMatches(value, authority, personaID, message, attachments) {
			matched = append(matched, value)
		}
	}
	return matched, enabled, rows.Err()
}

func (s *coreConfigStore) matchedRuntimeSkillCatalog(
	authority, personaID, message string, attachments []string,
) ([]skillCatalogEntry, int, error) {
	rows, err := s.db.Query(skillCatalogSelect + " WHERE enabled = 1 ORDER BY priority DESC, id")
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	matched := []skillCatalogEntry{}
	enabled := 0
	for rows.Next() {
		value, scanErr := scanSkillCatalogEntry(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		enabled++
		if skillMatches(value.mgmtSkill(), authority, personaID, message, attachments) {
			matched = append(matched, value)
		}
	}
	return matched, enabled, rows.Err()
}

func selectRuntimeSkillCatalog(values []skillCatalogEntry, max int) []skillCatalogEntry {
	if max <= 0 || len(values) <= max {
		return values
	}
	return values[:max]
}

func (s *coreConfigStore) loadRuntimeSkillDetails(ids []string) ([]mgmtSkill, error) {
	if len(ids) == 0 {
		return []mgmtSkill{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i], args[i] = "?", id
	}
	rows, err := s.db.Query(mgmtSkillSelect+" WHERE id IN ("+strings.Join(placeholders, ",")+")", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := make(map[string]mgmtSkill, len(ids))
	for rows.Next() {
		value, scanErr := scanMgmtSkill(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		byID[value.ID] = value
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	values := make([]mgmtSkill, 0, len(ids))
	for _, id := range ids {
		if value, ok := byID[id]; ok {
			values = append(values, value)
		}
	}
	return values, nil
}

func filterRuntimeToolPolicy(policy runtimeToolPolicy, skills []mgmtSkill, enabledSkills int) runtimeToolPolicy {
	if enabledSkills == 0 {
		return policy
	}
	allowedTools := map[string]bool{}
	allowedMCP := map[string]bool{}
	for _, skill := range skills {
		for _, value := range skill.RequiredTools {
			allowedTools[value] = true
		}
		for _, value := range skill.RequiredMCPServers {
			allowedMCP[value] = true
		}
	}
	tools := make([]runtimeTool, 0, len(policy.Tools))
	for _, tool := range policy.Tools {
		if allowedTools[tool.ID] || allowedTools[tool.Name] {
			tools = append(tools, tool)
		}
	}
	mcp := make([]runtimeMCPServer, 0, len(policy.MCPServers))
	for _, server := range policy.MCPServers {
		if allowedMCP[server.ID] || allowedMCP[server.Name] {
			mcp = append(mcp, server)
		}
	}
	policy.Tools = tools
	policy.MCPServers = mcp
	return policy
}

func runtimeSkills(values []mgmtSkill) []runtimeSkill {
	result := make([]runtimeSkill, 0, len(values))
	for _, value := range values {
		result = append(result, runtimeSkill{
			ID: value.ID, Name: value.Name, Description: value.Description,
			Instructions: value.Instructions,
		})
	}
	return result
}

func compileRuntimeSkills(values []runtimeSkill) string {
	if len(values) == 0 {
		return ""
	}
	sections := make([]string, 0, len(values))
	for _, value := range values {
		sections = append(sections, "### "+value.Name+"\n"+value.Instructions)
	}
	return strings.Join(sections, "\n\n")
}
