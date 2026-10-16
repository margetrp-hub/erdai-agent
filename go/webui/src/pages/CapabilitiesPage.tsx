import { ArrowUpRight, Blocks, CheckCircle2, CircleAlert, Filter, RefreshCw, Search, Sparkles, Wrench } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import type { ViewId } from '../components/AppShell';
import { Button, Panel, PanelHeading, StatusDot } from '../components/ui';
import { apiCollection, apiRequest, type JsonMap } from '../lib/api';
import './capabilities.css';

type Source = 'plugin' | 'skill' | 'tool';
type Capability = { id: string; name: string; description: string; source: Source; sourceLabel: string; enabled: boolean; state: string; category: string; commands: string[]; dependencies: string[]; toggleable: boolean; configView: ViewId; integrationId: string; canCheck: boolean };
const sourceNames: Record<Source, string> = { plugin: '能力包', skill: '技能', tool: '工具' };
const endpoints: Record<Source, string> = { plugin: '/api/v1/plugins', skill: '/api/v1/skills', tool: '/api/v1/tools' };
const categoryNames: Record<string, string> = { conversation: '聊天互动', memory: '记忆关系', knowledge: '知识学习', media: '图片视频', research: '联网研究', governance: '权限管理', monitoring: '状态监测', growth: '积分活动', extension: '扩展能力' };
const stateNames: Record<string, string> = { healthy: '检查通过', ready: '配置已就绪', needs_configuration: '还需配置', degraded: '最近有异常', unverified: '等待实际验证', unavailable: '检查失败', disabled: '已关闭', registered: '已登记', enabled: '已开启，尚未检查' };
const configViews = new Set<ViewId>(['system', 'roles', 'memories', 'worldbook', 'samples', 'knowledge', 'models', 'routing', 'integrations', 'plugins', 'tools', 'skills', 'security', 'runtime', 'devices', 'operations', 'points']);
const asObject = (value: unknown): JsonMap => value && typeof value === 'object' && !Array.isArray(value) ? value as JsonMap : {};
const strings = (value: unknown) => Array.isArray(value) ? value.map(String) : [];
const needsAttention = (state: string) => ['needs_configuration', 'degraded', 'unavailable'].includes(state);

function normalize(item: JsonMap, source: Source): Capability {
  const manifest = asObject(item.manifest);
  const view = String(item.configView || manifest.configView || (source === 'plugin' ? 'plugins' : source === 'skill' ? 'skills' : 'tools')) as ViewId;
  return {
    id: String(item.id), name: String(item.name || item.id), description: String(item.description || (source === 'skill' ? '按当前条件加载的表达或工作技能。' : '暂无用途说明。')),
    source, sourceLabel: source === 'plugin' ? item.source === 'external' ? '外部能力包' : '内置能力' : sourceNames[source], enabled: item.enabled === true,
    state: String(item.state || (item.enabled ? 'enabled' : 'disabled')), category: String(manifest.category || (source === 'skill' ? 'conversation' : 'extension')),
    commands: strings(manifest.commands || item.triggers), dependencies: strings(manifest.dependencies || item.requiredTools),
    toggleable: source === 'plugin' ? item.toggleable === true : true, configView: configViews.has(view) ? view : 'plugins',
    integrationId: String(String(manifest.configPath || '').match(/^\/api\/v1\/integrations\/([a-z0-9_]+)$/)?.[1] || item.integrationId || asObject(item.adapter).integrationId || ''), canCheck: source === 'plugin',
  };
}

export function CapabilitiesPage({ onNavigate, refreshKey = 0 }: { onNavigate: (view: ViewId, section?: string) => void; refreshKey?: number; activeInstanceId?: string }) {
  const [items, setItems] = useState<Capability[]>([]);
  const [loading, setLoading] = useState(true);
  const [errors, setErrors] = useState<string[]>([]);
  const [query, setQuery] = useState('');
  const [source, setSource] = useState('all');
  const [status, setStatus] = useState('all');
  const [category, setCategory] = useState('all');
  const [page, setPage] = useState(1);
  const [revision, setRevision] = useState(0);
  const [busy, setBusy] = useState<string[]>([]);
  const busyRef = useRef(new Set<string>());
  const [checks, setChecks] = useState<Record<string, JsonMap>>({});
  const [notice, setNotice] = useState('');
  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setErrors([]);
    void Promise.allSettled((Object.keys(endpoints) as Source[]).map(async (kind) => {
      const data = await apiCollection(endpoints[kind], controller.signal);
      return data.items.map((item) => normalize(item, kind));
    })).then((results) => {
      if (controller.signal.aborted) return;
      const loaded: Capability[] = [], failures: string[] = [];
      results.forEach((result, index) => {
        if (result.status === 'fulfilled') loaded.push(...result.value);
        else failures.push(`${sourceNames[(Object.keys(endpoints) as Source[])[index]]}未能读取：${result.reason instanceof Error ? result.reason.message : '请重试'}`);
      });
      setItems(loaded); setErrors(failures); setLoading(false);
    });
    return () => controller.abort();
  }, [refreshKey, revision]);
  useEffect(() => setPage(1), [query, source, status, category]);
  const stateOf = (item: Capability) => item.enabled ? String(checks[`${item.source}:${item.id}`]?.state || item.state) : 'disabled';
  const filtered = useMemo(() => items.filter((item) => {
    const currentState = item.enabled ? String(checks[`${item.source}:${item.id}`]?.state || item.state) : 'disabled';
    return (source === 'all' || item.source === source) && (category === 'all' || item.category === category)
      && (status === 'all' || status === 'enabled' && item.enabled || status === 'disabled' && !item.enabled || status === 'attention' && needsAttention(currentState))
      && (!query.trim() || `${item.name} ${item.description} ${item.commands.join(' ')} ${item.id} ${categoryNames[item.category] || ''}`.toLowerCase().includes(query.trim().toLowerCase()));
  }), [items, source, status, category, query, checks]);
  const pages = Math.max(1, Math.ceil(filtered.length / 12));
  const currentPage = Math.min(page, pages);
  const visible = filtered.slice((currentPage - 1) * 12, currentPage * 12);
  const begin = (id: string) => { if (busyRef.current.has(id)) return false; busyRef.current.add(id); setBusy([...busyRef.current]); return true; };
  const end = (id: string) => { busyRef.current.delete(id); setBusy([...busyRef.current]); };
  const check = async (item: Capability) => {
    const key = `${item.source}:${item.id}`;
    if (!begin(key)) return;
    try {
      const result = await apiRequest<JsonMap>(`${endpoints.plugin}/${encodeURIComponent(item.id)}/health`);
      setChecks((previous) => ({ ...previous, [key]: result }));
    } catch (cause) {
      setChecks((previous) => ({ ...previous, [key]: { state: 'unavailable', message: cause instanceof Error ? cause.message : '检查失败，请重试。' } }));
    } finally { end(key); }
  };
  const toggle = async (item: Capability) => {
    const key = `${item.source}:${item.id}`;
    if (!begin(key)) return;
    setNotice('');
    try {
      await apiRequest(`${endpoints[item.source]}/${encodeURIComponent(item.id)}`, { method: 'PUT', body: JSON.stringify({ enabled: !item.enabled }) });
      setChecks((previous) => { const next = { ...previous }; delete next[key]; return next; });
      setNotice(`${item.name}已${item.enabled ? '关闭' : '开启'}。开启状态不代表实际调用已经成功。`);
      setRevision((value) => value + 1);
    } catch (cause) { setNotice(cause instanceof Error ? cause.message : '操作失败，请重试。'); }
    finally { end(key); }
  };
  const configure = (item: Capability) => item.integrationId ? onNavigate('integrations', `policy:${item.integrationId}`) : onNavigate(item.configView);
  return <div className="view-stage capability-center">
    <Panel accent="cyan">
      <PanelHeading title="能力中心" description="看看豆包会做什么，在这里开启能力、补齐配置和查看检查结果。" action={<Button icon={<Blocks size={15} />} onClick={() => onNavigate('plugins')}>管理插件</Button>} />
      <div className="capability-summary">
        <div><strong>{loading ? '…' : items.length}</strong><span>已有能力与资源</span></div>
        <div><strong>{loading ? '…' : items.filter((item) => item.enabled).length}</strong><span>已开启</span></div>
        <div><strong>{loading ? '…' : items.filter((item) => needsAttention(stateOf(item))).length}</strong><span>需要处理</span></div>
        <p>这里管理全局能力。具体机器人是否允许使用，还取决于它的权限和配置。<button type="button" onClick={() => onNavigate('runtime')}>查看机器人设置 <ArrowUpRight size={13} /></button></p>
      </div>
    </Panel>
    <div className="capability-shortcuts"><Button icon={<Sparkles size={15} />} onClick={() => onNavigate('knowledge')}>知识与学习</Button><Button icon={<Sparkles size={15} />} onClick={() => onNavigate('skills')}>表达与工作技能</Button><Button icon={<Wrench size={15} />} onClick={() => onNavigate('tools')}>外部工具与服务</Button></div>
    <div className="capability-filters">
      <label className="capability-search"><Search size={16} /><input type="search" placeholder="搜索能力，例如：图片、联网、记忆" aria-label="搜索能力" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
      <label><span>来源</span><select value={source} onChange={(event) => setSource(event.target.value)}><option value="all">全部来源</option><option value="plugin">内置与外部能力包</option><option value="skill">技能</option><option value="tool">工具</option></select></label>
      <label><span>状态</span><select value={status} onChange={(event) => setStatus(event.target.value)}><option value="all">全部状态</option><option value="enabled">已开启</option><option value="disabled">已关闭</option><option value="attention">需要处理</option></select></label>
      <label><span>用途</span><select value={category} onChange={(event) => setCategory(event.target.value)}><option value="all">全部用途</option>{[...new Set(items.map((item) => item.category))].map((value) => <option value={value} key={value}>{categoryNames[value] || value}</option>)}</select></label>
      <Button variant="ghost" icon={<RefreshCw size={15} />} aria-label="重新读取能力" disabled={loading} onClick={() => setRevision((value) => value + 1)} />
    </div>
    {notice ? <div className="module-notice" role="status">{notice}</div> : null}
    {errors.length ? <div className="capability-errors" role="alert"><CircleAlert size={18} /><div>{errors.map((message) => <p key={message}>{message}</p>)}<Button onClick={() => setRevision((value) => value + 1)}>重新读取</Button></div></div> : null}
    {loading ? <div className="module-loading"><RefreshCw className="spin" size={20} /><span>正在读取能力…</span></div> : <>
      <div className="capability-grid">{visible.map((item) => {
        const state = stateOf(item);
        const result = checks[`${item.source}:${item.id}`];
        const key = `${item.source}:${item.id}`;
        const working = busy.includes(key);
        const Icon = item.source === 'tool' ? Wrench : item.source === 'skill' ? Sparkles : Blocks;
        return <article className="capability-card" key={key}>
          <div className="capability-card-title"><span className="capability-icon"><Icon size={21} /></span><div><h2>{item.name}</h2><span>{item.sourceLabel} · {categoryNames[item.category] || item.category}</span></div></div>
          <p className="capability-description">{item.description}</p>
          <div className="capability-state"><StatusDot tone={needsAttention(state) ? 'warn' : state === 'healthy' ? 'ok' : 'idle'} /><strong>{stateNames[state] || state}</strong>{result?.checkedAt ? <span>刚刚检查</span> : null}</div>
          {result?.message ? <p className="capability-check-message">{String(result.message)}</p> : null}
          {item.commands.length ? <div className="capability-commands">{item.commands.slice(0, 4).map((command) => <span key={command}>{command}</span>)}</div> : null}
          {item.dependencies.length ? <p className="capability-dependencies">需要：{item.dependencies.map((dependency) => items.find((candidate) => candidate.id === dependency && candidate.source === (item.source === 'skill' ? 'tool' : 'plugin'))?.name || dependency).join('、')}</p> : null}
          <div className="capability-actions">
            <Button variant="primary" onClick={() => configure(item)}>{state === 'needs_configuration' ? '去配置' : '设置'}</Button>
            {item.canCheck ? <Button disabled={working} icon={working ? <RefreshCw className="spin" size={14} /> : <CheckCircle2 size={14} />} onClick={() => void check(item)}>检查状态</Button> : null}
            {item.toggleable ? <button type="button" className={`plugin-quick-toggle ${item.enabled ? 'is-on' : ''}`} role="switch" aria-checked={item.enabled} aria-label={`${item.name}开关`} disabled={working} onClick={() => void toggle(item)}><span aria-hidden="true" /><strong>{item.enabled ? '已开启' : '已关闭'}</strong></button> : <span className="capability-readonly">在设置中管理</span>}
          </div>
        </article>;
      })}</div>
      {!filtered.length ? <div className="module-empty"><Filter size={20} /><p>{items.length ? '没有找到对应能力，换一个关键词或筛选条件。' : '还没有可展示的能力。'}</p>{items.length ? <Button onClick={() => { setQuery(''); setSource('all'); setStatus('all'); setCategory('all'); }}>清除筛选</Button> : null}</div> : null}
      {pages > 1 ? <div className="module-table-pager"><span>第 {currentPage} / {pages} 页，共 {filtered.length} 项</span><div><Button disabled={currentPage === 1} onClick={() => setPage(currentPage - 1)}>上一页</Button><Button disabled={currentPage === pages} onClick={() => setPage(currentPage + 1)}>下一页</Button></div></div> : null}
    </>}
    <p className="capability-footnote">检查状态只读取已有配置和运行证据。外部能力包登记后仍需配置对应适配器；登记不等于已能执行。</p>
  </div>;
}
