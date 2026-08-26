package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

const (
	defaultOPSCardBrowserURL = "http://erdai-monitor-browser:9222"
	defaultOPSCardTimeout    = 45 * time.Second
)

type sub2APILogin struct {
	AccessToken  string          `json:"access_token"`
	RefreshToken string          `json:"refresh_token"`
	ExpiresIn    int64           `json:"expires_in"`
	Requires2FA  bool            `json:"requires_2fa"`
	User         json.RawMessage `json:"user"`
}

func validOPSCardPageURL(parsed *url.URL) bool {
	if parsed == nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	return parsed.Scheme == "https" || parsed.Scheme == "http" && mgmtPrivateHost(parsed.Hostname())
}

func validOPSCardBrowserURL(parsed *url.URL) bool {
	return parsed != nil && parsed.Scheme == "http" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && mgmtPrivateHost(parsed.Hostname())
}

func normalizedOPSCardPageURL(raw string) (*url.URL, error) {
	pageURL, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || !validOPSCardPageURL(pageURL) {
		return nil, errors.New("Sub2API monitor card page URL is invalid")
	}
	query := pageURL.Query()
	query.Set("range", "90m")
	query.Set("group_by", "platform_group")
	query.Del("platform")
	query.Del("group")
	query.Del("model")
	pageURL.RawQuery = query.Encode()
	return pageURL, nil
}

func (a *AgentRuntime) sub2APIMonitorLogin(ctx context.Context, pageURL *url.URL) (sub2APILogin, error) {
	if strings.TrimSpace(a.sub2APIMonitorEmail) == "" || strings.TrimSpace(a.sub2APIMonitorPassword) == "" {
		return sub2APILogin{}, errors.New("Sub2API monitor account is not configured")
	}
	body, err := json.Marshal(map[string]string{
		"email": a.sub2APIMonitorEmail, "password": a.sub2APIMonitorPassword,
		"turnstile_token": "", "tencent_captcha_ticket": "", "tencent_captcha_randstr": "",
	})
	if err != nil {
		return sub2APILogin{}, err
	}
	loginURL := *pageURL
	loginURL.Path = "/api/v1/auth/login"
	loginURL.RawPath = ""
	loginURL.RawQuery = ""
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, loginURL.String(), bytes.NewReader(body))
	if err != nil {
		return sub2APILogin{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	response, err := a.client.Do(request)
	if err != nil {
		return sub2APILogin{}, fmt.Errorf("Sub2API monitor login failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return sub2APILogin{}, fmt.Errorf("Sub2API monitor login returned HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1024*1024+1))
	if err != nil || len(raw) == 0 || len(raw) > 1024*1024 {
		return sub2APILogin{}, errors.New("Sub2API monitor login returned an invalid response")
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return sub2APILogin{}, errors.New("Sub2API monitor login returned malformed JSON")
	}
	payload := raw
	if len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		payload = envelope.Data
	}
	var login sub2APILogin
	if err = json.Unmarshal(payload, &login); err != nil || login.Requires2FA ||
		strings.TrimSpace(login.AccessToken) == "" || len(login.User) == 0 {
		return sub2APILogin{}, errors.New("Sub2API monitor account requires attention")
	}
	if login.ExpiresIn <= 0 {
		login.ExpiresIn = 3600
	}
	return login, nil
}

func (a *AgentRuntime) cachedSub2APIMonitorLogin(ctx context.Context, pageURL *url.URL) (sub2APILogin, error) {
	if strings.TrimSpace(a.opsCaptureLogin.AccessToken) != "" && time.Now().Add(time.Minute).Before(a.opsCaptureLoginExpiresAt) {
		return a.opsCaptureLogin, nil
	}
	login, err := a.sub2APIMonitorLogin(ctx, pageURL)
	if err != nil {
		return sub2APILogin{}, err
	}
	a.opsCaptureLogin = login
	a.opsCaptureLoginExpiresAt = time.Now().Add(time.Duration(login.ExpiresIn) * time.Second)
	return login, nil
}

func (a *AgentRuntime) captureOPSCardPNG(ctx context.Context, policy opsPolicy) ([]byte, error) {
	a.opsCaptureMu.Lock()
	defer a.opsCaptureMu.Unlock()

	pageURL, err := normalizedOPSCardPageURL(policy.CardPageURL)
	if err != nil {
		return nil, err
	}

	browserRaw := strings.TrimSpace(policy.CardBrowserURL)
	if browserRaw == "" {
		browserRaw = defaultOPSCardBrowserURL
	}
	browserURL, err := url.Parse(browserRaw)
	if err != nil || !validOPSCardBrowserURL(browserURL) {
		return nil, errors.New("Sub2API monitor browser URL is invalid")
	}
	timeout := time.Duration(policy.CardCaptureTimeoutSeconds) * time.Second
	if timeout < 10*time.Second || timeout > 90*time.Second {
		timeout = defaultOPSCardTimeout
	}
	captureCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	login, err := a.cachedSub2APIMonitorLogin(captureCtx, pageURL)
	if err != nil {
		return nil, err
	}
	if !json.Valid(login.User) {
		return nil, errors.New("Sub2API monitor account returned invalid user data")
	}
	storage, _ := json.Marshal(map[string]string{
		"auth_token":       login.AccessToken,
		"refresh_token":    login.RefreshToken,
		"token_expires_at": strconv.FormatInt(time.Now().Add(time.Duration(login.ExpiresIn)*time.Second).UnixMilli(), 10),
		"auth_user":        string(login.User),
		"sub2api_locale":   "zh",
	})
	origin, _ := json.Marshal(pageURL.Scheme + "://" + pageURL.Host)
	seedScript := `(values => {
		if (location.origin !== ` + string(origin) + `) return;
		for (const [key, value] of Object.entries(values)) localStorage.setItem(key, value);
	})(` + string(storage) + `)`
	suppressAnnouncementScript := `(() => {
		const suppress = () => {
			document.querySelectorAll('[data-testid="announcement-popup-dismiss"]').forEach((button) => {
				const overlay = button.closest('.fixed.inset-0');
				if (overlay instanceof HTMLElement) overlay.style.setProperty('display', 'none', 'important');
			});
			document.body.style.overflow = '';
		};
		suppress();
		if (!window.__erdaiAnnouncementObserver) {
			window.__erdaiAnnouncementObserver = new MutationObserver(suppress);
			window.__erdaiAnnouncementObserver.observe(document.documentElement, { childList: true, subtree: true });
		}
	})()`

	allocatorCtx, allocatorCancel := chromedp.NewRemoteAllocator(captureCtx, browserURL.String())
	defer allocatorCancel()
	browserCtx, browserCancel := chromedp.NewContext(allocatorCtx, chromedp.WithNewBrowserContext())
	defer browserCancel()
	var screenshot []byte
	if err = chromedp.Run(browserCtx,
		chromedp.EmulateViewport(1024, 768),
		chromedp.ActionFunc(func(ctx context.Context) error {
			return emulation.SetTimezoneOverride("Asia/Shanghai").Do(ctx)
		}),
		chromedp.ActionFunc(func(ctx context.Context) error {
			_, scriptErr := page.AddScriptToEvaluateOnNewDocument(seedScript).Do(ctx)
			return scriptErr
		}),
		chromedp.Navigate(pageURL.String()),
		chromedp.Evaluate(suppressAnnouncementScript, nil),
		chromedp.WaitVisible(`.monitor-card`, chromedp.ByQuery),
		chromedp.WaitNotPresent(`header .animate-spin`, chromedp.ByQuery),
		chromedp.Sleep(100*time.Millisecond),
		chromedp.Screenshot(`div.space-y-6.pb-12`, &screenshot, chromedp.ByQuery),
	); err != nil {
		return nil, fmt.Errorf("Sub2API monitor card capture failed: %w", err)
	}
	if extension, _ := imageFormat(screenshot); extension != ".png" || len(screenshot) > maxImageBytes {
		return nil, errors.New("Sub2API monitor card capture returned an invalid PNG")
	}
	return screenshot, nil
}

func (a *AgentRuntime) captureOPSCardImage(ctx context.Context, policy opsPolicy) (agentAttachment, error) {
	data, err := a.captureOPSCardPNG(ctx, policy)
	if err != nil {
		return agentAttachment{}, err
	}
	attachment, err := a.storeImage(data)
	if err != nil {
		return agentAttachment{}, fmt.Errorf("store Sub2API monitor card capture: %w", err)
	}
	attachment.Name = "channel-monitor-90m.png"
	return attachment, nil
}
