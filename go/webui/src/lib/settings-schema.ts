export type FieldSpec = {
  label: string;
  description?: string;
  kind?: 'text' | 'textarea' | 'number' | 'boolean' | 'select' | 'list' | 'object' | 'secret';
  options?: Array<{ value: string; label: string }>;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  readOnly?: boolean;
  advanced?: boolean;
  placeholder?: string;
  itemDefaults?: unknown;
  nullable?: boolean;
  nullLabel?: string;
  reference?: 'personas' | 'models' | 'connections' | 'instances' | 'templates' | 'platforms' | 'members';
  scale?: number;
  required?: boolean;
};

const text = (label: string, description?: string): FieldSpec => ({ label, description, kind: 'text' });
const longText = (label: string, description?: string): FieldSpec => ({ label, description, kind: 'textarea' });
const flag = (label: string, description?: string): FieldSpec => ({ label, description, kind: 'boolean' });
const list = (label: string, description?: string): FieldSpec => ({ label, description, kind: 'list', itemDefaults: '' });
const number = (label: string, unit?: string, min?: number, max?: number, step = 1): FieldSpec => ({ label, kind: 'number', unit, min, max, step });
const choice = (label: string, values: Record<string, string>, description?: string): FieldSpec => ({
  label, description, kind: 'select', options: Object.entries(values).map(([value, name]) => ({ value, label: name })),
});
const ratio = (label: string, description?: string): FieldSpec => ({
  ...number(label, '%', 0, 1, 0.01), scale: 100, description,
});
const reference = (label: string, target: FieldSpec['reference'], description?: string): FieldSpec => ({
  ...text(label, description), reference: target,
});
const credential = (label: string): FieldSpec => ({
  ...text(label, '填写服务器已配置的环境变量名称，不要填入密钥本身。'), advanced: true, placeholder: '例如 ERDAI_MODEL_API_KEY',
});

// Display metadata only. The API contract layer decides which fields can be submitted.
// Enums and limits come from the corresponding Go validators, not from field-name guesses.
const common: Record<string, FieldSpec> = {
  id: { ...text('内部编号', '自动生成，创建后保持不变。'), readOnly: true, advanced: true },
  name: { ...text('名称'), required: true },
  displayName: { ...text('显示名称'), required: true },
  title: { ...text('标题'), required: true },
  description: longText('说明'),
  enabled: flag('启用'),
  namespace: { ...text('内容分区', '用于隔离不同范围的内容，通常保留现有值。'), advanced: true },
  personaId: reference('使用角色', 'personas'),
  sourcePersonaId: reference('从已有角色复制外观', 'personas'),
  activePersonaId: { ...reference('默认角色', 'personas', '清除全局默认不会删除角色或已有的会话绑定。'), nullable: true, nullLabel: '不设置默认角色' },
  instanceId: reference('机器人实例', 'instances'),
  agentInstanceId: reference('机器人实例', 'instances'),
  policyTemplateId: reference('使用策略模板', 'templates', '不选则沿用角色与全局设置。'),
  connectorId: reference('平台连接', 'platforms'),
  connectionId: reference('服务商连接', 'connections'),
  memoryNamespace: { ...text('记忆分区', '同一分区内按现有权限读取记忆；修改前确认是否需要隔离。'), advanced: true },
  config: { label: '配置内容', kind: 'object' },
  overrides: { label: '此机器人单独设置', kind: 'object', description: '仅覆盖明确设置的项目，其余沿用角色或策略模板。' },
  version: text('版本'),
  author: text('作者'),
  creator: text('创建者'),
  priority: { ...number('优先级'), advanced: true },
  content: longText('内容'),
  label: text('备注名称'),
  senderRef: reference('成员', 'members', '选择已识别的成员，系统保存对应身份；备注名称不会改变身份。'),
  source: text('来源说明'),
  tags: list('标签'),
  sourceUri: text('来源链接', '手动录入且没有来源链接时可留空。'),
  metadata: { label: '补充信息', kind: 'object', advanced: true },
  personality: longText('性格特点', '描述她怎样思考、表达和与人相处。'),
  scenario: longText('背景与情境', '角色所处的背景，不需要填写固定格式。'),
  firstMessage: longText('首次问候'),
  systemPrompt: { ...longText('角色补充要求', '角色层的行为要求，不会覆盖系统安全规则。'), advanced: true },
  postHistoryInstructions: { ...longText('对话后的补充要求'), advanced: true },
  messageExample: longText('说话示例', '用几段示例展示语气和节奏，无需列出大量规则。'),
  alternateGreetings: list('可选问候语'),
  characterVersion: { ...text('角色版本'), advanced: true },
  sourceFormat: { ...text('导入格式'), advanced: true },
  sourceVersion: { ...text('导入格式版本'), advanced: true },
  avatarDataUri: { ...longText('头像图片数据', '通常通过角色头像上传入口修改。'), advanced: true },
  visualDescription: longText('外观描述', '描述身份特征；穿搭和场景可由独立外观设置控制。'),
  promptNotes: longText('参考说明', '说明这份素材希望保留的特征。'),
  isPrimary: flag('作为主要身份参考'),
  sortOrder: { ...number('排列顺序'), advanced: true },
  mediaType: { ...text('素材类型'), readOnly: true },
  assetPath: { ...text('素材位置'), readOnly: true, advanced: true },
  mimeType: { ...text('文件类型'), readOnly: true, advanced: true },
  originalName: { ...text('原文件名'), readOnly: true },
  byteSize: { ...number('文件大小', '字节'), readOnly: true },
  contentUrl: { ...text('素材访问地址'), readOnly: true, advanced: true },
  keys: list('触发词', '对话命中这些词时，可带入这条背景设定。'),
  secondaryKeys: list('辅助触发词'),
  comment: text('条目名称'),
  constant: flag('始终带入', '开启后不依赖触发词。'),
  selective: flag('同时检查辅助词'),
  insertionOrder: { ...number('同位置排列顺序'), advanced: true },
  tokenBudget: { ...number('内容长度预算', '词元', 0), nullable: true, nullLabel: '不单独设置预算', advanced: true, description: '取消此条目的单独预算，保留系统整体长度控制。' },
  triggers: list('触发词'),
  supports: list('支持的特质', '填写相关特质的编号。'),
  conflicts: list('冲突的特质', '填写不应同时使用的特质编号。'),
  weight: number('影响权重', undefined, 0, 100, 0.1),
  sceneTags: list('适用场景'),
  relationshipStage: text('关系阶段', '留空表示不限关系阶段。'),
  emotion: text('适用情绪', '留空表示不限情绪。'),
  context: longText('场景说明'),
  candidateReplies: list('表达示例', '提供多种自然表达，不要求逐字照搬。'),
  forbiddenExpressions: list('避免的表达'),
  chatEndpointId: reference('聊天模型', 'models'),
  taskEndpointId: reference('复杂任务模型', 'models'),
  decisionEndpointId: reference('接话判断模型', 'models'),
  allowedToolIds: list('允许的工具'),
  deniedToolIds: list('禁用的工具'),
  proactiveEnabled: flag('允许主动接话', '已有参与模式时，以参与模式为准。'),
  initialReplyProbability: ratio('平时接话概率', '仍会经过场景判断、冷却与忙碌限制。'),
  afterReplyProbability: ratio('接话后的继续参与概率', '用于刚参与过的对话，仍受其他限制。'),
  participationMode: choice('群聊参与方式', { addressed_only: '仅被点名时回复', adaptive: '根据上下文自然参与', social: '更主动参与聊天' }),
  participationStyle: { ...choice('参与风格', { '': '沿用默认', balanced: '兼顾聊天与帮助', social: '偏向聊天互动', service: '偏向解答问题' }), advanced: true },
  unaddressedMode: { ...choice('未被点名时', { '': '沿用默认', off: '不主动回复', rare: '偶尔参与', adaptive: '根据上下文参与' }), advanced: true },
  addressKeywords: list('角色称呼'),
  maxReplyChars: number('回复长度上限', '字', 20, 1000),
  maxReplySentences: number('回复句数上限', '句', 1, 6),
  memoryPolicy: { ...text('记忆策略'), advanced: true },
  searchMode: choice('联网搜索方式', { '': '沿用默认', adaptive: '需要时自动搜索', explicit_only: '明确要求时才搜索' }),
  searchReplyStyle: choice('搜索结果表达', { '': '沿用默认', natural: '自然聊天', concise: '简短直接', source_first: '先给来源' }),
  visualPromptOverride: { ...longText('单独的画面要求'), advanced: true },
  visualStyle: { label: '照片变化范围', kind: 'object' },
  expressionPrompt: longText('表达习惯补充'),
  selfieTypes: list('拍摄方式', '可填写自拍、他人拍摄等具体风格，不限定为固定模板。'),
  outfits: list('穿搭方向'),
  scenes: list('生活场景'),
  provider: { ...text('服务商名称'), required: true },
  model: { ...text('模型名称', '使用服务商提供的准确模型名称。'), required: true },
  apiBase: { ...text('接口地址', '使用服务商提供的 HTTPS 地址；内部服务按现有服务器规则连接。'), placeholder: 'https://example.com/v1' },
  credentialRef: credential('密钥环境变量'),
  secretRef: credential('鉴权环境变量'),
  credentialRefs: { label: '鉴权设置', kind: 'object', description: '填写服务器环境变量名称，不保存真实密钥。' },
  pricingUrl: { ...text('价格来源地址'), advanced: true },
  timeoutSeconds: number('最长等待时间', '秒', 1, 300),
  inputCostPerMillion: { ...number('每百万输入词元价格', undefined, 0, undefined, 0.000001), advanced: true },
  outputCostPerMillion: { ...number('每百万输出词元价格', undefined, 0, undefined, 0.000001), advanced: true },
  qualityScore: { ...number('质量评分', undefined, 0, 1, 0.01), advanced: true },
  maxContextTokens: { ...number('上下文容量', '词元', 0), advanced: true, description: '0 表示未声明容量，不等于无限。' },
  executionKind: choice('模型用途', { llm: '对话与推理', tool: '工具处理', media: '图片与视频' }),
  adapterRef: { ...text('调用适配器', '填写系统已支持的适配器名称。'), advanced: true },
  capabilities: list('支持的能力'),
  requiredCapabilities: list('必须具备的能力'),
  preferredCapabilities: list('优先具备的能力'),
  lane: text('任务场景'),
  locks: { label: '指定场景使用的模型', kind: 'object', description: '各场景独立选择，未锁定的场景按现有路由规则处理。' },
  riskLevel: number('操作风险等级', '级', 0, 3),
  allowedAuthorities: { ...list('允许使用的人'), options: [{ value: 'member', label: '普通成员' }, { value: 'admin', label: '管理员' }] },
  approvalMode: choice('执行前确认', { auto: '自动执行', confirm: '先确认再执行', admin_only: '仅管理员可执行' }, '高风险工具与普通成员权限仍受服务器限制。'),
  inputSchema: { label: '工具输入字段', kind: 'object', advanced: true },
  endpoint: text('服务地址', '填写服务的 HTTP 或 HTTPS 地址。'),
  command: text('服务器启动命令', '本机命令必须已加入服务器允许清单。'),
  args: list('启动参数', '每项是一个独立参数，顺序会保留。'),
  toolPrefix: { ...text('工具名称前缀'), advanced: true },
  allowedTools: list('允许调用的工具', '为空时按该服务现有的开放规则处理。'),
  envHeaderRefs: { label: '请求头与环境变量', kind: 'object', advanced: true },
  instructions: longText('执行说明'),
  activationMode: choice('触发条件', { any: '满足任一条件', all: '满足全部条件', always: '始终启用' }),
  attachmentKinds: list('附件类型'),
  requiredTools: list('需要的工具'),
  requiredMcpServers: list('需要的外部服务'),
  personaIds: { ...list('适用角色', '不限定时保留空列表。'), reference: 'personas' },
  scopeKind: text('记忆所属范围'),
  scopeReference: text('所属成员或群', '沿用实际身份标识，不以昵称代替。'),
  kind: text('记忆类型'),
  confidence: ratio('可信程度'),
  importance: ratio('重要程度'),
  intimacy: number('亲密度', undefined, 0, 100, 0.1),
  locked: flag('固定亲密度', '关闭后由真实互动自然发展。'),
  intimacyLocked: flag('固定亲密度'),
  expiresAt: { ...text('失效时间', '不设失效时间时，这条记忆不会因到期而失效；填写时间须包含时区。'), nullable: true, nullLabel: '不设失效时间', placeholder: '例如 2026-10-31T23:59:00+08:00', advanced: true },
  transport: text('消息来源平台'),
  transportInstance: { ...text('平台账号范围', '* 表示匹配所有账号。'), advanced: true },
  conversationRef: text('群或会话范围', '* 表示匹配所有会话。'),
  settings: { label: '连接设置', kind: 'object' },
  manifest: { label: '能力说明', kind: 'object', advanced: true },
  category: text('分类'),
  commands: list('支持的命令'),
  permissions: list('允许的操作'),
  pluginId: text('所属能力包'),
  integrationId: text('关联的系统功能'),
  ownerKind: text('所属对象类型'),
  ownerId: text('所属对象'),
  autoInclude: flag('自动使用这个知识库'),
  createdAt: { ...text('创建时间'), readOnly: true },
  updatedAt: { ...text('更新时间'), readOnly: true },
  checkedAt: { ...text('检查时间'), readOnly: true },
  lastCheckedAt: { ...text('最近检查'), readOnly: true },
  lastCollectedAt: { ...text('最近学习时间'), readOnly: true },
  lastSeenAt: { ...text('最近活动'), readOnly: true },
  reviewedAt: { ...text('审核时间'), readOnly: true },
  contentHash: { ...text('内容校验值'), readOnly: true, advanced: true },
  credentialConfigured: { ...flag('密钥已配置'), readOnly: true },
  health: { label: '连接检查结果', kind: 'object', readOnly: true },
  usage: { label: '调用统计', kind: 'object', readOnly: true },
  status: { ...text('当前状态'), readOnly: true },
  statusMessage: { ...text('状态说明'), readOnly: true },
  pricingSource: { ...text('价格来源'), readOnly: true },
  pricingCheckedAt: { ...text('价格更新时间'), readOnly: true },
  pricingCurrency: { ...text('价格币种'), readOnly: true },
  accessCount: { ...number('使用次数', '次'), readOnly: true },
  documentCount: { ...number('文档数量', '份'), readOnly: true },
  referenceCount: { ...number('参考素材数量', '份'), readOnly: true },
  personaCount: { ...number('使用此外观的角色', '个'), readOnly: true },
  compatibilitySource: { ...text('兼容配置来源'), readOnly: true, advanced: true },
  createdByAuthority: { ...text('创建者权限'), readOnly: true },
  runtimeVersion: { ...text('当前程序版本'), readOnly: true },
  catalog: { label: '支持的平台类型', kind: 'list', readOnly: true },
};

const runtime: Record<string, FieldSpec> = {
  personaInjectionEnabled: flag('使用角色设定'),
  knowledgeInjectionEnabled: flag('使用知识库'),
  worldbookInjectionEnabled: flag('使用背景设定'),
  protectedRules: longText('系统安全规则', '优先级高于角色、背景设定与普通聊天内容。'),
  replyStyle: longText('希望怎样说话', '用自然语言描述语气、节奏和表达偏好，不必按固定模板写。'),
  avoidRepetitiveOpeners: flag('减少重复开场白'),
  knowledgeNamespace: { ...text('默认知识分区'), advanced: true },
  learningEnabled: flag('自动学习'),
  learningTopics: list('学习主题', '每项写一个希望关注的主题。'),
  learningIntervalHours: number('学习间隔', '小时', 6, 168),
};

const quotas: Record<string, FieldSpec> = {
  imageDailyLimit: { ...number('每人每天可生成图片', '张', 0, 100), description: '0 表示普通成员不能生成图片，不限额对象仍按豁免规则处理。' },
  videoDailyLimit: { ...number('每人每天可生成视频', '条', 0, 100), description: '0 表示普通成员不能生成视频，不限额对象仍按豁免规则处理。' },
  trustedAdminBypass: flag('管理员不受每日额度限制'),
  whitelist: { label: '不限额成员', kind: 'list', itemDefaults: { label: '', senderRef: '' }, description: '成员身份用于判断豁免，备注仅用于方便辨认。' },
  timezone: { ...text('每天重新计算额度的时区', '固定按北京时间计算。'), readOnly: true },
};

const policies: Record<string, FieldSpec> = {
  captureUnaddressedGroups: flag('记录未点名的群消息', '用于上下文理解，不代表一定回复。'),
  deliveryPollSeconds: { ...number('待发送消息检查间隔', '秒', 0), advanced: true },
  groupC2C: flag('接收群聊与私聊'),
  guildDirectMessage: flag('接收频道私信'),
  platformId: text('平台名称'),
  appid: text('应用编号'),
  adminOpenIds: longText('管理员账号标识'),
  providerId: text('服务商标识'),
  providerType: text('服务商类型'),
  defaultModel: text('默认模型名称'),
  fallbackModels: list('备用模型', '按顺序填写，保留现有故障切换关系。'),
  streaming: flag('使用流式回复'),
  webSearch: flag('允许联网搜索'),
  providerRetries: { ...number('调用失败重试次数', '次', 0), advanced: true },
  maxAgentSteps: { ...number('单次任务最多执行步骤', '步', 0), advanced: true },
  toolCallTimeoutSeconds: { ...number('工具调用最长等待', '秒', 0), advanced: true },
  segmentedReplyEnabled: flag('长回复分段发送'),
  segmentMinChars: number('分段最少字数', '字', 0),
  segmentMaxChars: number('每段最多字数', '字', 0),
  maxReplySegments: number('最多发送段数', '段', 0),
  segmentMinDelaySeconds: number('分段最短间隔', '秒', 0, undefined, 0.1),
  segmentMaxDelaySeconds: number('分段最长间隔', '秒', 0, undefined, 0.1),
  toolProgressEnabled: flag('执行任务时给进度提示'),
  toolProgressSearchEnabled: flag('搜索时给进度提示'),
  toolProgressSearchMessages: list('搜索等待提示'),
  toolProgressImageMessages: list('生图等待提示'),
  toolProgressPhotoMessages: list('拍照等待提示'),
  toolCompletionImageMessages: list('图片完成提示'),
  toolProgressVideoMessages: list('视频等待提示'),
  toolCompletionVideoMessages: list('视频完成提示'),
  toolProgressDocumentMessages: list('文档处理等待提示'),
  toolCompletionDocumentMessages: list('文档完成提示'),
  humanPacingEnabled: flag('按自然节奏发送'),
  humanPacingMaxSeconds: number('自然节奏最长等待', '秒', 0, undefined, 0.1),
  moodContinuityEnabled: flag('保持前后情绪连贯'),
  sexualTriggers: list('色情请求触发词'),
  violenceTriggers: list('暴力伤害触发词'),
  abuseTriggers: list('辱骂触发词'),
  provocationTriggers: list('挑衅触发词'),
  sexualContextExceptions: list('色情相关词的正常语境', '如医学、健康或举报等语境，按正常信息处理。'),
  violenceContextExceptions: list('暴力相关词的正常语境'),
  abuseContextExceptions: list('辱骂相关词的正常语境'),
  sexualReplies: list('拒绝色情请求的回复'),
  violenceReplies: list('拒绝伤害请求的回复'),
  abuseReplies: list('面对辱骂的回复'),
  provocationReplies: list('面对挑衅的回复'),
  modelInstruction: longText('边界处理补充要求'),
  enabledGroups: list('适用群聊', '空列表按当前策略的默认群范围处理，不代表关闭功能。'),
  initialProbability: ratio('平时接话概率', '仍需通过场景判断、冷却与忙碌限制；不是每条消息都直接抽签回复。'),
  probabilityDurationSeconds: number('接话后保持参与的时间', '秒', 0),
  decisionProviderId: reference('接话判断模型', 'models'),
  decisionIncludePersona: flag('判断接话时参考角色'),
  decisionTimeoutSeconds: number('接话判断最长等待', '秒', 0, undefined, 0.1),
  decisionPromptMode: { ...text('接话判断提示组合方式'), advanced: true },
  decisionExtraPrompt: longText('接话判断补充要求'),
  replyPromptMode: { ...text('群聊回复提示组合方式'), advanced: true },
  replyExtraPrompt: longText('群聊表达补充要求'),
  atLinkMaxMessages: { ...number('被点名后关联消息数', '条', 0), advanced: true },
  atLinkMaxSeconds: { ...number('被点名后关联时长', '秒', 0), advanced: true },
  concurrentMode: { ...text('并发消息处理方式'), advanced: true },
  smartBatchHintEnabled: { ...flag('合并消息时提供上下文提示'), advanced: true },
  smartMergeWaitSeconds: number('等待连续消息的时间', '秒', 0, undefined, 0.1),
  smartMaxBatchSize: number('一次最多合并消息', '条', 0),
  smartClaimDelaySeconds: { ...number('消息认领等待', '秒', 0, undefined, 0.01), advanced: true },
  concurrentWaitMaxLoops: { ...number('并发等待检查次数', '次', 0), advanced: true },
  concurrentWaitIntervalSeconds: { ...number('并发等待检查间隔', '秒', 0, undefined, 0.01), advanced: true },
  groupWaitWindowEnabled: { ...flag('启用群聊等待窗口'), advanced: true },
  maxContextMessages: number('判断时参考最近消息', '条', 0),
  includeTimestamp: { ...flag('上下文带上消息时间'), advanced: true },
  includeSenderInfo: { ...flag('上下文带上发言者'), advanced: true },
  triggerKeywords: list('容易触发接话的称呼或词'),
  keywordSmartMode: flag('触发词也结合语境判断'),
  commandPrefixes: { ...list('命令开头字符'), advanced: true },
  messageQualityEnabled: flag('判断消息是否值得参与'),
  questionBoost: ratio('遇到问题时增加参与倾向'),
  waterReduce: ratio('闲聊刷屏时减少参与倾向'),
  replyDensityEnabled: flag('限制短时间回复密度'),
  replyDensityWindowSeconds: number('回复密度统计时段', '秒', 0),
  replyDensityMaxReplies: number('统计时段内回复上限', '次', 0),
  replyDensitySoftLimitRatio: ratio('开始减少回复的占比'),
  replyDensityAiHint: { ...flag('提醒模型注意回复密度'), advanced: true },
  ignoreAtOthers: flag('被点名的是其他人时少插话'),
  ignoreAtOthersMode: { ...text('他人点名消息的处理方式'), advanced: true },
  ignoreAtAll: flag('忽略仅点名全体成员的消息'),
  duplicateFilterEnabled: flag('过滤重复消息'),
  proactiveChatEnabled: { ...flag('允许主动群聊', '已有群聊参与方式时，以参与方式为准。'), advanced: true },
  ignoreLowValueMedia: flag('跳过没有足够内容的表情和媒体'),
  lowValueMediaMarkers: { ...list('媒体占位标记'), advanced: true },
  lowValueMinTextChars: number('媒体旁最少有效文字', '字', 0, 20),
  typingSimulatorEnabled: flag('模拟输入等待'),
  typingSpeedCharsPerSecond: number('模拟输入速度', '字/秒', 0),
  typingMaxDelaySeconds: number('模拟输入最长等待', '秒', 0, undefined, 0.1),
  imageProcessingEnabled: flag('理解群聊图片'),
  imageScope: { ...text('群聊图片处理范围'), advanced: true },
  imageProviderId: reference('识图模型', 'models'),
  imagePrompt: longText('识图补充要求'),
  imageTimeoutSeconds: number('识图最长等待', '秒', 0),
  maxImagesPerMessage: number('每条消息最多处理图片', '张', 0),
  imageCacheEnabled: { ...flag('缓存图片理解结果'), advanced: true },
  proactiveCooldownSeconds: number('两次主动接话最短间隔', '秒', 0),
  suppressProactiveWhileBusy: flag('任务忙碌时暂停主动插话'),
  enableModelRouting: flag('聊天与复杂任务使用不同模型'),
  chatModel: text('普通聊天模型'),
  taskModel: text('复杂任务模型'),
  complexMessageChars: { ...number('长消息判断参考长度', '字', 0), advanced: true },
  collectTopicState: flag('跟踪正在聊的话题'),
  taskUnderstandingEnabled: flag('结合上下文理解任务意图'),
  summaryIntervalMessages: number('每隔多少消息整理摘要', '条', 2, 200),
  summaryWindowMessages: number('摘要参考消息数', '条', 2, 200),
  topicTtlHours: number('话题状态保留时间', '小时', 1, 720),
  contextMessagesPerPrompt: number('每次参考最近消息', '条', 6, 200),
  contextTokenBudget: number('对话上下文预算', '词元', 512, 100000),
  maxMessagesPerGroup: number('每个群保留消息上限', '条', 100, 100000),
  messageRetentionHours: number('消息保留时长', '小时', 1, 43800),
  coldRecallEnabled: flag('必要时回看更早的聊天'),
  coldRecallScanMessages: { ...number('更早聊天的检索范围', '条', 100, 20000), advanced: true },
  coldRecallMaxMessages: { ...number('最多取回早期消息', '条', 1, 30), advanced: true },
  searchConnectionId: reference('主要搜索连接', 'connections'),
  searchConnectionIds: { ...list('搜索备用连接', '按顺序尝试，不会自动替换已有选择。'), reference: 'connections' },
  mediaConnectionIds: { ...list('媒体服务连接', '保留已有调用顺序。'), reference: 'connections' },
  searchSummaryEndpointId: reference('搜索摘要模型', 'models'),
  searchModel: text('搜索模型名称'),
  imageModel: text('图片模型名称'),
  imageEditModel: text('图片编辑模型名称'),
  videoModel: text('视频模型名称'),
  searchSummaryMaxChars: number('搜索摘要长度', '字', 0),
  searchMaxSources: number('最多展示来源', '个', 0),
  searchIncludeSourceLinks: flag('回复附带来源链接'),
  videoTimeoutSeconds: number('生成视频最长等待', '秒', 0),
  ttsEnabled: flag('允许语音回复'),
  ttsApiBase: text('语音服务地址'),
  ttsCredentialRef: credential('语音服务密钥环境变量'),
  ttsVoiceId: text('声音名称'),
  ttsLanguage: text('语音语言', '使用语音服务支持的语言代码，例如 zh。'),
  ttsPersonaIds: { ...list('允许语音的角色'), reference: 'personas' },
  ttsAlways: flag('默认使用语音回复'),
  ttsTriggerKeywords: list('触发语音回复的词'),
  ttsMaxChars: number('语音回复文字上限', '字', 0),
  ttsTimeoutSeconds: number('语音生成最长等待', '秒', 0),
  learningWorkerEnabled: flag('启用学习任务处理'),
  learningPollSeconds: { ...number('检查学习任务间隔', '秒', 0), advanced: true },
  autoCapture: flag('从聊天中自动整理记忆'),
  retrievalLimit: number('每次最多召回记忆', '条', 1, 50),
  maxMemoriesPerScope: number('每个范围最多保留记忆', '条', 1, 100000),
  semanticRecallEnabled: flag('按含义查找记忆'),
  allowGroupSharedMemory: flag('允许群内共享记忆', '开启前确认适用范围与隐私边界。'),
  relationshipPulseEnabled: flag('根据互动节奏调整关系感'),
  outputFeedbackEnabled: flag('从纠正与认可中调整表达'),
  memoryResonanceEnabled: flag('关联相似的过往记忆'),
  circadianAwarenessEnabled: flag('考虑日常作息时段'),
  longingEnabled: flag('考虑一段时间未互动的状态'),
  dreamMemoryIsolation: flag('将想象内容与真实记忆隔离'),
  pulseMinInteractions: { ...number('判断关系节奏所需互动', '次', 3, 100), advanced: true },
  rhythmWindowEvents: { ...number('节奏分析参考事件', '条', 10, 500), advanced: true },
  timezoneOffsetMinutes: { ...number('本地时间偏移', '分钟', 0, 840), description: '北京时间为 480。当前接口只接受非负偏移。', advanced: true },
  vectorAlgorithm: choice('语义匹配来源', { local_hash: '本地轻量匹配', remote_embedding: '使用向量模型' }),
  dimensions: { ...number('向量维度', '维', 64, 2048), advanced: true },
  keywordWeight: ratio('关键词匹配权重'),
  vectorWeight: ratio('语义匹配权重'),
  minimumSimilarity: ratio('最低相似程度'),
  topK: number('最终提供的知识片段', '条', 1, 20),
  candidateK: { ...number('参与筛选的候选片段', '条', 0), advanced: true },
  chunkSize: { ...number('文档分段大小', '字', 0), advanced: true },
  chunkOverlap: { ...number('相邻分段重叠长度', '字', 0), advanced: true },
  embeddingEndpointId: reference('向量模型', 'models'),
  rerankEndpointId: reference('结果排序模型', 'models'),
  imageUnderstandingEnabled: flag('理解附件中的图片'),
  allowText: flag('允许纯文本附件'),
  allowDocx: flag('允许 Word 文档'),
  allowPdf: flag('允许 PDF 文档'),
  allowPptx: flag('允许演示文稿'),
  allowXlsx: flag('允许电子表格'),
  maxFileMb: number('单个附件最大大小', 'MB', 1, 100),
  maxExtractChars: number('最多读取附件文字', '字', 1000, 200000),
  extractionTimeoutSeconds: number('读取附件最长等待', '秒', 1, 300),
  recentAttachmentTtlSeconds: number('附件可续聊时间', '秒', 0, 31536000),
  recentAttachmentMax: number('最近附件保留上限', '份', 1, 5000),
  recentAttachmentContextMax: number('一次带入最近附件', '份', 1, 50),
  mediaRetentionHours: number('媒体文件保留时间', '小时', 24, 43800),
  mediaGCIntervalMinutes: { ...number('媒体清理检查间隔', '分钟', 15, 1440), advanced: true },
  statusUrl: text('渠道状态地址'),
  statusTitle: text('渠道状态标题'),
  requestTimeoutSeconds: number('请求最长等待', '秒', 0),
  cardPageUrl: text('状态卡片页面地址'),
  cardBrowserUrl: { ...text('卡片截图服务地址'), advanced: true },
  cardCaptureTimeoutSeconds: number('卡片截图最长等待', '秒', 10, 90),
  commandAliases: list('查询渠道状态的命令', '每条命令以 / 开头。'),
  timelinePoints: number('状态趋势展示点数', '个', 0),
  evaluationWindowMinutes: number('评估最近时段', '分钟', 1, 60),
  evaluationPollSeconds: { ...number('状态评估检查间隔', '秒', 15, 300), advanced: true },
  groupMultipliers: { label: '分组倍率', kind: 'object', advanced: true },
  showMultiplierNote: flag('显示倍率说明'),
  radarEnabled: flag('启用雷达查询'),
  radarUrl: text('雷达数据地址'),
  radarCommandAliases: list('雷达命令'),
  radarMinimumSamples: number('雷达最少样本数', '个', 1, 10000),
  radarFamilyOrder: list('雷达模型系列顺序'),
  radarRecommendationOrder: list('雷达推荐顺序'),
  radarRecommendations: { label: '雷达推荐设置', kind: 'object', advanced: true },
  summaryUrl: text('积分汇总地址'),
  registerBaseUrl: text('邀请注册页面地址'),
  pointsPerPaidInvitee: number('每位付费邀请用户奖励', '积分', 1, 1000000000),
  checkInPoints: number('每日签到奖励', '积分', 1, 1000000000),
  bindAliases: list('绑定账号命令'),
  linkAliases: list('邀请链接命令'),
  pointsAliases: list('查询积分命令'),
  checkInAliases: list('签到命令'),
  redeemAliases: list('积分兑换命令'),
  lotteryUrl: text('抽奖页面地址'),
  lotteryAliases: list('抽奖命令'),
  defaultImageCount: number('默认生成图片数', '张', 0),
  timeoutSeconds: number('最长等待时间', '秒', 0),
  maxImageCount: number('单次生成图片上限', '张', 0),
  maxRetryAttempts: number('生成失败最多重试', '次', 0),
  maxConcurrentTasks: number('同时生成的任务数', '个', 0),
  maxQueuedTasks: number('最多排队任务数', '个', 0),
  rateLimitSeconds: number('请求最短间隔', '秒', 0),
  dailyLimitEnabled: flag('启用生成次数限制'),
  dailyLimitCount: number('每日生成限制', '次', 0),
  maxImageSizeMb: number('单张图片大小上限', 'MB', 0),
  promptAuditEnabled: flag('检查生成要求'),
  promptAuditProviderId: reference('生成要求检查模型', 'models'),
  historyEnabled: flag('保留生成记录'),
  historyLimit: number('生成记录数量上限', '条', 0),
  historyRetentionDays: number('生成记录保留时长', '天', 0),
  visualDirectorEnabled: flag('自动安排拍摄细节'),
  visualUseTimeContext: flag('参考当前时间安排画面'),
  visualTimezone: text('拍摄时间使用的时区', '例如 Asia/Shanghai。'),
  visualVariationEnabled: flag('增加穿搭、场景与视角变化'),
  mediaQualityEnabled: flag('检查生成媒体质量'),
  mediaQualityEndpointId: reference('媒体质量检查模型', 'models'),
};

const platform: Record<string, FieldSpec> = {
  appid: text('应用编号'),
  admin_ids: longText('管理员账号', '按平台现有格式填写账号标识。'),
  admin_openids: longText('QQ 管理员账号标识'),
  app_id: text('应用编号'),
  client_id: text('应用编号'),
  corpid: text('企业编号'),
  agent_id: text('应用编号'),
  kf_name: text('客服名称'),
  enable_group_c2c: flag('接收群聊与私聊'),
  enable_guild_direct_message: flag('接收频道私信'),
  is_sandbox: flag('使用测试环境'),
  unified_webhook_mode: flag('使用统一消息回调'),
  callback_server_host: { ...text('回调监听地址'), advanced: true },
  port: { ...number('回调监听端口', undefined, 1, 65535), advanced: true },
  ws_reverse_host: { ...text('反向连接监听地址'), advanced: true },
  ws_reverse_port: { ...number('反向连接端口', undefined, 1, 65535), advanced: true },
  card_template_id: text('卡片模板编号'),
  domain: text('开放平台地址'),
  discord_proxy: { ...text('代理地址'), advanced: true },
  discord_command_register: flag('注册平台命令'),
  discord_activity_name: text('在线状态文字'),
  discord_allow_bot_messages: flag('接收其他机器人的消息'),
  lark_connection_mode: choice('连接方式', { socket: '长连接', webhook: '消息回调' }),
  slack_connection_mode: choice('连接方式', { socket: '长连接', webhook: '消息回调' }),
  wecom_ai_bot_connection_mode: choice('连接方式', { long_connection: '长连接', webhook: '消息回调' }),
  misskey_default_visibility: choice('默认可见范围', { public: '公开', home: '主页', followers: '关注者' }),
  misskey_local_only: flag('仅在当前站点可见'),
  misskey_enable_chat: flag('接收聊天消息'),
  misskey_allow_insecure_downloads: { ...flag('允许不安全的下载地址'), advanced: true },
  misskey_enable_file_upload: flag('允许上传文件'),
  misskey_upload_folder: text('上传文件夹'),
  max_message_length: number('消息长度上限', '字', 0),
  satori_auto_reconnect: flag('断线后自动重连'),
  start_message: longText('首次使用欢迎语'),
  telegram_command_register: flag('注册平台命令'),
  telegram_command_auto_refresh: flag('自动更新平台命令'),
  telegram_user_api_id: number('应用编号', undefined, 0),
  telegram_user_phone: text('登录手机号'),
  telegram_user_session_dir: { ...text('登录会话存放位置'), advanced: true },
  telegram_user_device_model: { ...text('设备名称'), advanced: true },
  telegram_user_system_version: { ...text('设备系统版本'), advanced: true },
  telegram_user_app_version: { ...text('客户端版本'), advanced: true },
  telegram_user_lang_code: text('客户端语言'),
  telegram_user_receive_groups: flag('接收群消息'),
  telegram_user_receive_private: flag('接收私聊'),
  telegram_user_download_media: flag('下载收到的媒体'),
  telegram_user_proactive_enabled: flag('允许主动发消息'),
  telegram_user_allow_chats: longText('允许使用的会话'),
  webchat_present_type: choice('网页聊天显示方式', { fullscreen: '独立页面', embedded: '嵌入其他页面' }),
  webchat_link_path: text('网页聊天入口'),
  wecom_ai_bot_name: text('机器人名称'),
  wecomaibot_ws_bot_id: text('机器人编号'),
  wecomaibot_init_respond_text: text('首次响应文字'),
  wecomaibot_friend_message_welcome_text: text('新联系人欢迎语'),
  only_use_webhook_url_to_send: flag('仅通过回调地址发送'),
  weixin_oc_bot_type: text('微信机器人类型'),
  active_send_mode: flag('主动发送回复'),
  passive_reply_placeholder: longText('等待回复时的提示'),
};

const platformEndpoints: Record<string, string> = {
  dingtalk_api_base_url: '钉钉接口地址', dingtalk_oapi_base_url: '钉钉兼容接口地址', dingtalk_public_base_url: '钉钉公网地址',
  discord_api_base_url: 'Discord 接口地址', discord_gateway_url: 'Discord 消息网关', kook_api_base_url: 'KOOK 接口地址', kook_gateway_url: 'KOOK 消息网关',
  lark_public_base_url: '飞书公网地址', lark_webhook_path: '飞书回调路径', line_api_base_url: 'LINE 接口地址', line_data_base_url: 'LINE 文件接口地址',
  line_public_base_url: 'LINE 公网地址', line_video_preview_url: '视频预览地址', mattermost_url: 'Mattermost 服务地址', misskey_instance_url: 'Misskey 站点地址',
  satori_api_base_url: 'Satori 接口地址', satori_endpoint: 'Satori 消息连接地址', slack_api_base_url: 'Slack 接口地址', slack_public_base_url: 'Slack 公网地址',
  slack_webhook_path: 'Slack 回调路径', telegram_api_base_url: 'Telegram 接口地址', telegram_file_base_url: 'Telegram 文件接口地址', telegram_public_base_url: 'Telegram 公网地址',
  webchat_api_path: '网页聊天接口路径', api_base_url: '平台接口地址', wecom_public_base_url: '企业微信公网地址', wecomaibot_ws_url: '企业微信长连接地址',
  wecom_ai_public_base_url: '企业微信机器人公网地址', weixin_oc_base_url: '个人微信接口地址', weixin_oc_cdn_base_url: '个人微信文件地址', weixin_oc_public_base_url: '个人微信公网地址', weixin_public_base_url: '微信公众号公网地址',
};
for (const [key, label] of Object.entries(platformEndpoints)) platform[key] = { ...text(label), advanced: true };
for (const key of ['dingtalk_media_host', 'lark_webhook_host', 'slack_webhook_host', 'telegram_media_host', 'webchat_host', 'weixin_oc_media_host']) {
  platform[key] = { ...text('监听地址'), advanced: true };
}
for (const key of ['dingtalk_media_port', 'lark_webhook_port', 'slack_webhook_port', 'telegram_media_port', 'webchat_port', 'weixin_oc_media_port']) {
  platform[key] = { ...number('监听端口', undefined, 1, 65535), advanced: true };
}
const platformNumbers: Record<string, [string, string]> = {
  kook_reconnect_delay: ['首次重连等待', '秒'], kook_max_reconnect_delay: ['最长重连等待', '秒'], kook_max_retry_delay: ['最长重试等待', '秒'],
  kook_heartbeat_interval: ['心跳间隔', '秒'], kook_heartbeat_timeout: ['心跳超时', '秒'], kook_max_heartbeat_failures: ['允许心跳失败次数', '次'], kook_max_consecutive_failures: ['允许连续失败次数', '次'],
  mattermost_reconnect_delay: ['重连等待', '秒'], misskey_download_timeout: ['下载最长等待', '秒'], misskey_download_chunk_size: ['每段下载大小', '字节'],
  misskey_max_download_bytes: ['下载大小上限', '字节'], misskey_upload_concurrency: ['同时上传数量', '个'], satori_heartbeat_interval: ['心跳间隔', '秒'], satori_reconnect_delay: ['重连等待', '秒'],
  telegram_command_register_interval: ['命令更新间隔', '秒'], telegram_polling_restart_delay: ['消息轮询恢复等待', '秒'], wecomaibot_heartbeat_interval: ['心跳间隔', '秒'],
  weixin_oc_qr_poll_interval: ['扫码状态检查间隔', '秒'], weixin_oc_long_poll_timeout_ms: ['消息轮询最长等待', '毫秒'], weixin_oc_api_timeout_ms: ['接口最长等待', '毫秒'],
};
for (const [key, [label, unit]] of Object.entries(platformNumbers)) platform[key] = { ...number(label, unit, 0), advanced: true, ...(key === 'misskey_max_download_bytes' ? { nullable: true, nullLabel: '不单独设置上限', description: '空值表示不单独指定此项下载大小上限。' } : {}) };

const capabilityOptions = {
  chat: '日常对话', reasoning: '复杂推理', vision: '理解图片', tool_calling: '调用工具', json_output: '结构化输出', long_context: '长上下文',
  web_search: '联网搜索', image_generation: '生成图片', video_generation: '生成视频', embedding: '语义向量', document_reading: '读取文档', code: '代码处理',
};
const platformOptions = {
  qq_official: 'QQ 官方机器人', qq_official_webhook: 'QQ 消息回调', aiocqhttp: 'OneBot QQ 连接', dingtalk: '钉钉', discord: 'Discord', kook: 'KOOK', lark: '飞书', line: 'LINE',
  mattermost: 'Mattermost', misskey: 'Misskey', satori: 'Satori', slack: 'Slack', telegram: 'Telegram 机器人', telegram_user: 'Telegram 用户账号', webchat: '网页聊天',
  wecom: '企业微信', wecom_ai_bot: '企业微信智能机器人', weixin_oc: '个人微信', weixin_official_account: '微信公众号',
};
const credentialNames: Record<string, string> = {
  secret: '应用密钥', token: '校验令牌', client_secret: '应用密钥', discord_token: '机器人令牌', kook_bot_token: '机器人令牌', app_secret: '应用密钥',
  webhook_uuid: '回调校验标识', lark_encrypt_key: '消息加密密钥', lark_verification_token: '回调校验令牌', channel_access_token: '访问令牌', channel_secret: '渠道密钥',
  mattermost_bot_token: '机器人令牌', misskey_token: '访问令牌', satori_token: '访问令牌', bot_token: '机器人令牌', app_token: '应用令牌', signing_secret: '签名密钥',
  telegram_token: '机器人令牌', api_hash: '应用校验密钥', webchat_token: '网页聊天令牌', encoding_aes_key: '消息加密密钥', wecomaibot_ws_secret: '长连接密钥',
  wecomaibot_token: '机器人令牌', wecomaibot_encoding_aes_key: '机器人加密密钥', msg_push_webhook_url: '消息推送地址', ws_reverse_token: '反向连接令牌',
};

function cleanPath(path: string) { return path.split('?')[0].replace(/\/$/, ''); }

function platformItemDefaults() {
  return { id: crypto.randomUUID(), displayName: '新平台连接', type: 'qq_official', enabled: false, settings: {}, credentialRefs: {} };
}

export function fieldSpec(path: string, key: string, parentPath = ''): FieldSpec {
  const apiPath = cleanPath(path);
  const parent = parentPath.replace(/\[\d+\]/g, '').replace(/\.\d+(?=\.|$)/g, '');
  // Arbitrary dictionaries may reuse names such as "mode" or "enabled" without sharing their meaning.
  if (/(?:^|\.)(metadata|headers|env|environment|extraHeaders|environmentOverrides|properties|arguments)(?:\.|$)/.test(parent) || parent.includes('inputSchema.')) {
    return { label: key, advanced: true };
  }
  if (/(?:^|\.)credentialRefs(?:\.|$)/.test(parent)) return { ...credential(credentialNames[key] || key), advanced: false };
  if (/(?:^|\.)envHeaderRefs(?:\.|$)/.test(parent)) return { ...credential(key), advanced: false };
  if (/(?:^|\.)groupMultipliers$/.test(parent)) return number(key, '倍', 0, 100, 0.01);
  if (/(?:^|\.)radarRecommendations$/.test(parent)) return text(key, '填写对应的模型系列名称。');
  if (/(?:^|\.)locks$/.test(parent)) return reference(humanValue('/api/v1/routing/lanes', 'lane', key), 'models');
  if (apiPath.includes('/runtime/media-quotas')) return { ...(quotas[key] || common[key] || { label: key, readOnly: !parent, advanced: true }) };
  if (key === 'mode') {
    if (apiPath.endsWith('/channel_runtime')) return choice('消息处理方式', { off: '暂停处理', shadow: '仅观察，不发送回复', active: '正常处理并回复' });
    if (apiPath.endsWith('/retrieval_policy')) return choice('知识查找方式', { keyword: '按关键词查找', vector: '按含义查找', hybrid: '结合关键词与含义' });
    if (apiPath.endsWith('/routing/control')) return choice('模型选择方式', { auto: '自动选择', manual: '使用指定模型' });
  }
  if (apiPath.includes('/mcp/servers') && key === 'transport') return choice('连接方式', { http: '远程 HTTP 服务', sse: '远程事件流服务', stdio: '服务器本机命令' });
  if (apiPath.includes('/platforms') || apiPath.endsWith('/channel_platforms')) {
    if (key === 'instances' && !parent) return { label: '平台连接列表', kind: 'list', readOnly: false, itemDefaults: platformItemDefaults(), description: '最多提交 64 个连接。此接口仅新增或更新，删除连接请进入对应平台的管理页面。' };
    if (key === 'type' && (!parent || parent === 'instances')) return choice('平台类型', platformOptions, '已有连接的平台类型不能更换；需要其他类型时新建连接。');
    if (platform[key]) return { ...platform[key], ...(/(?:^|\.)settings$/.test(parent) ? { advanced: false } : {}) };
  }
  if (apiPath.includes('/provider-connections')) {
    if (key === 'protocol') return choice('接口协议', { openai_chat_completion: 'OpenAI 对话接口', xai_responses: 'xAI Responses', openai_compatible: '通用 OpenAI 兼容接口', openai_embeddings: 'OpenAI 向量接口', openai_chat_rerank: '聊天模型排序', cohere_rerank: 'Cohere 排序接口' });
    if (key === 'timeoutSeconds') return number('最长等待时间', '秒', 1, 600);
  }
  if (apiPath.includes('/model-endpoints') && key === 'priority') return { ...number('路由优先级', undefined, undefined, undefined, 0.1), advanced: true };
  if (key === 'category' && /\/(visual-references|references)(?:\/|$)/.test(apiPath)) return choice('素材用途', { identity: '身份参考', style: '整体风格', expression: '表情参考', makeup: '妆容参考', outfit: '穿搭参考', scene: '场景参考', motion: '动作参考' });
  if (key === 'outfitLength' && apiPath.includes('/appearance-libraries')) return choice('穿搭长度偏好', { auto: '根据场景自然变化', short: '偏短款', long: '偏长款' });
  if (key === 'position' && apiPath.includes('/worldbook')) return { ...choice('背景设定放置位置', { before_char: '角色设定之前', after_char: '角色设定之后', before_example: '对话示例之前', after_example: '对话示例之后' }), advanced: true };
  if (key === 'layer' && apiPath.includes('/knowledge-bases')) return choice('知识使用范围', { global: '全局共享', domain: '领域知识', exclusive: '指定对象专用' });
  if (key === 'ownerKind' && apiPath.includes('/knowledge-bases')) return choice('专用对象类型', { persona: '角色', instance: '机器人实例' });
  if (['capabilities', 'requiredCapabilities', 'preferredCapabilities'].includes(key) && /\/(model-endpoints|tools|routing\/lanes)(?:\/|$)/.test(apiPath)) {
    return { ...common[key], options: Object.entries(capabilityOptions).map(([value, label]) => ({ value, label })) };
  }
  if (apiPath.endsWith('/content_boundary_policy') && ['sexualAction', 'violenceAction', 'abuseAction', 'provocationAction'].includes(key)) {
    const labels: Record<string, string> = { sexualAction: '色情请求如何处理', violenceAction: '暴力伤害请求如何处理', abuseAction: '遭到辱骂如何处理', provocationAction: '遇到挑衅如何处理' };
    return choice(labels[key], { model: '结合语境判断', refuse: '直接拒绝', counter: '简短回应边界', ignore: '不回复' });
  }
  if (key === 'participationMode' && /\/personas\/|\/agent-instances\/|\/agent-policy-templates\//.test(apiPath)) {
    return { ...common.participationMode, options: [{ value: '', label: '沿用默认' }, ...(common.participationMode.options || [])] };
  }
  if (apiPath.includes('/runtime/config') && runtime[key]) return { ...runtime[key] };
  if (apiPath.includes('/integrations/') && policies[key]) return { ...policies[key] };
  if (common[key]) return { ...common[key] };
  return { label: key, advanced: true, readOnly: !parent, description: !parent ? '当前界面尚未定义此字段，保留已有值。' : undefined };
}

// Scaffolds are opt-in for an explicitly added field or list item; never merge them into loaded settings.
export function settingsDefaults(path: string, parentPath = ''): unknown {
  const route = cleanPath(path);
  const parent = parentPath.replace(/\[\d+\]/g, '').replace(/\.\d+(?=\.|$)/g, '');
  if (route.endsWith('/channel_platforms')) {
    if (!parent) return { instances: [] };
    if (parent === 'instances') return platformItemDefaults();
  }
  if (route.endsWith('/runtime/media-quotas') && /(?:^|\.)whitelist$/.test(parent)) return { label: '', senderRef: '' };
  if (/(?:^|\.)visualStyle$/.test(parent)) return { selfieTypes: [], outfits: [], scenes: [] };
  if (/(?:^|\.)manifest$/.test(parent)) return { category: 'extension', capabilities: [], commands: [] };
  if (/(?:^|\.)inputSchema$/.test(parent)) return { type: 'object', properties: {}, required: [] };
  if (/(?:^|\.)overrides$/.test(parent) || (route.includes('/agent-policy-templates') && parent === 'config') || (!parent && route.includes('/personas/runtime-profiles'))) {
    // Empty strings/lists inherit for these profile fields. Boolean and numeric overrides are explicit opt-ins.
    return {
      chatEndpointId: '', taskEndpointId: '', decisionEndpointId: '', allowedToolIds: [], deniedToolIds: [],
      participationMode: '', participationStyle: '', unaddressedMode: '', addressKeywords: [],
      proactiveEnabled: false, initialReplyProbability: 0.12, afterReplyProbability: 0.24,
      maxReplyChars: 40, maxReplySentences: 2, memoryPolicy: '', searchMode: '', searchReplyStyle: '',
      visualPromptOverride: '', visualStyle: { selfieTypes: [], outfits: [], scenes: [] }, expressionPrompt: '',
    };
  }
  // Platform settings and credential names must be supplied by the selected platform's API catalog.
  if (/(?:^|\.)(metadata|locks|settings|credentialRefs|config|headers|env|properties)$/.test(parent)) return {};
  if (parent) return undefined;
  if (route === '/api/v1/runtime/config') return {
    activePersonaId: null, personaInjectionEnabled: true, knowledgeInjectionEnabled: true, worldbookInjectionEnabled: true,
    protectedRules: '', replyStyle: '', maxReplySentences: 2, maxReplyChars: 40, avoidRepetitiveOpeners: true,
    knowledgeNamespace: 'default', learningEnabled: false, learningTopics: [], learningIntervalHours: 24,
  };
  if (/\/worldbook(?:\/|$)/.test(route)) return { comment: '', keys: [], secondaryKeys: [], content: '', priority: 0, insertionOrder: 0, tokenBudget: null, enabled: true, constant: false, selective: true, position: 'before_char' };
  if (/\/traits(?:\/|$)/.test(route)) return { name: '', description: '', triggers: ['*'], supports: [], conflicts: [], source: '', weight: 1, enabled: true };
  if (/\/samples(?:\/|$)/.test(route)) return { sceneTags: [], relationshipStage: '', emotion: '', context: '', candidateReplies: [], forbiddenExpressions: [], source: '', weight: 1, enabled: true };
  if (/^\/api\/v1\/personas(?:\/[^/]+)?$/.test(route)) return {
    namespace: 'default', name: '', description: '', personality: '', scenario: '', firstMessage: '', systemPrompt: '',
    postHistoryInstructions: '', messageExample: '', alternateGreetings: [], tags: [], creator: '', characterVersion: '',
    sourceFormat: '', sourceVersion: '', avatarDataUri: '', visualDescription: '',
  };
  if (/^\/api\/v1\/agent-instances(?:\/[^/]+)?$/.test(route)) return { displayName: '', personaId: '', policyTemplateId: '', memoryNamespace: '', overrides: {}, enabled: false };
  if (/^\/api\/v1\/agent-policy-templates(?:\/[^/]+)?$/.test(route)) return { name: '', description: '', config: {}, version: 1, enabled: true };
  if (/^\/api\/v1\/appearance-libraries(?:\/[^/]+)?$/.test(route)) return { name: '', description: '', visualDescription: '', outfitLength: 'auto', enabled: true };
  if (/^\/api\/v1\/persona-bindings(?:\/|$)/.test(route)) return { personaId: '', transport: '*', transportInstance: '*', conversationRef: '*', priority: 100, enabled: true };
  if (/^\/api\/v1\/knowledge\/documents(?:\/|$)/.test(route)) return { namespace: 'default', title: '', sourceUri: '', content: '', metadata: {} };
  if (/^\/api\/v1\/tools(?:\/|$)/.test(route)) return { name: '', description: '', capabilities: [], riskLevel: 0, enabled: true, adapterRef: '', allowedAuthorities: ['member', 'admin'], approvalMode: 'auto', timeoutSeconds: 30, inputSchema: { type: 'object', properties: {}, required: [] } };
  if (/^\/api\/v1\/mcp\/servers(?:\/|$)/.test(route)) return { name: '', transport: 'http', endpoint: '', command: '', args: [], toolPrefix: 'mcp', enabled: false, secretRef: '', allowedTools: [], allowedAuthorities: ['admin'], approvalMode: 'admin_only', timeoutSeconds: 30 };
  if (/^\/api\/v1\/skills(?:\/|$)/.test(route)) return { name: '', description: '', instructions: '', enabled: true, activationMode: 'any', triggers: [], attachmentKinds: [], requiredTools: [], requiredMcpServers: [], allowedAuthorities: ['member', 'admin'], personaIds: [], priority: 0 };
  if (/^\/api\/v1\/platforms(?:\/|$)/.test(route)) return { displayName: '', type: 'qq_official', enabled: false, settings: {}, credentialRefs: {} };
  if (/^\/api\/v1\/provider-connections\//.test(route)) return { provider: '', protocol: 'openai_chat_completion', apiBase: '', credentialRef: '', pricingUrl: '', timeoutSeconds: 120, enabled: false };
  if (/^\/api\/v1\/model-endpoints\//.test(route)) return { provider: '', model: '', connectionId: '', enabled: false, capabilities: ['chat'], inputCostPerMillion: 0, outputCostPerMillion: 0, qualityScore: 0.5, priority: 0, maxContextTokens: 0, executionKind: 'llm', adapterRef: '' };
  if (route === '/api/v1/plugins') return { name: '', description: '', version: '0.1.0', author: '', enabled: true, manifest: { category: 'extension', capabilities: [], commands: [] } };
  if (/^\/api\/v1\/trusted-adapters(?:\/|$)/.test(route)) return { name: '', pluginId: '', integrationId: '', version: '1.0.0', permissions: ['health.read'], enabled: true };
  if (/^\/api\/v1\/runtime\/directives(?:\/|$)/.test(route)) return { content: '', enabled: true };
  if (/^\/api\/v1\/memories(?:\/|$)/.test(route)) return { personaId: '', scopeKind: 'user', scopeReference: '', content: '', kind: 'fact', confidence: 1, importance: 0.7 };
  if (route === '/api/v1/routing/control') return { mode: 'auto', locks: {} };
  if (route === '/api/v1/routing/lanes') return { lane: '', requiredCapabilities: [], preferredCapabilities: [] };
  return {};
}

export function humanValue(path: string, key: string, value: unknown): string {
  if (value === null) return fieldSpec(path, key).nullLabel || '未设置';
  if (value === undefined || value === null || value === '') return '未设置';
  if (key === 'credentialConfigured' && typeof value === 'boolean') return value ? '已配置' : '未配置';
  if (typeof value === 'boolean') return value ? '开启' : '关闭';
  if (Array.isArray(value)) {
    if (!value.length) return '未添加';
    const names = value.slice(0, 4).map((item) => {
      if (item && typeof item === 'object' && !Array.isArray(item)) {
        const row = item as Record<string, unknown>;
        return String(row.label || row.name || row.title || '已配置条目');
      }
      return humanValue(path, key, item);
    });
    return names.join('、') + (value.length > 4 ? ` 等 ${value.length} 项` : '');
  }
  if (typeof value === 'object') {
    const size = Object.keys(value as Record<string, unknown>).length;
    return size ? `已配置 ${size} 项` : '暂无单独设置';
  }
  if (key === 'lane') {
    const lanes: Record<string, string> = { chat: '日常聊天', task: '复杂任务', decision: '接话判断', tools: '工具调用', code: '代码任务', reasoning: '复杂推理', search: '联网搜索', vision: '图片理解', document: '文档阅读', image: '生成图片', video: '生成视频', embedding: '语义向量', rerank: '结果排序' };
    return lanes[String(value)] || String(value);
  }
  const spec = fieldSpec(path, key);
  const option = spec.options?.find((item) => item.value === String(value));
  if (option) return option.label;
  if (key === 'timezone' && value === 'Asia/Shanghai') return '北京时间';
  if (typeof value === 'number') {
    if (spec.scale) return `${new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 2 }).format(value * spec.scale)}${spec.unit || ''}`;
    const formatted = new Intl.NumberFormat('zh-CN', { maximumFractionDigits: 6 }).format(value);
    return spec.unit ? `${formatted} ${spec.unit}` : formatted;
  }
  return String(value);
}
