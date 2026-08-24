package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type mediaProviderCandidate struct {
	Connection providerConnectionConfig
	Model      string
}

func (a *AgentRuntime) mediaProviderCandidates(ctx context.Context, preferredIDs []string, capability string) ([]mediaProviderCandidate, error) {
	if a == nil || a.configStore == nil {
		return nil, errors.New("media provider is unavailable")
	}
	capability = strings.TrimSpace(capability)
	if capability == "" {
		return nil, errors.New("media capability is required")
	}
	seen := map[string]bool{}
	values := make([]mediaProviderCandidate, 0)
	add := func(connectionID string) error {
		connectionID = strings.TrimSpace(connectionID)
		if connectionID == "" || seen[connectionID] {
			return nil
		}
		var value mediaProviderCandidate
		err := a.configStore.db.QueryRowContext(ctx, `
			SELECT c.id, c.provider, c.protocol, c.api_base, c.credential_ref, c.timeout_seconds,
				e.model
			FROM provider_connections c
		JOIN model_endpoint_connections b ON b.connection_id = c.id
		JOIN model_endpoints e ON e.id = b.endpoint_id
		LEFT JOIN model_health h ON h.endpoint_id = e.id
		WHERE c.id = ? AND c.enabled = 1 AND e.enabled = 1
		  AND (h.endpoint_id IS NULL OR h.healthy = 1)
			  AND instr(e.capabilities_json, ?) > 0
			ORDER BY e.priority DESC, e.id LIMIT 1`, connectionID, `"`+capability+`"`).Scan(
			&value.Connection.ID, &value.Connection.Provider, &value.Connection.Protocol,
			&value.Connection.APIBase, &value.Connection.CredentialRef,
			&value.Connection.TimeoutSeconds, &value.Model)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		seen[connectionID] = true
		values = append(values, value)
		return nil
	}
	for _, id := range preferredIDs {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	rows, err := a.configStore.db.QueryContext(ctx, `
		SELECT c.id
		FROM provider_connections c
		JOIN model_endpoint_connections b ON b.connection_id = c.id
		JOIN model_endpoints e ON e.id = b.endpoint_id
		LEFT JOIN model_health h ON h.endpoint_id = e.id
		WHERE c.enabled = 1 AND e.enabled = 1 AND instr(e.capabilities_json, ?) > 0
		  AND (h.endpoint_id IS NULL OR h.healthy = 1)
		ORDER BY e.priority DESC, c.id`, `"`+capability+`"`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		if err := add(id); err != nil {
			return nil, err
		}
	}
	return values, rows.Err()
}
