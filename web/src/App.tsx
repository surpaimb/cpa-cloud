import { useEffect, useState } from 'react'
import { api, ApiError, type Session } from './api'
import { messageFor } from './hooks'
import { Button, Field, FormError, Icon, submitHandler } from './ui'
import { EmployeesPage } from './pages/EmployeesPage'
import { UpstreamsPage } from './pages/UpstreamsPage'
import { ModelsPage } from './pages/ModelsPage'
import { StatusPage } from './pages/StatusPage'
import { UsagePage } from './pages/UsagePage'

type Page = 'employees' | 'upstreams' | 'models' | 'usage' | 'status'

const navigation: { id: Page; label: string; icon: 'people' | 'link' | 'route' | 'status' }[] = [
  { id: 'employees', label: '员工与 Key', icon: 'people' },
  { id: 'upstreams', label: '上游连接', icon: 'link' },
  { id: 'models', label: '模型路由', icon: 'route' },
  { id: 'usage', label: '用量与成本', icon: 'status' },
  { id: 'status', label: '系统状态', icon: 'status' },
]

export function App() {
  const [checking, setChecking] = useState(true)
  const [session, setSession] = useState<Session | null>(null)

  useEffect(() => {
    api.session()
      .then(setSession)
      .catch((error) => { if (!(error instanceof ApiError && error.status === 401)) console.warn('Session check failed', error) })
      .finally(() => setChecking(false))
  }, [])

  if (checking) return <div className="boot-screen"><div className="brand-mark">C</div><span>正在连接 CPA Cloud…</span></div>
  if (!session) return <LoginScreen onLogin={(username, csrf_token) => setSession({ username, csrf_token })} />
  return <AdminShell session={session} onLogout={() => setSession(null)} />
}

function LoginScreen({ onLogin }: { onLogin: (username: string, csrf: string) => void }) {
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  return <main className="login-screen">
    <section className="login-intro">
      <div className="brand-lockup"><div className="brand-mark">C</div><div><strong>CPA Cloud</strong><span>AI 访问管理平台</span></div></div>
      <div className="login-copy"><h1>把 AI 访问管理，留在你的网络里。</h1><p>统一管理员工 Key、模型权限和上游连接。适用于云服务器、公司内网与个人电脑。</p></div>
      <small>管理员后台 · 单租户部署</small>
    </section>
    <section className="login-panel">
      <form className="login-form" onSubmit={submitHandler(async (form) => {
        setSubmitting(true); setError(null)
        const username = String(form.get('username') ?? '')
        try {
          const result = await api.login(username, String(form.get('password') ?? ''))
          onLogin(username, result.csrf_token)
        } catch (caught) { setError(messageFor(caught)) } finally { setSubmitting(false) }
      })}>
        <div><h1>管理员登录</h1><p>使用初始化服务时设置的管理员凭据。</p></div>
        <Field label="用户名"><input name="username" autoComplete="username" defaultValue="admin" required /></Field>
        <Field label="密码"><input name="password" type="password" autoComplete="current-password" required autoFocus /></Field>
        <FormError error={error} />
        <Button type="submit" disabled={submitting}>{submitting ? '正在登录…' : '登录'}</Button>
        <p className="security-note">凭据仅发送给当前 CPA Cloud 服务。</p>
      </form>
    </section>
  </main>
}

function AdminShell({ session, onLogout }: { session: Session; onLogout: () => void }) {
  const [page, setPage] = useState<Page>('employees')
  const [mobileNav, setMobileNav] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const content = {
    employees: <EmployeesPage csrf={session.csrf_token} />,
    upstreams: <UpstreamsPage csrf={session.csrf_token} />,
    models: <ModelsPage csrf={session.csrf_token} />,
    usage: <UsagePage csrf={session.csrf_token} />,
    status: <StatusPage />,
  }[page]

  return <div className="app-shell">
    <aside className={`sidebar ${mobileNav ? 'sidebar--open' : ''}`}>
      <div className="brand-lockup sidebar__brand"><div className="brand-mark">C</div><div><strong>CPA Cloud</strong><span>AI 访问管理平台</span></div></div>
      <nav aria-label="主导航">
        {navigation.map((item) => <button key={item.id} className={page === item.id ? 'active' : ''} onClick={() => { setPage(item.id); setMobileNav(false) }}><Icon name={item.icon} />{item.label}</button>)}
      </nav>
      <div className="sidebar__account">
        <div className="account-line"><span className="avatar">{session.username.slice(0, 1).toUpperCase()}</span><span><strong>{session.username}</strong><small>系统管理员</small></span></div>
        <button disabled={loggingOut} onClick={async () => {
          setLoggingOut(true)
          try { await api.logout(session.csrf_token) } catch { /* Local sign-out remains safe if the server session expired. */ }
          onLogout()
        }}><Icon name="logout" />{loggingOut ? '正在退出…' : '退出登录'}</button>
      </div>
    </aside>
    {mobileNav ? <button className="nav-scrim" aria-label="关闭导航" onClick={() => setMobileNav(false)} /> : null}
    <main className="workspace">
      <button className="mobile-menu" aria-label="打开导航" onClick={() => setMobileNav(true)}><Icon name="menu" /></button>
      {content}
    </main>
  </div>
}
