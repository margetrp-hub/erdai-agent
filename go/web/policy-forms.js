function element(form, name) {
  const control = form.elements[name];
  if (!control) throw new Error(`missing policy control: ${name}`);
  return control;
}

function commaList(value) {
  return String(value || '')
    .split(/[,，\n]/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function lineList(value) {
  return String(value || '')
    .split(/\r?\n/)
    .map((item) => item.trim())
    .filter(Boolean);
}

function numericRecord(value) {
  const result = {};
  for (const line of lineList(value)) {
    const match = line.match(/^(.+?)\s*[=:：]\s*(\d+(?:\.\d+)?)$/);
    if (!match) throw new Error(`倍率格式无效: ${line}`);
    result[match[1].trim()] = Number(match[2]);
  }
  return result;
}

function stringRecord(value) {
  const result = {};
  for (const line of lineList(value)) {
    const match = line.match(/^(.+?)\s*[=:：]\s*(.+)$/);
    if (!match) throw new Error(`映射格式无效: ${line}`);
    result[match[1].trim()] = match[2].trim();
  }
  return result;
}

function serialize(form, spec) {
  const payload = {};
  for (const name of spec.booleans || []) payload[name] = element(form, name).checked;
  for (const name of spec.numbers || []) payload[name] = Number(element(form, name).value);
  for (const name of spec.strings || []) payload[name] = element(form, name).value;
  for (const name of spec.lists || []) payload[name] = commaList(element(form, name).value);
  for (const name of spec.lineLists || []) payload[name] = lineList(element(form, name).value);
  for (const name of spec.numericRecords || []) payload[name] = numericRecord(element(form, name).value);
  for (const name of spec.stringRecords || []) payload[name] = stringRecord(element(form, name).value);
  return payload;
}

const MESSAGE_POLICY_SPEC = {
  booleans: [
		'segmentedReplyEnabled', 'toolProgressEnabled', 'toolProgressSearchEnabled',
  ],
  numbers: [
    'segmentMinChars', 'segmentMaxChars', 'maxReplySegments',
    'segmentMinDelaySeconds', 'segmentMaxDelaySeconds',
  ],
  lineLists: [
    'toolProgressSearchMessages', 'toolProgressImageMessages',
	'toolProgressPhotoMessages', 'toolCompletionImageMessages', 'toolProgressVideoMessages',
	'toolCompletionVideoMessages', 'toolProgressDocumentMessages',
	'toolCompletionDocumentMessages',
  ],
};

const CONTENT_BOUNDARY_POLICY_SPEC = {
  booleans: ['enabled'],
  strings: [
    'sexualAction', 'violenceAction', 'abuseAction', 'provocationAction',
    'modelInstruction',
  ],
  lists: [
    'sexualTriggers', 'violenceTriggers', 'abuseTriggers', 'provocationTriggers',
    'sexualContextExceptions', 'violenceContextExceptions', 'abuseContextExceptions',
  ],
  lineLists: ['sexualReplies', 'violenceReplies', 'abuseReplies', 'provocationReplies'],
};

const GROUP_CHAT_POLICY_SPEC = {
  booleans: [
    'enabled', 'decisionIncludePersona', 'smartBatchHintEnabled',
    'groupWaitWindowEnabled', 'includeTimestamp', 'includeSenderInfo',
    'keywordSmartMode', 'messageQualityEnabled', 'replyDensityEnabled',
    'replyDensityAiHint', 'ignoreAtOthers', 'ignoreAtAll',
    'duplicateFilterEnabled', 'typingSimulatorEnabled',
    'imageProcessingEnabled', 'imageCacheEnabled', 'ignoreLowValueMedia',
  ],
  numbers: [
    'initialProbability', 'afterReplyProbability', 'probabilityDurationSeconds',
    'decisionTimeoutSeconds', 'atLinkMaxMessages', 'atLinkMaxSeconds',
    'smartMergeWaitSeconds', 'smartMaxBatchSize', 'smartClaimDelaySeconds',
    'concurrentWaitMaxLoops', 'concurrentWaitIntervalSeconds', 'maxContextMessages',
    'questionBoost', 'waterReduce', 'replyDensityWindowSeconds',
    'replyDensityMaxReplies', 'replyDensitySoftLimitRatio',
    'typingSpeedCharsPerSecond', 'typingMaxDelaySeconds', 'imageTimeoutSeconds',
    'maxImagesPerMessage', 'lowValueMinTextChars',
  ],
  strings: [
    'participationMode',
    'decisionProviderId', 'decisionPromptMode', 'decisionExtraPrompt',
    'replyPromptMode', 'replyExtraPrompt', 'concurrentMode',
    'ignoreAtOthersMode', 'imageScope', 'imageProviderId', 'imagePrompt',
  ],
  lists: ['enabledGroups', 'triggerKeywords', 'commandPrefixes', 'lowValueMediaMarkers'],
};

const COMPANION_POLICY_SPEC = {
	booleans: ['enabled', 'enableModelRouting', 'collectTopicState', 'coldRecallEnabled'],
  numbers: [
    'complexMessageChars', 'summaryIntervalMessages', 'summaryWindowMessages',
		'topicTtlHours',
		'contextMessagesPerPrompt', 'contextTokenBudget', 'maxMessagesPerGroup', 'messageRetentionHours',
		'coldRecallScanMessages', 'coldRecallMaxMessages',
  ],
	strings: ['chatModel', 'taskModel'],
  lists: ['enabledGroups'],
};

const GROK_POLICY_SPEC = {
	booleans: ['enabled', 'learningWorkerEnabled', 'searchIncludeSourceLinks'],
	numbers: ['searchSummaryMaxChars', 'searchMaxSources', 'videoTimeoutSeconds', 'learningPollSeconds'],
	strings: ['searchConnectionId', 'searchModel', 'imageModel', 'imageEditModel', 'videoModel'],
	lists: ['searchConnectionIds', 'mediaConnectionIds'],
};

const MEMORY_POLICY_SPEC = {
  booleans: [
    'enabled', 'autoCapture', 'allowGroupSharedMemory', 'relationshipPulseEnabled',
    'outputFeedbackEnabled', 'memoryResonanceEnabled', 'circadianAwarenessEnabled',
    'longingEnabled', 'dreamMemoryIsolation',
  ],
  numbers: [
    'retrievalLimit', 'maxMemoriesPerScope', 'pulseMinInteractions',
    'rhythmWindowEvents', 'timezoneOffsetMinutes',
  ],
};

const RETRIEVAL_POLICY_SPEC = {
  booleans: ['enabled'],
  numbers: ['dimensions', 'keywordWeight', 'vectorWeight', 'minimumSimilarity', 'topK', 'candidateK', 'chunkSize', 'chunkOverlap'],
  strings: ['mode', 'vectorAlgorithm', 'embeddingEndpointId', 'rerankEndpointId'],
};

const DOCUMENT_POLICY_SPEC = {
  booleans: [
    'enabled', 'imageUnderstandingEnabled', 'allowText', 'allowDocx',
		'allowPdf', 'allowPptx', 'allowXlsx',
	],
	numbers: ['maxFileMb', 'maxExtractChars', 'extractionTimeoutSeconds', 'recentAttachmentTtlSeconds', 'recentAttachmentMax', 'recentAttachmentContextMax', 'mediaRetentionHours', 'mediaGCIntervalMinutes'],
};

const OPS_POLICY_SPEC = {
  booleans: ['enabled', 'showMultiplierNote', 'radarEnabled'],
  numbers: [
    'requestTimeoutSeconds', 'timelinePoints', 'evaluationWindowMinutes',
    'evaluationPollSeconds', 'radarMinimumSamples',
  ],
  strings: ['statusUrl', 'statusTitle', 'credentialRef', 'radarUrl'],
  lists: [
    'commandAliases', 'radarCommandAliases', 'radarFamilyOrder',
    'radarRecommendationOrder',
  ],
  numericRecords: ['groupMultipliers'],
  stringRecords: ['radarRecommendations'],
};

const IMAGE_POLICY_SPEC = {
  numbers: [
    'defaultImageCount', 'maxImageCount', 'maxImagesPerMessage', 'timeoutSeconds',
    'maxRetryAttempts', 'maxConcurrentTasks', 'maxQueuedTasks', 'rateLimitSeconds',
    'dailyLimitCount', 'maxImageSizeMb', 'historyLimit', 'historyRetentionDays',
  ],
  strings: ['providerId', 'model', 'credentialRef', 'promptAuditProviderId', 'visualTimezone'],
  booleans: [
    'enabled', 'dailyLimitEnabled', 'promptAuditEnabled', 'historyEnabled',
    'visualDirectorEnabled', 'visualUseTimeContext',
  ],
  lineLists: ['selfieTypes'],
};

export function serializeMessagePolicy(form) {
  return serialize(form, MESSAGE_POLICY_SPEC);
}

export function serializeContentBoundaryPolicy(form) {
  return serialize(form, CONTENT_BOUNDARY_POLICY_SPEC);
}

export function serializeGroupChatPolicy(form) {
  return serialize(form, GROUP_CHAT_POLICY_SPEC);
}

export function serializeCompanionPolicy(form) {
  return serialize(form, COMPANION_POLICY_SPEC);
}

export function serializeGrokPolicy(form) {
  return serialize(form, GROK_POLICY_SPEC);
}

export function serializeMemoryPolicy(form) {
  return serialize(form, MEMORY_POLICY_SPEC);
}

export function serializeRetrievalPolicy(form) {
  return serialize(form, RETRIEVAL_POLICY_SPEC);
}

export function serializeDocumentPolicy(form) {
  return serialize(form, DOCUMENT_POLICY_SPEC);
}

export function serializeOpsPolicy(form) {
  return serialize(form, OPS_POLICY_SPEC);
}

export function serializeImagePolicy(form) {
  return serialize(form, IMAGE_POLICY_SPEC);
}
