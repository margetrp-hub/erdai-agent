export type ApiEnvelope<T> = {
  data?: T;
  error?: {
    code?: string;
    message?: string;
  };
};

export type Overview = {
  schemaVersion?: number;
  counts?: Record<string, number>;
  models?: {
    configured?: number;
    healthy?: number;
    unhealthy?: number;
    unknown?: number;
  };
  routing?: {
    mode?: string;
    lockedLaneCount?: number;
  };
};

export type MediaCapabilityStatus = {
  status?: 'available' | 'degraded' | 'unverified' | string;
  reason?: string;
  probeHealthy?: boolean;
  successCount?: number;
  failureCount?: number;
  lastAttemptAt?: string;
};

export type Observability = {
  quality?: TaskQualityStats;
  media?: {
    image?: MediaCapabilityStatus;
    video?: MediaCapabilityStatus;
  };
  retrieval?: {
    enabled?: boolean;
    documentCount?: number;
    chunkCount?: number;
    embeddingCount?: number;
    queryCount24h?: number;
    embeddingQueryCount24h?: number;
    fallbackCount24h?: number;
    lastQueryAt?: string;
  };
  memory?: {
    storedCount?: number;
    accessedCount?: number;
    recallCount24h?: number;
    lastWriteAt?: string;
    lastAccessAt?: string;
    lastRecallAt?: string;
  };
};

export type TaskQualityStats = {
  windowHours: number;
  generatedRuns: number;
  deliveredRuns: number;
  qualityOperations: number;
  passed: number;
  failed: number;
  unverified: number;
  generationRounds: number;
  accepted: number;
  corrections: number;
  redos: number;
  rejected: number;
  adminFeedback: number;
  sampleLimited: boolean;
};

export type MediaQualityReport = {
  mediaType: string;
  operationId: string;
  status: string;
  selectedAttempt: number;
  selectionReason?: string;
  attempts: Array<{
    attempt: number;
    generationStatus: string;
    artifactNames: string[];
    assessment: {
      status: string;
      reason?: string;
      identityIssues: string[];
      constraintIssues: string[];
      qualityIssues: string[];
    };
    providerCostKnown: boolean;
  }>;
};

export type TaskDetail = {
  runId: string;
  steps: Array<{ id: string; kind: string; name: string; status: string; attempts: number; errorCode?: string }>;
  artifacts: Array<{ id?: number; kind: string; name: string; mimeType: string; contentUrl?: string }>;
  optimization: {
    intent?: JsonMap;
    visualPlans: JsonMap[];
    mediaQuality: MediaQualityReport[];
    feedback: Array<{ eventId: string; kind: string; source: string; createdAt: string }>;
  };
};

export type RuntimeConfig = {
  activePersonaId?: string;
  learningEnabled?: boolean;
};

export type InstallationCheck = {
  id: string;
  label: string;
  configured: boolean;
  required: boolean;
  source?: string;
  detail?: string;
};

export type InstallationStatus = {
  lifecycle: 'active' | 'setup_required' | string;
  ready: boolean;
  configuredCount: number;
  requiredCount: number;
  checks: InstallationCheck[];
  checkedAt?: string;
};

export type StableUpdate = {
  currentVersion: string;
  latestVersion?: string;
  releaseTag?: string;
  updateAvailable: boolean;
  upgradeReady?: boolean;
  repository: string;
  releaseUrl?: string;
  publishedAt?: string;
  releaseNotes?: string;
  assetName?: string;
  assetUrl?: string;
  assetDigest?: string;
  assetSize?: number;
  checkedAt?: string;
};

export type UpdateStatus = {
  agentConfigured: boolean;
  agentReady: boolean;
  state: 'unavailable' | 'idle' | 'pending' | 'running' | 'succeeded' | 'failed' | string;
  requestId?: string;
  targetVersion?: string;
  message?: string;
  requestedAt?: string;
  startedAt?: string;
  finishedAt?: string;
  heartbeatAt?: string;
};

export type Persona = {
  id: string;
  name?: string;
  description?: string;
  visualDescription?: string;
  avatarDataUri?: string;
};

export type AgentInstance = {
  id: string;
  displayName?: string;
  personaId?: string;
  policyTemplateId?: string;
  memoryNamespace?: string;
  enabled?: boolean;
  updatedAt?: string;
};

export type PersonaVisualReference = {
  id: string;
  personaId?: string;
  libraryId?: string;
  mediaType?: 'image' | 'video' | string;
  mimeType?: string;
  originalName?: string;
  byteSize?: number;
  category?: string;
  label?: string;
  promptNotes?: string;
  isPrimary?: boolean;
  enabled?: boolean;
  sortOrder?: number;
  contentUrl?: string;
  createdAt?: string;
  updatedAt?: string;
};

export type AppearanceLibrary = {
  id: string;
  namespace?: string;
  name?: string;
  description?: string;
  visualDescription?: string;
  outfitLength?: 'auto' | 'short' | 'long';
  sourcePersonaId?: string;
  enabled?: boolean;
  referenceCount?: number;
  personaCount?: number;
  previewUrl?: string;
  createdAt?: string;
  updatedAt?: string;
};

export type DashboardData = {
  overview: Overview;
  observability: Observability;
  config: RuntimeConfig;
  installation: InstallationStatus;
  personas: {
    items?: Persona[];
  };
  agentInstances: {
    items?: AgentInstance[];
  };
};

export type JsonMap = Record<string, unknown>;
export type ModuleData = Record<string, unknown>;

export class ApiError extends Error {
  readonly authRequired: boolean;
  readonly status: number;

  constructor(message: string, authRequired = false, status = 0) {
    super(message);
    this.name = 'ApiError';
    this.authRequired = authRequired;
    this.status = status;
  }
}

export async function apiRequest<T>(path: string, options?: RequestInit): Promise<T> {
  const isFormData = typeof FormData !== 'undefined' && options?.body instanceof FormData;
  const response = await fetch(path, {
    ...options,
    headers: {
      accept: 'application/json',
      ...(options?.body && !isFormData ? { 'content-type': 'application/json' } : {}),
      ...(options?.headers || {}),
    },
  }).catch((cause: unknown) => {
    if (cause instanceof Error && cause.name === 'AbortError') throw cause;
    throw new ApiError('无法连接服务器，请检查网络后重试。');
  });
  if (response.status === 204) return undefined as T;
  const payload = (await response.json().catch(() => null)) as ApiEnvelope<T> | null;
  if (!response.ok) {
    const detail = payload?.error?.message || payload?.error?.code || '';
    const message = response.status === 401 ? '登录已失效，请重新登录后再保存。'
      : response.status === 403 ? '当前登录没有执行这项操作的权限。'
        : response.status >= 500 ? `服务暂时无法处理请求，请稍后重试。${detail ? `（${detail}）` : ''}`
          : detail || `请求失败（HTTP ${response.status}）`;
    throw new ApiError(message, response.status === 401, response.status);
  }
  if (!payload || typeof payload !== 'object' || !Object.hasOwn(payload, 'data')) {
    throw new ApiError('服务器返回的内容不完整，请刷新或重试。', false, response.status);
  }
  return payload.data as T;
}

export function collectionItems<T = JsonMap>(value: unknown): T[] {
  if (Array.isArray(value)) return value as T[];
  if (value && typeof value === 'object') {
    const items = (value as { items?: unknown }).items;
    if (Array.isArray(items)) return items as T[];
  }
  return [];
}

export async function apiCollection<T = JsonMap>(path: string, signal?: AbortSignal): Promise<{ items: T[]; total: number }> {
  const url = new URL(path, 'http://localhost');
  url.searchParams.set('limit', '100');
  let offset = 0;
  const items: T[] = [];
  for (;;) {
    url.searchParams.set('offset', String(offset));
    const page = await apiRequest<unknown>(url.pathname + url.search, { signal });
    const batch = collectionItems<T>(page);
    const total = page && !Array.isArray(page) && typeof page === 'object' ? Number((page as JsonMap).total) : NaN;
    items.push(...batch);
    const hasMore = page && !Array.isArray(page) && typeof page === 'object' && (page as JsonMap).hasMore === true;
    if (Number.isFinite(total) ? items.length >= total : !hasMore) return { items, total: Number.isFinite(total) ? total : items.length };
    if (!batch.length || items.length >= 10000) throw new ApiError('记录数量过多或分页数据发生变化，请缩小筛选范围后重试。');
    offset += batch.length;
  }
}

export async function readSettingsRecord(path: string, source: JsonMap = {}): Promise<JsonMap> {
  const route = path.split('?')[0];
  const id = decodeURIComponent(route.split('/').pop() || '');
  let items: JsonMap[] | undefined;
  if (route.startsWith('/api/v1/persona-bindings/')) items = collectionItems(await apiRequest('/api/v1/persona-bindings'));
  if (route.startsWith('/api/v1/plugins/')) items = collectionItems(await apiRequest('/api/v1/plugins'));
  if (route.startsWith('/api/v1/provider-connections/')) items = (await apiCollection('/api/v1/provider-connections')).items;
  if (/^\/api\/v1\/(memories|relationships)\//.test(route)) {
    const collection = route.slice(0, route.lastIndexOf('/'));
    items = (await apiCollection(`${collection}?personaId=${encodeURIComponent(String(source.personaId || ''))}`)).items;
  }
  if (route === '/api/v1/routing/lanes') {
    items = collectionItems(await apiRequest(route));
    const record = items.find((item) => item.lane === source.lane);
    if (!record) throw new ApiError('这个模型分工已不存在，请刷新列表。');
    return record;
  }
  if (items) {
    const record = items.find((item) => item.id === id);
    if (!record) throw new ApiError('这条记录已不存在，请刷新列表。');
    return record;
  }
  return apiRequest<JsonMap>(path);
}

export async function getSession() {
  try {
    const session = await apiRequest<{ authenticated?: boolean }>('/auth/session');
    return session?.authenticated === true;
  } catch (error) {
    if (error instanceof ApiError && (error.status === 401 || error.status === 403)) return false;
    throw error;
  }
}

export function login(credentials: { token?: string; username?: string; password?: string }) {
  return apiRequest<{ authenticated: boolean }>('/auth/login', {
    method: 'POST',
    body: JSON.stringify(credentials),
  });
}

export function logout() {
  return apiRequest<{ loggedOut: boolean }>('/auth/logout', { method: 'POST' });
}

export async function loadDashboard(): Promise<DashboardData> {
  const [overview, observability, config, installation, personas, agentInstances] = await Promise.all([
    apiRequest<Overview>('/api/v1/overview'),
    apiRequest<Observability>('/api/v1/observability'),
    apiRequest<RuntimeConfig>('/api/v1/runtime/config'),
    apiRequest<InstallationStatus>('/api/v1/installation/status'),
    apiCollection<Persona>('/api/v1/personas?namespace=default'),
    apiRequest<{ items?: AgentInstance[] }>('/api/v1/agent-instances'),
  ]);
  return { overview, observability, config, installation, personas, agentInstances };
}

async function loadEntries(entries: Array<[string, string]>): Promise<ModuleData> {
  const values = await Promise.all(entries.map(([, path]) => path.includes('limit=100') ? apiCollection(path) : apiRequest<unknown>(path)));
  return Object.fromEntries(entries.map(([key], index) => [key, values[index]]));
}

export async function loadModuleData(view: string, preferredPersonaId?: string): Promise<ModuleData> {
  switch (view) {
    case 'operations':
      return loadEntries([
        ['runs', '/api/v1/runs'],
        ['runStats', '/api/v1/runs/stats?hours=24'],
        ['observability', '/api/v1/observability'],
        ['usageStats', '/api/v1/usage/stats?hours=24'],
        ['audit', '/api/v1/audit?limit=100'],
        ['shadow', '/api/v1/shadow/interactions?limit=50'],
      ]);
    case 'runtime':
      return loadEntries([
        ['platforms', '/api/v1/platforms'],
        ['platformRuntime', '/api/v1/platforms/runtime-status'],
        ['agentInstances', '/api/v1/agent-instances'],
        ['agentPolicyTemplates', '/api/v1/agent-policy-templates'],
        ['agentInstanceRoutes', '/api/v1/agent-instance-routes'],
        ['agentInstanceCapabilities', '/api/v1/agent-instance-capabilities'],
        ['personas', '/api/v1/personas?namespace=default&limit=100'],
        ['configLayers', '/api/v1/config/layers'],
        ['platformCatalog', '/api/v1/platforms/catalog'],
      ]);
    case 'roles': {
      const personas = await apiCollection<Persona>('/api/v1/personas?namespace=default');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [personaBindings, personaProfiles, appearanceLibraries, appearanceBindings] = await Promise.all([
        apiRequest<unknown>('/api/v1/persona-bindings'),
        apiRequest<unknown>('/api/v1/personas/runtime-profiles'),
        apiRequest<unknown>('/api/v1/appearance-libraries?namespace=default'),
        Promise.all((personas.items || []).map(async (persona) => [
          persona.id,
          await apiRequest<{ libraryId?: string }>(`/api/v1/personas/${encodeURIComponent(persona.id)}/appearance-library?namespace=default`),
        ] as const)),
      ]);
      const appearanceAssignments = Object.fromEntries(appearanceBindings.map(([id, binding]) => [id, binding.libraryId || '']));
      const selectedAppearanceLibraryId = appearanceAssignments[personaId] || '';
      const visualReferences = selectedAppearanceLibraryId
        ? await apiRequest<{ items?: PersonaVisualReference[] }>(`/api/v1/appearance-libraries/${encodeURIComponent(selectedAppearanceLibraryId)}/references?namespace=default`)
        : { items: [] };
      return { personas, personaBindings, personaProfiles, appearanceLibraries, appearanceAssignments, visualReferences, selectedAppearanceLibraryId, selectedPersonaId: personaId };
    }
    case 'memories': {
      const personas = await apiCollection<Persona>('/api/v1/personas?namespace=default');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [memories, relationships] = await Promise.all([
        personaId ? apiCollection(`/api/v1/memories?personaId=${encodeURIComponent(personaId)}`) : { items: [] },
        personaId ? apiCollection(`/api/v1/relationships?personaId=${encodeURIComponent(personaId)}`) : { items: [] },
      ]);
      return { personas, memories, relationships, selectedPersonaId: personaId };
    }
    case 'worldbook': {
      const personas = await apiCollection<Persona>('/api/v1/personas?namespace=default');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const worldbook = personaId ? await apiCollection(`/api/v1/personas/${encodeURIComponent(personaId)}/worldbook?namespace=default`) : { items: [] };
      return { personas, worldbook, selectedPersonaId: personaId };
    }
    case 'samples': {
      const personas = await apiCollection<Persona>('/api/v1/personas?namespace=default');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [samples, traits] = await Promise.all([
        personaId ? apiCollection(`/api/v1/personas/${encodeURIComponent(personaId)}/samples?namespace=default`) : { items: [] },
        personaId ? apiCollection(`/api/v1/personas/${encodeURIComponent(personaId)}/traits?namespace=default`) : { items: [] },
      ]);
      return { personas, samples, traits, selectedPersonaId: personaId };
    }
    case 'knowledge':
      return loadEntries([
        ['documents', '/api/v1/knowledge/documents?namespace=default&limit=100'],
        ['candidates', '/api/v1/runtime/knowledge-candidates?limit=100'],
        ['integrations', '/api/v1/integrations'],
        ['config', '/api/v1/runtime/config'],
      ]);
    case 'skills':
      return loadEntries([['skills', '/api/v1/skills']]);
    case 'plugins':
      return loadEntries([
        ['plugins', '/api/v1/plugins'],
        ['trustedAdapters', '/api/v1/trusted-adapters'],
      ]);
    case 'tools':
      return loadEntries([
        ['tools', '/api/v1/tools'],
        ['mcp', '/api/v1/mcp/servers'],
      ]);
    case 'integrations':
      return loadEntries([
        ['integrations', '/api/v1/integrations'],
        ['credentials', '/api/v1/credentials'],
        ['platforms', '/api/v1/platforms'],
        ['platformCatalog', '/api/v1/platforms/catalog'],
        ['platformRuntime', '/api/v1/platforms/runtime-status'],
        ['models', '/api/v1/model-endpoints'],
        ['providerConnections', '/api/v1/provider-connections'],
      ]);
    case 'models':
      return loadEntries([
        ['models', '/api/v1/model-endpoints'],
        ['providerConnections', '/api/v1/provider-connections'],
        ['providerDrivers', '/api/v1/provider-drivers'],
        ['health', '/api/v1/model-health'],
      ]);
    case 'routing':
      return loadEntries([
        ['lanes', '/api/v1/routing/lanes'],
        ['control', '/api/v1/routing/control'],
        ['models', '/api/v1/model-endpoints'],
      ]);
    case 'devices':
      return loadEntries([
        ['devices', '/api/v1/devices'],
        ['realtimeSessions', '/api/v1/realtime/sessions'],
      ]);
    case 'security':
      return loadEntries([
        ['integrations', '/api/v1/integrations'],
        ['config', '/api/v1/runtime/config'],
        ['directives', '/api/v1/runtime/directives?limit=100'],
      ]);
    case 'system':
      return loadEntries([
        ['config', '/api/v1/runtime/config'],
        ['mediaQuotas', '/api/v1/runtime/media-quotas'],
        ['configLayers', '/api/v1/config/layers'],
        ['integrations', '/api/v1/integrations'],
      ]);
    default:
      return {};
  }
}
