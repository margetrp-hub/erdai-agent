package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

type managedMemoryPayload struct {
	PersonaID      string   `json:"personaId"`
	ScopeKind      string   `json:"scopeKind"`
	ScopeReference string   `json:"scopeReference"`
	Content        string   `json:"content"`
	Kind           string   `json:"kind"`
	Confidence     *float64 `json:"confidence"`
	Importance     *float64 `json:"importance"`
	ExpiresAt      *string  `json:"expiresAt"`
}

var managedMemoryFields = coreFieldSet(
	"personaId", "scopeKind", "scopeReference", "content", "kind",
	"confidence", "importance", "expiresAt",
)

type managedRelationshipPayload struct {
	Intimacy *float64 `json:"intimacy"`
	Locked   *bool    `json:"locked"`
}

var managedRelationshipFields = coreFieldSet("intimacy", "locked")

func (a *AgentRuntime) handleManagementMemories(w http.ResponseWriter, r *http.Request, path string) error {
	if a.memory == nil {
		return &coreAPIError{status: http.StatusServiceUnavailable, code: "memory_unavailable", message: "memory is unavailable"}
	}
	if path == "/api/v1/memories" {
		switch r.Method {
		case http.MethodGet:
			personaID := strings.TrimSpace(r.URL.Query().Get("personaId"))
			scopeKind := strings.TrimSpace(r.URL.Query().Get("scopeKind"))
			if err := a.validateManagedMemoryPersona(r.Context(), personaID); err != nil {
				return err
			}
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			items, total, err := a.memory.ListPersonaMemories(r.Context(), personaID, scopeKind, limit, offset)
			if err == nil {
				mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": total})
			}
			return err
		case http.MethodPost:
			payload, scope, metadata, err := a.decodeManagedMemory(r)
			if err != nil {
				return err
			}
			memory, created, err := a.memory.AddMemoryWithMetadata(r.Context(), scope, payload.Content, metadata)
			if err != nil {
				return mgmtConstraintError(err, "memory already exists")
			}
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			mgmtWriteData(w, status, memory)
			return a.configStore.mgmtAudit("memory_created", "persona_memory", memory.ID, []string{"personaId", "scopeKind"})
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := mgmtPathID(path, "/api/v1/memories/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		payload, scope, metadata, decodeErr := a.decodeManagedMemory(r)
		if decodeErr != nil {
			return decodeErr
		}
		memory, found, updateErr := a.memory.UpdateMemory(r.Context(), scope, id, payload.Content, metadata)
		if updateErr != nil {
			return mgmtConstraintError(updateErr, "memory content conflicts with an existing record")
		}
		if !found {
			return mgmtNotFound("memory")
		}
		mgmtWriteData(w, http.StatusOK, memory)
		return a.configStore.mgmtAudit("memory_corrected", "persona_memory", id, []string{"personaId", "scopeKind"})
	case http.MethodDelete:
		personaID := strings.TrimSpace(r.URL.Query().Get("personaId"))
		scopeKind := strings.TrimSpace(r.URL.Query().Get("scopeKind"))
		scopeReference := strings.TrimSpace(r.URL.Query().Get("scopeReference"))
		if err = a.validateManagedMemoryScope(r.Context(), personaID, scopeKind, scopeReference); err != nil {
			return err
		}
		deleted, deleteErr := a.memory.ForgetMemory(r.Context(), personaMemoryScope(personaID, scopeKind, scopeReference), id)
		if deleteErr != nil {
			return deleteErr
		}
		if !deleted {
			return mgmtNotFound("memory")
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"deleted": true})
		return a.configStore.mgmtAudit("memory_deleted", "persona_memory", id, []string{"personaId", "scopeKind"})
	default:
		return mgmtMethodNotAllowed()
	}
}

func (a *AgentRuntime) handleManagementRelationships(w http.ResponseWriter, r *http.Request, path string) error {
	if a.memory == nil {
		return &coreAPIError{status: http.StatusServiceUnavailable, code: "memory_unavailable", message: "memory is unavailable"}
	}
	if path == "/api/v1/relationships" {
		if r.Method != http.MethodGet {
			return mgmtMethodNotAllowed()
		}
		personaID := strings.TrimSpace(r.URL.Query().Get("personaId"))
		if err := a.validateManagedMemoryPersona(r.Context(), personaID); err != nil {
			return err
		}
		limit, offset, err := parseCorePage(r.URL.Query())
		if err != nil {
			return err
		}
		items, total, err := a.memory.ListRelationships(r.Context(), personaID, limit, offset)
		if err == nil {
			mgmtWriteData(w, http.StatusOK, map[string]any{"items": items, "total": total})
		}
		return err
	}
	id, err := mgmtPathID(path, "/api/v1/relationships/")
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		var payload managedRelationshipPayload
		if _, err = decodeCoreObject(r, managedRelationshipFields, "relationship", &payload); err != nil {
			return err
		}
		if payload.Locked == nil {
			return coreInvalid("locked is required")
		}
		intimacy := 0.0
		if payload.Intimacy != nil {
			intimacy = *payload.Intimacy
		}
		if *payload.Locked && payload.Intimacy == nil {
			return coreInvalid("intimacy is required when locking a relationship")
		}
		if intimacy < 0 || intimacy > 100 {
			return coreInvalid("intimacy must be between 0 and 100")
		}
		item, found, updateErr := a.memory.UpdateRelationship(r.Context(), id, intimacy, *payload.Locked)
		if updateErr != nil {
			return updateErr
		}
		if !found {
			return mgmtNotFound("relationship")
		}
		mgmtWriteData(w, http.StatusOK, item)
		return a.configStore.mgmtAudit("relationship_adjusted", "persona_relationship", id, []string{"intimacy", "locked"})
	case http.MethodDelete:
		deleted, deleteErr := a.memory.DeleteRelationship(r.Context(), id)
		if deleteErr != nil {
			return deleteErr
		}
		if !deleted {
			return mgmtNotFound("relationship")
		}
		mgmtWriteData(w, http.StatusOK, map[string]any{"deleted": true})
		return a.configStore.mgmtAudit("relationship_deleted", "persona_relationship", id, nil)
	default:
		return mgmtMethodNotAllowed()
	}
}

func (a *AgentRuntime) decodeManagedMemory(r *http.Request) (managedMemoryPayload, string, MemoryMetadata, error) {
	var payload managedMemoryPayload
	if _, err := decodeCoreObject(r, managedMemoryFields, "memory", &payload); err != nil {
		return payload, "", MemoryMetadata{}, err
	}
	payload.PersonaID = strings.TrimSpace(payload.PersonaID)
	payload.ScopeKind = strings.TrimSpace(payload.ScopeKind)
	payload.ScopeReference = strings.TrimSpace(payload.ScopeReference)
	payload.Content = strings.TrimSpace(payload.Content)
	payload.Kind = strings.TrimSpace(payload.Kind)
	if err := a.validateManagedMemoryScope(r.Context(), payload.PersonaID, payload.ScopeKind, payload.ScopeReference); err != nil {
		return payload, "", MemoryMetadata{}, err
	}
	if payload.Content == "" || len([]rune(payload.Content)) > 500 || containsSensitiveMemory(payload.Content) {
		return payload, "", MemoryMetadata{}, coreInvalid("memory content is empty, too long, or sensitive")
	}
	if payload.Kind == "" {
		payload.Kind = "fact"
	}
	if len(payload.Kind) > 40 {
		return payload, "", MemoryMetadata{}, coreInvalid("memory kind is invalid")
	}
	confidence, importance := 1.0, 0.7
	if payload.Confidence != nil {
		confidence = *payload.Confidence
	}
	if payload.Importance != nil {
		importance = *payload.Importance
	}
	if confidence < 0 || confidence > 1 || importance < 0 || importance > 1 {
		return payload, "", MemoryMetadata{}, coreInvalid("memory confidence and importance must be between 0 and 1")
	}
	var expiresAt *time.Time
	if payload.ExpiresAt != nil && strings.TrimSpace(*payload.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*payload.ExpiresAt))
		if err != nil {
			return payload, "", MemoryMetadata{}, coreInvalid("expiresAt must use RFC3339")
		}
		expiresAt = &parsed
	}
	return payload, personaMemoryScope(payload.PersonaID, payload.ScopeKind, payload.ScopeReference), MemoryMetadata{
		Source: "admin_correction", Kind: payload.Kind, Confidence: confidence,
		Importance: importance, ExpiresAt: expiresAt,
	}, nil
}

func (a *AgentRuntime) validateManagedMemoryScope(ctx context.Context, personaID, scopeKind, scopeReference string) error {
	if err := a.validateManagedMemoryPersona(ctx, personaID); err != nil {
		return err
	}
	if scopeKind != "user" && scopeKind != "group" {
		return coreInvalid("scopeKind must be user or group")
	}
	if scopeReference == "" || len(scopeReference) > 240 {
		return coreInvalid("scopeReference is required and must not exceed 240 characters")
	}
	return nil
}

func (a *AgentRuntime) validateManagedMemoryPersona(ctx context.Context, personaID string) error {
	if personaID == "" || len(personaID) > 120 {
		return coreInvalid("personaId is required")
	}
	var exists int
	err := a.configStore.db.QueryRowContext(ctx, "SELECT count(*) FROM personas WHERE id = ?", personaID).Scan(&exists)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if exists != 1 {
		return coreInvalid("persona does not exist")
	}
	return nil
}
