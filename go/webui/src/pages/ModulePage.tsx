import {
  Activity,
  Archive,
  ArrowUpRight,
  Blocks,
  BookOpen,
  Check,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  Code2,
  Copy,
  Database,
  FileText,
  Gauge,
  HardDrive,
  History,
  KeyRound,
  Laptop,
  Link2,
  ListChecks,
  MessageSquare,
  Network,
  Pencil,
  Play,
  Power,
  Plus,
  RefreshCw,
  Route,
  Server,
  Settings2,
  ShieldCheck,
  Sparkles,
  Trash2,
  UserRound,
  Wrench,
  X,
} from 'lucide-react';
import { useEffect, useMemo, useState, type ReactNode } from 'react';
import type { ViewId } from '../components/AppShell';
import { Button, InfoDialog, Panel, PanelHeading, StatusDot } from '../components/ui';
import { VisualReferenceLibrary } from '../components/VisualReferenceLibrary';
import {
  apiRequest,
  collectionItems,
  loadModuleData,
  type JsonMap,
  type ModuleData,
  type AppearanceLibrary,
  type PersonaVisualReference,
} from '../lib/api';

type Accent = 'cyan' | 'rose' | 'amber' | 'violet' | 'green';
type TabId = string;

const icons: Record<string, typeof Activity> = {
  activity: Activity,
  archive: Archive,
  book: BookOpen,
  code: Code2,
  database: Database,
  file: FileText,
  gauge: Gauge,
  hardDrive: HardDrive,
  key: KeyRound,
  laptop: Laptop,
  link: Link2,
  list: ListChecks,
  message: MessageSquare,
  network: Network,
  route: Route,
  server: Server,
  settings: Settings2,
  shield: ShieldCheck,
  sparkles: Sparkles,
  user: UserRound,
  wrench: Wrench,
};

function asRecord(value: unknown): JsonMap {
  return value && typeof value === 'object' && !Array.isArray(value) ? value as JsonMap : {};
}

function text(value: unknown, fallback = '未设置') {
  if (value === undefined || value === null || value === '') return fallback;
  if (Array.isArray(value)) return value.join(' · ') || fallback;
  if (typeof value === 'object') return JSON.stringify(value);
  return String(value);
}

function number(value: unknown) {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? new Intl.NumberFormat('zh-CN').format(parsed) : '0';
}

function enabled(value: unknown) {
  return value === true || value === 'true' || value === 1;
}

function scoreBand(value: unknown) {
  const score = Math.max(0, Math.min(100, Number(value) || 0));
  if (score >= 80) return 'score-80';
  if (score >= 60) return 'score-60';
  if (score >= 40) return 'score-40';
  if (score >= 20) return 'score-20';
  return 'score-0';
}

function itemId(item: JsonMap) {
  return text(item.id, text(item.lane, 'record'));
}

function itemStatus(item: JsonMap) {
  if (enabled(item.enabled)) return { label: '启用', tone: 'ok' as const };
  if (item.enabled === false) return { label: '停用', tone: 'idle' as const };
  if (enabled(item.healthy) || ['connected', 'running', 'healthy'].includes(String(item.status))) {
    return { label: '正常', tone: 'ok' as const };
  }
  if (item.status) return { label: text(item.status), tone: 'warn' as const };
  return { label: '已登记', tone: 'idle' as const };
}

function endpoint(path: string, id?: string) {
  return id ? `${path}/${encodeURIComponent(id)}` : path;
}

function SectionTabs({
  tabs,
  active,
  onChange,
}: {
  tabs: Array<{ id: TabId; label: string; count?: number }>;
  active: TabId;
  onChange: (id: TabId) => void;
}) {
  return (
    <div className="module-tabs" role="tablist">
      {tabs.map((tab) => (
        <button
          className="module-tab"
          type="button"
          role="tab"
          aria-selected={active === tab.id}
          data-state={active === tab.id ? 'active' : 'inactive'}
          key={tab.id}
          onClick={() => onChange(tab.id)}
        >
          <span>{tab.label}</span>
          {tab.count === undefined ? null : <strong>{number(tab.count)}</strong>}
        </button>
      ))}
    </div>
  );
}

function MetricRail({ items }: { items: Array<{ label: string; value: ReactNode; note?: string; accent?: Accent }> }) {
  return (
    <div className="module-metric-rail">
      {items.map((item) => (
        <article className={`module-metric module-metric-${item.accent || 'cyan'}`} key={item.label}>
          <span>{item.label}</span>
          <strong>{item.value}</strong>
          {item.note ? <small>{item.note}</small> : null}
        </article>
      ))}
    </div>
  );
}

function RowActions({ children }: { children: ReactNode }) {
  return <div className="module-row-actions">{children}</div>;
}

function StatusLabel({ value }: { value: unknown }) {
  const status = itemStatus(asRecord(value));
  return (
    <span className="module-status">
      <StatusDot tone={status.tone} />
      {status.label}
    </span>
  );
}

function EmptyState({ text: message, action }: { text: string; action?: ReactNode }) {
  return (
    <div className="module-empty">
      <CircleAlert size={20} />
      <strong>{message}</strong>
      {action}
    </div>
  );
}

function RecordRow({
  item,
  title,
  icon = 'database',
  meta = [],
  detail,
  actions,
  current = false,
}: {
  item: JsonMap;
  title: string;
  icon?: string;
  meta?: string[];
  detail?: string;
  actions?: ReactNode;
  current?: boolean;
}) {
  const Icon = icons[icon] || Database;
  return (
    <article className={`module-record ${current ? 'is-current' : ''}`}>
      <span className="module-record-icon"><Icon size={16} /></span>
      <div className="module-record-main">
        <div className="module-record-title">
          <strong>{title}</strong>
          <StatusLabel value={item} />
        </div>
        {detail ? <p>{detail}</p> : null}
        <div className="module-record-meta">
          {meta.filter(Boolean).map((value) => <span key={value}>{value}</span>)}
        </div>
      </div>
      {actions ? <RowActions>{actions}</RowActions> : null}
    </article>
  );
}

function RecordTable({
  items,
  columns,
  empty = '暂无记录',
}: {
  items: JsonMap[];
  columns: Array<{ key: string; label: string }>;
  empty?: string;
}) {
  const pageSize = 8;
  const [page, setPage] = useState(1);
  const pages = Math.max(1, Math.ceil(items.length / pageSize));
  useEffect(() => setPage((current) => Math.min(current, pages)), [pages]);
  if (!items.length) return <EmptyState text={empty} />;
  const visibleItems = items.slice((page - 1) * pageSize, page * pageSize);
  return (
    <div className="module-table-shell">
      <div className="module-table-wrap">
        <table className="module-table">
          <thead><tr>{columns.map((column) => <th key={column.key}>{column.label}</th>)}</tr></thead>
          <tbody>
            {visibleItems.map((item, index) => (
              <tr key={itemId(item) + index}>
                {columns.map((column) => <td key={column.key}>{text(item[column.key], '-')}</td>)}
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <TablePager page={page} pages={pages} total={items.length} pageSize={pageSize} onPage={setPage} />
    </div>
  );
}

function TablePager({ page, pages, total, pageSize, onPage }: { page: number; pages: number; total: number; pageSize: number; onPage: (page: number) => void }) {
  if (total <= pageSize) return null;
  return (
    <div className="module-table-pager">
      <span>第 {page} / {pages} 页 · 共 {total} 条</span>
      <div>
        <Button variant="ghost" icon={<ChevronLeft size={14} />} aria-label="上一页" title="上一页" disabled={page <= 1} onClick={() => onPage(page - 1)} />
        <Button variant="ghost" icon={<ChevronRight size={14} />} aria-label="下一页" title="下一页" disabled={page >= pages} onClick={() => onPage(page + 1)} />
      </div>
    </div>
  );
}

function JsonEditorDialog({
  editor,
  onClose,
  onSave,
  saving,
}: {
  editor: { title: string; description: string; value: string } | null;
  onClose: () => void;
  onSave: (value: string) => void;
  saving: boolean;
}) {
  const [value, setValue] = useState(editor?.value || '');
  useEffect(() => setValue(editor?.value || ''), [editor]);
  return (
    <InfoDialog open={Boolean(editor)} onOpenChange={(open) => !open && onClose()} title={editor?.title || ''} description={editor?.description}>
      <form
        className="module-editor-form"
        onSubmit={(event) => {
          event.preventDefault();
          onSave(value);
        }}
      >
        <textarea className="json-editor" value={value} onChange={(event) => setValue(event.target.value)} spellCheck={false} />
        <div className="module-editor-actions">
          <Button type="button" variant="ghost" onClick={onClose}>取消</Button>
          <Button type="submit" variant="primary" icon={saving ? <RefreshCw className="spin" size={15} /> : <Check size={15} />} disabled={saving}>
            {saving ? '保存中' : '保存到 Core'}
          </Button>
        </div>
      </form>
    </InfoDialog>
  );
}

function PolicyPanel({
  title,
  description,
  value,
  endpointPath,
  accent,
  onEdit,
}: {
  title: string;
  description: string;
  value: unknown;
  endpointPath: string;
  accent: Accent;
  onEdit: (title: string, value: unknown, path: string, method?: 'PUT' | 'POST') => void;
}) {
  return (
    <Panel accent={accent}>
      <PanelHeading
        eyebrow="CORE POLICY"
        title={title}
        description={description}
        action={<Button variant="ghost" icon={<Pencil size={14} />} onClick={() => onEdit(title, value, endpointPath, 'PUT')}>编辑</Button>}
      />
      <div className="policy-preview">
        {Object.entries(asRecord(value)).slice(0, 8).map(([key, entry]) => (
          <div key={key}><span>{key}</span><strong>{text(entry, '未配置')}</strong></div>
        ))}
        {!Object.keys(asRecord(value)).length ? <span className="muted">尚未配置策略字段</span> : null}
      </div>
    </Panel>
  );
}

export function ModulePage({
  view,
  activeInstanceId,
  activePersonaId,
  refreshKey = 0,
  onNavigate,
}: {
  view: Exclude<ViewId, 'overview'>;
  activeInstanceId?: string;
  activePersonaId?: string;
  refreshKey?: number;
  onNavigate: (view: ViewId) => void;
}) {
  const [data, setData] = useState<ModuleData | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [reloadKey, setReloadKey] = useState(0);
  const [personaId, setPersonaId] = useState(activePersonaId || '');
  const [tab, setTab] = useState('');
  const [editor, setEditor] = useState<{ title: string; description: string; value: string; path: string; method: 'PUT' | 'POST' } | null>(null);
  const [saving, setSaving] = useState(false);
  const [notice, setNotice] = useState('');
  const [inspection, setInspection] = useState<{ title: string; description: string; value: unknown } | null>(null);

  useEffect(() => setPersonaId(activePersonaId || ''), [activePersonaId, view]);
  useEffect(() => {
    let alive = true;
    setLoading(true);
    setError('');
    loadModuleData(view, personaId || activePersonaId)
      .then((next) => {
        if (!alive) return;
        setData(next);
        if (view === 'memories' || view === 'worldbook' || view === 'samples') {
          setPersonaId(text(next.selectedPersonaId, personaId));
        }
      })
      .catch((cause) => {
        if (!alive) return;
        setError(cause instanceof Error ? cause.message : '加载模块失败');
      })
      .finally(() => alive && setLoading(false));
    return () => { alive = false; };
  }, [activePersonaId, personaId, refreshKey, reloadKey, view]);

  const items = (key: string) => collectionItems<JsonMap>(data?.[key]);
  const reload = () => setReloadKey((value) => value + 1);
  const openEditor = (title: string, value: unknown, path: string, method: 'PUT' | 'POST' = 'PUT', description = '编辑后由 Core 校验并持久化。') => {
    setEditor({ title, description, path, method, value: JSON.stringify(value ?? {}, null, 2) });
  };
  const saveEditor = async (value: string) => {
    let payload: unknown;
    try {
      payload = JSON.parse(value);
    } catch {
      setNotice('JSON 格式无效，未提交。');
      return;
    }
    setSaving(true);
    setNotice('');
    try {
      await apiRequest(editor?.path || '', { method: editor?.method || 'PUT', body: JSON.stringify(payload) });
      setEditor(null);
      setNotice('已保存到 Core。');
      reload();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : '保存失败');
    } finally {
      setSaving(false);
    }
  };
  const remove = async (path: string, message: string) => {
    if (!window.confirm(message)) return;
    try {
      await apiRequest(path, { method: 'DELETE' });
      setNotice('已删除。');
      reload();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : '删除失败');
    }
  };
  const toggle = async (path: string, value: boolean) => {
    try {
      await apiRequest(path, { method: 'PUT', body: JSON.stringify({ enabled: !value }) });
      setNotice('状态已更新。');
      reload();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : '更新失败');
    }
  };
  const content = loading ? <ModuleLoading /> : error ? <ModuleError message={error} onRetry={reload} /> : renderModule(view, data || {}, {
    activeInstanceId: activeInstanceId || '',
    activePersonaId: personaId,
    setPersonaId,
    tab,
    setTab,
    onNavigate,
    openEditor,
    remove,
    toggle,
    inspect: (title, description, value) => setInspection({ title, description, value }),
    reload,
  });

  return (
    <div className="view-stage module-stage">
      {notice ? <div className="module-notice"><CheckCircle2 size={15} />{notice}<button type="button" onClick={() => setNotice('')} aria-label="关闭提示"><X size={14} /></button></div> : null}
      {content}
      <JsonEditorDialog editor={editor} onClose={() => setEditor(null)} onSave={saveEditor} saving={saving} />
      <InfoDialog open={Boolean(inspection)} onOpenChange={(open) => !open && setInspection(null)} title={inspection?.title || ''} description={inspection?.description}>
        <div className="inspection-panel">
          {collectionItems<JsonMap>(asRecord(inspection?.value).tools).length ? <MetricRail items={[{ label: '发现工具', value: number(collectionItems(asRecord(inspection?.value).tools).length), accent: 'cyan' }, { label: '协议版本', value: text(asRecord(inspection?.value).protocolVersion, '-'), accent: 'green' }]} /> : null}
          {collectionItems<JsonMap>(asRecord(inspection?.value).tools).map((tool) => (
            <RecordRow key={itemId(tool)} item={tool} title={text(tool.name)} icon="wrench" detail={text(tool.description, '无说明')} meta={[enabled(tool.allowed) ? '已授权' : '未授权']} />
          ))}
          {!collectionItems(asRecord(inspection?.value).tools).length ? <pre className="inspection-json">{JSON.stringify(inspection?.value ?? {}, null, 2)}</pre> : null}
        </div>
      </InfoDialog>
    </div>
  );
}

function ModuleLoading() {
  return <div className="module-loading"><RefreshCw className="spin" size={22} /><strong>正在读取 Core 模块</strong><span>同步运行数据和配置边界</span></div>;
}

function ModuleError({ message, onRetry }: { message: string; onRetry: () => void }) {
  return <Panel accent="rose"><div className="module-error"><CircleAlert size={22} /><strong>{message}</strong><Button variant="secondary" onClick={onRetry}>重新读取</Button></div></Panel>;
}

type RenderContext = {
  activeInstanceId: string;
  activePersonaId: string;
  setPersonaId: (id: string) => void;
  tab: string;
  setTab: (tab: string) => void;
  onNavigate: (view: ViewId) => void;
  openEditor: (title: string, value: unknown, path: string, method?: 'PUT' | 'POST', description?: string) => void;
  remove: (path: string, message: string) => Promise<void>;
  toggle: (path: string, value: boolean) => Promise<void>;
  inspect: (title: string, description: string, value: unknown) => void;
  reload: () => void;
};

function renderModule(view: Exclude<ViewId, 'overview'>, data: ModuleData, ctx: RenderContext) {
  switch (view) {
    case 'operations': return <OperationsModule data={data} ctx={ctx} />;
    case 'runtime': return <RuntimeModule data={data} ctx={ctx} />;
    case 'roles': return <RolesModule data={data} ctx={ctx} />;
    case 'memories': return <MemoriesModule data={data} ctx={ctx} />;
    case 'worldbook': return <WorldbookModule data={data} ctx={ctx} />;
    case 'samples': return <SamplesModule data={data} ctx={ctx} />;
    case 'knowledge': return <KnowledgeModule data={data} ctx={ctx} />;
    case 'skills': return <SkillsModule data={data} ctx={ctx} />;
    case 'plugins': return <PluginsModule data={data} ctx={ctx} />;
    case 'tools': return <ToolsModule data={data} ctx={ctx} />;
    case 'integrations': return <IntegrationsModule data={data} ctx={ctx} />;
    case 'models': return <ModelsModule data={data} ctx={ctx} />;
    case 'routing': return <RoutingModule data={data} ctx={ctx} />;
    case 'devices': return <DevicesModule data={data} ctx={ctx} />;
    case 'security': return <SecurityModule data={data} ctx={ctx} />;
    case 'system': return <SystemModule data={data} ctx={ctx} />;
  }
}

function ModuleShell({ children, accent = 'cyan' }: { children: ReactNode; accent?: Accent }) {
  return <div className={`module-body module-body-${accent}`}>{children}</div>;
}

function PersonaSelector({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const people = collectionItems<JsonMap>(data.personas);
  if (!people.length) return null;
  const selected = ctx.activePersonaId || text(people[0].id);
  return (
    <label className="module-select">
      <span>当前角色</span>
      <select value={selected} onChange={(event) => ctx.setPersonaId(event.target.value)}>
        {people.map((persona) => <option value={text(persona.id)} key={text(persona.id)}>{text(persona.name, text(persona.id))}</option>)}
      </select>
    </label>
  );
}

function pluginTone(state: unknown): 'ok' | 'warn' | 'bad' | 'idle' {
  switch (String(state)) {
    case 'healthy':
    case 'ready':
      return 'ok';
    case 'needs_configuration':
    case 'degraded':
    case 'unverified':
      return 'warn';
    case 'unavailable':
      return 'bad';
    default:
      return 'idle';
  }
}

function pluginStateLabel(state: unknown) {
  switch (String(state)) {
    case 'healthy': return '运行正常';
    case 'ready': return '已就绪';
    case 'needs_configuration': return '待配置';
    case 'degraded': return '运行异常';
    case 'unverified': return '待真实任务验证';
    case 'unavailable': return '检查失败';
    case 'disabled': return '已停用';
    case 'registered': return '已登记';
    default: return '已登记';
  }
}

const PLUGIN_CONFIG_VIEWS: Record<string, ViewId> = {
  operations: 'operations', runtime: 'runtime', roles: 'roles', memories: 'memories',
  worldbook: 'worldbook', samples: 'samples', knowledge: 'knowledge', skills: 'skills',
  plugins: 'plugins', tools: 'tools', integrations: 'integrations', models: 'models',
  routing: 'routing', devices: 'devices', security: 'security', system: 'system',
};

function pluginConfigTarget(plugin: JsonMap, manifest: JsonMap) {
  const target = String(plugin.configView || manifest.configView || '').trim();
  return PLUGIN_CONFIG_VIEWS[target] || null;
}

function pluginOwnershipLabel(plugin: JsonMap) {
  if (text(plugin.source, 'builtin') === 'external' && text(asRecord(plugin.adapter).id, '') !== '') return '受信适配';
  if (text(plugin.source, 'builtin') === 'external') return '登记型';
  if (text(plugin.toggleMode, 'readonly') === 'policy_field') return '策略开关';
  if (text(plugin.integrationId, '') !== '') return 'Core 托管';
  return '资源型';
}

function pluginCategoryLabel(value: unknown) {
  const labels: Record<string, string> = {
    conversation: '对话', memory: '记忆', knowledge: '知识', media: '多模态',
    research: '研究', governance: '治理', monitoring: '监控', growth: '增长', extension: '扩展',
  };
  return labels[String(value)] || text(value, '扩展');
}

function PluginsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const plugins = collectionItems<JsonMap>(data.plugins);
  const trustedAdapters = collectionItems<JsonMap>(data.trustedAdapters);
  const [checking, setChecking] = useState('');
  const [health, setHealth] = useState<Record<string, JsonMap>>({});
  const [adapterHealth, setAdapterHealth] = useState<Record<string, JsonMap>>({});
  const [readiness, setReadiness] = useState<JsonMap | null>(null);
  const [pluginPage, setPluginPage] = useState(1);
  const enabledPlugins = plugins.filter((item) => enabled(item.enabled));
  const builtins = plugins.filter((item) => text(item.source, 'builtin') === 'builtin');
  const runCheck = async (plugin: JsonMap) => {
    const id = itemId(plugin);
    setChecking(id);
    try {
      const result = await apiRequest<JsonMap>(endpoint('/api/v1/plugins', id) + '/health');
      setHealth((previous) => ({ ...previous, [id]: result }));
    } catch (cause) {
      setHealth((previous) => ({ ...previous, [id]: { state: 'unavailable', message: cause instanceof Error ? cause.message : '检查失败' } }));
    } finally {
      setChecking('');
    }
  };
  const runAdapterCheck = async (adapter: JsonMap) => {
    const id = itemId(adapter);
    setChecking(`adapter:${id}`);
    try {
      const result = await apiRequest<JsonMap>(endpoint('/api/v1/trusted-adapters', id) + '/health');
      setAdapterHealth((previous) => ({ ...previous, [id]: result }));
    } catch (cause) {
      setAdapterHealth((previous) => ({ ...previous, [id]: { state: 'unavailable', message: cause instanceof Error ? cause.message : '检查失败' } }));
    } finally {
      setChecking('');
    }
  };
  const runAllChecks = async () => {
    setChecking('*');
    try {
      const result = await apiRequest<JsonMap>('/api/v1/plugins/readiness');
      const checked = collectionItems<JsonMap>(result.plugins);
      setHealth(Object.fromEntries(checked.map((item) => [text(item.pluginId), item])));
      setReadiness(result);
    } catch (cause) {
      setReadiness({ ready: false, state: 'unavailable', blocking: [], message: cause instanceof Error ? cause.message : '全量检查失败' });
    } finally {
      setChecking('');
    }
  };
  const blocking = collectionItems<JsonMap>(readiness?.blocking);
  const pluginPageSize = 8;
  const pluginPages = Math.max(1, Math.ceil(plugins.length / pluginPageSize));
  const visiblePlugins = plugins.slice((pluginPage - 1) * pluginPageSize, pluginPage * pluginPageSize);
  useEffect(() => setPluginPage((current) => Math.min(current, pluginPages)), [pluginPages]);
  return (
    <ModuleShell accent="cyan">
      <MetricRail items={[
        { label: '插件总数', value: number(plugins.length), note: 'registered packages', accent: 'cyan' },
        { label: '正在启用', value: number(enabledPlugins.length), note: 'enabled packages', accent: 'green' },
        { label: '内置插件', value: number(builtins.length), note: 'native Core', accent: 'violet' },
        { label: '受信适配器', value: number(trustedAdapters.length), note: 'Core managed', accent: 'amber' },
      ]} />
      <Panel accent="cyan">
        <PanelHeading
          eyebrow="PLUGIN REGISTRY"
          title="插件目录"
          description="配置探针和真实任务证据分开判断；待实测或最近失败不会显示成可用。"
          action={<div className="plugin-heading-actions">
            <Button variant="secondary" icon={checking === '*' ? <RefreshCw className="spin" size={15} /> : <ListChecks size={15} />} disabled={checking === '*'} onClick={runAllChecks}>检查全部</Button>
            <Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('登记外部能力包', {
              id: 'my-capability', name: '我的能力包', description: '', version: '0.1.0', author: '', enabled: true,
              manifest: { category: 'extension', capabilities: [], commands: [] },
            }, '/api/v1/plugins', 'POST', '只登记 manifest，不执行远程代码；需要运行适配器时再由 Core 增加受控实现。')}>登记能力包</Button>
          </div>}
        />
        {readiness ? <div className={`plugin-readiness ${enabled(readiness.ready) ? 'is-ready' : 'is-blocked'}`}>
          {enabled(readiness.ready) ? <CheckCircle2 size={17} /> : <CircleAlert size={17} />}
          <div>
            <strong>{enabled(readiness.ready) ? '已启用插件全部通过' : `${number(blocking.length)} 项需要处理`}</strong>
            <span>{enabled(readiness.ready) ? `检查时间 ${text(readiness.checkedAt)}` : blocking.map((item) => `${text(item.name, text(item.pluginId))}：${text(item.message, pluginStateLabel(item.state))}`).join('；')}</span>
          </div>
        </div> : null}
        {plugins.length ? <div className="module-table-shell plugin-table-shell">
          <div className="module-table-wrap">
            <table className="module-table plugin-table">
              <thead><tr><th>插件</th><th>状态</th><th>能力摘要</th><th>快捷开关</th><th>操作</th></tr></thead>
              <tbody>{visiblePlugins.map((plugin) => {
            const id = itemId(plugin);
            const manifest = asRecord(plugin.manifest);
            const pluginHealth = health[id];
            const state = pluginHealth?.state || plugin.state;
            const configTarget = pluginConfigTarget(plugin, manifest);
            const commands = collectionItems(manifest.commands);
            const capabilities = collectionItems(manifest.capabilities);
            const dependencies = collectionItems(manifest.dependencies);
            const resourceCount = pluginHealth?.resourceCount;
            const resourceCounts = asRecord(pluginHealth?.resourceCounts);
            const media = asRecord(pluginHealth?.media);
            const healthDetails = [
              text(pluginHealth?.message, ''),
              pluginHealth?.groupCount !== undefined ? `渠道 ${number(pluginHealth.groupCount)} 组` : '',
              pluginHealth?.bindingCount !== undefined ? `已绑定 QQ ${number(pluginHealth.bindingCount)} 个` : '',
              resourceCount !== undefined ? `资源 ${number(resourceCount)} 项${Object.keys(resourceCounts).length ? `（${Object.entries(resourceCounts).map(([key, value]) => `${key} ${number(value)}`).join(' · ')}）` : ''}` : '',
              Object.keys(media).length ? `真实任务成功 ${number(media.successCount)} / 失败 ${number(media.failureCount)}` : '',
            ].filter(Boolean).join(' · ');
            return (
              <tr key={id} className={enabled(plugin.enabled) ? 'is-enabled' : 'is-disabled'}>
                <td><div className="plugin-table-name"><div><Blocks size={16} /><strong>{text(plugin.name, id)}</strong><span className="module-badge">{text(plugin.source, 'builtin') === 'builtin' ? '内置' : '外部'}</span></div><span>{text(plugin.version, '未标版本')} · {text(plugin.author, '未知作者')}</span><p title={text(plugin.description)}>{text(plugin.description, '暂无插件说明。')}</p></div></td>
                <td><div className="plugin-table-state"><div><StatusDot tone={pluginTone(state)} /><strong>{pluginStateLabel(state)}</strong></div><span>{pluginCategoryLabel(manifest.category)} · {text(plugin.integrationId || asRecord(plugin.adapter).integrationId, '无接入策略')}</span>{healthDetails ? <small title={healthDetails}>{healthDetails}</small> : null}</div></td>
                <td><div className="plugin-table-capabilities"><div>{commands.slice(0, 2).map((command) => <span className="plugin-chip" key={String(command)}>{String(command)}</span>)}</div><p title={capabilities.map(String).join(' · ')}>{capabilities.slice(0, 3).map(String).join(' · ') || '未声明能力'}{capabilities.length > 3 ? ` · +${capabilities.length - 3}` : ''}</p>{dependencies.length ? <small>依赖：{dependencies.map(String).join(' · ')}</small> : null}</div></td>
                <td>{enabled(plugin.toggleable) ? <button type="button" role="switch" aria-checked={enabled(plugin.enabled)} aria-label={`${enabled(plugin.enabled) ? '停用' : '启用'}${text(plugin.name, id)}`} className={`plugin-quick-toggle ${enabled(plugin.enabled) ? 'is-on' : ''}`} onClick={() => ctx.toggle(endpoint('/api/v1/plugins', id), enabled(plugin.enabled))}><span aria-hidden="true" /><strong>{enabled(plugin.enabled) ? '已开启' : '已关闭'}</strong></button> : <span className="plugin-resource-badge">{pluginOwnershipLabel(plugin)}</span>}</td>
                <td><div className="plugin-table-actions"><Button variant="ghost" icon={checking === id ? <RefreshCw className="spin" size={14} /> : <Play size={14} />} aria-label="运行检查" title="运行检查" disabled={checking === id} onClick={() => runCheck(plugin)} /><Button variant="ghost" icon={<Settings2 size={14} />} aria-label="能力配置" title={configTarget ? '能力配置' : '无需配置'} disabled={!configTarget} onClick={() => { if (configTarget) ctx.onNavigate(configTarget); }} /><Button variant="ghost" icon={<ArrowUpRight size={14} />} aria-label="参考项目" title="参考项目" disabled={!collectionItems(manifest.references).length} onClick={() => { const reference = String(collectionItems(manifest.references)[0] || ''); if (reference) window.open(reference, '_blank', 'noopener,noreferrer'); }} /></div></td>
              </tr>
            );
          })}</tbody>
            </table>
          </div>
          <TablePager page={pluginPage} pages={pluginPages} total={plugins.length} pageSize={pluginPageSize} onPage={setPluginPage} />
        </div> : <EmptyState text="暂无插件。插件注册表为空时不会改变现有 Core 能力。" />}
      </Panel>
      <Panel accent="amber">
        <PanelHeading
          eyebrow="TRUSTED ADAPTERS"
          title="受信任适配器注册表"
          description="只有管理员在 Core 登记的适配器，才能把外部能力包绑定到现有集成；权限采用固定白名单。"
          action={<Button variant="primary" icon={<ShieldCheck size={15} />} onClick={() => ctx.openEditor('新增受信任适配器', {
            id: 'my-core-adapter', name: 'Core 适配器', pluginId: 'external-plugin-id', integrationId: 'memory_policy',
            version: '1.0.0', permissions: ['health.read', 'config.read'], enabled: true,
          }, '/api/v1/trusted-adapters', 'POST', '适配器只可绑定已登记的外部插件和 Core 已知集成；health.read 为必选权限。')}>新增适配器</Button>}
        />
        <div className="module-record-list">
          {trustedAdapters.map((adapter) => {
            const id = itemId(adapter);
            const currentHealth = adapterHealth[id];
            const state = currentHealth?.state || adapter.state;
            const permissions = collectionItems(adapter.permissions).map(String);
            const checkedAt = currentHealth?.checkedAt || adapter.lastCheckedAt;
            return <RecordRow
              key={id}
              item={{ ...adapter, state }}
              title={text(adapter.name, id)}
              icon="shield"
              detail={`${text(adapter.pluginName, text(adapter.pluginId))} → ${text(adapter.integrationId)}`}
              meta={[`v${text(adapter.version, '1.0.0')}`, permissions.join(' · '), text(currentHealth?.message || adapter.message), checkedAt ? `最近检查 ${text(checkedAt)}` : '尚未检查']}
              actions={<>
                <Button variant="ghost" icon={checking === `adapter:${id}` ? <RefreshCw className="spin" size={14} /> : <Play size={14} />} disabled={checking === `adapter:${id}`} onClick={() => runAdapterCheck(adapter)}>检查</Button>
                <Button variant="ghost" icon={<Power size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/trusted-adapters', id), enabled(adapter.enabled))}>{enabled(adapter.enabled) ? '停用' : '启用'}</Button>
                <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor(`编辑适配器 · ${text(adapter.name, id)}`, {
                  name: adapter.name, version: adapter.version, permissions: adapter.permissions, enabled: adapter.enabled,
                }, endpoint('/api/v1/trusted-adapters', id), 'PUT', '插件和 Core 集成绑定不可原地更换；需要更换时请删除后重新登记。')}>编辑</Button>
                <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/trusted-adapters', id), `确认删除受信任适配器“${text(adapter.name, id)}”？`)}>删除</Button>
              </>}
            />;
          })}
          {!trustedAdapters.length ? <EmptyState text="暂无受信任适配器。外部能力包会保持只读登记状态。" /> : null}
        </div>
      </Panel>
    </ModuleShell>
  );
}

function OperationsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const runs = collectionItems<JsonMap>(data.runs);
  const audit = collectionItems<JsonMap>(data.audit);
  const shadow = collectionItems<JsonMap>(data.shadow);
  const runStats = asRecord(data.runStats);
  const runStates = asRecord(runStats.runStates);
  const firstResponse = asRecord(runStats.firstResponse);
  const usage = asRecord(data.usageStats);
  const runCount = Object.values(runStates).reduce<number>((total, value) => total + Number(value || 0), 0);
  const modelCalls = Number(usage.calls || 0);
  const pricingComplete = enabled(usage.pricingComplete);
  const usageCost = Number(usage.estimatedCost || 0);
  const active = ctx.tab || 'runs';
  return (
    <ModuleShell>
      <MetricRail items={[
        { label: '24 小时运行', value: number(runCount), note: `${number(runStates.delivered || 0)} 次已投递` },
        { label: '首响中位数', value: firstResponse.samples ? `${number(firstResponse.p50Ms)} ms` : '暂无', note: `${number(firstResponse.samples || 0)} 个样本`, accent: 'green' },
        { label: '模型调用', value: number(modelCalls), note: '直达命令不调用模型', accent: 'violet' },
        { label: 'Token 消耗', value: number(usage.totalTokens), note: modelCalls === 0 ? '24 小时无模型调用' : pricingComplete ? `估算 $${usageCost.toFixed(6)}` : '成本待配置', accent: 'amber' },
      ]} />
      {modelCalls > 0 && !pricingComplete ? <p className="module-notice module-notice-inline"><CircleAlert size={15} />24 小时内有 {number(usage.unpricedCalls)} 次模型调用缺少价格，Token 统计有效，成本暂不作为结算依据。</p> : null}
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'runs', label: '最近运行', count: runs.length }, { id: 'audit', label: '审计事件', count: audit.length }, { id: 'shadow', label: '影子交互', count: shadow.length }]} />
      <Panel accent="cyan">
        <PanelHeading eyebrow="OPERATIONS" title={active === 'runs' ? '运行记录' : active === 'audit' ? '审计事件' : '影子交互'} description="来自 Core 的只读运行轨迹。" />
        {active === 'runs' ? (
          <div className="module-record-list">
            {runs.map((run) => <RecordRow key={itemId(run)} item={run} title={`${text(run.state)} · ${text(run.transport)}`} icon="activity" detail={text(run.routeReason, '未记录路由原因')} meta={[text(run.createdAt), `角色 ${text(run.personaId, '-')}`, `${number(run.totalDurationMs)} ms`, text(run.selectedModel, '模型未记录')]} actions={<Button variant="ghost" icon={<History size={14} />} onClick={async () => { try { const timeline = await apiRequest<unknown>(`/api/v1/runs/${encodeURIComponent(itemId(run))}`); ctx.inspect(`运行时间线 · ${itemId(run)}`, '按接收、上下文、路由、模型、质检、Outbox 和投递顺序记录。', timeline); } catch (cause) { window.alert(cause instanceof Error ? cause.message : '运行时间线读取失败'); } }}>时间线</Button>} />)}
            {!runs.length ? <EmptyState text="暂无运行记录。" /> : null}
          </div>
        ) : <RecordTable items={active === 'audit' ? audit : shadow} columns={active === 'audit' ? [{ key: 'createdAt', label: '时间' }, { key: 'actor', label: '操作者' }, { key: 'action', label: '动作' }, { key: 'targetType', label: '目标' }] : [{ key: 'createdAt', label: '时间' }, { key: 'transport', label: '通道' }, { key: 'lane', label: 'Lane' }, { key: 'selectedEndpointId', label: '模型端点' }, { key: 'messageLength', label: '输入长度' }]} empty={active === 'audit' ? '暂无审计记录。' : '暂无影子交互。'} />}
      </Panel>
    </ModuleShell>
  );
}

function RuntimeModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const instances = collectionItems<JsonMap>(data.agentInstances);
  const platforms = collectionItems<JsonMap>(data.platforms);
  const people = collectionItems<JsonMap>(data.personas);
  const templates = collectionItems<JsonMap>(data.agentPolicyTemplates);
  return (
    <ModuleShell accent="green">
      <MetricRail items={[{ label: '运行实例', value: number(instances.length), note: 'registered', accent: 'green' }, { label: '连接器', value: number(platforms.length), note: 'platforms', accent: 'cyan' }, { label: '启用实例', value: number(instances.filter((item) => enabled(item.enabled)).length), note: 'enabled', accent: 'rose' }, { label: '策略模板', value: number(templates.length), note: 'policy layers', accent: 'violet' }]} />
      <Panel accent="green">
        <PanelHeading eyebrow="RUNTIME GRAPH" title="运行链路" description="Core → 实例 → 角色 → 平台连接器。" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增运行实例', { id: '', displayName: '', personaId: text(people[0]?.id, ''), policyTemplateId: '', enabled: false }, '/api/v1/agent-instances', 'POST')}>新增实例</Button>} />
        <div className="architecture-strip">
          {['Core 主程序', '公共策略', '角色配置', '实例与路由'].map((label, index) => <span key={label}><strong>0{index + 1}</strong>{label}{index < 3 ? <ChevronRight size={15} /> : null}</span>)}
        </div>
        <div className="module-record-list">
          {instances.map((instance) => {
            const persona = people.find((item) => text(item.id) === text(instance.personaId));
            const connector = platforms.find((item) => text(item.id) === text(instance.connectorId));
            return <RecordRow key={itemId(instance)} item={instance} current={itemId(instance) === ctx.activeInstanceId} title={`${text(instance.displayName, itemId(instance))}${itemId(instance) === ctx.activeInstanceId ? ' · 当前' : ''}`} icon="activity" detail={`${text(persona?.name, text(instance.personaId, '未绑定角色'))} · ${text(instance.memoryNamespace, '默认记忆命名空间')}`} meta={[`策略 ${text(instance.policyTemplateId, '未绑定')}`, `连接器 ${text(connector?.displayName, '未绑定')}`, text(instance.updatedAt, '未更新')]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor(`实例配置 · ${text(instance.displayName)}`, instance, endpoint('/api/v1/agent-instances', itemId(instance)))}>编辑</Button><Button variant="ghost" icon={enabled(instance.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/agent-instances', itemId(instance)), enabled(instance.enabled))}>{enabled(instance.enabled) ? '停用' : '启用'}</Button></>} />;
          })}
          {!instances.length ? <EmptyState text="暂无运行实例。" /> : null}
        </div>
      </Panel>
      <Panel accent="cyan">
        <PanelHeading eyebrow="CONFIG LAYERS" title="配置分层" description="公共、角色和实例层按 Core 规则合并；这里显示只读来源。" />
        <RecordTable items={collectionItems<JsonMap>(data.configLayers)} columns={[{ key: 'layer', label: '层级' }, { key: 'source', label: '来源' }, { key: 'keys', label: '字段' }, { key: 'active', label: '状态' }]} empty="暂无配置分层数据。" />
      </Panel>
    </ModuleShell>
  );
}

function RolesModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const people = collectionItems<JsonMap>(data.personas);
  const bindings = collectionItems<JsonMap>(data.personaBindings);
  const profiles = collectionItems<JsonMap>(data.personaProfiles);
  const visualReferences = collectionItems<PersonaVisualReference>(data.visualReferences);
  const appearanceLibraries = collectionItems<AppearanceLibrary>(data.appearanceLibraries);
  const appearanceAssignments = asRecord(data.appearanceAssignments);
  const selectedAppearanceLibraryId = text(data.selectedAppearanceLibraryId, '');
  const [uploadingReferences, setUploadingReferences] = useState(false);
  const active = ctx.tab || 'cards';
  const activePersona = people.find((persona) => text(persona.id) === ctx.activePersonaId);
  const activePersonaName = text(activePersona?.name, ctx.activePersonaId || '当前角色');
  const referencePath = selectedAppearanceLibraryId
    ? `/api/v1/appearance-libraries/${encodeURIComponent(selectedAppearanceLibraryId)}/references`
    : '';
  const selectedAppearanceLibrary = appearanceLibraries.find((library) => library.id === selectedAppearanceLibraryId);
  const [savingOutfitLength, setSavingOutfitLength] = useState(false);
  const updateOutfitLength = async (outfitLength: 'auto' | 'short' | 'long') => {
    if (!selectedAppearanceLibraryId || savingOutfitLength) return;
    setSavingOutfitLength(true);
    try {
      await apiRequest(`/api/v1/appearance-libraries/${encodeURIComponent(selectedAppearanceLibraryId)}?namespace=default`, {
        method: 'PUT', body: JSON.stringify({ outfitLength }),
      });
      ctx.reload();
    } catch (cause) {
      window.alert(cause instanceof Error ? cause.message : '服装长度保存失败');
    } finally {
      setSavingOutfitLength(false);
    }
  };
  const uploadReferences = async (files: FileList) => {
    if (!ctx.activePersonaId || !selectedAppearanceLibraryId) return;
    setUploadingReferences(true);
    try {
      for (const file of Array.from(files)) {
        const isVideo = file.type.startsWith('video/');
        const form = new FormData();
        form.append('file', file);
        form.append('category', isVideo ? 'style' : 'identity');
        form.append('label', file.name.replace(/\.[^.]+$/, ''));
        form.append(
          'promptNotes',
          isVideo
            ? '仅参考动作、镜头、光线、服装和氛围，不复制人物脸部、身份、声音或具体场景。'
            : `身份锚点：固定${activePersonaName}的脸型、五官、发型、发色和成年感；不复制真实人物身份。`,
        );
        await apiRequest(referencePath, { method: 'POST', body: form });
      }
      ctx.reload();
    } catch (cause) {
      window.alert(cause instanceof Error ? cause.message : '参考素材导入失败');
    } finally {
      setUploadingReferences(false);
    }
  };
  const assignAppearanceLibrary = async (personaId: string, libraryId: string) => {
    if (!personaId || !libraryId) return;
    await apiRequest(`/api/v1/personas/${encodeURIComponent(personaId)}/appearance-library?namespace=default`, {
      method: 'PUT',
      body: JSON.stringify({ libraryId }),
    });
    ctx.reload();
  };
  const createAppearanceLibrary = async () => {
    const name = window.prompt('外观库名称');
    if (!name?.trim()) return;
    const visualDescription = window.prompt('外貌描述', text(activePersona?.visualDescription, ''));
    if (visualDescription === null) return;
    await apiRequest('/api/v1/appearance-libraries?namespace=default', {
      method: 'POST',
      body: JSON.stringify({ name: name.trim(), description: '供角色卡选择的共享外观库。', visualDescription: visualDescription.trim() }),
    });
    ctx.reload();
  };
  return (
    <ModuleShell accent="rose">
      <MetricRail items={[{ label: '角色卡', value: number(people.length), note: 'personas', accent: 'rose' }, { label: '外观库', value: number(appearanceLibraries.length), note: 'shared appearances', accent: 'cyan' }, { label: '启用绑定', value: number(bindings.filter((item) => item.enabled !== false).length), note: 'bindings', accent: 'green' }, { label: '运行档案', value: number(profiles.length), note: 'runtime profiles', accent: 'violet' }]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'cards', label: '角色卡', count: people.length }, { id: 'visual', label: '外观库', count: appearanceLibraries.length }, { id: 'bindings', label: '会话绑定', count: bindings.length }, { id: 'profiles', label: '运行档案', count: profiles.length }]} />
      {active === 'cards' ? <Panel accent="rose"><PanelHeading eyebrow="PERSONA REGISTRY" title="角色卡" description="角色卡负责性格与表达，并从独立外观库选择形象。" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新建角色卡', { id: crypto.randomUUID(), namespace: 'default', name: '', description: '', personality: '', scenario: '', systemPrompt: '', enabled: true }, '/api/v1/personas', 'POST')}>新建角色</Button>} /><div className="persona-modern-grid">{people.map((persona) => <PersonaCard key={itemId(persona)} persona={persona} active={text(persona.id) === ctx.activePersonaId} appearanceLibraries={appearanceLibraries} appearanceLibraryId={text(appearanceAssignments[itemId(persona)], '')} onAppearanceChange={(libraryId) => void assignAppearanceLibrary(itemId(persona), libraryId)} onEdit={() => ctx.openEditor(`编辑角色卡 · ${text(persona.name)}`, persona, `/api/v1/personas/${encodeURIComponent(itemId(persona))}?namespace=default`)} onActivate={() => apiRequest('/api/v1/runtime/config', { method: 'PUT', body: JSON.stringify({ activePersonaId: itemId(persona) }) }).then(ctx.reload)} onDelete={() => ctx.remove(`/api/v1/personas/${encodeURIComponent(itemId(persona))}?namespace=default`, '确定删除这个角色卡？')} />)}</div>{!people.length ? <EmptyState text="暂无角色卡。" /> : null}</Panel> : null}
      {active === 'visual' ? (
        <>
          <PersonaSelector data={data} ctx={ctx} />
          <VisualReferenceLibrary
            personaId={ctx.activePersonaId}
            personaName={activePersonaName}
            libraries={appearanceLibraries}
            libraryId={selectedAppearanceLibraryId}
            onLibraryChange={(libraryId) => void assignAppearanceLibrary(ctx.activePersonaId, libraryId)}
            onCreateLibrary={() => void createAppearanceLibrary()}
            onEditLibrary={(library) => ctx.openEditor('编辑外观库', { name: library.name || '', description: library.description || '', visualDescription: library.visualDescription || '', outfitLength: library.outfitLength || 'auto', enabled: library.enabled !== false }, `/api/v1/appearance-libraries/${encodeURIComponent(library.id)}?namespace=default`)}
            onOutfitLengthChange={updateOutfitLength}
            savingOutfitLength={savingOutfitLength}
            references={visualReferences}
            uploading={uploadingReferences}
            onUpload={uploadReferences}
            onEdit={(reference) => ctx.openEditor('编辑外观参考', reference, `${referencePath}/${encodeURIComponent(reference.id)}?namespace=default`)}
            onSetPrimary={(reference) => apiRequest(`${referencePath}/${encodeURIComponent(reference.id)}?namespace=default`, { method: 'PUT', body: JSON.stringify({ isPrimary: true, enabled: true }) }).then(ctx.reload)}
            onToggle={(reference) => ctx.toggle(`${referencePath}/${encodeURIComponent(reference.id)}?namespace=default`, reference.enabled !== false)}
            onDelete={(reference) => ctx.remove(
              `${referencePath}/${encodeURIComponent(reference.id)}?namespace=default`,
              selectedAppearanceLibrary?.personaCount && selectedAppearanceLibrary.personaCount > 1
                ? `这个外观库正被 ${selectedAppearanceLibrary.personaCount} 个角色使用，删除素材会同时影响这些角色。确定删除吗？`
                : '确定删除这份外观参考吗？',
            )}
          />
        </>
      ) : null}
      {active === 'bindings' ? <Panel accent="cyan"><PanelHeading eyebrow="PERSONA BINDINGS" title="会话绑定" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增角色绑定', { id: crypto.randomUUID(), personaId: text(people[0]?.id, ''), transport: '*', transportInstance: '*', conversationRef: '*', priority: 100, enabled: true }, '/api/v1/persona-bindings', 'POST')}>新增绑定</Button>} /><div className="module-record-list">{bindings.map((binding) => <RecordRow key={itemId(binding)} item={binding} title={`${text(binding.transport)} / ${text(binding.conversationRef)}`} icon="link" detail={`角色 ${text(people.find((item) => text(item.id) === text(binding.personaId))?.name, text(binding.personaId))}`} meta={[`实例 ${text(binding.transportInstance, '*')}`, `优先级 ${text(binding.priority)}`, text(binding.updatedAt, '未更新')]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑角色绑定', binding, `/api/v1/persona-bindings/${encodeURIComponent(itemId(binding))}`)}>编辑</Button><Button variant="ghost" onClick={() => ctx.toggle(`/api/v1/persona-bindings/${encodeURIComponent(itemId(binding))}`, enabled(binding.enabled))}>{enabled(binding.enabled) ? '停用' : '启用'}</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/persona-bindings/${encodeURIComponent(itemId(binding))}`, '确定删除这个角色绑定？')} /></>} />)}</div>{!bindings.length ? <EmptyState text="暂无会话绑定。" /> : null}</Panel> : null}
      {active === 'profiles' ? <Panel accent="violet"><PanelHeading eyebrow="RUNTIME PROFILES" title="运行档案" /><RecordTable items={profiles} columns={[{ key: 'personaId', label: '角色 ID' }, { key: 'chatEndpointId', label: '聊天端点' }, { key: 'taskEndpointId', label: '任务端点' }, { key: 'memoryPolicy', label: '记忆策略' }, { key: 'proactiveEnabled', label: '主动参与' }]} /></Panel> : null}
    </ModuleShell>
  );
}

function PersonaCard({ persona, active, appearanceLibraries, appearanceLibraryId, onAppearanceChange, onEdit, onActivate, onDelete }: { persona: JsonMap; active: boolean; appearanceLibraries: AppearanceLibrary[]; appearanceLibraryId: string; onAppearanceChange: (libraryId: string) => void; onEdit: () => void; onActivate: () => void; onDelete: () => void }) {
  const appearance = appearanceLibraries.find((library) => library.id === appearanceLibraryId);
  const avatar = text(appearance?.previewUrl, text(persona.avatarDataUri, ''));
  return (
    <article className={`persona-modern-card ${active ? 'is-active' : ''}`}>
      <div className="persona-modern-top"><span>ROLE / {itemId(persona)}</span>{active ? <span className="module-pill">当前默认</span> : <span className="module-readonly">未启用</span>}</div>
      <div className="persona-modern-identity">
        {avatar ? <img src={avatar} alt="" /> : <span className="persona-modern-avatar"><UserRound size={22} /></span>}
        <div><h3>{text(persona.name, itemId(persona))}</h3><p>{text(persona.description, '还没有角色简介')}</p></div>
      </div>
      <div className="persona-modern-meta">{text(persona.tags, '无标签')} · {text(persona.sourceFormat, 'native')}</div>
      <label className="persona-appearance-select"><span>外观库</span><select value={appearanceLibraryId} onChange={(event) => onAppearanceChange(event.target.value)}>{appearanceLibraries.map((library) => <option key={library.id} value={library.id}>{library.name || library.id}</option>)}</select></label>
      <RowActions><Button variant="ghost" icon={<Pencil size={14} />} onClick={onEdit} aria-label="编辑角色" title="编辑角色" />{active ? null : <Button variant="secondary" icon={<Check size={14} />} onClick={onActivate}>设为当前</Button>}<Button variant="ghost" icon={<Trash2 size={14} />} onClick={onDelete} aria-label="删除角色" title="删除角色" /></RowActions>
    </article>
  );
}

function MemoriesModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const memories = collectionItems<JsonMap>(data.memories);
  const relationships = collectionItems<JsonMap>(data.relationships);
  const active = ctx.tab || 'relationships';
  return <ModuleShell accent="rose"><PersonaSelector data={data} ctx={ctx} /><MetricRail items={[{ label: '关系记录', value: number(relationships.length), note: 'relationship graph', accent: 'rose' }, { label: '事实记忆', value: number(memories.length), note: 'scoped memories', accent: 'cyan' }]} /><SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'relationships', label: '关系与亲密度', count: relationships.length }, { id: 'facts', label: '事实记忆', count: memories.length }]} />{active === 'relationships' ? <Panel accent="rose"><PanelHeading eyebrow="RELATIONSHIP GRAPH" title="关系档案" description="亲密度默认随真实互动自动变化。" /><div className="relationship-modern-grid">{relationships.map((relationship) => { const state = asRecord(relationship.state); return <article className="relationship-modern-card" key={itemId(relationship)}><div className="relationship-modern-head"><div><strong>{text(relationship.senderDisplayName, '未命名成员')}</strong><span>{text(state.stage, '新成员')} · {state.intimacyLocked ? '人工锁定' : '自动演化'}</span></div><strong>{number(state.intimacy)}<small>/ 100</small></strong></div><div className="relationship-meter"><span className={`relationship-meter-value ${scoreBand(state.intimacy)}`} /></div><p>互动 {number(state.interactionCount)} 次 · 完整回复 {number(state.replyCount)} 次</p><RowActions><Button variant="ghost" onClick={() => ctx.openEditor(`调整关系 · ${text(relationship.senderDisplayName)}`, relationship, `/api/v1/relationships/${encodeURIComponent(itemId(relationship))}`)}>调整</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/relationships/${encodeURIComponent(itemId(relationship))}`, '确定清除这段关系记录？')} /></RowActions></article>; })}</div>{!relationships.length ? <EmptyState text="关系会在真实互动后自动建立。" /> : null}</Panel> : <Panel accent="cyan"><PanelHeading eyebrow="SCOPED MEMORY" title="事实记忆" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增记忆', { id: crypto.randomUUID(), personaId: ctx.activePersonaId, scopeKind: 'user', scopeReference: '', content: '', kind: 'fact', confidence: 1, importance: 0.7 }, '/api/v1/memories', 'POST')}>新增记忆</Button>} /><div className="module-record-list">{memories.map((memory) => <RecordRow key={itemId(memory)} item={memory} title={text(memory.content)} icon="database" meta={[text(memory.kind), text(memory.scopeReference), `重要度 ${text(memory.importance)}`, `召回 ${number(memory.accessCount)} 次`]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('纠正记忆', memory, `/api/v1/memories/${encodeURIComponent(itemId(memory))}`)}>编辑</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/memories/${encodeURIComponent(itemId(memory))}?personaId=${encodeURIComponent(text(memory.personaId, ctx.activePersonaId))}&scopeKind=${encodeURIComponent(text(memory.scopeKind, 'user'))}&scopeReference=${encodeURIComponent(text(memory.scopeReference, ''))}`, '确定删除这条记忆？')} /></>} />)}</div>{!memories.length ? <EmptyState text="这个角色还没有可管理的记忆。" /> : null}</Panel>}</ModuleShell>;
}

function WorldbookModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const entries = collectionItems<JsonMap>(data.worldbook);
  return <ModuleShell accent="violet"><PersonaSelector data={data} ctx={ctx} /><Panel accent="violet"><PanelHeading eyebrow="WORLD BOOK" title="世界书条目" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增世界书条目', { id: crypto.randomUUID(), comment: '', keys: [], secondaryKeys: [], content: '', priority: 0, insertionOrder: 0, tokenBudget: null, enabled: true, constant: false, selective: true, position: 'before_char' }, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/worldbook?namespace=default`, 'POST')}>新增条目</Button>} /><div className="module-record-list">{entries.map((entry) => <RecordRow key={itemId(entry)} item={entry} title={text(entry.comment, itemId(entry))} icon="book" detail={text(entry.content, '无内容')} meta={[`关键词 ${text(entry.keys, '无')}`, `优先级 ${text(entry.priority, '0')}`, text(entry.position, 'before_char')]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑世界书条目', entry, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/worldbook/${encodeURIComponent(itemId(entry))}?namespace=default`)}>编辑</Button><Button variant="ghost" onClick={() => ctx.toggle(`/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/worldbook/${encodeURIComponent(itemId(entry))}?namespace=default`, enabled(entry.enabled))}>{enabled(entry.enabled) ? '停用' : '启用'}</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/worldbook/${encodeURIComponent(itemId(entry))}?namespace=default`, '确定删除这个世界书条目？')} /></>} />)}</div>{!entries.length ? <EmptyState text="暂无世界书条目。" /> : null}</Panel></ModuleShell>;
}

function SamplesModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const samples = collectionItems<JsonMap>(data.samples);
  const traits = collectionItems<JsonMap>(data.traits);
  const active = ctx.tab || 'traits';
  return <ModuleShell accent="violet"><PersonaSelector data={data} ctx={ctx} /><MetricRail items={[{ label: '人格特质', value: number(traits.length), note: 'trait graph', accent: 'violet' }, { label: '场景样本', value: number(samples.length), note: 'reply rhythm', accent: 'rose' }]} /><SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'traits', label: '人格图谱', count: traits.length }, { id: 'samples', label: '场景样本', count: samples.length }]} />{active === 'traits' ? <Panel accent="violet"><PanelHeading eyebrow="TRAIT GRAPH" title="人格特质" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增人格特质', { id: crypto.randomUUID(), name: '', description: '', triggers: ['*'], supports: [], conflicts: [], source: '', weight: 1, enabled: true }, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/traits?namespace=default`, 'POST')}>新增特质</Button>} /><div className="module-record-list">{traits.map((trait) => <RecordRow key={itemId(trait)} item={trait} title={text(trait.name, itemId(trait))} icon="sparkles" detail={text(trait.description, '无行为描述')} meta={[`触发 ${text(trait.triggers, '无')}`, `权重 ${text(trait.weight, '1')}`, `支持 ${text(trait.supports, '无')}`]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑人格特质', trait, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/traits/${encodeURIComponent(itemId(trait))}?namespace=default`)}>编辑</Button><Button variant="ghost" onClick={() => ctx.toggle(`/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/traits/${encodeURIComponent(itemId(trait))}?namespace=default`, enabled(trait.enabled))}>{enabled(trait.enabled) ? '停用' : '启用'}</Button></>} />)}</div>{!traits.length ? <EmptyState text="暂无人格特质。" /> : null}</Panel> : <Panel accent="rose"><PanelHeading eyebrow="SCENE SAMPLES" title="场景样本" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增人格样本', { id: crypto.randomUUID(), sceneTags: [], relationshipStage: '', emotion: '', context: '', candidateReplies: [], forbiddenExpressions: [], source: '', weight: 1, enabled: true }, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/samples?namespace=default`, 'POST')}>新增样本</Button>} /><div className="module-record-list">{samples.map((sample) => <RecordRow key={itemId(sample)} item={sample} title={text(sample.sceneTags, itemId(sample))} icon="message" detail={text(sample.context, '无场景上下文')} meta={[`关系 ${text(sample.relationshipStage, '不限')}`, `情绪 ${text(sample.emotion, '不限')}`, `权重 ${text(sample.weight, '1')}`]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑人格样本', sample, `/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/samples/${encodeURIComponent(itemId(sample))}?namespace=default`)}>编辑</Button><Button variant="ghost" onClick={() => ctx.toggle(`/api/v1/personas/${encodeURIComponent(ctx.activePersonaId)}/samples/${encodeURIComponent(itemId(sample))}?namespace=default`, enabled(sample.enabled))}>{enabled(sample.enabled) ? '停用' : '启用'}</Button></>} />)}</div>{!samples.length ? <EmptyState text="暂无人格样本。" /> : null}</Panel>}</ModuleShell>;
}

function KnowledgeModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const docs = collectionItems<JsonMap>(data.documents);
  const candidates = collectionItems<JsonMap>(data.candidates);
  const integrations = collectionItems<JsonMap>(data.integrations);
  const policyMap = Object.fromEntries(integrations.map((item) => [text(item.id), asRecord(item.config)]));
  const active = ctx.tab || 'retrieval';
  return <ModuleShell accent="amber"><MetricRail items={[{ label: '正式文档', value: number(docs.length), note: 'curated', accent: 'cyan' }, { label: '待审候选', value: number(candidates.filter((item) => text(item.status) === 'pending').length), note: 'review queue', accent: 'amber' }, { label: '策略模块', value: number(integrations.length), note: 'Core policies', accent: 'violet' }]} /><SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'retrieval', label: '检索与向量' }, { id: 'reading', label: '文档读取' }, { id: 'documents', label: '正式知识', count: docs.length }, { id: 'candidates', label: '候选审核', count: candidates.length }]} />{active === 'retrieval' ? <><PolicyPanel title="检索与向量" description="Embedding、Rerank 和本地回退策略。" value={policyMap.retrieval_policy} endpointPath="/api/v1/integrations/retrieval_policy" accent="amber" onEdit={ctx.openEditor} /><PolicyPanel title="学习策略" description="自动采集产生候选，审核后才进入正式知识。" value={{ learningEnabled: policyMap.grok_policy?.learningWorkerEnabled, topics: policyMap.grok_policy?.learningTopics }} endpointPath="/api/v1/runtime/config" accent="cyan" onEdit={ctx.openEditor} /></> : null}{active === 'reading' ? <PolicyPanel title="文档与多模态" description="附件读取、提取上限和会话续接。" value={policyMap.document_policy} endpointPath="/api/v1/integrations/document_policy" accent="violet" onEdit={ctx.openEditor} /> : null}{active === 'documents' ? <Panel accent="cyan"><PanelHeading eyebrow="CURATED KNOWLEDGE" title="正式知识文档" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增知识文档', { id: crypto.randomUUID(), namespace: 'default', title: '', sourceUri: '', content: '', metadata: {} }, '/api/v1/knowledge/documents', 'POST')}>新增文档</Button>} /><div className="module-record-list">{docs.map((doc) => <RecordRow key={itemId(doc)} item={doc} title={text(doc.title, itemId(doc))} icon="file" detail={text(doc.sourceUri, '手工录入')} meta={[`命名空间 ${text(doc.namespace)}`, `Hash ${text(doc.contentHash, '未计算')}`, text(doc.updatedAt, '未更新')]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑知识文档', doc, `/api/v1/knowledge/documents/${encodeURIComponent(itemId(doc))}?namespace=default`)}>编辑</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/knowledge/documents/${encodeURIComponent(itemId(doc))}?namespace=default`, '确定删除这份知识文档？')} /></>} />)}</div>{!docs.length ? <EmptyState text="暂无正式知识文档。" /> : null}</Panel> : null}{active === 'candidates' ? <Panel accent="amber"><PanelHeading eyebrow="REVIEW QUEUE" title="候选知识审核" /><div className="module-record-list">{candidates.map((candidate) => <RecordRow key={itemId(candidate)} item={candidate} title={`${text(candidate.title, itemId(candidate))} · ${text(candidate.status)}`} icon="archive" detail={text(candidate.content, '无候选正文')} meta={[text(candidate.source, '未知来源'), text(candidate.createdAt, '未记录')]} actions={<>{text(candidate.status) === 'pending' ? <><Button variant="primary" icon={<Check size={14} />} onClick={() => apiRequest(`/api/v1/runtime/knowledge-candidates/${encodeURIComponent(itemId(candidate))}/review`, { method: 'POST', body: JSON.stringify({ decision: 'approved', authority: 'admin', knowledgeNamespace: 'default' }) }).then(ctx.reload)}>批准</Button><Button variant="ghost" onClick={() => apiRequest(`/api/v1/runtime/knowledge-candidates/${encodeURIComponent(itemId(candidate))}/review`, { method: 'POST', body: JSON.stringify({ decision: 'rejected', authority: 'admin', knowledgeNamespace: 'default' }) }).then(ctx.reload)}>拒绝</Button></> : null}<Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(`/api/v1/runtime/knowledge-candidates/${encodeURIComponent(itemId(candidate))}`, '确定删除这个候选知识？')} /></>} />)}</div>{!candidates.length ? <EmptyState text="暂无候选知识。" /> : null}</Panel> : null}</ModuleShell>;
}

function SkillsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const skills = collectionItems<JsonMap>(data.skills);
  return (
    <ModuleShell accent="amber">
      <MetricRail items={[
        { label: '技能目录', value: number(skills.length), note: 'registered skills', accent: 'amber' },
        { label: '已启用', value: number(skills.filter((item) => enabled(item.enabled)).length), note: 'active rules', accent: 'green' },
        { label: '附件触发', value: number(skills.filter((item) => collectionItems(item.attachmentKinds).length > 0).length), note: 'media-aware', accent: 'violet' },
      ]} />
      <Panel accent="amber">
        <PanelHeading
          eyebrow="SKILL REGISTRY"
          title="技能目录"
          description="只有命中触发条件的技能才会进入当前任务上下文。"
          action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增技能', {
            id: crypto.randomUUID(),
            name: '',
            description: '',
            instructions: '',
            enabled: true,
            activationMode: 'any',
            triggers: [],
            attachmentKinds: [],
            requiredTools: [],
            requiredMcpServers: [],
            allowedAuthorities: ['member', 'admin'],
            personaIds: [],
            priority: 0,
          }, '/api/v1/skills', 'POST')}>新增技能</Button>}
        />
        <div className="module-record-list">
          {skills.map((skill) => (
            <RecordRow
              key={itemId(skill)}
              item={skill}
              title={text(skill.name, itemId(skill))}
              icon="sparkles"
              detail={text(skill.description, '无技能说明')}
              meta={[
                `触发 ${text(skill.triggers, text(skill.attachmentKinds, '始终'))}`,
                `工具 ${text(skill.requiredTools, '无')}`,
                `MCP ${text(skill.requiredMcpServers, '无')}`,
                `优先级 ${text(skill.priority, '0')}`,
              ]}
              actions={<>
                <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑技能', skill, endpoint('/api/v1/skills', itemId(skill)))}>编辑</Button>
                <Button variant="ghost" icon={enabled(skill.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/skills', itemId(skill)), enabled(skill.enabled))}>{enabled(skill.enabled) ? '停用' : '启用'}</Button>
                <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/skills', itemId(skill)), '确定删除这个技能？')} />
              </>}
            />
          ))}
          {!skills.length ? <EmptyState text="暂无技能。创建后可配置触发条件、工具和角色范围。" /> : null}
        </div>
      </Panel>
    </ModuleShell>
  );
}

function ToolsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const tools = collectionItems<JsonMap>(data.tools);
  const mcp = collectionItems<JsonMap>(data.mcp);
  const active = ctx.tab || 'tools';
  return (
    <ModuleShell accent="amber">
      <MetricRail items={[
        { label: '工具', value: number(tools.length), note: 'tool registry', accent: 'amber' },
        { label: 'MCP 服务', value: number(mcp.length), note: 'remote servers', accent: 'violet' },
        { label: '已授权', value: number([...tools, ...mcp].filter((item) => enabled(item.enabled)).length), note: 'enabled surface', accent: 'green' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[
        { id: 'tools', label: '工具', count: tools.length },
        { id: 'mcp', label: 'MCP 服务', count: mcp.length },
      ]} />
      {active === 'tools' ? (
        <Panel accent="amber">
          <PanelHeading
            eyebrow="TOOL REGISTRY"
            title="工具注册表"
            description="工具权限、审批方式和输入 Schema 由 Core 执行。"
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增工具', {
              id: crypto.randomUUID(),
              name: '',
              description: '',
              capabilities: [],
              riskLevel: 0,
              enabled: true,
              adapterRef: '',
              allowedAuthorities: ['member', 'admin'],
              approvalMode: 'auto',
              timeoutSeconds: 30,
              inputSchema: { type: 'object', properties: {} },
            }, '/api/v1/tools', 'POST')}>新增工具</Button>}
          />
          <div className="module-record-list">
            {tools.map((tool) => (
              <RecordRow
                key={itemId(tool)}
                item={tool}
                title={text(tool.name, itemId(tool))}
                icon="wrench"
                detail={text(tool.description, '无工具说明')}
                meta={[`适配器 ${text(tool.adapterRef, '未配置')}`, `风险 ${text(tool.riskLevel, '0')}`, `审批 ${text(tool.approvalMode, '未设置')}`, text(tool.capabilities, '无能力声明')]}
                actions={<>
                  <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑工具', tool, endpoint('/api/v1/tools', itemId(tool)))}>编辑</Button>
                  <Button variant="ghost" icon={enabled(tool.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/tools', itemId(tool)), enabled(tool.enabled))}>{enabled(tool.enabled) ? '停用' : '启用'}</Button>
                  <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/tools', itemId(tool)), '确定删除这个工具？')} />
                </>}
              />
            ))}
            {!tools.length ? <EmptyState text="暂无工具。创建后可配置适配器、权限、审批和参数 Schema。" /> : null}
          </div>
        </Panel>
      ) : (
        <Panel accent="violet">
          <PanelHeading
            eyebrow="MCP REGISTRY"
            title="MCP 服务"
            description="密钥只保存服务器环境变量名；HTTP 服务支持实时发现。"
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增 MCP 服务', {
              id: crypto.randomUUID(),
              name: '',
              transport: 'http',
              endpoint: '',
              command: '',
              args: [],
              toolPrefix: 'mcp',
              enabled: false,
              secretRef: '',
              allowedTools: [],
              allowedAuthorities: ['admin'],
              approvalMode: 'admin_only',
              timeoutSeconds: 30,
            }, '/api/v1/mcp/servers', 'POST')}>新增 MCP</Button>}
          />
          <div className="module-record-list">
            {mcp.map((server) => (
              <RecordRow
                key={itemId(server)}
                item={server}
                title={text(server.name, itemId(server))}
                icon="network"
                detail={text(server.endpoint, text(server.command, '未配置连接地址'))}
                meta={[text(server.transport, 'http'), `前缀 ${text(server.toolPrefix, '无')}`, `审批 ${text(server.approvalMode, '未设置')}`, `白名单 ${text(server.allowedTools, '全部')}`]}
                actions={<>
                  {text(server.transport) === 'http' && enabled(server.enabled) ? <Button variant="ghost" icon={<RefreshCw size={14} />} onClick={async () => { try { const result = await apiRequest<JsonMap>(endpoint('/api/v1/mcp/servers', itemId(server)) + '/discover', { method: 'POST', body: '{}' }); ctx.inspect('MCP 实时发现', '本次结果来自 Core 的实时探测，不会自动写入工具目录。', result); } catch (cause) { window.alert(cause instanceof Error ? cause.message : 'MCP 检测失败'); } }}>检测</Button> : null}
                  <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑 MCP 服务', server, endpoint('/api/v1/mcp/servers', itemId(server)))}>编辑</Button>
                  {text(server.transport) === 'stdio' ? <span className="module-readonly">服务端启用</span> : <Button variant="ghost" icon={enabled(server.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/mcp/servers', itemId(server)), enabled(server.enabled))}>{enabled(server.enabled) ? '停用' : '启用'}</Button>}
                  <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/mcp/servers', itemId(server)), '确定删除这个 MCP 服务？')} />
                </>}
              />
            ))}
            {!mcp.length ? <EmptyState text="暂无 MCP 服务。创建后可配置连接、工具白名单和权限。" /> : null}
          </div>
        </Panel>
      )}
    </ModuleShell>
  );
}

function IntegrationsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const integrations = collectionItems<JsonMap>(data.integrations);
  const platforms = collectionItems<JsonMap>(data.platforms);
  const runtime = collectionItems<JsonMap>(data.platformRuntime);
  const models = collectionItems<JsonMap>(data.models);
  const active = ctx.tab || 'channels';
  const runtimeMap = new Map(runtime.map((item) => [text(item.id), item]));
  const policies = integrations.filter((item) => asRecord(item.config) && Object.keys(asRecord(item.config)).length > 0);
  const credentialConfig = asRecord(data.credentials);
  const credentials = collectionItems<JsonMap>(credentialConfig);
  return (
    <ModuleShell accent="violet">
      <MetricRail items={[
        { label: '平台连接器', value: number(platforms.length), note: 'transport adapters', accent: 'violet' },
        { label: '运行正常', value: number(platforms.filter((item) => enabled(item.enabled) && enabled(runtimeMap.get(text(item.id))?.healthy)).length), note: 'native runtime', accent: 'green' },
        { label: '模型端点', value: number(models.length), note: 'available endpoints', accent: 'cyan' },
        { label: '策略模块', value: number(policies.length), note: 'Core policies', accent: 'amber' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[
        { id: 'channels', label: '渠道与接管', count: platforms.length },
        { id: 'credentials', label: '凭据配置', count: credentials.length },
        { id: 'policies', label: '策略模块', count: policies.length },
        { id: 'catalog', label: '平台目录', count: collectionItems(data.platformCatalog).length },
      ]} />
      {active === 'channels' ? (
        <Panel accent="violet">
          <PanelHeading
            eyebrow="PLATFORM CONNECTORS"
            title="平台连接器"
            description="连接参数保存在 Core；凭据引用服务器环境变量，可在凭据配置中填写。"
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增平台连接器', {
              id: crypto.randomUUID(),
              type: 'custom',
              displayName: '',
              enabled: false,
              settings: {},
              credentialRefs: {},
            }, '/api/v1/platforms', 'POST')}>新增连接器</Button>}
          />
          <div className="module-record-list">
            {platforms.map((platform) => (
              <RecordRow
                key={itemId(platform)}
                item={platform}
                title={text(platform.displayName, itemId(platform))}
                icon="network"
                detail={`${text(platform.type, 'custom')} · ${enabled(platform.credentialConfigured) ? '凭据已配置' : '凭据未配置'}`}
                meta={[`运行 ${text(runtimeMap.get(itemId(platform))?.status, '未探测')}`, `参数 ${number(Object.keys(asRecord(platform.settings)).length)} 项`, text(platform.compatibilitySource, 'native')]}
                actions={<>
                  <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑平台连接器', platform, endpoint('/api/v1/platforms', itemId(platform)))}>编辑</Button>
                  <Button variant="ghost" icon={enabled(platform.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/platforms', itemId(platform)), enabled(platform.enabled))}>{enabled(platform.enabled) ? '停用' : '启用'}</Button>
                  {platform.compatibilitySource ? null : <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/platforms', itemId(platform)), '确定删除这个平台连接器？')} />}
                </>}
              />
            ))}
            {!platforms.length ? <EmptyState text="暂无平台连接器。" /> : null}
          </div>
        </Panel>
      ) : active === 'credentials' ? (
        <CredentialPanel credentials={credentials} credentialFileConfigured={enabled(credentialConfig.credentialFileConfigured)} onReload={ctx.reload} />
      ) : active === 'policies' ? (
        <div className="module-policy-grid">
          {policies.map((policy) => <PolicyPanel key={itemId(policy)} title={text(policy.displayName, text(policy.id))} description={text(policy.description, 'Core 策略模块')} value={policy.config} endpointPath={endpoint('/api/v1/integrations', itemId(policy))} accent="violet" onEdit={ctx.openEditor} />)}
          {!policies.length ? <EmptyState text="暂无可展示的接入策略。" /> : null}
        </div>
      ) : (
        <Panel accent="cyan">
          <PanelHeading eyebrow="PLATFORM CATALOG" title="平台目录" description="目录提供连接器类型和默认参数，不会直接启用平台。" />
          <RecordTable items={collectionItems<JsonMap>(data.platformCatalog)} columns={[{ key: 'type', label: '类型' }, { key: 'displayName', label: '名称' }, { key: 'description', label: '说明' }, { key: 'credentialRef', label: '凭据引用' }]} empty="暂无平台目录。" />
        </Panel>
      )}
    </ModuleShell>
  );
}

function CredentialPanel({ credentials, credentialFileConfigured, onReload }: { credentials: JsonMap[]; credentialFileConfigured: boolean; onReload: () => void }) {
  const [values, setValues] = useState<Record<string, string>>({});
  const [saving, setSaving] = useState('');
  const [notice, setNotice] = useState('');
  const save = async (name: string, value: string) => {
    setSaving(name);
    setNotice('');
    try {
      await apiRequest(`/api/v1/credentials/${encodeURIComponent(name)}`, { method: 'PUT', body: JSON.stringify({ value }) });
      setValues((current) => ({ ...current, [name]: '' }));
      setNotice(`${name} 已保存，当前进程立即生效。`);
      onReload();
    } catch (cause) {
      setNotice(cause instanceof Error ? cause.message : '凭据保存失败');
    } finally {
      setSaving('');
    }
  };
  return (
    <Panel accent="violet">
      <PanelHeading
        eyebrow="CREDENTIALS / HOST ENV"
        title="供应商与平台凭据"
        description="只显示配置状态，不回显密钥。输入后写入数据卷中的托管凭据文件，并在覆盖前保留 .bak。"
      />
      {!credentialFileConfigured ? <div className="module-notice module-notice-inline"><HardDrive size={15} />托管凭据文件尚未创建，首次保存时会在数据卷中创建。</div> : null}
      {notice ? <div className="module-notice module-notice-inline"><CheckCircle2 size={15} />{notice}</div> : null}
      <div className="credential-grid">
        {credentials.map((credential) => {
          const name = text(credential.name, '');
          const current = values[name] || '';
          const configured = enabled(credential.configured);
          return (
            <form className="credential-row" key={name} onSubmit={(event) => { event.preventDefault(); if (current.trim()) void save(name, current); }}>
              <div className="credential-copy">
                <strong>{text(credential.label, name)}</strong>
                <small>{name} · {configured ? '已配置' : '未配置'} · {text(credential.source, '未配置')}</small>
              </div>
              <input
                className="credential-input"
                type="password"
                autoComplete="new-password"
                value={current}
                placeholder={configured ? '输入新值覆盖' : '输入密钥'}
                onChange={(event) => setValues((items) => ({ ...items, [name]: event.target.value }))}
                disabled={saving === name}
              />
              <div className="credential-actions">
                <Button type="submit" variant="primary" icon={saving === name ? <RefreshCw className="spin" size={14} /> : <KeyRound size={14} />} disabled={!current.trim() || saving === name}>保存</Button>
                {configured && !enabled(credential.required) ? <Button type="button" variant="ghost" icon={<Trash2 size={14} />} disabled={saving === name} onClick={() => void save(name, '')}>清除</Button> : null}
              </div>
            </form>
          );
        })}
      </div>
      {!credentials.length ? <EmptyState text="暂无可管理凭据。先在供应商连接或平台连接器中配置 credentialRef。" /> : null}
    </Panel>
  );
}

function ModelsModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const models = collectionItems<JsonMap>(data.models);
  const connections = collectionItems<JsonMap>(data.providerConnections);
  const drivers = collectionItems<JsonMap>(data.providerDrivers);
  const health = asRecord(data.health);
  const active = ctx.tab || 'connections';
  const newConnectionId = crypto.randomUUID();
  const newModelId = crypto.randomUUID();
  const toggleModel = async (model: JsonMap) => {
    const usage = asRecord(model.usage);
    try {
      await apiRequest(endpoint('/api/v1/model-endpoints', itemId(model)), {
        method: 'PUT',
        body: JSON.stringify({
          provider: model.provider,
          model: model.model,
          connectionId: model.connectionId || '',
          enabled: !enabled(model.enabled),
          capabilities: model.capabilities || [],
          inputCostPerMillion: model.inputCostPerMillion,
          outputCostPerMillion: model.outputCostPerMillion,
          qualityScore: model.qualityScore,
          priority: model.priority,
          maxContextTokens: model.maxContextTokens,
          executionKind: model.executionKind,
          adapterRef: model.adapterRef,
          usage,
        }),
      });
      ctx.reload();
    } catch (cause) {
      window.alert(cause instanceof Error ? cause.message : '模型端点状态更新失败');
    }
  };
  const usage = models.reduce<{ calls: number; tokens: number; cost: number }>((total, model) => {
    const item = asRecord(model.usage);
    return { calls: total.calls + Number(item.calls || 0), tokens: total.tokens + Number(item.totalTokens || 0), cost: total.cost + Number(item.estimatedCost || 0) };
  }, { calls: 0, tokens: 0, cost: 0 });
  return (
    <ModuleShell accent="violet">
      <MetricRail items={[
        { label: '供应商连接', value: number(connections.length), note: 'provider connections', accent: 'violet' },
        { label: '模型端点', value: number(models.length), note: 'model endpoints', accent: 'cyan' },
        { label: '健康检查', value: text(health.healthy, '未检查'), note: `${text(health.unhealthy, '0')} 异常`, accent: 'green' },
        { label: '累计调用', value: number(usage.calls), note: `${number(usage.tokens)} tokens`, accent: 'amber' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'connections', label: '供应商连接', count: connections.length }, { id: 'endpoints', label: '模型端点', count: models.length }, { id: 'drivers', label: '协议驱动', count: drivers.length }]} />
      {active === 'connections' ? (
        <Panel accent="violet">
          <PanelHeading
            eyebrow="PROVIDER CONNECTIONS"
            title="供应商连接"
            description="连接、凭据引用、价格源和超时独立持有。"
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增供应商连接', {
              id: newConnectionId,
              provider: '',
              protocol: 'openai_chat_completion',
              apiBase: '',
              pricingUrl: '',
              credentialRef: 'ERDAI_MODEL_API_KEY',
              timeoutSeconds: 120,
              enabled: false,
            }, endpoint('/api/v1/provider-connections', newConnectionId), 'PUT')}>新增连接</Button>}
          />
          <div className="module-record-list">
            {connections.map((connection) => (
              <RecordRow
                key={itemId(connection)}
                item={connection}
                title={`${text(connection.provider, itemId(connection))} · ${text(connection.protocol)}`}
                icon="server"
                detail={`${text(connection.apiBase, '未设置 API Base')} · ${enabled(connection.credentialConfigured) ? '凭据已就绪' : '凭据缺失'}`}
                meta={[`超时 ${text(connection.timeoutSeconds, '120')} 秒`, text(connection.pricingUrl, '价格源未配置'), `调用 ${text(asRecord(connection.usage).calls, '0')} 次`]}
                actions={<>
                  <Button variant="ghost" icon={<Gauge size={14} />} onClick={async () => { try { const result = await apiRequest<JsonMap>(endpoint('/api/v1/provider-connections', itemId(connection)) + '/test', { method: 'POST', body: '{}' }); window.alert(enabled(result.healthy) ? `连接正常，${text(result.latencyMs, '-')} ms` : `连接失败：${text(result.statusMessage, '未知错误')}`); ctx.reload(); } catch (cause) { window.alert(cause instanceof Error ? cause.message : '连接测试失败'); } }}>测试</Button>
                  <Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑供应商连接', connection, endpoint('/api/v1/provider-connections', itemId(connection)))}>编辑</Button>
                  <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/provider-connections', itemId(connection)), '确定删除这个供应商连接？')} />
                </>}
              />
            ))}
            {!connections.length ? <EmptyState text="暂无供应商连接。" /> : null}
          </div>
        </Panel>
      ) : active === 'endpoints' ? (
        <Panel accent="cyan">
          <PanelHeading
            eyebrow="MODEL ENDPOINTS"
            title="模型端点"
            description="端点声明能力、质量、上下文和成本，供路由中心选择。"
            action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增模型端点', {
              id: newModelId,
              provider: '',
              model: '',
              connectionId: '',
              enabled: false,
              capabilities: ['text'],
              qualityScore: 0.5,
              priority: 0,
              maxContextTokens: 0,
              executionKind: 'llm',
              adapterRef: '',
            }, endpoint('/api/v1/model-endpoints', newModelId), 'PUT')}>新增端点</Button>}
          />
          <div className="module-record-list">
            {models.map((model) => {
              const modelHealth = asRecord(model.health);
              return <RecordRow key={itemId(model)} item={model} title={`${text(model.provider, 'unknown')} / ${text(model.model, itemId(model))}`} icon="cpu" detail={`${text(model.executionKind, 'llm')} · ${text(model.capabilities, '无能力声明')}`} meta={[`质量 ${text(model.qualityScore, '未设置')}`, `健康 ${text(modelHealth.status, '未检查')}`, `价格 ${text(model.pricingSource, '未配置')}`, `调用 ${text(asRecord(model.usage).calls, '0')}`]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑模型端点', model, endpoint('/api/v1/model-endpoints', itemId(model)))}>编辑</Button><Button variant="ghost" icon={enabled(model.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => toggleModel(model)}>{enabled(model.enabled) ? '停用' : '启用'}</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/model-endpoints', itemId(model)), '确定删除这个模型端点？')} /></>} />;
            })}
            {!models.length ? <EmptyState text="暂无模型端点。" /> : null}
          </div>
        </Panel>
      ) : (
        <Panel accent="amber">
          <PanelHeading eyebrow="PROVIDER DRIVERS" title="协议驱动" description="协议驱动决定连接探针和请求适配，不在此处修改。" />
          <RecordTable items={drivers} columns={[{ key: 'id', label: 'ID' }, { key: 'label', label: '名称' }, { key: 'description', label: '说明' }, { key: 'capabilities', label: '能力' }, { key: 'probePath', label: '探针路径' }]} empty="暂无协议驱动。" />
        </Panel>
      )}
    </ModuleShell>
  );
}

function RoutingModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const lanes = collectionItems<JsonMap>(data.lanes);
  const models = collectionItems<JsonMap>(data.models);
  const control = asRecord(data.control);
  const locks = asRecord(control.locks);
  const active = ctx.tab || 'control';
  return (
    <ModuleShell accent="violet">
      <MetricRail items={[
        { label: '能力通道', value: number(lanes.length), note: 'routing lanes', accent: 'violet' },
        { label: '可用模型', value: number(models.filter((item) => enabled(item.enabled)).length), note: 'enabled endpoints', accent: 'cyan' },
        { label: '当前模式', value: text(control.mode, 'auto'), note: 'global control', accent: 'green' },
        { label: '锁定场景', value: number(Object.keys(locks).length), note: 'manual locks', accent: 'amber' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'control', label: '全局控制' }, { id: 'lanes', label: '能力通道', count: lanes.length }]} />
      {active === 'control' ? (
        <Panel accent="violet">
          <PanelHeading eyebrow="ROUTING CONTROL" title="全局路由控制" description="自动模式按能力、健康、成本和优先级选择；手动模式只锁定指定场景。" action={<Button variant="primary" icon={<Pencil size={15} />} onClick={() => ctx.openEditor('编辑全局路由控制', control, '/api/v1/routing/control')}>编辑控制</Button>} />
          <div className="policy-preview">
            <div><span>路由模式</span><strong>{text(control.mode, 'auto')}</strong></div>
            <div><span>锁定场景</span><strong>{number(Object.keys(locks).length)}</strong></div>
            {Object.entries(locks).map(([lane, model]) => <div key={lane}><span>{lane}</span><strong>{text(model)}</strong></div>)}
          </div>
          <p className="module-notice module-notice-inline"><Route size={15} />编辑器中的 `locks` 使用 lane → endpoint ID 映射。</p>
        </Panel>
      ) : (
        <Panel accent="cyan">
          <PanelHeading eyebrow="CAPABILITY LANES" title="能力通道" description="每条通道分别声明必需能力和偏好能力。" />
          <div className="module-record-list">
            {lanes.map((lane) => (
              <RecordRow
                key={itemId(lane)}
                item={lane}
                title={text(lane.lane, itemId(lane))}
                icon="route"
                detail={`必需能力：${text(lane.requiredCapabilities, '无')}`}
                meta={[`偏好能力 ${text(lane.preferredCapabilities, '无')}`, `锁定端点 ${text(locks[text(lane.lane)], '未锁定')}`]}
                actions={<Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor(`编辑路由通道 · ${text(lane.lane)}`, lane, '/api/v1/routing/lanes')}>编辑</Button>}
              />
            ))}
            {!lanes.length ? <EmptyState text="暂无能力通道。" /> : null}
          </div>
        </Panel>
      )}
    </ModuleShell>
  );
}

function DevicesModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const devices = collectionItems<JsonMap>(data.devices);
  const sessions = collectionItems<JsonMap>(data.realtimeSessions);
  const [pairing, setPairing] = useState<JsonMap | null>(null);
  const active = ctx.tab || 'trusted';
  return (
    <ModuleShell accent="green">
      <MetricRail items={[
        { label: '可信设备', value: number(devices.length), note: 'trusted devices', accent: 'green' },
        { label: '在线设备', value: number(devices.filter((item) => enabled(item.online)).length), note: 'presence', accent: 'cyan' },
        { label: '实时会话', value: number(sessions.length), note: 'realtime gateway', accent: 'violet' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'trusted', label: '可信设备', count: devices.length }, { id: 'sessions', label: '实时会话', count: sessions.length }, { id: 'companion', label: '桌面 Companion' }]} />
      {active === 'trusted' ? (
        <Panel accent="green">
          <PanelHeading eyebrow="TRUSTED DEVICES" title="可信设备" description="撤销只影响设备凭据，不会删除角色或会话数据。" action={<Button variant="primary" icon={<KeyRound size={15} />} onClick={async () => { try { setPairing(await apiRequest<JsonMap>('/api/v1/realtime/pairing-codes', { method: 'POST', body: '{}' })); } catch (cause) { window.alert(cause instanceof Error ? cause.message : '配对码生成失败'); } }}>生成配对码</Button>} />
          {pairing ? <div className="pairing-panel"><strong>{text(pairing.code, '未返回')}</strong><span>{text(pairing.expiresAt, '稍后失效')} 前有效，成功配对后立即失效。</span></div> : null}
          <div className="module-record-list">
            {devices.map((device) => <RecordRow key={itemId(device)} item={{ ...device, enabled: text(device.status) === 'trusted' }} title={text(device.name, itemId(device))} icon="laptop" detail={`${text(device.id)} · ${text(device.status, 'unknown')}`} meta={[enabled(device.online) ? '在线' : '离线', `最近活动 ${text(device.lastSeenAt, '暂无')}`, text(device.platform, '未知平台')]} actions={text(device.status) === 'trusted' ? <Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/devices', itemId(device)), '确定撤销这个设备？')}>撤销</Button> : null} />)}
            {!devices.length ? <EmptyState text="还没有配对设备。" /> : null}
          </div>
        </Panel>
      ) : active === 'sessions' ? (
        <Panel accent="cyan">
          <PanelHeading eyebrow="REALTIME SESSIONS" title="实时会话" description="桌面、语音和数字人终端通过 Realtime Gateway 接入同一个 Core。" />
          <RecordTable items={sessions} columns={[{ key: 'deviceName', label: '设备' }, { key: 'state', label: '状态' }, { key: 'presence', label: 'Presence' }, { key: 'personaId', label: '角色' }, { key: 'lastClientSequence', label: '客户端序号' }, { key: 'lastServerSequence', label: '服务端序号' }]} empty="当前没有桌面会话。" />
        </Panel>
      ) : (
        <Panel accent="violet">
          <PanelHeading eyebrow="COMPANION" title="桌面 Companion" description="桌面、语音和未来数字人终端共用同一套实时网关。" action={<Button variant="primary" icon={<ArrowUpRight size={15} />} onClick={() => window.open('/companion.html', '_blank', 'noopener,noreferrer')}>打开 Companion</Button>} />
          <div className="module-empty"><Laptop size={22} /><strong>终端入口已保留</strong><span>从 Companion 连接后，设备和会话会回到本页统一观察。</span></div>
        </Panel>
      )}
    </ModuleShell>
  );
}

function SecurityModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const directives = collectionItems<JsonMap>(data.directives);
  const integrations = collectionItems<JsonMap>(data.integrations);
  const boundary = asRecord(integrations.find((item) => text(item.id) === 'content_boundary_policy')?.config);
  const config = asRecord(data.config);
  const active = ctx.tab || 'boundary';
  return (
    <ModuleShell accent="green">
      <MetricRail items={[
        { label: '安全模块', value: number(integrations.filter((item) => text(item.id).includes('policy')).length), note: 'protected policies', accent: 'green' },
        { label: '管理员指令', value: number(directives.length), note: 'admin directives', accent: 'rose' },
        { label: '启用指令', value: number(directives.filter((item) => enabled(item.enabled)).length), note: 'enforced', accent: 'cyan' },
      ]} />
      <div className="security-priority"><ShieldCheck size={16} /><strong>固定优先级：</strong>系统规则 &gt; 管理员长期指令 &gt; 当前管理员命令 &gt; 角色与世界书 &gt; 知识和记忆 &gt; 普通成员消息</div>
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'boundary', label: '内容边界' }, { id: 'rules', label: '系统规则' }, { id: 'directives', label: '管理员指令', count: directives.length }]} />
      {active === 'boundary' ? (
        <PolicyPanel title="内容与互动边界" description="分类动作、触发词和安全语境由 Core 在模型调用前执行。" value={boundary} endpointPath="/api/v1/integrations/content_boundary_policy" accent="green" onEdit={ctx.openEditor} />
      ) : active === 'rules' ? (
        <PolicyPanel title="系统安全规则" description="系统规则不由角色卡覆盖。" value={{ protectedRules: config.protectedRules }} endpointPath="/api/v1/runtime/config" accent="rose" onEdit={ctx.openEditor} />
      ) : (
        <Panel accent="cyan">
          <PanelHeading eyebrow="ADMIN DIRECTIVES" title="管理员长期指令" description="长期指令高于角色、知识、记忆和普通成员消息。" action={<Button variant="primary" icon={<Plus size={15} />} onClick={() => ctx.openEditor('新增管理员指令', { id: crypto.randomUUID(), content: '', enabled: true }, '/api/v1/runtime/directives', 'POST')}>新增指令</Button>} />
          <div className="module-record-list">
            {directives.map((directive) => <RecordRow key={itemId(directive)} item={directive} title={`${enabled(directive.enabled) ? '启用' : '停用'} · ${itemId(directive)}`} icon="shield" detail={text(directive.content, '无指令内容')} meta={[text(directive.updatedAt, '未更新'), text(directive.authority, 'admin')]} actions={<><Button variant="ghost" icon={<Pencil size={14} />} onClick={() => ctx.openEditor('编辑管理员指令', directive, endpoint('/api/v1/runtime/directives', itemId(directive)))}>编辑</Button><Button variant="ghost" icon={enabled(directive.enabled) ? <X size={14} /> : <Check size={14} />} onClick={() => ctx.toggle(endpoint('/api/v1/runtime/directives', itemId(directive)), enabled(directive.enabled))}>{enabled(directive.enabled) ? '停用' : '启用'}</Button><Button variant="ghost" icon={<Trash2 size={14} />} onClick={() => ctx.remove(endpoint('/api/v1/runtime/directives', itemId(directive)), '确定删除这条管理员指令？')} /></>} />)}
            {!directives.length ? <EmptyState text="暂无管理员长期指令。" /> : null}
          </div>
        </Panel>
      )}
    </ModuleShell>
  );
}

function SystemModule({ data, ctx }: { data: ModuleData; ctx: RenderContext }) {
  const config = asRecord(data.config);
  const quotas = asRecord(data.mediaQuotas);
  const layers = collectionItems<JsonMap>(data.configLayers);
  const active = ctx.tab || 'runtime';
  return (
    <ModuleShell accent="green">
      <MetricRail items={[
        { label: '当前角色', value: text(config.activePersonaId, '未选择'), note: 'runtime default', accent: 'green' },
        { label: '知识命名空间', value: text(config.knowledgeNamespace, 'default'), note: 'knowledge scope', accent: 'cyan' },
        { label: '每日图片', value: text(quotas.imageDailyLimit, '0'), note: `视频 ${text(quotas.videoDailyLimit, '0')}`, accent: 'violet' },
        { label: '配置分层', value: number(layers.length), note: 'resolved layers', accent: 'amber' },
      ]} />
      <SectionTabs active={active} onChange={ctx.setTab} tabs={[{ id: 'runtime', label: '运行时配置' }, { id: 'quotas', label: '媒体额度' }, { id: 'layers', label: '配置分层', count: layers.length }]} />
      {active === 'runtime' ? (
        <PolicyPanel title="运行时配置" description="自然对话、角色卡、世界书、知识和学习开关的统一默认值。" value={config} endpointPath="/api/v1/runtime/config" accent="green" onEdit={ctx.openEditor} />
      ) : active === 'quotas' ? (
        <PolicyPanel title="媒体额度" description="图片和视频额度由 Core 统一计算，可信管理员可按策略豁免。" value={quotas} endpointPath="/api/v1/runtime/media-quotas" accent="violet" onEdit={ctx.openEditor} />
      ) : (
        <Panel accent="amber">
          <PanelHeading eyebrow="CONFIG LAYERS" title="配置分层" description="只读查看最终合并来源，编辑具体策略请进入对应模块。" />
          <RecordTable items={layers} columns={[{ key: 'layer', label: '层级' }, { key: 'source', label: '来源' }, { key: 'keys', label: '字段' }, { key: 'active', label: '状态' }]} empty="暂无配置分层数据。" />
        </Panel>
      )}
    </ModuleShell>
  );
}
