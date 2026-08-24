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
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	videoUnavailableReply                = "视频通道暂时没额度。"
	defaultVideoPollInterval             = 4 * time.Second
	defaultVideoPollMaxTransientFailures = 450
	defaultVideoCreateMaxAttempts        = 3
	defaultVideoCreateRetryDelay         = 250 * time.Millisecond
	maxVideoGenerationDuration           = 45 * time.Minute
	maxVideoBytes                        = 256 * 1024 * 1024
	// Grok Console rejects prompts over 4096 bytes. Keep a little headroom for
	// provider-side normalization while preserving both the character profile
	// and the user's requested scene.
	maxVideoPromptBytes = 4000
)

func videoUnavailableReplyOptions() []string {
	return []string{
		videoUnavailableReply,
		"这会儿拍不了。",
		"今天的视频通道歇着了。",
		"成片这会儿接不上。",
	}
}

func imageUnavailableReplyOptions() []string {
	return []string{
		"这会儿画不了，通道没接上。",
		"生图这条线现在断着，先不接单。",
		"画笔这会儿借不到，晚点再说。",
		"图片通道还没醒，先欠着。",
	}
}

func isVideoUnavailableReply(text string) bool {
	text = strings.TrimSpace(text)
	for _, candidate := range videoUnavailableReplyOptions() {
		if text == candidate {
			return true
		}
	}
	return false
}

var transientVideoStatuses = map[int]bool{
	http.StatusNotFound:            true,
	http.StatusRequestTimeout:      true,
	http.StatusConflict:            true,
	http.StatusTooEarly:            true,
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

type videoHTTPError struct {
	StatusCode int
}

func (e *videoHTTPError) Error() string {
	return fmt.Sprintf("video provider returned HTTP %d", e.StatusCode)
}

type videoTask struct {
	ID     string
	Status string
	URL    string
}

type grokVideoPolicy struct {
	Enabled             bool     `json:"enabled"`
	APIBase             string   `json:"apiBase"`
	CredentialRef       string   `json:"credentialRef"`
	MediaConnectionIDs  []string `json:"mediaConnectionIds"`
	VideoModel          string   `json:"videoModel"`
	VideoTimeoutSeconds int      `json:"videoTimeoutSeconds"`
}

type videoGenerationOptions struct {
	AspectRatio string
	Resolution  string
	Duration    int
}

func videoGenerationOptionsForPrompt(prompt string) videoGenerationOptions {
	normalized := strings.ToLower(strings.TrimSpace(prompt))
	normalized = strings.ReplaceAll(normalized, "\uff1a", ":")
	normalized = strings.ReplaceAll(normalized, " ", "")
	options := videoGenerationOptions{
		AspectRatio: "16:9",
		Resolution:  "720p",
		Duration:    6,
	}
	switch {
	case strings.Contains(normalized, "9:16") ||
		strings.Contains(normalized, "\u7ad6\u5c4f") ||
		strings.Contains(normalized, "\u7ad6\u7248") ||
		strings.Contains(normalized, "portrait"):
		options.AspectRatio = "9:16"
	case strings.Contains(normalized, "1:1") ||
		strings.Contains(normalized, "\u65b9\u5f62") ||
		strings.Contains(normalized, "\u6b63\u65b9\u5f62") ||
		strings.Contains(normalized, "square"):
		options.AspectRatio = "1:1"
	case strings.Contains(normalized, "4:3"):
		options.AspectRatio = "4:3"
	case strings.Contains(normalized, "3:4"):
		options.AspectRatio = "3:4"
	case strings.Contains(normalized, "16:9") ||
		strings.Contains(normalized, "\u6a2a\u5c4f") ||
		strings.Contains(normalized, "\u6a2a\u7248") ||
		strings.Contains(normalized, "landscape"):
		options.AspectRatio = "16:9"
	}
	switch {
	case strings.Contains(normalized, "1080p"):
		options.Resolution = "1080p"
	case strings.Contains(normalized, "480p"):
		options.Resolution = "480p"
	case strings.Contains(normalized, "720p"):
		options.Resolution = "720p"
	}
	return options
}

func applyVideoGenerationOptions(
	request map[string]any,
	prompt string,
	model string,
) error {
	options := videoGenerationOptionsForPrompt(prompt)
	if options.Resolution == "1080p" &&
		!strings.Contains(strings.ToLower(strings.TrimSpace(model)), "1.5") {
		return errors.New("1080p requires a Grok video 1.5 model; use 720p or switch the video model")
	}
	request["aspect_ratio"] = options.AspectRatio
	request["resolution"] = options.Resolution
	request["duration"] = options.Duration
	return nil
}

func (a *AgentRuntime) generateVideo(ctx context.Context, run runRecord, prompt string) (toolResult, error) {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return toolResult{}, errors.New("video prompt is required")
	}
	var imagePolicy struct {
		Enabled bool `json:"enabled"`
	}
	if err := a.integrationConfig(ctx, "image_policy", &imagePolicy); err != nil || !imagePolicy.Enabled {
		return toolResult{}, errors.New("media generation is disabled")
	}
	var policy grokVideoPolicy
	if err := a.integrationConfig(ctx, "grok_policy", &policy); err != nil || !policy.Enabled {
		return toolResult{}, errors.New("Grok video generation is disabled")
	}
	type videoTarget struct {
		base, credential, model string
	}
	targets := make([]videoTarget, 0, 2)
	candidates, candidatesErr := a.mediaProviderCandidates(ctx, policy.MediaConnectionIDs, "video_generation")
	if candidatesErr == nil {
		for _, candidate := range candidates {
			if credential := getenv(candidate.Connection.CredentialRef); credential != "" {
				base, baseErr := secureServiceBase(candidate.Connection.APIBase)
				if baseErr == nil {
					targets = append(targets, videoTarget{base: base, credential: credential, model: candidate.Model})
				}
			}
		}
	}
	if len(targets) == 0 {
		credential := a.grokCredential(policy.CredentialRef)
		base, err := secureServiceBase(policy.APIBase)
		if err != nil {
			return toolResult{}, err
		}
		if credential == "" || strings.TrimSpace(policy.VideoModel) == "" {
			return toolResult{}, errors.New("Grok video generation is not configured")
		}
		targets = append(targets, videoTarget{base: base, credential: credential, model: policy.VideoModel})
	}
	timeout := time.Duration(policy.VideoTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	if timeout > maxVideoGenerationDuration {
		timeout = maxVideoGenerationDuration
	}
	videoContext, timeoutCancel := context.WithTimeout(ctx, timeout)
	videoContext, lifecycleCancel := a.trackVideoContext(videoContext)
	defer func() {
		lifecycleCancel()
		timeoutCancel()
	}()

	requestID := stableVideoRequestID(run)
	request := map[string]any{"prompt": fitVideoProviderPrompt(prompt)}
	if reference := a.personaAvatarDataURI(videoContext, run.PersonaID, prompt, false); reference != "" {
		request["reference_images"] = []map[string]string{{"url": reference}}
	}
	var base, credential string
	var task videoTask
	var lastErr error
	var payload map[string]any
	var err error
	for _, target := range targets {
		candidateRequest := make(map[string]any, len(request)+1)
		for key, value := range request {
			candidateRequest[key] = value
		}
		candidateRequest["model"] = target.model
		if optionsErr := applyVideoGenerationOptions(candidateRequest, prompt, target.model); optionsErr != nil {
			lastErr = optionsErr
			continue
		}
		payload, createErr := a.createVideoTask(videoContext, target.base+"/videos/generations", candidateRequest, requestID, target.credential)
		if createErr != nil {
			lastErr = createErr
			continue
		}
		task = normalizeVideoTask(payload)
		if task.ID == "" {
			lastErr = errors.New("video provider returned no task ID")
			continue
		}
		base, credential = target.base, target.credential
		break
	}
	if task.ID == "" && lastErr != nil {
		return toolResult{}, lastErr
	}
	if task.ID == "" {
		return toolResult{}, errors.New("video provider returned no task ID")
	}
	// Progress is announced only now, after the provider accepted the task and
	// returned a real task ID. Announcing before this point is an unbacked
	// promise: creation failures would leave a "马上给你看" with nothing behind it.
	a.emitMediaTaskProgress(ctx, run, "grok_generate_video", prompt)
	_ = a.recordRunStage(run.ID, "media_task_created", time.Now(), map[string]any{
		"kind": "video", "taskId": task.ID,
	})

	pollInterval := a.videoPollInterval
	if pollInterval <= 0 {
		pollInterval = defaultVideoPollInterval
	}
	maxTransientFailures := a.videoPollMaxTransientFailures
	if maxTransientFailures <= 0 {
		maxTransientFailures = defaultVideoPollMaxTransientFailures
	}
	transientFailures := 0
	for task.Status != "completed" && task.Status != "failed" {
		if err := waitVideoPoll(videoContext, pollInterval); err != nil {
			return toolResult{}, err
		}
		payload, err = a.videoJSON(
			videoContext,
			http.MethodGet,
			base+"/videos/"+url.PathEscape(task.ID),
			nil,
			requestID,
			credential,
		)
		if err != nil {
			var statusError *videoHTTPError
			if errors.As(err, &statusError) && transientVideoStatuses[statusError.StatusCode] {
				transientFailures++
				if transientFailures <= maxTransientFailures {
					continue
				}
			}
			return toolResult{}, err
		}
		transientFailures = 0
		next := normalizeVideoTask(payload)
		if next.ID == "" {
			next.ID = task.ID
		}
		if next.Status != task.Status {
			_ = a.recordRunStage(run.ID, "media_poll", time.Now(), map[string]any{
				"kind": "video", "taskId": next.ID, "status": next.Status,
			})
		}
		task = next
	}
	if task.Status == "failed" {
		return toolResult{}, errors.New("video provider reported failure")
	}

	assetURL := task.URL
	if assetURL == "" {
		assetURL = base + "/videos/" + url.PathEscape(task.ID) + "/content"
	}
	attachment, err := a.downloadAndStoreVideoWithKey(videoContext, base, assetURL, requestID, credential)
	// Some compatible video gateways return a loopback or container-local
	// asset URL. Keep the SSRF guard, but use the provider's authenticated
	// content route when the task ID is trusted and the returned URL is not.
	if err != nil && task.ID != "" && assetURL != base+"/videos/"+url.PathEscape(task.ID)+"/content" {
		fallbackURL := base + "/videos/" + url.PathEscape(task.ID) + "/content"
		attachment, err = a.downloadAndStoreVideoWithKey(videoContext, base, fallbackURL, requestID, credential)
	}
	if err != nil {
		return toolResult{}, err
	}
	_ = a.recordRunStage(run.ID, "media_download_completed", time.Now(), map[string]any{
		"kind": "video", "taskId": task.ID, "name": attachment.Name,
	})
	encoded, _ := json.Marshal(map[string]any{"ok": true, "result": "video_generated"})
	return toolResult{Content: string(encoded), Attachments: []agentAttachment{attachment}}, nil
}

// emitMediaTaskProgress announces a media task exactly once per run, and only
// once the provider actually accepted the task. enqueueDelivery deduplicates
// progress rows, so a second announcement from another path is a no-op.
func (a *AgentRuntime) emitMediaTaskProgress(ctx context.Context, run runRecord, toolName, message string) {
	var messagePolicy runtimeMessagePolicy
	if err := a.integrationConfig(ctx, "message_policy", &messagePolicy); err != nil {
		return
	}
	call := chatToolCall{}
	call.Function.Name = toolName
	progress := a.progressMessageForRun(ctx, run, messagePolicy, []chatToolCall{call}, message)
	if progress == "" {
		return
	}
	_ = a.enqueueDelivery(run, agentReply{Text: progress}, "progress", "")
}

func fitVideoProviderPrompt(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if len([]byte(prompt)) <= maxVideoPromptBytes {
		return prompt
	}
	const tailBytes = 1500
	const separator = "\n"
	headBytes := maxVideoPromptBytes - tailBytes - len(separator)
	if headBytes < 1 {
		headBytes = maxVideoPromptBytes / 2
	}
	head := utf8Prefix(prompt, headBytes)
	tail := utf8Suffix(prompt, tailBytes)
	return strings.TrimSpace(head) + separator + strings.TrimSpace(tail)
}

func (a *AgentRuntime) createVideoTask(
	ctx context.Context,
	endpoint string,
	payload any,
	requestID string,
	credential string,
) (map[string]any, error) {
	var lastErr error
	for attempt := 0; attempt < defaultVideoCreateMaxAttempts; attempt++ {
		result, err := a.videoJSON(ctx, http.MethodPost, endpoint, payload, requestID, credential)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !retryableVideoCreateError(err) || attempt+1 == defaultVideoCreateMaxAttempts {
			return nil, err
		}
		delay := defaultVideoCreateRetryDelay * time.Duration(1<<attempt)
		if err := waitVideoPoll(ctx, delay); err != nil {
			return nil, err
		}
	}
	return nil, lastErr
}

func retryableVideoCreateError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var statusError *videoHTTPError
	if errors.As(err, &statusError) {
		switch statusError.StatusCode {
		case http.StatusRequestTimeout, http.StatusConflict, http.StatusTooEarly,
			http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusBadGateway,
			http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return true
		default:
			return false
		}
	}
	var networkError interface{ Temporary() bool }
	return errors.As(err, &networkError) && networkError.Temporary()
}

func (a *AgentRuntime) activePersonaVideoPrompt(ctx context.Context, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if a == nil || a.configStore == nil || prompt == "" {
		return prompt
	}
	config, err := a.configStore.runtimeConfig()
	if err != nil {
		return prompt
	}
	persona, _, err := a.configStore.activePersonaAndWorldbook(config, prompt)
	if err != nil || persona == nil || strings.TrimSpace(persona.VisualDescription) == "" {
		return prompt
	}
	visualReferencePrompt := a.configStore.personaVisualReferencePrompt(persona.ID)
	referenceLine := ""
	if strings.TrimSpace(visualReferencePrompt) != "" {
		referenceLine = "角色参考资料：" + visualReferencePrompt
	}
	profile, _ := a.configStore.personaRuntimeProfile(persona.ID)
	visualOverrideLine := ""
	if strings.TrimSpace(profile.VisualPromptOverride) != "" {
		visualOverrideLine = "当前角色视觉覆盖：" + strings.TrimSpace(profile.VisualPromptOverride)
	}
	return strings.Join([]string{
		"保持当前角色卡是同一位人物，固定脸型、眼睛、发型、发色、年龄感和体态，不要换脸。",
		"主脸身份以当前角色的主参考图为准；参考视频只借鉴动作、镜头、光线、服装和氛围，不复制参考视频中的脸部、身份、声音或具体场景。",
		"角色外观基准：" + strings.TrimSpace(persona.VisualDescription),
		visualOverrideLine,
		referenceLine,
		"角色气质：聪明、可爱、带一点嘴硬和冷幽默；一次视频只安排一个主要动作和一组细微表情，动作之间保留自然停顿，避免夸张网红表演和无意义连续舞蹈。",
		"视频场景必须符合现实世界的季节、天气、地点、光线、服装和物理动作。",
		"用户场景要求：" + prompt,
	}, "\n")
}

func (a *AgentRuntime) personaVideoPromptForRun(ctx context.Context, run runRecord, prompt string) string {
	persona := a.personaForRun(run, prompt)
	if persona == nil || strings.TrimSpace(persona.VisualDescription) == "" {
		return strings.TrimSpace(prompt)
	}
	return personaVideoPrompt(prompt, persona)
}

func personaVideoPrompt(prompt string, persona *nativeActivePersona) string {
	if persona == nil || strings.TrimSpace(persona.VisualDescription) == "" {
		return strings.TrimSpace(prompt)
	}
	parts := []string{
		"保持当前角色卡是同一位人物，固定脸型、眼睛、发型、发色、年龄感和体态，不要换脸。",
		"主脸身份以当前角色的主参考图为准；参考视频只借鉴动作、镜头、光线、服装和氛围，不复制参考视频中的脸部、身份、声音或具体场景。",
		"角色外观基准：" + strings.TrimSpace(persona.VisualDescription),
	}
	if value := strings.TrimSpace(persona.VisualPromptOverride); value != "" {
		parts = append(parts, "当前角色视觉覆盖："+value)
	}
	if value := strings.TrimSpace(persona.VisualReferencePrompt); value != "" {
		parts = append(parts, "角色参考资料："+value)
	}
	parts = append(parts,
		"一次视频只安排一个主要动作和一组细微表情，动作之间保留自然停顿，避免夸张网红表演和无意义连续舞蹈。",
		"视频场景必须符合现实世界的季节、天气、地点、光线、服装和物理动作。",
		"用户场景要求："+strings.TrimSpace(prompt),
	)
	return strings.Join(parts, "\n")
}

func (a *AgentRuntime) trackVideoContext(ctx context.Context) (context.Context, context.CancelFunc) {
	tracked, cancel := context.WithCancel(ctx)
	if a == nil {
		return tracked, cancel
	}
	a.videoMu.Lock()
	if a.videoCancels == nil {
		a.videoCancels = make(map[uint64]context.CancelFunc)
	}
	a.videoCancelID++
	cancelID := a.videoCancelID
	a.videoCancels[cancelID] = cancel
	a.videoMu.Unlock()
	return tracked, func() {
		cancel()
		a.videoMu.Lock()
		delete(a.videoCancels, cancelID)
		a.videoMu.Unlock()
	}
}

func stableVideoRequestID(run runRecord) string {
	identity := strings.TrimSpace(run.EventID)
	if identity == "" {
		identity = strings.TrimSpace(run.ID)
	}
	sum := sha256.Sum256([]byte("erdai-video-v1:" + identity))
	return "erdai-" + hex.EncodeToString(sum[:12])
}

func (a *AgentRuntime) videoJSON(
	ctx context.Context,
	method string,
	endpoint string,
	payload any,
	requestID string,
	credential string,
) (map[string]any, error) {
	var body io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+credential)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Client-Request-ID", requestID)
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := a.doVideoRequest(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return nil, &videoHTTPError{StatusCode: response.StatusCode}
	}
	var result map[string]any
	if err := json.NewDecoder(io.LimitReader(response.Body, maxToolBody)).Decode(&result); err != nil {
		return nil, err
	}
	return result, nil
}

func (a *AgentRuntime) doVideoRequest(request *http.Request) (*http.Response, error) {
	client := *a.client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(request)
}

func normalizeVideoTask(payload map[string]any) videoTask {
	source := payload
	if data, ok := payload["data"].(map[string]any); ok {
		source = data
	}
	var dataItem map[string]any
	if items, ok := payload["data"].([]any); ok {
		for _, item := range items {
			candidate, _ := item.(map[string]any)
			if firstVideoString(candidate, "url", "video_url", "videoUrl") != "" || objectValue(candidate, "video") != nil {
				dataItem = candidate
				break
			}
		}
	}
	video := objectValue(source, "video")
	if video == nil {
		video = objectValue(payload, "video")
	}
	if video == nil && dataItem != nil {
		video = objectValue(dataItem, "video")
	}
	id := firstVideoString(source, "request_id", "requestId", "task_id", "taskId", "id", "video_id", "videoId")
	assetURL := firstVideoString(source, "url", "video_url", "videoUrl", "output_url", "outputUrl", "result_url", "resultUrl")
	if assetURL == "" {
		assetURL = firstVideoString(video, "url", "video_url", "videoUrl")
	}
	if assetURL == "" {
		assetURL = firstVideoString(dataItem, "url", "video_url", "videoUrl")
	}
	status := normalizeVideoStatus(firstVideoString(source, "status"))
	if status == "queued" && assetURL != "" {
		status = "completed"
	}
	return videoTask{ID: id, Status: status, URL: assetURL}
}

func objectValue(source map[string]any, key string) map[string]any {
	if source == nil {
		return nil
	}
	value, _ := source[key].(map[string]any)
	return value
}

func firstVideoString(source map[string]any, keys ...string) string {
	if source == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := source[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeVideoStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "completed", "succeeded", "success", "done", "finished":
		return "completed"
	case "failed", "fail", "error", "expired", "canceled", "cancelled":
		return "failed"
	case "processing", "running", "generating", "in_progress", "in-progress":
		return "in_progress"
	default:
		return "queued"
	}
}

func waitVideoPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (a *AgentRuntime) downloadAndStoreVideo(
	ctx context.Context,
	providerBase string,
	rawURL string,
	requestID string,
) (agentAttachment, error) {
	return a.downloadAndStoreVideoWithKey(ctx, providerBase, rawURL, requestID, a.grokAPIKey)
}

func (a *AgentRuntime) downloadAndStoreVideoWithKey(
	ctx context.Context,
	providerBase string,
	rawURL string,
	requestID string,
	credential string,
) (agentAttachment, error) {
	assetURL, err := resolveVideoAssetURL(providerBase, rawURL)
	if err != nil {
		return agentAttachment{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL.String(), nil)
	if err != nil {
		return agentAttachment{}, err
	}
	baseURL, _ := url.Parse(providerBase)
	if sameOrigin(baseURL, assetURL) {
		request.Header.Set("Authorization", "Bearer "+credential)
		request.Header.Set("X-Client-Request-ID", requestID)
	}
	request.Header.Set("Accept", "video/mp4, application/octet-stream;q=0.9")
	response, err := a.doVideoRequest(request)
	if err != nil {
		return agentAttachment{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return agentAttachment{}, &videoHTTPError{StatusCode: response.StatusCode}
	}
	if response.ContentLength > maxVideoBytes {
		return agentAttachment{}, errors.New("video asset exceeds the size limit")
	}
	contentType := strings.TrimSpace(response.Header.Get("Content-Type"))
	if contentType != "" {
		mediaType, _, parseErr := mime.ParseMediaType(contentType)
		if parseErr != nil || mediaType != "video/mp4" && mediaType != "application/octet-stream" {
			return agentAttachment{}, errors.New("video asset content type is unsupported")
		}
	}
	if strings.TrimSpace(a.mediaDir) == "" {
		return agentAttachment{}, errors.New("video media directory is not configured")
	}
	if err := os.MkdirAll(a.mediaDir, 0o700); err != nil {
		return agentAttachment{}, err
	}
	id, err := randomID("video")
	if err != nil {
		return agentAttachment{}, err
	}
	name := id + ".mp4"
	temporary, err := os.CreateTemp(a.mediaDir, ".video-*.tmp")
	if err != nil {
		return agentAttachment{}, err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err = temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return agentAttachment{}, err
	}

	limited := io.LimitReader(response.Body, maxVideoBytes+1)
	prefix := make([]byte, 12)
	if _, err = io.ReadFull(limited, prefix); err == nil && !bytes.Equal(prefix[4:8], []byte("ftyp")) {
		err = errors.New("video asset is not an MP4 file")
	}
	if err == nil {
		_, err = temporary.Write(prefix)
	}
	var copied int64
	if err == nil {
		copied, err = io.Copy(temporary, limited)
	}
	if err == nil && int64(len(prefix))+copied > maxVideoBytes {
		err = errors.New("video asset exceeds the size limit")
	}
	if err == nil {
		err = temporary.Sync()
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
		Kind: "video", LocalPath: mediaMountRoot + "/" + name, Name: name, MimeType: "video/mp4",
	}, nil
}

func resolveVideoAssetURL(providerBase string, raw string) (*url.URL, error) {
	base, err := url.Parse(providerBase)
	if err != nil {
		return nil, errors.New("video provider URL is invalid")
	}
	reference, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || reference.User != nil || reference.Fragment != "" {
		return nil, errors.New("video asset URL is invalid")
	}
	resolved := base.ResolveReference(reference)
	privateHTTP := resolved.Scheme == "http" && base.Scheme == "http" &&
		mgmtPrivateHost(base.Hostname()) && sameOrigin(base, resolved)
	if resolved.Host == "" || resolved.User != nil || (resolved.Scheme != "https" && !privateHTTP) {
		return nil, errors.New("video asset URL must use HTTPS or the configured private provider origin")
	}
	return resolved, nil
}

func sameOrigin(left *url.URL, right *url.URL) bool {
	return left != nil && right != nil && strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}
