package main

import (
	"database/sql"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

type nativePersonaSample struct {
	ID                   string   `json:"id"`
	PersonaID            string   `json:"personaId"`
	SceneTags            []string `json:"sceneTags"`
	RelationshipStage    string   `json:"relationshipStage"`
	Emotion              string   `json:"emotion"`
	Context              string   `json:"context"`
	CandidateReplies     []string `json:"candidateReplies"`
	ForbiddenExpressions []string `json:"forbiddenExpressions"`
	Source               string   `json:"source"`
	Weight               float64  `json:"weight"`
	Enabled              bool     `json:"enabled"`
	CreatedAt            string   `json:"createdAt"`
	UpdatedAt            string   `json:"updatedAt"`
}

type nativePersonaSampleContext struct {
	Items []nativePersonaSampleContextItem `json:"items"`
}

type nativePersonaSampleContextItem struct {
	ID                string   `json:"id"`
	SceneTags         []string `json:"sceneTags"`
	RelationshipStage string   `json:"relationshipStage"`
	Emotion           string   `json:"emotion"`
	Source            string   `json:"source"`
}

type corePersonaSamplePayload struct {
	ID                   *string   `json:"id"`
	SceneTags            *[]string `json:"sceneTags"`
	RelationshipStage    *string   `json:"relationshipStage"`
	Emotion              *string   `json:"emotion"`
	Context              *string   `json:"context"`
	CandidateReplies     *[]string `json:"candidateReplies"`
	ForbiddenExpressions *[]string `json:"forbiddenExpressions"`
	Source               *string   `json:"source"`
	Weight               *float64  `json:"weight"`
	Enabled              *bool     `json:"enabled"`
}

var corePersonaSampleCreateFields = coreFieldSet(
	"id", "sceneTags", "relationshipStage", "emotion", "context",
	"candidateReplies", "forbiddenExpressions", "source", "weight", "enabled",
)

var corePersonaSampleUpdateFields = coreFieldSet(
	"sceneTags", "relationshipStage", "emotion", "context",
	"candidateReplies", "forbiddenExpressions", "source", "weight", "enabled",
)

const nativePersonaSampleColumns = `
	id, persona_id, scene_tags_json, relationship_stage, emotion, context,
	candidate_replies_json, forbidden_expressions_json, source, weight, enabled,
	created_at, updated_at
`

func scanNativePersonaSample(scanner interface{ Scan(...any) error }) (nativePersonaSample, error) {
	var value nativePersonaSample
	var sceneTags, candidateReplies, forbiddenExpressions string
	var enabled int
	err := scanner.Scan(
		&value.ID, &value.PersonaID, &sceneTags, &value.RelationshipStage, &value.Emotion,
		&value.Context, &candidateReplies, &forbiddenExpressions, &value.Source,
		&value.Weight, &enabled, &value.CreatedAt, &value.UpdatedAt,
	)
	value.SceneTags = decodeJSONStringList(sceneTags)
	value.CandidateReplies = decodeJSONStringList(candidateReplies)
	value.ForbiddenExpressions = decodeJSONStringList(forbiddenExpressions)
	value.Enabled = enabled == 1
	return value, err
}

func (s *coreConfigStore) personaSample(namespace, personaID, id string) (nativePersonaSample, bool, error) {
	value, err := scanNativePersonaSample(s.db.QueryRow(`
		SELECT `+nativePersonaSampleColumns+` FROM persona_samples
		WHERE persona_id = ? AND id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
	`, personaID, id, namespace))
	if errors.Is(err, sql.ErrNoRows) {
		return nativePersonaSample{}, false, nil
	}
	return value, err == nil, err
}

func (s *coreConfigStore) listPersonaSamples(namespace, personaID string, limit, offset int) (corePage[nativePersonaSample], error) {
	result := corePage[nativePersonaSample]{Items: []nativePersonaSample{}, Limit: limit, Offset: offset}
	if err := s.db.QueryRow(`
		SELECT count(*) FROM persona_samples
		WHERE persona_id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
	`, personaID, namespace).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := s.db.Query(`
		SELECT `+nativePersonaSampleColumns+` FROM persona_samples
		WHERE persona_id = ?
		AND EXISTS (SELECT 1 FROM personas p WHERE p.id = persona_id AND p.namespace = ?)
		ORDER BY weight DESC, updated_at DESC, id LIMIT ? OFFSET ?
	`, personaID, namespace, limit, offset)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		value, scanErr := scanNativePersonaSample(rows)
		if scanErr != nil {
			return result, scanErr
		}
		result.Items = append(result.Items, value)
	}
	return result, rows.Err()
}

func personaSamplePayloadValues(payload corePersonaSamplePayload, current nativePersonaSample) (nativePersonaSample, error) {
	var err error
	if current.SceneTags == nil {
		current.SceneTags = []string{}
	}
	if current.CandidateReplies == nil {
		current.CandidateReplies = []string{}
	}
	if current.ForbiddenExpressions == nil {
		current.ForbiddenExpressions = []string{}
	}
	current.SceneTags, err = coreStringListValue(payload.SceneTags, current.SceneTags, "sceneTags", 32, 80)
	if err != nil {
		return current, err
	}
	current.CandidateReplies, err = coreStringListValue(payload.CandidateReplies, current.CandidateReplies, "candidateReplies", 16, 500)
	if err != nil {
		return current, err
	}
	if len(current.CandidateReplies) == 0 {
		return current, coreInvalid("candidateReplies requires at least one reply")
	}
	current.ForbiddenExpressions, err = coreStringListValue(payload.ForbiddenExpressions, current.ForbiddenExpressions, "forbiddenExpressions", 32, 200)
	if err != nil {
		return current, err
	}
	for _, field := range []struct {
		input    *string
		current  *string
		name     string
		maximum  int
		required bool
	}{
		{payload.RelationshipStage, &current.RelationshipStage, "relationshipStage", 80, false},
		{payload.Emotion, &current.Emotion, "emotion", 80, false},
		{payload.Context, &current.Context, "context", 4000, true},
		{payload.Source, &current.Source, "source", 1000, true},
	} {
		*field.current, err = coreStringValue(field.input, *field.current, field.name, field.maximum, field.required)
		if err != nil {
			return current, err
		}
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

func (s *coreConfigStore) createPersonaSample(namespace, personaID string, payload corePersonaSamplePayload) (nativePersonaSample, error) {
	id := ""
	var err error
	if payload.ID != nil {
		id, err = normalizeCoreText(*payload.ID, "id", 200, true)
	} else {
		id, err = newCoreUUID()
	}
	if err != nil {
		return nativePersonaSample{}, err
	}
	var exists int
	if err = s.db.QueryRow("SELECT count(*) FROM persona_samples WHERE id = ?", id).Scan(&exists); err != nil {
		return nativePersonaSample{}, err
	}
	if exists != 0 {
		return nativePersonaSample{}, coreInvalid("persona sample id already exists")
	}
	value, err := personaSamplePayloadValues(payload, nativePersonaSample{
		ID: id, PersonaID: personaID, Weight: 1, Enabled: true,
	})
	if err != nil {
		return nativePersonaSample{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = s.db.Exec(`
		INSERT INTO persona_samples (
			id, persona_id, scene_tags_json, relationship_stage, emotion, context,
			candidate_replies_json, forbidden_expressions_json, source, weight, enabled,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, value.ID, personaID, mgmtJSON(value.SceneTags), value.RelationshipStage, value.Emotion,
		value.Context, mgmtJSON(value.CandidateReplies), mgmtJSON(value.ForbiddenExpressions),
		value.Source, value.Weight, boolInt(value.Enabled), now, now)
	if err != nil {
		return nativePersonaSample{}, err
	}
	value, _, err = s.personaSample(namespace, personaID, id)
	return value, err
}

func (s *coreConfigStore) updatePersonaSample(namespace, personaID, id string, payload corePersonaSamplePayload) (nativePersonaSample, bool, error) {
	current, found, err := s.personaSample(namespace, personaID, id)
	if err != nil || !found {
		return nativePersonaSample{}, found, err
	}
	value, err := personaSamplePayloadValues(payload, current)
	if err != nil {
		return nativePersonaSample{}, true, err
	}
	_, err = s.db.Exec(`
		UPDATE persona_samples SET scene_tags_json = ?, relationship_stage = ?, emotion = ?,
			context = ?, candidate_replies_json = ?, forbidden_expressions_json = ?, source = ?,
			weight = ?, enabled = ?, updated_at = ? WHERE persona_id = ? AND id = ?
	`, mgmtJSON(value.SceneTags), value.RelationshipStage, value.Emotion, value.Context,
		mgmtJSON(value.CandidateReplies), mgmtJSON(value.ForbiddenExpressions), value.Source,
		value.Weight, boolInt(value.Enabled), time.Now().UTC().Format(time.RFC3339Nano), personaID, id)
	if err != nil {
		return nativePersonaSample{}, true, err
	}
	value, _, err = s.personaSample(namespace, personaID, id)
	return value, true, err
}

func (s *coreConfigStore) deletePersonaSample(namespace, personaID, id string) (bool, error) {
	if _, found, err := s.personaSample(namespace, personaID, id); err != nil || !found {
		return false, err
	}
	result, err := s.db.Exec("DELETE FROM persona_samples WHERE persona_id = ? AND id = ?", personaID, id)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *coreConfigStore) handlePersonaSamples(w http.ResponseWriter, r *http.Request, namespace, personaID string, rest []string) error {
	if len(rest) == 0 {
		switch r.Method {
		case http.MethodGet:
			limit, offset, err := parseCorePage(r.URL.Query())
			if err != nil {
				return err
			}
			data, err := s.listPersonaSamples(namespace, personaID, limit, offset)
			if err == nil {
				writeJSON(w, http.StatusOK, map[string]any{"data": data})
			}
			return err
		case http.MethodPost:
			var payload corePersonaSamplePayload
			if _, err := decodeCoreObject(r, corePersonaSampleCreateFields, "persona sample", &payload); err != nil {
				return err
			}
			data, err := s.createPersonaSample(namespace, personaID, payload)
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
		data, found, err := s.personaSample(namespace, personaID, id)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("persona sample")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return nil
	case http.MethodPut:
		var payload corePersonaSamplePayload
		if _, err = decodeCoreObject(r, corePersonaSampleUpdateFields, "persona sample", &payload); err != nil {
			return err
		}
		data, found, err := s.updatePersonaSample(namespace, personaID, id, payload)
		if err != nil {
			return err
		}
		if !found {
			return mgmtNotFound("persona sample")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": data})
		return nil
	case http.MethodDelete:
		deleted, err := s.deletePersonaSample(namespace, personaID, id)
		if err != nil {
			return err
		}
		if !deleted {
			return mgmtNotFound("persona sample")
		}
		writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"id": id, "deleted": true}})
		return nil
	default:
		return mgmtMethodNotAllowed()
	}
}

type personaSampleQuery struct {
	Message           string
	RecentMessages    []string
	RelationshipStage string
	Emotion           string
}

func personaSampleMatchScore(sample nativePersonaSample, query personaSampleQuery) (float64, bool) {
	message := strings.ToLower(strings.TrimSpace(query.Message))
	matched := false
	score := sample.Weight
	for _, tag := range sample.SceneTags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "*" {
			matched = true
			score += 0.1
		} else if tag != "" && strings.Contains(message, tag) {
			matched = true
			score += 4
		} else if tag != "" && recentMessageContains(query.RecentMessages, tag) {
			matched = true
			score += 1.25
		}
	}
	if personaContextMatches(sample.RelationshipStage, query.RelationshipStage) {
		score += 2.5
	}
	if personaContextMatches(sample.Emotion, query.Emotion) {
		score += 2
	}
	return score, matched
}

func (s *coreConfigStore) selectPersonaSamples(personaID, message string, limit int) ([]nativePersonaSample, error) {
	return s.selectContextualPersonaSamples(personaID, personaSampleQuery{Message: message}, limit)
}

func (s *coreConfigStore) selectContextualPersonaSamples(
	personaID string,
	query personaSampleQuery,
	limit int,
) ([]nativePersonaSample, error) {
	if personaID == "" || limit <= 0 {
		return []nativePersonaSample{}, nil
	}
	rows, err := s.db.Query(`
		SELECT `+nativePersonaSampleColumns+` FROM persona_samples
		WHERE persona_id = ? AND enabled = 1 ORDER BY weight DESC, id
	`, personaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type scoredSample struct {
		value nativePersonaSample
		score float64
	}
	scored := []scoredSample{}
	for rows.Next() {
		value, scanErr := scanNativePersonaSample(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		if score, matched := personaSampleMatchScore(value, query); matched {
			scored = append(scored, scoredSample{value: value, score: score})
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
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
	values := make([]nativePersonaSample, 0, len(scored))
	for _, item := range scored {
		values = append(values, item.value)
	}
	return values, nil
}

func recentMessageContains(messages []string, tag string) bool {
	for _, message := range messages {
		if strings.Contains(strings.ToLower(message), tag) {
			return true
		}
	}
	return false
}

func personaContextMatches(sampleValue, currentValue string) bool {
	sampleValue = strings.ToLower(strings.TrimSpace(sampleValue))
	currentValue = strings.ToLower(strings.TrimSpace(currentValue))
	if sampleValue == "" || currentValue == "" {
		return false
	}
	return strings.Contains(sampleValue, currentValue) || strings.Contains(currentValue, sampleValue)
}

func compilePersonaSamples(samples []nativePersonaSample) string {
	if len(samples) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("场景化表达参考：只学习判断、节奏和长度，禁止逐字复制；每次根据当前上下文重新组织，样本不能覆盖安全规则、管理员命令和事实要求。")
	for _, sample := range samples {
		builder.WriteString("\n- 场景：")
		builder.WriteString(strings.Join(sample.SceneTags, "、"))
		if sample.RelationshipStage != "" {
			builder.WriteString("；关系：")
			builder.WriteString(sample.RelationshipStage)
		}
		if sample.Emotion != "" {
			builder.WriteString("；情绪：")
			builder.WriteString(sample.Emotion)
		}
		builder.WriteString("；上下文：")
		builder.WriteString(sample.Context)
		builder.WriteString("；可参考：")
		builder.WriteString(strings.Join(sample.CandidateReplies, " / "))
		if len(sample.ForbiddenExpressions) > 0 {
			builder.WriteString("；避免：")
			builder.WriteString(strings.Join(sample.ForbiddenExpressions, "、"))
		}
	}
	return builder.String()
}
