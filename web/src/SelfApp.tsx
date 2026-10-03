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
// docs/employee-self-wallet-entry-classification-contract.md.
// docs/employee-self-subscription-status-contract.md.
// docs/employee-self-plan-catalog-contract.md.
// docs/employee-self-plan-purchase-contract.md.
// docs/employee-self-subscription-cancel-contract.md.
// docs/employee-self-one-shot-disarm-contract.md.
// docs/employee-self-subscription-purchase-snapshot-contract.md.
// docs/employee-self-monthly-renewal-contract.md.
// docs/employee-self-subscription-renewal-links-contract.md.
// docs/employee-self-redemption-contract.md.
// docs/employee-self-redemption-credit-history-contract.md.
// docs/employee-self-admin-adjustment-history-contract.md.
// docs/employee-self-upstream-estimated-cost-summary-contract.md.
import { type FormEvent, useEffect, useRef, useState } from 'react'
import { ApiError } from './api'
import { Button, Field, FormError } from './ui'

type Profile = { id: string; name: string; department: string; status: 'active' | 'disabled' }
type SelfSession = { csrf_token: string; profile: Profile; features?: { employee_self_upstream_estimated_cost_summary?: boolean; employee_self_wallet_balance?: boolean; employee_self_redemption?: boolean; employee_self_wallet_activity?: boolean; employee_self_wallet_entry_classification?: boolean; employee_self_redemption_credit_history?: boolean; employee_self_admin_adjustment_history?: boolean; employee_self_subscription_status?: boolean; employee_self_subscription_purchase_snapshot?: boolean; employee_self_subscription_renewal_links?: boolean; employee_self_subscription_cancel?: boolean; employee_self_one_shot_renewal_disarm?: boolean; employee_self_subscription_renewal?: boolean; employee_self_plan_catalog?: boolean; employee_self_plan_purchase?: boolean } }
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
type SelfEstimatedCostGroup = { price_currency: string | null; attempts: string; known_estimated_cost_micro: string; unknown_cost_attempts: string }
type SelfEstimatedCostSummary = { from: string; to: string; attempts: { total: string; pending: string; terminal: string }; costs: SelfEstimatedCostGroup[] }
type SelfWalletBalance = { currency: string; has_account: boolean; amount_micro: string | null }
type SelfRedemptionResult = { operation_id: string; replay: boolean; currency: string; amount_micro: string; credited_at: string }
type SelfWalletActivityItem = { occurred_at: string; delta_micro: string }
type SelfWalletActivityPage = { currency: string; has_account: boolean; window_start: string; window_end: string; items: SelfWalletActivityItem[]; next_cursor: string | null }
type SelfEntryKind = 'adjustment_credit' | 'adjustment_debit' | 'topup' | 'redemption' | 'subscription_charge' | 'subscription_credit' | 'refund' | 'usage_charge'
type SelfClassificationItem = SelfWalletActivityItem & { entry_kind: SelfEntryKind }
type SelfClassificationPage = Omit<SelfWalletActivityPage, 'items'> & { items: SelfClassificationItem[] }
type SelfRedemptionCreditItem = { credited_at: string; amount_micro: string }
type SelfRedemptionCreditPage = Omit<SelfWalletActivityPage, 'items'> & { items: SelfRedemptionCreditItem[] }
type SelfSubscriptionItem = { subscription_id: string; interval: 'one_time' | 'monthly'; status: 'active' | 'cancelled' | 'expired'; started_at: string; period_end_at: string | null; cancelled_at: string | null; revision?: number }
type SelfSubscriptionPage = { items: SelfSubscriptionItem[]; next_cursor: string | null }
type SelfPurchaseSnapshot = { subscription_id: string; plan_id: string; plan_revision: number; currency: string; interval: 'one_time' | 'monthly'; price_micro: string; credit_micro: string; started_at: string; period_end_at: string | null }
type SelfRenewalLinks = { subscription_id: string; predecessor_id: string | null; successor_id: string | null }
type SelfSubscriptionCancelResult = { operation_id: string; subscription_id: string; replay: boolean; status: 'cancelled'; revision: number; cancelled_at: string }
type SelfOneShotStatus = { subscription_id: string; state: 'none' | 'armed' | 'disarmed' | 'succeeded' | 'failed' | 'superseded' | 'cancelled'; revision: number; due_at: string | null; reason: string | null; terminal_at: string | null }
type SelfOneShotDisarmResult = SelfOneShotStatus & { operation_id: string; replay: boolean; state: 'disarmed'; revision: 2; due_at: string; reason: 'disarmed'; terminal_at: string }
type SelfPlanCatalogItem = { plan_id: string; name: string; interval: 'one_time' | 'monthly'; price_micro: string; credit_micro: string; revision: number }
type SelfPlanCatalogPage = { currency: string; available: boolean; items: SelfPlanCatalogItem[]; next_cursor: string | null }
type SelfPlanPurchaseQuote = { quote_token: string; plan_id: string; revision: number; currency: string; interval: 'one_time'; price_micro: string; credit_micro: string; expires_at: string }
type SelfPlanPurchaseResult = { operation_id: string; subscription_id: string; replay: boolean; plan_id: string; revision: number; currency: string; interval: 'one_time'; price_micro: string; credit_micro: string }
type SelfMonthlyRenewalQuote = { quote_token: string; predecessor_id: string; predecessor_period_end_at: string; plan_id: string; revision: number; currency: string; interval: 'monthly'; price_micro: string; credit_micro: string; expires_at: string }
type SelfMonthlyRenewalResult = { operation_id: string; predecessor_id: string; subscription_id: string; replay: boolean; plan_id: string; revision: number; currency: string; interval: 'monthly'; price_micro: string; credit_micro: string; started_at: string; period_end_at: string }

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

const selfCostDecimal = (value: unknown): value is string => typeof value === 'string' && value.length <= 19 && /^(0|[1-9][0-9]*)$/.test(value) && BigInt(value) <= 9223372036854775807n
const selfCostTime = (value: unknown): value is string => {
  if (typeof value !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$/.test(value)) return false
  const parsed = Date.parse(value)
  return Number.isFinite(parsed) && new Date(parsed).toISOString() === value.replace(/Z$/, '.000Z')
}

function validSelfEstimatedCostSummary(raw: unknown): raw is SelfEstimatedCostSummary {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  if (Object.keys(value).sort().join(',') !== 'attempts,costs,from,to' || !selfCostTime(value.from) || !selfCostTime(value.to) ||
    Date.parse(value.to) - Date.parse(value.from) !== 24 * 60 * 60 * 1000 ||
    !value.attempts || typeof value.attempts !== 'object' || Array.isArray(value.attempts) || !Array.isArray(value.costs)) return false
  const attempts = value.attempts as Record<string, unknown>
  if (Object.keys(attempts).sort().join(',') !== 'pending,terminal,total' || !selfCostDecimal(attempts.total) || !selfCostDecimal(attempts.pending) || !selfCostDecimal(attempts.terminal) ||
    BigInt(attempts.total) !== BigInt(attempts.pending) + BigInt(attempts.terminal)) return false
  let terminal = 0n
  let previous = ''
  for (const rawGroup of value.costs) {
    if (!rawGroup || typeof rawGroup !== 'object' || Array.isArray(rawGroup)) return false
    const group = rawGroup as Record<string, unknown>
    if (Object.keys(group).sort().join(',') !== 'attempts,known_estimated_cost_micro,price_currency,unknown_cost_attempts' ||
      !selfCostDecimal(group.attempts) || !selfCostDecimal(group.known_estimated_cost_micro) || !selfCostDecimal(group.unknown_cost_attempts) ||
      BigInt(group.unknown_cost_attempts) > BigInt(group.attempts) || BigInt(group.attempts) === 0n ||
      !(group.price_currency === null || typeof group.price_currency === 'string' && /^[A-Z]{3}$/.test(group.price_currency))) return false
    if (group.price_currency === null && (group.known_estimated_cost_micro !== '0' || group.unknown_cost_attempts !== group.attempts)) return false
    const key = group.price_currency === null ? 'ZZZZ' : group.price_currency as string
    if (previous !== '' && key <= previous) return false
    previous = key
    terminal += BigInt(group.attempts)
  }
  return terminal === BigInt(attempts.terminal)
}

function selfCostAmount(micro: string) {
  const value = BigInt(micro)
  return `${value / 1000000n}.${(value % 1000000n).toString().padStart(6, '0')}`
}

function SelfEstimatedCostPanel() {
  const [summary, setSummary] = useState<SelfEstimatedCostSummary | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort(); pending.current = null }, [])

  async function read() {
    generation.current++
    pending.current?.abort()
    const controller = new AbortController()
    pending.current = controller
    const current = generation.current
    setSummary(null)
    setError(false)
    setLoading(true)
    try {
      const raw = await selfRequest<unknown>('/usage/estimated-cost-summary', { signal: controller.signal })
      if (controller.signal.aborted || current !== generation.current) return
      if (!validSelfEstimatedCostSummary(raw)) throw new Error('Invalid estimated cost response')
      setSummary(raw)
    } catch {
      if (controller.signal.aborted || current !== generation.current) return
      setSummary(null)
      setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && current === generation.current) setLoading(false)
    }
  }

  return <section className="self-summary self-estimated-cost" aria-labelledby="self-estimated-cost-title">
    <h2 id="self-estimated-cost-title">上游内部估算成本</h2>
    <p>仅按本人上游尝试的冻结内部价格计算，不是供应商账单、员工应付金额、钱包扣款或模型权益。未知尝试不当作零费用。</p>
    <Button variant="secondary" disabled={loading} onClick={() => { void read() }}>{loading ? '正在读取…' : '查看最近 24 小时估算'}</Button>
    {loading ? <p role="status">正在读取上游内部估算…</p> : null}
    {error ? <p role="alert">估算摘要暂时无法读取，已清除旧结果；请稍后重试。</p> : null}
    {summary ? <>
      <p className="self-summary-window">统计时间：<time dateTime={summary.from}>{selfKeyDate(summary.from)}</time> 至 <time dateTime={summary.to}>{selfKeyDate(summary.to)}</time>（不含结束时刻）</p>
      <dl className="self-summary-counts"><div><dt>上游尝试</dt><dd>{summary.attempts.total}</dd></div><div><dt>待结束尝试</dt><dd>{summary.attempts.pending}</dd></div><div><dt>已终结尝试</dt><dd>{summary.attempts.terminal}</dd></div></dl>
      {summary.costs.length === 0 ? <p>本窗口暂无已终结的上游尝试。</p> : <ul className="self-summary-tokens">{summary.costs.map((group) => <li key={group.price_currency ?? 'unpriced'}>
        <strong>{group.price_currency ?? '未配置价格'}</strong>
        <dl><div><dt>已知估算部分</dt><dd>{selfCostAmount(group.known_estimated_cost_micro)}{group.price_currency ? ` ${group.price_currency}` : '（无定价）'}</dd></div><div><dt>未知成本尝试</dt><dd>{group.unknown_cost_attempts}</dd></div><div><dt>已终结尝试</dt><dd>{group.attempts}</dd></div></dl>
      </li>)}</ul>}
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

function validSelfRedemptionResult(raw: unknown, operationID: string): raw is SelfRedemptionResult {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  return Object.keys(value).sort().join(',') === 'amount_micro,credited_at,currency,operation_id,replay' &&
    value.operation_id === operationID && typeof value.replay === 'boolean' &&
    typeof value.currency === 'string' && /^[A-Z]{3}$/.test(value.currency) &&
    validPurchaseAmount(value.amount_micro) && typeof value.credited_at === 'string' &&
    purchaseUTC.test(value.credited_at) && Number.isFinite(Date.parse(value.credited_at))
}

function newSelfRedemptionOperationID(): string {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return `self-redeem-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function SelfRedemptionPanel({ csrf }: { csrf: string }) {
  const [result, setResult] = useState<SelfRedemptionResult | null>(null)
  const [retry, setRetry] = useState<{ operationID: string; code: string; until: number } | null>(null)
  const [reviewRequired, setReviewRequired] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const inFlight = useRef(false)

  useEffect(() => () => { generation.current++; pending.current?.abort(); pending.current = null }, [])
  useEffect(() => {
    if (!retry) return
    const timer = window.setTimeout(() => abandonPending(), Math.max(0, retry.until - Date.now()))
    return () => window.clearTimeout(timer)
  }, [retry])

  function abandonPending() {
    generation.current++
    pending.current?.abort()
    pending.current = null
    inFlight.current = false
    setRetry(null)
    setResult(null)
    setBusy(false)
    setReviewRequired(true)
    setError('待决信息已失效。请让管理员通过安全审计核对；不要另起操作重复兑换。')
  }

  function clear() {
    generation.current++
    pending.current?.abort()
    pending.current = null
    inFlight.current = false
    setRetry(null)
    setResult(null)
    setBusy(false)
    setReviewRequired(false)
    setError(null)
  }

  async function redeem(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (reviewRequired || inFlight.current || busy) return
    if (retry && Date.now() >= retry.until) { abandonPending(); return }
    const form = event.currentTarget
    const data = new FormData(form)
    let code = retry?.code ?? String(data.get('code') ?? '')
    let current_password = String(data.get('current_password') ?? '')
    form.reset()
    const passwordBytes = new TextEncoder().encode(current_password).length
    if (!code || new TextEncoder().encode(code).length > 256 || passwordBytes < 12 || passwordBytes > 72) {
      code = ''; current_password = ''
      clear()
      setError('请填写兑换码和 12–72 个 UTF-8 字节的当前密码。')
      return
    }
    let operationID = retry?.operationID ?? ''
    if (!retry) {
      try { operationID = newSelfRedemptionOperationID() } catch {
        code = ''; current_password = ''
        clear()
        setError('无法生成兑换操作编号，请稍后重试。')
        return
      }
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    inFlight.current = true
    setBusy(true)
    setResult(null)
    setError(null)
    let requestTimer: number | undefined
    let timedOut = false
    try {
      let body = JSON.stringify({ operation_id: operationID, code, current_password })
      current_password = ''
      const request = selfRequest<unknown>('/billing/redemptions', { method: 'POST', body, signal: controller.signal }, csrf)
      body = ''
      const raw = await Promise.race([request, new Promise<never>((_, reject) => {
        requestTimer = window.setTimeout(() => { timedOut = true; controller.abort(); reject(new Error('redemption_request_timeout')) }, 10_000)
      })])
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfRedemptionResult(raw, operationID)) throw new Error('Invalid redemption response')
      setRetry(null)
      setResult(raw)
    } catch (caught) {
      if (generation.current !== requestGeneration || controller.signal.aborted && !timedOut) return
      const uncertain = !(caught instanceof ApiError) || caught.status === 503
      setResult(null)
      if (uncertain) {
        setRetry({ operationID, code, until: retry?.until ?? Date.now() + 2 * 60 * 1000 })
        setError('结果未确认。请重新输入当前密码，显式重试同一兑换操作。')
      } else {
        setRetry(null)
        setError(caught.status === 409 ? '兑换暂不可用；请向管理员核对兑换码。' : '兑换未完成，请重新输入兑换码并确认。')
      }
    } finally {
      if (requestTimer !== undefined) window.clearTimeout(requestTimer)
      code = ''; current_password = ''; operationID = ''
      if (pending.current === controller) pending.current = null
      if (generation.current === requestGeneration) inFlight.current = false
      if (generation.current === requestGeneration) setBusy(false)
    }
  }

  return <section className="self-redemption" aria-labelledby="self-redemption-title">
    <h2 id="self-redemption-title">兑换码充值本人钱包</h2>
    <p>仅使用管理员发放的兑换码，为你本人直属钱包增加码载明币种的余额。它不是付款、订阅或模型权益凭证；提交后可另行读取最新钱包余额。</p>
    {error ? <p role="alert">{error}</p> : null}
    {result ? <p className="self-redemption-result" role="status">{result.replay ? '已确认原兑换' : '兑换成功'}：本人 {result.currency} 钱包入账 <strong>{result.amount_micro} micro</strong>，时间 <time dateTime={result.credited_at}>{selfKeyDate(result.credited_at)}</time>。</p> : null}
    {reviewRequired ? null : !retry ? <form key="redemption-new" className="self-redemption-form" onSubmit={(event) => { void redeem(event) }} autoComplete="off">
      <Field label="管理员发放的兑换码"><input name="code" type="password" required maxLength={256} autoComplete="off" spellCheck={false} disabled={busy} onChange={() => setResult(null)} /></Field>
      <Field label="当前密码"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} disabled={busy} /></Field>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required disabled={busy} />我确认将此码兑换入本人直属钱包。</label>
      <div className="self-actions"><Button type="submit" disabled={busy}>{busy ? '正在确认…' : '确认兑换'}</Button></div>
    </form> : <form key="redemption-retry" className="self-redemption-form" onSubmit={(event) => { void redeem(event) }} autoComplete="off">
      <p>上次提交结果不确定；只用原操作编号和原兑换码显式重试，不会另起一笔。此待决信息仅短暂保留于本页面内存。</p>
      <Field label="当前密码（重试原操作）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} disabled={busy} /></Field>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required disabled={busy} />我确认重试原兑换操作。</label>
      <div className="self-actions"><Button type="submit" disabled={busy}>{busy ? '正在核对…' : '重试原兑换'}</Button><Button type="button" variant="secondary" disabled={busy} onClick={abandonPending}>放弃重试</Button></div>
    </form>}
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

const selfEntryKinds = new Set<SelfEntryKind>(['adjustment_credit', 'adjustment_debit', 'topup', 'redemption',
  'subscription_charge', 'subscription_credit', 'refund', 'usage_charge'])

function validSelfClassificationPage(raw: unknown, currency: string, previous?: SelfClassificationPage): raw is SelfClassificationPage {
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
    const kind = entry.entry_kind
    const delta = entry.delta_micro
    const credit = kind === 'adjustment_credit' || kind === 'topup' || kind === 'redemption' || kind === 'subscription_credit' || kind === 'refund'
    return Object.keys(entry).sort().join(',') === 'delta_micro,entry_kind,occurred_at' && typeof entry.occurred_at === 'string' &&
      entryTime.test(entry.occurred_at) && !/\.\d*0Z$/.test(entry.occurred_at) && Number.isFinite(Date.parse(entry.occurred_at)) &&
      typeof kind === 'string' && selfEntryKinds.has(kind as SelfEntryKind) && typeof delta === 'string' &&
      /^(?:-[1-9]\d*|[1-9]\d*)$/.test(delta) && credit !== delta.startsWith('-')
  })
}

function SelfClassificationPanel() {
  const [currency, setCurrency] = useState('')
  const [page, setPage] = useState<SelfClassificationPage | null>(null)
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
      const path = `/billing/entry-classifications?currency=${encodeURIComponent(requestedCurrency)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfClassificationPage(value, requestedCurrency, prior ?? undefined)) throw new Error('Invalid wallet classification response')
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

  return <section className="self-wallet-activity self-wallet-classification" aria-labelledby="self-wallet-classification-title">
    <h2 id="self-wallet-classification-title">钱包分录类型</h2>
    <p>只按需显示本人直属钱包近 31 天的原始账本类型和金额变动。类型不证明付款、退款、用量收费、账单或业务来源；不包含 Key 或资源子账户。</p>
    <form className="self-wallet-form" onSubmit={(event) => { event.preventDefault(); void readPage() }}>
      <Field label="类型视图币种（三位大写字母，如 USD）"><input name="classification_currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取分录类型'}</Button>
    </form>
    {error ? <p role="alert">钱包分录类型暂时无法读取，请检查币种或稍后重试。</p> : null}
    {page && !error ? <div className="self-wallet-activity-result" role="status">
      <p>查询窗口：<time dateTime={page.window_start}>{selfKeyDate(page.window_start)}</time> 至 <time dateTime={page.window_end}>{selfKeyDate(page.window_end)}</time>（不含结束时刻）</p>
      {!page.has_account ? <p><strong>{page.currency}</strong> 暂无员工钱包账户（未显示为零余额）。</p>
        : page.items.length === 0 ? <p><strong>{page.currency}</strong> 员工钱包在此窗口暂无分录；这不代表余额为零。</p>
          : <ol className="self-wallet-activity-list">{page.items.map((item, index) => <li key={`${item.occurred_at}:${item.delta_micro}:${index}`}>
            <span><time dateTime={item.occurred_at}>{selfKeyDate(item.occurred_at)}</time><code className="self-entry-kind">{item.entry_kind}</code></span>
            <strong className={item.delta_micro.startsWith('-') ? 'self-wallet-activity-negative' : 'self-wallet-activity-positive'}>{item.delta_micro.startsWith('-') ? '' : '+'}{item.delta_micro} micro</strong>
          </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多分录'}</Button> : null}
    </div> : null}
  </section>
}

function validSelfRedemptionCreditPage(raw: unknown, currency: string, previous?: SelfRedemptionCreditPage): raw is SelfRedemptionCreditPage {
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
  const creditTime = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/
  return value.items.every((item: unknown) => {
    if (!item || typeof item !== 'object' || Array.isArray(item)) return false
    const credit = item as Record<string, unknown>
    return Object.keys(credit).sort().join(',') === 'amount_micro,credited_at' && typeof credit.credited_at === 'string' &&
      creditTime.test(credit.credited_at) && !/\.\d*0Z$/.test(credit.credited_at) && Number.isFinite(Date.parse(credit.credited_at)) &&
      typeof credit.amount_micro === 'string' && /^[1-9]\d*$/.test(credit.amount_micro) &&
      (credit.amount_micro.length < 19 || credit.amount_micro.length === 19 && credit.amount_micro <= '9223372036854775807')
  })
}

function SelfRedemptionCreditHistoryPanel() {
  const [currency, setCurrency] = useState('')
  const [page, setPage] = useState<SelfRedemptionCreditPage | null>(null)
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
      const path = `/billing/redemption-credits?currency=${encodeURIComponent(requestedCurrency)}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfRedemptionCreditPage(value, requestedCurrency, prior ?? undefined)) throw new Error('Invalid self redemption credit history response')
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

  return <section className="self-wallet-activity" aria-labelledby="self-redemption-credit-history-title">
    <h2 id="self-redemption-credit-history-title">自助兑换入账历史</h2>
    <p>仅按需显示你本人用兑换码向直属钱包完成的本地入账，不含管理员代兑或其他钱包来源。这不是余额、外部付款、退款、订阅权益或完整账单。</p>
    <form className="self-wallet-form" onSubmit={(event) => { event.preventDefault(); void readPage() }}>
      <Field label="自助兑换入账币种（三位大写字母，如 USD）"><input name="redemption_history_currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取自助兑换入账'}</Button>
    </form>
    {error ? <p role="alert">自助兑换入账历史暂时无法读取，请检查币种或稍后重试。</p> : null}
    {page && !error ? <div className="self-wallet-activity-result" role="status">
      <p>查询窗口：<time dateTime={page.window_start}>{selfKeyDate(page.window_start)}</time> 至 <time dateTime={page.window_end}>{selfKeyDate(page.window_end)}</time>（不含结束时刻）</p>
      {!page.has_account ? <p><strong>{page.currency}</strong> 暂无员工钱包账户（未显示为零余额）。</p>
        : page.items.length === 0 ? <p><strong>{page.currency}</strong> 员工钱包在此窗口暂无已验证的本人自助兑换入账；这不代表余额为零。</p>
          : <ol className="self-wallet-activity-list">{page.items.map((item, index) => <li key={`${item.credited_at}:${item.amount_micro}:${index}`}>
            <time dateTime={item.credited_at}>{selfKeyDate(item.credited_at)}</time>
            <strong className="self-wallet-activity-positive">+{item.amount_micro} micro</strong>
          </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多自助兑换入账'}</Button> : null}
    </div> : null}
  </section>
}

function validSelfAdminAdjustmentPage(raw: unknown, currency: string, previous?: SelfWalletActivityPage): raw is SelfWalletActivityPage {
  if (!validSelfWalletActivityPage(raw, currency, previous)) return false
  return raw.items.every((item) => {
    const amount = BigInt(item.delta_micro)
    return amount >= -9223372036854775808n && amount <= 9223372036854775807n
  })
}

function SelfAdminAdjustmentHistoryPanel() {
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
      const path = `/billing/admin-adjustments?currency=${requestedCurrency}&limit=20${cursor ? `&cursor=${cursor}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfAdminAdjustmentPage(value, requestedCurrency, prior ?? undefined)) throw new Error('Invalid self admin adjustment response')
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

  return <section className="self-wallet-activity" aria-labelledby="self-admin-adjustment-title">
    <h2 id="self-admin-adjustment-title">管理员本地钱包调整记录</h2>
    <p>仅按需显示管理员对你本人直属钱包近 31 天的本地金额调整。正负号只表示钱包增减，不说明调整原因，也不是付款、退款或完整账单。</p>
    <form className="self-wallet-form" onSubmit={(event) => { event.preventDefault(); void readPage() }}>
      <Field label="本地调整币种（三位大写字母，如 USD）"><input name="admin_adjustment_currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取本地调整记录'}</Button>
    </form>
    {error ? <p role="alert">管理员本地钱包调整记录暂时无法读取，请检查币种或稍后重试。</p> : null}
    {page && !error ? <div className="self-wallet-activity-result" role="status">
      <p>查询窗口：<time dateTime={page.window_start}>{selfKeyDate(page.window_start)}</time> 至 <time dateTime={page.window_end}>{selfKeyDate(page.window_end)}</time>（不含结束时刻）</p>
      {!page.has_account ? <p><strong>{page.currency}</strong> 暂无员工钱包账户（未显示为零余额）。</p>
        : page.items.length === 0 ? <p><strong>{page.currency}</strong> 员工钱包在此窗口暂无已验证的管理员本地调整；这不代表余额为零。</p>
          : <ol className="self-wallet-activity-list">{page.items.map((item, index) => <li key={`${item.occurred_at}:${item.delta_micro}:${index}`}>
            <time dateTime={item.occurred_at}>{selfKeyDate(item.occurred_at)}</time>
            <strong className={item.delta_micro.startsWith('-') ? 'self-wallet-activity-negative' : 'self-wallet-activity-positive'}>{item.delta_micro.startsWith('-') ? '' : '+'}{item.delta_micro} micro</strong>
          </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多本地调整'}</Button> : null}
    </div> : null}
  </section>
}

function validSelfSubscriptionPage(raw: unknown, cancelEnabled: boolean, previous?: SelfSubscriptionPage): raw is SelfSubscriptionPage {
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
    if (Object.keys(item).sort().join(',') !== (cancelEnabled ? 'cancelled_at,interval,period_end_at,revision,started_at,status,subscription_id' : 'cancelled_at,interval,period_end_at,started_at,status,subscription_id') ||
      typeof item.subscription_id !== 'string' || item.subscription_id.length < 1 || item.subscription_id.length > 256 ||
      item.subscription_id.trim() !== item.subscription_id || (priorID !== '' && item.subscription_id >= priorID) ||
      (item.interval !== 'one_time' && item.interval !== 'monthly') ||
      (item.status !== 'active' && item.status !== 'cancelled' && item.status !== 'expired') ||
      typeof item.started_at !== 'string' || !date.test(item.started_at) || !Number.isFinite(Date.parse(item.started_at)) ||
      (item.interval === 'monthly' ? typeof item.period_end_at !== 'string' || !/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/.test(item.period_end_at) || !Number.isFinite(Date.parse(item.period_end_at)) : item.period_end_at !== null) ||
      (item.status === 'cancelled' ? typeof item.cancelled_at !== 'string' || !date.test(item.cancelled_at) || !Number.isFinite(Date.parse(item.cancelled_at)) : item.cancelled_at !== null) ||
      (item.interval === 'one_time' && item.status === 'expired') ||
      (cancelEnabled && (typeof item.revision !== 'number' || !Number.isSafeInteger(item.revision) || item.revision < 1))) return false
    priorID = item.subscription_id
  }
  return true
}

const selfSubscriptionStatus: Record<SelfSubscriptionItem['status'], string> = { active: '有效', cancelled: '已取消', expired: '已到期' }

function validSelfSubscriptionCancelResult(raw: unknown, operationID: string, subscriptionID: string, expectedRevision: number): raw is SelfSubscriptionCancelResult {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  return Object.keys(value).sort().join(',') === 'cancelled_at,operation_id,replay,revision,status,subscription_id' &&
    value.operation_id === operationID && value.subscription_id === subscriptionID && value.status === 'cancelled' &&
    typeof value.replay === 'boolean' && value.revision === expectedRevision + 1 &&
    typeof value.cancelled_at === 'string' && purchaseUTC.test(value.cancelled_at) && Number.isFinite(Date.parse(value.cancelled_at))
}

function newSelfSubscriptionCancelOperationID(): string {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return `self-cancel-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function validSelfPurchaseSnapshot(raw: unknown, id: string): raw is SelfPurchaseSnapshot {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  const utc = (time: unknown) => typeof time === 'string' && purchaseUTC.test(time) && Number.isFinite(Date.parse(time))
  return Object.keys(value).sort().join(',') === 'credit_micro,currency,interval,period_end_at,plan_id,plan_revision,price_micro,started_at,subscription_id' &&
    value.subscription_id === id && typeof value.plan_id === 'string' && value.plan_id.length > 0 && value.plan_id.length <= 256 &&
    value.plan_id.trim() === value.plan_id && typeof value.plan_revision === 'number' && Number.isSafeInteger(value.plan_revision) && value.plan_revision > 0 &&
    typeof value.currency === 'string' && /^[A-Z]{3}$/.test(value.currency) &&
    (value.interval === 'one_time' || value.interval === 'monthly') &&
    validPurchaseAmount(value.price_micro) && validPurchaseAmount(value.credit_micro) && utc(value.started_at) &&
    (value.interval === 'one_time' ? value.period_end_at === null :
      typeof value.period_end_at === 'string' && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{9}Z$/.test(value.period_end_at) &&
      Number.isFinite(Date.parse(value.period_end_at)) && Date.parse(value.period_end_at) > Date.parse(value.started_at as string))
}

function SelfPurchaseSnapshotPanel({ id, selected, onSelect }: { id: string; selected: boolean; onSelect: (id: string) => void }) {
  const [snapshot, setSnapshot] = useState<SelfPurchaseSnapshot | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])
  useEffect(() => {
    if (selected) return
    generation.current++
    pending.current?.abort()
    pending.current = null
    setSnapshot(null); setLoading(false); setError(false)
  }, [selected])

  async function readSnapshot() {
    onSelect(id)
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setSnapshot(null); setError(false); setLoading(true)
    try {
      const value = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(id)}/purchase-snapshot`, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfPurchaseSnapshot(value, id)) throw new Error('Invalid purchase snapshot response')
      setSnapshot(value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setSnapshot(null); setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-purchase-snapshot" aria-label={`订阅 ${id} 的本地钱包购买记录`}>
    <Button variant="secondary" disabled={selected && loading} onClick={() => { void readSnapshot() }}>{selected && loading ? '正在读取购买记录…' : '查看购买时记录'}</Button>
    {selected && error ? <p role="alert">这条购买时记录暂时无法读取，请稍后重试。</p> : null}
    {selected && snapshot ? <div className="self-purchase-snapshot-result" role="status">
      <p>仅显示这条订阅在本地钱包中记录的购买时数据；不是当前套餐价格、钱包余额、外部账单、真实付款或模型使用权益。</p>
      <dl>
        <div><dt>订阅 ID</dt><dd>{snapshot.subscription_id}</dd></div>
        <div><dt>套餐 ID</dt><dd>{snapshot.plan_id}</dd></div>
        <div><dt>套餐版本</dt><dd>{snapshot.plan_revision}</dd></div>
        <div><dt>币种</dt><dd>{snapshot.currency}</dd></div>
        <div><dt>周期</dt><dd>{snapshot.interval === 'monthly' ? '单月' : '一次性'}</dd></div>
        <div><dt>购买时扣费</dt><dd>{snapshot.price_micro} micro</dd></div>
        <div><dt>购买时授予额度</dt><dd>{snapshot.credit_micro} micro</dd></div>
        <div><dt>开始时间</dt><dd><time dateTime={snapshot.started_at}>{selfKeyDate(snapshot.started_at)}</time></dd></div>
        <div><dt>冻结期限结束</dt><dd>{snapshot.period_end_at ? <time dateTime={snapshot.period_end_at}>{selfKeyDate(snapshot.period_end_at)}</time> : '无固定结束时间'}</dd></div>
      </dl>
    </div> : null}
  </section>
}

function validSelfRenewalLinks(raw: unknown, id: string): raw is SelfRenewalLinks {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  const validNeighbor = (neighbor: unknown) => neighbor === null ||
    typeof neighbor === 'string' && neighbor.length > 0 && neighbor.length <= 256 && neighbor.trim() === neighbor && neighbor !== id
  return Object.keys(value).sort().join(',') === 'predecessor_id,subscription_id,successor_id' &&
    value.subscription_id === id && validNeighbor(value.predecessor_id) && validNeighbor(value.successor_id) &&
    (value.predecessor_id === null || value.successor_id === null || value.predecessor_id !== value.successor_id)
}

function SelfRenewalLinksPanel({ id, selected, onSelect }: { id: string; selected: boolean; onSelect: (id: string) => void }) {
  const [links, setLinks] = useState<SelfRenewalLinks | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState(false)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])
  useEffect(() => {
    if (selected) return
    generation.current++
    pending.current?.abort()
    pending.current = null
    setLinks(null); setLoading(false); setError(false)
  }, [selected])

  async function readLinks() {
    onSelect(id)
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setLinks(null); setError(false); setLoading(true)
    try {
      const raw = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(id)}/renewal-links`, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfRenewalLinks(raw, id)) throw new Error('Invalid renewal links response')
      setLinks(raw)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setLinks(null); setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-renewal-links" aria-label={`订阅 ${id} 的直接续购关联`}>
    <Button variant="secondary" disabled={selected && loading} onClick={() => { void readLinks() }}>{selected && loading ? '正在读取关联…' : '查看直接续购关联'}</Button>
    {selected && error ? <p role="alert">这条订阅的续购关联暂时无法读取，请重新读取订阅状态后重试。</p> : null}
    {selected && links ? <div className="self-renewal-links-result" role="status">
      <p>仅显示这条订阅在本地记录的直接前驱和直接后继；不是完整链、账单、真实付款或模型使用权益。</p>
      <dl>
        <div><dt>本条订阅</dt><dd>{links.subscription_id}</dd></div>
        <div><dt>直接前驱</dt><dd>{links.predecessor_id ?? '无直接前驱'}</dd></div>
        <div><dt>直接后继</dt><dd>{links.successor_id ?? '无直接后继'}</dd></div>
      </dl>
    </div> : null}
  </section>
}

const selfOneShotLabels: Record<SelfOneShotStatus['state'], string> = {
  none: '没有一次性续购预约', armed: '已预约，尚未执行', disarmed: '已撤销预约',
  succeeded: '本地续购已提交', failed: '预约执行失败', superseded: '已由手动续购取代', cancelled: '订阅取消后预约终结',
}
const selfOneShotReasons: Record<string, string> = {
  disarmed: '员工或管理员已撤销', manual_renewal: '已手动续购', predecessor_cancelled: '原订阅已取消',
  commercial_disabled: '商业执行已关闭', plan_unavailable: '原套餐不可用', insufficient_balance: '钱包余额不足',
  owner_unavailable: '钱包归属不可用', period_unrepresentable: '下一周期无法确定',
}

function validSelfOneShotStatus(raw: unknown, id: string): raw is SelfOneShotStatus {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  if (Object.keys(value).sort().join(',') !== 'due_at,reason,revision,state,subscription_id,terminal_at' || value.subscription_id !== id ||
    typeof value.state !== 'string' || !(value.state in selfOneShotLabels) || typeof value.revision !== 'number' || !Number.isSafeInteger(value.revision)) return false
  const utc = (time: unknown) => typeof time === 'string' && purchaseUTC.test(time) && Number.isFinite(Date.parse(time))
  if (value.state === 'none') return value.revision === 0 && value.due_at === null && value.reason === null && value.terminal_at === null
  if (!utc(value.due_at)) return false
  if (value.state === 'armed') return value.revision === 1 && value.reason === null && value.terminal_at === null
  if (value.revision !== 2 || !utc(value.terminal_at)) return false
  if (value.state === 'succeeded') return value.reason === null
  if (value.state === 'disarmed') return value.reason === 'disarmed'
  if (value.state === 'superseded') return value.reason === 'manual_renewal'
  if (value.state === 'cancelled') return value.reason === 'predecessor_cancelled'
  return value.state === 'failed' && typeof value.reason === 'string' &&
    ['commercial_disabled', 'plan_unavailable', 'insufficient_balance', 'owner_unavailable', 'period_unrepresentable'].includes(value.reason)
}

function validSelfOneShotDisarmResult(raw: unknown, id: string, operationID: string): raw is SelfOneShotDisarmResult {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  if (Object.keys(value).sort().join(',') !== 'due_at,operation_id,reason,replay,revision,state,subscription_id,terminal_at' ||
    value.operation_id !== operationID || typeof value.replay !== 'boolean' || value.state !== 'disarmed' || value.reason !== 'disarmed') return false
  const { operation_id: _operationID, replay: _replay, ...status } = value
  return validSelfOneShotStatus(status, id)
}

function SelfOneShotRenewalPanel({ id, csrf, selected, onSelect }: { id: string; csrf: string; selected: boolean; onSelect: (id: string) => void }) {
  const [status, setStatus] = useState<SelfOneShotStatus | null>(null)
  const [loading, setLoading] = useState<'read' | 'disarm' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState(false)
  const [retry, setRetry] = useState<{ revision: number; operationID: string } | null>(null)
  const [outcome, setOutcome] = useState<SelfOneShotDisarmResult | null>(null)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])
  useEffect(() => {
    if (selected) return
    generation.current++
    pending.current?.abort()
    pending.current = null
    setStatus(null); setLoading(null); setError(null); setConfirm(false); setRetry(null); setOutcome(null)
  }, [selected])

  async function readReservation(afterDisarm = false) {
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setStatus(null)
    setConfirm(false)
    // A status refresh cannot establish which operation won an uncertain
    // commit. Keep the original ID until explicit replay or abandonment.
    if (afterDisarm) setRetry(null)
    if (!afterDisarm) setOutcome(null)
    setError(null)
    setLoading('read')
    try {
      const value = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(id)}/one-shot-renewal`, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfOneShotStatus(value, id)) throw new Error('Invalid reservation response')
      setStatus(value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setStatus(null)
      setError(afterDisarm ? '撤销已确认，但最新预约状态暂时无法读取；请手动刷新。' : '预约状态暂时无法读取，请稍后重试。')
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(null)
    }
  }

  async function submitDisarm(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (loading !== null || (!confirm && !retry)) return
    const form = event.currentTarget
    let password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const revision = retry?.revision ?? status?.revision
    if (revision !== 1 || new TextEncoder().encode(password).length < 12 || new TextEncoder().encode(password).length > 72) {
      password = ''
      setStatus(null); setConfirm(false); setOutcome(null)
      setError(retry ? '当前密码须为 12–72 个 UTF-8 字节；原撤销操作仍待确认，请重新输入密码。' : '当前密码须为 12–72 个 UTF-8 字节；请重新读取预约状态。')
      return
    }
    let operationID = retry?.operationID ?? ''
    if (!retry) {
      try {
        const bytes = new Uint8Array(16)
        globalThis.crypto.getRandomValues(bytes)
        operationID = `self-one-shot-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
      } catch {
        password = ''
        setStatus(null); setConfirm(false); setOutcome(null)
        setError('无法生成撤销操作编号，请稍后重试。')
        return
      }
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setStatus(null); setConfirm(false); setRetry(null); setOutcome(null)
    setLoading('disarm'); setError(null)
    try {
      const value = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(id)}/one-shot-renewal/disarm`, {
        method: 'POST', body: JSON.stringify({ operation_id: operationID, expected_revision: revision, current_password: password }), signal: controller.signal,
      }, csrf)
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfOneShotDisarmResult(value, id, operationID)) throw new Error('Invalid disarm response')
      setOutcome(value)
      pending.current = null
      setLoading(null)
      void readReservation(true)
    } catch (caught) {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      const uncertain = !(caught instanceof ApiError) || caught.status === 503
      setError(uncertain ? '撤销结果未确认；请重新输入当前密码，显式重试同一操作。' : '撤销未完成；请重新读取预约状态。')
      if (uncertain) setRetry({ revision, operationID })
    } finally {
      password = ''
      operationID = ''
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(null)
    }
  }

  return <section className="self-one-shot" aria-label={`订阅 ${id} 的一次性续购预约`}>
    <Button variant="secondary" disabled={loading !== null} onClick={() => { onSelect(id); void readReservation() }}>{loading === 'read' ? '正在读取预约…' : '查看续购预约'}</Button>
    {error ? <p role="alert">{error}</p> : null}
    {outcome ? <p role="status">{outcome.replay ? '已确认原撤销' : '撤销已确认'}：订阅 {id} 的一次性预约已撤销，时间 <time dateTime={outcome.terminal_at}>{selfKeyDate(outcome.terminal_at)}</time>。</p> : null}
    {status ? <div className="self-one-shot-status"><p>{selfOneShotLabels[status.state]}</p>
      {status.due_at ? <p>冻结到期时刻：<time dateTime={status.due_at}>{selfKeyDate(status.due_at)}</time>；预约版本：{status.revision}。</p> : null}
      {status.terminal_at ? <p>终结时间：<time dateTime={status.terminal_at}>{selfKeyDate(status.terminal_at)}</time>；原因：{status.reason ? selfOneShotReasons[status.reason] : '本地续购已提交'}。</p> : null}
      {status.state === 'armed' && !retry ? <Button variant="secondary" disabled={loading !== null} onClick={() => { setConfirm(true); setError(null); setOutcome(null) }}>撤销这一笔预约</Button> : null}
    </div> : null}
    {confirm && status?.state === 'armed' ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitDisarm(event) }}>
      <h3>确认撤销一次性续购预约</h3>
      <p>订阅 ID：<strong>{id}</strong>；预约存储版本：<strong>{status.revision}</strong>；冻结到期：<time dateTime={status.due_at ?? ''}>{selfKeyDate(status.due_at ?? '')}</time>。</p>
      <p>仅撤销这一笔一次性预约；到期扣费可能失败；不取消当前订阅、不退款；同一前驱不可再次预约。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我已阅读并确认只撤销这一笔预约。</label>
      <Field label="当前密码（确认撤销预约）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>确认撤销预约</Button><Button type="button" variant="secondary" onClick={() => setConfirm(false)}>返回预约状态</Button></div>
    </form> : null}
    {retry ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitDisarm(event) }}>
      <h3>上次撤销结果未确认</h3><p>仅对订阅 {id} 的原预约版本 {retry.revision} 使用原操作编号重试；不会自动重试。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认显式重试原撤销操作。</label>
      <Field label="当前密码（重试原撤销）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>确认重试撤销</Button><Button type="button" variant="secondary" onClick={() => { setRetry(null); setError(null) }}>放弃重试</Button></div>
    </form> : null}
  </section>
}

function validSelfMonthlyRenewalQuote(raw: unknown, predecessor: SelfSubscriptionItem): raw is SelfMonthlyRenewalQuote {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  return Object.keys(value).sort().join(',') === 'credit_micro,currency,expires_at,interval,plan_id,predecessor_id,predecessor_period_end_at,price_micro,quote_token,revision' &&
    value.predecessor_id === predecessor.subscription_id && value.predecessor_period_end_at === predecessor.period_end_at &&
    typeof value.quote_token === 'string' && value.quote_token.length > 0 && value.quote_token.length <= 2048 &&
    typeof value.plan_id === 'string' && value.plan_id.length > 0 && value.plan_id.length <= 256 && value.plan_id.trim() === value.plan_id &&
    typeof value.revision === 'number' && Number.isSafeInteger(value.revision) && value.revision > 0 &&
    typeof value.currency === 'string' && /^[A-Z]{3}$/.test(value.currency) && value.interval === 'monthly' &&
    validPurchaseAmount(value.price_micro) && validPurchaseAmount(value.credit_micro) &&
    typeof value.expires_at === 'string' && purchaseUTC.test(value.expires_at) && Number.isFinite(Date.parse(value.expires_at))
}

function validSelfMonthlyRenewalResult(raw: unknown, operationID: string, predecessorID: string): raw is SelfMonthlyRenewalResult {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  const utc = (time: unknown) => typeof time === 'string' && purchaseUTC.test(time) && Number.isFinite(Date.parse(time))
  return Object.keys(value).sort().join(',') === 'credit_micro,currency,interval,operation_id,period_end_at,plan_id,predecessor_id,price_micro,replay,revision,started_at,subscription_id' &&
    value.operation_id === operationID && value.predecessor_id === predecessorID &&
    typeof value.subscription_id === 'string' && value.subscription_id.length > 0 && value.subscription_id.length <= 256 &&
    typeof value.replay === 'boolean' && typeof value.plan_id === 'string' && value.plan_id.length > 0 && value.plan_id.length <= 256 &&
    typeof value.revision === 'number' && Number.isSafeInteger(value.revision) && value.revision > 0 &&
    typeof value.currency === 'string' && /^[A-Z]{3}$/.test(value.currency) && value.interval === 'monthly' &&
    validPurchaseAmount(value.price_micro) && validPurchaseAmount(value.credit_micro) &&
    utc(value.started_at) && utc(value.period_end_at) && Date.parse(value.period_end_at as string) > Date.parse(value.started_at as string)
}

function newSelfMonthlyRenewalOperationID(): string {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return `self-renew-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function SelfMonthlyRenewalPanel({ item, csrf, selected, onSelect, onSuccess }: {
  item: SelfSubscriptionItem; csrf: string; selected: boolean; onSelect: (id: string) => void; onSuccess: (result: SelfMonthlyRenewalResult) => void
}) {
  const [quote, setQuote] = useState<SelfMonthlyRenewalQuote | null>(null)
  const [retry, setRetry] = useState<{ operationID: string; token: string } | null>(null)
  const [loading, setLoading] = useState<'quote' | 'renew' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort(); pending.current = null }, [])
  useEffect(() => {
    if (selected) return
    generation.current++
    pending.current?.abort()
    pending.current = null
    setQuote(null); setRetry(null); setLoading(null); setError(null)
  }, [selected])

  function clear() {
    generation.current++
    pending.current?.abort()
    pending.current = null
    setQuote(null); setRetry(null); setLoading(null); setError(null)
  }

  function begin(kind: 'quote' | 'renew') {
    generation.current++
    pending.current?.abort()
    const controller = new AbortController()
    pending.current = controller
    setLoading(kind)
    setError(null)
    return { controller, requestGeneration: generation.current }
  }

  function current(controller: AbortController, requestGeneration: number) {
    return !controller.signal.aborted && generation.current === requestGeneration
  }

  function end(controller: AbortController, requestGeneration: number) {
    if (pending.current === controller) pending.current = null
    if (current(controller, requestGeneration)) setLoading(null)
  }

  async function requestQuote() {
    onSelect(item.subscription_id)
    const { controller, requestGeneration } = begin('quote')
    setQuote(null); setRetry(null)
    try {
      const raw = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(item.subscription_id)}/renewal-quotes`, { method: 'POST', signal: controller.signal }, csrf)
      if (!current(controller, requestGeneration)) return
      if (!validSelfMonthlyRenewalQuote(raw, item)) throw new Error('Invalid renewal quote response')
      setQuote(raw)
    } catch {
      if (!current(controller, requestGeneration)) return
      setQuote(null); setRetry(null)
      setError('续购报价不可用；原订阅、套餐或本人钱包可能已变化，请重新读取订阅状态。')
    } finally { end(controller, requestGeneration) }
  }

  async function submitRenewal(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (loading || (!quote && !retry)) return
    const form = event.currentTarget
    let current_password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const size = new TextEncoder().encode(current_password).length
    if (size < 12 || size > 72) {
      current_password = ''
      clear()
      setError('当前密码须为 12–72 个 UTF-8 字节；请重新取得报价。')
      return
    }
    let operationID = retry?.operationID ?? ''
    let token = retry?.token ?? quote?.quote_token ?? ''
    if (!retry) {
      try { operationID = newSelfMonthlyRenewalOperationID() } catch {
        current_password = ''
        clear()
        setError('无法生成续购操作编号，请稍后重新报价。')
        return
      }
    }
    const { controller, requestGeneration } = begin('renew')
    setQuote(null); setRetry(null)
    try {
      const raw = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(item.subscription_id)}/renew`, {
        method: 'POST', body: JSON.stringify({ operation_id: operationID, quote_token: token, current_password }), signal: controller.signal,
      }, csrf)
      if (!current(controller, requestGeneration)) return
      if (!validSelfMonthlyRenewalResult(raw, operationID, item.subscription_id)) throw new Error('Invalid renewal result response')
      onSuccess(raw)
    } catch (caught) {
      if (!current(controller, requestGeneration)) return
      const uncertain = !(caught instanceof ApiError) || caught.status === 503
      setError(uncertain ? '结果未确认；可重新输入当前密码，显式重试原操作。' : '续购未完成；请重新读取订阅状态并取得新报价。')
      if (uncertain) setRetry({ operationID, token })
    } finally {
      current_password = ''; token = ''; operationID = ''
      end(controller, requestGeneration)
    }
  }

  return <section className="self-monthly-renewal" aria-label={`订阅 ${item.subscription_id} 的月度续购`}>
    <Button variant="secondary" disabled={loading !== null} onClick={() => { void requestQuote() }}>{loading === 'quote' ? '正在读取续购报价…' : '获取此订阅的续购报价'}</Button>
    {selected && error ? <p role="alert">{error}</p> : null}
    {selected && quote ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitRenewal(event) }}>
      <h3>确认本人月度续购</h3>
      <p>原订阅 <strong>{quote.predecessor_id}</strong> 的冻结周期结束于 <time dateTime={quote.predecessor_period_end_at}>{selfKeyDate(quote.predecessor_period_end_at)}</time>。</p>
      <p>当前套餐 <strong>{quote.plan_id}</strong>（版本 {quote.revision}）：从本人现有 {quote.currency} 钱包扣除 <strong>{quote.price_micro} micro</strong>，记入 <strong>{quote.credit_micro} micro</strong> 额度。</p>
      <p>这是当前钱包价格的短期快照，尚未扣款；至 <time dateTime={quote.expires_at}>{selfKeyDate(quote.expires_at)}</time> 有效，最终以提交时再次核验为准。新周期从成交事务捕获的服务器 UTC 开始。</p>
      <p>本操作不是自动续费、真实付款或模型权益承诺。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认从本人现有钱包扣款并即时续购一个月。</label>
      <Field label="当前密码（确认续购）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'renew' ? '正在确认…' : '确认续购一个月'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={clear}>取消报价</Button></div>
    </form> : null}
    {selected && retry ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitRenewal(event) }}>
      <h3>上次续购结果未确认</h3>
      <p>仅用原操作编号和原报价令牌重试订阅 {item.subscription_id}；不会自动重试，也不会重新购买另一周期。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认显式重试原续购操作。</label>
      <Field label="当前密码（重试原操作）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'renew' ? '正在确认…' : '确认重试续购'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={clear}>放弃重试</Button></div>
    </form> : null}
  </section>
}

function SelfSubscriptionStatusPanel({ csrf, cancelEnabled, oneShotEnabled, purchaseSnapshotEnabled, renewalLinksEnabled, renewalEnabled }: { csrf: string; cancelEnabled: boolean; oneShotEnabled: boolean; purchaseSnapshotEnabled: boolean; renewalLinksEnabled: boolean; renewalEnabled: boolean }) {
  const [page, setPage] = useState<SelfSubscriptionPage | null>(null)
  const [loading, setLoading] = useState<'read' | 'cancel' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [confirm, setConfirm] = useState<SelfSubscriptionItem | null>(null)
  const [retry, setRetry] = useState<{ id: string; revision: number; operationID: string } | null>(null)
  const [outcome, setOutcome] = useState<SelfSubscriptionCancelResult | null>(null)
  const [oneShotTarget, setOneShotTarget] = useState<string | null>(null)
  const [purchaseSnapshotTarget, setPurchaseSnapshotTarget] = useState<string | null>(null)
  const [renewalLinksTarget, setRenewalLinksTarget] = useState<string | null>(null)
  const [renewalTarget, setRenewalTarget] = useState<string | null>(null)
  const [renewalOutcome, setRenewalOutcome] = useState<SelfMonthlyRenewalResult | null>(null)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort() }, [])

  async function readPage(cursor?: string, afterMutation = false) {
    const prior = cursor ? page : null
    if (cursor && (!prior || prior.next_cursor !== cursor)) {
      setPage(null)
      setError('订阅状态暂时无法读取，请稍后重试。')
      return
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setConfirm(null)
    setRetry(null)
    setPurchaseSnapshotTarget(null)
    setRenewalLinksTarget(null)
    setOneShotTarget(null)
    setRenewalTarget(null)
    if (!afterMutation) { setOutcome(null); setRenewalOutcome(null) }
    if (!cursor) setPage(null)
    setError(null)
    setLoading('read')
    try {
      const path = `/billing/subscriptions?limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfSubscriptionPage(value, cancelEnabled, prior ?? undefined)) throw new Error('Invalid subscription status response')
      setPage(prior ? { items: [...prior.items, ...value.items], next_cursor: value.next_cursor } : value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setPage(null)
      setError(afterMutation ? '操作已确认，但最新列表暂时无法读取；请手动刷新。' : '订阅状态暂时无法读取，请稍后重试。')
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(null)
    }
  }

  async function submitCancel(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!cancelEnabled || loading !== null || (!confirm && !retry)) return
    const form = event.currentTarget
    let password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const revision = retry?.revision ?? confirm?.revision
    const id = retry?.id ?? confirm?.subscription_id
    if (!id || !revision || !Number.isSafeInteger(revision) || new TextEncoder().encode(password).length < 12 || new TextEncoder().encode(password).length > 72) {
      password = ''
      setConfirm(null)
      setRetry(null)
      setPage(null)
      setError('当前密码须为 12–72 个 UTF-8 字节；请重新读取订阅状态。')
      return
    }
    let operationID = retry?.operationID ?? ''
    if (!retry) {
      try { operationID = newSelfSubscriptionCancelOperationID() } catch {
        password = ''
        setConfirm(null)
        setPage(null)
        setError('无法生成取消操作编号，请稍后重试。')
        return
      }
    }
    generation.current++
    pending.current?.abort()
    const requestGeneration = generation.current
    const controller = new AbortController()
    pending.current = controller
    setLoading('cancel')
    setPurchaseSnapshotTarget(null)
    setRenewalLinksTarget(null)
    setRenewalTarget(null)
    setRenewalOutcome(null)
    setError(null)
    setConfirm(null)
    setRetry(null)
    setPage(null)
    setOutcome(null)
    try {
      const raw = await selfRequest<unknown>(`/billing/subscriptions/${encodeURIComponent(id)}/cancel`, {
        method: 'POST', body: JSON.stringify({ operation_id: operationID, expected_revision: revision, current_password: password }), signal: controller.signal,
      }, csrf)
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfSubscriptionCancelResult(raw, operationID, id, revision)) throw new Error('Invalid cancellation response')
      setOutcome(raw)
      pending.current = null
      setLoading(null)
      void readPage(undefined, true)
    } catch (caught) {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      const uncertain = !(caught instanceof ApiError) || caught.status === 503
      setError(uncertain ? '取消结果未确认；请重新输入当前密码，显式重试同一操作。' : '取消未完成；请重新读取本人订阅状态。')
      if (uncertain) setRetry({ id, revision, operationID })
    } finally {
      password = ''
      operationID = ''
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(null)
    }
  }

  return <section className="self-subscriptions" aria-labelledby="self-subscriptions-title">
    <h2 id="self-subscriptions-title">我的订阅状态</h2>
    <p>仅按需查看你本人直接名下既有订阅的期限与状态，不含 Key 或资源子账户。列表按订阅 ID 稳定排列，不代表时间先后；到期不等于模型权益失效。续购仅对已到期单月订阅另行确认。</p>
    <Button variant="secondary" disabled={loading !== null} onClick={() => { void readPage() }}>{loading === 'read' ? '正在读取…' : '读取我的订阅状态'}</Button>
    {error ? <p role="alert">{error}</p> : null}
    {outcome ? <p className="self-subscriptions-success" role="status">{outcome.replay ? '已确认原取消' : '取消已确认'}：订阅 {outcome.subscription_id}，版本 {outcome.revision}；取消时间 <time dateTime={outcome.cancelled_at}>{selfKeyDate(outcome.cancelled_at)}</time>。</p> : null}
    {renewalOutcome ? <p className="self-subscriptions-success" role="status">{renewalOutcome.replay ? '已确认原续购' : '续购已确认'}：原订阅 {renewalOutcome.predecessor_id}，新订阅 {renewalOutcome.subscription_id}；从本人 {renewalOutcome.currency} 钱包扣除 {renewalOutcome.price_micro} micro、记入 {renewalOutcome.credit_micro} micro 额度。新周期始于 <time dateTime={renewalOutcome.started_at}>{selfKeyDate(renewalOutcome.started_at)}</time>，结束于 <time dateTime={renewalOutcome.period_end_at}>{selfKeyDate(renewalOutcome.period_end_at)}</time>。这不是外部付款或模型权益凭证。</p> : null}
    {page && !error ? <div className="self-subscriptions-result" role="status">
      {page.items.length === 0 ? <p>目前没有可显示的本人订阅记录。</p> : <ol className="self-subscriptions-list">{page.items.map((item) => <li key={item.subscription_id}>
        <div className="self-subscriptions-heading"><strong>{item.subscription_id}</strong><span className={`self-subscriptions-status self-subscriptions-status--${item.status}`}>{selfSubscriptionStatus[item.status]}</span></div>
        <dl><div><dt>周期</dt><dd>{item.interval === 'monthly' ? '单月' : '一次性'}</dd></div><div><dt>开始</dt><dd><time dateTime={item.started_at}>{selfKeyDate(item.started_at)}</time></dd></div>
          <div><dt>期限结束</dt><dd>{item.period_end_at ? <time dateTime={item.period_end_at}>{selfKeyDate(item.period_end_at)}</time> : '无固定结束时间'}</dd></div>
          <div><dt>取消时间</dt><dd>{item.cancelled_at ? <time dateTime={item.cancelled_at}>{selfKeyDate(item.cancelled_at)}</time> : '未取消'}</dd></div>
          {cancelEnabled ? <div><dt>存储版本</dt><dd>{item.revision}</dd></div> : null}</dl>
        {cancelEnabled && item.status === 'active' && item.revision ? <Button variant="secondary" disabled={loading !== null} onClick={() => { setConfirm(item); setRetry(null); setOutcome(null); setError(null) }}>取消此订阅</Button> : null}
        {purchaseSnapshotEnabled ? <SelfPurchaseSnapshotPanel id={item.subscription_id} selected={purchaseSnapshotTarget === item.subscription_id} onSelect={(id) => { setOneShotTarget(null); setRenewalTarget(null); setRenewalLinksTarget(null); setPurchaseSnapshotTarget(id) }} /> : null}
        {renewalLinksEnabled ? <SelfRenewalLinksPanel id={item.subscription_id} selected={renewalLinksTarget === item.subscription_id} onSelect={(id) => { setPurchaseSnapshotTarget(null); setOneShotTarget(null); setRenewalTarget(null); setRenewalLinksTarget(id) }} /> : null}
        {oneShotEnabled && item.interval === 'monthly' ? <SelfOneShotRenewalPanel key={`${item.subscription_id}:${item.revision ?? 'unknown'}`} id={item.subscription_id} csrf={csrf} selected={oneShotTarget === item.subscription_id} onSelect={(id) => { setPurchaseSnapshotTarget(null); setRenewalTarget(null); setRenewalLinksTarget(null); setOneShotTarget(id) }} /> : null}
        {renewalEnabled && item.interval === 'monthly' && item.status === 'expired' ? <SelfMonthlyRenewalPanel item={item} csrf={csrf} selected={renewalTarget === item.subscription_id} onSelect={(id) => { setPurchaseSnapshotTarget(null); setOneShotTarget(null); setRenewalLinksTarget(null); setRenewalTarget(id); setRenewalOutcome(null) }} onSuccess={(result) => { setRenewalOutcome(result); void readPage(undefined, true) }} /> : null}
      </li>)}</ol>}
      {page.next_cursor ? <Button variant="secondary" disabled={loading !== null} onClick={() => { void readPage(page.next_cursor ?? undefined) }}>{loading === 'read' ? '正在读取…' : '加载更多订阅'}</Button> : null}
    </div> : null}
    {confirm ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitCancel(event) }}>
      <h3>确认取消本人订阅</h3>
      <p>订阅 ID：<strong>{confirm.subscription_id}</strong>；当前存储版本：<strong>{confirm.revision}</strong>。</p>
      <p>这只终结本地订阅；不退款、不撤回已授额度，也不代表模型权益或外部支付取消。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我已阅读并确认取消这条订阅。</label>
      <Field label="当前密码（确认取消）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'cancel' ? '正在确认…' : '确认取消订阅'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={() => setConfirm(null)}>返回列表</Button></div>
    </form> : null}
    {retry ? <form className="self-subscriptions-confirm" onSubmit={(event) => { void submitCancel(event) }}>
      <h3>上次取消结果未确认</h3>
      <p>仅对订阅 {retry.id} 的原版本 {retry.revision} 使用原操作编号重试；不会自动重试。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认显式重试原取消操作。</label>
      <Field label="当前密码（重试原操作）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'cancel' ? '正在确认…' : '确认重试取消'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={() => { setRetry(null); setError(null) }}>放弃重试</Button></div>
    </form> : null}
  </section>
}

function planIDFollowsUTF8(id: string, prior: string): boolean {
  const current = new TextEncoder().encode(id)
  const previous = new TextEncoder().encode(prior)
  for (let index = 0; index < Math.min(current.length, previous.length); index++) {
    if (current[index] !== previous[index]) return current[index] > previous[index]
  }
  return current.length > previous.length
}

function validSelfPlanCatalogPage(raw: unknown, currency: string, previous?: SelfPlanCatalogPage): raw is SelfPlanCatalogPage {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const page = raw as Record<string, unknown>
  if (Object.keys(page).sort().join(',') !== 'available,currency,items,next_cursor' || page.currency !== currency ||
    typeof page.available !== 'boolean' || !Array.isArray(page.items) || page.items.length > 20 ||
    (page.next_cursor !== null && (typeof page.next_cursor !== 'string' || page.next_cursor.length < 1 || page.next_cursor.length > 1024)) ||
    (!page.available && (page.items.length !== 0 || page.next_cursor !== null)) ||
    (page.next_cursor !== null && page.items.length === 0)) return false
  const positiveMicro = /^[1-9][0-9]*$/
  let priorID = previous?.items.at(-1)?.plan_id ?? ''
  for (const rawItem of page.items) {
    if (!rawItem || typeof rawItem !== 'object' || Array.isArray(rawItem)) return false
    const item = rawItem as Record<string, unknown>
    if (Object.keys(item).sort().join(',') !== 'credit_micro,interval,name,plan_id,price_micro,revision' ||
      typeof item.plan_id !== 'string' || item.plan_id.length < 1 || item.plan_id.length > 256 ||
      item.plan_id.trim() !== item.plan_id || (priorID !== '' && !planIDFollowsUTF8(item.plan_id, priorID)) ||
      typeof item.name !== 'string' || item.name.length < 1 || item.name.length > 128 || item.name.trim() !== item.name ||
      (item.interval !== 'one_time' && item.interval !== 'monthly') ||
      typeof item.price_micro !== 'string' || item.price_micro.length > 19 || !positiveMicro.test(item.price_micro) || BigInt(item.price_micro) > 9223372036854775807n ||
      typeof item.credit_micro !== 'string' || item.credit_micro.length > 19 || !positiveMicro.test(item.credit_micro) || BigInt(item.credit_micro) > 9223372036854775807n ||
      typeof item.revision !== 'number' || !Number.isSafeInteger(item.revision) || item.revision < 1) return false
    priorID = item.plan_id
  }
  return true
}

function SelfPlanCatalogPanel() {
  const [currency, setCurrency] = useState('')
  const [page, setPage] = useState<SelfPlanCatalogPage | null>(null)
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
    setLoading(false)
    setError(false)
  }

  async function readPage(requestedCurrency: string, cursor?: string) {
    const prior = cursor ? page : null
    if (!/^[A-Z]{3}$/.test(requestedCurrency) || (cursor && (!prior || prior.currency !== requestedCurrency || prior.next_cursor !== cursor))) {
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
      const path = `/billing/plans?currency=${requestedCurrency}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const value = await selfRequest<unknown>(path, { signal: controller.signal })
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      if (!validSelfPlanCatalogPage(value, requestedCurrency, prior ?? undefined)) throw new Error('Invalid plan catalog response')
      setPage(prior && value.available ? { ...value, items: [...prior.items, ...value.items] } : value)
    } catch {
      if (controller.signal.aborted || generation.current !== requestGeneration) return
      setPage(null)
      setError(true)
    } finally {
      if (pending.current === controller) pending.current = null
      if (!controller.signal.aborted && generation.current === requestGeneration) setLoading(false)
    }
  }

  return <section className="self-plan-catalog" aria-labelledby="self-plan-catalog-title">
    <h2 id="self-plan-catalog-title">当前可用套餐目录</h2>
    <p>仅展示查询时该币种已启用套餐的当前信息，不是权益或持久报价。此目录只读，不会在这里购买、取消或续购；本人能否执行这些操作，以各自独立授权入口及服务端资格校验为准。</p>
    <form className="self-plan-catalog-form" onSubmit={(event: FormEvent<HTMLFormElement>) => { event.preventDefault(); void readPage(currency) }}>
      <Field label="套餐币种（三位大写字母，如 USD）"><input name="plan_currency" value={currency} onChange={(event) => changeCurrency(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading}>{loading ? '正在读取…' : '读取当前套餐'}</Button>
    </form>
    {error ? <p role="alert">当前套餐目录暂时无法读取，请检查币种或稍后重试。</p> : null}
    {page && !error ? <div className="self-plan-catalog-result" role="status">
      {!page.available ? <p>当前套餐目录不可用；未展示旧报价。</p>
        : page.items.length === 0 ? <p>{page.currency} 暂无已启用套餐。</p>
          : <ol className="self-plan-catalog-list">{page.items.map((item) => <li key={item.plan_id}>
            <div className="self-plan-catalog-heading"><strong>{item.name}</strong><span>{item.interval === 'monthly' ? '单月' : '一次性'}</span></div>
            <dl><div><dt>套餐 ID</dt><dd>{item.plan_id}</dd></div><div><dt>当前版本</dt><dd>{item.revision}</dd></div>
              <div><dt>标价</dt><dd>{item.price_micro} micro {page.currency}</dd></div><div><dt>授予额度</dt><dd>{item.credit_micro} micro</dd></div></dl>
          </li>)}</ol>}
      {page.available && page.next_cursor ? <Button variant="secondary" disabled={loading} onClick={() => { void readPage(page.currency, page.next_cursor ?? undefined) }}>{loading ? '正在读取…' : '加载更多套餐'}</Button> : null}
    </div> : null}
  </section>
}

const purchaseMicro = /^[1-9][0-9]*$/
const purchaseUTC = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/

function validPurchaseAmount(value: unknown): value is string {
  return typeof value === 'string' && value.length <= 19 && purchaseMicro.test(value) && BigInt(value) <= 9223372036854775807n
}

function validSelfPlanPurchaseQuote(raw: unknown, planID: string, currency: string): raw is SelfPlanPurchaseQuote {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  return Object.keys(value).sort().join(',') === 'credit_micro,currency,expires_at,interval,plan_id,price_micro,quote_token,revision' &&
    value.plan_id === planID && value.currency === currency && value.interval === 'one_time' &&
    typeof value.quote_token === 'string' && value.quote_token.length > 0 && value.quote_token.length <= 2048 &&
    typeof value.revision === 'number' && Number.isSafeInteger(value.revision) && value.revision > 0 &&
    validPurchaseAmount(value.price_micro) && validPurchaseAmount(value.credit_micro) &&
    typeof value.expires_at === 'string' && purchaseUTC.test(value.expires_at) && Number.isFinite(Date.parse(value.expires_at))
}

function validSelfPlanPurchaseResult(raw: unknown, operationID: string): raw is SelfPlanPurchaseResult {
  if (!raw || typeof raw !== 'object' || Array.isArray(raw)) return false
  const value = raw as Record<string, unknown>
  return Object.keys(value).sort().join(',') === 'credit_micro,currency,interval,operation_id,plan_id,price_micro,replay,revision,subscription_id' &&
    value.operation_id === operationID && typeof value.subscription_id === 'string' && value.subscription_id.length > 0 && value.subscription_id.length <= 256 &&
    typeof value.replay === 'boolean' && typeof value.plan_id === 'string' && value.plan_id.length > 0 && value.plan_id.length <= 256 &&
    typeof value.currency === 'string' && /^[A-Z]{3}$/.test(value.currency) && value.interval === 'one_time' &&
    typeof value.revision === 'number' && Number.isSafeInteger(value.revision) && value.revision > 0 &&
    validPurchaseAmount(value.price_micro) && validPurchaseAmount(value.credit_micro)
}

function newSelfPurchaseOperationID(): string {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  return `self-buy-${Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')}`
}

function SelfPlanPurchasePanel({ csrf }: { csrf: string }) {
  const [currency, setCurrency] = useState('')
  const [page, setPage] = useState<SelfPlanCatalogPage | null>(null)
  const [balance, setBalance] = useState<SelfWalletBalance | null>(null)
  const [quote, setQuote] = useState<SelfPlanPurchaseQuote | null>(null)
  const [result, setResult] = useState<SelfPlanPurchaseResult | null>(null)
  const [retry, setRetry] = useState<{ operationID: string; token: string } | null>(null)
  const [loading, setLoading] = useState<'catalog' | 'balance' | 'quote' | 'purchase' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const pending = useRef<AbortController | null>(null)
  const generation = useRef(0)

  useEffect(() => () => { generation.current++; pending.current?.abort(); pending.current = null }, [])

  function clearCurrent(nextCurrency = currency) {
    generation.current++
    pending.current?.abort()
    pending.current = null
    setCurrency(nextCurrency)
    setPage(null)
    setBalance(null)
    setQuote(null)
    setResult(null)
    setRetry(null)
    setError(null)
    setLoading(null)
  }

  function beginRequest(kind: 'catalog' | 'balance' | 'quote' | 'purchase') {
    generation.current++
    pending.current?.abort()
    const controller = new AbortController()
    pending.current = controller
    setLoading(kind)
    setError(null)
    return { controller, requestGeneration: generation.current }
  }

  function currentRequest(controller: AbortController, requestGeneration: number) {
    return !controller.signal.aborted && generation.current === requestGeneration
  }

  function endRequest(controller: AbortController, requestGeneration: number) {
    if (pending.current === controller) pending.current = null
    if (currentRequest(controller, requestGeneration)) setLoading(null)
  }

  function failRequest(message: string) {
    setPage(null)
    setBalance(null)
    setQuote(null)
    setResult(null)
    setRetry(null)
    setError(message)
  }

  async function readCatalog(cursor?: string) {
    const requestedCurrency = currency
    const prior = cursor ? page : null
    if (!/^[A-Z]{3}$/.test(requestedCurrency) || (cursor && (!prior || prior.next_cursor !== cursor))) {
      failRequest('请输入三位大写币种后再读取套餐。')
      return
    }
    const { controller, requestGeneration } = beginRequest('catalog')
    setQuote(null)
    setBalance(null)
    setResult(null)
    setRetry(null)
    if (!cursor) setPage(null)
    try {
      const path = `/billing/plans?currency=${requestedCurrency}&limit=20${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`
      const raw = await selfRequest<unknown>(path, { signal: controller.signal })
      if (!currentRequest(controller, requestGeneration)) return
      if (!validSelfPlanCatalogPage(raw, requestedCurrency, prior ?? undefined)) throw new Error('Invalid catalog response')
      setPage(prior && raw.available ? { ...raw, items: [...prior.items, ...raw.items] } : raw)
    } catch {
      if (currentRequest(controller, requestGeneration)) failRequest('当前套餐暂时无法读取，请检查币种或稍后重试。')
    } finally { endRequest(controller, requestGeneration) }
  }

  async function readBalance() {
    if (!page?.available || page.currency !== currency) return
    const requestedCurrency = currency
    const { controller, requestGeneration } = beginRequest('balance')
    setBalance(null)
    setQuote(null)
    setResult(null)
    setRetry(null)
    try {
      const raw = await selfRequest<unknown>(`/billing/balance?currency=${requestedCurrency}`, { signal: controller.signal })
      if (!currentRequest(controller, requestGeneration)) return
      if (!raw || typeof raw !== 'object' || Array.isArray(raw)) throw new Error('Invalid balance response')
      const value = raw as Record<string, unknown>
      if (Object.keys(value).sort().join(',') !== 'amount_micro,currency,has_account' || value.currency !== requestedCurrency || typeof value.has_account !== 'boolean' ||
        (value.has_account ? typeof value.amount_micro !== 'string' || !/^(?:0|-[1-9][0-9]*|[1-9][0-9]*)$/.test(value.amount_micro) : value.amount_micro !== null)) throw new Error('Invalid balance response')
      setBalance(value as SelfWalletBalance)
    } catch {
      if (currentRequest(controller, requestGeneration)) failRequest('本人钱包暂时无法读取，请重新读取套餐后再试。')
    } finally { endRequest(controller, requestGeneration) }
  }

  async function requestQuote(planID: string) {
    const item = page?.items.find((candidate) => candidate.plan_id === planID)
    if (!page?.available || page.currency !== currency || !balance?.has_account || balance.currency !== currency || item?.interval !== 'one_time') return
    const { controller, requestGeneration } = beginRequest('quote')
    setQuote(null)
    setResult(null)
    setRetry(null)
    try {
      const raw = await selfRequest<unknown>('/billing/plan-purchase-quotes', { method: 'POST', body: JSON.stringify({ plan_id: planID }), signal: controller.signal }, csrf)
      if (!currentRequest(controller, requestGeneration)) return
      if (!validSelfPlanPurchaseQuote(raw, planID, currency)) throw new Error('Invalid quote response')
      setQuote(raw)
    } catch {
      if (currentRequest(controller, requestGeneration)) failRequest('报价未能确认；套餐或钱包可能已变化，请重新读取。')
    } finally { endRequest(controller, requestGeneration) }
  }

  async function submitPurchase(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (loading || (!quote && !retry)) return
    const form = event.currentTarget
    let current_password = String(new FormData(form).get('current_password') ?? '')
    form.reset()
    const size = new TextEncoder().encode(current_password).length
    if (size < 12 || size > 72) {
      current_password = ''
      failRequest('当前密码须为 12–72 个 UTF-8 字节。请重新读取套餐。')
      return
    }
    let operationID = retry?.operationID ?? ''
    let token = retry?.token ?? quote?.quote_token ?? ''
    if (!retry) {
      try { operationID = newSelfPurchaseOperationID() } catch {
        current_password = ''
        failRequest('无法生成购买操作编号，请稍后重试。')
        return
      }
    }
    const { controller, requestGeneration } = beginRequest('purchase')
    setError(null)
    try {
      const raw = await selfRequest<unknown>('/billing/subscriptions', {
        method: 'POST', body: JSON.stringify({ operation_id: operationID, quote_token: token, current_password }), signal: controller.signal,
      }, csrf)
      if (!currentRequest(controller, requestGeneration)) return
      if (!validSelfPlanPurchaseResult(raw, operationID)) throw new Error('Invalid purchase response')
      setPage(null)
      setBalance(null)
      setQuote(null)
      setRetry(null)
      setResult(raw)
    } catch (caught) {
      if (!currentRequest(controller, requestGeneration)) return
      const uncertain = !(caught instanceof ApiError) || caught.status === 503
      failRequest(uncertain ? '结果未确认；可重新输入当前密码，显式重试同一操作。' : '购买未完成；请重新读取套餐、钱包并取得新报价。')
      if (uncertain) setRetry({ operationID, token })
    } finally {
      current_password = ''
      token = ''
      operationID = ''
      endRequest(controller, requestGeneration)
    }
  }

  return <section className="self-plan-purchase" aria-labelledby="self-plan-purchase-title">
    <h2 id="self-plan-purchase-title">本人钱包购买一次性套餐</h2>
    <p>仅开发预览；先主动读取当前套餐和本人钱包，再获取五分钟冻结报价。目录不是报价；购买不代表模型权益、支付或供应商状态。</p>
    <form className="self-plan-purchase-form" onSubmit={(event) => { event.preventDefault(); void readCatalog() }}>
      <Field label="购买币种（三位大写字母，如 USD）"><input name="purchase_currency" value={currency} onChange={(event) => clearCurrent(event.target.value)} required maxLength={3} pattern="[A-Z]{3}" autoComplete="off" spellCheck={false} /></Field>
      <Button type="submit" disabled={loading !== null}>{loading === 'catalog' ? '正在读取…' : '读取当前套餐'}</Button>
    </form>
    {error ? <p role="alert">{error}</p> : null}
    {result ? <p className="self-plan-purchase-success" role="status">{result.replay ? '已确认原购买' : '购买已确认'}：订阅 {result.subscription_id}；从本人 {result.currency} 钱包扣除 {result.price_micro} micro，并记入 {result.credit_micro} micro 额度。</p> : null}
    {page ? <div className="self-plan-purchase-catalog">
      {!page.available ? <p role="status">当前商业套餐不可用；没有新购买入口。已提交的原操作仍可按同 ID 重试。</p>
        : page.items.length === 0 ? <p role="status">{page.currency} 暂无可展示套餐。</p>
          : <><ol className="self-plan-catalog-list">{page.items.map((item) => <li key={item.plan_id}>
            <div className="self-plan-catalog-heading"><strong>{item.name}</strong><span>{item.interval === 'monthly' ? '单月（不可在此购买）' : '一次性'}</span></div>
            <dl><div><dt>套餐 ID</dt><dd>{item.plan_id}</dd></div><div><dt>目录版本</dt><dd>{item.revision}</dd></div><div><dt>目录价格</dt><dd>{item.price_micro} micro {page.currency}</dd></div><div><dt>目录额度</dt><dd>{item.credit_micro} micro</dd></div></dl>
            {item.interval === 'one_time' && balance?.has_account && balance.currency === page.currency ? <Button variant="secondary" disabled={loading !== null} onClick={() => { void requestQuote(item.plan_id) }}>获取此套餐的冻结报价</Button> : null}
          </li>)}</ol>
            <Button variant="secondary" disabled={loading !== null} onClick={() => { void readBalance() }}>{loading === 'balance' ? '正在读取钱包…' : '读取本币种本人钱包'}</Button>
            {balance ? balance.has_account ? <p role="status">本人 {balance.currency} 钱包当前余额：{balance.amount_micro} micro。购买时仍以最终事务余额为准。</p>
              : <p role="status">本人 {balance.currency} 钱包账户不存在；不能在此购买。</p> : null}
          </>}
      {page.available && page.next_cursor ? <Button variant="secondary" disabled={loading !== null} onClick={() => { void readCatalog(page.next_cursor ?? undefined) }}>加载更多套餐</Button> : null}
    </div> : null}
    {quote ? <form className="self-plan-purchase-confirm" onSubmit={(event) => { void submitPurchase(event) }}>
      <h3>确认冻结报价</h3>
      <p>一次性套餐 {quote.plan_id}（版本 {quote.revision}）：从你本人 {quote.currency} 钱包扣除 <strong>{quote.price_micro} micro</strong>，记入 <strong>{quote.credit_micro} micro</strong> 额度。</p>
      <p>报价至 <time dateTime={quote.expires_at}>{selfKeyDate(quote.expires_at)}</time> 有效；提交时仍会核对当前套餐、商业开关及钱包余额。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认上述本人钱包扣款与额度变动。</label>
      <Field label="当前密码（确认购买）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'purchase' ? '正在确认…' : '确认购买一次性套餐'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={() => clearCurrent()}>取消报价</Button></div>
    </form> : null}
    {retry ? <form className="self-plan-purchase-confirm" onSubmit={(event) => { void submitPurchase(event) }}>
      <h3>上次提交结果未确认</h3>
      <p>只会用原操作编号和原认证报价重试；若先前已提交，返回原订阅。若未提交且报价已过期，不会新增购买。</p>
      <label className="self-plan-purchase-check"><input name="confirm" type="checkbox" required />我确认显式重试上次操作；若尚未提交且原报价仍有效，可能完成先前确认的扣款。</label>
      <Field label="当前密码（重试原操作）"><input name="current_password" type="password" autoComplete="current-password" required minLength={12} /></Field>
      <div className="self-actions"><Button type="submit" disabled={loading !== null}>{loading === 'purchase' ? '正在核对…' : '用同一操作编号重试'}</Button><Button type="button" variant="secondary" disabled={loading !== null} onClick={() => clearCurrent()}>放弃本机重试</Button></div>
    </form> : null}
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
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_plan_purchase !== true
          ? <SelfWalletBalancePanel key={`wallet:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_redemption === true
          ? <SelfRedemptionPanel key={`redemption:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_wallet_activity === true
          ? <SelfWalletActivityPanel key={`wallet-activity:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_wallet_activity === true && session.features?.employee_self_wallet_entry_classification === true
          ? <SelfClassificationPanel key={`wallet-classification:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_wallet_activity === true && session.features?.employee_self_wallet_entry_classification === true && session.features?.employee_self_redemption_credit_history === true
          ? <SelfRedemptionCreditHistoryPanel key={`redemption-history:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_wallet_activity === true && session.features?.employee_self_wallet_entry_classification === true && session.features?.employee_self_admin_adjustment_history === true
          ? <SelfAdminAdjustmentHistoryPanel key={`admin-adjustment-history:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_subscription_status === true
          ? <SelfSubscriptionStatusPanel key={`subscriptions:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} cancelEnabled={session.features?.employee_self_subscription_cancel === true} oneShotEnabled={session.features?.employee_self_one_shot_renewal_disarm === true} purchaseSnapshotEnabled={session.features?.employee_self_subscription_purchase_snapshot === true} renewalLinksEnabled={session.features?.employee_self_subscription_renewal_links === true} renewalEnabled={session.features?.employee_self_subscription_renewal === true} /> : null}
        {session.features?.employee_self_plan_catalog === true && session.features?.employee_self_plan_purchase !== true
          ? <SelfPlanCatalogPanel key={`plan-catalog:${session.profile.id}:${session.csrf_token}`} /> : null}
        {session.features?.employee_self_wallet_balance === true && session.features?.employee_self_plan_catalog === true && session.features?.employee_self_plan_purchase === true
          ? <SelfPlanPurchasePanel key={`plan-purchase:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} /> : null}
        <SelfKeyIssuance key={`issue:${session.profile.id}:${session.csrf_token}`} csrf={session.csrf_token} onIssued={() => setKeyInventoryRevision((value) => value + 1)} />
        <SelfKeyInventory key={`keys:${session.profile.id}:${session.csrf_token}:${keyInventoryRevision}`} csrf={session.csrf_token} />
        <SelfTokenSummaryPanel key={`summary:${session.profile.id}:${session.csrf_token}`} />
        {session.features?.employee_self_upstream_estimated_cost_summary === true
          ? <SelfEstimatedCostPanel key={`estimated-cost:${session.profile.id}:${session.csrf_token}`} /> : null}
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
