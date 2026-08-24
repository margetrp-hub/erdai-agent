package main

import (
	"context"
	"strings"
)

const personaRuntimeScenePrefix = "runtime:"

func (a *AgentRuntime) personaFixedReply(
	ctx context.Context,
	run runRecord,
	scene string,
	fallback []string,
) string {
	candidates := []string{}
	tag := personaRuntimeScenePrefix + strings.TrimSpace(scene)
	if a != nil && a.configStore != nil && strings.TrimSpace(run.PersonaID) != "" {
		if samples, err := a.configStore.selectPersonaSamples(run.PersonaID, tag, 32); err == nil {
			for _, sample := range samples {
				if personaSampleHasExactTag(sample, tag) {
					candidates = append(candidates, sample.CandidateReplies...)
				}
			}
		}
	}
	if len(cleanReplyCandidates(candidates)) == 0 {
		candidates = fallback
	}
	recent := []string(nil)
	if a != nil {
		recent = a.recentAssistantReplyTexts(
			ctx, runtimeScopeFromRun(run).memoryConversationRef(), run.PersonaID, 12,
		)
	}
	return novelRandomMessage(candidates, recent)
}

func personaSampleHasExactTag(sample nativePersonaSample, expected string) bool {
	for _, tag := range sample.SceneTags {
		if strings.EqualFold(strings.TrimSpace(tag), expected) {
			return true
		}
	}
	return false
}

func novelRandomMessage(candidates, recent []string) string {
	filtered := cleanReplyCandidates(candidates)
	if len(filtered) == 0 {
		return ""
	}
	available := make([]string, 0, len(filtered))
	oldestMatches := []string{}
	oldestIndex := -1
	for _, candidate := range filtered {
		matchIndex := -1
		for index, previous := range recent {
			if nearDuplicateReply(candidate, previous) {
				matchIndex = index
				break
			}
		}
		if matchIndex < 0 {
			available = append(available, candidate)
			continue
		}
		if matchIndex > oldestIndex {
			oldestIndex = matchIndex
			oldestMatches = []string{candidate}
		} else if matchIndex == oldestIndex {
			oldestMatches = append(oldestMatches, candidate)
		}
	}
	if len(available) > 0 {
		return randomMessage(available)
	}
	return randomMessage(oldestMatches)
}

func cleanReplyCandidates(candidates []string) []string {
	result := make([]string, 0, len(candidates))
	seen := map[string]bool{}
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		key := normalizeReplyForComparison(candidate)
		if candidate == "" || key == "" || seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, candidate)
	}
	return result
}
