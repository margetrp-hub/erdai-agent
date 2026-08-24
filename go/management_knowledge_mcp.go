package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type mgmtCapability struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
}

var mgmtCapabilityCatalog = []mgmtCapability{
	{ID: "chat", Label: "Chat", Description: "General conversational completion"},
	{ID: "reasoning", Label: "Reasoning", Description: "Multi-step analysis and planning"},
	{ID: "vision", Label: "Vision", Description: "Understand image inputs"},
	{ID: "tool_calling", Label: "Tool calling", Description: "Emit structured tool calls"},
	{ID: "json_output", Label: "JSON output", Description: "Reliable structured JSON output"},
	{ID: "long_context", Label: "Long context", Description: "Handle extended context windows"},
	{ID: "web_search", Label: "Web search", Description: "Search current web information"},
	{ID: "image_generation", Label: "Image generation", Description: "Generate images"},
	{ID: "video_generation", Label: "Video generation", Description: "Generate videos"},
	{ID: "embedding", Label: "Embedding", Description: "Create semantic embeddings"},
	{ID: "document_reading", Label: "Document reading", Description: "Read Word, PowerPoint, Excel and text attachments"},
	{ID: "code", Label: "Code", Description: "Code-oriented reasoning and generation"},
}

func (s *coreConfigStore) handleManagementCapabilities(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	profiles, err := s.mgmtLaneProfiles()
	if err != nil {
		return err
	}
	lanes := make(map[string]any, len(profiles))
	for _, profile := range profiles {
		lanes[profile.Lane] = map[string]any{
			"required": profile.RequiredCapabilities, "preferred": profile.PreferredCapabilities,
		}
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{
		"capabilities": mgmtCapabilityCatalog, "lanes": lanes,
	})
	return nil
}

type mgmtKnowledgeDocument struct {
	ID          string         `json:"id"`
	Namespace   string         `json:"namespace"`
	Title       string         `json:"title"`
	SourceURI   string         `json:"sourceUri"`
	ContentHash string         `json:"contentHash"`
	Metadata    map[string]any `json:"metadata"`
	CreatedAt   string         `json:"createdAt"`
	UpdatedAt   string         `json:"updatedAt"`
	Content     *string        `json:"content,omitempty"`
}

func mgmtJSONObject(raw string) map[string]any {
	value := map[string]any{}
	if json.Unmarshal([]byte(raw), &value) != nil || value == nil {
		return map[string]any{}
	}
	return value
}

func scanMgmtKnowledge(scanner interface{ Scan(...any) error }, includeContent bool) (mgmtKnowledgeDocument, error) {
	var value mgmtKnowledgeDocument
	var content, metadataJSON string
	err := scanner.Scan(
		&value.ID, &value.Namespace, &value.Title, &value.SourceURI, &content,
		&value.ContentHash, &metadataJSON, &value.CreatedAt, &value.UpdatedAt,
	)
	if err != nil {
		return value, err
	}
	value.Metadata = mgmtJSONObject(metadataJSON)
	if includeContent {
		value.Content = &content
	}
	return value, nil
}

const mgmtKnowledgeSelect = `
	SELECT id, namespace, title, source_uri, content, content_hash, metadata_json,
		created_at, updated_at FROM knowledge_documents`

func (s *coreConfigStore) mgmtKnowledge(namespace, id string) (mgmtKnowledgeDocument, bool, error) {
	value, err := scanMgmtKnowledge(s.db.QueryRow(
		mgmtKnowledgeSelect+" WHERE namespace = ? AND id = ?", namespace, id,
	), true)
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtKnowledgeList(namespace string, limit, offset int) (map[string]any, error) {
	var total int64
	if err := s.db.QueryRow(
		"SELECT count(*) FROM knowledge_documents WHERE namespace = ?", namespace,
	).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(mgmtKnowledgeSelect+`
		WHERE namespace = ? ORDER BY updated_at DESC, id LIMIT ? OFFSET ?
	`, namespace, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mgmtKnowledgeDocument{}
	for rows.Next() {
		value, scanErr := scanMgmtKnowledge(rows, false)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

type mgmtKnowledgePayload struct {
	ID        *string         `json:"id"`
	Namespace *string         `json:"namespace"`
	Title     *string         `json:"title"`
	SourceURI *string         `json:"sourceUri"`
	Content   *string         `json:"content"`
	Metadata  *map[string]any `json:"metadata"`
}

type mgmtKnowledgeValues struct {
	Title, SourceURI, Content, ContentHash string
	Metadata                               map[string]any
}

var (
	mgmtKnowledgeCreateFields  = coreFieldSet("id", "namespace", "title", "sourceUri", "content", "metadata")
	mgmtKnowledgeUpdateFields  = coreFieldSet("title", "sourceUri", "content", "metadata")
	mgmtKnowledgePreviewFields = coreFieldSet("namespace", "query", "limit")
)

func mgmtKnowledgePayloadValues(input mgmtKnowledgePayload, current *mgmtKnowledgeDocument) (mgmtKnowledgeValues, error) {
	value := mgmtKnowledgeValues{Metadata: map[string]any{}}
	if current != nil {
		value.Title = current.Title
		value.SourceURI = current.SourceURI
		value.Content = *current.Content
		value.Metadata = current.Metadata
	}
	var err error
	if input.Title != nil {
		value.Title, err = normalizeCoreText(*input.Title, "title", 500, true)
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("title is required")
	}
	if input.SourceURI != nil {
		value.SourceURI, err = normalizeCoreText(*input.SourceURI, "sourceUri", 2000, false)
		if err != nil {
			return value, err
		}
	}
	if input.Content != nil {
		value.Content, err = normalizeCoreText(*input.Content, "content", 1000000, true)
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("content is required")
	}
	if input.Metadata != nil {
		if *input.Metadata == nil {
			return value, coreInvalid("metadata must be an object")
		}
		value.Metadata = *input.Metadata
	}
	metadataJSON, err := json.Marshal(value.Metadata)
	if err != nil {
		return value, coreInvalid("metadata must be an object")
	}
	if len(metadataJSON) > 64*1024 {
		return value, coreInvalid("metadata is too large")
	}
	hash := sha256.Sum256([]byte(value.Content))
	value.ContentHash = hex.EncodeToString(hash[:])
	return value, nil
}

func (s *coreConfigStore) mgmtCreateKnowledge(r *http.Request) (mgmtKnowledgeDocument, error) {
	var input mgmtKnowledgePayload
	_, err := decodeCoreObject(r, mgmtKnowledgeCreateFields, "knowledge document", &input)
	if err != nil {
		return mgmtKnowledgeDocument{}, err
	}
	id := ""
	if input.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = normalizeCoreText(*input.ID, "id", 120, true)
	}
	if err != nil {
		return mgmtKnowledgeDocument{}, err
	}
	namespace := ""
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	if namespace, err = normalizeCoreNamespace(namespace); err != nil {
		return mgmtKnowledgeDocument{}, err
	}
	if err = s.ensureKnowledgeBaseNamespace(namespace); err != nil {
		return mgmtKnowledgeDocument{}, err
	}
	value, err := mgmtKnowledgePayloadValues(input, nil)
	if err != nil {
		return mgmtKnowledgeDocument{}, err
	}
	var exists int
	if queryErr := s.db.QueryRow("SELECT 1 FROM knowledge_documents WHERE id = ?", id).Scan(&exists); queryErr == nil {
		return mgmtKnowledgeDocument{}, coreInvalid("knowledge document id already exists")
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return mgmtKnowledgeDocument{}, queryErr
	}
	if queryErr := s.db.QueryRow(`
		SELECT 1 FROM knowledge_documents WHERE namespace = ? AND content_hash = ?
	`, namespace, value.ContentHash).Scan(&exists); queryErr == nil {
		return mgmtKnowledgeDocument{}, coreInvalid("knowledge document content already exists in namespace")
	} else if !errors.Is(queryErr, sql.ErrNoRows) {
		return mgmtKnowledgeDocument{}, queryErr
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO knowledge_documents (
			id, namespace, title, source_uri, content, content_hash, metadata_json,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, namespace, value.Title, value.SourceURI, value.Content, value.ContentHash,
		mgmtJSON(value.Metadata), now, now)
	if err != nil {
		return mgmtKnowledgeDocument{}, mgmtConstraintError(err, "knowledge document id or content already exists")
	}
	created, _, err := s.mgmtKnowledge(namespace, id)
	return created, err
}

func (s *coreConfigStore) mgmtUpdateKnowledge(r *http.Request, namespace, id string) (mgmtKnowledgeDocument, bool, error) {
	current, found, err := s.mgmtKnowledge(namespace, id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtKnowledgePayload
	_, err = decodeCoreObject(r, mgmtKnowledgeUpdateFields, "knowledge document", &input)
	if err != nil {
		return current, true, err
	}
	value, err := mgmtKnowledgePayloadValues(input, &current)
	if err != nil {
		return current, true, err
	}
	var duplicateID string
	err = s.db.QueryRow(`
		SELECT id FROM knowledge_documents
		WHERE namespace = ? AND content_hash = ? AND id <> ?
	`, namespace, value.ContentHash, id).Scan(&duplicateID)
	if err == nil {
		return current, true, coreInvalid("knowledge document content already exists in namespace")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return current, true, err
	}
	_, err = s.db.Exec(`
		UPDATE knowledge_documents SET title = ?, source_uri = ?, content = ?,
			content_hash = ?, metadata_json = ?, updated_at = ?
		WHERE namespace = ? AND id = ?
	`, value.Title, value.SourceURI, value.Content, value.ContentHash,
		mgmtJSON(value.Metadata), mgmtNow(), namespace, id)
	if err != nil {
		return current, true, mgmtConstraintError(err, "knowledge document content already exists in namespace")
	}
	updated, _, err := s.mgmtKnowledge(namespace, id)
	return updated, true, err
}

type mgmtKnowledgeHit struct {
	ID        string         `json:"id"`
	Namespace string         `json:"namespace"`
	Title     string         `json:"title"`
	SourceURI string         `json:"sourceUri"`
	Snippet   string         `json:"snippet"`
	Rank      *float64       `json:"rank"`
	Metadata  map[string]any `json:"metadata"`
}

type mgmtKnowledgePreviewPayload struct {
	Namespace *string `json:"namespace"`
	Query     *string `json:"query"`
	Limit     *int    `json:"limit"`
}

func (s *coreConfigStore) mgmtPreviewKnowledge(r *http.Request) (map[string]any, error) {
	var input mgmtKnowledgePreviewPayload
	_, err := decodeCoreObject(r, mgmtKnowledgePreviewFields, "knowledge preview", &input)
	if err != nil {
		return nil, err
	}
	namespace := ""
	if input.Namespace != nil {
		namespace = *input.Namespace
	}
	if namespace, err = normalizeCoreNamespace(namespace); err != nil {
		return nil, err
	}
	if input.Query == nil {
		return nil, coreInvalid("query is required")
	}
	query, err := normalizeCoreText(*input.Query, "query", 500, true)
	if err != nil {
		return nil, err
	}
	limit := 5
	if input.Limit != nil {
		limit = *input.Limit
	}
	if limit < 1 || limit > 50 {
		return nil, coreInvalid("limit must be between 1 and 50")
	}
	items, err := s.mgmtSearchKnowledge(namespace, query, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"namespace": namespace, "query": query, "items": items}, nil
}

func (s *coreConfigStore) mgmtSearchKnowledge(namespace, query string, limit int) ([]mgmtKnowledgeHit, error) {
	hits, err := s.searchHybridKnowledge(namespace, query, limit)
	if err != nil {
		return nil, err
	}
	items := make([]mgmtKnowledgeHit, 0, len(hits))
	for _, hit := range hits {
		items = append(items, mgmtKnowledgeHit{
			ID: hit.ID, Namespace: hit.Namespace, Title: hit.Title, SourceURI: hit.SourceURI,
			Snippet: hit.Snippet, Rank: hit.Rank, Metadata: hit.Metadata,
		})
	}
	return items, nil
}

func (s *coreConfigStore) handleManagementKnowledge(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/knowledge/search-preview" {
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		value, err := s.mgmtPreviewKnowledge(r)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	}
	if path == "/api/v1/knowledge/documents" {
		switch r.Method {
		case http.MethodGet:
			namespace, err := normalizeCoreNamespace(r.URL.Query().Get("namespace"))
			if err != nil {
				return err
			}
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			value, err := s.mgmtKnowledgeList(namespace, limit, offset)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateKnowledge(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/knowledge/documents/")
	if err != nil {
		return err
	}
	id, err = normalizeCoreText(id, "id", 120, true)
	if err != nil {
		return err
	}
	namespace, err := normalizeCoreNamespace(r.URL.Query().Get("namespace"))
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtKnowledge(namespace, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge document")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateKnowledge(r, namespace, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge document")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM knowledge_documents WHERE namespace = ? AND id = ?", namespace, id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "knowledge document"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type mgmtAdminDirective struct {
	ID                 string `json:"id"`
	Content            string `json:"content"`
	Enabled            bool   `json:"enabled"`
	CreatedByAuthority string `json:"createdByAuthority"`
	CreatedAt          string `json:"createdAt"`
	UpdatedAt          string `json:"updatedAt"`
}

func scanMgmtDirective(scanner interface{ Scan(...any) error }) (mgmtAdminDirective, error) {
	var value mgmtAdminDirective
	var enabled int
	err := scanner.Scan(
		&value.ID, &value.Content, &enabled, &value.CreatedByAuthority,
		&value.CreatedAt, &value.UpdatedAt,
	)
	value.Enabled = enabled == 1
	return value, err
}

func (s *coreConfigStore) mgmtDirective(id string) (mgmtAdminDirective, bool, error) {
	value, err := scanMgmtDirective(s.db.QueryRow(`
		SELECT id, content, enabled, created_by_authority, created_at, updated_at
		FROM admin_directives WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtDirectiveList(limit, offset int) (map[string]any, error) {
	var total int64
	if err := s.db.QueryRow("SELECT count(*) FROM admin_directives").Scan(&total); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT id, content, enabled, created_by_authority, created_at, updated_at
		FROM admin_directives ORDER BY created_at, id LIMIT ? OFFSET ?
	`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mgmtAdminDirective{}
	for rows.Next() {
		value, scanErr := scanMgmtDirective(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

type mgmtDirectivePayload struct {
	ID      *string `json:"id"`
	Content *string `json:"content"`
	Enabled *bool   `json:"enabled"`
}

var (
	mgmtDirectiveCreateFields = coreFieldSet("id", "content", "enabled")
	mgmtDirectiveUpdateFields = coreFieldSet("content", "enabled")
)

func (s *coreConfigStore) mgmtCreateDirective(r *http.Request) (mgmtAdminDirective, error) {
	var input mgmtDirectivePayload
	fields, err := decodeCoreObject(r, mgmtDirectiveCreateFields, "administrator directive", &input)
	if err != nil {
		return mgmtAdminDirective{}, err
	}
	if raw, ok := fields["content"]; !ok || strings.TrimSpace(string(raw)) == "null" || input.Content == nil {
		return mgmtAdminDirective{}, coreInvalid("content is required")
	}
	id := ""
	if input.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = normalizeCoreText(*input.ID, "id", 120, true)
	}
	if err != nil {
		return mgmtAdminDirective{}, err
	}
	content, err := normalizeCoreText(*input.Content, "content", 20000, true)
	if err != nil {
		return mgmtAdminDirective{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO admin_directives (
			id, content, enabled, created_by_authority, created_at, updated_at
		) VALUES (?, ?, ?, 'admin', ?, ?)
	`, id, content, boolInt(enabled), now, now)
	if err != nil {
		return mgmtAdminDirective{}, mgmtConstraintError(err, "administrator directive id already exists")
	}
	value, _, err := s.mgmtDirective(id)
	return value, err
}

func (s *coreConfigStore) mgmtUpdateDirective(r *http.Request, id string) (mgmtAdminDirective, bool, error) {
	current, found, err := s.mgmtDirective(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtDirectivePayload
	fields, err := decodeCoreObject(r, mgmtDirectiveUpdateFields, "administrator directive", &input)
	if err != nil {
		return current, true, err
	}
	content := current.Content
	if raw, ok := fields["content"]; ok {
		if strings.TrimSpace(string(raw)) == "null" || input.Content == nil {
			return current, true, coreInvalid("content is required")
		}
		content, err = normalizeCoreText(*input.Content, "content", 20000, true)
		if err != nil {
			return current, true, err
		}
	}
	enabled := current.Enabled
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	_, err = s.db.Exec(`
		UPDATE admin_directives SET content = ?, enabled = ?, updated_at = ? WHERE id = ?
	`, content, boolInt(enabled), mgmtNow(), id)
	if err != nil {
		return current, true, err
	}
	value, _, err := s.mgmtDirective(id)
	return value, true, err
}

func (s *coreConfigStore) handleManagementDirectives(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/runtime/directives" {
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			value, err := s.mgmtDirectiveList(limit, offset)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateDirective(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/runtime/directives/")
	if err != nil {
		return err
	}
	id, err = normalizeCoreText(id, "id", 120, true)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtDirective(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("administrator directive")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateDirective(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("administrator directive")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM admin_directives WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "administrator directive"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type mgmtKnowledgeCandidate struct {
	ID         string   `json:"id"`
	Status     string   `json:"status"`
	Title      string   `json:"title"`
	Content    string   `json:"content"`
	SourceURI  string   `json:"sourceUri"`
	Tags       []string `json:"tags"`
	CreatedAt  string   `json:"createdAt"`
	ReviewedAt *string  `json:"reviewedAt"`
}

func scanMgmtCandidate(scanner interface{ Scan(...any) error }) (mgmtKnowledgeCandidate, error) {
	var value mgmtKnowledgeCandidate
	var tagsJSON string
	var reviewedAt sql.NullString
	err := scanner.Scan(
		&value.ID, &value.Status, &value.Title, &value.Content, &value.SourceURI,
		&tagsJSON, &value.CreatedAt, &reviewedAt,
	)
	if err != nil {
		return value, err
	}
	value.Tags = decodeJSONStringList(tagsJSON)
	if value.Tags == nil {
		value.Tags = []string{}
	}
	if reviewedAt.Valid {
		value.ReviewedAt = &reviewedAt.String
	}
	return value, nil
}

func (s *coreConfigStore) mgmtCandidate(id string) (mgmtKnowledgeCandidate, bool, error) {
	value, err := scanMgmtCandidate(s.db.QueryRow(`
		SELECT id, status, title, content, source_uri, tags_json, created_at, reviewed_at
		FROM knowledge_candidates WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtCandidateList(status string, limit, offset int) (map[string]any, error) {
	where := ""
	parameters := []any{}
	if status != "" {
		where = " WHERE status = ?"
		parameters = append(parameters, status)
	}
	var total int64
	if err := s.db.QueryRow("SELECT count(*) FROM knowledge_candidates"+where, parameters...).Scan(&total); err != nil {
		return nil, err
	}
	parameters = append(parameters, limit, offset)
	rows, err := s.db.Query(`
		SELECT id, status, title, content, source_uri, tags_json, created_at, reviewed_at
		FROM knowledge_candidates`+where+` ORDER BY created_at DESC, id LIMIT ? OFFSET ?
	`, parameters...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []mgmtKnowledgeCandidate{}
	for rows.Next() {
		value, scanErr := scanMgmtCandidate(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

type mgmtCandidatePayload struct {
	ID        *string   `json:"id"`
	Title     *string   `json:"title"`
	Content   *string   `json:"content"`
	SourceURI *string   `json:"sourceUri"`
	Tags      *[]string `json:"tags"`
}

var (
	mgmtCandidateCreateFields = coreFieldSet("id", "title", "content", "sourceUri", "tags")
	mgmtCandidateUpdateFields = coreFieldSet("title", "content", "sourceUri", "tags")
	mgmtCandidateReviewFields = coreFieldSet("decision", "knowledgeNamespace", "authority")
)

func (s *coreConfigStore) mgmtCreateCandidate(r *http.Request) (mgmtKnowledgeCandidate, error) {
	var input mgmtCandidatePayload
	fields, err := decodeCoreObject(r, mgmtCandidateCreateFields, "knowledge candidate", &input)
	if err != nil {
		return mgmtKnowledgeCandidate{}, err
	}
	if raw, ok := fields["title"]; !ok || strings.TrimSpace(string(raw)) == "null" || input.Title == nil {
		return mgmtKnowledgeCandidate{}, coreInvalid("title is required")
	}
	if raw, ok := fields["content"]; !ok || strings.TrimSpace(string(raw)) == "null" || input.Content == nil {
		return mgmtKnowledgeCandidate{}, coreInvalid("content is required")
	}
	id := ""
	if input.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = normalizeCoreText(*input.ID, "id", 110, true)
	}
	if err != nil {
		return mgmtKnowledgeCandidate{}, err
	}
	title, err := normalizeCoreText(*input.Title, "title", 500, true)
	if err != nil {
		return mgmtKnowledgeCandidate{}, err
	}
	content, err := normalizeCoreText(*input.Content, "content", 1000000, true)
	if err != nil {
		return mgmtKnowledgeCandidate{}, err
	}
	sourceURI := ""
	if input.SourceURI != nil {
		sourceURI, err = normalizeCoreText(*input.SourceURI, "sourceUri", 2000, false)
		if err != nil {
			return mgmtKnowledgeCandidate{}, err
		}
	}
	tags := []string{}
	if input.Tags != nil {
		tags, err = normalizeCoreStrings(*input.Tags, "tags", 64, 100)
		if err != nil {
			return mgmtKnowledgeCandidate{}, err
		}
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO knowledge_candidates (
			id, status, title, content, source_uri, tags_json, created_at, reviewed_at
		) VALUES (?, 'pending', ?, ?, ?, ?, ?, NULL)
	`, id, title, content, sourceURI, mgmtJSON(tags), now)
	if err != nil {
		return mgmtKnowledgeCandidate{}, mgmtConstraintError(err, "knowledge candidate id already exists")
	}
	value, _, err := s.mgmtCandidate(id)
	return value, err
}

func (s *coreConfigStore) mgmtUpdateCandidate(r *http.Request, id string) (mgmtKnowledgeCandidate, bool, error) {
	current, found, err := s.mgmtCandidate(id)
	if err != nil || !found {
		return current, found, err
	}
	if current.Status != "pending" {
		return current, true, coreInvalid("reviewed knowledge candidate cannot be edited")
	}
	var input mgmtCandidatePayload
	fields, err := decodeCoreObject(r, mgmtCandidateUpdateFields, "knowledge candidate", &input)
	if err != nil {
		return current, true, err
	}
	title, content, sourceURI, tags := current.Title, current.Content, current.SourceURI, current.Tags
	if raw, ok := fields["title"]; ok {
		if strings.TrimSpace(string(raw)) == "null" || input.Title == nil {
			return current, true, coreInvalid("title is required")
		}
		title, err = normalizeCoreText(*input.Title, "title", 500, true)
		if err != nil {
			return current, true, err
		}
	}
	if raw, ok := fields["content"]; ok {
		if strings.TrimSpace(string(raw)) == "null" || input.Content == nil {
			return current, true, coreInvalid("content is required")
		}
		content, err = normalizeCoreText(*input.Content, "content", 1000000, true)
		if err != nil {
			return current, true, err
		}
	}
	if raw, ok := fields["sourceUri"]; ok {
		if strings.TrimSpace(string(raw)) == "null" {
			sourceURI = ""
		} else if input.SourceURI != nil {
			sourceURI, err = normalizeCoreText(*input.SourceURI, "sourceUri", 2000, false)
			if err != nil {
				return current, true, err
			}
		}
	}
	if raw, ok := fields["tags"]; ok {
		if strings.TrimSpace(string(raw)) == "null" {
			tags = []string{}
		} else if input.Tags != nil {
			tags, err = normalizeCoreStrings(*input.Tags, "tags", 64, 100)
			if err != nil {
				return current, true, err
			}
		}
	}
	_, err = s.db.Exec(`
		UPDATE knowledge_candidates SET title = ?, content = ?, source_uri = ?, tags_json = ?
		WHERE id = ?
	`, title, content, sourceURI, mgmtJSON(tags), id)
	if err != nil {
		return current, true, err
	}
	value, _, err := s.mgmtCandidate(id)
	return value, true, err
}

type mgmtCandidateReviewPayload struct {
	Decision           string  `json:"decision"`
	KnowledgeNamespace *string `json:"knowledgeNamespace"`
	Authority          string  `json:"authority"`
}

type mgmtCandidateReviewResult struct {
	Candidate         mgmtKnowledgeCandidate `json:"candidate"`
	PublishedDocument *mgmtKnowledgeDocument `json:"publishedDocument"`
}

func (s *coreConfigStore) mgmtReviewCandidate(r *http.Request, id string) (mgmtCandidateReviewResult, bool, error) {
	var input mgmtCandidateReviewPayload
	_, err := decodeCoreObject(r, mgmtCandidateReviewFields, "knowledge candidate review", &input)
	if err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	if input.Authority != "admin" {
		return mgmtCandidateReviewResult{}, true, coreInvalid("administrator authority is required to review knowledge")
	}
	decision, err := normalizeCoreText(input.Decision, "decision", 20, true)
	if err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	if decision != "approved" && decision != "rejected" {
		return mgmtCandidateReviewResult{}, true, coreInvalid("knowledge candidate decision must be approved or rejected")
	}
	namespace := ""
	if input.KnowledgeNamespace != nil {
		namespace = *input.KnowledgeNamespace
	} else if err = s.db.QueryRow("SELECT knowledge_namespace FROM runtime_config WHERE id = 1").Scan(&namespace); err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	if namespace, err = normalizeCoreNamespace(namespace); err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	defer tx.Rollback()
	current, err := scanMgmtCandidate(tx.QueryRow(`
		SELECT id, status, title, content, source_uri, tags_json, created_at, reviewed_at
		FROM knowledge_candidates WHERE id = ?
	`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return mgmtCandidateReviewResult{}, false, nil
	}
	if err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	if current.Status != "pending" {
		return mgmtCandidateReviewResult{}, true, coreInvalid("knowledge candidate has already been reviewed")
	}
	reviewedAt := mgmtNow()
	var published *mgmtKnowledgeDocument
	if decision == "approved" {
		if err = syncKnowledgeBaseNamespace(tx, namespace); err != nil {
			return mgmtCandidateReviewResult{}, true, err
		}
		contentHash := sha256.Sum256([]byte(current.Content))
		document := mgmtKnowledgeDocument{
			ID: "candidate-" + current.ID, Namespace: namespace, Title: current.Title,
			SourceURI: current.SourceURI, ContentHash: hex.EncodeToString(contentHash[:]),
			Metadata: map[string]any{
				"candidateId": current.ID, "source": "reviewed_candidate", "tags": current.Tags,
			},
			CreatedAt: reviewedAt, UpdatedAt: reviewedAt,
		}
		document.Content = &current.Content
		_, err = tx.Exec(`
			INSERT INTO knowledge_documents (
				id, namespace, title, source_uri, content, content_hash, metadata_json,
				created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, document.ID, namespace, document.Title, document.SourceURI, current.Content,
			document.ContentHash, mgmtJSON(document.Metadata), reviewedAt, reviewedAt)
		if err != nil {
			return mgmtCandidateReviewResult{}, true,
				mgmtConstraintError(err, "knowledge document id or content already exists")
		}
		published = &document
	}
	if _, err = tx.Exec(`
		UPDATE knowledge_candidates SET status = ?, reviewed_at = ? WHERE id = ?
	`, decision, reviewedAt, id); err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	if err = tx.Commit(); err != nil {
		return mgmtCandidateReviewResult{}, true, err
	}
	current.Status = decision
	current.ReviewedAt = &reviewedAt
	return mgmtCandidateReviewResult{Candidate: current, PublishedDocument: published}, true, nil
}

func (s *coreConfigStore) handleManagementKnowledgeCandidates(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/runtime/knowledge-candidates" {
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			status := strings.TrimSpace(r.URL.Query().Get("status"))
			if status != "" && status != "pending" && status != "approved" && status != "rejected" {
				return coreInvalid("knowledge candidate status is not supported")
			}
			value, err := s.mgmtCandidateList(status, limit, offset)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, value)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateCandidate(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	const prefix = "/api/v1/runtime/knowledge-candidates/"
	if strings.HasSuffix(path, "/review") {
		id, err := mgmtPathID(strings.TrimSuffix(path, "/review"), prefix)
		if err != nil {
			return err
		}
		id, err = normalizeCoreText(id, "id", 120, true)
		if err != nil {
			return err
		}
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		value, found, err := s.mgmtReviewCandidate(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge candidate")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	}
	id, err := mgmtPathID(path, prefix)
	if err != nil {
		return err
	}
	id, err = normalizeCoreText(id, "id", 120, true)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtCandidate(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge candidate")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateCandidate(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge candidate")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM knowledge_candidates WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "knowledge candidate"); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type mgmtMCPConfig struct {
	SecretRef          string            `json:"secretRef"`
	EnvHeaderRefs      map[string]string `json:"envHeaderRefs,omitempty"`
	AllowedTools       []string          `json:"allowedTools"`
	AllowedAuthorities []string          `json:"allowedAuthorities"`
	ApprovalMode       string            `json:"approvalMode"`
	TimeoutSeconds     int               `json:"timeoutSeconds"`
}

type mgmtMCPServer struct {
	ID                 string            `json:"id"`
	Name               string            `json:"name"`
	Transport          string            `json:"transport"`
	Endpoint           string            `json:"endpoint"`
	Command            string            `json:"command"`
	Args               []string          `json:"args"`
	ToolPrefix         string            `json:"toolPrefix"`
	Enabled            bool              `json:"enabled"`
	SecretRef          string            `json:"secretRef"`
	AllowedTools       []string          `json:"allowedTools"`
	AllowedAuthorities []string          `json:"allowedAuthorities"`
	ApprovalMode       string            `json:"approvalMode"`
	TimeoutSeconds     int               `json:"timeoutSeconds"`
	CreatedAt          string            `json:"createdAt"`
	UpdatedAt          string            `json:"updatedAt"`
	EnvHeaderRefs      map[string]string `json:"-"`
}

func scanMgmtMCP(scanner interface{ Scan(...any) error }) (mgmtMCPServer, error) {
	var value mgmtMCPServer
	var argsJSON, configJSON string
	var enabled int
	err := scanner.Scan(
		&value.ID, &value.Name, &value.Transport, &value.Endpoint, &value.Command,
		&argsJSON, &value.ToolPrefix, &enabled, &configJSON, &value.CreatedAt, &value.UpdatedAt,
	)
	if err != nil {
		return value, err
	}
	value.Enabled = enabled == 1
	value.Args = decodeJSONStringList(argsJSON)
	if value.Args == nil {
		value.Args = []string{}
	}
	config := mgmtMCPConfig{
		AllowedTools: []string{}, AllowedAuthorities: []string{"admin"},
		ApprovalMode: "admin_only", TimeoutSeconds: 30,
	}
	if json.Unmarshal([]byte(configJSON), &config) != nil {
		config = mgmtMCPConfig{
			AllowedTools: []string{}, AllowedAuthorities: []string{"admin"},
			ApprovalMode: "admin_only", TimeoutSeconds: 30,
		}
	}
	if config.AllowedTools == nil {
		config.AllowedTools = []string{}
	}
	if config.AllowedAuthorities == nil {
		config.AllowedAuthorities = []string{"admin"}
	}
	if config.ApprovalMode == "" {
		config.ApprovalMode = "admin_only"
	}
	if config.TimeoutSeconds == 0 {
		config.TimeoutSeconds = 30
	}
	value.SecretRef = config.SecretRef
	value.EnvHeaderRefs = config.EnvHeaderRefs
	value.AllowedTools = config.AllowedTools
	value.AllowedAuthorities = config.AllowedAuthorities
	value.ApprovalMode = config.ApprovalMode
	value.TimeoutSeconds = config.TimeoutSeconds
	return value, nil
}

const mgmtMCPSelect = `
	SELECT id, name, transport, endpoint, command, args_json, tool_prefix, enabled,
		config_json, created_at, updated_at FROM mcp_servers`

func (s *coreConfigStore) mgmtMCPServer(id string) (mgmtMCPServer, bool, error) {
	value, err := scanMgmtMCP(s.db.QueryRow(mgmtMCPSelect+" WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) mgmtMCPServers() ([]mgmtMCPServer, error) {
	rows, err := s.db.Query(mgmtMCPSelect + " ORDER BY name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []mgmtMCPServer{}
	for rows.Next() {
		value, scanErr := scanMgmtMCP(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

type mgmtMCPPayload struct {
	ID                 *string   `json:"id"`
	Name               *string   `json:"name"`
	Transport          *string   `json:"transport"`
	Endpoint           *string   `json:"endpoint"`
	Command            *string   `json:"command"`
	Args               *[]string `json:"args"`
	ToolPrefix         *string   `json:"toolPrefix"`
	Enabled            *bool     `json:"enabled"`
	SecretRef          *string   `json:"secretRef"`
	AllowedTools       *[]string `json:"allowedTools"`
	AllowedAuthorities *[]string `json:"allowedAuthorities"`
	ApprovalMode       *string   `json:"approvalMode"`
	TimeoutSeconds     *int      `json:"timeoutSeconds"`
}

var (
	mgmtMCPCreateFields = coreFieldSet(
		"id", "name", "transport", "endpoint", "command", "args", "toolPrefix", "enabled",
		"secretRef", "allowedTools", "allowedAuthorities", "approvalMode", "timeoutSeconds",
	)
	mgmtMCPUpdateFields = coreFieldSet(
		"name", "transport", "endpoint", "command", "args", "toolPrefix", "enabled",
		"secretRef", "allowedTools", "allowedAuthorities", "approvalMode", "timeoutSeconds",
	)
	mgmtMCPCallFields = coreFieldSet("toolName", "arguments", "authority", "approved")
)

func mgmtMCPAuthorities(values []string) ([]string, error) {
	values, err := normalizeCoreStrings(values, "allowedAuthorities", 2, 20)
	if err != nil {
		return nil, err
	}
	for _, value := range values {
		if value != "member" && value != "admin" {
			return nil, coreInvalid("allowedAuthorities contains unsupported values: " + value)
		}
	}
	return values, nil
}

func mgmtMCPEndpoint(value string) (string, error) {
	value, err := normalizeCoreText(value, "endpoint", 1000, true)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		if parsed != nil && parsed.User != nil {
			return "", coreInvalid("endpoint must be an HTTP URL without credentials")
		}
		return "", coreInvalid("endpoint must be an HTTP URL")
	}
	return strings.TrimSuffix(value, "/"), nil
}

func mgmtMCPValues(input mgmtMCPPayload, current *mgmtMCPServer) (mgmtMCPServer, error) {
	value := mgmtMCPServer{
		Transport: "http", ToolPrefix: "mcp", Args: []string{}, Enabled: false,
		AllowedTools: []string{}, AllowedAuthorities: []string{"admin"},
		ApprovalMode: "admin_only", TimeoutSeconds: 30,
	}
	if current != nil {
		value = *current
	}
	var err error
	if input.Name != nil {
		value.Name, err = normalizeCoreText(*input.Name, "name", 120, true)
		if err != nil {
			return value, err
		}
	} else if current == nil {
		return value, coreInvalid("name is required")
	}
	if input.Transport != nil {
		value.Transport, err = normalizeCoreText(strings.ToLower(*input.Transport), "transport", 20, true)
		if err != nil {
			return value, err
		}
	}
	if value.Transport != "http" && value.Transport != "sse" && value.Transport != "stdio" {
		return value, coreInvalid("transport is not supported")
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if value.Transport == "stdio" {
		value.Endpoint = ""
		if input.Command != nil {
			value.Command, err = normalizeCoreText(*input.Command, "command", 240, true)
			if err != nil {
				return value, err
			}
		} else if value.Command == "" {
			return value, coreInvalid("command is required")
		}
		if value.Enabled && !nativeMCPStdioCommandAllowed(value.Command) {
			return value, coreInvalid("stdio command is not in ERDAI_MCP_STDIO_ALLOWLIST")
		}
		if input.Args != nil {
			value.Args, err = normalizeCoreStrings(*input.Args, "args", 64, 500)
			if err != nil {
				return value, err
			}
		}
	} else {
		value.Command = ""
		value.Args = []string{}
		if input.Endpoint != nil {
			value.Endpoint, err = mgmtMCPEndpoint(*input.Endpoint)
			if err != nil {
				return value, err
			}
		} else if value.Endpoint == "" {
			return value, coreInvalid("endpoint must be an HTTP URL")
		}
	}
	if input.ToolPrefix != nil {
		value.ToolPrefix, err = mgmtIdentifier(*input.ToolPrefix, "toolPrefix")
		if err != nil {
			return value, err
		}
	}
	if input.SecretRef != nil {
		value.SecretRef, err = normalizeCoreText(*input.SecretRef, "secretRef", 120, false)
		if err != nil {
			return value, err
		}
		if value.SecretRef != "" && !validNativeMCPEnvironmentReference(value.SecretRef) {
			return value, coreInvalid("secretRef must be an environment variable name, not a credential")
		}
	}
	if input.AllowedTools != nil {
		value.AllowedTools, err = normalizeCoreStrings(*input.AllowedTools, "allowedTools", 100, 160)
		if err != nil {
			return value, err
		}
	}
	if input.AllowedAuthorities != nil {
		value.AllowedAuthorities, err = mgmtMCPAuthorities(*input.AllowedAuthorities)
		if err != nil {
			return value, err
		}
	}
	if input.ApprovalMode != nil {
		value.ApprovalMode, err = normalizeCoreText(*input.ApprovalMode, "approvalMode", 20, true)
		if err != nil {
			return value, err
		}
	}
	if value.ApprovalMode != "auto" && value.ApprovalMode != "confirm" && value.ApprovalMode != "admin_only" {
		return value, coreInvalid("approvalMode is not supported")
	}
	if mgmtContains(value.AllowedAuthorities, "member") && value.ApprovalMode == "auto" {
		return value, coreInvalid("member-accessible MCP servers require confirmation")
	}
	if mgmtContains(value.AllowedAuthorities, "member") && value.ApprovalMode == "admin_only" {
		return value, coreInvalid("admin_only MCP servers cannot be granted to members")
	}
	if input.TimeoutSeconds != nil {
		value.TimeoutSeconds = *input.TimeoutSeconds
	}
	if value.TimeoutSeconds < 1 || value.TimeoutSeconds > 300 {
		return value, coreInvalid("timeoutSeconds must be an integer between 1 and 300")
	}
	return value, nil
}

func mgmtMCPConfigFor(value mgmtMCPServer) mgmtMCPConfig {
	return mgmtMCPConfig{
		SecretRef: value.SecretRef, EnvHeaderRefs: value.EnvHeaderRefs,
		AllowedTools: value.AllowedTools, AllowedAuthorities: value.AllowedAuthorities,
		ApprovalMode: value.ApprovalMode, TimeoutSeconds: value.TimeoutSeconds,
	}
}

func (s *coreConfigStore) mgmtCreateMCP(r *http.Request) (mgmtMCPServer, error) {
	var input mgmtMCPPayload
	fields, err := decodeCoreObject(r, mgmtMCPCreateFields, "MCP server", &input)
	if err != nil {
		return mgmtMCPServer{}, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return mgmtMCPServer{}, err
	}
	id := ""
	if input.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = mgmtIdentifier(*input.ID, "id")
	}
	if err != nil {
		return mgmtMCPServer{}, err
	}
	value, err := mgmtMCPValues(input, nil)
	if err != nil {
		return mgmtMCPServer{}, err
	}
	now := mgmtNow()
	_, err = s.db.Exec(`
		INSERT INTO mcp_servers (
			id, name, transport, endpoint, command, args_json, tool_prefix, enabled,
			config_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, id, value.Name, value.Transport, value.Endpoint, value.Command, mgmtJSON(value.Args),
		value.ToolPrefix, boolInt(value.Enabled), mgmtJSON(mgmtMCPConfigFor(value)), now, now)
	if err != nil {
		return mgmtMCPServer{}, mgmtConstraintError(err, "MCP server id or name already exists")
	}
	if err = s.mgmtAudit("create", "mcp_server", id, mgmtFieldNames(fields)); err != nil {
		return mgmtMCPServer{}, err
	}
	created, _, err := s.mgmtMCPServer(id)
	return created, err
}

func (s *coreConfigStore) mgmtUpdateMCP(r *http.Request, id string) (mgmtMCPServer, bool, error) {
	current, found, err := s.mgmtMCPServer(id)
	if err != nil || !found {
		return current, found, err
	}
	var input mgmtMCPPayload
	fields, err := decodeCoreObject(r, mgmtMCPUpdateFields, "MCP server", &input)
	if err != nil {
		return current, true, err
	}
	if err = mgmtRejectNullFields(fields); err != nil {
		return current, true, err
	}
	value, err := mgmtMCPValues(input, &current)
	if err != nil {
		return current, true, err
	}
	_, err = s.db.Exec(`
		UPDATE mcp_servers SET name = ?, transport = ?, endpoint = ?, command = ?,
			args_json = ?, tool_prefix = ?, enabled = ?, config_json = ?, updated_at = ?
		WHERE id = ?
	`, value.Name, value.Transport, value.Endpoint, value.Command, mgmtJSON(value.Args),
		value.ToolPrefix, boolInt(value.Enabled), mgmtJSON(mgmtMCPConfigFor(value)), mgmtNow(), id)
	if err != nil {
		return current, true, mgmtConstraintError(err, "MCP server id or name already exists")
	}
	if err = s.mgmtAudit("update", "mcp_server", id, mgmtFieldNames(fields)); err != nil {
		return current, true, err
	}
	updated, _, err := s.mgmtMCPServer(id)
	return updated, true, err
}

type mgmtMCPDiscoveredTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Allowed     bool           `json:"allowed"`
}

type mgmtMCPDiscovery struct {
	ServerID        string                  `json:"serverId"`
	ProtocolVersion string                  `json:"protocolVersion"`
	ServerInfo      map[string]any          `json:"serverInfo"`
	Tools           []mgmtMCPDiscoveredTool `json:"tools"`
}

func mgmtMCPClientAPIError(err error) error {
	code := nativeMCPErrorCode(err)
	status := http.StatusBadGateway
	message := "MCP request failed"
	if code == "timeout" {
		status = http.StatusGatewayTimeout
		message = "MCP request timed out"
	}
	return &coreAPIError{status: status, code: code, message: message}
}

func (a *AgentRuntime) mgmtDiscoverMCP(ctx context.Context, id string) (mgmtMCPDiscovery, bool, error) {
	view, found, err := a.configStore.mgmtMCPServer(id)
	if err != nil || !found {
		return mgmtMCPDiscovery{}, found, err
	}
	server, err := a.configStore.nativeMCPServer(id)
	if err != nil {
		return mgmtMCPDiscovery{}, true, mgmtMCPClientAPIError(err)
	}
	if !server.Enabled {
		return mgmtMCPDiscovery{}, true, mgmtMCPClientAPIError(nativeMCPFailure("disabled", nil))
	}
	timeout := effectiveNativeMCPTimeout(server, runtimeMCPServer{TimeoutSeconds: view.TimeoutSeconds})
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := newNativeMCPTransportClient(operationContext, a.client, server, timeout, net.DefaultResolver)
	if client != nil {
		defer client.Close()
	}
	if err == nil {
		err = client.connect(operationContext)
	}
	var tools []nativeMCPTool
	if err == nil {
		tools, err = client.listTools(operationContext)
	}
	if err != nil {
		return mgmtMCPDiscovery{}, true, mgmtMCPClientAPIError(err)
	}
	discovered := make([]mgmtMCPDiscoveredTool, 0, len(tools))
	for _, tool := range tools {
		discovered = append(discovered, mgmtMCPDiscoveredTool{
			Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema,
			Allowed: mgmtContains(view.AllowedTools, tool.Name),
		})
	}
	if err = a.configStore.mgmtAudit("discover", "mcp_server", id, []string{"toolCount"}); err != nil {
		return mgmtMCPDiscovery{}, true, err
	}
	protocolVersion, serverInfo := client.protocolDetails()
	if serverInfo == nil {
		serverInfo = map[string]any{}
	}
	return mgmtMCPDiscovery{
		ServerID: id, ProtocolVersion: protocolVersion,
		ServerInfo: serverInfo, Tools: discovered,
	}, true, nil
}

type mgmtMCPCallPayload struct {
	ToolName  *string        `json:"toolName"`
	Arguments map[string]any `json:"arguments"`
	Authority *string        `json:"authority"`
	Approved  *bool          `json:"approved"`
}

type mgmtMCPCallResult struct {
	ServerID  string          `json:"serverId"`
	ToolName  string          `json:"toolName"`
	Authority string          `json:"authority"`
	Result    json.RawMessage `json:"result"`
}

func (a *AgentRuntime) mgmtCallMCP(ctx context.Context, r *http.Request, id string) (mgmtMCPCallResult, bool, error) {
	var input mgmtMCPCallPayload
	fields, err := decodeCoreObject(r, mgmtMCPCallFields, "MCP call", &input)
	if err != nil {
		return mgmtMCPCallResult{}, true, err
	}
	if raw, ok := fields["toolName"]; !ok || strings.TrimSpace(string(raw)) == "null" || input.ToolName == nil {
		return mgmtMCPCallResult{}, true, coreInvalid("toolName is required")
	}
	if raw, ok := fields["arguments"]; ok && strings.TrimSpace(string(raw)) == "null" {
		return mgmtMCPCallResult{}, true, coreInvalid("arguments must be an object")
	}
	if raw, ok := fields["authority"]; ok && strings.TrimSpace(string(raw)) == "null" {
		return mgmtMCPCallResult{}, true, coreInvalid("authority must be a string")
	}
	toolName, err := normalizeCoreText(*input.ToolName, "toolName", 160, true)
	if err != nil {
		return mgmtMCPCallResult{}, true, err
	}
	authority := "member"
	if input.Authority != nil {
		authority, err = normalizeCoreText(*input.Authority, "authority", 20, true)
		if err != nil {
			return mgmtMCPCallResult{}, true, err
		}
	}
	if authority != "member" && authority != "admin" {
		return mgmtMCPCallResult{}, true, coreInvalid("authority is not supported")
	}
	arguments := input.Arguments
	if arguments == nil {
		arguments = map[string]any{}
	}
	if len(mgmtJSON(arguments)) > 50000 {
		return mgmtMCPCallResult{}, true, coreInvalid("arguments is too large")
	}
	view, found, err := a.configStore.mgmtMCPServer(id)
	if err != nil || !found {
		return mgmtMCPCallResult{}, found, err
	}
	if !mgmtContains(view.AllowedAuthorities, authority) {
		return mgmtMCPCallResult{}, true, coreInvalid("authority cannot use this MCP server")
	}
	if view.ApprovalMode == "admin_only" && authority != "admin" {
		return mgmtMCPCallResult{}, true, coreInvalid("this MCP server is restricted to administrators")
	}
	approved := input.Approved != nil && *input.Approved
	if view.ApprovalMode == "confirm" && !approved {
		return mgmtMCPCallResult{}, true, coreInvalid("this MCP call requires explicit confirmation")
	}
	if !mgmtContains(view.AllowedTools, toolName) {
		return mgmtMCPCallResult{}, true, coreInvalid("tool is not in the MCP allow-list")
	}
	server, err := a.configStore.nativeMCPServer(id)
	if err != nil {
		return mgmtMCPCallResult{}, true, mgmtMCPClientAPIError(err)
	}
	if !server.Enabled {
		return mgmtMCPCallResult{}, true, mgmtMCPClientAPIError(nativeMCPFailure("disabled", nil))
	}
	timeout := effectiveNativeMCPTimeout(server, runtimeMCPServer{TimeoutSeconds: view.TimeoutSeconds})
	operationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client, err := newNativeMCPTransportClient(operationContext, a.client, server, timeout, net.DefaultResolver)
	if client != nil {
		defer client.Close()
	}
	if err == nil {
		err = client.connect(operationContext)
	}
	var result json.RawMessage
	if err == nil {
		result, err = client.callTool(operationContext, toolName, arguments)
	}
	if err != nil {
		_ = a.configStore.mgmtAudit("call_failed", "mcp_tool", id+":"+toolName, []string{"authority", "errorCode"})
		return mgmtMCPCallResult{}, true, mgmtMCPClientAPIError(err)
	}
	if err = a.configStore.mgmtAudit("call_succeeded", "mcp_tool", id+":"+toolName, []string{"authority"}); err != nil {
		return mgmtMCPCallResult{}, true, err
	}
	return mgmtMCPCallResult{
		ServerID: id, ToolName: toolName, Authority: authority, Result: result,
	}, true, nil
}

func (a *AgentRuntime) handleManagementMCP(w http.ResponseWriter, r *http.Request, path string) error {
	s := a.configStore
	if path == "/api/v1/mcp/servers" {
		switch r.Method {
		case http.MethodGet:
			values, err := s.mgmtMCPServers()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, values)
			}
			return err
		case http.MethodPost:
			value, err := s.mgmtCreateMCP(r)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	const prefix = "/api/v1/mcp/servers/"
	if strings.HasSuffix(path, "/discover") {
		id, err := mgmtPathID(strings.TrimSuffix(path, "/discover"), prefix)
		if err != nil {
			return err
		}
		id, err = mgmtIdentifier(id, "id")
		if err != nil {
			return err
		}
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		value, found, err := a.mgmtDiscoverMCP(r.Context(), id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("MCP server")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	}
	if strings.HasSuffix(path, "/call") {
		id, err := mgmtPathID(strings.TrimSuffix(path, "/call"), prefix)
		if err != nil {
			return err
		}
		id, err = mgmtIdentifier(id, "id")
		if err != nil {
			return err
		}
		if r.Method != http.MethodPost {
			return mgmtMethodNotAllowed()
		}
		value, found, err := a.mgmtCallMCP(r.Context(), r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("MCP server")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	}
	id, err := mgmtPathID(path, prefix)
	if err != nil {
		return err
	}
	id, err = mgmtIdentifier(id, "id")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.mgmtMCPServer(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("MCP server")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		value, found, err := s.mgmtUpdateMCP(r, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("MCP server")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodDelete:
		result, err := s.db.Exec("DELETE FROM mcp_servers WHERE id = ?", id)
		if err != nil {
			return err
		}
		if _, err = mgmtDeleteResult(result, "MCP server"); err != nil {
			return err
		}
		if err = s.mgmtAudit("delete", "mcp_server", id, nil); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}
