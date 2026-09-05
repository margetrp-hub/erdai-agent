package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type appearanceLibrary struct {
	ID                string `json:"id"`
	Namespace         string `json:"namespace"`
	Name              string `json:"name"`
	Description       string `json:"description"`
	VisualDescription string `json:"visualDescription"`
	OutfitLength      string `json:"outfitLength"`
	SourcePersonaID   string `json:"sourcePersonaId,omitempty"`
	Enabled           bool   `json:"enabled"`
	ReferenceCount    int    `json:"referenceCount"`
	PersonaCount      int    `json:"personaCount"`
	PreviewURL        string `json:"previewUrl,omitempty"`
	CreatedAt         string `json:"createdAt"`
	UpdatedAt         string `json:"updatedAt"`
}

type appearanceLibraryReference struct {
	ID              string `json:"id"`
	LibraryID       string `json:"libraryId"`
	MediaType       string `json:"mediaType"`
	MimeType        string `json:"mimeType"`
	OriginalName    string `json:"originalName"`
	ByteSize        int64  `json:"byteSize"`
	Category        string `json:"category"`
	Label           string `json:"label"`
	PromptNotes     string `json:"promptNotes"`
	IsPrimary       bool   `json:"isPrimary"`
	Enabled         bool   `json:"enabled"`
	SortOrder       int    `json:"sortOrder"`
	ContentURL      string `json:"contentUrl"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
	sourcePersonaID string
	owned           bool
}

type appearanceLibraryPayload struct {
	Name              *string `json:"name"`
	Description       *string `json:"description"`
	VisualDescription *string `json:"visualDescription"`
	OutfitLength      *string `json:"outfitLength"`
	SourcePersonaID   *string `json:"sourcePersonaId"`
	Enabled           *bool   `json:"enabled"`
}

var appearanceLibraryFields = coreFieldSet("name", "description", "visualDescription", "outfitLength", "sourcePersonaId", "enabled")

func (s *coreConfigStore) handleAppearanceLibraryRequest(w http.ResponseWriter, r *http.Request, path string) error {
	parts := strings.Split(strings.TrimPrefix(path, "/api/v1/appearance-libraries"), "/")
	if len(parts) > 0 && parts[0] == "" {
		parts = parts[1:]
	}
	namespace, err := normalizeCoreNamespace(r.URL.Query().Get("namespace"))
	if err != nil {
		return err
	}
	if len(parts) == 0 {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAppearanceLibraries(namespace)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
			return nil
		case http.MethodPost:
			var payload appearanceLibraryPayload
			if _, err := decodeCoreObject(r, appearanceLibraryFields, "appearance library", &payload); err != nil {
				return err
			}
			item, err := s.createAppearanceLibrary(namespace, payload)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": item})
			return nil
		default:
			return mgmtMethodNotAllowed()
		}
	}
	libraryID, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, found, err := s.appearanceLibrary(namespace, libraryID)
			if err != nil {
				return err
			}
			if !found {
				return coreNotFound("appearance library not found")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": item})
		case http.MethodPut:
			var payload appearanceLibraryPayload
			fields, err := decodeCoreObject(r, appearanceLibraryFields, "appearance library", &payload)
			if err != nil {
				return err
			}
			item, found, err := s.updateAppearanceLibrary(namespace, libraryID, payload, fields)
			if err != nil {
				return err
			}
			if !found {
				return coreNotFound("appearance library not found")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": item})
		case http.MethodDelete:
			deleted, err := s.deleteAppearanceLibrary(namespace, libraryID)
			if err != nil {
				return err
			}
			if !deleted {
				return coreNotFound("appearance library not found")
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": libraryID, "deleted": true}})
		default:
			return mgmtMethodNotAllowed()
		}
		return nil
	}
	if parts[1] != "references" {
		return coreNotFound("appearance library route not found")
	}
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listAppearanceLibraryReferences(namespace, libraryID)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
		case http.MethodPost:
			item, err := s.uploadAppearanceLibraryReference(w, r, namespace, libraryID)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": item})
		default:
			return mgmtMethodNotAllowed()
		}
		return nil
	}
	referenceID, err := parseCorePathID(parts[2])
	if err != nil {
		return err
	}
	if len(parts) == 4 && parts[3] == "content" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		return s.serveAppearanceLibraryReference(w, r, namespace, libraryID, referenceID)
	}
	if len(parts) != 3 {
		return coreNotFound("appearance reference route not found")
	}
	switch r.Method {
	case http.MethodGet:
		item, found, err := s.appearanceLibraryReference(namespace, libraryID, referenceID)
		if err != nil {
			return err
		}
		if !found {
			return coreNotFound("appearance reference not found")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": item})
	case http.MethodPut:
		var payload personaVisualReferencePayload
		fields, err := decodeCoreObject(r, personaVisualReferenceFields, "appearance reference", &payload)
		if err != nil {
			return err
		}
		item, found, err := s.updateAppearanceLibraryReference(namespace, libraryID, referenceID, payload, fields)
		if err != nil {
			return err
		}
		if !found {
			return coreNotFound("appearance reference not found")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": item})
	case http.MethodDelete:
		deleted, err := s.deleteAppearanceLibraryReference(namespace, libraryID, referenceID)
		if err != nil {
			return err
		}
		if !deleted {
			return coreNotFound("appearance reference not found")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": referenceID, "deleted": true}})
	default:
		return mgmtMethodNotAllowed()
	}
	return nil
}

func coreNotFound(message string) error {
	return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: message}
}

func (s *coreConfigStore) listAppearanceLibraries(namespace string) ([]appearanceLibrary, error) {
	rows, err := s.db.Query(`SELECT l.id, l.namespace, l.name, l.description, l.visual_description, COALESCE(l.source_persona_id, ''),
		l.enabled, l.created_at, l.updated_at, l.outfit_length,
		(SELECT count(*) FROM appearance_library_references r WHERE r.library_id = l.id) +
		(SELECT count(*) FROM persona_visual_references r WHERE r.persona_id = l.source_persona_id),
		(SELECT count(*) FROM persona_appearance_libraries p WHERE p.library_id = l.id)
		FROM appearance_libraries l WHERE l.namespace = ? ORDER BY l.updated_at DESC, l.id`, namespace)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]appearanceLibrary, 0)
	for rows.Next() {
		var item appearanceLibrary
		var enabled int
		if err := rows.Scan(&item.ID, &item.Namespace, &item.Name, &item.Description, &item.VisualDescription, &item.SourcePersonaID,
			&enabled, &item.CreatedAt, &item.UpdatedAt, &item.OutfitLength, &item.ReferenceCount, &item.PersonaCount); err != nil {
			return nil, err
		}
		item.Enabled = enabled != 0
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for index := range items {
		items[index].PreviewURL = s.appearanceLibraryPreviewURL(items[index].ID, items[index].SourcePersonaID)
	}
	return items, nil
}

func (s *coreConfigStore) appearanceLibrary(namespace, id string) (appearanceLibrary, bool, error) {
	var item appearanceLibrary
	var enabled int
	err := s.db.QueryRow(`SELECT l.id, l.namespace, l.name, l.description, l.visual_description, COALESCE(l.source_persona_id, ''),
		l.enabled, l.created_at, l.updated_at, l.outfit_length,
		(SELECT count(*) FROM appearance_library_references r WHERE r.library_id = l.id) +
		(SELECT count(*) FROM persona_visual_references r WHERE r.persona_id = l.source_persona_id),
		(SELECT count(*) FROM persona_appearance_libraries p WHERE p.library_id = l.id)
		FROM appearance_libraries l WHERE l.namespace = ? AND l.id = ?`, namespace, id).Scan(
		&item.ID, &item.Namespace, &item.Name, &item.Description, &item.VisualDescription, &item.SourcePersonaID, &enabled,
		&item.CreatedAt, &item.UpdatedAt, &item.OutfitLength, &item.ReferenceCount, &item.PersonaCount)
	if errors.Is(err, sql.ErrNoRows) {
		return appearanceLibrary{}, false, nil
	}
	item.Enabled = enabled != 0
	item.PreviewURL = s.appearanceLibraryPreviewURL(item.ID, item.SourcePersonaID)
	return item, err == nil, err
}

func (s *coreConfigStore) appearanceLibraryPreviewURL(libraryID, sourcePersonaID string) string {
	var referenceID string
	err := s.db.QueryRow(`SELECT id FROM appearance_library_references
		WHERE library_id = ? AND enabled = 1 AND media_type = 'image'
		ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, libraryID).Scan(&referenceID)
	if errors.Is(err, sql.ErrNoRows) && sourcePersonaID != "" {
		err = s.db.QueryRow(`SELECT id FROM persona_visual_references
			WHERE persona_id = ? AND enabled = 1 AND media_type = 'image'
			ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, sourcePersonaID).Scan(&referenceID)
	}
	if err != nil || strings.TrimSpace(referenceID) == "" {
		return ""
	}
	return "/api/v1/appearance-libraries/" + libraryID + "/references/" + referenceID + "/content?namespace=default"
}

func (s *coreConfigStore) createAppearanceLibrary(namespace string, payload appearanceLibraryPayload) (appearanceLibrary, error) {
	name := ""
	description := ""
	visualDescription := ""
	sourcePersonaID := ""
	if payload.Name != nil {
		name = strings.TrimSpace(*payload.Name)
	}
	if payload.Description != nil {
		description = strings.TrimSpace(*payload.Description)
	}
	if payload.VisualDescription != nil {
		visualDescription = strings.TrimSpace(*payload.VisualDescription)
	}
	if payload.SourcePersonaID != nil {
		sourcePersonaID = strings.TrimSpace(*payload.SourcePersonaID)
	}
	var err error
	if name, err = normalizeCoreText(name, "name", 120, true); err != nil {
		return appearanceLibrary{}, err
	}
	if description, err = normalizeCoreText(description, "description", 1000, false); err != nil {
		return appearanceLibrary{}, err
	}
	if visualDescription, err = normalizeCoreText(visualDescription, "visualDescription", 4000, false); err != nil {
		return appearanceLibrary{}, err
	}
	if sourcePersonaID != "" {
		if _, found, err := s.persona(namespace, sourcePersonaID); err != nil {
			return appearanceLibrary{}, err
		} else if !found {
			return appearanceLibrary{}, coreInvalid("source persona not found in this namespace")
		}
	}
	id, err := newCoreUUID()
	if err != nil {
		return appearanceLibrary{}, err
	}
	outfitLength, err := normalizeOutfitLength(payload.OutfitLength)
	if err != nil {
		return appearanceLibrary{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = s.db.Exec(`INSERT INTO appearance_libraries
		(id, namespace, name, description, visual_description, source_persona_id, enabled, created_at, updated_at, outfit_length)
		VALUES (?, ?, ?, ?, ?, NULLIF(?, ''), 1, ?, ?, ?)`, id, namespace, name, description, visualDescription, sourcePersonaID, now, now, outfitLength); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return appearanceLibrary{}, coreInvalid("appearance library name already exists")
		}
		return appearanceLibrary{}, err
	}
	item, _, err := s.appearanceLibrary(namespace, id)
	return item, err
}

func (s *coreConfigStore) updateAppearanceLibrary(namespace, id string, payload appearanceLibraryPayload, fields map[string]json.RawMessage) (appearanceLibrary, bool, error) {
	current, found, err := s.appearanceLibrary(namespace, id)
	if err != nil || !found {
		return current, found, err
	}
	if payload.Name != nil {
		if current.Name, err = normalizeCoreText(*payload.Name, "name", 120, true); err != nil {
			return current, false, err
		}
	}
	if payload.Description != nil {
		if current.Description, err = normalizeCoreText(*payload.Description, "description", 1000, false); err != nil {
			return current, false, err
		}
	}
	if payload.VisualDescription != nil {
		if current.VisualDescription, err = normalizeCoreText(*payload.VisualDescription, "visualDescription", 4000, false); err != nil {
			return current, false, err
		}
	}
	if payload.Enabled != nil {
		current.Enabled = *payload.Enabled
	}
	if payload.OutfitLength != nil {
		if current.OutfitLength, err = normalizeOutfitLength(payload.OutfitLength); err != nil {
			return current, false, err
		}
	}
	if payload.SourcePersonaID != nil {
		return current, false, coreInvalid("source persona cannot be changed after library creation")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`UPDATE appearance_libraries SET name = ?, description = ?, visual_description = ?, enabled = ?, updated_at = ?, outfit_length = ?
		WHERE namespace = ? AND id = ?`, current.Name, current.Description, current.VisualDescription, boolInt(current.Enabled), now, current.OutfitLength, namespace, id)
	if err != nil {
		return current, false, err
	}
	_ = fields
	current.UpdatedAt = now
	return current, true, nil
}

func (s *coreConfigStore) deleteAppearanceLibrary(namespace, id string) (bool, error) {
	var bound int
	if err := s.db.QueryRow("SELECT count(*) FROM persona_appearance_libraries WHERE library_id = ?", id).Scan(&bound); err != nil {
		return false, err
	}
	if bound > 0 {
		return false, coreInvalid("appearance library is still assigned to a persona")
	}
	var storageNames []string
	rows, err := s.db.Query(`SELECT r.storage_name FROM appearance_library_references r
		JOIN appearance_libraries l ON l.id = r.library_id WHERE l.namespace = ? AND l.id = ?`, namespace, id)
	if err != nil {
		return false, err
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return false, err
		}
		storageNames = append(storageNames, name)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	result, err := s.db.Exec("DELETE FROM appearance_libraries WHERE namespace = ? AND id = ?", namespace, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	if count == 1 && strings.TrimSpace(s.mediaDir) != "" {
		for _, name := range storageNames {
			_ = os.Remove(filepath.Join(s.mediaDir, filepath.Base(name)))
		}
	}
	return count == 1, err
}

func (s *coreConfigStore) listAppearanceLibraryReferences(namespace, libraryID string) ([]appearanceLibraryReference, error) {
	library, found, err := s.appearanceLibrary(namespace, libraryID)
	if err != nil || !found {
		if err != nil {
			return nil, err
		}
		return nil, coreNotFound("appearance library not found")
	}
	owned, err := s.listOwnedAppearanceLibraryReferences(libraryID)
	if err != nil {
		return nil, err
	}
	if library.SourcePersonaID == "" {
		return owned, nil
	}
	source, err := s.listSourceAppearanceLibraryReferences(libraryID, library.SourcePersonaID, false)
	if err != nil {
		return nil, err
	}
	ownedHasPrimary := false
	for _, item := range owned {
		ownedHasPrimary = ownedHasPrimary || item.IsPrimary
	}
	if ownedHasPrimary {
		for index := range source {
			source[index].IsPrimary = false
		}
	}
	return append(source, owned...), nil
}

func (s *coreConfigStore) listSourceAppearanceLibraryReferences(libraryID, sourcePersonaID string, promptOnly bool) ([]appearanceLibraryReference, error) {
	query := `SELECT id, media_type, mime_type, original_name, byte_size,
		category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at
		FROM persona_visual_references WHERE persona_id = ?
		ORDER BY is_primary DESC, sort_order, created_at`
	if promptOnly {
		query = `SELECT id, media_type, mime_type, original_name, byte_size,
			category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at
			FROM persona_visual_references WHERE persona_id = ? AND enabled = 1 AND trim(prompt_notes) <> ''
			ORDER BY is_primary DESC, sort_order, created_at LIMIT 8`
	}
	rows, err := s.db.Query(query, sourcePersonaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]appearanceLibraryReference, 0)
	for rows.Next() {
		item, err := scanAppearanceLibraryReference(rows, libraryID, sourcePersonaID, false)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) listOwnedAppearanceLibraryReferences(libraryID string) ([]appearanceLibraryReference, error) {
	rows, err := s.db.Query(`SELECT id, media_type, mime_type, original_name, byte_size,
		category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at
		FROM appearance_library_references WHERE library_id = ?
		ORDER BY is_primary DESC, sort_order, created_at`, libraryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]appearanceLibraryReference, 0)
	for rows.Next() {
		item, err := scanAppearanceLibraryReference(rows, libraryID, "", true)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func scanAppearanceLibraryReference(scanner interface{ Scan(...any) error }, libraryID, sourcePersonaID string, owned bool) (appearanceLibraryReference, error) {
	var item appearanceLibraryReference
	var primary, enabled int
	err := scanner.Scan(&item.ID, &item.MediaType, &item.MimeType, &item.OriginalName, &item.ByteSize,
		&item.Category, &item.Label, &item.PromptNotes, &primary, &enabled, &item.SortOrder, &item.CreatedAt, &item.UpdatedAt)
	item.LibraryID = libraryID
	item.sourcePersonaID = sourcePersonaID
	item.owned = owned
	item.IsPrimary = primary != 0
	item.Enabled = enabled != 0
	item.ContentURL = "/api/v1/appearance-libraries/" + libraryID + "/references/" + item.ID + "/content?namespace=default"
	return item, err
}

func (s *coreConfigStore) appearanceLibraryReference(namespace, libraryID, referenceID string) (appearanceLibraryReference, bool, error) {
	if library, found, err := s.appearanceLibrary(namespace, libraryID); err != nil || !found {
		return appearanceLibraryReference{}, found, err
	} else if owned, err := s.listOwnedAppearanceLibraryReferences(libraryID); err != nil {
		return appearanceLibraryReference{}, false, err
	} else {
		for _, item := range owned {
			if item.ID == referenceID {
				return item, true, nil
			}
		}
		if library.SourcePersonaID != "" {
			row := s.db.QueryRow(`SELECT id, media_type, mime_type, original_name, byte_size,
			category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at
			FROM persona_visual_references WHERE persona_id = ? AND id = ?`, library.SourcePersonaID, referenceID)
			item, err := scanAppearanceLibraryReference(row, libraryID, library.SourcePersonaID, false)
			if errors.Is(err, sql.ErrNoRows) {
				return appearanceLibraryReference{}, false, nil
			}
			return item, err == nil, err
		}
	}
	return appearanceLibraryReference{}, false, nil
}

func (s *coreConfigStore) updateAppearanceLibraryReference(namespace, libraryID, referenceID string, payload personaVisualReferencePayload, fields map[string]json.RawMessage) (appearanceLibraryReference, bool, error) {
	item, found, err := s.appearanceLibraryReference(namespace, libraryID, referenceID)
	if err != nil || !found {
		return item, found, err
	}
	if !item.owned {
		updated, found, err := s.updatePersonaVisualReference(namespace, item.sourcePersonaID, referenceID, payload, fields)
		if err != nil || !found {
			return item, found, err
		}
		return appearanceLibraryReferenceFromPersona(updated, libraryID, item.sourcePersonaID), true, nil
	}
	current := personaVisualReference{ID: item.ID, PersonaID: item.sourcePersonaID, MediaType: item.MediaType, MimeType: item.MimeType,
		OriginalName: item.OriginalName, ByteSize: item.ByteSize, Category: item.Category, Label: item.Label, PromptNotes: item.PromptNotes,
		IsPrimary: item.IsPrimary, Enabled: item.Enabled, SortOrder: item.SortOrder, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
	current, err = normalizePersonaVisualReferenceMeta(payload, current)
	if err != nil {
		return item, false, err
	}
	if current.IsPrimary && current.MediaType != "image" {
		return item, false, coreInvalid("video reference cannot be the primary image")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.Begin()
	if err != nil {
		return item, false, err
	}
	defer tx.Rollback()
	if current.IsPrimary {
		if _, err = tx.Exec("UPDATE appearance_library_references SET is_primary = 0, updated_at = ? WHERE library_id = ?", now, libraryID); err != nil {
			return item, false, err
		}
	}
	if _, err = tx.Exec(`UPDATE appearance_library_references SET category = ?, label = ?, prompt_notes = ?,
		is_primary = ?, enabled = ?, sort_order = ?, updated_at = ? WHERE library_id = ? AND id = ?`, current.Category,
		current.Label, current.PromptNotes, boolInt(current.IsPrimary), boolInt(current.Enabled), current.SortOrder, now, libraryID, referenceID); err != nil {
		return item, false, err
	}
	if err = tx.Commit(); err != nil {
		return item, false, err
	}
	current.UpdatedAt = now
	return appearanceLibraryReferenceFromPersona(current, libraryID, ""), true, nil
}

func appearanceLibraryReferenceFromPersona(item personaVisualReference, libraryID, sourcePersonaID string) appearanceLibraryReference {
	return appearanceLibraryReference{ID: item.ID, LibraryID: libraryID, MediaType: item.MediaType, MimeType: item.MimeType,
		OriginalName: item.OriginalName, ByteSize: item.ByteSize, Category: item.Category, Label: item.Label, PromptNotes: item.PromptNotes,
		IsPrimary: item.IsPrimary, Enabled: item.Enabled, SortOrder: item.SortOrder, ContentURL: "/api/v1/appearance-libraries/" + libraryID + "/references/" + item.ID + "/content?namespace=default",
		CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt, sourcePersonaID: sourcePersonaID, owned: sourcePersonaID == ""}
}

func (s *coreConfigStore) deleteAppearanceLibraryReference(namespace, libraryID, referenceID string) (bool, error) {
	item, found, err := s.appearanceLibraryReference(namespace, libraryID, referenceID)
	if err != nil || !found {
		return found, err
	}
	if !item.owned {
		return s.deletePersonaVisualReference(namespace, item.sourcePersonaID, referenceID)
	}
	var storageName string
	if err := s.db.QueryRow("SELECT storage_name FROM appearance_library_references WHERE library_id = ? AND id = ?", libraryID, referenceID).Scan(&storageName); err != nil {
		return false, err
	}
	result, err := s.db.Exec("DELETE FROM appearance_library_references WHERE library_id = ? AND id = ?", libraryID, referenceID)
	if err != nil {
		return false, err
	}
	deleted, _ := result.RowsAffected()
	if deleted == 1 && strings.TrimSpace(s.mediaDir) != "" {
		_ = os.Remove(filepath.Join(s.mediaDir, filepath.Base(storageName)))
	}
	return deleted == 1, nil
}

func (s *coreConfigStore) uploadAppearanceLibraryReference(w http.ResponseWriter, r *http.Request, namespace, libraryID string) (appearanceLibraryReference, error) {
	if strings.TrimSpace(s.mediaDir) == "" {
		return appearanceLibraryReference{}, coreInvalid("media storage is not configured")
	}
	if _, found, err := s.appearanceLibrary(namespace, libraryID); err != nil {
		return appearanceLibraryReference{}, err
	} else if !found {
		return appearanceLibraryReference{}, coreNotFound("appearance library not found")
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxPersonaReferenceVideoBytes+1024)
	if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
		return appearanceLibraryReference{}, coreInvalid("multipart form is invalid or too large")
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return appearanceLibraryReference{}, coreInvalid("file is required")
	}
	defer file.Close()
	if header.Size <= 0 || header.Size > maxPersonaReferenceVideoBytes {
		return appearanceLibraryReference{}, coreInvalid("reference file is empty or exceeds 64 MiB")
	}
	peek := make([]byte, 512)
	read, err := io.ReadFull(file, peek)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return appearanceLibraryReference{}, coreInvalid("reference file cannot be read")
	}
	peek = peek[:read]
	headerMime := strings.ToLower(strings.TrimSpace(header.Header.Get("Content-Type")))
	detected := strings.ToLower(http.DetectContentType(peek))
	mediaType, ext := personaVisualReferenceMediaType(headerMime, detected, filepath.Ext(header.Filename), peek)
	if mediaType == "" {
		return appearanceLibraryReference{}, coreInvalid("only PNG, JPEG, WebP, MP4, or WebM references are supported")
	}
	if mediaType == "image" && header.Size > maxPersonaReferenceImageBytes {
		return appearanceLibraryReference{}, coreInvalid("image reference exceeds 12 MiB")
	}
	category := strings.TrimSpace(r.FormValue("category"))
	if category == "" {
		category = "identity"
	}
	label := strings.TrimSpace(r.FormValue("label"))
	notes := strings.TrimSpace(r.FormValue("promptNotes"))
	primary := strings.EqualFold(r.FormValue("isPrimary"), "true") || strings.EqualFold(r.FormValue("isPrimary"), "on")
	if mediaType == "video" {
		primary = false
	}
	if category, err = normalizeCoreText(category, "category", 40, true); err != nil {
		return appearanceLibraryReference{}, err
	}
	if _, ok := personaVisualReferenceCategories[category]; !ok {
		return appearanceLibraryReference{}, coreInvalid("category must be identity, style, expression, makeup, outfit, scene, or motion")
	}
	if label, err = normalizeCoreText(label, "label", 120, false); err != nil {
		return appearanceLibraryReference{}, err
	}
	if notes, err = normalizeCoreText(notes, "promptNotes", 2000, false); err != nil {
		return appearanceLibraryReference{}, err
	}
	if mediaType == "image" && category != "style" && !primary {
		var imageCount int
		if err = s.db.QueryRow(`SELECT count(*) FROM appearance_library_references
			WHERE library_id = ? AND media_type = 'image'`, libraryID).Scan(&imageCount); err != nil {
			return appearanceLibraryReference{}, err
		}
		primary = imageCount == 0
	}
	sortOrder := 0
	if raw := strings.TrimSpace(r.FormValue("sortOrder")); raw != "" {
		sortOrder, err = strconv.Atoi(raw)
		if err != nil || sortOrder < 0 || sortOrder > 100000 {
			return appearanceLibraryReference{}, coreInvalid("sortOrder must be between 0 and 100000")
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return appearanceLibraryReference{}, err
	}
	id, err := randomID("appearance-ref")
	if err != nil {
		return appearanceLibraryReference{}, err
	}
	storageName := id + ext
	if err = os.MkdirAll(s.mediaDir, 0o700); err != nil {
		return appearanceLibraryReference{}, err
	}
	tmp, err := os.CreateTemp(s.mediaDir, ".appearance-ref-*.tmp")
	if err != nil {
		return appearanceLibraryReference{}, err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err = io.CopyN(tmp, file, header.Size); err != nil && err != io.EOF {
		tmp.Close()
		return appearanceLibraryReference{}, err
	}
	if err = tmp.Chmod(0o600); err == nil {
		err = tmp.Close()
	} else {
		_ = tmp.Close()
	}
	if err != nil {
		return appearanceLibraryReference{}, err
	}
	destination := filepath.Join(s.mediaDir, storageName)
	if err = os.Rename(tmpName, destination); err != nil {
		return appearanceLibraryReference{}, err
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
		return appearanceLibraryReference{}, err
	}
	defer tx.Rollback()
	if primary {
		if _, err = tx.Exec("UPDATE appearance_library_references SET is_primary = 0, updated_at = ? WHERE library_id = ?", now, libraryID); err != nil {
			return appearanceLibraryReference{}, err
		}
	}
	_, err = tx.Exec(`INSERT INTO appearance_library_references
		(id, library_id, media_type, mime_type, original_name, storage_name, byte_size,
		 category, label, prompt_notes, is_primary, enabled, sort_order, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?, ?)`, id, libraryID, mediaType,
		mimeTypeForReference(headerMime, detected, mediaType), sanitizePersonaReferenceName(header.Filename), storageName,
		header.Size, category, label, notes, boolInt(primary), sortOrder, now, now)
	if err != nil {
		return appearanceLibraryReference{}, err
	}
	if err = tx.Commit(); err != nil {
		return appearanceLibraryReference{}, err
	}
	committed = true
	item, found, err := s.appearanceLibraryReference(namespace, libraryID, id)
	if err != nil || !found {
		return item, err
	}
	return item, nil
}

func (s *coreConfigStore) serveAppearanceLibraryReference(w http.ResponseWriter, r *http.Request, namespace, libraryID, referenceID string) error {
	if strings.TrimSpace(s.mediaDir) == "" {
		return coreNotFound("appearance reference content unavailable")
	}
	item, found, err := s.appearanceLibraryReference(namespace, libraryID, referenceID)
	if err != nil {
		return err
	}
	if !found {
		return coreNotFound("appearance reference not found")
	}
	var storageName, mimeType string
	if item.owned {
		err = s.db.QueryRow("SELECT storage_name, mime_type FROM appearance_library_references WHERE library_id = ? AND id = ?", libraryID, referenceID).Scan(&storageName, &mimeType)
	} else {
		err = s.db.QueryRow("SELECT storage_name, mime_type FROM persona_visual_references WHERE persona_id = ? AND id = ?", item.sourcePersonaID, referenceID).Scan(&storageName, &mimeType)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return coreNotFound("appearance reference not found")
	}
	if err != nil {
		return err
	}
	name := filepath.Base(storageName)
	if name == "." || name == "" || name != storageName {
		return coreNotFound("appearance reference content unavailable")
	}
	file, err := os.Open(filepath.Join(s.mediaDir, name))
	if errors.Is(err, os.ErrNotExist) {
		return coreNotFound("appearance reference content missing")
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

func (s *coreConfigStore) personaAppearanceLibrary(namespace, personaID string) (appearanceLibrary, bool, error) {
	var libraryID string
	err := s.db.QueryRow(`SELECT pal.library_id FROM persona_appearance_libraries pal
		JOIN personas p ON p.id = pal.persona_id
		WHERE p.namespace = ? AND pal.persona_id = ?`, namespace, personaID).Scan(&libraryID)
	if errors.Is(err, sql.ErrNoRows) {
		return appearanceLibrary{}, false, nil
	}
	if err != nil {
		return appearanceLibrary{}, false, err
	}
	return s.appearanceLibrary(namespace, libraryID)
}

func (s *coreConfigStore) handlePersonaAppearanceLibrary(w http.ResponseWriter, r *http.Request, namespace, personaID string) error {
	if _, found, err := s.persona(namespace, personaID); err != nil {
		return err
	} else if !found {
		return coreNotFound("persona not found")
	}
	switch r.Method {
	case http.MethodGet:
		library, found, err := s.personaAppearanceLibrary(namespace, personaID)
		if err != nil {
			return err
		}
		data := map[string]any{"personaId": personaID, "libraryId": "", "library": nil}
		if found {
			data["libraryId"] = library.ID
			data["library"] = library
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return nil
	case http.MethodPut:
		var payload struct {
			LibraryID string `json:"libraryId"`
		}
		if err := decodeJSONBody(r, &payload); err != nil {
			return err
		}
		libraryID, err := normalizeCoreText(payload.LibraryID, "libraryId", 120, true)
		if err != nil {
			return err
		}
		library, found, err := s.appearanceLibrary(namespace, libraryID)
		if err != nil {
			return err
		}
		if !found || !library.Enabled {
			return coreInvalid("appearance library is not available")
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = s.db.Exec(`INSERT INTO persona_appearance_libraries (persona_id, library_id, updated_at)
			VALUES (?, ?, ?) ON CONFLICT(persona_id) DO UPDATE SET library_id = excluded.library_id, updated_at = excluded.updated_at`, personaID, libraryID, now)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"personaId": personaID, "libraryId": library.ID, "library": library}})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) ensurePersonaAppearanceLibrary(namespace, personaID, personaName, visualDescription string) error {
	libraryID := "persona-appearance-" + personaID
	name := strings.TrimSpace(personaName)
	if name == "" {
		name = personaID
	}
	libraryName := name + " 默认外观库"
	var nameExists int
	if err := s.db.QueryRow(`SELECT count(*) FROM appearance_libraries WHERE namespace = ? AND name = ? AND id <> ?`, namespace, libraryName, libraryID).Scan(&nameExists); err != nil {
		return err
	}
	if nameExists > 0 {
		libraryName += " · " + personaID
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := s.db.Exec(`INSERT OR IGNORE INTO appearance_libraries
		(id, namespace, name, description, visual_description, source_persona_id, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?)`, libraryID, namespace, libraryName, "沿用角色卡已有视觉素材；可被多个角色共用。", visualDescription, personaID, now, now); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO persona_appearance_libraries (persona_id, library_id, updated_at)
		VALUES (?, ?, ?)`, personaID, libraryID, now)
	return err
}

func (s *coreConfigStore) appearanceLibraryPrimaryDataURI(personaID string) (string, error) {
	var libraryID string
	if err := s.db.QueryRow(`SELECT pal.library_id FROM persona_appearance_libraries pal
		JOIN appearance_libraries l ON l.id = pal.library_id
		WHERE pal.persona_id = ? AND l.enabled = 1`, personaID).Scan(&libraryID); err != nil && err != sql.ErrNoRows {
		return "", err
	} else if err == nil {
		return s.primaryAppearanceLibraryDataURI(libraryID)
	}
	return s.primaryPersonaVisualReferenceDataURILegacy(personaID)
}

func (s *coreConfigStore) primaryAppearanceLibraryDataURI(libraryID string) (string, error) {
	var storageName, mimeType string
	err := s.db.QueryRow(`SELECT storage_name, mime_type FROM appearance_library_references
		WHERE library_id = ? AND enabled = 1 AND media_type = 'image'
		ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, libraryID).Scan(&storageName, &mimeType)
	if err == sql.ErrNoRows {
		var sourcePersonaID string
		if sourceErr := s.db.QueryRow("SELECT COALESCE(source_persona_id, '') FROM appearance_libraries WHERE id = ?", libraryID).Scan(&sourcePersonaID); sourceErr != nil {
			return "", sourceErr
		} else if sourcePersonaID == "" {
			return "", nil
		}
		data, sourceErr := s.primaryPersonaVisualReferenceDataURILegacy(sourcePersonaID)
		if sourceErr != nil || data != "" {
			return data, sourceErr
		}
		var avatar string
		if sourceErr = s.db.QueryRow("SELECT avatar_data_uri FROM personas WHERE id = ?", sourcePersonaID).Scan(&avatar); sourceErr != nil {
			return "", sourceErr
		}
		return strings.TrimSpace(avatar), nil
	}
	if err != nil {
		return "", err
	}
	return s.readReferenceDataURI(storageName, mimeType)
}

func (s *coreConfigStore) primaryPersonaVisualReferenceDataURILegacy(personaID string) (string, error) {
	var storageName, mimeType string
	err := s.db.QueryRow(`SELECT storage_name, mime_type FROM persona_visual_references
		WHERE persona_id = ? AND enabled = 1 AND media_type = 'image'
		ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, personaID).Scan(&storageName, &mimeType)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return s.readReferenceDataURI(storageName, mimeType)
}

func (s *coreConfigStore) readReferenceDataURI(storageName, mimeType string) (string, error) {
	name := filepath.Base(storageName)
	if name == "." || name == "" || name != storageName {
		return "", nil
	}
	data, err := os.ReadFile(filepath.Join(s.mediaDir, name))
	if err != nil {
		return "", err
	}
	if len(data) > maxPersonaReferenceImageBytes {
		return "", coreInvalid("primary image reference exceeds 12 MiB")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func (s *coreConfigStore) appearanceLibraryPrompt(personaID string) string {
	var libraryID string
	if err := s.db.QueryRow(`SELECT pal.library_id FROM persona_appearance_libraries pal
		JOIN appearance_libraries l ON l.id = pal.library_id
		WHERE pal.persona_id = ? AND l.enabled = 1`, personaID).Scan(&libraryID); err == nil {
		if rows, err := s.listAppearanceLibraryReferencesForPrompt(libraryID); err == nil {
			return joinAppearancePrompt(rows)
		}
	}
	return s.personaVisualReferencePromptLegacy(personaID)
}

func (s *coreConfigStore) appearanceLibraryVisualDescription(personaID, fallback string) string {
	var value string
	if err := s.db.QueryRow(`SELECT l.visual_description FROM appearance_libraries l
		JOIN persona_appearance_libraries pal ON pal.library_id = l.id
		WHERE pal.persona_id = ? AND l.enabled = 1`, personaID).Scan(&value); err == nil {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func (s *coreConfigStore) personaHasAppearanceLibrary(personaID string) bool {
	var count int
	return s.db.QueryRow(`SELECT count(*) FROM persona_appearance_libraries pal
		JOIN appearance_libraries l ON l.id = pal.library_id
		WHERE pal.persona_id = ? AND l.enabled = 1`, personaID).Scan(&count) == nil && count > 0
}

func normalizeOutfitLength(value *string) (string, error) {
	if value == nil {
		return "auto", nil
	}
	switch *value {
	case "auto", "short", "long":
		return *value, nil
	default:
		return "", coreInvalid("outfitLength must be auto, short or long")
	}
}

func (s *coreConfigStore) appearanceLibraryOutfitLength(personaID string) string {
	var value string
	_ = s.db.QueryRow(`SELECT l.outfit_length FROM appearance_libraries l
		JOIN persona_appearance_libraries pal ON pal.library_id = l.id
		WHERE pal.persona_id = ? AND l.enabled = 1`, personaID).Scan(&value)
	return value
}

func (s *coreConfigStore) personaVisualReferencePromptLegacy(personaID string) string {
	rows, err := s.db.Query(`SELECT media_type, category, label, prompt_notes FROM persona_visual_references
		WHERE persona_id = ? AND enabled = 1 AND trim(prompt_notes) <> ''
		ORDER BY is_primary DESC, sort_order, created_at LIMIT 8`, personaID)
	if err != nil {
		return ""
	}
	defer rows.Close()
	items := make([]appearanceLibraryReference, 0, 8)
	for rows.Next() {
		var item appearanceLibraryReference
		if rows.Scan(&item.MediaType, &item.Category, &item.Label, &item.PromptNotes) == nil {
			item.Enabled = true
			items = append(items, item)
		}
	}
	return joinAppearancePrompt(items)
}

func (s *coreConfigStore) listAppearanceLibraryReferencesForPrompt(libraryID string) ([]appearanceLibraryReference, error) {
	items, err := s.listOwnedAppearanceLibraryReferences(libraryID)
	if err != nil {
		return nil, err
	}
	var sourcePersonaID string
	if err := s.db.QueryRow("SELECT COALESCE(source_persona_id, '') FROM appearance_libraries WHERE id = ?", libraryID).Scan(&sourcePersonaID); err != nil {
		return nil, err
	} else if sourcePersonaID == "" {
		return items, nil
	}
	source, err := s.listSourceAppearanceLibraryReferences(libraryID, sourcePersonaID, true)
	if err != nil {
		return nil, err
	}
	return append(source, items...), nil
}

func joinAppearancePrompt(items []appearanceLibraryReference) string {
	parts := make([]string, 0, 6)
	seen := make(map[string]struct{}, 6)
	for _, item := range items {
		if !item.Enabled || strings.TrimSpace(item.PromptNotes) == "" {
			continue
		}
		prefix := "身份参考"
		if item.Category == "style" || item.MediaType == "video" {
			prefix = "参考风格（仅提取动作、镜头、光线、服装和氛围，不复制人物脸部或身份）"
		}
		line := strings.TrimSpace(prefix + " / " + item.Label + ": " + item.PromptNotes)
		key := prefix + "\x00" + strings.TrimSpace(item.PromptNotes)
		if line == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, line)
		if len(parts) >= 6 {
			break
		}
	}
	return strings.Join(parts, "；")
}
