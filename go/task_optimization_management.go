package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const taskFeedbackPrefix = "task_feedback:"

func (a *AgentRuntime) handleVisualHistory(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	libraryID := strings.TrimSpace(r.URL.Query().Get("libraryId"))
	if libraryID == "" || len(libraryID) > 160 {
		return coreInvalid("appearance library is required")
	}
	plans, err := a.recentVisualPlans(r.Context(), visualPlanName(libraryID), 8)
	if err != nil {
		return err
	}
	items := make([]map[string]any, 0, len(plans))
	for _, plan := range plans {
		// Library history shares only visual choices, never another user's request.
		items = append(items, map[string]any{"mediaType": plan.MediaType, "attempt": plan.Attempt,
			"appearanceId": plan.AppearanceID, "appearanceRevision": plan.AppearanceRevision,
			"variables": plan.Variables, "createdAt": plan.CreatedAt})
	}
	w.Header().Set("Cache-Control", "no-store")
	mgmtWriteData(w, http.StatusOK, map[string]any{"items": items})
	return nil
}

func (a *AgentRuntime) handleTaskArtifact(w http.ResponseWriter, r *http.Request, runID, rawID string) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	id, err := strconv.ParseInt(rawID, 10, 64)
	if err != nil || id < 1 {
		return mgmtNotFound("artifact")
	}
	var localPath, mimeType string
	err = a.db.QueryRowContext(r.Context(), `SELECT local_path, mime_type FROM agent_task_artifacts
		WHERE id = ? AND run_id = ? AND kind IN ('image','video')`, id, runID).Scan(&localPath, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("artifact")
	}
	if err != nil {
		return err
	}
	if !strings.HasPrefix(localPath, mediaMountRoot+"/") {
		return mgmtNotFound("artifact")
	}
	relative := strings.TrimPrefix(localPath, mediaMountRoot+"/")
	if !filepath.IsLocal(relative) {
		return mgmtNotFound("artifact")
	}
	root, err := os.OpenRoot(a.mediaDir)
	if err != nil {
		return mgmtNotFound("artifact")
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return mgmtNotFound("artifact")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return mgmtNotFound("artifact")
	}
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/webp", "image/gif", "video/mp4", "video/webm":
	default:
		return mgmtNotFound("artifact")
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	http.ServeContent(w, r, filepath.Base(relative), info.ModTime(), file)
	return nil
}

var taskFeedbackEventPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,160}$`)

type taskFeedbackEvent struct {
	Version   int    `json:"version"`
	EventID   string `json:"eventId"`
	Kind      string `json:"kind"`
	Source    string `json:"source"`
	CreatedAt string `json:"createdAt"`
}

func validTaskFeedbackKind(kind string) bool {
	return kind == "accepted" || kind == "correction" || kind == "redo" || kind == "rejected"
}

func (a *AgentRuntime) recordTaskFeedback(ctx context.Context, runID, eventID, kind, source string) error {
	if source == "conversation" && eventID != "" && !taskFeedbackEventPattern.MatchString(eventID) {
		digest := sha256.Sum256([]byte(eventID))
		eventID = "message:" + hex.EncodeToString(digest[:])
	}
	if !taskFeedbackEventPattern.MatchString(eventID) || !validTaskFeedbackKind(kind) || (source != "admin" && source != "conversation") {
		return coreInvalid("invalid task feedback")
	}
	var exists int
	if err := a.db.QueryRowContext(ctx, "SELECT 1 FROM agent_runs WHERE id = ?", runID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return mgmtNotFound("task")
	} else if err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(runID + "\x00" + source + "\x00" + eventID))
	id := "feedback:" + hex.EncodeToString(digest[:])
	now := time.Now().UTC().Format(time.RFC3339Nano)
	value := taskFeedbackEvent{Version: 1, EventID: eventID, Kind: kind, Source: source, CreatedAt: now}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	ciphertext, err := a.encrypt(encoded)
	if err != nil {
		return err
	}
	result, err := a.db.ExecContext(ctx, `INSERT INTO agent_task_steps
		(id, run_id, step_index, kind, name, status, output_cipher, attempts, created_at, updated_at, finished_at)
		VALUES (?, ?, 0, 'tool', ?, 'succeeded', ?, 1, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`, id, runID, taskFeedbackPrefix+kind, ciphertext, now, now, now)
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil || inserted != 0 {
		return err
	}
	var existingName string
	if err = a.db.QueryRowContext(ctx, "SELECT name FROM agent_task_steps WHERE id = ?", id).Scan(&existingName); err != nil {
		return err
	}
	if existingName != taskFeedbackPrefix+kind {
		return coreInvalid("feedback event already has a different outcome")
	}
	return nil
}

func (a *AgentRuntime) handleTaskFeedback(w http.ResponseWriter, r *http.Request, runID string) error {
	if r.Method != http.MethodPost {
		return mgmtMethodNotAllowed()
	}
	var input struct {
		EventID string `json:"eventId"`
		Kind    string `json:"kind"`
	}
	if err := decodeJSONBody(r, &input); err != nil {
		return coreInvalid("invalid feedback body")
	}
	if err := a.recordTaskFeedback(r.Context(), runID, input.EventID, input.Kind, "admin"); err != nil {
		return err
	}
	w.Header().Set("Cache-Control", "no-store")
	mgmtWriteData(w, http.StatusOK, map[string]any{"runId": runID, "eventId": input.EventID, "kind": input.Kind, "source": "admin"})
	return nil
}

func (a *AgentRuntime) taskFeedbackEvents(ctx context.Context, runID string) ([]taskFeedbackEvent, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT output_cipher FROM agent_task_steps
		WHERE run_id = ? AND kind = 'tool' AND name LIKE 'task_feedback:%'
		ORDER BY created_at, id LIMIT 100`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []taskFeedbackEvent{}
	for rows.Next() {
		var ciphertext []byte
		if err = rows.Scan(&ciphertext); err != nil {
			return nil, err
		}
		plain, decodeErr := a.decrypt(ciphertext)
		if decodeErr != nil {
			return nil, decodeErr
		}
		var value taskFeedbackEvent
		if err = json.Unmarshal(plain, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (a *AgentRuntime) taskOptimizationDetails(ctx context.Context, runID string) (map[string]any, error) {
	feedback, err := a.taskFeedbackEvents(ctx, runID)
	if err != nil {
		return nil, err
	}
	intent, hasIntent, err := a.taskIntentForRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	plans, err := a.visualPlanTaskDetails(ctx, runID)
	if err != nil {
		return nil, err
	}
	quality, err := a.mediaQualityTaskDetails(ctx, runID)
	if err != nil {
		return nil, err
	}
	value := map[string]any{"version": 1, "feedback": feedback, "visualPlans": plans, "mediaQuality": quality}
	if hasIntent {
		value["intent"] = intent
	}
	return value, nil
}

type taskQualityObservability struct {
	WindowHours       int  `json:"windowHours"`
	GeneratedRuns     int  `json:"generatedRuns"`
	DeliveredRuns     int  `json:"deliveredRuns"`
	QualityOperations int  `json:"qualityOperations"`
	Passed            int  `json:"passed"`
	Failed            int  `json:"failed"`
	Unverified        int  `json:"unverified"`
	GenerationRounds  int  `json:"generationRounds"`
	Accepted          int  `json:"accepted"`
	Corrections       int  `json:"corrections"`
	Redos             int  `json:"redos"`
	Rejected          int  `json:"rejected"`
	AdminFeedback     int  `json:"adminFeedback"`
	SampleLimited     bool `json:"sampleLimited"`
}

func (a *AgentRuntime) taskQualityObservability(ctx context.Context) (taskQualityObservability, error) {
	value := taskQualityObservability{WindowHours: 24}
	cutoff := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339Nano)
	if err := a.db.QueryRowContext(ctx, `SELECT count(DISTINCT run_id) FROM agent_task_artifacts
		WHERE kind IN ('image', 'video') AND created_at >= ?`, cutoff).Scan(&value.GeneratedRuns); err != nil {
		return value, err
	}
	if err := a.db.QueryRowContext(ctx, `SELECT count(DISTINCT d.run_id) FROM agent_deliveries d
		WHERE d.phase = 'terminal' AND d.status = 'delivered' AND d.updated_at >= ?
		AND EXISTS (SELECT 1 FROM agent_task_artifacts ar WHERE ar.run_id = d.run_id AND ar.kind IN ('image','video'))`, cutoff).Scan(&value.DeliveredRuns); err != nil {
		return value, err
	}
	rows, err := a.db.QueryContext(ctx, `SELECT name, output_cipher FROM agent_task_steps
		WHERE kind = 'tool' AND updated_at >= ? AND output_cipher IS NOT NULL
		AND (name LIKE 'media_quality:%' OR name LIKE 'task_feedback:%')
		ORDER BY updated_at DESC, id LIMIT 10001`, cutoff)
	if err != nil {
		return value, err
	}
	defer rows.Close()
	for samples := 0; rows.Next(); samples++ {
		if samples == 10000 {
			value.SampleLimited = true
			break
		}
		var name string
		var ciphertext []byte
		if err = rows.Scan(&name, &ciphertext); err != nil {
			return value, err
		}
		plain, decodeErr := a.decrypt(ciphertext)
		if decodeErr != nil {
			return value, decodeErr
		}
		if strings.HasPrefix(name, taskFeedbackPrefix) {
			var event taskFeedbackEvent
			if err = json.Unmarshal(plain, &event); err != nil {
				return value, err
			}
			if event.Source == "admin" {
				value.AdminFeedback++
				continue
			}
			switch event.Kind {
			case "accepted":
				value.Accepted++
			case "correction":
				value.Corrections++
			case "redo":
				value.Redos++
			case "rejected":
				value.Rejected++
			}
			continue
		}
		var record mediaQualityReport
		if err = json.Unmarshal(plain, &record); err != nil {
			return value, err
		}
		for _, attempt := range record.Attempts {
			if attempt.GenerationStatus == "completed" {
				value.GenerationRounds++
			}
		}
		switch record.Status {
		case "passed":
			value.Passed++
		case "failed":
			value.Failed++
		case "unverified":
			value.Unverified++
		default:
			continue
		}
		value.QualityOperations++
	}
	return value, rows.Err()
}

func (a *AgentRuntime) pruneTaskOptimizationMetadata(ctx context.Context, now time.Time) error {
	cutoff := now.UTC().Add(-30 * 24 * time.Hour).Format(time.RFC3339Nano)
	_, err := a.db.ExecContext(ctx, `DELETE FROM agent_task_steps WHERE id IN (
		SELECT s.id FROM agent_task_steps s JOIN agent_runs r ON r.id = s.run_id
		WHERE s.kind = 'tool' AND s.name LIKE 'task_feedback:%' AND s.updated_at < ?
		AND r.state IN ('delivered','failed','cancelled') ORDER BY s.updated_at LIMIT 1000)`, cutoff)
	if err != nil {
		return err
	}
	if err = a.pruneMediaQualityMetadata(ctx, cutoff); err != nil {
		return err
	}
	if err = a.pruneVisualGenerationMetadata(ctx, cutoff); err != nil {
		return err
	}
	return a.pruneTaskIntentMetadata(ctx, cutoff)
}
