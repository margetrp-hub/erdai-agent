import {
  ArrowUpRight,
  ArrowUpCircle,
  Bot,
  Cable,
  Coins,
  Cpu,
  Database,
  FileText,
  GitBranch,
  HeartPulse,
  Image,
  ListChecks,
  MessageCircle,
  PackageCheck,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  Wrench,
} from 'lucide-react';
import { apiRequest, collectionItems, type AppearanceLibrary, type DashboardData, type InstallationCheck, type JsonMap, type MediaCapabilityStatus, type StableUpdate, type UpdateStatus } from '../lib/api';
import { Button, InfoDialog, Panel, PanelHeading } from '../components/ui';
import type { Navigate, ViewId } from '../components/AppShell';
import { TaskDetailDialog } from '../components/TaskDetailDialog';
import { useEffect, useState } from 'react';

type OverviewTab = 'signal' | 'inventory' | 'governance';

const quickActions: Array<{ title: string; detail: string; view: ViewId; section?: string; icon: typeof Bot }> = [
  { title: '调整群聊回复', detail: '什么时候回复、如何主动接话', view: 'system', section: 'runtime', icon: MessageCircle },
  { title: '设置照片视频', detail: '每日次数、管理员和不限额成员', view: 'system', section: 'quotas', icon: Image },
  { title: '更换角色外观', detail: '选择外观库、管理参考图片', view: 'roles', section: 'visual', icon: Bot },
  { title: '更换 AI 模型', detail: '聊天、生图与视频使用的模型', view: 'models', section: 'endpoints', icon: Cpu },
  { title: '管理积分活动', detail: '查看账户、流水与兑换订单', view: 'points', icon: Coins },
  { title: '查看任务记录', detail: '检查回复和生成是否成功', view: 'operations', section: 'runs', icon: ListChecks },
];

const inventory = [
  { key: 'personas', label: '角色卡', icon: Bot, view: 'roles' as ViewId, accent: 'rose' },
  { key: 'knowledge_documents', label: '知识文档', icon: FileText, view: 'knowledge' as ViewId, accent: 'cyan' },
  { key: 'tools', label: '工具', icon: Wrench, view: 'tools' as ViewId, accent: 'amber' },
  { key: 'skills', label: '技能', icon: Sparkles, view: 'skills' as ViewId, accent: 'violet' },
  { key: 'mcp_servers', label: 'MCP 服务', icon: PackageCheck, view: 'tools' as ViewId, accent: 'green' },
  { key: 'platform_integrations', label: '连接器', icon: Cable, view: 'integrations' as ViewId, accent: 'cyan' },
];

const readinessViews: Partial<Record<string, ViewId>> = {
  model_provider: 'models',
  qq_official: 'integrations',
  grok: 'models',
  image: 'models',
  ops: 'integrations',
};

const checkCopy: Record<string, { label: string; detail: string }> = {
  admin_token: { label: '后台登录保护', detail: '由服务器管理员设置登录密钥。' },
  runtime_token: { label: '服务之间的连接密钥', detail: '需要在服务器的运行配置中设置。' },
  encryption_key: { label: '运行数据加密', detail: '需要在服务器的运行配置中设置加密密钥。' },
  model_provider: { label: '聊天模型凭据', detail: '检查已配置的模型服务商与接口密钥。' },
  semantic_provider: { label: '语义搜索服务', detail: '用于理解和检索记忆，需要配置服务器连接密钥。' },
  qq_official: { label: 'QQ 平台连接', detail: '在平台连接页面管理和检查。' },
  grok: { label: '搜索与多媒体服务', detail: '在模型与连接页面管理。' },
  image: { label: '图片生成服务', detail: '在模型与连接页面管理。' },
};

function numberValue(value: number | undefined) {
  return value === undefined ? '—' : new Intl.NumberFormat('zh-CN').format(value);
}

function updateStateLabel(state: string) {
  return ({ idle: '已就绪', pending: '等待执行', running: '升级中', succeeded: '升级完成', failed: '升级失败' } as Record<string, string>)[state] || state;
}

function capabilityStatusLabel(capability?: MediaCapabilityStatus) {
  if (!capability) return '暂无记录';
  if (capability.probeHealthy === false) return '接口连接异常';
  if (capability.status === 'degraded') return (capability.failureCount || 0) > 0 ? '近期生成异常' : '生成状态待检查';
  if (capability.status === 'available' && (capability.successCount || 0) > 0) return '已有成功记录';
  return '尚未验证生成';
}

function capabilityStatusDetail(capability?: MediaCapabilityStatus) {
  if (capability?.probeHealthy === false) return '接口检查未通过，请检查模型连接';
  return '按实际生成记录判断';
}

function runStateLabel(state: string) {
  return ({ received: '已收到', queued: '排队中', running: '执行中', prepared: '等待发送', delivered: '已发送', failed: '失败', cancelled: '已取消', ignored: '未回复', completed: '已完成', succeeded: '已完成', superseded: '已被新任务替代' } as Record<string, string>)[state] || '查看任务状态';
}

function recentTime(value: unknown) {
  if (typeof value !== 'string' || !Number.isFinite(Date.parse(value))) return '时间未记录';
  return new Intl.DateTimeFormat('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', timeZone: 'Asia/Shanghai' }).format(new Date(value));
}

export function OverviewPage({
  data,
  activeInstanceId,
  onNavigate,
}: {
  data: DashboardData;
  activeInstanceId?: string;
  onNavigate: Navigate;
}) {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [checkDialog, setCheckDialog] = useState<InstallationCheck | null>(null);
  const [activeTab, setActiveTab] = useState<OverviewTab>('signal');
  const [update, setUpdate] = useState<StableUpdate | null>(null);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [requestingUpdate, setRequestingUpdate] = useState(false);
  const [updateError, setUpdateError] = useState('');
  const [recentRuns, setRecentRuns] = useState<JsonMap[] | null>(null);
  const [platforms, setPlatforms] = useState<JsonMap[] | null>(null);
  const [appearanceName, setAppearanceName] = useState('读取中…');
  const [runsError, setRunsError] = useState('');
  const [platformError, setPlatformError] = useState('');
  const [detailsNonce, setDetailsNonce] = useState(0);
  const [taskRunId, setTaskRunId] = useState<string | null>(null);
  const counts = data.overview.counts || {};
  const models = data.overview.models || {};
  const observability = data.observability || {};
  const activeInstance = (data.agentInstances.items || []).find((instance) => instance.id === activeInstanceId) || data.agentInstances.items?.[0];
  const activePersona = (data.personas.items || []).find((persona) => persona.id === (activeInstance?.personaId || data.config.activePersonaId));
  const missingChecks = data.installation.checks.filter((check) => check.required && !check.configured);
  const remainingChecks = data.installation.checks.filter((check) => !check.required || check.configured);
  const connectedPlatforms = platforms?.filter((platform) => platform.status === 'connected').length;
  const facts = [
    ['数据结构版本', data.overview.schemaVersion === undefined ? '未读取' : `v${data.overview.schemaVersion}`],
    ['模型接口检查', `${numberValue(models.healthy)} 正常 / ${numberValue(models.unhealthy)} 异常`],
    ['模型分工方式', ({ auto: '自动选择', manual: '手动指定', locked: '锁定模型' } as Record<string, string>)[data.overview.routing?.mode || ''] || data.overview.routing?.mode || '未读取'],
    ['自动学习', data.config.learningEnabled === undefined ? '未读取' : data.config.learningEnabled ? '已启用' : '已关闭'],
    ['图片生成', capabilityStatusLabel(observability.media?.image)],
    ['视频生成', capabilityStatusLabel(observability.media?.video)],
    ['查询知识（24 小时）', `${numberValue(observability.retrieval?.queryCount24h)} 次`],
    ['查询记忆（24 小时）', `${numberValue(observability.memory?.recallCount24h)} 次`],
  ];

  useEffect(() => {
    let current = true;
    setRecentRuns(null);
    setPlatforms(null);
    setRunsError('');
    setPlatformError('');
    void Promise.allSettled([
      apiRequest<unknown>('/api/v1/runs').then((value) => { if (current) setRecentRuns(collectionItems<JsonMap>(value).slice(0, 5)); }).catch((cause) => { if (current) setRunsError(cause instanceof Error ? cause.message : '任务记录读取失败'); }),
      apiRequest<unknown>('/api/v1/platforms/runtime-status').then((value) => { if (current) setPlatforms(collectionItems<JsonMap>(value)); }).catch((cause) => { if (current) setPlatformError(cause instanceof Error ? cause.message : '平台状态读取失败'); }),
    ]);
    return () => { current = false; };
  }, [data, detailsNonce]);

  useEffect(() => {
    let current = true;
    if (!activePersona) { setAppearanceName('先选择角色'); return; }
    setAppearanceName('读取中…');
    apiRequest<{ libraryId?: string; library?: AppearanceLibrary }>(`/api/v1/personas/${encodeURIComponent(activePersona.id)}/appearance-library?namespace=default`)
      .then((value) => { if (current) setAppearanceName(value.library?.name || (value.libraryId ? '已关联外观库' : '尚未选择')); })
      .catch(() => { if (current) setAppearanceName('暂时无法读取'); });
    return () => { current = false; };
  }, [activePersona, detailsNonce]);

  useEffect(() => {
    if (updateStatus?.state !== 'pending' && updateStatus?.state !== 'running') return;
    const timer = window.setInterval(async () => {
      try {
        setUpdateStatus(await apiRequest<UpdateStatus>('/api/v1/update/status'));
      } catch (cause) {
        setUpdateError(cause instanceof Error ? cause.message : '升级状态读取失败');
      }
    }, 3000);
    return () => window.clearInterval(timer);
  }, [updateStatus?.state]);

  return (
    <div className="view-stage friendly-overview">
      <header className="overview-welcome">
        <div><span className="overview-eyebrow">你的管理工作台</span><h1>{activeInstance?.displayName || '二呆智能体'}</h1><p>角色：{activePersona?.name || activePersona?.id || '尚未选择'}<span>·</span>外观：{appearanceName}</p></div>
        <button className="overview-instance-state" type="button" onClick={() => onNavigate('runtime')}>{activeInstance?.enabled === true ? '机器人已启用' : activeInstance?.enabled === false ? '机器人已停用' : '查看机器人设置'}<ArrowUpRight size={15} /></button>
      </header>
      <section className="quick-actions" aria-label="常用操作">
        {quickActions.map((item) => <button type="button" className="quick-action" key={item.title} onClick={() => onNavigate(item.view, item.section)}><span className="quick-action-icon"><item.icon size={21} /></span><span><strong>{item.title}</strong><small>{item.detail}</small></span><ArrowUpRight size={17} /></button>)}
      </section>
      <button className="capability-shortcut" type="button" onClick={() => onNavigate('capabilities')}><Sparkles size={18} /><span><strong>管理能力</strong><small>查看机器人能做什么，按需启用功能与插件</small></span><ArrowUpRight size={16} /></button>
      <div className="metric-rail">
        <article className="metric-cell metric-cyan">
          <span>平台连接 · 全部机器人</span>
          <strong>{platformError ? '读取失败' : platforms === null ? '读取中…' : platforms.length ? `${connectedPlatforms} / ${platforms.length}` : '尚无连接'}</strong>
          <small>{platforms?.length ? '已连接 / 已登记平台' : '状态来自平台实际连接'}</small>
        </article>
        <article className="metric-cell metric-rose">
          <span>图片生成</span>
          <strong>{capabilityStatusLabel(observability.media?.image)}</strong>
          <small>{capabilityStatusDetail(observability.media?.image)}</small>
        </article>
        <article className="metric-cell metric-amber">
          <span>模型接口连接</span>
          <strong>
            {numberValue(models.healthy)} <em>/ {numberValue(models.configured)}</em>
          </strong>
          <small>检查通过 / 已配置，不代表生成成功</small>
        </article>
        <article className="metric-cell metric-violet">
          <span>视频生成</span>
          <strong>{capabilityStatusLabel(observability.media?.video)}</strong>
          <small>{capabilityStatusDetail(observability.media?.video)}</small>
        </article>
      </div>

      <Panel accent={data.installation.ready ? 'green' : 'amber'} className="readiness-panel">
        <PanelHeading
          eyebrow="INSTALLATION / READINESS"
          title={data.installation.ready ? '基础配置已齐全' : '这些配置需要处理'}
          description={data.installation.ready ? '接下来可以调整聊天方式，或从上面的常用操作开始。配置齐全不代表每项功能都已验证成功。' : `还有 ${missingChecks.length} 项必要配置未完成，先处理下方项目。`}
          action={(
            <div className="panel-heading-actions">
              <Button variant="secondary" icon={<ArrowUpRight size={15} />} onClick={() => setDialogOpen(true)}>使用说明</Button>
              <Button
                variant="secondary"
                icon={<RefreshCw size={15} className={checkingUpdate ? 'spin' : ''} />}
                disabled={checkingUpdate}
                onClick={async () => {
                  setCheckingUpdate(true);
                  setUpdateError('');
                  try {
                    const [stable, status] = await Promise.all([
                      apiRequest<StableUpdate>('/api/v1/update/check'),
                      apiRequest<UpdateStatus>('/api/v1/update/status'),
                    ]);
                    setUpdate(stable);
                    setUpdateStatus(status);
                  } catch (cause) {
                    setUpdateError(cause instanceof Error ? cause.message : '新版本检查失败');
                  } finally {
                    setCheckingUpdate(false);
                  }
                }}
              >
                {checkingUpdate ? '检查中' : '检查新版本'}
              </Button>
            </div>
          )}
        />
        {missingChecks.length ? <div className="readiness-grid">
          {missingChecks.map((check) => (
            <button
              className={`readiness-item ${check.configured ? 'is-ready' : check.required ? 'is-required' : 'is-optional'}`}
              type="button"
              key={check.id}
              onClick={() => setCheckDialog(check)}
            >
              <span className="readiness-indicator" aria-hidden="true">{check.configured ? '✓' : check.required ? '!' : '·'}</span>
              <span className="readiness-copy">
                <strong>{checkCopy[check.id]?.label || check.label}</strong>
                <small>{checkCopy[check.id]?.detail || '点击查看配置要求。'}</small>
              </span>
            </button>
          ))}
        </div> : null}
        <details className="readiness-details"><summary>查看全部配置检查（{data.installation.configuredCount} 项已配置）</summary><div className="readiness-grid">{remainingChecks.map((check) => <button type="button" onClick={() => setCheckDialog(check)} className={`readiness-item ${check.configured ? 'is-ready' : 'is-optional'}`} key={check.id}><span className="readiness-indicator" aria-hidden="true">{check.configured ? '✓' : '·'}</span><span className="readiness-copy"><strong>{checkCopy[check.id]?.label || check.label}</strong><small>{check.configured ? '已配置' : '可选，尚未启用'}</small></span></button>)}</div></details>
        {platformError ? <p className="form-error readiness-feedback" role="alert">平台连接状态暂时无法读取。<Button onClick={() => setDetailsNonce((value) => value + 1)}>重试读取</Button></p> : null}
        {updateError ? <p className="form-error readiness-feedback">{updateError}</p> : null}
        {update ? (
          <div className={`update-result ${update.updateAvailable ? 'is-available' : ''}`}>
            <span>当前 v{update.currentVersion}</span>
            <strong>{update.latestVersion ? `稳定版 v${update.latestVersion}` : '暂无可用新版本'}</strong>
            <div className="update-actions">
              {update.updateAvailable && update.releaseUrl ? <a className="ui-button ui-button-ghost ui-button-text" href={update.releaseUrl} target="_blank" rel="noreferrer">查看发行版</a> : null}
              {update.updateAvailable && update.upgradeReady ? (
                <Button
                  variant="primary"
                  icon={<ArrowUpCircle size={14} />}
                  disabled={!updateStatus?.agentReady || requestingUpdate || updateStatus?.state === 'pending' || updateStatus?.state === 'running'}
                  onClick={async () => {
                    if (!window.confirm(`确认升级到 v${update.latestVersion}？升级期间服务会短暂重启。`)) return;
                    setRequestingUpdate(true);
                    setUpdateError('');
                    try {
                      const status = await apiRequest<UpdateStatus>('/api/v1/update/request', {
                        method: 'POST',
                        body: JSON.stringify({ version: update.latestVersion }),
                      });
                      setUpdateStatus(status);
                    } catch (cause) {
                      setUpdateError(cause instanceof Error ? cause.message : '升级请求失败');
                    } finally {
                      setRequestingUpdate(false);
                    }
                  }}
                >
                  {requestingUpdate ? '提交中' : '升级到新版本'}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        {updateStatus ? (
          <div className={`update-status ${updateStatus.agentReady ? 'is-ready' : 'is-unavailable'}`}>
            <span>服务器升级状态</span>
            <strong>{updateStatus.agentReady ? updateStateLabel(updateStatus.state) : '未就绪'}</strong>
            <small>{updateStatus.message || '升级任务由服务器执行，可在这里查看进度。'}</small>
          </div>
        ) : null}
      </Panel>

      <Panel accent="cyan" className="recent-tasks-panel">
        <PanelHeading title="最近任务" description="全部机器人的最新 5 条记录 · 时间为北京时间" action={<Button onClick={() => onNavigate('operations', 'runs')}>查看全部记录</Button>} />
        {runsError ? <div className="overview-empty" role="alert"><p>{runsError}</p><Button onClick={() => setDetailsNonce((value) => value + 1)}>重新读取</Button></div> : recentRuns === null ? <p className="overview-empty" role="status">正在读取任务…</p> : !recentRuns.length ? <p className="overview-empty">还没有任务记录，开始聊天后会出现在这里。</p> : <div className="recent-task-list">{recentRuns.map((run) => <button className="recent-task" type="button" key={String(run.id)} onClick={() => setTaskRunId(String(run.id))}><span className="recent-task-state" data-state={String(run.state)}>{runStateLabel(String(run.state))}</span><span className="recent-task-copy"><strong>{String(run.selectedModel || '对话任务')}</strong><small>{String(run.transport || '平台未记录')}{run.errorCode ? ' · 存在错误，点击查看详情' : ' · 点击查看任务详情'}</small></span><time>{recentTime(run.createdAt)}</time><ArrowUpRight size={16} /></button>)}</div>}
      </Panel>

      <div className="overview-tabs">
        <div className="tabs-list" role="tablist" aria-label="总览面板">
          <button
            className="tabs-trigger"
            type="button"
            role="tab"
            aria-selected={activeTab === 'signal'}
            aria-controls="overview-panel-signal"
            data-state={activeTab === 'signal' ? 'active' : 'inactive'}
            onClick={() => setActiveTab('signal')}
          >
            运行详情
          </button>
          <button
            className="tabs-trigger"
            type="button"
            role="tab"
            aria-selected={activeTab === 'inventory'}
            aria-controls="overview-panel-inventory"
            data-state={activeTab === 'inventory' ? 'active' : 'inactive'}
            onClick={() => setActiveTab('inventory')}
          >
            已有内容与能力
          </button>
          <button
            className="tabs-trigger"
            type="button"
            role="tab"
            aria-selected={activeTab === 'governance'}
            aria-controls="overview-panel-governance"
            data-state={activeTab === 'governance' ? 'active' : 'inactive'}
            onClick={() => setActiveTab('governance')}
          >
            管理记录
          </button>
        </div>

        {activeTab === 'signal' ? <div className="tabs-content" role="tabpanel" id="overview-panel-signal">
          <div className="overview-grid overview-grid-signal">
            <Panel accent="cyan">
              <PanelHeading
                eyebrow="RUNTIME FACTS"
                title="运行详情"
                description="全局配置与近期使用情况，供需要时进一步查看。"
              />
              <div className="fact-list">
                {facts.map(([label, value]) => (
                  <div className="fact-row" key={label}>
                    <span>{label}</span>
                    <strong>{value}</strong>
                  </div>
                ))}
              </div>
            </Panel>
            <Panel accent="green">
              <PanelHeading
                eyebrow="MESSAGE FLOW"
                title="消息处理情况"
                description="收到消息、执行任务和发送回复的累计记录。"
              />
              <div className="flow-track">
                <FlowNode label="收到消息" value={counts.transport_events} detail="累计接收记录" icon={Database} />
                <span className="flow-arrow">→</span>
                <FlowNode label="执行任务" value={counts.runs} detail="累计任务记录" icon={HeartPulse} />
                <span className="flow-arrow">→</span>
                <FlowNode label="发送回复" value={counts.deliveries} detail="累计发送记录" icon={GitBranch} />
              </div>
            </Panel>
          </div>
        </div> : null}

        {activeTab === 'inventory' ? <div className="tabs-content" role="tabpanel" id="overview-panel-inventory">
          <Panel accent="amber">
            <PanelHeading
              eyebrow="CAPABILITY INVENTORY"
              title="已有内容与能力"
              description="点击继续管理对应内容。已登记不等于已启用或可调用。"
            />
            <div className="inventory-grid">
              {inventory.map((item) => {
                const Icon = item.icon;
                return (
                  <button className={`inventory-item inventory-${item.accent}`} type="button" key={item.key} onClick={() => onNavigate(item.view)}>
                    <span className="inventory-icon">
                      <Icon size={17} />
                    </span>
                    <span className="inventory-copy">
                      <strong>{numberValue(counts[item.key])}</strong>
                      <small>{item.label}</small>
                    </span>
                    <ArrowUpRight size={15} className="inventory-arrow" />
                  </button>
                );
              })}
            </div>
          </Panel>
        </div> : null}

        {activeTab === 'governance' ? <div className="tabs-content" role="tabpanel" id="overview-panel-governance">
          <div className="overview-grid overview-grid-governance">
            <Panel accent="violet">
              <PanelHeading eyebrow="GOVERNANCE" title="管理记录" description="管理员操作、知识审核和任务记录。" />
              <div className="governance-grid">
                <GovernanceStat label="管理员指令" value={counts.admin_directives} />
                <GovernanceStat label="待审知识" value={counts.knowledge_candidates} tone="warn" />
                <GovernanceStat label="审计事件" value={counts.audit_events} />
                <GovernanceStat label="阶段事件" value={counts.run_stage_events} />
              </div>
            </Panel>
            <Panel accent="rose">
              <PanelHeading eyebrow="CONTROL NOTE" title="设置与隐私" description="打开页面只读取已有配置，不会自动修改机器人。" />
              <div className="control-note">
                <ShieldCheck size={22} />
                <p>修改设置后需要点击保存。接口密钥等敏感内容不会直接显示；全局设置会影响使用它的所有机器人。</p>
              </div>
            </Panel>
          </div>
        </div> : null}
      </div>

      <InfoDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title="从常用操作开始"
        description="先选择顶部的机器人，再进入要调整的功能。每张表单都会说明它影响的范围。"
      >
        <div className="dialog-contract">
          <div>
            <span>想调整聊天</span>
            <strong>进入“聊天与群聊”，选择回复方式</strong>
          </div>
          <div>
            <span>想调整图片或视频次数</span>
            <strong>进入“照片与视频”，设置每天的额度</strong>
          </div>
          <div>
            <span>需要进一步排查</span>
            <strong>查看任务详情，或使用左侧“找功能”</strong>
          </div>
        </div>
      </InfoDialog>
      <InfoDialog open={checkDialog !== null} onOpenChange={(open) => { if (!open) setCheckDialog(null); }} title={checkDialog ? checkCopy[checkDialog.id]?.label || checkDialog.label : '配置说明'} description={checkDialog ? checkCopy[checkDialog.id]?.detail || '查看此项配置的要求与来源。' : undefined}>
        {checkDialog ? <div className="installation-help"><p>{checkDialog.configured ? '这项基础配置已经就绪。功能是否正常，还需要结合实际任务记录判断。' : readinessViews[checkDialog.id] ? '请检查对应服务的设置。保存后刷新首页，再查看配置检查结果。' : '这项配置由服务器运行环境管理，不能在普通设置表单中修改。请让服务器管理员补齐后刷新检查结果。'}</p>{readinessViews[checkDialog.id] ? <Button variant="primary" onClick={() => { onNavigate(readinessViews[checkDialog.id]!, checkDialog.id === 'model_provider' ? 'connections' : undefined); setCheckDialog(null); }}>前往设置</Button> : null}<details><summary>技术信息</summary><p>检查项：{checkDialog.id}</p><p>配置来源：{checkDialog.source || '未记录'}</p><p>服务端说明：{checkDialog.detail || '未记录'}</p></details></div> : null}
      </InfoDialog>
      <TaskDetailDialog runId={taskRunId} onClose={() => setTaskRunId(null)} onChanged={() => setDetailsNonce((value) => value + 1)} />
    </div>
  );
}

function FlowNode({
  label,
  value,
  detail,
  icon: Icon,
}: {
  label: string;
  value?: number;
  detail: string;
  icon: typeof Database;
}) {
  return (
    <div className="flow-node">
      <span className="flow-icon">
        <Icon size={15} />
      </span>
      <span>{label}</span>
      <strong>{numberValue(value)}</strong>
      <small>{detail}</small>
    </div>
  );
}

function GovernanceStat({ label, value, tone = 'normal' }: { label: string; value?: number; tone?: 'normal' | 'warn' }) {
  return (
    <div className={`governance-stat ${tone === 'warn' ? 'is-warn' : ''}`}>
      <span>{label}</span>
      <strong>{numberValue(value)}</strong>
    </div>
  );
}
