package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCoreConfigSchemaInitializesNatively(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	var version int
	if err = store.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != nativeCoreSchemaVersion {
		t.Fatalf("schema version = %d", version)
	}
	for _, table := range []string{
		"personas", "persona_visual_references", "worldbook_entries", "persona_samples", "persona_traits", "knowledge_documents", "knowledge_vectors", "runtime_config",
		"admin_directives", "knowledge_candidates", "model_endpoints", "model_health",
		"routing_control", "routing_lane_profiles", "tools", "skills", "agent_plugins", "trusted_adapters", "trusted_adapter_health", "mcp_servers",
		"integration_settings", "platform_integrations", "audit_events", "shadow_interactions",
		"agent_policy_templates", "agent_instances", "agent_instance_connectors", "agent_instance_routes", "agent_instance_capabilities",
	} {
		var name string
		if err = store.db.QueryRow(
			"SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table,
		).Scan(&name); err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
	config, err := store.runtimeConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.MaxReplySentences != 2 || config.MaxReplyChars != 40 || !config.WorldbookInjectionEnabled {
		t.Fatalf("runtime defaults = %+v", config)
	}
	for _, integration := range []string{
		"channel_runtime", "qq_official", "provider_policy", "message_policy",
		"group_chat_policy", "companion_policy", "grok_policy",
		"memory_policy", "retrieval_policy", "document_policy", "ops_policy", "affiliate_policy", "image_policy",
	} {
		var raw string
		if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = ?", integration).Scan(&raw); err != nil {
			t.Fatalf("integration %s: %v", integration, err)
		}
		if !strings.HasPrefix(raw, "{") {
			t.Fatalf("integration %s is not JSON: %s", integration, raw)
		}
	}
	var pluginCount int
	if err = store.db.QueryRow("SELECT count(*) FROM agent_plugins WHERE source = 'builtin'").Scan(&pluginCount); err != nil {
		t.Fatal(err)
	}
	if pluginCount < 12 {
		t.Fatalf("builtin plugin seed count = %d", pluginCount)
	}
	var qqEnabled int
	if err = store.db.QueryRow(
		"SELECT json_extract(config_json, '$.enabled') FROM integration_settings WHERE id = 'qq_official'",
	).Scan(&qqEnabled); err != nil {
		t.Fatal(err)
	}
	if qqEnabled != 0 {
		t.Fatalf("fresh QQ integration must be disabled without credentials: %d", qqEnabled)
	}
	var instancePersona, memoryNamespace string
	var instanceEnabled int
	if err = store.db.QueryRow(`SELECT persona_id, memory_namespace, enabled
		FROM agent_instances WHERE id = 'doubao-qq'`).Scan(&instancePersona, &memoryNamespace, &instanceEnabled); err != nil {
		t.Fatal(err)
	}
	if instancePersona != "doubao" || memoryNamespace != legacyAgentInstanceID || instanceEnabled != 0 {
		t.Fatalf("fresh QQ runtime instance = %q/%q/%d", instancePersona, memoryNamespace, instanceEnabled)
	}
	var runtimeLinks int
	if err = store.db.QueryRow(`SELECT
		(SELECT count(*) FROM agent_instance_connectors WHERE instance_id = 'doubao-qq' AND connector_id = 'qq_official') +
		(SELECT count(*) FROM agent_instance_routes WHERE instance_id = 'doubao-qq' AND connector_id = 'qq_official')`).Scan(&runtimeLinks); err != nil {
		t.Fatal(err)
	}
	if runtimeLinks != 2 {
		t.Fatalf("fresh QQ runtime links = %d", runtimeLinks)
	}
	var radarURL string
	if err = store.db.QueryRow(
		"SELECT json_extract(config_json, '$.radarUrl') FROM integration_settings WHERE id = 'ops_policy'",
	).Scan(&radarURL); err != nil {
		t.Fatal(err)
	}
	if radarURL != "https://codexradar.com/api/intelligence-efficiency-metrics" {
		t.Fatalf("radar URL = %q", radarURL)
	}
	var imageEditModel string
	if err = store.db.QueryRow(
		"SELECT json_extract(config_json, '$.imageEditModel') FROM integration_settings WHERE id = 'grok_policy'",
	).Scan(&imageEditModel); err != nil {
		t.Fatal(err)
	}
	if imageEditModel != "grok-imagine-image" {
		t.Fatalf("Grok image edit model = %q", imageEditModel)
	}
	var imageTimeout, videoTimeout, toolTimeout, extractionTimeout, visionTimeout int
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.timeoutSeconds')
		FROM integration_settings WHERE id = 'image_policy'`).Scan(&imageTimeout); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.videoTimeoutSeconds')
		FROM integration_settings WHERE id = 'grok_policy'`).Scan(&videoTimeout); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.toolCallTimeoutSeconds')
		FROM integration_settings WHERE id = 'provider_policy'`).Scan(&toolTimeout); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.extractionTimeoutSeconds')
		FROM integration_settings WHERE id = 'document_policy'`).Scan(&extractionTimeout); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.imageTimeoutSeconds')
		FROM integration_settings WHERE id = 'group_chat_policy'`).Scan(&visionTimeout); err != nil {
		t.Fatal(err)
	}
	if imageTimeout != 600 || videoTimeout != 1200 || toolTimeout != 90 ||
		extractionTimeout != 90 || visionTimeout != 90 {
		t.Fatalf("timeout defaults = image:%d video:%d tool:%d document:%d vision:%d",
			imageTimeout, videoTimeout, toolTimeout, extractionTimeout, visionTimeout)
	}
	var visualDirectorEnabled, visualUseTimeContext int
	var visualTimezone string
	var selfieTypeCount int
	if err = store.db.QueryRow(`
		SELECT json_extract(config_json, '$.visualDirectorEnabled'),
			json_extract(config_json, '$.visualUseTimeContext'),
			json_extract(config_json, '$.visualTimezone'),
			json_array_length(json_extract(config_json, '$.selfieTypes'))
		FROM integration_settings WHERE id = 'image_policy'
	`).Scan(&visualDirectorEnabled, &visualUseTimeContext, &visualTimezone, &selfieTypeCount); err != nil {
		t.Fatal(err)
	}
	if visualDirectorEnabled != 1 || visualUseTimeContext != 1 ||
		visualTimezone != "Asia/Shanghai" || selfieTypeCount < 5 {
		t.Fatalf("visual director defaults = %d/%d/%q/%d", visualDirectorEnabled, visualUseTimeContext, visualTimezone, selfieTypeCount)
	}
	var videoModel, videoKind, videoAdapter string
	if err = store.db.QueryRow(`
		SELECT model, execution_kind, adapter_ref FROM model_endpoints
		WHERE id = 'grok-imagine-video' AND enabled = 1
	`).Scan(&videoModel, &videoKind, &videoAdapter); err != nil {
		t.Fatal(err)
	}
	if videoModel != "grok-imagine-video" || videoKind != "media" || videoAdapter != "grok_generate_video" {
		t.Fatalf("video endpoint = %q %q %q", videoModel, videoKind, videoAdapter)
	}
	var grokImageEnabled, legacyImageEnabled int
	if err = store.db.QueryRow(
		"SELECT count(*) FROM model_endpoints WHERE adapter_ref = 'grok_generate_image' AND enabled = 1",
	).Scan(&grokImageEnabled); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(
		"SELECT count(*) FROM model_endpoints WHERE adapter_ref = 'generate_image' AND enabled = 1",
	).Scan(&legacyImageEnabled); err != nil {
		t.Fatal(err)
	}
	if grokImageEnabled != 1 || legacyImageEnabled != 1 {
		t.Fatalf("image routes = Grok %d, legacy %d", grokImageEnabled, legacyImageEnabled)
	}
	for tool, enabled := range map[string]int{
		"generate_image": 1, "grok_generate_image": 1, "grok_generate_video": 1,
	} {
		var actual int
		if err = store.db.QueryRow("SELECT enabled FROM tools WHERE name = ?", tool).Scan(&actual); err != nil {
			t.Fatal(err)
		}
		if actual != enabled {
			t.Fatalf("tool %s enabled = %d, want %d", tool, actual, enabled)
		}
	}
	var mediaTools string
	if err = store.db.QueryRow("SELECT required_tools_json FROM skills WHERE id = 'media-generation'").Scan(&mediaTools); err != nil {
		t.Fatal(err)
	}
	if mediaTools != `["grok_generate_image","generate_image","grok_generate_video"]` {
		t.Fatalf("media tools = %s", mediaTools)
	}
	imageRoute, err := store.simulateNativeRoute("image")
	if err != nil || imageRoute.Selected == nil || imageRoute.Selected.Endpoint.AdapterRef != "grok_generate_image" ||
		len(imageRoute.Fallbacks) == 0 || imageRoute.Fallbacks[0].Endpoint.AdapterRef != "generate_image" {
		t.Fatalf("image route = %+v: %v", imageRoute, err)
	}
	var activePersona string
	if err = store.db.QueryRow("SELECT active_persona_id FROM runtime_config WHERE id = 1").Scan(&activePersona); err != nil {
		t.Fatal(err)
	}
	if activePersona != "doubao" {
		t.Fatalf("active persona = %q", activePersona)
	}
	var naturalSkillMode string
	if err = store.db.QueryRow("SELECT activation_mode FROM skills WHERE id = 'natural-cn-dialogue'").Scan(&naturalSkillMode); err != nil {
		t.Fatal(err)
	}
	if naturalSkillMode != "always" {
		t.Fatalf("natural language skill mode = %q", naturalSkillMode)
	}
	var description, scenario, systemPrompt, postHistory, messageExample, characterVersion, sourceVersion, visualDescription string
	if err = store.db.QueryRow(`
		SELECT description, scenario, system_prompt, post_history_instructions,
			message_example, character_version, source_version, visual_description
		FROM personas WHERE id = 'doubao'
	`).Scan(&description, &scenario, &systemPrompt, &postHistory, &messageExample, &characterVersion, &sourceVersion, &visualDescription); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(description, "群高级管家") || strings.Contains(scenario, "资料查询") {
		t.Fatalf("fresh persona still reads like a duty list: %q / %q", description, scenario)
	}
	for _, expected := range []string{"不要把自己说成一份角色说明", "平台显示名", "不使用 AI", "不编造真人姓名", "不要近似复用", "不要把执行拆成多轮口头确认"} {
		if !strings.Contains(systemPrompt, expected) {
			t.Fatalf("fresh persona prompt missing %q: %s", expected, systemPrompt)
		}
	}
	if !strings.Contains(postHistory, "最近对话比角色简介更重要") || messageExample != "" ||
		characterVersion != "1.8.0" || sourceVersion != "go-schema-35" ||
		!strings.Contains(visualDescription, "二十至二十三岁") || !strings.Contains(visualDescription, "服装随季节") || !strings.Contains(postHistory, "立即执行") {
		t.Fatalf("fresh persona conversation policy = %q / %q / %q / %q", postHistory, messageExample, characterVersion, sourceVersion)
	}
	for table, minimum := range map[string]int{
		"personas": 1, "worldbook_entries": 8, "persona_samples": 27, "persona_traits": 7, "knowledge_documents": 8,
		"model_endpoints": 6, "tools": 12,
	} {
		var count int
		if err = store.db.QueryRow("SELECT count(*) FROM " + table).Scan(&count); err != nil || count < minimum {
			t.Fatalf("native seed %s = %d, want at least %d: %v", table, count, minimum, err)
		}
	}
	var contextualExamples int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM worldbook_entries
		WHERE id LIKE 'doubao-fewshot-%' AND position = 'before_example' AND enabled = 1
	`).Scan(&contextualExamples); err != nil || contextualExamples != 4 {
		t.Fatalf("contextual few-shot entries = %d: %v", contextualExamples, err)
	}
	var curatedSamples int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM persona_samples
		WHERE persona_id = 'doubao' AND enabled = 1 AND source <> ''
		  AND id NOT LIKE 'doubao-runtime-%'
	`).Scan(&curatedSamples); err != nil || curatedSamples != len(nativeDoubaoPersonaSamples) {
		t.Fatalf("curated persona samples = %d: %v", curatedSamples, err)
	}
	var incompleteSamples int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM persona_samples
		WHERE persona_id = 'doubao' AND (
			scene_tags_json = '[]' OR relationship_stage = '' OR emotion = '' OR context = '' OR
			candidate_replies_json = '[]' OR forbidden_expressions_json = '[]' OR source = '' OR weight <= 0
		)
	`).Scan(&incompleteSamples); err != nil || incompleteSamples != 0 {
		t.Fatalf("incomplete persona samples = %d: %v", incompleteSamples, err)
	}
	var incompleteTraits int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM persona_traits
		WHERE persona_id = 'doubao' AND (
			name = '' OR description = '' OR triggers_json = '[]' OR source = '' OR weight <= 0
		)
	`).Scan(&incompleteTraits); err != nil || incompleteTraits != 0 {
		t.Fatalf("incomplete persona traits = %d: %v", incompleteTraits, err)
	}
	for _, sourceFragment := range []string{
		"scutcyr/CPED", "facebookresearch/ParlAI", "Hualeez/ThinkPersona", "scutcyr/SoulChat",
		"InteractiveNLP-Team/RoleLLM-public", "SillyTavern/SillyTavern", "Xerxes-2/tavern2agent",
		"a16z-infra/ai-town", "letta-ai/letta", "2722550596/pi-rp", "Entropy2077-axe/talk",
		"funnycups/Luker", "weidu12123/Liyuan", "deepsleep-claw/SleepTavernHome",
		"haiou-666/haiou2.0-Claude-Code-",
	} {
		var count int
		if err = store.db.QueryRow(
			"SELECT count(*) FROM persona_samples WHERE persona_id = 'doubao' AND source LIKE ?",
			"%"+sourceFragment+"%",
		).Scan(&count); err != nil || count == 0 {
			t.Fatalf("persona sample source %q = %d: %v", sourceFragment, count, err)
		}
	}
	var healthStatusMessageColumn int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM pragma_table_info('model_health') WHERE name = 'status_message'
	`).Scan(&healthStatusMessageColumn); err != nil || healthStatusMessageColumn != 1 {
		t.Fatalf("model health status message column = %d: %v", healthStatusMessageColumn, err)
	}
	for _, server := range []struct {
		id, endpoint, tools string
	}{
		{"context7-docs", "https://mcp.context7.com/mcp", "query-docs"},
		{"microsoft-learn", "https://learn.microsoft.com/api/mcp?maxTokenBudget=2000", "microsoft_docs_search"},
		{"cloudflare-docs", "https://docs.mcp.cloudflare.com/mcp", "search_cloudflare_documentation"},
		{"deepwiki-repositories", "https://mcp.deepwiki.com/mcp", "ask_question"},
	} {
		var endpoint, config string
		if err = store.db.QueryRow(
			"SELECT endpoint, config_json FROM mcp_servers WHERE id = ? AND enabled = 1", server.id,
		).Scan(&endpoint, &config); err != nil {
			t.Fatalf("MCP preset %s: %v", server.id, err)
		}
		if endpoint != server.endpoint || !strings.Contains(config, server.tools) {
			t.Fatalf("MCP preset %s = %s %s", server.id, endpoint, config)
		}
	}
	var learnEnabled, githubEnabled int
	var learnEndpoint, githubConfig string
	if err = store.db.QueryRow(`
		SELECT enabled, endpoint FROM mcp_servers WHERE id = 'microsoft-learn'
	`).Scan(&learnEnabled, &learnEndpoint); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`
		SELECT enabled, config_json FROM mcp_servers WHERE id = 'github-official'
	`).Scan(&githubEnabled, &githubConfig); err != nil {
		t.Fatal(err)
	}
	if learnEnabled != 1 || learnEndpoint != "https://learn.microsoft.com/api/mcp?maxTokenBudget=2000" {
		t.Fatalf("Microsoft Learn preset = enabled %d endpoint %q", learnEnabled, learnEndpoint)
	}
	if githubEnabled != 0 || !strings.Contains(githubConfig, "ERDAI_GITHUB_TOKEN") {
		t.Fatalf("GitHub preset = enabled %d config %s", githubEnabled, githubConfig)
	}
}

func TestCoreConfigSchemaV73AddsAffiliatePointsAlias(t *testing.T) {
	path, db := newTestCoreConfig(t)
	setTestIntegration(t, db, "affiliate_policy", map[string]any{
		"enabled": true, "pointsAliases": []string{"/查询积分", "/积分"},
	})
	if _, err := db.Exec("PRAGMA user_version = 72"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var raw string
	if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'affiliate_policy'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, "/积分查询") {
		t.Fatalf("v73 points aliases = %s", raw)
	}
}

func TestCoreConfigSchemaMigratesV27ToContextualFewShots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`
		DELETE FROM worldbook_entries WHERE id LIKE 'doubao-fewshot-%';
		PRAGMA user_version = 27;
	`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var count int
	if err = store.db.QueryRow(`
		SELECT count(*) FROM worldbook_entries
		WHERE id LIKE 'doubao-fewshot-%' AND position = 'before_example'
	`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("v27 contextual few-shot migration = %d: %v", count, err)
	}
}

func TestCoreConfigSchemaMigratesOnlyKnownLegacyDoubaoPersonas(t *testing.T) {
	tests := []struct {
		name, description, scenario, systemPrompt, sourceVersion, wantSourceVersion string
		schemaVersion                                                               int
	}{
		{
			name: "production-v23", schemaVersion: 23,
			description:   "高冷、嘴硬心软的群高级管家。聪明、干练、会接梗，真正需要帮助时办事可靠。",
			scenario:      "QQ群日常聊天、群务协作、资料查询、图片理解与生成、OPS 状态查询和轻量任务协助。",
			systemPrompt:  "你是豆包，一位高冷、聪明、嘴硬心软的群高级管家。旧提示。",
			sourceVersion: "admin-2026-08-01", wantSourceVersion: "go-schema-35",
		},
		{
			name: "seed-v24", schemaVersion: 24,
			description: "旧默认描述", scenario: "旧默认场景", systemPrompt: "旧默认提示",
			sourceVersion: "go-schema-24", wantSourceVersion: "go-schema-35",
		},
		{
			name: "custom-v25", schemaVersion: 25,
			description: "我自己写的角色", scenario: "我自己写的场景", systemPrompt: "保留我的自定义提示",
			sourceVersion: "admin-2026-08-01", wantSourceVersion: "admin-2026-08-01",
		},
		{
			name: "custom-v30-with-old-source-version", schemaVersion: 30,
			description: "我自己写的角色", scenario: "我自己写的场景", systemPrompt: "保留我的自定义提示",
			sourceVersion: "go-schema-27", wantSourceVersion: "go-schema-27",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "core.sqlite3")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(nativeCoreTables); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`
				INSERT INTO personas (
					id, namespace, name, description, scenario, system_prompt,
					character_version, source_version, created_at, updated_at
				) VALUES ('doubao', 'default', '豆包', ?, ?, ?, '1.0.0', ?,
					'2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z')
			`, test.description, test.scenario, test.systemPrompt, test.sourceVersion); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", test.schemaVersion)); err != nil {
				t.Fatal(err)
			}
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := openCoreConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var gotDescription, gotSystemPrompt, gotSourceVersion string
			if err = store.db.QueryRow(`
				SELECT description, system_prompt, source_version FROM personas WHERE id = 'doubao'
			`).Scan(&gotDescription, &gotSystemPrompt, &gotSourceVersion); err != nil {
				t.Fatal(err)
			}
			if gotSourceVersion != test.wantSourceVersion {
				t.Fatalf("source version = %q, want %q", gotSourceVersion, test.wantSourceVersion)
			}
			if test.wantSourceVersion == "go-schema-35" {
				if strings.Contains(gotDescription, "群高级管家") || !strings.Contains(gotSystemPrompt, "最近对话") ||
					!strings.Contains(gotSystemPrompt, "不要把执行拆成多轮口头确认") {
					t.Fatalf("legacy persona not migrated: %q / %q", gotDescription, gotSystemPrompt)
				}
			} else if gotDescription != test.description || gotSystemPrompt != test.systemPrompt {
				t.Fatalf("custom persona changed: %q / %q", gotDescription, gotSystemPrompt)
			}
		})
	}
}

func TestCoreConfigSchemaMigratesV26ToGrokMedia(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`
		UPDATE model_endpoints SET enabled = 1 WHERE adapter_ref = 'generate_image';
		DELETE FROM model_endpoints WHERE adapter_ref = 'grok_generate_image';
		UPDATE tools SET enabled = CASE WHEN name = 'generate_image' THEN 1 ELSE 0 END
		WHERE name IN ('generate_image', 'grok_generate_image', 'grok_generate_video');
		UPDATE skills SET required_tools_json = '["generate_image","grok_generate_image","grok_generate_video"]'
		WHERE id = 'media-generation';
		UPDATE integration_settings SET config_json = json_set(config_json, '$.enabled', json('true'))
		WHERE id = 'image_policy';
		UPDATE personas SET source_version = 'go-schema-26', character_version = '1.3.0'
		WHERE id = 'doubao';
		PRAGMA user_version = 26;
	`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var grokEndpoint, legacyEndpoint, grokTool, legacyTool, imagePolicyEnabled int
	var mediaTools, sourceVersion string
	if err = store.db.QueryRow("SELECT count(*) FROM model_endpoints WHERE adapter_ref = 'grok_generate_image' AND enabled = 1").Scan(&grokEndpoint); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT count(*) FROM model_endpoints WHERE adapter_ref = 'generate_image' AND enabled = 1").Scan(&legacyEndpoint); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT enabled FROM tools WHERE name = 'grok_generate_image'").Scan(&grokTool); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT enabled FROM tools WHERE name = 'generate_image'").Scan(&legacyTool); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT required_tools_json FROM skills WHERE id = 'media-generation'").Scan(&mediaTools); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT json_extract(config_json, '$.enabled') FROM integration_settings WHERE id = 'image_policy'").Scan(&imagePolicyEnabled); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT source_version FROM personas WHERE id = 'doubao'").Scan(&sourceVersion); err != nil {
		t.Fatal(err)
	}
	if grokEndpoint != 1 || legacyEndpoint != 1 || grokTool != 1 || legacyTool != 1 ||
		imagePolicyEnabled != 1 || mediaTools != `["grok_generate_image","generate_image","grok_generate_video"]` ||
		sourceVersion != "go-schema-35" {
		t.Fatalf("v26 migration = endpoints %d/%d tools %d/%d policy %d skill %s persona %s",
			grokEndpoint, legacyEndpoint, grokTool, legacyTool, imagePolicyEnabled, mediaTools, sourceVersion)
	}
}

func TestCoreConfigSchemaV31MigratesOnlyKnownOPSAndIdentityDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`
		UPDATE integration_settings
		SET config_json = json_set(config_json, '$.apiBase', 'https://grok.wsphl.cfd/v1')
		WHERE id = 'grok_policy';
		UPDATE integration_settings
		SET config_json = json_set(config_json, '$.statusTitle', 'Synai996 分组检测')
		WHERE id = 'ops_policy';
		UPDATE personas SET
			description = '豆包。聪明、嘴硬心软，熟了会轻轻逗人。',
			scenario = 'QQ群里和大家聊天；有人认真找她时，也会把事情办好。',
			system_prompt = '你是豆包。别人问“你是谁”或让你介绍自己时，只像一个有脾气的熟人报名字并接住关系，不列能力、职责或功能。',
			post_history_instructions = '旧默认', character_version = '1.4.0', source_version = 'go-schema-27'
		WHERE id = 'doubao';
		UPDATE persona_samples SET
			context = '对方想认识豆包，不是在索取产品能力清单。',
			candidate_replies_json = '["豆包。你先说找我干什么。","我叫豆包。别把开场搞得太正式。","豆包，群里那个管闲事的。"]',
			forbidden_expressions_json = '["我是一个AI助手","我可以为您提供","我的主要功能包括"]'
		WHERE id = 'doubao-sample-identity';
		UPDATE worldbook_entries SET content = '豆包：豆包。群里那个管事的。'
		WHERE id = 'doubao-fewshot-identity';
		PRAGMA user_version = 30;
	`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var title, grokBase, sourceVersion, systemPrompt, sampleReplies, example string
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.statusTitle')
		FROM integration_settings WHERE id = 'ops_policy'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT source_version, system_prompt FROM personas WHERE id = 'doubao'`).
		Scan(&sourceVersion, &systemPrompt); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.apiBase')
		FROM integration_settings WHERE id = 'grok_policy'`).Scan(&grokBase); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT candidate_replies_json FROM persona_samples
		WHERE id = 'doubao-sample-identity'`).Scan(&sampleReplies); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT content FROM worldbook_entries
		WHERE id = 'doubao-fewshot-identity'`).Scan(&example); err != nil {
		t.Fatal(err)
	}
	if title != "分组检测" || grokBase != "http://grok2api-local:8000/v1" || sourceVersion != "go-schema-35" ||
		!strings.Contains(systemPrompt, "平台显示名") || strings.Contains(sampleReplies, "我叫豆包") ||
		!strings.Contains(example, "角色：你都叫到我了") {
		t.Fatalf("v31 defaults = %q / %q / %q / %q / %q / %q", title, grokBase, sourceVersion, systemPrompt, sampleReplies, example)
	}
}

func TestCoreConfigSchemaV31PreservesCustomizedOPSAndPersona(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`
		UPDATE integration_settings SET config_json = json_set(config_json, '$.apiBase', 'https://custom.example/v1')
		WHERE id = 'grok_policy';
		UPDATE integration_settings SET config_json = json_set(config_json, '$.statusTitle', '我的线路')
		WHERE id = 'ops_policy';
		UPDATE personas SET system_prompt = '我的自定义提示', source_version = 'go-schema-27'
		WHERE id = 'doubao';
		UPDATE persona_samples SET candidate_replies_json = '["我的自定义回答"]'
		WHERE id = 'doubao-sample-identity';
		UPDATE worldbook_entries SET content = '我的自定义样本'
		WHERE id = 'doubao-fewshot-identity';
		PRAGMA user_version = 30;
	`); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var title, grokBase, prompt, replies, example string
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.statusTitle')
		FROM integration_settings WHERE id = 'ops_policy'`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT system_prompt FROM personas WHERE id = 'doubao'`).Scan(&prompt); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT json_extract(config_json, '$.apiBase')
		FROM integration_settings WHERE id = 'grok_policy'`).Scan(&grokBase); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT candidate_replies_json FROM persona_samples
		WHERE id = 'doubao-sample-identity'`).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow(`SELECT content FROM worldbook_entries
		WHERE id = 'doubao-fewshot-identity'`).Scan(&example); err != nil {
		t.Fatal(err)
	}
	if title != "我的线路" || grokBase != "https://custom.example/v1" || prompt != "我的自定义提示" || replies != `["我的自定义回答"]` || example != "我的自定义样本" {
		t.Fatalf("custom values changed = %q / %q / %q / %q / %q", title, grokBase, prompt, replies, example)
	}
}

func TestCoreConfigSchemaPreservesExistingV13Rows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`
		CREATE TABLE personas (
			id TEXT PRIMARY KEY, namespace TEXT NOT NULL DEFAULT 'default', name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '', personality TEXT NOT NULL DEFAULT '',
			scenario TEXT NOT NULL DEFAULT '', first_message TEXT NOT NULL DEFAULT '',
			system_prompt TEXT NOT NULL DEFAULT '', post_history_instructions TEXT NOT NULL DEFAULT '',
			message_example TEXT NOT NULL DEFAULT '', alternate_greetings_json TEXT NOT NULL DEFAULT '[]',
			tags_json TEXT NOT NULL DEFAULT '[]', creator TEXT NOT NULL DEFAULT '',
			character_version TEXT NOT NULL DEFAULT '', source_format TEXT NOT NULL DEFAULT 'native',
			source_version TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
		);
		INSERT INTO personas (id, namespace, name, created_at, updated_at)
		VALUES ('doubao', 'default', '豆包', '2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z');
		PRAGMA user_version = 13;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var name, avatar, visualDescription string
	if err = store.db.QueryRow("SELECT name, avatar_data_uri, visual_description FROM personas WHERE id = 'doubao'").Scan(&name, &avatar, &visualDescription); err != nil {
		t.Fatal(err)
	}
	if name != "豆包" || avatar != "" || strings.TrimSpace(visualDescription) == "" {
		t.Fatalf("preserved persona = %q, avatar = %q, visual = %q", name, avatar, visualDescription)
	}
	var integrations int
	if err = store.db.QueryRow(`SELECT count(*) FROM integration_settings WHERE id IN (
		'channel_runtime', 'qq_official', 'provider_policy', 'message_policy',
		'content_boundary_policy', 'group_chat_policy', 'companion_policy', 'grok_policy',
		'memory_policy', 'retrieval_policy', 'document_policy', 'ops_policy', 'image_policy'
	)`).Scan(&integrations); err != nil {
		t.Fatal(err)
	}
	if integrations != 13 {
		t.Fatalf("schema 13 startup did not fill required integrations: %d", integrations)
	}
	for _, entry := range []string{"doubao-social-boundaries", "doubao-chinese-internet-voice"} {
		var content string
		if err = store.db.QueryRow(
			"SELECT content FROM worldbook_entries WHERE id = ? AND persona_id = 'doubao'", entry,
		).Scan(&content); err != nil || strings.TrimSpace(content) == "" {
			t.Fatalf("worldbook preset %s: %q %v", entry, content, err)
		}
	}
}

func TestCoreConfigSchemaMigratesV14RuntimeIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(nativeCoreTables + `
		INSERT INTO integration_settings (id, config_json, updated_at) VALUES (
			'astrbot_transport',
			'{"mode":"active","agentCoreUrl":"http://doubao-agent-core:6280","consumerId":"erdai-runtime","fallbackOnCoreError":true}',
			'2026-08-02T00:00:00Z'
		);
		INSERT INTO integration_settings (id, config_json, updated_at) VALUES (
			'astrbot_platforms',
			'{"sourceVersion":"4.26.8","instances":[{"id":"telegram-main","type":"telegram","displayName":"Telegram","enabled":true,"credentialConfigured":true,"settings":{"start_message":"hi"},"credentialRefs":{"telegram_token":"ASTRBOT_TELEGRAM_TOKEN"}}]}',
			'2026-08-02T00:00:00Z'
		);
		INSERT INTO tools (id, name, config_json, created_at, updated_at) VALUES (
			'legacy-tool', 'legacy_tool', '{"adapterRef":"astrbot:memory_recall"}',
			'2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z'
		);
		INSERT INTO model_endpoints (id, provider, model, adapter_ref, created_at, updated_at) VALUES (
			'legacy-image', 'embedded', 'image', 'astrbot_plugin_image_generation',
			'2026-08-02T00:00:00Z', '2026-08-02T00:00:00Z'
		);
		INSERT INTO runtime_config (id, protected_rules, updated_at) VALUES (
			1, '只有 AstrBot 判定为管理员的事件才可信。', '2026-08-02T00:00:00Z'
		);
		INSERT INTO audit_events (actor, action, target_type, target_id, created_at) VALUES (
			'admin', 'update', 'integration', 'astrbot_transport', '2026-08-02T00:00:00Z'
		);
		INSERT INTO shadow_interactions (
			transport, conversation_hash, sender_hash, lane, route_json, created_at
		) VALUES (
			'qq_official', 'conversation', 'sender', 'image',
			'{"adapterRef":"astrbot_plugin_image_generation"}', '2026-08-02T00:00:00Z'
		);
		PRAGMA user_version = 14;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var config, adapter, endpointAdapter string
	if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'channel_runtime'").Scan(&config); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(config, `"mode":"active"`) ||
		strings.Contains(config, "agentCoreUrl") || strings.Contains(config, "consumerId") ||
		strings.Contains(config, "fallbackOnCoreError") || strings.Contains(config, "compatibilitySyncEnabled") {
		t.Fatalf("migrated runtime config = %s", config)
	}
	var oldCount int
	if err = store.db.QueryRow("SELECT count(*) FROM integration_settings WHERE id IN ('astrbot_transport', 'astrbot_platforms')").Scan(&oldCount); err != nil || oldCount != 0 {
		t.Fatalf("legacy integration count = %d: %v", oldCount, err)
	}
	if err = store.db.QueryRow("SELECT json_extract(config_json, '$.adapterRef') FROM tools WHERE id = 'legacy-tool'").Scan(&adapter); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT adapter_ref FROM model_endpoints WHERE id = 'legacy-image'").Scan(&endpointAdapter); err != nil {
		t.Fatal(err)
	}
	if adapter != "core:memory_recall" || endpointAdapter != "generate_image" {
		t.Fatalf("adapter migration = %q, %q", adapter, endpointAdapter)
	}
	var credentialRefs, rules, auditTarget, shadowRoute string
	if err = store.db.QueryRow("SELECT credential_refs_json FROM platform_integrations WHERE id = 'telegram-main'").Scan(&credentialRefs); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT protected_rules FROM runtime_config WHERE id = 1").Scan(&rules); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT target_id FROM audit_events LIMIT 1").Scan(&auditTarget); err != nil {
		t.Fatal(err)
	}
	if err = store.db.QueryRow("SELECT route_json FROM shadow_interactions LIMIT 1").Scan(&shadowRoute); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(credentialRefs, "ASTRBOT_") || !strings.Contains(credentialRefs, "ERDAI_TELEGRAM_TOKEN") {
		t.Fatalf("credential refs migration = %s", credentialRefs)
	}
	if strings.Contains(rules, "AstrBot") || strings.Contains(rules, "渠道运行层") || !strings.Contains(rules, "Go 平台连接器") {
		t.Fatalf("protected rules migration = %s", rules)
	}
	if auditTarget != "channel_runtime" || strings.Contains(shadowRoute, "astrbot") || !strings.Contains(shadowRoute, "generate_image") {
		t.Fatalf("historical metadata migration = %q, %s", auditTarget, shadowRoute)
	}
}

func TestCoreConfigSchemaRejectsNewerDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "core.sqlite3")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(fmt.Sprintf("PRAGMA user_version = %d", nativeCoreSchemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	store, err := openCoreConfigStore(path)
	if store != nil {
		_ = store.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "newer than supported") {
		t.Fatalf("newer schema error = %v", err)
	}
}

func TestCoreConfigSchemaV40AddsContextBudgetWithoutReplacingPolicy(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`
		UPDATE integration_settings SET config_json = '{"enabled":true,"chatModel":"custom-chat","contextMessagesPerPrompt":80}'
		WHERE id = 'companion_policy';
		PRAGMA user_version = 39;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var raw string
	if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'companion_policy'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		t.Fatal(err)
	}
	if policy["chatModel"] != "custom-chat" || policy["contextMessagesPerPrompt"] != float64(80) || policy["contextTokenBudget"] != float64(6000) {
		t.Fatalf("v40 companion policy = %#v", policy)
	}
}

func TestCoreConfigSchemaV41MigratesEndpointReferencesAndDoesNotOverwriteConnections(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`
		UPDATE integration_settings SET config_json = json_set(config_json,
			'$.chatModel', 'gpt-5.4-mini', '$.summaryProviderId', 'legacy-summary', '$.summaryModel', 'legacy-model')
		WHERE id = 'companion_policy';
		UPDATE integration_settings SET config_json = '{"providerId":"legacy-provider","providerType":"openai_chat_completion","apiBase":"https://legacy.example/v1","credentialRef":"ERDAI_LEGACY_KEY"}'
		WHERE id = 'provider_policy';
		UPDATE integration_settings SET config_json = json_set(config_json, '$.decisionProviderId', 'ohlaoo-gpt-5-4-mini')
		WHERE id = 'group_chat_policy';
		UPDATE integration_settings SET config_json = json_set(config_json, '$.promptAuditProviderId', 'ohlaoo-gpt-5-4-mini')
		WHERE id = 'image_policy';
		PRAGMA user_version = 40;
	`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'companion_policy'").Scan(&raw); err != nil {
		store.Close()
		t.Fatal(err)
	}
	var policy map[string]any
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if policy["chatModel"] != "ohlaoo-gpt-5.4-mini" || policy["taskModel"] != "ohlaoo-gpt-5.6-terra" ||
		policy["summaryProviderId"] != nil || policy["summaryModel"] != nil {
		store.Close()
		t.Fatalf("v41 companion policy = %#v", policy)
	}
	var unboundLLM int
	if err = store.db.QueryRow(`SELECT count(*) FROM model_endpoints endpoint
		LEFT JOIN model_endpoint_connections binding ON binding.endpoint_id = endpoint.id
		WHERE endpoint.enabled = 1 AND endpoint.execution_kind = 'llm' AND binding.endpoint_id IS NULL`).Scan(&unboundLLM); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if unboundLLM != 0 {
		store.Close()
		t.Fatalf("unbound migrated LLM endpoints = %d", unboundLLM)
	}
	for integrationID, field := range map[string]string{
		"group_chat_policy": "decisionProviderId",
		"image_policy":      "promptAuditProviderId",
	} {
		if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = ?", integrationID).Scan(&raw); err != nil {
			store.Close()
			t.Fatal(err)
		}
		policy = map[string]any{}
		if err = json.Unmarshal([]byte(raw), &policy); err != nil {
			store.Close()
			t.Fatal(err)
		}
		if policy[field] != "ohlaoo-gpt-5.4-mini" {
			store.Close()
			t.Fatalf("%s %s = %#v", integrationID, field, policy[field])
		}
	}
	var connectionID string
	if err = store.db.QueryRow("SELECT id FROM provider_connections ORDER BY id LIMIT 1").Scan(&connectionID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if _, err = store.db.Exec(`UPDATE provider_connections SET api_base = 'https://edited.example/v1'
		WHERE id = ?`, connectionID); err != nil {
		store.Close()
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var apiBase string
	if err = store.db.QueryRow("SELECT api_base FROM provider_connections WHERE id = ?", connectionID).Scan(&apiBase); err != nil {
		t.Fatal(err)
	}
	if apiBase != "https://edited.example/v1" {
		t.Fatalf("provider connection was overwritten on reopen: %q", apiBase)
	}
}

func TestCoreConfigSchemaV47DisablesDuplicateStyleDeliveries(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`UPDATE integration_settings SET config_json = json_set(config_json,
		'$.segmentedReplyEnabled', json('true'), '$.maxReplySegments', 2,
		'$.toolProgressSearchEnabled', json('true')) WHERE id = 'message_policy';
		PRAGMA user_version = 46;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var raw string
	if err = store.db.QueryRow("SELECT config_json FROM integration_settings WHERE id = 'message_policy'").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var policy map[string]any
	if err = json.Unmarshal([]byte(raw), &policy); err != nil {
		t.Fatal(err)
	}
	if policy["segmentedReplyEnabled"] != false || policy["maxReplySegments"] != float64(1) ||
		policy["toolProgressSearchEnabled"] != false {
		t.Fatalf("v47 message policy = %#v", policy)
	}
}

func TestCoreConfigSchemaV49MigratesOnlyLegacySearchLane(t *testing.T) {
	for _, test := range []struct {
		name string
		from string
		want string
	}{
		{name: "legacy-default", from: `["chat","web_search"]`, want: `["web_search"]`},
		{name: "administrator-custom", from: `["web_search","custom_search"]`, want: `["web_search","custom_search"]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			path, db := newTestCoreConfig(t)
			if _, err := db.Exec(`UPDATE routing_lane_profiles
				SET required_capabilities_json = ? WHERE lane = 'search'`, test.from); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if _, err := db.Exec(`PRAGMA user_version = 48`); err != nil {
				db.Close()
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			store, err := openCoreConfigStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer store.Close()
			var got string
			if err = store.db.QueryRow(`SELECT required_capabilities_json
				FROM routing_lane_profiles WHERE lane = 'search'`).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("search lane capabilities = %s, want %s", got, test.want)
			}
		})
	}
}

func TestCoreConfigSchemaV51MigratesOnlyLegacyMemoryCopy(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`UPDATE skills SET
		description = '按用户明确要求读取、写入或删除长期记忆。',
		instructions = '自动吸收低风险、稳定且反复出现的称呼、偏好、习惯和长期项目；不要只等“记住”两个字。一次性的情绪、临时安排、敏感信息和密钥不保存。记忆在后台静默完成，不向群友播报“已保存”；只有对方明确回忆或纠正时才自然提及。删除记忆仍需明确请求。'
		WHERE id = 'memory-control';
		PRAGMA user_version = 50;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var description, instructions string
	if err = store.db.QueryRow(`SELECT description, instructions FROM skills
		WHERE id = 'memory-control'`).Scan(&description, &instructions); err != nil {
		t.Fatal(err)
	}
	if description != "自动沉淀低风险、稳定且有复用价值的关系与偏好。" {
		t.Fatalf("memory skill description = %q", description)
	}
	if !strings.Contains(instructions, "不要只等") || !strings.Contains(instructions, "静默完成") {
		t.Fatalf("memory skill instructions = %q", instructions)
	}
}

func TestCoreConfigSchemaV74RestoresXiaomanVisualIsolation(t *testing.T) {
	path, db := newTestCoreConfig(t)
	if _, err := db.Exec(`UPDATE personas SET
		visual_description = 'schema 64 visual card', character_version = '1.3.0', source_version = 'go-schema-64'
		WHERE id = 'xiaoman';
		PRAGMA user_version = 73;`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := openCoreConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	persona, found, err := store.persona("default", "xiaoman")
	if err != nil || !found {
		t.Fatalf("xiaoman persona missing: found=%v err=%v", found, err)
	}
	if persona.CharacterVersion != "1.3.1" || persona.SourceVersion != "go-schema-74" ||
		!strings.Contains(persona.VisualDescription, "不得读取或复用豆包") {
		t.Fatalf("xiaoman v74 visual isolation = %+v", persona)
	}
}
