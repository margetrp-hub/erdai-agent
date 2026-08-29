package main

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	maxPersonaReferenceImageBytes   = 12 * 1024 * 1024
	maxPersonaReferenceVideoBytes   = 64 * 1024 * 1024
	maxPersonaReferencePackageBytes = 128 * 1024 * 1024
	maxPersonaReferencePackageItems = 32
)

var personaVisualReferenceCategories = map[string]struct{}{
	"identity": {}, "style": {}, "expression": {}, "makeup": {}, "outfit": {}, "scene": {}, "motion": {},
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

type personaVisualReference struct {
	ID           string `json:"id"`
	PersonaID    string `json:"personaId"`
	MediaType    string `json:"mediaType"`
	MimeType     string `json:"mimeType"`
	OriginalName string `json:"originalName"`
	ByteSize     int64  `json:"byteSize"`
	Category     string `json:"category"`
	Label        string `json:"label"`
	PromptNotes  string `json:"promptNotes"`
	IsPrimary    bool   `json:"isPrimary"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sortOrder"`
	ContentURL   string `json:"contentUrl"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type personaVisualReferencePayload struct {
	Category    *string `json:"category"`
	Label       *string `json:"label"`
	PromptNotes *string `json:"promptNotes"`
	IsPrimary   *bool   `json:"isPrimary"`
	Enabled     *bool   `json:"enabled"`
	SortOrder   *int    `json:"sortOrder"`
}

var personaVisualReferenceFields = coreFieldSet("category", "label", "promptNotes", "isPrimary", "enabled", "sortOrder")

type personaVisualReferenceManifest struct {
	Format     string                               `json:"format"`
	Version    int                                  `json:"version"`
	PersonaID  string                               `json:"personaId"`
	References []personaVisualReferenceManifestItem `json:"references"`
}

type personaVisualReferenceManifestItem struct {
	File         string `json:"file"`
	MediaType    string `json:"mediaType"`
	MimeType     string `json:"mimeType"`
	OriginalName string `json:"originalName"`
	Category     string `json:"category"`
	Label        string `json:"label"`
	PromptNotes  string `json:"promptNotes"`
	IsPrimary    bool   `json:"isPrimary"`
	Enabled      bool   `json:"enabled"`
	SortOrder    int    `json:"sortOrder"`
}

func (s *coreConfigStore) handlePersonaVisualReferences(w http.ResponseWriter, r *http.Request, namespace, personaID string, parts []string) error {
	if len(parts) == 0 {
		if r.Method == http.MethodGet {
			items, err := s.listPersonaVisualReferences(namespace, personaID)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
			return nil
		}
		if r.Method == http.MethodPost {
			item, err := s.uploadPersonaVisualReference(w, r, namespace, personaID)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": item})
			return nil
		}
		return mgmtMethodNotAllowed()
	}
	if len(parts) == 1 && parts[0] == "clone" {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		var payload struct {
			TargetPersonaID string `json:"targetPersonaId"`
		}
		if err := decodeJSONBody(r, &payload); err != nil {
			return err
		}
		items, err := s.clonePersonaVisualReferences(namespace, personaID, payload.TargetPersonaID)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
		return nil
	}
	if len(parts) == 1 && parts[0] == "export" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		return s.exportPersonaVisualReferences(w, r, namespace, personaID)
	}
	if len(parts) == 1 && parts[0] == "import" {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		items, err := s.importPersonaVisualReferences(w, r, namespace, personaID)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusCreated, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
		return nil
	}
	referenceID, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	if len(parts) == 2 && parts[1] == "content" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		return s.servePersonaVisualReference(w, r, namespace, personaID, referenceID)
	}
	if len(parts) != 1 {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference route not found"}
	}
	switch r.Method {
	case http.MethodGet:
		item, found, err := s.personaVisualReference(namespace, personaID, referenceID)
		if err != nil {
			return err
		}
		if !found {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": item})
	case http.MethodPut:
		var payload personaVisualReferencePayload
		fields, err := decodeCoreObject(r, personaVisualReferenceFields, "visual reference", &payload)
		if err != nil {
			return err
		}
		item, found, err := s.updatePersonaVisualReference(namespace, personaID, referenceID, payload, fields)
		if err != nil {
			return err
		}
		if !found {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": item})
	case http.MethodDelete:
		deleted, err := s.deletePersonaVisualReference(namespace, personaID, referenceID)
		if err != nil {
			return err
		}
		if !deleted {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": referenceID, "deleted": true}})
	default:
		return mgmtMethodNotAllowed()
	}
	return nil
}

func (s *coreConfigStore) exportPersonaVisualReferences(w http.ResponseWriter, r *http.Request, namespace, personaID string) error {
	if strings.TrimSpace(s.mediaDir) == "" {
		return coreInvalid("media storage is not configured")
	}
	if _, found, err := s.persona(namespace, personaID); err != nil {
		return err
	} else if !found {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
	}
	rows, err := s.db.Query(`SELECT id, media_type, mime_type, original_name, category, label, prompt_notes,
		is_primary, enabled, sort_order, storage_name FROM persona_visual_references
		WHERE persona_id = ? ORDER BY is_primary DESC, sort_order, created_at`, personaID)
	if err != nil {
		return err
	}
	defer rows.Close()
	manifest := personaVisualReferenceManifest{Format: "erdai-visual-references", Version: 1, PersonaID: personaID, References: make([]personaVisualReferenceManifestItem, 0)}
	files := make([]struct {
		name string
		data []byte
	}, 0)
	var total int64
	for rows.Next() {
		var id, mediaType, mimeType, originalName, category, label, notes, storageName string
		var primary, enabled, sortOrder int
		if err := rows.Scan(&id, &mediaType, &mimeType, &originalName, &category, &label, &notes, &primary, &enabled, &sortOrder, &storageName); err != nil {
			return err
		}
		name := filepath.Base(storageName)
		if name == "." || name == "" || name != storageName {
			return coreInvalid("visual reference content unavailable")
		}
		data, err := os.ReadFile(filepath.Join(s.mediaDir, name))
		if errors.Is(err, os.ErrNotExist) {
			return coreInvalid("visual reference content missing")
		}
		if err != nil {
			return err
		}
		total += int64(len(data))
		if total > maxPersonaReferencePackageBytes {
			return coreInvalid("visual reference package exceeds 128 MiB")
		}
		entry := "references/" + id + filepath.Ext(name)
		manifest.References = append(manifest.References, personaVisualReferenceManifestItem{
			File: entry, MediaType: mediaType, MimeType: mimeType, OriginalName: sanitizePersonaReferenceName(originalName),
			Category: category, Label: label, PromptNotes: notes, IsPrimary: primary != 0, Enabled: enabled != 0, SortOrder: sortOrder,
		})
		files = append(files, struct {
			name string
			data []byte
		}{name: entry, data: data})
	}
	if err := rows.Err(); err != nil {
		return err
	}
	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	manifestFile, err := archive.Create("manifest.json")
	if err != nil {
		return err
	}
	if _, err = manifestFile.Write(manifestJSON); err != nil {
		return err
	}
	for _, file := range files {
		entry, err := archive.Create(file.name)
		if err != nil {
			return err
		}
		if _, err = entry.Write(file.data); err != nil {
			return err
		}
	}
	if err = archive.Close(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="persona-`+personaID+`-visual-references.erdai.zip"`)
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(http.StatusOK)
	_, err = w.Write(output.Bytes())
	return err
}

func (s *coreConfigStore) importPersonaVisualReferences(w http.ResponseWriter, r *http.Request, namespace, personaID string) ([]personaVisualReference, error) {
	if strings.TrimSpace(s.mediaDir) == "" {
		return nil, coreInvalid("media storage is not configured")
	}
	if _, found, err := s.persona(namespace, personaID); err != nil {
		return nil, err
	} else if !found {
		return nil, &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPersonaReferencePackageBytes+1024)
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
		return nil, coreInvalid("visual reference package is invalid or too large")
	}
	file, header, err := r.FormFile("package")
	if err != nil {
		file, header, err = r.FormFile("file")
	}
	if err != nil {
		return nil, coreInvalid("package file is required")
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > maxPersonaReferencePackageBytes {
		return nil, coreInvalid("visual reference package exceeds 128 MiB")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxPersonaReferencePackageBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxPersonaReferencePackageBytes {
		return nil, coreInvalid("visual reference package exceeds 128 MiB")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, coreInvalid("visual reference package is not a valid ZIP")
	}
	entries := make(map[string]*zip.File, len(archive.File))
	var manifestFile *zip.File
	for _, entry := range archive.File {
		clean := path.Clean(entry.Name)
		if clean != entry.Name || path.IsAbs(entry.Name) || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
			return nil, coreInvalid("visual reference package contains an unsafe path")
		}
		if entry.FileInfo().Mode()&os.ModeSymlink != 0 || !entry.FileInfo().Mode().IsRegular() {
			return nil, coreInvalid("visual reference package contains unsupported entries")
		}
		if _, exists := entries[entry.Name]; exists {
			return nil, coreInvalid("visual reference package contains duplicate entries")
		}
		entries[entry.Name] = entry
		if entry.Name == "manifest.json" {
			manifestFile = entry
		}
	}
	if manifestFile == nil {
		return nil, coreInvalid("visual reference package is missing manifest.json")
	}
	manifestReader, err := manifestFile.Open()
	if err != nil {
		return nil, err
	}
	manifestJSON, err := io.ReadAll(io.LimitReader(manifestReader, 2*1024*1024))
	_ = manifestReader.Close()
	if err != nil {
		return nil, err
	}
	var manifest personaVisualReferenceManifest
	if err = json.Unmarshal(manifestJSON, &manifest); err != nil || manifest.Format != "erdai-visual-references" || manifest.Version != 1 {
		return nil, coreInvalid("unsupported visual reference package format")
	}
	if len(manifest.References) > maxPersonaReferencePackageItems {
		return nil, coreInvalid("visual reference package contains too many items")
	}
	type importedReference struct {
		meta personaVisualReferenceManifestItem
		data []byte
		ext  string
	}
	items := make([]importedReference, 0, len(manifest.References))
	seenFiles := make(map[string]struct{}, len(manifest.References))
	var total int64
	primaryAssigned := false
	for _, meta := range manifest.References {
		clean := path.Clean(meta.File)
		if clean != meta.File || path.IsAbs(meta.File) || !strings.HasPrefix(meta.File, "references/") || strings.Contains(meta.File, "\\") {
			return nil, coreInvalid("visual reference package contains an unsafe reference path")
		}
		if _, exists := seenFiles[meta.File]; exists {
			return nil, coreInvalid("visual reference package contains duplicate references")
		}
		seenFiles[meta.File] = struct{}{}
		entry := entries[meta.File]
		if entry == nil {
			return nil, coreInvalid("visual reference package is missing a referenced file")
		}
		if meta.MediaType != "image" && meta.MediaType != "video" {
			return nil, coreInvalid("visual reference media type is invalid")
		}
		if _, ok := personaVisualReferenceCategories[meta.Category]; !ok {
			return nil, coreInvalid("visual reference category is invalid")
		}
		if meta.SortOrder < 0 || meta.SortOrder > 100000 {
			return nil, coreInvalid("visual reference sort order is invalid")
		}
		if meta.IsPrimary && (meta.MediaType != "image" || primaryAssigned) {
			return nil, coreInvalid("visual reference package must contain at most one primary image")
		}
		if meta.IsPrimary {
			primaryAssigned = true
		}
		itemReader, err := entry.Open()
		if err != nil {
			return nil, err
		}
		itemData, err := io.ReadAll(io.LimitReader(itemReader, maxPersonaReferenceVideoBytes+1))
		_ = itemReader.Close()
		if err != nil {
			return nil, err
		}
		if len(itemData) == 0 || len(itemData) > maxPersonaReferenceVideoBytes {
			return nil, coreInvalid("visual reference file is empty or exceeds 64 MiB")
		}
		if meta.MediaType == "image" && len(itemData) > maxPersonaReferenceImageBytes {
			return nil, coreInvalid("image reference exceeds 12 MiB")
		}
		peek := itemData
		if len(peek) > 512 {
			peek = peek[:512]
		}
		detected := strings.ToLower(http.DetectContentType(peek))
		mediaType, ext := personaVisualReferenceMediaType(meta.MimeType, detected, filepath.Ext(meta.File), peek)
		if mediaType == "" || mediaType != meta.MediaType {
			return nil, coreInvalid("visual reference file content does not match manifest")
		}
		items = append(items, importedReference{meta: meta, data: itemData, ext: ext})
		total += int64(len(itemData))
		if total > maxPersonaReferencePackageBytes {
			return nil, coreInvalid("visual reference package exceeds 128 MiB")
		}
	}
	if len(items) == 0 {
		return []personaVisualReference{}, nil
	}
	if err = os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return nil, err
	}
	createdFiles := make([]string, 0, len(items))
	cleanup := func() {
		for _, name := range createdFiles {
			_ = os.Remove(name)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if primaryAssigned {
		if _, err = tx.Exec("UPDATE persona_visual_references SET is_primary = 0, updated_at = ? WHERE persona_id = ?", now, personaID); err != nil {
			return nil, err
		}
	}
	result := make([]personaVisualReference, 0, len(items))
	for _, imported := range items {
		id, err := randomID("persona-ref")
		if err != nil {
			cleanup()
			return nil, err
		}
		storageName := id + imported.ext
		destination := filepath.Join(s.mediaDir, storageName)
		if err = os.WriteFile(destination, imported.data, 0o600); err != nil {
			cleanup()
			return nil, err
		}
		createdFiles = append(createdFiles, destination)
		if _, err = tx.Exec(`INSERT INTO persona_visual_references
			(id, persona_id, media_type, mime_type, original_name, storage_name, byte_size,
			 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, personaID, imported.meta.MediaType,
			mimeTypeForReference(imported.meta.MimeType, http.DetectContentType(imported.data[:minInt(len(imported.data), 512)]), imported.meta.MediaType),
			sanitizePersonaReferenceName(imported.meta.OriginalName), storageName, len(imported.data), imported.meta.Category,
			strings.TrimSpace(imported.meta.Label), strings.TrimSpace(imported.meta.PromptNotes), boolInt(imported.meta.IsPrimary), boolInt(imported.meta.Enabled), imported.meta.SortOrder, now, now); err != nil {
			cleanup()
			return nil, err
		}
		result = append(result, personaVisualReference{ID: id, PersonaID: personaID, MediaType: imported.meta.MediaType,
			MimeType: imported.meta.MimeType, OriginalName: sanitizePersonaReferenceName(imported.meta.OriginalName), ByteSize: int64(len(imported.data)),
			Category: imported.meta.Category, Label: strings.TrimSpace(imported.meta.Label), PromptNotes: strings.TrimSpace(imported.meta.PromptNotes),
			IsPrimary: imported.meta.IsPrimary, Enabled: imported.meta.Enabled, SortOrder: imported.meta.SortOrder,
			ContentURL: "/api/v1/personas/" + personaID + "/visual-references/" + id + "/content?namespace=default", CreatedAt: now, UpdatedAt: now})
	}
	if err = tx.Commit(); err != nil {
		cleanup()
		return nil, err
	}
	return result, nil
}

func (s *coreConfigStore) clonePersonaVisualReferences(namespace, sourcePersonaID, targetPersonaID string) ([]personaVisualReference, error) {
	sourcePersonaID = strings.TrimSpace(sourcePersonaID)
	targetPersonaID = strings.TrimSpace(targetPersonaID)
	if sourcePersonaID == "" || targetPersonaID == "" || sourcePersonaID == targetPersonaID {
		return nil, coreInvalid("source and target persona must be different")
	}
	if strings.TrimSpace(s.mediaDir) == "" {
		return nil, coreInvalid("visual reference content unavailable")
	}
	if _, found, err := s.persona(namespace, targetPersonaID); err != nil {
		return nil, err
	} else if !found {
		return nil, &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "target persona not found"}
	}
	query := `SELECT r.id, r.media_type, r.mime_type, r.original_name, r.byte_size,
		r.category, r.label, r.prompt_notes, r.enabled, r.sort_order, r.storage_name
		FROM persona_visual_references r JOIN personas p ON p.id = r.persona_id
		WHERE p.namespace = ? AND r.persona_id = ?
		ORDER BY r.is_primary DESC, r.sort_order, r.created_at`
	rows, err := s.db.Query(query, namespace, sourcePersonaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type cloneRow struct {
		id, mediaType, mimeType, originalName string
		byteSize                              int64
		category, label, promptNotes          string
		enabled                               bool
		sortOrder                             int
		storageName                           string
	}
	clones := make([]cloneRow, 0)
	for rows.Next() {
		var row cloneRow
		var enabled int
		if err := rows.Scan(&row.id, &row.mediaType, &row.mimeType, &row.originalName, &row.byteSize,
			&row.category, &row.label, &row.promptNotes, &enabled, &row.sortOrder, &row.storageName); err != nil {
			return nil, err
		}
		row.enabled = enabled != 0
		clones = append(clones, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(clones) == 0 {
		return []personaVisualReference{}, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE persona_visual_references SET is_primary = 0 WHERE persona_id = ?`, targetPersonaID); err != nil {
		return nil, err
	}
	createdFiles := make([]string, 0, len(clones))
	result := make([]personaVisualReference, 0, len(clones))
	primaryAssigned := false
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, row := range clones {
		name := filepath.Base(row.storageName)
		if name == "." || name == "" || name != row.storageName {
			return nil, coreInvalid("source visual reference content unavailable")
		}
		input, err := os.Open(filepath.Join(s.mediaDir, name))
		if errors.Is(err, os.ErrNotExist) {
			return nil, coreInvalid("source visual reference content missing")
		}
		if err != nil {
			return nil, err
		}
		newID, err := newCoreUUID()
		if err != nil {
			input.Close()
			return nil, err
		}
		ext := filepath.Ext(name)
		storageName := "persona-ref-" + newID + ext
		outputPath := filepath.Join(s.mediaDir, storageName)
		output, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			input.Close()
			return nil, err
		}
		_, copyErr := io.Copy(output, input)
		input.Close()
		closeErr := output.Close()
		if copyErr != nil || closeErr != nil {
			_ = os.Remove(outputPath)
			if copyErr != nil {
				return nil, copyErr
			}
			return nil, closeErr
		}
		createdFiles = append(createdFiles, outputPath)
		isPrimary := row.mediaType == "image" && !primaryAssigned
		if isPrimary {
			primaryAssigned = true
		}
		if _, err = tx.Exec(`INSERT INTO persona_visual_references
			(id, persona_id, media_type, mime_type, original_name, storage_name, byte_size,
			 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newID, targetPersonaID, row.mediaType, row.mimeType, row.originalName, storageName,
			row.byteSize, row.category, row.label, row.promptNotes, boolInt(isPrimary), boolInt(row.enabled),
			row.sortOrder, now, now); err != nil {
			for _, path := range createdFiles {
				_ = os.Remove(path)
			}
			return nil, err
		}
		result = append(result, personaVisualReference{ID: newID, PersonaID: targetPersonaID, MediaType: row.mediaType,
			MimeType: row.mimeType, OriginalName: row.originalName, ByteSize: row.byteSize, Category: row.category,
			Label: row.label, PromptNotes: row.promptNotes, IsPrimary: isPrimary, Enabled: row.enabled,
			SortOrder: row.sortOrder, ContentURL: "/api/v1/personas/" + targetPersonaID + "/visual-references/" + newID + "/content?namespace=default",
			CreatedAt: now, UpdatedAt: now})
	}
	if err = tx.Commit(); err != nil {
		for _, path := range createdFiles {
			_ = os.Remove(path)
		}
		return nil, err
	}
	return result, nil
}

func (s *coreConfigStore) listPersonaVisualReferences(namespace, personaID string) ([]personaVisualReference, error) {
	rows, err := s.db.Query(`SELECT r.id, r.persona_id, r.media_type, r.mime_type, r.original_name,
		r.byte_size, r.category, r.label, r.prompt_notes, r.is_primary, r.enabled, r.sort_order,
		r.created_at, r.updated_at
		FROM persona_visual_references r JOIN personas p ON p.id = r.persona_id
		WHERE p.namespace = ? AND r.persona_id = ?
		ORDER BY r.is_primary DESC, r.sort_order, r.created_at`, namespace, personaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]personaVisualReference, 0)
	for rows.Next() {
		item, err := scanPersonaVisualReference(rows, personaID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) personaVisualReference(namespace, personaID, referenceID string) (personaVisualReference, bool, error) {
	row := s.db.QueryRow(`SELECT r.id, r.persona_id, r.media_type, r.mime_type, r.original_name,
		r.byte_size, r.category, r.label, r.prompt_notes, r.is_primary, r.enabled, r.sort_order,
		r.created_at, r.updated_at
		FROM persona_visual_references r JOIN personas p ON p.id = r.persona_id
		WHERE p.namespace = ? AND r.persona_id = ? AND r.id = ?`, namespace, personaID, referenceID)
	item, err := scanPersonaVisualReference(row, personaID)
	if errors.Is(err, sql.ErrNoRows) {
		return personaVisualReference{}, false, nil
	}
	return item, err == nil, err
}

func scanPersonaVisualReference(scanner interface{ Scan(...any) error }, personaID string) (personaVisualReference, error) {
	var item personaVisualReference
	var primary, enabled int
	err := scanner.Scan(&item.ID, &item.PersonaID, &item.MediaType, &item.MimeType, &item.OriginalName,
		&item.ByteSize, &item.Category, &item.Label, &item.PromptNotes, &primary, &enabled,
		&item.SortOrder, &item.CreatedAt, &item.UpdatedAt)
	item.PersonaID = personaID
	item.IsPrimary = primary != 0
	item.Enabled = enabled != 0
	item.ContentURL = "/api/v1/personas/" + item.PersonaID + "/visual-references/" + item.ID + "/content?namespace=default"
	return item, err
}

func normalizePersonaVisualReferenceMeta(payload personaVisualReferencePayload, current personaVisualReference) (personaVisualReference, error) {
	var err error
	if payload.Category != nil {
		current.Category, err = normalizeCoreText(*payload.Category, "category", 40, true)
		if err != nil {
			return current, err
		}
		if _, ok := personaVisualReferenceCategories[current.Category]; !ok {
			return current, coreInvalid("category must be identity, expression, makeup, outfit, scene, or motion")
		}
	}
	if payload.Label != nil {
		current.Label, err = normalizeCoreText(*payload.Label, "label", 120, false)
		if err != nil {
			return current, err
		}
	}
	if payload.PromptNotes != nil {
		current.PromptNotes, err = normalizeCoreText(*payload.PromptNotes, "promptNotes", 2000, false)
		if err != nil {
			return current, err
		}
	}
	if payload.IsPrimary != nil {
		current.IsPrimary = *payload.IsPrimary
	}
	if payload.Enabled != nil {
		current.Enabled = *payload.Enabled
	}
	if payload.SortOrder != nil {
		if *payload.SortOrder < 0 || *payload.SortOrder > 100000 {
			return current, coreInvalid("sortOrder must be between 0 and 100000")
		}
		current.SortOrder = *payload.SortOrder
	}
	return current, nil
}

func (s *coreConfigStore) updatePersonaVisualReference(namespace, personaID, referenceID string, payload personaVisualReferencePayload, fields map[string]json.RawMessage) (personaVisualReference, bool, error) {
	current, found, err := s.personaVisualReference(namespace, personaID, referenceID)
	if err != nil || !found {
		return current, found, err
	}
	current, err = normalizePersonaVisualReferenceMeta(payload, current)
	if err != nil {
		return current, false, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return current, false, err
	}
	defer tx.Rollback()
	if current.IsPrimary && current.MediaType != "image" {
		return current, false, coreInvalid("video reference cannot be the primary image")
	}
	if current.IsPrimary {
		if _, err = tx.Exec("UPDATE persona_visual_references SET is_primary = 0, updated_at = ? WHERE persona_id = ?", now, personaID); err != nil {
			return current, false, err
		}
	}
	_, err = tx.Exec(`UPDATE persona_visual_references SET category = ?, label = ?, prompt_notes = ?,
		is_primary = ?, enabled = ?, sort_order = ?, updated_at = ? WHERE id = ? AND persona_id = ?`,
		current.Category, current.Label, current.PromptNotes, boolInt(current.IsPrimary), boolInt(current.Enabled),
		current.SortOrder, now, referenceID, personaID)
	if err != nil {
		return current, false, err
	}
	if err = tx.Commit(); err != nil {
		return current, false, err
	}
	current.UpdatedAt = now
	_ = fields
	return current, true, nil
}

func (s *coreConfigStore) deletePersonaVisualReference(namespace, personaID, referenceID string) (bool, error) {
	item, found, err := s.personaVisualReference(namespace, personaID, referenceID)
	if err != nil || !found {
		return found, err
	}
	var storageName string
	if err = s.db.QueryRow("SELECT storage_name FROM persona_visual_references WHERE id = ? AND persona_id = ?", referenceID, personaID).Scan(&storageName); err != nil {
		return false, err
	}
	result, err := s.db.Exec(`DELETE FROM persona_visual_references WHERE id = ? AND persona_id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_visual_references.persona_id AND p.namespace = ?)`, referenceID, personaID, namespace)
	if err != nil {
		return false, err
	}
	deleted, _ := result.RowsAffected()
	if deleted == 1 && strings.TrimSpace(s.mediaDir) != "" {
		_ = os.Remove(filepath.Join(s.mediaDir, filepath.Base(storageName)))
	}
	_ = item
	return deleted == 1, nil
}

func (s *coreConfigStore) uploadPersonaVisualReference(w http.ResponseWriter, r *http.Request, namespace, personaID string) (personaVisualReference, error) {
	if strings.TrimSpace(s.mediaDir) == "" {
		return personaVisualReference{}, coreInvalid("media storage is not configured")
	}
	if _, found, err := s.persona(namespace, personaID); err != nil {
		return personaVisualReference{}, err
	} else if !found {
		return personaVisualReference{}, &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPersonaReferenceVideoBytes+1024)
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
		return personaVisualReference{}, coreInvalid("multipart form is invalid or too large")
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return personaVisualReference{}, coreInvalid("file is required")
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > maxPersonaReferenceVideoBytes {
		return personaVisualReference{}, coreInvalid("reference file is empty or exceeds 64 MiB")
	}
	peek := make([]byte, 512)
	read, err := io.ReadFull(file, peek)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return personaVisualReference{}, coreInvalid("reference file cannot be read")
	}
	peek = peek[:read]
	mimeType := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	detected := strings.ToLower(http.DetectContentType(peek))
	mediaType, ext := personaVisualReferenceMediaType(mimeType, detected, filepath.Ext(header.Filename), peek)
	if mediaType == "" {
		return personaVisualReference{}, coreInvalid("only PNG, JPEG, WebP, MP4, or WebM references are supported")
	}
	if mediaType == "image" && header.Size > maxPersonaReferenceImageBytes {
		return personaVisualReference{}, coreInvalid("image reference exceeds 12 MiB")
	}
	category := strings.TrimSpace(r.FormValue("category"))
	if category == "" {
		category = "identity"
	}
	label := strings.TrimSpace(r.FormValue("label"))
	notes := strings.TrimSpace(r.FormValue("promptNotes"))
	primary := strings.EqualFold(r.FormValue("isPrimary"), "true") || strings.EqualFold(r.FormValue("isPrimary"), "on")
	sortOrder := 0
	if rawSort := strings.TrimSpace(r.FormValue("sortOrder")); rawSort != "" {
		sortOrder, err = strconv.Atoi(rawSort)
		if err != nil || sortOrder < 0 || sortOrder > 100000 {
			return personaVisualReference{}, coreInvalid("sortOrder must be between 0 and 100000")
		}
	}
	if categoryValue, err := normalizeCoreText(category, "category", 40, true); err != nil {
		return personaVisualReference{}, err
	} else if _, ok := personaVisualReferenceCategories[categoryValue]; !ok {
		return personaVisualReference{}, coreInvalid("category must be identity, style, expression, makeup, outfit, scene, or motion")
	} else {
		category = categoryValue
	}
	if label, err = normalizeCoreText(label, "label", 120, false); err != nil {
		return personaVisualReference{}, err
	}
	if notes, err = normalizeCoreText(notes, "promptNotes", 2000, false); err != nil {
		return personaVisualReference{}, err
	}
	if mediaType == "video" {
		primary = false
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return personaVisualReference{}, err
	}
	id, err := randomID("persona-ref")
	if err != nil {
		return personaVisualReference{}, err
	}
	storageName := id + ext
	if err = os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return personaVisualReference{}, err
	}
	tmp, err := os.CreateTemp(s.mediaDir, ".persona-ref-*.tmp")
	if err != nil {
		return personaVisualReference{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.CopyN(tmp, file, header.Size); err != nil && err != io.EOF {
		tmp.Close()
		return personaVisualReference{}, err
	}
	if err = tmp.Chmod(0o600); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		return personaVisualReference{}, err
	}
	destination := filepath.Join(s.mediaDir, storageName)
	if err = os.Rename(tmpName, destination); err != nil {
		return personaVisualReference{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(destination)
		}
	}()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return personaVisualReference{}, err
	}
	defer tx.Rollback()
	if primary {
		if _, err = tx.Exec("UPDATE persona_visual_references SET is_primary = 0, updated_at = ? WHERE persona_id = ?", now, personaID); err != nil {
			return personaVisualReference{}, err
		}
	}
	_, err = tx.Exec(`INSERT INTO persona_visual_references
		(id, persona_id, media_type, mime_type, original_name, storage_name, byte_size,
		 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, id, personaID, mediaType, mimeTypeForReference(mimeType, detected, mediaType),
		sanitizePersonaReferenceName(header.Filename), storageName, header.Size, category, label, notes, boolInt(primary), sortOrder, now, now)
	if err != nil {
		return personaVisualReference{}, err
	}
	if err = tx.Commit(); err != nil {
		return personaVisualReference{}, err
	}
	committed = true
	item, found, err := s.personaVisualReference(namespace, personaID, id)
	if err != nil || !found {
		return item, err
	}
	return item, nil
}

func sanitizePersonaReferenceName(value string) string {
	value = filepath.Base(strings.TrimSpace(value))
	if value == "." || value == "" {
		return "reference"
	}
	if len([]rune(value)) > 120 {
		value = string([]rune(value)[:120])
	}
	return value
}

func personaVisualReferenceMediaType(headerMime, detected, extension string, peek []byte) (string, string) {
	_ = headerMime
	_ = extension
	switch detected {
	case "image/png":
		return "image", ".png"
	case "image/jpeg":
		return "image", ".jpg"
	case "image/webp":
		return "image", ".webp"
	}
	if len(peek) >= 12 && string(peek[4:8]) == "ftyp" {
		return "video", ".mp4"
	}
	if len(peek) >= 4 && string(peek[:4]) == "\x1a\x45\xdf\xa3" {
		return "video", ".webm"
	}
	return "", ""
}

func mimeTypeForReference(headerMime, detected, mediaType string) string {
	if mediaType == "image" {
		if strings.Contains(headerMime, "png") || detected == "image/png" {
			return "image/png"
		}
		if strings.Contains(headerMime, "webp") || detected == "image/webp" {
			return "image/webp"
		}
		return "image/jpeg"
	}
	if strings.Contains(headerMime, "webm") || detected == "video/webm" {
		return "video/webm"
	}
	return "video/mp4"
}

func (s *coreConfigStore) servePersonaVisualReference(w http.ResponseWriter, r *http.Request, namespace, personaID, referenceID string) error {
	if strings.TrimSpace(s.mediaDir) == "" {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference content unavailable"}
	}
	var storageName, mimeType string
	err := s.db.QueryRow(`SELECT r.storage_name, r.mime_type FROM persona_visual_references r
		JOIN personas p ON p.id = r.persona_id WHERE p.namespace = ? AND r.persona_id = ? AND r.id = ?`, namespace, personaID, referenceID).Scan(&storageName, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference not found"}
	}
	if err != nil {
		return err
	}
	name := filepath.Base(storageName)
	if name == "." || name == "" || name != storageName {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference content unavailable"}
	}
	path := filepath.Join(s.mediaDir, name)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "visual reference content missing"}
	}
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=300")
	http.ServeContent(w, r, name, info.ModTime(), file)
	return nil
}

func (s *coreConfigStore) primaryPersonaVisualReferenceDataURI(personaID string) (string, error) {
	return s.appearanceLibraryPrimaryDataURI(personaID)
}

func (s *coreConfigStore) personaVisualReferencePrompt(personaID string) string {
	return s.appearanceLibraryPrompt(personaID)
}
