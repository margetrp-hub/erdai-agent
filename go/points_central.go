package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// When configured, Sub2API is the sole ledger. Network/unknown results must
// never fall back to SQLite; safe retries keep the server's idempotency key.
type centralPointsClient struct {
	base, token string
	client      *http.Client
}

func newCentralPointsClient(base, token string) (*centralPointsClient, error) {
	if base == "" && token == "" {
		return nil, nil
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || len(token) < 32 {
		return nil, errors.New("ERDAI_CENTRAL_POINTS_URL must be an HTTPS origin with a dedicated service token")
	}
	return &centralPointsClient{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (p *centralPointsClient) call(ctx context.Context, input any, out any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api/v1/integrations/erdai/points", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-OHLAO-Points-Token", p.token)
	res, err := p.client.Do(req)
	if err != nil {
		return &coreAPIError{status: 503, code: "points_unconfirmed", message: "积分结果暂未确认，请刷新核对后重试"}
	}
	defer res.Body.Close()
	var envelope struct {
		Code    *int            `json:"code"`
		Message string          `json:"message"`
		Reason  string          `json:"reason"`
		Data    json.RawMessage `json:"data"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 512*1024+1))
	if err != nil || len(data) > 512*1024 || json.Unmarshal(data, &envelope) != nil {
		return &coreAPIError{status: 503, code: "points_unconfirmed", message: "积分结果暂未确认，请刷新核对后重试"}
	}
	if res.StatusCode != 200 || envelope.Code == nil || *envelope.Code != 0 {
		status := res.StatusCode
		if status < 400 {
			status = http.StatusBadGateway
		}
		return &coreAPIError{status: status, code: envelope.Reason, message: envelope.Message}
	}
	if out != nil {
		if len(envelope.Data) == 0 || bytes.Equal(envelope.Data, []byte("null")) || json.Unmarshal(envelope.Data, out) != nil {
			return &coreAPIError{status: 503, code: "points_unconfirmed", message: "积分结果暂未确认，请刷新核对后重试"}
		}
	}
	return nil
}
func (p *centralPointsClient) identityInput(identity pointsIdentity, action string, limit, offset int) (map[string]any, error) {
	input := map[string]any{"action": action, "limit": limit, "offset": offset}
	if identity.Transport == "sub2api" {
		if strings.TrimRight(identity.TransportInstance, "/") != p.base {
			return nil, coreInvalid("站点身份不属于当前积分主站")
		}
		id, err := strconv.ParseInt(identity.SenderRef, 10, 64)
		if err != nil || id <= 0 {
			return nil, coreInvalid("站点用户ID无效")
		}
		input["user_id"] = id
	} else {
		input["identity"] = map[string]string{"transport": identity.Transport, "instance": identity.TransportInstance, "subject": identity.SenderRef}
	}
	return input, nil
}
func (p *centralPointsClient) account(ctx context.Context, identity pointsIdentity, action string, limit, offset int) (pointsSiteAccount, bool, error) {
	var out struct {
		Account pointsSiteAccount `json:"account"`
		Awarded bool              `json:"awarded"`
	}
	input, err := p.identityInput(identity, action, limit, offset)
	if err == nil {
		err = p.call(ctx, input, &out)
	}
	return out.Account, out.Awarded, err
}
func (p *centralPointsClient) bind(ctx context.Context, identity pointsIdentity, code string) error {
	input, err := p.identityInput(identity, "bind", 20, 0)
	if err != nil {
		return err
	}
	input["code"] = code
	return p.call(ctx, input, nil)
}

func centralPointsOrderInput(order pointsOrder) map[string]any {
	return map[string]any{"accountId": order.AccountID, "source": order.Source, "externalRef": order.ExternalRef, "kind": order.Kind, "points": order.Points, "note": order.Note}
}

func (a *AgentRuntime) centralPointsCommand(ctx context.Context, run runRecord, command coreDirectCommand) (agentReply, error) {
	if a.centralPoints == nil {
		return agentReply{Text: "统一积分服务尚未配置，请联系管理员。"}, nil
	}
	if !qqAffiliateTransport(run.Transport) || run.SenderRef == "" {
		return agentReply{Text: "请使用本人 QQ 与二呆私聊操作。"}, nil
	}
	if command.Kind == directCommandAccountBind {
		if run.ConversationKind != "private" {
			return agentReply{Text: "请在 OH LAO 个人中心重新生成绑定码，并私聊发送给二呆。不要在群聊中发送绑定码。"}, nil
		}
		if err := a.centralPoints.bind(ctx, pointIdentity(run), command.AffiliateCode); err != nil {
			return agentReply{Text: centralPointsErrorText(err)}, nil
		}
		return agentReply{Text: "账号关联成功。积分由 OH LAO 统一保存，可在个人中心查看或解除关联。发送 /积分 查看余额。"}, nil
	}
	prefix := "🎁 统一积分账户"
	if command.Kind == directCommandCheckIn {
		if _, _, err := a.centralPoints.account(ctx, pointIdentity(run), "register", 20, 0); err != nil {
			return agentReply{Text: centralPointsErrorText(err)}, nil
		}
		account, awarded, err := a.centralPoints.account(ctx, pointIdentity(run), "checkin", 20, 0)
		if err != nil {
			return agentReply{Text: centralPointsErrorText(err)}, nil
		}
		prefix = "今日已签到"
		if awarded {
			prefix = "签到成功，+" + strconv.FormatInt(account.CheckInPoints, 10) + " 积分"
		}
	}
	summary, err := a.pointsAccount(ctx, run, command.AffiliatePolicy)
	if err != nil {
		return agentReply{Text: centralPointsErrorText(err)}, nil
	}
	note := ""
	if summary.SyncErr != nil {
		note = "\n邀请奖励尚未同步，已入账积分保留。"
	}
	if command.Kind == directCommandPointsRedeem {
		note += "\n奖品兑换暂未开放，当前不会扣除积分。"
	}
	return agentReply{Text: prefix + "\n当前积分：" + strconv.FormatInt(summary.TotalPoints, 10) + " 分" + note + "\n账号关联与积分明细：" + a.centralPoints.base + "/profile"}, nil
}
func centralPointsErrorText(err error) string {
	var apiErr *coreAPIError
	if errors.As(err, &apiErr) && apiErr.status >= 400 && apiErr.status < 500 {
		return apiErr.message
	}
	return "积分结果暂未确认，请发送 /积分 核对后再重试。不会转到旧账本重复记账。"
}

func (a *AgentRuntime) centralPointsManagement(w http.ResponseWriter, r *http.Request, path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/points/"), "/")
	if r.Method == http.MethodGet && path == "/api/v1/points/config" {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"owner": "sub2api", "bindingUrl": a.centralPoints.base + "/profile"}})
		return nil
	}
	input := map[string]any{}
	if r.Method == http.MethodGet {
		limit, offset, err := pointsPage(r)
		if err != nil {
			return err
		}
		input = map[string]any{"action": "manage", "path": strings.Join(parts, "/"), "limit": limit, "offset": offset, "query": r.URL.Query().Get("q"), "status": r.URL.Query().Get("status"), "account_id": r.URL.Query().Get("accountId")}
	} else if r.Method == http.MethodPost && len(parts) == 1 && parts[0] == "orders" {
		var order pointsOrder
		if err := decodeJSONBody(r, &order); err != nil {
			return err
		}
		input = map[string]any{"action": "reserve", "order": centralPointsOrderInput(order)}
	} else if r.Method == http.MethodPost && len(parts) == 3 && parts[0] == "orders" && parts[2] == "resolve" {
		var body struct {
			Status string `json:"status"`
			Note   string `json:"note"`
		}
		if err := decodeJSONBody(r, &body); err != nil {
			return err
		}
		input = map[string]any{"action": "resolve", "order": map[string]string{"id": parts[1]}, "status": body.Status, "note": body.Note}
	} else {
		return &coreAPIError{status: 409, code: "points_binding_moved", message: "账号关联已迁到 OH LAO 个人中心，请由用户生成绑定码完成验证。"}
	}
	var out json.RawMessage
	if err := a.centralPoints.call(r.Context(), input, &out); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
	return nil
}
