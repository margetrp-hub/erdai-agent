package main

import (
	"context"
	"database/sql"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type affiliateRoundTripper func(*http.Request) (*http.Response, error)

func (f affiliateRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestNormalizeAffiliateCode(t *testing.T) {
	for input, want := range map[string]string{
		" abcd ": "ABCD",
		"a1_b-2": "A1_B-2",
	} {
		got, ok := normalizeAffiliateCode(input)
		if !ok || got != want {
			t.Fatalf("normalizeAffiliateCode(%q) = %q/%v, want %q/true", input, got, ok, want)
		}
	}
	for _, input := range []string{"abc", "has space", "bad?code", ""} {
		if got, ok := normalizeAffiliateCode(input); ok {
			t.Fatalf("normalizeAffiliateCode(%q) = %q/true, want invalid", input, got)
		}
	}
}

func TestAffiliateRegisterLinkPreservesQuery(t *testing.T) {
	got, err := affiliateRegisterLink("https://ohlaoo.com/register?from=qq", "ABCD")
	if err != nil {
		t.Fatal(err)
	}
	if got != "https://ohlaoo.com/register?aff=ABCD&from=qq" {
		t.Fatalf("affiliateRegisterLink() = %q", got)
	}
}

func TestQQAffiliateTransport(t *testing.T) {
	for _, transport := range []string{"qq_official", "qq_official_webhook", "aiocqhttp", "onebot"} {
		if !qqAffiliateTransport(transport) {
			t.Fatalf("%q should be a QQ transport", transport)
		}
	}
	if qqAffiliateTransport("telegram") {
		t.Fatal("telegram must not be accepted as QQ")
	}
}

func TestValidOPSStatusURL(t *testing.T) {
	for _, value := range []string{
		"https://ohlaoo.com/ops-bot/group-status",
		"http://100.64.0.1:18089/api/ops/group-status",
		"http://192.168.0.10/ops-bot/group-status",
	} {
		parsed, err := url.Parse(value)
		if err != nil || !validOPSStatusURL(parsed) {
			t.Fatalf("validOPSStatusURL(%q) = false", value)
		}
	}
	for _, value := range []string{"http://ohlaoo.com/ops-bot/group-status", "ftp://192.0.2.1/status"} {
		parsed, err := url.Parse(value)
		if err != nil || validOPSStatusURL(parsed) {
			t.Fatalf("validOPSStatusURL(%q) = true", value)
		}
	}
}

func TestNormalizeDirectOPSGroupPayload(t *testing.T) {
	groups := []opsGroup{{GroupName: "ChatGPT 标准", Status: "operational"}}
	for index := range groups {
		group := &groups[index]
		if group.Name == "" {
			group.Name = group.GroupName
		}
		if group.CurrentStatus == "" {
			group.CurrentStatus = group.Status
		}
	}
	if groups[0].Name != "ChatGPT 标准" || groups[0].CurrentStatus != "operational" {
		t.Fatalf("normalized direct group = %+v", groups[0])
	}
}

func TestAffiliateCommandFlow(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE agent_affiliate_bindings (
		transport TEXT NOT NULL, transport_instance TEXT NOT NULL, sender_ref TEXT NOT NULL,
		affiliate_code TEXT NOT NULL, bound_at TEXT NOT NULL, updated_at TEXT NOT NULL,
		PRIMARY KEY (transport, transport_instance, sender_ref))`); err != nil {
		t.Fatal(err)
	}
	runtime := &AgentRuntime{
		db:       db,
		opsToken: "test-token",
		client: &http.Client{Transport: affiliateRoundTripper(func(request *http.Request) (*http.Response, error) {
			if request.URL.Query().Get("code") != "ABCD" || request.URL.Query().Get("token") != "test-token" {
				t.Fatalf("unexpected summary request: %s", request.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"data":{"aff_code":"ABCD","invited_count":4,"paid_invitee_count":2,"rebate_total":3.5}}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}
	policy := affiliatePolicy{
		Enabled: true, SummaryURL: "https://affiliate.test/summary",
		RegisterBaseURL: "https://ohlaoo.com/register", PointsPerPaidInvitee: 100,
	}
	run := runRecord{Transport: "aiocqhttp", TransportInstance: "qq-main", SenderRef: "123456"}

	bind, err := runtime.handleAffiliateCommand(context.Background(), run, coreDirectCommand{
		Kind: directCommandAffiliateBind, AffiliateCode: "abcd", AffiliatePolicy: policy,
	})
	if err != nil || !strings.Contains(bind.Text, "绑定成功：ABCD") {
		t.Fatalf("bind = %q, %v", bind.Text, err)
	}
	repeat, err := runtime.handleAffiliateCommand(context.Background(), run, coreDirectCommand{
		Kind: directCommandAffiliateBind, AffiliateCode: "ABCD", AffiliatePolicy: policy,
	})
	if err != nil || !strings.Contains(repeat.Text, "已绑定邀请码 ABCD") {
		t.Fatalf("repeat bind = %q, %v", repeat.Text, err)
	}
	link, err := runtime.handleAffiliateCommand(context.Background(), run, coreDirectCommand{
		Kind: directCommandAffiliateLink, AffiliatePolicy: policy,
	})
	if err != nil || !strings.Contains(link.Text, "https://ohlaoo.com/register?aff=ABCD") {
		t.Fatalf("link = %q, %v", link.Text, err)
	}
	points, err := runtime.handleAffiliateCommand(context.Background(), run, coreDirectCommand{
		Kind: directCommandAffiliatePoints, AffiliatePolicy: policy,
	})
	if err != nil || !strings.Contains(points.Text, "已邀请：4 人") ||
		!strings.Contains(points.Text, "已充值：2 人") || !strings.Contains(points.Text, "当前积分：200 分") {
		t.Fatalf("points = %q, %v", points.Text, err)
	}
}
