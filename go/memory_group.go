package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	maxMemoryQueryLimit     = 100
	maxGroupEventQueryLimit = 20000
)

type MemoryGroupStore struct {
	runtime     *AgentRuntime
	identityKey []byte
	now         func() time.Time
}

type RecalledMemory struct {
	ID               string     `json:"id"`
	PersonaID        string     `json:"personaId,omitempty"`
	ScopeKind        string     `json:"scopeKind,omitempty"`
	ScopeReference   string     `json:"scopeReference,omitempty"`
	UntrustedContent string     `json:"content"`
	Source           string     `json:"source"`
	Kind             string     `json:"kind"`
	Confidence       float64    `json:"confidence"`
	Importance       float64    `json:"importance"`
	AccessCount      int        `json:"accessCount"`
	LastAccessedAt   *time.Time `json:"lastAccessedAt,omitempty"`
	ExpiresAt        *time.Time `json:"expiresAt,omitempty"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
}

type MemoryMetadata struct {
	Source     string
	Kind       string
	Confidence float64
	Importance float64
	ExpiresAt  *time.Time
}

type GroupEventInput struct {
	ID                string
	Conversation      string
	Sender            string
	SenderDisplayName string
	PersonaID         string
	Role              string
	Text              string
	MessageID         string
	ThreadKey         string
	ReplyTo           *transportReplyReference
	OccurredAt        time.Time
	LegacySource      string
	LegacyID          string
}

type RecalledGroupEvent struct {
	ID                       string
	SenderRef                string
	SenderDisplayName        string
	PersonaID                string
	Role                     string
	UntrustedText            string
	MessageID                string
	ThreadKey                string
	ReplyToMessageID         string
	ReplyToSenderRef         string
	ReplyToSenderDisplayName string
	UntrustedQuotedText      string
	OccurredAt               time.Time
}

type RecalledEpisode struct {
	ID         string    `json:"id"`
	PersonaID  string    `json:"personaId"`
	ThreadKey  string    `json:"threadKey,omitempty"`
	Summary    string    `json:"summary"`
	EventCount int       `json:"eventCount"`
	StartedAt  time.Time `json:"startedAt"`
	EndedAt    time.Time `json:"endedAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type RelationshipState struct {
	Stage            string             `json:"stage"`
	Intimacy         float64            `json:"intimacy"`
	AutoIntimacy     float64            `json:"autoIntimacy"`
	IntimacyLocked   bool               `json:"intimacyLocked"`
	InteractionCount int                `json:"interactionCount"`
	AddressedCount   int                `json:"addressedCount"`
	ReplyCount       int                `json:"replyCount"`
	LastInteraction  time.Time          `json:"lastInteraction"`
	LastReply        time.Time          `json:"lastReply,omitempty"`
	Pulse            *RelationshipPulse `json:"pulse,omitempty"`
}

// RelationshipPulse is derived from actual interactions and memory use. It is
// an internal tendency, not a claim about what the other person feels.
type RelationshipPulse struct {
	Ready                 bool    `json:"ready"`
	OutputReflow          float64 `json:"outputReflow"`
	MemoryResonance       float64 `json:"memoryResonance"`
	RoutineExpectation    float64 `json:"routineExpectation"`
	Longing               float64 `json:"longing"`
	Sharing               float64 `json:"sharing"`
	BucketHealth          float64 `json:"bucketHealth"`
	MemoryCount           int     `json:"memoryCount"`
	MemoryKinds           int     `json:"memoryKinds"`
	RecentInteractions    int     `json:"recentInteractions"`
	ReplyCount            int     `json:"replyCount"`
	TypicalGapHours       float64 `json:"typicalGapHours"`
	HoursSinceInteraction float64 `json:"hoursSinceInteraction"`
	PreferredHour         int     `json:"preferredHour"`
	Evidence              string  `json:"evidence"`
}

type RelationshipIdentity struct {
	PersonaID         string
	ConversationRef   string
	SenderRef         string
	SenderDisplayName string
}

type ManagedRelationship struct {
	ID                string            `json:"id"`
	PersonaID         string            `json:"personaId"`
	ConversationRef   string            `json:"conversationRef"`
	SenderDisplayName string            `json:"senderDisplayName"`
	State             RelationshipState `json:"state"`
}

func NewMemoryGroupStore(runtime *AgentRuntime, identityKey []byte) (*MemoryGroupStore, error) {
	if runtime == nil || runtime.db == nil || runtime.aead == nil {
		return nil, errors.New("runtime database and encryption are required")
	}
	if len(identityKey) != sha256.Size {
		return nil, errors.New("identity key must contain exactly 32 bytes")
	}
	key := append([]byte(nil), identityKey...)
	return &MemoryGroupStore{
		runtime:     runtime,
		identityKey: key,
		now:         time.Now,
	}, nil
}

func (s *MemoryGroupStore) InitSchema(ctx context.Context) error {
	_, err := s.runtime.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS agent_memories (
			id TEXT PRIMARY KEY,
			scope_digest BLOB NOT NULL,
			content_cipher BLOB NOT NULL,
			content_digest BLOB NOT NULL,
			legacy_source TEXT,
			legacy_id TEXT,
			source TEXT NOT NULL DEFAULT 'manual',
			kind TEXT NOT NULL DEFAULT 'fact',
			confidence REAL NOT NULL DEFAULT 1,
			importance REAL NOT NULL DEFAULT 0.5,
			access_count INTEGER NOT NULL DEFAULT 0,
			last_accessed_at TEXT,
			expires_at TEXT,
			persona_id TEXT,
			scope_kind TEXT,
			scope_ref_cipher BLOB,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(scope_digest, content_digest)
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_memories_legacy
			ON agent_memories(legacy_source, legacy_id)
			WHERE legacy_source IS NOT NULL AND legacy_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_agent_memories_scope_updated
			ON agent_memories(scope_digest, updated_at DESC);

		CREATE TABLE IF NOT EXISTS conversation_events (
			id TEXT PRIMARY KEY,
			conversation_digest BLOB NOT NULL,
			sender_digest BLOB NOT NULL,
			sender_ref_cipher BLOB,
			sender_display_cipher BLOB,
			persona_id_cipher BLOB,
			role TEXT NOT NULL,
			text_cipher BLOB NOT NULL,
			message_id_cipher BLOB,
			thread_key_cipher BLOB,
			reply_to_message_id_cipher BLOB,
			reply_to_sender_ref_cipher BLOB,
			reply_to_sender_display_cipher BLOB,
			quoted_text_cipher BLOB,
			occurred_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			legacy_source TEXT,
			legacy_id TEXT
		);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_conversation_events_legacy
			ON conversation_events(legacy_source, legacy_id)
			WHERE legacy_source IS NOT NULL AND legacy_id IS NOT NULL;
		CREATE INDEX IF NOT EXISTS idx_conversation_events_recent
			ON conversation_events(conversation_digest, occurred_at DESC);
		CREATE INDEX IF NOT EXISTS idx_conversation_events_expiry
			ON conversation_events(expires_at);

		CREATE TABLE IF NOT EXISTS conversation_state (
			conversation_digest BLOB PRIMARY KEY,
			last_bot_reply_ack_at TEXT,
			updated_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS relationship_events (
			event_id TEXT PRIMARY KEY,
			conversation_digest BLOB NOT NULL,
			sender_digest BLOB NOT NULL,
			occurred_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS relationship_reply_events (
			delivery_id TEXT PRIMARY KEY,
			conversation_digest BLOB NOT NULL,
			sender_digest BLOB NOT NULL,
			occurred_at TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS member_relationship_state (
			conversation_digest BLOB NOT NULL,
			sender_digest BLOB NOT NULL,
			relationship_id TEXT NOT NULL DEFAULT '',
			persona_id TEXT NOT NULL DEFAULT '',
			conversation_ref_cipher BLOB,
			sender_ref_cipher BLOB,
			sender_display_cipher BLOB,
			interaction_count INTEGER NOT NULL DEFAULT 0,
			addressed_count INTEGER NOT NULL DEFAULT 0,
			reply_count INTEGER NOT NULL DEFAULT 0,
			manual_intimacy REAL,
			intimacy_locked INTEGER NOT NULL DEFAULT 0,
			last_interaction_at TEXT NOT NULL,
			last_reply_at TEXT,
			updated_at TEXT NOT NULL,
			PRIMARY KEY (conversation_digest, sender_digest)
		);

		CREATE TABLE IF NOT EXISTS conversation_episodes (
			id TEXT PRIMARY KEY,
			conversation_digest BLOB NOT NULL,
			persona_id TEXT NOT NULL,
			thread_key_cipher BLOB,
			summary_cipher BLOB NOT NULL,
			event_count INTEGER NOT NULL,
			started_at TEXT NOT NULL,
			ended_at TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			UNIQUE(conversation_digest, persona_id, id)
		);
	`)
	if err != nil {
		return err
	}
	for _, column := range []struct{ name, definition string }{
		{"sender_ref_cipher", "BLOB"},
		{"sender_display_cipher", "BLOB"},
		{"persona_id_cipher", "BLOB"},
		{"message_id_cipher", "BLOB"},
		{"thread_key_cipher", "BLOB"},
		{"reply_to_message_id_cipher", "BLOB"},
		{"reply_to_sender_ref_cipher", "BLOB"},
		{"reply_to_sender_display_cipher", "BLOB"},
		{"quoted_text_cipher", "BLOB"},
	} {
		if err = ensureRuntimeColumn(s.runtime.db, "conversation_events", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{
		{"relationship_id", "TEXT NOT NULL DEFAULT ''"},
		{"persona_id", "TEXT NOT NULL DEFAULT ''"},
		{"conversation_ref_cipher", "BLOB"},
		{"sender_ref_cipher", "BLOB"},
		{"sender_display_cipher", "BLOB"},
		{"manual_intimacy", "REAL"},
		{"intimacy_locked", "INTEGER NOT NULL DEFAULT 0"},
		{"reply_count", "INTEGER NOT NULL DEFAULT 0"},
		{"last_reply_at", "TEXT"},
	} {
		if err = ensureRuntimeColumn(s.runtime.db, "member_relationship_state", column.name, column.definition); err != nil {
			return err
		}
	}
	for _, column := range []struct{ name, definition string }{
		{"source", "TEXT NOT NULL DEFAULT 'manual'"},
		{"kind", "TEXT NOT NULL DEFAULT 'fact'"},
		{"confidence", "REAL NOT NULL DEFAULT 1"},
		{"importance", "REAL NOT NULL DEFAULT 0.5"},
		{"access_count", "INTEGER NOT NULL DEFAULT 0"},
		{"last_accessed_at", "TEXT"},
		{"expires_at", "TEXT"},
		{"persona_id", "TEXT"},
		{"scope_kind", "TEXT"},
		{"scope_ref_cipher", "BLOB"},
	} {
		if err = ensureRuntimeColumn(s.runtime.db, "agent_memories", column.name, column.definition); err != nil {
			return err
		}
	}
	// 机器人对话级情绪底色:被夸/被怼/办砸事的短寿命状态,只影响语气。
	for _, column := range []struct{ name, definition string }{
		{"bot_mood", "TEXT NOT NULL DEFAULT ''"},
		{"bot_mood_updated_at", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err = ensureRuntimeColumn(s.runtime.db, "conversation_state", column.name, column.definition); err != nil {
			return err
		}
	}
	_, err = s.runtime.db.ExecContext(ctx, `
		CREATE INDEX IF NOT EXISTS idx_agent_memories_scope_relevance
			ON agent_memories(scope_digest, importance DESC, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_agent_memories_expiry ON agent_memories(expires_at);
		CREATE INDEX IF NOT EXISTS idx_agent_memories_persona
			ON agent_memories(persona_id, scope_kind, updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_relationship_state_recent
			ON member_relationship_state(conversation_digest, last_interaction_at DESC);
		CREATE INDEX IF NOT EXISTS idx_relationship_reply_events_lookup
			ON relationship_reply_events(conversation_digest, sender_digest, occurred_at DESC);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_relationship_state_id
			ON member_relationship_state(relationship_id) WHERE relationship_id <> '';
		CREATE INDEX IF NOT EXISTS idx_conversation_episodes_recent
			ON conversation_episodes(conversation_digest, persona_id, ended_at DESC);
	`)
	if err == nil {
		_, err = s.runtime.db.ExecContext(ctx, `
			UPDATE member_relationship_state
			SET relationship_id = 'rel_' || lower(hex(conversation_digest) || hex(sender_digest))
			WHERE relationship_id = ''
		`)
	}
	if err != nil {
		return err
	}
	return s.backfillRelationshipIdentities(ctx)
}

func (s *MemoryGroupStore) backfillRelationshipIdentities(ctx context.Context) error {
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT state.rowid,
			(SELECT events.persona_id_cipher
			 FROM relationship_events relations
			 JOIN conversation_events events ON events.id = relations.event_id
			 WHERE relations.conversation_digest = state.conversation_digest
			   AND relations.sender_digest = state.sender_digest
			   AND events.persona_id_cipher IS NOT NULL
			 ORDER BY relations.occurred_at DESC LIMIT 1),
			(SELECT events.sender_ref_cipher
			 FROM relationship_events relations
			 JOIN conversation_events events ON events.id = relations.event_id
			 WHERE relations.conversation_digest = state.conversation_digest
			   AND relations.sender_digest = state.sender_digest
			   AND events.persona_id_cipher IS NOT NULL
			 ORDER BY relations.occurred_at DESC LIMIT 1),
			(SELECT events.sender_display_cipher
			 FROM relationship_events relations
			 JOIN conversation_events events ON events.id = relations.event_id
			 WHERE relations.conversation_digest = state.conversation_digest
			   AND relations.sender_digest = state.sender_digest
			   AND events.persona_id_cipher IS NOT NULL
			 ORDER BY relations.occurred_at DESC LIMIT 1)
		FROM member_relationship_state state
		WHERE state.persona_id = ''
	`)
	if err != nil {
		return err
	}
	type relationshipIdentityBackfill struct {
		rowID                    int64
		persona, sender, display []byte
		personaID                string
	}
	pending := make([]relationshipIdentityBackfill, 0)
	for rows.Next() {
		var item relationshipIdentityBackfill
		if err = rows.Scan(&item.rowID, &item.persona, &item.sender, &item.display); err != nil {
			rows.Close()
			return err
		}
		if len(item.persona) == 0 {
			continue
		}
		plaintext, decryptErr := s.runtime.decrypt(item.persona)
		if decryptErr != nil || strings.TrimSpace(string(plaintext)) == "" {
			continue
		}
		item.personaID = strings.TrimSpace(string(plaintext))
		pending = append(pending, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	tx, err := s.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range pending {
		if _, err = tx.ExecContext(ctx, `
			UPDATE member_relationship_state
			SET persona_id = ?, sender_ref_cipher = COALESCE(sender_ref_cipher, ?),
				sender_display_cipher = COALESCE(sender_display_cipher, ?)
			WHERE rowid = ? AND persona_id = ''
		`, item.personaID, nullableBytes(item.sender), nullableBytes(item.display), item.rowID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *MemoryGroupStore) MaybeRefreshEpisodeSummary(
	ctx context.Context, conversation, personaID, threadKey string,
	interval, window int, ttl time.Duration,
) error {
	if interval <= 0 || window <= 0 || ttl <= 0 || strings.TrimSpace(personaID) == "" {
		return nil
	}
	var count int
	if err := s.runtime.db.QueryRowContext(ctx, `
		SELECT count(*) FROM conversation_events
		WHERE conversation_digest = ? AND expires_at > ?
	`, s.digest("conversation", conversation), formatStoreTime(s.now().UTC())).Scan(&count); err != nil {
		return err
	}
	if count == 0 || count%interval != 0 {
		return nil
	}
	events, err := s.RecentPersonaGroupEvents(ctx, conversation, personaID, window)
	if err != nil || len(events) == 0 {
		return err
	}
	var summary strings.Builder
	for _, event := range events {
		line := conversationEventLine(event, truncateRunes(strings.TrimSpace(event.UntrustedText), 180))
		if line == "" {
			continue
		}
		if summary.Len() > 0 {
			summary.WriteString("；")
		}
		summary.WriteString(line)
		if summary.Len() >= 1800 {
			break
		}
	}
	if strings.TrimSpace(summary.String()) == "" {
		return nil
	}
	endedAt := events[len(events)-1].OccurredAt
	startedAt := events[0].OccurredAt
	if startedAt.After(endedAt) {
		startedAt, endedAt = endedAt, startedAt
	}
	threadKeyCipher, err := s.runtime.encrypt([]byte(strings.TrimSpace(threadKey)))
	if err != nil {
		return err
	}
	summaryCipher, err := s.runtime.encrypt([]byte(summary.String()))
	if err != nil {
		return err
	}
	id := "episode-" + hex.EncodeToString(s.digestBytes("episode", []byte(conversation), []byte(personaID), []byte(threadKey)))
	now := s.now().UTC()
	_, err = s.runtime.db.ExecContext(ctx, `
		INSERT INTO conversation_episodes (
			id, conversation_digest, persona_id, thread_key_cipher, summary_cipher,
			event_count, started_at, ended_at, expires_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET thread_key_cipher = excluded.thread_key_cipher,
			summary_cipher = excluded.summary_cipher, event_count = excluded.event_count,
			started_at = excluded.started_at, ended_at = excluded.ended_at,
			expires_at = excluded.expires_at, updated_at = excluded.updated_at
	`, id, s.digest("conversation", conversation), personaID, nullableBytes(threadKeyCipher), summaryCipher,
		len(events), formatStoreTime(startedAt), formatStoreTime(endedAt), formatStoreTime(now.Add(ttl)),
		formatStoreTime(now), formatStoreTime(now))
	return err
}

func (s *MemoryGroupStore) RecentPersonaEpisodes(
	ctx context.Context, conversation, personaID string, limit int,
) ([]RecalledEpisode, error) {
	limit = normalizeStoreLimit(limit)
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT id, persona_id, thread_key_cipher, summary_cipher, event_count,
			started_at, ended_at, updated_at
		FROM conversation_episodes
		WHERE conversation_digest = ? AND persona_id = ? AND expires_at > ?
		ORDER BY ended_at DESC, id DESC LIMIT ?
	`, s.digest("conversation", conversation), strings.TrimSpace(personaID), formatStoreTime(s.now().UTC()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]RecalledEpisode, 0, limit)
	for rows.Next() {
		var item RecalledEpisode
		var threadCipher, summaryCipher []byte
		var startedAt, endedAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.PersonaID, &threadCipher, &summaryCipher, &item.EventCount, &startedAt, &endedAt, &updatedAt); err != nil {
			return nil, err
		}
		plaintext, err := s.runtime.decrypt(summaryCipher)
		if err != nil {
			return nil, err
		}
		item.Summary = string(plaintext)
		if len(threadCipher) > 0 {
			plaintext, err = s.runtime.decrypt(threadCipher)
			if err != nil {
				return nil, err
			}
			item.ThreadKey = string(plaintext)
		}
		if item.StartedAt, err = parseStoreTime(startedAt); err != nil {
			return nil, err
		}
		if item.EndedAt, err = parseStoreTime(endedAt); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = parseStoreTime(updatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func (s *MemoryGroupStore) AddMemory(ctx context.Context, scope, content string) (RecalledMemory, bool, error) {
	return s.AddMemoryWithMetadata(ctx, scope, content, MemoryMetadata{
		Source: "manual", Kind: "fact", Confidence: 1, Importance: 0.7,
	})
}

func (s *MemoryGroupStore) AddMemoryWithMetadata(
	ctx context.Context,
	scope, content string,
	metadata MemoryMetadata,
) (RecalledMemory, bool, error) {
	return s.addMemory(ctx, scope, content, "", "", metadata)
}

func (s *MemoryGroupStore) ImportLegacyMemory(ctx context.Context, scope, source, legacyID, content string) (RecalledMemory, bool, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(legacyID) == "" {
		return RecalledMemory{}, false, errors.New("legacy source and id are required")
	}
	return s.addMemory(ctx, scope, content, source, legacyID, MemoryMetadata{
		Source: source, Kind: "legacy", Confidence: 1, Importance: 0.5,
	})
}

func (s *MemoryGroupStore) addMemory(
	ctx context.Context,
	scope, content, legacySource, legacyID string,
	metadata MemoryMetadata,
) (RecalledMemory, bool, error) {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(content) == "" {
		return RecalledMemory{}, false, errors.New("memory scope and content are required")
	}
	metadata = normalizeMemoryMetadata(metadata)
	personaID, scopeKind, scopeReference := parsePersonaMemoryScope(scope)
	var scopeReferenceCipher []byte
	if scopeReference != "" {
		var encryptErr error
		scopeReferenceCipher, encryptErr = s.runtime.encrypt([]byte(scopeReference))
		if encryptErr != nil {
			return RecalledMemory{}, false, encryptErr
		}
	}
	scopeDigest := s.digest("scope", scope)
	contentDigest := s.digestBytes("memory", scopeDigest, []byte(content))

	if legacySource != "" {
		existing, found, err := s.memoryByLegacyID(ctx, legacySource, legacyID)
		if err != nil {
			return RecalledMemory{}, false, err
		}
		if found {
			if subtle.ConstantTimeCompare(existing.scopeDigest, scopeDigest) != 1 {
				return RecalledMemory{}, false, errors.New("legacy memory belongs to another scope")
			}
			return existing.memory, false, nil
		}
	}

	ciphertext, err := s.runtime.encrypt([]byte(content))
	if err != nil {
		return RecalledMemory{}, false, err
	}
	id, err := secureRecordID("mem")
	if err != nil {
		return RecalledMemory{}, false, err
	}
	now := s.now().UTC()
	var sourceArg, legacyIDArg any
	if legacySource != "" {
		sourceArg = legacySource
		legacyIDArg = legacyID
	}
	result, err := s.runtime.db.ExecContext(ctx, `
		INSERT INTO agent_memories (
			id, scope_digest, content_cipher, content_digest,
			legacy_source, legacy_id, source, kind, confidence, importance, expires_at,
			persona_id, scope_kind, scope_ref_cipher, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, id, scopeDigest, ciphertext, contentDigest, sourceArg, legacyIDArg,
		metadata.Source, metadata.Kind, metadata.Confidence, metadata.Importance,
		formatOptionalStoreTime(metadata.ExpiresAt), nullableString(personaID), nullableString(scopeKind),
		nullableBytes(scopeReferenceCipher), formatStoreTime(now), formatStoreTime(now))
	if err != nil {
		return RecalledMemory{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return RecalledMemory{}, false, err
	}
	if inserted == 1 {
		return RecalledMemory{
			ID: id, PersonaID: personaID, ScopeKind: scopeKind, ScopeReference: scopeReference,
			UntrustedContent: content, Source: metadata.Source, Kind: metadata.Kind,
			Confidence: metadata.Confidence, Importance: metadata.Importance,
			ExpiresAt: metadata.ExpiresAt, CreatedAt: now, UpdatedAt: now,
		}, true, nil
	}

	memory, found, err := s.memoryByDigest(ctx, scopeDigest, contentDigest)
	if err != nil {
		return RecalledMemory{}, false, err
	}
	if !found && legacySource != "" {
		legacy, legacyFound, legacyErr := s.memoryByLegacyID(ctx, legacySource, legacyID)
		if legacyErr != nil {
			return RecalledMemory{}, false, legacyErr
		}
		if legacyFound && subtle.ConstantTimeCompare(legacy.scopeDigest, scopeDigest) == 1 {
			return legacy.memory, false, nil
		}
	}
	if !found {
		return RecalledMemory{}, false, errors.New("memory insert conflict could not be resolved")
	}
	return memory, false, nil
}

func parsePersonaMemoryScope(scope string) (string, string, string) {
	const prefix = "persona:"
	if !strings.HasPrefix(scope, prefix) {
		return "", "", ""
	}
	remainder := strings.TrimPrefix(scope, prefix)
	separator := strings.IndexByte(remainder, ':')
	if separator < 1 {
		return "", "", ""
	}
	personaID := strings.TrimSpace(remainder[:separator])
	remainder = remainder[separator+1:]
	separator = strings.IndexByte(remainder, ':')
	if separator < 1 {
		return "", "", ""
	}
	scopeKind := strings.TrimSpace(remainder[:separator])
	scopeReference := strings.TrimSpace(remainder[separator+1:])
	if personaID == "" || scopeKind == "" || scopeReference == "" {
		return "", "", ""
	}
	return personaID, scopeKind, scopeReference
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (s *MemoryGroupStore) ListMemories(ctx context.Context, scope string, limit int) ([]RecalledMemory, error) {
	return s.searchMemories(ctx, scope, "", limit)
}

func (s *MemoryGroupStore) SearchMemories(ctx context.Context, scope, query string, limit int) ([]RecalledMemory, error) {
	return s.searchMemories(ctx, scope, strings.ToLower(strings.TrimSpace(query)), limit)
}

func (s *MemoryGroupStore) searchMemories(ctx context.Context, scope, normalizedQuery string, limit int) ([]RecalledMemory, error) {
	if strings.TrimSpace(scope) == "" {
		return nil, errors.New("memory scope is required")
	}
	limit = normalizeStoreLimit(limit)
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT id, content_cipher, source, kind, confidence, importance, access_count,
			last_accessed_at, expires_at, created_at, updated_at
		FROM agent_memories
		WHERE scope_digest = ? AND (expires_at IS NULL OR expires_at > ?)
		ORDER BY updated_at DESC, id DESC
	`, s.digest("scope", scope), formatStoreTime(s.now().UTC()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scoredMemory struct {
		memory RecalledMemory
		score  float64
	}
	scored := make([]scoredMemory, 0, limit)
	for rows.Next() {
		memory, err := s.scanMemory(rows)
		if err != nil {
			return nil, err
		}
		score, matched := memoryRelevanceScore(memory, normalizedQuery, s.now().UTC())
		if matched {
			scored = append(scored, scoredMemory{memory: memory, score: score})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].memory.UpdatedAt.After(scored[j].memory.UpdatedAt)
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	memories := make([]RecalledMemory, 0, len(scored))
	accessedAt := formatStoreTime(s.now().UTC())
	for _, item := range scored {
		memories = append(memories, item.memory)
		_, _ = s.runtime.db.ExecContext(ctx, `
			UPDATE agent_memories SET access_count = access_count + 1, last_accessed_at = ? WHERE id = ?
		`, accessedAt, item.memory.ID)
	}
	return memories, nil
}

func normalizeMemoryMetadata(value MemoryMetadata) MemoryMetadata {
	value.Source = strings.TrimSpace(value.Source)
	if value.Source == "" {
		value.Source = "manual"
	}
	value.Kind = strings.TrimSpace(value.Kind)
	if value.Kind == "" {
		value.Kind = "fact"
	}
	value.Confidence = clampProbability(value.Confidence)
	if value.Confidence == 0 {
		value.Confidence = 0.5
	}
	value.Importance = clampProbability(value.Importance)
	if value.Importance == 0 {
		value.Importance = 0.5
	}
	if value.ExpiresAt != nil {
		expires := value.ExpiresAt.UTC()
		value.ExpiresAt = &expires
	}
	return value
}

func memoryRelevanceScore(memory RecalledMemory, query string, now time.Time) (float64, bool) {
	recency := math.Exp(-math.Max(0, now.Sub(memory.UpdatedAt).Hours()) / (24 * 90))
	base := memory.Confidence*0.12 + memory.Importance*0.18 + recency*0.10
	if query == "" {
		return base, true
	}
	content := strings.ToLower(strings.TrimSpace(memory.UntrustedContent))
	if content == "" {
		return 0, false
	}
	if memory.Kind == "address" && addressRecallQuestion(query) {
		return base + 1, true
	}
	queryTokens := memorySearchTokens(query)
	contentTokens := memorySearchTokens(content)
	intersection := 0
	for token := range queryTokens {
		if _, ok := contentTokens[token]; ok {
			intersection++
		}
	}
	contains := strings.Contains(content, query) || strings.Contains(query, content)
	if intersection == 0 && !contains {
		return 0, false
	}
	queryCoverage := float64(intersection) / float64(maxInt(1, len(queryTokens)))
	contentCoverage := float64(intersection) / float64(maxInt(1, len(contentTokens)))
	score := base + queryCoverage*0.38 + contentCoverage*0.17
	if contains {
		score += 0.25
	}
	return score, true
}

func memorySearchTokens(value string) map[string]struct{} {
	result := map[string]struct{}{}
	var ascii strings.Builder
	flushASCII := func() {
		if token := ascii.String(); len(token) >= 2 {
			result[token] = struct{}{}
		}
		ascii.Reset()
	}
	cjk := []rune{}
	flushCJK := func() {
		for index := range cjk {
			if index+1 < len(cjk) {
				result[string(cjk[index:index+2])] = struct{}{}
			}
		}
		cjk = cjk[:0]
	}
	for _, valueRune := range []rune(strings.ToLower(value)) {
		switch {
		case valueRune <= unicode.MaxASCII && (unicode.IsLetter(valueRune) || unicode.IsDigit(valueRune)):
			flushCJK()
			ascii.WriteRune(valueRune)
		case unicode.In(valueRune, unicode.Han, unicode.Hiragana, unicode.Katakana, unicode.Hangul):
			flushASCII()
			cjk = append(cjk, valueRune)
		default:
			flushASCII()
			flushCJK()
		}
	}
	flushASCII()
	flushCJK()
	return result
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func (s *MemoryGroupStore) ForgetMemory(ctx context.Context, scope, id string) (bool, error) {
	if strings.TrimSpace(scope) == "" || strings.TrimSpace(id) == "" {
		return false, errors.New("memory scope and id are required")
	}
	result, err := s.runtime.db.ExecContext(ctx,
		"DELETE FROM agent_memories WHERE id = ? AND scope_digest = ?",
		id, s.digest("scope", scope),
	)
	if err != nil {
		return false, err
	}
	deleted, err := result.RowsAffected()
	return deleted == 1, err
}

func (s *MemoryGroupStore) ListPersonaMemories(
	ctx context.Context,
	personaID, scopeKind string,
	limit, offset int,
) ([]RecalledMemory, int, error) {
	personaID = strings.TrimSpace(personaID)
	scopeKind = strings.TrimSpace(scopeKind)
	if personaID == "" {
		return nil, 0, errors.New("persona is required")
	}
	if scopeKind != "" && scopeKind != "user" && scopeKind != "group" {
		return nil, 0, errors.New("scope kind is invalid")
	}
	limit = normalizeStoreLimit(limit)
	if offset < 0 {
		offset = 0
	}
	where := "persona_id = ? AND (expires_at IS NULL OR expires_at > ?)"
	args := []any{personaID, formatStoreTime(s.now().UTC())}
	if scopeKind != "" {
		where += " AND scope_kind = ?"
		args = append(args, scopeKind)
	}
	var total int
	if err := s.runtime.db.QueryRowContext(ctx, "SELECT count(*) FROM agent_memories WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	queryArgs := append(append([]any{}, args...), limit, offset)
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT id, content_cipher, source, kind, confidence, importance, access_count,
			last_accessed_at, expires_at, created_at, updated_at,
			persona_id, scope_kind, scope_ref_cipher
		FROM agent_memories WHERE `+where+`
		ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]RecalledMemory, 0, limit)
	for rows.Next() {
		memory, scanErr := s.scanManagedMemory(rows)
		if scanErr != nil {
			return nil, 0, scanErr
		}
		items = append(items, memory)
	}
	return items, total, rows.Err()
}

func (s *MemoryGroupStore) UpdateMemory(
	ctx context.Context,
	scope, id, content string,
	metadata MemoryMetadata,
) (RecalledMemory, bool, error) {
	scope = strings.TrimSpace(scope)
	id = strings.TrimSpace(id)
	content = strings.TrimSpace(content)
	if scope == "" || id == "" || content == "" {
		return RecalledMemory{}, false, errors.New("memory scope, id and content are required")
	}
	metadata = normalizeMemoryMetadata(metadata)
	scopeDigest := s.digest("scope", scope)
	contentDigest := s.digestBytes("memory", scopeDigest, []byte(content))
	ciphertext, err := s.runtime.encrypt([]byte(content))
	if err != nil {
		return RecalledMemory{}, false, err
	}
	now := s.now().UTC()
	result, err := s.runtime.db.ExecContext(ctx, `
		UPDATE agent_memories SET content_cipher = ?, content_digest = ?, source = ?, kind = ?,
			confidence = ?, importance = ?, expires_at = ?, updated_at = ?
		WHERE id = ? AND scope_digest = ?
	`, ciphertext, contentDigest, metadata.Source, metadata.Kind, metadata.Confidence,
		metadata.Importance, formatOptionalStoreTime(metadata.ExpiresAt), formatStoreTime(now), id, scopeDigest)
	if err != nil {
		return RecalledMemory{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return RecalledMemory{}, false, err
	}
	personaID, scopeKind, scopeReference := parsePersonaMemoryScope(scope)
	return RecalledMemory{
		ID: id, PersonaID: personaID, ScopeKind: scopeKind, ScopeReference: scopeReference,
		UntrustedContent: content, Source: metadata.Source, Kind: metadata.Kind,
		Confidence: metadata.Confidence, Importance: metadata.Importance,
		ExpiresAt: metadata.ExpiresAt, UpdatedAt: now,
	}, true, nil
}

func (s *MemoryGroupStore) ObserveGroupEvent(ctx context.Context, input GroupEventInput, ttl time.Duration) (RecalledGroupEvent, bool, error) {
	if strings.TrimSpace(input.Conversation) == "" || strings.TrimSpace(input.Text) == "" {
		return RecalledGroupEvent{}, false, errors.New("conversation and text are required")
	}
	if ttl <= 0 {
		return RecalledGroupEvent{}, false, errors.New("conversation event ttl must be positive")
	}
	if (input.LegacySource == "") != (input.LegacyID == "") {
		return RecalledGroupEvent{}, false, errors.New("legacy source and id must be provided together")
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = s.now()
	}
	if input.ID == "" {
		var err error
		input.ID, err = secureRecordID("evt")
		if err != nil {
			return RecalledGroupEvent{}, false, err
		}
	}
	ciphertext, err := s.runtime.encrypt([]byte(input.Text))
	if err != nil {
		return RecalledGroupEvent{}, false, err
	}
	encrypted := make([][]byte, 9)
	optional := []string{
		input.Sender, input.SenderDisplayName, input.PersonaID, input.MessageID, input.ThreadKey,
		"", "", "", "",
	}
	if input.ReplyTo != nil {
		optional[5] = input.ReplyTo.MessageID
		optional[6] = input.ReplyTo.SenderKey
		optional[7] = input.ReplyTo.SenderDisplayName
		optional[8] = input.ReplyTo.Text
	}
	for index, value := range optional {
		if strings.TrimSpace(value) == "" {
			continue
		}
		encrypted[index], err = s.runtime.encrypt([]byte(value))
		if err != nil {
			return RecalledGroupEvent{}, false, err
		}
	}
	conversationDigest := s.digest("conversation", input.Conversation)
	senderDigest := s.digest("sender", input.Sender)
	occurredAt := input.OccurredAt.UTC()
	var sourceArg, legacyIDArg any
	if input.LegacySource != "" {
		sourceArg = input.LegacySource
		legacyIDArg = input.LegacyID
	}
	result, err := s.runtime.db.ExecContext(ctx, `
		INSERT INTO conversation_events (
			id, conversation_digest, sender_digest, sender_ref_cipher, sender_display_cipher, persona_id_cipher,
			role, text_cipher, message_id_cipher, thread_key_cipher,
			reply_to_message_id_cipher, reply_to_sender_ref_cipher,
			reply_to_sender_display_cipher, quoted_text_cipher,
			occurred_at, expires_at, legacy_source, legacy_id
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING
	`, input.ID, conversationDigest, senderDigest, encrypted[0], encrypted[1], encrypted[2], input.Role, ciphertext,
		encrypted[3], encrypted[4], encrypted[5], encrypted[6], encrypted[7], encrypted[8],
		formatStoreTime(occurredAt), formatStoreTime(occurredAt.Add(ttl)), sourceArg, legacyIDArg)
	if err != nil {
		return RecalledGroupEvent{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return RecalledGroupEvent{}, false, err
	}
	if inserted == 1 {
		return RecalledGroupEvent{
			ID: input.ID, SenderRef: input.Sender, SenderDisplayName: input.SenderDisplayName, PersonaID: input.PersonaID,
			Role: input.Role, UntrustedText: input.Text, MessageID: input.MessageID,
			ThreadKey: input.ThreadKey, ReplyToMessageID: optional[5],
			ReplyToSenderRef: optional[6], ReplyToSenderDisplayName: optional[7],
			UntrustedQuotedText: optional[8], OccurredAt: occurredAt,
		}, true, nil
	}

	existing, found, err := s.groupEventByIdentity(ctx, input.ID, input.LegacySource, input.LegacyID)
	if err != nil {
		return RecalledGroupEvent{}, false, err
	}
	if !found {
		return RecalledGroupEvent{}, false, errors.New("conversation event conflict could not be resolved")
	}
	if subtle.ConstantTimeCompare(existing.conversationDigest, conversationDigest) != 1 {
		return RecalledGroupEvent{}, false, errors.New("conversation event belongs to another conversation")
	}
	return existing.event, false, nil
}

func (s *MemoryGroupStore) RecentGroupEvents(ctx context.Context, conversation string, limit int) ([]RecalledGroupEvent, error) {
	if strings.TrimSpace(conversation) == "" {
		return nil, errors.New("conversation is required")
	}
	if err := s.PruneExpiredGroupEvents(ctx); err != nil {
		return nil, err
	}
	limit = normalizeGroupEventLimit(limit)
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT id, sender_ref_cipher, sender_display_cipher, persona_id_cipher, role, text_cipher,
			message_id_cipher, thread_key_cipher, reply_to_message_id_cipher,
			reply_to_sender_ref_cipher, reply_to_sender_display_cipher, quoted_text_cipher, occurred_at
		FROM conversation_events
		WHERE conversation_digest = ? AND expires_at > ?
		ORDER BY occurred_at DESC, id DESC
		LIMIT ?
	`, s.digest("conversation", conversation), formatStoreTime(s.now().UTC()), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	reverse := make([]RecalledGroupEvent, 0, limit)
	for rows.Next() {
		var event RecalledGroupEvent
		var ciphertext []byte
		optional := make([][]byte, 9)
		var occurredAt string
		if err := rows.Scan(&event.ID, &optional[0], &optional[1], &optional[2], &event.Role, &ciphertext,
			&optional[3], &optional[4], &optional[5], &optional[6], &optional[7], &optional[8], &occurredAt); err != nil {
			return nil, err
		}
		plaintext, err := s.runtime.decrypt(ciphertext)
		if err != nil {
			return nil, fmt.Errorf("decrypt conversation event %s: %w", event.ID, err)
		}
		event.UntrustedText = string(plaintext)
		values := make([]string, 9)
		for index := range values {
			if len(optional[index]) == 0 {
				continue
			}
			decoded, decryptErr := s.runtime.decrypt(optional[index])
			if decryptErr != nil {
				return nil, fmt.Errorf("decrypt conversation metadata %s: %w", event.ID, decryptErr)
			}
			values[index] = string(decoded)
		}
		event.SenderRef, event.SenderDisplayName, event.PersonaID = values[0], values[1], values[2]
		event.MessageID, event.ThreadKey = values[3], values[4]
		event.ReplyToMessageID, event.ReplyToSenderRef = values[5], values[6]
		event.ReplyToSenderDisplayName, event.UntrustedQuotedText = values[7], values[8]
		event.OccurredAt, err = parseStoreTime(occurredAt)
		if err != nil {
			return nil, err
		}
		reverse = append(reverse, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for left, right := 0, len(reverse)-1; left < right; left, right = left+1, right-1 {
		reverse[left], reverse[right] = reverse[right], reverse[left]
	}
	return reverse, nil
}

func (s *MemoryGroupStore) RecentPersonaGroupEvents(
	ctx context.Context,
	conversation, personaID string,
	limit int,
) ([]RecalledGroupEvent, error) {
	limit = normalizeGroupEventLimit(limit)
	scanLimit := limit * 4
	if scanLimit < 100 {
		scanLimit = 100
	}
	if scanLimit > maxGroupEventQueryLimit {
		scanLimit = maxGroupEventQueryLimit
	}
	events, err := s.RecentGroupEvents(ctx, conversation, scanLimit)
	if err != nil {
		return nil, err
	}
	filtered := make([]RecalledGroupEvent, 0, min(limit, len(events)))
	for index := len(events) - 1; index >= 0 && len(filtered) < limit; index-- {
		if personaEventMatches(events[index].PersonaID, personaID) {
			filtered = append(filtered, events[index])
		}
	}
	for left, right := 0, len(filtered)-1; left < right; left, right = left+1, right-1 {
		filtered[left], filtered[right] = filtered[right], filtered[left]
	}
	return filtered, nil
}

func personaEventMatches(eventPersonaID, requestedPersonaID string) bool {
	eventPersonaID = strings.TrimSpace(eventPersonaID)
	requestedPersonaID = strings.TrimSpace(requestedPersonaID)
	if eventPersonaID == requestedPersonaID {
		return true
	}
	return eventPersonaID == "" && (requestedPersonaID == "" || requestedPersonaID == "doubao")
}

func (s *MemoryGroupStore) SearchGroupEvents(
	ctx context.Context,
	conversation, query string,
	scanLimit, resultLimit int,
) ([]RecalledGroupEvent, error) {
	queryTokens := memorySearchTokens(stripHistoryRecallWords(query))
	if len(queryTokens) == 0 || resultLimit <= 0 {
		return nil, nil
	}
	events, err := s.RecentGroupEvents(ctx, conversation, scanLimit)
	if err != nil {
		return nil, err
	}
	type scoredEvent struct {
		event RecalledGroupEvent
		score float64
	}
	now := s.now().UTC()
	scored := make([]scoredEvent, 0, resultLimit)
	for _, event := range events {
		contentTokens := memorySearchTokens(event.UntrustedText)
		intersection := 0
		for token := range queryTokens {
			if _, ok := contentTokens[token]; ok {
				intersection++
			}
		}
		if intersection == 0 {
			continue
		}
		coverage := float64(intersection) / float64(maxInt(1, len(queryTokens)))
		recency := math.Exp(-math.Max(0, now.Sub(event.OccurredAt).Hours()) / (24 * 180))
		scored = append(scored, scoredEvent{event: event, score: coverage + recency*0.15})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].event.OccurredAt.After(scored[j].event.OccurredAt)
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > resultLimit {
		scored = scored[:resultLimit]
	}
	result := make([]RecalledGroupEvent, 0, len(scored))
	for _, item := range scored {
		result = append(result, item.event)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result, nil
}

func (s *MemoryGroupStore) SearchPersonaGroupEvents(
	ctx context.Context,
	conversation, personaID, query string,
	scanLimit, resultLimit int,
) ([]RecalledGroupEvent, error) {
	queryTokens := memorySearchTokens(stripHistoryRecallWords(query))
	if len(queryTokens) == 0 || resultLimit <= 0 {
		return nil, nil
	}
	events, err := s.RecentPersonaGroupEvents(ctx, conversation, personaID, scanLimit)
	if err != nil {
		return nil, err
	}
	type scoredEvent struct {
		event RecalledGroupEvent
		score float64
	}
	now := s.now().UTC()
	scored := make([]scoredEvent, 0, resultLimit)
	for _, event := range events {
		contentTokens := memorySearchTokens(event.UntrustedText)
		intersection := 0
		for token := range queryTokens {
			if _, ok := contentTokens[token]; ok {
				intersection++
			}
		}
		if intersection > 0 {
			coverage := float64(intersection) / float64(maxInt(1, len(queryTokens)))
			recency := math.Exp(-math.Max(0, now.Sub(event.OccurredAt).Hours()) / (24 * 180))
			scored = append(scored, scoredEvent{event: event, score: coverage + recency*0.15})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].event.OccurredAt.After(scored[j].event.OccurredAt)
		}
		return scored[i].score > scored[j].score
	})
	resultLimit = normalizeGroupEventLimit(resultLimit)
	if len(scored) > resultLimit {
		scored = scored[:resultLimit]
	}
	result := make([]RecalledGroupEvent, 0, len(scored))
	for _, item := range scored {
		result = append(result, item.event)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].OccurredAt.Before(result[j].OccurredAt)
	})
	return result, nil
}

func stripHistoryRecallWords(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{
		"之前", "上次", "以前", "前面", "当时", "刚才", "还记得", "记不记得",
		"我们说过", "我们聊过", "聊过", "提过", "回顾", "翻一下", "找一下", "那个",
	} {
		value = strings.ReplaceAll(value, marker, " ")
	}
	return strings.TrimSpace(value)
}

func (s *MemoryGroupStore) PruneExpiredGroupEvents(ctx context.Context) error {
	_, err := s.runtime.db.ExecContext(ctx,
		"DELETE FROM conversation_events WHERE expires_at <= ?",
		formatStoreTime(s.now().UTC()),
	)
	return err
}

func (s *MemoryGroupStore) TrimGroupEvents(ctx context.Context, conversation string, maximum int) error {
	if strings.TrimSpace(conversation) == "" || maximum < 1 {
		return errors.New("conversation and maximum are required")
	}
	_, err := s.runtime.db.ExecContext(ctx, `
		DELETE FROM conversation_events
		WHERE conversation_digest = ? AND id NOT IN (
			SELECT id FROM conversation_events
			WHERE conversation_digest = ?
			ORDER BY occurred_at DESC, id DESC LIMIT ?
		)
	`, s.digest("conversation", conversation), s.digest("conversation", conversation), maximum)
	return err
}

func (s *MemoryGroupStore) MarkBotReplyAck(ctx context.Context, conversation string, ackAt time.Time) error {
	if strings.TrimSpace(conversation) == "" {
		return errors.New("conversation is required")
	}
	if ackAt.IsZero() {
		ackAt = s.now()
	}
	formattedAck := formatStoreTime(ackAt.UTC())
	_, err := s.runtime.db.ExecContext(ctx, `
		INSERT INTO conversation_state (conversation_digest, last_bot_reply_ack_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(conversation_digest) DO UPDATE SET
			last_bot_reply_ack_at = CASE
				WHEN conversation_state.last_bot_reply_ack_at IS NULL
					OR excluded.last_bot_reply_ack_at > conversation_state.last_bot_reply_ack_at
				THEN excluded.last_bot_reply_ack_at
				ELSE conversation_state.last_bot_reply_ack_at
			END,
			updated_at = excluded.updated_at
	`, s.digest("conversation", conversation), formattedAck, formatStoreTime(s.now().UTC()))
	return err
}

// ObserveBotMoodCue 记录一条会指向机器人自身情绪的线索(被夸/被怼/办砸)。
// 空 mood 不写入;情绪只影响语气,由读取侧按 TTL 判定是否仍然有效。
func (s *MemoryGroupStore) ObserveBotMoodCue(ctx context.Context, conversation, mood string) error {
	if strings.TrimSpace(conversation) == "" || strings.TrimSpace(mood) == "" {
		return nil
	}
	now := formatStoreTime(s.now().UTC())
	_, err := s.runtime.db.ExecContext(ctx, `
		INSERT INTO conversation_state (conversation_digest, bot_mood, bot_mood_updated_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(conversation_digest) DO UPDATE SET
			bot_mood = excluded.bot_mood,
			bot_mood_updated_at = excluded.bot_mood_updated_at,
			updated_at = excluded.updated_at
	`, s.digest("conversation", conversation), strings.TrimSpace(mood), now, now)
	return err
}

// BotMood 返回该会话内机器人当前仍有效的情绪底色;过期返回空。
func (s *MemoryGroupStore) BotMood(ctx context.Context, conversation string) string {
	if strings.TrimSpace(conversation) == "" {
		return ""
	}
	var mood, updatedAt string
	err := s.runtime.db.QueryRowContext(ctx, `
		SELECT bot_mood, bot_mood_updated_at FROM conversation_state
		WHERE conversation_digest = ?
	`, s.digest("conversation", conversation)).Scan(&mood, &updatedAt)
	if err != nil || strings.TrimSpace(mood) == "" {
		return ""
	}
	if !moodStillFresh(updatedAt, s.now()) {
		return ""
	}
	return mood
}

func (s *MemoryGroupStore) LastBotReplyAck(ctx context.Context, conversation string) (time.Time, bool, error) {
	if strings.TrimSpace(conversation) == "" {
		return time.Time{}, false, errors.New("conversation is required")
	}
	var raw sql.NullString
	err := s.runtime.db.QueryRowContext(ctx,
		"SELECT last_bot_reply_ack_at FROM conversation_state WHERE conversation_digest = ?",
		s.digest("conversation", conversation),
	).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) || !raw.Valid {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, err
	}
	ackAt, err := parseStoreTime(raw.String)
	return ackAt, err == nil, err
}

func (s *MemoryGroupStore) ObserveRelationship(
	ctx context.Context,
	eventID, conversation, sender string,
	addressed bool,
	occurredAt time.Time,
	identities ...RelationshipIdentity,
) (RelationshipState, bool, error) {
	if strings.TrimSpace(eventID) == "" || strings.TrimSpace(conversation) == "" || strings.TrimSpace(sender) == "" {
		return RelationshipState{}, false, errors.New("relationship event, conversation and sender are required")
	}
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}
	conversationDigest := s.digest("conversation", conversation)
	senderDigest := s.digest("sender", sender)
	relationshipID := "rel_" + hex.EncodeToString(s.digestBytes("relationship", conversationDigest, senderDigest))[:24]
	identity := RelationshipIdentity{}
	if len(identities) > 0 {
		identity = identities[0]
	}
	identity.PersonaID = strings.TrimSpace(identity.PersonaID)
	encryptedIdentity := make([][]byte, 3)
	var err error
	for index, value := range []string{identity.ConversationRef, identity.SenderRef, identity.SenderDisplayName} {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		encryptedIdentity[index], err = s.runtime.encrypt([]byte(value))
		if err != nil {
			return RelationshipState{}, false, err
		}
	}
	tx, err := s.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return RelationshipState{}, false, err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO relationship_events (event_id, conversation_digest, sender_digest, occurred_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(event_id) DO NOTHING
	`, eventID, conversationDigest, senderDigest, formatStoreTime(occurredAt.UTC()))
	if err != nil {
		return RelationshipState{}, false, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return RelationshipState{}, false, err
	}
	if inserted == 1 {
		_, err = tx.ExecContext(ctx, `
			INSERT INTO member_relationship_state (
				conversation_digest, sender_digest, relationship_id, persona_id,
				conversation_ref_cipher, sender_ref_cipher, sender_display_cipher,
				interaction_count, addressed_count, last_interaction_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)
			ON CONFLICT(conversation_digest, sender_digest) DO UPDATE SET
				relationship_id = CASE WHEN member_relationship_state.relationship_id = '' THEN excluded.relationship_id ELSE member_relationship_state.relationship_id END,
				persona_id = CASE WHEN excluded.persona_id <> '' THEN excluded.persona_id ELSE member_relationship_state.persona_id END,
				conversation_ref_cipher = COALESCE(excluded.conversation_ref_cipher, member_relationship_state.conversation_ref_cipher),
				sender_ref_cipher = COALESCE(excluded.sender_ref_cipher, member_relationship_state.sender_ref_cipher),
				sender_display_cipher = COALESCE(excluded.sender_display_cipher, member_relationship_state.sender_display_cipher),
				interaction_count = interaction_count + 1,
				addressed_count = addressed_count + excluded.addressed_count,
				last_interaction_at = CASE
					WHEN excluded.last_interaction_at > last_interaction_at THEN excluded.last_interaction_at
					ELSE last_interaction_at END,
				updated_at = excluded.updated_at
		`, conversationDigest, senderDigest, relationshipID, identity.PersonaID,
			nullableBytes(encryptedIdentity[0]), nullableBytes(encryptedIdentity[1]), nullableBytes(encryptedIdentity[2]), boolInt(addressed),
			formatStoreTime(occurredAt.UTC()), formatStoreTime(s.now().UTC()))
		if err != nil {
			return RelationshipState{}, false, err
		}
	}
	state, err := relationshipStateFromRow(tx.QueryRowContext(ctx, `
		SELECT interaction_count, addressed_count, reply_count, last_interaction_at, last_reply_at,
			manual_intimacy, intimacy_locked
		FROM member_relationship_state WHERE conversation_digest = ? AND sender_digest = ?
	`, conversationDigest, senderDigest), s.now())
	if err != nil {
		return RelationshipState{}, false, err
	}
	if err = tx.Commit(); err != nil {
		return RelationshipState{}, false, err
	}
	return state, inserted == 1, nil
}

// ObserveRelationshipReply records a terminal assistant delivery once. This
// makes output feedback measurable without counting duplicate connector ACKs.
func (s *MemoryGroupStore) ObserveRelationshipReply(ctx context.Context, deliveryID, conversation, sender string, occurredAt time.Time) error {
	if strings.TrimSpace(deliveryID) == "" || strings.TrimSpace(conversation) == "" || strings.TrimSpace(sender) == "" {
		return errors.New("delivery, conversation and sender are required")
	}
	if occurredAt.IsZero() {
		occurredAt = s.now()
	}
	conversationDigest := s.digest("conversation", conversation)
	senderDigest := s.digest("sender", sender)
	tx, err := s.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		INSERT INTO relationship_reply_events (delivery_id, conversation_digest, sender_digest, occurred_at)
		VALUES (?, ?, ?, ?) ON CONFLICT(delivery_id) DO NOTHING
	`, deliveryID, conversationDigest, senderDigest, formatStoreTime(occurredAt.UTC()))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted == 1 {
		if _, err = tx.ExecContext(ctx, `
			UPDATE member_relationship_state
			SET reply_count = reply_count + 1, last_reply_at = ?, updated_at = ?
			WHERE conversation_digest = ? AND sender_digest = ?
		`, formatStoreTime(occurredAt.UTC()), formatStoreTime(s.now().UTC()), conversationDigest, senderDigest); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *MemoryGroupStore) Relationship(
	ctx context.Context,
	conversation, sender string,
) (RelationshipState, bool, error) {
	if strings.TrimSpace(conversation) == "" || strings.TrimSpace(sender) == "" {
		return RelationshipState{}, false, errors.New("conversation and sender are required")
	}
	state, err := relationshipStateFromRow(s.runtime.db.QueryRowContext(ctx, `
		SELECT interaction_count, addressed_count, reply_count, last_interaction_at, last_reply_at,
			manual_intimacy, intimacy_locked
		FROM member_relationship_state WHERE conversation_digest = ? AND sender_digest = ?
	`, s.digest("conversation", conversation), s.digest("sender", sender)), s.now())
	if errors.Is(err, sql.ErrNoRows) {
		return RelationshipState{Stage: "新群友"}, false, nil
	}
	return state, err == nil, err
}

func (s *MemoryGroupStore) ListRelationships(ctx context.Context, personaID string, limit, offset int) ([]ManagedRelationship, int, error) {
	personaID = strings.TrimSpace(personaID)
	if personaID == "" {
		return nil, 0, errors.New("persona id is required")
	}
	limit = normalizeStoreLimit(limit)
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.runtime.db.QueryRowContext(ctx,
		"SELECT count(*) FROM member_relationship_state WHERE persona_id = ?", personaID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.runtime.db.QueryContext(ctx, `
		SELECT relationship_id, persona_id, conversation_ref_cipher, sender_ref_cipher, sender_display_cipher,
			interaction_count, addressed_count, reply_count, last_interaction_at, last_reply_at,
			manual_intimacy, intimacy_locked, conversation_digest, sender_digest
		FROM member_relationship_state
		WHERE persona_id = ?
		ORDER BY last_interaction_at DESC, relationship_id DESC
		LIMIT ? OFFSET ?
	`, personaID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := make([]ManagedRelationship, 0, limit)
	type pulseInput struct {
		senderRef          string
		conversationDigest []byte
		senderDigest       []byte
	}
	pulseInputs := make([]pulseInput, 0, limit)
	for rows.Next() {
		var item ManagedRelationship
		var conversationCipher, senderCipher, displayCipher, conversationDigest, senderDigest []byte
		var lastInteraction, lastReply sql.NullString
		var manual sql.NullFloat64
		var locked int
		if err := rows.Scan(&item.ID, &item.PersonaID, &conversationCipher, &senderCipher, &displayCipher,
			&item.State.InteractionCount, &item.State.AddressedCount, &item.State.ReplyCount,
			&lastInteraction, &lastReply, &manual, &locked, &conversationDigest, &senderDigest); err != nil {
			return nil, 0, err
		}
		if conversationCipher != nil {
			value, err := s.runtime.decrypt(conversationCipher)
			if err != nil {
				return nil, 0, err
			}
			item.ConversationRef = string(value)
		}
		if displayCipher != nil {
			value, err := s.runtime.decrypt(displayCipher)
			if err != nil {
				return nil, 0, err
			}
			item.SenderDisplayName = string(value)
		}
		item.State.LastInteraction, err = parseStoreTime(lastInteraction.String)
		if err != nil {
			return nil, 0, err
		}
		if lastReply.Valid {
			item.State.LastReply, err = parseStoreTime(lastReply.String)
			if err != nil {
				return nil, 0, err
			}
		}
		item.State.AutoIntimacy = relationshipIntimacy(item.State.InteractionCount, item.State.AddressedCount, item.State.LastInteraction, s.now())
		item.State.Intimacy = item.State.AutoIntimacy
		item.State.IntimacyLocked = locked == 1 && manual.Valid
		if item.State.IntimacyLocked {
			item.State.Intimacy = math.Max(0, math.Min(100, manual.Float64))
		}
		item.State.Stage = relationshipStage(item.State.Intimacy)
		input := pulseInput{conversationDigest: append([]byte(nil), conversationDigest...), senderDigest: append([]byte(nil), senderDigest...)}
		if senderCipher != nil {
			value, decryptErr := s.runtime.decrypt(senderCipher)
			if decryptErr != nil {
				return nil, 0, decryptErr
			}
			input.senderRef = string(value)
		}
		items = append(items, item)
		pulseInputs = append(pulseInputs, input)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, err
	}
	if err = rows.Close(); err != nil {
		return nil, 0, err
	}
	policy := s.runtime.memoryPolicy(ctx)
	for index := range items {
		input := pulseInputs[index]
		if input.senderRef != "" {
			items[index].State.Pulse = s.relationshipPulse(ctx, items[index].PersonaID, input.senderRef, input.conversationDigest, input.senderDigest, items[index].State, policy)
		}
	}
	return items, total, nil
}

func (s *MemoryGroupStore) UpdateRelationship(ctx context.Context, id string, intimacy float64, locked bool) (ManagedRelationship, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" || intimacy < 0 || intimacy > 100 {
		return ManagedRelationship{}, false, errors.New("relationship id and intimacy between 0 and 100 are required")
	}
	var manual any
	if locked {
		manual = intimacy
	}
	result, err := s.runtime.db.ExecContext(ctx, `
		UPDATE member_relationship_state
		SET manual_intimacy = ?, intimacy_locked = ?, updated_at = ?
		WHERE relationship_id = ?
	`, manual, boolInt(locked), formatStoreTime(s.now().UTC()), id)
	if err != nil {
		return ManagedRelationship{}, false, err
	}
	changed, err := result.RowsAffected()
	if err != nil || changed == 0 {
		return ManagedRelationship{}, changed == 1, err
	}
	item, found, err := s.relationshipByID(ctx, id)
	return item, found, err
}

func (s *MemoryGroupStore) DeleteRelationship(ctx context.Context, id string) (bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return false, errors.New("relationship id is required")
	}
	tx, err := s.runtime.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var conversationDigest, senderDigest []byte
	if err = tx.QueryRowContext(ctx, `SELECT conversation_digest, sender_digest FROM member_relationship_state WHERE relationship_id = ?`, id).Scan(&conversationDigest, &senderDigest); errors.Is(err, sql.ErrNoRows) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relationship_events WHERE conversation_digest = ? AND sender_digest = ?`, conversationDigest, senderDigest); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relationship_reply_events WHERE conversation_digest = ? AND sender_digest = ?`, conversationDigest, senderDigest); err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM member_relationship_state WHERE relationship_id = ?`, id)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (s *MemoryGroupStore) relationshipByID(ctx context.Context, id string) (ManagedRelationship, bool, error) {
	var item ManagedRelationship
	var conversationCipher, senderCipher, displayCipher, conversationDigest, senderDigest []byte
	var lastInteraction, lastReply sql.NullString
	var manual sql.NullFloat64
	var locked int
	err := s.runtime.db.QueryRowContext(ctx, `
		SELECT relationship_id, persona_id, conversation_ref_cipher, sender_ref_cipher, sender_display_cipher,
			interaction_count, addressed_count, reply_count, last_interaction_at, last_reply_at,
			manual_intimacy, intimacy_locked, conversation_digest, sender_digest
		FROM member_relationship_state WHERE relationship_id = ?
	`, id).Scan(&item.ID, &item.PersonaID, &conversationCipher, &senderCipher, &displayCipher,
		&item.State.InteractionCount, &item.State.AddressedCount, &item.State.ReplyCount,
		&lastInteraction, &lastReply, &manual, &locked, &conversationDigest, &senderDigest)
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedRelationship{}, false, nil
	}
	if err != nil {
		return ManagedRelationship{}, false, err
	}
	if conversationCipher != nil {
		value, err := s.runtime.decrypt(conversationCipher)
		if err != nil {
			return ManagedRelationship{}, false, err
		}
		item.ConversationRef = string(value)
	}
	if displayCipher != nil {
		value, err := s.runtime.decrypt(displayCipher)
		if err != nil {
			return ManagedRelationship{}, false, err
		}
		item.SenderDisplayName = string(value)
	}
	item.State.LastInteraction, err = parseStoreTime(lastInteraction.String)
	if err != nil {
		return ManagedRelationship{}, false, err
	}
	if lastReply.Valid {
		item.State.LastReply, err = parseStoreTime(lastReply.String)
		if err != nil {
			return ManagedRelationship{}, false, err
		}
	}
	item.State.AutoIntimacy = relationshipIntimacy(item.State.InteractionCount, item.State.AddressedCount, item.State.LastInteraction, s.now())
	item.State.Intimacy = item.State.AutoIntimacy
	item.State.IntimacyLocked = locked == 1 && manual.Valid
	if item.State.IntimacyLocked {
		item.State.Intimacy = math.Max(0, math.Min(100, manual.Float64))
	}
	item.State.Stage = relationshipStage(item.State.Intimacy)
	if senderCipher != nil {
		value, decryptErr := s.runtime.decrypt(senderCipher)
		if decryptErr != nil {
			return ManagedRelationship{}, false, decryptErr
		}
		item.State.Pulse = s.relationshipPulse(ctx, item.PersonaID, string(value), conversationDigest, senderDigest, item.State, s.runtime.memoryPolicy(ctx))
	}
	return item, true, nil
}

func relationshipStateFromRow(row rowScanner, now time.Time) (RelationshipState, error) {
	var state RelationshipState
	var lastInteraction string
	var lastReply sql.NullString
	var manual sql.NullFloat64
	var locked int
	if err := row.Scan(&state.InteractionCount, &state.AddressedCount, &state.ReplyCount, &lastInteraction, &lastReply, &manual, &locked); err != nil {
		return RelationshipState{}, err
	}
	var err error
	state.LastInteraction, err = parseStoreTime(lastInteraction)
	if err == nil && lastReply.Valid {
		state.LastReply, err = parseStoreTime(lastReply.String)
	}
	state.AutoIntimacy = relationshipIntimacy(state.InteractionCount, state.AddressedCount, state.LastInteraction, now)
	state.Intimacy = state.AutoIntimacy
	state.IntimacyLocked = locked == 1 && manual.Valid
	if state.IntimacyLocked {
		state.Intimacy = math.Max(0, math.Min(100, manual.Float64))
	}
	state.Stage = relationshipStage(state.Intimacy)
	return state, err
}

func relationshipIntimacy(interactions, addressed int, lastInteraction, now time.Time) float64 {
	score := math.Log1p(float64(interactions))*13 + math.Log1p(float64(addressed))*11
	if !lastInteraction.IsZero() && now.After(lastInteraction) {
		days := now.Sub(lastInteraction).Hours() / 24
		score *= 0.55 + 0.45*math.Exp(-days/180)
	}
	return math.Round(math.Max(0, math.Min(100, score))*10) / 10
}

func relationshipStage(intimacy float64) string {
	switch {
	case intimacy >= 85:
		return "亲近的人"
	case intimacy >= 65:
		return "老熟人"
	case intimacy >= 38:
		return "熟悉群友"
	case intimacy >= 12:
		return "普通群友"
	default:
		return "新群友"
	}
}

func (s *MemoryGroupStore) TrimScope(ctx context.Context, scope string, maximum int) error {
	if strings.TrimSpace(scope) == "" || maximum < 1 {
		return errors.New("memory scope and positive maximum are required")
	}
	_, err := s.runtime.db.ExecContext(ctx, `
		DELETE FROM agent_memories WHERE id IN (
			SELECT id FROM agent_memories WHERE scope_digest = ?
			ORDER BY importance DESC, confidence DESC, updated_at DESC
			LIMIT -1 OFFSET ?
		)
	`, s.digest("scope", scope), maximum)
	return err
}

func (s *MemoryGroupStore) PruneExpiredMemories(ctx context.Context) error {
	_, err := s.runtime.db.ExecContext(ctx,
		"DELETE FROM agent_memories WHERE expires_at IS NOT NULL AND expires_at <= ?",
		formatStoreTime(s.now().UTC()),
	)
	return err
}

type memoryWithScope struct {
	memory      RecalledMemory
	scopeDigest []byte
}

func (s *MemoryGroupStore) memoryByLegacyID(ctx context.Context, source, legacyID string) (memoryWithScope, bool, error) {
	row := s.runtime.db.QueryRowContext(ctx, `
		SELECT id, scope_digest, content_cipher, source, kind, confidence, importance,
			access_count, last_accessed_at, expires_at, created_at, updated_at
		FROM agent_memories WHERE legacy_source = ? AND legacy_id = ?
	`, source, legacyID)
	var result memoryWithScope
	err := s.scanMemoryWithScope(row, &result)
	if errors.Is(err, sql.ErrNoRows) {
		return memoryWithScope{}, false, nil
	}
	if err != nil {
		return memoryWithScope{}, false, err
	}
	return result, true, nil
}

func (s *MemoryGroupStore) memoryByDigest(ctx context.Context, scopeDigest, contentDigest []byte) (RecalledMemory, bool, error) {
	row := s.runtime.db.QueryRowContext(ctx, `
		SELECT id, content_cipher, source, kind, confidence, importance, access_count,
			last_accessed_at, expires_at, created_at, updated_at
		FROM agent_memories WHERE scope_digest = ? AND content_digest = ?
	`, scopeDigest, contentDigest)
	memory, err := s.scanMemory(row)
	if errors.Is(err, sql.ErrNoRows) {
		return RecalledMemory{}, false, nil
	}
	return memory, err == nil, err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *MemoryGroupStore) scanMemory(row rowScanner) (RecalledMemory, error) {
	var memory RecalledMemory
	var ciphertext []byte
	var createdAt, updatedAt string
	var lastAccessedAt, expiresAt sql.NullString
	if err := row.Scan(
		&memory.ID, &ciphertext, &memory.Source, &memory.Kind, &memory.Confidence,
		&memory.Importance, &memory.AccessCount, &lastAccessedAt, &expiresAt,
		&createdAt, &updatedAt,
	); err != nil {
		return RecalledMemory{}, err
	}
	if err := s.fillMemory(&memory, ciphertext, createdAt, updatedAt, lastAccessedAt, expiresAt); err != nil {
		return RecalledMemory{}, err
	}
	return memory, nil
}

func (s *MemoryGroupStore) scanManagedMemory(row rowScanner) (RecalledMemory, error) {
	var memory RecalledMemory
	var ciphertext, referenceCipher []byte
	var createdAt, updatedAt string
	var lastAccessedAt, expiresAt, personaID, scopeKind sql.NullString
	if err := row.Scan(
		&memory.ID, &ciphertext, &memory.Source, &memory.Kind, &memory.Confidence,
		&memory.Importance, &memory.AccessCount, &lastAccessedAt, &expiresAt,
		&createdAt, &updatedAt, &personaID, &scopeKind, &referenceCipher,
	); err != nil {
		return RecalledMemory{}, err
	}
	if err := s.fillMemory(&memory, ciphertext, createdAt, updatedAt, lastAccessedAt, expiresAt); err != nil {
		return RecalledMemory{}, err
	}
	memory.PersonaID = personaID.String
	memory.ScopeKind = scopeKind.String
	if len(referenceCipher) > 0 {
		plaintext, err := s.runtime.decrypt(referenceCipher)
		if err != nil {
			return RecalledMemory{}, fmt.Errorf("decrypt memory scope %s: %w", memory.ID, err)
		}
		memory.ScopeReference = string(plaintext)
	}
	return memory, nil
}

func (s *MemoryGroupStore) scanMemoryWithScope(row rowScanner, result *memoryWithScope) error {
	var ciphertext []byte
	var createdAt, updatedAt string
	var lastAccessedAt, expiresAt sql.NullString
	if err := row.Scan(
		&result.memory.ID, &result.scopeDigest, &ciphertext, &result.memory.Source,
		&result.memory.Kind, &result.memory.Confidence, &result.memory.Importance,
		&result.memory.AccessCount, &lastAccessedAt, &expiresAt, &createdAt, &updatedAt,
	); err != nil {
		return err
	}
	return s.fillMemory(&result.memory, ciphertext, createdAt, updatedAt, lastAccessedAt, expiresAt)
}

func (s *MemoryGroupStore) fillMemory(
	memory *RecalledMemory,
	ciphertext []byte,
	createdAt, updatedAt string,
	lastAccessedAt, expiresAt sql.NullString,
) error {
	plaintext, err := s.runtime.decrypt(ciphertext)
	if err != nil {
		return fmt.Errorf("decrypt memory %s: %w", memory.ID, err)
	}
	memory.UntrustedContent = string(plaintext)
	memory.CreatedAt, err = parseStoreTime(createdAt)
	if err != nil {
		return err
	}
	memory.UpdatedAt, err = parseStoreTime(updatedAt)
	if err != nil {
		return err
	}
	if lastAccessedAt.Valid {
		parsed, parseErr := parseStoreTime(lastAccessedAt.String)
		if parseErr != nil {
			return parseErr
		}
		memory.LastAccessedAt = &parsed
	}
	if expiresAt.Valid {
		parsed, parseErr := parseStoreTime(expiresAt.String)
		if parseErr != nil {
			return parseErr
		}
		memory.ExpiresAt = &parsed
	}
	return nil
}

type groupEventWithConversation struct {
	event              RecalledGroupEvent
	conversationDigest []byte
}

func (s *MemoryGroupStore) groupEventByIdentity(ctx context.Context, id, legacySource, legacyID string) (groupEventWithConversation, bool, error) {
	query := `
		SELECT id, conversation_digest, sender_ref_cipher, sender_display_cipher, persona_id_cipher, role, text_cipher,
			message_id_cipher, thread_key_cipher, reply_to_message_id_cipher,
			reply_to_sender_ref_cipher, reply_to_sender_display_cipher, quoted_text_cipher, occurred_at
		FROM conversation_events WHERE id = ?
	`
	args := []any{id}
	if legacySource != "" {
		query = `
			SELECT id, conversation_digest, sender_ref_cipher, sender_display_cipher, persona_id_cipher, role, text_cipher,
				message_id_cipher, thread_key_cipher, reply_to_message_id_cipher,
				reply_to_sender_ref_cipher, reply_to_sender_display_cipher, quoted_text_cipher, occurred_at
			FROM conversation_events WHERE legacy_source = ? AND legacy_id = ?
		`
		args = []any{legacySource, legacyID}
	}
	var result groupEventWithConversation
	var ciphertext []byte
	optional := make([][]byte, 9)
	var occurredAt string
	err := s.runtime.db.QueryRowContext(ctx, query, args...).Scan(
		&result.event.ID, &result.conversationDigest, &optional[0], &optional[1], &optional[2],
		&result.event.Role, &ciphertext, &optional[3], &optional[4], &optional[5],
		&optional[6], &optional[7], &optional[8], &occurredAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return groupEventWithConversation{}, false, nil
	}
	if err != nil {
		return groupEventWithConversation{}, false, err
	}
	plaintext, err := s.runtime.decrypt(ciphertext)
	if err != nil {
		return groupEventWithConversation{}, false, err
	}
	result.event.UntrustedText = string(plaintext)
	values := make([]string, len(optional))
	for index := range optional {
		if len(optional[index]) == 0 {
			continue
		}
		decoded, decryptErr := s.runtime.decrypt(optional[index])
		if decryptErr != nil {
			return groupEventWithConversation{}, false, decryptErr
		}
		values[index] = string(decoded)
	}
	result.event.SenderRef, result.event.SenderDisplayName, result.event.PersonaID = values[0], values[1], values[2]
	result.event.MessageID, result.event.ThreadKey = values[3], values[4]
	result.event.ReplyToMessageID, result.event.ReplyToSenderRef = values[5], values[6]
	result.event.ReplyToSenderDisplayName, result.event.UntrustedQuotedText = values[7], values[8]
	result.event.OccurredAt, err = parseStoreTime(occurredAt)
	return result, err == nil, err
}

func (s *MemoryGroupStore) digest(label, value string) []byte {
	return s.digestBytes(label, []byte(value))
}

func (s *MemoryGroupStore) digestBytes(label string, parts ...[]byte) []byte {
	mac := hmac.New(sha256.New, s.identityKey)
	mac.Write([]byte(label))
	mac.Write([]byte{0})
	for _, part := range parts {
		mac.Write(part)
		mac.Write([]byte{0})
	}
	return mac.Sum(nil)
}

func secureRecordID(prefix string) (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(random), nil
}

func normalizeStoreLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > maxMemoryQueryLimit {
		return maxMemoryQueryLimit
	}
	return limit
}

func normalizeGroupEventLimit(limit int) int {
	if limit <= 0 {
		return 20
	}
	if limit > maxGroupEventQueryLimit {
		return maxGroupEventQueryLimit
	}
	return limit
}

func formatStoreTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func parseStoreTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored time: %w", err)
	}
	return parsed, nil
}

func formatOptionalStoreTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return formatStoreTime(value.UTC())
}
