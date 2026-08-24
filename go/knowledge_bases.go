package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
)

// knowledgeBase is a logical collection. Documents still use namespace as the
// physical key, which keeps the old CRUD and search contracts compatible.
type knowledgeBase struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Layer        string `json:"layer"`
	OwnerKind    string `json:"ownerKind"`
	OwnerID      string `json:"ownerId"`
	Namespace    string `json:"namespace"`
	Enabled      bool   `json:"enabled"`
	AutoInclude  bool   `json:"autoInclude"`
	Priority     int    `json:"priority"`
	DocumentCount int   `json:"documentCount"`
	CreatedAt    string `json:"createdAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type knowledgeBasePayload struct {
	ID          *string `json:"id"`
	Name        *string `json:"name"`
	Description *string `json:"description"`
	Layer       *string `json:"layer"`
	OwnerKind   *string `json:"ownerKind"`
	OwnerID     *string `json:"ownerId"`
	Namespace   *string `json:"namespace"`
	Enabled     *bool   `json:"enabled"`
	AutoInclude *bool   `json:"autoInclude"`
	Priority    *int    `json:"priority"`
}

var knowledgeBaseFields = coreFieldSet(
	"id", "name", "description", "layer", "ownerKind", "ownerId", "namespace",
	"enabled", "autoInclude", "priority",
)

func knowledgeBaseForNamespace(namespace string) (id, name, layer, ownerKind, ownerID string) {
	namespace = strings.TrimSpace(namespace)
	switch {
	case namespace == "" || namespace == "default" || namespace == "global":
		return "kb-global", "公共大知识库", "global", "", ""
	case strings.HasPrefix(namespace, "persona:"):
		ownerID = strings.TrimPrefix(namespace, "persona:")
		return "kb-persona-" + safeKnowledgeBaseID(ownerID), "角色专属知识库", "exclusive", "persona", ownerID
	case strings.HasPrefix(namespace, "instance:"):
		ownerID = strings.TrimPrefix(namespace, "instance:")
		return "kb-instance-" + safeKnowledgeBaseID(ownerID), "实例专属知识库", "exclusive", "instance", ownerID
	default:
		return "kb-domain-" + safeKnowledgeBaseID(namespace), "场景小知识库", "domain", "", ""
	}
}

func safeKnowledgeBaseID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:16]
}

func syncKnowledgeBases(tx coreSchemaTx) error {
	if _, err := tx.Exec(`INSERT OR IGNORE INTO knowledge_bases
		(id, name, description, layer, owner_kind, owner_id, namespace, enabled, auto_include, priority, created_at, updated_at)
		VALUES ('kb-global', '公共大知识库', '所有角色都可以使用的通用事实、规则和公共知识。', 'global', '', '', 'default', 1, 1, 100, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`); err != nil {
		return err
	}
	rows, err := tx.Query("SELECT DISTINCT namespace FROM knowledge_documents WHERE namespace <> 'default'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var namespace string
		if err = rows.Scan(&namespace); err != nil {
			return err
		}
		if err = syncKnowledgeBaseNamespace(tx, namespace); err != nil {
			return err
		}
	}
	return rows.Err()
}

func syncKnowledgeBaseNamespace(tx coreSchemaTx, namespace string) error {
	id, name, layer, ownerKind, ownerID := knowledgeBaseForNamespace(namespace)
	_, err := tx.Exec(`INSERT OR IGNORE INTO knowledge_bases
		(id, name, description, layer, owner_kind, owner_id, namespace, enabled, auto_include, priority, created_at, updated_at)
		VALUES (?, ?, '', ?, ?, ?, ?, 1, ?, 0, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'))`,
		id, name, layer, ownerKind, ownerID, namespace, layer == "domain" || layer == "exclusive")
	return err
}

func (s *coreConfigStore) ensureKnowledgeBaseNamespace(namespace string) error {
	return syncKnowledgeBaseNamespace(s.db, namespace)
}

func scanKnowledgeBase(scanner interface{ Scan(...any) error }) (knowledgeBase, error) {
	var value knowledgeBase
	var enabled, auto int
	if err := scanner.Scan(&value.ID, &value.Name, &value.Description, &value.Layer,
		&value.OwnerKind, &value.OwnerID, &value.Namespace, &enabled, &auto,
		&value.Priority, &value.DocumentCount, &value.CreatedAt, &value.UpdatedAt); err != nil {
		return value, err
	}
	value.Enabled, value.AutoInclude = enabled == 1, auto == 1
	return value, nil
}

const knowledgeBaseSelect = `SELECT b.id, b.name, b.description, b.layer, b.owner_kind, b.owner_id,
	b.namespace, b.enabled, b.auto_include, b.priority,
	(SELECT count(*) FROM knowledge_documents d WHERE d.namespace = b.namespace),
	b.created_at, b.updated_at FROM knowledge_bases b`

func (s *coreConfigStore) listKnowledgeBases() ([]knowledgeBase, error) {
	rows, err := s.db.Query(knowledgeBaseSelect + " ORDER BY CASE b.layer WHEN 'global' THEN 0 WHEN 'domain' THEN 1 ELSE 2 END, b.priority DESC, b.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []knowledgeBase{}
	for rows.Next() {
		value, scanErr := scanKnowledgeBase(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, value)
	}
	return items, rows.Err()
}

func (s *coreConfigStore) getKnowledgeBase(id string) (knowledgeBase, bool, error) {
	value, err := scanKnowledgeBase(s.db.QueryRow(knowledgeBaseSelect+" WHERE b.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return value, false, nil
	}
	return value, err == nil, err
}

func validateKnowledgeBasePayload(input knowledgeBasePayload, current *knowledgeBase) (knowledgeBase, error) {
	value := knowledgeBase{Enabled: true, AutoInclude: false}
	if current != nil {
		value = *current
	}
	if input.Name != nil {
		value.Name = strings.TrimSpace(*input.Name)
	}
	if input.Description != nil {
		value.Description = strings.TrimSpace(*input.Description)
	}
	if input.Layer != nil {
		value.Layer = strings.TrimSpace(strings.ToLower(*input.Layer))
	}
	if input.OwnerKind != nil {
		value.OwnerKind = strings.TrimSpace(strings.ToLower(*input.OwnerKind))
	}
	if input.OwnerID != nil {
		value.OwnerID = strings.TrimSpace(*input.OwnerID)
	}
	if input.Namespace != nil {
		var err error
		value.Namespace, err = normalizeCoreNamespace(*input.Namespace)
		if err != nil {
			return value, err
		}
	}
	if input.Enabled != nil {
		value.Enabled = *input.Enabled
	}
	if input.AutoInclude != nil {
		value.AutoInclude = *input.AutoInclude
	}
	if input.Priority != nil {
		value.Priority = *input.Priority
	}
	if value.Name == "" {
		return value, coreInvalid("name is required")
	}
	if value.Layer != "global" && value.Layer != "domain" && value.Layer != "exclusive" {
		return value, coreInvalid("layer must be global, domain or exclusive")
	}
	if value.Layer == "exclusive" {
		if value.OwnerKind != "persona" && value.OwnerKind != "instance" {
			return value, coreInvalid("exclusive ownerKind must be persona or instance")
		}
		if value.OwnerID == "" {
			return value, coreInvalid("exclusive ownerId is required")
		}
	} else if value.OwnerKind != "" || value.OwnerID != "" {
		return value, coreInvalid("global and domain bases cannot have an owner")
	}
	if value.Namespace == "" {
		return value, coreInvalid("namespace is required")
	}
	if value.Layer == "global" {
		value.AutoInclude = true
	}
	if value.Priority < -10000 || value.Priority > 10000 {
		return value, coreInvalid("priority is invalid")
	}
	return value, nil
}

func (s *coreConfigStore) putKnowledgeBase(id string, input knowledgeBasePayload, create bool) (knowledgeBase, error) {
	current, found, err := s.getKnowledgeBase(id)
	if err != nil {
		return current, err
	}
	if !create && !found {
		return current, mgmtNotFound("knowledge base")
	}
	if create && !found {
		current.ID = id
		current.Namespace = id
	}
	value, err := validateKnowledgeBasePayload(input, &current)
	if err != nil {
		return value, err
	}
	value.ID = id
	if current.CreatedAt != "" {
		value.CreatedAt = current.CreatedAt
	}
	if value.CreatedAt == "" {
		value.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`INSERT INTO knowledge_bases
		(id, name, description, layer, owner_kind, owner_id, namespace, enabled, auto_include, priority, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name, description=excluded.description,
		layer=excluded.layer, owner_kind=excluded.owner_kind, owner_id=excluded.owner_id,
		namespace=excluded.namespace, enabled=excluded.enabled, auto_include=excluded.auto_include,
		priority=excluded.priority, updated_at=excluded.updated_at`, value.ID, value.Name, value.Description,
		value.Layer, value.OwnerKind, value.OwnerID, value.Namespace, boolInt(value.Enabled), boolInt(value.AutoInclude),
		value.Priority, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return value, mgmtConstraintError(err, "knowledge base id or namespace already exists")
	}
	updated, _, err := s.getKnowledgeBase(id)
	return updated, err
}

func (s *coreConfigStore) handleKnowledgeBases(w http.ResponseWriter, r *http.Request, path string) error {
	const prefix = "/api/v1/knowledge/bases"
	if path == prefix {
		switch r.Method {
		case http.MethodGet:
			items, err := s.listKnowledgeBases()
			if err == nil {
				mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
			}
			return err
		case http.MethodPost:
			var input knowledgeBasePayload
			if _, err := decodeCoreObject(r, knowledgeBaseFields, "knowledge base", &input); err != nil {
				return err
			}
			if input.ID == nil || strings.TrimSpace(*input.ID) == "" {
				return coreInvalid("id is required")
			}
			id, err := normalizeCoreText(*input.ID, "id", 120, true)
			if err != nil {
				return err
			}
			value, err := s.putKnowledgeBase(id, input, true)
			if err == nil {
				mgmtWriteData(w, http.StatusCreated, value)
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id := strings.TrimPrefix(path, prefix+"/")
	if id == "" || strings.Contains(id, "/") {
		return mgmtNotFound("knowledge base")
	}
	id, err := normalizeCoreText(id, "id", 120, true)
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		value, found, err := s.getKnowledgeBase(id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("knowledge base")
		}
		mgmtWriteData(w, http.StatusOK, value)
		return nil
	case http.MethodPut:
		var input knowledgeBasePayload
		if _, err := decodeCoreObject(r, knowledgeBaseFields, "knowledge base", &input); err != nil {
			return err
		}
		value, err := s.putKnowledgeBase(id, input, false)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, value)
		}
		return err
	case http.MethodDelete:
		value, found, err := s.getKnowledgeBase(id)
		if err != nil || !found {
			if err != nil {
				return err
			}
			return mgmtNotFound("knowledge base")
		}
		if value.Layer == "global" {
			return coreInvalid("the global knowledge base cannot be deleted")
		}
		if _, err = s.db.Exec("DELETE FROM knowledge_bases WHERE id = ?", id); err != nil {
			return err
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"id": id, "deleted": true})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type knowledgeNamespaceSelection struct {
	Namespace string
	Priority  int
}

func (s *coreConfigStore) knowledgeNamespacesForRun(config nativeRuntimeConfig, personaID, instanceID string) ([]knowledgeNamespaceSelection, error) {
	rows, err := s.db.Query(`SELECT namespace, layer, owner_kind, owner_id, auto_include, priority
		FROM knowledge_bases WHERE enabled = 1 ORDER BY priority DESC, namespace`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	selected := []knowledgeNamespaceSelection{}
	seen := map[string]bool{}
	add := func(namespace string, priority int) {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" || seen[namespace] {
			return
		}
		seen[namespace] = true
		selected = append(selected, knowledgeNamespaceSelection{Namespace: namespace, Priority: priority})
	}
	for rows.Next() {
		var namespace, layer, ownerKind, ownerID string
		var autoInclude, priority int
		if err = rows.Scan(&namespace, &layer, &ownerKind, &ownerID, &autoInclude, &priority); err != nil {
			return nil, err
		}
		switch layer {
		case "global":
			if autoInclude == 1 || namespace == config.KnowledgeNamespace {
				add(namespace, priority+300)
			}
		case "domain":
			if namespace == config.KnowledgeNamespace || (config.KnowledgeNamespace == "default" && autoInclude == 1) {
				add(namespace, priority+200)
			}
		case "exclusive":
			if ownerKind == "persona" && ownerID == personaID && (autoInclude == 1 || namespace == config.KnowledgeNamespace) {
				add(namespace, priority+100)
			}
			if ownerKind == "instance" && ownerID == instanceID && (autoInclude == 1 || namespace == config.KnowledgeNamespace) {
				add(namespace, priority+120)
			}
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	// Preserve the old exact namespace even when its base was created by an
	// older binary or an operator imported the document directly.
	add(config.KnowledgeNamespace, 250)
	return selected, nil
}

func (s *coreConfigStore) searchHybridKnowledgeNamespaces(namespaces []knowledgeNamespaceSelection, message string, requestedLimit int) ([]nativeRAGItem, error) {
	if len(namespaces) == 0 {
		return s.searchHybridKnowledge("default", message, requestedLimit)
	}
	type combined struct {
		item  nativeRAGItem
		score float64
	}
	values := map[string]combined{}
	for _, selection := range namespaces {
		hits, err := s.searchHybridKnowledge(selection.Namespace, message, requestedLimit)
		if err != nil {
			return nil, err
		}
		boost := 1.0 + float64(selection.Priority)/1000.0
		for _, hit := range hits {
			score := 0.0
			if hit.Rank != nil {
				score = *hit.Rank * boost
			}
			current := values[hit.ID]
			if current.item.ID == "" || score > current.score {
				current.item = hit
				current.score = score
			}
			values[hit.ID] = current
		}
	}
	ranked := make([]combined, 0, len(values))
	for _, value := range values {
		ranked = append(ranked, value)
	}
	// Stable ordering keeps the higher-level base preference visible when
	// scores are close and prevents random response churn.
	for i := 1; i < len(ranked); i++ {
		for j := i; j > 0 && ranked[j].score > ranked[j-1].score; j-- {
			ranked[j], ranked[j-1] = ranked[j-1], ranked[j]
		}
	}
	limit := requestedLimit
	if limit <= 0 {
		limit = s.retrievalPolicy().TopK
	}
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	items := make([]nativeRAGItem, 0, len(ranked))
	for _, value := range ranked {
		score := value.score
		value.item.Rank = &score
		items = append(items, value.item)
	}
	return items, nil
}
