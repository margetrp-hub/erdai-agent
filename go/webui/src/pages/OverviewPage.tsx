import {
  ArrowUpRight,
  Bot,
  Cable,
  Database,
  FileText,
  GitBranch,
  HeartPulse,
  PackageCheck,
  ShieldCheck,
  Sparkles,
  Wrench,
} from 'lucide-react';
import type { DashboardData } from '../lib/api';
import { Button, InfoDialog, Panel, PanelHeading } from '../components/ui';
import type { ViewId } from '../components/AppShell';
import { useState } from 'react';

type OverviewTab = 'signal' | 'inventory' | 'governance';

const inventory = [
  { key: 'personas', label: '角色卡', icon: Bot, view: 'roles' as ViewId, accent: 'rose' },
  { key: 'knowledge_documents', label: '知识文档', icon: FileText, view: 'knowledge' as ViewId, accent: 'cyan' },
  { key: 'tools', label: '工具', icon: Wrench, view: 'tools' as ViewId, accent: 'amber' },
  { key: 'skills', label: '技能', icon: Sparkles, view: 'skills' as ViewId, accent: 'violet' },
  { key: 'mcp_servers', label: 'MCP 服务', icon: PackageCheck, view: 'tools' as ViewId, accent: 'green' },
  { key: 'platform_integrations', label: '连接器', icon: Cable, view: 'integrations' as ViewId, accent: 'cyan' },
];

function numberValue(value: number | undefined) {
  return new Intl.NumberFormat('zh-CN').format(value ?? 0);
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
  const counts = data.overview.counts || {};
  const models = data.overview.models || {};
  const activeInstance = (data.agentInstances.items || []).find((instance) => instance.id === activeInstanceId) || data.agentInstances.items?.[0];
  const activePersona = (data.personas.items || []).find((persona) => persona.id === (activeInstance?.personaId || data.config.activePersonaId));
  const facts = [
    ['Core Schema', `v${data.overview.schemaVersion ?? 0}`],
    ['模型状态', `${models.healthy ?? 0} 健康 / ${models.unhealthy ?? 0} 异常`],
    ['路由模式', data.overview.routing?.mode || 'auto'],
    ['自动学习', data.config.learningEnabled ? '已启用' : '已关闭'],
  ];

  return (
    <div className="view-stage">
      <header className="page-hero">
        <div>
          <span className="hero-kicker">SYSTEM / OVERVIEW</span>
          <h1>运行总览</h1>
          <p>把角色、模型、消息链路和治理状态收在一个可读的控制面上。</p>
        </div>
        <div className="hero-actions">
          <Button variant="secondary" icon={<ArrowUpRight size={15} />} onClick={() => setDialogOpen(true)}>
            查看运行契约
          </Button>
        </div>
      </header>

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
          <span>健康模型</span>
          <strong>
            {models.healthy ?? 0} <em>/ {models.configured ?? 0}</em>
          </strong>
          <small>Fresh health samples</small>
        </article>
        <article className="metric-cell metric-violet">
          <span>累计任务</span>
          <strong>{numberValue(counts.runs)}</strong>
          <small>Durable runs</small>
        </article>
      </div>

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
            <span>fallback</span>
            <strong>ERDAI_WEBUI_MODE=legacy</strong>
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
