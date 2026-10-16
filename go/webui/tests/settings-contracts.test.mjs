import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import test from 'node:test';
import { changedSettings, integrationFields, projectSettings, replacesSettings, sameSettings, settingsPayload, submittedSettingsMatch, writableFields } from '../src/lib/settings-contracts.ts';
import { apiCollection, apiRequest, getSession, readSettingsRecord } from '../src/lib/api.ts';

const modelPath = '/api/v1/model-endpoints/main';
const model = {
  id: 'main', provider: 'example', model: 'chat-v1', enabled: true, capabilities: ['chat', 'vision'],
  inputCostPerMillion: 1.2, outputCostPerMillion: 2.4, qualityScore: 0.8, priority: 7, maxContextTokens: 64000,
  executionKind: 'llm', adapterRef: '', connectionId: 'provider-one', usage: { calls: 42 },
  health: { healthy: true }, pricingSource: 'provider_feed', createdAt: 'yesterday', updatedAt: 'today',
};

test('integration write keys stay aligned with backend strict decoder', async () => {
  const go = await readFile(new URL('../../management_integrations_platforms.go', import.meta.url), 'utf8');
  for (const match of go.matchAll(/"([a-z_]+)": coreFieldSet\(([\s\S]*?)\),/g)) {
    const expected = [...match[2].matchAll(/"([^"\n]+)"/g)].map((item) => item[1]).sort();
    assert.deepEqual([...integrationFields[match[1]]].sort(), expected, match[1]);
  }
});

test('record write projections match the backend allowed fields', async () => {
  const cases = [
    ['config_store.go', 'corePersonaCreateFields', '/api/v1/personas', 'POST'],
    ['config_store.go', 'corePersonaUpdateFields', '/api/v1/personas/a', 'PUT'],
    ['config_store.go', 'coreWorldbookUpdateFields', '/api/v1/personas/a/worldbook/b', 'PUT'],
    ['skills.go', 'mgmtSkillUpdateFields', '/api/v1/skills/a', 'PUT'],
    ['management_models_routing.go', 'mgmtModelFields', modelPath, 'PUT'],
    ['management_memory.go', 'managedMemoryFields', '/api/v1/memories', 'POST'],
    ['management_tools_overview.go', 'mgmtToolUpdateFields', '/api/v1/tools/a', 'PUT'],
    ['management_knowledge_mcp.go', 'mgmtKnowledgeUpdateFields', '/api/v1/knowledge/documents/a', 'PUT'],
    ['management_knowledge_mcp.go', 'mgmtMCPUpdateFields', '/api/v1/mcp/servers/a', 'PUT'],
    ['persona_bindings.go', 'personaBindingFields', '/api/v1/persona-bindings/a', 'PUT'],
    ['persona_visual_references.go', 'personaVisualReferenceFields', '/api/v1/appearance-libraries/a/references/b', 'PUT'],
  ];
  for (const [file, variable, path, method] of cases) {
    const go = await readFile(new URL(`../../${file}`, import.meta.url), 'utf8');
    const match = go.match(new RegExp(`${variable}\\s*= coreFieldSet\\(([\\s\\S]*?)\\)`));
    assert.ok(match, variable);
    const expected = [...match[1].matchAll(/"([^"\n]+)"/g)].map((item) => item[1]).sort();
    assert.deepEqual(writableFields(path, method).sort(), expected, variable);
  }
});

test('model toggle retains writable configuration and removes read metadata', () => {
  const draft = projectSettings(modelPath, 'PUT', model);
  const sent = settingsPayload(modelPath, 'PUT', { ...draft, enabled: false });
  assert.equal(sent.enabled, false);
  for (const key of ['capabilities', 'inputCostPerMillion', 'outputCostPerMillion', 'priority', 'maxContextTokens', 'connectionId']) assert.deepEqual(sent[key], model[key]);
  for (const key of ['id', 'usage', 'health', 'createdAt', 'updatedAt', 'pricingSource']) assert.equal(Object.hasOwn(sent, key), false);
  assert.equal(replacesSettings(modelPath), true);
  assert.throws(() => settingsPayload(modelPath, 'PUT', model), /无法保存的字段/);
});

test('nested lists, maps, false, zero and null survive projection', () => {
  const quotaPath = '/api/v1/runtime/media-quotas';
  const quota = projectSettings(quotaPath, 'PUT', { imageDailyLimit: 0, videoDailyLimit: 100, trustedAdminBypass: false, whitelist: [], timezone: 'Asia/Shanghai' });
  assert.deepEqual(settingsPayload(quotaPath, 'PUT', quota), { imageDailyLimit: 0, videoDailyLimit: 100, trustedAdminBypass: false, whitelist: [] });
  for (const invalid of ['', -1, 101, 2.5, null]) assert.throws(() => settingsPayload(quotaPath, 'PUT', { ...quota, imageDailyLimit: invalid }));
  const worldbook = projectSettings('/api/v1/personas/a/worldbook/b', 'PUT', { id: 'b', tokenBudget: null, enabled: false, keys: [] });
  assert.deepEqual(worldbook, { tokenBudget: null, enabled: false, keys: [] });
  const metadata = { custom: { enabled: false, count: 0, tags: [], nullable: null } };
  assert.deepEqual(projectSettings('/api/v1/knowledge/documents/a', 'PUT', { title: 'a', metadata }).metadata, metadata);
  const locks = { chat: 'one', image: 'two' };
  assert.deepEqual(projectSettings('/api/v1/routing/control', 'PUT', { mode: 'manual', locks, updatedAt: 'today' }), { mode: 'manual', locks });
});

test('relationships and raw integration wrappers are converted to their write contracts', () => {
  assert.deepEqual(projectSettings('/api/v1/relationships/a', 'PUT', { id: 'a', senderRef: 'member', state: { intimacy: 81, intimacyLocked: true, replyCount: 42 } }), { intimacy: 81, locked: true });
  assert.deepEqual(projectSettings('/api/v1/integrations/image_policy', 'PUT', { id: 'image_policy', config: { enabled: false, selfieTypes: ['casual'], oldUnknownFlag: true } }), { enabled: false, selfieTypes: ['casual'] });
  const manifest = { category: 'extension', custom: { nested: [1, 2] }, capabilities: [] };
  assert.deepEqual(projectSettings('/api/v1/plugins', 'POST', { id: 'plugin', manifest }).manifest, manifest);
});

test('draft changes preserve unchanged arrays and detect meaningful concurrent changes only', () => {
  const baseline = { mode: 'manual', locks: { chat: 'one', image: 'two' } };
  assert.equal(sameSettings(baseline, { locks: { image: 'two', chat: 'one' }, mode: 'manual' }), true);
  assert.equal(sameSettings(baseline, { ...baseline, locks: { chat: 'three', image: 'two' } }), false);
  assert.deepEqual(changedSettings(baseline, { ...baseline, locks: { chat: 'three', image: 'two' } }), { locks: { chat: 'three', image: 'two' } });
  assert.equal(replacesSettings('/api/v1/persona-bindings/a'), true);
  assert.equal(replacesSettings('/api/v1/integrations/image_policy'), false);
});

test('save verification accepts server object defaults but catches changed values and retained deletions', () => {
  const submitted = { appid: '100000001', nested: { value: ' trimmed ', enabled: false } };
  const actual = { appid: '100000001', admin_openids: '', enable_group_c2c: true, nested: { value: 'trimmed', enabled: false, defaultMode: 'auto' } };
  assert.equal(submittedSettingsMatch(submitted, actual), true);
  assert.equal(submittedSettingsMatch(submitted, { ...actual, appid: 'other' }), false);
  assert.equal(submittedSettingsMatch(submitted, { ...actual, nested: { value: 'trimmed', enabled: true } }), false);
  assert.equal(submittedSettingsMatch({ retained: 1 }, { retained: 1, removed: 2 }, { retained: 1, removed: 2 }), false);
  assert.equal(submittedSettingsMatch({ retained: 1 }, { retained: 1, newDefault: 3 }, { retained: 1, removed: 2 }), true);
  assert.equal(submittedSettingsMatch(['chat', 'vision'], ['vision', 'chat']), false);
  assert.equal(submittedSettingsMatch([{ value: 1 }], [{ value: 1, extra: true }]), false);
  assert.equal(submittedSettingsMatch(null, {}), false);
  assert.equal(submittedSettingsMatch(0, false), false);
});

test('pagination reads records beyond 100 without discarding the envelope', async () => {
  const previous = globalThis.fetch;
  const offsets = [];
  globalThis.fetch = async (path) => {
    const offset = Number(new URL(path, 'http://localhost').searchParams.get('offset'));
    offsets.push(offset);
    return Response.json({ data: { total: 123, items: Array.from({ length: Math.min(100, 123 - offset) }, (_, index) => ({ id: String(offset + index) })) } });
  };
  try {
    const result = await apiCollection('/api/v1/personas?namespace=default');
    assert.equal(result.items.length, 123);
    assert.deepEqual(offsets, [0, 100]);
    assert.equal(result.items.at(-1).id, '122');
  } finally { globalThis.fetch = previous; }
});

test('pagination supports hasMore and preserves request cancellation', async () => {
  const previous = globalThis.fetch;
  const controller = new AbortController();
  const offsets = [];
  globalThis.fetch = async (path, options) => {
    assert.equal(options.signal, controller.signal);
    const offset = Number(new URL(path, 'http://localhost').searchParams.get('offset'));
    offsets.push(offset);
    return Response.json({ data: { hasMore: offset === 0, items: [{ id: String(offset) }] } });
  };
  try {
    const result = await apiCollection('/api/v1/tools', controller.signal);
    assert.deepEqual(result.items.map(item => item.id), ['0', '1']);
    assert.deepEqual(offsets, [0, 1]);
  } finally { globalThis.fetch = previous; }
});

test('knowledge detail fetch preserves full text and bindings use the collection read route', async () => {
  const previous = globalThis.fetch;
  const paths = [];
  const content = '完整正文\n'.repeat(1000);
  globalThis.fetch = async (path) => {
    paths.push(path);
    return Response.json({ data: path.includes('/knowledge/') ? { id: 'doc', title: '文档', content } : { items: [{ id: 'binding', personaId: 'doubao', enabled: true }] } });
  };
  try {
    assert.equal((await readSettingsRecord('/api/v1/knowledge/documents/doc?namespace=default')).content, content);
    assert.equal((await readSettingsRecord('/api/v1/persona-bindings/binding')).personaId, 'doubao');
    assert.deepEqual(paths, ['/api/v1/knowledge/documents/doc?namespace=default', '/api/v1/persona-bindings']);
  } finally { globalThis.fetch = previous; }
});

test('expired session errors identify reauthentication without discarding input', async () => {
  const previous = globalThis.fetch;
  globalThis.fetch = async () => Response.json({ error: { message: 'admin token required' } }, { status: 401 });
  try { await assert.rejects(apiRequest('/api/v1/runtime/config'), (error) => error.authRequired && error.status === 401 && /重新登录/.test(error.message)); }
  finally { globalThis.fetch = previous; }
});

test('successful empty or invalid API responses fail visibly instead of returning undefined', async () => {
  const previous = globalThis.fetch;
  try {
    for (const body of ['', '<html>proxy error</html>', 'null', '{}']) {
      globalThis.fetch = async () => new Response(body, { status: 200 });
      await assert.rejects(apiRequest('/api/v1/agent-instances'), /返回的内容不完整/);
    }
    globalThis.fetch = async () => new Response(null, { status: 204 });
    assert.equal(await apiRequest('/api/v1/test'), undefined);
  } finally { globalThis.fetch = previous; }
});

test('plugin save verification uses the available collection route', async () => {
  const previous = globalThis.fetch;
  const paths = [];
  globalThis.fetch = async (path) => {
    paths.push(path);
    return Response.json({ data: [{ id: 'external-one', name: '外部能力', enabled: false }] });
  };
  try {
    assert.equal((await readSettingsRecord('/api/v1/plugins/external-one')).name, '外部能力');
    assert.deepEqual(paths, ['/api/v1/plugins']);
  } finally { globalThis.fetch = previous; }
});

test('provider save verification uses collection read instead of unsupported item GET', async () => {
  const previous = globalThis.fetch;
  const paths = [];
  globalThis.fetch = async (path) => {
    paths.push(path);
    return Response.json({ data: { items: [{ id: 'provider-one', provider: 'test', apiBase: 'https://example.com/v1' }] } });
  };
  try {
    assert.equal((await readSettingsRecord('/api/v1/provider-connections/provider-one')).apiBase, 'https://example.com/v1');
    assert.equal(new URL(paths[0], 'http://localhost').pathname, '/api/v1/provider-connections');
  } finally { globalThis.fetch = previous; }
});

test('session checks distinguish signed out users from unavailable servers', async () => {
  const previous = globalThis.fetch;
  try {
    for (const status of [401, 403]) {
      globalThis.fetch = async () => Response.json({ error: { message: 'unauthorized' } }, { status });
      assert.equal(await getSession(), false);
    }
    for (const status of [500, 502]) {
      globalThis.fetch = async () => Response.json({ error: { message: 'upstream unavailable' } }, { status });
      await assert.rejects(getSession(), (error) => error.status === status && !error.authRequired);
    }
    globalThis.fetch = async () => { throw new TypeError('Failed to fetch'); };
    await assert.rejects(getSession(), /无法连接服务器/);
    globalThis.fetch = async () => Response.json({ data: { authenticated: true } });
    assert.equal(await getSession(), true);
  } finally { globalThis.fetch = previous; }
});
