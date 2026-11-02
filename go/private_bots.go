package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Private Bots are the owner/workspace boundary around the legacy
// agent_instances runtime.  Keeping the runtime instance as an implementation
// detail lets old routes keep working while new sessions select one Bot.
type privateBot struct {
	ID              string `json:"id"`
	OwnerRef        string `json:"ownerRef"`
	WorkspaceID     string `json:"workspaceId"`
	DisplayName     string `json:"displayName"`
	PersonaID       string `json:"personaId"`
	AgentInstanceID string `json:"agentInstanceId"`
	MemoryNamespace string `json:"memoryNamespace"`
	Visibility      string `json:"visibility"`
	Status          string `json:"status"`
	SourceBotID     string `json:"sourceBotId,omitempty"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type privateBotPayload struct {
	ID             *string `json:"id"`
	OwnerRef       *string `json:"ownerRef"`
	WorkspaceID    *string `json:"workspaceId"`
	DisplayName    *string `json:"displayName"`
	PersonaID      *string `json:"personaId"`
	Visibility     *string `json:"visibility"`
	Status         *string `json:"status"`
	SourceBotID    *string `json:"sourceBotId"`
	IdempotencyKey *string `json:"idempotencyKey"`
}

type privateBotSession struct {
	ID                string `json:"id"`
	BotID             string `json:"botId"`
	OwnerRef          string `json:"ownerRef"`
	WorkspaceID       string `json:"workspaceId"`
	Transport         string `json:"transport"`
	TransportInstance string `json:"transportInstance"`
	ConversationRef   string `json:"conversationRef"`
	Active            bool   `json:"active"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type privateBotSessionPayload struct {
	OwnerRef          *string `json:"ownerRef"`
	WorkspaceID       *string `json:"workspaceId"`
	Transport         *string `json:"transport"`
	TransportInstance *string `json:"transportInstance"`
	ConversationRef   *string `json:"conversationRef"`
	Active            *bool   `json:"active"`
}

type privateBotACL struct {
	BotID      string `json:"botId"`
	SubjectRef string `json:"subjectRef"`
	Role       string `json:"role"`
	Enabled    bool   `json:"enabled"`
	CreatedAt  string `json:"createdAt"`
	UpdatedAt  string `json:"updatedAt"`
}

type privateBotActivity struct {
	ID           string `json:"id"`
	BotID        string `json:"botId"`
	SessionID    string `json:"sessionId,omitempty"`
	State        string `json:"state"`
	Kind         string `json:"kind"`
	InputSummary string `json:"inputSummary"`
	ErrorCode    string `json:"errorCode"`
	Attempts     int    `json:"attempts"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

// privateBotTask is the read model exposed to the Bot cockpit.  It deliberately
// reuses the runtime's durable agent_runs/task_steps tables instead of creating
// a second planner or task state machine for private Bots.
type privateBotTask struct {
	RunID        string `json:"runId"`
	State        string `json:"state"`
	TaskID       string `json:"taskId,omitempty"`
	TaskRevision int    `json:"taskRevision,omitempty"`
	ErrorCode    string `json:"errorCode,omitempty"`
	StepCount    int    `json:"stepCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
	Retryable    bool   `json:"retryable"`
	DetailURL    string `json:"detailUrl"`
	RetryURL     string `json:"retryUrl,omitempty"`
}

var privateBotFields = coreFieldSet("id", "ownerRef", "workspaceId", "displayName", "personaId", "visibility", "status", "sourceBotId", "idempotencyKey")
var privateBotSessionFields = coreFieldSet("ownerRef", "workspaceId", "transport", "transportInstance", "conversationRef", "active")
var privateBotACLFields = coreFieldSet("subjectRef", "role", "enabled")

func privateBotText(value, field string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if (required && value == "") || len(value) > max || strings.ContainsRune(value, '\x00') {
		return "", coreInvalid(field + " is invalid")
	}
	return value, nil
}

func privateBotDefaults(input *privateBotPayload) (owner, workspace, name, persona, visibility, status, source, idem string, err error) {
	if input.OwnerRef != nil {
		owner = *input.OwnerRef
	}
	if strings.TrimSpace(owner) == "" {
		owner = "admin"
	}
	if input.WorkspaceID != nil {
		workspace = *input.WorkspaceID
	}
	if input.DisplayName != nil {
		name = *input.DisplayName
	}
	if input.PersonaID != nil {
		persona = *input.PersonaID
	}
	if input.Visibility != nil {
		visibility = *input.Visibility
	}
	if input.Status != nil {
		status = *input.Status
	}
	if input.SourceBotID != nil {
		source = *input.SourceBotID
	}
	if input.IdempotencyKey != nil {
		idem = *input.IdempotencyKey
	}
	if owner, err = privateBotText(owner, "ownerRef", 160, true); err != nil {
		return
	}
	if workspace == "" {
		workspace = "default"
	}
	if workspace, err = privateBotText(workspace, "workspaceId", 120, true); err != nil {
		return
	}
	if name, err = privateBotText(name, "displayName", 160, true); err != nil {
		return
	}
	persona = strings.TrimSpace(persona)
	if visibility == "" {
		visibility = "private"
	}
	if status == "" {
		status = "active"
	}
	if source, err = privateBotText(source, "sourceBotId", 120, false); err != nil {
		return
	}
	if idem, err = privateBotText(idem, "idempotencyKey", 200, false); err != nil {
		return
	}
	if visibility != "private" && visibility != "shared" && visibility != "template" {
		err = coreInvalid("visibility must be private, shared, or template")
		return
	}
	if status != "active" && status != "paused" && status != "archived" {
		err = coreInvalid("status must be active, paused, or archived")
		return
	}
	return
}

func (s *coreConfigStore) privateBot(id string) (privateBot, bool, error) {
	var value privateBot
	err := s.db.QueryRow(`SELECT id, owner_ref, workspace_id, display_name, persona_id, agent_instance_id,
		memory_namespace, visibility, status, source_bot_id, created_at, updated_at FROM private_bots WHERE id = ?`, id).
		Scan(&value.ID, &value.OwnerRef, &value.WorkspaceID, &value.DisplayName, &value.PersonaID, &value.AgentInstanceID,
			&value.MemoryNamespace, &value.Visibility, &value.Status, &value.SourceBotID, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	if err != nil {
		return value, false, err
	}
	return value, true, nil
}

func (s *coreConfigStore) canAccessPrivateBot(id, owner string, write bool) (privateBot, error) {
	bot, found, err := s.privateBot(id)
	if err != nil {
		return bot, err
	}
	if !found {
		return bot, mgmtNotFound("private bot")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || owner == bot.OwnerRef {
		return bot, nil
	}
	// Templates are intentionally readable by every workspace so they can be
	// copied into a private Bot. They remain immutable to non-owners.
	if bot.Visibility == "template" && !write {
		return bot, nil
	}
	var role string
	err = s.db.QueryRow(`SELECT role FROM private_bot_acl WHERE bot_id = ? AND subject_ref = ? AND enabled = 1`, id, owner).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return bot, &coreAPIError{status: http.StatusForbidden, code: "forbidden", message: "private bot access denied"}
	}
	if err != nil {
		return bot, err
	}
	if write && role != "editor" && role != "owner" {
		return bot, &coreAPIError{status: http.StatusForbidden, code: "forbidden", message: "private bot is read-only"}
	}
	return bot, nil
}

func (s *coreConfigStore) listPrivateBots(w http.ResponseWriter, r *http.Request) error {
	owner, workspace := strings.TrimSpace(r.URL.Query().Get("ownerRef")), strings.TrimSpace(r.URL.Query().Get("workspaceId"))
	if owner == "" {
		owner = "admin"
	}
	// A collection is an owner's working set, so it includes explicitly shared
	// Bots and public templates the owner can actually use. Keep visibility
	// checks in SQL so the UI cannot show an object the read endpoint rejects.
	query := `SELECT id, owner_ref, workspace_id, display_name, persona_id, agent_instance_id, memory_namespace, visibility, status, source_bot_id, created_at, updated_at
		FROM private_bots
		WHERE (owner_ref = ? OR visibility = 'template' OR EXISTS (
			SELECT 1 FROM private_bot_acl acl
			WHERE acl.bot_id = private_bots.id AND acl.subject_ref = ? AND acl.enabled = 1
		))`
	args := []any{owner, owner}
	if workspace != "" {
		query += " AND (workspace_id = ? OR visibility = 'template')"
		args = append(args, workspace)
	}
	query += " ORDER BY updated_at DESC, id"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []privateBot{}
	for rows.Next() {
		var value privateBot
		if err = rows.Scan(&value.ID, &value.OwnerRef, &value.WorkspaceID, &value.DisplayName, &value.PersonaID, &value.AgentInstanceID, &value.MemoryNamespace, &value.Visibility, &value.Status, &value.SourceBotID, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return err
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	return nil
}

func (s *coreConfigStore) createPrivateBot(w http.ResponseWriter, r *http.Request, sourceID string) error {
	var input privateBotPayload
	if _, err := decodeCoreObject(r, privateBotFields, "private bot", &input); err != nil {
		return err
	}
	owner, workspace, name, persona, visibility, status, source, idem, err := privateBotDefaults(&input)
	if err != nil {
		return err
	}
	if sourceID != "" {
		source = sourceID
	}
	if source != "" {
		sourceBot, accessErr := s.canAccessPrivateBot(source, owner, false)
		if accessErr != nil {
			return accessErr
		}
		if sourceBot.Visibility == "private" && sourceBot.OwnerRef != owner {
			return &coreAPIError{status: http.StatusForbidden, code: "forbidden", message: "private bot template is not shareable"}
		}
		if persona == "" {
			persona = sourceBot.PersonaID
		}
		if input.Visibility == nil {
			visibility = "private"
		}
	}
	if persona == "" {
		return coreInvalid("personaId is required")
	}
	var personaExists int
	if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", persona).Scan(&personaExists); err != nil {
		return err
	}
	if personaExists != 1 {
		return mgmtNotFound("persona")
	}
	if idem != "" {
		var existingID string
		if err = s.db.QueryRow(`SELECT bot_id FROM private_bot_idempotency WHERE owner_ref = ? AND workspace_id = ? AND idempotency_key = ?`, owner, workspace, idem).Scan(&existingID); err == nil {
			value, _, getErr := s.privateBot(existingID)
			if getErr != nil {
				return getErr
			}
			mgmtWriteData(w, http.StatusOK, map[string]any{"bot": value, "created": false})
			return nil
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
	}
	botID := ""
	if input.ID != nil {
		botID = strings.TrimSpace(*input.ID)
	}
	if botID == "" {
		botID, err = newCoreUUID()
		if err != nil {
			return err
		}
	}
	if botID, err = privateBotText(botID, "id", 120, true); err != nil {
		return err
	}
	instanceID := "private-" + botID
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var policyID, overrides string
	if source != "" {
		if err = tx.QueryRow(`SELECT COALESCE(policy_template_id, ''), overrides_json FROM agent_instances WHERE id = (SELECT agent_instance_id FROM private_bots WHERE id = ?)`, source).Scan(&policyID, &overrides); errors.Is(err, sql.ErrNoRows) {
			policyID, overrides = "", "{}"
		} else if err != nil {
			return err
		}
	}
	if overrides == "" {
		overrides = "{}"
	}
	if _, err = tx.Exec(`INSERT INTO agent_instances(id, display_name, persona_id, policy_template_id, memory_namespace, overrides_json, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, instanceID, name, persona, privateNullableString(policyID), "private-bot:"+botID, overrides, boolInt(status == "active"), now, now); err != nil {
		return mgmtConstraintError(err, "private bot runtime instance already exists")
	}
	if _, err = tx.Exec(`INSERT INTO private_bots(id, owner_ref, workspace_id, display_name, persona_id, agent_instance_id, memory_namespace, visibility, status, source_bot_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, botID, owner, workspace, name, persona, instanceID, "private-bot:"+botID, visibility, status, source, now, now); err != nil {
		return mgmtConstraintError(err, "private bot already exists")
	}
	if _, err = tx.Exec(`INSERT INTO private_bot_acl(bot_id, subject_ref, role, enabled, created_at, updated_at) VALUES (?, ?, 'owner', 1, ?, ?)`, botID, owner, now, now); err != nil {
		return err
	}
	if idem != "" {
		if _, err = tx.Exec(`INSERT INTO private_bot_idempotency(owner_ref, workspace_id, idempotency_key, bot_id, created_at) VALUES (?, ?, ?, ?, ?)`, owner, workspace, idem, botID, now); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	value, _, err := s.privateBot(botID)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusCreated, map[string]any{"bot": value, "created": true})
	return nil
}

func privateNullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func (s *coreConfigStore) updatePrivateBot(w http.ResponseWriter, r *http.Request, id string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	bot, err := s.canAccessPrivateBot(id, owner, r.Method != http.MethodGet)
	if err != nil {
		return err
	}
	var input privateBotPayload
	if _, err = decodeCoreObject(r, privateBotFields, "private bot", &input); err != nil {
		return err
	}
	if input.DisplayName != nil {
		if bot.DisplayName, err = privateBotText(*input.DisplayName, "displayName", 160, true); err != nil {
			return err
		}
	}
	if input.PersonaID != nil {
		bot.PersonaID = strings.TrimSpace(*input.PersonaID)
		if bot.PersonaID == "" {
			return coreInvalid("personaId is invalid")
		}
	}
	if input.Visibility != nil {
		bot.Visibility = strings.TrimSpace(*input.Visibility)
	}
	if input.Status != nil {
		bot.Status = strings.TrimSpace(*input.Status)
	}
	if strings.TrimSpace(bot.Status) != "archived" && strings.TrimSpace(bot.Status) != "" {
		var currentStatus string
		if err = s.db.QueryRow("SELECT status FROM private_bots WHERE id = ?", id).Scan(&currentStatus); err != nil {
			return err
		}
		if currentStatus == "archived" {
			return &coreAPIError{status: http.StatusConflict, code: "bot_archived", message: "archived private bot cannot be resumed"}
		}
	}
	if bot.Visibility != "private" && bot.Visibility != "shared" && bot.Visibility != "template" {
		return coreInvalid("visibility must be private, shared, or template")
	}
	if bot.Status != "active" && bot.Status != "paused" && bot.Status != "archived" {
		return coreInvalid("status must be active, paused, or archived")
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", bot.PersonaID).Scan(&exists); err != nil {
		return err
	}
	if exists != 1 {
		return mgmtNotFound("persona")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE private_bots SET display_name = ?, persona_id = ?, visibility = ?, status = ?, updated_at = ? WHERE id = ?`, bot.DisplayName, bot.PersonaID, bot.Visibility, bot.Status, now, id); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE agent_instances SET display_name = ?, persona_id = ?, enabled = ?, updated_at = ? WHERE id = ?`, bot.DisplayName, bot.PersonaID, boolInt(bot.Status == "active"), now, bot.AgentInstanceID); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	value, _, err := s.privateBot(id)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func (s *coreConfigStore) selectPrivateBot(w http.ResponseWriter, r *http.Request, id string) error {
	var input privateBotSessionPayload
	if _, err := decodeCoreObject(r, privateBotSessionFields, "private bot session", &input); err != nil {
		return err
	}
	owner := ""
	workspace := "default"
	transport, transportInstance, conversation := "", "", ""
	if input.OwnerRef != nil {
		owner = strings.TrimSpace(*input.OwnerRef)
	}
	if input.WorkspaceID != nil && strings.TrimSpace(*input.WorkspaceID) != "" {
		workspace = strings.TrimSpace(*input.WorkspaceID)
	}
	if input.Transport != nil {
		transport = strings.ToLower(strings.TrimSpace(*input.Transport))
	}
	if input.TransportInstance != nil {
		transportInstance = strings.TrimSpace(*input.TransportInstance)
	}
	if input.ConversationRef != nil {
		conversation = strings.TrimSpace(*input.ConversationRef)
	}
	if owner == "" || transport == "" || conversation == "" {
		return coreInvalid("ownerRef, transport, and conversationRef are required")
	}
	if transportInstance == "" {
		transportInstance = "*"
	}
	bot, err := s.canAccessPrivateBot(id, owner, true)
	if err != nil {
		return err
	}
	if bot.Status != "active" {
		return &coreAPIError{status: http.StatusConflict, code: "bot_not_active", message: "private bot is paused or archived"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	sessionID, err := newCoreUUID()
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Runtime resolution has no owner/workspace argument, so a transport scope
	// can only have one owner. Do not silently steal a scope from another
	// workspace; make the conflict actionable instead. A caller may still
	// replace its own selection for the same conversation.
	var existingOwner, existingWorkspace, existingBot string
	if err = tx.QueryRow(`SELECT owner_ref, workspace_id, bot_id FROM private_bot_sessions
		WHERE active = 1
		  AND transport IN (?, '*') AND ? IN (transport_instance, '*')
		  AND conversation_ref IN (?, '*') AND ? IN (conversation_ref, '*')
		ORDER BY updated_at DESC LIMIT 1`, transport, transportInstance, conversation, conversation).
		Scan(&existingOwner, &existingWorkspace, &existingBot); err == nil {
		if existingOwner != owner || existingWorkspace != workspace {
			return &coreAPIError{status: http.StatusConflict, code: "private_bot_scope_conflict", message: "conversation is already assigned to another owner or workspace"}
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// A conversation has one active Bot per owner/workspace. Deactivate the
	// previous selection before writing the replacement in the same transaction.
	if _, err = tx.Exec(`UPDATE private_bot_sessions SET active = 0, updated_at = ?
		WHERE owner_ref = ? AND workspace_id = ? AND active = 1
		  AND transport IN (?, '*') AND ? IN (transport_instance, '*')
		  AND conversation_ref IN (?, '*') AND ? IN (conversation_ref, '*')`, now, owner, workspace,
		transport, transportInstance, conversation, conversation); err != nil {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO private_bot_sessions(id, bot_id, owner_ref, workspace_id, transport, transport_instance, conversation_ref, active, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?) ON CONFLICT(owner_ref, workspace_id, transport, transport_instance, conversation_ref) DO UPDATE SET bot_id=excluded.bot_id, active=1, updated_at=excluded.updated_at`, sessionID, id, owner, workspace, transport, transportInstance, conversation, now, now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	value, err := s.privateBotSessionForScope(owner, workspace, transport, transportInstance, conversation)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func (s *coreConfigStore) handlePrivateBotACL(w http.ResponseWriter, r *http.Request, id string, subject string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	bot, err := s.canAccessPrivateBot(id, owner, r.Method != http.MethodGet)
	if err != nil {
		return err
	}
	if r.Method != http.MethodGet && bot.OwnerRef != owner {
		return &coreAPIError{status: http.StatusForbidden, code: "forbidden", message: "only the Bot owner can manage ACL"}
	}
	if r.Method == http.MethodGet {
		rows, queryErr := s.db.Query(`SELECT bot_id, subject_ref, role, enabled, created_at, updated_at FROM private_bot_acl WHERE bot_id = ? ORDER BY subject_ref`, id)
		if queryErr != nil {
			return queryErr
		}
		defer rows.Close()
		items := []privateBotACL{}
		for rows.Next() {
			var value privateBotACL
			var enabled int
			if err = rows.Scan(&value.BotID, &value.SubjectRef, &value.Role, &enabled, &value.CreatedAt, &value.UpdatedAt); err != nil {
				return err
			}
			value.Enabled = enabled == 1
			items = append(items, value)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
		return nil
	}
	if subject != "" {
		if r.Method != http.MethodDelete {
			return mgmtMethodNotAllowed()
		}
		if subject == bot.OwnerRef {
			return coreInvalid("owner ACL cannot be removed")
		}
		result, deleteErr := s.db.Exec("DELETE FROM private_bot_acl WHERE bot_id = ? AND subject_ref = ?", id, subject)
		if deleteErr != nil {
			return deleteErr
		}
		if _, deleteErr = mgmtDeleteResult(result, "ACL entry"); deleteErr != nil {
			return deleteErr
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"botId": id, "subjectRef": subject, "deleted": true})
		return nil
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	var input struct {
		SubjectRef string `json:"subjectRef"`
		Role       string `json:"role"`
		Enabled    *bool  `json:"enabled"`
	}
	if _, err = decodeCoreObject(r, privateBotACLFields, "private bot ACL", &input); err != nil {
		return err
	}
	input.SubjectRef, err = privateBotText(input.SubjectRef, "subjectRef", 160, true)
	if err != nil {
		return err
	}
	if input.SubjectRef == bot.OwnerRef {
		input.Role = "owner"
	} else if input.Role == "" {
		input.Role = "viewer"
	}
	if input.Role != "owner" && input.Role != "editor" && input.Role != "viewer" {
		return coreInvalid("role must be owner, editor, or viewer")
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO private_bot_acl(bot_id, subject_ref, role, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(bot_id, subject_ref) DO UPDATE SET role=excluded.role, enabled=excluded.enabled, updated_at=excluded.updated_at`, id, input.SubjectRef, input.Role, boolInt(enabled), now, now)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, privateBotACL{BotID: id, SubjectRef: input.SubjectRef, Role: input.Role, Enabled: enabled, CreatedAt: now, UpdatedAt: now})
	return nil
}

func (s *coreConfigStore) privateBotSessionForScope(owner, workspace, transport, transportInstance, conversation string) (privateBotSession, error) {
	var value privateBotSession
	var active int
	err := s.db.QueryRow(`SELECT id, bot_id, owner_ref, workspace_id, transport, transport_instance, conversation_ref, active, created_at, updated_at FROM private_bot_sessions WHERE owner_ref = ? AND workspace_id = ? AND transport = ? AND transport_instance = ? AND conversation_ref = ?`, owner, workspace, transport, transportInstance, conversation).Scan(&value.ID, &value.BotID, &value.OwnerRef, &value.WorkspaceID, &value.Transport, &value.TransportInstance, &value.ConversationRef, &active, &value.CreatedAt, &value.UpdatedAt)
	value.Active = active == 1
	return value, err
}

func (s *coreConfigStore) listPrivateBotActivities(w http.ResponseWriter, r *http.Request, id string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	if _, err := s.canAccessPrivateBot(id, owner, false); err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT id, bot_id, COALESCE(session_id, ''), state, kind, input_summary, error_code, attempts, created_at, updated_at FROM private_bot_activities WHERE bot_id = ? ORDER BY updated_at DESC, id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []privateBotActivity{}
	for rows.Next() {
		var value privateBotActivity
		if err = rows.Scan(&value.ID, &value.BotID, &value.SessionID, &value.State, &value.Kind, &value.InputSummary, &value.ErrorCode, &value.Attempts, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return err
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	return nil
}

func (s *coreConfigStore) createPrivateBotActivity(w http.ResponseWriter, r *http.Request, id string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	if _, err := s.canAccessPrivateBot(id, owner, true); err != nil {
		return err
	}
	var input struct {
		SessionID    string `json:"sessionId"`
		State        string `json:"state"`
		Kind         string `json:"kind"`
		InputSummary string `json:"inputSummary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return coreInvalid("invalid activity body")
	}
	if input.State == "" {
		input.State = "waiting"
	}
	if input.Kind == "" {
		input.Kind = "message"
	}
	var normalizeErr error
	if input.SessionID, normalizeErr = privateBotText(input.SessionID, "sessionId", 160, false); normalizeErr != nil {
		return normalizeErr
	}
	if input.Kind, normalizeErr = privateBotText(input.Kind, "kind", 80, true); normalizeErr != nil {
		return normalizeErr
	}
	if input.InputSummary, normalizeErr = privateBotText(input.InputSummary, "inputSummary", 2000, false); normalizeErr != nil {
		return normalizeErr
	}
	if !validPrivateBotState(input.State) {
		return coreInvalid("invalid activity state")
	}
	var err error
	if input.SessionID != "" {
		var sessionExists int
		if normalizeErr = s.db.QueryRow("SELECT count(*) FROM private_bot_sessions WHERE id = ? AND bot_id = ?", input.SessionID, id).Scan(&sessionExists); normalizeErr != nil {
			return normalizeErr
		}
		if sessionExists != 1 {
			return coreInvalid("sessionId does not belong to private bot")
		}
	}
	activityID, err := newCoreUUID()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = s.db.Exec(`INSERT INTO private_bot_activities(id, bot_id, session_id, state, kind, input_summary, created_at, updated_at) VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`, activityID, id, input.SessionID, input.State, input.Kind, input.InputSummary, now, now); err != nil {
		return err
	}
	value := privateBotActivity{ID: activityID, BotID: id, SessionID: input.SessionID, State: input.State, Kind: input.Kind, InputSummary: input.InputSummary, CreatedAt: now, UpdatedAt: now}
	mgmtWriteData(w, http.StatusCreated, value)
	return nil
}

func validPrivateBotState(value string) bool {
	switch value {
	case "waiting", "blocked", "retrying", "paused", "failed", "completed":
		return true
	}
	return false
}

func validPrivateBotTransition(from, to string) bool {
	if from == to {
		return from != "failed" && from != "completed"
	}
	switch from {
	case "waiting":
		return to == "blocked" || to == "retrying" || to == "paused" || to == "failed" || to == "completed"
	case "blocked":
		return to == "retrying" || to == "paused" || to == "failed" || to == "completed"
	case "retrying":
		return to == "waiting" || to == "blocked" || to == "paused" || to == "failed" || to == "completed"
	case "paused":
		return to == "waiting" || to == "retrying" || to == "failed"
	case "failed":
		return to == "retrying"
	default:
		return false
	}
}

func (s *coreConfigStore) transitionPrivateBotActivity(w http.ResponseWriter, r *http.Request, botID, activityID string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	if _, err := s.canAccessPrivateBot(botID, owner, true); err != nil {
		return err
	}
	var input struct {
		State     string `json:"state"`
		ErrorCode string `json:"errorCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		return coreInvalid("invalid activity transition")
	}
	input.State = strings.TrimSpace(input.State)
	input.ErrorCode = strings.TrimSpace(input.ErrorCode)
	if len(input.ErrorCode) > 160 {
		return coreInvalid("errorCode is invalid")
	}
	if !validPrivateBotState(input.State) {
		return coreInvalid("invalid activity state")
	}
	var current string
	if err := s.db.QueryRow("SELECT state FROM private_bot_activities WHERE id = ? AND bot_id = ?", activityID, botID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("activity")
	} else if err != nil {
		return err
	}
	if current == "completed" || (current == "failed" && input.State != "retrying") {
		return &coreAPIError{status: http.StatusConflict, code: "activity_terminal", message: "activity is already terminal"}
	}
	if !validPrivateBotTransition(current, input.State) {
		return &coreAPIError{status: http.StatusConflict, code: "invalid_activity_transition", message: "activity state transition is not allowed"}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.db.Exec(`UPDATE private_bot_activities SET state = ?, error_code = ?, attempts = attempts + CASE WHEN ? = 'retrying' THEN 1 ELSE 0 END, updated_at = ? WHERE id = ? AND bot_id = ?`, input.State, strings.TrimSpace(input.ErrorCode), input.State, now, activityID, botID)
	if err != nil {
		return err
	}
	var value privateBotActivity
	err = s.db.QueryRow(`SELECT id, bot_id, COALESCE(session_id, ''), state, kind, input_summary, error_code, attempts, created_at, updated_at FROM private_bot_activities WHERE id = ?`, activityID).Scan(&value.ID, &value.BotID, &value.SessionID, &value.State, &value.Kind, &value.InputSummary, &value.ErrorCode, &value.Attempts, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func (s *coreConfigStore) handlePrivateBotRequest(w http.ResponseWriter, r *http.Request, path string) error {
	prefix := "/api/v1/private-bots"
	if strings.HasPrefix(path, "/api/v1/bots") {
		prefix = "/api/v1/bots"
	}
	if path == prefix {
		if r.Method == http.MethodGet {
			return s.listPrivateBots(w, r)
		}
		if r.Method == http.MethodPost {
			return s.createPrivateBot(w, r, "")
		}
		return mgmtMethodNotAllowed()
	}
	rest := strings.TrimPrefix(path, prefix+"/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		return mgmtNotFound("private bot")
	}
	id, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	if len(parts) == 2 && parts[1] == "tasks" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		return s.listPrivateBotTasks(w, r, id, nil)
	}
	if len(parts) >= 2 && parts[1] == "copy" {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		return s.createPrivateBot(w, r, id)
	}
	if len(parts) >= 2 && parts[1] == "select" {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		return s.selectPrivateBot(w, r, id)
	}
	if len(parts) >= 2 && parts[1] == "acl" {
		subject := ""
		if len(parts) >= 3 {
			subject, err = parseCorePathID(parts[2])
			if err != nil {
				return err
			}
		}
		return s.handlePrivateBotACL(w, r, id, subject)
	}
	if len(parts) >= 2 && parts[1] == "sessions" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		return s.listPrivateBotSessions(w, r, id)
	}
	if len(parts) >= 2 && parts[1] == "activities" {
		if len(parts) == 2 {
			if r.Method == http.MethodGet {
				return s.listPrivateBotActivities(w, r, id)
			}
			if r.Method == http.MethodPost {
				return s.createPrivateBotActivity(w, r, id)
			}
			return mgmtMethodNotAllowed()
		}
		if len(parts) == 4 && parts[3] == "transition" {
			if r.Method != http.MethodPost {
				return mgmtMethodNotAllowed()
			}
			activityID, parseErr := parseCorePathID(parts[2])
			if parseErr != nil {
				return parseErr
			}
			return s.transitionPrivateBotActivity(w, r, id, activityID)
		}
		if len(parts) == 3 {
			if r.Method != http.MethodPost {
				return mgmtMethodNotAllowed()
			}
			activityID, parseErr := parseCorePathID(parts[2])
			if parseErr != nil {
				return parseErr
			}
			return s.transitionPrivateBotActivity(w, r, id, activityID)
		}
	}
	if len(parts) != 1 {
		return mgmtNotFound("private bot")
	}
	switch r.Method {
	case http.MethodGet:
		owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
		if owner == "" {
			owner = "admin"
		}
		value, err := s.canAccessPrivateBot(id, owner, false)
		if err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut, http.MethodPatch:
		return s.updatePrivateBot(w, r, id)
	case http.MethodDelete:
		owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
		if owner == "" {
			owner = "admin"
		}
		bot, err := s.canAccessPrivateBot(id, owner, true)
		if err != nil {
			return err
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec("UPDATE private_bots SET status = 'archived', updated_at = ? WHERE id = ?", now, id); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE agent_instances SET enabled = 0, updated_at = ? WHERE id = ?", now, bot.AgentInstanceID); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "status": "archived"})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

// handlePrivateBotRequestWithRuntime keeps the control-plane store as the
// owner/ACL source of truth while reading task state from the runtime database
// when deployments use separate SQLite files. Unified production databases use
// the same handle for both and incur no extra behavior.
func (s *coreConfigStore) handlePrivateBotRequestWithRuntime(a *AgentRuntime, w http.ResponseWriter, r *http.Request, path string) error {
	prefix := "/api/v1/private-bots"
	if strings.HasPrefix(path, "/api/v1/bots") {
		prefix = "/api/v1/bots"
	}
	rest := strings.TrimPrefix(path, prefix+"/")
	parts := strings.Split(rest, "/")
	if len(parts) == 2 && parts[0] != "" && parts[1] == "tasks" {
		id, err := parseCorePathID(parts[0])
		if err != nil {
			return err
		}
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		var runtimeDB *sql.DB
		if a != nil {
			runtimeDB = a.db
		}
		return s.listPrivateBotTasks(w, r, id, runtimeDB)
	}
	return s.handlePrivateBotRequest(w, r, path)
}

func (s *coreConfigStore) listPrivateBotSessions(w http.ResponseWriter, r *http.Request, id string) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	if _, err := s.canAccessPrivateBot(id, owner, false); err != nil {
		return err
	}
	rows, err := s.db.Query(`SELECT id, bot_id, owner_ref, workspace_id, transport, transport_instance, conversation_ref, active, created_at, updated_at FROM private_bot_sessions WHERE bot_id = ? ORDER BY updated_at DESC`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []privateBotSession{}
	for rows.Next() {
		var value privateBotSession
		var active int
		if err = rows.Scan(&value.ID, &value.BotID, &value.OwnerRef, &value.WorkspaceID, &value.Transport, &value.TransportInstance, &value.ConversationRef, &active, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return err
		}
		value.Active = active == 1
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	return nil
}

func (s *coreConfigStore) listPrivateBotTasks(w http.ResponseWriter, r *http.Request, id string, runtimeDB *sql.DB) error {
	owner := strings.TrimSpace(r.URL.Query().Get("ownerRef"))
	if owner == "" {
		owner = "admin"
	}
	bot, err := s.canAccessPrivateBot(id, owner, false)
	if err != nil {
		return err
	}
	if runtimeDB == nil {
		runtimeDB = s.db
	}
	if runtimeDB == nil {
		return errors.New("runtime database is unavailable")
	}
	rows, err := runtimeDB.QueryContext(r.Context(), `SELECT run.id, run.state,
		COALESCE(run.task_id, ''), COALESCE(run.task_revision, 0), COALESCE(run.error_code, ''),
		count(step.id), run.created_at, run.updated_at,
		CASE WHEN run.state IN ('failed', 'cancelled') AND length(COALESCE(run.input_cipher, X'')) > 0 THEN 1 ELSE 0 END
		FROM agent_runs run
		LEFT JOIN agent_task_steps step ON step.run_id = run.id
		WHERE run.agent_instance_id = ?
		GROUP BY run.id ORDER BY run.updated_at DESC, run.id LIMIT 100`, bot.AgentInstanceID)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []privateBotTask{}
	for rows.Next() {
		var item privateBotTask
		var retryable int
		if err = rows.Scan(&item.RunID, &item.State, &item.TaskID, &item.TaskRevision, &item.ErrorCode,
			&item.StepCount, &item.CreatedAt, &item.UpdatedAt, &retryable); err != nil {
			return err
		}
		item.Retryable = retryable == 1
		item.DetailURL = "/api/v1/tasks/" + item.RunID
		if item.Retryable {
			item.RetryURL = item.DetailURL + "/retry"
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
	return nil
}
