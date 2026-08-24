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

export type RuntimeConfig = {
  activePersonaId?: string;
  learningEnabled?: boolean;
};

export type Persona = {
  id: string;
  name?: string;
  description?: string;
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
  personaId: string;
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

export type DashboardData = {
  overview: Overview;
  config: RuntimeConfig;
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

  constructor(message: string, authRequired = false) {
    super(message);
    this.name = 'ApiError';
    this.authRequired = authRequired;
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
  });
  const payload = (await response.json().catch(() => ({}))) as ApiEnvelope<T>;
  if (!response.ok) {
    throw new ApiError(payload.error?.message || `请求失败（HTTP ${response.status}）`, response.status === 401);
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

export async function getSession() {
  const response = await fetch('/auth/session', {
    headers: { accept: 'application/json' },
  });
  const payload = (await response.json().catch(() => ({}))) as ApiEnvelope<{ authenticated?: boolean }>;
  return response.ok && payload.data?.authenticated === true;
}

export function login(token: string) {
  return apiRequest<{ authenticated: boolean }>('/auth/login', {
    method: 'POST',
    body: JSON.stringify({ token }),
  });
}

export function logout() {
  return apiRequest<{ loggedOut: boolean }>('/auth/logout', { method: 'POST' });
}

export async function loadDashboard(): Promise<DashboardData> {
  const [overview, config, personas, agentInstances] = await Promise.all([
    apiRequest<Overview>('/api/v1/overview'),
    apiRequest<RuntimeConfig>('/api/v1/runtime/config'),
    apiRequest<{ items?: Persona[] }>('/api/v1/personas?namespace=default&limit=100'),
    apiRequest<{ items?: AgentInstance[] }>('/api/v1/agent-instances'),
  ]);
  return { overview, config, personas, agentInstances };
}

async function loadEntries(entries: Array<[string, string]>): Promise<ModuleData> {
  const values = await Promise.all(entries.map(([, path]) => apiRequest<unknown>(path)));
  return Object.fromEntries(entries.map(([key], index) => [key, values[index]]));
}

export async function loadModuleData(view: string, preferredPersonaId?: string): Promise<ModuleData> {
  switch (view) {
    case 'operations':
      return loadEntries([
        ['runs', '/api/v1/runs'],
        ['runStats', '/api/v1/runs/stats?hours=24'],
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
      const personas = await apiRequest<{ items?: Persona[] }>('/api/v1/personas?namespace=default&limit=100');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [personaBindings, personaProfiles, visualReferences] = await Promise.all([
        apiRequest<unknown>('/api/v1/persona-bindings'),
        apiRequest<unknown>('/api/v1/personas/runtime-profiles'),
        personaId
          ? apiRequest<{ items?: PersonaVisualReference[] }>(`/api/v1/personas/${encodeURIComponent(personaId)}/visual-references?namespace=default`)
          : Promise.resolve({ items: [] }),
      ]);
      return { personas, personaBindings, personaProfiles, visualReferences, selectedPersonaId: personaId };
    }
    case 'memories': {
      const personas = await apiRequest<{ items?: Persona[] }>('/api/v1/personas?namespace=default&limit=100');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [memories, relationships] = await Promise.all([
        apiRequest<unknown>(`/api/v1/memories?personaId=${encodeURIComponent(personaId)}&limit=100`),
        apiRequest<unknown>(`/api/v1/relationships?personaId=${encodeURIComponent(personaId)}&limit=100`),
      ]);
      return { personas, memories, relationships, selectedPersonaId: personaId };
    }
    case 'worldbook': {
      const personas = await apiRequest<{ items?: Persona[] }>('/api/v1/personas?namespace=default&limit=100');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const worldbook = await apiRequest<unknown>(`/api/v1/personas/${encodeURIComponent(personaId)}/worldbook?namespace=default&limit=100`);
      return { personas, worldbook, selectedPersonaId: personaId };
    }
    case 'samples': {
      const personas = await apiRequest<{ items?: Persona[] }>('/api/v1/personas?namespace=default&limit=100');
      const personaId = preferredPersonaId || personas.items?.[0]?.id || '';
      const [samples, traits] = await Promise.all([
        apiRequest<unknown>(`/api/v1/personas/${encodeURIComponent(personaId)}/samples?namespace=default&limit=100`),
        apiRequest<unknown>(`/api/v1/personas/${encodeURIComponent(personaId)}/traits?namespace=default&limit=100`),
      ]);
      return { personas, samples, traits, selectedPersonaId: personaId };
    }
    case 'knowledge':
      return loadEntries([
        ['documents', '/api/v1/knowledge/documents?namespace=default&limit=100'],
        ['candidates', '/api/v1/runtime/knowledge-candidates?limit=100'],
        ['integrations', '/api/v1/integrations'],
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
