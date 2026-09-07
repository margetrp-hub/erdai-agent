package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func addVisualPlanReference(t *testing.T, runtime *AgentRuntime, personaID string) {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	if runtime.configStore.mediaDir == "" {
		runtime.configStore.mediaDir = t.TempDir()
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "identity.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(png)
	_ = writer.WriteField("category", "identity")
	_ = writer.WriteField("isPrimary", "true")
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/personas/"+personaID+"/visual-references?namespace=default", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	response := httptest.NewRecorder()
	if err = runtime.configStore.handlePersonaRequest(response, request, request.URL.Path); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusCreated {
		t.Fatalf("reference upload: %d %s", response.Code, response.Body.String())
	}
}

func visualPlanTestRun(t *testing.T, runtime *AgentRuntime, id string) runRecord {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err := runtime.db.Exec(`INSERT INTO agent_runs
		(id,event_id,reply_handle,conversation_ref,sender_ref,persona_id,state,created_at,updated_at)
		VALUES (?,?, 'reply','conversation','sender','doubao','running',?,?)`, id, id, now, now)
	if err != nil {
		t.Fatal(err)
	}
	return runRecord{ID: id, PersonaID: "doubao"}
}

func TestVisualPlanVariationAvoidsRecentColorsAndPairs(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	history := []visualGenerationPlan{}
	seen := map[string]bool{}
	for seed := uint64(0); seed < 120; seed++ {
		values := allocateVisualVariables("来一张你的自拍", now, seed, defaultImageVisualDirectorPolicy(), "short", history)
		for _, old := range history[:min(2, len(history))] {
			if values["primaryColor"] == old.Variables["primaryColor"] {
				t.Fatalf("repeated recent color at seed %d", seed)
			}
		}
		if len(history) > 0 && values["scene"] == history[0].Variables["scene"] && values["outfit"] == history[0].Variables["outfit"] {
			t.Fatalf("repeated pair at seed %d", seed)
		}
		seen[values["primaryColor"]] = true
		history = append([]visualGenerationPlan{{Variables: values}}, history...)
		history = history[:min(8, len(history))]
	}
	if !seen["紫色"] || len(seen) < 8 {
		t.Fatalf("random palette restricted: %+v", seen)
	}
}

func TestVisualPlanConstraintsWinWithoutPermanentColorBan(t *testing.T) {
	now := time.Now()
	for _, prompt := range []string{"这次不要紫色，穿红色短裙，在海边跑步，全身", "do not wear purple; wear red skirt"} {
		values := allocateVisualVariables(prompt, now, 19, defaultImageVisualDirectorPolicy(), "long", nil)
		if values["primaryColor"] != "红色" {
			t.Fatalf("explicit color lost: %+v", values)
		}
		if !strings.Contains(values["outfit"], "用户明确") {
			t.Fatalf("explicit outfit lost: %+v", values)
		}
	}
	_, forbidden := explicitVisualColor("不要总是紫色，每次随机一点")
	if forbidden["紫色"] {
		t.Fatal("diversity feedback became a color ban")
	}
	_, forbidden = explicitVisualColor("这次不要紫色")
	if !forbidden["紫色"] {
		t.Fatal("explicit current-request exclusion ignored")
	}
}

func TestVisualPlanPersistsSeedAndResamplesRepair(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "doubao")
	run := visualPlanTestRun(t, runtime, "visual-persist")
	ctx := context.Background()
	first, err := runtime.prepareVisualGeneration(ctx, run, "来一张你的自拍", "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := runtime.prepareVisualGeneration(ctx, run, "来一张你的自拍", "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if first.Seed != retry.Seed || first.Prompt != retry.Prompt || first.Reference != retry.Reference {
		t.Fatal("transport retry changed frozen plan")
	}
	paraphrase, err := runtime.prepareVisualGeneration(ctx, run, "换个说法拍你的生活自拍", "image", 0)
	if err != nil || paraphrase.OperationID != first.OperationID || paraphrase.Prompt != first.Prompt || paraphrase.UserPrompt != first.UserPrompt {
		t.Fatalf("model paraphrase replaced durable request: %+v, %v", paraphrase, err)
	}
	repair, err := runtime.prepareVisualGeneration(ctx, run, "来一张你的自拍", "image", 1)
	if err != nil {
		t.Fatal(err)
	}
	if repair.Seed == first.Seed || reflect.DeepEqual(repair.Variables, first.Variables) {
		t.Fatal("repair failed to resample")
	}
	if repair.ReferenceDigest != first.ReferenceDigest || repair.AppearanceID != first.AppearanceID {
		t.Fatal("repair changed identity")
	}
	if strings.Contains(first.Identity, "奶油色内搭") || strings.Contains(first.Identity, "紫色紧身上衣") || strings.Contains(first.Identity, "室内咖啡店") {
		t.Fatalf("legacy defaults leaked into identity: %s", first.Identity)
	}
	details, err := runtime.visualPlanTaskDetails(ctx, run.ID)
	if err != nil || len(details) != 2 {
		t.Fatalf("task details=%+v err=%v", details, err)
	}
	encoded, _ := json.Marshal(details)
	if strings.Contains(string(encoded), "data:image") {
		t.Fatal("public task details contain reference image")
	}
	if _, err = runtime.configStore.db.Exec("UPDATE persona_appearance_libraries SET updated_at='changed' WHERE persona_id='doubao'"); err != nil {
		t.Fatal(err)
	}
	if err = runtime.validateVisualGeneration(ctx, run, first); err == nil {
		t.Fatal("changed library binding accepted")
	}
}

func TestVisualPlanMissingReferenceDoesNotFallBackToAvatar(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "doubao")
	if _, err := runtime.configStore.db.Exec("UPDATE personas SET avatar_data_uri='data:image/png;base64,b2xk' WHERE id='doubao'"); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(runtime.configStore.mediaDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			if err = os.Remove(filepath.Join(runtime.configStore.mediaDir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = runtime.prepareVisualGeneration(context.Background(), runRecord{PersonaID: "doubao"}, "来一张你的自拍", "image", 0); err == nil {
		t.Fatal("missing reference silently fell back")
	}
	if reference := runtime.personaAvatarDataURI(context.Background(), "doubao", "自拍", true); reference != "" {
		t.Fatal("legacy resolver fell back after reference read failure")
	}
}

func TestVisualPlanCompilerKeepsConstraintsAndIdentityBeforeDecoration(t *testing.T) {
	plan := visualGenerationPlan{MediaType: "image", AppearanceID: "test", Identity: "唯一身份锚点", UserPrompt: "不要紫色，海边全身穿红色短裙", Variables: map[string]string{"scene": strings.Repeat("装饰", 3000)}}
	compiled, err := compileVisualGenerationPrompt(plan, "修复脸型")
	if err != nil {
		t.Fatal(err)
	}
	if len(compiled) > maxImagePromptBytes || !strings.Contains(compiled, plan.UserPrompt) || !strings.Contains(compiled, plan.Identity) || !strings.Contains(compiled, "修复脸型") {
		t.Fatal("required prompt sections were truncated")
	}
	plan.UserPrompt = strings.Repeat("必须保留", maxImagePromptBytes)
	if _, err = compileVisualGenerationPrompt(plan, ""); err == nil {
		t.Fatal("oversized explicit constraints silently truncated")
	}
	custom := "自定义红裙、海边与短发身份设定"
	if normalizeBuiltinVisualIdentity(custom) != custom {
		t.Fatal("custom appearance was rewritten")
	}
}

func TestVisualPlanImageFallbackOnlyOnDefiniteRejection(t *testing.T) {
	for _, err := range []error{context.DeadlineExceeded, errors.New("unexpected EOF"), &providerHTTPError{StatusCode: 502}, &providerHTTPError{StatusCode: 400}} {
		if imageProviderRejectedWithoutExecution(err) {
			t.Fatalf("ambiguous/policy error permits regeneration: %v", err)
		}
	}
	for _, status := range []int{401, 403, 404, 405, 429} {
		if !imageProviderRejectedWithoutExecution(&providerHTTPError{StatusCode: status}) {
			t.Fatalf("definitive rejection %d cannot fall back", status)
		}
	}
	if referenceImageCandidate(mediaProviderCandidate{Model: "gpt-image-2"}) {
		t.Fatal("generic generation capability treated as Grok reference-edit support")
	}
	if !referenceImageCandidate(mediaProviderCandidate{Model: "grok-imagine-image-lite"}) {
		t.Fatal("existing Grok reference-capable route excluded")
	}
}

func TestVisualPlanPruningDropsPrivateCopiesAndPreventsReplay(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "doubao")
	run := visualPlanTestRun(t, runtime, "visual-prune")
	ctx := context.Background()
	first, err := runtime.prepareVisualGeneration(ctx, run, "来一张你的自拍", "image", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = runtime.db.Exec("UPDATE agent_runs SET state='delivered' WHERE id=?", run.ID); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano)
	if err = runtime.pruneVisualGenerationMetadata(ctx, cutoff); err != nil {
		t.Fatal(err)
	}
	details, err := runtime.visualPlanTaskDetails(ctx, run.ID)
	if err != nil || len(details) != 1 {
		t.Fatalf("details=%+v err=%v", details, err)
	}
	if details[0].UserPrompt != "" || details[0].Identity != "" || details[0].Prompt != "" || details[0].ReferenceDigest != first.ReferenceDigest || len(details[0].Variables) == 0 {
		t.Fatalf("bad pruning: %+v", details[0])
	}
	if _, err = runtime.prepareVisualGeneration(ctx, run, "来一张你的自拍", "image", 0); err == nil {
		t.Fatal("pruned snapshot allowed replay")
	}
}

func TestVisualPlanSelectedLibraryOverridesRoleName(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	addVisualPlanReference(t, runtime, "xiaoman")
	if _, err := runtime.configStore.db.Exec(`UPDATE persona_appearance_libraries SET library_id=
		(SELECT library_id FROM persona_appearance_libraries WHERE persona_id='xiaoman') WHERE persona_id='doubao'`); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"来一张你的自拍", "生成一张你本人的照片", "生成一张你自己的照片", "生成一张你的全身照"} {
		plan, err := runtime.prepareVisualGeneration(context.Background(), runRecord{PersonaID: "doubao"}, prompt, "image", 0)
		if err != nil {
			t.Fatal(err)
		}
		if plan.AppearanceID == "" || plan.Reference == "" || plan.ReferenceDigest == "" || !strings.Contains(plan.Identity, "黑色长卷发") || strings.Contains(plan.Identity, "小巧柔和的鹅蛋脸") {
			t.Fatalf("request %q lost the selected library reference/identity: %+v", prompt, plan)
		}
	}
}

func TestVisualPlanCorrectionScalarPrecedenceAndUnspecifiedScene(t *testing.T) {
	for _, prompt := range []string{"紫色改成红色", "不要紫色换成红色", "原本紫色但是改穿红色", "不要红色\n最新用户要求：这次改成红色"} {
		color, forbidden := explicitVisualColor(prompt)
		if color != "红色" || forbidden["红色"] {
			t.Fatalf("replacement lost for %q: %s %+v", prompt, color, forbidden)
		}
	}
	color, _ := explicitVisualColor("给我红色裙子自拍\n本轮明确约束：换成蓝色\n最新用户要求：蓝色短裙")
	if color != "蓝色" {
		t.Fatalf("composite task prompt retained original color: %s", color)
	}
	color, _ = explicitVisualColor("a desired portrait")
	if color != "" {
		t.Fatalf("color substring inside unrelated word: %s", color)
	}
	for _, prompt := range []string{"现在来张自拍", "背景随机，自拍", "换个背景，来一张自拍", "场景每次都随机"} {
		seen := map[string]bool{}
		for seed := uint64(0); seed < 50; seed++ {
			values := allocateVisualVariables(prompt, time.Now(), seed, defaultImageVisualDirectorPolicy(), "short", nil)
			if strings.Contains(values["scene"], "用户明确") {
				t.Fatalf("unspecified scene suppressed: %q %+v", prompt, values)
			}
			seen[values["scene"]] = true
		}
		if len(seen) < 3 {
			t.Fatalf("scene did not vary: %q %+v", prompt, seen)
		}
	}
	for seed := uint64(0); seed < 50; seed++ {
		values := allocateVisualVariables("不要全身，来张自拍", time.Now(), seed, defaultImageVisualDirectorPolicy(), "short", nil)
		if strings.Contains(values["camera"], "全身") || strings.Contains(values["camera"], "穿搭") {
			t.Fatalf("negative camera ignored: %+v", values)
		}
	}
	if !visualSceneSpecified("在海边拍你的自拍") {
		t.Fatal("concrete user scene lost")
	}
}

func TestVisualPlanDisabledImageGatePrecedesMissingReference(t *testing.T) {
	runtime := newIdleRuntime(t)
	defer runtime.Close()
	if _, err := runtime.configStore.db.Exec("UPDATE integration_settings SET config_json='{}' WHERE id='image_policy'"); err != nil {
		t.Fatal(err)
	}
	_, err := runtime.generateImageForRun(context.Background(), runRecord{PersonaID: "doubao"}, "来张自拍", true)
	if err == nil || err.Error() != "image generation is disabled" {
		t.Fatalf("disabled image gate=%v", err)
	}
}
