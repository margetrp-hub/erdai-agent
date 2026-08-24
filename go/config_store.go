package main

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxPersonaAvatarBytes = 512 * 1024

type coreConfigStore struct {
	db       *sql.DB
	mediaDir string
}

type coreAPIError struct {
	status  int
	code    string
	message string
}

func (e *coreAPIError) Error() string { return e.message }

func coreInvalid(message string) error {
	return &coreAPIError{status: http.StatusBadRequest, code: "invalid_request", message: message}
}

func openCoreConfigStore(path string) (*coreConfigStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, nil
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`
		PRAGMA journal_mode = WAL;
		PRAGMA busy_timeout = 5000;
		PRAGMA foreign_keys = ON;
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("open core configuration database: %w", err)
	}
	store := &coreConfigStore{db: db}
	if err = migrateCoreConfig(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate core configuration database: %w", err)
	}
	return store, nil
}

func (s *coreConfigStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

type nativeRuntimeConfig struct {
	ActivePersonaID           *string  `json:"activePersonaId"`
	PersonaInjectionEnabled   bool     `json:"personaInjectionEnabled"`
	KnowledgeInjectionEnabled bool     `json:"knowledgeInjectionEnabled"`
	WorldbookInjectionEnabled bool     `json:"worldbookInjectionEnabled"`
	ProtectedRules            string   `json:"protectedRules"`
	ReplyStyle                string   `json:"replyStyle"`
	MaxReplySentences         int      `json:"maxReplySentences"`
	MaxReplyChars             int      `json:"maxReplyChars"`
	AvoidRepetitiveOpeners    bool     `json:"avoidRepetitiveOpeners"`
	KnowledgeNamespace        string   `json:"knowledgeNamespace"`
	LearningEnabled           bool     `json:"learningEnabled"`
	LearningTopics            []string `json:"learningTopics"`
	LearningIntervalHours     int      `json:"learningIntervalHours"`
	LastCollectedAt           *string  `json:"lastCollectedAt"`
	UpdatedAt                 string   `json:"updatedAt"`
}

type nativePersona struct {
	ID                      string   `json:"id"`
	Namespace               string   `json:"namespace"`
	Name                    string   `json:"name"`
	Description             string   `json:"description"`
	Personality             string   `json:"personality"`
	Scenario                string   `json:"scenario"`
	FirstMessage            string   `json:"firstMessage"`
	SystemPrompt            string   `json:"systemPrompt"`
	PostHistoryInstructions string   `json:"postHistoryInstructions"`
	MessageExample          string   `json:"messageExample"`
	AlternateGreetings      []string `json:"alternateGreetings"`
	Tags                    []string `json:"tags"`
	Creator                 string   `json:"creator"`
	CharacterVersion        string   `json:"characterVersion"`
	SourceFormat            string   `json:"sourceFormat"`
	SourceVersion           string   `json:"sourceVersion"`
	AvatarDataURI           string   `json:"avatarDataUri"`
	VisualDescription       string   `json:"visualDescription"`
	CreatedAt               string   `json:"createdAt"`
	UpdatedAt               string   `json:"updatedAt"`
}

type nativeWorldbookEntry struct {
	ID             string   `json:"id"`
	PersonaID      string   `json:"personaId"`
	Keys           []string `json:"keys"`
	SecondaryKeys  []string `json:"secondaryKeys"`
	Comment        string   `json:"comment"`
	Content        string   `json:"content"`
	Enabled        bool     `json:"enabled"`
	Constant       bool     `json:"constant"`
	Selective      bool     `json:"selective"`
	Priority       int      `json:"priority"`
	Position       string   `json:"position"`
	InsertionOrder int      `json:"insertionOrder"`
	TokenBudget    *int     `json:"tokenBudget"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

type corePage[T any] struct {
	Items  []T `json:"items"`
	Total  int `json:"total"`
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
}

type corePersonaPayload struct {
	ID                      *string   `json:"id"`
	Namespace               *string   `json:"namespace"`
	Name                    *string   `json:"name"`
	Description             *string   `json:"description"`
	Personality             *string   `json:"personality"`
	Scenario                *string   `json:"scenario"`
	FirstMessage            *string   `json:"firstMessage"`
	SystemPrompt            *string   `json:"systemPrompt"`
	PostHistoryInstructions *string   `json:"postHistoryInstructions"`
	MessageExample          *string   `json:"messageExample"`
	AlternateGreetings      *[]string `json:"alternateGreetings"`
	Tags                    *[]string `json:"tags"`
	Creator                 *string   `json:"creator"`
	CharacterVersion        *string   `json:"characterVersion"`
	SourceFormat            *string   `json:"sourceFormat"`
	SourceVersion           *string   `json:"sourceVersion"`
	AvatarDataURI           *string   `json:"avatarDataUri"`
	VisualDescription       *string   `json:"visualDescription"`
}

type coreWorldbookPayload struct {
	ID             *string         `json:"id"`
	Keys           *[]string       `json:"keys"`
	SecondaryKeys  *[]string       `json:"secondaryKeys"`
	Comment        *string         `json:"comment"`
	Content        *string         `json:"content"`
	Enabled        *bool           `json:"enabled"`
	Constant       *bool           `json:"constant"`
	Selective      *bool           `json:"selective"`
	Priority       *int            `json:"priority"`
	Position       *string         `json:"position"`
	InsertionOrder *int            `json:"insertionOrder"`
	TokenBudget    json.RawMessage `json:"tokenBudget"`
}

type coreRuntimeConfigPayload struct {
	ActivePersonaID           *string         `json:"activePersonaId"`
	PersonaInjectionEnabled   *bool           `json:"personaInjectionEnabled"`
	KnowledgeInjectionEnabled *bool           `json:"knowledgeInjectionEnabled"`
	WorldbookInjectionEnabled *bool           `json:"worldbookInjectionEnabled"`
	ProtectedRules            *string         `json:"protectedRules"`
	ReplyStyle                *string         `json:"replyStyle"`
	MaxReplySentences         *int            `json:"maxReplySentences"`
	MaxReplyChars             *int            `json:"maxReplyChars"`
	AvoidRepetitiveOpeners    *bool           `json:"avoidRepetitiveOpeners"`
	KnowledgeNamespace        *string         `json:"knowledgeNamespace"`
	LearningEnabled           *bool           `json:"learningEnabled"`
	LearningTopics            *[]string       `json:"learningTopics"`
	LearningIntervalHours     *int            `json:"learningIntervalHours"`
	LastCollectedAt           json.RawMessage `json:"lastCollectedAt"`
}

var corePersonaCreateFields = coreFieldSet(
	"id", "namespace", "name", "description", "personality", "scenario", "firstMessage",
	"systemPrompt", "postHistoryInstructions", "messageExample", "alternateGreetings", "tags",
	"creator", "characterVersion", "sourceFormat", "sourceVersion", "avatarDataUri", "visualDescription",
)

var corePersonaUpdateFields = coreFieldSet(
	"name", "description", "personality", "scenario", "firstMessage", "systemPrompt",
	"postHistoryInstructions", "messageExample", "alternateGreetings", "tags", "creator",
	"characterVersion", "sourceFormat", "sourceVersion", "avatarDataUri", "visualDescription",
)

var coreWorldbookCreateFields = coreFieldSet(
	"id", "keys", "secondaryKeys", "comment", "content", "enabled", "constant", "selective",
	"priority", "position", "insertionOrder", "tokenBudget",
)

var coreWorldbookUpdateFields = coreFieldSet(
	"keys", "secondaryKeys", "comment", "content", "enabled", "constant", "selective",
	"priority", "position", "insertionOrder", "tokenBudget",
)

var coreRuntimeConfigFields = coreFieldSet(
	"activePersonaId", "personaInjectionEnabled", "knowledgeInjectionEnabled",
	"worldbookInjectionEnabled", "protectedRules", "replyStyle", "maxReplySentences",
	"maxReplyChars", "avoidRepetitiveOpeners", "knowledgeNamespace", "learningEnabled",
	"learningTopics", "learningIntervalHours", "lastCollectedAt",
)

func coreFieldSet(fields ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

func decodeCoreObject(r *http.Request, allowed map[string]struct{}, label string, target any) (map[string]json.RawMessage, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRuntimeBody+1))
	if err != nil {
		return nil, coreInvalid("cannot read " + label + " body")
	}
	if len(body) > maxRuntimeBody {
		return nil, coreInvalid("request body is too large")
	}
	var fields map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &fields) != nil || fields == nil {
		return nil, coreInvalid(label + " body must be an object")
	}
	unknown := make([]string, 0)
	for field := range fields {
		if _, ok := allowed[field]; !ok {
			unknown = append(unknown, field)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, coreInvalid("unsupported " + label + " fields: " + strings.Join(unknown, ", "))
	}
	if err = json.Unmarshal(body, target); err != nil {
		return nil, coreInvalid(err.Error())
	}
	return fields, nil
}

func normalizeCoreText(value, name string, maximum int, required bool) (string, error) {
	value = strings.Map(func(r rune) rune {
		if r <= 8 || r == 11 || r == 12 || r >= 14 && r <= 31 || r == 127 {
			return -1
		}
		return r
	}, value)
	value = strings.TrimSpace(value)
	if required && value == "" {
		return "", coreInvalid(name + " is required")
	}
	if len([]rune(value)) > maximum {
		return "", coreInvalid(name + " is too long")
	}
	return value, nil
}

func normalizeCoreNamespace(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		value = "default"
	}
	return normalizeCoreText(value, "namespace", 120, true)
}

func normalizeCoreStrings(values []string, name string, maxItems, maxLength int) ([]string, error) {
	if len(values) > maxItems {
		return nil, coreInvalid(name + " has too many items")
	}
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized, err := normalizeCoreText(value, name, maxLength, true)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	return result, nil
}

func validateAvatarDataURI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	comma := strings.IndexByte(value, ',')
	if comma < 0 {
		return "", coreInvalid("avatarDataUri must be a base64 image data URI")
	}
	metadata := strings.Split(value[:comma], ";")
	if len(metadata) != 2 || !strings.EqualFold(metadata[1], "base64") || !strings.HasPrefix(strings.ToLower(metadata[0]), "data:") {
		return "", coreInvalid("avatarDataUri must be a base64 image data URI")
	}
	mimeType := strings.ToLower(strings.TrimPrefix(strings.ToLower(metadata[0]), "data:"))
	if mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return "", coreInvalid("avatarDataUri must be PNG, JPEG, or WebP")
	}
	encoded := value[comma+1:]
	if base64.StdEncoding.DecodedLen(len(encoded)) > maxPersonaAvatarBytes+2 {
		return "", coreInvalid("avatarDataUri decoded image exceeds 512 KiB")
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return "", coreInvalid("avatarDataUri contains invalid base64")
	}
	if len(decoded) == 0 || len(decoded) > maxPersonaAvatarBytes {
		return "", coreInvalid("avatarDataUri decoded image exceeds 512 KiB")
	}
	if detected := http.DetectContentType(decoded); detected != mimeType {
		return "", coreInvalid("avatarDataUri media type does not match its content")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(decoded), nil
}

func newCoreUUID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	value[6] = value[6]&0x0f | 0x40
	value[8] = value[8]&0x3f | 0x80
	raw := hex.EncodeToString(value)
	return raw[:8] + "-" + raw[8:12] + "-" + raw[12:16] + "-" + raw[16:20] + "-" + raw[20:], nil
}

func parseCorePage(values url.Values) (int, int, error) {
	limit, offset := 20, 0
	var err error
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 100 {
			return 0, 0, coreInvalid("limit must be between 1 and 100")
		}
	}
	if raw := values.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return 0, 0, coreInvalid("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

func parseCorePathID(value string) (string, error) {
	value, err := url.PathUnescape(value)
	if err != nil {
		return "", coreInvalid("path identifier is invalid")
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.Contains(value, "/") {
		return "", coreInvalid("path identifier is invalid")
	}
	return value, nil
}

func tokenMatches(received, expected string) bool {
	left := sha256.Sum256([]byte(strings.TrimSpace(received)))
	right := sha256.Sum256([]byte(strings.TrimSpace(expected)))
	return expected != "" && subtle.ConstantTimeCompare(left[:], right[:]) == 1
}

func (a *AgentRuntime) handleCoreConfig(w http.ResponseWriter, r *http.Request, path string) bool {
	if a.configStore == nil {
		return false
	}
	isPrepare := path == "/api/v1/runtime/prepare"
	isConfig := path == "/api/v1/runtime/config"
	isConfigLayers := path == "/api/v1/config/layers"
	isPersonaRuntime := path == "/api/v1/personas/runtime-profiles" || strings.HasPrefix(path, "/api/v1/personas/runtime-profiles/")
	isPersona := (path == "/api/v1/personas" || strings.HasPrefix(path, "/api/v1/personas/")) && !isPersonaRuntime
	isPersonaBinding := path == "/api/v1/persona-bindings" || strings.HasPrefix(path, "/api/v1/persona-bindings/")
	isAgentInstance := path == "/api/v1/agent-policy-templates" || strings.HasPrefix(path, "/api/v1/agent-policy-templates/") ||
		path == "/api/v1/agent-instances" || strings.HasPrefix(path, "/api/v1/agent-instances/") ||
		path == "/api/v1/agent-instance-routes" || strings.HasPrefix(path, "/api/v1/agent-instance-routes/") ||
		path == "/api/v1/agent-instance-capabilities" || strings.HasPrefix(path, "/api/v1/agent-instance-capabilities/")
	if !isPrepare && !isConfig && !isConfigLayers && !isPersona && !isPersonaRuntime && !isPersonaBinding && !isAgentInstance {
		return false
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodDelete {
		adminOK := tokenMatches(r.Header.Get(adminTokenHeader), a.adminToken)
		runtimeOK := isPrepare && a.authorized(r)
		if !adminOK && !runtimeOK {
			message := "administrator service token required"
			if isPrepare {
				message = "runtime service token required"
			}
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error": map[string]string{"code": "unauthorized", "message": message},
			})
			return true
		}
	}
	var err error
	switch {
	case isPrepare && r.Method == http.MethodPost:
		var data preparedRuntimeData
		data, err = a.configStore.prepareRuntimeRequest(r)
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		}
	case isConfig && r.Method == http.MethodGet:
		var data nativeRuntimeConfig
		data, err = a.configStore.runtimeConfig()
		if err == nil {
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		}
	case isConfig && r.Method == http.MethodPut:
		var payload coreRuntimeConfigPayload
		var fields map[string]json.RawMessage
		fields, err = decodeCoreObject(r, coreRuntimeConfigFields, "runtime config", &payload)
		if err == nil {
			var data nativeRuntimeConfig
			data, err = a.configStore.updateRuntimeConfig(payload, fields)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"data": data})
			}
		}
	case isConfigLayers:
		err = a.configStore.handleConfigLayers(w, r)
	case isPersona:
		err = a.configStore.handlePersonaRequest(w, r, path)
	case isPersonaRuntime:
		err = a.configStore.handlePersonaRuntimeRequest(w, r, path)
	case isPersonaBinding:
		err = a.configStore.handlePersonaBindingRequest(w, r, path)
	case isAgentInstance:
		if path == "/api/v1/agent-instance-capabilities" || strings.HasPrefix(path, "/api/v1/agent-instance-capabilities/") {
			err = a.configStore.handleAgentInstanceCapabilities(w, r, path)
		} else {
			err = a.configStore.handleAgentInstanceRequest(w, r, path)
		}
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
		})
		return true
	}
	if err != nil {
		writeCoreAPIError(w, err)
	}
	return true
}

func writeCoreAPIError(w http.ResponseWriter, err error) {
	var apiError *coreAPIError
	if errors.As(err, &apiError) {
		writeJSON(w, apiError.status, map[string]any{
			"error": map[string]string{"code": apiError.code, "message": apiError.message},
		})
		return
	}
	writeJSON(w, http.StatusInternalServerError, map[string]any{
		"error": map[string]string{"code": "internal_error", "message": "internal server error"},
	})
}

func (s *coreConfigStore) handlePersonaRequest(w http.ResponseWriter, r *http.Request, path string) error {
	if path == "/api/v1/personas" {
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
			data, err := s.listPersonas(namespace, limit, offset)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
			return nil
		case http.MethodPost:
			var payload corePersonaPayload
			if _, err := decodeCoreObject(r, corePersonaCreateFields, "persona", &payload); err != nil {
				return err
			}
			data, err := s.createPersona(payload)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": data})
			return nil
		}
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
		})
		return nil
	}
	remainder := strings.TrimPrefix(path, "/api/v1/personas/")
	parts := strings.Split(remainder, "/")
	isVisualReferencePath := len(parts) >= 2 && parts[1] == "visual-references" &&
		(len(parts) == 2 || len(parts) == 3 || len(parts) == 4 && parts[3] == "content")
	if len(parts) != 1 &&
		!(len(parts) == 2 && (parts[1] == "worldbook" || parts[1] == "samples" || parts[1] == "traits")) &&
		!(len(parts) == 3 && (parts[1] == "worldbook" || parts[1] == "samples" || parts[1] == "traits")) &&
		!isVisualReferencePath {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "route not found"}
	}
	personaID, err := parseCorePathID(parts[0])
	if err != nil {
		return err
	}
	namespace, err := normalizeCoreNamespace(r.URL.Query().Get("namespace"))
	if err != nil {
		return err
	}
	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			data, found, err := s.persona(namespace, personaID)
			if err != nil {
				return err
			}
			if !found {
				return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		case http.MethodPut:
			var payload corePersonaPayload
			if _, err := decodeCoreObject(r, corePersonaUpdateFields, "persona", &payload); err != nil {
				return err
			}
			data, found, err := s.updatePersona(namespace, personaID, payload)
			if err != nil {
				return err
			}
			if !found {
				return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		case http.MethodDelete:
			deleted, err := s.deletePersona(namespace, personaID)
			if err != nil {
				return err
			}
			if !deleted {
				return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": personaID, "deleted": true}})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
			})
		}
		return nil
	}
	if _, found, err := s.persona(namespace, personaID); err != nil {
		return err
	} else if !found {
		return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "persona not found"}
	}
	if parts[1] == "samples" {
		return s.handlePersonaSamples(w, r, namespace, personaID, parts[2:])
	}
	if parts[1] == "traits" {
		return s.handlePersonaTraits(w, r, namespace, personaID, parts[2:])
	}
	if parts[1] == "visual-references" {
		return s.handlePersonaVisualReferences(w, r, namespace, personaID, parts[2:])
	}
	if len(parts) == 2 {
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			data, err := s.listWorldbook(namespace, personaID, limit, offset)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusOK, map[string]any{"data": data})
		case http.MethodPost:
			var payload coreWorldbookPayload
			fields, err := decodeCoreObject(r, coreWorldbookCreateFields, "worldbook entry", &payload)
			if err != nil {
				return err
			}
			data, err := s.createWorldbook(namespace, personaID, payload, fields)
			if err != nil {
				return err
			}
			writeJSON(w, http.StatusCreated, map[string]any{"data": data})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
				"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
			})
		}
		return nil
	}
	entryID, err := parseCorePathID(parts[2])
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		data, found, err := s.worldbookEntry(namespace, personaID, entryID)
		if err != nil {
			return err
		}
		if !found {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "worldbook entry not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	case http.MethodPut:
		var payload coreWorldbookPayload
		fields, err := decodeCoreObject(r, coreWorldbookUpdateFields, "worldbook entry", &payload)
		if err != nil {
			return err
		}
		data, found, err := s.updateWorldbook(namespace, personaID, entryID, payload, fields)
		if err != nil {
			return err
		}
		if !found {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "worldbook entry not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
	case http.MethodDelete:
		deleted, err := s.deleteWorldbook(namespace, personaID, entryID)
		if err != nil {
			return err
		}
		if !deleted {
			return &coreAPIError{status: http.StatusNotFound, code: "not_found", message: "worldbook entry not found"}
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": entryID, "deleted": true}})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{
			"error": map[string]string{"code": "method_not_allowed", "message": "method not allowed"},
		})
	}
	return nil
}

func decodeJSONStringList(raw string) []string {
	var values []string
	if json.Unmarshal([]byte(raw), &values) != nil || values == nil {
		return []string{}
	}
	return values
}

func scanNativePersona(scanner interface{ Scan(...any) error }) (nativePersona, error) {
	var value nativePersona
	var alternate, tags string
	err := scanner.Scan(
		&value.ID, &value.Namespace, &value.Name, &value.Description, &value.Personality,
		&value.Scenario, &value.FirstMessage, &value.SystemPrompt, &value.PostHistoryInstructions,
		&value.MessageExample, &alternate, &tags, &value.Creator, &value.CharacterVersion,
		&value.SourceFormat, &value.SourceVersion, &value.AvatarDataURI, &value.VisualDescription,
		&value.CreatedAt, &value.UpdatedAt,
	)
	value.AlternateGreetings = decodeJSONStringList(alternate)
	value.Tags = decodeJSONStringList(tags)
	return value, err
}

const nativePersonaColumns = `
	id, namespace, name, description, personality, scenario, first_message, system_prompt,
	post_history_instructions, message_example, alternate_greetings_json, tags_json, creator,
	character_version, source_format, source_version, avatar_data_uri, visual_description, created_at, updated_at
`

func (s *coreConfigStore) persona(namespace, id string) (nativePersona, bool, error) {
	value, err := scanNativePersona(s.db.QueryRow(
		"SELECT "+nativePersonaColumns+" FROM personas WHERE namespace = ? AND id = ?", namespace, id,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nativePersona{}, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) listPersonas(namespace string, limit, offset int) (corePage[nativePersona], error) {
	result := corePage[nativePersona]{Items: []nativePersona{}, Limit: limit, Offset: offset}
	if err := s.db.QueryRow("SELECT count(*) FROM personas WHERE namespace = ?", namespace).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := s.db.Query(
		"SELECT "+nativePersonaColumns+" FROM personas WHERE namespace = ? ORDER BY updated_at DESC, id LIMIT ? OFFSET ?",
		namespace, limit, offset,
	)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		value, err := scanNativePersona(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, value)
	}
	return result, rows.Err()
}

func coreStringValue(value *string, current, name string, maximum int, required bool) (string, error) {
	if value == nil {
		return current, nil
	}
	return normalizeCoreText(*value, name, maximum, required)
}

func coreStringListValue(value *[]string, current []string, name string, maxItems, maxLength int) ([]string, error) {
	if value == nil {
		return current, nil
	}
	return normalizeCoreStrings(*value, name, maxItems, maxLength)
}

func personaPayloadValues(payload corePersonaPayload, current nativePersona) (nativePersona, error) {
	var err error
	if current.AlternateGreetings == nil {
		current.AlternateGreetings = []string{}
	}
	if current.Tags == nil {
		current.Tags = []string{}
	}
	if current.SourceFormat == "" {
		current.SourceFormat = "native"
	}
	if current.Name, err = coreStringValue(payload.Name, current.Name, "name", 120, true); err != nil {
		return current, err
	}
	fields := []struct {
		input   *string
		current *string
		name    string
		maximum int
	}{
		{payload.Description, &current.Description, "description", 20000},
		{payload.Personality, &current.Personality, "personality", 20000},
		{payload.Scenario, &current.Scenario, "scenario", 20000},
		{payload.FirstMessage, &current.FirstMessage, "firstMessage", 20000},
		{payload.SystemPrompt, &current.SystemPrompt, "systemPrompt", 20000},
		{payload.PostHistoryInstructions, &current.PostHistoryInstructions, "postHistoryInstructions", 20000},
		{payload.MessageExample, &current.MessageExample, "messageExample", 20000},
		{payload.Creator, &current.Creator, "creator", 200},
		{payload.CharacterVersion, &current.CharacterVersion, "characterVersion", 100},
		{payload.SourceVersion, &current.SourceVersion, "sourceVersion", 40},
		{payload.VisualDescription, &current.VisualDescription, "visualDescription", 4000},
	}
	for _, field := range fields {
		*field.current, err = coreStringValue(field.input, *field.current, field.name, field.maximum, false)
		if err != nil {
			return current, err
		}
	}
	current.SourceFormat, err = coreStringValue(payload.SourceFormat, current.SourceFormat, "sourceFormat", 100, true)
	if err != nil {
		return current, err
	}
	current.AlternateGreetings, err = coreStringListValue(payload.AlternateGreetings, current.AlternateGreetings, "alternateGreetings", 20, 4000)
	if err != nil {
		return current, err
	}
	current.Tags, err = coreStringListValue(payload.Tags, current.Tags, "tags", 64, 100)
	if err != nil {
		return current, err
	}
	if payload.AvatarDataURI != nil {
		current.AvatarDataURI, err = validateAvatarDataURI(*payload.AvatarDataURI)
		if err != nil {
			return current, err
		}
	}
	return current, nil
}

func (s *coreConfigStore) createPersona(payload corePersonaPayload) (nativePersona, error) {
	id, err := newCoreUUID()
	if err != nil {
		return nativePersona{}, err
	}
	if payload.ID != nil {
		id, err = normalizeCoreText(*payload.ID, "id", 120, true)
		if err != nil {
			return nativePersona{}, err
		}
	}
	namespace := "default"
	if payload.Namespace != nil {
		namespace, err = normalizeCoreNamespace(*payload.Namespace)
		if err != nil {
			return nativePersona{}, err
		}
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", id).Scan(&exists); err != nil {
		return nativePersona{}, err
	}
	if exists != 0 {
		return nativePersona{}, coreInvalid("persona id already exists")
	}
	value, err := personaPayloadValues(payload, nativePersona{Namespace: namespace, SourceFormat: "native"})
	if err != nil {
		return nativePersona{}, err
	}
	value.ID = id
	value.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	value.UpdatedAt = value.CreatedAt
	alternate, _ := json.Marshal(value.AlternateGreetings)
	tags, _ := json.Marshal(value.Tags)
	_, err = s.db.Exec(`
		INSERT INTO personas (
			id, namespace, name, description, personality, scenario, first_message,
			system_prompt, post_history_instructions, message_example, alternate_greetings_json,
			tags_json, creator, character_version, source_format, source_version, avatar_data_uri, visual_description,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, value.ID, value.Namespace, value.Name, value.Description, value.Personality, value.Scenario,
		value.FirstMessage, value.SystemPrompt, value.PostHistoryInstructions, value.MessageExample,
		string(alternate), string(tags), value.Creator, value.CharacterVersion, value.SourceFormat,
		value.SourceVersion, value.AvatarDataURI, value.VisualDescription, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return nativePersona{}, err
	}
	value, _, err = s.persona(namespace, id)
	return value, err
}

func (s *coreConfigStore) updatePersona(namespace, id string, payload corePersonaPayload) (nativePersona, bool, error) {
	current, found, err := s.persona(namespace, id)
	if err != nil || !found {
		return nativePersona{}, found, err
	}
	value, err := personaPayloadValues(payload, current)
	if err != nil {
		return nativePersona{}, true, err
	}
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	alternate, _ := json.Marshal(value.AlternateGreetings)
	tags, _ := json.Marshal(value.Tags)
	_, err = s.db.Exec(`
		UPDATE personas SET name = ?, description = ?, personality = ?, scenario = ?, first_message = ?,
			system_prompt = ?, post_history_instructions = ?, message_example = ?,
			alternate_greetings_json = ?, tags_json = ?, creator = ?, character_version = ?,
			source_format = ?, source_version = ?, avatar_data_uri = ?, visual_description = ?, updated_at = ?
		WHERE namespace = ? AND id = ?
	`, value.Name, value.Description, value.Personality, value.Scenario, value.FirstMessage,
		value.SystemPrompt, value.PostHistoryInstructions, value.MessageExample, string(alternate),
		string(tags), value.Creator, value.CharacterVersion, value.SourceFormat, value.SourceVersion,
		value.AvatarDataURI, value.VisualDescription, value.UpdatedAt, namespace, id)
	if err != nil {
		return nativePersona{}, true, err
	}
	value, _, err = s.persona(namespace, id)
	return value, true, err
}

func (s *coreConfigStore) deletePersona(namespace, id string) (bool, error) {
	result, err := s.db.Exec("DELETE FROM personas WHERE namespace = ? AND id = ?", namespace, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func scanNativeWorldbook(scanner interface{ Scan(...any) error }) (nativeWorldbookEntry, error) {
	var value nativeWorldbookEntry
	var keys, secondary string
	var enabled, constant, selective int
	var token sql.NullInt64
	err := scanner.Scan(
		&value.ID, &value.PersonaID, &keys, &secondary, &value.Comment, &value.Content,
		&enabled, &constant, &selective, &value.Priority, &value.Position, &value.InsertionOrder,
		&token, &value.CreatedAt, &value.UpdatedAt,
	)
	value.Keys = decodeJSONStringList(keys)
	value.SecondaryKeys = decodeJSONStringList(secondary)
	value.Enabled, value.Constant, value.Selective = enabled == 1, constant == 1, selective == 1
	if token.Valid {
		budget := int(token.Int64)
		value.TokenBudget = &budget
	}
	return value, err
}

const nativeWorldbookColumns = `
	w.id, w.persona_id, w.keys_json, w.secondary_keys_json, w.comment, w.content,
	w.enabled, w.constant, w.selective, w.priority, w.position, w.insertion_order,
	w.token_budget, w.created_at, w.updated_at
`

func (s *coreConfigStore) worldbookEntry(namespace, personaID, id string) (nativeWorldbookEntry, bool, error) {
	value, err := scanNativeWorldbook(s.db.QueryRow(`
		SELECT `+nativeWorldbookColumns+` FROM worldbook_entries w
		JOIN personas p ON p.id = w.persona_id
		WHERE p.namespace = ? AND w.persona_id = ? AND w.id = ?
	`, namespace, personaID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nativeWorldbookEntry{}, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) listWorldbook(namespace, personaID string, limit, offset int) (corePage[nativeWorldbookEntry], error) {
	result := corePage[nativeWorldbookEntry]{Items: []nativeWorldbookEntry{}, Limit: limit, Offset: offset}
	if err := s.db.QueryRow(`
		SELECT count(*) FROM worldbook_entries w JOIN personas p ON p.id = w.persona_id
		WHERE p.namespace = ? AND w.persona_id = ?
	`, namespace, personaID).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := s.db.Query(`
		SELECT `+nativeWorldbookColumns+` FROM worldbook_entries w
		JOIN personas p ON p.id = w.persona_id
		WHERE p.namespace = ? AND w.persona_id = ?
		ORDER BY w.priority DESC, w.insertion_order, w.id LIMIT ? OFFSET ?
	`, namespace, personaID, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		value, err := scanNativeWorldbook(rows)
		if err != nil {
			return result, err
		}
		result.Items = append(result.Items, value)
	}
	return result, rows.Err()
}

func worldbookPayloadValues(payload coreWorldbookPayload, fields map[string]json.RawMessage, current nativeWorldbookEntry) (nativeWorldbookEntry, error) {
	var err error
	if current.Keys == nil {
		current.Keys = []string{}
	}
	if current.SecondaryKeys == nil {
		current.SecondaryKeys = []string{}
	}
	if current.Position == "" {
		current.Position = "before_char"
	}
	current.Keys, err = coreStringListValue(payload.Keys, current.Keys, "keys", 64, 200)
	if err != nil {
		return current, err
	}
	current.SecondaryKeys, err = coreStringListValue(payload.SecondaryKeys, current.SecondaryKeys, "secondaryKeys", 64, 200)
	if err != nil {
		return current, err
	}
	current.Comment, err = coreStringValue(payload.Comment, current.Comment, "comment", 500, false)
	if err != nil {
		return current, err
	}
	current.Content, err = coreStringValue(payload.Content, current.Content, "content", 20000, true)
	if err != nil {
		return current, err
	}
	if payload.Enabled != nil {
		current.Enabled = *payload.Enabled
	}
	if payload.Constant != nil {
		current.Constant = *payload.Constant
	}
	if payload.Selective != nil {
		current.Selective = *payload.Selective
	}
	if payload.Priority != nil {
		current.Priority = *payload.Priority
	}
	if payload.InsertionOrder != nil {
		current.InsertionOrder = *payload.InsertionOrder
	}
	current.Position, err = coreStringValue(payload.Position, current.Position, "position", 40, true)
	if err != nil {
		return current, err
	}
	current.Position = strings.ToLower(current.Position)
	if current.Position != "before_char" && current.Position != "after_char" && current.Position != "before_example" && current.Position != "after_example" {
		return current, coreInvalid("position is not supported")
	}
	if raw, ok := fields["tokenBudget"]; ok {
		if string(raw) == "null" {
			current.TokenBudget = nil
		} else {
			var value int
			if json.Unmarshal(raw, &value) != nil || value < 0 {
				return current, coreInvalid("tokenBudget must be an integer at least 0")
			}
			current.TokenBudget = &value
		}
	}
	return current, nil
}

func (s *coreConfigStore) createWorldbook(namespace, personaID string, payload coreWorldbookPayload, fields map[string]json.RawMessage) (nativeWorldbookEntry, error) {
	id, err := newCoreUUID()
	if err != nil {
		return nativeWorldbookEntry{}, err
	}
	if payload.ID != nil {
		id, err = normalizeCoreText(*payload.ID, "id", 120, true)
		if err != nil {
			return nativeWorldbookEntry{}, err
		}
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM worldbook_entries WHERE id = ?", id).Scan(&exists); err != nil {
		return nativeWorldbookEntry{}, err
	}
	if exists != 0 {
		return nativeWorldbookEntry{}, coreInvalid("worldbook entry id already exists")
	}
	value, err := worldbookPayloadValues(payload, fields, nativeWorldbookEntry{
		ID: id, PersonaID: personaID, Enabled: true, Position: "before_char",
	})
	if err != nil {
		return nativeWorldbookEntry{}, err
	}
	value.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	value.UpdatedAt = value.CreatedAt
	keys, _ := json.Marshal(value.Keys)
	secondary, _ := json.Marshal(value.SecondaryKeys)
	_, err = s.db.Exec(`
		INSERT INTO worldbook_entries (
			id, persona_id, keys_json, secondary_keys_json, comment, content, enabled, constant,
			selective, priority, position, insertion_order, token_budget, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, value.ID, value.PersonaID, string(keys), string(secondary), value.Comment, value.Content,
		boolInt(value.Enabled), boolInt(value.Constant), boolInt(value.Selective), value.Priority,
		value.Position, value.InsertionOrder, value.TokenBudget, value.CreatedAt, value.UpdatedAt)
	if err != nil {
		return nativeWorldbookEntry{}, err
	}
	value, _, err = s.worldbookEntry(namespace, personaID, id)
	return value, err
}

func (s *coreConfigStore) updateWorldbook(namespace, personaID, id string, payload coreWorldbookPayload, fields map[string]json.RawMessage) (nativeWorldbookEntry, bool, error) {
	current, found, err := s.worldbookEntry(namespace, personaID, id)
	if err != nil || !found {
		return nativeWorldbookEntry{}, found, err
	}
	value, err := worldbookPayloadValues(payload, fields, current)
	if err != nil {
		return nativeWorldbookEntry{}, true, err
	}
	value.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	keys, _ := json.Marshal(value.Keys)
	secondary, _ := json.Marshal(value.SecondaryKeys)
	_, err = s.db.Exec(`
		UPDATE worldbook_entries SET keys_json = ?, secondary_keys_json = ?, comment = ?, content = ?,
			enabled = ?, constant = ?, selective = ?, priority = ?, position = ?, insertion_order = ?,
			token_budget = ?, updated_at = ? WHERE persona_id = ? AND id = ?
	`, string(keys), string(secondary), value.Comment, value.Content, boolInt(value.Enabled),
		boolInt(value.Constant), boolInt(value.Selective), value.Priority, value.Position,
		value.InsertionOrder, value.TokenBudget, value.UpdatedAt, personaID, id)
	if err != nil {
		return nativeWorldbookEntry{}, true, err
	}
	value, _, err = s.worldbookEntry(namespace, personaID, id)
	return value, true, err
}

func (s *coreConfigStore) deleteWorldbook(namespace, personaID, id string) (bool, error) {
	if _, found, err := s.worldbookEntry(namespace, personaID, id); err != nil || !found {
		return false, err
	}
	result, err := s.db.Exec("DELETE FROM worldbook_entries WHERE persona_id = ? AND id = ?", personaID, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count > 0, err
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func scanRuntimeConfig(scanner interface{ Scan(...any) error }) (nativeRuntimeConfig, error) {
	var value nativeRuntimeConfig
	var active, last sql.NullString
	var persona, knowledge, worldbook, repetitive, learning int
	var topics string
	err := scanner.Scan(
		&active, &persona, &knowledge, &worldbook, &value.ProtectedRules, &value.ReplyStyle,
		&value.MaxReplySentences, &value.MaxReplyChars, &repetitive, &value.KnowledgeNamespace,
		&learning, &topics, &value.LearningIntervalHours, &last, &value.UpdatedAt,
	)
	if active.Valid {
		value.ActivePersonaID = &active.String
	}
	if last.Valid {
		value.LastCollectedAt = &last.String
	}
	value.PersonaInjectionEnabled = persona == 1
	value.KnowledgeInjectionEnabled = knowledge == 1
	value.WorldbookInjectionEnabled = worldbook == 1
	value.AvoidRepetitiveOpeners = repetitive == 1
	value.LearningEnabled = learning == 1
	value.LearningTopics = decodeJSONStringList(topics)
	return value, err
}

func (s *coreConfigStore) runtimeConfig() (nativeRuntimeConfig, error) {
	return scanRuntimeConfig(s.db.QueryRow(`
		SELECT active_persona_id, persona_injection_enabled, knowledge_injection_enabled,
			worldbook_injection_enabled, protected_rules, reply_style, max_reply_sentences,
			max_reply_chars, avoid_repetitive_openers, knowledge_namespace, learning_enabled,
			learning_topics_json, learning_interval_hours, last_collected_at, updated_at
		FROM runtime_config WHERE id = 1
	`))
}

func (s *coreConfigStore) updateRuntimeConfig(payload coreRuntimeConfigPayload, fields map[string]json.RawMessage) (nativeRuntimeConfig, error) {
	current, err := s.runtimeConfig()
	if err != nil {
		return nativeRuntimeConfig{}, err
	}
	if raw, ok := fields["activePersonaId"]; ok {
		if string(raw) == "null" {
			current.ActivePersonaID = nil
		} else {
			var id string
			if json.Unmarshal(raw, &id) != nil {
				return nativeRuntimeConfig{}, coreInvalid("activePersonaId must be a string or null")
			}
			id, err = normalizeCoreText(id, "activePersonaId", 120, true)
			if err != nil {
				return nativeRuntimeConfig{}, err
			}
			var exists int
			if err = s.db.QueryRow("SELECT count(*) FROM personas WHERE id = ?", id).Scan(&exists); err != nil {
				return nativeRuntimeConfig{}, err
			}
			if exists == 0 {
				return nativeRuntimeConfig{}, coreInvalid("active persona does not exist")
			}
			current.ActivePersonaID = &id
		}
	}
	if payload.PersonaInjectionEnabled != nil {
		current.PersonaInjectionEnabled = *payload.PersonaInjectionEnabled
	}
	if payload.KnowledgeInjectionEnabled != nil {
		current.KnowledgeInjectionEnabled = *payload.KnowledgeInjectionEnabled
	}
	if payload.WorldbookInjectionEnabled != nil {
		current.WorldbookInjectionEnabled = *payload.WorldbookInjectionEnabled
	}
	if current.ProtectedRules, err = coreStringValue(payload.ProtectedRules, current.ProtectedRules, "protectedRules", 40000, false); err != nil {
		return nativeRuntimeConfig{}, err
	}
	if current.ReplyStyle, err = coreStringValue(payload.ReplyStyle, current.ReplyStyle, "replyStyle", 10000, false); err != nil {
		return nativeRuntimeConfig{}, err
	}
	if payload.MaxReplySentences != nil {
		current.MaxReplySentences = *payload.MaxReplySentences
	}
	if current.MaxReplySentences < 1 || current.MaxReplySentences > 6 {
		return nativeRuntimeConfig{}, coreInvalid("maxReplySentences must be between 1 and 6")
	}
	if payload.MaxReplyChars != nil {
		current.MaxReplyChars = *payload.MaxReplyChars
	}
	if current.MaxReplyChars < 20 || current.MaxReplyChars > 1000 {
		return nativeRuntimeConfig{}, coreInvalid("maxReplyChars must be between 20 and 1000")
	}
	if payload.AvoidRepetitiveOpeners != nil {
		current.AvoidRepetitiveOpeners = *payload.AvoidRepetitiveOpeners
	}
	if payload.KnowledgeNamespace != nil {
		current.KnowledgeNamespace, err = normalizeCoreNamespace(*payload.KnowledgeNamespace)
		if err != nil {
			return nativeRuntimeConfig{}, err
		}
	}
	if payload.LearningEnabled != nil {
		current.LearningEnabled = *payload.LearningEnabled
	}
	current.LearningTopics, err = coreStringListValue(payload.LearningTopics, current.LearningTopics, "learningTopics", 32, 200)
	if err != nil {
		return nativeRuntimeConfig{}, err
	}
	if payload.LearningIntervalHours != nil {
		current.LearningIntervalHours = *payload.LearningIntervalHours
	}
	if current.LearningIntervalHours < 6 || current.LearningIntervalHours > 168 {
		return nativeRuntimeConfig{}, coreInvalid("learningIntervalHours must be between 6 and 168")
	}
	if raw, ok := fields["lastCollectedAt"]; ok {
		if string(raw) == "null" || string(raw) == `""` {
			current.LastCollectedAt = nil
		} else {
			var timestamp string
			if json.Unmarshal(raw, &timestamp) != nil {
				return nativeRuntimeConfig{}, coreInvalid("lastCollectedAt must be an ISO timestamp or null")
			}
			parsed, parseErr := time.Parse(time.RFC3339Nano, timestamp)
			if parseErr != nil {
				return nativeRuntimeConfig{}, coreInvalid("lastCollectedAt must be an ISO timestamp or null")
			}
			timestamp = parsed.UTC().Format(time.RFC3339Nano)
			current.LastCollectedAt = &timestamp
		}
	}
	current.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	topics, _ := json.Marshal(current.LearningTopics)
	_, err = s.db.Exec(`
		UPDATE runtime_config SET active_persona_id = ?, persona_injection_enabled = ?,
			knowledge_injection_enabled = ?, worldbook_injection_enabled = ?, protected_rules = ?,
			reply_style = ?, max_reply_sentences = ?, max_reply_chars = ?, avoid_repetitive_openers = ?,
			knowledge_namespace = ?, learning_enabled = ?, learning_topics_json = ?,
			learning_interval_hours = ?, last_collected_at = ?, updated_at = ? WHERE id = 1
	`, current.ActivePersonaID, boolInt(current.PersonaInjectionEnabled), boolInt(current.KnowledgeInjectionEnabled),
		boolInt(current.WorldbookInjectionEnabled), current.ProtectedRules, current.ReplyStyle,
		current.MaxReplySentences, current.MaxReplyChars, boolInt(current.AvoidRepetitiveOpeners),
		current.KnowledgeNamespace, boolInt(current.LearningEnabled), string(topics),
		current.LearningIntervalHours, current.LastCollectedAt, current.UpdatedAt)
	if err != nil {
		return nativeRuntimeConfig{}, err
	}
	return s.runtimeConfig()
}

func (s *coreConfigStore) integrationRaw(id string) (json.RawMessage, error) {
	var raw string
	if err := s.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = ?", id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, coreInvalid("integration config is missing")
		}
		return nil, err
	}
	var object map[string]any
	if json.Unmarshal([]byte(raw), &object) != nil || object == nil {
		return json.RawMessage(`{}`), nil
	}
	return json.RawMessage(raw), nil
}
