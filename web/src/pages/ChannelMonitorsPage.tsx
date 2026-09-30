// Independently authored from docs/channel-monitor-contract.md.
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, ApiError, type AccountChannel, type ChannelMonitorInput, type ChannelMonitorPlan, type ChannelMonitorRun, type ModelAccount, type ModelRoute, type ScheduledTestScope, type Upstream } from '../api'
import { messageFor } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { PageHeader } from './EmployeesPage'

const scopeLabels: Record<ScheduledTestScope, string> = { local_credential: '本地凭据检查', catalog: '模型目录可达性' }
const resultLabels: Record<string, string> = {
  local_credential_ok: '凭据检查通过', catalog_ok: '模型目录可达', authentication_failed: '凭据不可用',
  rate_limited: '供应商限流', unsupported: '当前账号不支持', timeout: '测试超时',
  invalid_response: '目录响应无效', configuration_changed: '配置已变化', stale: '结果已过期',
  cancelled: '已取消', interrupted: '服务重启中断', test_in_progress: '同账号已有测试',
  capacity_exceeded: '并发容量已满', storage_unavailable: '存储暂不可用', internal_failure: '内部失败',
}

function utc(value: string | null) {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '无效时间' : date.toISOString().replace('.000Z', 'Z')
}

function resultLabel(run: ChannelMonitorRun | null) {
  if (!run) return '尚无结果'
  if (run.state === 'running') return '正在运行'
  return resultLabels[run.result_code ?? ''] ?? '测试失败'
}

type Directory = { channels: AccountChannel[]; models: ModelRoute[]; upstreams: Upstream[] }

export function ChannelMonitorsPage({ csrf }: { csrf: string }) {
  const [plans, setPlans] = useState<ChannelMonitorPlan[]>([])
  const [directory, setDirectory] = useState<Directory>({ channels: [], models: [], upstreams: [] })
  const [running, setRunning] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editor, setEditor] = useState<ChannelMonitorPlan | 'create' | null>(null)
  const [history, setHistory] = useState<ChannelMonitorPlan | null>(null)
  const [archiving, setArchiving] = useState<ChannelMonitorPlan | null>(null)
  const [actionID, setActionID] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const sequence = useRef(0)

  const reload = useCallback(async () => {
    const current = ++sequence.current
    setLoading(true); setError(null)
    try {
      const [status, channels, models, upstreams, plansPage] = await Promise.all([
        api.status(), api.accountChannels(), api.models(), api.upstreams(), api.channelMonitors(),
      ])
      if (current !== sequence.current) return
      setRunning(status.features?.channel_monitor_running === true)
      setDirectory({ channels: channels.items, models: models.items, upstreams: upstreams.items })
      setPlans(plansPage.items)
    } catch (caught) {
      if (current === sequence.current) setError(messageFor(caught))
    } finally { if (current === sequence.current) setLoading(false) }
  }, [])

  useEffect(() => { void reload(); return () => { sequence.current += 1 } }, [reload])

  async function toggle(plan: ChannelMonitorPlan) {
    if (!plan.enabled && plan.binding_state === 'stale') { setEditor(plan); return }
    setActionID(plan.id); setActionError(null)
    try {
      await api.updateChannelMonitor(plan.id, { expected_revision: plan.revision, enabled: !plan.enabled }, csrf)
      await reload()
    } catch (caught) {
      setActionError(caught instanceof ApiError && caught.code === 'revision_conflict' ? '计划已被修改，请重新加载。' : messageFor(caught))
    } finally { setActionID(null) }
  }

  return <>
    <PageHeader title="渠道监控" description="对选定渠道的现有模型与显式账号池路由做凭据或目录检查；不发送生成请求，也不代表生成可用。">
      <Button disabled={loading || directory.channels.length === 0 || directory.models.length === 0} onClick={() => setEditor('create')}><Icon name="plus" />创建计划</Button>
    </PageHeader>
    <section className={`scheduled-boundary ${running ? 'scheduled-boundary--running' : ''}`}>
      <div><strong>{running ? '后台运行已启用' : '后台运行默认关闭'}</strong><p>{running ? '到期计划由本服务进程执行；最多同时 2 个，每个上游账号最多 1 个。' : '可以保存计划；仅使用 --channel-monitors-enabled 启动服务后才会执行。'}</p></div>
      <span className={`status status--${running ? 'active' : 'disabled'}`}><i />{running ? '运行中' : '仅保存配置'}</span>
    </section>
    <div className="content-panel scheduled-panel">
      <PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && plans.length === 0 ? <EmptyState title="还没有渠道监控" body="先在模型路由中配置一个带渠道的显式账号池路由，再创建检查计划。" /> : null}
      {plans.length > 0 ? <div className="table-scroll"><table className="scheduled-table"><thead><tr><th>计划 / 绑定</th><th>检查</th><th>下次运行 UTC</th><th>最新结果</th><th>状态</th><th>操作</th></tr></thead><tbody>
        {plans.map((plan) => {
          const channel = directory.channels.find((item) => item.id === plan.channel_id)
          const upstream = directory.upstreams.find((item) => item.id === plan.upstream_id)
          return <tr key={plan.id}>
            <td><strong>{plan.name}</strong><small>{channel?.name ?? plan.channel_id} · {plan.model_id} · {upstream?.name ?? plan.upstream_id}</small></td>
            <td><strong>{scopeLabels[plan.scope]}</strong><small>每 {plan.interval_seconds} 秒</small></td>
            <td><strong>{plan.enabled ? utc(plan.next_run_at) : '已停用'}</strong><small>时间按 UTC 保存</small></td>
            <td><strong>{plan.binding_state === 'stale' ? '绑定已失效' : resultLabel(plan.latest_result)}</strong><small>{plan.latest_result ? utc(plan.latest_result.finished_at ?? plan.latest_result.started_at) : '—'}</small></td>
            <td><span className={`status status--${plan.binding_state === 'stale' ? 'warning' : plan.enabled ? running ? 'active' : 'warning' : 'disabled'}`}><i />{plan.binding_state === 'stale' ? '需重新绑定' : plan.enabled ? running ? '已启用' : '等待全局开关' : '已停用'}</span></td>
            <td><div className="row-actions"><button className="link-button" onClick={() => setHistory(plan)}>历史</button><button className="link-button" onClick={() => setEditor(plan)}>{plan.binding_state === 'stale' ? '重新绑定' : '编辑'}</button><button className="link-button" disabled={actionID === plan.id} onClick={() => void toggle(plan)}>{plan.enabled ? '停用' : '启用'}</button><button className="link-button scheduled-danger" onClick={() => setArchiving(plan)}>归档</button></div></td>
          </tr>
        })}
      </tbody></table></div> : null}
      {actionError ? <div className="inline-error" role="alert">{actionError}<button onClick={() => { setActionError(null); void reload() }}>重新加载</button></div> : null}
    </div>
    {editor ? <PlanEditor key={editor === 'create' ? 'create' : editor.id} plan={editor} directory={directory} csrf={csrf} onClose={() => setEditor(null)} onSaved={() => { setEditor(null); void reload() }} /> : null}
    {history ? <RunHistory plan={history} onClose={() => setHistory(null)} /> : null}
    {archiving ? <ArchivePlan plan={archiving} csrf={csrf} onClose={() => setArchiving(null)} onArchived={() => { setArchiving(null); void reload() }} /> : null}
  </>
}

function PlanEditor({ plan, directory, csrf, onClose, onSaved }: { plan: ChannelMonitorPlan | 'create'; directory: Directory; csrf: string; onClose: () => void; onSaved: () => void }) {
  const existing = plan === 'create' ? null : plan
  const [name, setName] = useState(existing?.name ?? '')
  const [channelID, setChannelID] = useState(existing?.channel_id ?? directory.channels[0]?.id ?? '')
  const [modelID, setModelID] = useState(existing?.model_id ?? directory.models.find((item) => item.enabled && !item.archived)?.id ?? '')
  const [upstreamID, setUpstreamID] = useState(existing?.upstream_id ?? '')
  const [scope, setScope] = useState<ScheduledTestScope>(existing?.scope ?? 'local_credential')
  const [interval, setInterval] = useState(String(existing?.interval_seconds ?? 300))
  const [enabled, setEnabled] = useState(existing?.enabled ?? false)
  const [rebindConfirmed, setRebindConfirmed] = useState(false)
  const [routes, setRoutes] = useState<ModelAccount[]>([])
  const [routesLoading, setRoutesLoading] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let active = true
    setRoutes([])
    if (!modelID) return () => { active = false }
    setRoutesLoading(true)
    api.modelAccounts(modelID).then((page) => { if (active) setRoutes(page.items) })
      .catch((caught) => { if (active) setError(messageFor(caught)) })
      .finally(() => { if (active) setRoutesLoading(false) })
    return () => { active = false }
  }, [modelID])

  const options = useMemo(() => routes.filter((item) => item.channel_id === channelID && directory.upstreams.some((upstream) => upstream.id === item.upstream_id && upstream.enabled && !upstream.archived)), [routes, channelID, directory.upstreams])
  const selectedRoute = options.find((item) => item.upstream_id === upstreamID)
  const bindingChange = !!existing && (existing.channel_id !== channelID || existing.model_id !== modelID || existing.upstream_id !== upstreamID || existing.binding_state === 'stale')

  return <Dialog title={existing ? '编辑渠道监控' : '创建渠道监控'} description="计划固定到选定的渠道、模型与显式账号池路由。路由或账号配置变化后，必须人工确认重新绑定。" onClose={onClose} closeDisabled={busy}>
    <form onSubmit={submitHandler(async () => {
      const seconds = Number(interval)
      if (!selectedRoute || !Number.isInteger(seconds) || seconds < 300 || seconds > 86400 || !name.trim()) { setError('请选择当前渠道中的有效显式路由，并填写 300–86400 秒的整数间隔。'); return }
      if (bindingChange && !rebindConfirmed) { setError('请确认重新绑定到当前选定路由。'); return }
      const body: ChannelMonitorInput = { name: name.trim(), channel_id: channelID, model_id: modelID, upstream_id: upstreamID, scope, interval_seconds: seconds, enabled }
      setBusy(true); setError(null)
      try {
        if (existing) await api.updateChannelMonitor(existing.id, { expected_revision: existing.revision, ...body, ...(bindingChange || rebindConfirmed ? { rebind: true as const } : {}) }, csrf)
        else await api.createChannelMonitor(body, csrf)
        onSaved()
      } catch (caught) { setError(caught instanceof ApiError && caught.code === 'revision_conflict' ? '计划已被修改，请关闭并重新加载。' : messageFor(caught)); setBusy(false) }
    })}>
      <div className="form-grid scheduled-form">
        <Field label="计划名称"><input required autoFocus maxLength={120} value={name} onChange={(event) => setName(event.target.value)} /></Field>
        <Field label="渠道"><select required value={channelID} onChange={(event) => { setChannelID(event.target.value); setUpstreamID(''); setRebindConfirmed(false) }}>{directory.channels.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>
        <Field label="对外模型"><select required value={modelID} onChange={(event) => { setModelID(event.target.value); setUpstreamID(''); setRebindConfirmed(false) }}>{directory.models.filter((item) => item.enabled && !item.archived).map((item) => <option key={item.id} value={item.id}>{item.id}</option>)}</select></Field>
        <Field label="显式账号池路由" hint="只显示属于所选渠道、且当前启用的上游账号。"><select required value={selectedRoute ? upstreamID : ''} disabled={routesLoading} onChange={(event) => { setUpstreamID(event.target.value); setRebindConfirmed(false) }}><option value="">{routesLoading ? '正在读取路由…' : '请选择路由'}</option>{options.map((item) => <option key={item.upstream_id} value={item.upstream_id}>{directory.upstreams.find((upstream) => upstream.id === item.upstream_id)?.name ?? item.upstream_id} · {item.upstream_model}</option>)}</select></Field>
        <Field label="检查类型"><select value={scope} onChange={(event) => setScope(event.target.value as ScheduledTestScope)}><option value="local_credential">本地凭据检查（零出站）</option><option value="catalog">模型目录可达（访问目录接口）</option></select></Field>
        <Field label="固定间隔（秒）" hint="300–86400 秒；下次运行按 UTC 保存。"><input type="number" min="300" max="86400" step="1" value={interval} onChange={(event) => setInterval(event.target.value)} /></Field>
        <label className="toggle-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用此计划</strong><small>全局后台开关关闭时仅保存，不执行。</small></span></label>
        {existing && (bindingChange || rebindConfirmed) ? <label className="toggle-field"><input type="checkbox" checked={rebindConfirmed} onChange={(event) => setRebindConfirmed(event.target.checked)} /><span><strong>确认重新绑定</strong><small>将后续检查绑定到当前渠道、模型与账号池路由；历史记录仍保留原绑定快照。</small></span></label> : null}
      </div>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" disabled={busy || routesLoading || !selectedRoute || !name.trim() || (bindingChange && !rebindConfirmed)}>{busy ? '正在保存…' : '保存计划'}</Button></div>
    </form>
  </Dialog>
}

function RunHistory({ plan, onClose }: { plan: ChannelMonitorPlan; onClose: () => void }) {
  const [items, setItems] = useState<ChannelMonitorRun[]>([])
  const [cursor, setCursor] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [moreLoading, setMoreLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const load = useCallback(async () => {
    setLoading(true); setError(null)
    try { const page = await api.channelMonitorRuns(plan.id); setItems(page.items); setCursor(page.next_cursor) }
    catch (caught) { setError(messageFor(caught)) }
    finally { setLoading(false) }
  }, [plan.id])
  useEffect(() => { void load() }, [load])
  async function loadMore() {
    if (!cursor || moreLoading) return
    setMoreLoading(true); setError(null)
    try { const page = await api.channelMonitorRuns(plan.id, cursor); setItems((current) => [...current, ...page.items]); setCursor(page.next_cursor) }
    catch (caught) { setError(messageFor(caught)) }
    finally { setMoreLoading(false) }
  }
  return <Dialog title={`${plan.name} · 运行历史`} description="最多保留 200 条已完成记录；只存结果码、绑定快照与时长，不保存凭据或响应正文。" onClose={onClose} wide>
    <PageState loading={loading} error={error && items.length === 0 ? error : null} onRetry={() => void load()} />
    {!loading && items.length === 0 ? <EmptyState title="尚无运行记录" body="计划到期并执行后会显示在这里。" /> : null}
    {items.length > 0 ? <div className="scheduled-history">{items.map((run) => <article key={run.operation_id}><header><strong>{resultLabel(run)}</strong><span>{scopeLabels[run.scope]}</span></header><dl><div><dt>开始 UTC</dt><dd>{utc(run.started_at)}</dd></div><div><dt>结束 UTC</dt><dd>{utc(run.finished_at)}</dd></div><div><dt>耗时</dt><dd>{run.latency_ms === null ? '—' : `${run.latency_ms} ms`}</dd></div><div><dt>计划版本</dt><dd>{run.plan_revision}</dd></div></dl><small>渠道 {run.channel_id} · 模型 {run.model_id} · 上游 {run.upstream_id} · 路由 {run.route_upstream_model} ({run.route_wire_protocol})</small></article>)}</div> : null}
    {error && items.length > 0 ? <FormError error={error} /> : null}
    {cursor ? <div className="dialog__actions"><Button variant="secondary" disabled={moreLoading} onClick={() => void loadMore()}>{moreLoading ? '正在读取…' : '加载更多'}</Button></div> : null}
  </Dialog>
}

function ArchivePlan({ plan, csrf, onClose, onArchived }: { plan: ChannelMonitorPlan; csrf: string; onClose: () => void; onArchived: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  return <Dialog title="归档渠道监控" description="归档后计划立即停用且不可恢复；已有运行历史仍保留。" onClose={onClose} closeDisabled={busy}>
    <div className="scheduled-archive-warning"><strong>{plan.name}</strong><p>若只想暂停，请返回列表使用“停用”。</p></div>
    <FormError error={error} />
    <div className="dialog__actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button variant="danger" disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await api.deleteChannelMonitor(plan.id, plan.revision, csrf); onArchived() } catch (caught) { setError(messageFor(caught)); setBusy(false) } }}>{busy ? '正在归档…' : '确认归档'}</Button></div>
  </Dialog>
}
