import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api,
  ApiError,
  type Employee,
  type EmployeeKey,
  type GovernanceBudgetLimits,
  type GovernanceGroup,
  type GovernanceHardLimits,
  type GovernancePolicy,
  type GovernancePolicyInput,
  type GovernanceReceipt,
  type GovernanceScopeKind,
  type GovernanceSettings,
  type GovernanceShadowLimits,
} from '../api'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { GovernanceObservations } from '../GovernanceObservations'
import { PageHeader } from './EmployeesPage'

type TargetKey = EmployeeKey & { employeeID: string; employeeName: string }
type FrozenWrite = {
  operationID: string
  kind: GovernanceReceipt['resource_kind']
  resourceID?: string
  send: () => Promise<GovernanceReceipt>
  after: (receipt: GovernanceReceipt) => Promise<void>
  conflict?: () => Promise<void>
}

function governanceMessage(error: unknown) {
  if (!(error instanceof ApiError)) return '无法确认写入结果。页面已保留原操作编号与载荷。'
  const fixed: Record<string, string> = {
    operation_conflict: '该操作编号已绑定其他写入；页面不会覆盖或自动更换编号。',
    revision_conflict: '配置已被其他操作更新。页面已重新读取当前状态，请确认后再提交新操作。',
    invalid_request: '治理配置无效，请检查字段与范围。',
    not_found: '治理对象不存在或已被删除。',
    storage_unavailable: '治理存储暂时不可用。写入结果可能未知，页面将保留原操作。',
  }
  return fixed[error.code] ?? (error.status === 403 ? '当前会话无权管理治理配置。' : '治理操作未完成，请按当前状态处理。')
}

function receiptMatches(receipt: GovernanceReceipt, frozen: FrozenWrite) {
  return receipt.operation_id === frozen.operationID && receipt.resource_kind === frozen.kind &&
    (!frozen.resourceID || receipt.resource_id === frozen.resourceID) && receipt.resource_id.length > 0 &&
    Number.isSafeInteger(receipt.revision) && receipt.revision > 0
}

function useGovernanceWrite() {
  const frozen = useRef<FrozenWrite | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [pending, setPending] = useState(false)
  const [receipt, setReceipt] = useState<GovernanceReceipt | null>(null)
  const [operationID, setOperationID] = useState<string | null>(null)
  const [conflictPending, setConflictPending] = useState(false)

  async function handleConflict(current: FrozenWrite) {
    if (!current.conflict) { setError('配置版本已变化；当前对象无法安全继续编辑。'); return }
    try {
      await current.conflict()
      setConflictPending(false)
      setError('配置已被其他操作更新。页面已读取当前状态，请确认后再提交新操作。')
    } catch {
      setConflictPending(true)
      setError('检测到版本冲突，但无法读取服务器当前状态。读取成功前不会允许新写入。')
    }
  }

  async function finish(next: GovernanceReceipt, current: FrozenWrite) {
    if (!receiptMatches(next, current)) {
      setPending(true)
      setError('服务返回的回执与冻结操作不匹配。页面不会继续写入或自动换号。')
      return
    }
    setPending(false); setReceipt(next); setError(null)
    try { await current.after(next) }
    catch { setError('写入回执已确认，但当前资源读取失败。请只重新读取当前资源，不要创建新操作。') }
  }

  async function queryCurrent(current: FrozenWrite) {
    try { await finish(await api.governanceOperation(current.operationID), current) }
    catch (caught) {
      setPending(true)
      setError(caught instanceof ApiError && caught.status === 404
        ? '服务暂未查到原操作；这不表示写入未执行。请查询原回执或用完全相同的操作重试。'
        : '无法确认原操作状态。页面已保留原操作编号与载荷。')
    }
  }

  async function execute(current: FrozenWrite) {
    frozen.current = current; setOperationID(current.operationID); setBusy(true); setError(null); setReceipt(null); setConflictPending(false)
    try { await finish(await current.send(), current) }
    catch (caught) {
      if (!(caught instanceof ApiError) || caught.status >= 500) {
        setPending(true)
        await queryCurrent(current)
      } else if (caught.status === 409 && caught.code === 'revision_conflict' && current.conflict) {
        await handleConflict(current)
      } else setError(governanceMessage(caught))
    } finally { setBusy(false) }
  }

  async function query() {
    if (!frozen.current) return
    setBusy(true); setError(null)
    try { await queryCurrent(frozen.current) } finally { setBusy(false) }
  }

  async function retry() {
    if (!frozen.current) return
    const current = frozen.current
    setBusy(true); setError(null)
    try { await finish(await current.send(), current) }
    catch (caught) {
      if (!(caught instanceof ApiError) || caught.status >= 500) await queryCurrent(current)
      else {
        setPending(false)
        if (caught.status === 409 && caught.code === 'revision_conflict' && current.conflict) {
          await handleConflict(current)
        } else setError(governanceMessage(caught))
      }
    } finally { setBusy(false) }
  }

  async function reread() {
    if (!receipt || !frozen.current) return
    setBusy(true); setError(null)
    try { await frozen.current.after(receipt) }
    catch { setError('仍无法读取当前资源；已确认的写入不会再次提交。') }
    finally { setBusy(false) }
  }

  async function rereadConflict() {
    if (!frozen.current?.conflict) return
    setBusy(true); setError(null)
    try { await handleConflict(frozen.current) }
    finally { setBusy(false) }
  }

  return { busy, error, pending, receipt, operationID, conflictPending, execute, query, retry, reread, rereadConflict }
}

function WriteRecovery({ state }: { state: ReturnType<typeof useGovernanceWrite> }) {
  if (state.conflictPending) return <div className="governance-recovery" role="alert"><strong>最新状态尚未确认</strong><p>{state.error}</p><Button type="button" variant="secondary" disabled={state.busy} onClick={() => void state.rereadConflict()}>{state.busy ? '读取中…' : '重新读取最新状态'}</Button></div>
  if (!state.pending && !state.receipt) return <FormError error={state.error} />
  return <div className="governance-recovery" role="status">
    {state.pending ? <><strong>写入结果尚未确认</strong><p>{state.error}</p><code>{state.operationID}</code><div><Button type="button" variant="secondary" disabled={state.busy} onClick={() => void state.query()}>查询原回执</Button><Button type="button" disabled={state.busy} onClick={() => void state.retry()}>用原操作重试</Button></div></> : <><strong>写入回执已确认</strong><p>{state.error ?? '已重新读取资源当前状态；回执版本不等同于当前版本。'}</p>{state.error ? <Button type="button" variant="secondary" disabled={state.busy} onClick={() => void state.reread()}>重新读取当前资源</Button> : null}</>}
  </div>
}

function validPositiveInteger(value: string) {
  if (!/^[1-9][0-9]*$/.test(value)) return false
  try { return BigInt(value) <= 9007199254740991n } catch { return false }
}

function parseLimit(value: string) { return value === '' ? null : validPositiveInteger(value) ? Number(value) : undefined }

function validateCost(cost: string, currency: string, label: string): string | null {
  if (cost !== '') {
    if (!/^[1-9][0-9]*$/.test(cost) || BigInt(cost) > 9223372036854775807n) return `${label}必须是规范十进制正整数字符串，且不超过 9223372036854775807。`
    if (!/^[A-Z]{3}$/.test(currency)) return `${label}需要三个大写 ASCII 字母的币种标识。`
  } else if (currency !== '') return `未设置${label}时不能设置币种。`
  return null
}

function validatePolicy(rpm: string, concurrency: string, budgetTPM: string, budgetCost: string, budgetCurrency: string, shadowTPM: string, shadowCost: string, shadowCurrency: string): string | null {
  if ([rpm, concurrency, budgetTPM, shadowTPM].some((value) => value !== '' && !validPositiveInteger(value))) return 'RPM、并发、TPM 硬预算和 TPM shadow 必须是 1 到 9007199254740991 的整数。'
  const budgetCostIssue = validateCost(budgetCost, budgetCurrency, '24 小时成本硬预算')
  if (budgetCostIssue) return budgetCostIssue
  const shadowCostIssue = validateCost(shadowCost, shadowCurrency, '成本 shadow 阈值')
  if (shadowCostIssue) return shadowCostIssue
  if ([rpm, concurrency, budgetTPM, budgetCost, shadowTPM, shadowCost].every((value) => value === '')) return '至少配置一个 RPM、并发、硬预算或 shadow 阈值。'
  return null
}

function emptyBudget(): GovernanceBudgetLimits {
  return { tpm: null, cost_micro: null, currency: null, window: null, unknown_mode: 'shadow' }
}

function policyBudget(policy: GovernancePolicy | null): GovernanceBudgetLimits {
  return policy?.budget ?? emptyBudget()
}

function policyInput(enabled: boolean, rpm: string, concurrency: string, budgetTPM: string, budgetCost: string, budgetCurrency: string, unknownMode: GovernanceBudgetLimits['unknown_mode'], shadowTPM: string, shadowCost: string, shadowCurrency: string): GovernancePolicyInput {
  return {
    enabled,
    hard: { rpm: parseLimit(rpm)!, concurrency: parseLimit(concurrency)! },
    budget: { tpm: parseLimit(budgetTPM)!, cost_micro: budgetCost || null, currency: budgetCost ? budgetCurrency : null, window: budgetCost ? 'rolling_24h' : null, unknown_mode: unknownMode },
    shadow: { tpm: parseLimit(shadowTPM)!, cost_micro: shadowCost || null, currency: shadowCost ? shadowCurrency : null, window: shadowCost ? 'rolling_24h' : null },
  }
}

function scopeLabel(kind: GovernanceScopeKind) { return kind === 'employee' ? '员工' : kind === 'key' ? 'Key' : '治理组' }

export function GovernancePage({ csrf }: { csrf: string }) {
  const [settings, setSettings] = useState<GovernanceSettings | null>(null)
  const [groups, setGroups] = useState<GovernanceGroup[]>([])
  const [policies, setPolicies] = useState<GovernancePolicy[]>([])
  const [employees, setEmployees] = useState<Employee[]>([])
  const [keys, setKeys] = useState<TargetKey[]>([])
  const [groupCursor, setGroupCursor] = useState<string | null>(null)
  const [policyCursor, setPolicyCursor] = useState<string | null>(null)
  const [groupLoadingMore, setGroupLoadingMore] = useState(false)
  const [policyLoadingMore, setPolicyLoadingMore] = useState(false)
  const [groupLoadError, setGroupLoadError] = useState<string | null>(null)
  const [policyLoadError, setPolicyLoadError] = useState<string | null>(null)
  const groupMoreActive = useRef(false)
  const policyMoreActive = useRef(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [unsupported, setUnsupported] = useState(false)
  const [groupEditor, setGroupEditor] = useState<GovernanceGroup | 'create' | null>(null)
  const [policyEditor, setPolicyEditor] = useState<GovernancePolicy | 'create' | null>(null)
  const settingsWrite = useGovernanceWrite()

  const reload = useCallback(async () => {
    setLoading(true); setError(null); setUnsupported(false)
    try {
      const [nextSettings, nextGroups, nextPolicies, employeePage] = await Promise.all([
        api.governanceSettings(), api.governanceGroups(), api.governancePolicies(), api.employees(),
      ])
      const keyPages = await Promise.all(employeePage.items.map(async (employee) => ({ employee, page: await api.keys(employee.id) })))
      setSettings(nextSettings); setGroups(nextGroups.items); setGroupCursor(nextGroups.next_cursor)
      setPolicies(nextPolicies.items); setPolicyCursor(nextPolicies.next_cursor); setEmployees(employeePage.items)
      setKeys(keyPages.flatMap(({ employee, page }) => page.items.map((key) => ({ ...key, employeeID: employee.id, employeeName: employee.name }))))
    } catch (caught) {
      if (caught instanceof ApiError && caught.status === 404) { setUnsupported(true); setError('当前服务版本不支持请求治理管理，请升级服务后重试。') }
      else setError('无法读取治理配置；页面不会假定治理已关闭或没有策略。')
    } finally { setLoading(false) }
  }, [])

  useEffect(() => { void reload() }, [reload])

  async function toggleSettings(kind: 'governance' | 'budget') {
    if (!settings) return
    const body = kind === 'governance'
      ? { operation_id: crypto.randomUUID(), expected_revision: settings.revision, enabled: !settings.enabled }
      : { operation_id: crypto.randomUUID(), expected_revision: settings.revision, enabled: settings.enabled, budget_enabled: !(settings.budget_enabled ?? false) }
    await settingsWrite.execute({
      operationID: body.operation_id, kind: 'settings', resourceID: 'singleton',
      send: () => api.putGovernanceSettings(body, csrf),
      after: async () => setSettings(await api.governanceSettings()),
      conflict: async () => setSettings(await api.governanceSettings()),
    })
  }

  async function moreGroups() {
    if (!groupCursor || groupMoreActive.current) return
    groupMoreActive.current = true
    setGroupLoadingMore(true); setGroupLoadError(null)
    try {
      const page = await api.governanceGroups(groupCursor)
      setGroups((current) => { const seen = new Set(current.map((item) => item.id)); return [...current, ...page.items.filter((item) => !seen.has(item.id))] })
      setGroupCursor(page.next_cursor)
    } catch { setGroupLoadError('无法读取更多治理组；现有列表保持不变。') }
    finally { groupMoreActive.current = false; setGroupLoadingMore(false) }
  }
  async function morePolicies() {
    if (!policyCursor || policyMoreActive.current) return
    policyMoreActive.current = true
    setPolicyLoadingMore(true); setPolicyLoadError(null)
    try {
      const page = await api.governancePolicies(policyCursor)
      setPolicies((current) => { const seen = new Set(current.map((item) => item.id)); return [...current, ...page.items.filter((item) => !seen.has(item.id))] })
      setPolicyCursor(page.next_cursor)
    } catch { setPolicyLoadError('无法读取更多治理策略；现有列表保持不变。') }
    finally { policyMoreActive.current = false; setPolicyLoadingMore(false) }
  }

  const employeeNames = new Map(employees.map((employee) => [employee.id, employee.name]))
  const groupNames = new Map(groups.map((group) => [group.id, group.name]))
  const keyNames = new Map(keys.map((key) => [key.id, `${key.employeeName} · ${key.name}`]))
  const targetName = (policy: GovernancePolicy) => policy.scope_kind === 'employee' ? employeeNames.get(policy.scope_id) : policy.scope_kind === 'group' ? groupNames.get(policy.scope_id) : keyNames.get(policy.scope_id)

  return <>
    <PageHeader title="请求治理" description="为员工、Key 与独立治理组配置 RPM、并发、可选硬预算及 shadow 观测。" />
    <section className="governance-boundary">
      <strong>首批治理边界</strong>
      <p>RPM 和并发由治理总开关控制；Token 与成本硬预算另有独立开关，默认关闭。Shadow 观测不扣费、不拦截，也不表示余额或正式账单。</p>
      <p>当前只有 OpenAI 官方 <code>gpt-4.1-2025-04-14</code> 的纯文本、非流式、单结果固定请求形状可以证明保守上界。选择“拒绝未知上界”后，其他模型或请求形状会被拒绝。</p>
      <p><code>count_tokens</code> 不计入生成请求治理。治理组与上游账号组彼此独立，策略不会恢复已撤销 Key 或扩大模型权限。</p>
    </section>
    <GovernanceObservations />
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {!loading && !error && settings ? <>
      <section className="content-panel governance-settings">
        <div><span>治理总开关</span><strong>{settings.enabled ? '已启用' : '默认关闭 / 当前关闭'}</strong><small>配置 revision {settings.revision} · 新准入按此状态读取</small></div>
        <Button variant={settings.enabled ? 'danger' : 'primary'} disabled={settingsWrite.busy || settingsWrite.pending || settingsWrite.conflictPending || unsupported} onClick={() => void toggleSettings('governance')}>{settingsWrite.busy ? '处理中…' : settings.enabled ? '关闭治理' : '启用治理'}</Button>
        <div><span>Token / 成本硬预算</span><strong>{settings.budget_enabled ? '预算已启用' : '预算默认关闭 / 当前关闭'}</strong><small>与治理总开关共用 revision；两者都启用时才执行预算拒绝</small></div>
        <Button variant={settings.budget_enabled ? 'danger' : 'primary'} disabled={settingsWrite.busy || settingsWrite.pending || settingsWrite.conflictPending || unsupported} onClick={() => void toggleSettings('budget')}>{settingsWrite.busy ? '处理中…' : settings.budget_enabled ? '关闭预算限制' : '启用预算限制'}</Button>
        <WriteRecovery state={settingsWrite} />
      </section>

      <section className="content-panel governance-panel">
        <div className="section-heading"><div><h2>治理组</h2><p>只使用显式员工成员；部门和上游账号组不会自动成为成员。</p></div><Button onClick={() => setGroupEditor('create')}><Icon name="plus" />新建治理组</Button></div>
        {groups.length === 0 ? <EmptyState title="还没有治理组" body="可先创建空组，再用组策略统一治理明确加入的员工。" /> : <div className="table-scroll"><table className="governance-table"><thead><tr><th>名称</th><th>显式成员</th><th>版本</th><th>操作</th></tr></thead><tbody>{groups.map((group) => <tr key={group.id}><td><strong>{group.name}</strong><small><code>{group.id}</code></small></td><td>{group.employee_ids.length}<small>{group.employee_ids.slice(0, 3).map((id) => employeeNames.get(id) ?? id).join('、') || '空组'}</small></td><td>r{group.revision}</td><td><button className="link-button" onClick={() => setGroupEditor(group)}>编辑成员</button></td></tr>)}</tbody></table></div>}
        {groupLoadError ? <div className="governance-page-error" role="alert"><span>{groupLoadError}</span><Button variant="secondary" disabled={groupLoadingMore} onClick={() => void moreGroups()}>重试加载治理组</Button></div> : null}
        {groupCursor && !groupLoadError ? <div className="pagination"><Button variant="secondary" disabled={groupLoadingMore} onClick={() => void moreGroups()}>{groupLoadingMore ? '读取中…' : '加载更多治理组'}</Button></div> : null}
      </section>

      <section className="content-panel governance-panel">
        <div className="section-heading"><div><h2>治理策略</h2><p>每个员工、Key 或治理组最多一条策略；Key 策略使用独立策略 revision。</p></div><Button onClick={() => setPolicyEditor('create')}><Icon name="plus" />新建策略</Button></div>
        {policies.length === 0 ? <EmptyState title="还没有治理策略" body="总开关开启但没有适用策略时，不会增加治理限制。" /> : <div className="table-scroll"><table className="governance-table governance-policy-table"><thead><tr><th>作用范围</th><th>RPM / 并发</th><th>硬预算</th><th>Shadow 配置</th><th>状态 / 版本</th><th>操作</th></tr></thead><tbody>{policies.map((policy) => { const budget = policyBudget(policy); return <tr key={policy.id}><td><strong>{scopeLabel(policy.scope_kind)} · {targetName(policy) ?? policy.scope_id}</strong><small><code>{policy.scope_id}</code></small></td><td>RPM {policy.hard.rpm ?? '不限'}<small>并发 {policy.hard.concurrency ?? '不限'}</small></td><td>TPM {budget.tpm ?? '未配置'}<small>{budget.cost_micro ? `${budget.cost_micro} μ ${budget.currency} / 24h` : '成本未配置'} · {budget.unknown_mode === 'deny_unknown' ? '未知上界拒绝' : '未知上界仅 shadow'}</small></td><td>TPM {policy.shadow.tpm ?? '未配置'}<small>{policy.shadow.cost_micro ? `${policy.shadow.cost_micro} μ ${policy.shadow.currency} / 24h` : '成本未配置'} · 仅 shadow</small></td><td>{policy.enabled ? '启用' : '停用'}<small>policy r{policy.revision}</small></td><td><button className="link-button" onClick={() => setPolicyEditor(policy)}>编辑策略</button></td></tr> })}</tbody></table></div>}
        {policyLoadError ? <div className="governance-page-error" role="alert"><span>{policyLoadError}</span><Button variant="secondary" disabled={policyLoadingMore} onClick={() => void morePolicies()}>重试加载策略</Button></div> : null}
        {policyCursor && !policyLoadError ? <div className="pagination"><Button variant="secondary" disabled={policyLoadingMore} onClick={() => void morePolicies()}>{policyLoadingMore ? '读取中…' : '加载更多策略'}</Button></div> : null}
      </section>
    </> : null}
    {groupEditor ? <GroupEditor key={groupEditor === 'create' ? 'create' : groupEditor.id} initial={groupEditor === 'create' ? null : groupEditor} employees={employees} csrf={csrf} onClose={() => setGroupEditor(null)} onSaved={async () => { setGroupEditor(null); await reload() }} /> : null}
    {policyEditor ? <PolicyEditor key={policyEditor === 'create' ? 'create' : policyEditor.id} initial={policyEditor === 'create' ? null : policyEditor} employees={employees} keys={keys} groups={groups} csrf={csrf} onClose={() => setPolicyEditor(null)} onSaved={async () => { setPolicyEditor(null); await reload() }} /> : null}
  </>
}

function GroupEditor({ initial, employees, csrf, onClose, onSaved }: { initial: GovernanceGroup | null; employees: Employee[]; csrf: string; onClose: () => void; onSaved: () => Promise<void> }) {
  const [baseline, setBaseline] = useState(initial)
  const [name, setName] = useState(initial?.name ?? '')
  const [members, setMembers] = useState(() => new Set(initial?.employee_ids ?? []))
  const [conflict, setConflict] = useState<GovernanceGroup | null>(null)
  const [validation, setValidation] = useState<string | null>(null)
  const write = useGovernanceWrite()

  async function save() {
    const normalized = name.trim()
    if (!normalized || new TextEncoder().encode(normalized).length > 128 || /[\x00-\x1f\x7f]/.test(normalized)) { setValidation('治理组名称需为 1..128 UTF-8 字节，且不含控制字符。'); return }
    const employee_ids = [...members].sort()
    if (employee_ids.length > 1000) { setValidation('每个治理组最多包含 1000 位员工。'); return }
    setValidation(null)
    const operation_id = crypto.randomUUID()
    const body = baseline
      ? { operation_id, expected_revision: baseline.revision, name: normalized, employee_ids }
      : { operation_id, name: normalized, employee_ids }
    await write.execute({
      operationID: operation_id, kind: 'group', resourceID: baseline?.id,
      send: () => baseline ? api.updateGovernanceGroup(baseline.id, body as { operation_id: string; expected_revision: number; name: string; employee_ids: string[] }, csrf) : api.createGovernanceGroup(body, csrf),
      after: async (receipt) => { await api.governanceGroup(receipt.resource_id); await onSaved() },
      conflict: baseline ? async () => setConflict(await api.governanceGroup(baseline.id)) : undefined,
    })
  }

  const frozen = write.pending || write.conflictPending || Boolean(write.receipt)
  return <Dialog title={baseline ? `编辑治理组 · ${baseline.name}` : '新建治理组'} description="治理组只包含这里明确选择的员工；保存名称与全量成员共用一次 CAS。" onClose={onClose} wide closeDisabled={write.busy || write.pending}>
    <form onSubmit={submitHandler(async () => save())}>
      <Field label="治理组名称"><input value={name} disabled={frozen || Boolean(conflict)} onChange={(event) => setName(event.target.value)} required autoFocus /></Field>
      <fieldset className="governance-member-picker" disabled={frozen || Boolean(conflict)}><legend>显式员工成员（{members.size}）</legend>{employees.length ? employees.map((employee) => <label key={employee.id}><input type="checkbox" checked={members.has(employee.id)} onChange={(event) => setMembers((current) => { const next = new Set(current); event.target.checked ? next.add(employee.id) : next.delete(employee.id); return next })} /><span><strong>{employee.name}</strong><small>{employee.department || '无部门'} · {employee.status === 'active' ? '启用' : '停用'}</small></span></label>) : <p>当前没有员工；允许保存空组。</p>}</fieldset>
      {conflict ? <div className="governance-conflict"><strong>服务器当前版本 r{conflict.revision}</strong><p>{conflict.name} · {conflict.employee_ids.length} 位显式成员。当前编辑未覆盖它。</p><Button type="button" variant="secondary" onClick={() => { setBaseline(conflict); setName(conflict.name); setMembers(new Set(conflict.employee_ids)); setConflict(null) }}>按最新状态重新编辑</Button></div> : null}
      <FormError error={validation} />
      <WriteRecovery state={write} />
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={write.busy || write.pending} onClick={onClose}>取消</Button><Button type="submit" disabled={write.busy || frozen || Boolean(conflict) || !name.trim()}>{write.busy ? '正在保存…' : '保存治理组'}</Button></div>
    </form>
  </Dialog>
}

function PolicyEditor({ initial, employees, keys, groups, csrf, onClose, onSaved }: { initial: GovernancePolicy | null; employees: Employee[]; keys: TargetKey[]; groups: GovernanceGroup[]; csrf: string; onClose: () => void; onSaved: () => Promise<void> }) {
  const initialBudget = policyBudget(initial)
  const [baseline, setBaseline] = useState(initial)
  const [kind, setKind] = useState<GovernanceScopeKind>(initial?.scope_kind ?? 'employee')
  const [scopeID, setScopeID] = useState(initial?.scope_id ?? employees[0]?.id ?? '')
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [rpm, setRPM] = useState(initial?.hard.rpm?.toString() ?? '')
  const [concurrency, setConcurrency] = useState(initial?.hard.concurrency?.toString() ?? '')
  const [budgetTPM, setBudgetTPM] = useState(initialBudget.tpm?.toString() ?? '')
  const [budgetCost, setBudgetCost] = useState(initialBudget.cost_micro ?? '')
  const [budgetCurrency, setBudgetCurrency] = useState(initialBudget.currency ?? '')
  const [unknownMode, setUnknownMode] = useState<GovernanceBudgetLimits['unknown_mode']>(initialBudget.unknown_mode)
  const [tpm, setTPM] = useState(initial?.shadow.tpm?.toString() ?? '')
  const [cost, setCost] = useState(initial?.shadow.cost_micro ?? '')
  const [currency, setCurrency] = useState(initial?.shadow.currency ?? '')
  const [validation, setValidation] = useState<string | null>(null)
  const [conflict, setConflict] = useState<GovernancePolicy | null>(null)
  const write = useGovernanceWrite()
  const targets = kind === 'employee' ? employees.map((item) => ({ id: item.id, name: `${item.name}${item.status === 'disabled' ? '（已停用）' : ''}` })) : kind === 'key' ? keys.map((item) => ({ id: item.id, name: `${item.employeeName} · ${item.name}${item.revoked_at ? '（已撤销）' : ''}` })) : groups.map((item) => ({ id: item.id, name: item.name }))

  function applyCurrent(current: GovernancePolicy) {
    const budget = policyBudget(current)
    setBaseline(current); setKind(current.scope_kind); setScopeID(current.scope_id); setEnabled(current.enabled)
    setRPM(current.hard.rpm?.toString() ?? ''); setConcurrency(current.hard.concurrency?.toString() ?? '')
    setBudgetTPM(budget.tpm?.toString() ?? ''); setBudgetCost(budget.cost_micro ?? ''); setBudgetCurrency(budget.currency ?? ''); setUnknownMode(budget.unknown_mode)
    setTPM(current.shadow.tpm?.toString() ?? ''); setCost(current.shadow.cost_micro ?? ''); setCurrency(current.shadow.currency ?? '')
  }

  async function save() {
    const issue = validatePolicy(rpm, concurrency, budgetTPM, budgetCost, budgetCurrency, tpm, cost, currency)
    if (issue) { setValidation(issue); return }
    if (!scopeID) { setValidation('请选择策略作用对象。'); return }
    setValidation(null)
    const operation_id = crypto.randomUUID()
    const limits = policyInput(enabled, rpm, concurrency, budgetTPM, budgetCost, budgetCurrency, unknownMode, tpm, cost, currency)
    const body = baseline ? { operation_id, expected_revision: baseline.revision, ...limits } : { operation_id, scope_kind: kind, scope_id: scopeID, ...limits }
    await write.execute({
      operationID: operation_id, kind: 'policy', resourceID: baseline?.id,
      send: () => baseline ? api.updateGovernancePolicy(baseline.id, body as { operation_id: string; expected_revision: number } & GovernancePolicyInput, csrf) : api.createGovernancePolicy(body as { operation_id: string; scope_kind: GovernanceScopeKind; scope_id: string } & GovernancePolicyInput, csrf),
      after: async (receipt) => { await api.governancePolicy(receipt.resource_id); await onSaved() },
      conflict: baseline ? async () => setConflict(await api.governancePolicy(baseline.id)) : undefined,
    })
  }

  const frozen = write.pending || write.conflictPending || Boolean(write.receipt)
  return <Dialog title={baseline ? '编辑治理策略' : '新建治理策略'} description="RPM、并发和可证明上界的 Token / 成本预算可以拒绝新请求；shadow 观测仍不拦截。" onClose={onClose} wide closeDisabled={write.busy || write.pending}>
    <form onSubmit={submitHandler(async () => save())}>
      <div className="governance-form-grid">
        <Field label="作用范围"><select value={kind} disabled={Boolean(baseline) || frozen || Boolean(conflict)} onChange={(event) => { const next = event.target.value as GovernanceScopeKind; setKind(next); setScopeID(next === 'employee' ? employees[0]?.id ?? '' : next === 'key' ? keys[0]?.id ?? '' : groups[0]?.id ?? '') }}><option value="employee">员工</option><option value="key">单个 Key</option><option value="group">治理组</option></select></Field>
        <Field label="作用对象" hint={kind === 'key' ? 'Key 策略使用独立 policy revision，不使用 Key 自身 revision。' : undefined}><select value={scopeID} disabled={Boolean(baseline) || frozen || Boolean(conflict)} onChange={(event) => setScopeID(event.target.value)}><option value="">请选择</option>{targets.map((target) => <option key={target.id} value={target.id}>{target.name}</option>)}</select></Field>
        <Field label="RPM 硬限制" hint="空白表示不限制；按固定 60 秒窗口。"><input inputMode="numeric" value={rpm} disabled={frozen || Boolean(conflict)} onChange={(event) => setRPM(event.target.value)} placeholder="不限制" /></Field>
        <Field label="并发硬限制" hint="空白表示不限制；超过后立即拒绝，不排队。"><input inputMode="numeric" value={concurrency} disabled={frozen || Boolean(conflict)} onChange={(event) => setConcurrency(event.target.value)} placeholder="不限制" /></Field>
        <Field label="TPM 硬预算" hint="安全整数。仅在预算总开关和治理总开关都启用时执行。"><input inputMode="numeric" value={budgetTPM} disabled={frozen || Boolean(conflict)} onChange={(event) => setBudgetTPM(event.target.value)} placeholder="不配置" /></Field>
        <Field label="24 小时成本硬预算（micro）" hint="规范十进制字符串，不转成浮点数；固定 rolling_24h。"><input inputMode="numeric" value={budgetCost} disabled={frozen || Boolean(conflict)} onChange={(event) => { setBudgetCost(event.target.value); if (!event.target.value) setBudgetCurrency('') }} placeholder="不配置" /></Field>
        <Field label="硬预算币种"><input value={budgetCurrency} disabled={frozen || Boolean(conflict) || !budgetCost} maxLength={3} onChange={(event) => setBudgetCurrency(event.target.value)} placeholder={budgetCost ? 'USD' : '未配置成本'} /></Field>
        <Field label="未知上界处理" hint="只影响 Token / 成本硬预算；不会把未知用量当作零。"><select value={unknownMode} disabled={frozen || Boolean(conflict)} onChange={(event) => setUnknownMode(event.target.value as GovernanceBudgetLimits['unknown_mode'])}><option value="shadow">保持 shadow，不按未知拒绝</option><option value="deny_unknown">拒绝无法证明上界的请求</option></select></Field>
        <Field label="TPM shadow 阈值" hint="仅保存与记录准入快照；尚无统计结论。"><input inputMode="numeric" value={tpm} disabled={frozen || Boolean(conflict)} onChange={(event) => setTPM(event.target.value)} placeholder="不配置" /></Field>
        <Field label="成本 shadow 阈值（micro）" hint="规范十进制字符串，保持原值精度；固定 rolling_24h。"><input inputMode="numeric" value={cost} disabled={frozen || Boolean(conflict)} onChange={(event) => { setCost(event.target.value); if (!event.target.value) setCurrency('') }} placeholder="不配置" /></Field>
        <Field label="成本币种"><input value={currency} disabled={frozen || Boolean(conflict) || !cost} maxLength={3} onChange={(event) => setCurrency(event.target.value)} placeholder={cost ? 'USD' : '未配置成本'} /></Field>
        <label className="toggle-field governance-enabled"><input type="checkbox" checked={enabled} disabled={frozen || Boolean(conflict)} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用此策略</strong><small>停用只影响新治理准入，不删除历史 RPM 事件或租约。</small></span></label>
      </div>
      <div className="governance-budget-note"><strong>固定 profile 条件</strong><span>当前仅 OpenAI 官方 <code>gpt-4.1-2025-04-14</code>、Chat Completions、纯文本、非流式、单结果请求可证明保守上界。选择“拒绝无法证明上界”会拒绝其他模型或请求形状。</span></div>
      <div className="governance-shadow-note"><strong>Shadow 不会阻断请求</strong><span>用量观测只解释历史阈值，不表示“将会拦截”、可用余额或正式账单。跨币种不会合并。</span></div>
      <FormError error={validation} />
      {conflict ? <div className="governance-conflict"><strong>服务器当前策略 r{conflict.revision}</strong><p>当前编辑未覆盖新版本。请先采用最新状态，再明确提交一个新 operation。</p><Button type="button" variant="secondary" onClick={() => { applyCurrent(conflict); setConflict(null) }}>按最新状态重新编辑</Button></div> : null}
      <WriteRecovery state={write} />
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={write.busy || write.pending} onClick={onClose}>取消</Button><Button type="submit" disabled={write.busy || frozen || Boolean(conflict) || !scopeID}>{write.busy ? '正在保存…' : '保存策略'}</Button></div>
    </form>
  </Dialog>
}
