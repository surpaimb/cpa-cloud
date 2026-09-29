// Independently authored UI for docs/admin-audit-overview-contract.md and
// docs/admin-audit-export-contract.md.
import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react'
import { adminAuditSources, api, ApiError, type AdminAuditFilters, type AdminAuditPage, type AdminAuditSource } from '../api'
import { messageFor } from '../hooks'
import { Button, EmptyState, Field, FormError, PageState } from '../ui'

const sourceLabels: Record<AdminAuditSource, string> = {
  account_pool: '账号池',
  account_lifecycle: '账号生命周期',
  governance_management: '治理管理',
  governance_general_budget: '通用预算',
  financial_commercial: '财务商业操作',
}

const legacySources: AdminAuditSource[] = adminAuditSources.slice(0, 4)

function validMetadata(value: string) {
  return !value || (value.trim() === value && new TextEncoder().encode(value).length <= 256 && !/[\x00-\x1f\x7f-\x9f]/u.test(value))
}

function localTimeToUTC(value: string) {
  const parsed = new Date(value)
  return Number.isFinite(parsed.getTime()) ? parsed.toISOString() : null
}

function formatTime(value: string) {
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'medium', timeStyle: 'medium' }).format(new Date(value))
}

export function AuditPage({ financialSource = false, csvExport = false }: { financialSource?: boolean; csvExport?: boolean }) {
  const availableSources = financialSource ? adminAuditSources : legacySources
  const [selectedSources, setSelectedSources] = useState(() => new Set<AdminAuditSource>(availableSources))
  const [actor, setActor] = useState('')
  const [action, setAction] = useState('')
  const [targetType, setTargetType] = useState('')
  const [targetID, setTargetID] = useState('')
  const [result, setResult] = useState<'' | 'succeeded'>('')
  const [from, setFrom] = useState('')
  const [to, setTo] = useState('')
  const [validation, setValidation] = useState<string | null>(null)
  const [data, setData] = useState<AdminAuditPage | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [exporting, setExporting] = useState(false)
  const [exportError, setExportError] = useState<string | null>(null)
  const [filters, setFilters] = useState<AdminAuditFilters>(() => ({ limit: 20, sources: [...availableSources] }))
  const [cursorChain, setCursorChain] = useState<Array<string | undefined>>([undefined])
  const [pageCache, setPageCache] = useState<AdminAuditPage[]>([])
  const [pageIndex, setPageIndex] = useState(0)
  const requestSequence = useRef(0)

  const load = useCallback(async (nextFilters: AdminAuditFilters, cursor: string | undefined, index: number, chain: Array<string | undefined>) => {
    const sequence = ++requestSequence.current
    setLoading(true)
    setError(null)
    setData(null)
    setFilters(nextFilters)
    setPageIndex(index)
    setCursorChain(chain)
    try {
      const page = await api.auditEvents(nextFilters, cursor)
      if (sequence !== requestSequence.current) return
      if (page.sources.some((source) => !availableSources.includes(source))
        || (nextFilters.sources && (page.sources.length !== nextFilters.sources.length
          || page.sources.some((source, index) => source !== nextFilters.sources?.[index])))) {
        throw new ApiError(502, 'invalid_response', '审计响应格式无效，未显示任何结果。')
      }
      setData(page)
      setPageCache((current) => [...current.slice(0, index), page])
    } catch (caught) {
      if (sequence !== requestSequence.current) return
      setError(messageFor(caught))
    } finally {
      if (sequence === requestSequence.current) setLoading(false)
    }
  }, [availableSources])

  useEffect(() => {
    setSelectedSources(new Set<AdminAuditSource>(availableSources))
    void load({ limit: 20, sources: [...availableSources] }, undefined, 0, [undefined])
  }, [availableSources, load])

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (selectedSources.size === 0) { setValidation('请至少选择一个事实来源。'); return }
    for (const [label, value] of [['操作人', actor], ['动作', action], ['目标类型', targetType], ['目标 ID', targetID]] as const) {
      if (!validMetadata(value)) { setValidation(`${label}必须是最多 256 字节、无首尾空白和控制字符的精确值。`); return }
    }
    if (Boolean(from) !== Boolean(to)) { setValidation('开始时间和结束时间必须同时填写或同时留空。'); return }
    let fromUTC: string | undefined
    let toUTC: string | undefined
    if (from && to) {
      fromUTC = localTimeToUTC(from) ?? undefined
      toUTC = localTimeToUTC(to) ?? undefined
      if (!fromUTC || !toUTC || fromUTC >= toUTC) { setValidation('时间范围无效；开始时间必须早于结束时间。'); return }
      if (new Date(toUTC).getTime() - new Date(fromUTC).getTime() > 31 * 24 * 60 * 60 * 1000) { setValidation('时间范围不能超过 31 天。'); return }
    }
    setValidation(null)
    const nextFilters: AdminAuditFilters = {
      limit: 20,
      sources: availableSources.filter((source) => selectedSources.has(source)),
      ...(actor ? { actor_id: actor } : {}),
      ...(action ? { action } : {}),
      ...(targetType ? { target_type: targetType } : {}),
      ...(targetID ? { target_id: targetID } : {}),
      ...(result ? { result } : {}),
      ...(fromUTC && toUTC ? { from: fromUTC, to: toUTC } : {}),
    }
    void load(nextFilters, undefined, 0, [undefined])
  }

  const retry = () => void load(filters, cursorChain[pageIndex], pageIndex, cursorChain)
  const previousPage = () => {
    if (pageIndex === 0 || !pageCache[pageIndex - 1]) return
    requestSequence.current += 1
    setLoading(false)
    setError(null)
    setPageIndex((current) => current - 1)
    setData(pageCache[pageIndex - 1])
  }
  const nextPage = () => {
    if (!data?.next_cursor) return
    const nextIndex = pageIndex + 1
    if (pageCache[nextIndex] && cursorChain[nextIndex] === data.next_cursor) {
      requestSequence.current += 1
      setLoading(false)
      setError(null)
      setPageIndex(nextIndex)
      setData(pageCache[nextIndex])
      return
    }
    const nextChain = [...cursorChain.slice(0, nextIndex), data.next_cursor]
    void load(filters, data.next_cursor, nextIndex, nextChain)
  }

  async function exportCSV() {
    if (!csvExport || !data || exporting || loading) return
    setExporting(true)
    setExportError(null)
    try {
      const blob = await api.auditExport({ ...filters, from: data.from, to: data.to })
      const objectURL = URL.createObjectURL(blob)
      const link = document.createElement('a')
      link.href = objectURL
      link.download = 'cpa-cloud-admin-audit.csv'
      document.body.append(link)
      try { link.click() } finally {
        link.remove()
        const revoke = URL.revokeObjectURL.bind(URL)
        window.setTimeout(() => revoke(objectURL), 1000)
      }
    } catch (caught) {
      setExportError(messageFor(caught))
    } finally {
      setExporting(false)
    }
  }

  return <>
    <header className="page-header audit-page-header"><div><h1>管理审计</h1><p>按固定字段查看{financialSource ? '五' : '四'}类现存事务事实，不读取请求正文或敏感凭据。</p></div><div className="page-header__action"><Button variant="secondary" disabled={loading} onClick={retry}>刷新本页</Button>{csvExport ? <Button variant="secondary" disabled={loading || exporting || !data} onClick={() => void exportCSV()}>{exporting ? '正在导出…' : '导出 CSV（最多 1000 行）'}</Button> : null}</div></header>
    <section className="audit-boundary" aria-label="审计范围说明"><strong>这是现存事实视图，不是完整历史</strong><p>{financialSource ? '包含账号池、账号生命周期、治理管理、通用预算及财务商业操作中仍保留的已提交事务事实；未关联管理员的财务事实明确标注，不代表外部支付完成。' : '仅包含账号池、账号生命周期、治理管理和通用预算中仍保留的成功事实。'}不覆盖登录、备份或全部管理操作，也不改变各来源原有保留规则；不是完整财务或统一审计。</p>{csvExport ? <p>CSV 按当前筛选窗口重新查询全部匹配事实，不限于当前页；超过 1000 行或 2 MiB 会报错，请缩小筛选范围。</p> : null}</section>
    {csvExport ? <FormError error={exportError} /> : null}
    <section className="content-panel audit-panel">
      <form className="audit-filters" onSubmit={submit}>
        <fieldset className="audit-source-options"><legend>事实来源</legend>{availableSources.map((source) => <label key={source}><input type="checkbox" checked={selectedSources.has(source)} onChange={(event) => setSelectedSources((current) => { const next = new Set(current); event.target.checked ? next.add(source) : next.delete(source); return next })} /><span>{sourceLabels[source]}</span></label>)}</fieldset>
        <div className="audit-filter-grid">
          <Field label="操作人（精确）"><input value={actor} onChange={(event) => setActor(event.target.value)} placeholder="admin_…" /></Field>
          <Field label="动作（精确）"><input value={action} onChange={(event) => setAction(event.target.value)} placeholder="例如 account_group.update" /></Field>
          <Field label="目标类型（精确）"><input value={targetType} onChange={(event) => setTargetType(event.target.value)} placeholder="例如 account_group" /></Field>
          <Field label="目标 ID（精确）"><input value={targetID} onChange={(event) => setTargetID(event.target.value)} placeholder="资源 ID" /></Field>
          <Field label="结果"><select value={result} onChange={(event) => setResult(event.target.value as '' | 'succeeded')}><option value="">全部结果</option><option value="succeeded">成功</option></select></Field>
          <Field label="开始时间（可选）"><input type="datetime-local" step="1" value={from} onChange={(event) => setFrom(event.target.value)} /></Field>
          <Field label="结束时间（可选）"><input type="datetime-local" step="1" value={to} onChange={(event) => setTo(event.target.value)} /></Field>
        </div>
        <div className="audit-filter-actions"><span>时间留空时由服务端固定为最近 24 小时；最长 31 天。</span><Button type="submit" disabled={loading}>应用筛选</Button></div>
        <FormError error={validation} />
      </form>
      <PageState loading={loading} error={error} onRetry={retry} />
      {!loading && !error && data ? <>
        <div className="audit-window"><div><span>查询窗口</span><strong>{formatTime(data.from)} — {formatTime(data.to)}</strong></div><div><span>首页水位</span><code>{data.snapshot_at}</code></div><small>第 {pageIndex + 1} 页 · {data.sources.map((source) => sourceLabels[source]).join('、')}</small></div>
        {data.items.length === 0 ? <EmptyState title="当前窗口没有匹配事实" body={`空结果只表示这${financialSource ? '五' : '四'}类来源在固定窗口和筛选条件下没有现存匹配项，不证明没有发生其他管理操作。`} /> : <div className="audit-list">{data.items.map((item) => <article className="audit-event" key={`${item.source}:${item.event_id}`}>
          <header><div><span className="audit-source">{sourceLabels[item.source]}</span><strong>{item.action}</strong></div><span className="audit-result">{item.source === 'financial_commercial' ? '事务已提交' : '成功'}</span></header>
          <dl className="audit-event__grid">
            <div><dt>时间</dt><dd><time dateTime={item.occurred_at}>{formatTime(item.occurred_at)}</time><code>{item.occurred_at}</code></dd></div>
            <div><dt>操作人</dt><dd>{item.actor_id === null ? <span>未关联管理员</span> : <code>{item.actor_id}</code>}</dd></div>
            <div><dt>目标</dt><dd><span>{item.target_type}</span><code>{item.target_id}</code></dd></div>
            <div><dt>事实 ID / revision</dt><dd><code>{item.event_id}</code><span>{item.revision === null ? '无 revision' : `r${item.revision}`}</span></dd></div>
          </dl>
        </article>)}</div>}
        <div className="audit-pagination"><Button variant="secondary" disabled={pageIndex === 0 || loading} onClick={previousPage}>上一页</Button><span>第 {pageIndex + 1} 页</span><Button variant="secondary" disabled={!data.next_cursor || loading} onClick={nextPage}>下一页</Button></div>
      </> : null}
    </section>
  </>
}
