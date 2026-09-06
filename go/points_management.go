package main

import (
	"net/http"
	"strconv"
	"strings"
)

func pointsPage(r *http.Request) (int, int, error) {
	limit, offset := 25, 0
	for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
		if raw := r.URL.Query().Get(key); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 || (key == "limit" && (value < 1 || value > 200)) || value > 1_000_000 {
				return 0, 0, coreInvalid("limit 须为 1..200，offset 须为 0..1000000")
			}
			*target = value
		}
	}
	return limit, offset, nil
}

func writePointsPage[T any](w http.ResponseWriter, items []T, limit, offset int) {
	more := len(items) > limit
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"items": items[:min(len(items), limit)], "hasMore": more, "offset": offset, "limit": limit,
	}})
}

func (a *AgentRuntime) handlePointsManagement(w http.ResponseWriter, r *http.Request, path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/points/"), "/")
	if r.Method == http.MethodGet {
		limit, offset, err := pointsPage(r)
		if err != nil {
			return err
		}
		switch {
		case len(parts) == 1 && parts[0] == "accounts":
			return a.listPointsAccounts(w, r, limit, offset)
		case len(parts) == 2 && parts[0] == "accounts":
			return a.readPointsAccount(w, r, parts[1], limit, offset)
		case len(parts) == 1 && parts[0] == "orders":
			return a.listPointsOrders(w, r, limit, offset)
		}
	}
	if r.Method == http.MethodPost {
		switch {
		case len(parts) == 3 && parts[0] == "accounts" && parts[2] == "identities":
			var input struct {
				pointsIdentity
				IdentityConfirmed bool   `json:"identityConfirmed"`
				Evidence          string `json:"evidence"`
			}
			if err := decodeJSONBody(r, &input); err != nil || !input.IdentityConfirmed {
				return coreInvalid("请核实并确认关联身份属于该用户")
			}
			id, err := a.linkPointsIdentity(r.Context(), parts[1], input.pointsIdentity, input.Evidence)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"accountId": id}})
			return nil
		case len(parts) == 3 && parts[0] == "accounts" && parts[2] == "merge":
			var input struct {
				TargetAccountID   string `json:"targetAccountId"`
				IdentityConfirmed bool   `json:"identityConfirmed"`
				Evidence          string `json:"evidence"`
			}
			if err := decodeJSONBody(r, &input); err != nil || !input.IdentityConfirmed {
				return coreInvalid("请核实并确认两个账户属于同一用户")
			}
			id, err := a.mergePointsAccounts(r.Context(), parts[1], input.TargetAccountID, input.Evidence)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"accountId": id}})
			return nil
		case len(parts) == 1 && parts[0] == "orders":
			var input pointsOrder
			if err := decodeJSONBody(r, &input); err != nil {
				return coreInvalid("积分交易请求无效")
			}
			order, err := a.reservePointsOrder(r.Context(), input)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": order})
			return nil
		case len(parts) == 3 && parts[0] == "orders" && parts[2] == "resolve":
			var input struct {
				Status string `json:"status"`
				Note   string `json:"note"`
			}
			if err := decodeJSONBody(r, &input); err != nil {
				return coreInvalid("积分交易处理请求无效")
			}
			order, err := a.resolvePointsOrder(r.Context(), parts[1], input.Status, input.Note)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": order})
			return nil
		}
	}
	return coreNotFound("积分接口不存在或请求方法不支持")
}

type managedPointsAccount struct {
	ID            string `json:"id"`
	Balance       int64  `json:"balance"`
	IdentityCount int    `json:"identityCount"`
	Users         string `json:"users"`
	CreatedAt     string `json:"createdAt"`
}

func (a *AgentRuntime) listPointsAccounts(w http.ResponseWriter, r *http.Request, limit, offset int) error {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) > 160 {
		return coreInvalid("账户查询条件过长")
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT p.id,
		(SELECT COALESCE(SUM(l.points), 0) FROM agent_points_ledger l JOIN agent_points_identities i USING (transport, transport_instance, sender_ref) WHERE i.account_id = p.id),
		(SELECT count(*) FROM agent_points_identities i WHERE i.account_id = p.id),
		(SELECT COALESCE(group_concat(DISTINCT i.sender_ref), '') FROM agent_points_identities i WHERE i.account_id = p.id), p.created_at
		FROM agent_points_accounts p WHERE p.merged_into = '' AND (? = '' OR instr(p.id, ?) > 0 OR EXISTS
		(SELECT 1 FROM agent_points_identities i WHERE i.account_id = p.id AND (instr(i.sender_ref, ?) > 0 OR instr(i.transport_instance, ?) > 0)))
		ORDER BY p.created_at DESC, p.id LIMIT ? OFFSET ?`, query, query, query, query, limit+1, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []managedPointsAccount{}
	for rows.Next() {
		var item managedPointsAccount
		if err = rows.Scan(&item.ID, &item.Balance, &item.IdentityCount, &item.Users, &item.CreatedAt); err != nil {
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writePointsPage(w, items, limit, offset)
	return nil
}

type managedPointsEntry struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Points       int64  `json:"points"`
	ReferenceKey string `json:"referenceKey"`
	Note         string `json:"note"`
	CreatedAt    string `json:"createdAt"`
	pointsIdentity
}

func (a *AgentRuntime) readPointsAccount(w http.ResponseWriter, r *http.Request, id string, limit, offset int) error {
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id, err = pointsCanonicalAccount(tx, id)
	if err != nil {
		return err
	}
	balance, err := pointsBalance(tx, id)
	if err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT transport, transport_instance, sender_ref FROM agent_points_identities WHERE account_id = ? ORDER BY transport, transport_instance, sender_ref`, id)
	if err != nil {
		return err
	}
	identities := []pointsIdentity{}
	for rows.Next() {
		var item pointsIdentity
		if err = rows.Scan(&item.Transport, &item.TransportInstance, &item.SenderRef); err != nil {
			rows.Close()
			return err
		}
		identities = append(identities, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	rows, err = tx.Query(`SELECT l.id, l.entry_type, l.points, l.reference_key, l.note, l.created_at, l.transport, l.transport_instance, l.sender_ref
		FROM agent_points_ledger l JOIN agent_points_identities i USING (transport, transport_instance, sender_ref)
		WHERE i.account_id = ? ORDER BY l.created_at DESC, l.rowid DESC LIMIT ? OFFSET ?`, id, limit+1, offset)
	if err != nil {
		return err
	}
	entries := []managedPointsEntry{}
	for rows.Next() {
		var item managedPointsEntry
		if err = rows.Scan(&item.ID, &item.Type, &item.Points, &item.ReferenceKey, &item.Note, &item.CreatedAt, &item.Transport, &item.TransportInstance, &item.SenderRef); err != nil {
			rows.Close()
			return err
		}
		entries = append(entries, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{
		"id": id, "balance": balance, "identities": identities, "entries": entries[:min(limit, len(entries))],
		"hasMore": len(entries) > limit, "limit": limit, "offset": offset,
	}})
	return nil
}

func (a *AgentRuntime) listPointsOrders(w http.ResponseWriter, r *http.Request, limit, offset int) error {
	status := r.URL.Query().Get("status")
	if status != "" && status != "reserved" && status != "committed" && status != "refunded" {
		return coreInvalid("积分订单状态无效")
	}
	accountID := r.URL.Query().Get("accountId")
	if accountID != "" {
		var err error
		accountID, err = pointsCanonicalAccount(a.db, accountID)
		if err != nil {
			return err
		}
	}
	rows, err := a.db.QueryContext(r.Context(), `SELECT `+pointsOrderColumns+` FROM agent_points_orders
		WHERE (? = '' OR status = ?) AND (? = '' OR account_id = ?) ORDER BY created_at DESC, id LIMIT ? OFFSET ?`, status, status, accountID, accountID, limit+1, offset)
	if err != nil {
		return err
	}
	defer rows.Close()
	items := []pointsOrder{}
	for rows.Next() {
		item, err := scanPointsOrder(rows)
		if err != nil {
			return err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	writePointsPage(w, items, limit, offset)
	return nil
}
