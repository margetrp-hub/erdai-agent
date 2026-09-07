package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

type visualGenerationPlan struct {
	Version            int               `json:"version"`
	OperationID        string            `json:"operationId"`
	MediaType          string            `json:"mediaType"`
	Attempt            int               `json:"attempt"`
	AppearanceID       string            `json:"appearanceId,omitempty"`
	AppearanceRevision string            `json:"appearanceRevision,omitempty"`
	BindingRevision    string            `json:"bindingRevision,omitempty"`
	ReferenceDigest    string            `json:"referenceDigest,omitempty"`
	Reference          string            `json:"-"`
	Seed               uint64            `json:"seed"`
	UserPrompt         string            `json:"userPrompt"`
	Identity           string            `json:"identity,omitempty"`
	OutfitLength       string            `json:"outfitLength,omitempty"`
	Variables          map[string]string `json:"variables"`
	Prompt             string            `json:"prompt"`
	CreatedAt          string            `json:"createdAt"`
}

type visualAppearanceSnapshot struct {
	UserPrompt      string `json:"userPrompt"`
	AppearanceID    string `json:"appearanceId"`
	Revision        string `json:"revision"`
	BindingRevision string `json:"bindingRevision"`
	ReferenceDigest string `json:"referenceDigest"`
	Reference       string `json:"reference"`
	Identity        string `json:"identity"`
	OutfitLength    string `json:"outfitLength"`
}

// Serialize only plan allocation, never provider execution. Concurrent requests
// for one library must see each other's newly allocated variation.
var visualPlanMu sync.Mutex

func visualOperationID(run runRecord, kind, prompt string) string {
	signature := run.ID + "\x00" + kind
	if strings.TrimSpace(run.ID) == "" {
		signature += "\x00" + strings.TrimSpace(prompt)
	}
	digest := sha256.Sum256([]byte(signature))
	return "visual-" + hex.EncodeToString(digest[:16])
}

func visualPlanName(libraryID string) string {
	digest := sha256.Sum256([]byte(libraryID))
	return "visual_plan:" + hex.EncodeToString(digest[:12])
}

func (a *AgentRuntime) readVisualRecord(ctx context.Context, id string, target any) (bool, error) {
	var ciphertext []byte
	err := a.db.QueryRowContext(ctx, "SELECT output_cipher FROM agent_task_steps WHERE id = ? AND status = 'succeeded'", id).Scan(&ciphertext)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	plain, err := a.decrypt(ciphertext)
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(plain, target)
}

func (a *AgentRuntime) writeVisualRecord(run runRecord, name string, attempt int, input, output any) error {
	id, err := a.beginTaskStep(run.ID, "", "tool", name, attempt, input)
	if err != nil {
		return err
	}
	return a.finishTaskStep(id, "succeeded", "", output)
}

func (a *AgentRuntime) resolveVisualAppearance(ctx context.Context, run runRecord, prompt, kind string) (visualAppearanceSnapshot, error) {
	var snapshot visualAppearanceSnapshot
	if a == nil || a.configStore == nil || (kind == "image" && !nativeSelfImageRequestPattern.MatchString(prompt)) {
		return snapshot, nil
	}
	resolvedID := strings.TrimSpace(run.PersonaID)
	if resolvedID == "" {
		config, err := a.configStore.runtimeConfig()
		if err != nil {
			return snapshot, err
		}
		if config.ActivePersonaID != nil {
			resolvedID = strings.TrimSpace(*config.ActivePersonaID)
		}
	}
	if resolvedID == "" {
		return snapshot, nil
	}
	tx, err := a.configStore.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return snapshot, err
	}
	defer tx.Rollback()
	var sourcePersonaID string
	var enabled bool
	err = tx.QueryRowContext(ctx, `SELECT l.id, l.updated_at, pal.updated_at, l.visual_description,
		l.outfit_length, COALESCE(l.source_persona_id, ''), l.enabled FROM persona_appearance_libraries pal
		JOIN appearance_libraries l ON l.id = pal.library_id WHERE pal.persona_id = ?`, resolvedID).Scan(
		&snapshot.AppearanceID, &snapshot.Revision, &snapshot.BindingRevision, &snapshot.Identity,
		&snapshot.OutfitLength, &sourcePersonaID, &enabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !enabled) {
		return snapshot, errors.New("selected appearance library is unavailable; select an enabled library with an identity reference")
	}
	if err != nil {
		return snapshot, err
	}
	var storageName, mimeType, category, notes string
	err = tx.QueryRowContext(ctx, `SELECT storage_name, mime_type, category, prompt_notes FROM appearance_library_references
		WHERE library_id = ? AND enabled = 1 AND media_type = 'image'
		ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, snapshot.AppearanceID).Scan(&storageName, &mimeType, &category, &notes)
	// Existing migrated libraries explicitly own their source-persona references.
	// An unavailable reference never falls back to that persona's avatar.
	if errors.Is(err, sql.ErrNoRows) && sourcePersonaID != "" {
		err = tx.QueryRowContext(ctx, `SELECT storage_name, mime_type, category, prompt_notes FROM persona_visual_references
			WHERE persona_id = ? AND enabled = 1 AND media_type = 'image'
			ORDER BY is_primary DESC, sort_order, created_at LIMIT 1`, sourcePersonaID).Scan(&storageName, &mimeType, &category, &notes)
	}
	if errors.Is(err, sql.ErrNoRows) {
		return snapshot, errors.New("selected appearance library has no usable identity reference; generation was not submitted")
	}
	if err != nil {
		return snapshot, err
	}
	snapshot.Reference, err = a.configStore.readReferenceDataURI(storageName, mimeType)
	if err != nil {
		return snapshot, fmt.Errorf("selected appearance reference could not be read: %w", err)
	}
	if snapshot.Reference == "" {
		return snapshot, errors.New("selected appearance reference is invalid")
	}
	_, encodedReference, ok := strings.Cut(snapshot.Reference, ",")
	if !ok {
		return snapshot, errors.New("selected appearance reference is invalid")
	}
	referenceBytes, err := base64.StdEncoding.DecodeString(encodedReference)
	if err != nil {
		return snapshot, errors.New("selected appearance reference is invalid")
	}
	digest := sha256.Sum256(referenceBytes)
	snapshot.ReferenceDigest = hex.EncodeToString(digest[:])
	snapshot.Identity = normalizeBuiltinVisualIdentity(snapshot.Identity)
	if category == "identity" && strings.TrimSpace(notes) != "" {
		snapshot.Identity += "\n身份参考备注：" + strings.TrimSpace(notes)
	}
	return snapshot, tx.Commit()
}

func (a *AgentRuntime) prepareVisualGeneration(ctx context.Context, run runRecord, prompt, kind string, attempt int) (visualGenerationPlan, error) {
	visualPlanMu.Lock()
	defer visualPlanMu.Unlock()
	prompt = strings.TrimSpace(prompt)
	if prompt == "" || attempt < 0 || attempt > 1 {
		return visualGenerationPlan{}, errors.New("invalid visual generation request")
	}
	opID := visualOperationID(run, kind, prompt)
	persistent := a != nil && a.db != nil && run.ID != "" && a.taskGraphRunExists(run.ID)
	snapshotName := "visual_snapshot:" + opID
	snapshotInput := map[string]string{"operationId": opID}
	encoded, _ := json.Marshal(snapshotInput)
	snapshotID := taskStepID(run.ID, "tool", 0, snapshotName, string(encoded))
	var snapshot visualAppearanceSnapshot
	found := false
	var err error
	if persistent {
		found, err = a.readVisualRecord(ctx, snapshotID, &snapshot)
		if err != nil {
			return visualGenerationPlan{}, err
		}
		if found && snapshot.AppearanceID != "" && snapshot.Reference == "" {
			return visualGenerationPlan{}, errors.New("visual task metadata expired; start a new request instead of replaying the old generation")
		}
	}
	if !found {
		snapshot, err = a.resolveVisualAppearance(ctx, run, prompt, kind)
		if err != nil {
			return visualGenerationPlan{}, err
		}
		snapshot.UserPrompt = prompt
		if persistent {
			if err = a.writeVisualRecord(run, snapshotName, 0, snapshotInput, snapshot); err != nil {
				return visualGenerationPlan{}, err
			}
		}
	}
	if snapshot.UserPrompt != "" {
		prompt = snapshot.UserPrompt
	}
	name := visualPlanName(snapshot.AppearanceID)
	input := map[string]any{"operationId": opID, "attempt": attempt}
	encoded, _ = json.Marshal(input)
	id := taskStepID(run.ID, "tool", attempt, name, string(encoded))
	var plan visualGenerationPlan
	if persistent {
		found, err = a.readVisualRecord(ctx, id, &plan)
		if err != nil {
			return plan, err
		}
		if found {
			plan.Reference = snapshot.Reference
			return plan, a.validateVisualGeneration(ctx, run, plan)
		}
	}
	now := time.Now().UTC()
	plan = visualGenerationPlan{Version: 1, OperationID: opID, MediaType: kind, Attempt: attempt,
		AppearanceID: snapshot.AppearanceID, AppearanceRevision: snapshot.Revision, BindingRevision: snapshot.BindingRevision,
		ReferenceDigest: snapshot.ReferenceDigest, Reference: snapshot.Reference, Identity: snapshot.Identity,
		OutfitLength: snapshot.OutfitLength, UserPrompt: prompt, Seed: nextSelfieVariationSeed(prompt, snapshot.AppearanceID, now),
		CreatedAt: now.Format(time.RFC3339Nano)}
	history := []visualGenerationPlan{}
	if persistent && snapshot.AppearanceID != "" {
		history, err = a.recentVisualPlans(ctx, name, 8)
		if err != nil {
			return plan, err
		}
	}
	plan.Variables = map[string]string{}
	if snapshot.AppearanceID != "" {
		plan.Variables = allocateVisualVariables(prompt, now, plan.Seed, a.imageVisualDirectorPolicy(ctx), snapshot.OutfitLength, history)
	}
	plan.Prompt, err = compileVisualGenerationPrompt(plan, "")
	if err != nil {
		return plan, err
	}
	if err = a.validateVisualGeneration(ctx, run, plan); err != nil {
		return plan, err
	}
	if persistent {
		err = a.writeVisualRecord(run, name, attempt, input, plan)
	}
	return plan, err
}

func (a *AgentRuntime) validateVisualGeneration(ctx context.Context, run runRecord, plan visualGenerationPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if a != nil && a.db != nil {
		if err := a.ensureMediaTaskCurrent(ctx, run); err != nil {
			return err
		}
	}
	if plan.AppearanceID == "" {
		return nil
	}
	current, err := a.resolveVisualAppearance(ctx, run, plan.UserPrompt, plan.MediaType)
	if err != nil {
		return err
	}
	if current.AppearanceID != plan.AppearanceID || current.Revision != plan.AppearanceRevision ||
		current.BindingRevision != plan.BindingRevision || current.ReferenceDigest != plan.ReferenceDigest ||
		current.Identity != plan.Identity || current.OutfitLength != plan.OutfitLength {
		return fmt.Errorf("%w: appearance binding changed", errTaskSuperseded)
	}
	return nil
}

func (a *AgentRuntime) recentVisualPlans(ctx context.Context, name string, limit int) ([]visualGenerationPlan, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT output_cipher FROM agent_task_steps
		WHERE kind = 'tool' AND name = ? AND status = 'succeeded' ORDER BY created_at DESC LIMIT ?`, name, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []visualGenerationPlan{}
	for rows.Next() {
		var cipher []byte
		if err = rows.Scan(&cipher); err != nil {
			return nil, err
		}
		plain, decryptErr := a.decrypt(cipher)
		if decryptErr != nil {
			return nil, decryptErr
		}
		var plan visualGenerationPlan
		if err = json.Unmarshal(plain, &plan); err != nil {
			return nil, err
		}
		result = append(result, plan)
	}
	return result, rows.Err()
}

func (a *AgentRuntime) visualPlanTaskDetails(ctx context.Context, runID string) ([]visualGenerationPlan, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT output_cipher FROM agent_task_steps
		WHERE run_id = ? AND kind = 'tool' AND name LIKE 'visual_plan:%' AND status = 'succeeded' ORDER BY created_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []visualGenerationPlan{}
	for rows.Next() {
		var cipher []byte
		if err = rows.Scan(&cipher); err != nil {
			return nil, err
		}
		plain, decryptErr := a.decrypt(cipher)
		if decryptErr != nil {
			return nil, decryptErr
		}
		var plan visualGenerationPlan
		if err = json.Unmarshal(plain, &plan); err != nil {
			return nil, err
		}
		result = append(result, plan)
	}
	return result, rows.Err()
}

func (a *AgentRuntime) ensureRunVisualAppearanceCurrent(ctx context.Context, runID string) error {
	if strings.TrimSpace(runID) == "" {
		return nil
	}
	plans, err := a.visualPlanTaskDetails(ctx, runID)
	if err != nil || len(plans) == 0 {
		return err
	}
	run := runRecord{ID: runID}
	if err = a.db.QueryRowContext(ctx, "SELECT persona_id,agent_instance_id FROM agent_runs WHERE id=?", runID).Scan(&run.PersonaID, &run.AgentInstanceID); err != nil {
		return err
	}
	for _, plan := range plans {
		if err = a.validateVisualGeneration(ctx, run, plan); err != nil {
			return err
		}
	}
	return nil
}

func (a *AgentRuntime) pruneVisualGenerationMetadata(ctx context.Context, cutoff string) error {
	rows, err := a.db.QueryContext(ctx, `WITH ranked AS (
		SELECT id, ROW_NUMBER() OVER (PARTITION BY name ORDER BY created_at DESC) AS recent_rank
		FROM agent_task_steps WHERE kind='tool' AND name LIKE 'visual_plan:%' AND status='succeeded'
	) SELECT step.id, step.name, step.output_cipher, COALESCE(ranked.recent_rank,0)
		FROM agent_task_steps step JOIN agent_runs run ON run.id=step.run_id
		LEFT JOIN ranked ON ranked.id=step.id
		WHERE step.kind='tool' AND (step.name LIKE 'visual_plan:%' OR step.name LIKE 'visual_snapshot:%')
		AND step.updated_at < ? AND step.input_cipher IS NOT NULL
		AND run.state IN ('delivered','failed','cancelled')`, cutoff)
	if err != nil {
		return err
	}
	type update struct {
		id     string
		output []byte
	}
	updates := []update{}
	for rows.Next() {
		var id, name string
		var cipher []byte
		var rank int
		if err = rows.Scan(&id, &name, &cipher, &rank); err != nil {
			rows.Close()
			return err
		}
		if len(cipher) == 0 {
			continue
		}
		plain, decryptErr := a.decrypt(cipher)
		if decryptErr != nil {
			rows.Close()
			return decryptErr
		}
		if strings.HasPrefix(name, "visual_snapshot:") {
			var snapshot visualAppearanceSnapshot
			if err = json.Unmarshal(plain, &snapshot); err != nil {
				rows.Close()
				return err
			}
			snapshot.Reference, snapshot.Identity, snapshot.UserPrompt = "", "", ""
			plain, err = json.Marshal(snapshot)
		} else {
			var plan visualGenerationPlan
			if err = json.Unmarshal(plain, &plan); err != nil {
				rows.Close()
				return err
			}
			plan.UserPrompt, plan.Identity, plan.Prompt = "", "", ""
			if rank > 8 {
				plan.Variables = nil
			}
			plain, err = json.Marshal(plan)
		}
		if err != nil {
			rows.Close()
			return err
		}
		cipher, err = a.encrypt(plain)
		if err != nil {
			rows.Close()
			return err
		}
		updates = append(updates, update{id: id, output: cipher})
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	if err = rows.Close(); err != nil {
		return err
	}
	for _, item := range updates {
		if _, err = a.db.ExecContext(ctx, "UPDATE agent_task_steps SET input_cipher=NULL,output_cipher=? WHERE id=?", item.output, item.id); err != nil {
			return err
		}
	}
	return nil
}

func visualChoice(seed *uint64, values []string) string {
	*seed += 0x9e3779b97f4a7c15
	z := *seed
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	z ^= z >> 31
	return values[int(z%uint64(len(values)))]
}

var visualPrimaryColors = []string{"红色", "蓝色", "绿色", "黄色", "白色", "黑色", "银灰色", "粉色", "紫色", "青色", "橙色"}

var visualColorToken = regexp.MustCompile(`(?i)银灰色|红色|蓝色|绿色|黄色|白色|黑色|粉色|紫色|青色|橙色|\b(?:purple|blue|green|yellow|white|black|pink|orange|red)\b`)

func visualConstraintClauses(prompt string) []string {
	var split strings.Builder
	previous := 0
	for _, boundary := range visualDirectiveBoundary.FindAllStringIndex(prompt, -1) {
		split.WriteString(prompt[previous:boundary[0]])
		if !visualNegationSuffix.MatchString(prompt[previous:boundary[0]]) {
			split.WriteByte('\n')
		}
		split.WriteString(prompt[boundary[0]:boundary[1]])
		previous = boundary[1]
	}
	split.WriteString(prompt[previous:])
	return strings.FieldsFunc(strings.ToLower(split.String()), func(r rune) bool { return strings.ContainsRune("，。；！？,.;!?\n", r) })
}

func visualClauseNegated(prefix string) bool {
	return videoHasAny(prefix, "不要", "不穿", "不用", "别用", "别拍", "不拍", "不是", "禁止", "避免", "不喜欢", "拒绝", "no ", "not ", "without ", "avoid ", "don't ")
}

func explicitVisualColor(prompt string) (string, map[string]bool) {
	forbidden := map[string]bool{}
	positive := ""
	aliases := map[string]string{"purple": "紫色", "blue": "蓝色", "green": "绿色", "yellow": "黄色", "white": "白色", "black": "黑色", "pink": "粉色", "orange": "橙色", "red": "红色"}
	for _, clause := range visualConstraintClauses(prompt) {
		for _, match := range visualColorToken.FindAllStringIndex(clause, -1) {
			color := clause[match[0]:match[1]]
			if alias := aliases[color]; alias != "" {
				color = alias
			}
			prefix := clause[:match[0]]
			if videoHasAny(prefix, "总是", "老是", "永远", "一直", "固定", "每次都", "always", "every time") {
				continue
			}
			if visualClauseNegated(prefix) {
				forbidden[color] = true
			} else {
				delete(forbidden, color)
				positive = color
			}
		}
	}
	if forbidden[positive] {
		positive = ""
	}
	return positive, forbidden
}

func visualCameraChoice(prompt string, seed *uint64, configured []string) string {
	forbidden := map[string]bool{}
	explicit := ""
	for _, clause := range visualConstraintClauses(prompt) {
		kind := explicitSelfieType(clause)
		if kind == "" {
			continue
		}
		if visualClauseNegated(clause) {
			forbidden[kind] = true
			if videoHasAny(clause, "全身", "穿搭") {
				forbidden["全身生活照"] = true
				forbidden["全身穿搭照"] = true
				forbidden["镜面穿搭自拍"] = true
			}
		} else {
			explicit = kind
			delete(forbidden, kind)
		}
	}
	if explicit != "" && !forbidden[explicit] {
		return explicit
	}
	choices := []string{}
	for _, kind := range configured {
		if !forbidden[kind] {
			choices = append(choices, kind)
		}
	}
	if len(choices) == 0 {
		return "按用户机位要求，不增加冲突的默认构图"
	}
	return visualChoice(seed, choices)
}

func visualSceneSpecified(prompt string) bool {
	markers := []string{"咖啡店", "书店", "街边", "街角", "室内", "户外", "公园", "海边", "家里", "卧室", "舞蹈室", "练习室", "舞台", "阳台", "窗边", "楼梯", "办公室", "工作室", "教室", "厨房", "浴室", "客厅", "书房", "露台", "河边", "沙滩", "森林", "山顶", "操场", "stadium", "beach", "bedroom", "indoors", "outdoors", "studio", "park"}
	for _, clause := range visualConstraintClauses(prompt) {
		if visualClauseNegated(clause) {
			continue
		}
		for _, marker := range markers {
			if strings.Contains(clause, marker) {
				return true
			}
		}
	}
	return false
}

func allocateVisualVariables(prompt string, now time.Time, seed uint64, policy imageVisualDirectorPolicy, outfitLength string, history []visualGenerationPlan) map[string]string {
	variables := map[string]string{}
	if !policy.Enabled {
		return variables
	}
	local := now.In(visualDirectorLocation(policy.Timezone))
	color, forbidden := explicitVisualColor(prompt)
	if color == "" {
		for _, previous := range history[:min(2, len(history))] {
			forbidden[previous.Variables["primaryColor"]] = true
		}
		choices := []string{}
		for _, candidate := range visualPrimaryColors {
			if !forbidden[candidate] {
				choices = append(choices, candidate)
			}
		}
		if len(choices) == 0 {
			variables["variationReason"] = "用户颜色限制优先，不添加默认颜色"
		} else {
			color = visualChoice(&seed, choices)
		}
	}
	variables["primaryColor"] = color
	types := normalizeSelfieTypes(policy.SelfieTypes)
	if len(types) == 0 {
		types = defaultSelfieTypes
	}
	photoType := visualCameraChoice(prompt, &seed, types)
	variables["camera"] = photoType
	scenes := []string{"明亮玄关", "书店", "展览空间", "商场露台", "树影街边", "河畔步道", "咖啡店外摆", "城市街角", "公园步道", "室内窗边"}
	outfits := []string{"针织上衣配半裙", "衬衫配连衣裙", "轻薄外套配裙装", "修身上衣配裤装", "无袖上衣配高腰裙", "连体裙装"}
	if outfitLength == "short" {
		outfits = []string{"短款针织上衣配高腰短裙", "短款衬衫配短裤", "膝上连衣裙", "轻薄短外套配短裙", "无袖上衣配短裙裤"}
	}
	if outfitLength == "long" {
		outfits = []string{"衬衫配长裤", "针织上衣配长裙", "合身长款连衣裙", "轻薄外套配长裤"}
	}
	sceneSpecified := visualSceneSpecified(prompt)
	outfitSpecified := videoHasAny(normalizeVisualPrompt(prompt), "裙", "裤", "衬衫", "上衣", "夹克", "外套", "连体", "针织", "吊带", "长款", "短款", "dress", "jacket", "shirt", "pants", "skirt")
	scene, outfit := visualChoice(&seed, scenes), visualChoice(&seed, outfits)
	if !sceneSpecified && !outfitSpecified && len(history) > 0 {
		previous := history[0].Variables
		if scene == previous["scene"] && outfit == previous["outfit"] {
			for _, candidate := range scenes {
				if candidate != scene {
					scene = candidate
					break
				}
			}
		}
	}
	if sceneSpecified {
		scene = "按用户明确场景；未指定的场景细节随机变化"
	}
	if outfitSpecified {
		outfit = "按用户明确款式和长度；未指定的材质与细节随机变化"
	}
	variables["scene"], variables["outfit"] = scene, outfit
	variables["makeup"] = visualChoice(&seed, []string{"清透蜜桃淡妆", "自然通勤淡妆", "柔粉水光淡妆", "精致外出淡妆"})
	variables["action"] = visualAction(photoType, seed)
	variables["mood"] = visualMood(seed / 17)
	if videoHasAny(normalizeVisualPrompt(prompt), "妆", "素颜", "makeup") {
		variables["makeup"] = "按用户明确妆容；未指定细节随机变化"
	}
	if videoHasAny(normalizeVisualPrompt(prompt), "跳舞", "舞蹈", "跑", "走", "站", "坐", "回头", "举", "拿", "手势", "dance", "walk", "run", "pose") {
		variables["action"] = "按用户明确动作；未指定细节随机变化"
	}
	if videoHasAny(normalizeVisualPrompt(prompt), "笑", "哭", "开心", "难过", "生气", "表情", "smile", "expression") {
		variables["mood"] = "按用户明确神态；未指定细节随机变化"
	}
	if policy.UseTimeContext {
		variables["time"] = visualTimeBlock(local.Hour())
		variables["season"] = visualSeason(local.Month())
	}
	return variables
}

func compileVisualGenerationPrompt(plan visualGenerationPlan, correction string) (string, error) {
	promptLimit := maxImagePromptBytes
	if plan.MediaType == "video" {
		promptLimit = maxVideoPromptBytes
	}
	if plan.AppearanceID == "" && strings.TrimSpace(correction) == "" {
		if len(plan.UserPrompt) > promptLimit {
			return "", errors.New("explicit visual requirements exceed provider prompt limit; shorten the request")
		}
		return strings.TrimSpace(plan.UserPrompt), nil
	}
	mandatory := "用户本次明确要求（包含否定约束，优先于所有默认变量）：\n" + strings.TrimSpace(plan.UserPrompt)
	if plan.AppearanceID != "" {
		mandatory += "\n身份只按当前外观库和附带参考图，不按角色名字换脸。参考图只锁定脸、发型、年龄和体态，不锁定衣服、背景、颜色、动作或机位。固定身份：\n" + plan.Identity
	}
	if strings.TrimSpace(correction) != "" {
		mandatory += "\n修正上次成片的问题，但不新增用户未要求的固定颜色或背景：\n" + strings.TrimSpace(correction)
	}
	if len(mandatory) > promptLimit {
		return "", errors.New("explicit visual requirements and identity exceed provider prompt limit; shorten the request")
	}
	parts := []string{mandatory}
	if plan.OutfitLength != "" {
		parts = append(parts, "用户未指定长度时外观库默认服装长度="+plan.OutfitLength+"；明确长度要求优先。")
	}
	if len(plan.Variables) > 0 {
		variables := []string{}
		for _, key := range []string{"primaryColor", "outfit", "scene", "camera", "makeup", "action", "mood", "time", "season", "variationReason"} {
			if value := plan.Variables[key]; value != "" {
				variables = append(variables, key+"="+value)
			}
		}
		parts = append(parts, "只用于未指定项目的本次随机变量，任何冲突均按用户要求："+strings.Join(variables, "；"))
	}
	if plan.AppearanceID != "" {
		parts = append(parts, "现实手机摄影，明确成年，自然肤质、合理解剖和物理，不照抄参考图的服装背景；没有固定禁用色。")
	}
	result := mandatory
	for _, part := range parts[1:] {
		available := promptLimit - len(result) - 1
		if available <= 0 {
			break
		}
		result += "\n" + utf8Prefix(part, available)
	}
	return result, nil
}

func normalizeBuiltinVisualIdentity(value string) string {
	// Normalize only known built-in anchors in the generated context. Never
	// rewrite custom appearance-library records or infer identity from role name.
	if value == canonicalDoubaoVisualDescription {
		return strings.Split(value, "以室内咖啡店生活照作为身份锚点")[0] + "保持同一张脸、五官比例、发型、发色、年龄感和体态；现实手机摄影、真实肤质和细小发丝。"
	}
	if value == canonicalXiaomanVisualDescription {
		return strings.Split(value, "她的视觉气质比豆包")[0] + "保持同一张脸、五官比例、发型、发色、年龄感和体态；真实手机摄影、自然肤质和合理物理比例。"
	}
	return strings.TrimSpace(value)
}
