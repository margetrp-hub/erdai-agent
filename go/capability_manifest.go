package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// capabilityManifest is the lifecycle record for an installable capability.
// It is deliberately separate from tools/agent_plugins: those tables describe
// the Core catalogue, while this table records an immutable version that can
// later be bound to a Bot or workspace. Registration never implies execution.
type capabilityManifest struct {
	ID            string         `json:"id"`
	Version       string         `json:"version"`
	Kind          string         `json:"kind"`
	Digest        string         `json:"digest"`
	Source        string         `json:"source"`
	State         string         `json:"state"`
	Manifest      map[string]any `json:"manifest"`
	RuntimeReady  bool           `json:"runtimeReady"`
	RuntimeReason string         `json:"runtimeReason"`
	CreatedAt     string         `json:"createdAt"`
	UpdatedAt     string         `json:"updatedAt"`
}

type capabilityManifestPayload struct {
	ID       *string         `json:"id"`
	Version  *string         `json:"version"`
	Kind     *string         `json:"kind"`
	Digest   *string         `json:"digest"`
	Source   *string         `json:"source"`
	State    *string         `json:"state"`
	Manifest *map[string]any `json:"manifest"`
}

type capabilityBinding struct {
	ID           int64  `json:"id"`
	CapabilityID string `json:"capabilityId"`
	Version      string `json:"version"`
	BotID        string `json:"botId,omitempty"`
	WorkspaceID  string `json:"workspaceId,omitempty"`
	Enabled      bool   `json:"enabled"`
	GrantedBy    string `json:"grantedBy"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type capabilityBindingPayload struct {
	BotID       *string `json:"botId"`
	WorkspaceID *string `json:"workspaceId"`
	Enabled     *bool   `json:"enabled"`
	GrantedBy   *string `json:"grantedBy"`
}

var capabilityManifestCreateFields = coreFieldSet("id", "version", "kind", "digest", "source", "state", "manifest")
var capabilityManifestUpdateFields = coreFieldSet("digest", "source", "state", "manifest")
var capabilityBindingFields = coreFieldSet("botId", "workspaceId", "enabled", "grantedBy")

func ensureCapabilityManifestSchema(tx coreSchemaTx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS capability_manifests (
			id TEXT NOT NULL,
			version TEXT NOT NULL,
			kind TEXT NOT NULL CHECK (kind IN ('tool', 'plugin', 'skill', 'mcp')),
			digest TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'external' CHECK (source IN ('builtin', 'mcp', 'external')),
			state TEXT NOT NULL DEFAULT 'staged' CHECK (state IN ('draft', 'staged', 'active', 'paused', 'retired')),
			manifest_json TEXT NOT NULL DEFAULT '{}',
			runtime_ready INTEGER NOT NULL DEFAULT 0 CHECK (runtime_ready IN (0, 1)),
			runtime_reason TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (id, version)
		)`,
		`CREATE TABLE IF NOT EXISTS capability_bindings (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			capability_id TEXT NOT NULL,
			version TEXT NOT NULL,
			bot_id TEXT NOT NULL DEFAULT '',
			workspace_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
			granted_by TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (capability_id, version) REFERENCES capability_manifests(id, version) ON DELETE CASCADE,
			UNIQUE (capability_id, version, bot_id, workspace_id),
			CHECK (bot_id <> '' OR workspace_id <> '')
		)`,
		`CREATE INDEX IF NOT EXISTS capability_manifests_state_idx ON capability_manifests(state, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS capability_bindings_bot_idx ON capability_bindings(bot_id, enabled, updated_at DESC)`,
		`CREATE INDEX IF NOT EXISTS capability_bindings_workspace_idx ON capability_bindings(workspace_id, enabled, updated_at DESC)`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	return nil
}

func capabilityDigest(manifest map[string]any) (string, error) {
	if manifest == nil {
		manifest = map[string]any{}
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func normalizeCapabilityText(value, field string, max int, required bool) (string, error) {
	value = strings.TrimSpace(value)
	if (required && value == "") || len(value) > max || strings.ContainsRune(value, '\x00') {
		return "", coreInvalid(field + " is invalid")
	}
	return value, nil
}

func capabilityManifestDefaults(input capabilityManifestPayload, current *capabilityManifest) (capabilityManifest, error) {
	value := capabilityManifest{}
	if current != nil {
		value = *current
		if current.Manifest != nil {
			value.Manifest = map[string]any{}
			for key, item := range current.Manifest {
				value.Manifest[key] = item
			}
		}
	}
	if input.ID != nil {
		value.ID = *input.ID
	}
	if input.Version != nil {
		value.Version = *input.Version
	}
	if input.Kind != nil {
		value.Kind = *input.Kind
	}
	if input.Source != nil {
		value.Source = *input.Source
	}
	if input.State != nil {
		value.State = *input.State
	}
	if input.Manifest != nil {
		value.Manifest = *input.Manifest
	}
	var err error
	if value.ID, err = normalizeCapabilityText(value.ID, "id", 120, true); err != nil {
		return value, err
	}
	if value.Version, err = normalizeCapabilityText(value.Version, "version", 60, true); err != nil {
		return value, err
	}
	if value.Kind, err = normalizeCapabilityText(strings.ToLower(value.Kind), "kind", 20, true); err != nil {
		return value, err
	}
	switch value.Kind {
	case "tool", "plugin", "skill", "mcp":
	default:
		return value, coreInvalid("kind must be tool, plugin, skill, or mcp")
	}
	if value.Source == "" {
		value.Source = "external"
	}
	switch value.Source {
	case "builtin", "mcp", "external":
	default:
		return value, coreInvalid("source must be builtin, mcp, or external")
	}
	if value.State == "" {
		value.State = "staged"
	}
	switch value.State {
	case "draft", "staged", "active", "paused", "retired":
	default:
		return value, coreInvalid("state is invalid")
	}
	if value.Manifest == nil {
		value.Manifest = map[string]any{}
	}
	encoded, err := json.Marshal(value.Manifest)
	if err != nil {
		return value, coreInvalid("manifest must be valid JSON")
	}
	if len(encoded) > 65536 {
		return value, coreInvalid("manifest is too large")
	}
	digest, err := capabilityDigest(value.Manifest)
	if err != nil {
		return value, err
	}
	if input.Digest != nil && strings.TrimSpace(*input.Digest) != "" && strings.TrimSpace(*input.Digest) != digest {
		return value, coreInvalid("digest does not match manifest")
	}
	value.Digest = digest
	value.RuntimeReady, value.RuntimeReason = capabilityRuntimeState(value.Source, value.State)
	return value, nil
}

func capabilityRuntimeState(source, state string) (bool, string) {
	if state == "retired" || state == "paused" {
		return false, "能力尚未激活"
	}
	if source == "builtin" {
		return true, "Core 内置适配器"
	}
	if source == "mcp" {
		return false, "已登记 MCP 能力，等待绑定受信任运行适配器"
	}
	return false, "已登记能力包，尚未安装受信任运行适配器"
}

func scanCapabilityManifest(scanner interface{ Scan(...any) error }) (capabilityManifest, error) {
	var value capabilityManifest
	var manifestJSON string
	var ready int
	err := scanner.Scan(&value.ID, &value.Version, &value.Kind, &value.Digest, &value.Source, &value.State,
		&manifestJSON, &ready, &value.RuntimeReason, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return value, err
	}
	value.RuntimeReady = ready == 1
	if json.Unmarshal([]byte(manifestJSON), &value.Manifest) != nil || value.Manifest == nil {
		value.Manifest = map[string]any{}
	}
	return value, nil
}

func (s *coreConfigStore) capabilityManifest(id, version string) (capabilityManifest, bool, error) {
	query := `SELECT id, version, kind, digest, source, state, manifest_json, runtime_ready,
		runtime_reason, created_at, updated_at FROM capability_manifests WHERE id = ?`
	args := []any{id}
	if strings.TrimSpace(version) != "" {
		query += " AND version = ?"
		args = append(args, version)
	} else {
		query += " ORDER BY updated_at DESC LIMIT 1"
	}
	value, err := scanCapabilityManifest(s.db.QueryRow(query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) capabilityManifestList(r *http.Request) ([]capabilityManifest, error) {
	query := `SELECT id, version, kind, digest, source, state, manifest_json, runtime_ready,
		runtime_reason, created_at, updated_at FROM capability_manifests WHERE 1 = 1`
	args := []any{}
	if kind := strings.TrimSpace(r.URL.Query().Get("kind")); kind != "" {
		query += " AND kind = ?"
		args = append(args, kind)
	}
	if state := strings.TrimSpace(r.URL.Query().Get("state")); state != "" {
		query += " AND state = ?"
		args = append(args, state)
	}
	query += " ORDER BY updated_at DESC, id, version"
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []capabilityManifest{}
	for rows.Next() {
		value, scanErr := scanCapabilityManifest(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *coreConfigStore) createCapabilityManifest(input capabilityManifestPayload) (capabilityManifest, error) {
	value, err := capabilityManifestDefaults(input, nil)
	if err != nil {
		return value, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO capability_manifests
		(id, version, kind, digest, source, state, manifest_json, runtime_ready, runtime_reason, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, value.ID, value.Version, value.Kind, value.Digest,
		value.Source, value.State, mgmtJSON(value.Manifest), boolInt(value.RuntimeReady), value.RuntimeReason, now, now)
	if err != nil {
		return value, mgmtConstraintError(err, "capability id and version already exist")
	}
	if err = s.mgmtAudit("create", "capability_manifest", value.ID+"@"+value.Version, []string{"manifest", "digest", "state"}); err != nil {
		return value, err
	}
	value.CreatedAt, value.UpdatedAt = now, now
	return value, nil
}

func (s *coreConfigStore) updateCapabilityManifest(id, version string, input capabilityManifestPayload) (capabilityManifest, bool, error) {
	current, found, err := s.capabilityManifest(id, version)
	if err != nil || !found {
		return current, found, err
	}
	if current.State == "retired" {
		return current, true, coreInvalid("retired capability cannot be edited")
	}
	value, err := capabilityManifestDefaults(input, &current)
	if err != nil {
		return current, true, err
	}
	if value.ID != current.ID || value.Version != current.Version || value.Kind != current.Kind {
		return current, true, coreInvalid("id, version, and kind are immutable")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`UPDATE capability_manifests SET digest=?, source=?, state=?, manifest_json=?, runtime_ready=?, runtime_reason=?, updated_at=? WHERE id=? AND version=?`,
		value.Digest, value.Source, value.State, mgmtJSON(value.Manifest), boolInt(value.RuntimeReady), value.RuntimeReason, now, id, version)
	if err != nil {
		return current, true, err
	}
	if err = s.mgmtAudit("update", "capability_manifest", id+"@"+version, []string{"manifest", "digest", "state"}); err != nil {
		return current, true, err
	}
	value.CreatedAt, value.UpdatedAt = current.CreatedAt, now
	return value, true, nil
}

func (s *coreConfigStore) deleteCapabilityManifest(id, version string) error {
	value, found, err := s.capabilityManifest(id, version)
	if err != nil {
		return err
	}
	if !found {
		return mgmtNotFound("capability manifest")
	}
	if value.State != "retired" && value.State != "draft" {
		return coreInvalid("only draft or retired capabilities can be deleted")
	}
	if _, err = s.db.Exec("DELETE FROM capability_manifests WHERE id=? AND version=?", id, version); err != nil {
		return err
	}
	return s.mgmtAudit("delete", "capability_manifest", id+"@"+version, []string{"state"})
}

func capabilityBindingFromRow(scanner interface{ Scan(...any) error }) (capabilityBinding, error) {
	var value capabilityBinding
	var enabled int
	err := scanner.Scan(&value.ID, &value.CapabilityID, &value.Version, &value.BotID, &value.WorkspaceID,
		&enabled, &value.GrantedBy, &value.CreatedAt, &value.UpdatedAt)
	value.Enabled = enabled == 1
	return value, err
}

func (s *coreConfigStore) capabilityBindings(id, version string) ([]capabilityBinding, error) {
	rows, err := s.db.Query(`SELECT id, capability_id, version, bot_id, workspace_id, enabled, granted_by, created_at, updated_at
		FROM capability_bindings WHERE capability_id = ? AND version = ? ORDER BY id`, id, version)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []capabilityBinding{}
	for rows.Next() {
		value, scanErr := capabilityBindingFromRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (s *coreConfigStore) createCapabilityBinding(id, version string, input capabilityBindingPayload) (capabilityBinding, error) {
	manifest, found, err := s.capabilityManifest(id, version)
	if err != nil {
		return capabilityBinding{}, err
	}
	if !found {
		return capabilityBinding{}, mgmtNotFound("capability manifest")
	}
	if manifest.State == "retired" {
		return capabilityBinding{}, coreInvalid("retired capability cannot be bound")
	}
	botID, workspaceID, grantedBy := "", "", "admin"
	if input.BotID != nil {
		botID = strings.TrimSpace(*input.BotID)
	}
	if input.WorkspaceID != nil {
		workspaceID = strings.TrimSpace(*input.WorkspaceID)
	}
	if input.GrantedBy != nil && strings.TrimSpace(*input.GrantedBy) != "" {
		grantedBy = strings.TrimSpace(*input.GrantedBy)
	}
	if botID == "" && workspaceID == "" {
		return capabilityBinding{}, coreInvalid("botId or workspaceId is required")
	}
	if len(botID) > 120 || len(workspaceID) > 120 || len(grantedBy) > 160 {
		return capabilityBinding{}, coreInvalid("binding scope is invalid")
	}
	if botID != "" {
		bot, exists, lookupErr := s.privateBot(botID)
		if lookupErr != nil {
			return capabilityBinding{}, lookupErr
		}
		if !exists {
			return capabilityBinding{}, mgmtNotFound("private bot")
		}
		if workspaceID == "" {
			workspaceID = bot.WorkspaceID
		} else if workspaceID != bot.WorkspaceID {
			return capabilityBinding{}, coreInvalid("workspaceId does not match private Bot")
		}
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO capability_bindings (capability_id, version, bot_id, workspace_id, enabled, granted_by, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(capability_id, version, bot_id, workspace_id) DO UPDATE SET enabled=excluded.enabled, granted_by=excluded.granted_by, updated_at=excluded.updated_at`,
		id, manifest.Version, botID, workspaceID, boolInt(enabled), grantedBy, now, now)
	if err != nil {
		return capabilityBinding{}, err
	}
	var value capabilityBinding
	err = s.db.QueryRow(`SELECT id, capability_id, version, bot_id, workspace_id, enabled, granted_by, created_at, updated_at
		FROM capability_bindings WHERE capability_id=? AND version=? AND bot_id=? AND workspace_id=?`, id, manifest.Version, botID, workspaceID).Scan(
		&value.ID, &value.CapabilityID, &value.Version, &value.BotID, &value.WorkspaceID, new(int), &value.GrantedBy, &value.CreatedAt, &value.UpdatedAt)
	if err != nil {
		return capabilityBinding{}, err
	}
	var enabledInt int
	if err = s.db.QueryRow("SELECT enabled FROM capability_bindings WHERE id=?", value.ID).Scan(&enabledInt); err != nil {
		return capabilityBinding{}, err
	}
	value.Enabled = enabledInt == 1
	if err = s.mgmtAudit("bind", "capability_manifest", id+"@"+manifest.Version, []string{"botId", "workspaceId", "enabled"}); err != nil {
		return capabilityBinding{}, err
	}
	return value, nil
}

// capabilityAllowed is intentionally small so the long-running job executor
// can reuse one authorization check when it starts consuming Bot-scoped tools.
func (s *coreConfigStore) capabilityAllowed(id, version, botID, workspaceID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT count(*) FROM capability_bindings
		WHERE capability_id=? AND version=? AND enabled=1 AND (bot_id=? OR (bot_id='' AND workspace_id=?))`,
		id, version, botID, workspaceID).Scan(&count)
	return count > 0, err
}

func (s *coreConfigStore) handleCapabilityManifests(w http.ResponseWriter, r *http.Request, path string) error {
	// Keep the endpoint safe for lightweight fixtures and pre-M3 databases that
	// have not restarted through migrateCoreConfig yet.
	if err := ensureCapabilityManifestSchema(s.db); err != nil {
		return err
	}
	const prefix = "/api/v1/capabilities/manifests"
	if path == prefix {
		switch r.Method {
		case http.MethodGet:
			values, err := s.capabilityManifestList(r)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, map[string]any{"items": values, "total": len(values)})
			}
			return err
		case http.MethodPost:
			var input capabilityManifestPayload
			if _, err := decodeCoreObject(r, capabilityManifestCreateFields, "capability manifest", &input); err != nil {
				return err
			}
			value, err := s.createCapabilityManifest(input)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	trimmed := strings.TrimPrefix(path, prefix+"/")
	parts := strings.Split(trimmed, "/")
	if len(parts) == 0 || parts[0] == "" {
		return mgmtNotFound("capability manifest")
	}
	id, err := mgmtIdentifier(parts[0], "id")
	if err != nil {
		return err
	}
	version := strings.TrimSpace(r.URL.Query().Get("version"))
	if len(parts) == 2 && parts[1] == "bindings" {
		if r.Method == http.MethodGet {
			manifest, found, lookupErr := s.capabilityManifest(id, version)
			if lookupErr != nil {
				return lookupErr
			}
			if !found {
				return mgmtNotFound("capability manifest")
			}
			values, lookupErr := s.capabilityBindings(id, manifest.Version)
			if lookupErr == nil {
				mgmtWriteData(w, http.StatusOK, map[string]any{"items": values, "total": len(values)})
			}
			return lookupErr
		}
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		var input capabilityBindingPayload
		if _, err := decodeCoreObject(r, capabilityBindingFields, "capability binding", &input); err != nil {
			return err
		}
		value, err := s.createCapabilityBinding(id, version, input)
		if err == nil {
			mgmtWriteData(w, http.StatusCreated, value)
		}
		return err
	}
	if len(parts) == 3 && parts[1] == "bindings" {
		if r.Method != http.MethodDelete {
			return mgmtMethodNotAllowed()
		}
		bindingID, parseErr := strconv.ParseInt(parts[2], 10, 64)
		if parseErr != nil || bindingID <= 0 {
			return coreInvalid("binding id is invalid")
		}
		result, deleteErr := s.db.Exec("DELETE FROM capability_bindings WHERE id=? AND capability_id=?", bindingID, id)
		if deleteErr != nil {
			return deleteErr
		}
		if _, deleteErr = mgmtDeleteResult(result, "capability binding"); deleteErr != nil {
			return deleteErr
		}
		if err = s.mgmtAudit("unbind", "capability_manifest", id, []string{"bindingId"}); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": bindingID, "deleted": true})
		return nil
	}
	if len(parts) != 1 {
		return mgmtNotFound("capability manifest")
	}
	if r.Method == http.MethodGet {
		value, found, lookupErr := s.capabilityManifest(id, version)
		if lookupErr != nil {
			return lookupErr
		}
		if !found {
			return mgmtNotFound("capability manifest")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	}
	if r.Method == http.MethodDelete {
		if version == "" {
			return coreInvalid("version query is required for delete")
		}
		if err = s.deleteCapabilityManifest(id, version); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "version": version, "deleted": true})
		return nil
	}
	if r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	if version == "" {
		return coreInvalid("version query is required for update")
	}
	var input capabilityManifestPayload
	fields, decodeErr := decodeCoreObject(r, capabilityManifestUpdateFields, "capability manifest", &input)
	if decodeErr != nil {
		return decodeErr
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return err
	}
	value, found, updateErr := s.updateCapabilityManifest(id, version, input)
	if updateErr != nil {
		return updateErr
	}
	if !found {
		return mgmtNotFound("capability manifest")
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}
