package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"regexp"
	"strings"
	"time"
)

func durableMemoryStatement(message string) bool {
	if strings.ContainsAny(message, "?？") || imaginaryMemoryContext(message) {
		return false
	}
	return !containsAnyText(message, []string{
		"今天", "今晚", "明天", "这次", "暂时", "现在想", "此刻", "只限", "可能", "也许", "才怪",
		"他说", "她说", "朋友说", "同事说", "有人说", "原话", "引用", "开玩笑",
	})
}

var memoryPreferenceFactPattern = regexp.MustCompile(`^我(?:现在|已经)?(?:不再|不|很|比较|最)?喜欢(.+)$`)

// Only explicit, mutually exclusive facts share a key. Liking tea does not
// replace liking coffee, and vague identity/habit prose is never auto-merged.
func stableMemoryFactKey(candidate stableMemoryCandidate) string {
	if candidate.Kind == "address" {
		return "address"
	}
	if candidate.Kind != "preference" {
		return ""
	}
	content := strings.TrimSpace(candidate.Content)
	if match := memoryPreferenceFactPattern.FindStringSubmatch(content); len(match) == 2 {
		object := strings.TrimSpace(match[1])
		object = strings.TrimPrefix(object, "喝")
		if object != "" && !containsAnyText(object, []string{"但是", "不过", "除了", "因为", "如果", "的时候"}) {
			return "preference:" + object
		}
	}
	return ""
}

func (s *MemoryGroupStore) captureMemoryFact(ctx context.Context, scope string, candidate stableMemoryCandidate, eventID string, observedAt time.Time) error {
	key := stableMemoryFactKey(candidate)
	metadata := MemoryMetadata{Source: "auto_capture", Kind: candidate.Kind, Confidence: 0.88, Importance: candidate.Importance}
	if key == "" {
		_, _, err := s.AddMemoryWithMetadata(ctx, scope, candidate.Content, metadata)
		return err
	}
	now := s.now().UTC()
	if observedAt.IsZero() || observedAt.After(now) {
		observedAt = now
	}
	scopeDigest := s.digest("scope", scope)
	factDigest := s.digestBytes("memory-fact", scopeDigest, []byte(key))
	eventDigest := s.digestBytes("memory-event", scopeDigest, []byte(eventID))
	contentDigest := s.digestBytes("memory", scopeDigest, []byte(candidate.Content))
	tx, err := s.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Include legacy automatic captures so the first explicit update also
	// supersedes facts written before fact keys were introduced.
	rows, err := tx.QueryContext(ctx, `SELECT id, content_cipher, kind, fact_key_digest,
		COALESCE(observed_at, created_at), source_event_digest
		FROM agent_memories WHERE scope_digest=? AND source='auto_capture'
		AND (fact_key_digest=? OR fact_key_digest IS NULL)`, scopeDigest, factDigest)
	if err != nil {
		return err
	}
	var replace []string
	stale := false
	for rows.Next() {
		var id, kind, at string
		var cipher, storedKey, storedEvent []byte
		if err = rows.Scan(&id, &cipher, &kind, &storedKey, &at, &storedEvent); err != nil {
			rows.Close()
			return err
		}
		if len(storedKey) == 0 {
			plain, decryptErr := s.runtime.decrypt(cipher)
			if decryptErr != nil {
				rows.Close()
				return decryptErr
			}
			if stableMemoryFactKey(stableMemoryCandidate{Content: string(plain), Kind: kind}) != key {
				continue
			}
		}
		storedAt, parseErr := time.Parse(time.RFC3339Nano, at)
		if parseErr == nil && storedAt.After(observedAt) {
			stale = true
		}
		if eventID != "" && len(storedEvent) > 0 && subtle.ConstantTimeCompare(storedEvent, eventDigest) == 1 {
			stale = true
		}
		replace = append(replace, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil || stale {
		return err
	}
	personaID, scopeKind, scopeReference := parsePersonaMemoryScope(scope)
	cipher, err := s.runtime.encrypt([]byte(candidate.Content))
	if err != nil {
		return err
	}
	var referenceCipher []byte
	if scopeReference != "" {
		referenceCipher, err = s.runtime.encrypt([]byte(scopeReference))
		if err != nil {
			return err
		}
	}
	id, err := secureRecordID("mem")
	if err != nil {
		return err
	}
	// Never overwrite a manually curated record with an automatic capture.
	var existingSource string
	err = tx.QueryRowContext(ctx, "SELECT id,source FROM agent_memories WHERE scope_digest=? AND content_digest=?", scopeDigest, contentDigest).Scan(&id, &existingSource)
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if err == nil && existingSource != "auto_capture" {
		return nil
	}
	for _, oldID := range replace {
		if _, err = tx.ExecContext(ctx, `UPDATE agent_memories SET expires_at=?
			WHERE id=? AND (expires_at IS NULL OR expires_at>?)`, formatStoreTime(now), oldID, formatStoreTime(now)); err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO agent_memories
		(id,scope_digest,content_cipher,content_digest,source,kind,confidence,importance,
		persona_id,scope_kind,scope_ref_cipher,created_at,updated_at,fact_key_digest,source_event_digest,observed_at)
		VALUES (?,?,?,?,'auto_capture',?,0.88,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(scope_digest,content_digest) DO UPDATE SET expires_at=NULL,updated_at=excluded.updated_at,
		fact_key_digest=excluded.fact_key_digest,source_event_digest=excluded.source_event_digest,observed_at=excluded.observed_at`,
		id, scopeDigest, cipher, contentDigest, candidate.Kind, candidate.Importance,
		nullableString(personaID), nullableString(scopeKind), nullableBytes(referenceCipher), formatStoreTime(now), formatStoreTime(now),
		factDigest, nullableBytes(eventDigest), formatStoreTime(observedAt))
	if err != nil {
		return err
	}
	return tx.Commit()
}
