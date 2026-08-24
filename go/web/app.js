import {
  serializeContentBoundaryPolicy,
  serializeCompanionPolicy,
  serializeGrokPolicy,
  serializeGroupChatPolicy,
  serializeImagePolicy,
  serializeMemoryPolicy,
  serializeRetrievalPolicy,
  serializeDocumentPolicy,
  serializeMessagePolicy,
  serializeOpsPolicy,
} from './policy-forms.js';
import {
  exportNativeCharacterCard,
  exportSillyTavernV2,
  importCharacterCard,
  normalizeAvatarDataUri,
} from './character-card.js';

const app = document.querySelector('#app');
let renderEpoch = 0;
const viewWarmups = new Map();
const state = {
  view: 'overview',
  overview: null,
  config: null,
  mediaQuotas: null,
  integrations: null,
  platforms: null,
  platformCatalog: null,
  platformRuntime: null,
  agentInstances: null,
  agentPolicyTemplates: null,
  agentInstanceRoutes: null,
  agentInstanceConnectors: null,
  agentInstanceCapabilities: null,
  configLayers: null,
  models: null,
  providerConnections: null,
  providerDrivers: null,
  health: null,
  healthHistory: null,
  runs: null,
  runTimeline: null,
	devices: null,
	realtimeSessions: null,
	pairingCode: null,
  personas: null,
  persona: null,
  personaBindings: null,
  personaProfiles: null,
  personaVisualReferences: null,
  personaEditorData: null,
  memories: null,
  relationships: null,
  memoryPersonaId: null,
  memoryScopeKind: '',
  worldbook: null,
  personaSamples: null,
  personaTraits: null,
  documents: null,
  candidates: null,
  directives: null,
  lanes: null,
  control: null,
  tools: null,
  toolsSection: 'tools',
  pageSections: {},
  skills: null,
  mcp: null,
  mcpInspection: null,
  capabilities: null,
  audit: null,
  shadow: null,
  editingModel: null,
  editingPersona: null,
  editingPersonaProfile: null,
  editingPersonaBinding: null,
  editingMemory: null,
  editingRelationship: null,
  editingDocument: null,
  editingWorldbook: null,
  editingPersonaSample: null,
  editingPersonaTrait: null,
  editingDirective: null,
  editingTool: null,
  editingSkill: null,
  editingMcp: null,
  editingPlatform: null,
  editingRuntimeInstance: null,
  runtimeWizard: null,
};

const DOMAINS = {
  workbench: { label: '工作台', views: ['overview', 'operations'] },
  runtime: { label: '运行中心', views: ['runtime'] },
  agents: { label: '智能体', views: ['roles', 'memories', 'worldbook', 'samples'] },
  capabilities: { label: '能力中心', views: ['knowledge', 'skills', 'tools'] },
  infrastructure: { label: '基础设施', views: ['integrations', 'models', 'routing', 'devices'] },
  governance: { label: '治理', views: ['security', 'system'] },
};
const MESSAGE_COPY_LIBRARY = {
  toolProgressSearchMessages: ['我去查查，等我一下。', '我翻翻最新消息。', '等会，我找准点。', '我去确认一下。', '这事得查，我去看看。', '我找找靠谱的说法。', '稍等，我去核一下。', '我看看最近怎么说。'],
  toolProgressImageMessages: ['行，我去画。', '等我一下，马上弄。', '我先把画面做出来。', '收到，我去试一版。', '给我点时间，我来弄。', '我先琢磨下画面。', '这张我来做。', '我去调一下画面。'],
  toolProgressPhotoMessages: ['那你等等，我去拍。', '想看呀？等我一下。', '行吧，给你拍一张。', '我挑个好看的角度。', '等会，先让我收拾下。', '今天心情好，拍一张。', '好啦，我去找光线。', '别催，我挑下衣服。'],
  toolCompletionImageMessages: ['好了，给你看。', '这版还行，收好。', '画完了，你看看。', '喏，刚做好的。', '成品到了。', '我弄好了，看这张。', '给你，刚出炉的。', '这次应该顺眼。'],
  toolProgressVideoMessages: ['我去做，得等一会。', '先别催，画面在动了。', '收到，我开始做。', '我去把它做成视频。', '等会，这个比较慢。', '我先调动作和镜头。', '行，我让它动起来。', '我去跑一版看看。'],
  toolCompletionVideoMessages: ['做好了，给你。', '视频出来了，看看。', '这版跑完了。', '弄好了，别挑太狠。', '成片到了。', '我看过了，可以发。', '好了，这次没鸽你。', '给你，刚导出来。'],
  toolProgressDocumentMessages: ['我整理一下。', '行，我给你排好。', '我先把内容捋顺。', '文件我来做。', '等会，我整理成文档。', '我先校一下格式。', '收到，我去排版。', '我把它收拾清楚。'],
  toolCompletionDocumentMessages: ['整理好了，给你。', '文件好了。', '弄完了，你先看。', '给你，格式也排好了。', '文件在这。', '我校过一遍了。', '好了，没漏内容。', '这版能直接用。'],
};
const VIEW_LABELS = {
  overview: '总览',
  runtime: '运行实例',
  operations: '任务与审计',
  roles: '角色库',
  memories: '记忆与关系',
  worldbook: '世界书',
  samples: '人格内核',
  knowledge: '知识与学习',
  skills: '技能',
  tools: '工具与 MCP',
  integrations: '平台与接入',
  models: '模型与供应商',
  routing: '模型路由',
  devices: '设备与桌面',
  security: '安全边界',
  system: '系统设置',
};

function domainForView(view) {
  return Object.entries(DOMAINS).find(([, value]) => value.views.includes(view))?.[0] || 'workbench';
}

function setHealth(text, ok = null) {
  const health = document.querySelector('#health');
  health.innerHTML = `<i class="status-dot ${ok === true ? 'ok' : ok === false ? 'bad' : ''}"></i><span>${esc(text)}</span>`;
  health.dataset.ok = ok === null ? '' : String(ok);
}

function updateNavigation() {
  const domain = domainForView(state.view);
  const definition = DOMAINS[domain];
  document.body.dataset.domain = domain;
  document.querySelectorAll('.nav-cluster').forEach((cluster) => cluster.classList.toggle('current', cluster.dataset.domain === domain));
  document.querySelectorAll('.tab').forEach((tab) => tab.classList.toggle('active', tab.dataset.view === state.view));
  document.querySelector('#domain-label').textContent = definition.label;
  document.querySelector('#breadcrumb').textContent = `${definition.label} / ${VIEW_LABELS[state.view] || '总览'}`;
}

function renderRoleMenu() {
  const people = state.personas?.items || [];
  const active = people.find((persona) => persona.id === state.config?.activePersonaId);
  document.querySelector('#current-role').textContent = active?.name || state.config?.activePersonaId || '未选择';
  document.querySelector('#role-menu').innerHTML = `${people.map((persona) => `
    <button class="role-menu-item ${persona.id === state.config?.activePersonaId ? 'active' : ''}" type="button" role="menuitem" data-role-id="${esc(persona.id)}">
      ${avatarMarkup(persona, 'role-menu-avatar')}
      <span><strong>${esc(persona.name)}</strong><small>${persona.id === state.config?.activePersonaId ? '当前角色' : esc(persona.description || '角色卡')}</small></span>
    </button>`).join('') || '<p class="role-menu-empty">还没有角色卡</p>'}
    <button class="role-menu-manage" type="button" data-role-manage>管理角色库</button>`;
}

function renderChrome() {
  updateNavigation();
  renderRoleMenu();
}

async function setView(view) {
  if (view === state.view) return;
  state.view = view;
  document.querySelector('#role-menu').hidden = true;
  document.querySelector('#role-switch').setAttribute('aria-expanded', 'false');
  await render();
}

async function api(path, options = {}) {
  const isMultipart = typeof FormData !== 'undefined' && options.body instanceof FormData;
  const response = await fetch(path, {
    ...options,
    headers: { accept: 'application/json', ...(options.body && !isMultipart ? { 'content-type': 'application/json' } : {}), ...(options.headers || {}) },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok) {
    const error = new Error(payload?.error?.message || `请求失败（HTTP ${response.status}）`);
    if (response.status === 401) {
      error.authRequired = true;
      renderLogin('登录已失效');
    }
    throw error;
  }
  return payload.data;
}
function esc(value) { return String(value ?? '').replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('>', '&gt;').replaceAll('"', '&quot;'); }
function json(value) { return JSON.stringify(value ?? []); }
function shell(title, description, body, actions = '') { return `<div class="section-head"><div><h2>${title}</h2><p>${description}</p></div><div class="section-actions">${actions}</div></div>${body}`; }
function button(label, action, secondary = false) { return `<button class="button ${secondary ? 'secondary' : ''}" type="button" data-action="${esc(action)}">${label}</button>`; }
function card(title, body, extra = '') { return `<article class="card"><div class="card-title"><h3>${title}</h3>${extra}</div>${body}</article>`; }
function controlDialog(title, body, description = '') { return `<div class="control-dialog-backdrop" data-dialog-backdrop><section class="control-dialog" role="dialog" aria-modal="true" aria-labelledby="control-dialog-title"><header class="control-dialog-head"><div><h3 id="control-dialog-title">${esc(title)}</h3>${description ? `<p>${esc(description)}</p>` : ''}</div><button class="icon-button icon-x" type="button" data-action="close-dialog" aria-label="关闭"></button></header><div class="control-dialog-body">${body}</div></section></div>`; }
function moduleGroup(title, description, body, accent, icon) { return `<section class="settings-module" data-accent="${esc(accent)}"><header class="module-head"><span class="module-icon"><span class="nav-icon icon-${esc(icon)}"></span></span><span><strong>${esc(title)}</strong><small>${esc(description)}</small></span></header><div class="module-body">${body}</div></section>`; }
function pageSection(view, fallback) { return state.pageSections?.[view] || fallback; }
function pageTabs(view, current, tabs) {
  return `<div class="page-subnav" role="tablist" aria-label="${esc(VIEW_LABELS[view] || view)}">${tabs.map(([id, label, count]) => `<button class="page-subnav-tab ${current === id ? 'active' : ''}" type="button" role="tab" aria-selected="${current === id}" data-action="set-section:${esc(view)}:${esc(id)}"><span>${esc(label)}</span>${count === undefined ? '' : `<strong>${esc(count)}</strong>`}</button>`).join('')}</div>`;
}
function clearActiveDialog() {
  state.editingProviderConnection = state.editingModel = state.editingPersona = state.editingPersonaProfile = state.editingPersonaBinding = null;
  state.personaEditorData = state.personaVisualReferences = null;
  state.editingMemory = state.editingRelationship = state.editingDocument = state.editingWorldbook = state.editingPersonaSample = state.editingPersonaTrait = null;
  state.editingDirective = state.editingTool = state.editingSkill = state.editingMcp = state.editingPlatform = state.editingRuntimeInstance = null;
  state.mcpInspection = state.healthHistory = state.runTimeline = state.pairingCode = null;
  state.runtimeWizard = null;
}
async function dismissActiveDialog() {
  const dialog = document.querySelector('.control-dialog-backdrop');
  if (dialog) {
    dialog.classList.add('closing');
    await new Promise((resolve) => setTimeout(resolve, 120));
  }
  clearActiveDialog();
}
function field(label, name, value = '', type = 'text', extra = '') { return `<label class="field"><span>${label}</span><input type="${type}" name="${name}" value="${esc(value)}" ${extra}></label>`; }
function textarea(label, name, value = '', extra = '') { return `<label class="field"><span>${label}</span><textarea name="${name}" ${extra}>${esc(value)}</textarea></label>`; }
function checkbox(label, name, checked, extra = '') { return `<label class="check"><input type="checkbox" name="${name}" ${checked ? 'checked' : ''} ${extra}><span>${label}</span></label>`; }
function selectField(label, name, value, options) { return `<label class="field"><span>${label}</span><select name="${name}">${options.map(([optionValue, optionLabel]) => `<option value="${esc(optionValue)}" ${value === optionValue ? 'selected' : ''}>${esc(optionLabel)}</option>`).join('')}</select></label>`; }
function listValue(value) { return Array.isArray(value) ? value.join(', ') : String(value || ''); }
function lineValue(value) { return Array.isArray(value) ? value.join('\n') : String(value || ''); }
function messageCopyValues(messages, name) {
  const configured = Array.isArray(messages[name]) ? messages[name].filter(Boolean) : [];
  return configured.length ? configured : MESSAGE_COPY_LIBRARY[name];
}
function messageCopyEditor(messages, name, label, rows = 6) {
  const values = messageCopyValues(messages, name);
  return textarea(`${label} · ${values.length} 条`, name, lineValue(values), `rows="${rows}"`);
}
function recordValue(value) { return value && typeof value === 'object' ? Object.entries(value).map(([key, item]) => `${key}=${item}`).join('\n') : ''; }
function splitList(value) { return String(value || '').split(/[,，\n]/).map((v) => v.trim()).filter(Boolean); }
function splitLines(value) { return String(value || '').split(/\r?\n/).map((v) => v.trim()).filter(Boolean); }
function mediaQuotaWhitelistValue(value) {
  return (Array.isArray(value) ? value : []).map((item) => `${item.label || ''}=${item.senderRef || ''}`).join('\n');
}
function parseMediaQuotaWhitelist(value) {
  return splitLines(value).map((line) => {
    const index = line.indexOf('=');
    if (index < 0) return { label: '', senderRef: line };
    return { label: line.slice(0, index).trim(), senderRef: line.slice(index + 1).trim() };
  }).filter((item) => item.senderRef);
}
function modelPayload(model, enabled = model.enabled) {
  return {
    provider: model.provider,
    model: model.model,
    enabled,
    capabilities: model.capabilities || [],
    inputCostPerMillion: model.inputCostPerMillion,
    outputCostPerMillion: model.outputCostPerMillion,
    qualityScore: model.qualityScore,
    priority: model.priority,
    maxContextTokens: model.maxContextTokens,
    executionKind: model.executionKind,
    adapterRef: model.adapterRef,
    connectionId: model.connectionId || '',
  };
}
function formActions(save = '保存到 Core', cancel = '') { const cancelLabel = cancel === 'cancel-edit' ? '取消' : cancel; return `<div class="form-actions"><button class="button" type="submit">${save}</button>${cancelLabel ? button(cancelLabel, 'cancel-edit', true) : ''}</div>`; }
function empty(text) { return `<p class="muted empty">${text}</p>`; }
function loadingView(view) {
  return `<div class="loading-stage" aria-label="正在加载 ${esc(VIEW_LABELS[view] || '页面')}">
    <div class="loading-heading"><span></span><strong></strong><small></small></div>
    <div class="loading-rail">${Array.from({ length: 4 }, (_, index) => `<i style="--loading-order:${index}"></i>`).join('')}</div>
    <div class="loading-lines">${Array.from({ length: 5 }, (_, index) => `<span style="--loading-order:${index}"></span>`).join('')}</div>
  </div>`;
}

function warmView(view) {
  if (viewWarmups.has(view)) return viewWarmups.get(view);
  const warmers = {
    runtime: async () => { [state.platforms, state.platformCatalog, state.platformRuntime, state.agentInstances, state.agentPolicyTemplates, state.agentInstanceRoutes, state.personas, state.configLayers] = await Promise.all([state.platforms || api('/api/v1/platforms'), state.platformCatalog || api('/api/v1/platforms/catalog'), state.platformRuntime || api('/api/v1/platforms/runtime-status'), state.agentInstances || api('/api/v1/agent-instances'), state.agentPolicyTemplates || api('/api/v1/agent-policy-templates'), state.agentInstanceRoutes || api('/api/v1/agent-instance-routes'), state.personas || api('/api/v1/personas?namespace=default&limit=100'), state.configLayers || api('/api/v1/config/layers')]); },
    operations: async () => { [state.audit, state.shadow, state.runs] = await Promise.all([state.audit || api('/api/v1/audit?limit=100'), state.shadow || api('/api/v1/shadow/interactions?limit=50'), state.runs || api('/api/v1/runs')]); },
    roles: async () => { [state.personas, state.personaBindings, state.personaProfiles] = await Promise.all([state.personas || api('/api/v1/personas?namespace=default&limit=100'), state.personaBindings || api('/api/v1/persona-bindings'), state.personaProfiles || api('/api/v1/personas/runtime-profiles')]); },
    knowledge: async () => { [state.documents, state.candidates, state.integrations] = await Promise.all([state.documents || api(`/api/v1/knowledge/documents?namespace=${encodeURIComponent(state.config?.knowledgeNamespace || 'default')}&limit=100`), state.candidates || api('/api/v1/runtime/knowledge-candidates?limit=100'), state.integrations || api('/api/v1/integrations')]); },
    skills: async () => { state.skills ||= await api('/api/v1/skills'); },
    tools: async () => { [state.tools, state.mcp] = await Promise.all([state.tools || api('/api/v1/tools'), state.mcp || api('/api/v1/mcp/servers')]); },
    integrations: async () => { [state.integrations, state.platforms, state.platformCatalog, state.platformRuntime, state.models, state.providerConnections] = await Promise.all([state.integrations || api('/api/v1/integrations'), state.platforms || api('/api/v1/platforms'), state.platformCatalog || api('/api/v1/platforms/catalog'), state.platformRuntime || api('/api/v1/platforms/runtime-status'), state.models || api('/api/v1/model-endpoints'), state.providerConnections || api('/api/v1/provider-connections')]); },
    models: async () => { [state.models, state.providerConnections, state.providerDrivers, state.health] = await Promise.all([state.models || api('/api/v1/model-endpoints'), state.providerConnections || api('/api/v1/provider-connections'), state.providerDrivers || api('/api/v1/provider-drivers'), state.health || api('/api/v1/model-health')]); },
    routing: async () => { [state.lanes, state.control, state.models] = await Promise.all([state.lanes || api('/api/v1/routing/lanes'), state.control || api('/api/v1/routing/control'), state.models || api('/api/v1/model-endpoints')]); },
    devices: async () => { [state.devices, state.realtimeSessions] = await Promise.all([state.devices || api('/api/v1/devices'), state.realtimeSessions || api('/api/v1/realtime/sessions')]); },
    security: async () => { [state.integrations, state.directives] = await Promise.all([state.integrations || api('/api/v1/integrations'), state.directives || api('/api/v1/runtime/directives?limit=100')]); },
  };
  if (!warmers[view]) return Promise.resolve();
  const promise = warmers[view]().catch(() => {}).finally(() => viewWarmups.delete(view));
  viewWarmups.set(view, promise);
  return promise;
}

function avatarMarkup(persona, className = 'persona-avatar') {
  const avatar = persona?.avatarDataUri;
  if (avatar) return `<img class="${className}" src="${esc(avatar)}" alt="${esc(persona.name || '智能体')}头像">`;
  const initials = [...String(persona?.name || '二呆')].slice(0, 2).join('');
  return `<span class="${className} avatar-fallback" aria-hidden="true">${esc(initials)}</span>`;
}

function downloadJSON(filename, value) {
  const blob = new Blob([`${JSON.stringify(value, null, 2)}\n`], { type: 'application/json;charset=utf-8' });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = filename;
  link.click();
  URL.revokeObjectURL(url);
}

function safeFilename(value) {
  return String(value || 'character').replace(/[\\/:*?"<>|\s]+/g, '-').replace(/^-+|-+$/g, '') || 'character';
}

async function fileDataUri(file) {
  if (!file || file.size === 0) return '';
  if (file.size > 512 * 1024) throw new RangeError('头像不能超过 512 KiB');
  if (!['image/png', 'image/jpeg', 'image/webp'].includes(file.type)) throw new TypeError('头像只支持 PNG、JPEG 或 WebP');
  const value = await new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(reader.result);
    reader.onerror = () => reject(new Error('头像读取失败'));
    reader.readAsDataURL(file);
  });
  return normalizeAvatarDataUri(value);
}

async function personaWithWorldbook(id) {
  const [persona, worldbook, traits, samples, runtimeProfile] = await Promise.all([
    api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`),
    api(`/api/v1/personas/${encodeURIComponent(id)}/worldbook?namespace=default&limit=100`),
    api(`/api/v1/personas/${encodeURIComponent(id)}/traits?namespace=default&limit=100`),
    api(`/api/v1/personas/${encodeURIComponent(id)}/samples?namespace=default&limit=100`),
    api(`/api/v1/personas/runtime-profiles/${encodeURIComponent(id)}`),
  ]);
  return {
    persona,
    worldbook: worldbook.items || [],
    traits: traits.items || [],
    samples: samples.items || [],
    runtimeProfile: runtimeProfile || {},
  };
}

async function importPersonaFile(file) {
  if (!file || file.size === 0) return;
  if (file.size > 2 * 1024 * 1024) throw new RangeError('角色卡 JSON 不能超过 2 MiB');
  const imported = importCharacterCard(await file.text());
  const personaId = crypto.randomUUID();
  try {
    await api('/api/v1/personas', {
      method: 'POST',
      body: JSON.stringify({ id: personaId, namespace: 'default', ...imported.persona }),
    });
    for (const entry of imported.worldbook) {
      await api(`/api/v1/personas/${encodeURIComponent(personaId)}/worldbook?namespace=default`, {
        method: 'POST',
        body: JSON.stringify({ id: crypto.randomUUID(), ...entry }),
      });
    }
    for (const trait of imported.traits || []) {
      await api(`/api/v1/personas/${encodeURIComponent(personaId)}/traits?namespace=default`, {
        method: 'POST', body: JSON.stringify({ id: crypto.randomUUID(), ...trait }),
      });
    }
    for (const sample of imported.samples || []) {
      await api(`/api/v1/personas/${encodeURIComponent(personaId)}/samples?namespace=default`, {
        method: 'POST', body: JSON.stringify({ id: crypto.randomUUID(), ...sample }),
      });
    }
    if (imported.runtimeProfile && Object.keys(imported.runtimeProfile).length > 0) {
      await api(`/api/v1/personas/runtime-profiles/${encodeURIComponent(personaId)}`, {
        method: 'PUT', body: JSON.stringify(imported.runtimeProfile),
      });
    }
  } catch (error) {
    await api(`/api/v1/personas/${encodeURIComponent(personaId)}?namespace=default`, { method: 'DELETE' }).catch(() => {});
    throw error;
  }
  invalidate('personas', 'personaProfiles', 'worldbook', 'personaTraits', 'personaSamples', 'overview');
}

async function loadCore() {
  app.classList.add('is-switching', 'showing-skeleton');
  app.innerHTML = loadingView(state.view);
  [state.overview, state.config, state.personas] = await Promise.all([
    api('/api/v1/overview'),
    api('/api/v1/runtime/config'),
    api('/api/v1/personas?namespace=default&limit=100'),
  ]);
  document.body.classList.remove('login-mode');
  document.querySelector('.tabs').hidden = false;
  document.querySelector('#sidebar-toggle').hidden = false;
  document.querySelector('#role-switch').hidden = false;
  document.querySelector('#refresh').hidden = false;
  document.querySelector('#logout').hidden = false;
  document.querySelector('#last-updated').hidden = false;
  document.querySelector('#last-updated').textContent = `刚刚刷新`;
  setHealth('Core 在线', true);
  renderChrome();
  await render();
}
function renderLogin(message = '') {
  renderEpoch += 1;
  app.classList.remove('is-switching', 'showing-skeleton', 'view-ready');
  document.body.classList.add('login-mode');
  document.body.classList.remove('sidebar-open');
  document.querySelector('.tabs').hidden = true;
  document.querySelector('#sidebar-toggle').hidden = true;
  document.querySelector('#role-switch').hidden = true;
  document.querySelector('#role-menu').hidden = true;
  document.querySelector('#refresh').hidden = true;
  document.querySelector('#logout').hidden = true;
  document.querySelector('#last-updated').hidden = true;
  setHealth('需要登录', false);
  app.innerHTML = shell('管理员登录', '', `${message ? `<div class="notice error">${esc(message)}</div>` : ''}<article class="card auth-card"><form class="form" data-form="admin-login">${field('管理员口令', 'token', '', 'password', 'required autocomplete="current-password"')}${formActions('登录')}</form></article>`);
}
async function bootstrap() {
  const response = await fetch('/auth/session', { headers: { accept: 'application/json' } });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || !payload?.data?.authenticated) {
    renderLogin();
    return;
  }
  await loadCore();
}
function invalidate(...keys) { keys.forEach((key) => { state[key] = null; }); }

function overviewView() {
  const counts = state.overview?.counts || {};
  const models = state.overview?.models || {};
  const activePersona = (state.personas?.items || []).find((persona) => persona.id === state.config?.activePersonaId);
  return shell('总览', '二呆智能体的运行状态、能力库存与控制链路。',
    `<div class="metrics">
      <article><strong>${esc(activePersona?.name || state.config?.activePersonaId || '未选择')}</strong><span>当前角色</span></article>
      <article><strong>${esc(counts.platform_integrations ?? 0)}</strong><span>平台实例</span></article>
      <article><strong>${esc(models.healthy ?? 0)} / ${esc(models.configured ?? 0)}</strong><span>健康模型</span></article>
      <article><strong>${esc(counts.runs ?? 0)}</strong><span>累计任务</span></article>
    </div>
    <div class="grid-2">
      ${card('运行事实', `<div class="kv"><span>Core Schema</span><strong>v${esc(state.overview?.schemaVersion ?? 0)}</strong><span>模型状态</span><strong>${esc(models.healthy ?? 0)} 健康 · ${esc(models.unhealthy ?? 0)} 异常 · ${esc(models.unknown ?? 0)} 未检查</strong><span>路由模式</span><strong>${esc(state.overview?.routing?.mode || 'auto')}</strong><span>锁定场景</span><strong>${esc(state.overview?.routing?.lockedLaneCount ?? 0)}</strong><span>自动学习</span><strong>${state.config?.learningEnabled ? '已启用' : '已关闭'}</strong></div>`)}
      ${card('能力库存', `<div class="overview-inventory"><button type="button" data-action="go-roles"><strong>${esc(counts.personas ?? 0)}</strong><span>角色卡</span></button><button type="button" data-action="go-knowledge"><strong>${esc(counts.knowledge_documents ?? 0)}</strong><span>知识文档</span></button><button type="button" data-action="go-tools"><strong>${esc(counts.tools ?? 0)}</strong><span>工具</span></button><button type="button" data-action="go-skills"><strong>${esc(counts.skills ?? 0)}</strong><span>技能</span></button><button type="button" data-action="go-tools"><strong>${esc(counts.mcp_servers ?? 0)}</strong><span>MCP 服务</span></button><button type="button" data-action="go-platforms"><strong>${esc(counts.platform_integrations ?? 0)}</strong><span>连接器</span></button></div>`)}
      ${card('消息链路', `<div class="overview-flow"><div><span>入站事件</span><strong>${esc(counts.transport_events ?? 0)}</strong><small>Transport v2</small></div><i></i><div><span>任务执行</span><strong>${esc(counts.runs ?? 0)}</strong><small>Go Core</small></div><i></i><div><span>投递记录</span><strong>${esc(counts.deliveries ?? 0)}</strong><small>Outbox</small></div></div>`)}
      ${card('治理状态', `<div class="kv"><span>管理员指令</span><strong>${esc(counts.admin_directives ?? 0)}</strong><span>待审知识</span><strong>${esc(counts.knowledge_candidates ?? 0)}</strong><span>审计事件</span><strong>${esc(counts.audit_events ?? 0)}</strong><span>运行阶段事件</span><strong>${esc(counts.run_stage_events ?? 0)}</strong></div>`)}
    </div>`);
}

async function systemView() {
  if (!state.personas) state.personas = await api('/api/v1/personas?namespace=default&limit=100');
  if (!state.mediaQuotas) state.mediaQuotas = await api('/api/v1/runtime/media-quotas');
  if (!state.configLayers) state.configLayers = await api('/api/v1/config/layers');
  const cfg = state.config || {};
  const currentPersona = (state.personas.items || []).find((persona) => persona.id === cfg.activePersonaId);
  const personaAction = currentPersona ? `open-persona:${currentPersona.id}` : 'go-roles';
  const section = pageSection('system', 'runtime');
  const tabs = pageTabs('system', section, [['runtime', '运行时'], ['quotas', '媒体额度']]);
  const runtime = card('运行时配置', `<form class="form" data-form="runtime">
      <div class="form-grid">${field('知识命名空间', 'knowledgeNamespace', cfg.knowledgeNamespace || 'default')}${field('学习周期（小时）', 'learningIntervalHours', cfg.learningIntervalHours || 24, 'number', 'min="6" max="168"')}${field('闲聊建议句数', 'maxReplySentences', cfg.maxReplySentences ?? 2, 'number', 'min="1" max="6"')}${field('闲聊建议字数', 'maxReplyChars', cfg.maxReplyChars ?? 40, 'number', 'min="20" max="1000"')}</div>
      <div class="setting-jump"><div><span>当前智能体</span><strong>${esc(currentPersona?.name || '未选择')}</strong></div><button class="button secondary" type="button" data-action="${esc(personaAction)}">${currentPersona ? '打开配置' : '前往选择'}</button></div>
      <div class="toggle-row">${checkbox('启用角色卡注入', 'personaInjectionEnabled', cfg.personaInjectionEnabled)}${checkbox('启用世界书匹配', 'worldbookInjectionEnabled', cfg.worldbookInjectionEnabled)}${checkbox('启用知识检索注入', 'knowledgeInjectionEnabled', cfg.knowledgeInjectionEnabled)}${checkbox('避免重复开场', 'avoidRepetitiveOpeners', cfg.avoidRepetitiveOpeners)}${checkbox('启用 Grok 自动学习（总开关）', 'learningEnabled', cfg.learningEnabled)}</div>
      ${field('学习主题（逗号分隔）', 'learningTopics', listValue(cfg.learningTopics))}
      ${textarea('自然对话策略', 'replyStyle', cfg.replyStyle || '', 'rows="7"')}
      <div class="setting-jump"><div><span>安全与管理员规则</span><strong>统一在安全边界中管理</strong></div><button class="button secondary" type="button" data-action="go-security">打开安全配置</button></div>
      ${formActions()}
    </form>`);
  const quotas = card('媒体额度', `<form class="form" data-form="media-quotas">
      <div class="form-grid">${field('每人每天图片数', 'imageDailyLimit', state.mediaQuotas.imageDailyLimit ?? 3, 'number', 'min="0" max="100" required')}${field('每人每天视频数', 'videoDailyLimit', state.mediaQuotas.videoDailyLimit ?? 3, 'number', 'min="0" max="100" required')}</div>
		<div class="toggle-row">${checkbox('可信管理员不计入额度', 'trustedAdminBypass', state.mediaQuotas.trustedAdminBypass !== false)}</div>
		${textarea('媒体额度白名单（每行：备注=SenderRef）', 'mediaQuotaWhitelist', mediaQuotaWhitelistValue(state.mediaQuotas.whitelist), 'rows="5"')}
      <p class="muted">按用户独立计数，成功才消耗额度；失败会自动释放。白名单同时适用于图片和视频。</p>
      ${formActions('保存媒体额度')}
    </form>`);
  const layers = (state.configLayers?.layers || []).map((layer, index) => `<details class="config-layer" ${index === 0 ? 'open' : ''}><summary><span class="config-layer-index">${String(index + 1).padStart(2, '0')}</span><strong>${esc(layer.label || layer.id)}</strong><small>${esc(layer.scope || '')} · ${esc(layer.kind || '')}</small></summary><div class="config-layer-body"><p>${esc(layer.description || '')}</p><div class="config-layer-meta"><span>存储：${esc((layer.storage || []).join(' · '))}</span><span>消费：${esc((layer.consumes || []).join(' · '))}</span><span>覆盖：${esc((layer.overrides || []).join('、') || '不可覆盖')}</span><span>字段：${esc(Object.keys(layer.fields || {}).join('、'))}</span></div></div></details>`).join('');
  const controls = (state.configLayers?.controls || []).map((control, index) => `<details class="config-layer" ${index === 0 ? 'open' : ''}><summary><span class="config-layer-index">${String(index + 1).padStart(2, '0')}</span><strong>${esc(control.label || control.id)}</strong><small>${esc(control.canonicalField || '')}</small></summary><div class="config-layer-body"><p>${esc(control.rule || '')}</p><div class="config-layer-meta"><span>归属：${esc(control.owner || '')}</span><span>辅助：${esc((control.supportingFields || []).join(' · ') || '无')}</span></div></div></details>`).join('');
  const layerCard = card('配置分层', `<p class="muted">${esc(state.configLayers?.mergeRule || '公共先行，角色和实例只覆盖已填写字段。')}</p><div class="config-layer-list">${layers}</div>${controls ? `<details class="config-layer-panel config-control-panel"><summary><strong>行为主开关</strong><span>一个行为只认一个主入口</span></summary><p class="muted">旧字段只用于迁移兼容；辅助字段只做能力、范围或安全约束。</p><div class="config-layer-list">${controls}</div></details>` : ''}<p class="muted">修改字段前先确认它属于哪一层；页面只展示有运行时消费路径的配置。</p>`);
  return shell('系统设置', '管理 Core 的全局默认值。', tabs + layerCard + (section === 'runtime' ? runtime : quotas));
}

function integrationMap(items) {
  return Object.fromEntries((items || []).map((item) => [item.id, item]));
}

const PLATFORM_FIELD_LABELS = {
  appid: 'App ID', client_id: 'Client ID', app_id: 'App ID', corpid: 'Corp ID',
	telegram_user_api_id: 'Telegram API ID', telegram_user_phone: '登录手机号',
	telegram_user_session_dir: '会话保存目录', telegram_user_device_model: '设备名称',
	telegram_user_system_version: '系统版本', telegram_user_app_version: '客户端版本',
	telegram_user_lang_code: '语言代码', telegram_user_receive_groups: '接收群消息',
	telegram_user_receive_private: '接收私聊', telegram_user_proactive_enabled: '允许主动参与',
	telegram_user_download_media: '下载并持久化附件',
	telegram_user_allow_chats: '允许的会话（留空为全部）', api_hash: 'Telegram API Hash',
  ws_reverse_host: '反向 WS 监听地址', ws_reverse_port: '反向 WS 端口',
  card_template_id: '卡片模板 ID', domain: 'API 域名', start_message: '欢迎语',
  unified_webhook_mode: '统一 Webhook', webhook_uuid: 'Webhook UUID',
  callback_server_host: '回调监听地址', port: '回调端口', active_send_mode: '主动发送模式',
  enable_group_c2c: '启用群聊 C2C', enable_guild_direct_message: '启用频道私信',
  admin_openids: '管理员 OpenID（逗号分隔）', admin_ids: '可信管理员账号（逗号分隔）',
};
function platformFieldLabel(name) {
  return PLATFORM_FIELD_LABELS[name] || name.replaceAll('_', ' ');
}
function platformCatalogItem(type) {
  return (state.platformCatalog || []).find((item) => item.type === type);
}
function platformSettingControl(name, value, defaults, options = {}) {
  const fallback = defaults?.[name];
  const current = value === undefined ? fallback : value;
  const label = platformFieldLabel(name);
  if (Array.isArray(options[name])) {
    return `<label class="field"><span>${esc(label)}</span><select name="setting:${esc(name)}">${options[name].map((option) => `<option value="${esc(option)}" ${option === current ? 'selected' : ''}>${esc(option)}</option>`).join('')}</select></label>`;
  }
  if (typeof fallback === 'boolean') return checkbox(label, `setting:${name}`, Boolean(current));
  if (typeof fallback === 'number' || fallback === null) return field(label, `setting:${name}`, current ?? '', 'number', 'min="0" step="any"');
  return field(label, `setting:${name}`, current ?? '');
}
function platformForm(platform = {}) {
  const catalog = platformCatalogItem(platform.type) || state.platformCatalog?.[0] || {};
  const defaults = catalog.settingDefaults || {};
  const settings = platform.settings || {};
  const credentialRefs = platform.credentialRefs || {};
	const runtime = (state.platformRuntime || []).find((item) => item.id === platform.id);
	const loginQR = platform.type === 'weixin_oc' && platform.id && runtime?.details?.qrAvailable
		? `<fieldset class="config-fieldset"><legend>微信登录</legend><div class="platform-login-qr"><img src="/api/v1/platforms/${encodeURIComponent(platform.id)}/login-qr?t=${Date.now()}" alt="个人微信登录二维码"><small>使用手机微信扫码并确认登录。</small></div></fieldset>`
		: '';
	const telegramUserLogin = platform.type === 'telegram_user' && platform.id
		? `<fieldset class="config-fieldset"><legend>MTProto 账号登录</legend>
			<div class="platform-auth-status"><span>当前状态</span><strong>${esc(runtime?.details?.authStep || runtime?.status || (platform.enabled ? '等待重启' : '未启用'))}</strong></div>
			<div class="form-grid">
				${field('手机号', 'telegramUserPhone', '', 'tel', 'autocomplete="tel" placeholder="+86..."')}
				${field('验证码', 'telegramUserCode', '', 'text', 'autocomplete="one-time-code"')}
				${field('两步验证密码', 'telegramUserPassword', '', 'password', 'autocomplete="current-password"')}
			</div>
			<div class="row">${button('发送验证码', `telegram-user-start:${platform.id}`, true)}${button('提交验证码', `telegram-user-code:${platform.id}`, true)}${button('提交 2FA', `telegram-user-password:${platform.id}`, true)}</div>
			<p class="muted">先保存并启用实例，再重启 Core。session 只保存在服务器数据目录，不会在后台回显。</p>
		</fieldset>`
		: '';
  const typeSelect = `<label class="field"><span>适配器类型</span><select name="type" id="platform-type" ${platform.id ? 'disabled' : ''}>${(state.platformCatalog || []).map((item) => `<option value="${esc(item.type)}" ${item.type === catalog.type ? 'selected' : ''}>${esc(item.displayName)} · ${esc(item.type)}</option>`).join('')}</select></label>`;
  const settingFields = (catalog.settingFields || []).map((name) => platformSettingControl(name, settings[name], defaults, catalog.settingOptions || {})).join('');
  const credentialFields = (catalog.credentialFields || []).map((name) => field(`${platformFieldLabel(name)} · 环境变量`, `credential:${name}`, credentialRefs[name] || '', 'text', 'placeholder="ERDAI_PLATFORM_SECRET"')).join('');
  return `<form class="form" data-form="platform" data-id="${esc(platform.id || '')}">
    <div class="form-grid">${field('实例 ID', 'id', platform.id || '', 'text', platform.id ? 'readonly' : 'required')}${typeSelect}${field('显示名称', 'displayName', platform.displayName || catalog.displayName || '', 'text', 'required')}</div>
    <div class="toggle-row">${checkbox('启用此平台', 'enabled', Boolean(platform.enabled))}${checkbox('凭据已配置', 'credentialConfigured', Boolean(platform.credentialConfigured))}</div>
    ${settingFields ? `<fieldset class="config-fieldset"><legend>连接与行为参数</legend><div class="form-grid">${settingFields}</div></fieldset>` : ''}
    ${credentialFields ? `<fieldset class="config-fieldset"><legend>凭据引用</legend><p class="muted">这里只填写服务器环境变量名，不填写真实密钥。</p><div class="form-grid">${credentialFields}</div></fieldset>` : ''}
		${loginQR}
		${telegramUserLogin}
    ${formActions(platform.id ? '保存平台' : '新增平台', 'cancel-edit')}
  </form>`;
}

function retrievalPolicyCard(policy = {}) {
  const semanticEndpoints = (state.models || []).filter((model) => (model.capabilities || []).includes('embedding'));
  const rerankEndpoints = (state.models || []).filter((model) => (model.capabilities || []).includes('rerank'));
  const endpointOptions = (items) => [['', '未配置（使用本地回退）'], ...items.map((item) => [item.id, `${item.provider} / ${item.model}`])];
  return card('知识检索与向量', `<form class="form" data-form="retrieval-policy">
    <div class="form-grid">${selectField('检索模式', 'mode', policy.mode || 'hybrid', [['hybrid', '混合检索'], ['keyword', '关键词'], ['vector', '向量']])}${selectField('向量算法', 'vectorAlgorithm', policy.vectorAlgorithm || 'remote_embedding', [['remote_embedding', '远程 Embedding'], ['local_hash', '本地回退向量']])}${selectField('Embedding 端点', 'embeddingEndpointId', policy.embeddingEndpointId || '', endpointOptions(semanticEndpoints))}${selectField('Rerank 端点', 'rerankEndpointId', policy.rerankEndpointId || '', endpointOptions(rerankEndpoints))}${field('分块字符数', 'chunkSize', policy.chunkSize ?? 900, 'number', 'min="200" max="4000"')}${field('分块重叠', 'chunkOverlap', policy.chunkOverlap ?? 140, 'number', 'min="0" max="1000"')}${field('候选条数', 'candidateK', policy.candidateK ?? 24, 'number', 'min="1" max="100"')}${field('向量维度（本地回退）', 'dimensions', policy.dimensions ?? 256, 'number', 'min="64" max="2048" step="64"')}${field('关键词权重', 'keywordWeight', policy.keywordWeight ?? 0.45, 'number', 'min="0" max="1" step="0.05"')}${field('向量权重', 'vectorWeight', policy.vectorWeight ?? 0.55, 'number', 'min="0" max="1" step="0.05"')}${field('最低相似度', 'minimumSimilarity', policy.minimumSimilarity ?? 0.08, 'number', 'min="0" max="1" step="0.01"')}${field('最终召回', 'topK', policy.topK ?? 5, 'number', 'min="1" max="20"')}</div>
    <div class="toggle-row">${checkbox('启用向量检索', 'enabled', policy.enabled !== false)}</div>
    <p class="muted">文档按窗口分块。Embedding 与 Rerank 使用各自绑定的供应商连接和密钥；远程失败时自动保留本地检索结果。</p>${formActions('保存检索策略')}
  </form>`);
}

function documentPolicyCard(policy = {}) {
  return card('文档与多模态', `<form class="form" data-form="document-policy">
		<div class="form-grid">${field('单文件上限（MB）', 'maxFileMb', policy.maxFileMb ?? 15, 'number', 'min="1" max="100"')}${field('单次提取字符', 'maxExtractChars', policy.maxExtractChars ?? 24000, 'number', 'min="1000" max="200000" step="1000"')}${field('提取超时（秒）', 'extractionTimeoutSeconds', policy.extractionTimeoutSeconds ?? 90, 'number', 'min="1" max="300"')}${field('附件续接时长（秒）', 'recentAttachmentTtlSeconds', policy.recentAttachmentTtlSeconds ?? 2592000, 'number', 'min="0" max="31536000"')}${field('会话保留附件数', 'recentAttachmentMax', policy.recentAttachmentMax ?? 500, 'number', 'min="1" max="5000"')}${field('单次带入附件数', 'recentAttachmentContextMax', policy.recentAttachmentContextMax ?? 12, 'number', 'min="1" max="50"')}${field('媒体保留（小时）', 'mediaRetentionHours', policy.mediaRetentionHours ?? 720, 'number', 'min="24" max="43800"')}${field('清理周期（分钟）', 'mediaGCIntervalMinutes', policy.mediaGCIntervalMinutes ?? 60, 'number', 'min="15" max="1440"')}</div>
		<div class="toggle-row">${checkbox('启用附件阅读', 'enabled', policy.enabled !== false)}${checkbox('启用图片理解', 'imageUnderstandingEnabled', policy.imageUnderstandingEnabled !== false)}${checkbox('读取文本/CSV/JSON', 'allowText', policy.allowText !== false)}${checkbox('读取 PDF .pdf', 'allowPdf', policy.allowPdf !== false)}${checkbox('读取 Word .docx', 'allowDocx', policy.allowDocx !== false)}${checkbox('读取 PPT .pptx', 'allowPptx', policy.allowPptx !== false)}${checkbox('读取 Excel .xlsx', 'allowXlsx', policy.allowXlsx !== false)}</div>
		<p class="muted">同一会话会按时间保留最近的图片与文档；后续提到“这张图”“这份文件”时自动带入，填 0 关闭续接。</p>${formActions('保存文档策略')}
  </form>`);
}

function runtimeStatusFor(platform, runtime) {
  if (!platform) return { label: '暂缓 · 尚未创建', tone: 'deferred', detail: '尚未建立平台连接器' };
  if (!platform.enabled) return { label: '暂缓 · 未启用', tone: 'deferred', detail: '实例已登记，等待启用与登录' };
  if (!runtime) return { label: '等待重启', tone: 'waiting', detail: '配置已保存，等待 Core 重启' };
  const status = String(runtime.status || '').toLowerCase();
  if (status === 'connected' || status === 'healthy' || status === 'running') return { label: '已连接', tone: 'connected', detail: runtime.lastError || '连接器正在运行' };
  if (platform.type === 'telegram_user' && runtime.details?.authStep) return { label: `待登录 · ${runtime.details.authStep}`, tone: 'waiting', detail: 'MTProto 会话尚未完成登录' };
  return { label: runtime.status || '未确认', tone: 'waiting', detail: runtime.lastError || '尚未获得实时状态' };
}

function agentInstanceStatusFor(instance, connectorBindings, routes, platformsByID, runtimeByID) {
  if (!instance.enabled) return { label: '暂缓 · 未启用', tone: 'deferred', detail: '运行实例未启用' };
  if (!connectorBindings.length) return { label: '暂缓 · 无连接器', tone: 'deferred', detail: '实例尚未绑定平台连接器' };
  if (!connectorBindings.some((binding) => binding.enabled)) return { label: '暂缓 · 连接器未启用', tone: 'deferred', detail: '实例连接器绑定未启用' };
  if (!routes.length) return { label: '暂缓 · 无路由', tone: 'deferred', detail: '实例尚未配置消息路由' };
  if (!routes.some((route) => route.enabled)) return { label: '暂缓 · 路由未启用', tone: 'deferred', detail: '实例路由未启用' };
  const connector = connectorBindings.find((binding) => binding.enabled);
  const platform = platformsByID.get(connector?.connectorId);
  return runtimeStatusFor(platform, runtimeByID.get(connector?.connectorId));
}

function runtimeWizardForm(seed = {}) {
  const people = state.personas?.items || [];
  const templates = state.agentPolicyTemplates?.items || [];
  const catalogs = (state.platformCatalog || []).filter((item) => ['aiocqhttp', 'telegram_user'].includes(item.type));
  const type = seed.type || catalogs[0]?.type || 'aiocqhttp';
  const options = catalogs.map((item) => [item.type, `${item.displayName || item.type} · ${item.type}`]);
  const roleOptions = people.map((persona) => [persona.id, persona.name]);
  const policyOptions = [['__new__', '新建本实例策略模板'], ...templates.map((template) => [template.id, `${template.name} · v${template.version}`])];
  const settingFields = catalogs.map((catalog) => {
    const defaults = catalog.settingDefaults || {};
    const values = seed.settings || {};
    const fields = (catalog.settingFields || []).map((name) => platformSettingControl(name, values[name], defaults, catalog.settingOptions || {})).join('');
    const credentials = (catalog.credentialFields || []).map((name) => field(`${platformFieldLabel(name)} · 环境变量`, `credential:${name}`, seed.credentialRefs?.[name] || '', 'text', 'placeholder="ERDAI_PLATFORM_SECRET"')).join('');
    return `<div class="runtime-connector-fields" data-connector-type="${esc(catalog.type)}"${catalog.type === type ? '' : ' hidden'}><fieldset class="config-fieldset"><legend>${esc(catalog.displayName || catalog.type)} 连接参数</legend><div class="form-grid">${fields || '<p class="muted">该连接器无需额外参数。</p>'}</div></fieldset>${credentials ? `<fieldset class="config-fieldset"><legend>凭据引用</legend><p class="muted">只填写服务器环境变量名，不填写真实密钥。</p><div class="form-grid">${credentials}</div></fieldset>` : ''}</div>`;
  }).join('');
  return controlDialog(seed.id ? '编辑运行实例' : '新增运行实例', `<form class="form runtime-wizard" data-form="runtime-instance" data-runtime-connector-type="${esc(type)}" data-runtime-wizard-step="1">
    <div class="runtime-wizard-progress" role="list" aria-label="新增实例步骤"><span data-runtime-step="1" class="active">01 角色卡</span><i></i><span data-runtime-step="2">02 连接器</span><i></i><span data-runtime-step="3">03 具体配置</span><i></i><span data-runtime-step="4">04 确认</span></div>
    <fieldset class="runtime-wizard-step active" data-step="1"><legend>绑定角色卡与策略</legend><p class="body-copy">实例持有自己的角色、记忆命名空间和策略模板；公共知识与工具仍由 Core 共享。</p>${selectField('角色卡', 'personaId', seed.personaId || people[0]?.id || '', roleOptions)}${selectField('策略模板', 'policyTemplateId', seed.policyTemplateId || '__new__', policyOptions)}${field('实例 ID', 'id', seed.id || '', 'text', seed.id ? 'readonly' : 'required placeholder="xiaoman-qq"')}${field('显示名称', 'displayName', seed.displayName || '', 'text', 'required placeholder="小满 · 个人 QQ"')}</fieldset>
    <fieldset class="runtime-wizard-step" data-step="2"><legend>选择平台连接器</legend><p class="body-copy">这里选择实际收发消息的适配器。个人 QQ 需要外部 OneBot/NapCat 侧车，Telegram User 需要 MTProto 登录态。</p>${selectField('连接器类型', 'type', type, options)}</fieldset>
    <fieldset class="runtime-wizard-step" data-step="3"><legend>具体配置</legend>${settingFields}<div class="toggle-row">${checkbox('已准备服务器凭据引用', 'credentialConfigured', Boolean(seed.credentialConfigured))}</div><p class="muted">创建后仍保持“暂缓 · 未启用”，不会在这里保存账号密码，也不会自动登录。</p></fieldset>
    <fieldset class="runtime-wizard-step" data-step="4"><legend>确认运行边界</legend><div class="runtime-confirm"><strong>Core → 运行实例 → 角色卡 → 平台连接器</strong><p>本向导会创建停用连接器、策略模板（如需要）、运行实例、实例连接器绑定和实例路由。启用、登录和重启请在平台连接器页面完成。</p><dl><div><dt>实例</dt><dd data-runtime-confirm="displayName">${esc(seed.displayName || '待填写')}</dd></div><div><dt>角色</dt><dd data-runtime-confirm="persona">${esc(people.find((item) => item.id === seed.personaId)?.name || people[0]?.name || '待选择')}</dd></div><div><dt>状态</dt><dd>暂缓 · 未启用</dd></div></dl></div></fieldset>
    <div class="runtime-wizard-actions"><button class="button secondary" type="button" data-action="close-dialog">取消</button><button class="button secondary" type="button" data-runtime-wizard-prev disabled>上一步</button><button class="button secondary" type="button" data-runtime-wizard-next>下一步</button><button class="button" type="submit" hidden>创建停用实例</button></div>
  </form>`, '实例创建不会自动启用连接器；账号登录由对应平台侧车完成。');
}

function setRuntimeWizardStep(form, step) {
  const nextStep = Math.max(1, Math.min(4, Number(step) || 1));
  form.dataset.runtimeWizardStep = String(nextStep);
  form.querySelectorAll('.runtime-wizard-step').forEach((section) => section.classList.toggle('active', Number(section.dataset.step) === nextStep));
  form.querySelectorAll('[data-runtime-step]').forEach((item) => item.classList.toggle('active', Number(item.dataset.runtimeStep) === nextStep));
  const prev = form.querySelector('[data-runtime-wizard-prev]');
  const next = form.querySelector('[data-runtime-wizard-next]');
  if (prev) prev.disabled = nextStep <= 1;
  if (next) next.hidden = nextStep >= 4;
  const submit = form.querySelector('button[type="submit"]');
  if (submit) submit.hidden = nextStep < 4;
  if (nextStep === 4) {
    const name = form.elements.displayName?.value.trim() || '待填写';
    const persona = form.elements.personaId?.selectedOptions?.[0]?.textContent || '待选择';
    const nameTarget = form.querySelector('[data-runtime-confirm="displayName"]');
    const personaTarget = form.querySelector('[data-runtime-confirm="persona"]');
    if (nameTarget) nameTarget.textContent = name;
    if (personaTarget) personaTarget.textContent = persona;
  }
}

function plannedRuntimeCard(id, title, description, actionLabel, action) {
  return `<article class="runtime-instance planned" data-state="deferred"><header class="runtime-instance-head"><div><span class="runtime-instance-id">INSTANCE / ${esc(id)}</span><h3>${esc(title)}</h3></div><span class="runtime-status deferred"><i class="status-dot"></i>暂缓 · 尚未创建</span></header><p class="body-copy">${esc(description)}</p><footer class="runtime-instance-actions">${button(actionLabel, action)}</footer></article>`;
}

function runtimeInstanceConfigForm(instance, platforms, capabilities) {
  const overrides = instance.overrides || {};
  const capability = capabilities.find((item) => item.instanceId === instance.id && item.capabilityId === 'group_moderation');
  const moderation = capability?.config || {};
  const connectorOptions = [['', '不配置执行连接器'], ...platforms.map((item) => [item.id, `${item.displayName} · ${item.type}`])];
  return `<form class="form" data-form="runtime-instance-config" data-id="${esc(instance.id)}">
    <fieldset class="config-fieldset"><legend>主动参与</legend><p class="muted">只控制本实例。明确 @、回复和自然续聊仍会按上下文判断。</p>
      ${selectField('参与模式（唯一主开关）', 'participationMode', overrides.participationMode || '', [['', '继承上层'], ['addressed_only', '仅被叫到（@/回复/命令）'], ['adaptive', '自适应插话'], ['social', '社交陪伴']])}
      <p class="muted">概率、密度和判断模型只在“自适应插话/社交陪伴”模式下生效。</p>
      <div class="form-grid">${field('普通插话概率（高级）', 'initialReplyProbability', overrides.initialReplyProbability ?? '', 'number', 'min="0" max="1" step="0.01" placeholder="继承全局"')}${field('刚聊过时概率（高级）', 'afterReplyProbability', overrides.afterReplyProbability ?? '', 'number', 'min="0" max="1" step="0.01" placeholder="继承全局"')}</div>
      ${field('称呼触发词', 'addressKeywords', listValue(overrides.addressKeywords || []), 'text', 'placeholder="例如：豆包"')}
      ${textarea('实例表达规则', 'expressionPrompt', overrides.expressionPrompt || '', 'rows="4" placeholder="只写这个实例独有的参与和表达边界"')}
    </fieldset>
    <fieldset class="config-fieldset"><legend>群管理能力</legend><p class="muted">能力归属当前实例；撤回由支持 delete_msg 且具备群管理权限的 OneBot/NapCat 连接器执行。</p>
      <div class="toggle-row">${checkbox('启用高置信垃圾广告识别', 'moderationEnabled', capability?.enabled === true)}${checkbox('管理员免检', 'exemptAdmins', moderation.exemptAdmins !== false)}</div>
      <div class="form-grid">${selectField('执行连接器', 'executorConnectorId', moderation.executorConnectorId || '', connectorOptions)}${selectField('处理模式', 'moderationMode', moderation.mode || 'audit', [['audit', '只审计'], ['enforce', '识别后撤回']])}${field('最低置信分', 'minimumScore', moderation.minimumScore ?? 3, 'number', 'min="2" max="6" step="1"')}</div>
      ${field('生效群号', 'groupIds', listValue(moderation.groupIds || []), 'text', 'placeholder="逗号分隔；留空表示全部群"')}${field('免检成员 QQ', 'allowedSenderIds', listValue(moderation.allowedSenderIds || []), 'text', 'placeholder="逗号分隔"')}
    </fieldset>${formActions('保存实例配置', 'cancel-edit')}
  </form>`;
}

function configLayerPanel(manifest) {
  const layers = manifest?.layers || [];
  if (!layers.length) return '';
  const rows = layers.map((layer, index) => `<div class="config-layer-row" data-layer="${esc(layer.id)}">
    <span class="config-layer-index">${String(index + 1).padStart(2, '0')}</span>
    <div><strong>${esc(layer.label)}</strong><small>${esc(layer.scope)} · ${esc(layer.description)}</small><small>字段：${esc(Object.keys(layer.fields || {}).slice(0, 6).join('、') || '按页面查看')}</small></div>
    <span class="config-layer-kind">${esc(layer.kind)}</span>
  </div>`).join('');
  const controls = (manifest?.controls || []).map((control, index) => `<details class="config-layer" ${index === 0 ? 'open' : ''}>
    <summary><span class="config-layer-index">${String(index + 1).padStart(2, '0')}</span><strong>${esc(control.label || control.id)}</strong><small>${esc(control.canonicalField || '')}</small></summary>
    <div class="config-layer-body"><p>${esc(control.rule || '')}</p><div class="config-layer-meta"><span>归属：${esc(control.owner || '')}</span><span>辅助字段：${esc((control.supportingFields || []).join(' · ') || '无')}</span><span>旧字段：${esc((control.legacyFields || []).join('、') || '无')}</span></div></div>
  </details>`).join('');
  return `<details class="config-layer-panel" open><summary><strong>配置分层</strong><span>公共 → 角色 → 实例</span></summary>
    <p class="muted">${esc(manifest.mergeRule || '后层覆盖同名字段，空字段继续继承。')}</p>
    <div class="config-layer-list">${rows}</div>
    ${controls ? `<details class="config-layer-panel config-control-panel"><summary><strong>行为主开关</strong><span>一个行为只认一个主入口</span></summary><p class="muted">旧字段只用于迁移兼容；辅助字段只做能力、范围或安全约束。</p><div class="config-layer-list">${controls}</div></details>` : ''}
    <small class="muted">开源接入：读取 GET /api/v1/config/layers；密钥只提交 credentialRef，不进入角色卡。</small>
  </details>`;
}

async function runtimeInstancesView() {
  if (!state.platforms || !state.platformRuntime || !state.personas || !state.platformCatalog || !state.agentInstances || !state.agentPolicyTemplates || !state.agentInstanceRoutes || !state.agentInstanceCapabilities) {
    [state.platforms, state.platformRuntime, state.personas, state.platformCatalog, state.agentInstances, state.agentPolicyTemplates, state.agentInstanceRoutes, state.agentInstanceCapabilities, state.configLayers] = await Promise.all([
      state.platforms || api('/api/v1/platforms'), state.platformRuntime || api('/api/v1/platforms/runtime-status'), state.personas || api('/api/v1/personas?namespace=default&limit=100'), state.platformCatalog || api('/api/v1/platforms/catalog'), state.agentInstances || api('/api/v1/agent-instances'), state.agentPolicyTemplates || api('/api/v1/agent-policy-templates'), state.agentInstanceRoutes || api('/api/v1/agent-instance-routes'), state.agentInstanceCapabilities || api('/api/v1/agent-instance-capabilities'), state.configLayers || api('/api/v1/config/layers'),
    ]);
  }
  if (!state.configLayers) state.configLayers = await api('/api/v1/config/layers');
  const platforms = state.platforms || [];
  const runtimeById = new Map((state.platformRuntime || []).map((item) => [item.id, item]));
  const people = state.personas?.items || [];
  const instances = state.agentInstances?.items || [];
  const templatesByID = new Map((state.agentPolicyTemplates?.items || []).map((item) => [item.id, item]));
  const routes = state.agentInstanceRoutes?.items || [];
  const platformsByID = new Map(platforms.map((item) => [item.id, item]));
  if (!state.agentInstanceConnectors) {
    const connectorRows = await Promise.all(instances.map(async (instance) => [instance.id, await api(`/api/v1/agent-instances/${encodeURIComponent(instance.id)}/connectors`)]));
    state.agentInstanceConnectors = Object.fromEntries(connectorRows);
  }
  const rows = instances.map((instance) => {
    const connectorBindings = state.agentInstanceConnectors?.[instance.id]?.items || [];
    const instanceRoutes = routes.filter((route) => route.instanceId === instance.id);
    const status = agentInstanceStatusFor(instance, connectorBindings, instanceRoutes, platformsByID, runtimeById);
    const persona = people.find((item) => item.id === instance.personaId);
    const template = templatesByID.get(instance.policyTemplateId);
    const connector = connectorBindings[0];
    const platform = platformsByID.get(connector?.connectorId);
    const routeSummary = instanceRoutes.length ? instanceRoutes.map((route) => `${route.transport} / ${route.conversationRef}`).join('，') : '尚未配置路由';
    const overrides = instance.overrides || {};
    const moderation = (state.agentInstanceCapabilities?.items || []).find((item) => item.instanceId === instance.id && item.capabilityId === 'group_moderation');
    const behavior = `${overrides.participationStyle || '继承'} · 插话 ${overrides.initialReplyProbability ?? '继承'} · 续聊 ${overrides.afterReplyProbability ?? '继承'}`;
    return `<article class="runtime-instance" data-state="${esc(status.tone)}"><header class="runtime-instance-head"><div><span class="runtime-instance-id">INSTANCE / ${esc(instance.id)}</span><h3>${esc(instance.displayName || instance.id)}</h3></div><span class="runtime-status ${esc(status.tone)}"><i class="status-dot ${status.tone === 'connected' ? 'ok' : ''}"></i>${esc(status.label)}</span></header><div class="runtime-instance-grid"><div><span>角色卡</span><strong>${esc(persona?.name || instance.personaId || '未绑定')}</strong><small>实例字段 · 记忆 ${esc(instance.memoryNamespace || '默认命名空间')}</small></div><div><span>运行策略</span><strong>${esc(template?.name || (instance.policyTemplateId || '未绑定模板'))}</strong><small>${esc(behavior)}</small></div><div><span>平台连接器</span><strong>${esc(platform?.displayName || connector?.connectorId || '未绑定')}</strong><small>${esc(platform?.type || '未登记')} · ${esc(routeSummary)} · ${moderation?.enabled ? '群管已授权' : '无群管权限'}</small></div></div><footer class="runtime-instance-actions">${button('实例配置', `runtime-config:${instance.id}`)}${platform ? button('连接器配置', `runtime-edit-platform:${platform.id}`, true) : ''}${persona ? button('角色卡', `runtime-open-role:${persona.id}`, true) : ''}</footer></article>`;
  }).join('');
  const hasXiaoman = instances.some((instance) => instance.id === 'xiaoman-qq' || (instance.personaId === 'xiaoman' && /小满/.test(instance.displayName || '')));
  const hasTelegram = instances.some((instance) => /telegram/i.test(instance.id) || /telegram/i.test(instance.displayName || ''));
  const planned = `${hasXiaoman ? '' : plannedRuntimeCard('xiaoman-qq', '小满 · 个人 QQ', '个人 QQ 连接器尚未登记。不会显示为可用，也不会读取或保存聊天中出现的密码。', '创建小满实例', 'new-xiaoman-qq')}${hasTelegram ? '' : plannedRuntimeCard('telegram-user', 'Telegram · 个人账号', 'MTProto 账号需要 API 凭据和登录会话。创建后仍保持停用，完成验证码登录后再启用。', '创建 Telegram 实例', 'new-telegram-user')}`;
  const wizard = state.runtimeWizard ? runtimeWizardForm(state.runtimeWizard) : '';
  const editor = state.editingRuntimeInstance ? controlDialog(`${state.editingRuntimeInstance.displayName || state.editingRuntimeInstance.id} · 实例配置`, runtimeInstanceConfigForm(state.editingRuntimeInstance, platforms, state.agentInstanceCapabilities?.items || []), '主动参与和管理权限属于运行实例，不写进角色卡。') : '';
  const architecture = `<div class="runtime-architecture" aria-label="运行层级"><div><span>01</span><strong>Core 主程序</strong><small>唯一配置源</small></div><i></i><div><span>02</span><strong>公共配置 / 策略</strong><small>全局默认与安全上限</small></div><i></i><div><span>03</span><strong>角色配置 / 策略</strong><small>人格、世界书与表达覆盖</small></div><i></i><div><span>04</span><strong>实例配置 / 策略</strong><small>账号、路由与实例隔离</small></div></div>`;
  return shell('运行中心', '把主程序、实例、角色卡和平台连接器放在同一条可追踪链路上。', architecture + configLayerPanel(state.configLayers) + `<div class="section-head runtime-section-head"><div><h2>运行实例</h2><p>已登记的实例按实时状态显示；主动参与和管理能力分别配置。</p></div><div class="section-actions">${button('新增运行实例', 'new-runtime-instance')}</div></div><div class="runtime-grid">${rows || empty('还没有运行实例。')}${planned}</div>${wizard}${editor}`);
}

async function integrationsView() {
  if (!state.integrations || !state.platforms || !state.platformCatalog || !state.platformRuntime || !state.models) {
    [state.integrations, state.platforms, state.platformCatalog, state.platformRuntime, state.models, state.providerConnections] = await Promise.all([
      api('/api/v1/integrations'), api('/api/v1/platforms'), api('/api/v1/platforms/catalog'), api('/api/v1/platforms/runtime-status'), api('/api/v1/model-endpoints'), api('/api/v1/provider-connections'),
    ]);
  }
  const all = integrationMap(state.integrations);
  const transport = all.channel_runtime?.config || {};
  const provider = all.provider_policy?.config || {};
  const messages = all.message_policy?.config || {};
  const groupChat = all.group_chat_policy?.config || {};
  const companion = all.companion_policy?.config || {};
  const grokPolicy = all.grok_policy?.config || {};
  const memoryPolicy = all.memory_policy?.config || {};
  const opsPolicy = all.ops_policy?.config || {};
  const imagePolicy = all.image_policy?.config || {};
  const llmModels = (state.models || []).filter((model) => model.enabled && model.executionKind === 'llm');
  const endpointOptions = (current, filter = () => true) => {
    const models = llmModels.filter(filter);
    const options = [['', '未指定（使用场景路由）'], ...models.map((model) => [model.id, `${model.provider} / ${model.model}`])];
    if (current && !models.some((model) => model.id === current)) options.push([current, `旧配置：${current}`]);
    return options;
  };
  const modeOptions = ['off', 'shadow', 'active'].map((mode) => `<option value="${mode}" ${transport.mode === mode ? 'selected' : ''}>${mode}</option>`).join('');
  const runtimeById = new Map((state.platformRuntime || []).map((item) => [item.id, item]));
  const platformRows = (state.platforms || []).map((platform) => { const runtime = runtimeById.get(platform.id); const runtimeStatus = runtimeStatusFor(platform, runtime); return `<div class="list-item"><div><button class="list-title-action" type="button" data-action="edit-platform:${esc(platform.id)}">${esc(platform.displayName)}</button> ${platform.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}<small>${esc(platform.type)} · ${platform.credentialConfigured ? '凭据已配置' : '凭据未配置'} · Go 原生连接：${esc(runtimeStatus.label)} · ${Object.keys(platform.settings || {}).length} 项参数</small></div><div class="row">${button('编辑', `edit-platform:${platform.id}`, true)}${button(platform.enabled ? '停用' : '启用', `toggle-platform:${platform.id}`, true)}${platform.compatibilitySource ? '' : button('删除', `delete-platform:${platform.id}`, true)}</div></div>`; }).join('');
  const section = pageSection('integrations', 'channels');
  const tabs = pageTabs('integrations', section, [['channels', '渠道与接管', (state.platforms || []).length], ['providers', '模型与联网'], ['media', '记忆与媒体'], ['conversation', '群聊与表达']]);
  const platformEditor = state.editingPlatform ? controlDialog(state.editingPlatform.id ? '编辑平台连接器' : '新增平台连接器', platformForm(state.editingPlatform), '连接参数保存到 Core；凭据只引用服务器环境变量。') : '';
  return shell('平台与接入', '管理连接器、媒体与群聊行为。', tabs +
    (section === 'channels' ? moduleGroup('渠道与接管', '连接器、实例和投递链路', card(`平台连接器 · ${(state.platforms || []).length}`, platformRows || empty('还没有平台连接器。'), button('新增连接器', 'new-platform')) +
    card('消息接管与投递', `<form class="form" data-form="transport-integration">
      <label class="field"><span>接管模式</span><select name="mode">${modeOptions}</select></label>
<div class="form-grid">${field('Outbox 轮询（秒）', 'deliveryPollSeconds', transport.deliveryPollSeconds ?? 1, 'number', 'min="0.2" max="30" step="0.2"')}</div>
<p class="muted">未 @ 消息是否参与，由“群聊参与模式”唯一决定；这里仅维护接管和 Outbox 投递链路。Go 进程直接接收平台事件、执行任务并投递 Outbox。</p>${formActions('保存接管设置')}
    </form>`), 'cyan', 'cable') : '') +
    (section === 'providers' ? moduleGroup('模型与联网能力', '执行、搜索和媒体路由', card('模型执行策略', `<form class="form" data-form="provider-integration">
      <div class="form-grid">${field('失败重试次数', 'providerRetries', provider.providerRetries ?? 2, 'number', 'min="0" max="4"')}${field('最大智能体步数', 'maxAgentSteps', provider.maxAgentSteps ?? 4, 'number', 'min="1" max="20"')}${field('单次工具超时（秒）', 'toolCallTimeoutSeconds', provider.toolCallTimeoutSeconds ?? 90, 'number', 'min="1" max="300"')}</div>
      <div class="toggle-row">${checkbox('接收上游流式响应', 'streaming', provider.streaming)}</div>
      <p class="muted">供应商地址、密钥引用和模型都在“供应商库”中配置；QQ 最终仍只发送完整回复。</p>${formActions('保存执行策略')}
    </form>`) +
    card('搜索与媒体路由', `<form class="form" data-form="grok-policy">
      <div class="form-grid">${field('兼容旧配置的首选搜索连接', 'searchConnectionId', grokPolicy.searchConnectionId || '')}${field('搜索模型', 'searchModel', grokPolicy.searchModel || '')}${field('搜索正文上限（字）', 'searchSummaryMaxChars', grokPolicy.searchSummaryMaxChars ?? 320, 'number', 'min="120" max="1200"')}${field('最多参考来源', 'searchMaxSources', grokPolicy.searchMaxSources ?? 2, 'number', 'min="0" max="3"')}${field('生图模型', 'imageModel', grokPolicy.imageModel || '')}${field('参考图编辑模型', 'imageEditModel', grokPolicy.imageEditModel || 'grok-imagine-image')}${field('视频模型', 'videoModel', grokPolicy.videoModel || '')}${field('视频超时（秒）', 'videoTimeoutSeconds', grokPolicy.videoTimeoutSeconds ?? 1200, 'number', 'min="10" max="2700"')}${field('学习检查周期（秒）', 'learningPollSeconds', grokPolicy.learningPollSeconds ?? 1800, 'number', 'min="300" max="86400"')}</div>
      ${textarea('搜索候选连接（按顺序，逗号分隔）', 'searchConnectionIds', listValue(grokPolicy.searchConnectionIds || (grokPolicy.searchConnectionId ? [grokPolicy.searchConnectionId] : [])), 'rows="2" placeholder="connection-grok-paid, connection-grok-local-search"')}
      ${textarea('图片/视频候选连接（按顺序，逗号分隔）', 'mediaConnectionIds', listValue(grokPolicy.mediaConnectionIds), 'rows="2" placeholder="connection-grok2api, connection-grok-paid-media"')}
      <div class="toggle-row">${checkbox('启用 Grok 能力（能力闸门）', 'enabled', grokPolicy.enabled !== false)}${checkbox('附带来源链接（表达选项）', 'searchIncludeSourceLinks', grokPolicy.searchIncludeSourceLinks === true)}${checkbox('启用待审核知识采集（学习子能力）', 'learningWorkerEnabled', grokPolicy.learningWorkerEnabled !== false)}</div>
      <p class="muted">角色页的“联网触发”才决定是否搜索；这里仅维护能力、连接和学习执行器。连接实例在“供应商库”维护。</p>${formActions('保存场景路由')}
    </form>`), 'violet', 'sparkles') : '') +
    (section === 'media' ? moduleGroup('记忆、运营与媒体', '记忆、OPS 和视觉生成', card('长期记忆', `<form class="form" data-form="memory-policy">
      <fieldset class="config-fieldset"><legend>沉淀与召回</legend>
        <div class="form-grid">${field('单次检索条数', 'retrievalLimit', memoryPolicy.retrievalLimit ?? 12, 'number', 'min="1" max="50"')}${field('每个范围最多记忆', 'maxMemoriesPerScope', memoryPolicy.maxMemoriesPerScope ?? 5000, 'number', 'min="1" max="100000"')}</div>
        <div class="toggle-row">${checkbox('启用长期记忆（总开关）', 'enabled', memoryPolicy.enabled !== false)}${checkbox('保守自动记忆（采集子开关）', 'autoCapture', memoryPolicy.autoCapture !== false)}${checkbox('允许本群共享记忆（范围）', 'allowGroupSharedMemory', memoryPolicy.allowGroupSharedMemory)}${checkbox('隔离梦境与假设（安全）', 'dreamMemoryIsolation', memoryPolicy.dreamMemoryIsolation !== false)}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>关系脉动</legend>
        <div class="form-grid">${field('成熟所需互动数', 'pulseMinInteractions', memoryPolicy.pulseMinInteractions ?? 5, 'number', 'min="3" max="100"')}${field('节律观察事件数', 'rhythmWindowEvents', memoryPolicy.rhythmWindowEvents ?? 60, 'number', 'min="10" max="500"')}${field('时区偏移（分钟）', 'timezoneOffsetMinutes', memoryPolicy.timezoneOffsetMinutes ?? 480, 'number', 'min="-720" max="840"')}</div>
        <div class="toggle-row">${checkbox('启用关系脉动', 'relationshipPulseEnabled', memoryPolicy.relationshipPulseEnabled !== false)}${checkbox('记录回复回流', 'outputFeedbackEnabled', memoryPolicy.outputFeedbackEnabled !== false)}${checkbox('记忆共振', 'memoryResonanceEnabled', memoryPolicy.memoryResonanceEnabled !== false)}${checkbox('作息预期', 'circadianAwarenessEnabled', memoryPolicy.circadianAwarenessEnabled !== false)}${checkbox('自然挂念', 'longingEnabled', memoryPolicy.longingEnabled !== false)}</div>
        <p class="muted">挂念只由真实互动间隔与关系变化生成；不得播报数值、监控作息或责备对方久未出现。</p>
      </fieldset>
      ${formActions('保存记忆策略')}
    </form>`) +
    card('OPS 分组状态', `<form class="form" data-form="ops-policy">
      <fieldset class="config-fieldset"><legend>渠道状态</legend>
        <div class="form-grid">${field('输出标题', 'statusTitle', opsPolicy.statusTitle || '分组检测')}${field('只读状态地址', 'statusUrl', opsPolicy.statusUrl || '')}${field('令牌环境变量', 'credentialRef', opsPolicy.credentialRef || '', 'text', 'placeholder="ERDAI_OPS_TOKEN"')}${field('请求超时（秒）', 'requestTimeoutSeconds', opsPolicy.requestTimeoutSeconds ?? 10, 'number', 'min="1" max="30"')}${field('展示最近状态数', 'timelinePoints', opsPolicy.timelinePoints ?? 10, 'number', 'min="1" max="30"')}${field('状态评估窗口（分钟）', 'evaluationWindowMinutes', opsPolicy.evaluationWindowMinutes ?? 5, 'number', 'min="1" max="60"')}${field('后台采样间隔（秒）', 'evaluationPollSeconds', opsPolicy.evaluationPollSeconds ?? 60, 'number', 'min="15" max="300"')}${field('命令别名（逗号分隔）', 'commandAliases', listValue(opsPolicy.commandAliases))}</div>
        ${textarea('分组倍率覆盖（每行：分组=倍率）', 'groupMultipliers', recordValue(opsPolicy.groupMultipliers), 'rows="5"')}
        <div class="toggle-row">${checkbox('启用 OPS 查询', 'enabled', opsPolicy.enabled !== false)}${checkbox('显示“倍率越小越便宜”', 'showMultiplierNote', opsPolicy.showMultiplierNote !== false)}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>Codex 智力雷达</legend>
        <div class="form-grid">${field('CodexRadar 数据地址', 'radarUrl', opsPolicy.radarUrl || 'https://codexradar.com/api/intelligence-efficiency-metrics')}${field('雷达命令（逗号分隔）', 'radarCommandAliases', listValue(opsPolicy.radarCommandAliases))}${field('最低样本量', 'radarMinimumSamples', opsPolicy.radarMinimumSamples ?? 5, 'number', 'min="1" max="10000"')}${field('模型家族顺序', 'radarFamilyOrder', listValue(opsPolicy.radarFamilyOrder))}${field('任务显示顺序', 'radarRecommendationOrder', listValue(opsPolicy.radarRecommendationOrder))}</div>
        ${textarea('任务推荐映射（每行：任务=模型家族）', 'radarRecommendations', recordValue(opsPolicy.radarRecommendations), 'rows="5"')}
        <div class="toggle-row">${checkbox('启用智力雷达', 'radarEnabled', opsPolicy.radarEnabled !== false)}</div>
      </fieldset>
      <p class="muted">令牌只通过环境变量注入；Core 数据库不保存令牌值。</p>${formActions('保存 OPS 策略')}
    </form>`) +
    card('图片生成', `<form class="form" data-form="image-policy">
      <fieldset class="config-fieldset"><legend>模型与任务</legend>
        <div class="form-grid">${field('供应商 ID', 'providerId', imagePolicy.providerId || '')}${field('模型', 'model', imagePolicy.model || '')}${field('密钥环境变量', 'credentialRef', imagePolicy.credentialRef || '', 'text', 'placeholder="ERDAI_IMAGE_API_KEY"')}${field('默认张数', 'defaultImageCount', imagePolicy.defaultImageCount ?? 1, 'number', 'min="1" max="10"')}${field('单任务最多张数', 'maxImageCount', imagePolicy.maxImageCount ?? 1, 'number', 'min="1" max="10"')}${field('单消息最多图片', 'maxImagesPerMessage', imagePolicy.maxImagesPerMessage ?? 1, 'number', 'min="1" max="10"')}${field('任务超时（秒）', 'timeoutSeconds', imagePolicy.timeoutSeconds ?? 600, 'number', 'min="10" max="900"')}${field('失败重试次数', 'maxRetryAttempts', imagePolicy.maxRetryAttempts ?? 1, 'number', 'min="0" max="10"')}${field('并发任务数', 'maxConcurrentTasks', imagePolicy.maxConcurrentTasks ?? 1, 'number', 'min="1" max="20"')}${field('排队任务数', 'maxQueuedTasks', imagePolicy.maxQueuedTasks ?? 3, 'number', 'min="0" max="100"')}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>限流、审计与历史</legend>
        <div class="form-grid">${field('用户冷却（秒）', 'rateLimitSeconds', imagePolicy.rateLimitSeconds ?? 60, 'number', 'min="0" max="86400"')}${field('每日张数', 'dailyLimitCount', imagePolicy.dailyLimitCount ?? 5, 'number', 'min="1" max="10000"')}${field('图片大小上限（MB）', 'maxImageSizeMb', imagePolicy.maxImageSizeMb ?? 8, 'number', 'min="1" max="100"')}${field('提示词审核模型 ID', 'promptAuditProviderId', imagePolicy.promptAuditProviderId || '')}${field('历史条数', 'historyLimit', imagePolicy.historyLimit ?? 200, 'number', 'min="0" max="10000"')}${field('历史保留天数', 'historyRetentionDays', imagePolicy.historyRetentionDays ?? 7, 'number', 'min="1" max="3650"')}</div>
        <div class="toggle-row">${checkbox('启用图片生成（总开关）', 'enabled', imagePolicy.enabled !== false)}${checkbox('启用每日限制（安全子开关）', 'dailyLimitEnabled', imagePolicy.dailyLimitEnabled !== false)}${checkbox('启用提示词审核（安全子开关）', 'promptAuditEnabled', imagePolicy.promptAuditEnabled !== false)}${checkbox('保存任务历史（记录选项）', 'historyEnabled', imagePolicy.historyEnabled !== false)}</div>
      </fieldset>
		<fieldset class="config-fieldset"><legend>角色视觉导演</legend>
			<div class="form-grid">${field('视觉时区', 'visualTimezone', imagePolicy.visualTimezone || 'Asia/Shanghai')}</div>
			<div class="toggle-row">${checkbox('启用视觉变量向量', 'visualDirectorEnabled', imagePolicy.visualDirectorEnabled !== false)}${checkbox('使用当前时间与季节', 'visualUseTimeContext', imagePolicy.visualUseTimeContext !== false)}</div>
			${textarea('自拍类型池（每行一种）', 'selfieTypes', lineValue(imagePolicy.selfieTypes || ['近景自拍', '半身生活照', '全身生活照', '全身穿搭照', '镜面穿搭自拍', '朋友视角抓拍', '坐姿生活照']), 'rows="7"')}
		</fieldset>
      <p class="muted">只保存密钥环境变量名；真实 Key 不进入 Core。</p>${formActions('保存图片策略')}
    </form>`), 'amber', 'activity') : '') +
    (section === 'conversation' ? moduleGroup('群聊与表达', '参与判断、上下文和回复节奏', card('群聊参与与上下文', `<form class="form" data-form="group-chat-policy">
      <fieldset class="config-fieldset"><legend>参与范围</legend>
        <div class="form-grid">${field('启用群（逗号分隔，留空为全部）', 'enabledGroups', listValue(groupChat.enabledGroups))}${field('触发词', 'triggerKeywords', listValue(groupChat.triggerKeywords || ['豆包']))}${field('命令前缀', 'commandPrefixes', listValue(groupChat.commandPrefixes || ['/', '!', '#']))}${field('首次插话概率', 'initialProbability', groupChat.initialProbability ?? 0.12, 'number', 'min="0" max="1" step="0.01"')}${field('有人接话后概率', 'afterReplyProbability', groupChat.afterReplyProbability ?? 0.24, 'number', 'min="0" max="1" step="0.01"')}${field('接话概率持续（秒）', 'probabilityDurationSeconds', groupChat.probabilityDurationSeconds ?? 180, 'number', 'min="0" max="86400"')}</div>
        <div class="toggle-row">${selectField('参与模式（唯一主开关）', 'participationMode', groupChat.participationMode || (groupChat.proactiveChatEnabled ? 'adaptive' : 'addressed_only'), [['addressed_only', '仅被叫到（@/回复/命令）'], ['adaptive', '自适应插话'], ['social', '社交陪伴']])}${checkbox('关键词智能判断（辅助）', 'keywordSmartMode', groupChat.keywordSmartMode)}${checkbox('消息质量判断（辅助）', 'messageQualityEnabled', groupChat.messageQualityEnabled !== false)}${checkbox('过滤重复回复（辅助）', 'duplicateFilterEnabled', groupChat.duplicateFilterEnabled !== false)}</div>
        <p class="muted">这里唯一决定主动参与的是“参与模式”。消息接管总开关在“渠道与接管”；辅助判断只会收紧，不会单独放行插话。</p>
      </fieldset>
      <fieldset class="config-fieldset"><legend>判断与回复提示</legend>
        <div class="form-grid">${selectField('群聊判断端点', 'decisionProviderId', groupChat.decisionProviderId || '', endpointOptions(groupChat.decisionProviderId || ''))}${field('判断超时（秒）', 'decisionTimeoutSeconds', groupChat.decisionTimeoutSeconds ?? 3, 'number', 'min="1" max="60" step="0.5"')}${selectField('判断提示模式', 'decisionPromptMode', groupChat.decisionPromptMode || 'append', [['append', '追加'], ['override', '覆盖']])}${selectField('回复提示模式', 'replyPromptMode', groupChat.replyPromptMode || 'append', [['append', '追加'], ['override', '覆盖']])}</div>
        ${textarea('判断附加提示', 'decisionExtraPrompt', groupChat.decisionExtraPrompt || '', 'rows="4"')}
        ${textarea('回复附加提示', 'replyExtraPrompt', groupChat.replyExtraPrompt || '', 'rows="4"')}
        <div class="toggle-row">${checkbox('判断时注入当前角色', 'decisionIncludePersona', groupChat.decisionIncludePersona !== false)}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>短上下文与并发</legend>
        <div class="form-grid">${selectField('并发模式', 'concurrentMode', groupChat.concurrentMode || 'smart', [['smart', '智能合并'], ['legacy', '兼容模式']])}${field('群上下文消息数', 'maxContextMessages', groupChat.maxContextMessages ?? 6, 'number', 'min="0" max="200"')}${field('@ 关联消息数', 'atLinkMaxMessages', groupChat.atLinkMaxMessages ?? 4, 'number', 'min="0" max="50"')}${field('@ 关联时长（秒）', 'atLinkMaxSeconds', groupChat.atLinkMaxSeconds ?? 90, 'number', 'min="0" max="3600"')}${field('智能合并等待（秒）', 'smartMergeWaitSeconds', groupChat.smartMergeWaitSeconds ?? 1.2, 'number', 'min="0" max="30" step="0.1"')}${field('智能合并最多消息', 'smartMaxBatchSize', groupChat.smartMaxBatchSize ?? 3, 'number', 'min="1" max="20"')}${field('批次认领延迟（秒）', 'smartClaimDelaySeconds', groupChat.smartClaimDelaySeconds ?? 0.05, 'number', 'min="0" max="5" step="0.01"')}${field('并发等待轮数', 'concurrentWaitMaxLoops', groupChat.concurrentWaitMaxLoops ?? 12, 'number', 'min="0" max="100"')}${field('每轮等待（秒）', 'concurrentWaitIntervalSeconds', groupChat.concurrentWaitIntervalSeconds ?? 0.1, 'number', 'min="0.01" max="5" step="0.01"')}</div>
        <div class="toggle-row">${checkbox('向模型提示合并批次', 'smartBatchHintEnabled', groupChat.smartBatchHintEnabled !== false)}${checkbox('启用群等待窗口', 'groupWaitWindowEnabled', groupChat.groupWaitWindowEnabled)}${checkbox('上下文带时间', 'includeTimestamp', groupChat.includeTimestamp)}${checkbox('上下文带发送者', 'includeSenderInfo', groupChat.includeSenderInfo !== false)}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>降噪与回复密度</legend>
        <div class="form-grid">${field('问题加权', 'questionBoost', groupChat.questionBoost ?? 0.03, 'number', 'min="0" max="1" step="0.01"')}${field('灌水降权', 'waterReduce', groupChat.waterReduce ?? 0.12, 'number', 'min="0" max="1" step="0.01"')}${field('密度窗口（秒）', 'replyDensityWindowSeconds', groupChat.replyDensityWindowSeconds ?? 300, 'number', 'min="1" max="86400"')}${field('窗口最多回复', 'replyDensityMaxReplies', groupChat.replyDensityMaxReplies ?? 1, 'number', 'min="1" max="100"')}${field('密度软限制比例', 'replyDensitySoftLimitRatio', groupChat.replyDensitySoftLimitRatio ?? 0.85, 'number', 'min="0" max="1" step="0.01"')}${selectField('@ 他人处理', 'ignoreAtOthersMode', groupChat.ignoreAtOthersMode || 'strict', [['strict', '严格忽略'], ['allow_with_bot', '同时 @ 豆包时允许']])}</div>
        <div class="toggle-row">${checkbox('启用回复密度限制', 'replyDensityEnabled', groupChat.replyDensityEnabled)}${checkbox('密度状态提示模型', 'replyDensityAiHint', groupChat.replyDensityAiHint)}${checkbox('忽略 @ 他人的消息', 'ignoreAtOthers', groupChat.ignoreAtOthers !== false)}${checkbox('忽略 @ 全体成员', 'ignoreAtAll', groupChat.ignoreAtAll !== false)}${checkbox('过滤纯表情/贴图', 'ignoreLowValueMedia', groupChat.ignoreLowValueMedia !== false)}</div>
        <div class="form-grid">${field('最少有效文字', 'lowValueMinTextChars', groupChat.lowValueMinTextChars ?? 2, 'number', 'min="0" max="20"')}${field('低价值消息标记（逗号分隔）', 'lowValueMediaMarkers', listValue(groupChat.lowValueMediaMarkers || ['[图片]', '[表情]', '[贴图]']))}</div>
      </fieldset>
      <fieldset class="config-fieldset"><legend>打字与图片理解</legend>
        <div class="form-grid">${field('打字速度（字/秒）', 'typingSpeedCharsPerSecond', groupChat.typingSpeedCharsPerSecond ?? 12, 'number', 'min="1" max="100" step="0.5"')}${field('打字最长延迟（秒）', 'typingMaxDelaySeconds', groupChat.typingMaxDelaySeconds ?? 1, 'number', 'min="0" max="30" step="0.1"')}${selectField('图片处理范围', 'imageScope', groupChat.imageScope || 'all', [['all', '全部图片'], ['mention_only', '仅提及机器人'], ['at_only', '仅 @ 机器人'], ['keyword_only', '仅触发词']])}${selectField('识图模型端点', 'imageProviderId', groupChat.imageProviderId || '', endpointOptions(groupChat.imageProviderId || '', (model) => (model.capabilities || []).includes('vision')))}${field('识图超时（秒）', 'imageTimeoutSeconds', groupChat.imageTimeoutSeconds ?? 90, 'number', 'min="1" max="300"')}${field('单条最多图片', 'maxImagesPerMessage', groupChat.maxImagesPerMessage ?? 3, 'number', 'min="1" max="10"')}</div>
        ${textarea('识图提示', 'imagePrompt', groupChat.imagePrompt || '', 'rows="4"')}
        <div class="toggle-row">${checkbox('启用打字模拟', 'typingSimulatorEnabled', groupChat.typingSimulatorEnabled)}${checkbox('启用图片理解', 'imageProcessingEnabled', groupChat.imageProcessingEnabled !== false)}${checkbox('缓存图片理解结果', 'imageCacheEnabled', groupChat.imageCacheEnabled)}</div>
      </fieldset>
      ${formActions('保存群聊策略')}
    </form>`) +
	card('分层上下文与模型路由', `<form class="form" data-form="companion-policy">
		<div class="form-grid">${field('启用群（逗号分隔）', 'enabledGroups', listValue(companion.enabledGroups))}${selectField('闲聊端点', 'chatModel', companion.chatModel || '', endpointOptions(companion.chatModel || ''))}${selectField('任务端点', 'taskModel', companion.taskModel || '', endpointOptions(companion.taskModel || ''))}${field('复杂消息阈值（字）', 'complexMessageChars', companion.complexMessageChars ?? 100, 'number', 'min="40" max="4000"')}${field('实时上下文消息数', 'contextMessagesPerPrompt', companion.contextMessagesPerPrompt ?? 40, 'number', 'min="6" max="200"')}${field('上下文 Token 预算', 'contextTokenBudget', companion.contextTokenBudget ?? 6000, 'number', 'min="512" max="100000"')}${field('摘要间隔消息数', 'summaryIntervalMessages', companion.summaryIntervalMessages ?? 12, 'number', 'min="2" max="200"')}${field('摘要窗口消息数', 'summaryWindowMessages', companion.summaryWindowMessages ?? 12, 'number', 'min="2" max="200"')}${field('摘要保留小时', 'topicTtlHours', companion.topicTtlHours ?? 6, 'number', 'min="1" max="720"')}${field('每会话冷存消息上限', 'maxMessagesPerGroup', companion.maxMessagesPerGroup ?? 20000, 'number', 'min="100" max="100000"')}${field('冷历史保留（小时）', 'messageRetentionHours', companion.messageRetentionHours ?? 8760, 'number', 'min="1" max="43800"')}${field('回顾时扫描消息数', 'coldRecallScanMessages', companion.coldRecallScanMessages ?? 5000, 'number', 'min="100" max="20000"')}${field('回顾时召回消息数', 'coldRecallMaxMessages', companion.coldRecallMaxMessages ?? 12, 'number', 'min="1" max="30"')}</div>
		<div class="toggle-row">${checkbox('启用分层上下文', 'enabled', companion.enabled !== false)}${checkbox('按消息类型路由模型', 'enableModelRouting', companion.enableModelRouting !== false)}${checkbox('维护热点记忆', 'collectTopicState', companion.collectTopicState !== false)}${checkbox('按需回顾冷历史', 'coldRecallEnabled', companion.coldRecallEnabled !== false)}</div>
		<p class="muted">实时层只带最近对话；稳定偏好进入热点记忆；只有出现“之前、上次、还记得”等回顾意图时才扫描冷历史。</p>${formActions('保存分层上下文策略')}
    </form>`) +
    card('消息节奏与自然反馈', `<form class="form" data-form="message-policy">
      <fieldset class="config-fieldset"><legend>回复节奏</legend>
        <div class="form-grid">${field('每段目标最少字数', 'segmentMinChars', messages.segmentMinChars ?? 10, 'number', 'min="10" max="80"')}${field('每段目标最多字数', 'segmentMaxChars', messages.segmentMaxChars ?? 20, 'number', 'min="10" max="100"')}${field('最多分段', 'maxReplySegments', messages.maxReplySegments ?? 2, 'number', 'min="1" max="5"')}${field('最短停顿（秒）', 'segmentMinDelaySeconds', messages.segmentMinDelaySeconds ?? 0, 'number', 'min="0" max="5" step="0.05"')}${field('最长停顿（秒）', 'segmentMaxDelaySeconds', messages.segmentMaxDelaySeconds ?? 0.25, 'number', 'min="0" max="8" step="0.05"')}</div>
      <div class="toggle-row">${checkbox('启用完整句分段', 'segmentedReplyEnabled', messages.segmentedReplyEnabled === true)}${checkbox('启用慢工具即时反馈（主开关）', 'toolProgressEnabled', messages.toolProgressEnabled !== false)}${checkbox('搜索前先提示（子开关）', 'toolProgressSearchEnabled', messages.toolProgressSearchEnabled === true)}</div>
      </fieldset>
      <div class="copy-library-head"><div><strong>随机话术库</strong><small>每次从对应候选池随机选一句</small></div>${button('补充默认候选', 'expand-message-copy', true)}</div>
      <div class="copy-module-grid">
        <fieldset class="config-fieldset copy-module" data-copy-kind="search"><legend>搜索</legend>${messageCopyEditor(messages, 'toolProgressSearchMessages', '搜索反馈文案')}</fieldset>
        <fieldset class="config-fieldset copy-module" data-copy-kind="image"><legend>图片与自拍</legend>${messageCopyEditor(messages, 'toolProgressImageMessages', '生图反馈文案')}${messageCopyEditor(messages, 'toolProgressPhotoMessages', '自拍反馈文案')}${messageCopyEditor(messages, 'toolCompletionImageMessages', '生图完成文案')}</fieldset>
        <fieldset class="config-fieldset copy-module" data-copy-kind="video"><legend>视频</legend>${messageCopyEditor(messages, 'toolProgressVideoMessages', '视频反馈文案')}${messageCopyEditor(messages, 'toolCompletionVideoMessages', '视频完成文案')}</fieldset>
        <fieldset class="config-fieldset copy-module" data-copy-kind="document"><legend>文档</legend>${messageCopyEditor(messages, 'toolProgressDocumentMessages', '文档反馈文案')}${messageCopyEditor(messages, 'toolCompletionDocumentMessages', '文档完成文案')}</fieldset>
      </div>
      ${formActions('保存消息策略')}
    </form>`), 'rose', 'messages-square') : '') + platformEditor);
}

function providerConnectionForm(connection = {}) {
  const drivers = state.providerDrivers || [];
  const options = drivers.map((driver) => [driver.id, driver.label]);
  const selected = drivers.find((driver) => driver.id === connection.protocol);
  return `<form class="form compact" data-form="provider-connection" data-id="${esc(connection.id || '')}">
    <div class="form-grid">${field('唯一 ID', 'id', connection.id || '', 'text', connection.id ? 'readonly' : 'required')}${field('供应商 ID', 'provider', connection.provider || '', 'text', 'required')}${selectField('协议驱动', 'protocol', connection.protocol || 'openai_chat_completion', options)}${field('API Base', 'apiBase', connection.apiBase || '', 'url', 'required')}${field('价格源 URL', 'pricingUrl', connection.pricingUrl || '', 'url', 'placeholder="https://provider.example/v1/models"')}${field('密钥环境变量', 'credentialRef', connection.credentialRef || 'ERDAI_MODEL_API_KEY')}${field('超时（秒）', 'timeoutSeconds', connection.timeoutSeconds || 120, 'number', 'min="1" max="600"')}</div>
    ${selected ? `<div class="driver-hint"><strong>${esc(selected.label)}</strong><span>${esc(selected.description)}</span><small>能力：${esc(listValue(selected.capabilities))} · 探针：${esc(selected.probePath)}</small></div>` : ''}
    ${checkbox('启用连接', 'enabled', connection.enabled !== false)}${formActions(connection.id ? '保存连接' : '新增连接', connection.id ? 'cancel-edit' : '')}
  </form>`;
}

function modelForm(model = {}, connections = []) {
  const connectionOptions = [['', '按供应商自动匹配'], ...connections.map((item) => [item.id, `${item.provider} · ${item.apiBase}`])];
  return `<form class="form compact" data-form="model" data-id="${esc(model.id || '')}">
    <div class="form-grid">${field('唯一 ID', 'id', model.id || '', 'text', model.id ? 'readonly' : 'required')}${field('供应商', 'provider', model.provider || '')}${field('模型名', 'model', model.model || '')}${selectField('供应商连接', 'connectionId', model.connectionId || '', connectionOptions)}${field('执行类型', 'executionKind', model.executionKind || 'llm')}${field('适配器引用', 'adapterRef', model.adapterRef || '')}${field('上下文 Token', 'maxContextTokens', model.maxContextTokens || 0, 'number')}${field('输入成本 / 百万', 'inputCostPerMillion', model.inputCostPerMillion || 0, 'number', 'step="0.0001"')}${field('输出成本 / 百万', 'outputCostPerMillion', model.outputCostPerMillion || 0, 'number', 'step="0.0001"')}${field('质量分（0-1）', 'qualityScore', model.qualityScore ?? 0.5, 'number', 'min="0" max="1" step="0.01"')}${field('优先级', 'priority', model.priority || 0, 'number')}</div>
    ${field('能力（逗号分隔）', 'capabilities', listValue(model.capabilities))}${checkbox('启用此端点', 'enabled', model.enabled !== false)}${formActions(model.id ? '保存模型' : '新增模型', model.id ? 'cancel-edit' : '')}
  </form>`;
}
function usageLine(usage = {}) {
  const calls = Number(usage.calls || 0);
  const tokens = Number(usage.totalTokens || 0);
  const cost = Number(usage.estimatedCost || 0);
  const tokenText = usage.tokenDataAvailable ? `${tokens.toLocaleString()} tokens` : 'Token 未回传';
  const costText = usage.pricingConfigured ? `$${cost.toFixed(4)}` : '未配单价';
  return `调用 ${calls.toLocaleString()} 次 · ${tokenText} · 估算 ${costText}`;
}
async function modelsView() {
  if (!state.models || !state.providerConnections || !state.providerDrivers) [state.models, state.providerConnections, state.providerDrivers] = await Promise.all([state.models || api('/api/v1/model-endpoints'), state.providerConnections || api('/api/v1/provider-connections'), state.providerDrivers || api('/api/v1/provider-drivers')]);
  if (!state.health) state.health = await api('/api/v1/model-health');
  const section = pageSection('models', 'connections');
  const connectionEditor = state.editingProviderConnection ? controlDialog(state.editingProviderConnection.id ? '编辑供应商连接' : '新增供应商连接', providerConnectionForm(state.editingProviderConnection), '协议、地址、凭据引用和价格源均由该连接独立持有。') : '';
  const healthHistory = state.healthHistory ? controlDialog(`健康历史 · ${state.healthHistory.id}`, `<div class="list">${(state.healthHistory.items || []).map((item) => `<div class="list-item compact-item"><div><strong>${item.healthy ? '正常' : '失败'} · ${item.latencyMs == null ? '-' : `${esc(item.latencyMs)} ms`}</strong><small>${esc(item.checkedAt)}${item.statusMessage ? ` · ${esc(item.statusMessage)}` : ''}</small></div></div>`).join('') || empty('暂无主动或兼容健康样本。')}</div>`, 'Core 主动健康检查的实时采样。') : '';
  const driverMap = new Map((state.providerDrivers || []).map((driver) => [driver.id, driver]));
  const groupedConnections = (state.providerConnections || []).reduce((groups, item) => { (groups[item.protocol] ||= []).push(item); return groups; }, {});
  const connectionRows = Object.entries(groupedConnections).map(([protocol, items]) => {
    const driver = driverMap.get(protocol);
    const rows = items.map((item) => `<div class="list-item"><div><strong>${esc(item.provider)} ${item.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>${esc(item.apiBase)} · ${item.credentialConfigured ? '凭据已就绪' : '凭据缺失'} · ${esc(item.timeoutSeconds)} 秒</small><small>${item.pricingUrl ? `价格源：${esc(item.pricingUrl)}` : '价格源未配置'} · ${usageLine(item.usage)}</small></div><div class="row">${button('测试', `test-provider:${item.id}`, true)}${item.pricingUrl ? button('同步价格', `sync-pricing:${item.id}`, true) : ''}${button('编辑', `edit-provider:${item.id}`, true)}${button('删除', `delete-provider:${item.id}`, true)}</div></div>`).join('');
    return `<section class="provider-protocol"><div class="provider-protocol-head"><div><strong>${esc(driver?.label || protocol)}</strong><small>${esc(driver?.description || '自定义协议')}</small></div><span class="pill">${items.length} 个连接</span></div>${rows}</section>`;
  }).join('');
  const editing = state.editingModel ? controlDialog(state.editingModel.id ? '编辑模型端点' : '新增模型端点', modelForm(state.editingModel, state.providerConnections || []), '端点引用供应商连接，并声明能力、质量和成本。') : '';
  const usage = (state.models || []).reduce((total, model) => ({ calls: total.calls + Number(model.usage?.calls || 0), tokens: total.tokens + Number(model.usage?.totalTokens || 0), cost: total.cost + Number(model.usage?.estimatedCost || 0), priced: total.priced || Boolean(model.usage?.pricingConfigured) }), { calls: 0, tokens: 0, cost: 0, priced: false });
  const usageCost = usage.priced ? `$${usage.cost.toFixed(4)}` : '待配置';
  const usageOverview = `<div class="usage-strip"><div><strong>${usage.calls.toLocaleString()}</strong><span>模型调用</span></div><div><strong>${usage.tokens.toLocaleString()}</strong><span>Token（已回传）</span></div><div><strong>${usageCost}</strong><span>成本估算</span></div></div>`;
  const rows = (state.models || []).map((m) => `<div class="list-item"><div><strong>${esc(m.provider)} / ${esc(m.model)}</strong><small>ID：${esc(m.id)} · ${esc(m.executionKind)} · 能力：${esc(listValue(m.capabilities) || '无')}</small><small>健康：${m.health ? esc(m.health.statusMessage || m.health.status) : '未检查'} · 质量 ${esc(m.qualityScore)}</small><small>价格：${esc(m.pricingSource || '未配置')} · ${esc(m.pricingCurrency || 'USD')}${m.pricingCheckedAt ? ` · ${esc(m.pricingCheckedAt)}` : ''}</small><small class="usage-line">${usageLine(m.usage)}${m.usage?.lastUsedAt ? ` · 最近 ${esc(m.usage.lastUsedAt)}` : ''}</small></div><div class="row">${button('历史', `health-history:${m.id}`, true)}${button('编辑', `edit-model:${m.id}`, true)}${button(m.enabled ? '停用' : '启用', `toggle-model:${m.id}`, true)}${button('删除', `delete-model:${m.id}`, true)}</div></div>`).join('');
  const tabs = pageTabs('models', section, [['connections', '供应商连接', (state.providerConnections || []).length], ['endpoints', '模型端点', (state.models || []).length]]);
  const body = section === 'connections'
    ? card('协议驱动与供应商连接', connectionRows || empty('还没有供应商连接。'), button('新增连接', 'new-provider'))
    : usageOverview + card('已配置端点', rows || empty('还没有模型端点。'), button('新增端点', 'new-model'));
  return shell('模型与供应商', '分开管理连接、模型能力、健康和成本。', tabs + body + connectionEditor + editing + healthHistory);
}

function personaForm(persona = {}) {
  return `<form class="form" data-form="persona" data-id="${esc(persona.id || '')}">
    <div class="persona-avatar-editor">
      ${avatarMarkup(persona, 'persona-avatar persona-avatar-large')}
      <div><strong>角色头像</strong><p class="muted">PNG、JPEG 或 WebP，不超过 512 KiB。</p><label class="button secondary file-button">选择图片<input type="file" name="avatarFile" accept="image/png,image/jpeg,image/webp"></label>${persona.avatarDataUri ? button('移除图片', 'remove-avatar', true) : ''}</div>
      <input type="hidden" name="avatarDataUri" value="${esc(persona.avatarDataUri || '')}">
    </div>
    <div class="form-grid">${field('名称', 'name', persona.name || '', 'text', 'required')}${field('标签（逗号分隔）', 'tags', listValue(persona.tags))}${field('作者', 'creator', persona.creator || '')}${field('角色版本', 'characterVersion', persona.characterVersion || '')}${field('来源格式', 'sourceFormat', persona.sourceFormat || 'native')}${field('来源版本', 'sourceVersion', persona.sourceVersion || '')}</div>
		${textarea('简介', 'description', persona.description || '')}${textarea('人物外观（用于本人照片与自拍）', 'visualDescription', persona.visualDescription || '', 'rows="5"')}${textarea('性格', 'personality', persona.personality || '', 'rows="8"')}${textarea('场景与世界观', 'scenario', persona.scenario || '', 'rows="8"')}${textarea('系统提示词', 'systemPrompt', persona.systemPrompt || '', 'rows="12"')}${textarea('历史消息后指令', 'postHistoryInstructions', persona.postHistoryInstructions || '', 'rows="6"')}${textarea('示例对话', 'messageExample', persona.messageExample || '', 'rows="8"')}${textarea('首条消息', 'firstMessage', persona.firstMessage || '')}${textarea('备用问候语（每行一条）', 'alternateGreetings', listValue(persona.alternateGreetings), 'rows="4"')}${formActions(persona.id ? '保存智能体' : '创建智能体', persona.id ? 'cancel-edit' : '')}
  </form>`;
}
function personaDossier(persona, profileItem = {}, editorData = {}) {
  if (!persona?.id) return '';
  const profile = profileItem?.profile || profileItem || {};
  const refs = editorData.references?.items || [];
  const samples = editorData.samples?.items || [];
  const traits = editorData.traits?.items || [];
  const worldbook = editorData.worldbook?.items || [];
  const primary = refs.find((ref) => ref.isPrimary && ref.mediaType === 'image')
    || refs.find((ref) => ref.mediaType === 'image');
  const portrait = primary
    ? `<img src="${esc(primary.contentUrl)}" alt="${esc(persona.name)}主参考形象">`
    : avatarMarkup(persona, 'persona-dossier-avatar');
  const tags = (persona.tags || []).map((tag) => `<span>${esc(tag)}</span>`).join('');
  const traitList = traits.slice(0, 6).map((trait) => `<li><strong>${esc(trait.name)}</strong><span>${esc(trait.description)}</span></li>`).join('');
  const gallery = refs.slice(0, 4).map((ref) => ref.mediaType === 'video'
    ? `<video src="${esc(ref.contentUrl)}" muted playsinline preload="metadata"></video>`
    : `<img src="${esc(ref.contentUrl)}" alt="${esc(ref.label || '形象参考')}" loading="lazy">`).join('');
  const limits = [
    profile.maxReplyChars ? `${profile.maxReplyChars} 字` : '继承字数',
    profile.maxReplySentences ? `${profile.maxReplySentences} 句` : '继承句数',
    profile.proactiveEnabled === false ? '主动参与关闭' : '主动参与开启',
    profile.memoryPolicy || '继承记忆策略',
  ];
  return `<section class="persona-dossier" aria-label="${esc(persona.name)}角色档案">
    <div class="persona-dossier-hero">
      <div class="persona-dossier-portrait">${portrait}<span>${esc(persona.characterVersion || 'V1')}</span></div>
      <div class="persona-dossier-intro">
        <p>CHARACTER DOSSIER / ${esc(persona.id)}</p>
        <h2>${esc(persona.name)}</h2>
        <div class="persona-dossier-tags">${tags || '<span>未设置标签</span>'}</div>
        <blockquote>${esc(persona.description || '还没有角色简介。')}</blockquote>
      </div>
      <dl class="persona-dossier-facts">
        <div><dt>人格特质</dt><dd>${traits.length}</dd></div>
        <div><dt>场景样本</dt><dd>${samples.length}</dd></div>
        <div><dt>世界书</dt><dd>${worldbook.length}</dd></div>
        <div><dt>形象素材</dt><dd>${refs.length}</dd></div>
      </dl>
    </div>
    <div class="persona-dossier-grid">
      <section><span class="dossier-index">01</span><h3>性格与关系</h3><p>${esc(persona.personality || '尚未配置性格。')}</p><ul>${traitList || '<li><span>还没有人格特质。</span></li>'}</ul></section>
      <section><span class="dossier-index">02</span><h3>表达与节奏</h3><p>${esc(profile.expressionPrompt || persona.postHistoryInstructions || '继承全局表达规则。')}</p><div class="persona-dossier-chips">${limits.map((item) => `<span>${esc(item)}</span>`).join('')}</div></section>
      <section class="persona-dossier-visual"><span class="dossier-index">03</span><h3>视觉身份</h3><p>${esc(persona.visualDescription || '尚未配置形象。')}</p><div class="persona-dossier-gallery">${gallery || '<span>暂无参考素材</span>'}</div></section>
      <section><span class="dossier-index">04</span><h3>世界与边界</h3><p>${esc(persona.scenario || '尚未配置场景。')}</p><small>${worldbook.length ? `${worldbook.length} 条世界书会按上下文注入` : '当前没有世界书条目'}</small></section>
    </div>
  </section>`;
}
function visualReferencePanel(persona) {
  if (!persona?.id) return '';
  const refs = state.personaVisualReferences?.personaId === persona.id ? (state.personaVisualReferences.items || []) : [];
  const categoryLabels = { identity: '主形象', expression: '表情', makeup: '妆容', outfit: '穿搭', scene: '场景', motion: '动态' };
  const cards = refs.map((ref) => {
    const media = ref.mediaType === 'video'
      ? `<video class="visual-reference-media" src="${esc(ref.contentUrl)}" controls muted playsinline preload="metadata"></video>`
      : `<img class="visual-reference-media" src="${esc(ref.contentUrl)}" alt="${esc(ref.label || '角色形象参考')}" loading="lazy">`;
    return `<article class="visual-reference-tile ${ref.isPrimary ? 'primary' : ''}">
      ${media}<div class="visual-reference-body"><div class="visual-reference-head"><strong>${esc(ref.label || '未命名参考')}</strong>${ref.isPrimary ? '<span class="pill">主参考</span>' : ''}</div>
      <small>${esc(categoryLabels[ref.category] || ref.category)} · ${esc(ref.mediaType)} · ${(Number(ref.byteSize || 0) / 1024 / 1024).toFixed(1)} MB</small>
      ${ref.promptNotes ? `<p>${esc(ref.promptNotes)}</p>` : ''}
      <div class="row visual-reference-actions">${ref.mediaType === 'image' && !ref.isPrimary ? button('设为主参考', `primary-visual-reference:${ref.id}`, true) : ''}${button('删除', `delete-visual-reference:${ref.id}`, true)}</div>
      <details class="visual-reference-details"><summary>整理</summary><form class="form compact" data-form="visual-reference-meta" data-persona="${esc(persona.id)}" data-id="${esc(ref.id)}">
        <div class="form-grid">${selectField('分类', 'category', ref.category, Object.entries(categoryLabels).map(([key, label]) => [key, label]))}${field('名称', 'label', ref.label || '')}${field('排序', 'sortOrder', ref.sortOrder ?? 0, 'number', 'min="0" max="100000"')}</div>
        ${textarea('生成备注', 'promptNotes', ref.promptNotes || '', 'rows="3"')}${checkbox('启用这份参考', 'enabled', ref.enabled !== false)}${formActions('保存整理')}
      </form></details></div></article>`;
  }).join('');
  return card('形象资料库', `<div class="visual-reference-layout"><form class="visual-reference-upload" data-form="visual-reference-upload" data-persona="${esc(persona.id)}">
    <div class="visual-reference-drop"><strong>上传参考图或视频</strong><span>图片 12 MB · 视频 64 MB</span><label class="button secondary file-button">选择附件<input type="file" name="file" accept="image/png,image/jpeg,image/webp,video/mp4,video/webm" required></label></div>
    <div class="form-grid">${selectField('分类', 'category', 'identity', Object.entries(categoryLabels).map(([key, label]) => [key, label]))}${field('名称', 'label', '', 'text', 'placeholder="例如：素颜近景"')}${field('排序', 'sortOrder', 0, 'number', 'min="0" max="100000"')}</div>
    ${textarea('给生图/视频的备注', 'promptNotes', '', 'rows="3" placeholder="只写稳定外观、妆容、表情或动态特征"')}
    ${checkbox('设为主参考（仅图片）', 'isPrimary', false)}${formActions('加入资料库')}
  </form><div class="visual-reference-grid">${cards || empty('还没有参考素材。先放一张稳定脸部参考，再补妆容、穿搭和动态。')}</div></div>`);
}
function visualReferencePackagePanel(persona) {
  if (!persona?.id) return '';
  return `<div class="visual-reference-package-actions"><div><strong>形象资料包</strong><span class="muted">可导出全部图片、视频和标注，换角色后继续编辑。</span></div><div class="row"><button class="button secondary" type="button" data-action="export-visual-references:${esc(persona.id)}">导出资料包</button><form class="inline-form" data-form="visual-reference-package-import" data-persona="${esc(persona.id)}"><label class="button secondary file-button">选择资料包<input type="file" name="package" accept=".zip,application/zip" required></label><button class="button" type="submit">导入资料包</button></form></div></div>`;
}
function personaBindingForm(binding = {}, people = []) {
  const options = people.map((persona) => `<option value="${esc(persona.id)}" ${persona.id === binding.personaId ? 'selected' : ''}>${esc(persona.name)}</option>`).join('');
  return `<form class="form compact" data-form="persona-binding" data-id="${esc(binding.id || '')}">
    <div class="form-grid"><label class="field"><span>角色</span><select name="personaId" required>${options}</select></label>${field('平台类型', 'transport', binding.transport || '*', 'text', 'required')}${field('会话标识', 'conversationRef', binding.conversationRef || '*', 'text', 'required')}${field('优先级', 'priority', binding.priority ?? 100, 'number', 'min="-10000" max="10000" required')}</div>
    ${checkbox('启用绑定', 'enabled', binding.enabled !== false)}${formActions(binding.id ? '保存绑定' : '新增绑定', 'cancel-edit')}
  </form>`;
}
const _personaBindingForm = personaBindingForm;
personaBindingForm = (binding = {}, people = []) => {
  const instance = field('连接器实例', 'transportInstance', binding.transportInstance || '*', 'text', 'required placeholder="* 表示全部实例"');
  return _personaBindingForm(binding, people).replace('</div>', `${instance}</div>`);
};
function personaRuntimeProfileForm(profile = {}, personaId = '') {
  const allowed = listValue(profile.allowedToolIds);
  const denied = listValue(profile.deniedToolIds);
  return `<form class="form compact" data-form="persona-runtime-profile" data-id="${esc(personaId)}">
    <div class="form-grid">${field('聊天端点', 'chatEndpointId', profile.chatEndpointId || '')}${field('任务端点', 'taskEndpointId', profile.taskEndpointId || '')}${field('判断端点', 'decisionEndpointId', profile.decisionEndpointId || '')}${field('最大字数', 'maxReplyChars', profile.maxReplyChars ?? '', 'number', 'min="20" max="1000"')}${field('最多句数', 'maxReplySentences', profile.maxReplySentences ?? '', 'number', 'min="1" max="6"')}</div>
	${selectField('参与模式（唯一主开关）', 'participationMode', profile.participationMode || '', [['', '继承公共策略'], ['addressed_only', '仅被叫到（@/回复/命令）'], ['adaptive', '自适应插话'], ['social', '社交陪伴']])}
    ${field('允许工具 ID', 'allowedToolIds', allowed)}${field('禁用工具 ID', 'deniedToolIds', denied)}
    ${selectField('联网触发', 'searchMode', profile.searchMode || '', [['', '继承全局'], ['adaptive', '自适应判断'], ['explicit_only', '仅明确要求时搜索']])}${selectField('联网表达', 'searchReplyStyle', profile.searchReplyStyle || '', [['', '继承角色'], ['natural', '真人口吻归纳'], ['concise', '极简结论'], ['source_first', '先结论后来源']])}
    ${textarea('表达规则', 'expressionPrompt', profile.expressionPrompt || '', 'rows="3"')}${textarea('视觉覆盖', 'visualPromptOverride', profile.visualPromptOverride || '', 'rows="3"')}${selectField('记忆策略', 'memoryPolicy', profile.memoryPolicy || '', [['', '继承全局'], ['isolated', '角色隔离'], ['shared', '共享公共记忆'], ['disabled', '关闭长期记忆']])}
    ${formActions('保存运行档案', 'cancel-edit')}
  </form>`;
}
async function rolesView() {
  if (!state.personas || !state.personaBindings) [state.personas, state.personaBindings] = await Promise.all([
    state.personas || api('/api/v1/personas?namespace=default&limit=100'),
    state.personaBindings || api('/api/v1/persona-bindings'),
  ]);
  if (!state.personaProfiles) state.personaProfiles = await api('/api/v1/personas/runtime-profiles');
  if (state.editingPersona?.id && state.personaEditorData?.personaId !== state.editingPersona.id) {
    const personaId = encodeURIComponent(state.editingPersona.id);
    const [references, samples, traits, worldbook] = await Promise.all([
      api('/api/v1/personas/' + personaId + '/visual-references?namespace=default'),
      api('/api/v1/personas/' + personaId + '/samples?namespace=default&limit=100'),
      api('/api/v1/personas/' + personaId + '/traits?namespace=default&limit=100'),
      api('/api/v1/personas/' + personaId + '/worldbook?namespace=default&limit=100'),
    ]);
    references.personaId = state.editingPersona.id;
    state.personaVisualReferences = references;
    state.personaEditorData = { personaId: state.editingPersona.id, references, samples, traits, worldbook };
  }
  const people = state.personas.items || [];
  const activePersona = people.find((persona) => persona.id === state.config?.activePersonaId);
  const bindings = state.personaBindings.items || [];
  const enabledBindings = bindings.filter((binding) => binding.enabled !== false).length;
  const section = pageSection('roles', 'cards');
  const roleSummary = `<div class="role-summary" aria-label="智能体状态">
    <div class="role-stat"><strong>${people.length}</strong><span>角色卡</span></div>
    <div class="role-stat"><strong>${enabledBindings}</strong><span>启用绑定</span></div>
    <div class="role-current"><span>当前默认</span><strong>${esc(activePersona?.name || '未设置')}</strong></div>
  </div>`;
  const editingProfile = (state.personaProfiles.items || []).find((item) => item.personaId === state.editingPersona?.id);
  const editor = state.editingPersona ? controlDialog(state.editingPersona.id ? '角色配置 · 编辑智能体' : '角色配置 · 新建智能体', personaDossier(state.editingPersona, editingProfile, state.personaEditorData || {}) + personaForm(state.editingPersona) + visualReferencePanel(state.editingPersona) + visualReferencePackagePanel(state.editingPersona), '人物、形象、世界书和样本属于角色配置层；公共安全与实例权限不会写入角色卡。') : '';
  const bindingEditor = state.editingPersonaBinding ? controlDialog(state.editingPersonaBinding.id ? '编辑角色绑定' : '新增角色绑定', personaBindingForm(state.editingPersonaBinding, people), '绑定决定某个平台或会话使用哪张角色卡。') : '';
  const profileEditor = state.editingPersonaProfile ? controlDialog('角色策略 · 运行档案', personaRuntimeProfileForm(state.editingPersonaProfile.profile || state.editingPersonaProfile, state.editingPersonaProfile.personaId || ''), '这里只覆盖当前角色的模型、工具、主动参与和记忆策略；未填写字段继续继承公共策略。') : '';
  const emptyState = `${empty('还没有智能体。先创建一个，再设为当前智能体。')}<div class="empty-action">${button('新建智能体', 'new-persona')}</div>`;
  const rows = people.map((persona) => {
    const active = persona.id === state.config.activePersonaId;
    const profileItem = (state.personaProfiles.items || []).find((item) => item.personaId === persona.id);
    const profile = profileItem?.profile || {};
    const profileSummary = [profile.chatEndpointId ? `聊天 ${profile.chatEndpointId}` : '聊天继承全局', profile.participationMode || (profile.proactiveEnabled === false ? '仅被叫到' : '继承参与模式')].join(' · ');
    return `<article class="persona-tile ${active ? 'active' : ''}">
      <div class="persona-card-top"><span class="persona-role">ROLE / ${esc(persona.id)}</span>${active ? '<span class="pill">当前默认</span>' : '<span class="readonly">未启用</span>'}</div>
      <button class="persona-open" type="button" data-action="edit-persona:${esc(persona.id)}">
        ${avatarMarkup(persona)}
        <span class="persona-copy"><span class="persona-name">${esc(persona.name)}</span><span class="persona-description">${esc(persona.description || '还没有简介')}</span></span>
      </button>
      <div class="persona-meta"><span>${esc(listValue(persona.tags) || '无标签')}</span><span>${esc(persona.sourceFormat || 'native')}</span><span>${esc(profileSummary)}</span></div>
      <div class="row persona-actions">${active ? '' : button('启用', `activate:${persona.id}`)}${button('运行档案', `edit-persona-profile:${persona.id}`, true)}${button('记忆', `persona-memories:${persona.id}`, true)}${button('编辑', `edit-persona:${persona.id}`, true)}${button('复制', `copy-persona:${persona.id}`, true)}${button('导出', `export-persona:${persona.id}`, true)}${button('V2', `export-v2:${persona.id}`, true)}${button('删除', `delete-persona:${persona.id}`, true)}</div>
    </article>`;
  }).join('');
  const bindingRows = bindings.map((binding) => {
    const persona = people.find((item) => item.id === binding.personaId);
    return `<div class="list-item"><div><strong>${esc(persona?.name || binding.personaId)} ${binding.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>${esc(binding.transport)} / 实例 ${esc(binding.transportInstance || '*')} / ${esc(binding.conversationRef)} · 优先级 ${esc(binding.priority)}</small></div><div class="row">${button('编辑', `edit-persona-binding:${binding.id}`, true)}${button(binding.enabled ? '停用' : '启用', `toggle-persona-binding:${binding.id}`, true)}${button('删除', `delete-persona-binding:${binding.id}`, true)}</div></div>`;
  }).join('');
  const actions = `${button('导入角色卡', 'import-persona', true)}${button('新建智能体', 'new-persona')}<input class="visually-hidden" id="persona-import" type="file" accept=".json,application/json">`;
  const tabs = pageTabs('roles', section, [['cards', '角色卡', people.length], ['bindings', '会话绑定', bindings.length]]);
  const body = section === 'cards' ? `<div class="persona-grid">${rows || emptyState}</div>` : card('会话绑定', bindingRows || empty('暂无会话绑定。'), button('新增绑定', 'new-persona-binding'));
  return shell('智能体', '角色卡是可切换的运行单元。', roleSummary + tabs + `<p class="muted">角色配置管人物、外形、世界书和样本；角色策略管模型、联网和表达。公共边界与实例权限不会被角色卡改写。</p>` + body + editor + profileEditor + bindingEditor, actions);
}
function memoryForm(memory = {}, personaId) {
  const kind = memory.scopeKind || 'user';
  const memoryKinds = [['fact', '事实'], ['preference', '偏好'], ['habit', '习惯'], ['project', '长期项目'], ['address', '称呼'], ['identity', '身份偏好'], ['experience', '共同经历'], ['boundary', '边界']];
  if (memory.kind && !memoryKinds.some(([value]) => value === memory.kind)) memoryKinds.push([memory.kind, memory.kind]);
  return `<form class="form compact" data-form="persona-memory" data-id="${esc(memory.id || '')}">
    <input type="hidden" name="personaId" value="${esc(personaId)}">
    <div class="form-grid">${selectField('作用域', 'scopeKind', kind, [['user', '成员'], ['group', '群聊']])}${field('成员或会话引用', 'scopeReference', memory.scopeReference || '', 'text', 'required maxlength="240"')}${selectField('记忆桶', 'kind', memory.kind || 'fact', memoryKinds)}${field('可信度', 'confidence', memory.confidence ?? 1, 'number', 'min="0" max="1" step="0.05"')}${field('重要度', 'importance', memory.importance ?? 0.7, 'number', 'min="0" max="1" step="0.05"')}${field('过期时间（RFC3339，可空）', 'expiresAt', memory.expiresAt || '')}</div>
    ${textarea('记忆内容', 'content', memory.content || '', 'rows="4" required maxlength="500"')}
    ${formActions(memory.id ? '保存纠正' : '新增记忆', 'cancel-edit')}
  </form>`;
}
function relationshipForm(relationship) {
  const current = Math.round(relationship.state?.intimacy ?? relationship.state?.autoIntimacy ?? 0);
  return `<form class="form compact" data-form="relationship" data-id="${esc(relationship.id)}">
    <div class="form-grid">${field('亲密度', 'intimacy', current, 'range', 'min="0" max="100" step="1"')}${field('当前数值', 'intimacyValue', current, 'number', 'min="0" max="100" step="1"')}</div>
    <div class="toggle-row">${checkbox('锁定人工亲密度', 'locked', relationship.state?.intimacyLocked === true)}</div>
    <p class="muted">解锁后会根据直接互动、共同经历和时间自然变化。</p>
    ${formActions('保存关系', 'cancel-edit')}
  </form>`;
}

function memoryKernelMap() {
  const items = [
    ['reply', '输出回流', '终态回复 ACK 后才计入'],
    ['resonance', '记忆共振', '召回次数与重要度共同决定'],
    ['routine', '作息预期', '从真实互动节律学习'],
    ['longing', '挂念机制', '过了常见间隔才自然升起'],
    ['dream', '梦境隔离', '梦境、假设不沉淀为事实'],
    ['bucket', '桶分类清理', '按类型、可信度和重要度整理'],
  ];
  return `<div class="memory-kernel-map" aria-label="通用记忆内核">${items.map(([icon, title, text], index) => `<div class="memory-kernel-node" data-pulse-icon="${icon}"><span>${String(index + 1).padStart(2, '0')}</span><i aria-hidden="true"></i><strong>${esc(title)}</strong><small>${esc(text)}</small></div>`).join('')}</div>`;
}

function relationshipPulseView(pulse = {}) {
  const metric = (label, value, note) => {
    const score = Math.max(0, Math.min(100, Number(value) || 0));
    return `<div class="relationship-pulse-metric"><div><span>${esc(label)}</span><strong>${Math.round(score)}</strong></div><div class="relationship-pulse-track"><i style="width:${score}%"></i></div><small>${esc(note)}</small></div>`;
  };
  if (!pulse.ready) return `<div class="relationship-pulse-wait"><strong>关系还在成形</strong><small>${esc(pulse.evidence || '互动证据不足，暂不推断挂念与作息。')}</small></div>`;
  const hour = Number(pulse.preferredHour);
  const rhythmNote = hour >= 0 ? `常见时段约 ${String(hour).padStart(2, '0')}:00` : '尚未形成稳定时段';
  return `<div class="relationship-pulse-grid">
    ${metric('输出回流', pulse.outputReflow, `已完成 ${pulse.replyCount || 0} 次终态回复`)}
    ${metric('记忆共振', pulse.memoryResonance, `${pulse.memoryCount || 0} 条记忆，${pulse.memoryKinds || 0} 类`)}
    ${metric('作息预期', pulse.routineExpectation, rhythmNote)}
    ${metric('自然挂念', pulse.longing, `常见间隔约 ${Math.round(pulse.typicalGapHours || 0)} 小时`)}
    ${metric('分享意愿', pulse.sharing, '只影响主动分享，不制造话题')}
    ${metric('分类健康', pulse.bucketHealth, '可信度、重要度与分类覆盖')}
    <p>${esc(pulse.evidence || '')}</p>
  </div>`;
}

async function memoriesView() {
  if (!state.personas) state.personas = await api('/api/v1/personas?namespace=default&limit=100');
  const people = state.personas.items || [];
  const selected = people.find((persona) => persona.id === state.memoryPersonaId)
    || people.find((persona) => persona.id === state.config.activePersonaId)
    || people[0];
  if (!selected) return shell('角色记忆', '请先创建角色卡。', empty('暂无可管理的角色。'), button('前往智能体', 'go-roles'));
  state.memoryPersonaId = selected.id;
  if (!state.memories || state.memories.personaId !== selected.id || state.memories.scopeKind !== state.memoryScopeKind) {
    const query = new URLSearchParams({ personaId: selected.id, limit: '100' });
    if (state.memoryScopeKind) query.set('scopeKind', state.memoryScopeKind);
    state.memories = await api(`/api/v1/memories?${query}`);
    state.memories.personaId = selected.id;
    state.memories.scopeKind = state.memoryScopeKind;
  }
	if (!state.relationships || state.relationships.personaId !== selected.id) {
		state.relationships = await api(`/api/v1/relationships?personaId=${encodeURIComponent(selected.id)}&limit=100`);
		state.relationships.personaId = selected.id;
	}
  const section = pageSection('memories', 'relationships');
  const personaSelector = `<div class="row"><label class="field compact-select"><span>角色卡</span><select id="memory-persona">${people.map((persona) => `<option value="${esc(persona.id)}" ${persona.id === selected.id ? 'selected' : ''}>${esc(persona.name)}</option>`).join('')}</select></label><label class="field compact-select"><span>作用域</span><select id="memory-scope-filter"><option value="" ${state.memoryScopeKind === '' ? 'selected' : ''}>全部</option><option value="user" ${state.memoryScopeKind === 'user' ? 'selected' : ''}>成员</option><option value="group" ${state.memoryScopeKind === 'group' ? 'selected' : ''}>群聊</option></select></label></div>`;
  const editor = state.editingMemory ? controlDialog(state.editingMemory.id ? '纠正记忆' : '新增记忆', memoryForm(state.editingMemory, selected.id), '事实记忆按角色和成员或群聊范围隔离。') : '';
	const rows = (state.memories.items || []).map((memory) => `<div class="list-item memory-record"><div><strong>${esc(memory.content)}</strong><small><span class="memory-kind">${esc(memory.kind)}</span>${memory.scopeKind === 'group' ? '群聊' : '成员'} · ${esc(memory.scopeReference)} · 重要度 ${esc(memory.importance)} · 可信度 ${esc(memory.confidence)}</small><small>召回 ${esc(memory.accessCount || 0)} 次 · ${esc(memory.source)} · 更新于 ${esc(memory.updatedAt)}</small></div><div class="row">${button('纠正', `edit-memory:${memory.id}`, true)}${button('删除', `delete-memory:${memory.id}`, true)}</div></div>`).join('');
	const relationshipEditor = state.editingRelationship ? controlDialog(`调整 ${state.editingRelationship.senderDisplayName || '群友'}`, relationshipForm(state.editingRelationship), '亲密度默认随真实互动自动变化，也可以人工锁定。') : '';
	const relationshipRows = (state.relationships.items || []).map((relationship) => {
		const value = Math.round(relationship.state?.intimacy || 0);
		const automatic = Math.round(relationship.state?.autoIntimacy || 0);
		const pulse = { ...(relationship.state?.pulse || {}), replyCount: relationship.state?.replyCount || 0 };
		return `<div class="relationship-item"><div class="relationship-main"><div><strong>${esc(relationship.senderDisplayName || '未命名成员')}</strong><small>${esc(relationship.state?.stage || '新成员')} · ${relationship.state?.intimacyLocked ? `人工锁定，自动值 ${automatic}` : '自动演化'} · 最近互动 ${esc(relationship.state?.lastInteraction || '')}</small></div><div class="relationship-score"><strong>${value}</strong><span>/ 100</span></div></div><div class="intimacy-meter" aria-label="亲密度 ${value}"><span style="width:${Math.max(0, Math.min(100, value))}%"></span></div><details class="relationship-pulse"><summary>记忆脉动 <span>${pulse.ready ? '已形成' : '学习中'}</span></summary>${relationshipPulseView(pulse)}</details><div class="relationship-foot"><small>互动 ${esc(relationship.state?.interactionCount || 0)} 次 · 直接点名 ${esc(relationship.state?.addressedCount || 0)} 次 · 完整回复 ${esc(relationship.state?.replyCount || 0)} 次</small><div class="row">${button('调整', `edit-relationship:${relationship.id}`, true)}${relationship.state?.intimacyLocked ? button('恢复自动', `auto-relationship:${relationship.id}`, true) : ''}${button('清除', `delete-relationship:${relationship.id}`, true)}</div></div></div>`;
	}).join('');
  const tabs = pageTabs('memories', section, [['relationships', '关系与亲密度', (state.relationships.items || []).length], ['facts', '事实记忆', (state.memories.items || []).length]]);
  const body = section === 'relationships'
    ? card('关系档案', `<div class="relationship-list">${relationshipRows || empty('关系会在真实互动后自动建立。')}</div>`)
    : card('事实记忆', rows || empty('这个角色还没有可管理的记忆。'), button('新增记忆', 'new-memory'));
  return shell('记忆与关系', '通用内核负责记住与演化，角色卡只决定如何表达。', memoryKernelMap() + tabs + body + relationshipEditor + editor, personaSelector);
}
function personaDirectiveForm(d = {}) { return `<form class="form inline-editor" data-form="directive" data-id="${esc(d.id || '')}">${textarea('指令内容', 'content', d.content || '', 'rows="4"')}${checkbox('启用', 'enabled', d.enabled !== false)}${formActions(d.id ? '保存指令' : '新增指令', d.id ? 'cancel-edit' : '')}</form>`; }

async function worldbookView() {
  if (!state.personas) state.personas = await api('/api/v1/personas?namespace=default&limit=100');
  const selected = state.persona || (state.personas.items || []).find((p) => p.id === state.config.activePersonaId) || state.personas.items?.[0];
  if (!selected) return shell('世界书', '请先创建角色卡。', empty('暂无可编辑的角色。'), button('前往角色卡', 'go-roles'));
  if (!state.worldbook || state.worldbook.personaId !== selected.id) { state.worldbook = await api(`/api/v1/personas/${encodeURIComponent(selected.id)}/worldbook?namespace=default&limit=100`); state.worldbook.personaId = selected.id; }
  const editor = state.editingWorldbook ? controlDialog(state.editingWorldbook.id ? '编辑世界书条目' : '新增世界书条目', worldbookForm(state.editingWorldbook, selected.id), '关键词、优先级和插入位置会进入实时上下文编译。') : '';
  const personaSelector = `<label class="field compact-select"><span>配置角色</span><select id="worldbook-persona">${(state.personas.items || []).map((p) => `<option value="${esc(p.id)}" ${p.id === selected.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}</select></label>`;
  return shell('世界书', `当前角色：${esc(selected.name)}`, card('条目', (state.worldbook.items || []).map((w) => `<div class="list-item"><div><strong>${esc(w.comment || w.id)} ${w.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>关键词：${esc(listValue(w.keys) || '无')} · 优先级：${esc(w.priority)} · 位置：${esc(w.position)}</small><small>${esc(w.content)}</small></div><div class="row">${button('编辑', `edit-worldbook:${w.id}`, true)}${button(w.enabled ? '停用' : '启用', `toggle-worldbook:${w.id}`, true)}${button('删除', `delete-worldbook:${w.id}`, true)}</div></div>`).join('') || empty('暂无世界书条目。'), button('新增条目', 'new-worldbook')) + editor, personaSelector);
}
function worldbookForm(w = {}, personaId) { return `<form class="form" data-form="worldbook" data-id="${esc(w.id || '')}" data-persona="${esc(personaId)}">${field('备注', 'comment', w.comment || '')}${field('关键词（逗号分隔）', 'keys', listValue(w.keys))}${field('次级关键词（逗号分隔）', 'secondaryKeys', listValue(w.secondaryKeys))}<div class="form-grid">${field('优先级', 'priority', w.priority || 0, 'number')}${field('插入顺序', 'insertionOrder', w.insertionOrder || 0, 'number')}${field('Token 预算', 'tokenBudget', w.tokenBudget ?? '', 'number')}<label class="field"><span>插入位置</span><select name="position"><option value="before_char" ${w.position === 'before_char' || !w.position ? 'selected' : ''}>角色前</option><option value="after_char" ${w.position === 'after_char' ? 'selected' : ''}>角色后</option><option value="before_example" ${w.position === 'before_example' ? 'selected' : ''}>示例前</option><option value="after_example" ${w.position === 'after_example' ? 'selected' : ''}>示例后</option></select></label></div>${textarea('内容', 'content', w.content || '', 'rows="8"')}${checkbox('启用', 'enabled', w.enabled !== false)}${checkbox('常量条目', 'constant', Boolean(w.constant))}${checkbox('选择性触发', 'selective', Boolean(w.selective))}${formActions(w.id ? '保存条目' : '新增条目', w.id ? 'cancel-edit' : '')}</form>`; }

function personaSampleForm(sample = {}, personaId) {
  return `<form class="form" data-form="persona-sample" data-id="${esc(sample.id || '')}" data-persona="${esc(personaId)}">
    <div class="form-grid">${field('场景标签（逗号分隔）', 'sceneTags', listValue(sample.sceneTags), 'text', 'required')}${field('关系阶段', 'relationshipStage', sample.relationshipStage || '')}${field('情绪', 'emotion', sample.emotion || '')}${field('权重', 'weight', sample.weight ?? 1, 'number', 'min="0" max="100" step="0.1" required')}</div>
    ${textarea('上下文', 'context', sample.context || '', 'rows="5" required')}
    ${textarea('候选短回复（每行一条）', 'candidateReplies', lineValue(sample.candidateReplies), 'rows="6" required')}
    ${textarea('禁用表达（每行一条）', 'forbiddenExpressions', lineValue(sample.forbiddenExpressions), 'rows="5"')}
    ${field('来源与许可证备注', 'source', sample.source || '', 'text', 'required')}
    ${checkbox('启用', 'enabled', sample.enabled !== false)}
    ${formActions(sample.id ? '保存样本' : '新增样本', sample.id ? 'cancel-edit' : '')}
  </form>`;
}

function personaTraitForm(trait = {}, personaId) {
  return `<form class="form" data-form="persona-trait" data-id="${esc(trait.id || '')}" data-persona="${esc(personaId)}">
    <div class="form-grid">${field('特质名称', 'name', trait.name || '', 'text', 'required')}${field('触发条件（逗号分隔，* 为常驻）', 'triggers', listValue(trait.triggers))}${field('权重', 'weight', trait.weight ?? 1, 'number', 'min="0" max="100" step="0.1" required')}${field('来源与方法备注', 'source', trait.source || '', 'text', 'required')}</div>
    ${textarea('行为描述', 'description', trait.description || '', 'rows="5" required')}
    <div class="form-grid">${field('支持节点（ID 或名称）', 'supports', listValue(trait.supports))}${field('冲突节点（ID 或名称）', 'conflicts', listValue(trait.conflicts))}</div>
    ${checkbox('启用', 'enabled', trait.enabled !== false)}
    ${formActions(trait.id ? '保存特质' : '新增特质', trait.id ? 'cancel-edit' : '')}
  </form>`;
}

async function personaSamplesView() {
  if (!state.personas) state.personas = await api('/api/v1/personas?namespace=default&limit=100');
  const selected = state.persona || (state.personas.items || []).find((p) => p.id === state.config.activePersonaId) || state.personas.items?.[0];
  if (!selected) return shell('人格样本库', '请先创建智能体。', empty('暂无可配置角色。'), button('前往智能体', 'go-roles'));
  if (!state.personaSamples || state.personaSamples.personaId !== selected.id || !state.personaTraits || state.personaTraits.personaId !== selected.id) {
    [state.personaSamples, state.personaTraits] = await Promise.all([
      api(`/api/v1/personas/${encodeURIComponent(selected.id)}/samples?namespace=default&limit=100`),
      api(`/api/v1/personas/${encodeURIComponent(selected.id)}/traits?namespace=default&limit=100`),
    ]);
    state.personaSamples.personaId = selected.id;
    state.personaTraits.personaId = selected.id;
  }
  const section = pageSection('samples', 'traits');
  const sampleEditor = state.editingPersonaSample ? controlDialog(state.editingPersonaSample.id ? '编辑人格样本' : '新增人格样本', personaSampleForm(state.editingPersonaSample, selected.id), '场景样本提供节奏，不会直接复读候选答案。') : '';
  const traitEditor = state.editingPersonaTrait ? controlDialog(state.editingPersonaTrait.id ? '编辑人格特质' : '新增人格特质', personaTraitForm(state.editingPersonaTrait, selected.id), '人格图谱用于组合本轮稳定特质。') : '';
  const selector = `<label class="field compact-select"><span>配置角色</span><select id="sample-persona">${(state.personas.items || []).map((p) => `<option value="${esc(p.id)}" ${p.id === selected.id ? 'selected' : ''}>${esc(p.name)}</option>`).join('')}</select></label>`;
  const rows = (state.personaSamples.items || []).map((sample) => `<div class="list-item"><div><strong>${esc(listValue(sample.sceneTags) || sample.id)} ${sample.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>关系：${esc(sample.relationshipStage || '不限')} · 情绪：${esc(sample.emotion || '不限')} · 权重：${esc(sample.weight)}</small><small>${esc(sample.context)}</small><small>候选：${esc(listValue(sample.candidateReplies))}</small><small>来源：${esc(sample.source)}</small></div><div class="row">${button('编辑', `edit-persona-sample:${sample.id}`, true)}${button(sample.enabled ? '停用' : '启用', `toggle-persona-sample:${sample.id}`, true)}${button('删除', `delete-persona-sample:${sample.id}`, true)}</div></div>`).join('');
  const traitRows = (state.personaTraits.items || []).map((trait) => `<div class="list-item"><div><strong>${esc(trait.name)} ${trait.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>触发：${esc(listValue(trait.triggers) || '无')} · 权重：${esc(trait.weight)}</small><small>${esc(trait.description)}</small><small>支持：${esc(listValue(trait.supports) || '无')} · 冲突：${esc(listValue(trait.conflicts) || '无')}</small></div><div class="row">${button('编辑', `edit-persona-trait:${trait.id}`, true)}${button(trait.enabled ? '停用' : '启用', `toggle-persona-trait:${trait.id}`, true)}${button('删除', `delete-persona-trait:${trait.id}`, true)}</div></div>`).join('');
  const tabs = pageTabs('samples', section, [['traits', '人格图谱', (state.personaTraits.items || []).length], ['samples', '场景样本', (state.personaSamples.items || []).length]]);
  const body = section === 'traits' ? card('人格图谱', traitRows || empty('暂无人格特质。'), button('新增特质', 'new-persona-trait')) : card('场景样本', rows || empty('暂无人格样本。'), button('新增样本', 'new-persona-sample'));
  return shell('人格内核', `当前角色：${esc(selected.name)}`, tabs + body + traitEditor + sampleEditor, selector);
}

async function knowledgeView() {
  if (!state.documents || !state.candidates || !state.integrations || !state.knowledgeBases) {
    [state.documents, state.candidates, state.integrations, state.knowledgeBases] = await Promise.all([
      state.documents || api(`/api/v1/knowledge/documents?namespace=${encodeURIComponent(state.config.knowledgeNamespace || 'default')}&limit=100`),
      state.candidates || api('/api/v1/runtime/knowledge-candidates?limit=100'),
      state.integrations || api('/api/v1/integrations'),
      state.knowledgeBases || api('/api/v1/knowledge/bases'),
    ]);
  }
  const docs = state.documents.items || [], candidates = state.candidates.items || [];
  const bases = state.knowledgeBases.items || [];
  const policies = integrationMap(state.integrations);
  const retrievalPolicy = policies.retrieval_policy?.config || {};
  const documentPolicy = policies.document_policy?.config || {};
  const section = pageSection('knowledge', 'retrieval');
  const editor = state.editingDocument ? controlDialog(state.editingDocument.id ? '编辑知识文档' : '新增知识文档', documentForm(state.editingDocument), '保存后进入分块、Embedding、混合召回和重排链路。') : '';
  const tabs = pageTabs('knowledge', section, [['retrieval', '检索与向量'], ['reading', '文档读取'], ['documents', '正式知识', docs.length], ['candidates', '候选审核', candidates.length]]);
  let body = retrievalPolicyCard(retrievalPolicy) + card('学习策略', `<form class="form" data-form="learning"><div class="toggle-row">${checkbox('启用自动采集', 'enabled', state.config.learningEnabled)}</div>${field('主题（逗号分隔）', 'topics', listValue(state.config.learningTopics))}${field('周期（小时）', 'interval', state.config.learningIntervalHours || 24, 'number', 'min="6" max="168"')}<p class="muted">${state.config.lastCollectedAt ? `上次采集：${esc(state.config.lastCollectedAt)}` : '尚未完成自动采集。'} 候选内容需审核后入库。</p>${formActions('保存学习策略')}</form>`);
  const layerLabel = { global: '大知识库', domain: '小知识库', exclusive: '专属知识库' };
  const baseRows = bases.map((base) => `<div class="list-item knowledge-base-row"><div><strong>${esc(base.name)} ${base.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>${esc(layerLabel[base.layer] || base.layer)} · ${esc(base.namespace)} · ${esc(base.documentCount)} 份文档</small><small>${esc(base.description || (base.autoInclude ? '自动参与相关召回' : '仅在命中归属时召回'))}</small></div><div class="row">${base.layer !== 'global' ? button(base.enabled ? '停用' : '启用', `toggle-knowledge-base:${base.id}`, true) : '<span class="readonly">公共默认</span>'}</div></div>`).join('');
  if (section === 'retrieval') body = card('知识库分层', `<p class="muted">公共大库统一复用；小库按场景启用；专属库只随当前角色或运行实例召回。</p>${baseRows || empty('暂无知识库目录。')}`) + body;
  if (section === 'retrieval') body += `<div class="empty-action">${button('新增知识库', 'new-knowledge-base')}</div>`;
  if (section === 'reading') body = documentPolicyCard(documentPolicy);
  if (state.editingKnowledgeBase && section === 'retrieval') body += controlDialog('知识库设置', knowledgeBaseForm(state.editingKnowledgeBase), '知识库只管理归属和召回策略，不复制文档。');
  if (section === 'documents') body = card('正式知识文档', docs.map((d) => `<div class="list-item"><div><strong>${esc(d.title)}</strong><small>命名空间：${esc(d.namespace)} · 来源：${esc(d.sourceUri || '手工录入')}</small><small>内容哈希：${esc(d.contentHash || '未计算')}</small></div><div class="row">${button('编辑', `edit-document:${d.id}`, true)}${button('删除', `delete-document:${d.id}`, true)}</div></div>`).join('') || empty('暂无正式知识文档。'), button('新增知识文档', 'new-document'));
  if (section === 'candidates') body = card('候选知识审核', candidates.map((c) => `<div class="list-item"><div><strong>${esc(c.title)} · ${esc(c.status)}</strong><small>${esc(c.content)}</small></div><div class="row">${c.status === 'pending' ? `${button('批准', `approve:${c.id}`)}${button('拒绝', `reject:${c.id}`, true)}` : ''}${button('删除', `delete-candidate:${c.id}`, true)}</div></div>`).join('') || empty('暂无候选知识。'));
  return shell('知识与学习', '检索、阅读、正式知识和候选审核分开管理。', tabs + body + editor);
}
function documentForm(d = {}) { return `<form class="form" data-form="document" data-id="${esc(d.id || '')}">${field('标题', 'title', d.title || '')}${field('来源 URI', 'sourceUri', d.sourceUri || '')}${textarea('内容', 'content', d.content || '', 'rows="14"')}${textarea('元数据 JSON', 'metadata', JSON.stringify(d.metadata || {}, null, 2), 'rows="5"')}${formActions(d.id ? '保存文档' : '新增文档', d.id ? 'cancel-edit' : '')}</form>`; }

function knowledgeBaseForm(base = {}) { const layer = base.layer || 'domain'; return `<form class="form" data-form="knowledge-base" data-id="${esc(base.id || '')}">${field('唯一 ID', 'id', base.id || '', 'text', base.id ? 'readonly' : 'required')}${field('名称', 'name', base.name || '', 'text', 'required')}${selectField('层级', 'layer', layer, [['global', '大知识库'], ['domain', '小知识库'], ['exclusive', '专属知识库']])}${field('命名空间', 'namespace', base.namespace || '', 'text', 'required')}${field('归属类型', 'ownerKind', base.ownerKind || '', 'text', 'placeholder="persona 或 instance"')}${field('归属 ID', 'ownerId', base.ownerId || '')}${field('优先级', 'priority', base.priority ?? 0, 'number')}${textarea('说明', 'description', base.description || '', 'rows="3"')}${checkbox('启用并自动参与召回', 'enabled', base.enabled !== false)}${checkbox('作为小库默认召回', 'autoInclude', base.autoInclude === true)}${formActions(base.id ? '保存知识库' : '创建知识库', 'cancel-edit')}</form>`; }
function authorityFields(authorities = []) {
  return `<div class="toggle-row">${checkbox('普通成员', 'authorityMember', authorities.includes('member'))}${checkbox('管理员', 'authorityAdmin', authorities.includes('admin'))}</div>`;
}
function approvalSelect(value = 'admin_only') {
  return `<label class="field"><span>审批方式</span><select name="approvalMode"><option value="auto" ${value === 'auto' ? 'selected' : ''}>自动执行</option><option value="confirm" ${value === 'confirm' ? 'selected' : ''}>执行前确认</option><option value="admin_only" ${value === 'admin_only' ? 'selected' : ''}>仅管理员</option></select></label>`;
}
function toolForm(tool = {}) {
  const authorities = tool.allowedAuthorities || (Number(tool.riskLevel || 0) >= 2 ? ['admin'] : ['member', 'admin']);
  return `<form class="form" data-form="tool" data-id="${esc(tool.id || '')}">
    <div class="form-grid">${field('唯一 ID', 'id', tool.id || '', 'text', tool.id ? 'readonly' : 'required')}${field('名称', 'name', tool.name || '', 'text', 'required')}${field('适配器引用', 'adapterRef', tool.adapterRef || '', 'text', 'required')}${field('超时（秒）', 'timeoutSeconds', tool.timeoutSeconds ?? 30, 'number', 'min="1" max="300" required')}<label class="field"><span>风险等级</span><select name="riskLevel"><option value="0" ${Number(tool.riskLevel || 0) === 0 ? 'selected' : ''}>0 · 只读低风险</option><option value="1" ${Number(tool.riskLevel) === 1 ? 'selected' : ''}>1 · 有限外部影响</option><option value="2" ${Number(tool.riskLevel) === 2 ? 'selected' : ''}>2 · 高风险写操作</option><option value="3" ${Number(tool.riskLevel) === 3 ? 'selected' : ''}>3 · 敏感或破坏性操作</option></select></label>${approvalSelect(tool.approvalMode || (Number(tool.riskLevel || 0) >= 2 ? 'admin_only' : 'auto'))}</div>
    ${textarea('说明', 'description', tool.description || '', 'rows="3"')}${field('能力（逗号分隔）', 'capabilities', listValue(tool.capabilities))}<div class="field"><span>允许调用的身份</span>${authorityFields(authorities)}</div>${textarea('输入参数 JSON Schema', 'inputSchema', JSON.stringify(tool.inputSchema || { type: 'object', properties: {} }, null, 2), 'rows="10" spellcheck="false"')}${checkbox('启用此工具', 'enabled', tool.enabled !== false)}<p class="muted">风险等级 2、3 只能授权管理员；仅管理员模式不能同时授权普通成员。</p>${formActions(tool.id ? '保存工具' : '新增工具', tool.id ? '取消编辑' : '')}
  </form>`;
}
function mcpForm(server = {}) {
  const transport = server.transport || 'http';
  const authorities = server.allowedAuthorities || ['admin'];
  return `<form class="form" data-form="mcp" data-id="${esc(server.id || '')}">
    <div class="form-grid">${field('唯一 ID', 'id', server.id || '', 'text', server.id ? 'readonly' : 'required')}${field('名称', 'name', server.name || '', 'text', 'required')}<label class="field"><span>传输方式</span><select name="transport"><option value="http" ${transport === 'http' ? 'selected' : ''}>Streamable HTTP</option><option value="sse" ${transport === 'sse' ? 'selected' : ''}>SSE</option><option value="stdio" ${transport === 'stdio' ? 'selected' : ''}>stdio（仅服务端预配）</option></select></label>${field('工具前缀', 'toolPrefix', server.toolPrefix || 'mcp', 'text', 'required')}${field('超时（秒）', 'timeoutSeconds', server.timeoutSeconds ?? 30, 'number', 'min="1" max="300" required')}${approvalSelect(server.approvalMode || 'admin_only')}</div>
    <div class="form-grid">${field('HTTP / SSE 地址', 'endpoint', server.endpoint || '', 'url', 'placeholder="https://mcp.example.com/mcp"')}${field('stdio 命令', 'command', server.command || '', 'text', 'placeholder="仅由服务端管理员配置"')}</div>
    ${field('stdio 参数（逗号或换行分隔）', 'args', listValue(server.args))}${field('允许的工具名（逗号或换行分隔）', 'allowedTools', listValue(server.allowedTools))}${field('密钥环境变量名', 'secretRef', server.secretRef || '', 'text', 'placeholder="例如 GROK_API_KEY"')}<div class="field"><span>允许调用的身份</span>${authorityFields(authorities)}</div>${checkbox('启用此 MCP 服务', 'enabled', server.enabled === true, transport === 'stdio' ? 'disabled' : '')}<p class="muted">这里只保存环境变量名，不填写或回显真实密钥。普通成员可访问时必须选择“执行前确认”；stdio 只能由服务端完成安全预配后启用。</p>${formActions(server.id ? '保存 MCP 服务' : '新增 MCP 服务', server.id ? '取消编辑' : '')}
  </form>`;
}
function skillForm(skill = {}) {
  const authorities = skill.allowedAuthorities || ['member', 'admin'];
  const attachments = skill.attachmentKinds || [];
  return `<form class="form" data-form="skill" data-id="${esc(skill.id || '')}">
    <div class="form-grid">${field('唯一 ID', 'id', skill.id || '', 'text', skill.id ? 'readonly' : 'required')}${field('名称', 'name', skill.name || '', 'text', 'required')}${selectField('触发方式', 'activationMode', skill.activationMode || 'any', [['any', '任一条件'], ['all', '全部条件'], ['always', '始终启用']])}${field('优先级', 'priority', skill.priority ?? 0, 'number', 'min="-1000" max="1000"')}</div>
    ${textarea('说明', 'description', skill.description || '', 'rows="3"')}${textarea('触发后注入的技能指令', 'instructions', skill.instructions || '', 'rows="8" required')}
    <div class="form-grid">${field('触发词（逗号或换行）', 'triggers', listValue(skill.triggers))}${field('需要的工具 ID / 名称', 'requiredTools', listValue(skill.requiredTools))}${field('需要的 MCP 服务 ID', 'requiredMcpServers', listValue(skill.requiredMcpServers))}${field('限定角色 ID（留空为全部）', 'personaIds', listValue(skill.personaIds))}</div>
    <fieldset class="config-fieldset"><legend>附件触发</legend><div class="toggle-row">${checkbox('图片', 'attachmentImage', attachments.includes('image'))}${checkbox('音频', 'attachmentAudio', attachments.includes('audio'))}${checkbox('视频', 'attachmentVideo', attachments.includes('video'))}${checkbox('文件', 'attachmentFile', attachments.includes('file'))}</div></fieldset>
    <div class="field"><span>允许使用的身份</span>${authorityFields(authorities)}</div>
    ${checkbox('启用此技能', 'enabled', skill.enabled !== false)}
    ${formActions(skill.id ? '保存技能' : '新增技能', skill.id ? '取消编辑' : '')}
  </form>`;
}

async function skillsView() {
  if (!state.skills) state.skills = await api('/api/v1/skills');
  const editor = state.editingSkill ? controlDialog(state.editingSkill.id ? '编辑技能' : '新增技能', skillForm(state.editingSkill), '只有命中的技能才注入指令并开放所需工具。') : '';
  const rows = (state.skills || []).map((skill) => `<div class="list-item"><div><strong>${esc(skill.name)} ${skill.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</strong><small>${esc(skill.description || '无说明')}</small><small>${esc(skill.activationMode)} · 触发：${esc(listValue(skill.triggers) || listValue(skill.attachmentKinds) || '始终')} · 工具：${esc(listValue(skill.requiredTools) || '无')} · MCP：${esc(listValue(skill.requiredMcpServers) || '无')} · 优先级 ${esc(skill.priority)}</small></div><div class="row">${button('编辑', `edit-skill:${skill.id}`, true)}${button(skill.enabled ? '停用' : '启用', `toggle-skill:${skill.id}`, true)}${button('删除', `delete-skill:${skill.id}`, true)}</div></div>`).join('');
  return shell('技能', '按消息、附件、身份和角色触发。', card('技能目录', rows || empty('暂无技能。'), button('新增技能', 'new-skill')) + editor);
}

async function toolsView() {
  if (!state.tools) state.tools = await api('/api/v1/tools');
  if (!state.mcp) state.mcp = await api('/api/v1/mcp/servers');
  const section = state.toolsSection === 'mcp' ? 'mcp' : 'tools';
  const toolRows = (state.tools || []).map((tool) => `<div class="registry-row"><span class="registry-icon" aria-hidden="true"><span class="nav-icon icon-wrench"></span></span><div class="registry-main"><div class="registry-name"><strong>${esc(tool.name)}</strong>${tool.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</div><small class="registry-description">${esc(tool.description || '无说明')}</small><div class="registry-meta"><span>${esc(tool.id)}</span><span>${esc(tool.adapterRef || '未配置适配器')}</span><span>风险 ${esc(tool.riskLevel)}</span><span>${esc(tool.approvalMode)}</span></div></div><div class="registry-actions">${button('编辑', `edit-tool:${tool.id}`, true)}${button(tool.enabled ? '停用' : '启用', `toggle-tool:${tool.id}`, true)}${button('删除', `delete-tool:${tool.id}`, true)}</div></div>`).join('');
  const mcpRows = (state.mcp || []).map((server) => `<div class="registry-row"><span class="registry-icon" aria-hidden="true"><span class="nav-icon icon-cable"></span></span><div class="registry-main"><div class="registry-name"><strong>${esc(server.name)}</strong>${server.enabled ? '<span class="pill">启用</span>' : '<span class="pill off">停用</span>'}</div><small class="registry-description">${esc(server.endpoint || server.command || '未配置连接地址')}</small><div class="registry-meta"><span>${esc(server.id)}</span><span>${esc(server.transport)}</span><span>前缀 ${esc(server.toolPrefix || '无')}</span><span>${esc(server.approvalMode)}</span></div></div><div class="registry-actions">${server.transport === 'http' && server.enabled ? button('检测', `inspect-mcp:${server.id}`, true) : ''}${button('编辑', `edit-mcp:${server.id}`, true)}${server.transport === 'stdio' ? '<span class="readonly">服务端启用</span>' : button(server.enabled ? '停用' : '启用', `toggle-mcp:${server.id}`, true)}${button('删除', `delete-mcp:${server.id}`, true)}</div></div>`).join('');
  const toolEmpty = `${empty('暂无工具。创建后可配置适配器、权限、审批和参数 Schema。')}<div class="empty-action">${button('配置第一个工具', 'new-tool')}</div>`;
  const mcpEmpty = `${empty('暂无 MCP 服务。创建后可配置连接、工具白名单和权限。')}<div class="empty-action">${button('配置第一个 MCP 服务', 'new-mcp')}</div>`;
  const tabs = `<div class="tools-subnav" role="tablist" aria-label="工具与 MCP"><button class="tools-subnav-tab ${section === 'tools' ? 'active' : ''}" type="button" role="tab" aria-selected="${section === 'tools'}" data-action="show-tools"><span class="nav-icon icon-wrench"></span><span>工具</span><strong>${esc((state.tools || []).length)}</strong></button><button class="tools-subnav-tab ${section === 'mcp' ? 'active' : ''}" type="button" role="tab" aria-selected="${section === 'mcp'}" data-action="show-mcp"><span class="nav-icon icon-cable"></span><span>MCP</span><strong>${esc((state.mcp || []).length)}</strong></button></div>`;
  const currentRows = section === 'tools' ? toolRows : mcpRows;
  const currentEmpty = section === 'tools' ? toolEmpty : mcpEmpty;
  const currentTitle = section === 'tools' ? `工具注册表 · ${(state.tools || []).length}` : `MCP 服务 · ${(state.mcp || []).length}`;
  const currentAction = section === 'tools' ? button('新增工具', 'new-tool') : button('新增 MCP', 'new-mcp');
  let dialog = '';
  if (state.editingTool) dialog = controlDialog(state.editingTool.id ? '编辑工具' : '新增工具', toolForm(state.editingTool), '权限、审批和输入结构由 Core 实时执行。');
  else if (state.editingMcp) dialog = controlDialog(state.editingMcp.id ? '编辑 MCP 服务' : '新增 MCP 服务', mcpForm(state.editingMcp), '连接凭据只引用服务器环境变量。');
  else if (state.mcpInspection) dialog = controlDialog(`检测结果 · ${state.mcpInspection.serverInfo?.name || state.mcpInspection.serverId}`, `<div class="inspection-summary"><strong>${esc(state.mcpInspection.tools?.length || 0)}</strong><span>发现工具</span><small>协议 ${esc(state.mcpInspection.protocolVersion)}</small></div><div class="inspection-list">${(state.mcpInspection.tools || []).map((tool) => `<div class="inspection-row"><div><strong>${esc(tool.name)}</strong><small>${esc(tool.description || '无说明')}</small></div>${tool.allowed ? '<span class="pill">已授权</span>' : '<span class="pill off">未授权</span>'}</div>`).join('') || empty('服务连接成功，但没有返回工具。')}</div>`, '只显示本次实时发现结果。');
  return shell('工具与 MCP', '统一配置 Core 工具目录、权限、审批和 MCP 连接。Streamable HTTP 已支持真实发现与调用；密钥只从服务器环境变量读取。',
    tabs + card(currentTitle, `<div class="registry-list">${currentRows || currentEmpty}</div>`, currentAction) + dialog);
}

async function routingView() {
  if (!state.lanes) state.lanes = await api('/api/v1/routing/lanes');
  if (!state.control) state.control = await api('/api/v1/routing/control');
  if (!state.models) state.models = await api('/api/v1/model-endpoints');
  const lanes = state.lanes.items || state.lanes || [];
  const locks = state.control.locks || {};
  const section = pageSection('routing', 'control');
  const lockFields = lanes.map((p) => `<label class="field"><span>${esc(p.lane)} 固定端点</span><select name="lock-${esc(p.lane)}"><option value="">不锁定</option>${(state.models || []).map((model) => `<option value="${esc(model.id)}" ${locks[p.lane] === model.id ? 'selected' : ''}>${esc(model.provider)} / ${esc(model.model)}</option>`).join('')}</select></label>`).join('');
  const tabs = pageTabs('routing', section, [['control', '全局控制'], ['lanes', '能力通道', lanes.length]]);
  const body = section === 'control'
    ? card('全局控制', `<form class="form" data-form="routing-control"><label class="field"><span>路由模式</span><select name="mode"><option value="auto" ${state.control.mode === 'auto' ? 'selected' : ''}>自动</option><option value="manual" ${state.control.mode === 'manual' ? 'selected' : ''}>手动锁定</option></select></label><div class="form-grid">${lockFields}</div>${formActions('保存路由设置')}</form>`)
    : card('能力通道', `<div class="list">${lanes.map((p) => `<form class="list-item lane-form" data-form="lane" data-lane="${esc(p.lane)}"><div><strong>${esc(p.lane)}</strong><small>必需能力：${esc(listValue(p.requiredCapabilities))}</small></div><div class="lane-fields">${field('必需能力', 'required', listValue(p.requiredCapabilities))}${field('偏好能力', 'preferred', listValue(p.preferredCapabilities))}<button class="button secondary" type="submit">保存</button></div></form>`).join('')}</div>`);
  return shell('模型路由', '按能力、健康、成本和锁定策略选择模型。', tabs + body);
}

async function operationsView() {
  if (!state.audit) state.audit = await api('/api/v1/audit?limit=100');
  if (!state.shadow) state.shadow = await api('/api/v1/shadow/interactions?limit=50');
  if (!state.runs) state.runs = await api('/api/v1/runs');
  const section = pageSection('operations', 'runs');
  const timeline = state.runTimeline ? controlDialog(`运行时间线 · ${state.runTimeline.id}`, `<div class="list">${(state.runTimeline.items || []).map((item) => `<div class="list-item compact-item"><div><strong>${esc(item.stage)}</strong><small>${esc(item.completedAt)} · ${esc(item.durationMs)} ms</small><small>${esc(item.detailsJson || '')}</small></div></div>`).join('') || empty('该运行还没有阶段记录。')}</div>`, '按接收、上下文、路由、模型、质检、Outbox 和投递顺序记录。') : '';
  const runRows = (state.runs || []).map((run) => `<div class="list-item"><div><strong>${esc(run.state)} · ${esc(run.transport)}</strong><small>${esc(run.createdAt)} · 角色 ${esc(run.personaId || '-')} · 总耗时 ${esc(run.totalDurationMs || 0)} ms</small><small>${esc(run.selectedEndpointId || '-')} / ${esc(run.selectedModel || '-')} · 调用 ${esc(run.providerCalls || 0)} 次</small><small>${esc(run.routeReason || '')}${run.errorCode ? ` · ${esc(run.errorCode)}` : ''}</small></div><div class="row">${button('时间线', `open-run:${run.id}`, true)}</div></div>`).join('');
  const tabs = pageTabs('operations', section, [['runs', '最近运行', (state.runs || []).length], ['audit', '审计事件', (state.audit || []).length], ['shadow', '影子交互', (state.shadow || []).length]]);
  let body = card('最近运行', runRows || empty('暂无运行记录。'));
  if (section === 'audit') body = card('审计事件', `<div class="table-wrap"><table><thead><tr><th>时间</th><th>操作者</th><th>动作</th><th>目标</th></tr></thead><tbody>${(state.audit || []).map((e) => `<tr><td>${esc(e.createdAt)}</td><td>${esc(e.actor)}</td><td>${esc(e.action)}</td><td>${esc(e.targetType)} / ${esc(e.targetId || '-')}</td></tr>`).join('') || '<tr><td colspan="4">暂无审计记录</td></tr>'}</tbody></table></div>`);
  if (section === 'shadow') body = card('最近影子交互', `<div class="table-wrap"><table><thead><tr><th>时间</th><th>通道</th><th>Lane</th><th>模型</th><th>输入</th></tr></thead><tbody>${(state.shadow || []).map((e) => `<tr><td>${esc(e.createdAt)}</td><td>${esc(e.transport)}</td><td>${esc(e.lane)}</td><td>${esc(e.selectedEndpointId || '-')}</td><td>${esc(e.messageLength)} 字</td></tr>`).join('') || '<tr><td colspan="5">暂无影子交互</td></tr>'}</tbody></table></div>`);
  return shell('任务与审计', '查看真实运行、审计和影子交互。', tabs + body + timeline);
}

async function devicesView() {
	if (!state.devices) state.devices = await api('/api/v1/devices');
	if (!state.realtimeSessions) state.realtimeSessions = await api('/api/v1/realtime/sessions');
	const section = pageSection('devices', 'trusted');
	const code = state.pairingCode ? controlDialog('一次性配对码', `<div class="pairing-code">${esc(state.pairingCode.code)}</div><p class="muted">${esc(state.pairingCode.expiresAt)} 前有效，成功配对后立即失效。</p>`, '仅用于本次设备配对。') : '';
	const devices = (state.devices || []).map((device) => `<div class="list-item"><div><strong>${esc(device.name)} ${device.online ? '<span class="pill">在线</span>' : '<span class="pill off">离线</span>'}</strong><small>${esc(device.id)} · ${esc(device.status)} · 最近活动 ${esc(device.lastSeenAt || '暂无')}</small></div><div class="row">${device.status === 'trusted' ? button('撤销', `revoke-device:${device.id}`, true) : ''}</div></div>`).join('');
	const sessions = (state.realtimeSessions || []).map((session) => `<div class="list-item"><div><strong>${esc(session.deviceName)} · ${esc(session.state)}</strong><small>${esc(session.id)} · 角色 ${esc(session.personaId || '当前默认')} · ${esc(session.presence)}</small><small>客户端序号 ${esc(session.lastClientSequence)} · 服务端序号 ${esc(session.lastServerSequence)}</small></div></div>`).join('');
	const tabs = pageTabs('devices', section, [['trusted', '可信设备', (state.devices || []).length], ['sessions', '实时会话', (state.realtimeSessions || []).length], ['companion', '桌面 Companion']]);
	let body = card('可信设备', `<div class="list">${devices || empty('还没有配对设备。')}</div>`, button('生成配对码', 'create-pairing'));
	if (section === 'sessions') body = card('实时会话', `<div class="list">${sessions || empty('当前没有桌面会话。')}</div>`);
	if (section === 'companion') body = card('桌面 Companion', '<p class="body-copy">桌面、语音和未来数字人终端都通过 Realtime Gateway 接入同一个 Core。</p>', '<a class="button" href="/companion.html" target="_blank" rel="noopener">打开 Companion</a>');
	return shell('设备与桌面', '管理可信设备和实时终端。', tabs + body + code);
}

async function securityView() {
	if (!state.integrations) state.integrations = await api('/api/v1/integrations');
  if (!state.directives) state.directives = await api('/api/v1/runtime/directives?limit=100');
	const boundary = integrationMap(state.integrations).content_boundary_policy?.config || {};
	const actionOptions = [['model', '交给人格与模型'], ['refuse', '直接拒绝'], ['counter', '短句反击'], ['ignore', '忽略不回']];
  const directives = state.directives.items || [];
  const section = pageSection('security', 'boundary');
  const directiveRows = directives.map((directive) => `<div class="list-item"><div><strong>${directive.enabled ? '启用' : '停用'} · ${esc(directive.id)}</strong><small>${esc(directive.content)}</small></div><div class="row">${button(directive.enabled ? '停用' : '启用', `toggle-directive:${directive.id}`, true)}${button('编辑', `edit-directive:${directive.id}`, true)}${button('删除', `delete-directive:${directive.id}`, true)}</div></div>`).join('');
	const directiveEditor = state.editingDirective ? controlDialog(state.editingDirective.id ? '编辑管理员指令' : '新增管理员指令', personaDirectiveForm(state.editingDirective), '管理员长期指令高于角色、知识、记忆和普通成员消息。') : '';
	const boundaryCard = card('内容与互动边界', `<form class="form" data-form="content-boundary-policy">
			<fieldset class="config-fieldset"><legend>分类动作</legend>
				<div class="form-grid">${selectField('露骨色情', 'sexualAction', boundary.sexualAction || 'refuse', actionOptions)}${selectField('现实伤害', 'violenceAction', boundary.violenceAction || 'refuse', actionOptions)}${selectField('严重辱骂', 'abuseAction', boundary.abuseAction || 'counter', actionOptions)}${selectField('一般挑衅', 'provocationAction', boundary.provocationAction || 'model', actionOptions)}</div>
				<div class="toggle-row">${checkbox('启用内容边界', 'enabled', boundary.enabled !== false)}</div>
			</fieldset>
			<fieldset class="config-fieldset"><legend>触发词与安全语境</legend>
				<div class="form-grid">${textarea('色情触发词', 'sexualTriggers', lineValue(boundary.sexualTriggers), 'rows="6"')}${textarea('暴力触发词', 'violenceTriggers', lineValue(boundary.violenceTriggers), 'rows="6"')}${textarea('严重辱骂触发词', 'abuseTriggers', lineValue(boundary.abuseTriggers), 'rows="6"')}${textarea('一般挑衅触发词', 'provocationTriggers', lineValue(boundary.provocationTriggers), 'rows="6"')}</div>
				<div class="form-grid">${textarea('色情安全语境', 'sexualContextExceptions', lineValue(boundary.sexualContextExceptions), 'rows="5"')}${textarea('暴力安全语境', 'violenceContextExceptions', lineValue(boundary.violenceContextExceptions), 'rows="5"')}${textarea('辱骂引用/求助语境', 'abuseContextExceptions', lineValue(boundary.abuseContextExceptions), 'rows="5"')}</div>
			</fieldset>
			<fieldset class="config-fieldset"><legend>短句库</legend>
				<div class="form-grid">${textarea('色情拒绝文案', 'sexualReplies', lineValue(boundary.sexualReplies), 'rows="5"')}${textarea('暴力拒绝文案', 'violenceReplies', lineValue(boundary.violenceReplies), 'rows="5"')}${textarea('辱骂反击文案', 'abuseReplies', lineValue(boundary.abuseReplies), 'rows="5"')}${textarea('挑衅回应文案', 'provocationReplies', lineValue(boundary.provocationReplies), 'rows="5"')}</div>
				${textarea('模型边界指令', 'modelInstruction', boundary.modelInstruction || '', 'rows="7"')}
			</fieldset>
			<p class="muted">“忽略”不会进入对话上下文；拒绝和反击由 Core 在模型调用前执行。教育、新闻、求助和引用语境可通过例外词放行。</p>${formActions('保存互动边界')}
		</form>`);
	const rulesCard = card('系统规则', `<form class="form" data-form="security-rules">${textarea('系统安全与权限边界', 'protectedRules', state.config?.protectedRules || '', 'rows="16"')}${formActions('保存系统规则')}</form>`);
  const directivesCard = card('管理员长期指令', `<div class="list">${directiveRows || empty('暂无管理员长期指令。')}</div>`, button('新增指令', 'new-directive'));
	const tabs = pageTabs('security', section, [['boundary', '内容边界'], ['rules', '系统规则'], ['directives', '管理员指令', directives.length]]);
	const body = section === 'boundary' ? boundaryCard : section === 'rules' ? rulesCard : directivesCard;
	return shell('安全边界', '系统规则和管理员指令不可被角色覆盖。', `<p class="priority-note"><strong>固定优先级：</strong>系统规则 &gt; 管理员长期指令 &gt; 当前管理员命令 &gt; 当前智能体与世界书 &gt; 知识和记忆 &gt; 普通成员消息</p>` + tabs + body + directiveEditor);
}

async function render() {
  const view = state.view;
  const epoch = ++renderEpoch;
  renderChrome();
  app.classList.add('is-switching');
  app.classList.remove('view-ready');
  const loadingTimer = setTimeout(() => {
    if (epoch !== renderEpoch) return;
    app.innerHTML = loadingView(view);
    app.classList.add('showing-skeleton');
  }, 90);
  try {
    const views = { overview: overviewView, runtime: runtimeInstancesView, system: systemView, integrations: integrationsView, models: modelsView, roles: rolesView, memories: memoriesView, worldbook: worldbookView, samples: personaSamplesView, knowledge: knowledgeView, skills: skillsView, tools: toolsView, routing: routingView, operations: operationsView, devices: devicesView, security: securityView };
    const html = await views[view]();
    if (epoch !== renderEpoch) return;
    clearTimeout(loadingTimer);
    app.innerHTML = `<div class="view-stage" data-rendered-view="${esc(view)}">${html}</div>`;
    app.classList.remove('is-switching', 'showing-skeleton');
    requestAnimationFrame(() => app.classList.add('view-ready'));
    renderChrome();
  } catch (error) {
    if (epoch !== renderEpoch) return;
    clearTimeout(loadingTimer);
    app.classList.remove('is-switching', 'showing-skeleton');
    app.innerHTML = `<div class="notice error">${esc(error.message)}</div>`;
  }
}

document.querySelectorAll('.tab').forEach((tab) => {
  tab.addEventListener('pointerenter', () => warmView(tab.dataset.view), { passive: true });
  tab.addEventListener('focus', () => warmView(tab.dataset.view), { passive: true });
  tab.addEventListener('click', () => {
    document.body.classList.remove('sidebar-open');
    document.querySelector('#sidebar-toggle').setAttribute('aria-expanded', 'false');
    setView(tab.dataset.view);
  });
});
document.querySelector('#sidebar-toggle').addEventListener('click', () => {
  const open = document.body.classList.toggle('sidebar-open');
  document.querySelector('#sidebar-toggle').setAttribute('aria-expanded', String(open));
});
document.querySelector('#role-switch').addEventListener('click', () => {
  const menu = document.querySelector('#role-menu');
  menu.hidden = !menu.hidden;
  document.querySelector('#role-switch').setAttribute('aria-expanded', String(!menu.hidden));
});
document.querySelector('#refresh').addEventListener('click', () => {
  Object.keys(state).forEach((key) => { if (!['view', 'pageSections', 'toolsSection'].includes(key)) state[key] = null; });
  viewWarmups.clear();
  loadCore();
});
document.querySelector('#logout').addEventListener('click', async () => {
  await fetch('/auth/logout', { method: 'POST', headers: { accept: 'application/json' } }).catch(() => {});
  Object.keys(state).forEach((key) => { if (key !== 'view') state[key] = null; });
  renderLogin();
});
document.addEventListener('change', async (event) => {
	if (event.target.form?.dataset.form === 'relationship' && event.target.name === 'intimacy') {
		event.target.form.elements.intimacyValue.value = event.target.value;
	}
	if (event.target.form?.dataset.form === 'relationship' && event.target.name === 'intimacyValue') {
		event.target.form.elements.intimacy.value = event.target.value;
	}
  if (event.target.id === 'persona-import') {
    try {
      await importPersonaFile(event.target.files?.[0]);
      event.target.value = '';
      await render();
    } catch (error) {
      alert(error.message);
    }
    return;
  }
  if (event.target.name === 'avatarFile' && event.target.form?.dataset.form === 'persona') {
    try {
      state.editingPersona = { ...state.editingPersona, avatarDataUri: await fileDataUri(event.target.files?.[0]) };
      await render();
    } catch (error) {
      alert(error.message);
    }
    return;
  }
  if (event.target.id === 'platform-type' && state.editingPlatform && !state.editingPlatform.id) {
    const catalog = platformCatalogItem(event.target.value);
    state.editingPlatform = {
      type: event.target.value,
      displayName: catalog?.displayName || event.target.value,
      settings: { ...(catalog?.settingDefaults || {}) },
      credentialRefs: {},
      enabled: false,
      credentialConfigured: false,
    };
    render();
  }
  if (event.target.id === 'worldbook-persona') {
    state.persona = (state.personas?.items || []).find((persona) => persona.id === event.target.value) || null;
    state.worldbook = null;
    state.editingWorldbook = null;
    render();
  }
  if (event.target.id === 'memory-persona' || event.target.id === 'memory-scope-filter') {
    if (event.target.id === 'memory-persona') state.memoryPersonaId = event.target.value;
    if (event.target.id === 'memory-scope-filter') state.memoryScopeKind = event.target.value;
    state.memories = null;
    state.relationships = null;
    state.editingMemory = null;
		state.editingRelationship = null;
    render();
    return;
  }
  if (event.target.id === 'sample-persona') {
    state.persona = (state.personas?.items || []).find((persona) => persona.id === event.target.value) || null;
    state.personaSamples = null;
    state.personaTraits = null;
    state.editingPersonaSample = null;
    state.editingPersonaTrait = null;
    render();
  }
  if (event.target.name === 'transport' && event.target.form?.dataset.form === 'mcp') {
    const enabled = event.target.form.elements.enabled;
		enabled.disabled = false;
  }
  if (event.target.name === 'type' && event.target.form?.dataset.form === 'runtime-instance') {
    event.target.form.dataset.runtimeConnectorType = event.target.value;
    event.target.form.querySelectorAll('[data-connector-type]').forEach((section) => section.hidden = section.dataset.connectorType !== event.target.value);
  }
});

document.addEventListener('click', async (event) => {
  if (event.target.matches('[data-dialog-backdrop]')) {
    await dismissActiveDialog();
    await render();
    return;
  }
  const roleOption = event.target.closest('[data-role-id]');
  if (roleOption) {
    const id = roleOption.dataset.roleId;
    try {
      await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ activePersonaId: id }) });
      state.config = await api('/api/v1/runtime/config');
      invalidate('overview');
      renderRoleMenu();
      document.querySelector('#role-menu').hidden = true;
      document.querySelector('#role-switch').setAttribute('aria-expanded', 'false');
      await render();
    } catch (error) { alert(error.message); }
    return;
  }
  if (event.target.closest('[data-role-manage]')) {
    await setView('roles');
    return;
  }
  if (!event.target.closest('.instance-picker')) {
    document.querySelector('#role-menu').hidden = true;
    document.querySelector('#role-switch').setAttribute('aria-expanded', 'false');
  }
  const raw = event.target.closest('[data-action]')?.dataset.action;
  const wizardStep = event.target.closest('[data-runtime-step]');
  if (wizardStep) {
    const form = wizardStep.closest('form[data-form="runtime-instance"]');
    if (form) setRuntimeWizardStep(form, Number(wizardStep.dataset.runtimeStep));
    return;
  }
  if (event.target.closest('[data-runtime-wizard-next]') || event.target.closest('[data-runtime-wizard-prev]')) {
    const form = event.target.closest('form[data-form="runtime-instance"]');
    if (form) {
      const current = Number(form.dataset.runtimeWizardStep || 1);
      setRuntimeWizardStep(form, current + (event.target.closest('[data-runtime-wizard-next]') ? 1 : -1));
    }
    return;
  }
  if (!raw) return;
  const [kind, ...parts] = raw.split(':');
  const id = parts[0];
  try {
    if (kind === 'set-section') {
      state.pageSections = { ...(state.pageSections || {}), [id]: parts[1] };
      clearActiveDialog();
    }
    if (kind === 'show-tools') { state.toolsSection = 'tools'; state.editingMcp = state.mcpInspection = null; }
    if (kind === 'show-mcp') { state.toolsSection = 'mcp'; state.editingTool = null; }
    if (kind === 'close-dialog') await dismissActiveDialog();
    if (kind === 'expand-message-copy') {
      const form = event.target.closest('form[data-form="message-policy"]');
      for (const [name, defaults] of Object.entries(MESSAGE_COPY_LIBRARY)) {
        const control = form?.elements[name];
        if (!control) continue;
        const merged = [...new Set([...splitLines(control.value), ...defaults])];
        control.value = merged.join('\n');
      }
      return;
    }
    if (kind === 'new-platform') {
      const catalog = state.platformCatalog?.[0];
      state.editingPlatform = { type: catalog?.type || 'qq_official', displayName: catalog?.displayName || '', settings: { ...(catalog?.settingDefaults || {}) }, credentialRefs: {}, enabled: false, credentialConfigured: false };
    }
    if (kind === 'new-runtime-instance') { state.runtimeWizard = { type: 'aiocqhttp', displayName: '', id: '', personaId: state.config?.activePersonaId || state.personas?.items?.[0]?.id || '', settings: {}, credentialRefs: {}, enabled: false, credentialConfigured: false }; }
    if (kind === 'new-telegram-user') { state.runtimeWizard = { type: 'telegram_user', displayName: 'Telegram · 个人账号', id: 'telegram-user', personaId: state.config?.activePersonaId || state.personas?.items?.[0]?.id || '', settings: {}, credentialRefs: {}, enabled: false, credentialConfigured: false }; }
    if (kind === 'new-xiaoman-qq') { state.runtimeWizard = { type: 'aiocqhttp', displayName: '小满 · 个人 QQ', id: 'xiaoman-qq', personaId: (state.personas?.items || []).find((item) => item.id === 'xiaoman')?.id || state.personas?.items?.[0]?.id || '', settings: {}, credentialRefs: {}, enabled: false, credentialConfigured: false }; }
    if (kind === 'runtime-edit-platform') { state.runtimeWizard = null; state.view = 'integrations'; state.editingPlatform = await api(`/api/v1/platforms/${encodeURIComponent(id)}`); }
    if (kind === 'runtime-config') state.editingRuntimeInstance = (state.agentInstances?.items || []).find((item) => item.id === id) || null;
    if (kind === 'runtime-open-role') { state.runtimeWizard = null; state.view = 'roles'; state.editingPersona = await api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`); state.personaVisualReferences = state.personaEditorData = null; }
    if (kind === 'runtime-open-policy') { state.runtimeWizard = null; state.view = 'roles'; state.editingPersonaProfile = await api(`/api/v1/personas/runtime-profiles/${encodeURIComponent(id)}`); }
    if (kind === 'edit-platform') state.editingPlatform = await api(`/api/v1/platforms/${encodeURIComponent(id)}`);
    if (kind === 'toggle-platform') { const platform = (state.platforms || []).find((item) => item.id === id); await api(`/api/v1/platforms/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !platform.enabled }) }); invalidate('platforms', 'platformRuntime', 'integrations', 'overview'); }
    if (kind === 'delete-platform' && confirm('确定删除这个平台实例？')) { await api(`/api/v1/platforms/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('platforms', 'platformRuntime', 'overview'); }
		if (kind === 'telegram-user-start') {
			const form = event.target.closest('form[data-form="platform"]');
			const phone = form?.elements.telegramUserPhone?.value.trim() || '';
			const result = await api(`/api/v1/platforms/${encodeURIComponent(id)}/telegram-user/auth/start`, { method: 'POST', body: JSON.stringify({ phone }) });
			alert(result.authStep === 'code_required' ? '验证码已发送。' : `当前状态：${result.authStep}`);
			invalidate('platformRuntime');
		}
		if (kind === 'telegram-user-code') {
			const form = event.target.closest('form[data-form="platform"]');
			const code = form?.elements.telegramUserCode?.value.trim() || '';
			const result = await api(`/api/v1/platforms/${encodeURIComponent(id)}/telegram-user/auth/code`, { method: 'POST', body: JSON.stringify({ code }) });
			alert(result.authStep === 'password_required' ? '还需要两步验证密码。' : 'Telegram 账号已登录。');
			invalidate('platformRuntime');
		}
		if (kind === 'telegram-user-password') {
			const form = event.target.closest('form[data-form="platform"]');
			const password = form?.elements.telegramUserPassword?.value || '';
			await api(`/api/v1/platforms/${encodeURIComponent(id)}/telegram-user/auth/password`, { method: 'POST', body: JSON.stringify({ password }) });
			if (form?.elements.telegramUserPassword) form.elements.telegramUserPassword.value = '';
			alert('Telegram 账号已登录。');
			invalidate('platformRuntime');
		}
    if (kind === 'new-model') state.editingModel = {};
		if (kind === 'new-provider') state.editingProviderConnection = { enabled: true, protocol: 'openai_chat_completion', timeoutSeconds: 120 };
		if (kind === 'edit-provider') state.editingProviderConnection = (state.providerConnections || []).find((item) => item.id === id) || null;
		if (kind === 'delete-provider' && confirm('确定删除这个供应商连接？')) { await api(`/api/v1/provider-connections/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('providerConnections', 'models'); }
		if (kind === 'test-provider') { const result = await api(`/api/v1/provider-connections/${encodeURIComponent(id)}/test`, { method: 'POST', body: '{}' }); alert(result.healthy ? `连接正常，${result.latencyMs} ms` : `连接失败：${result.statusMessage || '未知错误'}`); invalidate('health', 'models'); }
		if (kind === 'sync-pricing') { const result = await api(`/api/v1/provider-connections/${encodeURIComponent(id)}/pricing-sync`, { method: 'POST', body: '{}' }); alert(`已同步 ${result.updatedModels} 个模型的价格`); invalidate('providerConnections', 'models'); }
		if (kind === 'health-history') { state.healthHistory = { id, items: await api(`/api/v1/model-health/${encodeURIComponent(id)}/history`) }; }
		if (kind === 'open-run') { state.runTimeline = { id, items: await api(`/api/v1/runs/${encodeURIComponent(id)}`) }; }
		if (kind === 'create-pairing') { state.pairingCode = await api('/api/v1/realtime/pairing-codes', { method: 'POST', body: '{}' }); }
		if (kind === 'revoke-device' && confirm('确定撤销这个设备？')) { await api(`/api/v1/devices/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('devices', 'realtimeSessions'); }
    if (kind === 'edit-model') state.editingModel = (state.models || []).find((m) => m.id === id);
    if (kind === 'delete-model' && confirm('确定删除这个模型端点？')) { await api(`/api/v1/model-endpoints/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('models', 'health', 'overview'); }
    if (kind === 'toggle-model') { const m = (state.models || []).find((x) => x.id === id); await api(`/api/v1/model-endpoints/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(modelPayload(m, !m.enabled)) }); invalidate('models', 'overview'); }
    if (kind === 'import-persona') { document.querySelector('#persona-import')?.click(); return; }
    if (kind === 'new-persona') { state.editingPersona = {}; state.personaVisualReferences = state.personaEditorData = null; }
    if (kind === 'new-persona-binding') state.editingPersonaBinding = { enabled: true, priority: 100, transport: '*', transportInstance: '*', conversationRef: '*', personaId: state.config.activePersonaId || state.personas?.items?.[0]?.id || '' };
    if (kind === 'edit-persona-binding') state.editingPersonaBinding = (state.personaBindings?.items || []).find((item) => item.id === id) || null;
    if (kind === 'toggle-persona-binding') { const binding = (state.personaBindings?.items || []).find((item) => item.id === id); await api(`/api/v1/persona-bindings/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ personaId: binding.personaId, transport: binding.transport, transportInstance: binding.transportInstance || '*', conversationRef: binding.conversationRef, priority: binding.priority, enabled: !binding.enabled }) }); invalidate('personaBindings'); }
    if (kind === 'delete-persona-binding' && confirm('确定删除这个角色绑定？')) { await api(`/api/v1/persona-bindings/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('personaBindings'); }
    if (kind === 'edit-persona') { state.editingPersona = await api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`); state.personaVisualReferences = state.personaEditorData = null; }
    if (kind === 'edit-persona-profile') state.editingPersonaProfile = await api(`/api/v1/personas/runtime-profiles/${encodeURIComponent(id)}`);
    if (kind === 'persona-memories') { state.view = 'memories'; state.memoryPersonaId = id; state.memories = state.relationships = null; state.editingMemory = state.editingRelationship = null; }
    if (kind === 'new-memory') state.editingMemory = { scopeKind: state.memoryScopeKind || 'user', confidence: 1, importance: 0.7 };
    if (kind === 'edit-memory') state.editingMemory = (state.memories?.items || []).find((memory) => memory.id === id) || null;
		if (kind === 'edit-relationship') state.editingRelationship = (state.relationships?.items || []).find((relationship) => relationship.id === id) || null;
		if (kind === 'auto-relationship') { await api(`/api/v1/relationships/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ locked: false }) }); state.editingRelationship = null; invalidate('relationships'); }
		if (kind === 'delete-relationship' && confirm('确定清除这段关系记录？之后会从新的互动重新认识。')) { await api(`/api/v1/relationships/${encodeURIComponent(id)}`, { method: 'DELETE' }); state.editingRelationship = null; invalidate('relationships'); }
    if (kind === 'delete-memory') {
      const memory = (state.memories?.items || []).find((item) => item.id === id);
      if (memory && confirm('确定删除这条记忆？')) {
        const query = new URLSearchParams({ personaId: memory.personaId, scopeKind: memory.scopeKind, scopeReference: memory.scopeReference });
        await api(`/api/v1/memories/${encodeURIComponent(id)}?${query}`, { method: 'DELETE' });
        invalidate('memories');
      }
    }
    if (kind === 'open-persona') { state.view = 'roles'; state.editingPersona = await api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`); state.personaVisualReferences = state.personaEditorData = null; }
    if (kind === 'remove-avatar') { state.editingPersona = { ...state.editingPersona, avatarDataUri: '' }; }
		if (kind === 'primary-visual-reference') {
			const personaID = state.personaVisualReferences?.personaId || state.editingPersona?.id;
			if (personaID) await api(`/api/v1/personas/${encodeURIComponent(personaID)}/visual-references/${encodeURIComponent(id)}?namespace=default`, { method: 'PUT', body: JSON.stringify({ isPrimary: true }) });
			state.personaVisualReferences = state.personaEditorData = null;
		}
		if (kind === 'delete-visual-reference' && confirm('确定删除这份形象参考？')) {
			const personaID = state.personaVisualReferences?.personaId || state.editingPersona?.id;
			if (personaID) await api(`/api/v1/personas/${encodeURIComponent(personaID)}/visual-references/${encodeURIComponent(id)}?namespace=default`, { method: 'DELETE' });
			state.personaVisualReferences = state.personaEditorData = null;
		}
		if (kind === 'export-visual-references') {
			const link = document.createElement('a');
			link.href = `/api/v1/personas/${encodeURIComponent(id)}/visual-references/export?namespace=default`;
			link.download = `persona-${id}-visual-references.erdai.zip`;
			document.body.appendChild(link);
			link.click();
			link.remove();
		}
		if (kind === 'copy-persona') { const source = await api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`); const copyID = crypto.randomUUID(); await api('/api/v1/personas', { method: 'POST', body: JSON.stringify({ id: copyID, namespace: 'default', name: `${source.name} 副本`, description: source.description, visualDescription: source.visualDescription, personality: source.personality, scenario: source.scenario, systemPrompt: source.systemPrompt, postHistoryInstructions: source.postHistoryInstructions, messageExample: source.messageExample, firstMessage: source.firstMessage, alternateGreetings: source.alternateGreetings, tags: source.tags, creator: source.creator, characterVersion: source.characterVersion, sourceFormat: source.sourceFormat, sourceVersion: source.sourceVersion, avatarDataUri: source.avatarDataUri }) }); await api(`/api/v1/personas/${encodeURIComponent(id)}/visual-references/clone?namespace=default`, { method: 'POST', body: JSON.stringify({ targetPersonaId: copyID }) }); invalidate('personas', 'overview'); }
    if (kind === 'export-persona' || kind === 'export-v2') { const value = await personaWithWorldbook(id); const filename = safeFilename(value.persona.name); downloadJSON(`${filename}.${kind === 'export-v2' ? 'sillytavern-v2' : 'erdai'}.json`, kind === 'export-v2' ? exportSillyTavernV2(value.persona, value.worldbook, value.traits, value.samples, value.runtimeProfile) : exportNativeCharacterCard(value.persona, value.worldbook, value.traits, value.samples, value.runtimeProfile)); }
    if (kind === 'delete-persona' && confirm('确定删除这个智能体及其世界书？')) { if (state.config.activePersonaId === id) { await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ activePersonaId: null }) }); state.config = await api('/api/v1/runtime/config'); } await api(`/api/v1/personas/${encodeURIComponent(id)}?namespace=default`, { method: 'DELETE' }); invalidate('personas', 'overview', 'worldbook'); }
    if (kind === 'activate') { await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ activePersonaId: id }) }); state.config = await api('/api/v1/runtime/config'); invalidate('overview', 'personas'); }
    if (kind === 'new-directive') state.editingDirective = {};
    if (kind === 'edit-directive') state.editingDirective = await api(`/api/v1/runtime/directives/${encodeURIComponent(id)}`);
    if (kind === 'toggle-directive') { const d = (state.directives.items || []).find((x) => x.id === id); await api(`/api/v1/runtime/directives/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !d.enabled }) }); invalidate('directives'); }
    if (kind === 'delete-directive' && confirm('确定删除这条管理员指令？')) { await api(`/api/v1/runtime/directives/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('directives'); }
    if (kind === 'new-worldbook') state.editingWorldbook = {};
    if (kind === 'edit-worldbook') state.editingWorldbook = await api(`/api/v1/personas/${encodeURIComponent(state.worldbook.personaId)}/worldbook/${encodeURIComponent(id)}?namespace=default`);
    if (kind === 'toggle-worldbook') { const w = (state.worldbook.items || []).find((x) => x.id === id); await api(`/api/v1/personas/${encodeURIComponent(state.worldbook.personaId)}/worldbook/${encodeURIComponent(id)}?namespace=default`, { method: 'PUT', body: JSON.stringify({ enabled: !w.enabled }) }); invalidate('worldbook'); }
    if (kind === 'delete-worldbook' && confirm('确定删除这个世界书条目？')) { await api(`/api/v1/personas/${encodeURIComponent(state.worldbook.personaId)}/worldbook/${encodeURIComponent(id)}?namespace=default`, { method: 'DELETE' }); invalidate('worldbook'); }
    if (kind === 'new-persona-sample') state.editingPersonaSample = {};
    if (kind === 'edit-persona-sample') state.editingPersonaSample = await api(`/api/v1/personas/${encodeURIComponent(state.personaSamples.personaId)}/samples/${encodeURIComponent(id)}?namespace=default`);
    if (kind === 'toggle-persona-sample') { const sample = (state.personaSamples.items || []).find((item) => item.id === id); await api(`/api/v1/personas/${encodeURIComponent(state.personaSamples.personaId)}/samples/${encodeURIComponent(id)}?namespace=default`, { method: 'PUT', body: JSON.stringify({ enabled: !sample.enabled }) }); invalidate('personaSamples'); }
    if (kind === 'delete-persona-sample' && confirm('确定删除这个人格样本？')) { await api(`/api/v1/personas/${encodeURIComponent(state.personaSamples.personaId)}/samples/${encodeURIComponent(id)}?namespace=default`, { method: 'DELETE' }); invalidate('personaSamples'); }
    if (kind === 'new-persona-trait') state.editingPersonaTrait = {};
    if (kind === 'edit-persona-trait') state.editingPersonaTrait = await api(`/api/v1/personas/${encodeURIComponent(state.personaTraits.personaId)}/traits/${encodeURIComponent(id)}?namespace=default`);
    if (kind === 'toggle-persona-trait') { const trait = (state.personaTraits.items || []).find((item) => item.id === id); await api(`/api/v1/personas/${encodeURIComponent(state.personaTraits.personaId)}/traits/${encodeURIComponent(id)}?namespace=default`, { method: 'PUT', body: JSON.stringify({ enabled: !trait.enabled }) }); invalidate('personaTraits'); }
    if (kind === 'delete-persona-trait' && confirm('确定删除这个人格特质？')) { await api(`/api/v1/personas/${encodeURIComponent(state.personaTraits.personaId)}/traits/${encodeURIComponent(id)}?namespace=default`, { method: 'DELETE' }); invalidate('personaTraits'); }
    if (kind === 'new-knowledge-base') state.editingKnowledgeBase = {};
    if (kind === 'new-document') state.editingDocument = {};
    if (kind === 'edit-document') state.editingDocument = await api(`/api/v1/knowledge/documents/${encodeURIComponent(id)}?namespace=${encodeURIComponent(state.config.knowledgeNamespace || 'default')}`);
    if (kind === 'delete-document' && confirm('确定删除这份知识文档？')) { await api(`/api/v1/knowledge/documents/${encodeURIComponent(id)}?namespace=${encodeURIComponent(state.config.knowledgeNamespace || 'default')}`, { method: 'DELETE' }); invalidate('documents', 'overview'); }
    if (kind === 'toggle-knowledge-base') { const base = (state.knowledgeBases?.items || []).find((item) => item.id === id); if (base) { await api(`/api/v1/knowledge/bases/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !base.enabled }) }); invalidate('knowledgeBases', 'documents', 'overview'); } }
    if (kind === 'approve' || kind === 'reject') { const decision = kind === 'approve' ? 'approved' : 'rejected'; await api(`/api/v1/runtime/knowledge-candidates/${encodeURIComponent(id)}/review`, { method: 'POST', body: JSON.stringify({ decision, authority: 'admin', knowledgeNamespace: state.config.knowledgeNamespace || 'default' }) }); invalidate('candidates', 'documents', 'overview'); }
    if (kind === 'delete-candidate' && confirm('确定删除这个候选知识？')) { await api(`/api/v1/runtime/knowledge-candidates/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('candidates'); }
    if (kind === 'new-tool') { state.toolsSection = 'tools'; state.editingMcp = state.mcpInspection = null; state.editingTool = {}; }
    if (kind === 'edit-tool') { state.toolsSection = 'tools'; state.editingMcp = state.mcpInspection = null; state.editingTool = await api(`/api/v1/tools/${encodeURIComponent(id)}`); }
    if (kind === 'toggle-tool') { const tool = (state.tools || []).find((item) => item.id === id); await api(`/api/v1/tools/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !tool.enabled }) }); invalidate('tools', 'audit'); }
    if (kind === 'delete-tool' && confirm('确定删除这个工具？')) { await api(`/api/v1/tools/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('tools', 'audit'); }
    if (kind === 'new-skill') state.editingSkill = {};
    if (kind === 'edit-skill') state.editingSkill = await api(`/api/v1/skills/${encodeURIComponent(id)}`);
    if (kind === 'toggle-skill') { const skill = (state.skills || []).find((item) => item.id === id); await api(`/api/v1/skills/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !skill.enabled }) }); invalidate('skills', 'audit'); }
    if (kind === 'delete-skill' && confirm('确定删除这个技能？')) { await api(`/api/v1/skills/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('skills', 'audit'); }
    if (kind === 'new-mcp') { state.toolsSection = 'mcp'; state.editingTool = state.mcpInspection = null; state.editingMcp = {}; }
    if (kind === 'inspect-mcp') { state.toolsSection = 'mcp'; state.editingTool = state.editingMcp = null; state.mcpInspection = await api(`/api/v1/mcp/servers/${encodeURIComponent(id)}/discover`, { method: 'POST', body: '{}' }); }
    if (kind === 'edit-mcp') { state.toolsSection = 'mcp'; state.editingTool = state.mcpInspection = null; state.editingMcp = await api(`/api/v1/mcp/servers/${encodeURIComponent(id)}`); }
    if (kind === 'toggle-mcp') { const server = (state.mcp || []).find((item) => item.id === id); await api(`/api/v1/mcp/servers/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !server.enabled }) }); invalidate('mcp', 'audit'); }
    if (kind === 'delete-mcp' && confirm('确定删除这个 MCP 服务？')) { await api(`/api/v1/mcp/servers/${encodeURIComponent(id)}`, { method: 'DELETE' }); invalidate('mcp', 'audit'); }
		if (kind === 'cancel-edit') await dismissActiveDialog();
    if (kind === 'go-roles') state.view = 'roles';
    if (kind === 'go-knowledge') state.view = 'knowledge';
    if (kind === 'go-tools') state.view = 'tools';
    if (kind === 'go-skills') state.view = 'skills';
    if (kind === 'go-platforms') state.view = 'integrations';
    if (kind === 'go-security') state.view = 'security';
    await render();
  } catch (error) { alert(error.message); }
});

document.addEventListener('keydown', async (event) => {
  if (event.key !== 'Escape' || !document.querySelector('.control-dialog-backdrop')) return;
  await dismissActiveDialog();
  await render();
});

document.addEventListener('submit', async (event) => {
  const form = event.target; event.preventDefault();
  const data = Object.fromEntries(new FormData(form).entries());
  try {
    if (form.dataset.form === 'knowledge-base') { const body = { name: data.name, description: data.description, layer: data.layer, namespace: data.namespace, ownerKind: data.ownerKind, ownerId: data.ownerId, enabled: form.elements.enabled.checked, autoInclude: form.elements.autoInclude.checked, priority: Number(data.priority) }; const id = form.dataset.id || data.id.trim(); if (!id) throw new Error('知识库 ID 不能为空'); await api(`/api/v1/knowledge/bases/${encodeURIComponent(id)}`, { method: form.dataset.id ? 'PUT' : 'POST', body: JSON.stringify({ id, ...body }) }); state.editingKnowledgeBase = null; invalidate('knowledgeBases', 'documents', 'overview'); }
    if (form.dataset.form === 'admin-login') {
      const response = await fetch('/auth/login', { method: 'POST', headers: { accept: 'application/json', 'content-type': 'application/json' }, body: JSON.stringify({ token: data.token }) });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) throw new Error(payload?.error?.message || '登录失败');
      Object.keys(state).forEach((key) => { if (key !== 'view') state[key] = null; });
      await loadCore();
      return;
    }
    if (form.dataset.form === 'runtime-instance') {
      const type = data.type;
      const catalog = platformCatalogItem(type);
      if (!catalog) throw new Error('该连接器尚未进入平台目录');
      const settings = {};
      const credentialRefs = {};
      for (const name of catalog.settingFields || []) {
        const element = form.elements[`setting:${name}`];
        const fallback = catalog.settingDefaults?.[name];
        if (!element) continue;
        if (typeof fallback === 'boolean') settings[name] = element.checked;
        else if (typeof fallback === 'number' || fallback === null) settings[name] = element.value === '' ? null : Number(element.value);
        else settings[name] = element.value;
      }
      for (const name of catalog.credentialFields || []) {
        const reference = form.elements[`credential:${name}`]?.value.trim();
        if (reference) credentialRefs[name] = reference;
      }
      const instanceID = data.id.trim();
      if (!instanceID) throw new Error('实例 ID 不能为空');
      const displayName = data.displayName.trim();
      const connectorID = `${instanceID}-connector`;
      const platformBody = { displayName, enabled: false, credentialConfigured: form.elements.credentialConfigured.checked || Object.keys(credentialRefs).length > 0, settings, credentialRefs };
      const templateID = data.policyTemplateId === '__new__' ? `${instanceID}-policy` : (data.policyTemplateId || '');
      const routeID = `${instanceID}-route`;
      const created = { platform: false, template: false, instance: false, connector: false, route: false };
      try {
        await api('/api/v1/platforms', { method: 'POST', body: JSON.stringify({ id: connectorID, type, ...platformBody }) });
        created.platform = true;
        if (data.policyTemplateId === '__new__' || !data.policyTemplateId) {
          await api('/api/v1/agent-policy-templates', { method: 'POST', body: JSON.stringify({ id: templateID, name: `${displayName} 运行策略`, description: `实例 ${instanceID} 的默认运行策略`, config: {}, enabled: true }) });
          created.template = true;
        }
        await api('/api/v1/agent-instances', { method: 'POST', body: JSON.stringify({ id: instanceID, displayName, personaId: data.personaId, policyTemplateId: templateID, memoryNamespace: `instance:${instanceID}`, overrides: {}, enabled: false }) });
        created.instance = true;
        await api(`/api/v1/agent-instances/${encodeURIComponent(instanceID)}/connectors`, { method: 'POST', body: JSON.stringify({ connectorId: connectorID, enabled: false, priority: 100 }) });
        created.connector = true;
        await api('/api/v1/agent-instance-routes', { method: 'POST', body: JSON.stringify({ id: routeID, instanceId: instanceID, connectorId: connectorID, transport: type, conversationRef: '*', priority: 200, enabled: false }) });
        created.route = true;
      } catch (error) {
        if (created.route) await api(`/api/v1/agent-instance-routes/${encodeURIComponent(routeID)}`, { method: 'DELETE' }).catch(() => {});
        if (created.connector) await api(`/api/v1/agent-instances/${encodeURIComponent(instanceID)}/connectors/${encodeURIComponent(connectorID)}`, { method: 'DELETE' }).catch(() => {});
        if (created.instance) await api(`/api/v1/agent-instances/${encodeURIComponent(instanceID)}`, { method: 'DELETE' }).catch(() => {});
        if (created.template) await api(`/api/v1/agent-policy-templates/${encodeURIComponent(templateID)}`, { method: 'DELETE' }).catch(() => {});
        if (created.platform) await api(`/api/v1/platforms/${encodeURIComponent(connectorID)}`, { method: 'DELETE' }).catch(() => {});
        throw error;
      }
      state.runtimeWizard = null;
      invalidate('platforms', 'platformRuntime', 'agentInstances', 'agentPolicyTemplates', 'agentInstanceRoutes', 'agentInstanceConnectors', 'agentInstanceCapabilities', 'integrations', 'overview');
    }
    if (form.dataset.form === 'runtime-instance-config') {
      const instance = (state.agentInstances?.items || []).find((item) => item.id === form.dataset.id);
      if (!instance) throw new Error('运行实例不存在');
      const overrides = { ...(instance.overrides || {}) };
      const participationMode = data.participationMode.trim();
      if (!participationMode) {
        delete overrides.participationMode;
        delete overrides.proactiveEnabled;
        delete overrides.unaddressedMode;
        delete overrides.participationStyle;
      } else {
        overrides.participationMode = participationMode;
        overrides.proactiveEnabled = participationMode !== 'addressed_only';
        overrides.unaddressedMode = participationMode === 'addressed_only' ? 'off' : 'adaptive';
        overrides.participationStyle = participationMode === 'social' ? 'social' : 'balanced';
      }
      overrides.expressionPrompt = data.expressionPrompt.trim();
      overrides.addressKeywords = splitList(data.addressKeywords);
      if (data.initialReplyProbability === '') delete overrides.initialReplyProbability;
      else overrides.initialReplyProbability = Number(data.initialReplyProbability);
      if (data.afterReplyProbability === '') delete overrides.afterReplyProbability;
      else overrides.afterReplyProbability = Number(data.afterReplyProbability);
      await api(`/api/v1/agent-instances/${encodeURIComponent(instance.id)}`, { method: 'PUT', body: JSON.stringify({ overrides }) });
      await api(`/api/v1/agent-instance-capabilities/${encodeURIComponent(instance.id)}/group_moderation`, { method: 'PUT', body: JSON.stringify({
        enabled: form.elements.moderationEnabled.checked,
        config: { mode: data.moderationMode, executorConnectorId: data.executorConnectorId, groupIds: splitList(data.groupIds), exemptAdmins: form.elements.exemptAdmins.checked, minimumScore: Number(data.minimumScore), allowedSenderIds: splitList(data.allowedSenderIds) },
      }) });
      state.editingRuntimeInstance = null;
      invalidate('agentInstances', 'agentInstanceCapabilities');
    }
    if (form.dataset.form === 'platform') {
      const type = form.dataset.id ? state.editingPlatform.type : data.type;
      const catalog = platformCatalogItem(type);
      const settings = {};
      const credentialRefs = {};
      for (const name of catalog?.settingFields || []) {
        const element = form.elements[`setting:${name}`];
        const fallback = catalog.settingDefaults?.[name];
        if (!element) continue;
        if (typeof fallback === 'boolean') settings[name] = element.checked;
        else if (typeof fallback === 'number' || fallback === null) settings[name] = element.value === '' ? null : Number(element.value);
        else settings[name] = element.value;
      }
      for (const name of catalog?.credentialFields || []) {
        const reference = form.elements[`credential:${name}`]?.value.trim();
        if (reference) credentialRefs[name] = reference;
      }
      const body = { displayName: data.displayName, enabled: form.elements.enabled.checked, credentialConfigured: form.elements.credentialConfigured.checked, settings, credentialRefs };
      if (form.dataset.id) await api(`/api/v1/platforms/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) });
      else await api('/api/v1/platforms', { method: 'POST', body: JSON.stringify({ id: data.id, type, ...body }) });
      state.editingPlatform = null;
      invalidate('platforms', 'platformRuntime', 'integrations', 'overview');
    }
    if (form.dataset.form === 'runtime') {
      await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ knowledgeNamespace: data.knowledgeNamespace, personaInjectionEnabled: form.elements.personaInjectionEnabled.checked, worldbookInjectionEnabled: form.elements.worldbookInjectionEnabled.checked, knowledgeInjectionEnabled: form.elements.knowledgeInjectionEnabled.checked, avoidRepetitiveOpeners: form.elements.avoidRepetitiveOpeners.checked, replyStyle: data.replyStyle, maxReplySentences: Number(data.maxReplySentences), maxReplyChars: Number(data.maxReplyChars), learningEnabled: form.elements.learningEnabled.checked, learningTopics: splitList(data.learningTopics), learningIntervalHours: Number(data.learningIntervalHours) }) });
      state.config = await api('/api/v1/runtime/config'); invalidate('overview', 'personas', 'documents');
    }
    if (form.dataset.form === 'media-quotas') {
      state.mediaQuotas = await api('/api/v1/runtime/media-quotas', { method: 'PUT', body: JSON.stringify({ imageDailyLimit: Number(data.imageDailyLimit), videoDailyLimit: Number(data.videoDailyLimit), trustedAdminBypass: form.elements.trustedAdminBypass.checked, whitelist: parseMediaQuotaWhitelist(data.mediaQuotaWhitelist) }) });
    }
    if (form.dataset.form === 'transport-integration') {
      await api('/api/v1/integrations/channel_runtime', { method: 'PUT', body: JSON.stringify({ mode: data.mode, deliveryPollSeconds: Number(data.deliveryPollSeconds) }) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'provider-integration') {
      await api('/api/v1/integrations/provider_policy', { method: 'PUT', body: JSON.stringify({ streaming: form.elements.streaming.checked, providerRetries: Number(data.providerRetries), maxAgentSteps: Number(data.maxAgentSteps), toolCallTimeoutSeconds: Number(data.toolCallTimeoutSeconds) }) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'grok-policy') {
      await api('/api/v1/integrations/grok_policy', { method: 'PUT', body: JSON.stringify(serializeGrokPolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'memory-policy') {
      await api('/api/v1/integrations/memory_policy', { method: 'PUT', body: JSON.stringify(serializeMemoryPolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'retrieval-policy') {
      await api('/api/v1/integrations/retrieval_policy', { method: 'PUT', body: JSON.stringify(serializeRetrievalPolicy(form)) });
      invalidate('integrations', 'documents');
    }
    if (form.dataset.form === 'document-policy') {
      await api('/api/v1/integrations/document_policy', { method: 'PUT', body: JSON.stringify(serializeDocumentPolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'ops-policy') {
      await api('/api/v1/integrations/ops_policy', { method: 'PUT', body: JSON.stringify(serializeOpsPolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'image-policy') {
      await api('/api/v1/integrations/image_policy', { method: 'PUT', body: JSON.stringify(serializeImagePolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'group-chat-policy') {
      await api('/api/v1/integrations/group_chat_policy', { method: 'PUT', body: JSON.stringify(serializeGroupChatPolicy(form)) });
      invalidate('integrations');
    }
    if (form.dataset.form === 'companion-policy') {
      await api('/api/v1/integrations/companion_policy', { method: 'PUT', body: JSON.stringify(serializeCompanionPolicy(form)) });
      invalidate('integrations');
    }
		if (form.dataset.form === 'message-policy') {
      await api('/api/v1/integrations/message_policy', { method: 'PUT', body: JSON.stringify(serializeMessagePolicy(form)) });
      invalidate('integrations');
		}
		if (form.dataset.form === 'content-boundary-policy') {
			await api('/api/v1/integrations/content_boundary_policy', { method: 'PUT', body: JSON.stringify(serializeContentBoundaryPolicy(form)) });
			invalidate('integrations');
		}
    if (form.dataset.form === 'provider-connection') { const id = data.id.trim(); await api(`/api/v1/provider-connections/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ provider: data.provider, protocol: data.protocol, apiBase: data.apiBase, pricingUrl: data.pricingUrl, credentialRef: data.credentialRef, timeoutSeconds: Number(data.timeoutSeconds), enabled: form.elements.enabled.checked }) }); state.editingProviderConnection = null; invalidate('providerConnections', 'models'); }
    if (form.dataset.form === 'model') { const body = { provider: data.provider, model: data.model, connectionId: data.connectionId, enabled: form.elements.enabled.checked, capabilities: splitList(data.capabilities), inputCostPerMillion: Number(data.inputCostPerMillion), outputCostPerMillion: Number(data.outputCostPerMillion), qualityScore: Number(data.qualityScore), priority: Number(data.priority), maxContextTokens: Number(data.maxContextTokens), executionKind: data.executionKind, adapterRef: data.adapterRef }; const id = data.id.trim() || `${data.provider}-${data.model}`.replace(/[^\w.-]+/g, '-'); await api(`/api/v1/model-endpoints/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) }); state.editingModel = null; invalidate('models', 'health', 'overview'); }
		if (form.dataset.form === 'persona') { const body = { name: data.name, description: data.description, visualDescription: data.visualDescription, personality: data.personality, scenario: data.scenario, systemPrompt: data.systemPrompt, postHistoryInstructions: data.postHistoryInstructions, messageExample: data.messageExample, firstMessage: data.firstMessage, alternateGreetings: splitList(data.alternateGreetings), tags: splitList(data.tags), creator: data.creator, characterVersion: data.characterVersion, sourceFormat: data.sourceFormat, sourceVersion: data.sourceVersion, avatarDataUri: normalizeAvatarDataUri(data.avatarDataUri) }; if (form.dataset.id) await api(`/api/v1/personas/${encodeURIComponent(form.dataset.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/personas', { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID(), namespace: 'default' }) }); state.editingPersona = null; state.personaVisualReferences = state.personaEditorData = null; invalidate('personas', 'overview'); }
		if (form.dataset.form === 'visual-reference-upload') { const body = new FormData(form); await api(`/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/visual-references?namespace=default`, { method: 'POST', body }); state.personaVisualReferences = state.personaEditorData = null; }
		if (form.dataset.form === 'visual-reference-package-import') { const body = new FormData(form); await api(`/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/visual-references/import?namespace=default`, { method: 'POST', body }); state.personaVisualReferences = state.personaEditorData = null; }
		if (form.dataset.form === 'visual-reference-meta') { const body = { category: data.category, label: data.label, promptNotes: data.promptNotes, sortOrder: Number(data.sortOrder), enabled: form.elements.enabled.checked }; await api(`/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/visual-references/${encodeURIComponent(form.dataset.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify(body) }); state.personaVisualReferences = state.personaEditorData = null; }
		if (form.dataset.form === 'persona-runtime-profile') { const participationMode = data.participationMode.trim(); const body = { chatEndpointId: data.chatEndpointId, taskEndpointId: data.taskEndpointId, decisionEndpointId: data.decisionEndpointId, allowedToolIds: splitList(data.allowedToolIds), deniedToolIds: splitList(data.deniedToolIds), participationMode, maxReplyChars: data.maxReplyChars === '' ? null : Number(data.maxReplyChars), maxReplySentences: data.maxReplySentences === '' ? null : Number(data.maxReplySentences), memoryPolicy: data.memoryPolicy, searchMode: data.searchMode, searchReplyStyle: data.searchReplyStyle, visualPromptOverride: data.visualPromptOverride, expressionPrompt: data.expressionPrompt }; await api(`/api/v1/personas/runtime-profiles/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) }); state.editingPersonaProfile = null; invalidate('personaProfiles', 'personas', 'overview'); }
    if (form.dataset.form === 'persona-binding') { const id = form.dataset.id || crypto.randomUUID(); await api(`/api/v1/persona-bindings/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify({ personaId: data.personaId, transport: data.transport, transportInstance: data.transportInstance || '*', conversationRef: data.conversationRef, priority: Number(data.priority), enabled: form.elements.enabled.checked }) }); state.editingPersonaBinding = null; invalidate('personaBindings'); }
    if (form.dataset.form === 'persona-memory') { const body = { personaId: data.personaId, scopeKind: data.scopeKind, scopeReference: data.scopeReference, content: data.content, kind: data.kind, confidence: Number(data.confidence), importance: Number(data.importance), expiresAt: data.expiresAt || null }; const path = form.dataset.id ? `/api/v1/memories/${encodeURIComponent(form.dataset.id)}` : '/api/v1/memories'; await api(path, { method: form.dataset.id ? 'PUT' : 'POST', body: JSON.stringify(body) }); state.editingMemory = null; invalidate('memories', 'audit'); }
		if (form.dataset.form === 'relationship') { const intimacy = Number(data.intimacyValue || data.intimacy); await api(`/api/v1/relationships/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify({ intimacy, locked: form.elements.locked.checked }) }); state.editingRelationship = null; invalidate('relationships', 'audit'); }
    if (form.dataset.form === 'directive') { const body = { content: data.content, enabled: form.elements.enabled.checked }; if (form.dataset.id) await api(`/api/v1/runtime/directives/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/runtime/directives', { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID() }) }); state.editingDirective = null; invalidate('directives'); }
    if (form.dataset.form === 'security-rules') { await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ protectedRules: data.protectedRules }) }); state.config = await api('/api/v1/runtime/config'); invalidate('overview'); }
    if (form.dataset.form === 'worldbook') { const body = { comment: data.comment, keys: splitList(data.keys), secondaryKeys: splitList(data.secondaryKeys), content: data.content, priority: Number(data.priority), insertionOrder: Number(data.insertionOrder), tokenBudget: data.tokenBudget === '' ? null : Number(data.tokenBudget), enabled: form.elements.enabled.checked, constant: form.elements.constant.checked, selective: form.elements.selective.checked, position: data.position }; const base = `/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/worldbook`; if (form.dataset.id) await api(`${base}/${encodeURIComponent(form.dataset.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify(body) }); else await api(`${base}?namespace=default`, { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID() }) }); state.editingWorldbook = null; invalidate('worldbook'); }
    if (form.dataset.form === 'persona-sample') { const body = { sceneTags: splitList(data.sceneTags), relationshipStage: data.relationshipStage, emotion: data.emotion, context: data.context, candidateReplies: splitLines(data.candidateReplies), forbiddenExpressions: splitLines(data.forbiddenExpressions), source: data.source, weight: Number(data.weight), enabled: form.elements.enabled.checked }; const base = `/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/samples`; if (form.dataset.id) await api(`${base}/${encodeURIComponent(form.dataset.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify(body) }); else await api(`${base}?namespace=default`, { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID() }) }); state.editingPersonaSample = null; invalidate('personaSamples'); }
    if (form.dataset.form === 'persona-trait') { const body = { name: data.name, description: data.description, triggers: splitList(data.triggers), supports: splitList(data.supports), conflicts: splitList(data.conflicts), source: data.source, weight: Number(data.weight), enabled: form.elements.enabled.checked }; const base = `/api/v1/personas/${encodeURIComponent(form.dataset.persona)}/traits`; if (form.dataset.id) await api(`${base}/${encodeURIComponent(form.dataset.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify(body) }); else await api(`${base}?namespace=default`, { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID() }) }); state.editingPersonaTrait = null; invalidate('personaTraits'); }
    if (form.dataset.form === 'document') { let metadata; try { metadata = JSON.parse(data.metadata || '{}'); } catch { throw new Error('元数据必须是有效的 JSON 对象'); } if (!metadata || Array.isArray(metadata) || typeof metadata !== 'object') throw new Error('元数据必须是 JSON 对象'); const body = { title: data.title, sourceUri: data.sourceUri, content: data.content, metadata }; const ns = state.config.knowledgeNamespace || 'default'; if (form.dataset.id) await api(`/api/v1/knowledge/documents/${encodeURIComponent(form.dataset.id)}?namespace=${encodeURIComponent(ns)}`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/knowledge/documents', { method: 'POST', body: JSON.stringify({ ...body, id: crypto.randomUUID(), namespace: ns }) }); state.editingDocument = null; invalidate('documents', 'overview'); }
    if (form.dataset.form === 'tool') { let inputSchema; try { inputSchema = JSON.parse(data.inputSchema || '{}'); } catch { throw new Error('输入参数 Schema 必须是有效的 JSON 对象'); } if (!inputSchema || Array.isArray(inputSchema) || typeof inputSchema !== 'object') throw new Error('输入参数 Schema 必须是 JSON 对象'); const body = { name: data.name, description: data.description, capabilities: splitList(data.capabilities), riskLevel: Number(data.riskLevel), enabled: form.elements.enabled.checked, adapterRef: data.adapterRef, allowedAuthorities: [form.elements.authorityMember.checked ? 'member' : '', form.elements.authorityAdmin.checked ? 'admin' : ''].filter(Boolean), approvalMode: data.approvalMode, timeoutSeconds: Number(data.timeoutSeconds), inputSchema }; if (form.dataset.id) await api(`/api/v1/tools/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/tools', { method: 'POST', body: JSON.stringify({ id: data.id, ...body }) }); state.editingTool = null; invalidate('tools', 'audit'); }
    if (form.dataset.form === 'skill') { const attachmentKinds = [form.elements.attachmentImage.checked ? 'image' : '', form.elements.attachmentAudio.checked ? 'audio' : '', form.elements.attachmentVideo.checked ? 'video' : '', form.elements.attachmentFile.checked ? 'file' : ''].filter(Boolean); const body = { name: data.name, description: data.description, instructions: data.instructions, enabled: form.elements.enabled.checked, activationMode: data.activationMode, triggers: splitList(data.triggers), attachmentKinds, requiredTools: splitList(data.requiredTools), requiredMcpServers: splitList(data.requiredMcpServers), allowedAuthorities: [form.elements.authorityMember.checked ? 'member' : '', form.elements.authorityAdmin.checked ? 'admin' : ''].filter(Boolean), personaIds: splitList(data.personaIds), priority: Number(data.priority) }; if (form.dataset.id) await api(`/api/v1/skills/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/skills', { method: 'POST', body: JSON.stringify({ id: data.id, ...body }) }); state.editingSkill = null; invalidate('skills', 'audit'); }
    if (form.dataset.form === 'mcp') { const body = { name: data.name, transport: data.transport, endpoint: data.endpoint, command: data.command, args: splitList(data.args), toolPrefix: data.toolPrefix, enabled: form.elements.enabled.checked, secretRef: data.secretRef, allowedTools: splitList(data.allowedTools), allowedAuthorities: [form.elements.authorityMember.checked ? 'member' : '', form.elements.authorityAdmin.checked ? 'admin' : ''].filter(Boolean), approvalMode: data.approvalMode, timeoutSeconds: Number(data.timeoutSeconds) }; if (form.dataset.id) await api(`/api/v1/mcp/servers/${encodeURIComponent(form.dataset.id)}`, { method: 'PUT', body: JSON.stringify(body) }); else await api('/api/v1/mcp/servers', { method: 'POST', body: JSON.stringify({ id: data.id, ...body }) }); state.editingMcp = null; invalidate('mcp', 'audit'); }
    if (form.dataset.form === 'learning') { await api('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ learningEnabled: form.elements.enabled.checked, learningTopics: splitList(data.topics), learningIntervalHours: Number(data.interval) }) }); state.config = await api('/api/v1/runtime/config'); invalidate('overview', 'candidates'); }
    if (form.dataset.form === 'routing-control') { const locks = {}; for (const lane of (state.lanes.items || state.lanes || [])) { const endpointId = data[`lock-${lane.lane}`]; if (endpointId) locks[lane.lane] = endpointId; } await api('/api/v1/routing/control', { method: 'PUT', body: JSON.stringify({ mode: data.mode, locks }) }); state.control = await api('/api/v1/routing/control'); invalidate('overview'); }
    if (form.dataset.form === 'lane') { await api('/api/v1/routing/lanes', { method: 'PUT', body: JSON.stringify({ lane: form.dataset.lane, requiredCapabilities: splitList(data.required), preferredCapabilities: splitList(data.preferred) }) }); invalidate('lanes'); }
    await render();
  } catch (error) {
    if (form.dataset.form === 'admin-login') renderLogin(error.message);
    else alert(error.message);
  }
});

bootstrap().catch((error) => { if (error.authRequired) return; setHealth('Core 不可用', false); app.innerHTML = `<div class="notice error">${esc(error.message)}</div>`; });
