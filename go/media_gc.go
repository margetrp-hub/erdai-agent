package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type mediaGCCandidate struct {
	Name       string `json:"name"`
	Bytes      int64  `json:"bytes"`
	ModifiedAt string `json:"modifiedAt"`
}

type mediaGCReport struct {
	ID                        string             `json:"id"`
	DryRun                    bool               `json:"dryRun"`
	StartedAt                 string             `json:"startedAt"`
	FinishedAt                string             `json:"finishedAt"`
	Cutoff                    string             `json:"cutoff"`
	RetentionHours            int                `json:"retentionHours"`
	ScannedFiles              int                `json:"scannedFiles"`
	ScannedBytes              int64              `json:"scannedBytes"`
	CandidateFiles            int                `json:"candidateFiles"`
	CandidateBytes            int64              `json:"candidateBytes"`
	ProtectedFiles            int                `json:"protectedFiles"`
	ProtectedBytes            int64              `json:"protectedBytes"`
	ProtectedActiveTask       int                `json:"protectedActiveTask"`
	ProtectedDelivery         int                `json:"protectedDelivery"`
	ProtectedRecent           int                `json:"protectedRecent"`
	ProtectedPersonaReference int                `json:"protectedPersonaReference"`
	ProtectedAfterScan        int                `json:"protectedAfterScan"`
	SkippedYoung              int                `json:"skippedYoung"`
	SkippedNonRegular         int                `json:"skippedNonRegular"`
	DeletedFiles              int                `json:"deletedFiles"`
	DeletedBytes              int64              `json:"deletedBytes"`
	FailedFiles               int                `json:"failedFiles"`
	ExpiredAttachmentRows     int64              `json:"expiredAttachmentRows"`
	Candidates                []mediaGCCandidate `json:"candidates"`
	Errors                    []string           `json:"errors,omitempty"`
}

func (a *AgentRuntime) startMediaGCWorker(ctx context.Context) {
	a.workers.Add(1)
	go func() {
		defer a.workers.Done()
		for {
			policy := a.documentPolicy()
			timer := time.NewTimer(time.Duration(policy.MediaGCIntervalMinutes) * time.Minute)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				report, err := a.runMediaGCAudited(time.Now().UTC(), policy, false)
				if err != nil {
					log.Printf("media GC failed: %v", err)
				} else if report.DeletedFiles > 0 {
					log.Printf("media GC deleted %d files and reclaimed %d bytes", report.DeletedFiles, report.DeletedBytes)
				}
			}
		}
	}()
}

// runMediaGC preserves the original internal contract for focused callers.
func (a *AgentRuntime) runMediaGC(now time.Time, policy documentPolicy) int {
	report, _ := a.runMediaGCAudited(now, policy, false)
	return report.DeletedFiles
}

func (a *AgentRuntime) runMediaGCAudited(now time.Time, policy documentPolicy, dryRun bool) (mediaGCReport, error) {
	report := mediaGCReport{
		DryRun: dryRun, StartedAt: now.Format(time.RFC3339Nano), RetentionHours: policy.MediaRetentionHours,
		Candidates: []mediaGCCandidate{}, Errors: []string{},
	}
	if a == nil || a.db == nil || strings.TrimSpace(a.mediaDir) == "" {
		return report, fmt.Errorf("media storage is not configured")
	}
	if policy.MediaRetentionHours < 24 {
		return report, fmt.Errorf("media retention must be at least 24 hours")
	}
	a.mediaGCMu.Lock()
	defer a.mediaGCMu.Unlock()

	id, err := randomID("media-gc")
	if err != nil {
		return report, err
	}
	report.ID = id
	cutoff := now.Add(-time.Duration(policy.MediaRetentionHours) * time.Hour)
	report.Cutoff = cutoff.Format(time.RFC3339Nano)
	protected, err := a.protectedMediaFiles(now, cutoff)
	if err != nil {
		return report, err
	}
	entries, err := os.ReadDir(a.mediaDir)
	if err != nil {
		return report, err
	}

	root, err := filepath.Abs(filepath.Clean(a.mediaDir))
	if err != nil {
		return report, err
	}
	for _, entry := range entries {
		path := filepath.Join(root, entry.Name())
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			report.SkippedNonRegular++
			continue
		}
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			report.FailedFiles++
			report.addError(entry.Name(), infoErr)
			continue
		}
		if !info.Mode().IsRegular() {
			report.SkippedNonRegular++
			continue
		}
		report.ScannedFiles++
		report.ScannedBytes += info.Size()
		reasons := protected[entry.Name()]
		if len(reasons) > 0 {
			report.ProtectedFiles++
			report.ProtectedBytes += info.Size()
			if reasons["active_task"] {
				report.ProtectedActiveTask++
			}
			if reasons["pending_delivery"] {
				report.ProtectedDelivery++
			}
			if reasons["recent_attachment"] || reasons["recent_artifact"] || reasons["recent_run"] {
				report.ProtectedRecent++
			}
			if reasons["persona_reference"] {
				report.ProtectedPersonaReference++
			}
			continue
		}
		if !info.ModTime().Before(cutoff) {
			report.SkippedYoung++
			continue
		}
		report.CandidateFiles++
		report.CandidateBytes += info.Size()
		if len(report.Candidates) < 500 {
			report.Candidates = append(report.Candidates, mediaGCCandidate{
				Name: entry.Name(), Bytes: info.Size(), ModifiedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
			})
		}
		if dryRun {
			continue
		}
		// Re-read references immediately before deletion so a newly queued task or delivery wins.
		currentProtected, protectionErr := a.protectedMediaFiles(now, cutoff)
		if protectionErr != nil {
			return report, protectionErr
		}
		if len(currentProtected[entry.Name()]) > 0 {
			report.ProtectedFiles++
			report.ProtectedBytes += info.Size()
			report.ProtectedAfterScan++
			continue
		}
		current, statErr := os.Lstat(path)
		if statErr != nil || !current.Mode().IsRegular() || current.Size() != info.Size() || !current.ModTime().Equal(info.ModTime()) {
			report.FailedFiles++
			if statErr == nil {
				statErr = fmt.Errorf("file changed during scan")
			}
			report.addError(entry.Name(), statErr)
			continue
		}
		if removeErr := os.Remove(path); removeErr != nil {
			report.FailedFiles++
			report.addError(entry.Name(), removeErr)
			continue
		}
		report.DeletedFiles++
		report.DeletedBytes += info.Size()
	}
	if !dryRun {
		result, deleteErr := a.db.Exec("DELETE FROM agent_recent_attachments WHERE expires_at <= ?", now.Format(time.RFC3339Nano))
		if deleteErr != nil {
			report.addError("agent_recent_attachments", deleteErr)
		} else {
			report.ExpiredAttachmentRows, _ = result.RowsAffected()
		}
	}
	report.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err = a.persistMediaGCReport(report); err != nil {
		return report, err
	}
	return report, nil
}

func (report *mediaGCReport) addError(name string, err error) {
	if err == nil || len(report.Errors) >= 20 {
		return
	}
	report.Errors = append(report.Errors, name+": "+err.Error())
}

func (a *AgentRuntime) protectedMediaFiles(now, cutoff time.Time) (map[string]map[string]bool, error) {
	protected := map[string]map[string]bool{}
	add := func(localPath, reason string) {
		base := filepath.Base(strings.TrimSpace(localPath))
		if base == "" || base == "." || base == string(os.PathSeparator) {
			return
		}
		if protected[base] == nil {
			protected[base] = map[string]bool{}
		}
		protected[base][reason] = true
	}

	rows, err := a.db.Query(`SELECT artifact.local_path,
		CASE WHEN run.state IN ('queued', 'running') OR step.status IN ('pending', 'running') THEN 1 ELSE 0 END
		FROM agent_task_artifacts artifact
		JOIN agent_runs run ON run.id = artifact.run_id
		JOIN agent_task_steps step ON step.id = artifact.step_id
		WHERE artifact.created_at >= ? OR run.state IN ('queued', 'running') OR step.status IN ('pending', 'running')`,
		cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("scan task artifact references: %w", err)
	}
	for rows.Next() {
		var localPath string
		var active int
		if err = rows.Scan(&localPath, &active); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read task artifact reference: %w", err)
		}
		reason := "recent_artifact"
		if active == 1 {
			reason = "active_task"
		}
		add(localPath, reason)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate task artifact references: %w", err)
	}
	rows.Close()

	rows, err = a.db.Query(`SELECT attachments_cipher FROM agent_recent_attachments WHERE expires_at > ?`, now.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("scan recent attachment references: %w", err)
	}
	for rows.Next() {
		var ciphertext []byte
		if err = rows.Scan(&ciphertext); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read recent attachment reference: %w", err)
		}
		if err = a.addEncryptedTransportAttachments(ciphertext, "recent_attachment", add); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode recent attachment reference: %w", err)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate recent attachment references: %w", err)
	}
	rows.Close()

	rows, err = a.db.Query(`SELECT attachments_cipher, state FROM agent_runs
		WHERE attachments_cipher IS NOT NULL AND length(attachments_cipher) > 0
		AND (created_at >= ? OR state IN ('queued', 'running'))`, cutoff.Format(time.RFC3339Nano))
	if err != nil {
		return nil, fmt.Errorf("scan run attachment references: %w", err)
	}
	for rows.Next() {
		var ciphertext []byte
		var state string
		if err = rows.Scan(&ciphertext, &state); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read run attachment reference: %w", err)
		}
		reason := "recent_run"
		if state == "queued" || state == "running" {
			reason = "active_task"
		}
		if err = a.addEncryptedTransportAttachments(ciphertext, reason, add); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode run attachment reference: %w", err)
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate run attachment references: %w", err)
	}
	rows.Close()

	rows, err = a.db.Query(`SELECT payload_json FROM agent_deliveries WHERE status IN ('pending', 'sending')`)
	if err != nil {
		return nil, fmt.Errorf("scan pending delivery references: %w", err)
	}
	for rows.Next() {
		var payload string
		if err = rows.Scan(&payload); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read pending delivery reference: %w", err)
		}
		var message transportDeliveryMessage
		if err = json.Unmarshal([]byte(payload), &message); err != nil {
			rows.Close()
			return nil, fmt.Errorf("decode pending delivery reference: %w", err)
		}
		for _, attachment := range message.Attachments {
			add(attachment.LocalPath, "pending_delivery")
		}
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate pending delivery references: %w", err)
	}
	rows.Close()

	referenceDB := a.db
	if a.configStore != nil && a.configStore.db != nil {
		referenceDB = a.configStore.db
	}
	rows, err = referenceDB.Query(`SELECT storage_name FROM persona_visual_references
		WHERE enabled = 1`)
	if err != nil {
		return nil, fmt.Errorf("scan persona visual references: %w", err)
	}
	for rows.Next() {
		var storageName string
		if err = rows.Scan(&storageName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("read persona visual reference: %w", err)
		}
		add(storageName, "persona_reference")
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate persona visual references: %w", err)
	}
	rows.Close()
	return protected, nil
}

func (a *AgentRuntime) addEncryptedTransportAttachments(ciphertext []byte, reason string, add func(string, string)) error {
	plain, err := a.decrypt(ciphertext)
	if err != nil {
		return err
	}
	var attachments []transportAttachment
	if err = json.Unmarshal(plain, &attachments); err != nil {
		return err
	}
	for _, attachment := range attachments {
		add(attachment.LocalPath, reason)
	}
	return nil
}

func (a *AgentRuntime) persistMediaGCReport(report mediaGCReport) error {
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = a.db.Exec(`INSERT INTO agent_media_gc_runs
		(id, dry_run, started_at, finished_at, retention_hours, scanned_files, candidate_files,
		 candidate_bytes, protected_files, deleted_files, deleted_bytes, failed_files, report_json)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, report.ID, boolInt(report.DryRun), report.StartedAt,
		report.FinishedAt, report.RetentionHours, report.ScannedFiles, report.CandidateFiles,
		report.CandidateBytes, report.ProtectedFiles, report.DeletedFiles, report.DeletedBytes,
		report.FailedFiles, string(encoded))
	return err
}

func (a *AgentRuntime) handleMediaGCManagement(w http.ResponseWriter, r *http.Request) error {
	if r.Method == http.MethodGet {
		rows, err := a.db.QueryContext(r.Context(), `SELECT report_json FROM agent_media_gc_runs ORDER BY started_at DESC LIMIT 20`)
		if err != nil {
			return err
		}
		defer rows.Close()
		reports := []mediaGCReport{}
		for rows.Next() {
			var raw string
			var report mediaGCReport
			if rows.Scan(&raw) == nil && json.Unmarshal([]byte(raw), &report) == nil {
				reports = append(reports, report)
			}
		}
		mgmtWriteData(w, http.StatusOK, reports)
		return rows.Err()
	}
	if r.Method != http.MethodPost {
		return mgmtMethodNotAllowed()
	}
	var input struct {
		DryRun *bool `json:"dryRun"`
	}
	if err := decodeJSONBody(r, &input); err != nil || input.DryRun == nil {
		return coreInvalid("dryRun must be explicitly set")
	}
	report, err := a.runMediaGCAudited(time.Now().UTC(), a.documentPolicy(), *input.DryRun)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, report)
	return nil
}
