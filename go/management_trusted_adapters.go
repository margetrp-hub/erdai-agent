package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
)

type mgmtTrustedAdapter struct {
	ID              string   `json:"id"`
	Name            string   `json:"name"`
	PluginID        string   `json:"pluginId"`
	PluginName      string   `json:"pluginName"`
	IntegrationID   string   `json:"integrationId"`
	Version         string   `json:"version"`
	Permissions     []string `json:"permissions"`
	Enabled         bool     `json:"enabled"`
	State           string   `json:"state"`
	Message         string   `json:"message"`
	LastHealthState string   `json:"lastHealthState,omitempty"`
	LastCheckedAt   string   `json:"lastCheckedAt,omitempty"`
	CreatedAt       string   `json:"createdAt"`
	UpdatedAt       string   `json:"updatedAt"`
}

type mgmtTrustedAdapterPayload struct {
	ID            *string   `json:"id"`
	Name          *string   `json:"name"`
	PluginID      *string   `json:"pluginId"`
	IntegrationID *string   `json:"integrationId"`
	Version       *string   `json:"version"`
	Permissions   *[]string `json:"permissions"`
	Enabled       *bool     `json:"enabled"`
}

type mgmtTrustedAdapterHealth struct {
	State     string `json:"state"`
	Message   string `json:"message"`
	CheckedAt string `json:"checkedAt"`
}

var trustedAdapterCreateFields = coreFieldSet("id", "name", "pluginId", "integrationId", "version", "permissions", "enabled")
var trustedAdapterUpdateFields = coreFieldSet("name", "version", "permissions", "enabled")
var trustedAdapterPermissions = coreFieldSet("config.read", "health.read", "runtime.toggle")

func trustedAdapterHasPermission(adapter mgmtTrustedAdapter, permission string) bool {
	for _, current := range adapter.Permissions {
		if current == permission {
			return true
		}
	}
	return false
}

func normalizeTrustedAdapterPermissions(values []string, integrationID string) ([]string, error) {
	if len(values) == 0 || len(values) > len(trustedAdapterPermissions) {
		return nil, coreInvalid("permissions must contain 1 to 3 trusted permissions")
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		permission := strings.TrimSpace(value)
		if _, ok := trustedAdapterPermissions[permission]; !ok {
			return nil, coreInvalid("unsupported trusted adapter permission: " + permission)
		}
		if _, duplicate := seen[permission]; duplicate {
			continue
		}
		seen[permission] = struct{}{}
		result = append(result, permission)
	}
	if _, ok := seen["health.read"]; !ok {
		return nil, coreInvalid("health.read permission is required")
	}
	if _, ok := seen["runtime.toggle"]; ok {
		fields := mgmtIntegrationFields[integrationID]
		if _, toggleable := fields["enabled"]; !toggleable {
			return nil, coreInvalid("integration does not expose a managed runtime switch")
		}
	}
	sort.Strings(result)
	return result, nil
}

func (s *coreConfigStore) trustedAdapterRuntimeState(adapter mgmtTrustedAdapter) (string, string, error) {
	if !adapter.Enabled {
		return "disabled", "受信任适配器已停用", nil
	}
	integration, found, err := s.mgmtIntegration(adapter.IntegrationID)
	if err != nil {
		return "unavailable", "Core 集成读取失败", err
	}
	if !found {
		return "unavailable", "Core 集成不存在", nil
	}
	state := pluginState(true, integration.Config, adapter.IntegrationID, "builtin", "readonly")
	switch state {
	case "disabled":
		return state, "Core 集成已停用", nil
	case "needs_configuration":
		return state, "Core 集成尚未完成配置", nil
	default:
		return "ready", "Core 集成已就绪", nil
	}
}

func (s *coreConfigStore) mgmtTrustedAdapters() ([]mgmtTrustedAdapter, error) {
	rows, err := s.db.Query(`
		SELECT a.id, a.name, a.plugin_id, p.name, a.integration_id, a.version,
			a.permissions_json, a.enabled, a.created_at, a.updated_at,
			(SELECT h.state FROM trusted_adapter_health h WHERE h.adapter_id = a.id ORDER BY h.checked_at DESC, h.id DESC LIMIT 1),
			(SELECT h.checked_at FROM trusted_adapter_health h WHERE h.adapter_id = a.id ORDER BY h.checked_at DESC, h.id DESC LIMIT 1)
		FROM trusted_adapters a
		JOIN agent_plugins p ON p.id = a.plugin_id
		ORDER BY a.name, a.id
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtTrustedAdapter{}
	for rows.Next() {
		var value mgmtTrustedAdapter
		var permissionsJSON string
		var enabled int
		var lastState, lastChecked sql.NullString
		if err = rows.Scan(&value.ID, &value.Name, &value.PluginID, &value.PluginName, &value.IntegrationID,
			&value.Version, &permissionsJSON, &enabled, &value.CreatedAt, &value.UpdatedAt, &lastState, &lastChecked); err != nil {
			return nil, err
		}
		value.Enabled = enabled == 1
		if json.Unmarshal([]byte(permissionsJSON), &value.Permissions) != nil || value.Permissions == nil {
			value.Permissions = []string{}
		}
		value.LastHealthState = lastState.String
		value.LastCheckedAt = lastChecked.String
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	for index := range values {
		values[index].State, values[index].Message, err = s.trustedAdapterRuntimeState(values[index])
		if err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *coreConfigStore) mgmtTrustedAdapter(id string) (mgmtTrustedAdapter, bool, error) {
	values, err := s.mgmtTrustedAdapters()
	if err != nil {
		return mgmtTrustedAdapter{}, false, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, true, nil
		}
	}
	return mgmtTrustedAdapter{}, false, nil
}

func (s *coreConfigStore) validateTrustedAdapterTarget(pluginID, integrationID string) error {
	var source string
	if err := s.db.QueryRow("SELECT source FROM agent_plugins WHERE id = ?", pluginID).Scan(&source); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("plugin")
	} else if err != nil {
		return err
	}
	if source != "external" {
		return coreInvalid("trusted adapters can only bind external plugins")
	}
	if _, supported := mgmtIntegrationFields[integrationID]; !supported {
		return coreInvalid("integration is not managed by Core")
	}
	if _, found, err := s.mgmtStoredIntegration(integrationID); err != nil {
		return err
	} else if !found {
		return mgmtNotFound("integration")
	}
	return nil
}

func (s *coreConfigStore) createTrustedAdapter(input mgmtTrustedAdapterPayload) (mgmtTrustedAdapter, error) {
	if input.ID == nil || input.Name == nil || input.PluginID == nil || input.IntegrationID == nil || input.Permissions == nil {
		return mgmtTrustedAdapter{}, coreInvalid("id, name, pluginId, integrationId and permissions are required")
	}
	id, err := mgmtIdentifier(*input.ID, "id")
	if err != nil {
		return mgmtTrustedAdapter{}, err
	}
	name, err := normalizeCoreText(*input.Name, "name", 120, false)
	if err != nil {
		return mgmtTrustedAdapter{}, err
	}
	pluginID, err := mgmtIdentifier(*input.PluginID, "pluginId")
	if err != nil {
		return mgmtTrustedAdapter{}, err
	}
	integrationID, err := mgmtIdentifier(*input.IntegrationID, "integrationId")
	if err != nil {
		return mgmtTrustedAdapter{}, err
	}
	if err = s.validateTrustedAdapterTarget(pluginID, integrationID); err != nil {
		return mgmtTrustedAdapter{}, err
	}
	permissions, err := normalizeTrustedAdapterPermissions(*input.Permissions, integrationID)
	if err != nil {
		return mgmtTrustedAdapter{}, err
	}
	version := "1.0.0"
	if input.Version != nil {
		version, err = normalizeCoreText(*input.Version, "version", 40, true)
		if err != nil {
			return mgmtTrustedAdapter{}, err
		}
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := mgmtNow()
	if _, err = s.db.Exec(`INSERT INTO trusted_adapters
		(id, name, plugin_id, integration_id, version, permissions_json, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, name, pluginID, integrationID, version, mgmtJSON(permissions), boolInt(enabled), now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return mgmtTrustedAdapter{}, coreInvalid("adapter id, name or plugin binding already exists")
		}
		return mgmtTrustedAdapter{}, err
	}
	if err = s.mgmtAudit("create", "trusted_adapter", id, []string{"pluginId", "integrationId", "permissions"}); err != nil {
		return mgmtTrustedAdapter{}, err
	}
	value, _, err := s.mgmtTrustedAdapter(id)
	return value, err
}

func (s *coreConfigStore) updateTrustedAdapter(r *http.Request, id string) (mgmtTrustedAdapter, bool, error) {
	current, found, err := s.mgmtTrustedAdapter(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtTrustedAdapterPayload
	fields, err := decodeCoreObject(r, trustedAdapterUpdateFields, "trusted adapter", &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	if len(fields) == 0 {
		return current, true, coreInvalid("at least one adapter field is required")
	}
	name, version, permissions, enabled := current.Name, current.Version, current.Permissions, current.Enabled
	if input.Name != nil {
		name, err = normalizeCoreText(*input.Name, "name", 120, false)
		if err != nil {
			return current, true, err
		}
	}
	if input.Version != nil {
		version, err = normalizeCoreText(*input.Version, "version", 40, true)
		if err != nil {
			return current, true, err
		}
	}
	if input.Permissions != nil {
		permissions, err = normalizeTrustedAdapterPermissions(*input.Permissions, current.IntegrationID)
		if err != nil {
			return current, true, err
		}
	}
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	if _, err = s.db.Exec(`UPDATE trusted_adapters SET name = ?, version = ?, permissions_json = ?, enabled = ?, updated_at = ? WHERE id = ?`,
		name, version, mgmtJSON(permissions), boolInt(enabled), mgmtNow(), id); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return current, true, coreInvalid("adapter name already exists")
		}
		return current, true, err
	}
	if err = s.mgmtAudit("update", "trusted_adapter", id, mgmtFieldNames(fields)); err != nil {
		return current, true, err
	}
	value, _, err := s.mgmtTrustedAdapter(id)
	return value, true, err
}

func (s *coreConfigStore) checkTrustedAdapter(id string) (map[string]any, error) {
	adapter, found, err := s.mgmtTrustedAdapter(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, mgmtNotFound("trusted adapter")
	}
	checkedAt := mgmtNow()
	if _, err = s.db.Exec(`INSERT INTO trusted_adapter_health (adapter_id, state, message, checked_at) VALUES (?, ?, ?, ?)`,
		id, adapter.State, adapter.Message, checkedAt); err != nil {
		return nil, err
	}
	_, _ = s.db.Exec(`DELETE FROM trusted_adapter_health WHERE adapter_id = ? AND id NOT IN (
		SELECT id FROM trusted_adapter_health WHERE adapter_id = ? ORDER BY checked_at DESC, id DESC LIMIT 50
	)`, id, id)
	rows, err := s.db.Query(`SELECT state, message, checked_at FROM trusted_adapter_health
		WHERE adapter_id = ? ORDER BY checked_at DESC, id DESC LIMIT 20`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	history := []mgmtTrustedAdapterHealth{}
	for rows.Next() {
		var item mgmtTrustedAdapterHealth
		if err = rows.Scan(&item.State, &item.Message, &item.CheckedAt); err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{
		"adapterId": adapter.ID, "pluginId": adapter.PluginID, "integrationId": adapter.IntegrationID,
		"state": adapter.State, "message": adapter.Message, "checkedAt": checkedAt, "history": history,
	}, nil
}

func (s *coreConfigStore) handleManagementTrustedAdapters(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/trusted-adapters" {
		switch r.Method {
		case http.MethodGet:
			values, err := s.mgmtTrustedAdapters()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		case http.MethodPost:
			var input mgmtTrustedAdapterPayload
			fields, err := decodeCoreObject(r, trustedAdapterCreateFields, "trusted adapter", &input)
			if err != nil {
				return err
			}
			if err = mgmtRejectNullFields(fields); err != nil {
				return err
			}
			value, err := s.createTrustedAdapter(input)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	trimmed := strings.TrimPrefix(path, "/api/v1/trusted-adapters/")
	if strings.HasSuffix(trimmed, "/health") {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		id := strings.TrimSuffix(trimmed, "/health")
		if id == "" || strings.Contains(id, "/") {
			return coreInvalid("trusted adapter id is invalid")
		}
		value, err := s.checkTrustedAdapter(id)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	}
	id := trimmed
	if id == "" || strings.Contains(id, "/") {
		return coreInvalid("trusted adapter id is invalid")
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtTrustedAdapter(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("trusted adapter")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.updateTrustedAdapter(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("trusted adapter")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		if _, found, err := s.mgmtTrustedAdapter(id); err != nil {
			return err
		} else if !found {
			return mgmtNotFound("trusted adapter")
		}
		if _, err := s.db.Exec("DELETE FROM trusted_adapters WHERE id = ?", id); err != nil {
			return err
		}
		if err := s.mgmtAudit("delete", "trusted_adapter", id, []string{"pluginId", "integrationId"}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}
