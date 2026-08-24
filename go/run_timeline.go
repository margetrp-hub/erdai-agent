package main

import (
	"database/sql"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type runTimelineEvent struct {
	ID          int64  `json:"id"`
	RunID       string `json:"runId"`
	Stage       string `json:"stage"`
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
	DurationMS  int64  `json:"durationMs"`
	DetailsJSON string `json:"detailsJson"`
}

func (a *AgentRuntime) handleRunTimeline(w http.ResponseWriter, r *http.Request, path string) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	if path == "/api/v1/runs/stats" {
		return a.handleRunStats(w, r)
	}
	if path == "/api/v1/runs" {
		rows, err := a.db.Query(`SELECT id, event_id, agent_instance_id, transport, transport_instance, conversation_ref, persona_id, state,
			error_code, selected_endpoint_id, selected_model, route_reason, provider_calls,
			total_duration_ms, created_at, updated_at FROM agent_runs ORDER BY created_at DESC LIMIT 100`)
		if err != nil {
			return err
		}
		defer rows.Close()
		values := []map[string]any{}
		for rows.Next() {
			var id, eventID, agentInstanceID, transport, transportInstance, conversation, persona, state, endpointID, model, routeReason, created, updated string
			var providerCalls, totalDurationMS int
			var errorCode sql.NullString
			if err := rows.Scan(&id, &eventID, &agentInstanceID, &transport, &transportInstance, &conversation, &persona, &state, &errorCode,
				&endpointID, &model, &routeReason, &providerCalls, &totalDurationMS, &created, &updated); err != nil {
				return err
			}
			values = append(values, map[string]any{
				"id": id, "eventId": eventID, "agentInstanceId": agentInstanceID,
				"transport": transport, "transportInstance": transportInstance, "conversationRef": conversation,
				"personaId": persona, "state": state, "errorCode": errorCode.String,
				"selectedEndpointId": endpointID, "selectedModel": model, "routeReason": routeReason,
				"providerCalls": providerCalls, "totalDurationMs": totalDurationMS,
				"createdAt": created, "updatedAt": updated,
			})
		}
		mgmtWriteData(w, http.StatusOK, values)
		return rows.Err()
	}
	id, err := mgmtPathID(path, "/api/v1/runs/")
	if err != nil {
		return err
	}
	rows, err := a.db.Query(`SELECT id, run_id, stage, started_at, completed_at, duration_ms, details_json
		FROM run_stage_events WHERE run_id = ? ORDER BY id`, id)
	if err != nil {
		return err
	}
	defer rows.Close()
	values := []runTimelineEvent{}
	for rows.Next() {
		var value runTimelineEvent
		if err := rows.Scan(&value.ID, &value.RunID, &value.Stage, &value.StartedAt, &value.CompletedAt, &value.DurationMS, &value.DetailsJSON); err != nil {
			return err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(values) == 0 {
		var exists int
		if err := a.db.QueryRow("SELECT 1 FROM agent_runs WHERE id = ?", id).Scan(&exists); err != nil {
			if err == sql.ErrNoRows {
				return mgmtNotFound("run")
			}
			return err
		}
	}
	mgmtWriteData(w, http.StatusOK, values)
	return nil
}

// handleRunStats aggregates run outcomes, failure classes, participation
// dispositions, and first-response latency over a recent window. It answers
// the questions the raw run list cannot: how often replies are sampled out,
// merged, superseded, or failing, and how fast the first visible response is.
func (a *AgentRuntime) handleRunStats(w http.ResponseWriter, r *http.Request) error {
	hours := 48
	if raw := strings.TrimSpace(r.URL.Query().Get("hours")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 336 {
			return coreInvalid("hours must be an integer between 1 and 336")
		}
		hours = parsed
	}
	since := time.Now().UTC().Add(-time.Duration(hours) * time.Hour).Format(time.RFC3339Nano)
	countGroups := func(query string) (map[string]int, error) {
		rows, err := a.db.Query(query, since)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		values := map[string]int{}
		for rows.Next() {
			var key string
			var count int
			if err := rows.Scan(&key, &count); err != nil {
				return nil, err
			}
			if strings.TrimSpace(key) == "" {
				key = "(none)"
			}
			values[key] = count
		}
		return values, rows.Err()
	}
	states, err := countGroups(`SELECT state, count(*) FROM agent_runs
		WHERE created_at >= ? GROUP BY state`)
	if err != nil {
		return err
	}
	errorCodes, err := countGroups(`SELECT error_code, count(*) FROM agent_runs
		WHERE created_at >= ? AND error_code IS NOT NULL AND error_code <> '' GROUP BY error_code`)
	if err != nil {
		return err
	}
	failureClasses, err := countGroups(`SELECT failure_class, count(*) FROM agent_runs
		WHERE created_at >= ? AND failure_class <> '' GROUP BY failure_class`)
	if err != nil {
		return err
	}
	dispositions, err := countGroups(`SELECT disposition || ':' || reason, count(*) FROM agent_transport_events
		WHERE created_at >= ? GROUP BY disposition, reason`)
	if err != nil {
		return err
	}
	latency := map[string]any{"samples": 0}
	var samples, over10s int
	if err := a.db.QueryRow(`SELECT count(*), count(CASE WHEN first_response_ms > 10000 THEN 1 END)
		FROM agent_runs WHERE created_at >= ? AND first_response_ms > 0`, since).Scan(&samples, &over10s); err != nil {
		return err
	}
	if samples > 0 {
		percentile := func(fraction float64) (int64, error) {
			offset := int(float64(samples-1) * fraction)
			var value int64
			err := a.db.QueryRow(`SELECT first_response_ms FROM agent_runs
				WHERE created_at >= ? AND first_response_ms > 0
				ORDER BY first_response_ms LIMIT 1 OFFSET `+strconv.Itoa(offset), since).Scan(&value)
			return value, err
		}
		p50, err := percentile(0.50)
		if err != nil {
			return err
		}
		p95, err := percentile(0.95)
		if err != nil {
			return err
		}
		var maximum int64
		if err := a.db.QueryRow(`SELECT max(first_response_ms) FROM agent_runs
			WHERE created_at >= ? AND first_response_ms > 0`, since).Scan(&maximum); err != nil {
			return err
		}
		latency = map[string]any{
			"samples": samples, "p50Ms": p50, "p95Ms": p95, "maxMs": maximum, "over10s": over10s,
		}
	}
	mgmtWriteData(w, http.StatusOK, map[string]any{
		"windowHours":    hours,
		"runStates":      states,
		"errorCodes":     errorCodes,
		"failureClasses": failureClasses,
		"dispositions":   dispositions,
		"firstResponse":  latency,
	})
	return nil
}
