package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

type instanceModelLayer struct {
	Source     string `json:"source"`
	Value      string `json:"value"`
	Enabled    bool   `json:"enabled"`
	Overridden bool   `json:"overridden"`
}

type instanceModelEndpoint struct {
	ID       string `json:"id"`
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Enabled  bool   `json:"enabled"`
}

type instanceEffectiveModel struct {
	Lane       string                 `json:"lane"`
	Preference string                 `json:"preference"`
	Source     string                 `json:"source"`
	Mode       string                 `json:"mode"`
	Endpoint   *instanceModelEndpoint `json:"endpoint"`
	Layers     []instanceModelLayer   `json:"layers"`
}

type instanceEffectiveModels struct {
	InstanceID       string                   `json:"instanceId"`
	PersonaID        string                   `json:"personaId"`
	PolicyTemplateID string                   `json:"policyTemplateId"`
	Enabled          bool                     `json:"enabled"`
	AutomaticRouting bool                     `json:"automaticRouting"`
	Models           []instanceEffectiveModel `json:"models"`
}

func (s *coreConfigStore) handleAgentInstanceEffectiveModels(w http.ResponseWriter, r *http.Request, id string) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	id, err := parseCorePathID(id)
	if err != nil {
		return err
	}
	instance, found, err := s.agentInstance(id)
	if err != nil {
		return err
	}
	if !found {
		return mgmtNotFound("agent instance")
	}
	value, err := s.effectiveInstanceModels(instance)
	if err != nil {
		return err
	}
	mgmtWriteData(w, http.StatusOK, value)
	return nil
}

// These are the effective configured preferences, not a prediction of a future
// request's selected endpoint. Health, input capabilities and fallbacks still
// participate in per-request routing.
func (s *coreConfigStore) effectiveInstanceModels(instance agentInstance) (instanceEffectiveModels, error) {
	value := instanceEffectiveModels{
		InstanceID: instance.ID, PersonaID: instance.PersonaID, PolicyTemplateID: instance.PolicyTemplateID,
		Enabled: instance.Enabled, AutomaticRouting: true, Models: []instanceEffectiveModel{},
	}
	effective, err := s.effectivePersonaRuntimeProfile(instance.PersonaID, instance.ID)
	if err != nil {
		return value, err
	}
	persona, err := s.personaRuntimeProfile(instance.PersonaID)
	if err != nil {
		return value, err
	}
	var template, overrides personaRuntimeProfile
	templateEnabled := false
	if instance.PolicyTemplateID != "" {
		stored, found, err := s.agentPolicyTemplate(instance.PolicyTemplateID)
		if err != nil {
			return value, err
		}
		if found {
			templateEnabled = stored.Enabled && instance.ID != legacyAgentInstanceID
			if err = json.Unmarshal(stored.Config, &template); err != nil {
				return value, err
			}
		}
	}
	if err = json.Unmarshal(instance.Overrides, &overrides); err != nil {
		return value, err
	}
	companion := runtimeCompanionContextPolicy{EnableModelRouting: true}
	if raw, err := s.integrationRaw("companion_policy"); err == nil {
		_ = json.Unmarshal(raw, &companion)
	}
	var group groupParticipationPolicy
	if raw, err := s.integrationRaw("group_chat_policy"); err == nil {
		_ = json.Unmarshal(raw, &group)
	}
	value.AutomaticRouting = companion.EnableModelRouting
	public := map[string]string{"chat": companion.ChatModel, "task": companion.TaskModel, "decision": group.DecisionProviderID}
	for _, lane := range []string{"chat", "task", "decision"} {
		item := instanceEffectiveModel{Lane: lane, Source: "public", Mode: "configured_preference", Layers: []instanceModelLayer{
			{Source: "public", Value: strings.TrimSpace(public[lane]), Enabled: true},
			{Source: "persona", Value: personaRuntimeEndpoint(persona, lane), Enabled: true},
			{Source: "template", Value: personaRuntimeEndpoint(template, lane), Enabled: templateEnabled},
			{Source: "instance", Value: personaRuntimeEndpoint(overrides, lane), Enabled: instance.ID != legacyAgentInstanceID},
		}}
		item.Preference = personaRuntimeEndpoint(effective, lane)
		if item.Preference == "" {
			item.Preference = strings.TrimSpace(public[lane])
		} else {
			for _, layer := range item.Layers[1:] {
				if layer.Enabled && layer.Value != "" {
					item.Source = layer.Source
				}
			}
		}
		for index := range item.Layers {
			layer := &item.Layers[index]
			layer.Overridden = layer.Enabled && layer.Value != "" && layer.Source != item.Source
		}
		if item.Preference == "" {
			item.Mode = "public_default"
			if lane == "decision" {
				item.Mode = "heuristic"
			}
		} else {
			item.Endpoint, err = s.instancePreferenceEndpoint(lane, item.Preference)
			if err != nil {
				return value, err
			}
		}
		value.Models = append(value.Models, item)
	}
	return value, nil
}

func (s *coreConfigStore) instancePreferenceEndpoint(lane, preference string) (*instanceModelEndpoint, error) {
	var endpoint instanceModelEndpoint
	// Chat/task preferences accept IDs or model names, exactly as
	// preferNativeRouteModel does. Group decisions require an exact endpoint ID.
	// A disabled match remains visible as configuration, never as a usable route.
	err := s.db.QueryRow(`SELECT id, model, provider, enabled FROM model_endpoints
		WHERE id = ? OR (? != 'decision' AND model = ?)
		ORDER BY enabled DESC, CASE WHEN id = ? THEN 0 ELSE 1 END, priority DESC, id LIMIT 1`,
		preference, lane, preference, preference).Scan(&endpoint.ID, &endpoint.Model, &endpoint.Provider, &endpoint.Enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &endpoint, nil
}
