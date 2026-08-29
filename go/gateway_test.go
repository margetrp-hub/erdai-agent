package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var modernAssetPattern = regexp.MustCompile(`/(?:assets/[^"']+\.(?:js|css)|favicon\.svg)`)

func TestGatewayHealthAndModernStatic(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()

	health, err := http.Get(gateway.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", health.StatusCode)
	}

	response, err := http.Get(gateway.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	index := string(body)
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("static response = %d %q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	for _, marker := range []string{"二呆智能体 · 控制台", `<div id="root"></div>`, `type="module"`, "/assets/"} {
		if !strings.Contains(index, marker) {
			t.Fatalf("modern WebUI index missing %q", marker)
		}
	}
	if strings.Contains(index, `data-view="overview"`) || strings.Contains(index, "/app.js") {
		t.Fatal("legacy WebUI is still being served")
	}
	for _, asset := range modernAssetPattern.FindAllString(index, -1) {
		assetResponse, requestErr := http.Get(gateway.URL + asset)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		assetResponse.Body.Close()
		if assetResponse.StatusCode != http.StatusOK {
			t.Fatalf("asset %s status = %d", asset, assetResponse.StatusCode)
		}
	}
}

func TestCoreListenerExposesOnlyTransportRuntimeAPI(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	gateway := NewGateway("")
	gateway.runtime = runtime

	management := httptest.NewRecorder()
	gateway.ServeHTTP(management, httptest.NewRequest(http.MethodGet, "/api/v1/runtime/config", nil))
	if management.Code != http.StatusNotFound {
		t.Fatalf("core listener exposed management route: %d %s", management.Code, management.Body.String())
	}

	transport := httptest.NewRecorder()
	gateway.ServeHTTP(transport, httptest.NewRequest(http.MethodPost, "/api/v1/transport/events", nil))
	if transport.Code != http.StatusUnauthorized {
		t.Fatalf("transport route boundary = %d %s", transport.Code, transport.Body.String())
	}

	prepare := httptest.NewRecorder()
	gateway.ServeHTTP(prepare, httptest.NewRequest(http.MethodPost, "/api/v1/runtime/prepare", nil))
	if prepare.Code != http.StatusUnauthorized {
		t.Fatalf("runtime prepare route boundary = %d %s", prepare.Code, prepare.Body.String())
	}
}

func TestModernWebUIAssetsAndConditionalCaching(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	indexResponse, err := http.Get(gateway.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	indexBody, err := io.ReadAll(indexResponse.Body)
	indexResponse.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	assets := modernAssetPattern.FindAllString(string(indexBody), -1)
	var scriptPath, stylePath string
	for _, asset := range assets {
		if strings.HasSuffix(asset, ".js") {
			scriptPath = asset
		}
		if strings.HasSuffix(asset, ".css") {
			stylePath = asset
		}
	}
	if scriptPath == "" || stylePath == "" {
		t.Fatalf("modern asset paths not found: %v", assets)
	}

	checkAsset := func(path string, markers []string) string {
		t.Helper()
		response, requestErr := http.Get(gateway.URL + path)
		if requestErr != nil {
			t.Fatal(requestErr)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK || response.Header.Get("ETag") == "" {
			t.Fatalf("asset %s = %d, etag %q", path, response.StatusCode, response.Header.Get("ETag"))
		}
		content := string(body)
		for _, marker := range markers {
			if !strings.Contains(content, marker) {
				t.Fatalf("asset %s missing %q", path, marker)
			}
		}
		return content
	}
	script := checkAsset(scriptPath, []string{
		"二呆智能体", "受信任适配器", "外观库", "快捷开关", "第 ",
		"/api/v1/plugins/readiness",
		"/api/v1/appearance-libraries", "/api/v1/runtime/media-quotas",
	})
	for _, removed := range []string{"CAPABILITIES / PLUGINS", "ERDAI_WEBUI_MODE"} {
		if strings.Contains(script, removed) {
			t.Fatalf("modern WebUI still contains retired marker %q", removed)
		}
	}
	checkAsset(stylePath, []string{".visual-library-actions", ".persona-modern-grid", ".module-table-pager", ".plugin-quick-toggle", "data-ui-theme=anime"})
	for _, match := range regexp.MustCompile(`limit=([0-9]+)`).FindAllStringSubmatch(script, -1) {
		limit, parseErr := strconv.Atoi(match[1])
		if parseErr != nil {
			t.Fatal(parseErr)
		}
		if limit > 100 {
			t.Fatalf("WebUI requests unsupported page limit %d", limit)
		}
	}

	first, err := http.Get(gateway.URL + scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	etag := first.Header.Get("ETag")
	first.Body.Close()
	request, _ := http.NewRequest(http.MethodGet, gateway.URL+scriptPath, nil)
	request.Header.Set("If-None-Match", etag)
	cached, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	cached.Body.Close()
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional asset status = %d", cached.StatusCode)
	}
}

func TestGatewayRejectsUnknownAPIRoutesWithoutProxying(t *testing.T) {
	gateway := httptest.NewServer(NewGateway("server-side-admin-token-1234567890"))
	defer gateway.Close()
	request, _ := http.NewRequest(http.MethodGet, gateway.URL+"/api/v1/overview?x=1", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Cookie", "session=secret")
	request.Header.Set(adminTokenHeader, "attacker-controlled-token")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown API status = %d", response.StatusCode)
	}
	bad, _ := http.Get(gateway.URL + "/api/v1/../admin")
	bad.Body.Close()
	if bad.StatusCode != http.StatusNotFound {
		t.Fatalf("path traversal status = %d", bad.StatusCode)
	}
	post, _ := http.NewRequest(http.MethodPatch, gateway.URL+"/api/v1/overview", nil)
	bad, _ = http.DefaultClient.Do(post)
	bad.Body.Close()
	if bad.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", bad.StatusCode)
	}
}

func TestCoreAndAdminListenersKeepMutationAuthoritySeparate(t *testing.T) {
	runtime := newManagementRuntime(t)
	coreGateway := NewGateway("")
	coreGateway.runtime = runtime
	adminGateway := NewGateway(managementAdminToken)
	adminGateway.runtime = runtime

	payload, err := json.Marshal(map[string]any{"mode": "shadow"})
	if err != nil {
		t.Fatal(err)
	}
	request := func(gateway *Gateway, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/integrations/channel_runtime", bytes.NewReader(payload))
		req.Header.Set("content-type", "application/json")
		if token != "" {
			req.Header.Set(adminTokenHeader, token)
		}
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		return response
	}

	if response := request(coreGateway, ""); response.Code != http.StatusNotFound {
		t.Fatalf("anonymous core mutation = %d: %s", response.Code, response.Body.String())
	}
	if response := request(coreGateway, managementAdminToken); response.Code != http.StatusNotFound {
		t.Fatalf("authenticated core mutation = %d: %s", response.Code, response.Body.String())
	}
	if response := request(adminGateway, "attacker-controlled-token"); response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated admin listener mutation = %d: %s", response.Code, response.Body.String())
	}
	if response := request(adminGateway, managementAdminToken); response.Code != http.StatusOK {
		t.Fatalf("token-authenticated admin listener mutation = %d: %s", response.Code, response.Body.String())
	}
}

func TestAdminListenerBrowserSessionRequiresSameOriginForWrites(t *testing.T) {
	runtime := newManagementRuntime(t)
	gateway := NewGateway(managementAdminToken)
	gateway.runtime = runtime

	loginBody, _ := json.Marshal(map[string]string{"token": managementAdminToken})
	login := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(loginBody))
	login.Header.Set("content-type", "application/json")
	loginResponse := httptest.NewRecorder()
	gateway.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK || len(loginResponse.Result().Cookies()) != 1 {
		t.Fatalf("login = %d: %s", loginResponse.Code, loginResponse.Body.String())
	}
	sessionCookie := loginResponse.Result().Cookies()[0]

	payload, _ := json.Marshal(map[string]any{"mode": "shadow"})
	request := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "http://admin.test/api/v1/integrations/channel_runtime", bytes.NewReader(payload))
		req.Header.Set("content-type", "application/json")
		req.Header.Set("Origin", origin)
		req.AddCookie(sessionCookie)
		response := httptest.NewRecorder()
		gateway.ServeHTTP(response, req)
		return response
	}
	if response := request("https://attacker.test"); response.Code != http.StatusForbidden {
		t.Fatalf("cross-origin session write = %d: %s", response.Code, response.Body.String())
	}
	if response := request("http://admin.test"); response.Code != http.StatusOK {
		t.Fatalf("same-origin session write = %d: %s", response.Code, response.Body.String())
	}

	logout := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
	logout.AddCookie(sessionCookie)
	logoutResponse := httptest.NewRecorder()
	gateway.ServeHTTP(logoutResponse, logout)
	if logoutResponse.Code != http.StatusOK {
		t.Fatalf("logout = %d: %s", logoutResponse.Code, logoutResponse.Body.String())
	}
	if response := request("http://admin.test"); response.Code != http.StatusUnauthorized {
		t.Fatalf("logged-out session write = %d: %s", response.Code, response.Body.String())
	}
}
