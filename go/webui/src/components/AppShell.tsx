import {
  Activity,
  Bot,
  Blocks,
  Cable,
  ChevronDown,
  Cpu,
  Coins,
  Database,
  LayoutDashboard,
  LibraryBig,
  ListChecks,
  LogOut,
  MonitorCog,
  RefreshCw,
  Route,
  Settings,
  ShieldCheck,
  Sparkles,
  Wrench,
} from 'lucide-react';
import type { ReactNode } from 'react';
import type { AgentInstance, Persona } from '../lib/api';
import { ThemeSwitcher } from './ThemeSwitcher';
import { Button, StatusDot } from './ui';

export type ViewId =
  | 'overview'
  | 'operations'
  | 'points'
  | 'runtime'
  | 'roles'
  | 'memories'
  | 'worldbook'
  | 'samples'
  | 'knowledge'
  | 'skills'
  | 'plugins'
  | 'tools'
  | 'integrations'
  | 'models'
  | 'routing'
  | 'devices'
  | 'security'
  | 'system';

type NavIcon = typeof LayoutDashboard;

type NavGroup = {
  id: string;
  label: string;
  accent: 'cyan' | 'rose' | 'amber' | 'violet' | 'green';
  icon: NavIcon;
  items: Array<{ id: ViewId; label: string; icon: NavIcon }>;
};

const navGroups: NavGroup[] = [
  {
    id: 'workbench',
    label: '工作台',
    accent: 'cyan',
    icon: LayoutDashboard,
    items: [
      { id: 'overview', label: '总览', icon: LayoutDashboard },
      { id: 'operations', label: '任务与审计', icon: ListChecks },
      { id: 'points', label: '积分与活动', icon: Coins },
    ],
  },
  {
    id: 'runtime',
    label: '运行中心',
    accent: 'green',
    icon: Activity,
    items: [{ id: 'runtime', label: '运行实例', icon: Activity }],
  },
  {
    id: 'agents',
    label: '智能体',
    accent: 'rose',
    icon: Bot,
    items: [
      { id: 'roles', label: '角色库', icon: Bot },
      { id: 'memories', label: '记忆与关系', icon: Database },
      { id: 'worldbook', label: '世界书', icon: LibraryBig },
      { id: 'samples', label: '人格内核', icon: Sparkles },
    ],
  },
  {
    id: 'capabilities',
    label: '能力中心',
    accent: 'amber',
    icon: Sparkles,
    items: [
      { id: 'knowledge', label: '知识与学习', icon: LibraryBig },
      { id: 'skills', label: '技能', icon: Sparkles },
      { id: 'plugins', label: '插件中心', icon: Blocks },
      { id: 'tools', label: '工具与 MCP', icon: Wrench },
    ],
  },
  {
    id: 'infrastructure',
    label: '基础设施',
    accent: 'violet',
    icon: Cpu,
    items: [
      { id: 'integrations', label: '平台与接入', icon: Cable },
      { id: 'models', label: '模型与供应商', icon: Cpu },
      { id: 'routing', label: '模型路由', icon: Route },
      { id: 'devices', label: '设备与桌面', icon: MonitorCog },
    ],
  },
  {
    id: 'governance',
    label: '治理',
    accent: 'green',
    icon: ShieldCheck,
    items: [
      { id: 'security', label: '安全边界', icon: ShieldCheck },
      { id: 'system', label: '系统设置', icon: Settings },
    ],
  },
];

export function AppShell({
  activeView,
  activeInstance,
  instances,
  activePersona,
  connected,
  onNavigate,
  onInstanceChange,
  onRefresh,
  onLogout,
  children,
}: {
  activeView: ViewId;
  activeInstance?: AgentInstance;
  instances: AgentInstance[];
  activePersona?: Persona;
  connected: boolean;
  onNavigate: (view: ViewId) => void;
  onInstanceChange: (id: string) => void;
  onRefresh: () => void;
  onLogout: () => void;
  children: ReactNode;
}) {
  const activeGroup = navGroups.find((group) => group.items.some((item) => item.id === activeView));

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="topbar-inner">
          <div className="brand">
            <span className="brand-mark">二</span>
            <span className="brand-copy">
              <strong>二呆智能体</strong>
              <small>CONTROL PLANE / ERDAI CORE</small>
            </span>
          </div>
          <div className="instance-switch">
            <span className="instance-label">当前实例</span>
            <StatusDot tone={activeInstance?.enabled === false ? 'idle' : activeInstance ? 'ok' : 'idle'} />
            {instances.length ? (
              <select
                className="instance-select"
                aria-label="当前运行实例"
                value={activeInstance?.id || ''}
                onChange={(event) => onInstanceChange(event.target.value)}
              >
                {instances.map((instance) => <option value={instance.id} key={instance.id}>{instance.displayName || instance.id}</option>)}
              </select>
            ) : <strong>未登记实例</strong>}
            <span className="instance-persona">{activePersona?.name || activePersona?.id || '未绑定角色'}</span>
            <ChevronDown size={14} aria-hidden="true" />
          </div>
          <div className="top-actions">
            <ThemeSwitcher />
            <span className={`connection-state ${connected ? 'is-online' : 'is-offline'}`}>
              <StatusDot tone={connected ? 'ok' : 'bad'} />
              {connected ? 'Core 在线' : 'Core 连接异常'}
            </span>
            <Button variant="ghost" icon={<RefreshCw size={15} />} onClick={onRefresh} aria-label="刷新数据" />
            <Button variant="ghost" icon={<LogOut size={15} />} onClick={onLogout} aria-label="退出登录" />
          </div>
        </div>
      </header>

      <div className="shell-layout">
        <aside className="sidebar">
          <div className="sidebar-heading">
            <span>{activeGroup?.label || '工作台'}</span>
            <small>WEBUI / 01</small>
          </div>
          <nav className="sidebar-nav" aria-label="控制台导航">
            {navGroups.map((group) => {
              const GroupIcon = group.icon;
              return (
                <div className={`nav-group nav-group-${group.accent} ${activeGroup?.id === group.id ? 'is-current' : ''}`} key={group.id}>
                  <div className="nav-group-title">
                    <GroupIcon size={13} />
                    <span>{group.label}</span>
                  </div>
                  {group.items.map((item) => {
                    const Icon = item.icon;
                    return (
                      <button
                        className={`nav-item ${activeView === item.id ? 'is-active' : ''}`}
                        key={item.id}
                        type="button"
                        onClick={() => onNavigate(item.id)}
                      >
                        <Icon size={15} />
                        <span>{item.label}</span>
                      </button>
                    );
                  })}
                </div>
              );
            })}
          </nav>
          <div className="sidebar-footer">
            <StatusDot tone={connected ? 'ok' : 'bad'} />
            <span>配置源由 Core 管理</span>
          </div>
        </aside>

        <main className="workspace">
          <div className="workspace-context">
            <span>CONTROL PLANE</span>
            <i />
            <strong>{activeGroup?.label || '工作台'}</strong>
            <i />
            <span>{navGroups.flatMap((group) => group.items).find((item) => item.id === activeView)?.label || '总览'}</span>
          </div>
          {children}
        </main>
      </div>
    </div>
  );
}
