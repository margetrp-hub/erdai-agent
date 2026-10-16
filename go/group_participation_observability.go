package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"time"
)

const groupParticipationDecisionAudit = "group_participation.decision"

type groupParticipationDecisionObservation struct {
	EndpointID string `json:"endpointId"`
	DurationMS int64  `json:"durationMs"`
	Result     string `json:"result"`
	ErrorCode  string `json:"errorCode"`
}

type groupParticipationObservability struct {
	Attempts       int            `json:"attempts"`
	Replies        int            `json:"replies"`
	Ignored        int            `json:"ignored"`
	Errors         int            `json:"errors"`
	AvgDurationMS  float64        `json:"avgDurationMs"`
	FailuresByCode map[string]int `json:"failuresByCode"`
	LastAttemptAt  string         `json:"lastAttemptAt,omitempty"`
}

func normalizeGroupParticipationErrorCode(code string) string {
	switch code {
	case "route", "connection", "credential", "timeout", "cancelled", "provider_http", "invalid_response", "context":
		return code
	default:
		return "context"
	}
}

func groupParticipationErrorCode(err error, fallback string) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return "timeout"
	}
	var httpError *providerHTTPError
	if errors.As(err, &httpError) {
		return "provider_http"
	}
	var syntaxError *json.SyntaxError
	var typeError *json.UnmarshalTypeError
	if fallback == "connection" && (errors.As(err, &syntaxError) || errors.As(err, &typeError) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
		return "invalid_response"
	}
	return normalizeGroupParticipationErrorCode(fallback)
}

func (a *AgentRuntime) recordGroupParticipationDecision(ctx context.Context, endpointID string, started time.Time, allowed bool, errorCode string) {
	if a == nil || a.configStore == nil || a.configStore.db == nil {
		return
	}
	endpointID = strings.TrimSpace(endpointID)
	if len(endpointID) > 120 || !mgmtIdentifierPattern.MatchString(endpointID) {
		endpointID = ""
	}
	result := "ignore"
	if allowed {
		result = "reply"
	}
	if errorCode != "" {
		result = "error"
		errorCode = normalizeGroupParticipationErrorCode(errorCode)
	}
	observation := groupParticipationDecisionObservation{
		EndpointID: endpointID,
		DurationMS: max(0, time.Since(started).Milliseconds()),
		Result:     result, ErrorCode: errorCode,
	}
	// This deliberately has no error text, message, prompt, response, sender,
	// conversation, credential, or URL. Cancellation must still be observable,
	// but recording telemetry must never turn a declined gate into a reply.
	auditContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_, _ = a.configStore.db.ExecContext(auditContext, `INSERT INTO audit_events
		(actor, action, target_type, target_id, details_json, created_at)
		VALUES ('runtime', ?, 'group_participation', NULL, ?, ?)`,
		groupParticipationDecisionAudit, mgmtJSON(observation), mgmtNow())
}

func (a *AgentRuntime) groupParticipationObservability(ctx context.Context) (groupParticipationObservability, error) {
	value := groupParticipationObservability{FailuresByCode: map[string]int{}}
	now := time.Now().UTC()
	rows, err := a.configStore.db.QueryContext(ctx, `WITH decisions AS (
		SELECT json_extract(details_json, '$.result') AS result,
			COALESCE(json_extract(details_json, '$.errorCode'), '') AS error_code,
			CASE WHEN json_type(details_json, '$.durationMs') IN ('integer', 'real')
				THEN max(0, json_extract(details_json, '$.durationMs')) ELSE 0 END AS duration_ms,
			created_at
		FROM audit_events
		WHERE action = ? AND created_at >= ? AND created_at <= ? AND json_valid(details_json)
	)
	SELECT result, error_code, count(*), sum(duration_ms), max(created_at)
	FROM decisions WHERE result IN ('reply', 'ignore', 'error') GROUP BY result, error_code`,
		groupParticipationDecisionAudit, now.Add(-24*time.Hour).Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		return value, err
	}
	defer rows.Close()
	var durationMS float64
	for rows.Next() {
		var result, code, lastAttempt string
		var count int
		var totalDuration float64
		if err := rows.Scan(&result, &code, &count, &totalDuration, &lastAttempt); err != nil {
			return value, err
		}
		value.Attempts += count
		durationMS += totalDuration
		switch result {
		case "reply":
			value.Replies += count
		case "ignore":
			value.Ignored += count
		case "error":
			value.Errors += count
			value.FailuresByCode[normalizeGroupParticipationErrorCode(code)] += count
		}
		if lastAttempt > value.LastAttemptAt {
			value.LastAttemptAt = lastAttempt
		}
	}
	if err := rows.Err(); err != nil {
		return value, err
	}
	if value.Attempts > 0 {
		value.AvgDurationMS = durationMS / float64(value.Attempts)
	}
	return value, nil
}
