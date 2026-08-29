package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

func TestOPSCardPageIsPinnedToNinetyMinutes(t *testing.T) {
	pageURL, err := normalizedOPSCardPageURL("https://ohlao.cfd/monitor/cards?range=24h&group_by=platform&platform=openai&group=7&model=gpt")
	if err != nil {
		t.Fatal(err)
	}
	query := pageURL.Query()
	if query.Get("range") != "90m" || query.Get("group_by") != "platform_group" {
		t.Fatalf("card query = %q", pageURL.RawQuery)
	}
	for _, key := range []string{"platform", "group", "model"} {
		if query.Has(key) {
			t.Fatalf("card query retained %s filter: %q", key, pageURL.RawQuery)
		}
	}
}

func TestValidOPSCardPageContentRejectsEmptyShell(t *testing.T) {
	if validOPSCardPageContent(1, "") || validOPSCardPageContent(0, "08/28 12:00\n渠道") {
		t.Fatal("empty monitor card shell was accepted")
	}
	if !validOPSCardPageContent(2, "📡 渠道监控 08/28 12:00\n🟢 ChatGPT 可用 100.0% 0.1x") {
		t.Fatal("timestamped monitor card content was rejected")
	}
	if validOPSCardPageContent(2, "🟢 ChatGPT 可用 100.0% 0.1x\n没有时间戳") {
		t.Fatal("untimestamped monitor card content was accepted")
	}
}

func TestFreshOPSCardPageContentRejectsStaleCards(t *testing.T) {
	now := time.Date(2026, 8, 28, 13, 40, 0, 0, time.FixedZone("CST", 8*60*60))
	content := "📡 渠道监控 08/28 13:10\n🟢 ChatGPT 可用率 100.0% 0.1x"
	if !freshOPSCardPageContent(2, content, now, 90*time.Minute) {
		t.Fatal("fresh monitor card content was rejected")
	}
	stale := "📡 渠道监控 08/28 11:49\n🟢 ChatGPT 可用率 100.0% 0.1x"
	if freshOPSCardPageContent(2, stale, now, 90*time.Minute) {
		t.Fatal("stale monitor card content was accepted")
	}
	future := "📡 渠道监控 08/28 14:00\n🟢 ChatGPT 可用率 100.0% 0.1x"
	if freshOPSCardPageContent(2, future, now, 90*time.Minute) {
		t.Fatal("future monitor card content was accepted")
	}
}

func TestSub2APIMonitorLoginUsesDedicatedAccount(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/api/v1/auth/login" || r.Method != http.MethodPost {
			t.Fatalf("login request = %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["email"] != "monitor@example.invalid" || body["password"] != "test-password" {
			t.Fatalf("login body used unexpected credentials")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"access_token":"access","refresh_token":"refresh","expires_in":3600,"user":{"id":7,"role":"user"}}}`))
	}))
	defer server.Close()
	pageURL, err := url.Parse(server.URL + "/monitor/cards")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &AgentRuntime{
		client: server.Client(), sub2APIMonitorEmail: "monitor@example.invalid", sub2APIMonitorPassword: "test-password",
	}
	login, err := runtime.sub2APIMonitorLogin(context.Background(), pageURL)
	if err != nil {
		t.Fatal(err)
	}
	if login.AccessToken != "access" || login.RefreshToken != "refresh" || login.ExpiresIn != 3600 {
		t.Fatalf("login response = %+v", login)
	}
	if _, err = runtime.cachedSub2APIMonitorLogin(context.Background(), pageURL); err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.cachedSub2APIMonitorLogin(context.Background(), pageURL); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("login requests = %d, want direct login plus one cached-session login", requests.Load())
	}
}

func TestOPSCardBrowserMustBePrivate(t *testing.T) {
	publicURL, _ := url.Parse("https://browser.example.com:9222")
	privateURL, _ := url.Parse("http://erdai-monitor-browser:9222")
	if validOPSCardBrowserURL(publicURL) || !validOPSCardBrowserURL(privateURL) {
		t.Fatal("browser URL trust boundary is incorrect")
	}
}

func TestLiveOPSCardCapture(t *testing.T) {
	pageURL := os.Getenv("ERDAI_TEST_SUB2API_CARD_URL")
	browserURL := os.Getenv("ERDAI_TEST_MONITOR_BROWSER_URL")
	email := os.Getenv("ERDAI_TEST_SUB2API_MONITOR_EMAIL")
	password := os.Getenv("ERDAI_TEST_SUB2API_MONITOR_PASSWORD")
	if pageURL == "" || browserURL == "" || email == "" || password == "" {
		t.Skip("live Sub2API monitor capture is not configured")
	}
	runtime := &AgentRuntime{
		client: http.DefaultClient, sub2APIMonitorEmail: email, sub2APIMonitorPassword: password,
	}
	image, err := runtime.captureOPSCardPNG(context.Background(), opsPolicy{
		CardPageURL: pageURL, CardBrowserURL: browserURL, CardCaptureTimeoutSeconds: 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if output := os.Getenv("ERDAI_TEST_CAPTURE_OUTPUT"); output != "" {
		if err = os.WriteFile(output, image, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("captured %d PNG bytes", len(image))
}
