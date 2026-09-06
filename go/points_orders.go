package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

type pointsOrder struct {
	ID             string `json:"id"`
	AccountID      string `json:"accountId"`
	Source         string `json:"source"`
	ExternalRef    string `json:"externalRef"`
	Kind           string `json:"kind"`
	Points         int64  `json:"points"`
	Status         string `json:"status"`
	Note           string `json:"note"`
	ResolutionNote string `json:"resolutionNote"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

const pointsOrderColumns = `id, account_id, source, external_ref, kind, points, status, note, resolution_note, created_at, updated_at`

func scanPointsOrder(row interface{ Scan(...any) error }) (pointsOrder, error) {
	var order pointsOrder
	err := row.Scan(&order.ID, &order.AccountID, &order.Source, &order.ExternalRef, &order.Kind,
		&order.Points, &order.Status, &order.Note, &order.ResolutionNote, &order.CreatedAt, &order.UpdatedAt)
	return order, err
}

func (a *AgentRuntime) reservePointsOrder(ctx context.Context, input pointsOrder) (pointsOrder, error) {
	input.Source = strings.TrimSpace(input.Source)
	input.ExternalRef = strings.TrimSpace(input.ExternalRef)
	input.Note = strings.TrimSpace(input.Note)
	if input.AccountID == "" || input.Source == "" || len(input.Source) > 80 || input.ExternalRef == "" || len(input.ExternalRef) > 160 ||
		(input.Kind != "redemption" && input.Kind != "lottery") || input.Points < 1 || input.Points > 1_000_000_000 || input.Note == "" || len(input.Note) > 1000 {
		return pointsOrder{}, coreInvalid("账户、业务来源、唯一订单号、交易类型、积分和说明为必填项；单笔积分须为 1..1000000000")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return pointsOrder{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE agent_points_accounts SET updated_at = ? WHERE id = ?`, now, input.AccountID); err != nil {
		return pointsOrder{}, err
	}
	accountID, err := pointsCanonicalAccount(tx, input.AccountID)
	if err != nil {
		return pointsOrder{}, err
	}
	existing, err := scanPointsOrder(tx.QueryRow(`SELECT `+pointsOrderColumns+` FROM agent_points_orders WHERE source = ? AND external_ref = ?`, input.Source, input.ExternalRef))
	if err == nil {
		if existing.AccountID != accountID || existing.Kind != input.Kind || existing.Points != input.Points || existing.Note != input.Note {
			return pointsOrder{}, pointsConflict("points_idempotency_conflict", "相同业务订单号不能用于不同交易")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return pointsOrder{}, err
	}
	balance, err := pointsBalance(tx, accountID)
	if err != nil {
		return pointsOrder{}, err
	}
	if balance < input.Points {
		return pointsOrder{}, pointsConflict("insufficient_points", "可用积分不足")
	}
	var identity pointsIdentity
	err = tx.QueryRow(`SELECT transport, transport_instance, sender_ref FROM agent_points_identities WHERE account_id = ? ORDER BY transport, transport_instance, sender_ref LIMIT 1`, accountID).
		Scan(&identity.Transport, &identity.TransportInstance, &identity.SenderRef)
	if err != nil {
		return pointsOrder{}, err
	}
	id, err := randomID("po")
	if err != nil {
		return pointsOrder{}, err
	}
	input.ID, input.AccountID, input.Status = id, accountID, "reserved"
	input.CreatedAt, input.UpdatedAt, input.ResolutionNote = now, now, ""
	_, err = tx.Exec(`INSERT INTO agent_points_orders (`+pointsOrderColumns+`, transport, transport_instance, sender_ref) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, accountID, input.Source, input.ExternalRef, input.Kind, input.Points, input.Status, input.Note, "", now, now,
		identity.Transport, identity.TransportInstance, identity.SenderRef)
	if err != nil {
		return pointsOrder{}, err
	}
	_, err = tx.Exec(`INSERT INTO agent_points_ledger (id, transport, transport_instance, sender_ref, entry_type, points, reference_key, note, created_at)
		VALUES (?, ?, ?, ?, 'redemption', ?, ?, ?, ?)`, id+"_debit", identity.Transport, identity.TransportInstance, identity.SenderRef,
		-input.Points, "order:"+id+":debit", input.Note, now)
	if err != nil {
		return pointsOrder{}, err
	}
	return input, tx.Commit()
}

func (a *AgentRuntime) resolvePointsOrder(ctx context.Context, id, status, note string) (pointsOrder, error) {
	if (status != "committed" && status != "refunded") || strings.TrimSpace(note) == "" || len(note) > 1000 {
		return pointsOrder{}, coreInvalid("交易状态及处理凭据为必填项")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return pointsOrder{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE agent_points_orders SET updated_at = updated_at WHERE id = ?`, id); err != nil {
		return pointsOrder{}, err
	}
	order, err := scanPointsOrder(tx.QueryRow(`SELECT `+pointsOrderColumns+` FROM agent_points_orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return pointsOrder{}, coreNotFound("积分订单不存在")
	}
	if err != nil {
		return pointsOrder{}, err
	}
	if order.Status == status {
		return order, nil
	}
	if order.Status != "reserved" {
		return pointsOrder{}, pointsConflict("points_order_finalized", "交易已结算，不能重复发放或退款")
	}
	if status == "refunded" {
		// Only a confirmed unfulfilled reservation can be refunded. Timeouts are not proof of failure.
		_, err = tx.Exec(`INSERT INTO agent_points_ledger (id, transport, transport_instance, sender_ref, entry_type, points, reference_key, note, created_at)
			SELECT ?, transport, transport_instance, sender_ref, 'adjustment', points, ?, ?, ? FROM agent_points_orders WHERE id = ?`,
			id+"_refund", "order:"+id+":refund", strings.TrimSpace(note), now, id)
		if err != nil {
			return pointsOrder{}, err
		}
		if _, err = pointsBalance(tx, order.AccountID); err != nil {
			return pointsOrder{}, err
		}
	}
	_, err = tx.Exec(`UPDATE agent_points_orders SET status = ?, resolution_note = ?, updated_at = ? WHERE id = ?`, status, strings.TrimSpace(note), now, id)
	if err != nil {
		return pointsOrder{}, err
	}
	order.Status, order.ResolutionNote, order.UpdatedAt = status, strings.TrimSpace(note), now
	return order, tx.Commit()
}
