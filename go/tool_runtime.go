package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	defaultMaxAgentSteps          = 6
	defaultChatCompletionMaxToken = 160
	maxToolBody                   = 5 * 1024 * 1024
	maxImageBytes                 = 20 * 1024 * 1024
	maxImagePromptBytes           = 7600
	mediaMountRoot                = "/erdai-media"
)

type runtimeToolPolicy struct {
	Authority          string             `json:"authority"`
	Tools              []runtimeTool      `json:"tools"`
	MCPServers         []runtimeMCPServer `json:"mcpServers"`
	MaxAgentSteps      int                `json:"maxAgentSteps,omitempty"`
	ToolTimeoutSeconds int                `json:"toolTimeoutSeconds,omitempty"`
	Streaming          bool               `json:"streaming,omitempty"`
}

type runtimeMCPServer struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Transport      string   `json:"transport"`
	ToolPrefix     string   `json:"toolPrefix"`
	AllowedTools   []string `json:"allowedTools"`
	ApprovalMode   string   `json:"approvalMode"`
	TimeoutSeconds int      `json:"timeoutSeconds"`
}

type runtimeMessagePolicy struct {
	ToolProgressEnabled            *bool    `json:"toolProgressEnabled"`
	ToolProgressSearchEnabled      *bool    `json:"toolProgressSearchEnabled"`
	ToolProgressSearchMessages     []string `json:"toolProgressSearchMessages"`
	ToolProgressImageMessages      []string `json:"toolProgressImageMessages"`
	ToolProgressPhotoMessages      []string `json:"toolProgressPhotoMessages"`
	ToolCompletionImageMessages    []string `json:"toolCompletionImageMessages"`
	ToolProgressVideoMessages      []string `json:"toolProgressVideoMessages"`
	ToolCompletionVideoMessages    []string `json:"toolCompletionVideoMessages"`
	ToolProgressDocumentMessages   []string `json:"toolProgressDocumentMessages"`
	ToolCompletionDocumentMessages []string `json:"toolCompletionDocumentMessages"`
	SegmentedReplyEnabled          *bool    `json:"segmentedReplyEnabled"`
	SegmentMinChars                int      `json:"segmentMinChars"`
	SegmentMaxChars                int      `json:"segmentMaxChars"`
	MaxReplySegments               int      `json:"maxReplySegments"`
	MaxReplyChars                  int      `json:"maxReplyChars,omitempty"`
	MaxReplySentences              int      `json:"maxReplySentences,omitempty"`
}

type runtimeTool struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Description    string         `json:"description"`
	Capabilities   []string       `json:"capabilities"`
	RiskLevel      int            `json:"riskLevel"`
	AdapterRef     string         `json:"adapterRef"`
	ApprovalMode   string         `json:"approvalMode"`
	TimeoutSeconds int            `json:"timeoutSeconds"`
	InputSchema    map[string]any `json:"inputSchema"`
}

type agentAttachment struct {
	Kind      string `json:"kind"`
	LocalPath string `json:"localPath"`
	Name      string `json:"name,omitempty"`
	MimeType  string `json:"mimeType,omitempty"`
}

type agentReply struct {
	Text        string
	Attachments []agentAttachment
	Segments    []string
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatCompletion struct {
	Usage   chatUsage `json:"usage"`
	Choices []struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	FirstTokenMS int64 `json:"-"`
}

type chatUsage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
}

func (u chatUsage) normalized() (int64, int64, int64) {
	input, output := u.PromptTokens, u.CompletionTokens
	if input == 0 {
		input = u.InputTokens
	}
	if output == 0 {
		output = u.OutputTokens
	}
	total := u.TotalTokens
	if total == 0 {
		total = input + output
	}
	return input, output, total
}

type toolResult struct {
	Content             string
	Attachments         []agentAttachment
	UserMessage         string
	PreserveUserMessage bool
}

// MCP calls share the same result contract. The transport client is added in a
// separate migration so an unconfigured MCP server always fails closed.
type mcpToolCaller interface {
	Call(context.Context, string, json.RawMessage) (toolResult, error)
}

func ensureRuntimeColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var index int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&index, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func (a *AgentRuntime) runAgentLoop(
	ctx context.Context,
	run runRecord,
	message, systemPrompt, model, apiBase string,
	policy runtimeToolPolicy,
	messagePolicy runtimeMessagePolicy,
) (agentReply, error) {
	return a.runAgentLoopWithModelsKey(
		ctx, run, message, systemPrompt, []string{model}, 0,
		apiBase, a.modelAPIKey, policy, messagePolicy,
	)
}

func (a *AgentRuntime) runAgentLoopWithModels(
	ctx context.Context,
	run runRecord,
	message, systemPrompt string,
	models []string,
	providerRetries int,
	apiBase string,
	policy runtimeToolPolicy,
	messagePolicy runtimeMessagePolicy,
) (agentReply, error) {
	return a.runAgentLoopWithModelsKey(ctx, run, message, systemPrompt, models, providerRetries, apiBase, a.modelAPIKey, policy, messagePolicy)
}

func (a *AgentRuntime) runAgentLoopWithModelsKey(
	ctx context.Context,
	run runRecord,
	message, systemPrompt string,
	models []string,
	providerRetries int,
	apiBase, apiKey string,
	policy runtimeToolPolicy,
	messagePolicy runtimeMessagePolicy,
) (agentReply, error) {
	targets := make([]runtimeProviderTarget, 0, len(models))
	for _, model := range models {
		targets = append(targets, runtimeProviderTarget{
			Model: model, APIBase: apiBase, APIKey: apiKey, ProviderRetries: providerRetries,
		})
	}
	return a.runAgentLoopWithTargets(ctx, run, message, systemPrompt, targets, policy, messagePolicy)
}

func (a *AgentRuntime) runAgentLoopWithTargets(
	ctx context.Context,
	run runRecord,
	message, systemPrompt string,
	targets []runtimeProviderTarget,
	policy runtimeToolPolicy,
	messagePolicy runtimeMessagePolicy,
) (agentReply, error) {
	if len(targets) == 0 {
		return agentReply{}, errors.New("provider model is not configured")
	}
	for index := range targets {
		targets[index].APIBase = strings.TrimRight(strings.TrimSpace(targets[index].APIBase), "/")
		parsed, err := url.Parse(targets[index].APIBase)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return agentReply{}, errors.New("provider API base is invalid")
		}
	}
	documentPolicy := a.documentPolicy()
	recentReplies := a.recentAssistantReplyTexts(ctx, runtimeScopeFromRun(run).memoryConversationRef(), run.PersonaID, 12)
	systemPrompt = withNaturalReplyGuard(systemPrompt, message, recentReplies)
	tools := authorizedToolDefinitions(
		policy, run.IsAdmin, message,
		hasReadableDocumentAttachment(run.Attachments, documentPolicy),
	)
	mcpDefinitions, mcpRoutes := a.discoverCoreMCPTools(ctx, policy, run.IsAdmin, message)
	tools = append(tools, mcpDefinitions...)
	messages := []map[string]any{
		{"role": "system", "content": systemPrompt},
		{"role": "user", "content": a.multimodalUserContent(ctx, message, run.Attachments, documentPolicy)},
	}
	var attachments []agentAttachment
	progressQueued := false
	toolUsed := false
	targetIndex := 0
	maxSteps := policy.MaxAgentSteps
	if maxSteps <= 0 || maxSteps > 20 {
		maxSteps = defaultMaxAgentSteps
	}
	// Search is a run-scoped fact lookup. Repeated model tool calls reuse the
	// first result instead of charging and waiting on the upstream again.
	searchCache := map[string]toolResult{}
	for step := 0; step < maxSteps; step++ {
		payload := map[string]any{"messages": messages, "stream": policy.Streaming}
		if len(tools) > 0 {
			payload["tools"] = tools
			payload["tool_choice"] = "auto"
			if officeDocumentRequestIntent(message) && hasRuntimeTool(tools, "create_office_document") {
				// A clear document request must enter the document tool on the first turn.
				// This avoids a confirmation-only model reply with no attachment.
				payload["tool_choice"] = map[string]any{
					"type":     "function",
					"function": map[string]string{"name": "create_office_document"},
				}
			}
		}
		if len(tools) == 0 && len(run.Attachments) == 0 && inferNativeLane(message, false, false, false) == "chat" {
			payload["max_tokens"] = defaultChatCompletionMaxToken
		}
		modelStarted := time.Now()
		requestTargets := boundedProviderTargets(message, len(run.Attachments) > 0, targets)
		if targetIndex >= len(requestTargets) {
			targetIndex = 0
		}
		modelStepID := ""
		if a.taskGraphRunExists(run.ID) {
			var stepErr error
			modelStepID, stepErr = a.beginTaskStep(run.ID, "", "model", requestTargets[targetIndex].Model, step, payload)
			if stepErr != nil {
				return agentReply{Attachments: attachments}, stepErr
			}
		}
		requestedTarget := targetIndex
		_ = a.recordRunStage(run.ID, "model_started", modelStarted, map[string]any{
			"endpointId": requestTargets[targetIndex].EndpointID, "model": requestTargets[targetIndex].Model,
		})
		// Every lane gets a per-step wall budget across the whole fallback
		// chain. A single slow endpoint must not consume the run, and a chain
		// of fallbacks must never stretch one message into a minute-scale wait.
		stepBudget := nonChatModelStepBudget
		if plainChatProviderBudgetApplies(message, len(run.Attachments) > 0) {
			stepBudget = plainChatProviderBudget
		}
		requestContext, cancelRequest := context.WithTimeout(ctx, stepBudget)
		completion, usedTarget, err := a.chatCompletionWithTargets(requestContext, payload, requestTargets, targetIndex, func(target runtimeProviderTarget, duration time.Duration, attemptErr error) {
			_, _ = a.db.Exec("UPDATE agent_runs SET provider_calls = provider_calls + 1 WHERE id = ?", run.ID)
			_ = a.recordRunStage(run.ID, "provider_attempt", time.Now().Add(-duration), map[string]any{
				"endpointId": target.EndpointID,
				"model":      target.Model,
				"durationMs": duration.Milliseconds(),
				"outcome":    providerAttemptOutcome(attemptErr),
			})
		})
		cancelRequest()
		if completion.FirstTokenMS > 0 {
			_ = a.recordRunStage(run.ID, "model_first_token", modelStarted, map[string]any{"latencyMs": completion.FirstTokenMS})
		}
		_ = a.recordRunStage(run.ID, "model_completed", modelStarted, map[string]any{
			"endpointId": requestTargets[usedTarget].EndpointID, "model": requestTargets[usedTarget].Model, "error": err != nil,
		})
		if err != nil {
			if modelStepID != "" {
				_ = a.finishTaskStep(modelStepID, "failed", "provider_failed", nil)
			}
			return agentReply{Attachments: attachments}, err
		}
		a.recordProviderUsage(run.ID, requestTargets[usedTarget], completion.Usage)
		if modelStepID != "" {
			_ = a.finishTaskStep(modelStepID, "succeeded", "", completion)
		}
		targetIndex = usedTarget
		targets = requestTargets
		routeReason := ""
		if usedTarget != requestedTarget {
			routeReason = "runtime fallback to " + targets[usedTarget].EndpointID
		}
		_, _ = a.db.Exec(`UPDATE agent_runs SET selected_endpoint_id = ?, selected_model = ?,
			route_reason = CASE WHEN ? = '' THEN route_reason ELSE route_reason || '; ' || ? END
			WHERE id = ?`, targets[usedTarget].EndpointID, targets[usedTarget].Model, routeReason, routeReason, run.ID)
		if len(completion.Choices) == 0 {
			return agentReply{Attachments: attachments}, errors.New("provider returned no choices")
		}
		assistant := completion.Choices[0].Message
		if len(assistant.ToolCalls) == 0 {
			text := strings.TrimSpace(assistant.Content)
			if text == "" && len(attachments) == 0 {
				return agentReply{}, errors.New("provider returned empty content")
			}
			finalPolicy := messagePolicy
			// Tool results may be structured or deliberately longer than chat.
			// Keep hard chat compression from triggering a second paid model call.
			if toolUsed {
				finalPolicy.MaxReplyChars = 0
				finalPolicy.MaxReplySentences = 0
			}
			return a.finalizeAgentReplyKey(
				ctx, message, systemPrompt, targets[targetIndex].APIBase,
				targets[targetIndex].APIKey,
				[]string{targets[targetIndex].Model}, 0,
				recentReplies, finalPolicy,
				agentReply{Text: text, Attachments: attachments}, true,
			), nil
		}
		messages = append(messages, map[string]any{
			"role": "assistant", "content": assistant.Content, "tool_calls": assistant.ToolCalls,
		})
		if !progressQueued {
			// Video progress is announced after the provider task is actually
			// created (inside generateVideo), never before.
			progressCalls := make([]chatToolCall, 0, len(assistant.ToolCalls))
			for _, toolCall := range assistant.ToolCalls {
				if strings.Contains(strings.ToLower(toolCall.Function.Name), "video") {
					continue
				}
				progressCalls = append(progressCalls, toolCall)
			}
			if progress := a.progressMessageForRun(ctx, run, messagePolicy, progressCalls, message); progress != "" {
				if err := a.enqueueDelivery(run, agentReply{Text: progress}, "progress", ""); err != nil {
					return agentReply{Attachments: attachments}, err
				}
				progressQueued = true
			}
		}
		for _, call := range assistant.ToolCalls {
			toolUsed = true
			adapter := normalizeAdapterRef(call.Function.Name)
			result, cached := searchCache[adapter]
			if !cached {
				result = a.executePersistentToolCall(ctx, run, message, policy, mcpRoutes, step, modelStepID, call)
				if adapter == "grok_web_search" {
					searchCache[adapter] = result
				}
			}
			attachments = append(attachments, result.Attachments...)
			if result.UserMessage != "" {
				if adapter == "grok_web_search" {
					result.UserMessage = humanizeSearchReply(result.UserMessage)
				}
				finalPolicy := messagePolicy
				if toolUsed {
					finalPolicy.MaxReplyChars = 0
					finalPolicy.MaxReplySentences = 0
				}
				return a.finalizeAgentReplyKey(
					ctx, message, systemPrompt, targets[targetIndex].APIBase,
					targets[targetIndex].APIKey,
					[]string{targets[targetIndex].Model}, 0,
					recentReplies, finalPolicy,
					agentReply{Text: result.UserMessage, Attachments: attachments},
					!result.PreserveUserMessage,
				), nil
			}
			messages = append(messages, map[string]any{
				"role": "tool", "tool_call_id": call.ID, "content": result.Content,
			})
		}
	}
	return agentReply{Attachments: attachments}, errors.New("agent tool loop exceeded step limit")
}

func (a *AgentRuntime) finalizeAgentReply(
	ctx context.Context,
	message, systemPrompt, apiBase string,
	models []string,
	modelIndex int,
	recent []string,
	messagePolicy runtimeMessagePolicy,
	reply agentReply,
	allowRewrite bool,
) agentReply {
	return a.finalizeAgentReplyKey(
		ctx, message, systemPrompt, apiBase, a.modelAPIKey, models, modelIndex,
		recent, messagePolicy, reply, allowRewrite,
	)
}

func (a *AgentRuntime) finalizeAgentReplyKey(
	ctx context.Context,
	message, systemPrompt, apiBase, apiKey string,
	models []string,
	modelIndex int,
	recent []string,
	messagePolicy runtimeMessagePolicy,
	reply agentReply,
	allowRewrite bool,
) agentReply {
	text := strings.TrimSpace(reply.Text)
	if runes := []rune(text); len(runes) > 12000 {
		if safe := trimReplyAtNaturalBoundary(text, 12000); safe != "" {
			text = safe
		}
	}
	budgetApplies := compactReplyBudgetApplies(message, messagePolicy)
	preserveFormatting := replyHasFormalLayout(text) && !budgetApplies
	if allowRewrite && !preserveFormatting {
		text = a.ensureNaturalChatReplyKey(
			ctx, message, systemPrompt, text, apiBase, apiKey,
			models, modelIndex, recent,
			messagePolicy.MaxReplyChars, messagePolicy.MaxReplySentences,
		)
	}
	if budgetApplies {
		text = plainChatReply(text)
		if replyExceedsBudget(text, messagePolicy.MaxReplyChars, messagePolicy.MaxReplySentences) {
			text = compactReplyToBudget(text, messagePolicy.MaxReplyChars, messagePolicy.MaxReplySentences)
		}
	}
	reply.Text = text
	if len(reply.Attachments) == 0 && replyMakesUnbackedMediaPromise(text) {
		reply.Text = "这次还没真做出来，先别等。"
		text = reply.Text
	}
	reply.Segments = splitReplyText(text, messagePolicy, preserveFormatting)
	return reply
}

func trimReplyAtNaturalBoundary(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 || runeCount(text) <= limit {
		return text
	}
	runes := []rune(text)
	prefix := strings.TrimSpace(string(runes[:limit]))
	if replyEndsAtNaturalBoundary(prefix) {
		return prefix
	}
	units := naturalReplyUnits(prefix)
	if len(units) <= 1 {
		return text
	}
	return strings.TrimSpace(strings.Join(units[:len(units)-1], ""))
}

func replyEndsAtNaturalBoundary(text string) bool {
	runes := []rune(strings.TrimSpace(text))
	for len(runes) > 0 && replyClosingRunes[runes[len(runes)-1]] {
		runes = runes[:len(runes)-1]
	}
	return len(runes) > 0 && isReplyBoundary(runes[len(runes)-1])
}

func hasRuntimeTool(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		function, _ := tool["function"].(map[string]any)
		toolName, _ := function["name"].(string)
		if toolName == name {
			return true
		}
	}
	return false
}

type providerHTTPError struct {
	StatusCode int
	Message    string
}

const (
	defaultGrokImageAttemptTimeout = 120 * time.Second
	defaultImageAttemptTimeout     = 90 * time.Second
)

func (e *providerHTTPError) Error() string {
	return fmt.Sprintf("provider returned HTTP %d", e.StatusCode)
}

func retryableProviderError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var responseError *providerHTTPError
	if errors.As(err, &responseError) {
		return responseError.StatusCode == http.StatusNotFound ||
			responseError.StatusCode == http.StatusRequestTimeout ||
			responseError.StatusCode == http.StatusConflict ||
			responseError.StatusCode == http.StatusTooEarly ||
			responseError.StatusCode == http.StatusTooManyRequests ||
			responseError.StatusCode >= http.StatusInternalServerError
	}
	var networkError net.Error
	return errors.As(err, &networkError)
}

func (a *AgentRuntime) chatCompletionWithFallback(
	ctx context.Context,
	endpoint string,
	payload map[string]any,
	models []string,
	startIndex, providerRetries int,
) (chatCompletion, int, error) {
	return a.chatCompletionWithFallbackKey(ctx, endpoint, a.modelAPIKey, payload, models, startIndex, providerRetries)
}

func (a *AgentRuntime) chatCompletionWithFallbackKey(
	ctx context.Context,
	endpoint, apiKey string,
	payload map[string]any,
	models []string,
	startIndex, providerRetries int,
) (chatCompletion, int, error) {
	if len(models) == 0 {
		return chatCompletion{}, 0, errors.New("provider model is not configured")
	}
	if startIndex < 0 || startIndex >= len(models) {
		startIndex = 0
	}
	if providerRetries < 0 {
		providerRetries = 0
	} else if providerRetries > 4 {
		providerRetries = 4
	}
	attempts := providerRetries + 1
	var lastErr error
	lastModel := startIndex
	for attempt := 0; attempt < attempts; attempt++ {
		modelIndex := (startIndex + attempt) % len(models)
		lastModel = modelIndex
		payload["model"] = models[modelIndex]
		var completion chatCompletion
		lastErr = a.postProviderJSON(ctx, endpoint, apiKey, payload, &completion)
		if lastErr == nil && usableChatCompletion(completion) {
			return completion, modelIndex, nil
		}
		if lastErr == nil {
			lastErr = errors.New("provider returned empty content")
			continue
		}
		if !retryableProviderError(lastErr) {
			return chatCompletion{}, modelIndex, lastErr
		}
	}
	return chatCompletion{}, lastModel, fmt.Errorf("provider attempts exhausted: %w", lastErr)
}

func (a *AgentRuntime) chatCompletionWithTargets(
	ctx context.Context,
	payload map[string]any,
	targets []runtimeProviderTarget,
	startIndex int,
	onAttempt func(runtimeProviderTarget, time.Duration, error),
) (chatCompletion, int, error) {
	if len(targets) == 0 {
		return chatCompletion{}, 0, errors.New("provider model is not configured")
	}
	if startIndex < 0 || startIndex >= len(targets) {
		startIndex = 0
	}
	lastTarget := startIndex
	var lastErr error
	totalAttempts := len(targets)
	if retries := targets[startIndex].ProviderRetries + 1; retries > totalAttempts {
		totalAttempts = retries
	}
	if totalAttempts > len(targets)+4 {
		totalAttempts = len(targets) + 4
	}
	// 401/403 is a credential/permission fault: retrying the same credential is
	// pointless, and a run must not walk every fallback burning wall-clock. One
	// hop to a target with a different credential pair is allowed; a second
	// rejected credential ends the request immediately.
	rejectedCredentials := map[string]bool{}
	attempted := false
	for attempt := 0; attempt < totalAttempts; attempt++ {
		targetIndex := (startIndex + attempt) % len(targets)
		target := targets[targetIndex]
		if rejectedCredentials[providerCredentialPair(target)] {
			continue
		}
		if attempted {
			if remaining, bounded := providerDeadlineRemaining(ctx); bounded && remaining < 2*time.Second {
				break
			}
		}
		lastTarget = targetIndex
		payload["model"] = target.Model
		requestContext := ctx
		cancel := func() {}
		if timeout := providerAttemptTimeout(ctx, target.TimeoutSeconds); timeout > 0 {
			requestContext, cancel = context.WithTimeout(ctx, timeout)
		}
		attemptStarted := time.Now()
		attempted = true
		var completion chatCompletion
		emptyContent := false
		lastErr = a.postProviderJSON(
			requestContext,
			strings.TrimRight(target.APIBase, "/")+"/chat/completions",
			target.APIKey,
			payload,
			&completion,
		)
		cancel()
		if lastErr == nil && !usableChatCompletion(completion) {
			emptyContent = true
			lastErr = errors.New("provider returned empty content")
		}
		if onAttempt != nil {
			onAttempt(target, time.Since(attemptStarted), lastErr)
		}
		if lastErr == nil {
			return completion, targetIndex, nil
		}
		if emptyContent {
			continue
		}
		if errors.Is(lastErr, context.DeadlineExceeded) && ctx.Err() == nil {
			continue
		}
		if providerAuthorizationError(lastErr) {
			rejectedCredentials[providerCredentialPair(target)] = true
			if len(rejectedCredentials) >= 2 || !hasAlternateCredential(targets, rejectedCredentials) {
				return chatCompletion{}, targetIndex, lastErr
			}
			continue
		}
		if !retryableProviderError(lastErr) {
			return chatCompletion{}, targetIndex, lastErr
		}
	}
	return chatCompletion{}, lastTarget, fmt.Errorf("provider targets exhausted: %w", lastErr)
}

func providerCredentialPair(target runtimeProviderTarget) string {
	return strings.TrimRight(target.APIBase, "/") + "\x00" + target.APIKey
}

func hasAlternateCredential(targets []runtimeProviderTarget, rejected map[string]bool) bool {
	for _, target := range targets {
		if !rejected[providerCredentialPair(target)] {
			return true
		}
	}
	return false
}

// providerDeadlineRemaining reports how much of the caller's deadline is left,
// so a fallback chain never starts an attempt it cannot finish.
func providerDeadlineRemaining(ctx context.Context) (time.Duration, bool) {
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return 0, false
	}
	return time.Until(deadline), true
}

// providerAttemptTimeout clips one attempt to the remaining request deadline
// so a single slow endpoint cannot consume the entire fallback budget.
func providerAttemptTimeout(ctx context.Context, timeoutSeconds int) time.Duration {
	timeout := time.Duration(timeoutSeconds) * time.Second
	remaining, bounded := providerDeadlineRemaining(ctx)
	if !bounded {
		if timeout <= 0 {
			return 0
		}
		return timeout
	}
	if timeout <= 0 || timeout > remaining {
		return max(remaining, time.Millisecond)
	}
	return timeout
}

func providerAuthorizationError(err error) bool {
	var responseError *providerHTTPError
	return errors.As(err, &responseError) &&
		(responseError.StatusCode == http.StatusUnauthorized || responseError.StatusCode == http.StatusForbidden)
}

const (
	failureClassCredential   = "credential"
	failureClassQuota        = "quota_exhausted"
	failureClassRateLimit    = "rate_limit"
	failureClassTimeout      = "timeout"
	failureClassContent      = "content"
	failureClassUpstreamDown = "upstream_down"
	failureClassUnknown      = "unknown"
)

// classifyProviderFailure maps a provider error onto one of six operational
// classes. Each class carries its own fallback policy and user phrasing, and
// the class is persisted per run for aggregated statistics.
func classifyProviderFailure(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return failureClassTimeout
	}
	var responseError *providerHTTPError
	if errors.As(err, &responseError) {
		message := strings.ToLower(strings.TrimSpace(responseError.Message))
		if strings.Contains(message, "subscription:free-usage-exhausted") ||
			strings.Contains(message, "free usage quota exceeded") ||
			strings.Contains(message, "usage quota exceeded") ||
			strings.Contains(message, "quota exhausted") {
			return failureClassQuota
		}
		switch {
		case responseError.StatusCode == http.StatusUnauthorized || responseError.StatusCode == http.StatusForbidden:
			return failureClassCredential
		case responseError.StatusCode == http.StatusTooManyRequests:
			return failureClassRateLimit
		case responseError.StatusCode == http.StatusBadRequest || responseError.StatusCode == http.StatusUnprocessableEntity:
			return failureClassContent
		case responseError.StatusCode >= http.StatusInternalServerError:
			return failureClassUpstreamDown
		}
	}
	var videoError *videoHTTPError
	if errors.As(err, &videoError) {
		switch {
		case videoError.StatusCode == http.StatusUnauthorized || videoError.StatusCode == http.StatusForbidden:
			return failureClassCredential
		case videoError.StatusCode == http.StatusTooManyRequests:
			return failureClassRateLimit
		case videoError.StatusCode >= http.StatusInternalServerError:
			return failureClassUpstreamDown
		}
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return failureClassTimeout
		}
		return failureClassUpstreamDown
	}
	return failureClassUnknown
}

func providerAttemptOutcome(err error) string {
	if err == nil {
		return "succeeded"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled"
	}
	var responseError *providerHTTPError
	if errors.As(err, &responseError) {
		return fmt.Sprintf("http_%d", responseError.StatusCode)
	}
	if strings.Contains(strings.ToLower(err.Error()), "empty content") {
		return "empty_content"
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		return "network_error"
	}
	return "provider_error"
}

func usableChatCompletion(completion chatCompletion) bool {
	if len(completion.Choices) == 0 {
		return false
	}
	message := completion.Choices[0].Message
	return strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) > 0
}

func (a *AgentRuntime) multimodalUserContent(ctx context.Context, message string, attachments []transportAttachment, policy documentPolicy) any {
	if len(attachments) == 0 {
		return message
	}
	parts := []map[string]any{{"type": "text", "text": message}}
	for _, attachment := range attachments {
		if attachment.Kind == "image" && policy.ImageUnderstandingEnabled && (strings.TrimSpace(attachment.SourceURL) != "" || strings.TrimSpace(attachment.LocalPath) != "") {
			imageURL := attachment.SourceURL
			if data, mimeType, err := a.readLocalMedia(attachment.LocalPath, maxImageBytes); err == nil {
				imageURL = "data:" + firstNonEmpty(attachment.MimeType, mimeType) + ";base64," + base64.StdEncoding.EncodeToString(data)
			}
			// QQ/CDN attachment URLs are often short-lived or inaccessible to the
			// upstream model. Inline a bounded copy when Core can fetch it safely.
			if strings.TrimSpace(attachment.LocalPath) == "" {
				if dataURL, err := a.inboundImageDataURL(ctx, attachment.SourceURL, policy); err == nil {
					imageURL = dataURL
				}
			}
			if strings.TrimSpace(imageURL) == "" {
				continue
			}
			if strings.HasPrefix(imageURL, "data:") {
				// Already inlined from the durable local copy.
			} else if dataURL, err := a.inboundImageDataURL(ctx, imageURL, policy); err == nil {
				imageURL = dataURL
			}
			parts = append(parts, map[string]any{
				"type": "image_url",
				"image_url": map[string]string{
					"url": imageURL, "detail": "auto",
				},
			})
			continue
		}
		if attachment.Kind == "file" && policy.Enabled && documentAllowed(documentExtension(attachment.Name), policy) {
			parts = append(parts, map[string]any{"type": "text", "text": fmt.Sprintf(
				"当前消息包含附件 %s（%s）。需要读取内容时调用 read_document，attachmentId 使用 %s。",
				attachment.Name, documentExtension(attachment.Name), attachment.ID,
			)})
		}
	}
	if len(parts) == 1 {
		return message
	}
	return parts
}

func (a *AgentRuntime) readLocalMedia(localPath string, limit int64) ([]byte, string, error) {
	base := filepath.Base(strings.TrimSpace(localPath))
	if base == "." || base == "" || strings.TrimSpace(a.mediaDir) == "" {
		return nil, "", errors.New("local media path is invalid")
	}
	data, err := os.ReadFile(filepath.Join(a.mediaDir, base))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return nil, "", errors.New("local media is unavailable")
	}
	mimeType := http.DetectContentType(data)
	return data, mimeType, nil
}

func (a *AgentRuntime) inboundRunImageReference(ctx context.Context, run runRecord) (string, error) {
	policy := a.documentPolicy()
	for _, attachment := range run.Attachments {
		if strings.ToLower(strings.TrimSpace(attachment.Kind)) != "image" {
			continue
		}
		if data, detectedMime, err := a.readLocalMedia(attachment.LocalPath, maxImageBytes); err == nil {
			mimeType := firstNonEmpty(strings.TrimSpace(attachment.MimeType), detectedMime)
			return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
		}
		if sourceURL := strings.TrimSpace(attachment.SourceURL); sourceURL != "" {
			if dataURL, err := a.inboundImageDataURL(ctx, sourceURL, policy); err == nil {
				return dataURL, nil
			}
		}
	}
	return "", errors.New("inbound image is unavailable")
}

func (a *AgentRuntime) inboundImageDataURL(ctx context.Context, rawURL string, policy documentPolicy) (string, error) {
	endpoint, err := validateNativeMCPEndpoint(ctx, rawURL, net.DefaultResolver)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	client := *a.client
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many image redirects")
		}
		_, redirectErr := validateNativeMCPEndpoint(next.Context(), next.URL.String(), net.DefaultResolver)
		return redirectErr
	}
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("inbound image download returned HTTP %d", response.StatusCode)
	}
	limit := int64(maxImageBytes)
	if policy.MaxFileMB > 0 && int64(policy.MaxFileMB)*1024*1024 < limit {
		limit = int64(policy.MaxFileMB) * 1024 * 1024
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || len(data) == 0 || int64(len(data)) > limit {
		return "", errors.New("inbound image is empty or too large")
	}
	mimeType := strings.ToLower(strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0]))
	if !strings.HasPrefix(mimeType, "image/") {
		mimeType = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mimeType, "image/") {
		return "", errors.New("inbound attachment is not an image")
	}
	return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func hasReadableDocumentAttachment(attachments []transportAttachment, policy documentPolicy) bool {
	for _, attachment := range attachments {
		if attachment.Kind == "file" && documentAllowed(documentExtension(attachment.Name), policy) {
			return true
		}
	}
	return false
}

func progressMessageOptions(policy runtimeMessagePolicy, calls []chatToolCall, message string) (string, []string) {
	if policy.ToolProgressEnabled != nil && !*policy.ToolProgressEnabled {
		return "", nil
	}
	imageTask := false
	videoTask := false
	documentTask := false
	searchTask := false
	for _, call := range calls {
		name := strings.ToLower(call.Function.Name)
		if strings.Contains(name, "search") {
			searchTask = true
		}
		if strings.Contains(name, "video") {
			videoTask = true
			break
		}
		if strings.Contains(name, "image") {
			imageTask = true
		}
		if strings.Contains(name, "document") || strings.Contains(name, "office") {
			documentTask = true
		}
	}
	if searchTask && (policy.ToolProgressSearchEnabled == nil || !*policy.ToolProgressSearchEnabled) {
		return "", nil
	}
	if !imageTask && !videoTask && !documentTask && !searchTask {
		return "", nil
	}
	scene := "search-progress"
	candidates := policy.ToolProgressSearchMessages
	if videoTask {
		scene = "video-progress"
		candidates = policy.ToolProgressVideoMessages
	} else if imageTask {
		scene = "image-progress"
		candidates = policy.ToolProgressImageMessages
		if nativePhotoRequestPattern.MatchString(message) {
			scene = "photo-progress"
			if len(policy.ToolProgressPhotoMessages) > 0 {
				candidates = policy.ToolProgressPhotoMessages
			}
		}
	} else if documentTask {
		scene = "document-progress"
		candidates = policy.ToolProgressDocumentMessages
	}
	if len(candidates) == 0 {
		if videoTask {
			candidates = []string{"我去做，视频会慢一点。", "行，我开始做这段视频。"}
		} else if imageTask {
			candidates = []string{"我去画，弄好就发你。", "行，我先把图做出来。"}
		} else if documentTask {
			candidates = []string{"我整理一下，文件弄好发你。", "行，我把它做成文件。"}
		} else {
			candidates = []string{"我去查一下。", "行，我找点可靠的来源。"}
		}
	}
	return scene, candidates

}

func progressMessage(policy runtimeMessagePolicy, calls []chatToolCall, message string) string {
	_, candidates := progressMessageOptions(policy, calls, message)
	return randomMessage(candidates)

}

func (a *AgentRuntime) progressMessageForRun(
	ctx context.Context,
	run runRecord,
	policy runtimeMessagePolicy,
	calls []chatToolCall,
	message string,
) string {
	scene, candidates := progressMessageOptions(policy, calls, message)
	if scene == "" {
		return ""
	}
	return a.personaFixedReply(ctx, run, scene, candidates)
}

func boundedProviderTargets(message string, hasAttachments bool, targets []runtimeProviderTarget) []runtimeProviderTarget {
	if !plainChatProviderBudgetApplies(message, hasAttachments) || len(targets) == 0 {
		return targets
	}
	limit := min(len(targets), plainChatProviderTargetLimit)
	bounded := append([]runtimeProviderTarget(nil), targets[:limit]...)
	for index := range bounded {
		bounded[index].ProviderRetries = 0
		if bounded[index].TimeoutSeconds <= 0 || bounded[index].TimeoutSeconds > plainChatProviderAttemptBudget {
			bounded[index].TimeoutSeconds = plainChatProviderAttemptBudget
		}
	}
	return bounded
}

func plainChatProviderBudgetApplies(message string, hasAttachments bool) bool {
	return !hasAttachments && inferNativeLane(message, false, false, false) == "chat"
}

const (
	plainChatProviderBudget        = 20 * time.Second
	plainChatProviderAttemptBudget = 15
	plainChatProviderTargetLimit   = 3
	// nonChatModelStepBudget bounds a single agent-loop model call (with all
	// its provider fallbacks) outside the plain-chat lane. Tool and media
	// execution time is budgeted separately and is not covered by this cap.
	nonChatModelStepBudget = 75 * time.Second
)

var unbackedMediaPromisePattern = regexp.MustCompile(`(?i)((马上|等会|待会|一会|稍后|这就).{0,12}(发你|给你看|给你(一张)?(图片|照片|自拍|视频))|(弄好|做好|拍好|画好|生成好|重新来|去拍|去画|去做).{0,12}(发你|给你看|给你图|图片|照片|自拍|视频)|(图片|照片|自拍|视频).{0,12}(马上|等会|待会|一会|稍后).{0,6}(发你|给你看)|(重新|再)来一(张|个)|马上(就)?(安排|弄|画|拍|生成)|这就去(弄|做|画|拍|生成)|稍等.{0,4}(马上|这就)?(发|给)你|等我.{0,4}(弄|画|拍|生成)(好|完))`)

func replyMakesUnbackedMediaPromise(text string) bool {
	return unbackedMediaPromisePattern.MatchString(strings.TrimSpace(text))
}

func imageCompletionMessage(policy runtimeMessagePolicy) string {
	return randomMessage(imageCompletionOptions(policy))
}

func videoCompletionMessage(policy runtimeMessagePolicy) string {
	return randomMessage(videoCompletionOptions(policy))
}

func documentCompletionMessage(policy runtimeMessagePolicy) string {
	return randomMessage(documentCompletionOptions(policy))
}

func imageCompletionOptions(policy runtimeMessagePolicy) []string {
	candidates := policy.ToolCompletionImageMessages
	if len(candidates) == 0 {
		candidates = []string{"弄好了，给你看看。", "画完了，这张给你。"}
	}
	return candidates
}

func videoCompletionOptions(policy runtimeMessagePolicy) []string {
	candidates := policy.ToolCompletionVideoMessages
	if len(candidates) == 0 {
		candidates = []string{"做好了，视频给你。", "视频好了，看看这版。"}
	}
	return candidates
}

func documentCompletionOptions(policy runtimeMessagePolicy) []string {
	candidates := policy.ToolCompletionDocumentMessages
	if len(candidates) == 0 {
		candidates = []string{"整理好了，文件给你。", "做好了，你看看这版。"}
	}
	return candidates
}

func normalizeAdapterRef(value string) string {
	return strings.TrimPrefix(strings.TrimSpace(value), "core:")
}

func isImageGenerationAdapter(value string) bool {
	switch normalizeAdapterRef(value) {
	case "generate_image", "grok_generate_image":
		return true
	default:
		return false
	}
}

func randomMessage(candidates []string) string {
	filtered := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if value := strings.TrimSpace(candidate); value != "" {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return ""
	}
	index, err := rand.Int(rand.Reader, big.NewInt(int64(len(filtered))))
	if err != nil {
		return filtered[0]
	}
	return filtered[index.Int64()]
}

func authorizedToolDefinitions(policy runtimeToolPolicy, isAdmin bool, message string, hasDocument bool) []map[string]any {
	expected := "member"
	if isAdmin {
		expected = "admin"
	}
	if policy.Authority != expected {
		return nil
	}
	definitions := make([]map[string]any, 0, len(policy.Tools))
	for _, tool := range policy.Tools {
		if strings.TrimSpace(tool.Name) == "" || tool.InputSchema == nil ||
			!toolPermitted(tool, isAdmin, message) {
			continue
		}
		if normalizeAdapterRef(tool.AdapterRef) == "read_document" && !hasDocument {
			continue
		}
		if normalizeAdapterRef(tool.AdapterRef) == "grok_web_search" && !explicitWebSearchIntent(message) {
			continue
		}
		definitions = append(definitions, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.InputSchema,
			},
		})
	}
	return definitions
}

func (a *AgentRuntime) executeToolCall(
	ctx context.Context,
	run runRecord,
	message string,
	policy runtimeToolPolicy,
	mcpRoutes map[string]mcpBridgeRoute,
	call chatToolCall,
) toolResult {
	fail := func(code string) toolResult {
		body, _ := json.Marshal(map[string]any{"ok": false, "error": code})
		return toolResult{Content: string(body)}
	}
	expected := "member"
	if run.IsAdmin {
		expected = "admin"
	}
	if policy.Authority != expected {
		return fail("authority_mismatch")
	}
	var selected *runtimeTool
	for index := range policy.Tools {
		if policy.Tools[index].Name == call.Function.Name {
			selected = &policy.Tools[index]
			break
		}
	}
	if selected == nil {
		if route, ok := mcpRoutes[call.Function.Name]; ok {
			timeout := route.TimeoutSeconds
			if policy.ToolTimeoutSeconds > 0 && (timeout <= 0 || timeout > policy.ToolTimeoutSeconds) {
				timeout = policy.ToolTimeoutSeconds
			}
			if timeout <= 0 || timeout > 300 {
				timeout = 30
			}
			toolContext, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
			return a.callCoreMCP(toolContext, route, call.Function.Arguments)
		}
		return fail("tool_not_allowed")
	}
	if !toolPermitted(*selected, run.IsAdmin, message) {
		return fail("approval_required")
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return fail("invalid_arguments")
	}
	timeout := time.Duration(selected.TimeoutSeconds) * time.Second
	adapterRef := normalizeAdapterRef(selected.AdapterRef)
	if policy.ToolTimeoutSeconds > 0 && adapterRef != "grok_generate_video" && !isImageGenerationAdapter(adapterRef) &&
		(timeout <= 0 || timeout > time.Duration(policy.ToolTimeoutSeconds)*time.Second) {
		timeout = time.Duration(policy.ToolTimeoutSeconds) * time.Second
	}
	if adapterRef == "grok_generate_video" {
		if timeout <= 0 || timeout > maxVideoGenerationDuration {
			timeout = maxVideoGenerationDuration
		}
	} else if isImageGenerationAdapter(adapterRef) {
		fallbackSeconds := selected.TimeoutSeconds
		if fallbackSeconds <= 0 {
			fallbackSeconds = 600
		}
		timeout = a.imageTaskTimeout(ctx, fallbackSeconds)
	} else if normalizeAdapterRef(selected.AdapterRef) == "read_document" {
		policyTimeout := time.Duration(a.documentPolicy().ExtractionTimeoutSeconds) * time.Second
		if timeout <= 0 || timeout > policyTimeout {
			timeout = policyTimeout
		}
	} else if timeout <= 0 || timeout > 300*time.Second {
		timeout = 30 * time.Second
	}
	toolContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := a.executeBuiltIn(toolContext, run, *selected, arguments)
	if err != nil {
		var exceeded *mediaQuotaExceededError
		if errors.As(err, &exceeded) {
			return a.quotaToolResult(ctx, run, exceeded.Kind)
		}
		return fail("tool_execution_failed")
	}
	return result
}

func toolPermitted(tool runtimeTool, isAdmin bool, message string) bool {
	if tool.RiskLevel == 0 && tool.ApprovalMode == "auto" {
		return true
	}
	adapter := normalizeAdapterRef(tool.AdapterRef)
	normalized := strings.ToLower(strings.TrimSpace(message))
	explicit := func(words ...string) bool {
		for _, word := range words {
			if strings.Contains(normalized, word) {
				return true
			}
		}
		return false
	}
	if tool.ApprovalMode == "confirm" && tool.RiskLevel <= 1 {
		switch adapter {
		case "generate_image", "grok_generate_image":
			return nativeImageLanePattern.MatchString(normalized)
		case "grok_generate_video":
			return explicitVideoGenerationIntent(normalized)
		case "memory_remember":
			return explicit("记住", "记一下", "帮我记")
		case "create_office_document":
			return officeDocumentRequestIntent(normalized)
		}
	}
	if tool.ApprovalMode == "admin_only" && isAdmin {
		switch adapter {
		case "memory_forget":
			return explicit("忘记", "删除记忆", "清除记忆")
		}
	}
	return false
}

func (a *AgentRuntime) executeBuiltIn(ctx context.Context, run runRecord, tool runtimeTool, arguments map[string]any) (toolResult, error) {
	adapter := normalizeAdapterRef(tool.AdapterRef)
	switch adapter {
	case "grok_web_search":
		return a.grokSearchForRun(ctx, run, stringArgument(arguments, "query"))
	case "grok_generate_image":
		return a.executeQuotaMedia(ctx, run, mediaKindImage, func() (toolResult, error) {
			prompt := a.personaImagePromptForRun(ctx, run, stringArgument(arguments, "prompt"))
			return a.generateImageForPersona(ctx, prompt, true, run.PersonaID)
		})
	case "generate_image":
		return a.executeQuotaMedia(ctx, run, mediaKindImage, func() (toolResult, error) {
			prompt := a.personaImagePromptForRun(ctx, run, stringArgument(arguments, "prompt"))
			return a.generateImageForPersona(ctx, prompt, false, run.PersonaID)
		})
	case "grok_generate_video":
		return a.executeQuotaMedia(ctx, run, mediaKindVideo, func() (toolResult, error) {
			return a.generateVideo(ctx, run, a.personaVideoPromptForRun(ctx, run, stringArgument(arguments, "prompt")))
		})
	case "ops_status", "query_ops_status":
		return a.queryOPS(ctx, "")
	case "memory_recall":
		return a.recallMemory(ctx, run, stringArgument(arguments, "query"))
	case "memory_remember":
		return a.rememberMemory(ctx, run, stringArgument(arguments, "fact"))
	case "memory_forget":
		return a.forgetMemory(ctx, run, stringArgument(arguments, "query"))
	case "read_document":
		return a.readDocumentAttachment(ctx, run, stringArgument(arguments, "attachmentId"))
	case "create_office_document":
		result, err := a.createOfficeDocument(ctx, arguments)
		if err == nil && result.UserMessage != "" {
			var policy runtimeMessagePolicy
			policyErr := a.integrationConfig(ctx, "message_policy", &policy)
			if policyErr == nil {
				result.UserMessage = a.personaFixedReply(
					ctx, run, "document-completion", documentCompletionOptions(policy),
				)
			}
		}
		return result, err
	default:
		return toolResult{}, errors.New("tool adapter is not implemented")
	}
}

func attachmentKinds(attachments []transportAttachment) (bool, bool, bool) {
	hasImage, hasAudio, hasDocument := false, false, false
	for _, attachment := range attachments {
		switch attachment.Kind {
		case "image":
			hasImage = true
		case "audio":
			hasAudio = true
		case "file":
			hasDocument = true
		}
	}
	return hasImage, hasAudio, hasDocument
}

func (a *AgentRuntime) rememberMemory(ctx context.Context, run runRecord, fact string) (toolResult, error) {
	fact = strings.TrimSpace(fact)
	if a.memory == nil || fact == "" || len([]rune(fact)) > 500 || containsSensitiveMemory(fact) {
		return toolResult{}, errors.New("memory content is invalid or sensitive")
	}
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	memory, inserted, err := a.memory.AddMemory(ctx, scope, fact)
	if err != nil {
		return toolResult{}, err
	}
	policy := a.memoryPolicy(ctx)
	if policy.MaxMemoriesPerScope > 0 {
		_ = a.memory.TrimScope(ctx, scope, policy.MaxMemoriesPerScope)
	}
	encoded, _ := json.Marshal(map[string]any{
		"ok": true, "saved": inserted, "memoryId": memory.ID,
	})
	return toolResult{Content: string(encoded)}, nil
}

func (a *AgentRuntime) forgetMemory(ctx context.Context, run runRecord, query string) (toolResult, error) {
	if a.memory == nil || strings.TrimSpace(query) == "" {
		return toolResult{}, errors.New("memory query is required")
	}
	scope := personaMemoryScope(run.PersonaID, "user", runtimeScopeFromRun(run).userMemoryRef())
	memories, err := a.memory.SearchMemories(ctx, scope, query, 20)
	if err != nil {
		return toolResult{}, err
	}
	deleted := 0
	for _, memory := range memories {
		ok, err := a.memory.ForgetMemory(ctx, scope, memory.ID)
		if err != nil {
			return toolResult{}, err
		}
		if ok {
			deleted++
		}
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "deleted": deleted})
	return toolResult{Content: string(encoded)}, nil
}

func containsSensitiveMemory(content string) bool {
	normalized := strings.ToLower(content)
	for _, marker := range []string{
		"密码", "密钥", "身份证", "银行卡", "private key", "access token", "api key", "sk-",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) recallMemory(ctx context.Context, run runRecord, query string) (result toolResult, recallErr error) {
	started := time.Now()
	userMatches, groupMatches := 0, 0
	defer func() {
		_ = a.recordRunStage(run.ID, "memory_recall", started, map[string]any{
			"queryChars":   len([]rune(strings.TrimSpace(query))),
			"userMatches":  userMatches,
			"groupMatches": groupMatches,
			"returned":     userMatches + groupMatches,
			"success":      recallErr == nil,
		})
	}()
	if a.memory == nil {
		return toolResult{}, errors.New("memory store is not configured")
	}
	policy := a.memoryPolicy(ctx)
	if !policy.Enabled {
		return toolResult{}, errors.New("memory is disabled")
	}
	scope := runtimeScopeFromRun(run)
	userMemories, err := a.memory.SearchMemories(ctx, personaMemoryScope(run.PersonaID, "user", scope.userMemoryRef()), query, policy.RetrievalLimit)
	if err != nil {
		return toolResult{}, err
	}
	userMatches = len(userMemories)
	groupMemories := []RecalledMemory{}
	if policy.AllowGroupSharedMemory {
		groupMemories, err = a.memory.SearchMemories(ctx, personaMemoryScope(run.PersonaID, "group", scope.groupMemoryRef()), query, policy.RetrievalLimit)
		if err != nil {
			return toolResult{}, err
		}
		groupMatches = len(groupMemories)
	}
	items := make([]string, 0, len(userMemories)+len(groupMemories))
	for _, memory := range append(userMemories, groupMemories...) {
		items = append(items, memory.UntrustedContent)
	}
	encoded, _ := json.Marshal(map[string]any{
		"ok": true, "untrusted": true, "memories": items,
	})
	return toolResult{Content: string(encoded)}, nil
}

func stringArgument(arguments map[string]any, name string) string {
	value, _ := arguments[name].(string)
	return strings.TrimSpace(value)
}

func (a *AgentRuntime) grokSearch(ctx context.Context, query string) (toolResult, error) {
	text, _, err := a.grokResearch(ctx, query)
	if err != nil {
		return toolResult{}, err
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": text})
	return toolResult{Content: string(encoded)}, nil
}

func (a *AgentRuntime) grokSearchForRun(ctx context.Context, run runRecord, query string) (toolResult, error) {
	text, _, err := a.grokResearchForRun(ctx, &run, query)
	if err != nil {
		return toolResult{}, err
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": text})
	return toolResult{Content: string(encoded)}, nil
}

func (a *AgentRuntime) grokSearchForRunWithPrompt(ctx context.Context, run runRecord, query, systemPrompt string) (toolResult, error) {
	text, _, err := a.grokResearchForRunWithPrompt(ctx, &run, query, systemPrompt)
	if err != nil {
		return toolResult{}, err
	}
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": text})
	return toolResult{Content: string(encoded)}, nil
}

func (a *AgentRuntime) grokResearch(ctx context.Context, query string) (string, []searchSource, error) {
	return a.grokResearchForRun(ctx, nil, query)
}

func (a *AgentRuntime) grokResearchForRun(ctx context.Context, run *runRecord, query string) (string, []searchSource, error) {
	return a.grokResearchForRunWithPrompt(ctx, run, query, "")
}

func (a *AgentRuntime) grokResearchForRunWithPrompt(ctx context.Context, run *runRecord, query, systemPrompt string) (text string, sources []searchSource, err error) {
	if query == "" {
		return "", nil, errors.New("Grok search query is required")
	}
	originalQuery := strings.TrimSpace(query)
	if run != nil {
		cachedText, cachedSources, handled, cacheErr := a.beginSearchRun(run, originalQuery)
		if handled {
			return cachedText, cachedSources, cacheErr
		}
		defer func() {
			if err != nil {
				a.finishSearchRunFailure(run.ID, err)
				return
			}
			a.finishSearchRunSuccess(run.ID, text, sources)
		}()
	}
	if nativeText, nativeSources, handled, nativeErr := a.grokNativeResearchForRun(ctx, run, originalQuery, systemPrompt); handled && nativeErr == nil {
		return nativeText, nativeSources, nil
	} else if handled && ctx.Err() != nil {
		return "", nil, ctx.Err()
	}
	query = a.expandSearchQuery(run, query)
	var policy struct {
		Enabled                  bool   `json:"enabled"`
		APIBase                  string `json:"apiBase"`
		CredentialRef            string `json:"credentialRef"`
		SearchConnectionID       string `json:"searchConnectionId"`
		SearchSummaryEndpointID  string `json:"searchSummaryEndpointId"`
		SearchModel              string `json:"searchModel"`
		SearchSummaryMaxChars    int    `json:"searchSummaryMaxChars"`
		SearchMaxSources         *int   `json:"searchMaxSources"`
		SearchIncludeSourceLinks *bool  `json:"searchIncludeSourceLinks"`
	}
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil || !policy.Enabled {
		return "", nil, errors.New("Grok search is disabled")
	}
	summaryConnection := providerConnectionConfig{}
	if connection, ok, connectionErr := a.providerConnectionForEndpoint(policy.SearchSummaryEndpointID, ""); connectionErr != nil {
		return "", nil, connectionErr
	} else if ok {
		summaryConnection = connection
	} else if connection, ok, connectionErr := a.providerConnectionByID(ctx, policy.SearchConnectionID); connectionErr != nil {
		return "", nil, connectionErr
	} else if ok {
		summaryConnection = connection
	}
	if summaryConnection.ID != "" {
		policy.APIBase = summaryConnection.APIBase
		policy.CredentialRef = summaryConnection.CredentialRef
	}
	credential := a.grokCredential(policy.CredentialRef)
	if credential == "" {
		return "", nil, errors.New("Grok search is not configured")
	}
	base, err := secureServiceBase(policy.APIBase)
	if err != nil {
		return "", nil, err
	}
	maxChars := policy.SearchSummaryMaxChars
	if maxChars < 120 || maxChars > 1200 {
		maxChars = 320
	}
	maxSources := 2
	if policy.SearchMaxSources != nil {
		maxSources = *policy.SearchMaxSources
	}
	if maxSources < 0 || maxSources > 3 {
		maxSources = 2
	}
	includeSourceLinks := false
	if policy.SearchIncludeSourceLinks != nil {
		includeSourceLinks = *policy.SearchIncludeSourceLinks
	}
	includeSourceLinks = includeSourceLinks || searchQueryRequestsSources(originalQuery)
	retrievalQuery := searchRetrievalQuery(query)
	sources, err = a.searchRSS(ctx, retrievalQuery)
	if err != nil || len(sources) == 0 {
		return "", nil, errors.New("web search returned no sources")
	}
	// Drop off-topic RSS items before they reach the summary model. A short
	// honest "没搜到" beats a confident answer built on unrelated sources.
	sources = filterRelevantSearchSources(retrievalQuery, sources)
	if len(sources) == 0 || !searchResultRelevant(retrievalQuery, "", sources) {
		return "", nil, errors.New("web search results were not relevant")
	}
	var sourceText strings.Builder
	for index, source := range sources {
		content := strings.TrimSpace(firstNonEmpty(source.Content, source.Snippet))
		if len([]rune(content)) > 1800 {
			content = truncateRunes(content, 1800)
		}
		fmt.Fprintf(&sourceText, "%d. %s\n%s\n%s\n", index+1, source.Title, content, source.URL)
	}
	summaryPrompt := searchSummaryInstruction(maxChars, maxSources)
	if personaPrompt := compactSearchSystemPrompt(systemPrompt); personaPrompt != "" {
		summaryPrompt += "\n保持下面角色的表达节奏，但不要改变检索事实：\n" + personaPrompt
	}
	summaryPrompt += "\n搜索结果只作内部参考。不要输出‘目前能确认的是’、‘资料显示’、‘百度百科’、来源清单或搜索过程；直接按当前角色像聊天一样回答。陪伴型角色先接话，再简短说结论。"
	payload := map[string]any{
		"model": policy.SearchModel,
		"messages": []map[string]string{
			{"role": "system", "content": summaryPrompt},
			{"role": "user", "content": query + "\n\n搜索结果：\n" + sourceText.String()},
		},
		"stream": false, "temperature": 0.2, "max_tokens": min(maxChars*2, 800),
	}
	var completion chatCompletion
	summaryContext, cancelSummary := context.WithTimeout(ctx, searchSummaryAttemptLimit)
	summaryErr := a.postProviderJSON(summaryContext, base+"/chat/completions", credential, payload, &completion)
	cancelSummary()
	if summaryErr != nil && errors.Is(ctx.Err(), context.Canceled) {
		return "", nil, ctx.Err()
	}
	text = ""
	if summaryErr == nil && len(completion.Choices) > 0 {
		text = strings.TrimSpace(completion.Choices[0].Message.Content)
		if run != nil {
			a.recordProviderUsage(run.ID, runtimeProviderTarget{
				EndpointID: summaryConnection.ID, Provider: summaryConnection.Provider,
				Model: policy.SearchModel, APIBase: policy.APIBase, APIKey: credential,
				TimeoutSeconds: int(searchSummaryAttemptLimit / time.Second),
			}, completion.Usage)
		}
	}
	if searchSummaryNeedsRetry(text, maxChars, maxSources) {
		text = compactSearchFallback(sources, maxChars)
	}
	if !searchResultRelevant(retrievalQuery, text, sources) {
		return "", nil, errors.New("web search summary was not relevant")
	}
	text = finalizeSearchSummary(text, sources, maxChars, maxSources, includeSourceLinks)
	if run != nil {
		a.recordSearchEntity(*run, originalQuery, sources)
	}
	return text, sources, nil
}

func searchRetrievalQuery(query string) string {
	original := strings.TrimSpace(query)
	replacer := strings.NewReplacer(
		"帮我搜索一下", " ", "帮我搜索", " ", "搜索一下", " ",
		"帮我搜一下", " ", "帮我搜", " ", "帮我查一下", " ", "帮我查", " ", "查一下", " ",
		"联网看看", " ", "网上看看", " ", "给我找一下", " ", "找一下", " ",
		"并用一句话总结", " ", "用一句话总结", " ", "一句话总结", " ",
		"简单总结", " ", "总结一下", " ", "今天的", " ", "今天", " ",
		"的最新消息", " 最新消息", "，", " ", "。", " ", "？", " ", "！", " ",
	)
	query = strings.Join(strings.Fields(replacer.Replace(original)), " ")
	if query == "" {
		return original
	}
	return query
}

// beginSearchRun reserves one web-search attempt for a run. Repeated tool
// calls in the same agent loop must reuse the first result or first failure.
func (a *AgentRuntime) beginSearchRun(run *runRecord, query string) (string, []searchSource, bool, error) {
	if a == nil || a.db == nil || run == nil || strings.TrimSpace(run.ID) == "" {
		return "", nil, false, nil
	}
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(query)))))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	result, err := a.db.Exec(`INSERT OR IGNORE INTO agent_search_runs
		(run_id, query_hash, status, created_at, updated_at) VALUES (?, ?, 'running', ?, ?)`,
		run.ID, hash, now, now)
	if err != nil {
		return "", nil, true, fmt.Errorf("search state persist failed: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed == 1 {
		return "", nil, false, nil
	}
	var status, errorMessage string
	var resultCipher, sourcesCipher []byte
	err = a.db.QueryRow(`SELECT status, result_cipher, sources_cipher, error_message
		FROM agent_search_runs WHERE run_id = ?`, run.ID).
		Scan(&status, &resultCipher, &sourcesCipher, &errorMessage)
	if err != nil {
		return "", nil, true, fmt.Errorf("search state read failed: %w", err)
	}
	switch status {
	case "succeeded":
		text, sources, decodeErr := a.decodeSearchCache(resultCipher, sourcesCipher)
		return text, sources, true, decodeErr
	case "failed":
		if strings.TrimSpace(errorMessage) == "" {
			errorMessage = "web search failed"
		}
		return "", nil, true, errors.New(errorMessage)
	default:
		return "", nil, true, errors.New("web search already attempted for this message")
	}
}

func (a *AgentRuntime) finishSearchRunSuccess(runID, text string, sources []searchSource) {
	if a == nil || a.db == nil || strings.TrimSpace(runID) == "" {
		return
	}
	resultCipher, err := a.encrypt([]byte(text))
	if err != nil {
		return
	}
	sourcesJSON, err := json.Marshal(sources)
	if err != nil {
		return
	}
	sourcesCipher, err := a.encrypt(sourcesJSON)
	if err != nil {
		return
	}
	_, _ = a.db.Exec(`UPDATE agent_search_runs SET status = 'succeeded', result_cipher = ?,
		sources_cipher = ?, error_message = '', updated_at = ? WHERE run_id = ?`,
		resultCipher, sourcesCipher, time.Now().UTC().Format(time.RFC3339Nano), runID)
}

func (a *AgentRuntime) finishSearchRunFailure(runID string, searchErr error) {
	if a == nil || a.db == nil || strings.TrimSpace(runID) == "" {
		return
	}
	message := "web search failed"
	if searchErr != nil && strings.TrimSpace(searchErr.Error()) != "" {
		message = searchErr.Error()
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, _ = a.db.Exec(`UPDATE agent_search_runs SET status = 'failed', error_message = ?,
		updated_at = ? WHERE run_id = ?`, message, time.Now().UTC().Format(time.RFC3339Nano), runID)
}

func (a *AgentRuntime) decodeSearchCache(resultCipher, sourcesCipher []byte) (string, []searchSource, error) {
	if len(resultCipher) == 0 || len(sourcesCipher) == 0 {
		return "", nil, errors.New("cached web search result is incomplete")
	}
	result, err := a.decrypt(resultCipher)
	if err != nil {
		return "", nil, err
	}
	sourcesJSON, err := a.decrypt(sourcesCipher)
	if err != nil {
		return "", nil, err
	}
	var sources []searchSource
	if err := json.Unmarshal(sourcesJSON, &sources); err != nil {
		return "", nil, err
	}
	return string(result), sources, nil
}

func searchQueryRequestsSources(query string) bool {
	query = strings.ToLower(strings.TrimSpace(query))
	return containsAnyText(query, []string{
		"来源", "出处", "原文", "链接", "网址", "url", "source", "link",
	})
}

type searchSource struct {
	Title     string
	URL       string
	Snippet   string
	Content   string
	Published string
}

func (a *AgentRuntime) searchRSS(ctx context.Context, query string) ([]searchSource, error) {
	endpoint, err := secureSearchBase(a.searchBaseURL)
	if err != nil {
		return nil, err
	}
	parameters := endpoint.Query()
	parameters.Set("q", query)
	endpoint.RawQuery = parameters.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/rss+xml, application/xml;q=0.9")
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned HTTP %d", response.StatusCode)
	}
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Link        string `xml:"link"`
				Description string `xml:"description"`
				Published   string `xml:"pubDate"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(&feed); err != nil {
		return nil, err
	}
	sources := make([]searchSource, 0, 6)
	for _, item := range feed.Channel.Items {
		title := strings.TrimSpace(item.Title)
		link, err := url.Parse(strings.TrimSpace(item.Link))
		if err != nil || title == "" || link.Scheme != "https" || link.Host == "" {
			continue
		}
		source := searchSource{Title: title, URL: link.String(), Snippet: cleanSearchText(item.Description), Published: strings.TrimSpace(item.Published)}
		if len(sources) < 3 {
			fetchCtx, cancel := context.WithTimeout(ctx, 800*time.Millisecond)
			source.Content, _ = a.fetchSearchContent(fetchCtx, source.URL)
			cancel()
		}
		if source.Content == "" {
			source.Content = source.Snippet
		}
		sources = append(sources, source)
		if len(sources) == 6 {
			break
		}
	}
	return sources, nil
}

var searchHTMLTagPattern = regexp.MustCompile(`<[^>]+>`)

func cleanSearchText(value string) string {
	value = html.UnescapeString(searchHTMLTagPattern.ReplaceAllString(value, " "))
	return strings.Join(strings.Fields(value), " ")
}

func (a *AgentRuntime) fetchSearchContent(ctx context.Context, rawURL string) (string, error) {
	endpoint, err := validateNativeMCPEndpoint(ctx, rawURL, net.DefaultResolver)
	if err != nil {
		return "", err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Accept", "text/html, text/plain;q=0.8")
	request.Header.Set("User-Agent", "Mozilla/5.0")
	response, err := a.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("source returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 128*1024))
	if err != nil {
		return "", err
	}
	return truncateRunes(cleanSearchText(string(data)), 4000), nil
}

var (
	searchURLPattern      = regexp.MustCompile(`https?://[^\s\]\[()<>，。]+`)
	searchMarkdownPattern = regexp.MustCompile(`\[([^\]]+)]\(https?://[^)]+\)`)
	searchCitationPattern = regexp.MustCompile(`(?:\[\[\d+\]\]|\[\d+\]|【\d+】)(?:\([^)]*\))?`)
	searchListPrefix      = regexp.MustCompile(`^\s*(?:[-*•]|\d+[.)、])\s*`)
)

func searchSummaryInstruction(maxChars, maxSources int) string {
	return fmt.Sprintf(
		"只根据给定资料直接回答问题。先归纳结论，不要逐条复述标题、摘要或搜索结果，不要写成资料清单。正文最多%d个中文字符，通常2到4句；最多引用%d个来源。资料不足就明确说不足，不得编造，也不得声称还要搜索。",
		maxChars, maxSources,
	)
}

func searchSummaryNeedsRetry(text string, maxChars, maxSources int) bool {
	text = strings.TrimSpace(text)
	if text == "" || looksLikeSearchUnavailable(text) {
		return true
	}
	body := searchSummaryBody(text)
	if body == "" || len([]rune(body)) > maxChars {
		return true
	}
	lines := nonEmptyLines(text)
	if len(lines) > maxSources+5 || len(searchURLPattern.FindAllString(text, -1)) > maxSources {
		return true
	}
	listLines := 0
	for _, line := range lines {
		if searchListPrefix.MatchString(line) {
			listLines++
		}
	}
	return listLines > 2
}

func finalizeSearchSummary(text string, sources []searchSource, maxChars, maxSources int, includeLinks bool) string {
	body := searchSummaryBody(text)
	if len([]rune(body)) > maxChars {
		body = truncateSearchSentence(body, maxChars)
	}
	// Search answers are conclusions by default. Sources are an explicit opt-in,
	// otherwise even host names make the reply read like a search results page.
	if !includeLinks || maxSources == 0 || len(sources) == 0 {
		return body
	}
	references := make([]string, 0, maxSources)
	seen := map[string]struct{}{}
	for _, source := range sources {
		parsed, err := url.Parse(strings.TrimSpace(source.URL))
		if err != nil || parsed.Host == "" {
			continue
		}
		reference := source.URL
		if _, ok := seen[reference]; ok {
			continue
		}
		seen[reference] = struct{}{}
		references = append(references, reference)
		if len(references) == maxSources {
			break
		}
	}
	if len(references) == 0 {
		return body
	}
	return strings.TrimSpace(body) + "\n参考：" + strings.Join(references, "、")
}

func searchSummaryBody(text string) string {
	text = searchCitationPattern.ReplaceAllString(text, "")
	text = searchMarkdownPattern.ReplaceAllString(text, "$1")
	text = searchURLPattern.ReplaceAllString(text, "")
	parts := make([]string, 0, 4)
	seen := map[string]struct{}{}
	for _, line := range nonEmptyLines(text) {
		line = strings.TrimSpace(searchListPrefix.ReplaceAllString(line, ""))
		lower := strings.ToLower(line)
		if line == "" || strings.HasPrefix(lower, "来源") || strings.HasPrefix(lower, "参考") ||
			strings.HasPrefix(lower, "sources") || strings.HasPrefix(lower, "references") ||
			strings.TrimSuffix(line, "：") == "搜索结果" {
			continue
		}
		key := normalizeReplyForComparison(line)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		parts = append(parts, line)
	}
	return strings.Join(parts, "\n")
}

func nonEmptyLines(text string) []string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func truncateSearchSentence(text string, maxChars int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxChars {
		return string(runes)
	}
	boundary := -1
	for index, character := range runes[:maxChars] {
		if strings.ContainsRune("。！？!?；;", character) {
			boundary = index + 1
		}
	}
	if boundary >= maxChars/2 {
		return strings.TrimSpace(string(runes[:boundary]))
	}
	return strings.TrimSpace(string(runes[:maxChars-1])) + "…"
}

func compactSearchFallback(sources []searchSource, maxChars int) string {
	for _, source := range sources {
		content := cleanSearchText(firstNonEmpty(source.Title, source.Snippet))
		if content == "" {
			continue
		}
		return truncateSearchSentence("目前能确认的是："+content, maxChars)
	}
	return "这次搜到的资料太散，我先不乱下结论。"
}

func humanizeSearchReply(text string) string {
	text = searchSummaryBody(text)
	for _, prefix := range []string{
		"目前能确认的是：", "目前能确认的是:", "资料显示：", "资料显示:",
		"搜索结果显示：", "搜索结果显示:", "根据搜索结果，", "根据搜索结果,",
		"根据百度百科，", "根据百度百科,",
	} {
		text = strings.TrimSpace(strings.TrimPrefix(text, prefix))
	}
	text = strings.TrimSpace(strings.TrimSuffix(text, "_百度百科"))
	text = strings.TrimSpace(strings.TrimSuffix(text, "- 百度百科"))
	return text
}

func (a *AgentRuntime) expandSearchQuery(run *runRecord, query string) string {
	query = strings.TrimSpace(query)
	if run == nil || len(run.Attachments) > 0 || !searchFollowUpIntent(query) {
		return query
	}
	var hint string
	var encrypted []byte
	var expiresAt string
	_ = a.db.QueryRow(`SELECT persona_id, entity_cipher, entity_hint, expires_at FROM agent_search_entities
		WHERE agent_instance_id = ? AND transport = ? AND transport_instance = ?
		AND conversation_ref = ? AND sender_ref = ? AND thread_key = ?
		AND persona_id = ? AND (expires_at = '' OR expires_at > ?)
		ORDER BY created_at DESC LIMIT 1`, runtimeInstanceScopeID(*run), run.Transport,
		runtimeTransportInstanceScopeID(*run), run.ConversationRef, run.SenderRef, run.ThreadKey,
		run.PersonaID, time.Now().UTC().Format(time.RFC3339Nano)).Scan(new(string), &encrypted, &hint, &expiresAt)
	if len(encrypted) > 0 {
		if plain, err := a.decrypt(encrypted); err == nil {
			hint = string(plain)
		}
	}
	if strings.TrimSpace(hint) == "" {
		return query
	}
	return hint + " " + query
}

func searchFollowUpIntent(query string) bool {
	for _, marker := range []string{
		"\u8fd9\u662f\u8c01", "\u8fd9\u4e2a\u4eba", "\u4ed6\u662f\u8c01", "\u5979\u662f\u8c01", "\u5b83\u662f\u8c01",
		"\u539f\u578b", "\u51fa\u5904", "\u6765\u6e90",
	} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	query = strings.TrimSpace(query)
	for _, marker := range []string{"这是谁", "这个人", "他是谁", "她是谁", "原型", "出处", "来源", "它是谁"} {
		if strings.Contains(query, marker) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) recordSearchEntity(run runRecord, query string, sources []searchSource) {
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 500 {
		return
	}
	entityHint := deriveSearchEntityHint(query, sources)
	if entityHint == "" {
		return
	}
	urls := make([]string, 0, len(sources))
	for _, source := range sources {
		urls = append(urls, source.URL)
	}
	encoded, _ := json.Marshal(urls)
	entityCipher, err := a.encrypt([]byte(entityHint))
	if err != nil {
		return
	}
	queryCipher, err := a.encrypt([]byte(query))
	if err != nil {
		return
	}
	sourcesCipher, err := a.encrypt(encoded)
	if err != nil {
		return
	}
	expiresAt := time.Now().UTC().Add(30 * time.Minute).Format(time.RFC3339Nano)
	_, _ = a.db.Exec(`INSERT INTO agent_search_entities
		(agent_instance_id, transport, transport_instance, conversation_ref, sender_ref, thread_key, persona_id, entity_hint, query, sources_json,
		 entity_cipher, query_cipher, sources_cipher, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '[]', ?, ?, ?, ?, ?)`, runtimeInstanceScopeID(run), run.Transport,
		runtimeTransportInstanceScopeID(run), run.ConversationRef, run.SenderRef, run.ThreadKey, run.PersonaID,
		entityHint, entityCipher, queryCipher, sourcesCipher, expiresAt, time.Now().UTC().Format(time.RFC3339Nano))
}

func deriveSearchEntityHint(query string, sources []searchSource) string {
	value := strings.TrimSpace(query)
	for _, prefix := range []string{
		"\u5e2e\u6211\u641c\u7d22", "\u5e2e\u6211\u67e5\u4e00\u4e0b", "\u5e2e\u6211\u627e\u4e00\u4e0b", "\u67e5\u4e00\u4e0b", "\u641c\u7d22",
		"\u544a\u8bc9\u6211", "please search", "search for", "look up",
	} {
		value = strings.TrimSpace(strings.TrimPrefix(strings.ToLower(value), strings.ToLower(prefix)))
	}
	for _, suffix := range []string{
		"\u662f\u8c01", "\u662f\u4ec0\u4e48", "\u7684\u539f\u578b\u662f\u8c01", "\u51fa\u81ea\u54ea\u91cc", "\u662f\u4ec0\u4e48\u4f5c\u54c1",
		"who is it", "what is it",
	} {
		value = strings.TrimSpace(strings.TrimSuffix(strings.ToLower(value), strings.ToLower(suffix)))
	}
	value = strings.Trim(value, " \t\r\n,.;:!?\u3002\uff0c\uff1b\uff1a\uff01\uff1f")
	if len([]rune(value)) >= 2 && len([]rune(value)) <= 80 {
		return value
	}
	for _, source := range sources {
		if title := strings.TrimSpace(source.Title); title != "" {
			return truncateRunes(title, 80)
		}
	}
	return ""
}

func secureSearchBase(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("ERDAI_SEARCH_BASE_URL must be an HTTPS URL without credentials or fragment")
	}
	return parsed, nil
}

func looksLikeSearchPromise(text string) bool {
	for _, marker := range []string{"我将搜索", "准备搜索", "先进行搜索", "先进行网络搜索", "I will search"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func looksLikeSearchUnavailable(text string) bool {
	if looksLikeSearchPromise(text) {
		return true
	}
	text = strings.ToLower(strings.TrimSpace(text))
	for _, marker := range []string{
		"\u6ca1\u8054\u7f51", "\u6ca1\u6709\u8054\u7f51", "\u4e0d\u80fd\u8054\u7f51", "\u65e0\u6cd5\u8054\u7f51", "\u6ca1\u6709\u8054\u7f51\u68c0\u7d22",
		"\u4e0d\u80fd\u641c\u7d22", "\u65e0\u6cd5\u641c\u7d22", "\u628a\u94fe\u63a5\u53d1\u6765", "\u628a\u622a\u56fe\u53d1\u6765",
		"cannot browse", "can't browse", "unable to browse", "no web access",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) generateImage(ctx context.Context, prompt string, grok bool) (toolResult, error) {
	return a.generateImageForPersona(ctx, prompt, grok, "")
}

func (a *AgentRuntime) generateImageForPersona(ctx context.Context, prompt string, grok bool, personaID string) (toolResult, error) {
	reference := ""
	if grok {
		reference = a.personaAvatarDataURI(ctx, personaID, prompt, true)
	}
	result, err := a.generateImageOnce(ctx, prompt, grok, reference)
	if err != nil && grok && reference != "" {
		result, err = a.generateImageOnce(ctx, prompt, true, "")
	}
	if err == nil || !grok || ctx.Err() != nil {
		return result, err
	}
	fallback, fallbackErr := a.generateImageOnce(ctx, prompt, false, "")
	if fallbackErr == nil {
		return fallback, nil
	}
	return toolResult{}, fmt.Errorf("Grok image generation failed: %w; image fallback failed: %v", err, fallbackErr)
}

func personaImagePrompt(prompt string, persona *nativeActivePersona) string {
	now := time.Now()
	return personaImagePromptAt(
		prompt, persona, now, defaultImageVisualDirectorPolicy(),
		nextSelfieVariationSeed(prompt, personaID(persona), now),
	)
}

func personaID(persona *nativeActivePersona) string {
	if persona == nil {
		return ""
	}
	return persona.ID
}

func personaImagePromptAt(
	prompt string,
	persona *nativeActivePersona,
	now time.Time,
	policy imageVisualDirectorPolicy,
	variationSeed uint64,
) string {
	prompt = strings.TrimSpace(prompt)
	if persona == nil || strings.TrimSpace(persona.VisualDescription) == "" ||
		!nativeSelfImageRequestPattern.MatchString(prompt) {
		return prompt
	}
	parts := []string{
		"生成同一位成年女性角色本人的现实世界生活照，保持脸型、五官、发型、发色、年龄、体态和整体气质稳定；这是同一个人，不是换脸。照片像她本人或朋友用手机随手拍到的瞬间，不是为了展示商品而摆拍。",
		"固定人物外观：" + strings.TrimSpace(persona.VisualDescription),
		"用户这次的场景要求：" + prompt,
		"场景必须符合现实：季节、天气、时间、地点、光线、衣着和物体相互匹配；炎热夏天穿透气的短袖或轻薄裙装，寒冷天气才穿厚外套。动作、手脚、镜面反射和透视符合真实物理。构图允许轻微歪斜、人物偏一侧、裁切不完美、自然抓拍和一点点运动感，不要每次正面居中看镜头。",
	}
	if override := strings.TrimSpace(persona.VisualPromptOverride); override != "" {
		parts = append(parts, "当前角色视觉覆盖："+override)
	}
	if referencePrompt := strings.TrimSpace(persona.VisualReferencePrompt); referencePrompt != "" {
		parts = append(parts, "已整理的角色参考资料（只用于稳定外观，不照抄场景）："+referencePrompt)
	}
	if variation := visualDirectorPrompt(prompt, now, variationSeed, policy); variation != "" {
		parts = append(parts, variation)
	}
	normalized := strings.ToLower(prompt)
	switch {
	case strings.Contains(normalized, "全身") || strings.Contains(normalized, "穿搭"):
		parts = append(parts, "构图以自然站姿或生活动作展示完整穿搭，四肢完整，不要夸张模特姿势。")
	case strings.Contains(normalized, "头像") || strings.Contains(normalized, "证件照"):
		parts = append(parts, "构图以肩部以上为主，眼神自然，背景干净，但不要做成严肃商务照。")
	case strings.Contains(normalized, "睡前") || strings.Contains(normalized, "居家") || strings.Contains(normalized, "夜晚"):
		parts = append(parts, "使用温暖居家光线和放松神态，画面有生活感，不要影棚布光。")
	case strings.Contains(normalized, "开心") || strings.Contains(normalized, "笑") || strings.Contains(normalized, "可爱"):
		parts = append(parts, "表情明亮俏皮，笑容自然克制，不做幼态夸张表情。")
	default:
		parts = append(parts, "按手机前置镜头的自然自拍构图，轻微生活感，避免商业人像和冷峻时尚大片。")
	}
	parts = append(parts,
		"必须是现实摄影中的自然人形象；整体可爱、灵动、亲近，但明确成年，不幼态化。保留真实皮肤纹理、细小发丝、轻微表情不对称和普通手机镜头的自然质感。",
		"禁止棚拍、影棚背景、商业模特姿势、网红写真模板、完美对称构图、过度磨皮、塑料皮肤、固定端杯姿势、固定白T恤、固定背景和摆拍式职业头像。也禁止动画、二次元、3D 渲染、卡通、玩偶、儿童脸、机器人、机械身体、屏幕脸、AI 或产品标志、Logo、文字水印。不要成熟商务精英脸、僵硬证件照。",
	)
	return strings.Join(parts, "\n")
}

func (a *AgentRuntime) personaImagePrompt(ctx context.Context, prompt string, persona *nativeActivePersona) string {
	now := time.Now()
	return personaImagePromptAt(
		prompt, persona, now, a.imageVisualDirectorPolicy(ctx),
		nextSelfieVariationSeed(prompt, personaID(persona), now),
	)
}

func (a *AgentRuntime) activePersonaImagePrompt(ctx context.Context, prompt string) string {
	if !nativeSelfImageRequestPattern.MatchString(strings.TrimSpace(prompt)) || a == nil || a.configStore == nil {
		return prompt
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return prompt
	}
	persona, _, err := a.configStore.activePersonaAndWorldbook(config, prompt)
	if err != nil || persona == nil {
		return prompt
	}
	visualPrompt := a.configStore.personaVisualReferencePrompt(persona.ID)
	profile, _ := a.configStore.personaRuntimeProfile(persona.ID)
	return a.personaImagePrompt(ctx, prompt, &nativeActivePersona{
		ID: persona.ID, Namespace: persona.Namespace, Name: persona.Name,
		Description: persona.Description, VisualDescription: persona.VisualDescription,
		VisualPromptOverride: profile.VisualPromptOverride,
		CharacterVersion:     persona.CharacterVersion, VisualReferencePrompt: visualPrompt,
	})
}

func (a *AgentRuntime) personaImagePromptForRun(ctx context.Context, run runRecord, prompt string) string {
	persona := a.personaForRun(run, prompt)
	if persona == nil {
		return prompt
	}
	return a.personaImagePrompt(ctx, prompt, persona)
}

func (a *AgentRuntime) personaForRun(run runRecord, prompt string) *nativeActivePersona {
	if a == nil || a.configStore == nil || strings.TrimSpace(run.PersonaID) == "" {
		return nil
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return nil
	}
	personaID := strings.TrimSpace(run.PersonaID)
	persona, _, err := a.configStore.personaAndWorldbook(config, &personaID, prompt)
	if err != nil || persona == nil {
		return nil
	}
	profile, _ := a.configStore.effectivePersonaRuntimeProfile(persona.ID, run.AgentInstanceID)
	return &nativeActivePersona{
		ID: persona.ID, Namespace: persona.Namespace, Name: persona.Name,
		Description: persona.Description, VisualDescription: persona.VisualDescription,
		VisualPromptOverride:  profile.VisualPromptOverride,
		VisualReferencePrompt: a.configStore.personaVisualReferencePrompt(persona.ID),
		CharacterVersion:      persona.CharacterVersion,
	}
}

func (a *AgentRuntime) activePersonaAvatarDataURI(ctx context.Context, prompt string, selfOnly bool) string {
	return a.personaAvatarDataURI(ctx, "", prompt, selfOnly)
}

func (a *AgentRuntime) personaAvatarDataURI(ctx context.Context, personaID, prompt string, selfOnly bool) string {
	prompt = strings.TrimSpace(prompt)
	if a == nil || a.configStore == nil || prompt == "" ||
		(selfOnly && !nativeSelfImageRequestPattern.MatchString(prompt)) {
		return ""
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return ""
	}
	resolvedPersonaID := config.ActivePersonaID
	if value := strings.TrimSpace(personaID); value != "" {
		resolvedPersonaID = &value
	}
	persona, _, err := a.configStore.personaAndWorldbook(config, resolvedPersonaID, prompt)
	if err != nil || persona == nil {
		return ""
	}
	if reference, referenceErr := a.configStore.primaryPersonaVisualReferenceDataURI(persona.ID); referenceErr == nil && strings.TrimSpace(reference) != "" {
		return reference
	}
	return strings.TrimSpace(persona.AvatarDataURI)
}

func (a *AgentRuntime) generateImageOnce(ctx context.Context, prompt string, grok bool, reference string) (toolResult, error) {
	if prompt == "" {
		return toolResult{}, errors.New("image prompt is required")
	}
	var imagePolicy struct {
		Enabled bool   `json:"enabled"`
		Model   string `json:"model"`
	}
	if err := a.integrationConfig(ctx, "image_policy", &imagePolicy); err != nil || !imagePolicy.Enabled {
		return toolResult{}, errors.New("image generation is disabled")
	}
	type imageTarget struct {
		apiBase string
		model   string
		key     string
		timeout time.Duration
	}
	targets := make([]imageTarget, 0, 2)
	if grok {
		var policy struct {
			Enabled            bool     `json:"enabled"`
			APIBase            string   `json:"apiBase"`
			CredentialRef      string   `json:"credentialRef"`
			MediaConnectionIDs []string `json:"mediaConnectionIds"`
			ImageModel         string   `json:"imageModel"`
			ImageEditModel     string   `json:"imageEditModel"`
		}
		if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil || !policy.Enabled {
			return toolResult{}, errors.New("Grok image generation is disabled")
		}
		if strings.TrimSpace(policy.ImageEditModel) == "" {
			policy.ImageEditModel = "grok-imagine-image"
		}
		candidates, err := a.mediaProviderCandidates(ctx, policy.MediaConnectionIDs, "image_generation")
		if err != nil {
			return toolResult{}, err
		}
		for _, candidate := range candidates {
			model := candidate.Model
			if strings.TrimSpace(reference) != "" && strings.TrimSpace(policy.ImageEditModel) != "" {
				model = policy.ImageEditModel
			}
			if key := getenv(candidate.Connection.CredentialRef); key != "" {
				targets = append(targets, imageTarget{
					apiBase: candidate.Connection.APIBase,
					model:   model,
					key:     key,
					timeout: imageProviderAttemptTimeout(candidate.Connection.TimeoutSeconds, true),
				})
			}
		}
		if len(targets) == 0 {
			model := policy.ImageModel
			if strings.TrimSpace(reference) != "" && strings.TrimSpace(policy.ImageEditModel) != "" {
				model = policy.ImageEditModel
			}
			targets = append(targets, imageTarget{
				apiBase: policy.APIBase,
				model:   model,
				key:     a.grokCredential(policy.CredentialRef),
				timeout: imageProviderAttemptTimeout(0, true),
			})
		}
	} else {
		provider, err := a.providerPolicy(ctx)
		if err != nil {
			return toolResult{}, err
		}
		targets = append(targets, imageTarget{
			apiBase: provider.APIBase,
			model:   imagePolicy.Model,
			key:     a.imageAPIKey,
			timeout: imageProviderAttemptTimeout(0, false),
		})
	}
	var lastErr error
	for _, target := range targets {
		if target.key == "" {
			lastErr = errors.New("image generation is not configured")
			continue
		}
		base, err := secureServiceBase(target.apiBase)
		if err != nil {
			lastErr = err
			continue
		}
		var response struct {
			Data []struct {
				Base64 string `json:"b64_json"`
				URL    string `json:"url"`
			} `json:"data"`
		}
		payload := map[string]any{"model": target.model, "prompt": fitImageProviderPrompt(prompt), "n": 1, "response_format": "b64_json"}
		endpoint := base + "/images/generations"
		if grok && strings.TrimSpace(reference) != "" {
			endpoint = base + "/images/edits"
			payload["image"] = map[string]string{"url": strings.TrimSpace(reference)}
		}
		attemptContext, cancelAttempt := context.WithTimeout(ctx, target.timeout)
		err = a.postProviderJSON(attemptContext, endpoint, target.key, payload, &response)
		if err != nil {
			cancelAttempt()
			lastErr = err
			continue
		}
		if len(response.Data) == 0 {
			cancelAttempt()
			lastErr = errors.New("image provider returned no image")
			continue
		}
		var image []byte
		if response.Data[0].Base64 != "" {
			image, err = base64.StdEncoding.DecodeString(response.Data[0].Base64)
		} else {
			image, err = a.downloadImage(attemptContext, response.Data[0].URL, base)
		}
		cancelAttempt()
		if err != nil {
			lastErr = err
			continue
		}
		attachment, err := a.storeImage(image)
		if err != nil {
			return toolResult{}, err
		}
		encoded, _ := json.Marshal(map[string]any{"ok": true, "result": "image_generated"})
		return toolResult{Content: string(encoded), Attachments: []agentAttachment{attachment}}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("image generation is not configured")
	}
	return toolResult{}, lastErr
}

func fitImageProviderPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if len([]byte(prompt)) <= maxImagePromptBytes {
		return prompt
	}
	const tailBytes = 1200
	const separator = "\n"
	head := utf8Prefix(prompt, maxImagePromptBytes-tailBytes-len(separator))
	tail := utf8Suffix(prompt, tailBytes)
	return strings.TrimSpace(head) + separator + strings.TrimSpace(tail)
}

func utf8Prefix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	used := 0
	result := make([]rune, 0, len(value))
	for _, character := range value {
		size := len(string(character))
		if used+size > maxBytes {
			break
		}
		result = append(result, character)
		used += size
	}
	return string(result)
}

func utf8Suffix(value string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	runes := []rune(value)
	used := 0
	start := len(runes)
	for start > 0 {
		size := len(string(runes[start-1]))
		if used+size > maxBytes {
			break
		}
		start--
		used += size
	}
	return string(runes[start:])
}

func (a *AgentRuntime) imageTaskTimeout(ctx context.Context, fallbackSeconds int) time.Duration {
	seconds := fallbackSeconds
	var policy struct {
		TimeoutSeconds int `json:"timeoutSeconds"`
	}
	if err := a.integrationConfig(ctx, "image_policy", &policy); err == nil && policy.TimeoutSeconds > 0 {
		seconds = policy.TimeoutSeconds
	}
	if seconds <= 0 {
		seconds = 600
	}
	if seconds > 900 {
		seconds = 900
	}
	return time.Duration(seconds) * time.Second
}

func imageProviderAttemptTimeout(configuredSeconds int, grok bool) time.Duration {
	limit := defaultImageAttemptTimeout
	if grok {
		limit = defaultGrokImageAttemptTimeout
	}
	configured := time.Duration(configuredSeconds) * time.Second
	if configured <= 0 || configured > limit {
		return limit
	}
	return configured
}

func (a *AgentRuntime) integrationConfig(_ context.Context, id string, target any) error {
	if a == nil || a.configStore == nil {
		return errors.New("integration config store is unavailable")
	}
	raw, err := a.configStore.integrationRaw(id)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func secureServiceBase(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("tool service URL is invalid")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !mgmtPrivateHost(parsed.Hostname())) {
		return "", errors.New("tool service URL must use HTTPS or an approved private HTTP host")
	}
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (a *AgentRuntime) postProviderJSON(ctx context.Context, endpoint, key string, payload, target any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json, text/event-stream")
	started := time.Now()
	response, err := a.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
		return &providerHTTPError{StatusCode: response.StatusCode, Message: string(body)}
	}
	if strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		switch value := target.(type) {
		case *chatCompletion:
			return decodeChatCompletionStream(response.Body, value, started)
		case *xaiResponsesResponse:
			return decodeXAIResponsesStream(response.Body, value)
		default:
			return errors.New("streaming provider response is unsupported for this request")
		}
	}
	return json.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(target)
}

func decodeChatCompletionStream(body io.Reader, completion *chatCompletion, started time.Time) error {
	type streamToolCallDelta struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type streamDelta struct {
		Content   string                `json:"content"`
		ToolCalls []streamToolCallDelta `json:"tool_calls"`
	}
	type streamChunk struct {
		Choices []struct {
			Delta streamDelta `json:"delta"`
		} `json:"choices"`
		Usage *chatUsage `json:"usage"`
	}
	scanner := bufio.NewScanner(io.LimitReader(body, maxToolBody))
	scanner.Buffer(make([]byte, 64*1024), maxToolBody)
	var content strings.Builder
	toolCalls := []chatToolCall{}
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil {
				completion.Usage = *chunk.Usage
			}
			continue
		}
		if chunk.Usage != nil {
			completion.Usage = *chunk.Usage
		}
		if completion.FirstTokenMS == 0 {
			completion.FirstTokenMS = time.Since(started).Milliseconds()
			if completion.FirstTokenMS == 0 {
				completion.FirstTokenMS = 1
			}
		}
		content.WriteString(chunk.Choices[0].Delta.Content)
		for _, delta := range chunk.Choices[0].Delta.ToolCalls {
			for len(toolCalls) <= delta.Index {
				toolCalls = append(toolCalls, chatToolCall{})
			}
			call := &toolCalls[delta.Index]
			if delta.ID != "" {
				call.ID = delta.ID
			}
			if delta.Type != "" {
				call.Type = delta.Type
			}
			call.Function.Name += delta.Function.Name
			call.Function.Arguments += delta.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	completion.Choices = make([]struct {
		Message struct {
			Role      string         `json:"role"`
			Content   string         `json:"content"`
			ToolCalls []chatToolCall `json:"tool_calls"`
		} `json:"message"`
	}, 1)
	completion.Choices[0].Message.Role = "assistant"
	completion.Choices[0].Message.Content = content.String()
	completion.Choices[0].Message.ToolCalls = toolCalls
	return nil
}

func responseText(response map[string]any) string {
	if text, ok := response["output_text"].(string); ok {
		return strings.TrimSpace(text)
	}
	output, _ := response["output"].([]any)
	for _, item := range output {
		object, _ := item.(map[string]any)
		content, _ := object["content"].([]any)
		for _, part := range content {
			value, _ := part.(map[string]any)
			if text, ok := value["text"].(string); ok && strings.TrimSpace(text) != "" {
				return strings.TrimSpace(text)
			}
		}
	}
	return ""
}

func (a *AgentRuntime) downloadImage(ctx context.Context, rawURL, providerBase string) ([]byte, error) {
	base, err := url.Parse(providerBase)
	if err != nil || base.Host == "" {
		return nil, errors.New("image provider URL is invalid")
	}
	reference, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || reference.User != nil || reference.Fragment != "" {
		return nil, errors.New("image URL is invalid")
	}
	if reference.IsAbs() && reference.Scheme == "http" && base.Scheme == "http" && mgmtPrivateHost(base.Hostname()) {
		referenceHost := strings.Trim(strings.ToLower(reference.Hostname()), "[]")
		referenceIP := net.ParseIP(referenceHost)
		if referenceHost == "localhost" || referenceIP != nil && referenceIP.IsLoopback() {
			reference.Scheme = base.Scheme
			reference.Host = base.Host
		}
	}
	parsed := base.ResolveReference(reference)
	privateHTTP := parsed.Scheme == "http" && base.Scheme == "http" &&
		mgmtPrivateHost(base.Hostname()) && sameOrigin(base, parsed)
	if parsed.Host == "" || parsed.User != nil || (parsed.Scheme != "https" && !privateHTTP) {
		return nil, errors.New("image URL must use HTTPS or the configured private provider origin")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("image download returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil || len(data) == 0 || len(data) > maxImageBytes {
		return nil, errors.New("image download is invalid")
	}
	return data, nil
}

func (a *AgentRuntime) storeImage(data []byte) (agentAttachment, error) {
	extension, mimeType := imageFormat(data)
	if extension == "" || len(data) > maxImageBytes || strings.TrimSpace(a.mediaDir) == "" {
		return agentAttachment{}, errors.New("image data is invalid")
	}
	if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
		return agentAttachment{}, err
	}
	id, err := randomID("image")
	if err != nil {
		return agentAttachment{}, err
	}
	name := id + extension
	temporary, err := os.CreateTemp(a.mediaDir, ".image-*.tmp")
	if err != nil {
		return agentAttachment{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(data)
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return agentAttachment{}, err
	}
	destination := filepath.Join(a.mediaDir, name)
	if err := os.Rename(temporaryName, destination); err != nil {
		return agentAttachment{}, err
	}
	return agentAttachment{
		Kind: "image", LocalPath: mediaMountRoot + "/" + name, Name: name, MimeType: mimeType,
	}, nil
}

func imageFormat(data []byte) (string, string) {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte("\x89PNG\r\n\x1a\n")):
		return ".png", "image/png"
	case len(data) >= 3 && bytes.Equal(data[:3], []byte("\xff\xd8\xff")):
		return ".jpg", "image/jpeg"
	case len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp", "image/webp"
	case len(data) >= 6 && (bytes.Equal(data[:6], []byte("GIF87a")) || bytes.Equal(data[:6], []byte("GIF89a"))):
		return ".gif", "image/gif"
	default:
		return "", ""
	}
}
