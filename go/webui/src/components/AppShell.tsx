import {
  Activity,
  Bot,
  Blocks,
  Cable,
  ChevronDown,
  Cpu,
  Coins,
  Database,
  Image,
  LayoutDashboard,
  LibraryBig,
  ListChecks,
  LogOut,
  Menu,
  MessageCircle,
  MonitorCog,
  RefreshCw,
  Route,
  Search,
  Settings,
  ShieldCheck,
  Sparkles,
  Wrench,
} from 'lucide-react';
import { useState, type ReactNode } from 'react';
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
  | 'capabilities'
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
export type Navigate = (view: ViewId, section?: string) => void;

type NavGroup = {
  id: string;
  label: string;
  accent: 'cyan' | 'rose' | 'amber' | 'violet' | 'green';
  icon: NavIcon;
  items: Array<{ id: ViewId; section?: string; label: string; icon: NavIcon; keywords?: string }>;
};

const navGroups: NavGroup[] = [
  {
    id: 'workbench',
    label: '工作台',
    accent: 'cyan',
    icon: LayoutDashboard,
    items: [
      { id: 'overview', label: '首页', icon: LayoutDashboard, keywords: '总览 状态 健康' },
      { id: 'operations', section: 'runs', label: '任务与记录', icon: ListChecks, keywords: '审计 日志 错误 失败' },
    ],
  },
  {
    id: 'chat', label: '聊天与群聊', accent: 'cyan', icon: MessageCircle,
    items: [{ id: 'system', section: 'runtime', label: '聊天与群聊设置', icon: MessageCircle, keywords: '系统 主动 回复 插话 互动 概率' }],
  },
  {
    id: 'media', label: '照片与视频', accent: 'cyan', icon: Image,
    items: [{ id: 'system', section: 'quotas', label: '图片与视频额度', icon: Image, keywords: '媒体 白名单 不限额 次数' }],
  },
  {
    id: 'agents',
    label: '角色与记忆',
    accent: 'cyan',
    icon: Bot,
    items: [
      { id: 'roles', section: 'cards', label: '角色卡', icon: Bot, keywords: '性格 人设 提示词' },
      { id: 'roles', section: 'visual', label: '外观与参考图片', icon: Image, keywords: '形象 自拍 外观库' },
      { id: 'memories', label: '记忆与关系', icon: Database },
      { id: 'worldbook', label: '背景设定', icon: LibraryBig, keywords: '世界书' },
      { id: 'samples', label: '说话风格', icon: Sparkles, keywords: '人格 内核 样本' },
    ],
  },
  {
    id: 'connections', label: '模型与连接', accent: 'cyan', icon: Cable,
    items: [
      { id: 'models', section: 'endpoints', label: '使用的 AI 模型', icon: Cpu, keywords: '文本 生图 视频' },
      { id: 'models', section: 'connections', label: '模型服务商', icon: Cable, keywords: '供应商 密钥 接口' },
      { id: 'integrations', label: '平台连接', icon: Cable, keywords: 'QQ 微信 接入 连接器' },
      { id: 'routing', label: '模型分工', icon: Route, keywords: '路由' },
    ],
  },
  {
    id: 'capabilities', label: '能力中心', accent: 'cyan', icon: Sparkles,
    items: [
      { id: 'capabilities', label: '全部能力', icon: Sparkles, keywords: '功能 插件中心 启用' },
      { id: 'knowledge', label: '知识与学习', icon: LibraryBig, keywords: '文档 审核 检索' },
      { id: 'skills', label: '技能', icon: Sparkles },
      { id: 'plugins', label: '扩展插件', icon: Blocks },
      { id: 'tools', label: '外部工具', icon: Wrench, keywords: 'MCP' },
    ],
  },
  {
    id: 'points', label: '积分与活动', accent: 'cyan', icon: Coins,
    items: [{ id: 'points', label: '积分与活动', icon: Coins, keywords: '签到 兑换 抽奖 邀请 订单' }],
  },
  {
    id: 'maintenance', label: '系统与排障', accent: 'cyan', icon: Settings,
    items: [
      { id: 'runtime', label: '机器人实例', icon: Activity, keywords: '运行 实例 策略 高级' },
      { id: 'devices', label: '设备与桌面', icon: MonitorCog },
      { id: 'security', label: '权限与安全', icon: ShieldCheck, keywords: '治理 边界' },
      { id: 'system', section: 'layers', label: '配置来源', icon: Settings, keywords: '高级 分层 生效' },
    ],
  },
];

export function AppShell({
  activeView,
  activeSection,
  activeInstance,
  instances,
  activePersona,
  connected,
  refreshing = false,
  loggingOut = false,
  onNavigate,
  onInstanceChange,
  onRefresh,
  onLogout,
  children,
}: {
  activeView: ViewId;
  activeSection?: string;
  activeInstance?: AgentInstance;
  instances: AgentInstance[];
  activePersona?: Persona;
  connected: boolean;
  refreshing?: boolean;
  loggingOut?: boolean;
  onNavigate: Navigate;
  onInstanceChange: (id: string) => void;
  onRefresh: () => void;
  onLogout: () => void;
  children: ReactNode;
}) {
  const [search, setSearch] = useState('');
  const [mobileNavOpen, setMobileNavOpen] = useState(false);
  const sectionDefaults: Partial<Record<ViewId, string>> = { system: 'runtime', roles: 'cards', models: 'connections' };
  const matchesActive = (item: NavGroup['items'][number]) => item.id === activeView && (!(activeView in sectionDefaults) || item.section === (activeSection || sectionDefaults[activeView]));
  const activeGroup = navGroups.find((group) => group.items.some(matchesActive)) || navGroups.find((group) => group.items.some((item) => item.id === activeView));
  const activeItem = activeGroup?.items.find(matchesActive) || activeGroup?.items.find((item) => item.id === activeView);
  const query = search.trim().toLowerCase();
  const filteredGroups = navGroups.map((group) => ({
    ...group,
    items: group.items.filter((item) => !query || `${item.label} ${item.keywords || ''} ${group.label}`.toLowerCase().includes(query)),
  })).filter((group) => group.items.length > 0);

  return (
    <div className="app-shell friendly-shell">
      <header className="topbar">
        <div className="topbar-inner">
          <div className="brand">
            <span className="brand-mark">二</span>
            <span className="brand-copy">
              <strong>二呆智能体</strong>
              <small>管理工作台</small>
            </span>
          </div>
          <div className="instance-switch">
            <span className="instance-label">当前机器人</span>
            {instances.length ? (
              <select
                className="instance-select"
                aria-label="选择机器人"
                value={activeInstance?.id || ''}
                onChange={(event) => onInstanceChange(event.target.value)}
              >
                {instances.map((instance) => <option value={instance.id} key={instance.id}>{instance.displayName || instance.id}</option>)}
              </select>
            ) : <strong>尚未添加机器人</strong>}
            <span className="instance-persona">{activePersona?.name || activePersona?.id || '未选择角色'}</span>
            <ChevronDown size={14} aria-hidden="true" />
          </div>
          <div className="top-actions">
            <ThemeSwitcher />
            <span className={`connection-state ${connected ? 'is-online' : 'is-offline'}`}>
              <StatusDot tone={connected ? 'ok' : 'bad'} />
              {connected ? '后台已连接' : '连接异常'}
            </span>
            <Button variant="ghost" icon={<RefreshCw size={15} className={refreshing ? 'spin' : ''} />} onClick={onRefresh} disabled={refreshing || loggingOut} aria-label="刷新数据" title="刷新数据" />
            <Button variant="ghost" icon={<LogOut size={15} />} onClick={onLogout} disabled={loggingOut || refreshing} aria-label="退出登录" title={loggingOut ? '正在退出…' : '退出登录'} />
          </div>
        </div>
      </header>

      <div className="shell-layout">
        <aside className={`sidebar ${mobileNavOpen ? 'is-mobile-open' : ''}`}>
          <div className="sidebar-heading">
            <span className="desktop-nav-title">功能导航</span>
            <button className="mobile-nav-toggle" type="button" onClick={() => setMobileNavOpen((open) => !open)} aria-expanded={mobileNavOpen} aria-controls="admin-navigation">
              <Menu size={17} /><span>{activeItem?.label || '首页'}</span><ChevronDown size={16} />
            </button>
          </div>
          <label className="nav-search">
            <Search size={16} aria-hidden="true" />
            <input type="search" value={search} onChange={(event) => setSearch(event.target.value)} placeholder="找功能，如：额度、QQ" aria-label="查找功能" />
          </label>
          <nav className="sidebar-nav" id="admin-navigation" aria-label="管理后台导航">
            {filteredGroups.map((group) => {
              const GroupIcon = group.icon;
              const expanded = activeGroup?.id === group.id || Boolean(query);
              const visibleItems = expanded && (group.items.length > 1 || query) ? group.items : [];
              const firstItem = group.items[0];
              return (
                <div className={`nav-group nav-group-${group.accent} ${activeGroup?.id === group.id ? 'is-current' : ''}`} key={group.id}>
                  <button className="nav-group-title nav-primary" type="button" onClick={() => { onNavigate(firstItem.id, firstItem.section); setMobileNavOpen(group.items.length > 1); setSearch(''); }} aria-current={activeGroup?.id === group.id ? 'location' : undefined}>
                    <GroupIcon size={18} />
                    <span>{group.label}</span>
                    {group.items.length > 1 ? <ChevronDown size={14} className={expanded ? 'is-expanded' : ''} /> : null}
                  </button>
                  <div className="nav-group-items">
                  {visibleItems.map((item) => {
                    const Icon = item.icon;
                    return (
                      <button
                        className={`nav-item ${matchesActive(item) ? 'is-active' : ''}`}
                        key={`${item.id}-${item.section || ''}`}
                        type="button"
                        aria-current={matchesActive(item) ? 'page' : undefined}
                        onClick={() => { onNavigate(item.id, item.section); setMobileNavOpen(false); setSearch(''); }}
                      >
                        <Icon size={15} />
                        <span>{item.label}</span>
                      </button>
                    );
                  })}
                  </div>
                </div>
              );
            })}
            {!filteredGroups.length ? <p className="nav-empty">没有找到“{search}”，试试“模型”或“群聊”。</p> : null}
          </nav>
          <div className="sidebar-footer">
            <StatusDot tone={connected ? 'ok' : 'bad'} />
            <span>{connected ? '已连接管理服务' : '暂时无法连接，请刷新重试'}</span>
          </div>
        </aside>

        <main className="workspace">
          <div className="workspace-context">
            <span>管理后台</span>
            <i />
            <strong>{activeGroup?.label || '工作台'}</strong>
            <i />
            <span>{activeItem?.label || '首页'}</span>
          </div>
          {children}
        </main>
      </div>
    </div>
  );
}
