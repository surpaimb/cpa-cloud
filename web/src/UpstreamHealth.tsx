import { useEffect, useRef, useState } from 'react'
import {
  ApiError,
  api,
  type Upstream,
  type UpstreamHealthScope,
  type UpstreamObservation,
  type UpstreamTestOperation,
} from './api'
import { Button, Dialog, Field, FormError } from './ui'
import { RecoveryStateSummary } from './AccountRecovery'

type TestWrite = { operation_id: string; expected_revision: number; scope: UpstreamHealthScope }

const resultCopy: Record<string, { title: string; detail: string; tone: 'active' | 'warning' | 'danger' }> = {
  local_credential_ok: { title: '本地凭据可解析', detail: '只完成本机解密与格式检查；不表示上游认证或生成可用。', tone: 'active' },
  catalog_ok: { title: '目录读取完成', detail: '只验证本次目录请求；不表示模型生成可用。', tone: 'active' },
  authentication_failed: { title: '凭据检查未通过', detail: '本地凭据不可用或上游未接受凭据；请核对配置或重新授权。', tone: 'danger' },
  rate_limited: { title: '目录请求受到限流', detail: '本次目录受限；可先核对这次结果，再由管理员手动新建测试。', tone: 'warning' },
  unsupported: { title: '此测试范围不可用', detail: '当前账号类型或服务端功能开关不支持该检查。', tone: 'warning' },
  timeout: { title: '测试超时', detail: '结果未证明目录或生成可用。', tone: 'warning' },
  invalid_response: { title: '目录响应无效', detail: '上游返回内容未通过固定协议校验。', tone: 'danger' },
  configuration_changed: { title: '账号配置无法用于测试', detail: '配置可能已变化或不满足当前测试要求，请刷新列表并核对配置。', tone: 'warning' },
  stale: { title: '测试结果已过期', detail: '测试期间账号凭据或版本发生变化，结果未覆盖当前观测。', tone: 'warning' },
  cancelled: { title: '测试已取消', detail: '本次没有得到可用性结论。', tone: 'warning' },
  interrupted: { title: '测试被中断', detail: '服务不会自动重发这次操作；可查询记录后再决定是否新建测试。', tone: 'warning' },
  internal_failure: { title: '测试未能完成', detail: '本次未取得可用性结论；请核对记录后再决定是否新建测试。', tone: 'danger' },
}

const cooldownCopy: Record<string, string> = {
  rate_limited: '上游限流',
  overloaded: '上游过载',
  transient: '暂时故障',
  authentication: '凭据故障',
  permanent: '账号配置故障',
}

function displayTime(value?: string | null) {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return '时间不可用'
  return new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(parsed)
}

function resultMessage(code: string | null, scope?: UpstreamHealthScope) {
  if (!code) return { title: '等待测试结果', detail: '服务尚未返回终态。', tone: 'warning' as const }
  if (code === 'authentication_failed' && scope === 'local_credential') {
    return { title: '本地凭据检查未通过', detail: '凭据无法在本机解密或解析；此检查没有访问上游。', tone: 'danger' as const }
  }
  return resultCopy[code] ?? { title: '测试已完成', detail: '服务返回了当前界面不认识的固定结果，请刷新后台版本后查看。', tone: 'warning' as const }
}

function observationMessage(observation: UpstreamObservation) {
  return resultMessage(observation.result_code, observation.scope)
}

function testErrorMessage(error: unknown) {
  if (!(error instanceof ApiError)) return '测试结果未确认。请查询原操作或使用同一操作编号重试，系统不会自动重发。'
  const messages: Record<string, string> = {
    revision_conflict: '账号版本已变化。请刷新列表，并按当前版本新建测试。',
    operation_conflict: '该操作编号已用于不同测试。请刷新列表核对，不要覆盖已有记录。',
    test_in_progress: '这个账号已有测试在运行。当前操作未自动改号，请稍后重试相同操作。',
    test_capacity_exceeded: '全局测试容量已满。当前操作未自动改号，请稍后重试相同操作。',
    test_history_full: '测试记录已达上限，无法创建新测试。已有操作仍可查询。',
    invalid_request: '测试参数无效，请刷新列表后重新开始。',
    not_found: '账号已不存在，请刷新列表。',
  }
  return messages[error.code] ?? '测试结果未确认。请查询原操作，服务未返回可展示的最终结果。'
}

function clearErrorMessage(error: unknown) {
  if (!(error instanceof ApiError)) return '清除结果未确认。请刷新列表核对当前冷却状态，不要假定已经清除。'
  if (error.status === 409 || error.code === 'revision_conflict' || error.code === 'cooldown_conflict') {
    return '账号或冷却状态已更新。请刷新列表后再操作。'
  }
  if (error.code === 'not_found') return '账号已不存在，请刷新列表。'
  return '无法清除冷却，请刷新列表核对状态。'
}

export function UpstreamHealth({ item, csrf, serverTime, onReload }: {
  item: Upstream
  csrf: string
  serverTime?: string
  onReload: () => Promise<void>
}) {
  const [open, setOpen] = useState(false)
  const [scope, setScope] = useState<UpstreamHealthScope>('local_credential')
  const [write, setWrite] = useState<TestWrite | null>(null)
  const [operation, setOperation] = useState<UpstreamTestOperation | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [clearBusy, setClearBusy] = useState(false)
  const [clearError, setClearError] = useState<string | null>(null)
  const sequence = useRef(0)
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => {
    sequence.current += 1
    controller.current?.abort()
  }, [])

  async function acceptResult(next: UpstreamTestOperation, token: number) {
    if (token !== sequence.current) return
    setOperation(next)
    setError(null)
    if (next.state === 'completed') {
      try {
        await onReload()
      } catch {
        if (token === sequence.current) setError('测试结果已记录，但列表刷新失败。请手动刷新核对账号版本与观测。')
      }
    }
  }

  async function submitTest(payload: TestWrite) {
    const token = ++sequence.current
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    setBusy(true)
    setError(null)
    try {
      const next = await api.startUpstreamTest(item.id, payload, csrf, abort.signal)
      await acceptResult(next, token)
    } catch (caught) {
      if (token === sequence.current && !(caught instanceof DOMException && caught.name === 'AbortError')) setError(testErrorMessage(caught))
    } finally {
      if (token === sequence.current) setBusy(false)
    }
  }

  async function startTest() {
    const payload = { operation_id: crypto.randomUUID(), expected_revision: item.revision, scope }
    setWrite(payload)
    setOperation(null)
    await submitTest(payload)
  }

  async function queryTest() {
    if (!write) return
    const token = ++sequence.current
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    setBusy(true)
    setError(null)
    try {
      const next = await api.upstreamTest(item.id, write.operation_id, abort.signal)
      await acceptResult(next, token)
    } catch (caught) {
      if (token === sequence.current && !(caught instanceof DOMException && caught.name === 'AbortError')) setError(testErrorMessage(caught))
    } finally {
      if (token === sequence.current) setBusy(false)
    }
  }

  async function clearCooldown() {
    if (!item.cooldown) return
    const token = ++sequence.current
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    setClearBusy(true)
    setClearError(null)
    try {
      await api.clearUpstreamCooldown(item.id, {
        expected_revision: item.revision,
        expected_cooldown_event_id: item.cooldown.event_id,
      }, csrf, abort.signal)
      if (token === sequence.current) await onReload()
    } catch (caught) {
      if (token === sequence.current && !(caught instanceof DOMException && caught.name === 'AbortError')) setClearError(clearErrorMessage(caught))
    } finally {
      if (token === sequence.current) setClearBusy(false)
    }
  }

  const latest = item.latest_observation ? observationMessage(item.latest_observation) : null
  const current = operation ? resultMessage(operation.result_code, operation.scope) : null
  const unresolved = write && operation?.state !== 'completed'
  const writeUsesCurrentRevision = !write || write.expected_revision === item.revision
  const serverSnapshot = serverTime ? `服务端状态时间：${displayTime(serverTime)}` : '状态由服务端返回；刷新前不在浏览器中推断变化。'

  return <div className="upstream-health">
    <div className="upstream-health__summary">
      {latest ? <><span className={`status status--${latest.tone}`}><i />{latest.title}</span><small>{item.latest_observation?.scope === 'catalog' ? '目录观测，不代表生成可用' : '本地检查，不代表上游认证'}</small></> : <><span className="status"><i />尚无测试观测</span><small>未检查不代表不可用。</small></>}
      {item.cooldown ? <div className="cooldown-summary"><strong>{item.cooldown.active ? `冷却中：${cooldownCopy[item.cooldown.failure_class] ?? '暂不可调度'}` : '服务端标记冷却已到期'}</strong><small>{item.cooldown.active ? `截至 ${displayTime(item.cooldown.cooldown_until)}` : '请刷新列表确认最新调度状态。'}</small></div> : <small>当前列表没有冷却记录。</small>}
      {item.recovery ? <RecoveryStateSummary state={item.recovery} /> : null}
    </div>
    <div className="upstream-health__actions">
      <button className="link-button" onClick={() => setOpen(true)}>账号测试</button>
      {item.cooldown && (item.cooldown.active || item.recovery) ? <button className="link-button" disabled={clearBusy} onClick={() => void clearCooldown()}>{clearBusy ? '清除中…' : item.recovery ? '清除冷却与隔离' : '清除冷却'}</button> : item.cooldown ? <button className="link-button" disabled={clearBusy} onClick={() => void onReload()}>刷新冷却状态</button> : null}
    </div>
    {clearError ? <span className="health-action-error" role="alert">{clearError}<button className="link-button" onClick={() => void onReload()}>刷新列表</button></span> : null}
    {open ? <Dialog title={`测试 ${item.name}`} description="测试结果是带范围的观测，不会启用账号，也不表示模型生成健康。" onClose={() => setOpen(false)} closeDisabled={busy}>
      {!item.enabled ? <div className="membership-limitations"><strong>账号当前已停用</strong>测试或清除冷却都不会启用此账号。</div> : null}
      {item.provider_kind === 'codex-membership' && scope === 'catalog' ? <div className="membership-limitations"><strong>Codex 目录测试可能刷新凭据</strong>服务端会使用共享刷新流程；完成后页面将重新读取账号 revision。</div> : null}
      <div className="health-test-form">
        <Field label="测试范围" hint={scope === 'local_credential' ? '只在服务端本机解密并检查格式，不访问上游。' : '读取上游模型目录，不发送生成请求，也不证明生成可用。'}>
          <select value={scope} disabled={busy || Boolean(unresolved)} onChange={(event) => setScope(event.target.value as UpstreamHealthScope)}>
            <option value="local_credential">本地凭据检查</option>
            <option value="catalog">目录测试</option>
          </select>
        </Field>
        {operation ? <div className="health-test-result" role="status">
          <span className={`status status--${current?.tone ?? 'warning'}`}><i />{operation.state === 'completed' ? current?.title : '测试仍在进行'}</span>
          <p>{operation.state === 'completed' ? current?.detail : '页面不会自动轮询或创建新操作，请手动查询原操作。'}</p>
          <dl><div><dt>操作编号</dt><dd><code>{operation.operation_id}</code></dd></div><div><dt>范围</dt><dd>{operation.scope === 'catalog' ? '目录测试' : '本地凭据检查'}</dd></div><div><dt>受测版本</dt><dd>{operation.tested_revision ?? '尚未确定'}</dd></div>{operation.finished_at ? <div><dt>完成时间</dt><dd>{displayTime(operation.finished_at)}</dd></div> : null}</dl>
        </div> : write ? <div className="health-test-result" role="status"><strong>操作编号已保留</strong><p>未知结果不会自动换号或重发。</p><code>{write.operation_id}</code></div> : null}
        {!writeUsesCurrentRevision ? <div className="batch-uncertain">账号已从 r{write?.expected_revision} 更新为 r{item.revision}。原操作仍可查询，但新测试必须由你按当前版本明确开始。</div> : null}
        <FormError error={error} />
        {error ? <button type="button" className="link-button health-refresh-link" disabled={busy} onClick={() => void onReload()}>刷新账号列表</button> : null}
        <small className="health-server-time">{serverSnapshot}</small>
      </div>
      <div className="dialog__actions health-dialog-actions">
        <Button type="button" variant="secondary" disabled={busy} onClick={() => setOpen(false)}>关闭</Button>
        {write && unresolved ? <><Button type="button" variant="secondary" disabled={busy} onClick={() => void queryTest()}>{busy ? '查询中…' : '查询原操作'}</Button>{writeUsesCurrentRevision ? <Button type="button" disabled={busy} onClick={() => void submitTest(write)}>{busy ? '提交中…' : '重试相同操作'}</Button> : <Button type="button" disabled={busy} onClick={() => void startTest()}>{busy ? '测试中…' : '按当前版本新建测试'}</Button>}</> : <Button type="button" disabled={busy} onClick={() => void startTest()}>{busy ? '测试中…' : operation ? '开始新的测试' : '开始测试'}</Button>}
      </div>
    </Dialog> : null}
  </div>
}
