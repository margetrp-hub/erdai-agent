package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var stableUpdateRequestMu sync.Mutex

type installationCheck struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Configured bool   `json:"configured"`
	Required   bool   `json:"required"`
	Source     string `json:"source"`
	Detail     string `json:"detail"`
}

type installationStatus struct {
	Lifecycle       string              `json:"lifecycle"`
	Ready           bool                `json:"ready"`
	ConfiguredCount int                 `json:"configuredCount"`
	RequiredCount   int                 `json:"requiredCount"`
	Checks          []installationCheck `json:"checks"`
	CheckedAt       string              `json:"checkedAt"`
}

type stableUpdate struct {
	CurrentVersion  string `json:"currentVersion"`
	LatestVersion   string `json:"latestVersion,omitempty"`
	ReleaseTag      string `json:"releaseTag,omitempty"`
	UpdateAvailable bool   `json:"updateAvailable"`
	UpgradeReady    bool   `json:"upgradeReady"`
	Repository      string `json:"repository"`
	ReleaseURL      string `json:"releaseUrl,omitempty"`
	PublishedAt     string `json:"publishedAt,omitempty"`
	ReleaseNotes    string `json:"releaseNotes,omitempty"`
	AssetName       string `json:"assetName,omitempty"`
	AssetURL        string `json:"assetUrl,omitempty"`
	AssetDigest     string `json:"assetDigest,omitempty"`
	AssetSize       int64  `json:"assetSize,omitempty"`
	CheckedAt       string `json:"checkedAt"`
}

type githubRelease struct {
	TagName     string               `json:"tag_name"`
	Draft       bool                 `json:"draft"`
	Prerelease  bool                 `json:"prerelease"`
	HTMLURL     string               `json:"html_url"`
	PublishedAt string               `json:"published_at"`
	Body        string               `json:"body"`
	Assets      []githubReleaseAsset `json:"assets"`
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type updateAgentStatus struct {
	AgentConfigured bool   `json:"agentConfigured"`
	AgentReady      bool   `json:"agentReady"`
	State           string `json:"state"`
	RequestID       string `json:"requestId,omitempty"`
	TargetVersion   string `json:"targetVersion,omitempty"`
	Message         string `json:"message,omitempty"`
	RequestedAt     string `json:"requestedAt,omitempty"`
	StartedAt       string `json:"startedAt,omitempty"`
	FinishedAt      string `json:"finishedAt,omitempty"`
	HeartbeatAt     string `json:"heartbeatAt,omitempty"`
}

type stableUpdateRequest struct {
	RequestID     string `json:"requestId"`
	Repository    string `json:"repository"`
	TargetVersion string `json:"targetVersion"`
	ReleaseTag    string `json:"releaseTag"`
	AssetName     string `json:"assetName"`
	AssetURL      string `json:"assetUrl"`
	AssetDigest   string `json:"assetDigest,omitempty"`
	AssetSize     int64  `json:"assetSize,omitempty"`
	RequestedAt   string `json:"requestedAt"`
}

type updateRequestPayload struct {
	Version string `json:"version"`
}

func updateRequestFilePath() string {
	if value := strings.TrimSpace(os.Getenv("ERDAI_UPDATE_REQUEST_FILE")); value != "" {
		return filepath.Clean(value)
	}
	return "/app/data/update-request.json"
}

func updateStatusFilePath() string {
	if value := strings.TrimSpace(os.Getenv("ERDAI_UPDATE_STATUS_FILE")); value != "" {
		return filepath.Clean(value)
	}
	return "/app/update-status/update-status.json"
}

func (a *AgentRuntime) handleManagementInstallation(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	mgmtWriteData(w, http.StatusOK, a.installationStatus())
	return nil
}

func (a *AgentRuntime) installationStatus() installationStatus {
	checks := []installationCheck{
		{ID: "admin_token", Label: "管理员会话密钥", Configured: len(strings.TrimSpace(a.adminToken)) >= 32, Required: true, Source: "process environment", Detail: "ERDAI_ADMIN_TOKEN"},
		{ID: "runtime_token", Label: "运行时服务密钥", Configured: len(strings.TrimSpace(a.runtimeToken)) >= 32, Required: true, Source: "process environment / managed credential file", Detail: "ERDAI_RUNTIME_TOKEN"},
		{ID: "encryption_key", Label: "运行数据加密", Configured: a.aead != nil, Required: true, Source: "process environment", Detail: "ERDAI_RUN_ENCRYPTION_KEY"},
		{ID: "model_provider", Label: "主模型供应商", Configured: strings.TrimSpace(a.modelAPIKey) != "", Required: true, Source: "process environment / managed credential file / provider reference", Detail: "主模型凭据已加载"},
		{ID: "semantic_provider", Label: "Embedding 服务", Configured: envConfigured("ERDAI_LOCAL_SEMANTIC_KEY"), Required: true, Source: "process environment / managed credential file", Detail: "ERDAI_LOCAL_SEMANTIC_KEY"},
		{ID: "qq_official", Label: "QQ 官方连接器", Configured: a.configuredIntegration("qq_official", "ERDAI_QQ_SECRET"), Required: false, Source: "Core config / process environment", Detail: "可在平台与接入中测试"},
		{ID: "grok", Label: "Grok 搜索与多媒体", Configured: strings.TrimSpace(a.grokAPIKey) != "" || a.configuredIntegration("grok_policy", "ERDAI_GROK_API_KEY"), Required: false, Source: "Core config / process environment", Detail: "可在模型与供应商中测试"},
		{ID: "image", Label: "图片生成", Configured: strings.TrimSpace(a.imageAPIKey) != "" || a.configuredIntegration("image_policy", "ERDAI_IMAGE_API_KEY"), Required: false, Source: "Core config / process environment", Detail: "可在模型与供应商中测试"},
	}
	configuredCount := 0
	requiredCount := 0
	ready := true
	for _, check := range checks {
		if check.Configured {
			configuredCount++
		}
		if check.Required {
			requiredCount++
			if !check.Configured {
				ready = false
			}
		}
	}
	lifecycle := "setup_required"
	if ready {
		lifecycle = "active"
	}
	return installationStatus{
		Lifecycle: lifecycle, Ready: ready, ConfiguredCount: configuredCount,
		RequiredCount: requiredCount, Checks: checks,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func envConfigured(name string) bool {
	value, ok := os.LookupEnv(name)
	return ok && strings.TrimSpace(value) != ""
}

func (a *AgentRuntime) configuredIntegration(id, fallbackEnv string) bool {
	if a.configStore != nil && a.configStore.db != nil {
		var raw string
		if err := a.configStore.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = ?", id).Scan(&raw); err == nil {
			var config map[string]any
			if json.Unmarshal([]byte(raw), &config) == nil {
				if configured, ok := config["credentialConfigured"].(bool); ok && configured {
					return true
				}
				if ref, ok := config["credentialRef"].(string); ok && envConfigured(ref) {
					return true
				}
			}
		}
	}
	return envConfigured(fallbackEnv)
}

func (a *AgentRuntime) handleManagementUpdateCheck(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	value, err := a.checkStableUpdate(r.Context())
	if err != nil {
		return &coreAPIError{status: http.StatusBadGateway, code: "update_check_failed", message: err.Error()}
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

func (a *AgentRuntime) handleManagementUpdateStatus(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	status, err := a.readUpdateAgentStatus()
	if err != nil {
		return &coreAPIError{status: http.StatusBadGateway, code: "update_agent_status_unavailable", message: err.Error()}
	}
	mgmtWriteData(w, http.StatusOK, status)
	return nil
}

func (a *AgentRuntime) handleManagementUpdateRequest(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodPost {
		return mgmtMethodNotAllowed()
	}
	var payload updateRequestPayload
	if _, err := decodeCoreObject(r, coreFieldSet("version"), "update", &payload); err != nil {
		return err
	}
	update, err := a.checkStableUpdate(r.Context())
	if err != nil {
		return &coreAPIError{status: http.StatusBadGateway, code: "update_check_failed", message: err.Error()}
	}
	if !update.UpdateAvailable {
		return &coreAPIError{status: http.StatusConflict, code: "update_not_available", message: "当前没有可用的 Stable 更新"}
	}
	if !update.UpgradeReady {
		return &coreAPIError{status: http.StatusConflict, code: "release_bundle_unavailable", message: "Stable 发布缺少受支持的 Release Bundle 资产"}
	}
	stableUpdateRequestMu.Lock()
	defer stableUpdateRequestMu.Unlock()
	status, err := a.readUpdateAgentStatus()
	if err != nil {
		return &coreAPIError{status: http.StatusBadGateway, code: "update_agent_status_unavailable", message: err.Error()}
	}
	if !status.AgentReady {
		return &coreAPIError{status: http.StatusConflict, code: "update_agent_unavailable", message: "宿主机 Stable 升级代理未就绪"}
	}
	if status.State == "pending" || status.State == "running" {
		return &coreAPIError{status: http.StatusConflict, code: "update_in_progress", message: "已有 Stable 升级正在处理"}
	}
	if _, ok, err := readStableUpdateRequest(); err != nil {
		return &coreAPIError{status: http.StatusConflict, code: "update_request_unavailable", message: err.Error()}
	} else if ok && status.State != "succeeded" && status.State != "failed" {
		return &coreAPIError{status: http.StatusConflict, code: "update_in_progress", message: "已有 Stable 升级请求等待宿主机处理"}
	}
	if version := strings.TrimSpace(payload.Version); version != "" && version != update.LatestVersion {
		return coreInvalid("只能请求刚刚检查到的 Stable 版本")
	}
	requestID, err := newUpdateRequestID()
	if err != nil {
		return err
	}
	request := stableUpdateRequest{
		RequestID: requestID, Repository: update.Repository, TargetVersion: update.LatestVersion,
		ReleaseTag: update.ReleaseTag, AssetName: update.AssetName, AssetURL: update.AssetURL,
		AssetDigest: update.AssetDigest, AssetSize: update.AssetSize,
		RequestedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeStableUpdateRequest(request); err != nil {
		return &coreAPIError{status: http.StatusConflict, code: "update_request_unavailable", message: err.Error()}
	}
	mgmtWriteData(w, http.StatusAccepted, updateAgentStatus{
		AgentConfigured: true, AgentReady: true, State: "pending", RequestID: requestID,
		TargetVersion: update.LatestVersion, RequestedAt: request.RequestedAt,
	})
	return nil
}

func newUpdateRequestID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return time.Now().UTC().Format("20060102-150405") + "-" + hex.EncodeToString(raw), nil
}

func (a *AgentRuntime) readUpdateAgentStatus() (updateAgentStatus, error) {
	status := updateAgentStatus{State: "unavailable"}
	raw, err := os.ReadFile(updateStatusFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return status, fmt.Errorf("读取 Stable 升级代理状态失败: %w", err)
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return status, errors.New("Stable 升级代理状态格式无效")
	}
	status.AgentConfigured = true
	status.AgentReady = status.AgentReady && freshUpdateHeartbeat(status.HeartbeatAt)
	if status.State == "" {
		status.State = "idle"
	}
	if request, ok, requestErr := readStableUpdateRequest(); requestErr != nil {
		return status, requestErr
	} else if ok && request.RequestID != status.RequestID {
		status.State = "pending"
		status.RequestID = request.RequestID
		status.TargetVersion = request.TargetVersion
		status.RequestedAt = request.RequestedAt
		status.Message = "等待宿主机 Stable 升级代理处理"
	}
	return status, nil
}

func freshUpdateHeartbeat(value string) bool {
	when, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(value))
	if err != nil {
		return false
	}
	age := time.Since(when)
	return age >= -30*time.Second && age <= 90*time.Second
}

func writeStableUpdateRequest(request stableUpdateRequest) error {
	path := updateRequestFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("创建 Stable 升级请求目录失败: %w", err)
	}
	data, err := json.MarshalIndent(request, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".erdai-update-request-*")
	if err != nil {
		return fmt.Errorf("创建 Stable 升级请求临时文件失败: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0600); err == nil {
		_, err = temporary.Write(append(data, '\n'))
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("写入 Stable 升级请求失败: %w", err)
	}
	if err = os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("替换 Stable 升级请求失败: %w", err)
	}
	return nil
}

func readStableUpdateRequest() (stableUpdateRequest, bool, error) {
	raw, err := os.ReadFile(updateRequestFilePath())
	if errors.Is(err, os.ErrNotExist) {
		return stableUpdateRequest{}, false, nil
	}
	if err != nil {
		return stableUpdateRequest{}, false, fmt.Errorf("读取 Stable 升级请求失败: %w", err)
	}
	var request stableUpdateRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return stableUpdateRequest{}, false, errors.New("Stable 升级请求格式无效")
	}
	return request, true, nil
}

func (a *AgentRuntime) checkStableUpdate(ctx context.Context) (stableUpdate, error) {
	repository := strings.TrimSpace(a.updateRepository)
	if repository == "" {
		repository = strings.TrimSpace(os.Getenv("ERDAI_UPDATE_REPOSITORY"))
	}
	if repository == "" {
		repository = "margetrp-hub/erdai-agent"
	}
	if !validGitHubRepository(repository) {
		return stableUpdate{}, errors.New("Stable 更新仓库配置无效")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(a.updateAPIBaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.github.com"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/repos/"+repository+"/releases?per_page=20", nil)
	if err != nil {
		return stableUpdate{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "ErDai-Agent-Update/"+erdaiRuntimeVersion)
	client := a.client
	if client == nil {
		client = &http.Client{}
	}
	checkCtx, cancel := context.WithTimeout(request.Context(), 8*time.Second)
	defer cancel()
	request = request.WithContext(checkCtx)
	response, err := client.Do(request)
	if err != nil {
		return stableUpdate{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return stableUpdate{}, fmt.Errorf("GitHub Stable 更新检查返回 HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return stableUpdate{}, err
	}
	var releases []githubRelease
	if err := json.Unmarshal(body, &releases); err != nil {
		return stableUpdate{}, errors.New("GitHub Stable 更新响应格式无效")
	}
	value := stableUpdate{
		CurrentVersion: erdaiRuntimeVersion, Repository: repository,
		CheckedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if release, tag, ok := latestStableRelease(releases); ok {
		value.LatestVersion = strings.TrimPrefix(tag, "v")
		value.ReleaseTag = tag
		value.UpdateAvailable = stableVersionNewer(value.LatestVersion, erdaiRuntimeVersion)
		value.ReleaseURL = release.HTMLURL
		value.PublishedAt = release.PublishedAt
		value.ReleaseNotes = truncateReleaseNotes(release.Body, 600)
		if asset, ok := stableBundleAsset(release, tag, value.LatestVersion); ok {
			value.AssetName, value.AssetURL, value.AssetDigest, value.AssetSize = asset.Name, asset.BrowserDownloadURL, asset.Digest, asset.Size
			value.UpgradeReady = value.UpdateAvailable
		}
	}
	return value, nil
}

func latestStableRelease(releases []githubRelease) (githubRelease, string, bool) {
	var selected githubRelease
	var selectedVersion [3]int
	selectedTag := ""
	found := false
	for _, release := range releases {
		tag := strings.TrimSpace(release.TagName)
		version, ok := parseStableVersion(tag)
		if release.Draft || release.Prerelease || !ok {
			continue
		}
		if !found || versionTupleGreater(version, selectedVersion) {
			selected, selectedTag, selectedVersion, found = release, tag, version, true
		}
	}
	return selected, selectedTag, found
}

func stableBundleAsset(release githubRelease, tag, version string) (githubReleaseAsset, bool) {
	allowed := map[string]struct{}{
		"erdai-agent-stable-" + tag + ".tar.gz":     {},
		"erdai-agent-stable-" + version + ".tar.gz": {},
		"erdai-agent-" + tag + ".tar.gz":            {},
		"erdai-agent-" + version + ".tar.gz":        {},
	}
	for _, asset := range release.Assets {
		if _, ok := allowed[strings.TrimSpace(asset.Name)]; !ok || !validGitHubAssetURL(asset.BrowserDownloadURL) || !validAssetDigest(asset.Digest) {
			continue
		}
		if asset.Size <= 0 || asset.Size > 4*1024*1024*1024 {
			continue
		}
		return asset, true
	}
	return githubReleaseAsset{}, false
}

func validGitHubAssetURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && parsed.Scheme == "https" && strings.EqualFold(parsed.Host, "github.com") && strings.Contains(parsed.Path, "/releases/download/")
}

func validAssetDigest(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func validGitHubRepository(repository string) bool {
	parts := strings.Split(repository, "/")
	return len(parts) == 2 && strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != "" && !strings.ContainsAny(repository, " \t\r\n")
}

func stableReleaseTag(tag string) bool {
	_, ok := parseStableVersion(tag)
	return ok
}

func stableVersionNewer(candidate, current string) bool {
	candidateVersion, candidateOK := parseStableVersion(candidate)
	currentVersion, currentOK := parseStableVersion(current)
	if !candidateOK || !currentOK {
		return false
	}
	return versionTupleGreater(candidateVersion, currentVersion)
}

func versionTupleGreater(candidate, current [3]int) bool {
	for index := range candidate {
		if candidate[index] != current[index] {
			return candidate[index] > current[index]
		}
	}
	return false
}

func parseStableVersion(value string) ([3]int, bool) {
	var result [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) != len(result) {
		return result, false
	}
	for index, part := range parts {
		if part == "" || len(part) > 9 {
			return result, false
		}
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return result, false
		}
		result[index] = number
	}
	return result, true
}

func truncateReleaseNotes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "..."
}
