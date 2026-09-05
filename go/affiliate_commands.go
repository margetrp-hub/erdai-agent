package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type affiliatePolicy struct {
	Enabled               bool     `json:"enabled"`
	SummaryURL            string   `json:"summaryUrl"`
	RegisterBaseURL       string   `json:"registerBaseUrl"`
	RequestTimeoutSeconds int      `json:"requestTimeoutSeconds"`
	PointsPerPaidInvitee  int64    `json:"pointsPerPaidInvitee"`
	CheckInPoints         int64    `json:"checkInPoints"`
	BindAliases           []string `json:"bindAliases"`
	LinkAliases           []string `json:"linkAliases"`
	PointsAliases         []string `json:"pointsAliases"`
	CheckInAliases        []string `json:"checkInAliases"`
	RedeemAliases         []string `json:"redeemAliases"`
	LotteryURL            string   `json:"lotteryUrl"`
	LotteryAliases        []string `json:"lotteryAliases"`
}

type affiliateSummary struct {
	Code             string  `json:"aff_code"`
	InvitedCount     int64   `json:"invited_count"`
	PaidInviteeCount int64   `json:"paid_invitee_count"`
	RebateTotal      float64 `json:"rebate_total"`
}

func qqAffiliateTransport(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "qq_official", "qq_official_webhook", "aiocqhttp", "onebot":
		return true
	default:
		return false
	}
}

func normalizeAffiliateCode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 4 || len(value) > 32 {
		return "", false
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') &&
			(char < '0' || char > '9') && char != '_' && char != '-' {
			return "", false
		}
	}
	return strings.ToUpper(value), true
}

func (a *AgentRuntime) fetchAffiliateSummary(ctx context.Context, code string, policy affiliatePolicy) (affiliateSummary, error) {
	if strings.TrimSpace(a.opsToken) == "" {
		return affiliateSummary{}, errors.New("affiliate credential is not configured")
	}
	endpoint, err := url.Parse(strings.TrimSpace(policy.SummaryURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return affiliateSummary{}, errors.New("affiliate summary URL is invalid")
	}
	query := endpoint.Query()
	query.Set("code", code)
	query.Set("token", a.opsToken)
	endpoint.RawQuery = query.Encode()
	timeout := policy.RequestTimeoutSeconds
	if timeout < 1 || timeout > 30 {
		timeout = 6
	}
	requestCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return affiliateSummary{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Ops-Status-Token", a.opsToken)
	request.Header.Set("User-Agent", "ErDai-Agent-Affiliate/"+erdaiRuntimeVersion)
	response, err := a.client.Do(request)
	if err != nil {
		return affiliateSummary{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound {
		return affiliateSummary{}, sql.ErrNoRows
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return affiliateSummary{}, fmt.Errorf("affiliate summary returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data affiliateSummary `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(&payload); err != nil {
		return affiliateSummary{}, err
	}
	if canonical, ok := normalizeAffiliateCode(payload.Data.Code); !ok {
		return affiliateSummary{}, errors.New("affiliate summary returned invalid data")
	} else {
		payload.Data.Code = canonical
	}
	if !strings.EqualFold(payload.Data.Code, code) || payload.Data.PaidInviteeCount < 0 || payload.Data.InvitedCount < 0 {
		return affiliateSummary{}, errors.New("affiliate summary does not match the requested account")
	}
	return payload.Data, nil
}

func (a *AgentRuntime) boundAffiliateCode(ctx context.Context, run runRecord) (string, error) {
	var code string
	err := a.db.QueryRowContext(ctx, `SELECT affiliate_code FROM agent_affiliate_bindings
		WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`,
		strings.ToLower(strings.TrimSpace(run.Transport)), strings.TrimSpace(run.TransportInstance), strings.TrimSpace(run.SenderRef)).Scan(&code)
	return code, err
}

type pointsAccount struct {
	LocalPoints  int64
	InvitePoints int64
	TotalPoints  int64
	Summary      *affiliateSummary
	SyncErr      error
}

func pointsScope(run runRecord) (string, string, string) {
	return strings.ToLower(strings.TrimSpace(run.Transport)), strings.TrimSpace(run.TransportInstance), strings.TrimSpace(run.SenderRef)
}

func (a *AgentRuntime) pointsLedgerBalance(ctx context.Context, run runRecord) (int64, error) {
	transport, instance, sender := pointsScope(run)
	var balance int64
	err := a.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(points), 0) FROM agent_points_ledger
		WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`, transport, instance, sender).Scan(&balance)
	return balance, err
}

func pointsPerInvitee(policy affiliatePolicy) int64 {
	if policy.PointsPerPaidInvitee < 1 {
		return 100
	}
	return policy.PointsPerPaidInvitee
}

func checkInPoints(policy affiliatePolicy) int64 {
	if policy.CheckInPoints < 1 {
		return 10
	}
	return policy.CheckInPoints
}

func shanghaiDate(now time.Time) string {
	return now.In(time.FixedZone("CST", 8*60*60)).Format("2006-01-02")
}

func (a *AgentRuntime) recordDailyCheckIn(ctx context.Context, run runRecord, policy affiliatePolicy) (bool, int64, error) {
	transport, instance, sender := pointsScope(run)
	points := checkInPoints(policy)
	date := shanghaiDate(time.Now().UTC())
	id, err := randomID("points")
	if err != nil {
		return false, 0, err
	}
	result, err := a.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_points_ledger
		(id, transport, transport_instance, sender_ref, entry_type, points, reference_key, note, created_at)
		VALUES (?, ?, ?, ?, 'check_in', ?, ?, ?, ?)`, id, transport, instance, sender, points,
		"check-in:"+date, "每日签到", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, 0, err
	}
	awarded, err := result.RowsAffected()
	if err != nil {
		return false, 0, err
	}
	balance, err := a.pointsLedgerBalance(ctx, run)
	return awarded > 0, balance, err
}

func (a *AgentRuntime) pointsAccount(ctx context.Context, run runRecord, policy affiliatePolicy) (pointsAccount, error) {
	account := pointsAccount{}
	code, err := a.boundAffiliateCode(ctx, run)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return account, err
	}
	if err == nil {
		verified, syncErr := a.affiliateOwnerVerified(ctx, run, code)
		if syncErr == nil && !verified {
			syncErr = errAffiliateOwnershipRequired
		}
		var summary affiliateSummary
		if syncErr == nil {
			summary, syncErr = a.fetchAffiliateSummary(ctx, code, policy)
		}
		if syncErr == nil {
			syncErr = a.creditInvitePoints(ctx, run, summary, policy)
			if syncErr == nil {
				account.Summary = &summary
			}
		}
		account.SyncErr = syncErr
	}
	transport, instance, sender := pointsScope(run)
	err = a.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(points), 0),
		COALESCE(SUM(CASE WHEN entry_type = 'adjustment' AND substr(reference_key, 1, 7) = 'invite:' THEN points ELSE 0 END), 0)
		FROM agent_points_ledger WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`,
		transport, instance, sender).Scan(&account.TotalPoints, &account.InvitePoints)
	account.LocalPoints = account.TotalPoints - account.InvitePoints
	return account, err
}

func (a *AgentRuntime) creditInvitePoints(ctx context.Context, run runRecord, summary affiliateSummary, policy affiliatePolicy) error {
	code, valid := normalizeAffiliateCode(summary.Code)
	if !valid {
		return errors.New("invalid affiliate award code")
	}
	summary.Code = code
	verified, err := a.affiliateOwnerVerified(ctx, run, code)
	if err != nil {
		return err
	}
	if !verified {
		return errAffiliateOwnershipRequired
	}
	rate := pointsPerInvitee(policy)
	if summary.PaidInviteeCount < 0 || summary.PaidInviteeCount > math.MaxInt64/rate {
		return errors.New("affiliate points exceed the supported range")
	}
	id, err := randomID("points")
	if err != nil {
		return err
	}
	transport, instance, sender := pointsScope(run)
	prefix := "invite:" + summary.Code + ":"
	// A single write computes the high-water mark and credits only new invitees.
	// Older concurrent responses and temporary count drops cannot claw back awards.
	_, err = a.db.ExecContext(ctx, `INSERT INTO agent_points_ledger
		(id, transport, transport_instance, sender_ref, entry_type, points, reference_key, note, created_at)
		SELECT ?, ?, ?, ?, 'adjustment', (? - awarded_count) * ?, ?, ?, ?
		FROM (SELECT COALESCE(MAX(CAST(substr(reference_key, length(?) + 1) AS INTEGER)), 0) AS awarded_count
			FROM agent_points_ledger WHERE entry_type = 'adjustment' AND substr(reference_key, 1, length(?)) = ?)
		WHERE ? > awarded_count AND EXISTS (SELECT 1 FROM agent_affiliate_owners
			WHERE affiliate_code = ? AND transport = ? AND transport_instance = ? AND sender_ref = ?)`, id, transport, instance, sender, summary.PaidInviteeCount, rate,
		fmt.Sprintf("%s%d", prefix, summary.PaidInviteeCount), "邀请奖励同步", time.Now().UTC().Format(time.RFC3339Nano),
		prefix, prefix, prefix, summary.PaidInviteeCount, code, transport, instance, sender)
	return err
}

func pointsAccountText(title string, account pointsAccount) string {
	lines := []string{title}
	if account.Summary != nil {
		lines = append(lines, fmt.Sprintf("邀请码：%s", account.Summary.Code), fmt.Sprintf("已邀请：%d 人", account.Summary.InvitedCount), fmt.Sprintf("已充值：%d 人", account.Summary.PaidInviteeCount))
	}
	lines = append(lines, fmt.Sprintf("本地积分：%d 分", account.LocalPoints), fmt.Sprintf("邀请积分：%d 分", account.InvitePoints), fmt.Sprintf("当前积分：%d 分", account.TotalPoints))
	if errors.Is(account.SyncErr, errAffiliateOwnershipRequired) {
		lines = append(lines, "邀请码归属待管理员核验；已入账积分保留，签到不受影响，核验后同步新增邀请奖励。")
	} else if account.SyncErr != nil {
		lines = append(lines, "邀请数据暂未同步，已入账积分保留；新增奖励稍后再查。")
	}
	return strings.Join(lines, "\n")
}

func (a *AgentRuntime) pointsCatalogCount(ctx context.Context) (int64, error) {
	var count int64
	err := a.db.QueryRowContext(ctx, `SELECT count(*) FROM agent_points_catalog WHERE enabled = 1`).Scan(&count)
	return count, err
}

func affiliateRegisterLink(baseURL, code string) (string, error) {
	endpoint, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" {
		return "", errors.New("affiliate register URL is invalid")
	}
	query := endpoint.Query()
	query.Set("aff", code)
	endpoint.RawQuery = query.Encode()
	return endpoint.String(), nil
}

func (a *AgentRuntime) handleAffiliateCommand(ctx context.Context, run runRecord, command coreDirectCommand) (agentReply, error) {
	if !qqAffiliateTransport(run.Transport) || strings.TrimSpace(run.SenderRef) == "" {
		return agentReply{Text: "积分与活动目前只支持 QQ 用户。"}, nil
	}
	policy := command.AffiliatePolicy
	switch command.Kind {
	case directCommandAffiliateBind:
		code, ok := normalizeAffiliateCode(command.AffiliateCode)
		if !ok {
			return agentReply{Text: "用法：/绑定 邀请码"}, nil
		}
		existing, err := a.boundAffiliateCode(ctx, run)
		if err == nil {
			if strings.EqualFold(existing, code) {
				return agentReply{Text: "这个 QQ 已绑定邀请码 " + existing + "。邀请奖励需管理员核验站点账号归属后同步。"}, nil
			}
			return agentReply{Text: "这个 QQ 已绑定邀请码 " + existing + "，如需更换请联系管理员。"}, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return agentReply{}, err
		}
		summary, err := a.fetchAffiliateSummary(ctx, code, policy)
		if errors.Is(err, sql.ErrNoRows) {
			return agentReply{Text: "没查到这个邀请码，请检查后再试。"}, nil
		}
		if err != nil {
			return agentReply{Text: "邀请系统暂时查不到，稍后再试。"}, nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = a.db.ExecContext(ctx, `INSERT OR IGNORE INTO agent_affiliate_bindings
			(transport, transport_instance, sender_ref, affiliate_code, bound_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)`, strings.ToLower(strings.TrimSpace(run.Transport)),
			strings.TrimSpace(run.TransportInstance), strings.TrimSpace(run.SenderRef), summary.Code, now, now)
		if err != nil {
			return agentReply{}, err
		}
		// Read back after INSERT OR IGNORE: racing requests may bind another code.
		bound, err := a.boundAffiliateCode(ctx, run)
		if err != nil {
			return agentReply{}, err
		}
		return agentReply{Text: "绑定成功：" + bound + "\n邀请码归属待管理员核验，核验前不发放邀请奖励。发送 /邀请链接 获取链接。"}, nil

	case directCommandAffiliateLink:
		code, err := a.boundAffiliateCode(ctx, run)
		if errors.Is(err, sql.ErrNoRows) {
			return agentReply{Text: "还没有绑定邀请码，请先发送：/绑定 邀请码"}, nil
		}
		if err != nil {
			return agentReply{}, err
		}
		link, err := affiliateRegisterLink(policy.RegisterBaseURL, code)
		if err != nil {
			return agentReply{Text: "邀请链接暂时没有配置好，请联系管理员。"}, nil
		}
		return agentReply{Text: "你的专属邀请链接：\n" + link + "\n好友通过链接注册并充值后，会计入你的积分。"}, nil

	case directCommandAffiliatePoints:
		account, err := a.pointsAccount(ctx, run, policy)
		if err != nil {
			return agentReply{}, err
		}
		return agentReply{Text: pointsAccountText("🎁 积分账户", account)}, nil

	case directCommandCheckIn:
		awarded, _, err := a.recordDailyCheckIn(ctx, run, policy)
		if err != nil {
			return agentReply{}, err
		}
		account, err := a.pointsAccount(ctx, run, policy)
		if err != nil {
			return agentReply{}, err
		}
		if awarded {
			return agentReply{Text: fmt.Sprintf("签到成功，+%d 积分\n%s", checkInPoints(policy), pointsAccountText("🎁 积分账户", account))}, nil
		}
		return agentReply{Text: "今天已经签到过了。\n" + pointsAccountText("🎁 积分账户", account)}, nil

	case directCommandPointsRedeem:
		account, err := a.pointsAccount(ctx, run, policy)
		if err != nil {
			return agentReply{}, err
		}
		catalogCount, err := a.pointsCatalogCount(ctx)
		if err != nil {
			return agentReply{}, err
		}
		if catalogCount == 0 {
			return agentReply{Text: "🛍 积分兑换\n" + pointsAccountText("🎁 积分账户", account) + "\n兑换中心已准备，奖品暂未上架，后续会在这里开放。"}, nil
		}
		return agentReply{Text: fmt.Sprintf("🛍 积分兑换\n%s\n当前有 %d 个奖品，兑换流程正在准备中。", pointsAccountText("🎁 积分账户", account), catalogCount)}, nil

	case directCommandLottery:
		lotteryURL := strings.TrimSpace(policy.LotteryURL)
		if lotteryURL == "" {
			return agentReply{Text: "抽奖入口暂未配置好，请联系管理员。"}, nil
		}
		endpoint, err := url.Parse(lotteryURL)
		if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" {
			return agentReply{Text: "抽奖入口暂未配置好，请联系管理员。"}, nil
		}
		return agentReply{Text: "🎰 抽奖活动\n打开现有薅乐马抽奖页：\n" + lotteryURL}, nil
	default:
		return agentReply{}, errors.New("affiliate command is invalid")
	}
}
