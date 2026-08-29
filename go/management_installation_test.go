package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagementInstallationStatusRedactsCredentialValues(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.modelAPIKey = "model-secret-that-must-not-appear"
	runtime.grokAPIKey = "grok-secret-that-must-not-appear"
	runtime.opsToken = "ops-secret-that-must-not-appear"
	response := managementRequest(t, runtime, http.MethodGet, "/api/v1/installation/status", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("installation status = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	for _, secret := range []string{runtime.modelAPIKey, runtime.grokAPIKey, runtime.opsToken} {
		if strings.Contains(body, secret) {
			t.Fatalf("installation status leaked secret %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, `"id":"model_provider"`) || !strings.Contains(body, `"configured":true`) {
		t.Fatalf("installation status omitted configured model: %s", body)
	}
}

func TestManagementInstallationStatusExcludesOptionalPlugins(t *testing.T) {
	runtime := newManagementRuntime(t)
	runtime.opsToken = "configured-plugin-token"
	status := runtime.installationStatus()
	for _, check := range status.Checks {
		if check.ID == "ops" || strings.Contains(check.Label, "Sub2API") {
			t.Fatalf("optional plugin appeared in installation checks: %+v", check)
		}
	}
}

// nextStableTestVersion derives a version strictly newer than the running
// build, so this suite survives every release bump instead of hardcoding one.
func nextStableTestVersion(t *testing.T) string {
	t.Helper()
	parts := strings.Split(erdaiRuntimeVersion, ".")
	if len(parts) != 3 {
		t.Fatalf("unexpected runtime version %q", erdaiRuntimeVersion)
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		t.Fatalf("unexpected runtime version %q", erdaiRuntimeVersion)
	}
	return parts[0] + "." + strconv.Itoa(minor+1) + ".0"
}

func TestManagementStableUpdateSelectsOnlyStableRelease(t *testing.T) {
	runtime := newManagementRuntime(t)
	nextVersion := nextStableTestVersion(t)
	nextTag := "v" + nextVersion
	nextAsset := "erdai-agent-stable-" + nextTag + ".tar.gz"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/example/erdai-agent/releases" || r.URL.Query().Get("per_page") != "20" {
			t.Fatalf("unexpected update request: %s", r.URL.String())
		}
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`[
            {"tag_name":"v0.12.0-rc1","prerelease":true,"html_url":"https://example.test/rc"},
            {"tag_name":"v0.11.9","html_url":"https://example.test/older"},
            {"tag_name":"` + nextTag + `","html_url":"https://example.test/stable","published_at":"2026-08-24T00:00:00Z","body":"stable release","assets":[{"name":"` + nextAsset + `","browser_download_url":"https://github.com/example/erdai-agent/releases/download/` + nextTag + `/` + nextAsset + `","digest":"sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef","size":123}]}
        ]`))
	}))
	defer server.Close()
	runtime.client = server.Client()
	runtime.updateRepository = "example/erdai-agent"
	runtime.updateAPIBaseURL = server.URL
	response := managementRequest(t, runtime, http.MethodGet, "/api/v1/update/check", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("update check = %d: %s", response.Code, response.Body.String())
	}
	body := response.Body.String()
	if !strings.Contains(body, `"latestVersion":"`+nextVersion+`"`) || !strings.Contains(body, `"updateAvailable":true`) || !strings.Contains(body, `"upgradeReady":true`) || strings.Contains(body, "0.12.0-rc1") {
		t.Fatalf("stable update selection = %s", body)
	}
	statusPath := filepath.Join(t.TempDir(), "update-status.json")
	requestPath := filepath.Join(t.TempDir(), "update-request.json")
	t.Setenv("ERDAI_UPDATE_STATUS_FILE", statusPath)
	t.Setenv("ERDAI_UPDATE_REQUEST_FILE", requestPath)
	status := `{"agentConfigured":true,"agentReady":true,"state":"idle","heartbeatAt":"` + time.Now().UTC().Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(statusPath, []byte(status), 0600); err != nil {
		t.Fatal(err)
	}
	requestResponse := managementRequest(t, runtime, http.MethodPost, "/api/v1/update/request", map[string]string{"version": nextVersion}, "admin")
	if requestResponse.Code != http.StatusAccepted {
		t.Fatalf("update request = %d: %s", requestResponse.Code, requestResponse.Body.String())
	}
	var request stableUpdateRequest
	requestRaw, err := os.ReadFile(requestPath)
	if err != nil || json.Unmarshal(requestRaw, &request) != nil {
		t.Fatalf("update request file = %s, err=%v", requestRaw, err)
	}
	if request.TargetVersion != nextVersion || request.AssetName != nextAsset || request.AssetDigest == "" {
		t.Fatalf("unexpected update request: %#v", request)
	}
	statusResponse := managementRequest(t, runtime, http.MethodGet, "/api/v1/update/status", nil, "admin")
	if statusResponse.Code != http.StatusOK || !strings.Contains(statusResponse.Body.String(), `"state":"pending"`) || !strings.Contains(statusResponse.Body.String(), request.RequestID) {
		t.Fatalf("pending update status = %d: %s", statusResponse.Code, statusResponse.Body.String())
	}
	duplicate := managementRequest(t, runtime, http.MethodPost, "/api/v1/update/request", map[string]string{"version": "0.12.0"}, "admin")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate update request = %d: %s", duplicate.Code, duplicate.Body.String())
	}
}

func TestStableUpdateRejectsDowngradeAndUnverifiedAsset(t *testing.T) {
	if stableVersionNewer("0.11.0", "0.11.1") || stableVersionNewer("0.11.1", "0.11.1") {
		t.Fatal("Stable version comparison accepted a downgrade or same version")
	}
	if !stableVersionNewer("0.12.0", "0.11.9") || !stableReleaseTag("v1.2.3") || stableReleaseTag("v1.2.3-rc1") {
		t.Fatal("Stable semantic version validation is inconsistent")
	}
	release := githubRelease{Assets: []githubReleaseAsset{{
		Name: "erdai-agent-stable-v0.12.0.tar.gz", BrowserDownloadURL: "https://github.com/example/erdai-agent/releases/download/v0.12.0/erdai-agent-stable-v0.12.0.tar.gz", Size: 123,
	}}}
	if _, ok := stableBundleAsset(release, "v0.12.0", "0.12.0"); ok {
		t.Fatal("Release asset without a GitHub SHA-256 digest was accepted")
	}
}

func TestUpdateAgentStatusRequiresFreshHeartbeat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "update-status.json")
	t.Setenv("ERDAI_UPDATE_STATUS_FILE", path)
	t.Setenv("ERDAI_UPDATE_REQUEST_FILE", filepath.Join(t.TempDir(), "update-request.json"))
	runtime := newManagementRuntime(t)
	stale := `{"agentReady":true,"state":"idle","heartbeatAt":"` + time.Now().UTC().Add(-2*time.Minute).Format(time.RFC3339Nano) + `"}`
	if err := os.WriteFile(path, []byte(stale), 0600); err != nil {
		t.Fatal(err)
	}
	status, err := runtime.readUpdateAgentStatus()
	if err != nil || status.AgentReady {
		t.Fatalf("stale update agent status = %#v, err=%v", status, err)
	}
}

func TestManagementCredentialsPersistWithoutReturningValue(t *testing.T) {
	runtime := newManagementRuntime(t)
	path := filepath.Join(t.TempDir(), "runtime.env")
	if err := os.WriteFile(path, []byte("ERDAI_QQ_SECRET=old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ERDAI_MANAGED_CREDENTIALS_FILE", path)
	t.Setenv("ERDAI_QQ_SECRET", "")
	response := managementRequest(t, runtime, http.MethodPut, "/api/v1/credentials/ERDAI_QQ_SECRET", map[string]string{"value": "qq-secret-value"}, "admin")
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "qq-secret-value") {
		t.Fatalf("credential update = %d: %s", response.Code, response.Body.String())
	}
	if got := os.Getenv("ERDAI_QQ_SECRET"); got != "qq-secret-value" {
		t.Fatalf("process credential = %q", got)
	}
	backup, err := os.ReadFile(path + ".bak")
	if err != nil || string(backup) != "ERDAI_QQ_SECRET=old\n" {
		t.Fatalf("credential backup = %q, err=%v", string(backup), err)
	}
	readback := managementRequest(t, runtime, http.MethodGet, "/api/v1/credentials", nil, "admin")
	if readback.Code != http.StatusOK || strings.Contains(readback.Body.String(), "qq-secret-value") || !strings.Contains(readback.Body.String(), `"configured":true`) {
		t.Fatalf("credential readback = %d: %s", readback.Code, readback.Body.String())
	}
}
