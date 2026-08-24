const AVATAR_TYPES = new Set(['image/png', 'image/jpeg', 'image/webp']);
const MAX_AVATAR_DATA_URI_LENGTH = 720_000;

function cleanText(value, maximum = 20_000) {
  return typeof value === 'string'
    ? value.replace(/[\u0000-\u0008\u000B\u000C\u000E-\u001F\u007F]/g, '').trim().slice(0, maximum)
    : '';
}

function cleanList(value, maximumItems = 64, maximumLength = 200) {
  if (!Array.isArray(value)) return [];
  return [...new Set(value.map((item) => cleanText(item, maximumLength)).filter(Boolean))].slice(0, maximumItems);
}

function cleanInteger(value, fallback = 0) {
  const parsed = Number.parseInt(value, 10);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function cleanPosition(value) {
  const position = cleanText(value, 40).toLowerCase();
  return ['before_char', 'after_char', 'before_example', 'after_example'].includes(position)
    ? position
    : 'before_char';
}

export function normalizeAvatarDataUri(value) {
  if (typeof value !== 'string') return '';
  const avatar = value.trim();
  if (!avatar) return '';
  if (avatar.length > MAX_AVATAR_DATA_URI_LENGTH) throw new RangeError('头像不能超过 512 KiB');
  const match = /^data:(image\/(?:png|jpeg|webp));base64,([a-z0-9+/]+={0,2})$/i.exec(avatar);
  if (!match || !AVATAR_TYPES.has(match[1].toLowerCase())) {
    throw new TypeError('头像只支持 PNG、JPEG 或 WebP');
  }
  const padding = match[2].endsWith('==') ? 2 : match[2].endsWith('=') ? 1 : 0;
  if (Math.floor(match[2].length * 3 / 4) - padding > 512 * 1024) {
    throw new RangeError('头像不能超过 512 KiB');
  }
  return avatar;
}

function personaFields(source = {}) {
  return {
    name: cleanText(source.name, 120),
		description: cleanText(source.description),
		visualDescription: cleanText(source.visualDescription ?? source.visual_description, 4_000),
    personality: cleanText(source.personality),
    scenario: cleanText(source.scenario),
    firstMessage: cleanText(source.firstMessage ?? source.first_mes),
    systemPrompt: cleanText(source.systemPrompt ?? source.system_prompt),
    postHistoryInstructions: cleanText(source.postHistoryInstructions ?? source.post_history_instructions),
    messageExample: cleanText(source.messageExample ?? source.mes_example),
    alternateGreetings: cleanList(source.alternateGreetings ?? source.alternate_greetings, 20, 4_000),
    tags: cleanList(source.tags, 64, 100),
    creator: cleanText(source.creator, 200),
    characterVersion: cleanText(source.characterVersion ?? source.character_version, 100),
    avatarDataUri: normalizeAvatarDataUri(source.avatarDataUri ?? source.avatar_data_uri),
  };
}

function worldbookFields(entry = {}, index = 0) {
  return {
    comment: cleanText(entry.comment, 500),
    keys: cleanList(entry.keys, 64, 200),
    secondaryKeys: cleanList(entry.secondaryKeys ?? entry.secondary_keys, 64, 200),
    content: cleanText(entry.content),
    enabled: entry.enabled !== false,
    constant: entry.constant === true,
    selective: entry.selective === true,
    priority: cleanInteger(entry.priority),
    position: cleanPosition(entry.position),
    insertionOrder: cleanInteger(entry.insertionOrder ?? entry.insertion_order, index),
    tokenBudget: entry.tokenBudget == null && entry.token_budget == null
      ? null
      : Math.max(0, cleanInteger(entry.tokenBudget ?? entry.token_budget)),
  };
}

function traitFields(trait = {}) {
  const weight = Number(trait.weight);
  return {
    name: cleanText(trait.name, 120),
    description: cleanText(trait.description, 2_000),
    triggers: cleanList(trait.triggers, 32, 100),
    supports: cleanList(trait.supports, 24, 120),
    conflicts: cleanList(trait.conflicts, 24, 120),
    source: cleanText(trait.source, 1_000),
    weight: Number.isFinite(weight) ? Math.min(100, Math.max(0, weight)) : 1,
    enabled: trait.enabled !== false,
  };
}

function sampleFields(sample = {}) {
  const weight = Number(sample.weight);
  return {
    sceneTags: cleanList(sample.sceneTags, 32, 80),
    relationshipStage: cleanText(sample.relationshipStage, 80),
    emotion: cleanText(sample.emotion, 80),
    context: cleanText(sample.context, 4_000),
    candidateReplies: cleanList(sample.candidateReplies, 16, 500),
    forbiddenExpressions: cleanList(sample.forbiddenExpressions, 32, 200),
    source: cleanText(sample.source, 1_000),
    weight: Number.isFinite(weight) ? Math.min(100, Math.max(0, weight)) : 1,
    enabled: sample.enabled !== false,
  };
}

function runtimeProfileFields(profile = {}) {
  const cleanEndpoint = (value) => cleanText(value, 160);
  const cleanFlag = (value) => typeof value === 'boolean' ? value : undefined;
  const cleanLimit = (value, minimum, maximum) => {
    const parsed = cleanInteger(value, 0);
    return parsed >= minimum && parsed <= maximum ? parsed : undefined;
  };
  const result = {
    chatEndpointId: cleanEndpoint(profile.chatEndpointId),
    taskEndpointId: cleanEndpoint(profile.taskEndpointId),
    decisionEndpointId: cleanEndpoint(profile.decisionEndpointId),
    allowedToolIds: cleanList(profile.allowedToolIds, 128, 160),
    deniedToolIds: cleanList(profile.deniedToolIds, 128, 160),
    memoryPolicy: cleanText(profile.memoryPolicy, 200),
    searchMode: cleanText(profile.searchMode, 40),
    searchReplyStyle: cleanText(profile.searchReplyStyle, 40),
    visualPromptOverride: cleanText(profile.visualPromptOverride, 4_000),
		expressionPrompt: cleanText(profile.expressionPrompt, 4_000),
		unaddressedMode: cleanText(profile.unaddressedMode, 20),
  };
  const proactive = cleanFlag(profile.proactiveEnabled);
  const maxChars = cleanLimit(profile.maxReplyChars, 20, 1_000);
  const maxSentences = cleanLimit(profile.maxReplySentences, 1, 6);
  if (proactive !== undefined) result.proactiveEnabled = proactive;
  if (maxChars !== undefined) result.maxReplyChars = maxChars;
  if (maxSentences !== undefined) result.maxReplySentences = maxSentences;
  return result;
}

export function exportNativeCharacterCard(persona, worldbook = [], traits = [], samples = [], runtimeProfile = {}) {
  return {
    format: 'erdai-character-card',
		version: 4,
    exportedAt: new Date().toISOString(),
    persona: {
      ...personaFields(persona),
      sourceFormat: cleanText(persona.sourceFormat, 100) || 'native',
      sourceVersion: cleanText(persona.sourceVersion, 40),
    },
    worldbook: worldbook.filter((entry) => entry?.content).slice(0, 1_000).map(worldbookFields),
    traits: traits.filter((trait) => trait?.name && trait?.description).slice(0, 200).map(traitFields),
    samples: samples.filter((sample) => sample?.context && sample?.candidateReplies?.length).slice(0, 1_000).map(sampleFields),
    runtimeProfile: runtimeProfileFields(runtimeProfile),
  };
}

export function exportSillyTavernV2(persona, worldbook = [], traits = [], samples = [], runtimeProfile = {}) {
  const normalized = personaFields(persona);
  return {
    spec: 'chara_card_v2',
    spec_version: '2.0',
    data: {
      name: normalized.name,
      description: normalized.description,
      personality: normalized.personality,
      scenario: normalized.scenario,
      first_mes: normalized.firstMessage,
      mes_example: normalized.messageExample,
      creator_notes: '',
      system_prompt: normalized.systemPrompt,
      post_history_instructions: normalized.postHistoryInstructions,
      alternate_greetings: normalized.alternateGreetings,
      tags: normalized.tags,
      creator: normalized.creator,
      character_version: normalized.characterVersion,
			extensions: {
				...(normalized.avatarDataUri ? { erdai_avatar_data_uri: normalized.avatarDataUri } : {}),
				...(normalized.visualDescription ? { erdai_visual_description: normalized.visualDescription } : {}),
        erdai_persona_traits: traits.filter((trait) => trait?.name && trait?.description).slice(0, 200).map(traitFields),
				erdai_persona_samples: samples.filter((sample) => sample?.context && sample?.candidateReplies?.length).slice(0, 1_000).map(sampleFields),
        erdai_runtime_profile: runtimeProfileFields(runtimeProfile),
      },
      character_book: {
        name: `${normalized.name} 世界书`,
        entries: worldbook.filter((entry) => entry?.content).slice(0, 1_000).map((entry, index) => {
          const item = worldbookFields(entry, index);
          return {
            id: index,
            keys: item.keys,
            secondary_keys: item.secondaryKeys,
            comment: item.comment,
            content: item.content,
            enabled: item.enabled,
            constant: item.constant,
            selective: item.selective,
            priority: item.priority,
            position: item.position,
            insertion_order: item.insertionOrder,
            token_budget: item.tokenBudget,
          };
        }),
      },
    },
  };
}

export function importCharacterCard(input) {
  const card = typeof input === 'string' ? JSON.parse(input) : input;
  if (!card || typeof card !== 'object' || Array.isArray(card)) {
    throw new TypeError('角色卡必须是 JSON 对象');
  }
	if (card.format === 'erdai-character-card' && [1, 2, 3, 4].includes(Number(card.version))) {
    const persona = personaFields(card.persona);
    if (!persona.name) throw new TypeError('角色卡缺少名称');
    return {
      persona: { ...persona, sourceFormat: 'erdai-native', sourceVersion: String(card.version) },
      worldbook: Array.isArray(card.worldbook)
        ? card.worldbook.filter((entry) => entry?.content).slice(0, 1_000).map(worldbookFields)
        : [],
      traits: Array.isArray(card.traits) ? card.traits.slice(0, 200).map(traitFields) : [],
      samples: Array.isArray(card.samples) ? card.samples.slice(0, 1_000).map(sampleFields) : [],
      runtimeProfile: runtimeProfileFields(card.runtimeProfile),
    };
  }
  if (card.spec !== 'chara_card_v2' || !String(card.spec_version ?? '').startsWith('2')) {
    throw new RangeError('仅支持二呆角色卡或 SillyTavern V2 JSON');
  }
  const data = card.data;
  if (!data || typeof data !== 'object' || Array.isArray(data)) {
    throw new TypeError('SillyTavern 角色卡缺少 data');
  }
	const persona = personaFields({
		...data,
		avatarDataUri: data.extensions?.erdai_avatar_data_uri,
		visualDescription: data.extensions?.erdai_visual_description,
	});
  if (!persona.name) throw new TypeError('角色卡缺少名称');
  const entries = Array.isArray(data.character_book?.entries) ? data.character_book.entries : [];
  return {
    persona: { ...persona, sourceFormat: 'sillytavern-v2', sourceVersion: cleanText(card.spec_version, 40) },
    worldbook: entries.filter((entry) => entry?.content).slice(0, 1_000).map(worldbookFields),
    traits: Array.isArray(data.extensions?.erdai_persona_traits)
      ? data.extensions.erdai_persona_traits.slice(0, 200).map(traitFields)
      : [],
      samples: Array.isArray(data.extensions?.erdai_persona_samples)
      ? data.extensions.erdai_persona_samples.slice(0, 1_000).map(sampleFields)
      : [],
    runtimeProfile: runtimeProfileFields(data.extensions?.erdai_runtime_profile),
  };
}
