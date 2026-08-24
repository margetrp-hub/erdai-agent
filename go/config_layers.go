package main

import "net/http"

// configLayer describes one ownership boundary in the Core configuration
// contract. The manifest is explicit so clients do not infer ownership from
// database table names.
type configLayer struct {
	ID           string            `json:"id"`
	Label        string            `json:"label"`
	Kind         string            `json:"kind"`
	Scope        string            `json:"scope"`
	Storage      []string          `json:"storage"`
	Consumes     []string          `json:"consumes"`
	Fields       map[string]string `json:"fields"`
	LegacyFields []string          `json:"legacyFields,omitempty"`
	Overrides    []string          `json:"overrides"`
	Description  string            `json:"description"`
}

type configLayerManifest struct {
	Version    string               `json:"version"`
	Precedence []string             `json:"precedence"`
	MergeRule  string               `json:"mergeRule"`
	Layers     []configLayer        `json:"layers"`
	Controls   []configControlGroup `json:"controls"`
	OpenSource map[string]any       `json:"openSource"`
}

type configControlGroup struct {
	ID               string   `json:"id"`
	Label            string   `json:"label"`
	CanonicalField   string   `json:"canonicalField"`
	SupportingFields []string `json:"supportingFields,omitempty"`
	Owner            string   `json:"owner"`
	LegacyFields     []string `json:"legacyFields,omitempty"`
	Rule             string   `json:"rule"`
}

func coreConfigLayerManifest() configLayerManifest {
	return configLayerManifest{
		Version:   "erdai-config-layers-v1",
		MergeRule: "按公共配置、公共策略、角色配置、角色策略、实例配置、实例策略的顺序合并；后层只覆盖已配置字段，实例策略只能收紧安全边界。",
		Precedence: []string{
			"public_config",
			"public_policy",
			"role_config",
			"role_policy",
			"instance_config",
			"instance_policy",
		},
		Layers: []configLayer{
			{
				ID: "public_config", Label: "公共配置", Kind: "config", Scope: "core",
				Storage: []string{"runtime_config", "provider_connections", "model_endpoints", "platform_integrations"},
				Consumes: []string{"模型连接与健康", "平台连接器", "默认角色", "知识库命名空间"},
				Fields: map[string]string{
					"activePersonaId": "runtime_config", "knowledgeNamespace": "runtime_config",
					"replyStyle": "runtime_config", "connectionId": "model_endpoint_connections",
					"credentialRef": "provider_connections",
				},
				Overrides:   []string{},
				Description: "Core 的基础设施与默认值，没有角色或实例覆盖时直接生效。",
			},
			{
				ID: "public_policy", Label: "公共策略", Kind: "policy", Scope: "core",
				Storage: []string{"integration_settings.group_chat_policy", "integration_settings.companion_policy", "integration_settings.message_policy", "integration_settings.content_boundary_policy", "integration_settings.memory_policy", "integration_settings.grok_policy"},
				Consumes: []string{"群聊门禁", "参与模式默认值", "回复节奏", "安全边界", "搜索与记忆默认行为"},
				Fields: map[string]string{
					"participationMode": "integration_settings.group_chat_policy",
					"initialProbability": "integration_settings.group_chat_policy",
					"replyDensityMaxReplies": "integration_settings.group_chat_policy",
					"sexualAction": "integration_settings.content_boundary_policy",
				},
				LegacyFields: []string{"proactiveChatEnabled"},
				Overrides:    []string{},
				Description:  "所有角色共享的行为底线与默认策略。",
			},
			{
				ID: "role_config", Label: "角色配置", Kind: "config", Scope: "persona",
				Storage: []string{"personas", "worldbook_entries", "persona_visual_references", "persona_traits", "persona_samples"},
				Consumes: []string{"身份与外形", "世界书", "形象素材", "人格特质", "场景样本"},
				Fields: map[string]string{
					"name": "personas", "personality": "personas", "visualDescription": "personas",
					"systemPrompt": "personas", "worldbook": "worldbook_entries",
				},
				Overrides:   []string{},
				Description: "角色是谁、记得什么世界，以及如何被表现。",
			},
			{
				ID: "role_policy", Label: "角色策略", Kind: "policy", Scope: "persona",
				Storage: []string{"persona_runtime_profiles"},
				Consumes: []string{"角色模型端点", "工具权限", "参与模式", "搜索触发方式", "搜索表达风格", "角色记忆策略"},
				Fields: map[string]string{
					"chatEndpointId": "persona_runtime_profiles", "allowedToolIds": "persona_runtime_profiles",
					"participationMode": "persona_runtime_profiles", "searchMode": "persona_runtime_profiles",
					"searchReplyStyle": "persona_runtime_profiles", "memoryPolicy": "persona_runtime_profiles",
				},
				LegacyFields: []string{"proactiveEnabled", "unaddressedMode", "participationStyle"},
				Overrides:    []string{"public_policy"},
				Description:  "角色对公共策略的个性化覆盖；空字段继续继承公共层。",
			},
			{
				ID: "instance_config", Label: "实例配置", Kind: "config", Scope: "runtime_instance",
				Storage: []string{"agent_instances", "platform_integrations", "agent_instance_connectors", "agent_instance_routes"},
				Consumes: []string{"账号凭据引用", "连接器状态", "会话路由", "记忆命名空间"},
				Fields: map[string]string{
					"memoryNamespace": "agent_instances", "connectorId": "agent_instance_connectors",
					"conversationRef": "agent_instance_routes", "credentialRefs": "platform_integrations",
				},
				Overrides:   []string{},
				Description: "同一 Core 内不同账号、通道和运行实例的隔离配置。",
			},
			{
				ID: "instance_policy", Label: "实例策略", Kind: "policy", Scope: "runtime_instance",
				Storage: []string{"agent_policy_templates", "agent_instances.overrides"},
				Consumes: []string{"实例参与模式", "实例工具权限", "实例表达覆盖", "实例模型覆盖"},
				Fields: map[string]string{
					"policyTemplateId": "agent_instances", "participationMode": "agent_instances.overrides",
					"initialReplyProbability": "agent_instances.overrides", "allowedToolIds": "agent_instances.overrides",
					"expressionPrompt": "agent_instances.overrides",
				},
				LegacyFields: []string{"proactiveEnabled", "unaddressedMode", "participationStyle"},
				Overrides:    []string{"role_policy", "public_policy"},
				Description:  "只影响当前运行实例，先合并策略模板，再合并实例覆盖。",
			},
		},
		Controls: []configControlGroup{
			{
				ID: "group_participation", Label: "群聊参与", CanonicalField: "participationMode",
				SupportingFields: []string{"group_chat_policy.enabled", "initialProbability", "afterReplyProbability", "replyDensityMaxReplies"},
				Owner: "group_chat_policy -> persona_runtime_profiles -> agent_instances.overrides",
				LegacyFields: []string{"proactiveChatEnabled", "proactiveEnabled", "unaddressedMode", "participationStyle"},
				Rule: "参与模式决定是否主动插话：addressed_only 只接 @、回复和命令；概率与密度只在模式开启后调节。group_chat_policy.enabled 只控制群聊观察与处理，不是第二个主动插话开关。",
			},
			{
				ID: "transport_takeover", Label: "消息接管", CanonicalField: "channel_runtime.mode",
				SupportingFields: []string{"channel_runtime.captureUnaddressedGroups"}, Owner: "channel_runtime",
				LegacyFields: []string{"captureUnaddressedGroups"},
				Rule: "只控制 Core 是否接管入站事件；不决定角色是否插话。off/shadow 下不创建回复任务。",
			},
			{
				ID: "search_activation", Label: "联网搜索", CanonicalField: "persona_runtime_profiles.searchMode",
				SupportingFields: []string{"grok_policy.enabled", "model_endpoints.enabled", "searchConnectionId"},
				Owner: "grok_policy + persona_runtime_profiles",
				Rule: "角色 searchMode 决定何时触发搜索；供应商和端点 enabled 只表示能力可用，不会单独触发搜索。",
			},
			{
				ID: "media_generation", Label: "图片与视频生成", CanonicalField: "image_policy.enabled",
				SupportingFields: []string{"grok_policy.imageModel", "grok_policy.videoModel", "model_endpoints.enabled", "dailyLimitEnabled"},
				Owner: "image_policy + grok_policy + model_endpoints",
				Rule: "image_policy.enabled 是媒体生成总开关；模型端点 enabled 是能力可用性；额度和提示词审核是安全门禁，不能单独开启生成。",
			},
			{
				ID: "group_moderation", Label: "群管撤回", CanonicalField: "agent_instance_capabilities.group_moderation.enabled",
				SupportingFields: []string{"mode", "minimumScore", "groupIds", "exemptAdmins"}, Owner: "agent instance capability",
				Rule: "enabled 是群管能力总开关；audit/enforce 只决定动作；群号、置信分和管理员豁免是范围与安全约束。",
			},
			{
				ID: "tool_progress", Label: "工具进度提示", CanonicalField: "message_policy.toolProgressEnabled",
				SupportingFields: []string{"toolProgressSearchEnabled", "toolProgressImageMessages", "toolProgressVideoMessages"}, Owner: "message_policy",
				Rule: "toolProgressEnabled 关闭时不发送任何进度提示；搜索、图片和视频选项只是开启后的子能力。",
			},
			{
				ID: "learning", Label: "自动学习", CanonicalField: "runtime_config.learningEnabled",
				SupportingFields: []string{"memory_policy.autoCapture", "grok_policy.learningWorkerEnabled", "learningPollSeconds"},
				Owner: "runtime_config + memory_policy + grok_policy",
				Rule: "learningEnabled 是学习总开关；autoCapture 与 worker enabled 只负责采集和执行子能力，不能反向开启总开关。",
			},
		},
		OpenSource: map[string]any{
			"entrypoint": "运行中心 -> 配置分层",
			"safeToEdit": []string{"public_config", "public_policy", "role_config", "role_policy", "instance_config", "instance_policy"},
			"secretRule": "只保存 credentialRef；密钥由服务端环境变量提供。",
		},
	}
}

func (s *coreConfigStore) handleConfigLayers(w http.ResponseWriter, r *http.Request) error {
	if r.Method != http.MethodGet {
		return mgmtMethodNotAllowed()
	}
	mgmtWriteData(w, http.StatusOK, coreConfigLayerManifest())
	return nil
}
