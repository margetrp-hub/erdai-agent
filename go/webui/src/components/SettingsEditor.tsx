import { Check, ChevronDown, Code2, Plus, RefreshCw, Search, Trash2 } from 'lucide-react';
import { useEffect, useId, useRef, useState } from 'react';
import { apiCollection, apiRequest, collectionItems, type JsonMap } from '../lib/api';
import { fieldSpec, humanValue, settingsDefaults, type FieldSpec } from '../lib/settings-schema';
import { writableFields } from '../lib/settings-contracts';
import { Button, InfoDialog } from './ui';
import './settings-editor.css';

type Editor = { title: string; description: string; value: string; path: string; method: 'PUT' | 'POST'; context?: JsonMap };
type Option = { value: string; label: string; provider?: string };
type Choices = Record<string, Option[]>;
type FieldContext = { path: string; method: 'PUT' | 'POST'; choices: Choices; disabled: boolean; search: string; platform?: JsonMap };
const isObject = (value: unknown): value is JsonMap => Boolean(value) && typeof value === 'object' && !Array.isArray(value);
const clone = <T,>(value: T): T => structuredClone(value);

function referenceKind(key: string, parent: string) {
  if (['personaId', 'activePersonaId', 'sourcePersonaId'].includes(key)) return 'personas';
  if (['connectionId', 'providerConnectionId'].includes(key)) return 'connections';
  if (['agentInstanceId', 'instanceId'].includes(key)) return 'instances';
  if (key === 'policyTemplateId') return 'templates';
  if (['platformId', 'connectorId'].includes(key)) return 'platforms';
  if (['senderRef', 'senderReference'].includes(key)) return 'members';
  if (['supports', 'conflicts'].includes(key)) return 'traits';
  if (['allowedToolIds', 'deniedToolIds', 'requiredTools'].includes(key)) return 'tools';
  if (key === 'requiredMcpServers') return 'servers';
  if (key === 'pluginId') return 'plugins';
  if (key === 'integrationId') return 'integrations';
  if (['credentialRef', 'secretRef'].includes(key) || /(?:credentialRefs|envHeaderRefs)$/.test(parent)) return 'credentials';
  if (parent === 'locks' || /(?:[Ee]ndpointId|[Mm]odelEndpointId)$/.test(key)) return 'models';
  return '';
}

async function allItems(path: string, signal: AbortSignal): Promise<JsonMap[]> {
  return (await apiCollection(path, signal)).items;
}

function useChoices(editor: Editor | null) {
  const [choices, setChoices] = useState<Choices>({});
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [catalog, setCatalog] = useState<JsonMap[]>([]);
  const [revision, setRevision] = useState(0);
  useEffect(() => {
    setChoices({}); setCatalog([]);
    setError('');
    if (!editor) return;
    const controller = new AbortController();
    const kinds = new Set<string>();
    const visit = (value: unknown, parent = '') => {
      if (Array.isArray(value)) value.forEach((item, index) => visit(item, `${parent}.${index}`));
      if (!isObject(value)) return;
      Object.entries(value).forEach(([key, child]) => {
        const kind = fieldSpec(editor.path, key, parent).reference || referenceKind(key, parent);
        if (kind) kinds.add(kind);
        visit(child, parent ? `${parent}.${key}` : key);
      });
    };
    try { visit(JSON.parse(editor.value)); } catch { return; }
    visit(settingsDefaults(editor.path));
    if (/\/agent-(instances|policy-templates)(?:\/|$)/.test(editor.path)) visit(settingsDefaults(editor.path, 'overrides'), 'overrides');
    if (editor.path.includes('media-quotas')) kinds.add('members');
    if (editor.path.includes('/routing/control')) { kinds.add('models'); kinds.add('lanes'); }
    if (editor.path.includes('/platforms')) { kinds.add('catalog'); kinds.add('credentials'); }
    const personaId = editor.path.match(/\/personas\/([^/]+)\//)?.[1];
    const routes: Record<string, string> = {
      personas: '/api/v1/personas?namespace=default', models: '/api/v1/model-endpoints',
      connections: '/api/v1/provider-connections', instances: '/api/v1/agent-instances',
      templates: '/api/v1/agent-policy-templates', platforms: '/api/v1/platforms', lanes: '/api/v1/routing/lanes',
      tools: '/api/v1/tools', servers: '/api/v1/mcp/servers', plugins: '/api/v1/plugins', integrations: '/api/v1/integrations', credentials: '/api/v1/credentials',
      traits: `/api/v1/personas/${personaId || ''}/traits?namespace=default`,
    };
    setLoading(kinds.size > 0);
    const load = async (kind: string) => {
      if (kind === 'catalog') {
        const rows = collectionItems<JsonMap>(await apiRequest('/api/v1/platforms/catalog', { signal: controller.signal }));
        if (!controller.signal.aborted) setCatalog(rows);
        return [];
      }
      if (kind === 'members') {
        const people = await allItems(routes.personas, controller.signal);
        const relationships = (await Promise.all(people.map((person) => allItems(`/api/v1/relationships?personaId=${encodeURIComponent(String(person.id))}`, controller.signal)))).flat();
        const unique = new Map<string, Option>();
        relationships.forEach((person) => {
          if (typeof person.senderRef !== 'string' || !person.senderRef) return;
          unique.set(person.senderRef, { value: person.senderRef, label: `${String(person.senderDisplayName || '未命名成员')} · ${person.senderRef.slice(-8)}` });
        });
        return [...unique.values()];
      }
      const items = await allItems(routes[kind], controller.signal);
      return items.map((item) => ({
        value: String(kind === 'credentials' ? item.env || item.envName || item.name || item.id || '' : item.id ?? item.lane ?? ''),
        label: String(item.displayName || item.label || item.name || item.model || (item.lane ? humanValue(editor.path, 'lane', item.lane) : item.id) || '未命名') + (item.enabled === false ? '（已停用）' : ''),
        provider: typeof item.provider === 'string' ? item.provider : undefined,
      })).filter((item) => item.value);
    };
    void Promise.allSettled([...kinds].map(async (kind) => [kind, await load(kind)] as const)).then((results) => {
      if (controller.signal.aborted) return;
      const next: Choices = {};
      let failed = false;
      results.forEach((result) => { if (result.status === 'fulfilled') next[result.value[0]] = result.value[1]; else failed = true; });
      setChoices(next);
      setError(failed ? '部分可选项未能加载。当前选择已保留，可以重试。' : '');
      setLoading(false);
    });
    return () => controller.abort();
  }, [editor, revision]);
  return { choices, catalog, error, loading, retry: () => setRevision((value) => value + 1) };
}

function OptionInput({ value, options, label, required, disabled, onChange }: { value: string; options: Option[]; label: string; required?: boolean; disabled?: boolean; onChange: (value: string) => void }) {
  const [query, setQuery] = useState('');
  const current = options.find((option) => option.value === value);
  const visible = options.filter((option) => option.value === value || !query || `${option.label} ${option.value}`.toLowerCase().includes(query.toLowerCase()));
  return <div className="settings-choice">
    {options.length > 8 ? <input aria-label={`查找${label}`} type="search" placeholder="输入名称查找" value={query} disabled={disabled} onChange={(event) => setQuery(event.target.value)} /> : null}
    <select aria-label={label} value={value} required={required} disabled={disabled} onChange={(event) => onChange(event.target.value)}>
      <option value="">{required ? '请选择' : '不指定 / 沿用原有默认规则'}</option>
      {value && !current ? <option value={value}>当前值：{value}（保留）</option> : null}
      {visible.map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}
    </select>
  </div>;
}

function emptyLike(value: unknown): unknown {
  if (Array.isArray(value)) return [];
  if (isObject(value)) return Object.fromEntries(Object.entries(value).map(([key, entry]) => [key, emptyLike(entry)]));
  if (typeof value === 'number') return 0;
  if (typeof value === 'boolean') return false;
  return '';
}

function RoutingLocks({ value, context, onChange }: { value: JsonMap; context: FieldContext; onChange: (value: unknown) => void }) {
  const lanes = context.choices.lanes || [];
  const keys = [...new Set([...lanes.map((lane) => lane.value), ...Object.keys(value)])];
  return <div className="settings-object">
    <p className="settings-help">为需要固定的场景选择模型。未指定的场景使用自动选择；只有手动模式会使用这些指定模型。</p>
    {!keys.length ? <p className="settings-help">正在读取任务场景，加载完成后即可选择。</p> : null}
    {keys.map((lane) => <div className="settings-field" key={lane}>
      <span className="settings-label">{lanes.find((entry) => entry.value === lane)?.label || humanValue(context.path, 'lane', lane)}</span>
      <OptionInput label={`${humanValue(context.path, 'lane', lane)}使用的模型`} value={String(value[lane] || '')} options={context.choices.models || []} disabled={context.disabled} onChange={(next) => {
        const updated = { ...value };
        if (next) updated[lane] = next; else delete updated[lane];
        onChange(updated);
      }} />
    </div>)}
  </div>;
}

function SettingsField({ name, value, parent, context, onChange, custom = false, listItem = false }: { name: string; value: unknown; parent: string; context: FieldContext; onChange: (value: unknown) => void; custom?: boolean; listItem?: boolean }) {
  const inputId = useId();
  const spec = fieldSpec(context.path, name, parent);
  const label = listItem ? `${spec.label}内容` : custom ? name : spec.label;
  const nestedPath = parent ? `${parent}.${name}` : name;
  const extended = spec as FieldSpec & { scale?: number; required?: boolean };
  const numeric = spec.kind === 'number' || typeof value === 'number';
  const scale = extended.scale || 1;
  const arbitrary = /(?:^|\.)(?:metadata|headers|env|environment|arguments|extraHeaders|environmentOverrides)(?:\.|$)/.test(parent);
  const refKind = arbitrary ? '' : spec.reference || referenceKind(name, parent);
  const readOnly = !custom && spec.readOnly;
  const disabled = context.disabled;
  const nullable = spec.nullable || value === null;
  const array = Array.isArray(value) || !listItem && spec.kind === 'list';
  const object = isObject(value) || !listItem && spec.kind === 'object';
  let control;
  if (readOnly) {
    control = <div className="settings-readonly">{humanValue(context.path, name, value)}</div>;
  } else if (name === 'locks' && isObject(value) && context.path.includes('/routing/control')) {
    control = <RoutingLocks value={value} context={context} onChange={onChange} />;
  } else if (name === 'inputSchema' && isObject(value)) {
    control = <InputSchema value={value} context={context} onChange={onChange} />;
  } else if (value === null) {
    control = null;
  } else if (array) {
    const list = Array.isArray(value) ? value : [];
    const scaffold = settingsDefaults(context.path, nestedPath);
    const defaultItem = spec.itemDefaults ?? (isObject(scaffold) && !Object.keys(scaffold).length ? undefined : scaffold) ?? (list.length ? emptyLike(list[0]) : '');
    control = <div className="settings-list">
      {list.map((item, index) => <div className="settings-list-item" key={index}>
        <div className="settings-list-item-content">
          {isObject(item) ? <ObjectFields value={item} parent={`${nestedPath}.${index}`} context={context} onChange={(next) => onChange(list.map((entry, position) => position === index ? next : entry))} />
            : <SettingsField name={name} parent={`${nestedPath}.${index}`} value={item} context={context} onChange={(next) => onChange(list.map((entry, position) => position === index ? next : entry))} custom listItem />}
        </div>
        <Button type="button" variant="ghost" disabled={disabled} aria-label={`移除${label}第 ${index + 1} 项`} icon={<Trash2 size={15} />} onClick={() => onChange(list.filter((_, position) => position !== index))} />
      </div>)}
      {!list.length ? <p className="settings-help">尚未添加。</p> : null}
      <Button type="button" disabled={disabled} icon={<Plus size={14} />} onClick={() => onChange([...list, clone(defaultItem)])}>添加{label.replace(/列表$/, '')}</Button>
    </div>;
  } else if (object) {
    control = <ObjectFields value={isObject(value) ? value : {}} parent={nestedPath} context={context} onChange={onChange} />;
  } else if (typeof value === 'boolean' || spec.kind === 'boolean') {
    control = <label className="settings-switch" htmlFor={inputId}><input id={inputId} type="checkbox" role="switch" checked={value === true} disabled={disabled} onChange={(event) => onChange(event.target.checked)} /><span>{value === true ? '已开启' : '已关闭'}</span></label>;
  } else if (refKind === 'credentials') {
    control = <div className="settings-choice"><input id={inputId} list={`${inputId}-options`} value={String(value ?? '')} disabled={disabled} placeholder="选择已配置凭据，或填写服务器上的变量名称" onChange={(event) => onChange(event.target.value)} /><datalist id={`${inputId}-options`}>{(context.choices.credentials || []).map((option) => <option key={option.value} value={option.value}>{option.label}</option>)}</datalist></div>;
  } else if (spec.options || refKind) {
    control = <OptionInput label={label} value={String(value ?? '')} options={spec.options || context.choices[refKind] || []} required={extended.required || (refKind === 'members' && name === 'senderRef')} disabled={disabled} onChange={onChange} />;
  } else if (numeric) {
    control = <div className="settings-number"><input id={inputId} type="number" required min={spec.min === undefined ? undefined : spec.min * scale} max={spec.max === undefined ? undefined : spec.max * scale} step={spec.step === undefined ? 'any' : spec.step * scale} value={typeof value === 'number' ? Number((value * scale).toPrecision(12)) : String(value ?? '')} disabled={disabled} onChange={(event) => onChange(event.target.value === '' ? '' : Number(event.target.value) / scale)} /><span>{spec.unit || (scale === 100 ? '%' : '')}</span></div>;
  } else if (spec.kind === 'textarea' || typeof value === 'string' && (value.includes('\n') || value.length > 100)) {
    control = <textarea id={inputId} rows={4} value={String(value ?? '')} placeholder={spec.placeholder} disabled={disabled} required={extended.required} onChange={(event) => onChange(event.target.value)} />;
  } else {
    control = <input id={inputId} type={spec.kind === 'secret' ? 'password' : 'text'} value={String(value ?? '')} placeholder={spec.placeholder} autoComplete={spec.kind === 'secret' ? 'new-password' : undefined} disabled={disabled} required={extended.required} onChange={(event) => onChange(event.target.value)} />;
  }
  return <div className={`settings-field${array || object ? ' settings-field-wide' : ''}`}>
    <div className="settings-field-heading"><label className="settings-label" htmlFor={inputId}>{label}</label>{readOnly ? <span className="settings-tag">只读</span> : null}</div>
    {spec.description ? <p className="settings-help" id={`${inputId}-help`}>{spec.description}</p> : null}
    {nullable && !readOnly ? <label className="settings-auto"><input type="checkbox" checked={value === null} disabled={disabled} onChange={(event) => onChange(event.target.checked ? null : numeric ? (spec.min ?? 0) : '')} />{spec.nullLabel || '沿用默认设置'}</label> : null}
    {control}
  </div>;
}

function InputSchema({ value, context, onChange }: { value: JsonMap; context: FieldContext; onChange: (value: JsonMap) => void }) {
  const [name, setName] = useState('');
  const [error, setError] = useState('');
  if (value.type && value.type !== 'object' || value.oneOf || value.anyOf || value.allOf || value.$ref) return <div className="settings-help">此工具使用组合输入结构，现有内容会完整保留。需要修改结构时，可打开下方“开发者配置”。</div>;
  const properties = isObject(value.properties) ? value.properties : {};
  const required = Array.isArray(value.required) ? value.required.map(String) : [];
  const setProperty = (key: string, property: JsonMap) => onChange({ ...value, type: 'object', properties: { ...properties, [key]: property } });
  return <div className="settings-schema-builder">
    <p className="settings-help">定义这个工具需要填写的内容。已有的额外校验规则会保留。</p>
    {Object.entries(properties).map(([key, entry]) => {
      const property = isObject(entry) ? entry : {};
      if (!isObject(entry) || Array.isArray(property.type) || property.oneOf || property.anyOf || property.allOf || property.$ref) return <div className="settings-schema-property" key={key}><strong>{key}</strong><p className="settings-schema-description settings-help">此字段使用组合或引用规则，已完整保留。需要调整时请使用开发者配置。</p></div>;
      return <div className="settings-schema-property" key={key}>
        <strong>{key}</strong>
        <select aria-label={`${key}字段类型`} disabled={context.disabled} value={String(property.type || '')} onChange={(event) => {
          const next = { ...property };
          if (event.target.value) next.type = event.target.value; else delete next.type;
          if (event.target.value === 'array' && !next.items) next.items = { type: 'string' };
          setProperty(key, next);
        }}>
          <option value="">未指定</option><option value="string">文字</option><option value="number">数字</option><option value="integer">整数</option><option value="boolean">是或否</option><option value="array">列表</option><option value="object">一组字段</option>
        </select>
        <label className="settings-auto"><input type="checkbox" disabled={context.disabled} checked={required.includes(key)} onChange={(event) => onChange({ ...value, required: event.target.checked ? [...required, key] : required.filter((item) => item !== key) })} />必填</label>
        <Button type="button" variant="ghost" disabled={context.disabled} aria-label={`移除输入字段 ${key}`} icon={<Trash2 size={14} />} onClick={() => { const next = { ...properties }; delete next[key]; onChange({ ...value, properties: next, required: required.filter((item) => item !== key) }); }} />
        <input aria-label={`${key}填写说明`} className="settings-schema-description" placeholder="用一句话说明要填什么" value={String(property.description || '')} disabled={context.disabled} onChange={(event) => setProperty(key, { ...property, description: event.target.value })} />
        {property.type === 'object' ? <div className="settings-schema-nested"><InputSchema value={property} context={context} onChange={(next) => setProperty(key, next)} /></div> : null}
      </div>;
    })}
    <div className="settings-add-key"><input aria-label="新增工具输入字段名称" placeholder="新字段名称，例如 question" value={name} disabled={context.disabled} onChange={(event) => { setName(event.target.value); setError(''); }} /><Button type="button" disabled={context.disabled || !name.trim()} icon={<Plus size={14} />} onClick={() => {
      const key = name.trim();
      if (Object.hasOwn(properties, key) || ['__proto__', 'constructor', 'prototype'].includes(key)) { setError('这个名称已存在或不可使用。'); return; }
      setProperty(key, { type: 'string', description: '' }); setName('');
    }}>添加字段</Button></div>
    {error ? <p role="alert" className="settings-error">{error}</p> : null}
  </div>;
}

const groupChatSections = [
  ['什么时候回复', 'enabled enabledGroups participationMode triggerKeywords keywordSmartMode ignoreAtOthers ignoreAtOthersMode ignoreAtAll commandPrefixes'],
  ['主动接话', 'initialProbability afterReplyProbability probabilityDurationSeconds proactiveCooldownSeconds suppressProactiveWhileBusy messageQualityEnabled questionBoost waterReduce ignoreLowValueMedia lowValueMediaMarkers lowValueMinTextChars'],
  ['接话理解与表达', 'decisionProviderId decisionIncludePersona decisionTimeoutSeconds decisionExtraPrompt replyExtraPrompt maxContextMessages includeTimestamp includeSenderInfo atLinkMaxMessages atLinkMaxSeconds'],
  ['频率与连续消息', 'replyDensityEnabled replyDensityWindowSeconds replyDensityMaxReplies replyDensitySoftLimitRatio smartMergeWaitSeconds smartMaxBatchSize duplicateFilterEnabled'],
  ['图片理解', 'imageProcessingEnabled imageScope imageProviderId imagePrompt imageTimeoutSeconds maxImagesPerMessage imageCacheEnabled'],
];
const messageSections = [
  ['自然发送节奏', 'segmentedReplyEnabled segmentMinChars segmentMaxChars maxReplySegments segmentMinDelaySeconds segmentMaxDelaySeconds humanPacingEnabled humanPacingMaxSeconds moodContinuityEnabled'],
  ['任务进度提示', 'toolProgressEnabled toolProgressSearchEnabled'],
  ['搜索提示语', 'toolProgressSearchMessages'],
  ['图片与照片提示语', 'toolProgressImageMessages toolProgressPhotoMessages toolCompletionImageMessages'],
  ['视频提示语', 'toolProgressVideoMessages toolCompletionVideoMessages'],
  ['文档提示语', 'toolProgressDocumentMessages toolCompletionDocumentMessages'],
];

function fieldActive(key: string, value: JsonMap, path: string) {
  if (path.includes('/mcp/servers')) {
    if (['command', 'args'].includes(key)) return value.transport === 'stdio';
    if (['endpoint', 'secretRef'].includes(key)) return value.transport !== 'stdio';
  }
  const dependsOn: Record<string, string[]> = {
    segmentedReplyEnabled: ['segmentMinChars', 'segmentMaxChars', 'maxReplySegments', 'segmentMinDelaySeconds', 'segmentMaxDelaySeconds'],
    humanPacingEnabled: ['humanPacingMaxSeconds'],
    imageProcessingEnabled: ['imageScope', 'imageProviderId', 'imagePrompt', 'imageTimeoutSeconds', 'maxImagesPerMessage', 'imageCacheEnabled'],
    replyDensityEnabled: ['replyDensityWindowSeconds', 'replyDensityMaxReplies', 'replyDensitySoftLimitRatio', 'replyDensityAiHint'],
    typingSimulatorEnabled: ['typingSpeedCharsPerSecond', 'typingMaxDelaySeconds'],
    messageQualityEnabled: ['questionBoost', 'waterReduce'],
    ignoreLowValueMedia: ['lowValueMediaMarkers', 'lowValueMinTextChars'],
    learningEnabled: ['learningTopics', 'learningIntervalHours'],
    locked: ['intimacy'],
  };
  if (Object.entries(dependsOn).some(([toggle, fields]) => value[toggle] === false && fields.includes(key))) return false;
  if (path.endsWith('/group_chat_policy') && value.participationMode === 'addressed_only' && ['initialProbability', 'afterReplyProbability', 'probabilityDurationSeconds', 'proactiveCooldownSeconds', 'suppressProactiveWhileBusy'].includes(key)) return false;
  return true;
}

function ObjectFields({ value, parent = '', context, onChange }: { value: JsonMap; parent?: string; context: FieldContext; onChange: (value: JsonMap) => void }) {
  const [newKey, setNewKey] = useState('');
  const [addError, setAddError] = useState('');
  const [expanded, setExpanded] = useState(false);
  const [newType, setNewType] = useState('text');
  const platformDrafts = useRef<Record<string, { settings: unknown; credentialRefs: unknown }>>({});
  const [platformNotice, setPlatformNotice] = useState('');
  const custom = /(?:^|\.)(?:metadata|headers|env|environment|arguments|extraHeaders|environmentOverrides|envHeaderRefs|groupMultipliers|radarRecommendations)(?:\.|$)/.test(parent);
  const typedDictionary = /(?:^|\.)(?:metadata|arguments)(?:\.|$)/.test(parent);
  const defaults = parent === 'settings' && context.platform ? context.platform.settingDefaults
    : parent === 'credentialRefs' && context.platform ? Object.fromEntries((Array.isArray(context.platform.credentialFields) ? context.platform.credentialFields : []).map((key) => [String(key), '']))
      : settingsDefaults(context.path, parent);
  const optional = isObject(defaults) ? Object.keys(defaults).filter((key) => !(key in value) && !fieldSpec(context.path, key, parent).readOnly && (parent || writableFields(context.path, context.method).includes(key))) : [];
  const entries = Object.entries(value);
  const query = context.search.trim().toLowerCase();
  const matches = (key: string, entry: unknown): boolean => {
    const spec = fieldSpec(context.path, key, parent);
    return `${spec.label} ${spec.description || ''} ${key}`.toLowerCase().includes(query) || isObject(entry) && Object.keys(entry).some((child) => fieldSpec(context.path, child, parent ? `${parent}.${key}` : key).label.includes(query));
  };
  const filtered = query ? entries.filter(([key, entry]) => matches(key, entry) || Array.isArray(entry) || isObject(entry)) : entries.filter(([key]) => custom || fieldActive(key, value, context.path));
  const common = filtered.filter(([key]) => custom || !fieldSpec(context.path, key, parent).advanced && !fieldSpec(context.path, key, parent).readOnly);
  const advanced = custom ? [] : filtered.filter(([key]) => fieldSpec(context.path, key, parent).advanced && !fieldSpec(context.path, key, parent).readOnly);
  const readOnly = custom ? [] : filtered.filter(([key]) => fieldSpec(context.path, key, parent).readOnly);
  const render = ([key, entry]: [string, unknown]) => <div key={key} className={custom ? 'settings-dictionary-row' : 'settings-field-container'}>
    <SettingsField name={key} value={entry} parent={parent} context={context} custom={custom} onChange={(next) => {
      if (!parent && key === 'type' && context.method === 'POST' && context.path.split('?')[0] === '/api/v1/platforms' && next !== value.type) {
        platformDrafts.current[String(value.type)] = { settings: clone(value.settings || {}), credentialRefs: clone(value.credentialRefs || {}) };
        const saved = platformDrafts.current[String(next)] || { settings: {}, credentialRefs: {} };
        setPlatformNotice('已切换平台类型，只显示该平台的参数。切回原类型时，之前填写的参数会恢复。');
        onChange({ ...value, type: next, ...clone(saved) });
        return;
      }
      const selectedProvider = key === 'connectionId' ? context.choices.connections?.find((item) => item.value === next)?.provider : undefined;
      onChange({ ...value, [key]: next, ...(selectedProvider ? { provider: selectedProvider } : {}) });
    }} />
    {custom ? <Button type="button" variant="ghost" disabled={context.disabled} aria-label={`移除 ${key}`} icon={<Trash2 size={14} />} onClick={() => { const next = { ...value }; delete next[key]; onChange(next); }} /> : null}
  </div>;
  const groups = parent ? [] : context.path.endsWith('/group_chat_policy') ? groupChatSections : context.path.endsWith('/message_policy') ? messageSections : [];
  const groupFor = (key: string) => groups.find(([, fields]) => fields.split(' ').includes(key))?.[0] || '兼容与调优';
  const groupNames = [...new Set([...groups.map(([label]) => label), ...[...common, ...advanced].map(([key]) => groupFor(key))])];
  const moreSettings = (fields: [string, unknown][]) => fields.length ? <details className="settings-section" open={Boolean(query) || expanded || undefined}><summary><ChevronDown size={15} />更多设置 <span>{fields.length} 项</span></summary><div className="settings-fields">{fields.map(render)}</div></details> : null;
  return <div className={parent ? 'settings-object' : 'settings-root'}>
    {platformNotice ? <p className="settings-scope" role="status">{platformNotice}</p> : null}
    {groups.length ? groupNames.map((label, index) => {
      const basic = common.filter(([key]) => groupFor(key) === label), extra = advanced.filter(([key]) => groupFor(key) === label);
      return basic.length || extra.length ? <details className="settings-purpose" key={label} open={index === 0 || Boolean(query) || expanded || undefined}><summary><ChevronDown size={15} />{label}<span>{basic.length + extra.length} 项</span></summary><div className="settings-fields">{basic.map(render)}</div>{moreSettings(extra)}</details> : null;
    }) : <><div className="settings-fields">{common.map(render)}</div>{moreSettings(advanced)}</>}
    {!query && filtered.length < entries.length ? <p className="settings-help">当前未启用功能的关联参数已收起，原有值会保留；开启后可继续调整。</p> : null}
    {readOnly.length ? <details className="settings-section settings-technical"><summary>查看只读信息 <span>{readOnly.length} 项</span></summary><div className="settings-fields">{readOnly.map(render)}</div></details> : null}
    {optional.length ? <label className="settings-optional"><span>添加可选设置</span><select value="" aria-label={parent ? `添加${fieldSpec(context.path, parent).label}项目` : '添加可选设置'} disabled={context.disabled} onChange={(event) => { if (event.target.value && isObject(defaults)) { setExpanded(true); onChange({ ...value, [event.target.value]: clone(defaults[event.target.value]) }); } }}><option value="">选择需要设置的项目</option>{optional.filter((key) => fieldActive(key, value, context.path)).map((key) => <option key={key} value={key}>{fieldSpec(context.path, key, parent).label}</option>)}</select></label> : null}
    {custom ? <div className="settings-add-key"><input type="text" value={newKey} placeholder="项目名称" aria-label="新项目名称" disabled={context.disabled} onChange={(event) => { setNewKey(event.target.value); setAddError(''); }} /><Button type="button" disabled={context.disabled || !newKey.trim()} icon={<Plus size={14} />} onClick={() => {
      const key = newKey.trim();
      if (key in value || ['__proto__', 'constructor', 'prototype'].includes(key)) { setAddError('名称已存在或不可使用，请换一个名称。'); return; }
      const initial = parent.endsWith('groupMultipliers') ? 1 : !typedDictionary ? '' : newType === 'number' ? 0 : newType === 'boolean' ? false : newType === 'list' ? [] : newType === 'object' ? {} : '';
      onChange({ ...value, [key]: initial }); setNewKey('');
    }}>添加</Button>{typedDictionary ? <select value={newType} aria-label="新项目类型" disabled={context.disabled} onChange={(event) => setNewType(event.target.value)}><option value="text">文字</option><option value="number">数字</option><option value="boolean">是或否</option><option value="list">列表</option><option value="object">一组字段</option></select> : null}{addError ? <span role="alert">{addError}</span> : null}</div> : null}
    {!entries.length && !optional.length && !custom ? <p className="settings-help">暂无可编辑项目。</p> : null}
    {query && !filtered.length ? <p className="settings-help">没有找到对应设置，试试更短的关键词。</p> : null}
  </div>;
}

export function SettingsEditor({ editor, onClose, onSave, saving, error = '' }: { editor: Editor | null; onClose: () => void; onSave: (value: string) => void; saving: boolean; error?: string }) {
  const [draft, setDraft] = useState<JsonMap>({});
  const [initial, setInitial] = useState('{}');
  const [search, setSearch] = useState('');
  const [rawMode, setRawMode] = useState(false);
  const [rawValue, setRawValue] = useState('');
  const [localError, setLocalError] = useState('');
  const [discard, setDiscard] = useState(false);
  const options = useChoices(editor);
  useEffect(() => {
    setSearch(''); setRawMode(false); setLocalError(''); setDiscard(false);
    if (!editor) return;
    try {
      const value: unknown = JSON.parse(editor.value);
      if (!isObject(value)) throw new Error('配置必须是一个对象。');
      setDraft(value); setInitial(JSON.stringify(value)); setRawValue(JSON.stringify(value, null, 2));
    } catch { setLocalError('配置读取失败，请关闭后重试。'); }
  }, [editor]);
  const dirty = rawMode ? rawValue !== JSON.stringify(JSON.parse(initial), null, 2) : JSON.stringify(draft) !== initial;
  const close = () => { if (saving) return; if (dirty) setDiscard(true); else onClose(); };
  const parseRaw = (): JsonMap | null => {
    try { const parsed: unknown = JSON.parse(rawValue); if (!isObject(parsed)) throw new Error(); return parsed; }
    catch { setLocalError('原始配置格式不正确，修改仍已保留。请检查后再保存或返回表单。'); return null; }
  };
  const isQuota = editor?.path.includes('media-quotas');
  return <InfoDialog open={Boolean(editor)} onOpenChange={(open) => !open && close()} title={editor?.title || '编辑设置'} description={isQuota ? '设置成员每天可生成的图片和视频次数。' : '修改后点击保存；未改动的设置将保留。'} className="settings-dialog">
    <form className="settings-form" onSubmit={(event) => {
      event.preventDefault();
      if (saving) return;
      const value = rawMode ? parseRaw() : draft;
      if (!value) return;
      setLocalError(''); onSave(JSON.stringify(value));
    }}>
      {!rawMode ? <div className="settings-toolbar"><label className="settings-search"><Search size={16} /><input type="search" placeholder="查找设置" aria-label="查找设置" value={search} onChange={(event) => setSearch(event.target.value)} /></label><span>中文表单</span></div> : null}
      {isQuota ? <p className="settings-scope">全局额度规则 · 每天按北京时间重新计算。设为 0 时，普通成员无法生成该类内容；豁免对象仍可使用。</p> : null}
      {options.loading ? <p className="settings-help" role="status">正在加载可选角色、模型或成员…</p> : null}
      {options.error ? <div className="settings-warning" role="status">{options.error}<Button type="button" variant="ghost" onClick={options.retry}>重试</Button></div> : null}
      {rawMode ? <label className="settings-raw"><span>开发者配置</span><textarea spellCheck={false} value={rawValue} rows={18} disabled={saving} onChange={(event) => { setRawValue(event.target.value); setLocalError(''); }} /><span className="settings-help">保存时仍按接口规则校验。原始结构中的只读字段不能修改。</span></label>
        : editor ? <ObjectFields value={draft} context={{ path: editor.path, method: editor.method, choices: options.choices, disabled: saving, search, platform: options.catalog.find((row) => (row.id || row.type) === (draft.type || editor.context?.type)) }} onChange={(next) => { setDraft(next); setLocalError(''); setDiscard(false); }} /> : null}
      {error || localError ? <div className="settings-error" role="alert">{localError || error}</div> : null}
      {discard ? <div className="settings-discard" role="alert"><span>还有未保存的修改。</span><Button type="button" onClick={() => setDiscard(false)}>继续编辑</Button><Button type="button" variant="ghost" onClick={onClose}>放弃修改</Button></div> : null}
      <footer className="settings-footer">
        <Button type="button" variant="ghost" disabled={saving} icon={<Code2 size={14} />} onClick={() => {
          if (rawMode) { const value = parseRaw(); if (!value) return; setDraft(value); }
          else setRawValue(JSON.stringify(draft, null, 2));
          setLocalError(''); setRawMode(!rawMode);
        }}>{rawMode ? '返回表单' : '开发者配置'}</Button>
        <span className="settings-save-state">{dirty ? '有未保存修改' : '当前设置'}</span>
        <Button type="button" variant="ghost" disabled={saving} onClick={close}>取消</Button>
        <Button type="submit" variant="primary" disabled={saving} icon={saving ? <RefreshCw className="spin" size={15} /> : <Check size={15} />}>{saving ? '保存中…' : '保存'}</Button>
      </footer>
    </form>
  </InfoDialog>;
}
