// Independently implemented from docs/adr/0002-automated-backup-key-custody.md.
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, type BackupKeyProvider, type BackupPlan, type BackupPlanInput, type BackupRun } from '../api'
import { messageFor } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { PageHeader } from './EmployeesPage'

const runLabels: Record<BackupRun['status'], string> = {
  running: '执行中',
  succeeded: '成功',
  failed: '失败',
  cancelled: '已取消',
  interrupted: '服务重启中断',
}

const reasonLabels: Record<string, string> = {
  unsupported_platform: '当前系统没有可用的受保护密钥提供方',
  protected_store_invalid: '受保护密钥存储不可用',
  provisioning_interrupted: '密钥初始化曾被中断',
  rotation_interrupted: '密钥轮换曾被中断',
}

function when(value: string | null | undefined) {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

function intervalLabel(seconds: number) {
  if (seconds % 86400 === 0) return `${seconds / 86400} 天`
  if (seconds % 3600 === 0) return `${seconds / 3600} 小时`
  return `${Math.round(seconds / 60)} 分钟`
}

function sizeLabel(size: number | null) {
  if (size === null) return '—'
  if (size < 1024) return `${size} B`
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KiB`
  return `${(size / 1024 / 1024).toFixed(1)} MiB`
}

function actionMessage(error: unknown) {
  if (error instanceof ApiError) {
    if (error.code === 'revision_conflict') return '配置已被其他管理员更新，请重新加载后再试。'
    if (error.code === 'key_provider_unavailable') return '当前机器的受保护密钥存储不可用。'
    if (error.code === 'backup_worker_disabled') return '服务未启用自动备份执行。'
    if (error.code === 'backup_not_runnable') return '当前已有备份在运行，或计划配置已变化。'
  }
  return messageFor(error)
}

export function BackupsPage({ csrf }: { csrf: string }) {
  const [providers, setProviders] = useState<BackupKeyProvider[]>([])
  const [plans, setPlans] = useState<BackupPlan[]>([])
  const [runs, setRuns] = useState<BackupRun[]>([])
  const [runningAllowed, setRunningAllowed] = useState(false)
  const [providerReady, setProviderReady] = useState(false)
  const [providerStoreReady, setProviderStoreReady] = useState(false)
  const [providerReason, setProviderReason] = useState<string | null>(null)
  const [unsupported, setUnsupported] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editor, setEditor] = useState<BackupPlan | 'create' | null>(null)
  const [keyAction, setKeyAction] = useState<BackupKeyProvider | 'create' | null>(null)
  const [actionID, setActionID] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const sequence = useRef(0)

  const reload = useCallback(async (quiet = false) => {
    const current = ++sequence.current
    const abort = new AbortController()
    if (!quiet) setLoading(true)
    setError(null); setUnsupported(false)
    try {
      const status = await api.status()
      if (current !== sequence.current) return
      if (status.features?.automated_backups_configuration !== true) {
        setUnsupported(true); setProviders([]); setPlans([]); setRuns([])
        return
      }
      setRunningAllowed(status.features?.automated_backups_running === true)
      setProviderReady(status.features?.backup_key_provider_ready === true)
      const [providerPage, planPage, runPage] = await Promise.all([
        api.backupKeyProviders(abort.signal), api.backupPlans(abort.signal), api.backupRuns(undefined, undefined, abort.signal),
      ])
      if (current !== sequence.current) return
      setProviders(providerPage.items); setPlans(planPage.items); setRuns(runPage.items)
      setProviderReady(providerPage.ready)
      setProviderStoreReady(providerPage.store_ready)
      setProviderReason(providerPage.reason_code)
    } catch (caught) {
      if (current !== sequence.current || abort.signal.aborted) return
      if (caught instanceof ApiError && caught.status === 404) setUnsupported(true)
      else setError(actionMessage(caught))
    } finally {
      if (current === sequence.current) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    return () => { sequence.current += 1 }
  }, [reload])

  const hasRunning = runs.some((run) => run.status === 'running')
  useEffect(() => {
    if (!hasRunning || unsupported) return
    const timer = window.setInterval(() => { void reload(true) }, 3000)
    return () => window.clearInterval(timer)
  }, [hasRunning, reload, unsupported])

  async function runNow(plan: BackupPlan) {
    setActionID(plan.id); setActionError(null)
    try { await api.createBackupRun(plan.id, plan.revision, csrf); await reload(true) }
    catch (caught) { setActionError(actionMessage(caught)) }
    finally { setActionID(null) }
  }

  async function togglePlan(plan: BackupPlan) {
    setActionID(plan.id); setActionError(null)
    try {
      if (plan.enabled) await api.disableBackupPlan(plan.id, plan.revision, csrf)
      else await api.updateBackupPlan(plan.id, { expected_revision: plan.revision, enabled: true }, csrf)
      await reload(true)
    } catch (caught) { setActionError(actionMessage(caught)) }
    finally { setActionID(null) }
  }

  async function cancelRun(run: BackupRun) {
    setActionID(run.id); setActionError(null)
    try { await api.cancelBackupRun(run.id, csrf); await reload(true) }
    catch (caught) { setActionError(actionMessage(caught)) }
    finally { setActionID(null) }
  }

  return <>
    <PageHeader title="自动备份" description="创建经过认证加密的本机备份，并可在隔离目录中执行完整恢复演练。">
      <div className="page-header-buttons"><Button variant="secondary" disabled={!providerStoreReady || loading} onClick={() => setKeyAction('create')}><Icon name="key" />创建密钥</Button><Button disabled={!providerReady || providers.length === 0 || loading} onClick={() => setEditor('create')}><Icon name="plus" />创建计划</Button></div>
    </PageHeader>
    <section className={`backup-boundary ${runningAllowed && providerReady ? 'backup-boundary--ready' : ''}`}>
      <div><strong>{runningAllowed ? '自动执行已启用' : '自动执行默认关闭'}</strong><p>{providerReady ? '备份密钥由当前 Windows 用户的 DPAPI 保护；服务不会在数据库或接口中返回明文密钥。' : reasonLabels[providerReason ?? ''] ?? '当前没有可解析的活动备份密钥。'}</p></div>
      <div className="backup-boundary__states"><span className={`status status--${runningAllowed ? 'active' : 'warning'}`}><i />{runningAllowed ? '调度运行中' : '仅保存配置'}</span><span className={`status status--${providerReady ? 'active' : 'danger'}`}><i />{providerReady ? '密钥就绪' : '密钥不可用'}</span></div>
    </section>
    <div className="backup-recovery-limit"><strong>异机恢复尚未提供</strong><span>DPAPI 用户范围密钥通常绑定当前 Windows 用户与主机；当前版本没有密钥导出、异机导入或重新封装流程。</span></div>
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {!loading && unsupported ? <div className="content-panel"><EmptyState title="当前服务不支持自动备份" body="升级服务并启用该能力后，管理员才会看到备份配置。" /></div> : null}
    {!loading && !unsupported && !error ? <>
      <section className="backup-section">
        <header><div><h2>密钥提供方</h2><p>轮换会创建新版本；已有备份仍使用运行时记录的旧版本。</p></div></header>
        <div className="content-panel">
          {providers.length === 0 ? <EmptyState title="还没有备份密钥" body={providerStoreReady ? '创建第一个由当前 Windows 用户保护的备份密钥。' : '此部署目前不能创建可无人值守使用的系统保护密钥。'} action={providerStoreReady ? <Button onClick={() => setKeyAction('create')}>创建密钥</Button> : undefined} /> : <div className="table-scroll"><table className="backup-table"><thead><tr><th>提供方</th><th>保护范围</th><th>活动版本</th><th>状态</th><th>操作</th></tr></thead><tbody>{providers.map((provider) => <tr key={provider.id}><td><strong>Windows DPAPI</strong><small>{provider.id}</small></td><td><strong>当前服务用户</strong><small>不可跨用户或跨机器直接解封</small></td><td><strong>v{provider.active_version}</strong><small>配置版本 {provider.revision}</small></td><td><span className={`status status--${provider.status === 'ready' ? 'active' : 'danger'}`}><i />{provider.status === 'ready' ? '可用' : reasonLabels[provider.reason_code ?? ''] ?? '不可用'}</span></td><td><button className="link-button" disabled={provider.status !== 'ready'} onClick={() => setKeyAction(provider)}>轮换 / 回退</button></td></tr>)}</tbody></table></div>}
        </div>
      </section>
      <section className="backup-section">
        <header><div><h2>备份计划</h2><p>固定间隔为 15 分钟至 31 天；保留数量只作用于系统可证明归属的包。</p></div></header>
        <div className="content-panel">
          {plans.length === 0 ? <EmptyState title="还没有备份计划" body="计划可先保存为停用状态；打开全局执行开关后才会自动运行。" action={providers.length ? <Button onClick={() => setEditor('create')}>创建第一个计划</Button> : undefined} /> : <div className="table-scroll"><table className="backup-table backup-plan-table"><thead><tr><th>计划</th><th>间隔 / 保留</th><th>恢复演练</th><th>下次运行</th><th>最新结果</th><th>操作</th></tr></thead><tbody>{plans.map((plan) => <tr key={plan.id}><td><strong>{plan.name}</strong><small>{plan.enabled ? '已启用' : '已停用'} · 版本 {plan.revision}</small></td><td><strong>{intervalLabel(plan.interval_seconds)}</strong><small>保留 {plan.retention_count} 个包</small></td><td><span className={`status status--${plan.rehearsal_enabled ? 'active' : 'disabled'}`}><i />{plan.rehearsal_enabled ? '每次执行' : '仅校验包'}</span></td><td><strong>{plan.enabled ? when(plan.next_run_at) : '—'}</strong><small>{plan.enabled && !runningAllowed ? '等待全局开关' : ''}</small></td><td><strong>{plan.latest_run ? runLabels[plan.latest_run.status] : '尚未运行'}</strong><small>{plan.latest_run ? when(plan.latest_run.finished_at ?? plan.latest_run.started_at) : '—'}</small></td><td><div className="row-actions"><button className="link-button" onClick={() => setEditor(plan)}>编辑</button><button className="link-button" disabled={actionID === plan.id} onClick={() => void togglePlan(plan)}>{plan.enabled ? '停用' : '启用'}</button><button className="link-button" disabled={!runningAllowed || !providerReady || hasRunning || actionID === plan.id} onClick={() => void runNow(plan)}>立即运行</button></div></td></tr>)}</tbody></table></div>}
        </div>
      </section>
      <section className="backup-section">
        <header><div><h2>最近运行</h2><p>运行记录包含密钥版本、校验与演练结果，但不暴露服务器文件路径。</p></div></header>
        <div className="content-panel">{runs.length === 0 ? <EmptyState title="尚无备份运行" body="计划自动到期或管理员手动运行后会显示在这里。" /> : <div className="table-scroll"><table className="backup-table backup-run-table"><thead><tr><th>运行</th><th>状态</th><th>密钥版本</th><th>包</th><th>校验 / 演练</th><th>操作</th></tr></thead><tbody>{runs.map((run) => <tr key={run.id}><td><strong>{run.trigger_kind === 'manual' ? '手动运行' : '计划运行'}</strong><small>{when(run.started_at)} · {run.id}</small></td><td><span className={`status status--${run.status === 'succeeded' ? 'active' : run.status === 'running' ? 'warning' : 'danger'}`}><i />{runLabels[run.status]}</span>{run.error_code ? <small>{run.error_code}</small> : null}</td><td><strong>v{run.key_provider_version}</strong><small>{run.key_provider_id}</small></td><td><strong>{sizeLabel(run.package_size)}</strong><small>{run.package_retained ? '已保留' : run.package_deleted_at ? '已按策略删除' : '未保留'}</small></td><td><strong>{run.verified_at ? '校验通过' : '未完成校验'}</strong><small>{run.rehearsal_status === 'succeeded' ? '恢复演练通过' : run.rehearsal_status === 'skipped' ? '未要求演练' : run.rehearsal_status === 'pending' ? '等待演练' : '恢复演练失败'}</small></td><td>{run.status === 'running' ? <button className="link-button backup-danger" disabled={actionID === run.id} onClick={() => void cancelRun(run)}>取消</button> : '—'}</td></tr>)}</tbody></table></div>}</div>
      </section>
      {actionError ? <div className="inline-error backup-action-error" role="alert">{actionError}<button onClick={() => { setActionError(null); void reload() }}>重新加载</button></div> : null}
    </> : null}
    {editor ? <BackupPlanEditor plan={editor} providers={providers.filter((item) => item.status === 'ready')} csrf={csrf} onClose={() => setEditor(null)} onSaved={() => { setEditor(null); void reload() }} /> : null}
    {keyAction ? <BackupKeyDialog provider={keyAction} csrf={csrf} onClose={() => setKeyAction(null)} onSaved={() => { setKeyAction(null); void reload() }} /> : null}
  </>
}

function BackupPlanEditor({ plan, providers, csrf, onClose, onSaved }: { plan: BackupPlan | 'create'; providers: BackupKeyProvider[]; csrf: string; onClose: () => void; onSaved: () => void }) {
  const existing = plan === 'create' ? null : plan
  const [name, setName] = useState(existing?.name ?? '')
  const [providerID, setProviderID] = useState(existing?.key_provider_id ?? providers[0]?.id ?? '')
  const [interval, setInterval] = useState(String(existing?.interval_seconds ?? 86400))
  const [retention, setRetention] = useState(String(existing?.retention_count ?? 7))
  const [rehearsal, setRehearsal] = useState(existing?.rehearsal_enabled ?? true)
  const [enabled, setEnabled] = useState(existing?.enabled ?? false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  return <Dialog title={existing ? '编辑备份计划' : '创建备份计划'} description="所有时间以 UTC 保存；接口不会接收或返回备份目录路径。" onClose={onClose} closeDisabled={busy}>
    <form onSubmit={submitHandler(async () => {
      const seconds = Number(interval); const keep = Number(retention)
      if (!Number.isInteger(seconds) || seconds < 900 || seconds > 2678400) { setError('间隔必须是 900 到 2678400 秒的整数。'); return }
      if (!Number.isInteger(keep) || keep < 1 || keep > 365) { setError('保留数量必须是 1 到 365 的整数。'); return }
      const body: BackupPlanInput = { name: name.trim(), key_provider_id: providerID, interval_seconds: seconds, retention_count: keep, rehearsal_enabled: rehearsal, enabled }
      setBusy(true); setError(null)
      try {
        if (existing) await api.updateBackupPlan(existing.id, { expected_revision: existing.revision, ...body }, csrf)
        else await api.createBackupPlan(body, csrf)
        onSaved()
      } catch (caught) { setError(actionMessage(caught)); setBusy(false) }
    })}>
      <div className="form-grid backup-form"><Field label="计划名称"><input value={name} maxLength={120} required autoFocus onChange={(event) => setName(event.target.value)} /></Field><Field label="密钥提供方"><select value={providerID} required onChange={(event) => setProviderID(event.target.value)}>{providers.map((provider) => <option key={provider.id} value={provider.id}>Windows DPAPI · v{provider.active_version}</option>)}</select></Field><Field label="固定间隔（秒）" hint="900–2678400 秒"><input type="number" min="900" max="2678400" step="1" value={interval} onChange={(event) => setInterval(event.target.value)} /></Field><Field label="保留包数量" hint="1–365 个"><input type="number" min="1" max="365" step="1" value={retention} onChange={(event) => setRetention(event.target.value)} /></Field><label className="toggle-field"><input type="checkbox" checked={rehearsal} onChange={(event) => setRehearsal(event.target.checked)} /><span><strong>执行恢复演练</strong><small>在隔离临时目录恢复并打开数据库，然后清理已知文件。</small></span></label><label className="toggle-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用此计划</strong><small>全局执行开关关闭时仍只保存配置。</small></span></label></div>
      <FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button type="submit" disabled={busy || !name.trim() || !providerID}>{busy ? '正在保存…' : '保存计划'}</Button></div>
    </form>
  </Dialog>
}

function BackupKeyDialog({ provider, csrf, onClose, onSaved }: { provider: BackupKeyProvider | 'create'; csrf: string; onClose: () => void; onSaved: () => void }) {
  const existing = provider === 'create' ? null : provider
  const [version, setVersion] = useState(existing ? String(existing.active_version) : '1')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  if (!existing) return <Dialog title="创建备份密钥" description="将生成随机包装密钥，并用当前 Windows 服务用户的 DPAPI 保护。明文不会显示。" onClose={onClose} closeDisabled={busy}><div className="backup-key-warning"><strong>恢复边界</strong><p>同一用户配置文件可在服务重启后解封；复制数据库和备份包本身不能绕过 DPAPI 保护。</p></div><FormError error={error} /><div className="dialog__actions"><Button variant="secondary" disabled={busy} onClick={onClose}>取消</Button><Button disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await api.createBackupKeyProvider(csrf); onSaved() } catch (caught) { setError(actionMessage(caught)); setBusy(false) } }}>{busy ? '正在保护密钥…' : '创建受保护密钥'}</Button></div></Dialog>

  return <Dialog title="轮换或回退密钥" description={`当前活动版本为 v${existing.active_version}；轮换不会删除旧版本。`} onClose={onClose} closeDisabled={busy}><div className="backup-key-actions"><section><h3>轮换到新版本</h3><p>生成新的随机密钥并原子切换活动版本。正在运行的备份继续使用已固定的版本。</p><Button disabled={busy} onClick={async () => { setBusy(true); setError(null); try { await api.rotateBackupKeyProvider(existing.id, existing.revision, csrf); onSaved() } catch (caught) { setError(actionMessage(caught)); setBusy(false) } }}>轮换到 v{existing.active_version + 1}</Button></section><section><h3>激活已有版本</h3><p>仅能激活本机受保护存储中仍可成功解封的版本。</p><Field label="版本号"><input type="number" min="1" max="9007199254740991" step="1" value={version} onChange={(event) => setVersion(event.target.value)} /></Field><Button variant="secondary" disabled={busy || !Number.isSafeInteger(Number(version)) || Number(version) < 1} onClick={async () => { setBusy(true); setError(null); try { await api.activateBackupKeyProviderVersion(existing.id, existing.revision, Number(version), csrf); onSaved() } catch (caught) { setError(actionMessage(caught)); setBusy(false) } }}>激活所选版本</Button></section></div><FormError error={error} /></Dialog>
}
