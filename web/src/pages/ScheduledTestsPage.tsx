// Independently implemented from docs/parity-next-batch-2026-09-24.md.
import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api,
  ApiError,
  type ScheduledTestInput,
  type ScheduledTestPlan,
  type ScheduledTestRun,
  type ScheduledTestScope,
  type Upstream,
} from '../api'
import { messageFor } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { PageHeader } from './EmployeesPage'

const scopeLabels: Record<ScheduledTestScope, string> = {
  local_credential: '凭据检查',
  catalog: '模型目录可达',
}

const resultLabels: Record<string, string> = {
  local_credential_ok: '凭据检查通过',
  catalog_ok: '模型目录可达',
  authentication_failed: '凭据不可用',
  rate_limited: '供应商限流',
  unsupported: '当前账号不支持',
  timeout: '测试超时',
  invalid_response: '目录响应无效',
  configuration_changed: '配置已变化',
  stale: '结果已过期',
  cancelled: '已取消',
  interrupted: '服务重启中断',
  test_in_progress: '同账号已有测试',
  capacity_exceeded: '并发容量已满',
  storage_unavailable: '存储暂不可用',
  internal_failure: '内部失败',
}

function when(value: string | null) {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

function intervalLabel(seconds: number) {
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  return `${Math.round(seconds / 60)} 分钟`
}

function runLabel(run: ScheduledTestRun | null) {
  if (!run) return '尚未运行'
  if (run.state === 'running') return '正在运行'
  return resultLabels[run.result_code ?? ''] ?? '测试失败'
}

export function ScheduledTestsPage({ csrf }: { csrf: string }) {
  const [plans, setPlans] = useState<ScheduledTestPlan[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [runningAllowed, setRunningAllowed] = useState(false)
  const [unsupported, setUnsupported] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editor, setEditor] = useState<ScheduledTestPlan | 'create' | null>(null)
  const [history, setHistory] = useState<ScheduledTestPlan | null>(null)
  const [archiving, setArchiving] = useState<ScheduledTestPlan | null>(null)
  const [actionID, setActionID] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const sequence = useRef(0)

  const reload = useCallback(async () => {
    const current = ++sequence.current
    const abort = new AbortController()
    setLoading(true); setError(null); setUnsupported(false)
    try {
      const [status, upstreamPage] = await Promise.all([api.status(), api.upstreams(abort.signal)])
      if (current !== sequence.current) return
      const configurable = status.features?.scheduled_tests_configuration === true
      setRunningAllowed(status.features?.scheduled_tests_running === true)
      setUpstreams(upstreamPage.items)
      if (!configurable) {
        setPlans([]); setUnsupported(true)
        return
      }
      const page = await api.scheduledTests(abort.signal)
      if (current === sequence.current) setPlans(page.items)
    } catch (caught) {
      if (current !== sequence.current || abort.signal.aborted) return
      if (caught instanceof ApiError && caught.status === 404) setUnsupported(true)
      else setError(messageFor(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    return () => { sequence.current += 1 }
  }, [reload])

  async function toggle(plan: ScheduledTestPlan) {
    setActionID(plan.id); setActionError(null)
    try {
      await api.updateScheduledTest(plan.id, { expected_revision: plan.revision, enabled: !plan.enabled }, csrf)
      await reload()
    } catch (caught) {
      setActionError(caught instanceof ApiError && caught.code === 'revision_conflict' ? '计划已被其他操作更新，请重新加载后再试。' : messageFor(caught))
    } finally { setActionID(null) }
  }

  return <>
    <PageHeader title="定时测试" description="按固定间隔检查本地凭据或模型目录可达性；这些结果不代表模型生成可用。">
      <Button disabled={unsupported || loading || upstreams.length === 0} onClick={() => setEditor('create')}><Icon name="plus" />创建计划</Button>
    </PageHeader>
    <section className={`scheduled-boundary ${runningAllowed ? 'scheduled-boundary--running' : ''}`}>
      <div><strong>{runningAllowed ? '后台运行已启用' : '后台运行默认关闭'}</strong><p>{runningAllowed ? '到期计划会由当前服务进程执行，全局最多同时运行 2 个。' : '仍可创建和编辑计划；只有使用 --scheduled-tests-enabled 启动服务后才会执行。'}</p></div>
      <span className={`status status--${runningAllowed ? 'active' : 'disabled'}`}><i />{runningAllowed ? '运行中' : '仅保存配置'}</span>
    </section>
    <div className="content-panel scheduled-panel">
      <PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && unsupported ? <EmptyState title="当前服务不支持定时测试" body="旧服务器不会暴露计划配置或运行能力；升级服务后再使用此页面。" /> : null}
      {!loading && !unsupported && !error && plans.length === 0 ? <EmptyState title="还没有定时测试" body="创建计划后可保存检查范围与间隔。后台开关关闭时不会发出任何测试。" action={upstreams.length ? <Button onClick={() => setEditor('create')}>创建第一个计划</Button> : undefined} /> : null}
      {plans.length ? <div className="table-scroll"><table className="scheduled-table"><thead><tr><th>计划</th><th>检查类型</th><th>间隔 / 下次运行</th><th>最新结果</th><th>状态</th><th>操作</th></tr></thead><tbody>
        {plans.map((plan) => {
          const upstream = upstreams.find((item) => item.id === plan.upstream_id)
          return <tr key={plan.id}>
            <td><strong>{plan.name}</strong><small>{upstream?.name ?? plan.upstream_id}</small></td>
            <td><strong>{scopeLabels[plan.scope]}</strong><small>{plan.scope === 'catalog' ? '只读取模型目录，不发送生成请求' : '只在本机解密并检查凭据格式'}</small></td>
            <td><strong>{intervalLabel(plan.interval_seconds)}</strong><small>{plan.enabled ? `下次：${when(plan.next_run_at)}` : '已停用，不会到期'}</small></td>
            <td><strong>{runLabel(plan.latest_result)}</strong><small>{plan.latest_result ? when(plan.latest_result.finished_at ?? plan.latest_result.started_at) : '—'}</small></td>
            <td><span className={`status status--${plan.enabled ? runningAllowed ? 'active' : 'warning' : 'disabled'}`}><i />{plan.enabled ? runningAllowed ? '已启用' : '等待全局开关' : '已停用'}</span></td>
            <td><div className="row-actions"><button className="link-button" onClick={() => setHistory(plan)}>历史</button><button className="link-button" onClick={() => setEditor(plan)}>编辑</button><button className="link-button" disabled={actionID === plan.id} onClick={() => void toggle(plan)}>{actionID === plan.id ? '处理中…' : plan.enabled ? '停用' : '启用'}</button><button className="link-button scheduled-danger" onClick={() => setArchiving(plan)}>归档</button></div></td>
          </tr>
        })}
      </tbody></table></div> : null}
      {actionError ? <div className="inline-error scheduled-action-error" role="alert">{actionError}<button onClick={() => { setActionError(null); void reload() }}>重新加载</button></div> : null}
    </div>
    {editor ? <PlanEditor plan={editor} upstreams={upstreams} csrf={csrf} onClose={() => setEditor(null)} onSaved={() => { setEditor(null); void reload() }} /> : null}
    {history ? <RunHistory plan={history} onClose={() => setHistory(null)} /> : null}
    {archiving ? <ArchivePlan plan={archiving} csrf={csrf} onClose={() => setArchiving(null)} onArchived={() => { setArchiving(null); void reload() }} /> : null}
  </>
}

function PlanEditor({ plan, upstreams, csrf, onClose, onSaved }: { plan: ScheduledTestPlan | 'create'; upstreams: Upstream[]; csrf: string; onClose: () => void; onSaved: () => void }) {
  const existing = plan === 'create' ? null : plan
  const existingUpstreamMissing = existing !== null && !upstreams.some((item) => item.id === existing.upstream_id)
  const [name, setName] = useState(existing?.name ?? '')
  const [upstreamID, setUpstreamID] = useState(existingUpstreamMissing ? '' : existing?.upstream_id ?? upstreams[0]?.id ?? '')
  const [scope, setScope] = useState<ScheduledTestScope>(existing?.scope ?? 'local_credential')
  const [interval, setInterval] = useState(String(existing?.interval_seconds ?? 300))
  const [enabled, setEnabled] = useState(existing?.enabled ?? false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  return <Dialog title={existing ? '编辑定时测试' : '创建定时测试'} description="时间统一以 UTC 保存；首批仅支持 5 分钟至 24 小时的固定间隔。" onClose={onClose} closeDisabled={busy}>
    <form onSubmit={submitHandler(async () => {
      const seconds = Number(interval)
      if (!Number.isInteger(seconds) || seconds < 300 || seconds > 86400) { setError('间隔必须是 300 到 86400 秒的整数。'); return }
      const body: ScheduledTestInput = { name: name.trim(), upstream_id: upstreamID, scope, interval_seconds: seconds, enabled }
      setBusy(true); setError(null)
      try {
        if (existing) await api.updateScheduledTest(existing.id, { expected_revision: existing.revision, ...body }, csrf)
        else await api.createScheduledTest(body, csrf)
        onSaved()
      } catch (caught) {
        setError(caught instanceof ApiError && caught.code === 'revision_conflict' ? '计划已被其他操作更新，请关闭并重新加载。' : messageFor(caught))
        setBusy(false)
      }
    })}>
      <div className="form-grid scheduled-form">
        <Field label="计划名称"><input value={name} maxLength={120} required autoFocus onChange={(event) => setName(event.target.value)} /></Field>
        <Field label="上游账号"><select value={upstreamID} required onChange={(event) => setUpstreamID(event.target.value)}>{existingUpstreamMissing ? <option value="" disabled>已归档账号 · 请选择替代上游</option> : null}{upstreams.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.provider_kind}</option>)}</select></Field>
        <Field label="检查类型"><select value={scope} onChange={(event) => setScope(event.target.value as ScheduledTestScope)}><option value="local_credential">凭据检查（零出站）</option><option value="catalog">模型目录可达（会访问目录接口）</option></select></Field>
        <Field label="固定间隔（秒）" hint="300–86400 秒，不是 cron，也不提供时区调度。"><input type="number" min="300" max="86400" step="1" value={interval} onChange={(event) => setInterval(event.target.value)} /></Field>
        <label className="toggle-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用此计划</strong><small>全局后台开关关闭时，计划仍只保存而不会执行。</small></span></label>
      </div>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" disabled={busy || !name.trim() || !upstreamID}>{busy ? '正在保存…' : '保存计划'}</Button></div>
    </form>
  </Dialog>
}

function RunHistory({ plan, onClose }: { plan: ScheduledTestPlan; onClose: () => void }) {
  const [items, setItems] = useState<ScheduledTestRun[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [moreLoading, setMoreLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true); setError(null)
    try { const page = await api.scheduledTestRuns(plan.id); setItems(page.items); setCursor(page.next_cursor) }
    catch (caught) { setError(messageFor(caught)) }
    finally { setLoading(false) }
  }, [plan.id])

  useEffect(() => { void load() }, [load])
  async function loadMore() {
    if (!cursor || moreLoading) return
    setMoreLoading(true); setError(null)
    try { const page = await api.scheduledTestRuns(plan.id, cursor); setItems((current) => [...current, ...page.items]); setCursor(page.next_cursor) }
    catch (caught) { setError(messageFor(caught)) }
    finally { setMoreLoading(false) }
  }

  return <Dialog title={`${plan.name} · 运行历史`} description="每页最多 50 条；记录只含固定结果码和时长，不保存响应正文或凭据。" onClose={onClose} wide>
    <PageState loading={loading} error={error && items.length === 0 ? error : null} onRetry={() => void load()} />
    {!loading && items.length === 0 ? <EmptyState title="尚无运行记录" body="计划第一次到期并完成后会显示在这里。" /> : null}
    {items.length ? <div className="scheduled-history">{items.map((run) => <article key={run.operation_id}><header><strong>{runLabel(run)}</strong><span>{scopeLabels[run.scope]}</span></header><dl><div><dt>开始</dt><dd>{when(run.started_at)}</dd></div><div><dt>结束</dt><dd>{when(run.finished_at)}</dd></div><div><dt>耗时</dt><dd>{run.latency_ms === null ? '—' : `${run.latency_ms} ms`}</dd></div><div><dt>计划版本</dt><dd>{run.plan_revision}</dd></div></dl><code>{run.operation_id}</code></article>)}</div> : null}
    {error && items.length ? <FormError error={error} /> : null}
    {cursor ? <div className="dialog__actions"><Button variant="secondary" disabled={moreLoading} onClick={() => void loadMore()}>{moreLoading ? '正在读取…' : '加载更多'}</Button></div> : null}
  </Dialog>
}

function ArchivePlan({ plan, csrf, onClose, onArchived }: { plan: ScheduledTestPlan; csrf: string; onClose: () => void; onArchived: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  return <Dialog title="归档定时测试" description="归档后不能恢复，计划将立即停用；已有运行历史仍保留。" onClose={onClose} closeDisabled={busy}>
    <div className="scheduled-archive-warning"><strong>{plan.name}</strong><p>这不是临时停用。若只想暂停执行，请返回列表使用“停用”。</p></div>
    <FormError error={error} />
    <div className="dialog__actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button variant="danger" disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await api.deleteScheduledTest(plan.id, plan.revision, csrf); onArchived() } catch (caught) { setError(messageFor(caught)); setBusy(false) } }}>{busy ? '正在归档…' : '确认永久归档'}</Button></div>
  </Dialog>
}
