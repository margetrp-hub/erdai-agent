package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

const (
	defaultImageDailyLimit = 3
	defaultVideoDailyLimit = 3
	maxMediaDailyLimit     = 100
	maxMediaQuotaWhitelist = 100
)

var shanghaiTime = time.FixedZone("Asia/Shanghai", 8*60*60)

type mediaKind string

const (
	mediaKindImage mediaKind = "image"
	mediaKindVideo mediaKind = "video"
)

type mediaQuotaConfig struct {
	ImageDailyLimit    int                        `json:"imageDailyLimit"`
	VideoDailyLimit    int                        `json:"videoDailyLimit"`
	TrustedAdminBypass bool                       `json:"trustedAdminBypass"`
	Whitelist          []mediaQuotaWhitelistEntry `json:"whitelist"`
	Timezone           string                     `json:"timezone"`
}

type mediaQuotaWhitelistEntry struct {
	Label     string `json:"label"`
	SenderRef string `json:"senderRef"`
}

type mediaQuotaExceededError struct {
	Kind mediaKind
}

func (e *mediaQuotaExceededError) Error() string {
	return string(e.Kind) + " daily quota exceeded"
}

type mediaQuotaStore struct {
	db          *sql.DB
	identityKey []byte
	now         func() time.Time
}

type mediaQuotaReservation struct {
	store         *mediaQuotaStore
	subjectDigest []byte
	day           string
	kind          mediaKind
}

func (a *AgentRuntime) executeQuotaMedia(
	ctx context.Context,
	run runRecord,
	kind mediaKind,
	execute func() (toolResult, error),
) (toolResult, error) {
	config, err := a.mediaQuota.config(ctx)
	if err != nil {
		return toolResult{}, err
	}
	if config.exempts(run.SenderRef, run.IsAdmin) {
		return execute()
	}
	reservation, err := a.mediaQuota.reserveForRun(ctx, run, kind)
	if err != nil {
		return toolResult{}, err
	}
	committed := false
	finalizeContext := a.lifecycle
	if finalizeContext == nil {
		finalizeContext = ctx
	}
	defer func() {
		if !committed {
			_ = reservation.release(finalizeContext)
		}
	}()
	result, err := execute()
	if err != nil {
		return toolResult{}, err
	}
	if err := reservation.commit(finalizeContext); err != nil {
		return toolResult{}, err
	}
	committed = true
	return result, nil
}

func newMediaQuotaStore(db *sql.DB, identityKey []byte) (*mediaQuotaStore, error) {
	if db == nil {
		return nil, errors.New("media quota database is required")
	}
	if len(identityKey) != sha256.Size {
		return nil, errors.New("media quota identity key must contain exactly 32 bytes")
	}
	return &mediaQuotaStore{
		db: db, identityKey: append([]byte(nil), identityKey...), now: time.Now,
	}, nil
}

func (s *mediaQuotaStore) initSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS media_quota_config (
			singleton INTEGER PRIMARY KEY CHECK(singleton = 1),
			image_daily_limit INTEGER NOT NULL CHECK(image_daily_limit BETWEEN 0 AND 100),
			video_daily_limit INTEGER NOT NULL CHECK(video_daily_limit BETWEEN 0 AND 100),
			trusted_admin_bypass INTEGER NOT NULL DEFAULT 1 CHECK(trusted_admin_bypass IN (0, 1)),
			whitelist_json TEXT NOT NULL DEFAULT '[]',
			updated_at TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS media_quota_usage (
			subject_digest BLOB NOT NULL,
			usage_day TEXT NOT NULL,
			media_kind TEXT NOT NULL CHECK(media_kind IN ('image', 'video')),
			used_count INTEGER NOT NULL DEFAULT 0 CHECK(used_count >= 0),
			reserved_count INTEGER NOT NULL DEFAULT 0 CHECK(reserved_count >= 0),
			updated_at TEXT NOT NULL,
			PRIMARY KEY(subject_digest, usage_day, media_kind)
		);
	`); err != nil {
		return err
	}
	if err := ensureRuntimeColumn(s.db, "media_quota_config", "trusted_admin_bypass", "INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if err := ensureRuntimeColumn(s.db, "media_quota_config", "whitelist_json", "TEXT NOT NULL DEFAULT '[]'"); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO media_quota_config (
			singleton, image_daily_limit, video_daily_limit,
			trusted_admin_bypass, whitelist_json, updated_at
		) VALUES (1, ?, ?, 1, '[]', ?)
	`, defaultImageDailyLimit, defaultVideoDailyLimit, formatStoreTime(s.now().UTC()))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE media_quota_usage
		SET reserved_count = 0, updated_at = ?
		WHERE reserved_count > 0
	`, formatStoreTime(s.now().UTC()))
	return err
}

func (s *mediaQuotaStore) config(ctx context.Context) (mediaQuotaConfig, error) {
	var config mediaQuotaConfig
	var trustedAdminBypass int
	var whitelistJSON string
	err := s.db.QueryRowContext(ctx, `
		SELECT image_daily_limit, video_daily_limit, trusted_admin_bypass, whitelist_json
		FROM media_quota_config WHERE singleton = 1
	`).Scan(&config.ImageDailyLimit, &config.VideoDailyLimit, &trustedAdminBypass, &whitelistJSON)
	if err != nil {
		return config, err
	}
	config.TrustedAdminBypass = trustedAdminBypass == 1
	if err := json.Unmarshal([]byte(whitelistJSON), &config.Whitelist); err != nil {
		return mediaQuotaConfig{}, errors.New("media quota whitelist is invalid")
	}
	config.Timezone = "Asia/Shanghai"
	return config, nil
}

func (s *mediaQuotaStore) updateConfig(ctx context.Context, imageLimit, videoLimit int) (mediaQuotaConfig, error) {
	current, err := s.config(ctx)
	if err != nil {
		return mediaQuotaConfig{}, err
	}
	return s.updatePolicy(ctx, imageLimit, videoLimit, current.TrustedAdminBypass, current.Whitelist)
}

func (s *mediaQuotaStore) updatePolicy(
	ctx context.Context,
	imageLimit, videoLimit int,
	trustedAdminBypass bool,
	whitelist []mediaQuotaWhitelistEntry,
) (mediaQuotaConfig, error) {
	if imageLimit < 0 || imageLimit > maxMediaDailyLimit ||
		videoLimit < 0 || videoLimit > maxMediaDailyLimit {
		return mediaQuotaConfig{}, errors.New("media daily limits must be between 0 and 100")
	}
	whitelist, err := normalizeMediaQuotaWhitelist(whitelist)
	if err != nil {
		return mediaQuotaConfig{}, err
	}
	encoded, err := json.Marshal(whitelist)
	if err != nil {
		return mediaQuotaConfig{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE media_quota_config
		SET image_daily_limit = ?, video_daily_limit = ?,
			trusted_admin_bypass = ?, whitelist_json = ?, updated_at = ?
		WHERE singleton = 1
	`, imageLimit, videoLimit, boolInt(trustedAdminBypass), string(encoded), formatStoreTime(s.now().UTC()))
	if err != nil {
		return mediaQuotaConfig{}, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return mediaQuotaConfig{}, err
	}
	if changed != 1 {
		return mediaQuotaConfig{}, errors.New("media quota configuration is missing")
	}
	return s.config(ctx)
}

func normalizeMediaQuotaWhitelist(values []mediaQuotaWhitelistEntry) ([]mediaQuotaWhitelistEntry, error) {
	if len(values) > maxMediaQuotaWhitelist {
		return nil, errors.New("media quota whitelist has too many entries")
	}
	result := make([]mediaQuotaWhitelistEntry, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value.Label = strings.TrimSpace(value.Label)
		value.SenderRef = strings.TrimSpace(value.SenderRef)
		if value.SenderRef == "" || len(value.SenderRef) > 200 || len(value.Label) > 80 {
			return nil, errors.New("media quota whitelist entry is invalid")
		}
		if seen[value.SenderRef] {
			continue
		}
		seen[value.SenderRef] = true
		result = append(result, value)
	}
	return result, nil
}

func (c mediaQuotaConfig) exempts(senderRef string, isAdmin bool) bool {
	if isAdmin && c.TrustedAdminBypass {
		return true
	}
	senderRef = strings.TrimSpace(senderRef)
	for _, entry := range c.Whitelist {
		if senderRef != "" && strings.TrimSpace(entry.SenderRef) == senderRef {
			return true
		}
	}
	return false
}

func (s *mediaQuotaStore) reserve(ctx context.Context, sender string, kind mediaKind) (*mediaQuotaReservation, error) {
	return s.reserveSubject(ctx, "", sender, kind)
}

func (s *mediaQuotaStore) reserveForRun(ctx context.Context, run runRecord, kind mediaKind) (*mediaQuotaReservation, error) {
	instanceID := strings.TrimSpace(run.AgentInstanceID)
	// Keep the historic subject digest for pre-instance runs. New explicit
	// instances use their own namespace and cannot share a quota with it.
	if instanceID == "" || instanceID == legacyAgentInstanceID {
		instanceID = ""
	}
	return s.reserveSubject(ctx, instanceID, run.SenderRef, kind)
}

func (s *mediaQuotaStore) reserveSubject(ctx context.Context, instanceID, sender string, kind mediaKind) (*mediaQuotaReservation, error) {
	if strings.TrimSpace(sender) == "" || kind != mediaKindImage && kind != mediaKindVideo {
		return nil, errors.New("media quota identity or kind is invalid")
	}
	day := s.now().In(shanghaiTime).Format("2006-01-02")
	digest := s.digestSubject(instanceID, sender)
	now := formatStoreTime(s.now().UTC())
	var reserved int
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO media_quota_usage (
			subject_digest, usage_day, media_kind, used_count, reserved_count, updated_at
		)
		SELECT ?, ?, ?, 0, 1, ?
		WHERE (
			SELECT CASE ?
				WHEN 'image' THEN image_daily_limit
				ELSE video_daily_limit
			END
			FROM media_quota_config WHERE singleton = 1
		) > 0
		ON CONFLICT(subject_digest, usage_day, media_kind) DO UPDATE SET
			reserved_count = media_quota_usage.reserved_count + 1,
			updated_at = excluded.updated_at
		WHERE media_quota_usage.used_count + media_quota_usage.reserved_count < (
			SELECT CASE ?
				WHEN 'image' THEN image_daily_limit
				ELSE video_daily_limit
			END
			FROM media_quota_config WHERE singleton = 1
		)
		RETURNING reserved_count
	`, digest, day, string(kind), now, string(kind), string(kind)).Scan(&reserved)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &mediaQuotaExceededError{Kind: kind}
	}
	if err != nil {
		return nil, err
	}
	return &mediaQuotaReservation{
		store: s, subjectDigest: digest, day: day, kind: kind,
	}, nil
}

func (r *mediaQuotaReservation) commit(ctx context.Context) error {
	result, err := r.store.db.ExecContext(ctx, `
		UPDATE media_quota_usage
		SET reserved_count = reserved_count - 1,
			used_count = used_count + 1,
			updated_at = ?
		WHERE subject_digest = ? AND usage_day = ? AND media_kind = ?
			AND reserved_count > 0
	`, formatStoreTime(r.store.now().UTC()), r.subjectDigest, r.day, string(r.kind))
	return requireSingleQuotaReservation(result, err)
}

func (r *mediaQuotaReservation) release(ctx context.Context) error {
	result, err := r.store.db.ExecContext(ctx, `
		UPDATE media_quota_usage
		SET reserved_count = reserved_count - 1, updated_at = ?
		WHERE subject_digest = ? AND usage_day = ? AND media_kind = ?
			AND reserved_count > 0
	`, formatStoreTime(r.store.now().UTC()), r.subjectDigest, r.day, string(r.kind))
	return requireSingleQuotaReservation(result, err)
}

func requireSingleQuotaReservation(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errors.New("media quota reservation is no longer active")
	}
	return nil
}

func (s *mediaQuotaStore) digestSender(sender string) []byte {
	return s.digestSubject("", sender)
}

func (s *mediaQuotaStore) digestSubject(instanceID, sender string) []byte {
	mac := hmac.New(sha256.New, s.identityKey)
	mac.Write([]byte("media-quota-subject"))
	mac.Write([]byte{0})
	mac.Write([]byte(strings.TrimSpace(instanceID)))
	mac.Write([]byte{0})
	mac.Write([]byte(sender))
	return mac.Sum(nil)
}

func mediaQuotaReply(kind mediaKind) string {
	return randomMessage(mediaQuotaReplyOptions(kind))
}

func mediaQuotaReplyOptions(kind mediaKind) []string {
	if kind == mediaKindVideo {
		return []string{
			"\u4eca\u5929\u4e0d\u6298\u817e\u89c6\u9891\u4e86\uff0c\u660e\u5929\u518d\u8bf4\u3002",
			"\u4eca\u5929\u7684\u89c6\u9891\u5148\u5230\u8fd9\uff0c\u6539\u5929\u3002",
			"\u4eca\u5929\u4e0d\u60f3\u62cd\u4e86\uff0c\u4e0b\u6b21\u5427\u3002",
		}
	}
	return []string{
		"\u4eca\u5929\u5df2\u7ecf\u7ed9\u4f60\u770b\u8fc7\u54af\uff0c\u660e\u5929\u518d\u8bf4\u3002",
		"\u4eca\u5929\u6ca1\u5fc3\u60c5\u81ea\u62cd\u4e86\uff0c\u4e0b\u6b21\u5427\u3002",
		"\u4eca\u5929\u7684\u7167\u7247\u5148\u5230\u8fd9\uff0c\u660e\u5929\u770b\u3002",
	}
}

func isMediaQuotaReply(kind mediaKind, text string) bool {
	if kind == mediaKindVideo {
		return text == "\u4eca\u5929\u4e0d\u6298\u817e\u89c6\u9891\u4e86\uff0c\u660e\u5929\u518d\u8bf4\u3002" ||
			text == "\u4eca\u5929\u7684\u89c6\u9891\u5148\u5230\u8fd9\uff0c\u6539\u5929\u3002" ||
			text == "\u4eca\u5929\u4e0d\u60f3\u62cd\u4e86\uff0c\u4e0b\u6b21\u5427\u3002"
	}
	return text == "\u4eca\u5929\u5df2\u7ecf\u7ed9\u4f60\u770b\u8fc7\u54af\uff0c\u660e\u5929\u518d\u8bf4\u3002" ||
		text == "\u4eca\u5929\u6ca1\u5fc3\u60c5\u81ea\u62cd\u4e86\uff0c\u4e0b\u6b21\u5427\u3002" ||
		text == "\u4eca\u5929\u7684\u7167\u7247\u5148\u5230\u8fd9\uff0c\u660e\u5929\u770b\u3002"
}

func (a *AgentRuntime) handleMediaQuotaAdmin(w http.ResponseWriter, r *http.Request) {
	if !a.authorizedAdmin(r) {
		writeJSON(w, http.StatusUnauthorized, map[string]any{
			"error": map[string]string{"code": "unauthorized", "message": "admin token required"},
		})
		return
	}
	switch r.Method {
	case http.MethodGet:
		config, err := a.mediaQuota.config(r.Context())
		if err != nil {
			runtimeError(w, http.StatusInternalServerError, "media_quota_read_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": config})
	case http.MethodPut:
		var body struct {
			ImageDailyLimit    *int                        `json:"imageDailyLimit"`
			VideoDailyLimit    *int                        `json:"videoDailyLimit"`
			TrustedAdminBypass *bool                       `json:"trustedAdminBypass"`
			Whitelist          *[]mediaQuotaWhitelistEntry `json:"whitelist"`
		}
		if err := decodeJSONBody(r, &body); err != nil ||
			body.ImageDailyLimit == nil || body.VideoDailyLimit == nil ||
			*body.ImageDailyLimit < 0 || *body.ImageDailyLimit > maxMediaDailyLimit ||
			*body.VideoDailyLimit < 0 || *body.VideoDailyLimit > maxMediaDailyLimit {
			runtimeError(w, http.StatusBadRequest, "invalid_media_quota")
			return
		}
		current, err := a.mediaQuota.config(r.Context())
		if err != nil {
			runtimeError(w, http.StatusInternalServerError, "media_quota_read_failed")
			return
		}
		trustedAdminBypass := current.TrustedAdminBypass
		if body.TrustedAdminBypass != nil {
			trustedAdminBypass = *body.TrustedAdminBypass
		}
		whitelist := current.Whitelist
		if body.Whitelist != nil {
			whitelist = *body.Whitelist
		}
		config, err := a.mediaQuota.updatePolicy(
			r.Context(), *body.ImageDailyLimit, *body.VideoDailyLimit,
			trustedAdminBypass, whitelist,
		)
		if err != nil {
			runtimeError(w, http.StatusInternalServerError, "media_quota_update_failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": config})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (a *AgentRuntime) authorizedAdmin(r *http.Request) bool {
	raw := strings.TrimSpace(r.Header.Get(adminTokenHeader))
	left := sha256.Sum256([]byte(raw))
	right := sha256.Sum256([]byte(a.adminToken))
	return a.adminToken != "" && hmac.Equal(left[:], right[:])
}

func (a *AgentRuntime) quotaToolResult(ctx context.Context, run runRecord, kind mediaKind) toolResult {
	scene := "image-quota"
	if kind == mediaKindVideo {
		scene = "video-quota"
	}
	message := a.personaFixedReply(ctx, run, scene, mediaQuotaReplyOptions(kind))
	body, _ := json.Marshal(map[string]any{
		"ok": false, "error": string(kind) + "_daily_limit", "message": message,
	})
	return toolResult{
		Content: string(body), UserMessage: message, PreserveUserMessage: true,
	}
}
