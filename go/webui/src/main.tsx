import { useCallback, useEffect, useMemo, useState } from 'react';
import { createRoot } from 'react-dom/client';
import { KeyRound, LoaderCircle, LockKeyhole, RefreshCw, TriangleAlert, UserRound } from 'lucide-react';
import { AppShell, type ViewId } from './components/AppShell';
import { Button } from './components/ui';
import { ThemeSwitcher } from './components/ThemeSwitcher';
import { ModulePage } from './pages/ModulePage';
import { OverviewPage } from './pages/OverviewPage';
import { PointsPage } from './pages/PointsPage';
import { CapabilitiesPage } from './pages/CapabilitiesPage';
import { ApiError, getSession, loadDashboard, login, logout, type DashboardData } from './lib/api';
import { initializeUiTheme } from './theme';
import './styles.css';
import './points.css';
import './shell-friendly.css';

initializeUiTheme();

function App() {
  const [authenticated, setAuthenticated] = useState<boolean | null>(null);
  const [data, setData] = useState<DashboardData | null>(null);
  const [activeView, setActiveView] = useState<ViewId>('overview');
  const [activeSection, setActiveSection] = useState<string | undefined>();
  const [activeInstanceId, setActiveInstanceId] = useState('');
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [logoutError, setLogoutError] = useState('');
  const [loggingOut, setLoggingOut] = useState(false);
  const [refreshNonce, setRefreshNonce] = useState(0);

  const refresh = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      setData(await loadDashboard());
      setAuthenticated(true);
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : '管理数据加载失败';
      if (cause instanceof ApiError && cause.authRequired) {
        setAuthenticated(false);
        setData(null);
      } else {
        setError(message);
      }
    } finally {
      setLoading(false);
    }
  }, []);

  const checkSession = useCallback(() => {
    setLoading(true);
    setError('');
    return getSession()
      .then((isAuthenticated) => {
        setAuthenticated(isAuthenticated);
        if (isAuthenticated) {
          return refresh();
        }
        setLoading(false);
        return undefined;
      })
      .catch((cause) => {
        setError(cause instanceof Error ? cause.message : '暂时无法连接管理服务');
        setAuthenticated(null);
        setLoading(false);
      });
  }, [refresh]);

  useEffect(() => { void checkSession(); }, [checkSession]);

  const navigate = useCallback((view: ViewId, section?: string) => {
    setActiveView(view);
    setActiveSection(section);
  }, []);

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
    return <LoginScreen onSuccess={() => { setAuthenticated(true); void refresh(); }} />;
  }

  if (loading && !data) {
    return <LoadingScreen />;
  }

  if (!data) {
    return <main className="loading-screen connection-recovery"><TriangleAlert size={32} /><strong>暂时无法打开管理后台</strong><p role="alert">{error || '没有读取到管理数据，请重试。'}</p><Button variant="primary" icon={<RefreshCw size={16} />} onClick={() => void checkSession()}>重新连接</Button></main>;
  }

  return (
    <AppShell
      activeView={activeView}
      activeSection={activeSection}
      activeInstance={activeInstance}
      instances={instances}
      activePersona={activePersona}
      connected={!error}
      refreshing={loading}
      loggingOut={loggingOut}
      onNavigate={navigate}
      onInstanceChange={setActiveInstanceId}
      onRefresh={refreshAll}
      onLogout={async () => {
        setLoggingOut(true);
        setLogoutError('');
        try {
          await logout();
          setAuthenticated(false);
          setData(null);
        } catch (cause) {
          if (cause instanceof ApiError && cause.authRequired) {
            setAuthenticated(false);
            setData(null);
          } else {
            setLogoutError(`退出登录未完成，请重试。${cause instanceof Error ? cause.message : ''}`);
          }
        } finally {
          setLoggingOut(false);
        }
      }}
    >
      {activeView === 'overview' ? (
        <OverviewPage data={data} activeInstanceId={activeInstanceId} onNavigate={navigate} />
      ) : activeView === 'points' ? (
        <PointsPage refreshKey={refreshNonce} />
      ) : activeView === 'capabilities' ? (
        <CapabilitiesPage refreshKey={refreshNonce} activeInstanceId={activeInstanceId} onNavigate={navigate} />
      ) : (
        <ModulePage
          key={activeView}
          view={activeView}
          initialTab={activeSection}
          onTabChange={setActiveSection}
          activeInstanceId={activeInstanceId}
          activePersonaId={activePersona?.id || data.config.activePersonaId}
          refreshKey={refreshNonce}
          onNavigate={navigate}
        />
      )}
      {error || logoutError ? <div className="toast-error" role="alert"><TriangleAlert size={16} /><span>{error ? `${error} · 当前显示上次读取的数据` : logoutError}</span>{error ? <Button onClick={refreshAll} disabled={loading}>重试</Button> : <Button onClick={() => setLogoutError('')}>知道了</Button>}</div> : null}
    </AppShell>
  );
}

function LoginScreen({ onSuccess }: { onSuccess: () => void }) {
  const [mode, setMode] = useState<'token' | 'password'>('token');
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  return (
    <main className="auth-layout friendly-auth">
      <ThemeSwitcher placement="auth" />
      <div className="auth-grid" />
      <section className="auth-panel">
        <div className="auth-mark">二</div>
        <span className="hero-kicker">二呆智能体 · 管理后台</span>
        <h1>欢迎回来</h1>
        <p>管理角色、聊天方式和日常使用设置。</p>
        <div className="login-modes" role="group" aria-label="选择登录方式">
          <button type="button" aria-pressed={mode === 'token'} disabled={submitting} onClick={() => { setMode('token'); setError(''); }}>登录密钥</button>
          <button type="button" aria-pressed={mode === 'password'} disabled={submitting} onClick={() => { setMode('password'); setError(''); }}>账号密码</button>
        </div>
        <form
          onSubmit={async (event) => {
            event.preventDefault();
            setSubmitting(true);
            setError('');
            try {
              if (mode === 'token' && !token.trim()) {
                throw new Error('请填写管理员登录密钥');
              }
              if (mode === 'password' && (!username.trim() || !password)) {
                throw new Error('请填写管理员账号和密码');
              }
              await login(mode === 'token' ? { token: token.trim() } : { username: username.trim(), password });
              onSuccess();
            } catch (cause) {
              setError(cause instanceof Error ? cause.message : '登录失败');
            } finally {
              setSubmitting(false);
            }
          }}
        >
          {mode === 'password' ? <><label className="input-field">
            <span>管理员账号</span>
            <span className="input-wrap">
              <UserRound size={15} />
              <input
                type="text"
                value={username}
                onChange={(event) => setUsername(event.target.value)}
                autoComplete="username"
                placeholder="输入管理员账号"
                required
                disabled={submitting}
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
                required
                disabled={submitting}
              />
            </span>
          </label><p className="login-help">仅适用于已设置管理员账号密码的服务。没有账号时，请使用登录密钥。</p></> : <label className="input-field">
            <span>管理员登录密钥</span>
            <span className="input-wrap">
              <LockKeyhole size={15} />
              <input
                type="password"
                value={token}
                onChange={(event) => setToken(event.target.value)}
                autoComplete="off"
                placeholder="粘贴你的登录密钥"
                required
                disabled={submitting}
                aria-describedby="login-token-help"
              />
            </span>
            <small className="login-help" id="login-token-help">填写部署时设置的管理员密钥，这种方式无需账号密码。</small>
          </label>}
          {error ? <p className="form-error" role="alert">{error}</p> : null}
          <Button variant="primary" type="submit" disabled={submitting} icon={submitting ? <LoaderCircle className="spin" size={16} /> : <LockKeyhole size={16} />}>
            {submitting ? '正在登录…' : '进入管理后台'}
          </Button>
        </form>
        <small className="auth-footnote"><LockKeyhole size={13} /> 仅管理员可进入，请勿分享登录信息。</small>
      </section>
    </main>
  );
}

function LoadingScreen() {
  return (
    <main className="loading-screen">
      <div className="loading-beacon" />
      <LoaderCircle className="spin" size={22} />
      <strong>正在打开二呆管理后台</strong>
      <span>读取机器人状态和当前设置</span>
    </main>
  );
}

const appRoot = import.meta.hot?.data.appRoot || createRoot(document.getElementById('root')!);
if (import.meta.hot) import.meta.hot.data.appRoot = appRoot;
appRoot.render(<App />);

export default App;
