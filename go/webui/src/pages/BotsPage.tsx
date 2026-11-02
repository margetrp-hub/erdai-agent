import { Copy, ListChecks, Pause, Play, Plus, RefreshCw, RotateCcw, ShieldCheck, UserRound } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Button, InfoDialog, Panel, PanelHeading, StatusDot } from '../components/ui';
import {
  ApiError,
  apiCollection,
  apiRequest,
  copyPrivateBot,
  createPrivateBot,
  loadPrivateBots,
  loadPrivateBotTasks,
  retryTask,
  selectPrivateBot,
  updatePrivateBotState,
  type Persona,
  type PrivateBot,
  type PrivateBotTask,
} from '../lib/api';

type DialogMode = 'create' | 'copy' | null;

function botStatus(bot: PrivateBot) {
  if (bot.status === 'paused' || bot.enabled === false) return { label: '已暂停', tone: 'idle' as const };
  if (bot.status === 'archived') return { label: '已归档', tone: 'bad' as const };
  if (bot.status === 'waiting' || bot.status === 'blocked') return { label: bot.status === 'waiting' ? '等待中' : '需要处理', tone: 'warn' as const };
  return { label: '运行中', tone: 'ok' as const };
}
function botName(bot: PrivateBot) {
  return bot.displayName || bot.id;
}

function describeStatus(bot: PrivateBot) {
  const status = botStatus(bot);
  if (status.label === '已暂停') return '暂停后不会接收新的任务；已有记录仍保留。';
  if (status.label === '已归档') return '归档 Bot 只读保留，不能继续接收任务。';
  return '可以接收聊天、主动任务和内部 specialist 协作。';
}

export function BotsPage({ activeInstanceId, onInstanceChange, onRefresh }: { activeInstanceId?: string; onInstanceChange: (id: string) => void; onRefresh?: () => Promise<void> }) {
  const [bots, setBots] = useState<PrivateBot[]>([]);
  const [personas, setPersonas] = useState<Persona[]>([]);
  const [legacy, setLegacy] = useState(false);
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [dialog, setDialog] = useState<DialogMode>(null);
  const [source, setSource] = useState<PrivateBot | null>(null);
  const [name, setName] = useState('');
  const [personaId, setPersonaId] = useState('');
  const [tasks, setTasks] = useState<PrivateBotTask[]>([]);
  const [tasksLoading, setTasksLoading] = useState(false);

  const refresh = async () => {
    setLoading(true);
    setError('');
    try {
      const [result, people] = await Promise.all([
        loadPrivateBots(),
        apiCollection<Persona>('/api/v1/personas?namespace=default'),
      ]);
      setBots(result.items);
      setLegacy(result.legacy);
      setPersonas(people.items);
      setPersonaId((current) => current || people.items[0]?.id || '');
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '私人 Bot 加载失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void refresh(); }, []);

  const activeBot = useMemo(
    () => bots.find((bot) => (bot.agentInstanceId || bot.id) === activeInstanceId) || bots[0],
    [activeInstanceId, bots],
  );

  useEffect(() => {
    let cancelled = false;
    const loadTasks = async () => {
      if (!activeBot || legacy) {
        setTasks([]);
        return;
      }
      setTasksLoading(true);
      try {
        const result = await loadPrivateBotTasks(activeBot.id, activeBot.ownerRef || 'admin');
        if (!cancelled) setTasks(result.items.slice(0, 8));
      } catch (cause) {
        if (!cancelled) setError(cause instanceof Error ? cause.message : '任务记录加载失败');
      } finally {
        if (!cancelled) setTasksLoading(false);
      }
    };
    void loadTasks();
    return () => { cancelled = true; };
  }, [activeBot, legacy]);

  const choose = async (bot: PrivateBot) => {
    setSaving(true);
    setError('');
    try {
      if (!legacy) await selectPrivateBot(bot.id, {
        ownerRef: bot.ownerRef || 'admin',
        workspaceId: bot.workspaceId || 'default',
        transport: 'qq_official',
        transportInstance: '*',
        conversationRef: '*',
      });
      onInstanceChange(bot.agentInstanceId || bot.id);
      setNotice(`已切换到「${botName(bot)}」。后续对话使用它自己的记忆和平台连接。`);
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '切换 Bot 失败');
    } finally {
      setSaving(false);
    }
  };

  const toggle = async (bot: PrivateBot) => {
    const next = botStatus(bot).label === '已暂停' ? 'active' : 'paused';
    setSaving(true);
    setError('');
    try {
      if (legacy) {
        await apiRequest(`/api/v1/agent-instances/${encodeURIComponent(bot.agentInstanceId || bot.id)}`, { method: 'PUT', body: JSON.stringify({ enabled: next === 'active' }) });
      } else {
        await updatePrivateBotState(bot.id, next);
      }
      setNotice(next === 'active' ? `「${botName(bot)}」已恢复。` : `「${botName(bot)}」已暂停，不会接收新任务。`);
      await refresh();
      await onRefresh?.();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '更新 Bot 状态失败');
    } finally {
      setSaving(false);
    }
  };

  const openCreate = () => { setSource(null); setDialog('create'); setName(''); };
  const openCopy = (bot: PrivateBot) => { setSource(bot); setDialog('copy'); setName(`${botName(bot)}的副本`); };
  const submit = async () => {
    const displayName = name.trim();
    if (!displayName) { setError('请先填写 Bot 名称。'); return; }
    if (dialog === 'create' && !personaId) { setError('创建 Bot 需要先绑定一个角色；请先在角色库新增角色。'); return; }
    setSaving(true);
    setError('');
    try {
      const idempotencyKey = window.crypto.randomUUID();
      const result = dialog === 'copy' && source
        ? await copyPrivateBot(source.id, { ownerRef: source.ownerRef || 'admin', workspaceId: source.workspaceId || 'default', displayName, idempotencyKey })
        : await createPrivateBot({ id: `bot-${window.crypto.randomUUID()}`, ownerRef: 'admin', workspaceId: 'default', displayName, personaId, idempotencyKey });
      setDialog(null);
      setNotice(dialog === 'copy' ? `已复制「${botName(source as PrivateBot)}」，新 Bot 与原 Bot 的记忆和连接保持隔离。` : `已创建「${botName(result.bot)}」。`);
      await refresh();
      await onRefresh?.();
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : '保存 Bot 失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="view-stage bots-stage">
      <div className="bots-intro">
        <div>
          <span className="hero-kicker">PRIVATE BOT WORKSPACE</span>
          <h1>私人 Bot</h1>
          <p>每个 Bot 都有独立的角色、记忆、平台连接和任务记录。先选中一个，再进入其它管理页面。</p>
        </div>
        <Button variant="primary" icon={<Plus size={16} />} onClick={openCreate}>新建 Bot</Button>
      </div>
      {legacy ? <div className="module-notice module-notice-inline"><ShieldCheck size={15} />当前 Core 尚未提供私人 Bot 归属字段，已用旧运行实例只读兼容；升级后可启用复制和隔离。</div> : null}
      {notice ? <div className="module-notice module-notice-inline">{notice}</div> : null}
      {error ? <div className="module-notice module-notice-inline module-notice-error">{error}<Button variant="ghost" icon={<RefreshCw size={14} />} onClick={() => void refresh()}>重试</Button></div> : null}
      <Panel accent="cyan">
        <PanelHeading title="我的 Bot" description={`${bots.length} 个 Bot · 当前选择：${activeBot ? botName(activeBot) : '尚未选择'}`} action={<Button variant="ghost" icon={<RefreshCw size={15} className={loading ? 'spin' : ''} />} onClick={() => void refresh()} disabled={loading}>刷新</Button>} />
        {loading ? <div className="module-loading"><RefreshCw className="spin" size={22} /><strong>正在读取私人 Bot</strong><span>同步归属、状态和角色</span></div> : (
          <div className="module-record-list">
            {bots.map((bot) => {
              const status = botStatus(bot);
              const selected = (bot.agentInstanceId || bot.id) === activeInstanceId;
              const persona = personas.find((item) => item.id === bot.personaId);
              return <article className={`module-record bot-record ${selected ? 'is-current' : ''}`} key={bot.id}>
                <span className="module-record-icon"><UserRound size={16} /></span>
                <div className="module-record-main">
                  <div className="module-record-title"><strong>{botName(bot)}{selected ? ' · 当前' : ''}</strong><span className="module-status"><StatusDot tone={status.tone} />{status.label}</span></div>
                  <p>{describeStatus(bot)}</p>
                  <div className="module-record-meta"><span>角色：{persona?.name || bot.personaId || '未绑定'}</span><span>记忆：{bot.memoryNamespace || '独立命名空间'}</span><span>可见性：{bot.visibility || 'private'}</span></div>
                </div>
                <div className="module-row-actions">
                  <Button variant={selected ? 'secondary' : 'primary'} onClick={() => void choose(bot)} disabled={saving || status.label === '已归档'}>{selected ? '当前 Bot' : '切换'}</Button>
                  <Button variant="ghost" icon={<Copy size={14} />} onClick={() => openCopy(bot)} disabled={saving || status.label === '已归档'}>复制</Button>
                  <Button variant="ghost" icon={status.label === '已暂停' ? <Play size={14} /> : <Pause size={14} />} onClick={() => void toggle(bot)} disabled={saving || status.label === '已归档'}>{status.label === '已暂停' ? '恢复' : '暂停'}</Button>
                </div>
              </article>;
            })}
            {!bots.length ? <div className="module-empty"><UserRound size={22} /><strong>还没有私人 Bot</strong><span>先创建一个 Bot，再绑定角色和平台。</span><Button variant="primary" icon={<Plus size={15} />} onClick={openCreate}>创建第一个 Bot</Button></div> : null}
          </div>
        )}
      </Panel>
      {!legacy && activeBot ? <Panel accent="amber">
        <PanelHeading title="最近任务" description="沿用 Core 的真实运行记录；失败任务可以从原任务入口重试。" action={<Button variant="ghost" icon={<RefreshCw size={15} className={tasksLoading ? 'spin' : ''} />} onClick={() => { void loadPrivateBotTasks(activeBot.id, activeBot.ownerRef || 'admin').then((result) => setTasks(result.items.slice(0, 8))); }} disabled={tasksLoading}>刷新</Button>} />
        {tasksLoading && !tasks.length ? <div className="module-loading"><RefreshCw className="spin" size={20} /><strong>正在读取任务</strong><span>同步这个 Bot 的运行状态</span></div> : tasks.length ? <div className="module-record-list">
          {tasks.map((task) => <article className="module-record bot-task-record" key={task.runId}>
            <span className="module-record-icon"><ListChecks size={16} /></span>
            <div className="module-record-main">
              <div className="module-record-title"><strong>{task.taskId || task.runId}</strong><span className="module-status">{task.state}</span></div>
              <p>{task.errorCode ? `失败原因：${task.errorCode}` : `${task.stepCount} 个执行步骤 · 最近更新 ${task.updatedAt || '未知'}`}</p>
            </div>
            <div className="module-row-actions">
              <a className="ui-button ui-button-ghost ui-button-text" href={task.detailUrl}>查看</a>
              {task.retryable ? <Button variant="ghost" icon={<RotateCcw size={14} />} onClick={() => { void retryTask(task.runId).then(() => loadPrivateBotTasks(activeBot.id, activeBot.ownerRef || 'admin')).then((result) => setTasks(result.items.slice(0, 8))); }}>重试</Button> : null}
            </div>
          </article>)}
        </div> : <div className="module-empty"><ListChecks size={22} /><strong>还没有任务记录</strong><span>这个 Bot 接到任务后，执行步骤和失败恢复会显示在这里。</span></div>}
      </Panel> : null}
      <InfoDialog open={Boolean(dialog)} onOpenChange={(open) => !open && !saving && setDialog(null)} title={dialog === 'copy' ? '复制私人 Bot' : '创建私人 Bot'} description={dialog === 'copy' ? '只复制公开配置；聊天、记忆、附件、凭据和平台会话保持隔离。' : '先给 Bot 一个容易识别的名字，后续再绑定角色、模型和平台。'}>
        <div className="bot-form">
          <label className="input-field"><span>Bot 名称</span><input value={name} onChange={(event) => setName(event.target.value)} placeholder="例如：豆包·私人版" disabled={saving} autoFocus /></label>
          {dialog === 'create' ? <label className="input-field"><span>初始角色</span><select value={personaId} onChange={(event) => setPersonaId(event.target.value)} disabled={saving}><option value="">暂不绑定角色</option>{personas.map((persona) => <option value={persona.id} key={persona.id}>{persona.name || persona.id}</option>)}</select></label> : null}
          <div className="bot-form-actions"><Button variant="secondary" onClick={() => setDialog(null)} disabled={saving}>取消</Button><Button variant="primary" onClick={() => void submit()} disabled={saving || (dialog === 'create' && !personaId)}>{saving ? '正在保存…' : dialog === 'copy' ? '复制 Bot' : '创建 Bot'}</Button></div>
        </div>
      </InfoDialog>
    </div>
  );
}

