package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type nativePersonaTrait struct {
	ID          string   `json:"id"`
	PersonaID   string   `json:"personaId"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Triggers    []string `json:"triggers"`
	Supports    []string `json:"supports"`
	Conflicts   []string `json:"conflicts"`
	Source      string   `json:"source"`
	Weight      float64  `json:"weight"`
	Enabled     bool     `json:"enabled"`
	CreatedAt   string   `json:"createdAt"`
	UpdatedAt   string   `json:"updatedAt"`
}

type nativePersonaTraitContext struct {
	Items []nativePersonaTraitContextItem `json:"items"`
}

type nativePersonaTraitContextItem struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Source string `json:"source"`
}

type corePersonaTraitPayload struct {
	ID          *string   `json:"id"`
	Name        *string   `json:"name"`
	Description *string   `json:"description"`
	Triggers    *[]string `json:"triggers"`
	Supports    *[]string `json:"supports"`
	Conflicts   *[]string `json:"conflicts"`
	Source      *string   `json:"source"`
	Weight      *float64  `json:"weight"`
	Enabled     *bool     `json:"enabled"`
}

var corePersonaTraitCreateFields = coreFieldSet(
	"id", "name", "description", "triggers", "supports", "conflicts", "source", "weight", "enabled",
)

var corePersonaTraitUpdateFields = coreFieldSet(
	"name", "description", "triggers", "supports", "conflicts", "source", "weight", "enabled",
)

const nativePersonaTraitColumns = `
	id, persona_id, name, description, triggers_json, supports_json, conflicts_json,
	source, weight, enabled, created_at, updated_at
`

func scanNativePersonaTrait(scanner interface{ Scan(...any) error }) (nativePersonaTrait, error) {
	var value nativePersonaTrait
	var triggers, supports, conflicts string
	var enabled int
	err := scanner.Scan(
		&value.ID, &value.PersonaID, &value.Name, &value.Description,
		&triggers, &supports, &conflicts, &value.Source, &value.Weight,
		&enabled, &value.CreatedAt, &value.UpdatedAt,
	)
	value.Triggers = decodeJSONStringList(triggers)
	value.Supports = decodeJSONStringList(supports)
	value.Conflicts = decodeJSONStringList(conflicts)
	value.Enabled = enabled == 1
	return value, err
}

func (s *coreConfigStore) personaTrait(namespace, personaID, id string) (nativePersonaTrait, bool, error) {
	value, err := scanNativePersonaTrait(s.db.QueryRow(`
		SELECT `+nativePersonaTraitColumns+` FROM persona_traits
		WHERE persona_id = ? AND id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
	`, personaID, id, namespace))
	if errors.Is(err, sql.ErrNoRows) {
		return nativePersonaTrait{}, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) listPersonaTraits(namespace, personaID string, limit, offset int) (corePage[nativePersonaTrait], error) {
	result := corePage[nativePersonaTrait]{Items: []nativePersonaTrait{}, Limit: limit, Offset: offset}
	if err := s.db.QueryRow(`
		SELECT count(*) FROM persona_traits WHERE persona_id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
	`, personaID, namespace).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := s.db.Query(`
		SELECT `+nativePersonaTraitColumns+` FROM persona_traits WHERE persona_id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
		ORDER BY weight DESC, name, id LIMIT ? OFFSET ?
	`, personaID, namespace, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		value, scanErr := scanNativePersonaTrait(rows)
		if scanErr != nil {
			return result, scanErr
		}
		result.Items = append(result.Items, value)
	}
	return result, rows.Err()
}

func personaTraitPayloadValues(payload corePersonaTraitPayload, current nativePersonaTrait) (nativePersonaTrait, error) {
	var err error
	for _, field := range []struct {
		input    *string
		current  *string
		name     string
		maximum  int
		required bool
	}{
		{payload.Name, &current.Name, "name", 120, true},
		{payload.Description, &current.Description, "description", 2000, true},
		{payload.Source, &current.Source, "source", 1000, true},
	} {
		*field.current, err = coreStringValue(field.input, *field.current, field.name, field.maximum, field.required)
		if err != nil {
			return current, err
		}
	}
	current.Triggers, err = coreStringListValue(payload.Triggers, current.Triggers, "triggers", 32, 100)
	if err != nil {
		return current, err
	}
	current.Supports, err = coreStringListValue(payload.Supports, current.Supports, "supports", 24, 120)
	if err != nil {
		return current, err
	}
	current.Conflicts, err = coreStringListValue(payload.Conflicts, current.Conflicts, "conflicts", 24, 120)
	if err != nil {
		return current, err
	}
	if payload.Weight != nil {
		if *payload.Weight < 0 || *payload.Weight > 100 {
			return current, coreInvalid("weight must be between 0 and 100")
		}
		current.Weight = *payload.Weight
	}
	if payload.Enabled != nil {
		current.Enabled = *payload.Enabled
	}
	return current, nil
}

func (s *coreConfigStore) createPersonaTrait(namespace, personaID string, payload corePersonaTraitPayload) (nativePersonaTrait, error) {
	id := ""
	var err error
	if payload.ID == nil {
		id, err = newCoreUUID()
	} else {
		id, err = normalizeCoreText(*payload.ID, "id", 200, true)
	}
	if err != nil {
		return nativePersonaTrait{}, err
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM persona_traits WHERE id = ?", id).Scan(&exists); err != nil {
		return nativePersonaTrait{}, err
	}
	if exists != 0 {
		return nativePersonaTrait{}, coreInvalid("persona trait id already exists")
	}
	value, err := personaTraitPayloadValues(payload, nativePersonaTrait{
		ID: id, PersonaID: personaID, Weight: 1, Enabled: true,
	})
	if err != nil {
		return nativePersonaTrait{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		INSERT INTO persona_traits (
			id, persona_id, name, description, triggers_json, supports_json, conflicts_json,
			source, weight, enabled, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, value.ID, personaID, value.Name, value.Description, mgmtJSON(value.Triggers),
		mgmtJSON(value.Supports), mgmtJSON(value.Conflicts), value.Source, value.Weight,
		boolInt(value.Enabled), now, now)
	if err != nil {
		return nativePersonaTrait{}, err
	}
	value, _, err = s.personaTrait(namespace, personaID, id)
	return value, err
}

func (s *coreConfigStore) updatePersonaTrait(namespace, personaID, id string, payload corePersonaTraitPayload) (nativePersonaTrait, bool, error) {
	current, found, err := s.personaTrait(namespace, personaID, id)
	if err != nil || !found {
		return nativePersonaTrait{}, found, err
	}
	value, err := personaTraitPayloadValues(payload, current)
	if err != nil {
		return nativePersonaTrait{}, true, err
	}
	_, err = s.db.Exec(`
		UPDATE persona_traits SET name = ?, description = ?, triggers_json = ?, supports_json = ?,
			conflicts_json = ?, source = ?, weight = ?, enabled = ?, updated_at = ?
		WHERE persona_id = ? AND id = ?
	`, value.Name, value.Description, mgmtJSON(value.Triggers), mgmtJSON(value.Supports),
		mgmtJSON(value.Conflicts), value.Source, value.Weight, boolInt(value.Enabled),
		time.Now().UTC().Format(time.RFC3339Nano), personaID, id)
	if err != nil {
		return nativePersonaTrait{}, true, err
	}
	value, _, err = s.personaTrait(namespace, personaID, id)
	return value, true, err
}

func (s *coreConfigStore) deletePersonaTrait(namespace, personaID, id string) (bool, error) {
	if _, found, err := s.personaTrait(namespace, personaID, id); err != nil || !found {
		return false, err
	}
	result, err := s.db.Exec("DELETE FROM persona_traits WHERE persona_id = ? AND id = ?", personaID, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *coreConfigStore) handlePersonaTraits(w http.ResponseWriter, r *http.Request, namespace, personaID string, rest []string) error {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			data, err := s.listPersonaTraits(namespace, personaID, limit, offset)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"data": data})
			}
			return err
		case http.MethodPost:
			var payload corePersonaTraitPayload
			if _, err := decodeCoreObject(r, corePersonaTraitCreateFields, "persona trait", &payload); err != nil {
				return err
			}
			data, err := s.createPersonaTrait(namespace, personaID, payload)
			if err == nil {
				writeJSON(w, http.StatusCreated, map[string]any{"data": data})
			}
			return err
		default:
			return mgmtMethodNotAllowed()
		}
	}
	id, err := parseCorePathID(rest[0])
	if err != nil {
		return err
	}
	switch r.Method {
	case http.MethodGet:
		data, found, err := s.personaTrait(namespace, personaID, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("persona trait")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return nil
	case http.MethodPut:
		var payload corePersonaTraitPayload
		if _, err = decodeCoreObject(r, corePersonaTraitUpdateFields, "persona trait", &payload); err != nil {
			return err
		}
		data, found, err := s.updatePersonaTrait(namespace, personaID, id, payload)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("persona trait")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return nil
	case http.MethodDelete:
		deleted, err := s.deletePersonaTrait(namespace, personaID, id)
		if err != nil {
			return err
		}
		if !deleted {
			return mgmtNotFound("persona trait")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": id, "deleted": true}})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

func (s *coreConfigStore) selectPersonaTraits(personaID string, query personaSampleQuery, limit int) ([]nativePersonaTrait, error) {
	if personaID == "" || limit <= 0 {
		return []nativePersonaTrait{}, nil
	}
	rows, err := s.db.Query(`
		SELECT `+nativePersonaTraitColumns+` FROM persona_traits
		WHERE persona_id = ? AND enabled = 1 ORDER BY weight DESC, id
	`, personaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []nativePersonaTrait{}
	for rows.Next() {
		value, scanErr := scanNativePersonaTrait(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		values = append(values, value)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	type scoredTrait struct {
		value nativePersonaTrait
		score float64
	}
	scores := map[string]float64{}
	byReference := map[string]nativePersonaTrait{}
	for _, value := range values {
		byReference[strings.ToLower(value.ID)] = value
		byReference[strings.ToLower(value.Name)] = value
		for _, trigger := range value.Triggers {
			trigger = strings.ToLower(strings.TrimSpace(trigger))
			switch {
			case trigger == "*":
				scores[value.ID] += value.Weight + 0.1
			case trigger != "" && strings.Contains(strings.ToLower(query.Message), trigger):
				scores[value.ID] += value.Weight + 4
			case trigger != "" && recentMessageContains(query.RecentMessages, trigger):
				scores[value.ID] += value.Weight + 1.25
			case personaContextMatches(trigger, query.RelationshipStage) || personaContextMatches(trigger, query.Emotion):
				scores[value.ID] += value.Weight + 2
			}
		}
	}
	for _, value := range values {
		if scores[value.ID] == 0 {
			continue
		}
		for _, reference := range value.Supports {
			if target, ok := byReference[strings.ToLower(strings.TrimSpace(reference))]; ok {
				scores[target.ID] += value.Weight * 0.2
			}
		}
		for _, reference := range value.Conflicts {
			if target, ok := byReference[strings.ToLower(strings.TrimSpace(reference))]; ok && scores[target.ID] > 0 {
				scores[target.ID] -= value.Weight * 0.25
			}
		}
	}
	scored := []scoredTrait{}
	for _, value := range values {
		if scores[value.ID] > 0 {
			scored = append(scored, scoredTrait{value: value, score: scores[value.ID]})
		}
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score == scored[j].score {
			return scored[i].value.ID < scored[j].value.ID
		}
		return scored[i].score > scored[j].score
	})
	if len(scored) > limit {
		scored = scored[:limit]
	}
	selected := make([]nativePersonaTrait, 0, len(scored))
	for _, value := range scored {
		selected = append(selected, value.value)
	}
	return selected, nil
}

func compilePersonaTraits(traits []nativePersonaTrait) string {
	if len(traits) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("本轮人格图谱：按当前关系、情绪和语境自然体现，不要逐条自我介绍，也不要覆盖安全与管理员规则。")
	for _, trait := range traits {
		builder.WriteString("\n- ")
		builder.WriteString(trait.Name)
		builder.WriteString("：")
		builder.WriteString(trait.Description)
		if len(trait.Supports) > 0 {
			builder.WriteString("；支持：")
			builder.WriteString(strings.Join(trait.Supports, "、"))
		}
		if len(trait.Conflicts) > 0 {
			builder.WriteString("；避免同时放大：")
			builder.WriteString(strings.Join(trait.Conflicts, "、"))
		}
	}
	return builder.String()
}
