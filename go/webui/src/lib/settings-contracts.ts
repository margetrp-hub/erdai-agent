import type { JsonMap } from './api';

export type WriteMethod = 'PUT' | 'POST';
const keys = (value: string) => value.split(' ');

// These are write contracts, not GET response shapes. Keep nested extension data intact.
export const integrationFields: Record<string, string[]> = Object.fromEntries(Object.entries({
  channel_runtime: 'mode captureUnaddressedGroups deliveryPollSeconds',
  qq_official: 'enabled groupC2C guildDirectMessage credentialConfigured platformId appid adminOpenIds credentialRefs',
  provider_policy: 'providerId providerType apiBase credentialRef defaultModel fallbackModels streaming webSearch providerRetries maxAgentSteps toolCallTimeoutSeconds credentialConfigured',
  message_policy: 'segmentedReplyEnabled segmentMinChars segmentMaxChars maxReplySegments segmentMinDelaySeconds segmentMaxDelaySeconds toolProgressEnabled toolProgressSearchEnabled toolProgressSearchMessages toolProgressImageMessages toolProgressPhotoMessages toolCompletionImageMessages toolProgressVideoMessages toolCompletionVideoMessages toolProgressDocumentMessages toolCompletionDocumentMessages humanPacingEnabled humanPacingMaxSeconds moodContinuityEnabled',
  content_boundary_policy: 'enabled sexualAction violenceAction abuseAction provocationAction sexualTriggers violenceTriggers abuseTriggers provocationTriggers sexualContextExceptions violenceContextExceptions abuseContextExceptions sexualReplies violenceReplies abuseReplies provocationReplies modelInstruction',
  group_chat_policy: 'enabled enabledGroups initialProbability afterReplyProbability probabilityDurationSeconds decisionProviderId decisionIncludePersona decisionTimeoutSeconds decisionPromptMode decisionExtraPrompt replyPromptMode replyExtraPrompt atLinkMaxMessages atLinkMaxSeconds concurrentMode smartBatchHintEnabled smartMergeWaitSeconds smartMaxBatchSize smartClaimDelaySeconds concurrentWaitMaxLoops concurrentWaitIntervalSeconds groupWaitWindowEnabled maxContextMessages includeTimestamp includeSenderInfo triggerKeywords keywordSmartMode commandPrefixes messageQualityEnabled questionBoost waterReduce replyDensityEnabled replyDensityWindowSeconds replyDensityMaxReplies replyDensitySoftLimitRatio replyDensityAiHint ignoreAtOthers ignoreAtOthersMode ignoreAtAll duplicateFilterEnabled participationMode proactiveChatEnabled ignoreLowValueMedia lowValueMediaMarkers lowValueMinTextChars typingSimulatorEnabled typingSpeedCharsPerSecond typingMaxDelaySeconds imageProcessingEnabled imageScope imageProviderId imagePrompt imageTimeoutSeconds maxImagesPerMessage imageCacheEnabled proactiveCooldownSeconds suppressProactiveWhileBusy',
  companion_policy: 'enabled enabledGroups enableModelRouting chatModel taskModel complexMessageChars collectTopicState taskUnderstandingEnabled summaryIntervalMessages summaryWindowMessages topicTtlHours contextMessagesPerPrompt contextTokenBudget maxMessagesPerGroup messageRetentionHours coldRecallEnabled coldRecallScanMessages coldRecallMaxMessages',
  grok_policy: 'enabled apiBase credentialRef searchConnectionId searchSummaryEndpointId searchModel imageModel imageEditModel videoModel searchConnectionIds mediaConnectionIds searchSummaryMaxChars searchMaxSources searchIncludeSourceLinks videoTimeoutSeconds ttsEnabled ttsApiBase ttsCredentialRef ttsVoiceId ttsLanguage ttsPersonaIds ttsAlways ttsTriggerKeywords ttsMaxChars ttsTimeoutSeconds learningWorkerEnabled learningPollSeconds',
  memory_policy: 'enabled autoCapture retrievalLimit maxMemoriesPerScope semanticRecallEnabled allowGroupSharedMemory relationshipPulseEnabled outputFeedbackEnabled memoryResonanceEnabled circadianAwarenessEnabled longingEnabled dreamMemoryIsolation pulseMinInteractions rhythmWindowEvents timezoneOffsetMinutes',
  retrieval_policy: 'enabled mode vectorAlgorithm dimensions keywordWeight vectorWeight minimumSimilarity topK candidateK chunkSize chunkOverlap embeddingEndpointId rerankEndpointId',
  document_policy: 'enabled imageUnderstandingEnabled allowText allowDocx allowPdf allowPptx allowXlsx maxFileMb maxExtractChars extractionTimeoutSeconds recentAttachmentTtlSeconds recentAttachmentMax recentAttachmentContextMax mediaRetentionHours mediaGCIntervalMinutes',
  ops_policy: 'enabled statusUrl statusTitle credentialRef requestTimeoutSeconds cardPageUrl cardBrowserUrl cardCaptureTimeoutSeconds commandAliases timelinePoints evaluationWindowMinutes evaluationPollSeconds groupMultipliers showMultiplierNote radarEnabled radarUrl radarCommandAliases radarMinimumSamples radarFamilyOrder radarRecommendationOrder radarRecommendations',
  affiliate_policy: 'enabled summaryUrl registerBaseUrl credentialRef requestTimeoutSeconds pointsPerPaidInvitee checkInPoints bindAliases linkAliases pointsAliases checkInAliases redeemAliases lotteryUrl lotteryAliases',
  image_policy: 'enabled providerId model credentialRef defaultImageCount maxImageCount maxImagesPerMessage timeoutSeconds maxRetryAttempts maxConcurrentTasks maxQueuedTasks rateLimitSeconds dailyLimitEnabled dailyLimitCount maxImageSizeMb promptAuditEnabled promptAuditProviderId historyEnabled historyLimit historyRetentionDays visualDirectorEnabled visualUseTimeContext visualTimezone selfieTypes visualVariationEnabled mediaQualityEnabled mediaQualityEndpointId',
  channel_platforms: 'instances',
}).map(([id, value]) => [id, keys(value)]));

const personaFields = keys('name description personality scenario firstMessage systemPrompt postHistoryInstructions messageExample alternateGreetings tags creator characterVersion sourceFormat sourceVersion avatarDataUri visualDescription');
const platformFields = keys('displayName enabled credentialConfigured settings credentialRefs');
const modelFields = keys('provider model enabled capabilities inputCostPerMillion outputCostPerMillion qualityScore priority maxContextTokens executionKind adapterRef connectionId');

export function settingPath(path: string) { return path.split('?')[0]; }

export function writableFields(path: string, method: WriteMethod): string[] {
  const route = settingPath(path);
  const create = method === 'POST';
  const withID = (fields: string[]) => create ? ['id', ...fields] : fields;
  if (route.startsWith('/api/v1/integrations/')) return integrationFields[decodeURIComponent(route.slice('/api/v1/integrations/'.length))] || [];
  if (route === '/api/v1/runtime/media-quotas') return keys('imageDailyLimit videoDailyLimit trustedAdminBypass whitelist');
  if (route === '/api/v1/runtime/config') return keys('activePersonaId personaInjectionEnabled knowledgeInjectionEnabled worldbookInjectionEnabled protectedRules replyStyle maxReplySentences maxReplyChars avoidRepetitiveOpeners knowledgeNamespace learningEnabled learningTopics learningIntervalHours');
  if (/^\/api\/v1\/relationships\//.test(route)) return keys('intimacy locked');
  if (/^\/api\/v1\/memories(?:\/|$)/.test(route)) return keys('personaId scopeKind scopeReference content kind confidence importance expiresAt');
  if (/\/(?:visual-references|references)\/[^/]+$/.test(route)) return keys('category label promptNotes isPrimary enabled sortOrder');
  if (/\/worldbook(?:\/|$)/.test(route)) return withID(keys('keys secondaryKeys comment content enabled constant selective priority position insertionOrder tokenBudget'));
  if (/\/traits(?:\/|$)/.test(route)) return withID(keys('name description triggers supports conflicts source weight enabled'));
  if (/\/samples(?:\/|$)/.test(route)) return withID(keys('sceneTags relationshipStage emotion context candidateReplies forbiddenExpressions source weight enabled'));
  if (/^\/api\/v1\/personas(?:\/[^/]+)?$/.test(route)) return create ? ['id', 'namespace', ...personaFields] : personaFields;
  if (/^\/api\/v1\/appearance-libraries(?:\/[^/]+)?$/.test(route)) return keys('name description visualDescription outfitLength enabled').concat(create ? ['sourcePersonaId'] : []);
  if (/^\/api\/v1\/persona-bindings(?:\/|$)/.test(route)) return keys('personaId transport transportInstance conversationRef priority enabled');
  if (/^\/api\/v1\/agent-instances(?:\/[^/]+)?$/.test(route)) return keys('id displayName personaId policyTemplateId memoryNamespace overrides enabled');
  if (/^\/api\/v1\/knowledge\/documents(?:\/|$)/.test(route)) return create ? keys('id namespace title sourceUri content metadata') : keys('title sourceUri content metadata');
  if (/^\/api\/v1\/skills(?:\/|$)/.test(route)) return withID(keys('name description instructions enabled activationMode triggers attachmentKinds requiredTools requiredMcpServers allowedAuthorities personaIds priority'));
  if (/^\/api\/v1\/tools(?:\/|$)/.test(route)) return withID(keys('name description capabilities riskLevel enabled adapterRef allowedAuthorities approvalMode timeoutSeconds inputSchema'));
  if (/^\/api\/v1\/mcp\/servers(?:\/|$)/.test(route)) return withID(keys('name transport endpoint command args toolPrefix enabled secretRef allowedTools allowedAuthorities approvalMode timeoutSeconds'));
  if (/^\/api\/v1\/platforms(?:\/[^/]+)?$/.test(route)) return create ? ['id', 'type', ...platformFields] : platformFields;
  if (/^\/api\/v1\/provider-connections\//.test(route)) return keys('provider protocol apiBase credentialRef pricingUrl timeoutSeconds enabled');
  if (/^\/api\/v1\/model-endpoints\//.test(route)) return modelFields;
  if (route === '/api/v1/routing/control') return keys('mode locks');
  if (route === '/api/v1/routing/lanes') return keys('lane requiredCapabilities preferredCapabilities');
  if (/^\/api\/v1\/runtime\/directives(?:\/|$)/.test(route)) return withID(keys('content enabled'));
  if (route === '/api/v1/plugins') return keys('id name description version author enabled integrationId manifest');
  if (/^\/api\/v1\/trusted-adapters(?:\/|$)/.test(route)) return create ? keys('id name pluginId integrationId version permissions enabled') : keys('name version permissions enabled');
  throw new Error('这个设置暂未定义安全的保存方式，请更新管理页面后重试。');
}

export function asSettingsObject(value: unknown): JsonMap {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('设置必须是一组字段，不能是空值或列表。');
  return value as JsonMap;
}

export function projectSettings(path: string, method: WriteMethod, value: unknown): JsonMap {
  let source = asSettingsObject(value);
  const route = settingPath(path);
  if (route.startsWith('/api/v1/integrations/') && source.config && typeof source.config === 'object') source = asSettingsObject(source.config);
  if (route.startsWith('/api/v1/relationships/') && source.state) {
    const state = asSettingsObject(source.state);
    source = { intimacy: state.intimacy, locked: state.intimacyLocked === true };
  }
  const allowed = writableFields(path, method);
  const result = Object.fromEntries(Object.entries(source).filter(([key]) => allowed.includes(key)));
  if (route === '/api/v1/integrations/channel_platforms' && Array.isArray(result.instances)) {
    result.instances = result.instances.map((item) => projectSettings('/api/v1/platforms', 'POST', item));
  }
  return result;
}

export function settingsPayload(path: string, method: WriteMethod, value: unknown): JsonMap {
  const source = asSettingsObject(value);
  const allowed = writableFields(path, method);
  const unknown = Object.keys(source).filter((key) => !allowed.includes(key));
  if (unknown.length) throw new Error(`包含无法保存的字段：${unknown.join('、')}。请重新打开设置后重试。`);
  const route = settingPath(path);
  if (route === '/api/v1/runtime/media-quotas') {
    for (const key of ['imageDailyLimit', 'videoDailyLimit']) {
      if (!Number.isInteger(source[key]) || Number(source[key]) < 0 || Number(source[key]) > 100) throw new Error('图片和视频的每日额度都需要填写 0–100 的整数。');
    }
  }
  if (route.startsWith('/api/v1/relationships/')) {
    if (typeof source.locked !== 'boolean') throw new Error('请选择关系自动发展，或固定亲密度。');
    if (source.locked && (typeof source.intimacy !== 'number' || source.intimacy < 0 || source.intimacy > 100)) throw new Error('固定亲密度需要填写 0–100 之间的数字。');
  }
  return source;
}

export function replacesSettings(path: string): boolean {
  const route = settingPath(path);
  return /^\/api\/v1\/(model-endpoints|persona-bindings|memories)\//.test(route) || ['/api/v1/routing/control', '/api/v1/routing/lanes', '/api/v1/integrations/channel_platforms'].includes(route);
}

export function sameSettings(left: unknown, right: unknown): boolean {
  if (Object.is(left, right)) return true;
  if (Array.isArray(left) || Array.isArray(right)) return Array.isArray(left) && Array.isArray(right) && left.length === right.length && left.every((value, index) => sameSettings(value, right[index]));
  if (!left || !right || typeof left !== 'object' || typeof right !== 'object') return false;
  const a = left as JsonMap, b = right as JsonMap;
  return Object.keys(a).length === Object.keys(b).length && Object.keys(a).every((key) => Object.hasOwn(b, key) && sameSettings(a[key], b[key]));
}

export function changedSettings(original: JsonMap, next: JsonMap): JsonMap {
  return Object.fromEntries(Object.entries(next).filter(([key, value]) => !sameSettings(original[key], value)));
}

// A server may add defaults to an object. Submitted values and intentional removals
// must still match; arrays are ordered values and must be preserved exactly.
export function submittedSettingsMatch(submitted: unknown, actual: unknown, original?: unknown): boolean {
  if (sameSettings(submitted, actual)) return true;
  if (typeof submitted === 'string') return actual === submitted.trim();
  if (!submitted || !actual || typeof submitted !== 'object' || typeof actual !== 'object' || Array.isArray(submitted) || Array.isArray(actual)) return false;
  const expected = submitted as JsonMap;
  const saved = actual as JsonMap;
  const before = original && typeof original === 'object' && !Array.isArray(original) ? original as JsonMap : {};
  if (!Object.entries(expected).every(([key, value]) => Object.hasOwn(saved, key) && submittedSettingsMatch(value, saved[key], before[key]))) return false;
  return Object.keys(before).every((key) => Object.hasOwn(expected, key) || !Object.hasOwn(saved, key));
}
