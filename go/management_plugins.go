package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type mgmtPlugin struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Version       string              `json:"version"`
	Author        string              `json:"author"`
	Source        string              `json:"source"`
	Enabled       bool                `json:"enabled"`
	State         string              `json:"state"`
	IntegrationID string              `json:"integrationId"`
	Config        map[string]any      `json:"config"`
	Manifest      map[string]any      `json:"manifest"`
	CreatedAt     string              `json:"createdAt"`
	UpdatedAt     string              `json:"updatedAt"`
	Toggleable    bool                `json:"toggleable"`
	ToggleMode    string              `json:"toggleMode"`
	ConfigView    string              `json:"configView"`
	Adapter       *mgmtTrustedAdapter `json:"adapter,omitempty"`
}

type mgmtPluginPayload struct {
	ID            *string         `json:"id"`
	Name          *string         `json:"name"`
	Description   *string         `json:"description"`
	Version       *string         `json:"version"`
	Author        *string         `json:"author"`
	Enabled       *bool           `json:"enabled"`
	IntegrationID *string         `json:"integrationId"`
	Manifest      *map[string]any `json:"manifest"`
}

var mgmtPluginCreateFields = coreFieldSet("id", "name", "description", "version", "author", "enabled", "integrationId", "manifest")

var pluginExternalReservedManifestFields = coreFieldSet(
	"integrationId", "toggleMode", "configPath", "configView",
	"health", "healthMode", "healthTable", "healthTables", "requiredToolIds",
)

var pluginConfigViews = coreFieldSet(
	"operations", "runtime", "roles", "memories", "worldbook", "samples",
	"knowledge", "skills", "plugins", "tools", "integrations", "models",
	"routing", "devices", "security", "system",
)

func pluginCategory(value any) string {
	category := strings.TrimSpace(pluginStringValue(value))
	if category == "" {
		return "extension"
	}
	return category
}

func pluginManifestValues(input mgmtPluginPayload) (mgmtPlugin, error) {
	value := mgmtPlugin{Enabled: true, Version: "0.1.0", Author: "外部贡献者", Manifest: map[string]any{}}
	var err error
	if input.ID == nil || input.Name == nil {
		return value, coreInvalid("id and name are required")
	}
	if value.ID, err = mgmtIdentifier(*input.ID, "id"); err != nil {
		return value, err
	}
	if value.Name, err = normalizeCoreText(*input.Name, "name", 120, false); err != nil {
		return value, err
	}
	if input.Description != nil {
		if value.Description, err = normalizeCoreText(*input.Description, "description", 2000, false); err != nil {
			return value, err
		}
	}
	if input.Version != nil {
		if value.Version, err = normalizeCoreText(*input.Version, "version", 40, true); err != nil {
			return value, err
		}
	}
	if input.Author != nil {
		if value.Author, err = normalizeCoreText(*input.Author, "author", 120, false); err != nil {
			return value, err
		}
	}
	if input.Enabled != nil && !*input.Enabled {
		return value, coreInvalid("external plugins are registration-only and cannot be disabled")
	}
	if input.Manifest != nil {
		if *input.Manifest == nil {
			return value, coreInvalid("manifest must be an object")
		}
		value.Manifest = *input.Manifest
	}
	if input.IntegrationID != nil && strings.TrimSpace(*input.IntegrationID) != "" {
		return value, coreInvalid("external plugins cannot bind Core integrations")
	}
	if err = rejectExternalPluginRuntimeFields(value.Manifest); err != nil {
		return value, err
	}
	if _, ok := value.Manifest["category"]; !ok {
		value.Manifest["category"] = pluginCategory(nil)
	}
	value.Manifest["manifestSchemaVersion"] = 1
	value.Manifest["toggleMode"] = "readonly"
	value.Manifest["healthMode"] = "registration"
	if len(mgmtJSON(value.Manifest)) > 50000 {
		return value, coreInvalid("manifest is too large")
	}
	value.Source = "external"
	return value, nil
}

func rejectExternalPluginRuntimeFields(value any) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			for reserved := range pluginExternalReservedManifestFields {
				if strings.EqualFold(strings.TrimSpace(key), reserved) {
					return coreInvalid("external manifest field " + key + " is reserved for Core")
				}
			}
			if err := rejectExternalPluginRuntimeFields(child); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := rejectExternalPluginRuntimeFields(child); err != nil {
				return err
			}
		}
	}
	return nil
}

func sanitizeExternalPluginManifest(value any) map[string]any {
	manifest, ok := value.(map[string]any)
	if !ok || manifest == nil {
		manifest = map[string]any{}
	}
	var clean func(any) any
	clean = func(current any) any {
		switch typed := current.(type) {
		case map[string]any:
			result := map[string]any{}
			for key, child := range typed {
				reservedField := false
				for reserved := range pluginExternalReservedManifestFields {
					if strings.EqualFold(strings.TrimSpace(key), reserved) {
						reservedField = true
						break
					}
				}
				if !reservedField {
					result[key] = clean(child)
				}
			}
			return result
		case []any:
			result := make([]any, 0, len(typed))
			for _, child := range typed {
				result = append(result, clean(child))
			}
			return result
		default:
			return current
		}
	}
	result, _ := clean(manifest).(map[string]any)
	if _, ok := result["category"]; !ok {
		result["category"] = "extension"
	}
	result["manifestSchemaVersion"] = 1
	result["toggleMode"] = "readonly"
	result["healthMode"] = "registration"
	return result
}

func pluginToggleMode(plugin mgmtPlugin) string {
	if plugin.Source != "builtin" {
		return "readonly"
	}
	mode := strings.TrimSpace(pluginStringValue(plugin.Manifest["toggleMode"]))
	if mode != "policy_field" || plugin.IntegrationID == "" {
		return "readonly"
	}
	fields, found := mgmtIntegrationFields[plugin.IntegrationID]
	if !found {
		return "readonly"
	}
	if _, found = fields["enabled"]; !found {
		return "readonly"
	}
	return "policy_field"
}

func pluginConfigView(manifest map[string]any) string {
	view := strings.TrimSpace(pluginStringValue(manifest["configView"]))
	if _, ok := pluginConfigViews[view]; ok {
		return view
	}
	return ""
}

func pluginState(enabled bool, config map[string]any, integrationID, source, toggleMode string) string {
	if source == "external" && integrationID == "" {
		return "registered"
	}
	if !enabled {
		return "disabled"
	}
	if integrationID == "" {
		if source == "external" {
			return "registered"
		}
		return "ready"
	}
	if toggleMode == "policy_field" && !integrationEnabled(config) {
		return "disabled"
	}
	switch integrationID {
	case "ops_policy":
		if strings.TrimSpace(pluginStringValue(config["statusUrl"])) == "" {
			return "needs_configuration"
		}
	case "affiliate_policy":
		if strings.TrimSpace(pluginStringValue(config["summaryUrl"])) == "" {
			return "needs_configuration"
		}
	case "qq_official", "provider_policy", "grok_policy", "image_policy":
		if configured, ok := config["credentialConfigured"].(bool); ok && !configured {
			return "needs_configuration"
		}
	}
	return "ready"
}

func integrationEnabled(config map[string]any) bool {
	value, present := config["enabled"]
	return !present || enabledValue(value)
}

func enabledValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed == 1
	case int:
		return typed == 1
	default:
		return strings.EqualFold(strings.TrimSpace(pluginStringValue(value)), "true")
	}
}

func pluginStringValue(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimSpace(jsonString(value)))
}

func jsonString(value any) string {
	if typed, ok := value.(string); ok {
		return typed
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func (s *coreConfigStore) mgmtPlugins() ([]mgmtPlugin, error) {
	rows, err := s.db.Query(`SELECT id, name, description, version, author, source, enabled, manifest_json, created_at, updated_at
		FROM agent_plugins ORDER BY name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtPlugin{}
	for rows.Next() {
		var value mgmtPlugin
		var enabled int
		var manifestJSON string
		if err := rows.Scan(&value.ID, &value.Name, &value.Description, &value.Version, &value.Author,
			&value.Source, &enabled, &manifestJSON, &value.CreatedAt, &value.UpdatedAt); err != nil {
			return nil, err
		}
		value.Enabled = enabled == 1
		value.Manifest = mgmtDecodeObjectJSON(manifestJSON)
		value.IntegrationID = pluginStringValue(value.Manifest["integrationId"])
		value.ToggleMode = pluginToggleMode(value)
		value.Toggleable = value.ToggleMode == "policy_field"
		value.ConfigView = pluginConfigView(value.Manifest)
		value.Config = map[string]any{}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	adapters, err := s.mgmtTrustedAdapters()
	if err != nil {
		return nil, err
	}
	adapterByPlugin := map[string]mgmtTrustedAdapter{}
	for _, adapter := range adapters {
		adapterByPlugin[adapter.PluginID] = adapter
	}
	for index := range values {
		value := &values[index]
		if value.Source == "external" {
			if adapter, found := adapterByPlugin[value.ID]; found {
				adapterCopy := adapter
				value.Adapter = &adapterCopy
				if !adapter.Enabled {
					value.Enabled = false
				} else {
					value.IntegrationID = adapter.IntegrationID
					if trustedAdapterHasPermission(adapter, "config.read") {
						value.ConfigView = "integrations"
					}
					if trustedAdapterHasPermission(adapter, "runtime.toggle") {
						if _, found := mgmtIntegrationFields[adapter.IntegrationID]["enabled"]; found {
							value.ToggleMode = "adapter_policy_field"
							value.Toggleable = true
						}
					}
				}
			}
		}
		if value.IntegrationID != "" {
			integration, found, err := s.mgmtStoredIntegration(value.IntegrationID)
			if err != nil {
				return nil, err
			}
			if found {
				value.Config = integration.Config
				value.Enabled = integrationEnabled(integration.Config)
			}
		}
		value.State = pluginState(value.Enabled, value.Config, value.IntegrationID, value.Source, value.ToggleMode)
	}
	return values, nil
}

func pluginHealthTable(name string) (string, bool) {
	switch name {
	case "tools":
		return "tools", true
	case "skills":
		return "skills", true
	case "mcp_servers":
		return "mcp_servers", true
	case "platform_integrations":
		return "platform_integrations", true
	case "agent_instances":
		return "agent_instances", true
	case "personas":
		return "personas", true
	case "knowledge_documents":
		return "knowledge_documents", true
	default:
		return "", false
	}
}

func (s *coreConfigStore) catalogPluginHealth(plugin mgmtPlugin, result map[string]any) error {
	if tables := collectionStringValues(plugin.Manifest["healthTables"]); len(tables) > 0 {
		counts := map[string]int64{}
		var total int64
		for _, name := range tables {
			table, valid := pluginHealthTable(name)
			if !valid {
				continue
			}
			var count int64
			if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil {
				return err
			}
			counts[table] = count
			total += count
		}
		result["state"] = "ready"
		result["resourceCount"] = total
		result["resourceCounts"] = counts
		return nil
	}
	healthMode := strings.TrimSpace(pluginStringValue(plugin.Manifest["healthMode"]))
	if healthMode == "" {
		healthMode = strings.TrimSpace(pluginStringValue(plugin.Manifest["health"]))
	}
	table, ok := pluginHealthTable(strings.TrimSpace(pluginStringValue(plugin.Manifest["healthTable"])))
	if !ok {
		switch healthMode {
		case "tools":
			table = "tools"
		case "skills":
			table = "skills"
		case "mcp":
			table = "mcp_servers"
		default:
			result["state"] = "ready"
			return nil
		}
	}
	var total int64
	if err := s.db.QueryRow("SELECT count(*) FROM " + table).Scan(&total); err != nil {
		return err
	}
	result["state"] = "ready"
	result["resourceCount"] = total
	result["resourceTable"] = table
	return nil
}

func collectionStringValues(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return nil
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		if name := strings.TrimSpace(pluginStringValue(item)); name != "" {
			values = append(values, name)
		}
	}
	return values
}

func (s *coreConfigStore) mgmtPlugin(id string) (mgmtPlugin, bool, error) {
	values, err := s.mgmtPlugins()
	if err != nil {
		return mgmtPlugin{}, false, err
	}
	for _, value := range values {
		if value.ID == id {
			return value, true, nil
		}
	}
	return mgmtPlugin{}, false, nil
}

func (s *coreConfigStore) updatePluginEnabled(id string, enabled bool) (mgmtPlugin, bool, error) {
	plugin, found, err := s.mgmtPlugin(id)
	if err != nil || !found {
		return plugin, found, err
	}
	if !plugin.Toggleable {
		return plugin, true, coreInvalid("plugin does not expose a managed runtime switch")
	}
	tx, err := s.db.BeginTx(context.Background(), nil)
	if err != nil {
		return plugin, true, err
	}
	defer tx.Rollback()
	config := map[string]any{}
	var raw string
	if err = tx.QueryRow("SELECT config_json FROM integration_settings WHERE id = ?", plugin.IntegrationID).Scan(&raw); err != nil {
		return plugin, true, err
	}
	if json.Unmarshal([]byte(raw), &config) != nil || config == nil {
		config = map[string]any{}
	}
	config["enabled"] = enabled
	if _, err = tx.Exec("UPDATE integration_settings SET config_json = ?, updated_at = ? WHERE id = ?", mgmtJSON(config), mgmtNow(), plugin.IntegrationID); err != nil {
		return plugin, true, err
	}
	if _, err = tx.Exec("UPDATE agent_plugins SET enabled = ?, updated_at = ? WHERE id = ?", boolInt(enabled), mgmtNow(), id); err != nil {
		return plugin, true, err
	}
	if err = tx.Commit(); err != nil {
		return plugin, true, err
	}
	if err = s.mgmtAudit("update", "plugin", id, []string{"enabled"}); err != nil {
		return plugin, true, err
	}
	return s.mgmtPlugin(id)
}

func (s *coreConfigStore) createPlugin(input mgmtPluginPayload) (mgmtPlugin, error) {
	value, err := pluginManifestValues(input)
	if err != nil {
		return value, err
	}
	manifest := value.Manifest
	now := mgmtNow()
	if _, err = s.db.Exec(`INSERT INTO agent_plugins
		(id, name, description, version, author, source, enabled, manifest_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'external', ?, ?, ?, ?)`, value.ID, value.Name, value.Description,
		value.Version, value.Author, boolInt(value.Enabled), mgmtJSON(manifest), now, now); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return value, coreInvalid("plugin id or name already exists")
		}
		return value, err
	}
	if err = s.mgmtAudit("create", "plugin", value.ID, []string{"manifest", "source"}); err != nil {
		return value, err
	}
	created, found, err := s.mgmtPlugin(value.ID)
	if err != nil {
		return value, err
	}
	if !found {
		return value, mgmtNotFound("plugin")
	}
	return created, nil
}

func (s *coreConfigStore) pluginHealth(a *AgentRuntime, id string) (map[string]any, error) {
	plugin, found, err := s.mgmtPlugin(id)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, mgmtNotFound("plugin")
	}
	result := map[string]any{
		"pluginId": plugin.ID, "state": plugin.State, "checkedAt": mgmtNow(),
		"healthMode": pluginStringValue(plugin.Manifest["healthMode"]),
	}
	if plugin.Adapter != nil {
		result["adapterId"] = plugin.Adapter.ID
		result["adapterPermissions"] = plugin.Adapter.Permissions
		result["healthMode"] = "trusted_adapter"
	}
	if plugin.State == "disabled" || plugin.State == "needs_configuration" || plugin.State == "registered" {
		return result, nil
	}
	dependencyStates := map[string]string{}
	for _, dependencyID := range collectionStringValues(plugin.Manifest["dependencies"]) {
		dependency, dependencyFound, dependencyErr := s.mgmtPlugin(dependencyID)
		if dependencyErr != nil {
			return nil, dependencyErr
		}
		if !dependencyFound {
			dependencyStates[dependencyID] = "missing"
			continue
		}
		dependencyStates[dependencyID] = dependency.State
	}
	for _, dependencyState := range dependencyStates {
		if dependencyState != "ready" && dependencyState != "healthy" {
			result["state"] = "degraded"
			result["message"] = "依赖能力未就绪"
			result["dependencyStates"] = dependencyStates
			return result, nil
		}
	}
	if len(dependencyStates) > 0 {
		result["dependencyStates"] = dependencyStates
	}
	switch plugin.ID {
	case "sub2api-channel-monitor":
		if a == nil {
			result["state"] = "unavailable"
			return result, nil
		}
		policy := opsPolicy{}
		if err := a.integrationConfig(context.Background(), "ops_policy", &policy); err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		if strings.TrimSpace(policy.CardPageURL) != "" {
			image, captureErr := a.captureOPSCardPNG(ctx, policy)
			if captureErr != nil {
				result["state"] = "unavailable"
				result["message"] = captureErr.Error()
				return result, nil
			}
			result["state"] = "healthy"
			result["source"] = "sub2api_card_page_90m"
			result["imageBytes"] = len(image)
			return result, nil
		}
		groups, fetchErr := a.fetchOPSGroups(ctx, policy)
		if fetchErr != nil {
			result["state"] = "unavailable"
			result["message"] = fetchErr.Error()
			return result, nil
		}
		counts := map[string]int{}
		for _, group := range groups {
			counts[normalizeOPSStatus(group.CurrentStatus)]++
		}
		result["state"] = "healthy"
		result["groupCount"] = len(groups)
		result["statusCounts"] = counts
		result["source"] = "sub2api_passive_monitor"
	case "affiliate-invite":
		var count int64
		if err := s.db.QueryRow("SELECT count(*) FROM agent_affiliate_bindings").Scan(&count); err != nil && !errors.Is(err, sql.ErrNoRows) {
			result["state"] = "ready"
			result["message"] = "运行时首次启用后创建绑定表"
			return result, nil
		}
		result["state"] = "ready"
		result["bindingCount"] = count
	default:
		if plugin.IntegrationID != "" {
			integration, found, lookupErr := s.mgmtStoredIntegration(plugin.IntegrationID)
			if lookupErr != nil {
				return nil, lookupErr
			}
			if !found {
				result["state"] = "unavailable"
				result["message"] = "接入策略不存在"
				return result, nil
			}
			if !integrationEnabled(integration.Config) {
				result["state"] = "disabled"
				return result, nil
			}
			result["state"] = "ready"
			result["integrationId"] = plugin.IntegrationID
			return result, nil
		}
		if err := s.catalogPluginHealth(plugin, result); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (s *coreConfigStore) handleManagementPlugins(a *AgentRuntime, w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/plugins" {
		if r.Method == http.MethodPost {
			var input mgmtPluginPayload
			if _, err := decodeCoreObject(r, mgmtPluginCreateFields, "plugin", &input); err != nil {
				return err
			}
			value, err := s.createPlugin(input)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		}
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		values, err := s.mgmtPlugins()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, values)
		}
		return err
	}
	trimmed := strings.TrimPrefix(path, "/api/v1/plugins/")
	if strings.HasSuffix(trimmed, "/health") {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		id := strings.TrimSuffix(trimmed, "/health")
		if id == "" || strings.Contains(id, "/") {
			return coreInvalid("plugin id is invalid")
		}
		value, err := s.pluginHealth(a, id)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	}
	id := trimmed
	if id == "" || strings.Contains(id, "/") {
		return coreInvalid("plugin id is invalid")
	}
	if r.Method == http.MethodDelete {
		plugin, found, err := s.mgmtPlugin(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("plugin")
		}
		if plugin.Source != "external" {
			return coreInvalid("builtin plugins cannot be deleted")
		}
		if _, err = s.db.Exec("DELETE FROM agent_plugins WHERE id = ?", id); err != nil {
			return err
		}
		if err = s.mgmtAudit("delete", "plugin", id, []string{"source"}); err != nil {
			return err
		}
		w.WriteHeader(http.StatusNoContent)
		return nil
	}
	if r.Method != http.MethodPut {
		return mgmtMethodNotAllowed()
	}
	var input mgmtPluginPayload
	fields, err := decodeCoreObject(r, coreFieldSet("enabled"), "plugin", &input)
	if err != nil {
		return err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return err
	}
	if input.Enabled == nil {
		return coreInvalid("enabled is required")
	}
	value, found, err := s.updatePluginEnabled(id, *input.Enabled)
	if err != nil {
		return err
	}
	if !found {
		return mgmtNotFound("plugin")
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}
