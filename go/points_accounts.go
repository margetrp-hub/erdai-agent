package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const pointsAccountTables = `
CREATE TABLE IF NOT EXISTS agent_points_accounts (
  id TEXT PRIMARY KEY,
  merged_into TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_points_identities (
  transport TEXT NOT NULL,
  transport_instance TEXT NOT NULL,
  sender_ref TEXT NOT NULL,
  account_id TEXT NOT NULL REFERENCES agent_points_accounts(id),
  PRIMARY KEY (transport, transport_instance, sender_ref)
);
CREATE INDEX IF NOT EXISTS agent_points_identities_account_idx ON agent_points_identities(account_id);
CREATE TABLE IF NOT EXISTS agent_points_checkins (
  account_id TEXT NOT NULL REFERENCES agent_points_accounts(id),
  day TEXT NOT NULL,
  PRIMARY KEY (account_id, day)
);
CREATE TABLE IF NOT EXISTS agent_points_account_merges (
  source_account_id TEXT PRIMARY KEY REFERENCES agent_points_accounts(id),
  target_account_id TEXT NOT NULL REFERENCES agent_points_accounts(id),
  evidence TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS agent_points_identity_links (
  transport TEXT NOT NULL,
  transport_instance TEXT NOT NULL,
  sender_ref TEXT NOT NULL,
  account_id TEXT NOT NULL REFERENCES agent_points_accounts(id),
  evidence TEXT NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY (transport, transport_instance, sender_ref)
);
CREATE TABLE IF NOT EXISTS agent_points_orders (
  id TEXT PRIMARY KEY,
  account_id TEXT NOT NULL REFERENCES agent_points_accounts(id),
  source TEXT NOT NULL,
  external_ref TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('redemption', 'lottery')),
  points INTEGER NOT NULL CHECK (points > 0),
  status TEXT NOT NULL CHECK (status IN ('reserved', 'committed', 'refunded')),
  note TEXT NOT NULL,
  resolution_note TEXT NOT NULL DEFAULT '',
  transport TEXT NOT NULL,
  transport_instance TEXT NOT NULL,
  sender_ref TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE (source, external_ref)
);
CREATE INDEX IF NOT EXISTS agent_points_orders_account_idx ON agent_points_orders(account_id, created_at DESC, id);
`

type pointsIdentity struct {
	Transport         string `json:"transport"`
	TransportInstance string `json:"transportInstance"`
	SenderRef         string `json:"senderRef"`
}

func pointIdentity(run runRecord) pointsIdentity {
	transport, instance, sender := pointsScope(run)
	return pointsIdentity{transport, instance, sender}
}

func (identity pointsIdentity) run() runRecord {
	return runRecord{Transport: identity.Transport, TransportInstance: identity.TransportInstance, SenderRef: identity.SenderRef}
}

func (identity pointsIdentity) initialAccountID() string {
	data, _ := json.Marshal([]string{identity.Transport, identity.TransportInstance, identity.SenderRef})
	return fmt.Sprintf("pa_%x", sha256.Sum256(data))
}

func ensurePointsIdentity(tx coreSchemaTx, identity pointsIdentity) (string, error) {
	id := identity.initialAccountID()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	// The first write also serializes account reads with merges and debits in SQLite.
	if _, err := tx.Exec(`INSERT OR IGNORE INTO agent_points_accounts (id, created_at, updated_at)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM agent_points_identities WHERE transport = ? AND transport_instance = ? AND sender_ref = ?)`,
		id, now, now, identity.Transport, identity.TransportInstance, identity.SenderRef); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO agent_points_identities (transport, transport_instance, sender_ref, account_id) VALUES (?, ?, ?, ?)`,
		identity.Transport, identity.TransportInstance, identity.SenderRef, id); err != nil {
		return "", err
	}
	err := tx.QueryRow(`SELECT account_id FROM agent_points_identities WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`,
		identity.Transport, identity.TransportInstance, identity.SenderRef).Scan(&id)
	return id, err
}

func (a *AgentRuntime) linkPointsIdentity(ctx context.Context, accountID string, identity pointsIdentity, evidence string) (string, error) {
	identity = pointIdentity(identity.run())
	if (!qqAffiliateTransport(identity.Transport) && identity.Transport != "sub2api" && identity.Transport != "newapi") ||
		identity.TransportInstance == "" || len(identity.TransportInstance) > 200 || identity.SenderRef == "" || len(identity.SenderRef) > 128 ||
		strings.TrimSpace(evidence) == "" || len(evidence) > 1000 {
		return "", coreInvalid("平台、实例、用户 ID 和身份核验说明为必填项")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE agent_points_accounts SET updated_at = ? WHERE id = ?`, now, accountID); err != nil {
		return "", err
	}
	accountID, err = pointsCanonicalAccount(tx, accountID)
	if err != nil {
		return "", err
	}
	var existing string
	err = tx.QueryRow(`SELECT account_id FROM agent_points_identities WHERE transport = ? AND transport_instance = ? AND sender_ref = ?`,
		identity.Transport, identity.TransportInstance, identity.SenderRef).Scan(&existing)
	if err == nil {
		if existing == accountID {
			return accountID, nil
		}
		return "", pointsConflict("points_identity_already_linked", "该身份已有积分账户，请核对明细后使用账户合并")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	_, err = tx.Exec(`INSERT INTO agent_points_identities (transport, transport_instance, sender_ref, account_id) VALUES (?, ?, ?, ?)`,
		identity.Transport, identity.TransportInstance, identity.SenderRef, accountID)
	if err != nil {
		return "", err
	}
	_, err = tx.Exec(`INSERT INTO agent_points_identity_links (transport, transport_instance, sender_ref, account_id, evidence, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		identity.Transport, identity.TransportInstance, identity.SenderRef, accountID, strings.TrimSpace(evidence), now)
	if err != nil {
		return "", err
	}
	return accountID, tx.Commit()
}

func migratePointsAccounts(tx coreSchemaTx) error {
	if _, err := tx.Exec(pointsAccountTables); err != nil {
		return err
	}
	rows, err := tx.Query(`SELECT transport, transport_instance, sender_ref FROM agent_points_ledger
		UNION SELECT transport, transport_instance, sender_ref FROM agent_affiliate_bindings
		UNION SELECT transport, transport_instance, sender_ref FROM agent_affiliate_owners`)
	if err != nil {
		return err
	}
	var identities []pointsIdentity
	for rows.Next() {
		var identity pointsIdentity
		if err = rows.Scan(&identity.Transport, &identity.TransportInstance, &identity.SenderRef); err != nil {
			rows.Close()
			return err
		}
		identities = append(identities, identity)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, identity := range identities {
		if _, err = ensurePointsIdentity(tx, identity); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO agent_points_checkins (account_id, day)
		SELECT i.account_id, substr(l.reference_key, 10) FROM agent_points_ledger l
		JOIN agent_points_identities i USING (transport, transport_instance, sender_ref)
		WHERE l.entry_type = 'check_in' AND l.reference_key LIKE 'check-in:____-__-__'`)
	return err
}

func initPointsAccounts(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = migratePointsAccounts(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *AgentRuntime) pointsIdentityAccount(ctx context.Context, run runRecord) (string, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	id, err := ensurePointsIdentity(tx, pointIdentity(run))
	if err != nil {
		return "", err
	}
	return id, tx.Commit()
}

func pointsCanonicalAccount(tx coreSchemaTx, id string) (string, error) {
	for range 64 {
		var next string
		err := tx.QueryRow(`SELECT merged_into FROM agent_points_accounts WHERE id = ?`, id).Scan(&next)
		if errors.Is(err, sql.ErrNoRows) {
			return "", &coreAPIError{status: http.StatusNotFound, code: "points_account_not_found", message: "积分账户不存在"}
		}
		if err != nil {
			return "", err
		}
		if next == "" {
			return id, nil
		}
		id = next
	}
	return "", errors.New("points account merge cycle")
}

func pointsBalance(tx coreSchemaTx, id string) (int64, error) {
	var balance int64
	err := tx.QueryRow(`SELECT COALESCE(SUM(l.points), 0) FROM agent_points_ledger l
		JOIN agent_points_identities i USING (transport, transport_instance, sender_ref) WHERE i.account_id = ?`, id).Scan(&balance)
	return balance, err
}

func pointsConflict(code, message string) error {
	return &coreAPIError{status: http.StatusConflict, code: code, message: message}
}

func (a *AgentRuntime) mergePointsAccounts(ctx context.Context, source, target, evidence string) (string, error) {
	if strings.TrimSpace(evidence) == "" || len(evidence) > 1000 || source == "" || target == "" || source == target {
		return "", coreInvalid("两个不同账户及身份核验说明为必填项")
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = tx.Exec(`UPDATE agent_points_accounts SET updated_at = ? WHERE id = ?`, now, target); err != nil {
		return "", err
	}
	resolvedSource, err := pointsCanonicalAccount(tx, source)
	if err != nil {
		return "", err
	}
	resolvedTarget, err := pointsCanonicalAccount(tx, target)
	if err != nil {
		return "", err
	}
	if resolvedSource == resolvedTarget {
		return resolvedTarget, nil
	}
	if resolvedSource != source {
		return "", pointsConflict("points_account_already_merged", "原账户已合并，请刷新后操作")
	}
	// Joining accounts never claims a second site owner's invitation rewards.
	var ownerCount int
	err = tx.QueryRow(`SELECT count(DISTINCT o.affiliate_code COLLATE NOCASE) FROM agent_affiliate_owners o
		JOIN agent_points_identities i USING (transport, transport_instance, sender_ref) WHERE i.account_id IN (?, ?)`, source, resolvedTarget).Scan(&ownerCount)
	if err != nil {
		return "", err
	}
	if ownerCount > 1 {
		return "", pointsConflict("points_owner_conflict", "两个账户已核验为不同站点归属，不能合并")
	}
	for _, operation := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO agent_points_account_merges (source_account_id, target_account_id, evidence, created_at) VALUES (?, ?, ?, ?)`, []any{source, resolvedTarget, strings.TrimSpace(evidence), now}},
		{`INSERT OR IGNORE INTO agent_points_checkins (account_id, day) SELECT ?, day FROM agent_points_checkins WHERE account_id = ?`, []any{resolvedTarget, source}},
		{`UPDATE agent_points_identities SET account_id = ? WHERE account_id = ?`, []any{resolvedTarget, source}},
		{`UPDATE agent_points_orders SET account_id = ? WHERE account_id = ?`, []any{resolvedTarget, source}},
		{`UPDATE agent_points_accounts SET merged_into = ?, updated_at = ? WHERE id = ? OR merged_into = ?`, []any{resolvedTarget, now, source, source}},
	} {
		if _, err = tx.Exec(operation.query, operation.args...); err != nil {
			return "", err
		}
	}
	if _, err = pointsBalance(tx, resolvedTarget); err != nil {
		return "", err
	}
	return resolvedTarget, tx.Commit()
}
