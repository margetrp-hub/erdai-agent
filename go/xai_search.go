package main

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// xaiResponsesResponse is intentionally tolerant of the Responses API output
// shape. Gateways have emitted both message-level and content-level text blocks.
type xaiResponsesResponse struct {
	Output []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Content []struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			Annotations []struct {
				Type  string `json:"type"`
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"annotations"`
		} `json:"content"`
	} `json:"output"`
	Usage         chatUsage      `json:"usage"`
	StreamText    string         `json:"-"`
	StreamSources []searchSource `json:"-"`
}

type xaiSearchConnection struct {
	providerConnectionConfig
	Model string
}

const (
	nativeSearchAttemptLimit  = 12 * time.Second
	searchSummaryAttemptLimit = 10 * time.Second
	searchTaskLimit           = 30 * time.Second
)

func nativeSearchAttemptTimeout(connection xaiSearchConnection, remaining time.Duration) time.Duration {
	// The in-network Grok adapter is useful when healthy but should fail fast;
	// the paid HTTPS connection gets the larger share of the run budget.
	limit := nativeSearchAttemptLimit
	base := strings.ToLower(strings.TrimSpace(connection.APIBase))
	if strings.HasPrefix(base, "http://") || strings.Contains(strings.ToLower(connection.ID), "local") {
		limit = 4 * time.Second
	} else if limit > 10*time.Second {
		limit = 10 * time.Second
	}
	if remaining > 0 && remaining < limit {
		limit = remaining
	}
	if limit < time.Second {
		return time.Second
	}
	return limit
}

func (a *AgentRuntime) xaiSearchConnection(ctx context.Context, searchConnectionID, policyAPIBase, policyCredentialRef, searchModel string) (xaiSearchConnection, error) {
	if a == nil || a.configStore == nil {
		return xaiSearchConnection{}, errors.New("search provider is unavailable")
	}
	var value xaiSearchConnection
	var err error
	if id := strings.TrimSpace(searchConnectionID); id != "" {
		err = a.configStore.db.QueryRowContext(ctx, `SELECT id, provider, protocol, api_base, credential_ref, timeout_seconds
			FROM provider_connections WHERE id = ? AND enabled = 1`, id).
			Scan(&value.ID, &value.Provider, &value.Protocol, &value.APIBase, &value.CredentialRef, &value.TimeoutSeconds)
	} else {
		err = a.configStore.db.QueryRowContext(ctx, `SELECT id, provider, protocol, api_base, credential_ref, timeout_seconds
			FROM provider_connections WHERE protocol = 'xai_responses' AND enabled = 1 ORDER BY id LIMIT 1`).
			Scan(&value.ID, &value.Provider, &value.Protocol, &value.APIBase, &value.CredentialRef, &value.TimeoutSeconds)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return xaiSearchConnection{}, errors.New("no enabled xai_responses provider connection")
	}
	if err != nil {
		return xaiSearchConnection{}, err
	}
	if value.Protocol != "xai_responses" {
		return xaiSearchConnection{}, errors.New("search provider connection must use xai_responses")
	}
	if strings.TrimSpace(value.CredentialRef) == "" {
		value.CredentialRef = policyCredentialRef
	}
	value.APIBase = strings.TrimRight(strings.TrimSpace(value.APIBase), "/")
	if value.APIBase == "" {
		value.APIBase = strings.TrimRight(strings.TrimSpace(policyAPIBase), "/")
	}
	if value.APIBase == "" || value.CredentialRef == "" {
		return xaiSearchConnection{}, errors.New("search provider is not configured")
	}
	value.Model = strings.TrimSpace(searchModel)
	if value.Model == "" {
		value.Model = "grok-4.5"
	}
	return value, nil
}

func (a *AgentRuntime) xaiSearchConnections(ctx context.Context, preferred []string, policyAPIBase, policyCredentialRef, searchModel string) ([]xaiSearchConnection, error) {
	seen := map[string]bool{}
	values := make([]xaiSearchConnection, 0)
	add := func(id string) error {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			return nil
		}
		value, err := a.xaiSearchConnection(ctx, id, policyAPIBase, policyCredentialRef, searchModel)
		if err != nil {
			return nil
		}
		seen[id] = true
		values = append(values, value)
		return nil
	}
	for _, id := range preferred {
		if err := add(id); err != nil {
			return nil, err
		}
	}
	rows, err := a.configStore.db.QueryContext(ctx, `SELECT id FROM provider_connections
		WHERE protocol = 'xai_responses' AND enabled = 1 ORDER BY id`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(values) == 0 {
		return nil, errors.New("no enabled xai_responses provider connection")
	}
	return values, nil
}

func (a *AgentRuntime) grokNativeResearchForRun(ctx context.Context, run *runRecord, originalQuery string, systemPrompt string) (string, []searchSource, bool, error) {
	var policy struct {
		Enabled               bool     `json:"enabled"`
		APIBase               string   `json:"apiBase"`
		CredentialRef         string   `json:"credentialRef"`
		SearchConnectionID    string   `json:"searchConnectionId"`
		SearchConnectionIDs   []string `json:"searchConnectionIds"`
		SearchModel           string   `json:"searchModel"`
		SearchSummaryMaxChars int      `json:"searchSummaryMaxChars"`
		SearchMaxSources      *int     `json:"searchMaxSources"`
		SearchIncludeLinks    *bool    `json:"searchIncludeSourceLinks"`
	}
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil || !policy.Enabled {
		return "", nil, false, err
	}
	preferred := append([]string{}, policy.SearchConnectionIDs...)
	if strings.TrimSpace(policy.SearchConnectionID) != "" {
		preferred = append([]string{policy.SearchConnectionID}, preferred...)
	}
	connections, err := a.xaiSearchConnections(ctx, preferred, policy.APIBase, policy.CredentialRef, policy.SearchModel)
	if err != nil {
		return "", nil, true, err
	}
	maxChars := policy.SearchSummaryMaxChars
	if maxChars < 40 || maxChars > 1200 {
		maxChars = 120
	}
	maxSources := 2
	if policy.SearchMaxSources != nil && *policy.SearchMaxSources >= 0 && *policy.SearchMaxSources <= 2 {
		maxSources = *policy.SearchMaxSources
	}
	includeLinks := searchQueryRequestsSources(originalQuery)
	if policy.SearchIncludeLinks != nil {
		includeLinks = includeLinks || *policy.SearchIncludeLinks
	}
	query := a.expandSearchQuery(run, originalQuery)
	prompt := compactSearchSystemPrompt(systemPrompt)
	if prompt == "" {
		prompt = "保持当前角色的说话方式。"
	}
	prompt += fmt.Sprintf("\n联网检索只用于回答当前问题。搜索结果是内部素材，不要原样转发，不要提到‘目前能确认的是’、‘资料显示’、‘百度百科’、搜索过程或结果列表。保持当前角色的说话方式：陪伴型角色先自然接话，再用一两句说清结论；可以带一点个人反应，但不能编造。最多两句、最多%d字；除非用户明确要来源，不要输出网址。", maxChars)
	var lastErr error
	for _, connection := range connections {
		key := getenv(connection.CredentialRef)
		if key == "" {
			lastErr = errors.New("search provider credential is not configured")
			continue
		}
		payload := map[string]any{
			"model": connection.Model,
			"input": []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": query}},
			"tools": []map[string]string{{"type": "web_search"}}, "stream": false,
			"max_output_tokens": min(max(maxChars, 80), 160),
		}
		remaining := time.Duration(0)
		if deadline, ok := ctx.Deadline(); ok {
			remaining = time.Until(deadline)
		}
		attemptTimeout := nativeSearchAttemptTimeout(connection, remaining)
		if connection.TimeoutSeconds > 0 && time.Duration(connection.TimeoutSeconds)*time.Second < attemptTimeout {
			attemptTimeout = time.Duration(connection.TimeoutSeconds) * time.Second
		}
		requestCtx, cancel := context.WithTimeout(ctx, attemptTimeout)
		var response xaiResponsesResponse
		err = a.postProviderJSON(requestCtx, connection.APIBase+"/responses", key, payload, &response)
		cancel()
		if err != nil {
			lastErr = fmt.Errorf("native web search via %s: %w", connection.ID, err)
			continue
		}
		text, sources := parseXAIResponses(response)
		if strings.TrimSpace(text) == "" {
			lastErr = errors.New("native web search returned no summary")
			continue
		}
		if !searchResultRelevant(query, text, sources) {
			lastErr = errors.New("native web search result was not relevant")
			continue
		}
		text = finalizeSearchSummary(text, sources, maxChars, maxSources, includeLinks)
		if run != nil {
			a.recordSearchEntity(*run, originalQuery, sources)
			target := runtimeProviderTarget{EndpointID: connection.ID, Provider: connection.Provider, Model: connection.Model, APIBase: connection.APIBase, APIKey: key, TimeoutSeconds: connection.TimeoutSeconds}
			a.recordProviderUsage(run.ID, target, response.Usage)
		}
		return text, sources, true, nil
	}
	if lastErr == nil {
		lastErr = errors.New("native web search failed")
	}
	return "", nil, true, lastErr
}

func decodeXAIResponsesStream(body io.Reader, response *xaiResponsesResponse) error {
	type streamEvent struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Annotation struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"annotation"`
		Response xaiResponsesResponse `json:"response"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	scanner := bufio.NewScanner(io.LimitReader(body, maxToolBody))
	scanner.Buffer(make([]byte, 64*1024), maxToolBody)
	var text strings.Builder
	sources := []searchSource{}
	var completed *xaiResponsesResponse
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		switch event.Type {
		case "response.output_text.delta":
			text.WriteString(event.Delta)
		case "response.output_text.annotation.added":
			if strings.TrimSpace(event.Annotation.URL) != "" {
				sources = append(sources, searchSource{URL: event.Annotation.URL, Title: event.Annotation.Title})
			}
		case "response.completed":
			value := event.Response
			completed = &value
		case "response.failed", "error":
			if message := strings.TrimSpace(event.Error.Message); message != "" {
				return errors.New(message)
			}
			return errors.New("native web search stream failed")
		}
	}
	if completed != nil {
		*response = *completed
	}
	response.StreamText = strings.TrimSpace(text.String())
	response.StreamSources = sources
	if err := scanner.Err(); err != nil {
		if sentence := completeSearchStreamSentence(response.StreamText); sentence != "" {
			response.StreamText = sentence
			return nil
		}
		return err
	}
	if response.StreamText == "" {
		text, _ := parseXAIResponses(*response)
		if text == "" {
			return errors.New("native web search stream returned no summary")
		}
	}
	return nil
}

func completeSearchStreamSentence(text string) string {
	runes := []rune(strings.TrimSpace(text))
	last := -1
	for index, character := range runes {
		if strings.ContainsRune("。！？.!?", character) {
			last = index + 1
		}
	}
	if last < 12 {
		return ""
	}
	return strings.TrimSpace(string(runes[:last]))
}

func compactSearchSystemPrompt(systemPrompt string) string {
	const maxRunes = 1800
	runes := []rune(strings.TrimSpace(systemPrompt))
	if len(runes) > maxRunes {
		runes = runes[len(runes)-maxRunes:]
	}
	return strings.TrimSpace(string(runes))
}

func parseXAIResponses(response xaiResponsesResponse) (string, []searchSource) {
	var parts []string
	var sources []searchSource
	seen := map[string]struct{}{}
	for _, source := range response.StreamSources {
		if strings.TrimSpace(source.URL) == "" {
			continue
		}
		if _, ok := seen[source.URL]; ok {
			continue
		}
		seen[source.URL] = struct{}{}
		sources = append(sources, source)
	}
	for _, item := range response.Output {
		if value := strings.TrimSpace(item.Text); value != "" {
			parts = append(parts, value)
		}
		for _, content := range item.Content {
			if value := strings.TrimSpace(content.Text); value != "" {
				parts = append(parts, value)
			}
			for _, annotation := range content.Annotations {
				if strings.TrimSpace(annotation.URL) == "" {
					continue
				}
				if _, ok := seen[annotation.URL]; ok {
					continue
				}
				seen[annotation.URL] = struct{}{}
				sources = append(sources, searchSource{Title: strings.TrimSpace(annotation.Title), URL: strings.TrimSpace(annotation.URL)})
			}
		}
	}
	if len(parts) == 0 {
		if value := strings.TrimSpace(response.StreamText); value != "" {
			parts = append(parts, value)
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n")), sources
}

// filterRelevantSearchSources keeps only sources that share at least one
// query term, ordered by how many terms they hit. One on-topic source must not
// drag two unrelated RSS items into the summary with it.
func filterRelevantSearchSources(query string, sources []searchSource) []searchSource {
	terms := searchTerms(query)
	if len(terms) == 0 || len(sources) == 0 {
		return sources
	}
	type scored struct {
		source searchSource
		hits   int
	}
	kept := make([]scored, 0, len(sources))
	for _, source := range sources {
		haystack := strings.ToLower(source.Title + " " + source.Snippet + " " + source.Content)
		hits := 0
		for _, term := range terms {
			if strings.Contains(haystack, term) {
				hits++
			}
		}
		if hits > 0 {
			kept = append(kept, scored{source: source, hits: hits})
		}
	}
	if len(kept) == 0 {
		return nil
	}
	sort.SliceStable(kept, func(left, right int) bool { return kept[left].hits > kept[right].hits })
	result := make([]searchSource, 0, len(kept))
	for _, entry := range kept {
		result = append(result, entry.source)
	}
	return result
}

func searchResultRelevant(query, text string, sources []searchSource) bool {
	terms := searchTerms(query)
	if len(terms) == 0 {
		return true
	}
	joined := strings.ToLower(text)
	for _, source := range sources {
		joined += " " + strings.ToLower(source.Title+" "+source.Snippet+" "+source.Content)
	}
	hits := 0
	for _, term := range terms {
		if strings.Contains(joined, term) {
			hits++
		}
	}
	return hits > 0 || len(sources) == 0
}

func searchTerms(value string) []string {
	value = strings.ToLower(strings.TrimSpace(value))
	terms := []string{}
	for _, token := range strings.FieldsFunc(value, func(r rune) bool {
		return strings.ContainsRune(" ，。！？?,.!:：；;、\n\t", r)
	}) {
		runes := []rune(token)
		if len(runes) >= 2 {
			terms = append(terms, token)
		}
		if len(runes) >= 4 {
			for index := 0; index+1 < len(runes) && len(terms) < 64; index++ {
				pair := string(runes[index : index+2])
				if !containsAnyText(pair, []string{"帮我", "一下", "什么", "今天", "最近", "目前", "现在", "请问"}) {
					terms = append(terms, pair)
				}
			}
		}
	}
	return terms
}
