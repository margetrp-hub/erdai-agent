package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// A replay must retain its execution authority and identity, not a transient
// prompt's mood, clock, recent conversation, or newly retrieved tool results.
func (a *AgentRuntime) taskModelContract(ctx context.Context, run runRecord, message, systemPrompt string,
	policy runtimeToolPolicy, tools []map[string]any, mcpRoutes map[string]mcpBridgeRoute,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	contract := map[string]any{
		"version": 1, "runId": run.ID, "scope": runtimeScopeFromRun(run),
		"personaId": run.PersonaID, "conversationKind": run.ConversationKind,
		"message": message, "replyToMessageId": run.ReplyToMessageID,
		"attachments": run.Attachments, "isAdmin": run.IsAdmin,
		"policy": policy, "tools": tools, "mcpRoutes": mcpRoutes,
	}
	if a.configStore == nil {
		contract["systemPrompt"] = systemPrompt
		return taskModelDigest(contract)
	}
	store := a.configStore
	config, err := store.runtimeConfig()
	if err != nil {
		return "", err
	}
	personaID := strings.TrimSpace(run.PersonaID)
	if personaID == "" && config.ActivePersonaID != nil {
		personaID = strings.TrimSpace(*config.ActivePersonaID)
	}
	contract["resolvedPersonaId"] = personaID
	contract["runtimeConfig"] = map[string]any{
		"personaInjectionEnabled":   config.PersonaInjectionEnabled,
		"knowledgeInjectionEnabled": config.KnowledgeInjectionEnabled,
		"worldbookInjectionEnabled": config.WorldbookInjectionEnabled,
		"protectedRules":            config.ProtectedRules, "replyStyle": config.ReplyStyle,
		"maxReplySentences": config.MaxReplySentences, "maxReplyChars": config.MaxReplyChars,
		"avoidRepetitiveOpeners": config.AvoidRepetitiveOpeners,
		"knowledgeNamespace":     config.KnowledgeNamespace,
	}
	boundary, err := store.contentBoundaryPolicy()
	if err != nil {
		return "", err
	}
	contract["contentBoundary"] = boundary
	directives, err := store.enabledAdminDirectives()
	if err != nil {
		return "", err
	}
	contract["adminDirectives"] = directives
	persona, worldbook, err := store.personaAndWorldbook(config, &personaID, message)
	if err != nil {
		return "", err
	}
	if persona != nil {
		// Avatar bytes are not the appearance library and do not enter prompts.
		persona.AvatarDataURI = ""
	}
	contract["persona"], contract["worldbook"] = persona, worldbook
	profile, err := store.effectivePersonaRuntimeProfile(personaID, run.AgentInstanceID)
	if err != nil {
		return "", err
	}
	contract["runtimeProfile"] = profile
	if instanceID := strings.TrimSpace(run.AgentInstanceID); instanceID != "" && instanceID != legacyAgentInstanceID {
		// The runtime profile helper permits a missing template, but a failed
		// template query must not silently produce a compatible checkpoint.
		var templateID, templateConfig, overrides string
		var enabled int
		err = store.db.QueryRowContext(ctx, `SELECT COALESCE(i.policy_template_id,''),
			COALESCE(t.config_json,''), COALESCE(t.enabled,0), i.overrides_json
			FROM agent_instances i LEFT JOIN agent_policy_templates t ON t.id=i.policy_template_id
			WHERE i.id=?`, instanceID).Scan(&templateID, &templateConfig, &enabled, &overrides)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
		contract["instancePolicy"] = []any{templateID, templateConfig, enabled, overrides}
	}
	var libraryID, libraryNamespace, libraryRevision, bindingRevision, sourcePersonaID string
	var visualDescription, outfitLength string
	var libraryEnabled int
	err = store.db.QueryRowContext(ctx, `SELECT l.id,l.namespace,l.updated_at,pal.updated_at,
		COALESCE(l.source_persona_id,''),l.visual_description,l.outfit_length,l.enabled
		FROM persona_appearance_libraries pal JOIN appearance_libraries l ON l.id=pal.library_id
		WHERE pal.persona_id=?`, personaID).Scan(&libraryID, &libraryNamespace, &libraryRevision,
		&bindingRevision, &sourcePersonaID, &visualDescription, &outfitLength, &libraryEnabled)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	if err == nil {
		owned, err := store.listOwnedAppearanceLibraryReferences(libraryID)
		if err != nil {
			return "", err
		}
		var source []appearanceLibraryReference
		if sourcePersonaID != "" {
			source, err = store.listSourceAppearanceLibraryReferences(libraryID, sourcePersonaID, false)
			if err != nil {
				return "", err
			}
		}
		contract["appearance"] = map[string]any{
			"id": libraryID, "namespace": libraryNamespace, "revision": libraryRevision,
			"bindingRevision": bindingRevision, "sourcePersonaId": sourcePersonaID,
			"visualDescription": visualDescription, "outfitLength": outfitLength,
			"enabled": libraryEnabled, "ownedReferences": owned, "sourceReferences": source,
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return taskModelDigest(contract)
}
