package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

var errAffiliateOwnershipRequired = errors.New("affiliate ownership verification required")

func (a *AgentRuntime) affiliateOwnerVerified(ctx context.Context, run runRecord, code string) (bool, error) {
	transport, instance, sender := pointsScope(run)
	var found int
	err := a.db.QueryRowContext(ctx, `SELECT 1 FROM agent_affiliate_owners o
		JOIN agent_affiliate_bindings b ON b.affiliate_code = o.affiliate_code COLLATE NOCASE
		AND b.transport = o.transport AND b.transport_instance = o.transport_instance AND b.sender_ref = o.sender_ref
		WHERE o.affiliate_code = ? AND o.transport = ? AND o.transport_instance = ? AND o.sender_ref = ?`,
		code, transport, instance, sender).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Approval is an administrator attestation after checking the site account;
// knowing a public invite code alone is never proof of ownership.
func (a *AgentRuntime) handleAffiliateOwnership(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		limit, offset := 200, 0
		for key, target := range map[string]*int{"limit": &limit, "offset": &offset} {
			if raw := r.URL.Query().Get(key); raw != "" {
				value, err := strconv.Atoi(raw)
				if err != nil || value < 0 || (key == "limit" && (value < 1 || value > 200)) {
					return coreInvalid("limit must be 1..200 and offset must be non-negative")
				}
				*target = value
			}
		}
		statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
		if statusFilter != "" && statusFilter != "pending" && statusFilter != "verified" && statusFilter != "conflict" {
			return coreInvalid("status must be pending, verified or conflict")
		}
		query := `SELECT * FROM (SELECT b.transport, b.transport_instance, b.sender_ref, b.affiliate_code,
			CASE WHEN o.sender_ref IS NULL THEN 'pending' WHEN o.transport = b.transport
			AND o.transport_instance = b.transport_instance AND o.sender_ref = b.sender_ref THEN 'verified' ELSE 'conflict' END AS status,
			b.bound_at
			FROM agent_affiliate_bindings b LEFT JOIN agent_affiliate_owners o ON o.affiliate_code = b.affiliate_code COLLATE NOCASE
			) WHERE 1 = 1`
		args := []any{}
		for _, filter := range []struct{ parameter, column string }{
			{"status", "status"}, {"affiliateCode", "affiliate_code COLLATE NOCASE"},
			{"transport", "transport"}, {"transportInstance", "transport_instance"}, {"senderRef", "sender_ref"},
		} {
			if value := strings.TrimSpace(r.URL.Query().Get(filter.parameter)); value != "" {
				query += " AND " + filter.column + " = ?"
				args = append(args, value)
			}
		}
		query += " ORDER BY bound_at, transport, transport_instance, sender_ref LIMIT ? OFFSET ?"
		args = append(args, limit+1, offset)
		rows, err := a.db.QueryContext(r.Context(), query, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []map[string]string{}
		for rows.Next() {
			var transport, instance, sender, code, status, boundAt string
			if err := rows.Scan(&transport, &instance, &sender, &code, &status, &boundAt); err != nil {
				return err
			}
			items = append(items, map[string]string{"transport": transport, "transportInstance": instance, "senderRef": sender, "affiliateCode": code, "status": status})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		hasMore := len(items) > limit
		var nextOffset any
		if hasMore {
			nextOffset = offset + limit
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"data": items[:min(limit, len(items))], "truncated": hasMore,
			"limit": limit, "offset": offset, "nextOffset": nextOffset,
		})
		return nil
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return nil
	}
	var input struct {
		Transport          string `json:"transport"`
		TransportInstance  string `json:"transportInstance"`
		SenderRef          string `json:"senderRef"`
		AffiliateCode      string `json:"affiliateCode"`
		OwnershipConfirmed bool   `json:"ownershipConfirmed"`
		Evidence           string `json:"evidence"`
	}
	if err := decodeJSONBody(r, &input); err != nil {
		return coreInvalid("invalid ownership verification")
	}
	code, valid := normalizeAffiliateCode(input.AffiliateCode)
	run := runRecord{Transport: input.Transport, TransportInstance: input.TransportInstance, SenderRef: input.SenderRef}
	transport, instance, sender := pointsScope(run)
	if !valid || !qqAffiliateTransport(transport) || instance == "" || sender == "" || !input.OwnershipConfirmed || strings.TrimSpace(input.Evidence) == "" || len(input.Evidence) > 1000 {
		return coreInvalid("QQ account, invite code and confirmed ownership evidence are required")
	}
	bound, err := a.boundAffiliateCode(r.Context(), run)
	if err != nil {
		return err
	}
	if !strings.EqualFold(bound, code) {
		return coreInvalid("account is not bound to the requested invite code")
	}
	_, err = a.db.ExecContext(r.Context(), `INSERT OR IGNORE INTO agent_affiliate_owners
		(affiliate_code, transport, transport_instance, sender_ref, verified_at, evidence)
		VALUES (?, ?, ?, ?, ?, ?)`, code, transport, instance, sender, time.Now().UTC().Format(time.RFC3339Nano), strings.TrimSpace(input.Evidence))
	if err != nil {
		return err
	}
	verified, err := a.affiliateOwnerVerified(r.Context(), run, code)
	if err != nil {
		return err
	}
	if !verified {
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]string{"code": "affiliate_owner_conflict", "message": "invite code already has a verified owner; no balances or bindings were changed"}})
		return nil
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"affiliateCode": code, "verified": true}})
	return nil
}
