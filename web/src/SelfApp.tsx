// Independently authored for docs/employee-self-service-foundation-contract.md
// docs/employee-self-password-change-contract.md, and
// docs/employee-self-key-inventory-contract.md.
import { useEffect, useRef, useState } from 'react'
import { ApiError } from './api'
import { Button, Field, FormError } from './ui'

type Profile = { id: string; name: string; department: string; status: 'active' | 'disabled' }
type SelfSession = { csrf_token: string; profile: Profile }
type SelfKey = { id: string; name: string; created_at: string; expires_at: string | null; revoked_at: string | null; status: 'active' | 'expired' | 'revoked' }
type SelfKeyPage = { items: SelfKey[]; next_cursor: string | null }

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

function selfKeyDate(value: string) {
  return new Date(value).toLocaleString('zh-CN')
}

function SelfKeyInventory() {
  const [items, setItems] = useState<SelfKey[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const pendingPage = useRef<AbortController | null>(null)

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    void selfRequest<SelfKeyPage>('/keys', { signal: controller.signal }).then((page) => {
      if (!active) return
      setItems(page.items)
      setCursor(page.next_cursor)
      setError(false)
    }).catch(() => {
      if (!active) return
      setItems([])
      setCursor(null)
      setError(true)
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false; controller.abort(); pendingPage.current?.abort() }
  }, [])

  async function loadMore() {
    if (!cursor || loading || pendingPage.current) return
    const controller = new AbortController()
    pendingPage.current = controller
    setLoading(true)
    setError(false)
    try {
      const page = await selfRequest<SelfKeyPage>(`/keys?cursor=${encodeURIComponent(cursor)}`, { signal: controller.signal })
      if (controller.signal.aborted) return
      setItems((prior) => [...prior, ...page.items])
      setCursor(page.next_cursor)
    } catch {
      if (controller.signal.aborted) return
      // A failed page cannot leave a potentially stale partial inventory visible.
      setItems([])
      setCursor(null)
      setError(true)
    } finally {
      if (!controller.signal.aborted) setLoading(false)
      if (pendingPage.current === controller) pendingPage.current = null
    }
  }

  return <section className="self-keys" aria-labelledby="self-keys-title">
    <h2 id="self-keys-title">我的 API Key</h2>
    <p>仅显示你已有 Key 的名称、时间和状态；Key 明文只在管理员创建时显示一次。</p>
    {loading && items.length === 0 ? <p role="status">正在读取 Key 列表…</p> : null}
    {error ? <p role="alert">Key 列表暂时无法读取，请稍后重新登录或刷新页面。</p> : null}
    {!error && !loading && items.length === 0 ? <p>暂无 API Key。</p> : null}
    {items.length > 0 ? <ul className="self-key-list">{items.map((item) => <li key={item.id}>
      <div className="self-key-top"><strong>{item.name}</strong><span className={`self-key-status self-key-status--${item.status}`}>{item.status === 'active' ? '有效' : item.status === 'expired' ? '已到期' : '已撤销'}</span></div>
      <dl><div><dt>Key ID</dt><dd>{item.id}</dd></div><div><dt>创建时间</dt><dd><time dateTime={item.created_at}>{selfKeyDate(item.created_at)}</time></dd></div><div><dt>到期时间</dt><dd>{item.expires_at ? <time dateTime={item.expires_at}>{selfKeyDate(item.expires_at)}</time> : '永不过期'}</dd></div>{item.revoked_at ? <div><dt>撤销时间</dt><dd><time dateTime={item.revoked_at}>{selfKeyDate(item.revoked_at)}</time></dd></div> : null}</dl>
    </li>)}</ul> : null}
    {cursor && !error ? <Button variant="secondary" disabled={loading} onClick={loadMore}>{loading ? '正在加载…' : '加载更多'}</Button> : null}
  </section>
}

export function SelfApp() {
  const [checking, setChecking] = useState(true)
  const [session, setSession] = useState<SelfSession | null>(null)
  const [mode, setMode] = useState<'login' | 'enroll'>('login')
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [changingPassword, setChangingPassword] = useState(false)
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
        <header><span className="self-kicker">PERSONAL PROFILE</span><h1>你好，{session.profile.name}</h1><p>你可以查看个人资料和已有 API Key 的基本信息；创建、策略与撤销仍由管理员管理。</p></header>
        <dl className="self-profile"><div><dt>员工 ID</dt><dd>{session.profile.id}</dd></div><div><dt>姓名</dt><dd>{session.profile.name}</dd></div><div><dt>部门</dt><dd>{session.profile.department || '未设置'}</dd></div><div><dt>状态</dt><dd>{session.profile.status === 'active' ? '启用' : '已停用'}</dd></div></dl>
        <SelfKeyInventory key={`${session.profile.id}:${session.csrf_token}`} />
        {changingPassword ? <form className="self-form self-password-form" onSubmit={async (event) => {
          event.preventDefault()
          const form = event.currentTarget
          const values = new FormData(form)
          const current_password = String(values.get('current_password') ?? '')
          const new_password = String(values.get('new_password') ?? '')
          const size = new TextEncoder().encode(new_password).length
          if (size < 12 || size > 72) {
            setError('新密码须为 12–72 个 UTF-8 字节。')
            form.reset()
            return
          }
          setBusy(true)
          setError(null)
          try {
            await selfRequest<void>('/password', { method: 'POST', body: JSON.stringify({ current_password, new_password }) }, session.csrf_token)
            setSession(null)
            setChangingPassword(false)
            setNotice('密码已更新，所有设备均已退出。请使用新密码重新登录。')
          } catch (caught) {
            if (caught instanceof ApiError && caught.status === 503) {
              setSession(null)
              setChangingPassword(false)
              setNotice('结果未确认。请重新登录，先尝试新密码，再尝试旧密码；若均失败，请联系管理员。')
            } else {
              setError(caught instanceof ApiError && caught.status === 429 ? '尝试次数过多，请稍后再试。' : '当前密码无效或请求未通过，请检查后重试。')
            }
          } finally {
            form.reset()
            setBusy(false)
          }
        }}>
          <Field label="当前密码"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
          <Field label="新密码（12–72 字节）"><input name="new_password" type="password" autoComplete="new-password" required minLength={12} /></Field>
          <FormError error={error} />
          <div className="self-actions"><Button type="submit" disabled={busy}>{busy ? '正在更新…' : '确认修改密码'}</Button><Button type="button" variant="secondary" disabled={busy} onClick={() => { setChangingPassword(false); setError(null) }}>取消</Button></div>
        </form> : <FormError error={error} />}
        <div className="self-actions">
          {!changingPassword ? <Button variant="secondary" disabled={busy} onClick={() => { setChangingPassword(true); setError(null) }}>修改密码</Button> : null}
          <Button variant="secondary" disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await selfRequest<void>('/sessions', { method: 'DELETE' }, session.csrf_token); setSession(null); setChangingPassword(false) } catch { setError('退出失败，请稍后重试。') } finally { setBusy(false) } }}>{busy ? '正在退出…' : '退出登录'}</Button>
        </div>
      </> : <>
        <header><span className="self-kicker">EMPLOYEE ACCESS</span><h1>{mode === 'login' ? '员工登录' : '首次开通'}</h1><p>{mode === 'login' ? '使用员工 ID 和自己设置的密码。' : '输入管理员提供的一次性开通码，设置你的密码。'}</p></header>
        {notice ? <p className="self-notice" role="status">{notice}</p> : null}
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
            setNotice(null)
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
