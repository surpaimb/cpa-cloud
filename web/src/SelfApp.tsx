// Independently authored for docs/employee-self-service-foundation-contract.md
// docs/employee-self-password-change-contract.md, and
// docs/employee-self-key-inventory-contract.md.
// docs/employee-self-request-history-contract.md.
// docs/employee-self-token-summary-contract.md.
// docs/employee-self-key-revocation-contract.md.
// docs/employee-self-signout-others-contract.md.
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { ApiError } from './api'
import { Button, Field, FormError } from './ui'

type Profile = { id: string; name: string; department: string; status: 'active' | 'disabled' }
type SelfSession = { csrf_token: string; profile: Profile }
type SelfKey = { id: string; name: string; created_at: string; expires_at: string | null; revoked_at: string | null; status: 'active' | 'expired' | 'revoked' }
type SelfKeyPage = { items: SelfKey[]; next_cursor: string | null }
type SelfRequest = { id: string; key_id: string; model_id: string; status: 'pending' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'; started_at: string; finished_at: string | null }
type SelfRequestPage = { items: SelfRequest[]; next_cursor: string | null }
type SelfTokenCounts = { known_total: string; unknown_attempts: string }
type SelfTokenSummary = {
  from: string; to: string
  requests: { total: string; pending: string; succeeded: string; failed: string; cancelled: string; interrupted: string }
  attempts: { total: string; pending: string; input_tokens: SelfTokenCounts; output_tokens: SelfTokenCounts; cache_read_tokens: SelfTokenCounts; cache_write_tokens: SelfTokenCounts }
}

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

function SelfKeyInventory({ csrf }: { csrf: string }) {
  const [items, setItems] = useState<SelfKey[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const [confirmID, setConfirmID] = useState<string | null>(null)
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)
  const pendingPage = useRef<AbortController | null>(null)
  const pendingRevoke = useRef<AbortController | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
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
    return () => { active = false; mounted.current = false; controller.abort(); pendingPage.current?.abort(); pendingRevoke.current?.abort() }
  }, [])

  async function revoke(event: FormEvent<HTMLFormElement>, id: string) {
    event.preventDefault()
    if (revokeBusy) return
    const form = event.currentTarget
    let current_password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const size = new TextEncoder().encode(current_password).length
    if (size < 12 || size > 72) {
      current_password = ''
      setRevokeError('当前密码须为 12–72 个 UTF-8 字节。')
      return
    }
    const controller = new AbortController()
    pendingRevoke.current = controller
    setRevokeBusy(true)
    setRevokeError(null)
    try {
      await selfRequest<void>(`/keys/${encodeURIComponent(id)}/revoke`, { method: 'POST', body: JSON.stringify({ current_password }), signal: controller.signal }, csrf)
      if (!mounted.current || controller.signal.aborted) return
      setConfirmID(null)
      try {
        const page = await selfRequest<SelfKeyPage>('/keys', { signal: controller.signal })
        if (!mounted.current || controller.signal.aborted) return
        setItems(page.items)
        setCursor(page.next_cursor)
        setError(false)
      } catch {
        if (!mounted.current || controller.signal.aborted) return
        setItems([])
        setCursor(null)
        setError(true)
      }
    } catch (caught) {
      if (!mounted.current || controller.signal.aborted) return
      setRevokeError(caught instanceof ApiError && caught.status === 429
        ? '尝试次数过多，请稍后再试。'
        : caught instanceof ApiError && caught.status === 503
          ? '撤销结果未确认，请刷新列表后重试或联系管理员。'
          : '撤销未完成，请检查当前密码或稍后重试。')
    } finally {
      current_password = ''
      if (pendingRevoke.current === controller) pendingRevoke.current = null
      if (mounted.current) setRevokeBusy(false)
    }
  }

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
      {item.status !== 'revoked' ? <div className="self-key-revoke">
        {confirmID === item.id ? <form onSubmit={(event) => { void revoke(event, item.id) }}>
          <p>确认撤销“{item.name}”？撤销后此 Key 不能再发起新请求，正在进行的请求不会因此中断。</p>
          <Field label="当前密码（撤销确认）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
          {revokeError ? <p role="alert">{revokeError}</p> : null}
          <div className="self-actions"><Button type="submit" disabled={revokeBusy}>{revokeBusy ? '正在撤销…' : '确认撤销 Key'}</Button><Button type="button" variant="secondary" disabled={revokeBusy} onClick={() => { setConfirmID(null); setRevokeError(null) }}>取消</Button></div>
        </form> : <Button variant="secondary" disabled={revokeBusy} onClick={() => { setConfirmID(item.id); setRevokeError(null) }} aria-label={`撤销 ${item.name}`}>撤销 Key</Button>}
      </div> : null}
    </li>)}</ul> : null}
    {cursor && !error ? <Button variant="secondary" disabled={loading} onClick={loadMore}>{loading ? '正在加载…' : '加载更多'}</Button> : null}
  </section>
}

const requestStatus: Record<SelfRequest['status'], string> = {
  pending: '进行中', succeeded: '已完成', failed: '失败', cancelled: '已取消', interrupted: '已中断',
}

function SelfRequestHistory() {
  const [items, setItems] = useState<SelfRequest[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const pendingPage = useRef<AbortController | null>(null)

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    void selfRequest<SelfRequestPage>('/usage/requests', { signal: controller.signal }).then((page) => {
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
      const page = await selfRequest<SelfRequestPage>(`/usage/requests?cursor=${encodeURIComponent(cursor)}`, { signal: controller.signal })
      if (controller.signal.aborted) return
      setItems((prior) => [...prior, ...page.items])
      setCursor(page.next_cursor)
    } catch {
      if (controller.signal.aborted) return
      setItems([])
      setCursor(null)
      setError(true)
    } finally {
      if (!controller.signal.aborted) setLoading(false)
      if (pendingPage.current === controller) pendingPage.current = null
    }
  }

  return <section className="self-history" aria-labelledby="self-history-title">
    <h2 id="self-history-title">我的请求记录</h2>
    <p>只显示最近 24 小时的本人请求活动，不包含内容、用量或费用。</p>
    {loading && items.length === 0 ? <p role="status">正在读取请求记录…</p> : null}
    {error ? <p role="alert">请求记录暂时无法读取，请稍后重新登录或刷新页面。</p> : null}
    {!error && !loading && items.length === 0 ? <p>最近 24 小时暂无请求记录。</p> : null}
    {items.length > 0 ? <ul className="self-history-list">{items.map((item) => <li key={item.id}>
      <div className="self-history-top"><strong>{item.model_id}</strong><span className={`self-history-status self-history-status--${item.status}`}>{requestStatus[item.status]}</span></div>
      <dl><div><dt>请求 ID</dt><dd>{item.id}</dd></div><div><dt>Key ID</dt><dd>{item.key_id}</dd></div><div><dt>开始时间</dt><dd><time dateTime={item.started_at}>{selfKeyDate(item.started_at)}</time></dd></div><div><dt>结束时间</dt><dd>{item.finished_at ? <time dateTime={item.finished_at}>{selfKeyDate(item.finished_at)}</time> : '尚未结束'}</dd></div></dl>
    </li>)}</ul> : null}
    {cursor && !error ? <Button variant="secondary" disabled={loading} onClick={loadMore}>{loading ? '正在加载…' : '加载更多请求'}</Button> : null}
  </section>
}

function SelfTokenSummaryPanel() {
  const [summary, setSummary] = useState<SelfTokenSummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    void selfRequest<SelfTokenSummary>('/usage/summary', { signal: controller.signal }).then((value) => {
      if (!active) return
      setSummary(value)
      setError(false)
    }).catch(() => {
      if (!active) return
      setSummary(null)
      setError(true)
    }).finally(() => { if (active) setLoading(false) })
    return () => { active = false; controller.abort() }
  }, [])

  const tokenRows = summary ? [
    ['输入', summary.attempts.input_tokens],
    ['输出', summary.attempts.output_tokens],
    ['缓存读取', summary.attempts.cache_read_tokens],
    ['缓存写入', summary.attempts.cache_write_tokens],
  ] as const : []

  return <section className="self-summary" aria-labelledby="self-summary-title">
    <h2 id="self-summary-title">我的 Token 用量</h2>
    <p>按上游尝试统计已知值；存在未知尝试时，已知合计不是完整用量，也不是账单。</p>
    {loading ? <p role="status">正在读取 Token 汇总…</p> : null}
    {error ? <p role="alert">Token 汇总暂时无法读取，请稍后重新登录或刷新页面。</p> : null}
    {summary && !error ? <>
      <p className="self-summary-window">统计时间：<time dateTime={summary.from}>{selfKeyDate(summary.from)}</time> 至 <time dateTime={summary.to}>{selfKeyDate(summary.to)}</time>（不含结束时刻）</p>
      <dl className="self-summary-counts">
        <div><dt>本人请求</dt><dd>{summary.requests.total}</dd></div>
        <div><dt>进行中</dt><dd>{summary.requests.pending}</dd></div>
        <div><dt>已完成</dt><dd>{summary.requests.succeeded}</dd></div>
        <div><dt>失败</dt><dd>{summary.requests.failed}</dd></div>
        <div><dt>已取消</dt><dd>{summary.requests.cancelled}</dd></div>
        <div><dt>已中断</dt><dd>{summary.requests.interrupted}</dd></div>
        <div><dt>上游尝试</dt><dd>{summary.attempts.total}</dd></div>
        <div><dt>待结束尝试</dt><dd>{summary.attempts.pending}</dd></div>
      </dl>
      <ul className="self-summary-tokens">{tokenRows.map(([label, counts]) => <li key={label}>
        <strong>{label}</strong>
        <dl><div><dt>已知 Token（上游尝试）</dt><dd>{counts.known_total}</dd></div><div><dt>未知尝试</dt><dd>{counts.unknown_attempts}</dd></div></dl>
      </li>)}</ul>
    </> : null}
  </section>
}

function SelfSignOutOthers({ csrf }: { csrf: string }) {
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [message, setMessage] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const pending = useRef<AbortController | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; pending.current?.abort() }
  }, [])

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (busy) return
    const form = event.currentTarget
    let current_password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const size = new TextEncoder().encode(current_password).length
    if (size < 12 || size > 72) {
      current_password = ''
      setError('当前密码须为 12–72 个 UTF-8 字节。')
      return
    }
    const controller = new AbortController()
    pending.current = controller
    setBusy(true)
    setError(null)
    setMessage(null)
    try {
      await selfRequest<void>('/sessions/revoke-others', { method: 'POST', body: JSON.stringify({ current_password }), signal: controller.signal }, csrf)
      if (!mounted.current || controller.signal.aborted) return
      setConfirming(false)
      setMessage('其他设备已退出；当前设备仍保持登录。')
    } catch (caught) {
      if (!mounted.current || controller.signal.aborted) return
      setError(caught instanceof ApiError && caught.status === 429
        ? '尝试次数过多，请稍后再试。'
        : caught instanceof ApiError && caught.status === 503
          ? '结果未确认，请稍后重试或联系管理员。'
          : '操作未完成，请检查当前密码或稍后重试。')
    } finally {
      current_password = ''
      if (pending.current === controller) pending.current = null
      if (mounted.current) setBusy(false)
    }
  }

  return <section className="self-sessions" aria-labelledby="self-sessions-title">
    <h2 id="self-sessions-title">其他设备登录</h2>
    <p>如果曾在其他设备登录，可以用当前密码使那些设备的员工自助会话失效。当前设备会保持登录。</p>
    {message ? <p role="status">{message}</p> : null}
    {confirming ? <form className="self-form" onSubmit={(event) => { void submit(event) }}>
      <p>确认退出其他设备？这不会影响员工 API Key 或管理员会话。</p>
      <Field label="当前密码（退出其他设备）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      {error ? <p role="alert">{error}</p> : null}
      <div className="self-actions"><Button type="submit" disabled={busy}>{busy ? '正在处理…' : '确认退出其他设备'}</Button><Button type="button" variant="secondary" disabled={busy} onClick={() => { setConfirming(false); setError(null) }}>取消</Button></div>
    </form> : <Button variant="secondary" disabled={busy} onClick={() => { setConfirming(true); setError(null); setMessage(null) }}>退出其他设备</Button>}
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
        <header><span className="self-kicker">PERSONAL PROFILE</span><h1>你好，{session.profile.name}</h1><p>你可以查看个人资料、已有 API Key 的基本信息、本人请求记录和已知 Token 汇总，并用当前密码撤销自己的已有 Key 或退出其他设备；Key 创建与策略仍由管理员管理。</p></header>
        <dl className="self-profile"><div><dt>员工 ID</dt><dd>{session.profile.id}</dd></div><div><dt>姓名</dt><dd>{session.profile.name}</dd></div><div><dt>部门</dt><dd>{session.profile.department || '未设置'}</dd></div><div><dt>状态</dt><dd>{session.profile.status === 'active' ? '启用' : '已停用'}</dd></div></dl>
        <SelfKeyInventory key={`keys:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} />
        <SelfTokenSummaryPanel key={`summary:${session.profile.id}:${session.csrf_token}`} />
        <SelfRequestHistory key={`requests:${session.profile.id}:${session.csrf_token}`} />
        <SelfSignOutOthers key={`sessions:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} />
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
