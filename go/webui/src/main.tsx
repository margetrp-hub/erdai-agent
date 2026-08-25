import { useCallback, useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { KeyRound, LoaderCircle, LockKeyhole, TriangleAlert, UserRound } from 'lucide-react';
import { AppShell, type ViewId } from './components/AppShell';
import { Button } from './components/ui';
import { ThemeSwitcher } from './components/ThemeSwitcher';
import { ModulePage } from './pages/ModulePage';
import { OverviewPage } from './pages/OverviewPage';
import { ApiError, getSession, loadDashboard, login, logout, type DashboardData } from './lib/api';
import { initializeUiTheme } from './theme';
import './styles.css';

initializeUiTheme();

function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [data, setData] = useState<DashboardData | null>(null);
  const [activeView, setActiveView] = useState<ViewId>('overview');
  const [activeInstanceId, setActiveInstanceId] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refreshNonce, setRefreshNonce] = useState(0);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setData(await loadDashboard());
      setAuthenticated(true);
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '加载 Core 数据失败';
      if (cause instanceof ApiError && cause.authRequired) {
        setAuthenticated(false);
      } else {
        setError(message);
      }
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    getSession()
      .then((isAuthenticated) => {
        setAuthenticated(isAuthenticated);
        if (isAuthenticated) {
          return refresh();
        }
        setLoading(false);
        return undefined;
      })
      .catch((cause) => {
        setError(cause instanceof Error ? cause.message : '无法连接 Core');
        setAuthenticated(false);
        setLoading(false);
      });
  }, [refresh]);

  const activePersona = useMemo(
    () => {
      const instance = data?.agentInstances.items?.find((item) => item.id === activeInstanceId);
      return data?.personas.items?.find((persona) => persona.id === (instance?.personaId || data.config.activePersonaId));
    },
    [activeInstanceId, data],
  );
  const activeInstance = useMemo(
    () => data?.agentInstances.items?.find((item) => item.id === activeInstanceId),
    [activeInstanceId, data],
  );
  const instances = data?.agentInstances.items || [];

  useEffect(() => {
    if (!instances.length) {
      setActiveInstanceId('');
      return;
    }
    setActiveInstanceId((current) => instances.some((item) => item.id === current) ? current : instances[0].id);
  }, [data]);
  const refreshAll = useCallback(async () => {
    setRefreshNonce((value) => value + 1);
    await refresh();
  }, [refresh]);

  if (authenticated === false) {
    return <LoginScreen onSuccess={() => refresh()} />;
  }

  if (authenticated === null || loading || !data) {
    return <LoadingScreen />;
  }

  return (
    <AppShell
      activeView={activeView}
      activeInstance={activeInstance}
      instances={instances}
      activePersona={activePersona}
      connected={!error}
      onNavigate={setActiveView}
      onInstanceChange={setActiveInstanceId}
      onRefresh={refreshAll}
      onLogout={async () => {
        await logout().catch(() => undefined);
        setAuthenticated(false);
        setData(null);
      }}
    >
      {activeView === 'overview' ? (
        <OverviewPage data={data} activeInstanceId={activeInstanceId} onNavigate={setActiveView} />
      ) : (
        <ModulePage
          key={activeView}
          view={activeView}
          activeInstanceId={activeInstanceId}
          activePersonaId={activePersona?.id || data.config.activePersonaId}
          refreshKey={refreshNonce}
          onNavigate={setActiveView}
        />
      )}
      {error ? <div className="toast-error"><TriangleAlert size={16} />{error}</div> : null}
    </AppShell>
  );
}

function LoginScreen({ onSuccess }: { onSuccess: () => void }) {
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  return (
    <main className="auth-layout">
      <ThemeSwitcher placement="auth" />
      <div className="auth-grid" />
      <section className="auth-panel">
        <div className="auth-mark">二</div>
        <span className="hero-kicker">ADMIN SESSION / ERDAI CORE</span>
        <h1>进入控制面</h1>
        <p>同源管理员会话，敏感配置仍由 Core 保管。</p>
        <form
          onSubmit={async (event) => {
            event.preventDefault();
            setSubmitting(true);
            setError('');
            try {
              if (!token.trim() && (!username.trim() || !password)) {
                throw new Error('请输入管理员账号和密码，或输入兼容服务令牌');
              }
              await login(token.trim() ? { token } : { username, password });
              onSuccess();
            } catch (cause) {
              setError(cause instanceof Error ? cause.message : '登录失败');
            } finally {
              setSubmitting(false);
            }
          }}
        >
          <label className="input-field">
            <span>管理员账号</span>
            <span className="input-wrap">
              <UserRound size={15} />
              <input
                type="text"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                autoComplete="username"
                placeholder="ERDAI_ADMIN_USERNAME"
              />
            </span>
          </label>
          <label className="input-field">
            <span>管理员密码</span>
            <span className="input-wrap">
              <KeyRound size={15} />
              <input
                type="password"
                value={password}
                onChange={(event) => setPassword(event.target.value)}
                autoComplete="current-password"
                placeholder="管理员密码"
              />
            </span>
          </label>
          <label className="input-field">
            <span>兼容服务令牌</span>
            <span className="input-wrap">
              <LockKeyhole size={15} />
              <input
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                autoComplete="off"
                placeholder="ERDAI_ADMIN_TOKEN（可选）"
              />
            </span>
          </label>
          {error ? <p className="form-error">{error}</p> : null}
          <Button variant="primary" type="submit" disabled={submitting} icon={submitting ? <LoaderCircle className="spin" size={16} /> : <LockKeyhole size={16} />}>
            {submitting ? '正在验证' : '登录控制面'}
          </Button>
        </form>
        <small className="auth-footnote">ERDAI CORE · SAME-ORIGIN ADMIN SESSION · TOKEN FALLBACK</small>
      </section>
    </main>
  );
}

function LoadingScreen() {
  return (
    <main className="loading-screen">
      <div className="loading-beacon" />
      <LoaderCircle className="spin" size={22} />
      <strong>正在连接二呆智能体 Core</strong>
      <span>读取运行事实和角色配置</span>
    </main>
  );
}

createRoot(document.getElementById('root')!).render(<App />);

export default App;
