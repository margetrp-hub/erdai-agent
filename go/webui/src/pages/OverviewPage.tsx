import {
  ArrowUpRight,
  ArrowUpCircle,
  Bot,
  Cable,
  Database,
  FileText,
  GitBranch,
  HeartPulse,
  PackageCheck,
  RefreshCw,
  ShieldCheck,
  Sparkles,
  Wrench,
} from 'lucide-react';
import { apiRequest, type DashboardData, type StableUpdate, type UpdateStatus } from '../lib/api';
import { Button, InfoDialog, Panel, PanelHeading } from '../components/ui';
import type { ViewId } from '../components/AppShell';
import { useEffect, useState } from 'react';

type OverviewTab = 'signal' | 'inventory' | 'governance';

const inventory = [
  { key: 'personas', label: '角色卡', icon: Bot, view: 'roles' as ViewId, accent: 'rose' },
  { key: 'knowledge_documents', label: '知识文档', icon: FileText, view: 'knowledge' as ViewId, accent: 'cyan' },
  { key: 'tools', label: '工具', icon: Wrench, view: 'tools' as ViewId, accent: 'amber' },
  { key: 'skills', label: '技能', icon: Sparkles, view: 'skills' as ViewId, accent: 'violet' },
  { key: 'mcp_servers', label: 'MCP 服务', icon: PackageCheck, view: 'tools' as ViewId, accent: 'green' },
  { key: 'platform_integrations', label: '连接器', icon: Cable, view: 'integrations' as ViewId, accent: 'cyan' },
];

const readinessViews: Partial<Record<string, ViewId>> = {
  admin_token: 'system',
  runtime_token: 'system',
  encryption_key: 'system',
  model_provider: 'models',
  semantic_provider: 'system',
  qq_official: 'integrations',
  grok: 'models',
  image: 'models',
  ops: 'integrations',
};

function numberValue(value: number | undefined) {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0);
}

function updateStateLabel(state: string) {
  return ({ idle: '已就绪', pending: '等待执行', running: '升级中', succeeded: '升级完成', failed: '升级失败' } as Record<string, string>)[state] || state;
}

function capabilityStatusLabel(status?: string) {
  return ({ available: '真实任务可用', degraded: '真实任务异常', unverified: '待真实任务验证' } as Record<string, string>)[status || ''] || '暂无证据';
}

export function OverviewPage({
  data,
  activeInstanceId,
  onNavigate,
}: {
  data: DashboardData;
  activeInstanceId?: string;
  onNavigate: (view: ViewId) => void;
}) {
  const [dialogOpen, setDialogOpen] = useState(false);
  const [activeTab, setActiveTab] = useState<OverviewTab>('signal');
  const [update, setUpdate] = useState<StableUpdate | null>(null);
  const [updateStatus, setUpdateStatus] = useState<UpdateStatus | null>(null);
  const [checkingUpdate, setCheckingUpdate] = useState(false);
  const [requestingUpdate, setRequestingUpdate] = useState(false);
  const [updateError, setUpdateError] = useState('');
  const counts = data.overview.counts || {};
  const models = data.overview.models || {};
  const observability = data.observability || {};
  const activeInstance = (data.agentInstances.items || []).find((instance) => instance.id === activeInstanceId) || data.agentInstances.items?.[0];
  const activePersona = (data.personas.items || []).find((persona) => persona.id === (activeInstance?.personaId || data.config.activePersonaId));
  const facts = [
    ['Core Schema', `v${data.overview.schemaVersion ?? 0}`],
    ['端点探针', `${models.healthy ?? 0} 健康 / ${models.unhealthy ?? 0} 异常`],
    ['路由模式', data.overview.routing?.mode || 'auto'],
    ['自动学习', data.config.learningEnabled ? '已启用' : '已关闭'],
    ['生图', capabilityStatusLabel(observability.media?.image?.status)],
    ['生视频', capabilityStatusLabel(observability.media?.video?.status)],
    ['知识召回（24h）', `${observability.retrieval?.queryCount24h ?? 0} 次`],
    ['记忆召回（24h）', `${observability.memory?.recallCount24h ?? 0} 次`],
  ];

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
    <div className="view-stage">
      <div className="metric-rail">
        <article className="metric-cell metric-cyan">
          <span>当前实例</span>
          <strong>{activeInstance?.displayName || activeInstance?.id || '未登记实例'}</strong>
          <small>角色：{activePersona?.name || activePersona?.id || '未绑定'}</small>
        </article>
        <article className="metric-cell metric-rose">
          <span>运行实例</span>
          <strong>{numberValue(data.agentInstances.items?.length)}</strong>
          <small>独立运行边界</small>
        </article>
        <article className="metric-cell metric-amber">
          <span>探针健康</span>
          <strong>
            {models.healthy ?? 0} <em>/ {models.configured ?? 0}</em>
          </strong>
          <small>接口探针，不代表真实生成</small>
        </article>
        <article className="metric-cell metric-violet">
          <span>累计任务</span>
          <strong>{numberValue(counts.runs)}</strong>
          <small>Durable runs</small>
        </article>
      </div>

      <Panel accent={data.installation.ready ? 'green' : 'amber'} className="readiness-panel">
        <PanelHeading
          eyebrow="INSTALLATION / READINESS"
          title={data.installation.ready ? '运行条件已满足' : '首次启动还缺少配置'}
          description={`${data.installation.configuredCount} 项已配置 · ${data.installation.requiredCount} 项为运行必需。敏感值只保留在运行环境，不会回显。`}
          action={(
            <div className="panel-heading-actions">
              <Button variant="secondary" icon={<ArrowUpRight size={15} />} onClick={() => setDialogOpen(true)}>运行契约</Button>
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
                    setUpdateError(cause instanceof Error ? cause.message : 'Stable 更新检查失败');
                  } finally {
                    setCheckingUpdate(false);
                  }
                }}
              >
                {checkingUpdate ? '检查中' : '检查 Stable 更新'}
              </Button>
            </div>
          )}
        />
        <div className="readiness-grid">
          {data.installation.checks.map((check) => (
            <button
              className={`readiness-item ${check.configured ? 'is-ready' : check.required ? 'is-required' : 'is-optional'}`}
              type="button"
              key={check.id}
              onClick={() => {
                const view = readinessViews[check.id];
                if (view) onNavigate(view);
              }}
            >
              <span className="readiness-indicator" aria-hidden="true">{check.configured ? '✓' : check.required ? '!' : '·'}</span>
              <span className="readiness-copy">
                <strong>{check.label}</strong>
                <small>{check.configured ? '已配置' : check.required ? '待配置' : '未启用'} · {check.detail}</small>
              </span>
            </button>
          ))}
        </div>
        {updateError ? <p className="form-error readiness-feedback">{updateError}</p> : null}
        {update ? (
          <div className={`update-result ${update.updateAvailable ? 'is-available' : ''}`}>
            <span>当前 v{update.currentVersion}</span>
            <strong>{update.latestVersion ? `Stable v${update.latestVersion}` : '暂无可用 Stable 发布'}</strong>
            <div className="update-actions">
              {update.updateAvailable && update.releaseUrl ? <a className="ui-button ui-button-ghost ui-button-text" href={update.releaseUrl} target="_blank" rel="noreferrer">查看发行版</a> : null}
              {update.updateAvailable && update.upgradeReady ? (
                <Button
                  variant="primary"
                  icon={<ArrowUpCircle size={14} />}
                  disabled={!updateStatus?.agentReady || requestingUpdate || updateStatus?.state === 'pending' || updateStatus?.state === 'running'}
                  onClick={async () => {
                    if (!window.confirm(`确认升级到 Stable v${update.latestVersion}？升级期间服务会短暂重启。`)) return;
                    setRequestingUpdate(true);
                    setUpdateError('');
                    try {
                      const status = await apiRequest<UpdateStatus>('/api/v1/update/request', {
                        method: 'POST',
                        body: JSON.stringify({ version: update.latestVersion }),
                      });
                      setUpdateStatus(status);
                    } catch (cause) {
                      setUpdateError(cause instanceof Error ? cause.message : 'Stable 升级请求失败');
                    } finally {
                      setRequestingUpdate(false);
                    }
                  }}
                >
                  {requestingUpdate ? '提交中' : '提交 Stable 升级'}
                </Button>
              ) : null}
            </div>
          </div>
        ) : null}
        {updateStatus ? (
          <div className={`update-status ${updateStatus.agentReady ? 'is-ready' : 'is-unavailable'}`}>
            <span>宿主机升级代理</span>
            <strong>{updateStatus.agentReady ? updateStateLabel(updateStatus.state) : '未就绪'}</strong>
            <small>{updateStatus.message || '只有受信任的宿主机代理会执行下载、校验、备份和回滚。'}</small>
          </div>
        ) : null}
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
            运行信号
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
            能力库存
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
            治理观察
          </button>
        </div>

        {activeTab === 'signal' ? <div className="tabs-content" role="tabpanel" id="overview-panel-signal">
          <div className="overview-grid overview-grid-signal">
            <Panel accent="cyan">
              <PanelHeading
                eyebrow="RUNTIME FACTS"
                title="运行事实"
                description="当前实例正在使用的核心配置和健康状态。"
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
                title="消息链路"
                description="从入站事件到 Outbox 投递的连续路径。"
              />
              <div className="flow-track">
                <FlowNode label="入站事件" value={counts.transport_events} detail="Transport v2" icon={Database} />
                <span className="flow-arrow">→</span>
                <FlowNode label="任务执行" value={counts.runs} detail="Go Core" icon={HeartPulse} />
                <span className="flow-arrow">→</span>
                <FlowNode label="投递记录" value={counts.deliveries} detail="Outbox" icon={GitBranch} />
              </div>
            </Panel>
          </div>
        </div> : null}

        {activeTab === 'inventory' ? <div className="tabs-content" role="tabpanel" id="overview-panel-inventory">
          <Panel accent="amber">
            <PanelHeading
              eyebrow="CAPABILITY INVENTORY"
              title="能力库存"
              description="点击进入对应模块；迁移期间旧页面仍然保留。"
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
              <PanelHeading eyebrow="GOVERNANCE" title="治理状态" description="需要持续关注的审计与学习信号。" />
              <div className="governance-grid">
                <GovernanceStat label="管理员指令" value={counts.admin_directives} />
                <GovernanceStat label="待审知识" value={counts.knowledge_candidates} tone="warn" />
                <GovernanceStat label="审计事件" value={counts.audit_events} />
                <GovernanceStat label="阶段事件" value={counts.run_stage_events} />
              </div>
            </Panel>
            <Panel accent="rose">
              <PanelHeading eyebrow="CONTROL NOTE" title="配置边界" description="Core 是唯一配置源，页面只负责观察和编辑。" />
              <div className="control-note">
                <ShieldCheck size={22} />
                <p>敏感凭据不会回显到 WebUI。页面迁移期间，所有接口仍然走同源的管理员会话。</p>
              </div>
            </Panel>
          </div>
        </div> : null}
      </div>

      <InfoDialog
        open={dialogOpen}
        onOpenChange={setDialogOpen}
        title="运行契约"
        description="现代 WebUI 第一阶段仅替换展示层，不改变 Core 的数据和权限边界。"
      >
        <div className="dialog-contract">
          <div>
            <span>API source</span>
            <strong>/api/v1/overview</strong>
          </div>
          <div>
            <span>auth boundary</span>
            <strong>same-origin admin session</strong>
          </div>
          <div>
            <span>release channel</span>
            <strong>Stable only</strong>
          </div>
        </div>
      </InfoDialog>
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
