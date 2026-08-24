package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"
)

type personaBinding struct {
	ID              string `json:"id"`
	PersonaID       string `json:"personaId"`
	Transport       string `json:"transport"`
	TransportInstance string `json:"transportInstance"`
	ConversationRef string `json:"conversationRef"`
	Priority        int    `json:"priority"`
	Enabled         bool   `json:"enabled"`
	CreatedAt       string `json:"createdAt"`
	UpdatedAt       string `json:"updatedAt"`
}

type personaBindingPayload struct {
	PersonaID       string `json:"personaId"`
	Transport       string `json:"transport"`
	TransportInstance string `json:"transportInstance"`
	ConversationRef string `json:"conversationRef"`
	Priority        int    `json:"priority"`
	Enabled         *bool  `json:"enabled"`
}

var personaBindingFields = coreFieldSet("personaId", "transport", "transportInstance", "conversationRef", "priority", "enabled")

func (s *coreConfigStore) resolvePersonaID(transport, conversation string, fallback *string) (*string, error) {
	return s.resolvePersonaIDForInstance("*", transport, conversation, fallback)
}

func (s *coreConfigStore) resolvePersonaIDForInstance(transportInstance, transport, conversation string, fallback *string) (*string, error) {
	transportInstance = strings.TrimSpace(transportInstance)
	transport = strings.TrimSpace(transport)
	conversation = strings.TrimSpace(conversation)
	var personaID string
	err := s.db.QueryRow(`
		SELECT persona_id FROM persona_bindings
		WHERE enabled = 1 AND transport_instance IN (?, '*') AND transport IN (?, '*') AND conversation_ref IN (?, '*')
		ORDER BY (transport_instance = ?) DESC, (conversation_ref = ?) DESC, (transport = ?) DESC, priority DESC, updated_at DESC
		LIMIT 1
	`, transportInstance, transport, conversation, transportInstance, conversation, transport).Scan(&personaID)
	if errors.Is(err, sql.ErrNoRows) {
		if fallback == nil {
			return nil, nil
		}
		value := strings.TrimSpace(*fallback)
		if value == "" {
			return nil, nil
		}
		return &value, nil
	}
	if err != nil {
		return nil, err
	}
	return &personaID, nil
}

func (s *coreConfigStore) handlePersonaBindingRequest(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/persona-bindings" {
		if r.Method != http.MethodGet {
			return &coreAPIError{status: http.StatusMethodNotAllowed, code: "method_not_allowed", message: "method not allowed"}
		}
		rows, err := s.db.Query(`
			SELECT id, persona_id, transport, transport_instance, conversation_ref, priority, enabled, created_at, updated_at
			FROM persona_bindings ORDER BY priority DESC, updated_at DESC
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		items := []personaBinding{}
		for rows.Next() {
			var item personaBinding
			var enabled int
			if err = rows.Scan(&item.ID, &item.PersonaID, &item.Transport, &item.TransportInstance, &item.ConversationRef,
				&item.Priority, &enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
				return err
			}
			item.Enabled = enabled == 1
			items = append(items, item)
		}
		if err = rows.Err(); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"items": items, "total": len(items)}})
		return nil
	}
	id, err := parseCorePathID(strings.TrimPrefix(path, "/api/v1/persona-bindings/"))
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodPut:
		var payload personaBindingPayload
		if _, err = decodeCoreObject(r, personaBindingFields, "persona binding", &payload); err != nil {
			return err
		}
		payload.PersonaID = strings.TrimSpace(payload.PersonaID)
		payload.Transport = strings.ToLower(strings.TrimSpace(payload.Transport))
		payload.TransportInstance = strings.TrimSpace(payload.TransportInstance)
		payload.ConversationRef = strings.TrimSpace(payload.ConversationRef)
		if payload.Transport == "" {
			payload.Transport = "*"
		}
		if payload.TransportInstance == "" {
			payload.TransportInstance = "*"
		}
		if payload.ConversationRef == "" {
			payload.ConversationRef = "*"
		}
		if payload.PersonaID == "" || len(payload.PersonaID) > 120 || len(payload.Transport) > 80 || len(payload.TransportInstance) > 120 || len(payload.ConversationRef) > 240 || payload.Priority < -10000 || payload.Priority > 10000 {
			return coreInvalid("persona binding fields are invalid")
		}
		var exists int
		if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", payload.PersonaID).Scan(&exists); err != nil {
			return err
		}
		if exists != 1 {
			return coreInvalid("persona does not exist")
		}
		enabled := true
		if payload.Enabled != nil {
			enabled = *payload.Enabled
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		_, err = s.db.Exec(`
			INSERT INTO persona_bindings (id, persona_id, transport, transport_instance, conversation_ref, priority, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET persona_id = excluded.persona_id, transport = excluded.transport,
				transport_instance = excluded.transport_instance,
				conversation_ref = excluded.conversation_ref, priority = excluded.priority,
				enabled = excluded.enabled, updated_at = excluded.updated_at
		`, id, payload.PersonaID, payload.Transport, payload.TransportInstance, payload.ConversationRef, payload.Priority, boolInt(enabled), now, now)
		if err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": id}})
		return nil
	case http.MethodDelete:
		result, deleteErr := s.db.Exec("DELETE FROM persona_bindings WHERE id = ?", id)
		if deleteErr != nil {
			return deleteErr
		}
		deleted, _ := result.RowsAffected()
		if deleted == 0 {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona binding not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"deleted": true}})
		return nil
	default:
		return &coreAPIError{status: http.StatusMethodNotAllowed, code: "method_not_allowed", message: "method not allowed"}
	}
}
