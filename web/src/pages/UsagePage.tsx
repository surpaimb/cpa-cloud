import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  api,
  ApiError,
  usageExportURL,
  type Employee,
  type ModelRoute,
  type PriceRate,
  type Upstream,
  type UpstreamPrice,
  type UsageAttempt,
  type UsageFilters,
  type UsageProvider,
  type UsageRequestItem,
  type UsageRequestsPage,
  type UsageStatus,
  type UsageSummary,
  type UsageSettlementReport,
} from '../api'
import { messageFor } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

const statusLabels: Record<UsageStatus, string> = {
  pending: '处理中',
  succeeded: '成功',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '已中断',
}

const providerLabels: Record<UsageProvider, string> = {
  openai: 'OpenAI',
  'openai-compatible': 'OpenAI 兼容',
  anthropic: 'Anthropic',
  gemini: 'Gemini',
  codex: 'Codex',
}

function localDateTime(date: Date) {
  const offset = date.getTimezoneOffset() * 60_000
  return new Date(date.getTime() - offset).toISOString().slice(0, 19)
}

function utcSeconds(local: string) {
  const date = new Date(local)
  if (!local || Number.isNaN(date.getTime())) return null
  return date.toISOString().replace(/\.\d{3}Z$/, 'Z')
}

function initialDates() {
  const to = new Date()
  const from = new Date(to.getTime() - 24 * 60 * 60 * 1000)
  return { from: localDateTime(from), to: localDateTime(to) }
}

export function formatDecimalInteger(value: string | null) {
  if (value === null) return '未知'
  return value.replace(/\B(?=(\d{3})+(?!\d))/g, ',')
}

export function formatMicrocurrency(value: string, currency: string) {
  const padded = value.padStart(7, '0')
  const whole = padded.slice(0, -6).replace(/\B(?=(\d{3})+(?!\d))/g, ',')
  const fraction = padded.slice(-6).replace(/0+$/, '')
  return `${whole}${fraction ? `.${fraction}` : ''} ${currency}`
}

function dateLabel(value: string | null) {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

type FilterOptions = { employees: Employee[]; models: ModelRoute[]; upstreams: Upstream[] }

export function UsagePage({ csrf }: { csrf: string }) {
  const initial = useMemo(initialDates, [])
  const [draft, setDraft] = useState({ ...initial, employee_id: '', key_id: '', model_id: '', upstream_id: '', provider: '', status: '' })
  const [active, setActive] = useState<UsageFilters | null>(null)
  const [summary, setSummary] = useState<UsageSummary | null>(null)
  const [page, setPage] = useState<UsageRequestsPage | null>(null)
  const [cursors, setCursors] = useState<Array<string | undefined>>([undefined])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [filterError, setFilterError] = useState<string | null>(null)
  const [selectedRequest, setSelectedRequest] = useState<UsageRequestItem | null>(null)
  const [options, setOptions] = useState<FilterOptions>({ employees: [], models: [], upstreams: [] })
  const [optionsError, setOptionsError] = useState<string | null>(null)
  const sequence = useRef(0)
  const controller = useRef<AbortController | null>(null)
  const retryAction = useRef<(() => void) | null>(null)

  const loadFirst = useCallback(async (filters: UsageFilters) => {
    retryAction.current = () => { void loadFirst(filters) }
    const current = ++sequence.current
    controller.current?.abort()
    const nextController = new AbortController()
    controller.current = nextController
    setLoading(true)
    setError(null)
    try {
      const [nextSummary, nextPage] = await Promise.all([
        api.usageSummary(filters, nextController.signal),
        api.usageRequests(filters, undefined, nextController.signal),
      ])
      if (current !== sequence.current) return
      const pinned = { ...filters, from: nextPage.from, to: nextPage.to }
      setActive(pinned)
      setSummary(nextSummary)
      setPage(nextPage)
      setCursors([undefined])
    } catch (caught) {
      if (current !== sequence.current || nextController.signal.aborted) return
      setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [])

  const loadPage = useCallback(async (cursorValue: string | undefined, nextCursors: Array<string | undefined>) => {
    if (!active) return
    retryAction.current = () => { void loadPage(cursorValue, nextCursors) }
    const current = ++sequence.current
    controller.current?.abort()
    const nextController = new AbortController()
    controller.current = nextController
    setLoading(true)
    setError(null)
    try {
      const next = await api.usageRequests(active, cursorValue, nextController.signal)
      if (current !== sequence.current) return
      setPage(next)
      setCursors(nextCursors)
    } catch (caught) {
      if (current !== sequence.current || nextController.signal.aborted) return
      setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [active])

  useEffect(() => {
    const from = utcSeconds(initial.from)
    const to = utcSeconds(initial.to)
    if (from && to) void loadFirst({ from, to })
    return () => controller.current?.abort()
  }, [initial, loadFirst])

  useEffect(() => {
    const abort = new AbortController()
    Promise.all([api.employees(), api.models(), api.upstreams(abort.signal)])
      .then(([employees, models, upstreams]) => setOptions({ employees: employees.items, models: models.items, upstreams: upstreams.items }))
      .catch((caught) => { if (!abort.signal.aborted) setOptionsError(messageFor(caught)) })
    return () => abort.abort()
  }, [])

  function applyFilters(event: FormEvent) {
    event.preventDefault()
    const from = utcSeconds(draft.from)
    const to = utcSeconds(draft.to)
    if (!from || !to || new Date(from) >= new Date(to)) {
      setFilterError('开始时间必须早于结束时间。')
      return
    }
    if (new Date(to).getTime() - new Date(from).getTime() > 31 * 24 * 60 * 60 * 1000) {
      setFilterError('时间范围不能超过 31 天。')
      return
    }
    setFilterError(null)
    void loadFirst({
      from,
      to,
      employee_id: draft.employee_id || undefined,
      key_id: draft.key_id.trim() || undefined,
      model_id: draft.model_id || undefined,
      upstream_id: draft.upstream_id || undefined,
      provider: (draft.provider || undefined) as UsageProvider | undefined,
      status: (draft.status || undefined) as UsageStatus | undefined,
    })
  }

  function refreshRecent() {
    const recent = initialDates()
    const from = utcSeconds(recent.from)
    const to = utcSeconds(recent.to)
    setDraft({ ...draft, ...recent })
    setFilterError(null)
    if (from && to) void loadFirst({
      from,
      to,
      employee_id: draft.employee_id || undefined,
      key_id: draft.key_id.trim() || undefined,
      model_id: draft.model_id || undefined,
      upstream_id: draft.upstream_id || undefined,
      provider: (draft.provider || undefined) as UsageProvider | undefined,
      status: (draft.status || undefined) as UsageStatus | undefined,
    })
  }

  const pageIndex = cursors.length - 1
  return <>
    <PageHeader title="用量与成本" description="查看请求与实际尝试的元数据及内部估算成本。未知用量单独显示；这里不是供应商正式账单。" />
    <form className="usage-filters" onSubmit={applyFilters}>
      <Field label="开始时间"><input type="datetime-local" step="1" value={draft.from} onChange={(event) => setDraft({ ...draft, from: event.target.value })} /></Field>
      <Field label="结束时间"><input type="datetime-local" step="1" value={draft.to} onChange={(event) => setDraft({ ...draft, to: event.target.value })} /></Field>
      <Field label="员工"><select value={draft.employee_id} onChange={(event) => setDraft({ ...draft, employee_id: event.target.value })}><option value="">全部员工</option>{options.employees.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>
      <Field label="模型"><select value={draft.model_id} onChange={(event) => setDraft({ ...draft, model_id: event.target.value })}><option value="">全部模型</option>{options.models.map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</select></Field>
      <Field label="上游"><select value={draft.upstream_id} onChange={(event) => setDraft({ ...draft, upstream_id: event.target.value })}><option value="">全部上游</option>{options.upstreams.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>
      <Field label="Provider"><select value={draft.provider} onChange={(event) => setDraft({ ...draft, provider: event.target.value })}><option value="">全部 Provider</option>{Object.entries(providerLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
      <Field label="状态"><select value={draft.status} onChange={(event) => setDraft({ ...draft, status: event.target.value })}><option value="">全部状态</option>{Object.entries(statusLabels).map(([value, label]) => <option key={value} value={value}>{label}</option>)}</select></Field>
      <Field label="Key ID（可选）"><input value={draft.key_id} maxLength={160} placeholder="例如 key_…" onChange={(event) => setDraft({ ...draft, key_id: event.target.value })} /></Field>
      <div className="usage-filters__submit"><Button type="submit" disabled={loading}>应用筛选</Button><Button type="button" variant="secondary" disabled={loading} onClick={refreshRecent}>最近 24 小时</Button></div>
      {filterError ? <div className="inline-error usage-filters__error" role="alert">{filterError}</div> : null}
      {optionsError ? <div className="field-note usage-filters__error">筛选选项暂时无法读取；仍可按时间、Provider、状态或 Key 查询。</div> : null}
    </form>

    <PageState loading={loading && !summary} error={error && !summary ? error : null} onRetry={() => retryAction.current?.()} />
    {summary ? <UsageSummaryCards summary={summary} /> : null}
    <SettlementReports filters={active} />
    <section className="content-panel usage-requests" aria-labelledby="usage-requests-title">
      <header className="section-heading"><div><h2 id="usage-requests-title">员工请求</h2><p>{active ? `${dateLabel(active.from)} 至 ${dateLabel(active.to)} · 固定查询窗口` : '正在准备查询窗口…'}</p></div>{loading && summary ? <span className="subtle-loading">正在更新…</span> : null}</header>
      {error && summary ? <div className="inline-error usage-inline-error" role="alert">{error}<button onClick={() => retryAction.current?.()}>重试</button></div> : null}
      {!loading && !error && page?.items.length === 0 ? <EmptyState title="这个窗口内没有请求" body="调整时间或筛选条件后重新查询。" /> : null}
      {page?.items.length ? <div className="table-scroll"><table className="usage-table"><thead><tr><th>开始时间</th><th>员工 / Key</th><th>模型</th><th>Provider</th><th>状态</th><th>尝试</th><th>操作</th></tr></thead><tbody>{page.items.map((item) => <tr key={item.id}><td>{dateLabel(item.started_at)}</td><td><strong>{options.employees.find((employee) => employee.id === item.employee_id)?.name ?? item.employee_id}</strong><small>{item.key_id}</small></td><td><code>{item.model_id}</code></td><td>{providerLabels[item.provider] ?? item.provider}</td><td><Status value={item.status} /></td><td>{formatDecimalInteger(item.attempt_count)}</td><td><button className="link-button" onClick={() => setSelectedRequest(item)}>查看尝试</button></td></tr>)}</tbody></table></div> : null}
      {page ? <div className="pagination"><Button variant="secondary" disabled={loading || pageIndex === 0} onClick={() => void loadPage(cursors[pageIndex - 1], cursors.slice(0, -1))}>上一页</Button><span>第 {pageIndex + 1} 页</span><Button variant="secondary" disabled={loading || !page.next_cursor} onClick={() => { if (page.next_cursor) void loadPage(page.next_cursor, [...cursors, page.next_cursor]) }}>下一页</Button></div> : null}
    </section>

    <PriceCatalog csrf={csrf} upstreams={options.upstreams} />
    {selectedRequest ? <AttemptDialog request={selectedRequest} onClose={() => setSelectedRequest(null)} /> : null}
  </>
}

function SettlementReports({ filters }: { filters: UsageFilters | null }) {
  const [report, setReport] = useState<UsageSettlementReport | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const sequence = useRef(0)
  async function load(granularity: 'day' | 'month') {
    if (!filters) return
    const current = ++sequence.current
    const abort = new AbortController()
    setLoading(true); setError(null)
    try {
      const next = granularity === 'day' ? await api.usageSettlementDaily(filters, abort.signal) : await api.usageSettlementMonthly(filters, abort.signal)
      if (current === sequence.current) setReport(next)
    } catch (caught) {
      if (current === sequence.current && !abort.signal.aborted) setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }
  return <section className="content-panel usage-settlement" aria-labelledby="usage-settlement-title">
    <header className="section-heading"><div><h2 id="usage-settlement-title">对账汇总与导出</h2><p>按服务完成时间生成日/月汇总。修正是追加事实；未知证据和未知成本不会显示为零，金额仍是配置价格的内部估算。</p></div><div className="usage-settlement__actions"><Button variant="secondary" disabled={!filters || loading} onClick={() => void load('day')}>查看日汇总</Button><Button variant="secondary" disabled={!filters || loading} onClick={() => void load('month')}>查看月汇总</Button>{filters ? <a className="button button--secondary" href={usageExportURL(filters)} download="usage-settlement.csv">导出 CSV（最多 5000 行）</a> : null}</div></header>
    {loading ? <div className="subtle-loading">正在读取对账汇总…</div> : null}
    <FormError error={error} />
    {!loading && !error && report?.items.length === 0 ? <EmptyState title="没有可汇总的终态尝试" body="这不表示用量为零；可能是筛选窗口为空。" /> : null}
    {report?.items.length ? <div className="table-scroll"><table className="usage-table"><thead><tr><th>期间</th><th>币种</th><th>请求 / 尝试</th><th>内部估算成本</th><th>未知成本</th><th>缺少证据</th><th>修正</th><th>推理 Token</th></tr></thead><tbody>{report.items.map((item) => <tr key={`${item.period_start}:${item.currency}`}><td>{dateLabel(item.period_start)}<small>至 {dateLabel(item.period_end)}</small></td><td>{item.currency}</td><td>{formatDecimalInteger(item.requests)} / {formatDecimalInteger(item.attempts)}</td><td>{item.currency === 'UNKNOWN' ? '不可估算' : formatMicrocurrency(item.known_estimated_cost_micro, item.currency)}</td><td>{formatDecimalInteger(item.unknown_cost_attempts)}</td><td>{formatDecimalInteger(item.missing_evidence_attempts)}</td><td>{formatDecimalInteger(item.corrections)}</td><td>{formatDecimalInteger(item.reasoning_tokens.known_total)}<small>{formatDecimalInteger(item.reasoning_tokens.unknown_attempts)} 次未知</small></td></tr>)}</tbody></table></div> : null}
  </section>
}

function UsageSummaryCards({ summary }: { summary: UsageSummary }) {
  return <section className="usage-summary" aria-label="用量汇总">
    <article className="usage-card usage-card--requests"><span>员工请求</span><strong>{formatDecimalInteger(summary.requests.total)}</strong><div className="status-breakdown">{(Object.keys(statusLabels) as UsageStatus[]).map((status) => <span key={status}>{statusLabels[status]} <b>{formatDecimalInteger(summary.requests[status])}</b></span>)}</div></article>
    {summary.attempts.map((attempt) => <article className="usage-card" key={attempt.currency}><header><div><span>{attempt.currency === 'UNKNOWN' ? '未配置价格' : `${attempt.currency} 估算成本`}</span><strong>{attempt.currency === 'UNKNOWN' ? '不可估算' : formatMicrocurrency(attempt.known_cost_micro, attempt.currency)}</strong></div><small>{formatDecimalInteger(attempt.unknown_cost_attempts)} 次成本未知</small></header><div className="token-grid"><TokenMetric label="普通输入" counter={attempt.input_tokens} /><TokenMetric label="输出" counter={attempt.output_tokens} /><TokenMetric label="缓存读取" counter={attempt.cache_read_tokens} /><TokenMetric label="缓存写入" counter={attempt.cache_write_tokens} /></div><footer>{formatDecimalInteger(attempt.total)} 次实际尝试 · 不与其他币种相加</footer></article>)}
    {summary.attempts.length === 0 ? <article className="usage-card usage-card--empty"><span>实际尝试</span><strong>暂无数据</strong><p>请求可能在上游执行前已失败，或当前窗口为空。</p></article> : null}
  </section>
}

function TokenMetric({ label, counter }: { label: string; counter: { known_total: string; unknown_attempts: string } }) {
  return <div><span>{label}</span><strong>{formatDecimalInteger(counter.known_total)}</strong><small>{formatDecimalInteger(counter.unknown_attempts)} 次未知</small></div>
}

function Status({ value }: { value: UsageStatus }) {
  const tone = value === 'succeeded' ? 'active' : value === 'pending' ? 'warning' : value === 'cancelled' || value === 'interrupted' ? 'disabled' : 'danger'
  return <span className={`status status--${tone}`}><i />{statusLabels[value]}</span>
}

function AttemptDialog({ request, onClose }: { request: UsageRequestItem; onClose: () => void }) {
  const [items, setItems] = useState<UsageAttempt[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const sequence = useRef(0)
  const load = useCallback(async () => {
    const current = ++sequence.current
    const abort = new AbortController()
    setLoading(true); setError(null)
    try {
      const result = await api.usageAttempts(request.id, abort.signal)
      if (current === sequence.current) setItems(result.items)
    } catch (caught) {
      if (current === sequence.current && !abort.signal.aborted) setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
    return () => abort.abort()
  }, [request.id])
  useEffect(() => { void load(); return () => { sequence.current += 1 } }, [load])
  return <Dialog title="实际尝试详情" description={`请求 ${request.id} · ${request.model_id}`} onClose={onClose} wide>
    <PageState loading={loading} error={error} onRetry={() => void load()} />
    {!loading && !error && items.length === 0 ? <EmptyState title="没有上游尝试" body="该请求在实际执行上游前结束。" /> : null}
    {items.length ? <div className="attempt-list">{items.map((item) => <article key={item.id}><header><div><strong>{item.account_id}</strong><small>{item.dispatch} · {providerLabels[item.provider] ?? item.provider}</small></div><Status value={item.status} /></header><dl><div><dt>普通输入</dt><dd>{formatDecimalInteger(item.input_tokens)}</dd></div><div><dt>输出</dt><dd>{formatDecimalInteger(item.output_tokens)}</dd></div><div><dt>缓存读取</dt><dd>{formatDecimalInteger(item.cache_read_tokens)}</dd></div><div><dt>缓存写入</dt><dd>{formatDecimalInteger(item.cache_write_tokens)}</dd></div><div><dt>估算成本</dt><dd>{item.cost_micro === null || item.currency === null ? '未知' : formatMicrocurrency(item.cost_micro, item.currency)}</dd></div><div><dt>价格版本</dt><dd>{item.price_version ?? '未配置'}</dd></div></dl><footer>{dateLabel(item.started_at)} → {dateLabel(item.finished_at)}</footer></article>)}</div> : null}
    <div className="dialog__actions"><Button variant="secondary" onClick={onClose}>关闭</Button></div>
  </Dialog>
}

function PriceCatalog({ csrf, upstreams }: { csrf: string; upstreams: Upstream[] }) {
  const [upstreamID, setUpstreamID] = useState('')
  const [items, setItems] = useState<UpstreamPrice[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<UpstreamPrice | 'new' | null>(null)
  const sequence = useRef(0)
  useEffect(() => {
    if (!upstreamID && upstreams[0]) setUpstreamID(upstreams[0].id)
  }, [upstreamID, upstreams])
  const load = useCallback(async (): Promise<UpstreamPrice[] | null> => {
    if (!upstreamID) { setItems([]); setLoading(false); return [] }
    const current = ++sequence.current
    const abort = new AbortController()
    setLoading(true); setError(null)
    try {
      const result = await api.upstreamPrices(upstreamID, abort.signal)
      if (current === sequence.current) setItems(result.items)
      return result.items
    } catch (caught) {
      if (current === sequence.current && !abort.signal.aborted) setError(messageFor(caught))
      return null
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [upstreamID])
  useEffect(() => { void load(); return () => { sequence.current += 1 } }, [load])
  const upstream = upstreams.find((item) => item.id === upstreamID)
  return <section className="content-panel price-catalog" aria-labelledby="price-catalog-title">
    <header className="section-heading"><div><h2 id="price-catalog-title">价格管理</h2><p>按账号与实际模型追加价格版本。费率单位为 microcurrency / 百万 Token；停用后新尝试成本为未知。</p></div><Button disabled={!upstreamID || loading} onClick={() => setEditing('new')}>添加模型价格</Button></header>
    <div className="price-toolbar"><Field label="上游账号"><select value={upstreamID} onChange={(event) => { const nextID = event.target.value; if (nextID === upstreamID) return; sequence.current += 1; setItems([]); setError(null); setLoading(Boolean(nextID)); setUpstreamID(nextID); setEditing(null) }}><option value="">选择上游</option>{upstreams.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>{upstream ? <small><code>{upstream.id}</code> · {upstream.enabled ? '启用' : '已停用（仍可预配置）'}</small> : null}</div>
    <PageState loading={loading} error={error} onRetry={() => void load()} />
    {!loading && !error && upstreamID && items.length === 0 ? <EmptyState title="这个账号还没有价格" body="添加实际上游模型的费率后，新开始的尝试会固定使用该版本。" action={<Button onClick={() => setEditing('new')}>添加价格</Button>} /> : null}
    {items.length ? <div className="table-scroll"><table className="price-table"><thead><tr><th>实际模型</th><th>版本</th><th>币种</th><th>输入 / 输出</th><th>缓存读 / 写</th><th>操作</th></tr></thead><tbody>{items.map((item) => <tr key={item.upstream_model}><td><code>{item.upstream_model}</code></td><td><strong>r{item.revision}</strong><small>{item.version}</small></td><td>{item.price?.currency ?? <span className="muted-copy">已停用</span>}</td><td>{item.price ? <><span>{formatDecimalInteger(item.price.input_per_million_micro)}</span><small>{formatDecimalInteger(item.price.output_per_million_micro)}</small></> : '—'}</td><td>{item.price ? <><span>{formatDecimalInteger(item.price.cache_read_per_million_micro)}</span><small>{formatDecimalInteger(item.price.cache_write_per_million_micro)}</small></> : '—'}</td><td><button className="link-button" onClick={() => setEditing(item)}>追加版本</button></td></tr>)}</tbody></table></div> : null}
    {editing && upstreamID ? <PriceEditor csrf={csrf} upstreamID={upstreamID} current={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void load() }} onReload={async (model) => { const loaded = await load(); return loaded === null ? { ok: false as const } : { ok: true as const, item: loaded.find((item) => item.upstream_model === model) } }} /> : null}
  </section>
}

type PriceWrite = { operation_id: string; expected_revision: number; upstream_model: string; price: PriceRate | null }
const maxSafeInteger = '9007199254740991'
function validRate(value: string) {
  return /^(0|[1-9]\d*)$/.test(value) && (value.length < maxSafeInteger.length || value.length === maxSafeInteger.length && value <= maxSafeInteger)
}

function samePrice(left: PriceRate | null | undefined, right: PriceRate | null) {
  if (left === null || left === undefined || right === null) return left === right
  return left.currency === right.currency
    && left.input_per_million_micro === right.input_per_million_micro
    && left.output_per_million_micro === right.output_per_million_micro
    && left.cache_read_per_million_micro === right.cache_read_per_million_micro
    && left.cache_write_per_million_micro === right.cache_write_per_million_micro
}

function PriceEditor({ csrf, upstreamID, current, onClose, onSaved, onReload }: { csrf: string; upstreamID: string; current?: UpstreamPrice; onClose: () => void; onSaved: () => void; onReload: (model: string) => Promise<{ ok: boolean; item?: UpstreamPrice }> }) {
  const [model, setModel] = useState(current?.upstream_model ?? '')
  const [enabled, setEnabled] = useState(current?.price !== null)
  const [currency, setCurrency] = useState(current?.price?.currency ?? 'USD')
  const [input, setInput] = useState(current?.price?.input_per_million_micro ?? '0')
  const [output, setOutput] = useState(current?.price?.output_per_million_micro ?? '0')
  const [cacheRead, setCacheRead] = useState(current?.price?.cache_read_per_million_micro ?? '0')
  const [cacheWrite, setCacheWrite] = useState(current?.price?.cache_write_per_million_micro ?? '0')
  const [revision, setRevision] = useState(current?.revision ?? 0)
  const [operationID, setOperationID] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [uncertain, setUncertain] = useState<PriceWrite | null>(null)
  const [conflict, setConflict] = useState(false)

  function buildPayload(): PriceWrite | null {
    const cleanModel = model.trim()
    const cleanCurrency = currency.trim().toUpperCase()
    if (!cleanModel || cleanModel !== model || /\p{Cc}/u.test(model) || new TextEncoder().encode(model).length > 256) { setError('实际上游模型须为 1–256 字节，不能包含首尾空白或控制字符。'); return null }
    if (enabled && (!/^[A-Z]{3}$/.test(cleanCurrency) || ![input, output, cacheRead, cacheWrite].every(validRate))) {
      setError('币种必须是三个大写字母；四项费率必须是安全范围内的非负十进制整数。')
      return null
    }
    return { operation_id: operationID, expected_revision: revision, upstream_model: cleanModel, price: enabled ? { currency: cleanCurrency, input_per_million_micro: input, output_per_million_micro: output, cache_read_per_million_micro: cacheRead, cache_write_per_million_micro: cacheWrite } : null }
  }

  async function save(payload: PriceWrite) {
    setBusy(true); setError(null)
    try {
      await api.saveUpstreamPrice(upstreamID, payload, csrf)
      setUncertain(null)
      onSaved()
    } catch (caught) {
      if (caught instanceof ApiError) {
        if (caught.status === 409) {
          setConflict(true)
          setError('价格已被其他操作更新。本次编辑仍保留，请先重新加载当前版本。')
        } else {
          setError(messageFor(caught))
        }
      } else {
        setUncertain(payload)
        setError('未能确认保存结果。可用同一操作编号重试，或重新加载目录核对。')
      }
      setBusy(false)
    }
  }

  async function reloadCurrent() {
    setBusy(true); setError(null)
    const result = await onReload(model.trim())
    setBusy(false)
    if (!result.ok) {
      setError('重新加载目录失败。原操作编号与提交内容仍保留，请重试加载或用同一操作编号重试保存。')
      return
    }
    const latest = result.item
    if (uncertain) {
      if (latest && latest.upstream_model === uncertain.upstream_model && latest.revision > uncertain.expected_revision && samePrice(latest.price, uncertain.price)) {
        onSaved()
        return
      }
      setError('目录中未确认原提交已生效。原操作编号与提交内容仍保留，可安全重试同一请求。')
      return
    }
    if (latest) {
      setRevision(latest.revision)
      setOperationID(crypto.randomUUID())
      setConflict(false)
      setUncertain(null)
      setError('已加载最新 revision；表单内容仍保留，请核对后重新保存。')
    } else if (revision === 0) {
      setOperationID(crypto.randomUUID())
      setConflict(false)
      setUncertain(null)
      setError('目录中仍没有该模型；可核对后重新保存。')
    } else {
      setError('未找到当前模型价格，请关闭编辑器并重新选择。')
    }
  }

  const locked = busy || uncertain !== null || conflict
  return <Dialog title={current ? `追加 ${current.upstream_model} 的价格版本` : '添加模型价格'} description={`当前 expected revision：${revision}。成功后会生成不可修改的新版本。`} onClose={onClose} closeDisabled={busy}>
    <form onSubmit={(event) => { event.preventDefault(); const payload = buildPayload(); if (payload) void save(payload) }}>
      <div className="form-grid price-form">
        <Field label="实际上游模型"><input value={model} readOnly={Boolean(current) || locked} maxLength={256} onChange={(event) => setModel(event.target.value)} required autoFocus /></Field>
        <label className="toggle-field"><input type="checkbox" checked={enabled} disabled={locked} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用价格</strong><small>取消勾选会追加 price=null 的停用版本。</small></span></label>
        {enabled ? <><Field label="币种"><input value={currency} disabled={locked} maxLength={3} onChange={(event) => setCurrency(event.target.value.toUpperCase())} /></Field><RateField label="普通输入" value={input} disabled={locked} onChange={setInput} /><RateField label="输出" value={output} disabled={locked} onChange={setOutput} /><RateField label="缓存读取" value={cacheRead} disabled={locked} onChange={setCacheRead} /><RateField label="缓存写入" value={cacheWrite} disabled={locked} onChange={setCacheWrite} /></> : null}
      </div>
      <p className="price-unit-note">所有费率均为 microcurrency / 1,000,000 Token，使用十进制整数字符串传输。</p>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>关闭</Button>{uncertain || conflict ? <Button type="button" variant="secondary" disabled={busy} onClick={() => void reloadCurrent()}>重新加载目录</Button> : null}{uncertain ? <Button type="button" disabled={busy} onClick={() => void save(uncertain)}>用同一操作编号重试</Button> : <Button type="submit" disabled={busy || conflict}>{busy ? '正在保存…' : enabled ? '追加价格版本' : '追加停用版本'}</Button>}</div>
    </form>
  </Dialog>
}

function RateField({ label, value, disabled, onChange }: { label: string; value: string; disabled: boolean; onChange: (value: string) => void }) {
  return <Field label={`${label}费率`} hint="microcurrency / 百万 Token"><input inputMode="numeric" value={value} disabled={disabled} onChange={(event) => onChange(event.target.value)} /></Field>
}
