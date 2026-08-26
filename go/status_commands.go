package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	directCommandOPSAll          = "ops_all"
	directCommandOPSGroup        = "ops_group"
	directCommandRadar           = "radar"
	directCommandAffiliateBind   = "affiliate_bind"
	directCommandAffiliateLink   = "affiliate_link"
	directCommandAffiliatePoints = "affiliate_points"
)

var errCoreDirectCommandDisabled = errors.New("core direct command is disabled for this agent instance")

type opsPolicy struct {
	Enabled                   bool               `json:"enabled"`
	StatusURL                 string             `json:"statusUrl"`
	StatusTitle               string             `json:"statusTitle"`
	RequestTimeoutSeconds     int                `json:"requestTimeoutSeconds"`
	CardPageURL               string             `json:"cardPageUrl"`
	CardBrowserURL            string             `json:"cardBrowserUrl"`
	CardCaptureTimeoutSeconds int                `json:"cardCaptureTimeoutSeconds"`
	CommandAliases            []string           `json:"commandAliases"`
	TimelinePoints            int                `json:"timelinePoints"`
	EvaluationWindowMinutes   int                `json:"evaluationWindowMinutes"`
	EvaluationPollSeconds     int                `json:"evaluationPollSeconds"`
	GroupMultipliers          map[string]float64 `json:"groupMultipliers"`
	ShowMultiplierNote        bool               `json:"showMultiplierNote"`
	RadarEnabled              bool               `json:"radarEnabled"`
	RadarURL                  string             `json:"radarUrl"`
	RadarCommandAliases       []string           `json:"radarCommandAliases"`
	RadarMinimumSamples       int                `json:"radarMinimumSamples"`
	RadarFamilyOrder          []string           `json:"radarFamilyOrder"`
	RadarRecommendationOrder  []string           `json:"radarRecommendationOrder"`
	RadarRecommendations      map[string]string  `json:"radarRecommendations"`
}

type coreDirectCommand struct {
	Kind            string
	Group           string
	AffiliateCode   string
	Groups          []opsGroup
	Policy          opsPolicy
	AffiliatePolicy affiliatePolicy
}

func coreDirectCommandToolIDs(command coreDirectCommand) []string {
	switch command.Kind {
	case directCommandOPSAll, directCommandOPSGroup:
		return []string{"ops-status", "query_ops_status", "ops_status"}
	case directCommandRadar:
		return []string{"ops-status", "radar", "query_radar", "ops_status"}
	case directCommandAffiliateBind, directCommandAffiliateLink, directCommandAffiliatePoints:
		return []string{"affiliate"}
	default:
		return nil
	}
}

func (a *AgentRuntime) coreDirectCommandAllowed(personaID, instanceID string, command coreDirectCommand) (bool, error) {
	toolIDs := coreDirectCommandToolIDs(command)
	if len(toolIDs) == 0 {
		return false, nil
	}
	if command.Kind == directCommandAffiliateBind || command.Kind == directCommandAffiliateLink || command.Kind == directCommandAffiliatePoints {
		return command.AffiliatePolicy.Enabled, nil
	}
	profile, err := a.configStore.effectivePersonaRuntimeProfile(personaID, instanceID)
	if err != nil {
		return false, err
	}
	return personaRuntimeAllowsAnyTool(profile, toolIDs...), nil
}

type opsTimelinePoint struct {
	Status    string     `json:"status"`
	CheckedAt *time.Time `json:"checked_at"`
	LatencyMS *float64   `json:"latency_ms"`
	Requests  int64      `json:"requests"`
	ErrorRate *float64   `json:"error_rate"`
}

type opsGroup struct {
	Name             string             `json:"name"`
	GroupName        string             `json:"group_name"`
	RateMultiplier   *float64           `json:"rate_multiplier"`
	Multiplier       *float64           `json:"multiplier"`
	CurrentStatus    string             `json:"current_status"`
	Status           string             `json:"status"`
	StatusSource     string             `json:"status_source"`
	StatusReason     string             `json:"status_reason"`
	MonitorCheckedAt *time.Time         `json:"monitor_checked_at"`
	MonitorWindowMin int                `json:"monitor_window_minutes"`
	MonitorRequests  int64              `json:"monitor_requests"`
	MonitorErrorRate *float64           `json:"monitor_error_rate"`
	SuccessRate24H   *float64           `json:"success_rate_24h"`
	AverageLatencyMS *float64           `json:"average_latency_ms"`
	Samples24H       int64              `json:"samples_24h"`
	UpdatedAt        *time.Time         `json:"updated_at"`
	Timeline         []opsTimelinePoint `json:"timeline"`
	EvaluatedStatus  string             `json:"-"`
	EvaluationPoints int                `json:"-"`
}

type opsStatusSample struct {
	Status    string
	CheckedAt time.Time
}

type radarModel struct {
	ID              string   `json:"id"`
	Label           string   `json:"label"`
	Group           string   `json:"group"`
	Average         *float64 `json:"average"`
	Count           int      `json:"count"`
	Model           string   `json:"model"`
	Effort          string   `json:"effort"`
	IQ              *float64 `json:"iq"`
	AveragePriceUSD *float64 `json:"average_price_usd"`
	AverageMinutes  *float64 `json:"average_minutes"`
	Runs24H         int      `json:"runs_24h"`
}

type radarPayload struct {
	Models          []radarModel `json:"models"`
	Points          []radarModel `json:"points"`
	UpdatedAt       time.Time    `json:"updated_at"`
	SourceUpdatedAt time.Time    `json:"source_updated_at"`
	Runs24HTotal    int          `json:"runs_24h_total"`
	Window          string       `json:"window"`
}

func isSlashCommand(message string) bool {
	message = strings.TrimSpace(message)
	return strings.HasPrefix(message, "/") && !strings.ContainsAny(message, "\r\n") && len([]rune(message)) <= 120
}

func commandAlias(message string, aliases []string) bool {
	message = strings.TrimSpace(message)
	for _, alias := range aliases {
		if strings.EqualFold(message, strings.TrimSpace(alias)) {
			return true
		}
	}
	return false
}

func canonicalStatusName(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(value)), " "))
}

func (a *AgentRuntime) resolveCoreDirectCommand(ctx context.Context, message string) (coreDirectCommand, bool) {
	if !isSlashCommand(message) {
		return coreDirectCommand{}, false
	}
	var affiliate affiliatePolicy
	if err := a.integrationConfig(ctx, "affiliate_policy", &affiliate); err == nil && affiliate.Enabled {
		trimmed := strings.TrimSpace(message)
		if commandAlias(trimmed, affiliate.BindAliases) || strings.HasPrefix(strings.ToLower(trimmed), "/绑定 ") {
			code := ""
			parts := strings.Fields(trimmed)
			if len(parts) == 2 {
				code = parts[1]
			}
			return coreDirectCommand{Kind: directCommandAffiliateBind, AffiliateCode: code, AffiliatePolicy: affiliate}, true
		}
		if commandAlias(trimmed, affiliate.LinkAliases) {
			return coreDirectCommand{Kind: directCommandAffiliateLink, AffiliatePolicy: affiliate}, true
		}
		if commandAlias(trimmed, affiliate.PointsAliases) {
			return coreDirectCommand{Kind: directCommandAffiliatePoints, AffiliatePolicy: affiliate}, true
		}
	}
	var policy opsPolicy
	if err := a.integrationConfig(ctx, "ops_policy", &policy); err != nil {
		return coreDirectCommand{}, false
	}
	if policy.Enabled && commandAlias(message, policy.CommandAliases) {
		return coreDirectCommand{Kind: directCommandOPSAll, Policy: policy}, true
	}
	if policy.Enabled && policy.RadarEnabled && commandAlias(message, policy.RadarCommandAliases) {
		return coreDirectCommand{Kind: directCommandRadar, Policy: policy}, true
	}
	if !policy.Enabled {
		return coreDirectCommand{}, false
	}
	requested := canonicalStatusName(strings.TrimPrefix(strings.TrimSpace(message), "/"))
	if requested == "" {
		return coreDirectCommand{}, false
	}
	groups, err := a.fetchOPSGroups(ctx, policy)
	if err != nil {
		return coreDirectCommand{}, false
	}
	for _, group := range groups {
		if canonicalStatusName(group.Name) == requested {
			return coreDirectCommand{
				Kind: directCommandOPSGroup, Group: group.Name,
				Groups: groups, Policy: policy,
			}, true
		}
	}
	return coreDirectCommand{}, false
}

func (a *AgentRuntime) fetchOPSGroups(ctx context.Context, policy opsPolicy) ([]opsGroup, error) {
	if a.opsToken == "" {
		return nil, errors.New("OPS status is not configured")
	}
	statusURL, err := url.Parse(policy.StatusURL)
	if !validOPSStatusURL(statusURL) {
		return nil, errors.New("OPS status URL is invalid")
	}
	query := statusURL.Query()
	query.Set("token", a.opsToken)
	statusURL.RawQuery = query.Encode()
	timeout := policy.RequestTimeoutSeconds
	if timeout < 1 || timeout > 30 {
		timeout = 10
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, statusURL.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ErDai-Agent-OPS/"+erdaiRuntimeVersion)
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("OPS returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data []opsGroup `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(&payload); err != nil {
		return nil, err
	}
	for index := range payload.Data {
		group := &payload.Data[index]
		// The direct v2 OPS endpoint uses group_name/status; the bridge uses
		// name/current_status. Normalize both contracts before command formatting.
		if strings.TrimSpace(group.Name) == "" {
			group.Name = strings.TrimSpace(group.GroupName)
		}
		if strings.TrimSpace(group.CurrentStatus) == "" {
			group.CurrentStatus = strings.TrimSpace(group.Status)
		}
	}
	a.recordAndEvaluateOPSGroups(ctx, payload.Data, policy)
	return payload.Data, nil
}

func validOPSStatusURL(statusURL *url.URL) bool {
	if statusURL == nil || statusURL.Host == "" {
		return false
	}
	if statusURL.Scheme == "https" {
		return true
	}
	if statusURL.Scheme != "http" {
		return false
	}
	// Private HTTP is accepted for an operator-owned internal monitor. Public
	// HTTP is rejected so a policy typo cannot send the OPS token in cleartext.
	ip := net.ParseIP(statusURL.Hostname())
	if ip == nil {
		return false
	}
	if ip.IsPrivate() || ip.IsLoopback() {
		return true
	}
	if ipv4 := ip.To4(); ipv4 != nil && ipv4[0] == 100 && ipv4[1] >= 64 && ipv4[1] <= 127 {
		return true
	}
	return false
}

func (a *AgentRuntime) startOPSStatusWorker(ctx context.Context) {
	if a == nil || strings.TrimSpace(a.opsToken) == "" {
		return
	}
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		for {
			var policy opsPolicy
			if err := a.integrationConfig(ctx, "ops_policy", &policy); err != nil {
				return
			}
			interval := policy.EvaluationPollSeconds
			if interval < 15 || interval > 300 {
				interval = 60
			}
			timer := time.NewTimer(time.Duration(interval) * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				if policy.Enabled {
					_, _ = a.fetchOPSGroups(ctx, policy)
				}
			}
		}
	}()
}

func normalizeOPSStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "operational":
		return "operational"
	case "degraded":
		return "degraded"
	case "error", "failed", "unavailable":
		return "error"
	case "observing":
		return "observing"
	default:
		return "unknown"
	}
}

func evaluateOPSWindow(samples []opsStatusSample, previousStatus string) (string, int) {
	if len(samples) == 0 {
		return "unknown", 0
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].CheckedAt.Before(samples[j].CheckedAt) })
	hasOperational, hasDegraded, allErrors := false, false, true
	for _, sample := range samples {
		switch normalizeOPSStatus(sample.Status) {
		case "operational":
			hasOperational = true
			allErrors = false
		case "degraded":
			hasDegraded = true
			allErrors = false
		case "error":
		default:
			allErrors = false
		}
	}
	if hasOperational {
		return "operational", len(samples)
	}
	if hasDegraded {
		return "degraded", len(samples)
	}
	if allErrors && normalizeOPSStatus(previousStatus) == "error" {
		return "error", len(samples)
	}
	if allErrors {
		return "observing", len(samples)
	}
	return "unknown", len(samples)
}

func (a *AgentRuntime) recordAndEvaluateOPSGroups(ctx context.Context, groups []opsGroup, policy opsPolicy) {
	if a == nil || a.db == nil || len(groups) == 0 {
		return
	}
	now := time.Now().UTC()
	for index := range groups {
		group := &groups[index]
		if strings.EqualFold(strings.TrimSpace(group.StatusSource), "passive_monitor") {
			// Sub2API V2 already evaluates the current 10-minute window. Do not
			// re-evaluate its minute buckets with the legacy active-probe rules.
			group.EvaluatedStatus = normalizeOPSStatus(group.CurrentStatus)
			group.EvaluationPoints = len(group.Timeline)
			continue
		}
		for _, point := range group.Timeline {
			if point.CheckedAt == nil || normalizeOPSStatus(point.Status) == "unknown" {
				continue
			}
			_, _ = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_ops_status_samples
				(group_name, status, checked_at, source) VALUES (?, ?, ?, 'ops_timeline')`,
				group.Name, normalizeOPSStatus(point.Status), point.CheckedAt.UTC().Format(time.RFC3339Nano))
		}
		if group.UpdatedAt != nil && normalizeOPSStatus(group.CurrentStatus) != "unknown" {
			_, _ = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_ops_status_samples
				(group_name, status, checked_at, source) VALUES (?, ?, ?, 'ops_current')`,
				group.Name, normalizeOPSStatus(group.CurrentStatus), group.UpdatedAt.UTC().Format(time.RFC3339Nano))
		}
		reference := groupReferenceTime(*group)
		windowMinutes := policy.EvaluationWindowMinutes
		if windowMinutes < 1 || windowMinutes > 60 {
			windowMinutes = 5
		}
		cutoff := reference.Add(-time.Duration(windowMinutes) * time.Minute)
		rows, err := a.db.QueryContext(ctx, `SELECT status, checked_at FROM agent_ops_status_samples
			WHERE group_name = ? AND checked_at > ? AND checked_at <= ? ORDER BY checked_at`,
			group.Name, cutoff.Format(time.RFC3339Nano), reference.Format(time.RFC3339Nano))
		if err != nil {
			continue
		}
		samples := []opsStatusSample{}
		for rows.Next() {
			var sample opsStatusSample
			var checkedAt string
			if rows.Scan(&sample.Status, &checkedAt) == nil {
				if parsed, parseErr := time.Parse(time.RFC3339Nano, checkedAt); parseErr == nil {
					sample.CheckedAt = parsed
					samples = append(samples, sample)
				}
			}
		}
		rows.Close()
		var previousStatus string
		_ = a.db.QueryRowContext(ctx, `SELECT status FROM agent_ops_status_samples
			WHERE group_name = ? AND checked_at <= ? ORDER BY checked_at DESC LIMIT 1`,
			group.Name, cutoff.Format(time.RFC3339Nano)).Scan(&previousStatus)
		group.EvaluatedStatus, group.EvaluationPoints = evaluateOPSWindow(samples, previousStatus)
		if group.EvaluatedStatus == "unknown" {
			group.EvaluatedStatus = normalizeOPSStatus(group.CurrentStatus)
		}
	}
	_, _ = a.db.ExecContext(ctx, "DELETE FROM agent_ops_status_samples WHERE checked_at < ?", now.Add(-24*time.Hour).Format(time.RFC3339Nano))
}

func groupReferenceTime(group opsGroup) time.Time {
	var latest time.Time
	if group.UpdatedAt != nil {
		latest = group.UpdatedAt.UTC()
	}
	for _, point := range group.Timeline {
		if point.CheckedAt != nil && point.CheckedAt.After(latest) {
			latest = point.CheckedAt.UTC()
		}
	}
	if latest.IsZero() {
		latest = time.Now().UTC()
	}
	return latest
}

func (a *AgentRuntime) queryOPS(ctx context.Context, groupName string) (toolResult, error) {
	var policy opsPolicy
	if err := a.integrationConfig(ctx, "ops_policy", &policy); err != nil || !policy.Enabled {
		return toolResult{}, errors.New("OPS status is disabled")
	}
	groups, err := a.fetchOPSGroups(ctx, policy)
	if err != nil {
		return toolResult{}, err
	}
	text := ""
	if strings.TrimSpace(groupName) == "" {
		text = formatOPSAll(groups, policy)
	} else {
		for _, group := range groups {
			if canonicalStatusName(group.Name) == canonicalStatusName(groupName) {
				text = formatOPSGroup(group, policy)
				break
			}
		}
		if text == "" {
			return toolResult{}, errors.New("OPS group was not found")
		}
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": text})
	return toolResult{Content: string(encoded)}, nil
}

func currentOPSStatus(group opsGroup) string {
	if strings.EqualFold(strings.TrimSpace(group.StatusSource), "passive_monitor") {
		if status := latestOPSPassiveStatus(group.Timeline); status != "unknown" {
			return status
		}
		return normalizeOPSStatus(group.CurrentStatus)
	}
	if strings.TrimSpace(group.EvaluatedStatus) != "" {
		return normalizeOPSStatus(group.EvaluatedStatus)
	}
	if strings.TrimSpace(group.CurrentStatus) != "" {
		return normalizeOPSStatus(group.CurrentStatus)
	}
	if len(group.Timeline) > 0 {
		return normalizeOPSStatus(group.Timeline[0].Status)
	}
	return "unknown"
}

func opsMultiplier(group opsGroup, configured map[string]float64) (float64, bool) {
	if group.RateMultiplier != nil {
		return *group.RateMultiplier, true
	}
	if group.Multiplier != nil {
		return *group.Multiplier, true
	}
	value, ok := configured[group.Name]
	return value, ok
}

func formatMultiplier(value float64, ok bool) string {
	if !ok {
		return "未知"
	}
	return strconv.FormatFloat(value, 'f', -1, 64) + "x"
}

func latestOPSUpdate(groups []opsGroup) time.Time {
	var latest time.Time
	for _, group := range groups {
		candidate := group.UpdatedAt
		if candidate == nil {
			candidate = group.MonitorCheckedAt
		}
		if candidate == nil && len(group.Timeline) > 0 {
			candidate = group.Timeline[len(group.Timeline)-1].CheckedAt
		}
		if candidate != nil && candidate.After(latest) {
			latest = *candidate
		}
	}
	if latest.IsZero() {
		latest = time.Now()
	}
	return latest
}

func chinaTime(value time.Time) string {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	return value.In(zone).Format("01-02 15:04")
}

func formatOPSAll(groups []opsGroup, policy opsPolicy) string {
	groups = visibleOPSAllGroups(groups)
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	title := strings.TrimSpace(policy.StatusTitle)
	if title == "" {
		title = "渠道监控"
	}
	lines := []string{fmt.Sprintf("📡 %s  %s", title, chinaTime(latestOPSUpdate(groups))), "━━━━━━━━━━━━"}
	if len(groups) == 0 {
		lines = append(lines, "暂无已配置监控的分组")
	}
	for _, group := range groups {
		name := strings.Join(strings.Fields(group.Name), " ")
		if name == "" {
			continue
		}
		icon, status := formatOPSEvaluation(currentOPSStatus(group))
		multiplier, ok := opsMultiplier(group, policy.GroupMultipliers)
		line := fmt.Sprintf("%s %s  %s", icon, name, status)
		if availability := formatOPSAvailability(group); availability != "" {
			line += "  " + availability
		}
		line += "  " + formatMultiplier(multiplier, ok)
		if timeline := formatOPSTimeline(group.Timeline); timeline != "" {
			line += "  " + timeline
		}
		lines = append(lines, line)
	}
	lines = append(lines, "━━━━━━━━━━━━")
	if policy.ShowMultiplierNote {
		lines = append(lines, "💰 倍率越低越便宜")
	}
	lines = append(lines, "🔎 /渠道查看，单组发 /名称，评分发 /雷达")
	return strings.Join(lines, "\n")
}

func formatOPSAvailability(group opsGroup) string {
	errorRate := group.MonitorErrorRate
	if strings.EqualFold(strings.TrimSpace(group.StatusSource), "passive_monitor") && len(group.Timeline) > 0 {
		latest := group.Timeline[len(group.Timeline)-1]
		if latest.ErrorRate != nil {
			errorRate = latest.ErrorRate
		}
	}
	if errorRate == nil {
		return ""
	}
	rate := *errorRate
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return fmt.Sprintf("可用率 %.1f%%", (1-rate)*100)
}

func latestOPSPassiveStatus(points []opsTimelinePoint) string {
	if len(points) == 0 {
		return "unknown"
	}
	return normalizeOPSStatus(points[len(points)-1].Status)
}

func formatOPSTimeline(points []opsTimelinePoint) string {
	if len(points) == 0 {
		return ""
	}
	const maxPoints = 3
	if len(points) > maxPoints {
		points = points[len(points)-maxPoints:]
	}
	icons := make([]string, 0, len(points))
	for _, point := range points {
		switch normalizeOPSStatus(point.Status) {
		case "operational":
			icons = append(icons, "🟢")
		case "degraded":
			icons = append(icons, "🟡")
		case "error":
			icons = append(icons, "🔴")
		case "observing":
			icons = append(icons, "⚪")
		default:
			icons = append(icons, "⚫")
		}
	}
	return strings.Join(icons, "")
}

func visibleOPSAllGroups(groups []opsGroup) []opsGroup {
	visible := make([]opsGroup, 0, len(groups))
	for _, group := range groups {
		if strings.EqualFold(strings.TrimSpace(group.StatusReason), "no_probe_configured") {
			continue
		}
		visible = append(visible, group)
	}
	return visible
}

func formatOPSGroup(group opsGroup, policy opsPolicy) string {
	name := strings.Join(strings.Fields(group.Name), " ")
	icon, status := formatOPSEvaluation(currentOPSStatus(group))
	windowMinutes := policy.EvaluationWindowMinutes
	if windowMinutes < 1 || windowMinutes > 60 {
		windowMinutes = 5
	}
	success := "暂无数据"
	if group.SuccessRate24H != nil {
		success = strconv.FormatFloat(*group.SuccessRate24H, 'f', 1, 64) + "%"
	}
	latency := "暂无数据"
	if group.AverageLatencyMS != nil {
		if *group.AverageLatencyMS >= 1000 {
			latency = strconv.FormatFloat(*group.AverageLatencyMS/1000, 'f', 1, 64) + "s"
		} else {
			latency = strconv.FormatFloat(*group.AverageLatencyMS, 'f', 0, 64) + "ms"
		}
	}
	points := policy.TimelinePoints
	if points < 1 || points > 3 {
		points = 3
	}
	visible := group.Timeline
	if len(visible) > points {
		visible = visible[:points]
	}
	trend := make([]string, points)
	for index := range trend {
		trend[index] = "⬛"
	}
	for index, point := range visible {
		trend[points-1-index] = formatOPSTimeline([]opsTimelinePoint{point})
	}
	multiplier, ok := opsMultiplier(group, policy.GroupMultipliers)
	evaluationLabel := fmt.Sprintf("%d分钟评估", windowMinutes)
	windowLabel := fmt.Sprintf("最近%d分钟·单格=5分钟", points*5)
	if strings.EqualFold(strings.TrimSpace(group.StatusSource), "passive_monitor") {
		evaluationLabel = "近15分钟状态"
		windowLabel = "最近15分钟·单格=5分钟"
	}
	availability := formatOPSAvailability(group)
	if availability == "" {
		availability = "可用率：暂无数据"
	}
	return strings.Join([]string{
		fmt.Sprintf("📊 %s 状态", name), "━━━━━━━━━━━━",
		fmt.Sprintf("%s %s：%s", icon, evaluationLabel, status),
		fmt.Sprintf("📈 近15分钟%s", availability),
		fmt.Sprintf("📈 近24h成功率：%s", success),
		fmt.Sprintf("⏱ 平均延迟：%s", latency),
		"📉 近况：", strings.Join(trend, ""),
		fmt.Sprintf("🟢可用 🟡波动 🔴故障 ⚪观察 ⬛无数据 · %s", windowLabel),
		fmt.Sprintf("💰 倍率：%s", formatMultiplier(multiplier, ok)),
	}, "\n")
}

func formatOPSEvaluation(status string) (string, string) {
	switch normalizeOPSStatus(status) {
	case "operational":
		return "🟢", "可用"
	case "degraded":
		return "🟡", "波动"
	case "observing":
		return "⚪", "观察中"
	default:
		return "🔴", "暂不可用"
	}
}

func (a *AgentRuntime) queryRadar(ctx context.Context, policy opsPolicy) (toolResult, error) {
	if !policy.Enabled || !policy.RadarEnabled {
		return toolResult{}, errors.New("model radar is disabled")
	}
	radarURL, err := url.Parse(policy.RadarURL)
	if err != nil || radarURL.Scheme != "https" || radarURL.Host == "" {
		return toolResult{}, errors.New("model radar URL is invalid")
	}
	timeout := policy.RequestTimeoutSeconds
	if timeout < 1 || timeout > 30 {
		timeout = 10
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, radarURL.String(), nil)
	if err != nil {
		return toolResult{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ErDai-Agent-Radar/"+erdaiRuntimeVersion)
	response, err := a.client.Do(request)
	if err != nil {
		return toolResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return toolResult{}, fmt.Errorf("model radar returned HTTP %d", response.StatusCode)
	}
	var payload radarPayload
	if err := json.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(&payload); err != nil {
		return toolResult{}, err
	}
	text, err := formatRadar(payload, policy, radarURL.Host)
	if err != nil {
		return toolResult{}, err
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": text})
	return toolResult{Content: string(encoded)}, nil
}

func bestRadarModels(payload radarPayload, policy opsPolicy) map[string]radarModel {
	minimum := policy.RadarMinimumSamples
	if minimum < 1 {
		minimum = 5
	}
	allowed := make(map[string]bool, len(policy.RadarFamilyOrder))
	for _, family := range policy.RadarFamilyOrder {
		allowed[canonicalStatusName(family)] = true
	}
	best := make(map[string]radarModel)
	for _, model := range normalizedRadarModels(payload) {
		family := canonicalStatusName(model.Group)
		if model.Average == nil || model.Count < minimum || !allowed[family] {
			continue
		}
		current, exists := best[family]
		if !exists || *model.Average > *current.Average || (*model.Average == *current.Average && model.Count > current.Count) {
			best[family] = model
		}
	}
	return best
}

func normalizedRadarModels(payload radarPayload) []radarModel {
	if len(payload.Points) == 0 {
		return payload.Models
	}
	models := make([]radarModel, 0, len(payload.Points))
	for _, point := range payload.Points {
		family := radarFamilyName(point.Model)
		point.Group = family
		point.Label = strings.TrimSpace(family + " " + point.Effort)
		point.Average = point.IQ
		point.Count = point.Runs24H
		models = append(models, point)
	}
	return models
}

func radarFamilyName(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "gpt-5.6-sol":
		return "GPT-5.6 Sol"
	case "gpt-5.6-terra":
		return "GPT-5.6 Terra"
	case "gpt-5.6-luna":
		return "GPT-5.6 Luna"
	case "gpt-5.5":
		return "GPT-5.5"
	case "deepseek-v4-flash":
		return "DeepSeek V4 Flash"
	default:
		return strings.TrimSpace(model)
	}
}

func formatScore(value float64) string {
	return strings.TrimSuffix(strconv.FormatFloat(value, 'f', 1, 64), ".0")
}

func radarModelName(model radarModel) (string, string) {
	family := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(model.Group), " ", "-"))
	effort := strings.TrimSpace(strings.TrimPrefix(model.Label, model.Group))
	return family, effort
}

func formatRadar(payload radarPayload, policy opsPolicy, source string) (string, error) {
	best := bestRadarModels(payload, policy)
	models := make([]radarModel, 0, len(best))
	for _, family := range policy.RadarFamilyOrder {
		if model, ok := best[canonicalStatusName(family)]; ok {
			models = append(models, model)
		}
	}
	if len(models) == 0 {
		return "", errors.New("model radar has no eligible samples")
	}
	sort.SliceStable(models, func(i, j int) bool {
		if *models[i].Average == *models[j].Average {
			return models[i].Count > models[j].Count
		}
		return *models[i].Average > *models[j].Average
	})
	medals := []string{"🥇", "🥈", "🥉"}
	if len(payload.Points) > 0 {
		lines := []string{"📡 Codex 雷达 · 智力效率", "━━━━━━━━━━━━"}
		for index, model := range models {
			medal := "·"
			if index < len(medals) {
				medal = medals[index]
			}
			line := fmt.Sprintf("%s %s  IQ %s", medal, model.Label, formatScore(*model.Average))
			details := make([]string, 0, 3)
			if model.AveragePriceUSD != nil {
				details = append(details, "$"+strconv.FormatFloat(*model.AveragePriceUSD, 'f', 2, 64))
			}
			if model.AverageMinutes != nil {
				details = append(details, formatScore(*model.AverageMinutes)+"分钟")
			}
			if model.Count > 0 {
				details = append(details, strconv.Itoa(model.Count)+"次")
			}
			if len(details) > 0 {
				line += " · " + strings.Join(details, " · ")
			}
			lines = append(lines, line)
		}
		updated := payload.SourceUpdatedAt
		if updated.IsZero() {
			updated = payload.UpdatedAt
		}
		if updated.IsZero() {
			updated = time.Now()
		}
		footer := "📅 " + chinaTime(updated)
		if payload.Runs24HTotal > 0 {
			footer = fmt.Sprintf("📊 近24小时 %d 次 · %s", payload.Runs24HTotal, chinaTime(updated))
		}
		lines = append(lines, "━━━━━━━━━━━━", footer, "数据来源：CodexRadar "+source)
		return strings.Join(lines, "\n"), nil
	}
	lines := []string{"📡 Codex 模型雷达 · 社区评分", "━━━━━━━━━━━━"}
	for index, model := range models {
		medal := "·"
		if index < len(medals) {
			medal = medals[index]
		}
		lines = append(lines, fmt.Sprintf("%s %s  %s分 · %d样本", medal, model.Label, formatScore(*model.Average), model.Count))
	}
	lines = append(lines, "━━━━━━━━━━━━", "🎯 按任务推荐（社区评分推导）")
	for _, task := range policy.RadarRecommendationOrder {
		family := policy.RadarRecommendations[task]
		model, ok := best[canonicalStatusName(family)]
		if !ok {
			continue
		}
		name, effort := radarModelName(model)
		lines = append(lines, fmt.Sprintf("· %s：%s · 推理档 %s  %s分", task, name, effort, formatScore(*model.Average)))
	}
	minimum := policy.RadarMinimumSamples
	if minimum < 1 {
		minimum = 5
	}
	updated := payload.UpdatedAt
	if updated.IsZero() {
		updated = time.Now()
	}
	lines = append(lines, "━━━━━━━━━━━━",
		fmt.Sprintf("📅 数据更新于 %s · 近24h滚动 · 样本≥%d", chinaTime(updated), minimum),
		"数据来源：Codex 雷达 "+source)
	return strings.Join(lines, "\n"), nil
}
