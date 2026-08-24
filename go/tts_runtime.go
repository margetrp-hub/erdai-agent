package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultTTSTimeoutSeconds = 60
	maxTTSAudioBytes         = 12 * 1024 * 1024
	defaultTTSVoiceID        = "eve"
	defaultTTSLanguage       = "zh"
)

var defaultTTSTriggerKeywords = []string{
	"\u8bed\u97f3",
	"\u53d1\u8bed\u97f3",
	"\u7528\u8bed\u97f3",
	"\u8bed\u97f3\u56de\u590d",
	"\u8bf4\u53e5\u8bdd",
	"\u7528\u58f0\u97f3",
	"tts",
}

type grokTTSPolicy struct {
	Enabled         bool     `json:"ttsEnabled"`
	APIBase         string   `json:"ttsApiBase"`
	CredentialRef   string   `json:"ttsCredentialRef"`
	VoiceID         string   `json:"ttsVoiceId"`
	Language        string   `json:"ttsLanguage"`
	PersonaIDs      []string `json:"ttsPersonaIds"`
	Always          bool     `json:"ttsAlways"`
	TriggerKeywords []string `json:"ttsTriggerKeywords"`
	MaxChars        int      `json:"ttsMaxChars"`
	TimeoutSeconds  int      `json:"ttsTimeoutSeconds"`
}

func ttsRequested(message string, policy grokTTSPolicy) bool {
	if policy.Always {
		return true
	}
	message = strings.ToLower(strings.TrimSpace(message))
	if message == "" {
		return false
	}
	keywords := policy.TriggerKeywords
	if len(keywords) == 0 {
		keywords = defaultTTSTriggerKeywords
	}
	for _, keyword := range keywords {
		if value := strings.ToLower(strings.TrimSpace(keyword)); value != "" &&
			strings.Contains(message, value) {
			return true
		}
	}
	return false
}

func ttsPersonaAllowed(personaID string, policy grokTTSPolicy) bool {
	personaID = strings.TrimSpace(personaID)
	if personaID == "" {
		return false
	}
	personas := policy.PersonaIDs
	if len(personas) == 0 {
		personas = []string{"doubao", "xiaoman"}
	}
	for _, candidate := range personas {
		if strings.EqualFold(personaID, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func (a *AgentRuntime) maybeAttachPersonaSpeech(
	ctx context.Context,
	run runRecord,
	message string,
	reply agentReply,
) agentReply {
	if a == nil || a.configStore == nil || strings.TrimSpace(reply.Text) == "" {
		return reply
	}
	var policy grokTTSPolicy
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil ||
		!policy.Enabled || !ttsPersonaAllowed(run.PersonaID, policy) ||
		!ttsRequested(message, policy) {
		return reply
	}
	for _, attachment := range reply.Attachments {
		if strings.EqualFold(strings.TrimSpace(attachment.Kind), "audio") {
			return reply
		}
	}
	maxChars := policy.MaxChars
	if maxChars <= 0 || maxChars > 2000 {
		maxChars = 240
	}
	text := truncateTTSText(strings.TrimSpace(reply.Text), maxChars)
	if text == "" {
		return reply
	}
	timeoutSeconds := policy.TimeoutSeconds
	if timeoutSeconds <= 0 || timeoutSeconds > 180 {
		timeoutSeconds = defaultTTSTimeoutSeconds
	}
	ttsContext, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()
	attachment, err := a.synthesizeGrokTTS(ttsContext, run, text, policy)
	if err != nil {
		log.Printf("tts skipped for run %s: %v", run.ID, err)
		return reply
	}
	reply.Attachments = append(reply.Attachments, attachment)
	return reply
}

func truncateTTSText(value string, maxChars int) string {
	value = strings.TrimSpace(value)
	if maxChars <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= maxChars {
		return value
	}
	return strings.TrimSpace(string(runes[:maxChars]))
}

func ttsEndpoint(raw string) (string, error) {
	base, err := secureServiceBase(raw)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(strings.ToLower(base), "/v1") {
		return base + "/tts", nil
	}
	return base + "/v1/tts", nil
}

func stableTTSRequestID(run runRecord) string {
	identity := strings.TrimSpace(run.EventID)
	if identity == "" {
		identity = strings.TrimSpace(run.ID)
	}
	sum := sha256.Sum256([]byte("erdai-tts-v1:" + identity))
	return "erdai-" + hex.EncodeToString(sum[:12])
}

func (a *AgentRuntime) synthesizeGrokTTS(
	ctx context.Context,
	run runRecord,
	text string,
	policy grokTTSPolicy,
) (agentAttachment, error) {
	text = truncateTTSText(text, 2000)
	if text == "" {
		return agentAttachment{}, errors.New("tts text is empty")
	}
	if strings.TrimSpace(a.mediaDir) == "" {
		return agentAttachment{}, errors.New("tts media directory is not configured")
	}
	key := a.grokCredential(policy.CredentialRef)
	if key == "" {
		return agentAttachment{}, errors.New("tts credential is not configured")
	}
	apiBase := strings.TrimSpace(policy.APIBase)
	if apiBase == "" {
		apiBase = "https://api.x.ai/v1"
	}
	endpoint, err := ttsEndpoint(apiBase)
	if err != nil {
		return agentAttachment{}, err
	}
	voiceID := strings.TrimSpace(policy.VoiceID)
	if voiceID == "" {
		voiceID = defaultTTSVoiceID
	}
	language := strings.TrimSpace(policy.Language)
	if language == "" {
		language = defaultTTSLanguage
	}
	body, err := json.Marshal(map[string]any{
		"text": text, "voice_id": voiceID, "language": language,
	})
	if err != nil {
		return agentAttachment{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return agentAttachment{}, err
	}
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "audio/mpeg, application/octet-stream;q=0.9")
	request.Header.Set("X-Client-Request-ID", stableTTSRequestID(run))
	client := a.client
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return agentAttachment{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		return agentAttachment{}, fmt.Errorf("tts provider returned HTTP %d: %s",
			response.StatusCode, strings.TrimSpace(string(detail)))
	}
	if contentType := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Type"))); contentType != "" &&
		!strings.HasPrefix(contentType, "audio/") && !strings.HasPrefix(contentType, "application/octet-stream") {
		return agentAttachment{}, fmt.Errorf("tts provider returned unsupported content type %q", contentType)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxTTSAudioBytes+1))
	if err != nil {
		return agentAttachment{}, err
	}
	if len(data) == 0 {
		return agentAttachment{}, errors.New("tts provider returned empty audio")
	}
	if len(data) > maxTTSAudioBytes {
		return agentAttachment{}, errors.New("tts audio exceeds the size limit")
	}
	if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
		return agentAttachment{}, err
	}
	id, err := randomID("voice")
	if err != nil {
		return agentAttachment{}, err
	}
	name := id + ".mp3"
	temporary, err := os.CreateTemp(a.mediaDir, ".voice-*.tmp")
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
		Kind: "audio", LocalPath: mediaMountRoot + "/" + name,
		Name: name, MimeType: "audio/mpeg",
	}, nil
}
