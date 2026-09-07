package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type pointsSiteClient struct {
	Token             string `json:"token"`
	Transport         string `json:"transport"`
	TransportInstance string `json:"transportInstance"`
}

func parsePointsSiteClients(config RuntimeConfig) ([]pointsSiteClient, error) {
	if strings.TrimSpace(config.PointsSiteClients) == "" {
		return nil, nil
	}
	var clients []pointsSiteClient
	invalid := errors.New("ERDAI_POINTS_SITE_CLIENTS requires distinct scoped site credentials")
	if len(config.PointsSiteClients) > 16384 || json.Unmarshal([]byte(config.PointsSiteClients), &clients) != nil || len(clients) > 16 {
		return nil, invalid
	}
	tokens, instances := map[string]bool{}, map[string]bool{}
	for _, client := range clients {
		origin, err := url.Parse(client.TransportInstance)
		if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" ||
			(client.Transport != "sub2api" && client.Transport != "newapi") || len(client.Token) < 32 || len(client.Token) > 256 ||
			strings.TrimSpace(client.Token) != client.Token || client.Token == config.AdminToken || client.Token == config.RuntimeToken || client.Token == config.PointsReadToken ||
			tokens[client.Token] || instances[client.Transport+":"+client.TransportInstance] {
			return nil, invalid
		}
		tokens[client.Token], instances[client.Transport+":"+client.TransportInstance] = true, true
	}
	return clients, nil
}

type pointsSiteEntry struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Points    int64  `json:"points"`
	CreatedAt string `json:"createdAt"`
}

type pointsSiteAccount struct {
	AccountID      string            `json:"accountId"`
	Balance        int64             `json:"balance"`
	Day            string            `json:"day"`
	CheckedIn      bool              `json:"checkedIn"`
	CheckInPoints  int64             `json:"checkInPoints"`
	CheckInEnabled bool              `json:"checkInEnabled"`
	Linked         bool              `json:"linked"`
	Entries        []pointsSiteEntry `json:"entries"`
	HasMore        bool              `json:"hasMore"`
	Offset         int               `json:"offset"`
	Limit          int               `json:"limit"`
}

func (a *AgentRuntime) registerPointsSite(ctx context.Context, identity pointsIdentity) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err := ensurePointsIdentity(tx, identity)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO agent_points_identity_links
		(transport, transport_instance, sender_ref, account_id, evidence, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		identity.Transport, identity.TransportInstance, identity.SenderRef, id, "Authenticated site session verified by scoped activity service", time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (a *AgentRuntime) readPointsSite(ctx context.Context, identity pointsIdentity, policy affiliatePolicy, limit, offset int) (pointsSiteAccount, error) {
	result := pointsSiteAccount{Day: shanghaiDate(time.Now()), CheckInPoints: checkInPoints(policy), CheckInEnabled: policy.Enabled, Entries: []pointsSiteEntry{}, Limit: limit, Offset: offset}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`SELECT account_id FROM agent_points_identities WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`,
		identity.Transport, identity.TransportInstance, identity.SenderRef).Scan(&result.AccountID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, pointsConflict("points_account_required", "请先开通积分账户")
	}
	if err != nil {
		return result, err
	}
	result.Balance, err = pointsBalance(tx, result.AccountID)
	if err != nil {
		return result, err
	}
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM agent_points_checkins WHERE account_id = ? AND day = ?),
		(SELECT COUNT(*) > 1 FROM agent_points_identities WHERE account_id = ?)`, result.AccountID, result.Day, result.AccountID).Scan(&result.CheckedIn, &result.Linked)
	if err != nil {
		return result, err
	}
	rows, err := tx.Query(`SELECT l.id, CASE WHEN l.reference_key LIKE 'invite:%' THEN 'invite' ELSE l.entry_type END, l.points, l.created_at
		FROM agent_points_ledger l JOIN agent_points_identities i USING (transport, transport_instance, sender_ref)
		WHERE i.account_id = ? ORDER BY l.created_at DESC, l.rowid DESC LIMIT ? OFFSET ?`, result.AccountID, limit+1, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry pointsSiteEntry
		if err = rows.Scan(&entry.ID, &entry.Type, &entry.Points, &entry.CreatedAt); err != nil {
			return result, err
		}
		result.Entries = append(result.Entries, entry)
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	result.HasMore = len(result.Entries) > limit
	result.Entries = result.Entries[:min(limit, len(result.Entries))]
	return result, nil
}

func (a *AgentRuntime) handlePointsSiteBridge(w http.ResponseWriter, r *http.Request) {
	var client *pointsSiteClient
	token := r.Header.Get("X-ErDai-Points-Token")
	for i := range a.pointsSiteClients {
		candidate := &a.pointsSiteClients[i]
		if len(candidate.Token) >= 32 && candidate.Token != a.adminToken && candidate.Token != a.runtimeToken && candidate.Token != a.pointsReadToken && tokenMatches(token, candidate.Token) {
			client = candidate
		}
	}
	if client == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "scoped site credential required"}})
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/points-bridge/v1/site/")
	if !(path == "account" && (r.Method == http.MethodGet || r.Method == http.MethodPost)) && !(path == "checkin" && r.Method == http.MethodPost) {
		writeCoreAPIError(w, coreNotFound("points site route not found"))
		return
	}
	limit, offset, err := pointsPage(r)
	if err != nil || limit > 50 {
		writeCoreAPIError(w, coreInvalid("积分流水分页无效"))
		return
	}
	sender := r.URL.Query().Get("senderRef")
	if r.Method == http.MethodPost {
		var input struct {
			SenderRef string `json:"senderRef"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&input); err != nil {
			writeCoreAPIError(w, coreInvalid("站点身份请求无效"))
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			writeCoreAPIError(w, coreInvalid("站点身份请求无效"))
			return
		}
		sender = input.SenderRef
	}
	userID, err := strconv.ParseInt(sender, 10, 64)
	if err != nil || userID <= 0 || strconv.FormatInt(userID, 10) != sender {
		writeCoreAPIError(w, coreInvalid("站点用户 ID 无效"))
		return
	}
	identity := pointsIdentity{client.Transport, client.TransportInstance, sender}
	var policy affiliatePolicy
	if err = a.integrationConfig(r.Context(), "affiliate_policy", &policy); err != nil {
		writeCoreAPIError(w, err)
		return
	}
	if path == "account" && r.Method == http.MethodPost {
		if err = a.registerPointsSite(r.Context(), identity); err != nil {
			writeCoreAPIError(w, err)
			return
		}
	}
	account, err := a.readPointsSite(r.Context(), identity, policy, limit, offset)
	if err != nil {
		writeCoreAPIError(w, err)
		return
	}
	awarded := false
	if path == "checkin" {
		if !policy.Enabled {
			writeCoreAPIError(w, pointsConflict("checkin_disabled", "签到暂未开放"))
			return
		}
		awarded, _, err = a.recordDailyCheckIn(r.Context(), identity.run(), policy)
		if err != nil {
			writeCoreAPIError(w, err)
			return
		}
		account, err = a.readPointsSite(r.Context(), identity, policy, limit, 0)
		if err != nil {
			writeCoreAPIError(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": account, "awarded": awarded})
}
