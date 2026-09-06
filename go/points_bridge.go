package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// A separate read-only credential lets the activity service display balances
// without gaining management, merge, debit or refund authority.
func (a *AgentRuntime) handlePointsReadBridge(w http.ResponseWriter, r *http.Request) bool {
	if !strings.HasPrefix(r.URL.Path, "/points-bridge/") {
		return false
	}
	w.Header().Set("Cache-Control", "no-store")
	if len(a.pointsReadToken) < 32 || a.pointsReadToken == a.adminToken || a.pointsReadToken == a.runtimeToken ||
		!tokenMatches(r.Header.Get("X-ErDai-Points-Token"), a.pointsReadToken) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "points read credential required"}})
		return true
	}
	if r.Method != http.MethodGet || r.URL.Path != "/points-bridge/v1/account" {
		writeCoreAPIError(w, coreNotFound("points bridge route not found"))
		return true
	}
	identity := pointsIdentity{r.URL.Query().Get("transport"), r.URL.Query().Get("transportInstance"), r.URL.Query().Get("senderRef")}
	if (identity.Transport != "sub2api" && identity.Transport != "newapi") || identity.TransportInstance == "" || len(identity.TransportInstance) > 200 || identity.SenderRef == "" || len(identity.SenderRef) > 128 {
		writeCoreAPIError(w, coreInvalid("site identity is required"))
		return true
	}
	var id string
	var balance int64
	err := a.db.QueryRowContext(r.Context(), `SELECT actor.account_id,
		(SELECT COALESCE(SUM(l.points), 0) FROM agent_points_ledger l JOIN agent_points_identities i USING (transport, transport_instance, sender_ref) WHERE i.account_id = actor.account_id)
		FROM agent_points_identities actor WHERE actor.transport = ? AND actor.transport_instance = ? AND actor.sender_ref = ?`,
		identity.Transport, identity.TransportInstance, identity.SenderRef).Scan(&id, &balance)
	if errors.Is(err, sql.ErrNoRows) {
		writeCoreAPIError(w, &coreAPIError{status: http.StatusConflict, code: "points_identity_unverified", message: "站点账号尚未核验关联积分账户"})
	} else if err != nil {
		writeCoreAPIError(w, err)
	} else {
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"accountId": id, "balance": balance}})
	}
	return true
}
