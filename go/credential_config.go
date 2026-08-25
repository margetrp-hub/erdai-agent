package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The admin UI may manage provider/platform credentials, but never the keys
// that protect the database or the administrator session itself.
var managedCredentialDefaults = map[string]string{
	"ERDAI_RUNTIME_TOKEN":      "运行时服务密钥",
	"ERDAI_MODEL_API_KEY":      "主模型供应商密钥",
	"ERDAI_GROK_API_KEY":       "Grok 搜索与多媒体密钥",
	"ERDAI_IMAGE_API_KEY":      "图片生成密钥",
	"ERDAI_OPS_TOKEN":          "Sub2API 监控密钥",
	"ERDAI_QQ_SECRET":          "QQ Secret",
	"ERDAI_LOCAL_SEMANTIC_KEY": "Embedding 服务密钥",
}

var requiredManagedCredentials = map[string]struct{}{
	"ERDAI_RUNTIME_TOKEN":      {},
	"ERDAI_MODEL_API_KEY":      {},
	"ERDAI_LOCAL_SEMANTIC_KEY": {},
}

var managedCredentialPrefixes = []string{
	"ERDAI_", "ASTRBOT_", "TELEGRAM_", "DISCORD_", "QQ_", "LARK_", "DINGTALK_", "GROK_", "OPENAI_",
}

var blockedManagedCredentials = map[string]struct{}{
	"ERDAI_ADMIN_TOKEN":             {},
	"ERDAI_RUN_ENCRYPTION_KEY":      {},
	"ERDAI_RUNTIME_IDENTITY_SECRET": {},
	"ERDAI_ADMIN_PASSWORD":          {},
	"ERDAI_ADMIN_PASSWORD_SHA256":   {},
	"ERDAI_CORE_LISTEN":             {},
	"ERDAI_ADMIN_LISTEN":            {},
	"ERDAI_CONFIG_DATABASE":         {},
	"ERDAI_RUNTIME_DATABASE":        {},
	"ERDAI_LEGACY_RUNTIME_DATABASE": {},
	"ERDAI_RUNTIME_ENV_PATH":        {},
	"ERDAI_MEDIA_DIR":               {},
	"ERDAI_SEARCH_BASE_URL":         {},
	"ERDAI_MCP_STDIO_ALLOWLIST":     {},
}

type managedCredential struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Configured bool   `json:"configured"`
	Persisted  bool   `json:"persisted"`
	Required   bool   `json:"required"`
	Source     string `json:"source"`
}

type managedCredentialPayload struct {
	Value string `json:"value"`
}

func managedCredentialPath() string {
	if value := strings.TrimSpace(os.Getenv("ERDAI_MANAGED_CREDENTIALS_FILE")); value != "" {
		return filepath.Clean(value)
	}
	return "/app/data/managed-credentials.env"
}

func managedCredentialNameAllowed(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 96 {
		return false
	}
	if _, blocked := blockedManagedCredentials[name]; blocked {
		return false
	}
	for _, value := range name {
		if (value < 'A' || value > 'Z') && (value < '0' || value > '9') && value != '_' {
			return false
		}
	}
	for _, prefix := range managedCredentialPrefixes {
		if strings.HasPrefix(name, prefix) {
			if prefix != "ERDAI_" {
				return true
			}
			suffix := strings.TrimPrefix(name, prefix)
			return strings.HasSuffix(suffix, "_KEY") || strings.HasSuffix(suffix, "_TOKEN") || strings.HasSuffix(suffix, "_SECRET") || strings.HasSuffix(suffix, "_PASSWORD")
		}
	}
	return false
}

func managedCredentialLabel(name string) string {
	if label := managedCredentialDefaults[name]; label != "" {
		return label
	}
	return "平台或供应商凭据"
}

func managedCredentialRequired(name string) bool {
	_, required := requiredManagedCredentials[name]
	return required
}

func managedEnvLines(path string) ([]string, bool, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), true, nil
}

func managedEnvKey(line string) string {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	index := strings.IndexByte(line, '=')
	if index <= 0 {
		return ""
	}
	return strings.TrimSpace(line[:index])
}

func formatManagedEnvValue(value string) string {
	if value == "" {
		return ""
	}
	safe := true
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && !strings.ContainsRune("._/:@+,-", char) {
			safe = false
			break
		}
	}
	if safe {
		return value
	}
	// Compose treats single-quoted env-file values literally; only the quote
	// itself needs escaping. This also prevents `$` from being interpolated.
	return "'" + strings.ReplaceAll(value, "'", "\\'") + "'"
}

func updateManagedEnvFile(path, name, value string) error {
	if !managedCredentialNameAllowed(name) {
		return errors.New("凭据名称不在允许的管理范围内")
	}
	lines, exists, err := managedEnvLines(path)
	if err != nil {
		return fmt.Errorf("读取托管凭据文件: %w", err)
	}
	var original []byte
	if exists {
		original, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取托管凭据文件: %w", err)
		}
	} else {
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return fmt.Errorf("创建托管凭据目录: %w", err)
		}
	}
	backupPath := path + ".bak"
	if err := os.WriteFile(backupPath, original, 0600); err != nil {
		return fmt.Errorf("写入托管凭据回滚副本: %w", err)
	}
	updated := false
	for index, line := range lines {
		if managedEnvKey(line) != name {
			continue
		}
		updated = true
		if value == "" {
			lines[index] = ""
		} else {
			lines[index] = name + "=" + formatManagedEnvValue(value)
		}
		break
	}
	if !updated && value != "" {
		if len(lines) > 0 && lines[len(lines)-1] != "" {
			lines = append(lines, "")
		}
		lines = append(lines, name+"="+formatManagedEnvValue(value))
	}
	newline := "\n"
	if strings.Contains(string(original), "\r\n") {
		newline = "\r\n"
	}
	content := strings.Join(lines, newline)
	temporary, err := os.CreateTemp(filepath.Dir(path), ".erdai-runtime-env-*")
	if err != nil {
		return fmt.Errorf("创建托管凭据临时文件: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.WriteString(content)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写入托管凭据文件: %w", err)
	}
	if err = os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("替换托管凭据文件: %w", err)
	}
	return nil
}

func (a *AgentRuntime) managedCredentialNames() []string {
	values := map[string]struct{}{}
	for name := range managedCredentialDefaults {
		values[name] = struct{}{}
	}
	for _, value := range os.Environ() {
		name := strings.SplitN(value, "=", 2)[0]
		if managedCredentialNameAllowed(name) {
			values[name] = struct{}{}
		}
	}
	if a != nil && a.configStore != nil {
		rows, err := a.configStore.db.Query("SELECT credential_ref FROM provider_connections WHERE trim(credential_ref) <> ''")
		if err == nil {
			defer rows.Close()
			for rows.Next() {
				var name string
				if rows.Scan(&name) == nil && managedCredentialNameAllowed(name) {
					values[strings.TrimSpace(name)] = struct{}{}
				}
			}
		}
		platformRows, platformErr := a.configStore.db.Query("SELECT credential_refs_json FROM platform_integrations WHERE trim(credential_refs_json) <> ''")
		if platformErr == nil {
			defer platformRows.Close()
			for platformRows.Next() {
				var raw string
				if platformRows.Scan(&raw) != nil {
					continue
				}
				var refs map[string]any
				if json.Unmarshal([]byte(raw), &refs) != nil {
					continue
				}
				collectManagedCredentialRefs(refs, values)
			}
		}
	}
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}

func collectManagedCredentialRefs(value any, names map[string]struct{}) {
	switch typed := value.(type) {
	case string:
		if managedCredentialNameAllowed(typed) {
			names[strings.TrimSpace(typed)] = struct{}{}
		}
	case map[string]any:
		for _, child := range typed {
			collectManagedCredentialRefs(child, names)
		}
	case []any:
		for _, child := range typed {
			collectManagedCredentialRefs(child, names)
		}
	}
}

func (a *AgentRuntime) managedCredentials() (map[string]any, error) {
	path := managedCredentialPath()
	lines, persistedFile, err := managedEnvLines(path)
	if err != nil {
		return nil, err
	}
	persisted := map[string]bool{}
	for _, line := range lines {
		if name := managedEnvKey(line); managedCredentialNameAllowed(name) {
			persisted[name] = true
		}
	}
	items := make([]managedCredential, 0)
	for _, name := range a.managedCredentialNames() {
		configured := strings.TrimSpace(getenv(name)) != ""
		source := "未配置"
		if configured {
			source = "当前进程"
		}
		if persisted[name] {
			source = "托管凭据文件"
		}
		items = append(items, managedCredential{Name: name, Label: managedCredentialLabel(name), Configured: configured, Persisted: persisted[name], Required: managedCredentialRequired(name), Source: source})
	}
	return map[string]any{
		"items":                    items,
		"credentialFileConfigured": persistedFile,
		"managedAt":                time.Now().UTC().Format(time.RFC3339Nano),
	}, nil
}

func loadManagedCredentialsFile(path string) error {
	lines, exists, err := managedEnvLines(path)
	if err != nil || !exists {
		return err
	}
	for _, line := range lines {
		name := managedEnvKey(line)
		if !managedCredentialNameAllowed(name) {
			continue
		}
		value := strings.TrimSpace(line[strings.IndexByte(line, '=')+1:])
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = strings.ReplaceAll(value[1:len(value)-1], "\\'", "'")
		}
		if value != "" {
			if err := os.Setenv(name, value); err != nil {
				return fmt.Errorf("加载托管凭据 %s: %w", name, err)
			}
		}
	}
	return nil
}

func (a *AgentRuntime) applyManagedCredential(name, value string) {
	if value == "" {
		_ = os.Unsetenv(name)
	} else {
		_ = os.Setenv(name, value)
	}
	switch name {
	case "ERDAI_RUNTIME_TOKEN":
		a.runtimeToken = strings.TrimSpace(value)
	case "ERDAI_MODEL_API_KEY":
		a.modelAPIKey = strings.TrimSpace(value)
	case "ERDAI_GROK_API_KEY":
		a.grokAPIKey = strings.TrimSpace(value)
	case "ERDAI_IMAGE_API_KEY":
		a.imageAPIKey = strings.TrimSpace(value)
	case "ERDAI_OPS_TOKEN":
		a.opsToken = strings.TrimSpace(value)
	}
}

func (a *AgentRuntime) handleManagedCredentials(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/credentials" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		value, err := a.managedCredentials()
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	}
	if !strings.HasPrefix(path, "/api/v1/credentials/") {
		return mgmtNotFound("credential")
	}
	name, err := url.PathUnescape(strings.TrimPrefix(path, "/api/v1/credentials/"))
	if err != nil || !managedCredentialNameAllowed(name) || strings.Contains(name, "/") {
		return coreInvalid("凭据名称无效")
	}
	if r.Method != http.MethodPut && r.Method != http.MethodDelete {
		return mgmtMethodNotAllowed()
	}
	value := ""
	if r.Method == http.MethodPut {
		var payload managedCredentialPayload
		if _, err = decodeCoreObject(r, coreFieldSet("value"), "credential", &payload); err != nil {
			return err
		}
		if len(payload.Value) > 16384 {
			return coreInvalid("凭据长度不能超过 16384 字符")
		}
		value = payload.Value
	}
	if strings.TrimSpace(value) == "" {
		value = ""
	}
	if value == "" && managedCredentialRequired(name) {
		return coreInvalid("运行必需凭据不能清除")
	}
	if err = updateManagedEnvFile(managedCredentialPath(), name, value); err != nil {
		return &coreAPIError{status: http.StatusConflict, code: "credential_persistence_unavailable", message: err.Error()}
	}
	a.applyManagedCredential(name, value)
	mgmtWriteData(w, http.StatusOK, map[string]any{
		"name": name, "configured": strings.TrimSpace(value) != "", "persisted": true,
		"restartRequired": false, "updatedAt": time.Now().UTC().Format(time.RFC3339Nano),
	})
	return nil
}
