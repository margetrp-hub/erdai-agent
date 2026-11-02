import { Ban, Check, ChevronDown, ChevronRight, Clock3, Pause, Play, RefreshCw, Send, TriangleAlert } from 'lucide-react';
import { useCallback, useEffect, useMemo, useState } from 'react';
import { apiCollection, apiRequest, type JsonMap } from '../lib/api';
import { Button, Panel, PanelHeading, StatusDot } from '../components/ui';
import './task-inbox.css';

type DurableTask = {
  id: string;
  title?: string;
  instruction?: string;
  status?: string;
  state?: string;
  reason?: string;
  nextAction?: string;
  ownerRef?: string;
  botId?: string;
  createdAt?: string;
  updatedAt?: string;
  nextRunAt?: string;
  attempts?: number;
  maxAttempts?: number;
  source?: string;
  result?: string;
};

type DurableEvent = {
  id: number;
  eventType?: string;
  fromStatus?: string;
  toStatus?: string;
  reason?: string;
  nextAction?: string;
  actor?: string;
  createdAt?: string;
};

type TaskCollection = { items: DurableTask[]; total: number; durable: boolean };

const activeStates = new Set(['planned', 'queued', 'leased', 'running', 'checkpointed', 'waiting_approval', 'waiting_external', 'paused', 'retrying', 'unknown_manual']);
const stateLabels: Record<string, string> = {
  planned: '已计划', queued: '排队中', leased: '已领取', running: '执行中', checkpointed: '已保存进度',
  waiting_approval: '等待确认', waiting_external: '等待外部结果', paused: '已暂停', retrying: '重试中',
  succeeded: '已完成', failed: '失败', cancelled: '已取消', dead_letter: '转人工', unknown_manual: '待核实',
};

function normalizeTask(value: JsonMap): DurableTask {
  return {
    id: String(value.id || value.runId || ''), title: value.title ? String(value.title) : undefined,
    instruction: value.instruction ? String(value.instruction) : value.goal ? String(value.goal) : undefined,
    status: value.status ? String(value.status) : undefined, state: value.state ? String(value.state) : undefined,
    reason: value.reason ? String(value.reason) : value.errorCode ? String(value.errorCode) : undefined,
    nextAction: value.nextAction ? String(value.nextAction) : undefined,
    ownerRef: value.ownerRef ? String(value.ownerRef) : undefined, botId: value.botId ? String(value.botId) : undefined,
    createdAt: value.createdAt ? String(value.createdAt) : undefined, updatedAt: value.updatedAt ? String(value.updatedAt) : undefined,
    nextRunAt: value.nextRunAt ? String(value.nextRunAt) : undefined,
    attempts: value.attempts === undefined ? undefined : Number(value.attempts),
    maxAttempts: value.maxAttempts === undefined ? undefined : Number(value.maxAttempts),
    source: value.source ? String(value.source) : undefined,
    result: value.result ? String(value.result) : undefined,
  };
}

async function loadTasks(): Promise<TaskCollection> {
  try {
    const result = await apiCollection<JsonMap>('/api/v1/jobs');
    return { items: result.items.map(normalizeTask).filter((item) => item.id), total: result.total, durable: true };
  } catch (cause) {
    if (!(cause instanceof Error) || !('status' in cause) || (cause as { status?: number }).status !== 404) throw cause;
    const result = await apiCollection<JsonMap>('/api/v1/tasks');
    return { items: result.items.map(normalizeTask).filter((item) => item.id), total: result.total, durable: false };
  }
}

function stateOf(task: DurableTask) { return task.status || task.state || 'unknown'; }
function labelOf(state: string) { return stateLabels[state] || state; }
function formatTime(value?: string) {
  if (!value || !Number.isFinite(Date.parse(value))) return '时间未记录';
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Shanghai' }).format(new Date(value));
}
function toneOf(state: string): 'ok' | 'warn' | 'bad' | 'idle' {
  if (state === 'succeeded') return 'ok';
  if (['failed', 'dead_letter', 'unknown_manual'].includes(state)) return 'bad';
  if (['waiting_approval', 'waiting_external', 'paused', 'retrying'].includes(state)) return 'warn';
  return 'idle';
}

const eventLabels: Record<string, string> = {
  created: '已创建', queued: '已排队', leased: '已领取', running: '开始执行', checkpointed: '已保存进度',
  waiting_approval: '等待确认', waiting_external: '等待外部结果', paused: '已暂停', retrying: '自动重试',
  succeeded: '已完成', failed: '执行失败', cancelled: '已取消', dead_letter: '转人工处理', unknown_manual: '待核实',
  approved: '已批准', resumed: '已恢复', retried: '已重新排队', finished: '本轮完成', scheduled: '已安排下一次执行', retry_due: '重试时间到', lease_expired: '租约已过期', goal_updated: '目标已更新',
};

function eventLabel(event: DurableEvent) {
  return eventLabels[event.eventType || ''] || event.eventType || `${event.fromStatus || ''} → ${event.toStatus || ''}`;
}

async function invokeTaskAction(id: string, action: 'pause' | 'resume' | 'cancel' | 'retry' | 'approve') {
  return apiRequest<JsonMap>(`/api/v1/jobs/${encodeURIComponent(id)}/${action}`, { method: 'POST' });
}

export function TaskInboxPage() {
  const [tasks, setTasks] = useState<DurableTask[]>([]);
  const [durable, setDurable] = useState(true);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [instruction, setInstruction] = useState('');
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});
  const [events, setEvents] = useState<Record<string, DurableEvent[]>>({});
  const [eventLoading, setEventLoading] = useState<Record<string, boolean>>({});
  const [eventErrors, setEventErrors] = useState<Record<string, string>>({});

  const reload = useCallback(async (quiet = false) => {
    if (!quiet) setLoading(true);
    try {
      const result = await loadTasks();
      setTasks(result.items); setDurable(result.durable); setError('');
    } catch (cause) { setError(cause instanceof Error ? cause.message : '任务读取失败'); }
    finally { if (!quiet) setLoading(false); }
  }, []);

  useEffect(() => { void reload(); }, [reload]);
  const hasActive = useMemo(() => tasks.some((task) => activeStates.has(stateOf(task))), [tasks]);
  useEffect(() => {
    if (!hasActive) return undefined;
    const timer = window.setInterval(() => { void reload(true); }, 5000);
    return () => window.clearInterval(timer);
  }, [hasActive, reload]);

  const create = async () => {
    const value = instruction.trim();
    if (!value || saving) return;
    setSaving(true); setError(''); setNotice('');
    try {
      await apiRequest('/api/v1/jobs', { method: 'POST', body: JSON.stringify({ instruction: value, sourceTransport: 'admin_webui' }) });
      setInstruction(''); setNotice('任务已创建，智能体会在 VPS 上持续执行并记录进度。'); await reload(true);
    } catch (cause) { setError(cause instanceof Error ? cause.message : '任务创建失败'); }
    finally { setSaving(false); }
  };

  const action = async (task: DurableTask, kind: 'pause' | 'resume' | 'cancel' | 'retry' | 'approve') => {
    if (saving) return;
    setSaving(true); setError(''); setNotice('');
    try { await invokeTaskAction(task.id, kind); setNotice(`任务已${kind === 'pause' ? '暂停' : kind === 'resume' ? '恢复' : kind === 'cancel' ? '取消' : kind === 'retry' ? '重新排队' : '批准'}。`); await reload(true); }
    catch (cause) { setError(cause instanceof Error ? cause.message : '任务操作失败'); }
    finally { setSaving(false); }
  };

  const toggleEvents = async (task: DurableTask) => {
    const open = !expanded[task.id];
    setExpanded((current) => ({ ...current, [task.id]: open }));
    if (!open || events[task.id] || eventLoading[task.id]) return;
    setEventLoading((current) => ({ ...current, [task.id]: true }));
    setEventErrors((current) => ({ ...current, [task.id]: '' }));
    try {
      const value = await apiRequest<unknown>(`/api/v1/jobs/${encodeURIComponent(task.id)}/events`);
      const items: unknown[] = Array.isArray(value) ? value : (value && typeof value === 'object' && Array.isArray((value as JsonMap).items) ? (value as JsonMap).items as unknown[] : []);
      setEvents((current) => ({ ...current, [task.id]: items.map((item: unknown) => {
        const value = item as JsonMap;
        return { id: Number(value.id), eventType: value.eventType ? String(value.eventType) : undefined, fromStatus: value.fromStatus ? String(value.fromStatus) : undefined, toStatus: value.toStatus ? String(value.toStatus) : undefined, reason: value.reason ? String(value.reason) : undefined, nextAction: value.nextAction ? String(value.nextAction) : undefined, actor: value.actor ? String(value.actor) : undefined, createdAt: value.createdAt ? String(value.createdAt) : undefined };
      }) }));
    } catch (cause) {
      setEventErrors((current) => ({ ...current, [task.id]: cause instanceof Error ? cause.message : '进度读取失败' }));
    } finally {
      setEventLoading((current) => ({ ...current, [task.id]: false }));
    }
  };

  return <div className="view-stage task-inbox-page">
    <Panel accent="cyan" className="task-command-panel">
      <PanelHeading title="给智能体派活" description="用一句话描述目标。创建后任务会保存到 VPS，重启后也能继续。" action={<Button variant="ghost" icon={<RefreshCw size={15} className={loading ? 'spin' : ''} />} onClick={() => void reload()} disabled={loading}>刷新</Button>} />
      <div className="task-command-row"><textarea value={instruction} onChange={(event) => setInstruction(event.target.value)} onKeyDown={(event) => { if ((event.ctrlKey || event.metaKey) && event.key === 'Enter') { event.preventDefault(); void create(); } }} placeholder="例如：每天早上九点检查渠道，有异常才提醒我" rows={3} disabled={saving} /><Button variant="primary" icon={<Send size={15} />} onClick={() => void create()} disabled={saving || !instruction.trim()}>{saving ? '提交中…' : '创建任务'}</Button></div>
      <div className="task-command-hint"><Clock3 size={14} />支持一次性任务、定时任务和持续监控；执行前会遵循工具权限与审批规则。按 Ctrl/⌘ + Enter 提交。</div>
    </Panel>
    {notice ? <div className="module-notice" role="status"><Check size={15} />{notice}</div> : null}
    {error ? <div className="module-notice module-notice-error" role="alert"><TriangleAlert size={15} />{error}</div> : null}
    {!durable && !loading ? <div className="module-notice" role="status">当前服务尚未启用长期任务接口，下面显示的是一次性运行记录。升级 Core 后会自动切换到长期任务。</div> : null}
    <Panel accent="amber">
      <PanelHeading title="任务收件箱" description={hasActive ? '有任务正在执行，页面每 5 秒自动刷新。' : '所有任务都保留目标、状态和下一步动作。'} action={<span className="task-count">{tasks.length} 项</span>} />
      {loading ? <div className="module-loading"><RefreshCw className="spin" size={20} /><strong>正在读取任务</strong><span>同步 VPS 上的任务状态</span></div> : tasks.length ? <div className="task-inbox-list">{tasks.map((task) => {
        const state = stateOf(task); const paused = state === 'paused';
        const canPause = ['queued', 'checkpointed'].includes(state);
        const canCancel = ['planned', 'queued', 'checkpointed', 'waiting_approval', 'waiting_external', 'paused', 'retrying', 'unknown_manual'].includes(state);
        const isExpanded = expanded[task.id];
        return <article className="task-inbox-item" key={task.id}><div className="task-inbox-icon"><StatusDot tone={toneOf(state)} /></div><div className="task-inbox-main"><div className="task-inbox-title"><strong>{task.title || task.instruction || task.id}</strong><span className="module-status">{labelOf(state)}</span></div><p>{task.nextAction || task.reason || (task.instruction && task.instruction !== task.title ? task.instruction : '暂无下一步说明')}</p><div className="task-inbox-meta"><span>更新：{formatTime(task.updatedAt || task.createdAt)}</span>{task.nextRunAt ? <span>下次执行：{formatTime(task.nextRunAt)}</span> : null}{task.attempts !== undefined ? <span>尝试：{task.attempts}{task.maxAttempts ? `/${task.maxAttempts}` : ''}</span> : null}</div>{isExpanded ? <div className="task-event-timeline">{eventLoading[task.id] ? <span className="task-event-muted">正在读取进度…</span> : eventErrors[task.id] ? <span className="task-event-error">{eventErrors[task.id]}</span> : events[task.id]?.length ? events[task.id].map((event) => <div className="task-event" key={event.id}><span className="task-event-time">{formatTime(event.createdAt)}</span><strong>{eventLabel(event)}</strong>{event.reason ? <span>{event.reason}</span> : null}{event.nextAction ? <span className="task-event-next">下一步：{event.nextAction}</span> : null}</div>) : <span className="task-event-muted">还没有可展示的进度事件。</span>}</div> : null}</div><div className="task-inbox-actions"><Button variant="ghost" icon={isExpanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />} onClick={() => void toggleEvents(task)} disabled={eventLoading[task.id]}>{isExpanded ? '收起进度' : '查看进度'}</Button>{state === 'waiting_approval' ? <Button variant="primary" icon={<Check size={14} />} onClick={() => void action(task, 'approve')} disabled={saving}>批准</Button> : null}{paused ? <Button variant="ghost" icon={<Play size={14} />} onClick={() => void action(task, 'resume')} disabled={saving}>继续</Button> : null}{canPause ? <Button variant="ghost" icon={<Pause size={14} />} onClick={() => void action(task, 'pause')} disabled={saving}>暂停</Button> : null}{['failed', 'cancelled', 'dead_letter', 'unknown_manual'].includes(state) ? <Button variant="ghost" icon={<RefreshCw size={14} />} onClick={() => void action(task, 'retry')} disabled={saving}>{state === 'unknown_manual' ? '确认后重试' : '重试'}</Button> : null}{canCancel ? <Button variant="ghost" icon={<Ban size={14} />} onClick={() => void action(task, 'cancel')} disabled={saving}>取消</Button> : null}</div></article>;
      })}</div> : <div className="module-empty"><Clock3 size={22} /><strong>还没有长期任务</strong><span>在上面输入一句话，智能体会在 VPS 上创建并持续执行。</span></div>}
    </Panel>
  </div>;
}
