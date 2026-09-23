import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import {
  api,
  type GovernanceObservationFilters,
  type GovernanceObservationItem,
  type GovernanceObservationsPage,
  type GovernanceObservationState,
  type GovernanceScopeKind,
} from './api'
import { messageFor } from './hooks'
import { Button, EmptyState, Field, PageState } from './ui'
import { formatDecimalInteger, formatMicrocurrency } from './pages/UsagePage'

const scopeLabels: Record<GovernanceScopeKind, string> = { employee: '员工', key: 'Key', group: '治理组' }
const stateLabels: Record<GovernanceObservationState, string> = { exceeded: '已超过', below: '未超过阈值', unknown: '无法判断' }

function timeLabel(value: string) {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

function StateBadge({ state }: { state: GovernanceObservationState | null }) {
  if (state === null) return <span className="observation-state observation-state--unset">未配置阈值</span>
  return <span className={`observation-state observation-state--${state}`}>{stateLabels[state]}</span>
}

function Counts({ item, kind }: { item: GovernanceObservationItem; kind: 'tpm' | 'cost' }) {
  const totals = item.scope_totals[kind]
  const unknown = kind === 'tpm' ? item.scope_totals.tpm.unknown_token_attempts : item.scope_totals.cost.unknown_cost_attempts
  return <div className="observation-counts">
    <span>已知尝试 <strong>{formatDecimalInteger(totals.known_attempts)}</strong></span>
    <span>未知尝试 <strong>{formatDecimalInteger(unknown)}</strong></span>
    <span>处理中尝试 <strong>{formatDecimalInteger(totals.pending_attempts)}</strong></span>
    <span>待派发请求 <strong>{formatDecimalInteger(totals.pending_requests_without_attempt)}</strong></span>
    <span>零尝试请求 <strong>{formatDecimalInteger(totals.zero_attempt_requests)}</strong></span>
  </div>
}

function ObservationCard({ item }: { item: GovernanceObservationItem }) {
  const snapshot = item.snapshot
  const configuredCurrency = snapshot.shadow_currency
  return <article className="observation-card">
    <header>
      <div>
        <span>{scopeLabels[snapshot.scope_kind]} scope</span>
        <strong>{snapshot.scope_id}</strong>
        <small>policy {snapshot.policy_id} · r{snapshot.policy_revision} · settings r{snapshot.settings_revision}{snapshot.group_revision ? ` · group r${snapshot.group_revision}` : ''}</small>
      </div>
      <span className="observation-card__history">历史阈值快照</span>
    </header>
    <div className="observation-metrics">
      <section>
        <div className="observation-metric__heading"><div><span>最近 60 秒 Token</span><strong>{formatDecimalInteger(item.scope_totals.tpm.known_tokens)}</strong></div><StateBadge state={item.interpretation.tpm_state} /></div>
        <p>{snapshot.shadow_tpm ? `历史阈值 ${formatDecimalInteger(snapshot.shadow_tpm)} Token` : '该历史快照未配置 TPM 阈值'}</p>
        <Counts item={item} kind="tpm" />
      </section>
      <section>
        <div className="observation-metric__heading"><div><span>最近 24 小时内部估算成本</span><strong>{configuredCurrency && snapshot.shadow_cost_micro ? `${formatMicrocurrency(snapshot.shadow_cost_micro, configuredCurrency)} 阈值` : '未配置成本阈值'}</strong></div><StateBadge state={item.interpretation.cost_state} /></div>
        {item.scope_totals.cost.by_currency.length ? <div className="observation-currencies">{item.scope_totals.cost.by_currency.map((total) => <div key={total.currency}><span>{total.currency}</span><strong>{formatMicrocurrency(total.known_cost_micro, total.currency)}</strong><small>{formatDecimalInteger(total.attempts)} 次已知尝试</small></div>)}</div> : <p>窗口内没有已知成本；这不等于成本为零。</p>}
        <Counts item={item} kind="cost" />
        <p className="observation-incomparable">其他币种不可比较尝试：{item.interpretation.incomparable_currency_attempts === null ? '不适用' : `${formatDecimalInteger(item.interpretation.incomparable_currency_attempts)} 次`}。不同币种不会相加或换算。</p>
      </section>
    </div>
  </article>
}

export function GovernanceObservations() {
  const [open, setOpen] = useState(false)
  const [draft, setDraft] = useState({ scope_kind: '', scope_id: '', policy_id: '' })
  const [active, setActive] = useState<GovernanceObservationFilters>({ limit: 20 })
  const [page, setPage] = useState<GovernanceObservationsPage | null>(null)
  const [cursors, setCursors] = useState<Array<string | undefined>>([undefined])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [filterError, setFilterError] = useState<string | null>(null)
  const sequence = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const retry = useRef<(() => void) | null>(null)

  const load = useCallback(async (filters: GovernanceObservationFilters, cursor: string | undefined, nextCursors: Array<string | undefined>) => {
    retry.current = () => { void load(filters, cursor, nextCursors) }
    const current = ++sequence.current
    controller.current?.abort()
    const nextController = new AbortController()
    controller.current = nextController
    setLoading(true)
    setError(null)
    try {
      const next = await api.governanceObservations(filters, cursor, nextController.signal)
      if (current !== sequence.current) return
      setActive(filters)
      setPage(next)
      setCursors(nextCursors)
    } catch (caught) {
      if (current !== sequence.current || nextController.signal.aborted) return
      setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => () => controller.current?.abort(), [])

  function openObservations() {
    setOpen(true)
    if (!page && !loading) void load({ limit: 20 }, undefined, [undefined])
  }

  function apply(event: FormEvent) {
    event.preventDefault()
    const scopeID = draft.scope_id.trim()
    const policyID = draft.policy_id.trim()
    if (!draft.scope_kind && scopeID) {
	  setFilterError('填写范围 ID 时必须选择范围类型。')
      return
    }
    if (new TextEncoder().encode(scopeID).length > 200 || new TextEncoder().encode(policyID).length > 200) {
	  setFilterError('范围 ID 与策略 ID 最多 200 字节。')
      return
    }
    setFilterError(null)
    const filters: GovernanceObservationFilters = { limit: 20 }
    if (draft.scope_kind) filters.scope_kind = draft.scope_kind as GovernanceScopeKind
    if (scopeID) filters.scope_id = scopeID
    if (policyID) filters.policy_id = policyID
    void load(filters, undefined, [undefined])
  }

  const pageIndex = cursors.length - 1
  return <section className="content-panel governance-observations" aria-labelledby="governance-observations-title">
    <div className="section-heading">
      <div><h2 id="governance-observations-title">Shadow 用量观测</h2><p>查看历史阈值对当前窗口的解释。这里只观测，不扣费，也不拦截员工请求。</p></div>
      {!open ? <Button onClick={openObservations}>查看用量观测</Button> : <Button variant="secondary" disabled={loading} onClick={() => void load(active, undefined, [undefined])}>刷新首屏</Button>}
    </div>
    {!open ? <p className="governance-observations__intro">Token 固定查看最近 60 秒，内部估算成本固定查看最近 24 小时。未知与处理中记录不会显示为“未超过阈值”。</p> : <>
      <form className="observation-filters" onSubmit={apply}>
        <Field label="范围类型"><select value={draft.scope_kind} onChange={(event) => setDraft({ ...draft, scope_kind: event.target.value })}><option value="">全部范围</option><option value="employee">员工</option><option value="key">Key</option><option value="group">治理组</option></select></Field>
        <Field label="范围 ID（可选）"><input value={draft.scope_id} maxLength={200} placeholder="按具体范围筛选" onChange={(event) => setDraft({ ...draft, scope_id: event.target.value })} /></Field>
        <Field label="策略 ID（可选）"><input value={draft.policy_id} maxLength={200} placeholder="policy_…" onChange={(event) => setDraft({ ...draft, policy_id: event.target.value })} /></Field>
        <div className="observation-filters__submit"><Button type="submit" disabled={loading}>应用筛选</Button></div>
        {filterError ? <div className="inline-error observation-filters__error" role="alert">{filterError}</div> : null}
      </form>
      <PageState loading={loading && !page} error={error && !page ? error : null} onRetry={() => retry.current?.()} />
      {page ? <div className="observation-window"><div><span>Token 窗口</span><strong>{timeLabel(page.tpm_from)} 至 {timeLabel(page.window_end)}</strong></div><div><span>成本窗口</span><strong>{timeLabel(page.cost_from)} 至 {timeLabel(page.window_end)}</strong></div><small>本页读取于 {timeLabel(page.observed_at)}。游标固定窗口边界，但不是数据库历史快照；需要最新完整结果时请刷新首屏。</small></div> : null}
      {page ? <p className="observation-attribution-note">同一请求可同时计入员工、Key 与治理组。同一范围的不同历史阈值使用相同窗口总计，请勿把不同卡片的 Token 或金额相加。</p> : null}
      {error && page ? <div className="inline-error usage-inline-error" role="alert">{error}<button onClick={() => retry.current?.()}>重试</button></div> : null}
      {!loading && !error && page?.items.length === 0 ? <EmptyState title="窗口内无观测" body="没有准入快照时无法证明用量为零，也不会显示“未超过阈值”。可调整筛选或刷新首屏。" /> : null}
      {page?.items.length ? <div className="observation-list">{page.items.map((item) => <ObservationCard key={`${item.snapshot.scope_kind}:${item.snapshot.scope_id}:${item.snapshot.policy_id}:${item.snapshot.policy_revision}:${item.snapshot.group_revision ?? ''}:${item.snapshot.settings_revision}`} item={item} />)}</div> : null}
      {page ? <div className="pagination"><Button variant="secondary" disabled={loading || pageIndex === 0} onClick={() => void load(active, cursors[pageIndex - 1], cursors.slice(0, -1))}>上一页</Button><span>第 {pageIndex + 1} 页</span><Button variant="secondary" disabled={loading || !page.next_cursor} onClick={() => { if (page.next_cursor) void load(active, page.next_cursor, [...cursors, page.next_cursor]) }}>下一页</Button></div> : null}
    </>}
  </section>
}
