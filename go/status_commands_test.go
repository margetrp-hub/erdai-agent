package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func floatPointer(value float64) *float64 { return &value }

func timePointer(value time.Time) *time.Time { return &value }

func TestAffiliatePointsQueryAliasRoutesDirectly(t *testing.T) {
	runtime := newDormantRuntime(t)
	defer runtime.Close()
	command, ok := runtime.resolveCoreDirectCommand(context.Background(), "/积分查询")
	if !ok || command.Kind != directCommandAffiliatePoints {
		t.Fatalf("/积分查询 route = %#v, %v", command, ok)
	}
}

func TestOPSFormatsAllGroupsAndOneDetailedGroup(t *testing.T) {
	updated := time.Date(2026, 8, 3, 6, 21, 0, 0, time.UTC)
	groups := []opsGroup{
		{
			Name: "Codex_GPT", RateMultiplier: floatPointer(0.15), CurrentStatus: "operational",
			SuccessRate24H: floatPointer(100), AverageLatencyMS: floatPointer(10200),
			UpdatedAt: timePointer(updated), Timeline: []opsTimelinePoint{
				{Status: "operational"}, {Status: "degraded"}, {Status: "error"},
			},
		},
		{Name: "default", RateMultiplier: floatPointer(0), CurrentStatus: "error", UpdatedAt: timePointer(updated)},
	}
	policy := opsPolicy{
		StatusTitle: "分组检测", TimelinePoints: 5,
		EvaluationWindowMinutes: 5,
		ShowMultiplierNote:      true, GroupMultipliers: map[string]float64{},
	}

	all := formatOPSAll(groups, policy)
	for _, expected := range []string{
		"📡 分组检测  08-03 14:21",
		"🟢 Codex_GPT  可用  0.15x",
		"🔴 default  暂不可用  0x",
		"倍率越低越便宜",
		"/渠道查看，单组发 /名称，评分发 /雷达",
	} {
		if !strings.Contains(all, expected) {
			t.Fatalf("all-groups output missing %q:\n%s", expected, all)
		}
	}

	detail := formatOPSGroup(groups[0], policy)
	for _, expected := range []string{
		"📊 Codex_GPT 状态", "5分钟评估：可用", "近24h成功率：100.0%",
		"平均延迟：10.2s", "🔴🟡🟢", "最近15分钟·单格=5分钟", "倍率：0.15x",
	} {
		if !strings.Contains(detail, expected) {
			t.Fatalf("group output missing %q:\n%s", expected, detail)
		}
	}
}

func TestOPSStatusIconsDistinguishEvaluationStates(t *testing.T) {
	tests := []struct {
		status string
		icon   string
		label  string
	}{
		{status: "operational", icon: "🟢", label: "可用"},
		{status: "degraded", icon: "🟡", label: "波动"},
		{status: "observing", icon: "⚪", label: "观察中"},
		{status: "error", icon: "🔴", label: "暂不可用"},
		{status: "failed", icon: "🔴", label: "暂不可用"},
		{status: "unavailable", icon: "🔴", label: "暂不可用"},
	}
	for _, tt := range tests {
		icon, label := formatOPSEvaluation(tt.status)
		if icon != tt.icon || label != tt.label {
			t.Fatalf("formatOPSEvaluation(%q) = %q, %q; want %q, %q", tt.status, icon, label, tt.icon, tt.label)
		}
	}
}

func TestFormatOPSAllOmitsUnmonitoredGroups(t *testing.T) {
	rate := 0.08
	text := formatOPSAll([]opsGroup{
		{Name: "No Probe", RateMultiplier: &rate, CurrentStatus: "observing", StatusSource: "account_capacity", StatusReason: "no_probe_configured"},
		{Name: "Stale Probe", RateMultiplier: &rate, CurrentStatus: "observing", StatusSource: "active_probe", StatusReason: "probe_stale"},
		{Name: "Healthy", RateMultiplier: &rate, CurrentStatus: "operational", StatusSource: "account_capacity", StatusReason: "available_accounts"},
	}, opsPolicy{StatusTitle: "分组检测", ShowMultiplierNote: true})
	if strings.Contains(text, "No Probe") {
		t.Fatalf("unmonitored group leaked into /渠道 output: %s", text)
	}
	for _, want := range []string{"Stale Probe", "Healthy"} {
		if !strings.Contains(text, want) {
			t.Fatalf("configured group %q missing from /渠道 output: %s", want, text)
		}
	}
}

func TestFormatOPSAllUsesPassiveTenMinuteTimeline(t *testing.T) {
	rate := 0.08
	firstRate := 0.70
	secondRate := 0.02
	latestRate := 0.03
	text := formatOPSAll([]opsGroup{
		{
			Name: "ChatGPT 标准", RateMultiplier: &rate, CurrentStatus: "error",
			StatusSource: "passive_monitor", StatusReason: "passive_error_warning",
			MonitorErrorRate: floatPointer(0.602),
			Timeline: []opsTimelinePoint{
				{Status: "degraded", Requests: 50, ErrorRate: &firstRate},
				{Status: "operational", Requests: 100, ErrorRate: &secondRate},
				{Status: "operational", Requests: 100, ErrorRate: &latestRate},
			},
		},
	}, opsPolicy{StatusTitle: "渠道监控", ShowMultiplierNote: true})
	for _, expected := range []string{
		"📡 渠道监控",
		"🟢 ChatGPT 标准  可用  可用率 97.0%  0.08x  🟡🟢🟢",
		"倍率越低越便宜",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("passive output missing %q:\n%s", expected, text)
		}
	}
}

func TestLatestPassiveWindowFallsBackToAggregateWithoutTimeline(t *testing.T) {
	rate := 0.08
	group := opsGroup{
		Name: "ChatGPT 标准", RateMultiplier: &rate, CurrentStatus: "degraded",
		StatusSource: "passive_monitor", MonitorErrorRate: floatPointer(0.091),
	}
	if got := currentOPSStatus(group); got != "degraded" {
		t.Fatalf("status fallback = %q", got)
	}
	if got := formatOPSAvailability(group); got != "可用率 90.9%" {
		t.Fatalf("availability fallback = %q", got)
	}
}

func TestOPSFiveMinuteWindowDoesNotFailOnOneSample(t *testing.T) {
	now := time.Date(2026, 8, 7, 1, 42, 0, 0, time.UTC)
	status, count := evaluateOPSWindow([]opsStatusSample{
		{Status: "operational", CheckedAt: now.Add(-2 * time.Minute)},
		{Status: "error", CheckedAt: now},
	}, "operational")
	if status != "operational" || count != 2 {
		t.Fatalf("transient evaluation = %q, %d", status, count)
	}
}

func TestOPSFiveMinuteWindowRequiresContinuousFailure(t *testing.T) {
	now := time.Date(2026, 8, 7, 1, 42, 0, 0, time.UTC)
	samples := []opsStatusSample{
		{Status: "error", CheckedAt: now.Add(-4 * time.Minute)},
		{Status: "error", CheckedAt: now},
	}
	if status, _ := evaluateOPSWindow(samples, "operational"); status != "observing" {
		t.Fatalf("short failure = %q", status)
	}
	if status, _ := evaluateOPSWindow(samples, "error"); status != "error" {
		t.Fatalf("continuous failure = %q", status)
	}
}

func TestRadarFormattingUsesEligibleRollingSamples(t *testing.T) {
	updated := time.Date(2026, 8, 3, 6, 48, 0, 0, time.UTC)
	payload := radarPayload{UpdatedAt: updated, Window: "rolling_24h", Models: []radarModel{
		{Label: "GPT-5.6 Sol medium", Group: "GPT-5.6 Sol", Average: floatPointer(8.8), Count: 122},
		{Label: "GPT-5.6 Sol ultra", Group: "GPT-5.6 Sol", Average: floatPointer(9.9), Count: 2},
		{Label: "GPT-5.6 Terra max", Group: "GPT-5.6 Terra", Average: floatPointer(8.5), Count: 19},
		{Label: "GPT-5.6 Luna max", Group: "GPT-5.6 Luna", Average: floatPointer(8), Count: 46},
		{Label: "DeepSeek V4 Flash max", Group: "DeepSeek V4 Flash", Average: floatPointer(9.5), Count: 200},
	}}
	policy := opsPolicy{
		RadarMinimumSamples:      5,
		RadarFamilyOrder:         []string{"GPT-5.6 Sol", "GPT-5.6 Terra", "GPT-5.6 Luna"},
		RadarRecommendationOrder: []string{"复杂任务", "日常开发", "轻量任务"},
		RadarRecommendations: map[string]string{
			"复杂任务": "GPT-5.6 Terra", "日常开发": "GPT-5.6 Sol", "轻量任务": "GPT-5.6 Luna",
		},
	}

	text, err := formatRadar(payload, policy, "codex-radar.roixw.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"🥇 GPT-5.6 Sol medium  8.8分 · 122样本",
		"🥈 GPT-5.6 Terra max  8.5分 · 19样本",
		"🥉 GPT-5.6 Luna max  8分 · 46样本",
		"复杂任务：gpt-5.6-terra · 推理档 max  8.5分",
		"数据更新于 08-03 14:48 · 近24h滚动 · 样本≥5",
		"数据来源：Codex 雷达 codex-radar.roixw.com",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("radar output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "Sol ultra") || strings.Contains(text, "DeepSeek") {
		t.Fatalf("ineligible radar model leaked into output:\n%s", text)
	}
}

func TestRadarFormattingUsesCodexRadarIntelligenceMetrics(t *testing.T) {
	updated := time.Date(2026, 8, 9, 4, 32, 0, 0, time.UTC)
	payload := radarPayload{SourceUpdatedAt: updated, Runs24HTotal: 1246, Points: []radarModel{
		{Model: "gpt-5.6-sol", Effort: "xhigh", IQ: floatPointer(106.4), AveragePriceUSD: floatPointer(6.32), AverageMinutes: floatPointer(25), Runs24H: 39},
		{Model: "gpt-5.6-sol", Effort: "low", IQ: floatPointer(77.1), AveragePriceUSD: floatPointer(2), AverageMinutes: floatPointer(11), Runs24H: 44},
		{Model: "gpt-5.6-terra", Effort: "max", IQ: floatPointer(94.6), AveragePriceUSD: floatPointer(3.84), AverageMinutes: floatPointer(31), Runs24H: 47},
		{Model: "gpt-5.6-luna", Effort: "max", IQ: floatPointer(92.9), AveragePriceUSD: floatPointer(0.47), AverageMinutes: floatPointer(33), Runs24H: 70},
		{Model: "gpt-5.5", Effort: "xhigh", IQ: floatPointer(90.4), AveragePriceUSD: floatPointer(5.75), AverageMinutes: floatPointer(23), Runs24H: 64},
		{Model: "deepseek-v4-flash", Effort: "max", IQ: floatPointer(79.3), AveragePriceUSD: floatPointer(0.10), AverageMinutes: floatPointer(28), Runs24H: 146},
	}}
	policy := opsPolicy{RadarMinimumSamples: 5, RadarFamilyOrder: []string{
		"GPT-5.6 Sol", "GPT-5.6 Terra", "GPT-5.6 Luna", "GPT-5.5", "DeepSeek V4 Flash",
	}}

	text, err := formatRadar(payload, policy, "codexradar.com")
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		"📡 Codex 雷达 · 智力效率",
		"🥇 GPT-5.6 Sol xhigh  IQ 106.4 · $6.32 · 25分钟 · 39次",
		"GPT-5.6 Terra max  IQ 94.6 · $3.84 · 31分钟 · 47次",
		"GPT-5.6 Luna max  IQ 92.9 · $0.47 · 33分钟 · 70次",
		"近24小时 1246 次 · 08-09 12:32",
		"数据来源：CodexRadar codexradar.com",
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("metrics output missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, "Sol low") || strings.Contains(text, "按任务推荐") {
		t.Fatalf("metrics output contains an inferior effort or inferred recommendation:\n%s", text)
	}
}

func TestSlashCommandCandidateIsBounded(t *testing.T) {
	for _, value := range []string{"/渠道", "/Codex_GPT", "/雷达"} {
		if !isSlashCommand(value) {
			t.Fatalf("expected slash command: %q", value)
		}
	}
	for _, value := range []string{"渠道", "/渠道\n/雷达", "/" + strings.Repeat("a", 121)} {
		if isSlashCommand(value) {
			t.Fatalf("unexpected slash command: %q", value)
		}
	}
}

func TestOPSStatusSkillIsBoundToDoubao(t *testing.T) {
	store, err := openCoreConfigStore(filepath.Join(t.TempDir(), "core.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var personas string
	if err = store.db.QueryRow("SELECT persona_ids_json FROM skills WHERE id = 'ops-status'").Scan(&personas); err != nil {
		t.Fatal(err)
	}
	if personas != `["doubao"]` {
		t.Fatalf("OPS persona binding = %s", personas)
	}
}
