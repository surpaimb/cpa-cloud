// Independently authored for docs/employee-self-service-foundation-contract.md
// docs/employee-self-password-change-contract.md, and
// docs/employee-self-key-inventory-contract.md.
// docs/employee-self-request-history-contract.md.
// docs/employee-self-token-summary-contract.md.
// docs/employee-self-key-revocation-contract.md.
// docs/employee-self-signout-others-contract.md.
// docs/employee-self-key-issuance-contract.md.
// docs/employee-self-key-token-summary-contract.md.
// docs/employee-self-key-request-history-contract.md.
// docs/employee-self-wallet-balance-contract.md.
// docs/employee-self-wallet-activity-contract.md.
// docs/employee-self-subscription-status-contract.md.
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { ApiError } from './api'
import { Button, Field, FormError } from './ui'

type Profile = { id: string; name: string; department: string; status: 'active' | 'disabled' }
type SelfSession = { csrf_token: string; profile: Profile; features?: { employee_self_wallet_balance?: boolean; employee_self_wallet_activity?: boolean; employee_self_subscription_status?: boolean } }
type SelfKey = { id: string; name: string; created_at: string; expires_at: string | null; revoked_at: string | null; status: 'active' | 'expired' | 'revoked' }
type SelfKeyPage = { items: SelfKey[]; next_cursor: string | null }
type SelfKeySlot = { id: string; name: string; expires_at: string | null }
type SelfIssuedKey = SelfKeySlot & { key: string }
type SelfRequest = { id: string; key_id: string; model_id: string; status: 'pending' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'; started_at: string; finished_at: string | null }
type SelfRequestPage = { items: SelfRequest[]; next_cursor: string | null }
type SelfTokenCounts = { known_total: string; unknown_attempts: string }
type SelfTokenSummary = {
  from: string; to: string
  requests: { total: string; pending: string; succeeded: string; failed: string; cancelled: string; interrupted: string }
  attempts: { total: string; pending: string; input_tokens: SelfTokenCounts; output_tokens: SelfTokenCounts; cache_read_tokens: SelfTokenCounts; cache_write_tokens: SelfTokenCounts }
}
type SelfWalletBalance = { currency: string; has_account: boolean; amount_micro: string | null }
type SelfWalletActivityItem = { occurred_at: string; delta_micro: string }
type SelfWalletActivityPage = { currency: string; has_account: boolean; window_start: string; window_end: string; items: SelfWalletActivityItem[]; next_cursor: string | null }
type SelfSubscriptionItem = { subscription_id: string; interval: 'one_time' | 'monthly'; status: 'active' | 'cancelled' | 'expired'; started_at: string; period_end_at: string | null; cancelled_at: string | null }
type SelfSubscriptionPage = { items: SelfSubscriptionItem[]; next_cursor: string | null }

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

function SelfKeyTokenSummaryPanel({ keyID, keyName, onClose }: { keyID: string; keyName: string; onClose: () => void }) {
  const [summary, setSummary] = useState<SelfTokenSummary | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)

  useEffect(() => {
    let active = true
    const controller = new AbortController()
    setSummary(null)
    setLoading(true)
    setError(false)
    void selfRequest<SelfTokenSummary>(`/keys/${encodeURIComponent(keyID)}/usage/summary`, { signal: controller.signal }).then((value) => {
      if (!active || controller.signal.aborted) return
      setSummary(value)
    }).catch(() => {
      if (!active || controller.signal.aborted) return
      setSummary(null)
      setError(true)
    }).finally(() => { if (active && !controller.signal.aborted) setLoading(false) })
    return () => { active = false; controller.abort() }
  }, [keyID])

  const tokenRows = summary ? [
    ['输入', summary.attempts.input_tokens],
    ['输出', summary.attempts.output_tokens],
    ['缓存读取', summary.attempts.cache_read_tokens],
    ['缓存写入', summary.attempts.cache_write_tokens],
  ] as const : []

  return <section className="self-summary self-key-token-summary" aria-labelledby="self-key-token-summary-title">
    <div className="self-key-token-summary-heading"><h3 id="self-key-token-summary-title">{keyName} 的已知 Token</h3><Button variant="secondary" onClick={onClose}>关闭汇总</Button></div>
    <p>只统计此 Key 的上游尝试已知值；未知尝试另列。这不是完整用量、计费或合规结论。</p>
    {loading ? <p role="status">正在读取此 Key 的 Token 汇总…</p> : null}
    {error ? <p role="alert">此 Key 的 Token 汇总暂时无法读取，请稍后重试。</p> : null}
    {summary && !error ? <>
      <p className="self-summary-window">统计时间：<time dateTime={summary.from}>{selfKeyDate(summary.from)}</time> 至 <time dateTime={summary.to}>{selfKeyDate(summary.to)}</time>（不含结束时刻）</p>
      <dl className="self-summary-counts">
        <div><dt>此 Key 请求</dt><dd>{summary.requests.total}</dd></div>
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

function SelfKeyRequestHistoryPanel({ keyID, keyName, onClose }: { keyID: string; keyName: string; onClose: () => void }) {
  const [items, setItems] = useState<SelfRequest[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const pendingPage = useRef<AbortController | null>(null)
  const mounted = useRef(true)

  useEffect(() => {
    mounted.current = true
    let active = true
    const controller = new AbortController()
    setItems([])
    setCursor(null)
    setLoading(true)
    setError(false)
    void selfRequest<SelfRequestPage>(`/keys/${encodeURIComponent(keyID)}/usage/requests`, { signal: controller.signal }).then((page) => {
      if (!active || controller.signal.aborted) return
      setItems(page.items)
      setCursor(page.next_cursor)
    }).catch(() => {
      if (!active || controller.signal.aborted) return
      setItems([])
      setCursor(null)
      setError(true)
    }).finally(() => { if (active && !controller.signal.aborted) setLoading(false) })
    return () => { active = false; mounted.current = false; controller.abort(); pendingPage.current?.abort() }
  }, [keyID])

  async function loadMore() {
    if (!cursor || loading || pendingPage.current) return
    const controller = new AbortController()
    pendingPage.current = controller
    setLoading(true)
    setError(false)
    try {
      const page = await selfRequest<SelfRequestPage>(`/keys/${encodeURIComponent(keyID)}/usage/requests?cursor=${encodeURIComponent(cursor)}`, { signal: controller.signal })
      if (!mounted.current || controller.signal.aborted) return
      setItems((prior) => [...prior, ...page.items])
      setCursor(page.next_cursor)
    } catch {
      if (!mounted.current || controller.signal.aborted) return
      setItems([])
      setCursor(null)
      setError(true)
    } finally {
      if (mounted.current && !controller.signal.aborted) setLoading(false)
      if (pendingPage.current === controller) pendingPage.current = null
    }
  }

  return <section className="self-history self-key-history" aria-label={`${keyName} 的请求活动`}>
    <div className="self-key-history-heading"><h3>{keyName} 的请求活动</h3><Button variant="secondary" onClick={onClose}>关闭活动</Button></div>
    <p>只显示此 Key 最近 24 小时的最小请求活动；不含正文、Token 或费用，也不代表完整用量、账单或合规结论。</p>
    {loading && items.length === 0 ? <p role="status">正在读取此 Key 的请求活动…</p> : null}
    {error ? <p role="alert">此 Key 的请求活动暂时无法读取，请稍后重试。</p> : null}
    {!error && !loading && items.length === 0 ? <p>此 Key 最近 24 小时暂无请求活动。</p> : null}
    {items.length > 0 ? <ul className="self-history-list">{items.map((item) => <li key={item.id}>
      <div className="self-history-top"><strong>{item.model_id}</strong><span className={`self-history-status self-history-status--${item.status}`}>{requestStatus[item.status]}</span></div>
      <dl><div><dt>请求 ID</dt><dd>{item.id}</dd></div><div><dt>Key ID</dt><dd>{item.key_id}</dd></div><div><dt>开始时间</dt><dd><time dateTime={item.started_at}>{selfKeyDate(item.started_at)}</time></dd></div><div><dt>结束时间</dt><dd>{item.finished_at ? <time dateTime={item.finished_at}>{selfKeyDate(item.finished_at)}</time> : '尚未结束'}</dd></div></dl>
    </li>)}</ul> : null}
    {cursor && !error ? <Button variant="secondary" disabled={loading} onClick={loadMore}>{loading ? '正在加载…' : '加载更多请求'}</Button> : null}
  </section>
}

function SelfKeyInventory({ csrf }: { csrf: string }) {
  const [items, setItems] = useState<SelfKey[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState(false)
  const [confirmID, setConfirmID] = useState<string | null>(null)
  const [revokeBusy, setRevokeBusy] = useState(false)
  const [revokeError, setRevokeError] = useState<string | null>(null)
  const [selectedKeyView, setSelectedKeyView] = useState<{ id: string; kind: 'tokens' | 'requests' } | null>(null)
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
      setSelectedKeyView(null)
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
      setSelectedKeyView(null)
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
        setSelectedKeyView(null)
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
      setSelectedKeyView(null)
    } finally {
      if (!controller.signal.aborted) setLoading(false)
      if (pendingPage.current === controller) pendingPage.current = null
    }
  }

  const selectedKey = items.find((item) => item.id === selectedKeyView?.id)

  return <section className="self-keys" aria-labelledby="self-keys-title">
    <h2 id="self-keys-title">我的 API Key</h2>
    <p>仅显示你已领取或管理员创建的 Key 名称、时间和状态；明文只在创建或领取成功时显示一次。</p>
    {loading && items.length === 0 ? <p role="status">正在读取 Key 列表…</p> : null}
    {error ? <p role="alert">Key 列表暂时无法读取，请稍后重新登录或刷新页面。</p> : null}
    {!error && !loading && items.length === 0 ? <p>暂无 API Key。</p> : null}
    {items.length > 0 ? <ul className="self-key-list">{items.map((item) => <li key={item.id}>
      <div className="self-key-top"><strong>{item.name}</strong><span className={`self-key-status self-key-status--${item.status}`}>{item.status === 'active' ? '有效' : item.status === 'expired' ? '已到期' : '已撤销'}</span></div>
      <dl><div><dt>Key ID</dt><dd>{item.id}</dd></div><div><dt>创建时间</dt><dd><time dateTime={item.created_at}>{selfKeyDate(item.created_at)}</time></dd></div><div><dt>到期时间</dt><dd>{item.expires_at ? <time dateTime={item.expires_at}>{selfKeyDate(item.expires_at)}</time> : '永不过期'}</dd></div>{item.revoked_at ? <div><dt>撤销时间</dt><dd><time dateTime={item.revoked_at}>{selfKeyDate(item.revoked_at)}</time></dd></div> : null}</dl>
      <div className="self-key-view-actions"><Button variant="secondary" onClick={() => setSelectedKeyView((prior) => prior?.id === item.id && prior.kind === 'tokens' ? null : { id: item.id, kind: 'tokens' })} aria-label={`查看 ${item.name} 的已知 Token`}>{selectedKeyView?.id === item.id && selectedKeyView.kind === 'tokens' ? '收起此 Key 汇总' : '查看已知 Token'}</Button><Button variant="secondary" onClick={() => setSelectedKeyView((prior) => prior?.id === item.id && prior.kind === 'requests' ? null : { id: item.id, kind: 'requests' })} aria-label={`查看 ${item.name} 的请求活动`}>{selectedKeyView?.id === item.id && selectedKeyView.kind === 'requests' ? '收起此 Key 活动' : '查看请求活动'}</Button></div>
      {item.status !== 'revoked' ? <div className="self-key-revoke">
        {confirmID === item.id ? <form onSubmit={(event) => { void revoke(event, item.id) }}>
          <p>确认撤销“{item.name}”？撤销后此 Key 不能再发起新请求，正在进行的请求不会因此中断。</p>
          <Field label="当前密码（撤销确认）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
          {revokeError ? <p role="alert">{revokeError}</p> : null}
          <div className="self-actions"><Button type="submit" disabled={revokeBusy}>{revokeBusy ? '正在撤销…' : '确认撤销 Key'}</Button><Button type="button" variant="secondary" disabled={revokeBusy} onClick={() => { setConfirmID(null); setRevokeError(null) }}>取消</Button></div>
        </form> : <Button variant="secondary" disabled={revokeBusy} onClick={() => { setConfirmID(item.id); setRevokeError(null) }} aria-label={`撤销 ${item.name}`}>撤销 Key</Button>}
      </div> : null}
    </li>)}</ul> : null}
    {selectedKey && selectedKeyView?.kind === 'tokens' ? <SelfKeyTokenSummaryPanel key={`tokens:${selectedKey.id}`} keyID={selectedKey.id} keyName={selectedKey.name} onClose={() => setSelectedKeyView(null)} /> : null}
    {selectedKey && selectedKeyView?.kind === 'requests' ? <SelfKeyRequestHistoryPanel key={`requests:${selectedKey.id}`} keyID={selectedKey.id} keyName={selectedKey.name} onClose={() => setSelectedKeyView(null)} /> : null}
    {cursor && !error ? <Button variant="secondary" disabled={loading} onClick={loadMore}>{loading ? '正在加载…' : '加载更多'}</Button> : null}
  </section>
}

function SelfKeyIssuance({ csrf, onIssued }: { csrf: string; onIssued: () => void }) {
  const [slot, setSlot] = useState<SelfKeySlot | null>(null)
  const [loading, setLoading] = useState(true)
  const [confirming, setConfirming] = useState(false)
  const [busy, setBusy] = useState(false)
  const [issued, setIssued] = useState<SelfIssuedKey | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [copied, setCopied] = useState(false)
  const mounted = useRef(true)
  const pendingIssue = useRef<AbortController | null>(null)
  useEffect(() => {
    mounted.current = true
    const controller = new AbortController()
    void selfRequest<{ items: SelfKeySlot[] }>('/key-slots', { signal: controller.signal }).then((result) => {
      if (mounted.current) setSlot(result.items[0] ?? null)
    }).catch(() => { if (mounted.current) setError('授权槽位暂时无法读取，请刷新后重试。') }).finally(() => { if (mounted.current) setLoading(false) })
    return () => { mounted.current = false; controller.abort(); pendingIssue.current?.abort() }
  }, [])

  async function issue(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!slot || busy) return
    const form = event.currentTarget
    let current_password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const size = new TextEncoder().encode(current_password).length
    if (size < 12 || size > 72) {
      current_password = ''
      setError('当前密码须为 12–72 个 UTF-8 字节。')
      return
    }
    setBusy(true); setError(null); setIssued(null)
    const controller = new AbortController()
    pendingIssue.current = controller
    try {
      const result = await selfRequest<SelfIssuedKey>('/keys/issue', { method: 'POST', body: JSON.stringify({ slot_id: slot.id, current_password }), signal: controller.signal }, csrf)
      if (!mounted.current || controller.signal.aborted) return
      setIssued(result)
      setSlot(null)
      setConfirming(false)
      onIssued()
    } catch (caught) {
      if (!mounted.current || controller.signal.aborted) return
      setIssued(null)
      setError(caught instanceof ApiError && caught.status === 503
        ? '领取结果未确认。请刷新并检查 Key 列表；若已签发但未看到明文，请联系管理员撤销并重新创建。'
        : caught instanceof ApiError && caught.status === 429
          ? '尝试次数过多，请稍后再试。'
          : '领取未完成。请检查密码或联系管理员确认授权状态。')
    } finally {
      current_password = ''
      if (pendingIssue.current === controller) pendingIssue.current = null
      if (mounted.current) setBusy(false)
    }
  }

  if (loading) return <section className="self-keys" aria-label="领取管理员授权的 Key"><p role="status">正在检查授权槽位…</p></section>
  if (!slot && !issued && !error) return null
  return <section className="self-keys" aria-labelledby="self-key-issue-title">
    <h2 id="self-key-issue-title">领取管理员授权的 Key</h2>
    {issued ? <><p role="status">Key 已签发。请立即复制并安全保存；关闭或离开后无法再次查看。</p><div className="copy-field"><code data-testid="self-issued-key" style={{ overflowWrap: 'anywhere' }}>{issued.key}</code><Button variant="secondary" onClick={async () => { try { await navigator.clipboard.writeText(issued.key); setCopied(true) } catch { setError('复制失败，请手动选择并复制。') } }}>{copied ? '已复制' : '复制 Key'}</Button></div><div className="self-actions"><Button variant="secondary" onClick={() => { setIssued(null); setCopied(false); setError(null) }}>我已保存，关闭</Button></div></> : null}
    {slot ? <><p>管理员已为你预留一个新的 Key：{slot.name}（{slot.id}）。它有独立的 Key ID 和管理员配置的限额，不是现有 Key 的额度延伸。{slot.expires_at ? `到期：${selfKeyDate(slot.expires_at)}。` : '默认永不过期。'}</p>{confirming ? <form className="self-form" onSubmit={(event) => { void issue(event) }}><p>确认领取后，明文只显示这一次。请准备安全的保存位置。</p><Field label="当前密码（领取确认）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field><div className="self-actions"><Button type="submit" disabled={busy}>{busy ? '正在领取…' : '确认领取 Key'}</Button><Button type="button" variant="secondary" disabled={busy} onClick={() => { setConfirming(false); setError(null) }}>取消</Button></div></form> : <Button variant="secondary" onClick={() => { setConfirming(true); setError(null) }}>领取此 Key</Button>}</> : null}
    {error ? <p role="alert">{error}</p> : null}
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

function SelfWalletBalancePanel() {
  const [currency, setCurrency] = useState('')
  const [balance, setBalance] = useState<SelfWalletBalance | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])

  function changeCurrency(value: string) {
    generation.current++
    pending.current?.abort()
    pending.current = null
    setCurrency(value)
    setBalance(null)
    setError(false)
    setLoading(false)
  }

  async function readBalance(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!/^[A-Z]{3}$/.test(currency)) {
      setBalance(null)
      setError(true)
      return
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const requestedCurrency = currency
    const controller = new AbortController()
    pending.current = controller
    setBalance(null)
    setError(false)
    setLoading(true)
    try {
      const value = await selfRequest<SelfWalletBalance>(`/billing/balance?currency=${encodeURIComponent(requestedCurrency)}`, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (value.currency !== requestedCurrency || typeof value.has_account !== 'boolean' ||
        (value.has_account ? typeof value.amount_micro !== 'string' || !/^(?:0|-[1-9][0-9]*|[1-9][0-9]*)$/.test(value.amount_micro) : value.amount_micro !== null)) {
        throw new Error('Invalid balance response')
      }
      setBalance(value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setBalance(null)
      setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-wallet" aria-labelledby="self-wallet-title">
    <h2 id="self-wallet-title">我的钱包余额</h2>
    <p>仅查看你本人名下的员工钱包，不包括 API Key 或资源子账户。请输入一个币种后按需读取；这不是供应商余额或完整账单。</p>
    <form className="self-wallet-form" onSubmit={(event) => { void readBalance(event) }}>
      <Field label="币种（三位大写字母，如 USD）"><input name="currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取余额'}</Button>
    </form>
    {error ? <p role="alert">钱包余额暂时无法读取，请检查币种或稍后重试。</p> : null}
    {balance && !error ? balance.has_account
      ? <p className="self-wallet-result" role="status"><strong>{balance.currency}</strong> 员工钱包余额：<output>{balance.amount_micro}</output> micro</p>
      : <p className="self-wallet-result" role="status"><strong>{balance.currency}</strong> 暂无员工钱包账户（未显示为零余额）。</p> : null}
  </section>
}

function validSelfWalletActivityPage(raw: unknown, currency: string, previous?: SelfWalletActivityPage): raw is SelfWalletActivityPage {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  if (Object.keys(value).sort().join(',') !== 'currency,has_account,items,next_cursor,window_end,window_start' ||
    value.currency !== currency || typeof value.has_account !== 'boolean' || !Array.isArray(value.items) || value.items.length > 20 ||
    (value.next_cursor !== null && (typeof value.next_cursor !== 'string' || value.next_cursor.length < 1 || value.next_cursor.length > 1024))) return false
  const windowTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/
  if (typeof value.window_start !== 'string' || typeof value.window_end !== 'string' ||
    !windowTime.test(value.window_start) || !windowTime.test(value.window_end) ||
    Date.parse(value.window_end) - Date.parse(value.window_start) !== 31 * 24 * 60 * 60 * 1000) return false
  if (previous && (value.window_start !== previous.window_start || value.window_end !== previous.window_end || value.has_account !== previous.has_account)) return false
  if (!value.has_account && (value.items.length !== 0 || value.next_cursor !== null)) return false
  if (value.next_cursor !== null && value.items.length === 0) return false
  const entryTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/
  return value.items.every((item: unknown) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return false
    const entry = item as Record<string, unknown>
    return Object.keys(entry).sort().join(',') === 'delta_micro,occurred_at' && typeof entry.occurred_at === 'string' &&
      entryTime.test(entry.occurred_at) && !/\.\d*0Z$/.test(entry.occurred_at) && Number.isFinite(Date.parse(entry.occurred_at)) &&
      typeof entry.delta_micro === 'string' && /^(?:-[1-9]\d*|[1-9]\d*)$/.test(entry.delta_micro)
  })
}

function SelfWalletActivityPanel() {
  const [currency, setCurrency] = useState('')
  const [page, setPage] = useState<SelfWalletActivityPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])

  function changeCurrency(value: string) {
    generation.current++
    pending.current?.abort()
    pending.current = null
    setCurrency(value)
    setPage(null)
    setError(false)
    setLoading(false)
  }

  async function readPage(cursor?: string) {
    const requestedCurrency = currency
    const prior = cursor ? page : null
    if (!/^[A-Z]{3}$/.test(requestedCurrency) || (cursor && (!prior || prior.next_cursor !== cursor))) {
      setPage(null)
      setError(true)
      return
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    if (!cursor) setPage(null)
    setError(false)
    setLoading(true)
    try {
      const path = `/billing/entries?currency=${encodeURIComponent(requestedCurrency)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfWalletActivityPage(value, requestedCurrency, prior ?? undefined)) throw new Error('Invalid wallet activity response')
      setPage(prior ? { ...value, items: [...prior.items, ...value.items] } : value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setPage(null)
      setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-wallet-activity" aria-labelledby="self-wallet-activity-title">
    <h2 id="self-wallet-activity-title">最近钱包变动</h2>
    <p>仅显示你本人名下员工钱包近 31 天的逐笔金额变动，不含 API Key 或资源子账户。按需读取；这不是完整账单、支付记录或实时余额。同一秒内按稳定存储键显示，不保证纳秒先后。</p>
    <form className="self-wallet-form" onSubmit={(event) => { event.preventDefault(); void readPage() }}>
      <Field label="流水币种（三位大写字母，如 USD）"><input name="activity_currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取最近变动'}</Button>
    </form>
    {error ? <p role="alert">最近钱包变动暂时无法读取，请检查币种或稍后重试。</p> : null}
    {page && !error ? <div className="self-wallet-activity-result" role="status">
      <p>查询窗口：<time dateTime={page.window_start}>{selfKeyDate(page.window_start)}</time> 至 <time dateTime={page.window_end}>{selfKeyDate(page.window_end)}</time>（不含结束时刻）</p>
      {!page.has_account ? <p><strong>{page.currency}</strong> 暂无员工钱包账户（未显示为零余额）。</p>
        : page.items.length === 0 ? <p><strong>{page.currency}</strong> 员工钱包在此窗口暂无变动；这不代表余额为零。</p>
          : <ol className="self-wallet-activity-list">{page.items.map((item, index) => <li key={`${item.occurred_at}:${item.delta_micro}:${index}`}>
            <time dateTime={item.occurred_at}>{selfKeyDate(item.occurred_at)}</time>
            <strong className={item.delta_micro.startsWith('-') ? 'self-wallet-activity-negative' : 'self-wallet-activity-positive'}>{item.delta_micro.startsWith('-') ? '' : '+'}{item.delta_micro} micro</strong>
          </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多变动'}</Button> : null}
    </div> : null}
  </section>
}

function validSelfSubscriptionPage(raw: unknown, previous?: SelfSubscriptionPage): raw is SelfSubscriptionPage {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const page = raw as Record<string, unknown>
  if (Object.keys(page).sort().join(',') !== 'items,next_cursor' || !Array.isArray(page.items) || page.items.length > 20 ||
    (page.next_cursor !== null && (typeof page.next_cursor !== 'string' || page.next_cursor.length < 1 || page.next_cursor.length > 1024)) ||
    (page.next_cursor !== null && page.items.length === 0)) return false
  const date = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/
  let priorID = previous?.items.at(-1)?.subscription_id ?? ''
  for (const rawItem of page.items) {
    if (!rawItem || typeof rawItem !== 'object' || Array.isArray(rawItem)) return false
    const item = rawItem as Record<string, unknown>
    if (Object.keys(item).sort().join(',') !== 'cancelled_at,interval,period_end_at,started_at,status,subscription_id' ||
      typeof item.subscription_id !== 'string' || item.subscription_id.length < 1 || item.subscription_id.length > 256 ||
      item.subscription_id.trim() !== item.subscription_id || (priorID !== '' && item.subscription_id >= priorID) ||
      (item.interval !== 'one_time' && item.interval !== 'monthly') ||
      (item.status !== 'active' && item.status !== 'cancelled' && item.status !== 'expired') ||
      typeof item.started_at !== 'string' || !date.test(item.started_at) || !Number.isFinite(Date.parse(item.started_at)) ||
      (item.interval === 'monthly' ? typeof item.period_end_at !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/.test(item.period_end_at) || !Number.isFinite(Date.parse(item.period_end_at)) : item.period_end_at !== null) ||
      (item.status === 'cancelled' ? typeof item.cancelled_at !== 'string' || !date.test(item.cancelled_at) || !Number.isFinite(Date.parse(item.cancelled_at)) : item.cancelled_at !== null) ||
      (item.interval === 'one_time' && item.status === 'expired')) return false
    priorID = item.subscription_id
  }
  return true
}

const selfSubscriptionStatus: Record<SelfSubscriptionItem['status'], string> = { active: '有效', cancelled: '已取消', expired: '已到期' }

function SelfSubscriptionStatusPanel() {
  const [page, setPage] = useState<SelfSubscriptionPage | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])

  async function readPage(cursor?: string) {
    const prior = cursor ? page : null
    if (cursor && (!prior || prior.next_cursor !== cursor)) {
      setPage(null)
      setError(true)
      return
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    if (!cursor) setPage(null)
    setError(false)
    setLoading(true)
    try {
      const path = `/billing/subscriptions?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfSubscriptionPage(value, prior ?? undefined)) throw new Error('Invalid subscription status response')
      setPage(prior ? { items: [...prior.items, ...value.items], next_cursor: value.next_cursor } : value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setPage(null)
      setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-subscriptions" aria-labelledby="self-subscriptions-title">
    <h2 id="self-subscriptions-title">我的订阅状态</h2>
    <p>仅按需查看你本人直接名下既有订阅的期限与状态，不含 Key 或资源子账户。这不是模型使用权益、账单或购买入口；列表按订阅 ID 稳定排列，不代表时间先后。</p>
    <Button variant="secondary" disabled={loading} onClick={() => { void readPage() }}>{loading ? '正在读取…' : '读取我的订阅状态'}</Button>
    {error ? <p role="alert">订阅状态暂时无法读取，请稍后重试。</p> : null}
    {page && !error ? <div className="self-subscriptions-result" role="status">
      {page.items.length === 0 ? <p>目前没有可显示的本人订阅记录。</p> : <ol className="self-subscriptions-list">{page.items.map((item) => <li key={item.subscription_id}>
        <div className="self-subscriptions-heading"><strong>{item.subscription_id}</strong><span className={`self-subscriptions-status self-subscriptions-status--${item.status}`}>{selfSubscriptionStatus[item.status]}</span></div>
        <dl><div><dt>周期</dt><dd>{item.interval === 'monthly' ? '单月' : '一次性'}</dd></div><div><dt>开始</dt><dd><time dateTime={item.started_at}>{selfKeyDate(item.started_at)}</time></dd></div>
          <div><dt>期限结束</dt><dd>{item.period_end_at ? <time dateTime={item.period_end_at}>{selfKeyDate(item.period_end_at)}</time> : '无固定结束时间'}</dd></div>
          <div><dt>取消时间</dt><dd>{item.cancelled_at ? <time dateTime={item.cancelled_at}>{selfKeyDate(item.cancelled_at)}</time> : '未取消'}</dd></div></dl>
      </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多订阅'}</Button> : null}
    </div> : null}
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
  const [keyInventoryRevision, setKeyInventoryRevision] = useState(0)
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
        <header><span className="self-kicker">PERSONAL PROFILE</span><h1>你好，{session.profile.name}</h1><p>你可以查看个人资料、API Key、本人请求记录和 Token 汇总，也可以撤销自己的 Key 或退出其他设备。仅在管理员明确授权槽位后，你才能领取一个新的 Key；权限和限额仍由管理员管理。</p></header>
        <dl className="self-profile"><div><dt>员工 ID</dt><dd>{session.profile.id}</dd></div><div><dt>姓名</dt><dd>{session.profile.name}</dd></div><div><dt>部门</dt><dd>{session.profile.department || '未设置'}</dd></div><div><dt>状态</dt><dd>{session.profile.status === 'active' ? '启用' : '已停用'}</dd></div></dl>
        {session.features?.employee_self_wallet_balance === true ? <SelfWalletBalancePanel key={`wallet:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_wallet_activity === true
          ? <SelfWalletActivityPanel key={`wallet-activity:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_subscription_status === true
          ? <SelfSubscriptionStatusPanel key={`subscriptions:${session.profile.id}:${session.csrf_token}`} /> : null}
        <SelfKeyIssuance key={`issue:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} onIssued={() => setKeyInventoryRevision((value) => value + 1)} />
        <SelfKeyInventory key={`keys:${session.profile.id}:${session.csrf_token}:${keyInventoryRevision}`} csrf={session.csrf_token} />
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
