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

func TestGatewayHealthAndStatic(t *testing.T) {
	gateway := NewGateway("")
	server := httptest.NewServer(gateway)
	defer server.Close()
	response, err := http.Get(server.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d", response.StatusCode)
	}
	response.Body.Close()
	response, err = http.Get(server.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("static status = %d", response.StatusCode)
	}
	if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	for _, section := range []string{
		"二呆智能体",
		"data-view=\"overview\"",
		"data-view=\"system\"",
		"data-view=\"models\"",
		"data-view=\"roles\"",
		"data-view=\"worldbook\"",
		"data-view=\"knowledge\"",
		"data-view=\"tools\"",
		"data-view=\"routing\"",
		"data-view=\"operations\"",
		"data-view=\"security\"",
	} {
		if !strings.Contains(string(body), section) {
			t.Fatalf("static page missing %s", section)
		}
	}
	favicon, err := http.Get(server.URL + "/favicon.svg")
	if err != nil {
		t.Fatal(err)
	}
	defer favicon.Body.Close()
	if favicon.StatusCode != http.StatusOK {
		t.Fatalf("favicon status = %d", favicon.StatusCode)
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

func TestToolAndMCPConfigurationUIIsEmbedded(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	response, err := http.Get(gateway.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, marker := range []string{
		`data-form="tool"`, `data-form="mcp"`,
		`data-form="admin-login"`, `/auth/login`, `/auth/logout`,
		`new-tool`, `edit-tool`, `toggle-tool`, `delete-tool`,
		`new-mcp`, `edit-mcp`, `toggle-mcp`, `delete-mcp`,
		`inspect-mcp`, `/discover`, `/api/v1/tools`, `/api/v1/mcp/servers`,
		`tools-subnav`, `show-tools`, `show-mcp`, `control-dialog`, `close-dialog`, `registry-row`,
		`page-subnav`, `page-subnav-tab`, `set-section`, `pageSections`, `clearActiveDialog`,
		`renderEpoch`, `viewWarmups`, `loadingView`, `warmView`, `data-rendered-view`,
		`['channels', '渠道与接管'`, `['connections', '供应商连接'`, `['cards', '角色卡'`,
		`['relationships', '关系与亲密度'`, `['retrieval', '检索与向量'`, `['runs', '最近运行'`,
		`Go 原生连接`, `toolProgressPhotoMessages`, `自拍反馈文案`,
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("embedded app.js missing %s", marker)
		}
	}
	for _, marker := range []string{"memoryKernelMap", "relationshipPulseView"} {
		if !strings.Contains(string(body), marker) {
			t.Fatalf("management UI missing memory-kernel marker %q", marker)
		}
	}
	if strings.Contains(script, "只读元数据") {
		t.Fatal("embedded app.js still contains the legacy read-only tools UI")
	}
}

func TestEmbeddedWebAssetsUseConditionalCaching(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()

	response, err := http.Get(gateway.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	etag := response.Header.Get("ETag")
	response.Body.Close()
	if etag == "" {
		t.Fatal("embedded web asset is missing an ETag")
	}
	if got := response.Header.Get("Cache-Control"); !strings.Contains(got, "must-revalidate") {
		t.Fatalf("embedded web asset cache-control = %q", got)
	}

	request, _ := http.NewRequest(http.MethodGet, gateway.URL+"/app.js", nil)
	request.Header.Set("If-None-Match", etag)
	cached, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer cached.Body.Close()
	if cached.StatusCode != http.StatusNotModified {
		t.Fatalf("conditional asset status = %d", cached.StatusCode)
	}
}

func TestPersonaMemoryManagementUIIsEmbedded(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	response, err := http.Get(gateway.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, marker := range []string{
		`async function memoriesView()`, `data-form="persona-memory"`, `/api/v1/memories`,
		`persona-memories`, `edit-memory`, `delete-memory`,
	} {
		if !strings.Contains(script, marker) {
			t.Fatalf("embedded memory UI missing %s", marker)
		}
	}
}

func TestKnowledgeOwnsVectorAndDocumentConfiguration(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	response, err := http.Get(gateway.URL + "/app.js")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	script := string(body)
	for _, form := range []string{`data-form="retrieval-policy"`, `data-form="document-policy"`} {
		if strings.Count(script, form) != 1 {
			t.Fatalf("configuration form %s must have one owner", form)
		}
	}
	start := strings.Index(script, "async function knowledgeView()")
	if start < 0 {
		t.Fatal("knowledge view was not found")
	}
	end := strings.Index(script[start:], "function documentForm")
	if end < 0 {
		t.Fatal("knowledge view end was not found")
	}
	knowledge := script[start : start+end]
	for _, marker := range []string{"retrievalPolicyCard(retrievalPolicy)", "documentPolicyCard(documentPolicy)"} {
		if !strings.Contains(knowledge, marker) {
			t.Fatalf("knowledge view missing %s", marker)
		}
	}
}

func TestEmbeddedUIUsesUnifiedProductIdentity(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	for _, asset := range []string{"/", "/app.js"} {
		response, err := http.Get(gateway.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		content := string(body)
		if !strings.Contains(content, "二呆智能体") {
			t.Fatalf("%s is missing the unified product name", asset)
		}
		for _, legacyName := range []string{"AstrBot", "astrbot", "4.26.8"} {
			if strings.Contains(content, legacyName) {
				t.Fatalf("%s exposes legacy product name %q", asset, legacyName)
			}
		}
	}
}

func TestEmbeddedUIDoesNotRequestUnsupportedPageLimits(t *testing.T) {
	response := httptest.NewRecorder()
	NewGateway("").ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/app.js", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("/app.js status = %d", response.Code)
	}
	for _, match := range regexp.MustCompile(`limit=([0-9]+)`).FindAllStringSubmatch(response.Body.String(), -1) {
		limit, err := strconv.Atoi(match[1])
		if err != nil {
			t.Fatal(err)
		}
		if limit > 100 {
			t.Fatalf("/app.js requests unsupported page limit %d", limit)
		}
	}
}

func TestCharacterCardAndMediaQuotaUIIsEmbedded(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	for _, asset := range []string{"/app.js", "/character-card.js", "/styles.css"} {
		response, err := http.Get(gateway.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", asset, response.StatusCode)
		}
		content := string(body)
		if asset == "/app.js" {
			for _, marker := range []string{
				"import-persona", "export-persona", "export-v2", "avatarDataUri",
				"/api/v1/runtime/media-quotas", "trustedAdminBypass", "mediaQuotaWhitelist",
				"visualDirectorEnabled", "visualTimezone", "selfieTypes", "personaDossier",
			} {
				if !strings.Contains(content, marker) {
					t.Fatalf("embedded app.js missing %s", marker)
				}
			}
		}
		if asset == "/styles.css" && (!strings.Contains(content, ".persona-grid") || !strings.Contains(content, ".persona-dossier") || !strings.Contains(content, ".memory-kernel-map") || !strings.Contains(content, ".relationship-pulse-grid")) {
			t.Fatal("embedded styles.css missing persona card or dossier layout")
		}
	}
}

func TestUnifiedLeftControlPlaneShellIsEmbedded(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	checks := map[string][]string{
		"/": {
			"topbar-inner", "instance-switch", "sidebar-toggle", "data-domain=\"workbench\"",
			"data-domain=\"agents\"", "data-domain=\"capabilities\"", "data-domain=\"infrastructure\"",
			"data-domain=\"governance\"", "data-view=\"roles\"", "data-view=\"operations\"",
		},
		"/app.js": {
			"const DOMAINS", "MESSAGE_COPY_LIBRARY", "moduleGroup", "expand-message-copy",
			"renderRoleMenu", "data-role-id", "domainForView",
		},
		"/styles.css": {
			"OPS-derived control-plane shell", ".nav-cluster[data-domain=\"agents\"]",
			".settings-module", ".copy-module-grid", ".role-menu", ".overview-inventory",
			"r47 control-room composition", ".loading-stage", "workspace-enter", "prefers-reduced-motion",
		},
		"/icons/panel-left.svg": {"<svg", "<rect", "M9 3v18"},
	}
	for asset, markers := range checks {
		response, err := http.Get(gateway.URL + asset)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		response.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", asset, response.StatusCode)
		}
		content := string(body)
		if asset == "/" && strings.Contains(content, "workspace-nav") {
			t.Fatal("top work-domain navigation must be removed")
		}
		for _, marker := range markers {
			if !strings.Contains(content, marker) {
				t.Fatalf("%s missing %q", asset, marker)
			}
		}
	}
}

func TestNestedAuthorityCheckboxesKeepTheirNativeWidth(t *testing.T) {
	gateway := httptest.NewServer(NewGateway(""))
	defer gateway.Close()
	response, err := http.Get(gateway.URL + "/styles.css")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	styles := string(body)
	if strings.Contains(styles, ".field input,") {
		t.Fatal("nested authority checkboxes must not inherit full-width field input styles")
	}
	if !strings.Contains(styles, ".field > input,") {
		t.Fatal("direct field inputs must retain the standard input styles")
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
