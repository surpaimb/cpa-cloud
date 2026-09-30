// Independently authored for docs/employee-self-service-foundation-contract.md.
import { useEffect, useState } from 'react'
import { ApiError } from './api'
import { Button, Field, FormError } from './ui'

type Profile = { id: string; name: string; department: string; status: 'active' | 'disabled' }
type SelfSession = { csrf_token: string; profile: Profile }

async function selfRequest<T>(path: string, init: RequestInit = {}, csrf?: string): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('X-Self-Request', '1')
  if (init.body) headers.set('Content-Type', 'application/json')
  if (csrf) headers.set('X-CSRF-Token', csrf)
  const response = await fetch(`/self/api/v1${path}`, { ...init, headers, credentials: 'same-origin' })
  if (!response.ok) {
    let error: { error?: { code?: string; message?: string } } = {}
    try { error = await response.json() as typeof error } catch { /* Keep a non-sensitive fallback. */ }
    throw new ApiError(response.status, error.error?.code ?? 'request_failed', error.error?.message ?? '请求失败，请稍后重试。')
  }
  if (response.status === 204) return undefined as T
  return await response.json() as T
}

export function SelfApp() {
  const [checking, setChecking] = useState(true)
  const [session, setSession] = useState<SelfSession | null>(null)
  const [mode, setMode] = useState<'login' | 'enroll'>('login')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => { document.title = 'CPA Cloud 员工自助入口' }, [])
  useEffect(() => {
    let active = true
    void selfRequest<SelfSession>('/session').then((result) => { if (active) setSession(result) }).catch(() => { /* Signed out or unavailable. */ }).finally(() => { if (active) setChecking(false) })
    return () => { active = false }
  }, [])

  if (checking) return <div className="boot-screen"><div className="brand-mark">C</div><span>正在连接 CPA Cloud…</span></div>
  return <main className="self-screen">
    <div className="self-card">
      <div className="brand-lockup"><div className="brand-mark">C</div><div><strong>CPA Cloud</strong><span>员工自助入口</span></div></div>
      {session ? <>
        <header><span className="self-kicker">PERSONAL PROFILE</span><h1>你好，{session.profile.name}</h1><p>这里只显示你的基本资料。API Key 和使用量仍由管理员管理。</p></header>
        <dl className="self-profile"><div><dt>员工 ID</dt><dd>{session.profile.id}</dd></div><div><dt>姓名</dt><dd>{session.profile.name}</dd></div><div><dt>部门</dt><dd>{session.profile.department || '未设置'}</dd></div><div><dt>状态</dt><dd>{session.profile.status === 'active' ? '启用' : '已停用'}</dd></div></dl>
        <FormError error={error} />
        <Button variant="secondary" disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await selfRequest<void>('/sessions', { method: 'DELETE' }, session.csrf_token); setSession(null) } catch { setError('退出失败，请稍后重试。') } finally { setBusy(false) } }}>{busy ? '正在退出…' : '退出登录'}</Button>
      </> : <>
        <header><span className="self-kicker">EMPLOYEE ACCESS</span><h1>{mode === 'login' ? '员工登录' : '首次开通'}</h1><p>{mode === 'login' ? '使用员工 ID 和自己设置的密码。' : '输入管理员提供的一次性开通码，设置你的密码。'}</p></header>
        <form className="self-form" onSubmit={async (event) => {
          event.preventDefault(); setBusy(true); setError(null)
          const form = new FormData(event.currentTarget)
          const employee_id = String(form.get('employee_id') ?? '')
          const password = String(form.get('password') ?? '')
          try {
            const result = mode === 'login'
              ? await selfRequest<SelfSession>('/sessions', { method: 'POST', body: JSON.stringify({ employee_id, password }) })
              : await selfRequest<SelfSession>('/enroll', { method: 'POST', body: JSON.stringify({ employee_id, enrollment_secret: String(form.get('enrollment_secret') ?? ''), password }) })
            setSession(result)
          } catch (caught) { setError(caught instanceof ApiError && caught.status === 429 ? '尝试次数过多，请稍后再试。' : '凭据或开通码无效，请检查后重试。') }
          finally { setBusy(false) }
        }}>
          <Field label="员工 ID"><input name="employee_id" autoComplete="username" required maxLength={200} /></Field>
          {mode === 'enroll' ? <Field label="一次性开通码"><input name="enrollment_secret" autoComplete="off" required /></Field> : null}
          <Field label={mode === 'enroll' ? '设置密码（12–72 字节）' : '密码'}><input name="password" type="password" autoComplete={mode === 'enroll' ? 'new-password' : 'current-password'} required minLength={12} /></Field>
          <FormError error={error} /><Button type="submit" disabled={busy}>{busy ? '请稍候…' : mode === 'login' ? '登录' : '开通并登录'}</Button>
        </form>
        <button className="link-button self-mode" onClick={() => { setMode(mode === 'login' ? 'enroll' : 'login'); setError(null) }}>{mode === 'login' ? '持有开通码？首次设置密码' : '已有密码？返回登录'}</button>
      </>}
      <p className="self-footnote">仅供公司内部使用。请勿在共用设备上保存密码。</p>
    </div>
  </main>
}
