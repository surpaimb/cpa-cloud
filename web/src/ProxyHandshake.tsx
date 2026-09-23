import { useEffect, useRef, useState } from 'react'
import {
  api,
  ApiError,
  type OutboundProxy,
  type OutboundProxyTestOperation,
  type OutboundProxyTestResultCode,
  type OutboundProxyTestWrite,
  type Upstream,
  type UpstreamProxyState,
} from './api'
import { Button, Dialog, Field, FormError } from './ui'

export type ProxyHandshakeAccount = { upstream: Upstream; proxyState: UpstreamProxyState }

const pollLimit = 8
const pollDelayMs = 500
const resultCopy: Record<OutboundProxyTestResultCode, { title: string; detail: string; tone: 'active' | 'warning' | 'danger' }> = {
  handshake_ok: { title: '代理与目标 TLS 握手完成', detail: '只验证本次 HTTPS CONNECT 与目标 TLS；未发送模型请求，也不表示模型生成可用。', tone: 'active' },
  configuration_changed: { title: '配置已变化', detail: '测试快照已不是当前配置，结果不能证明当前连接可用。', tone: 'warning' },
  proxy_unavailable: { title: '代理不可用', detail: '本次未能通过代理建立所需连接。', tone: 'danger' },
  target_unavailable: { title: '目标不可用', detail: '已保存目标未完成 TLS 握手；未发送模型请求。', tone: 'danger' },
  address_rejected: { title: '地址被安全策略拒绝', detail: '代理或目标地址未通过固定出站范围检查。', tone: 'danger' },
  timeout: { title: '握手检查超时', detail: '本次未取得连接可用性结论。', tone: 'warning' },
  cancelled: { title: '握手检查已取消', detail: '本次没有得到连接可用性结论。', tone: 'warning' },
  interrupted: { title: '握手检查被中断', detail: '服务不会自动重放连接；可查询记录后再决定是否新建检查。', tone: 'warning' },
  internal_failure: { title: '握手检查未能完成', detail: '服务只返回固定故障类别；不会展示代理、证书或远端错误正文。', tone: 'danger' },
}

function isAPIKey(upstream: Upstream) {
  return upstream.provider_kind === 'openai-compatible' || upstream.provider_kind === 'anthropic-api-key' || upstream.provider_kind === 'gemini-api-key'
}

function isHTTPS(endpoint: string) {
  try { return new URL(endpoint).protocol === 'https:' }
  catch { return false }
}

function eligibleAccounts(proxy: OutboundProxy, accounts: ProxyHandshakeAccount[]) {
  if (!proxy.enabled) return []
  return accounts.filter(({ upstream, proxyState }) => upstream.enabled && isAPIKey(upstream) && isHTTPS(upstream.endpoint) &&
    proxyState.upstream_id === upstream.id && proxyState.binding?.proxy_id === proxy.id && proxyState.binding.enabled)
}

function operationMatches(operation: OutboundProxyTestOperation, proxyID: string, write: OutboundProxyTestWrite) {
  const states = ['pending', 'in_progress', 'completed']
  const results = Object.keys(resultCopy)
  return operation.operation_id === write.operation_id && operation.proxy_id === proxyID && operation.upstream_id === write.upstream_id &&
    Number.isSafeInteger(operation.proxy_revision) && operation.proxy_revision === write.expected_proxy_revision &&
    Number.isSafeInteger(operation.connection_revision) && operation.connection_revision === write.expected_connection_revision &&
    Number.isSafeInteger(operation.upstream_revision) && operation.upstream_revision === write.expected_upstream_revision &&
    states.includes(operation.state) && (operation.result_code === null || results.includes(operation.result_code)) &&
    (operation.state === 'completed' ? operation.result_code !== null : operation.result_code === null) &&
    (operation.latency_ms === null || Number.isInteger(operation.latency_ms) && operation.latency_ms >= 0 && operation.latency_ms <= 10000)
}

function waitForPoll(signal: AbortSignal) {
  return new Promise<void>((resolve, reject) => {
    const timer = window.setTimeout(done, pollDelayMs)
    function done() { signal.removeEventListener('abort', aborted); resolve() }
    function aborted() { window.clearTimeout(timer); reject(new DOMException('aborted', 'AbortError')) }
    signal.addEventListener('abort', aborted, { once: true })
  })
}

function fixedError(error: unknown, query = false) {
  if (!(error instanceof ApiError)) return query
    ? '无法确认原操作状态。操作编号已保留，请稍后手动查询。'
    : '提交结果未知。系统将只查询原操作，不会创建新操作编号。'
  if (query && error.status === 404) return '服务暂未查到原操作；这不表示检查未执行。操作编号已保留，页面不会自动创建新操作。'
  const messages: Record<string, string> = {
    operation_conflict: '该操作编号已用于不同检查。请核对记录，不要覆盖或自动换号。',
    revision_conflict: '代理、连接或账号版本已变化。本次没有按新版本自动创建检查。',
    configuration_changed: '绑定或配置已变化，请刷新页面后重新选择。',
    test_in_progress: '此代理已有握手检查在运行。当前操作没有自动改号。',
    test_capacity_exceeded: '握手检查容量已满。当前操作没有自动改号。',
    test_history_full: '握手检查历史已达上限，已有操作仍可查询。',
    invalid_request: '握手检查参数无效，请刷新页面核对配置。',
    not_found: query ? '服务暂未查到原操作；这不表示检查未执行。操作编号已保留。' : '代理或账号已不存在，请刷新页面。',
  }
  return messages[error.code] ?? (query ? '无法读取原操作的固定状态。操作编号已保留。' : '握手检查未被服务接受，请刷新配置后核对。')
}

function unknownWrite(error: unknown) {
  return !(error instanceof ApiError) || error.status >= 500
}

function displayTime(value: string | null) {
  if (!value) return '—'
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? '时间不可用' : new Intl.DateTimeFormat('zh-CN', { dateStyle: 'short', timeStyle: 'medium' }).format(parsed)
}

export function ProxyHandshakeDialog({ proxy, accounts, csrf, onClose }: { proxy: OutboundProxy; accounts: ProxyHandshakeAccount[]; csrf: string; onClose: () => void }) {
  const eligible = eligibleAccounts(proxy, accounts)
  const [upstreamID, setUpstreamID] = useState(eligible[0]?.upstream.id ?? '')
  const [write, setWrite] = useState<OutboundProxyTestWrite | null>(null)
  const [operation, setOperation] = useState<OutboundProxyTestOperation | null>(null)
  const [busy, setBusy] = useState(false)
  const [timedOut, setTimedOut] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const sequence = useRef(0)
  const controller = useRef<AbortController | null>(null)

  useEffect(() => () => { sequence.current += 1; controller.current?.abort() }, [])

  function accept(next: OutboundProxyTestOperation, payload: OutboundProxyTestWrite, token: number) {
    if (token !== sequence.current) return false
    if (!operationMatches(next, proxy.id, payload)) {
      setError('服务返回的操作记录与已冻结请求不匹配。页面不会继续查询或重新连接。')
      return false
    }
    setOperation(next)
    setError(null)
    return true
  }

  async function poll(payload: OutboundProxyTestWrite, first: OutboundProxyTestOperation, token: number, abort: AbortController) {
    let current = first
    for (let attempt = 0; attempt < pollLimit && current.state !== 'completed'; attempt += 1) {
      await waitForPoll(abort.signal)
      current = await api.outboundProxyTest(proxy.id, payload.operation_id, abort.signal)
      if (!accept(current, payload, token)) return
    }
    if (token === sequence.current && current.state !== 'completed') setTimedOut(true)
  }

  async function queryAfterUnknown(payload: OutboundProxyTestWrite, token: number, abort: AbortController) {
    try {
      const found = await api.outboundProxyTest(proxy.id, payload.operation_id, abort.signal)
      if (accept(found, payload, token) && found.state !== 'completed') await poll(payload, found, token, abort)
    } catch (caught) {
      if (token === sequence.current && !(caught instanceof DOMException && caught.name === 'AbortError')) setError(fixedError(caught, true))
    }
  }

  async function start() {
    const selected = eligible.find(({ upstream }) => upstream.id === upstreamID)
    if (!selected) { setError('请选择一个仍启用且明确绑定此代理的 HTTPS API Key 账号。'); return }
    const payload: OutboundProxyTestWrite = {
      operation_id: crypto.randomUUID(),
      expected_proxy_revision: proxy.revision,
      expected_connection_revision: proxy.connection_revision,
      upstream_id: selected.upstream.id,
      expected_upstream_revision: selected.proxyState.upstream_revision,
    }
    const token = ++sequence.current
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    setWrite(payload); setOperation(null); setTimedOut(false); setError(null); setBusy(true)
    try {
      const next = await api.startOutboundProxyTest(proxy.id, payload, csrf, abort.signal)
      if (accept(next, payload, token) && next.state !== 'completed') await poll(payload, next, token, abort)
    } catch (caught) {
      if (token !== sequence.current || caught instanceof DOMException && caught.name === 'AbortError') return
      if (unknownWrite(caught)) {
        setError(fixedError(caught))
        await queryAfterUnknown(payload, token, abort)
      } else setError(fixedError(caught))
    } finally {
      if (token === sequence.current) setBusy(false)
    }
  }

  async function queryOriginal() {
    if (!write) return
    const token = ++sequence.current
    controller.current?.abort()
    const abort = new AbortController()
    controller.current = abort
    setBusy(true); setError(null); setTimedOut(false)
    try {
      const next = await api.outboundProxyTest(proxy.id, write.operation_id, abort.signal)
      if (accept(next, write, token) && next.state !== 'completed') setTimedOut(true)
    } catch (caught) {
      if (token === sequence.current && !(caught instanceof DOMException && caught.name === 'AbortError')) setError(fixedError(caught, true))
    } finally {
      if (token === sequence.current) setBusy(false)
    }
  }

  const selected = eligible.find(({ upstream }) => upstream.id === upstreamID)
  const result = operation?.result_code ? resultCopy[operation.result_code] : null
  const currentAccount = operation ? accounts.find(({ upstream }) => upstream.id === operation.upstream_id) : null
  const currentSnapshot = Boolean(operation && currentAccount && proxy.revision === operation.proxy_revision && proxy.connection_revision === operation.connection_revision &&
    currentAccount.upstream.revision === operation.upstream_revision && currentAccount.upstream.enabled && currentAccount.proxyState.binding?.proxy_id === proxy.id)
  const successful = operation?.result_code === 'handshake_ok'
  const tone = successful && !currentSnapshot ? 'warning' : result?.tone ?? 'warning'
  const title = successful && !currentSnapshot ? '历史握手通过，当前配置未验证' : result?.title

  return <Dialog title={`仅握手检查 · ${proxy.name}`} description="只建立 HTTPS CONNECT 与目标 TLS，不发送目录、模型请求或上游 API Key。" onClose={() => { sequence.current += 1; controller.current?.abort(); onClose() }}>
    {!proxy.enabled ? <div className="proxy-uncertain"><strong>代理已停用</strong><p>停用状态不能新建握手检查；既有账号绑定不会因此自动解除。</p></div> : null}
    <div className="health-test-form">
      <Field label="已绑定目标账号" hint="只列出已启用、目标为 HTTPS、且当前明确绑定此代理的 API Key 账号。"><select value={upstreamID} disabled={busy || Boolean(write)} onChange={(event) => setUpstreamID(event.target.value)}><option value="">请选择</option>{eligible.map(({ upstream, proxyState }) => <option key={upstream.id} value={upstream.id}>{upstream.name} · 账号 r{proxyState.upstream_revision}</option>)}</select></Field>
      {eligible.length === 0 ? <div className="membership-limitations"><strong>没有可用于检查的绑定账号</strong>Codex、HTTP 目标、停用账号或未绑定账号不会列入。请先完成有效绑定。</div> : null}
      {selected && !write ? <div className="proxy-test-snapshot"><strong>即将冻结的版本</strong><span>代理 r{proxy.revision} / 连接 r{proxy.connection_revision} / 账号 r{selected.proxyState.upstream_revision}</span></div> : null}
      {write ? <div className="health-test-result" role="status">
        {operation ? <><span className={`status status--${operation.state === 'completed' ? tone : 'warning'}`}><i />{operation.state === 'completed' ? title : '握手检查仍在进行'}</span><p>{operation.state === 'completed' ? (successful && !currentSnapshot ? '此结果属于旧版本快照；请勿当作当前代理可用。' : result?.detail) : timedOut ? '自动查询已停止；服务操作可能仍在进行，可手动查询原操作。' : '正在有界查询原操作；关闭页面不会取消服务端已保存的检查。'}</p>
          <dl><div><dt>操作编号</dt><dd><code>{operation.operation_id}</code></dd></div><div><dt>代理版本</dt><dd>r{operation.proxy_revision} / 连接 r{operation.connection_revision}</dd></div><div><dt>账号版本</dt><dd>r{operation.upstream_revision}</dd></div><div><dt>状态</dt><dd>{operation.state}</dd></div>{operation.latency_ms !== null ? <div><dt>双层握手耗时</dt><dd>{operation.latency_ms} ms</dd></div> : null}{operation.finished_at ? <div><dt>完成时间</dt><dd>{displayTime(operation.finished_at)}</dd></div> : null}</dl></> : <><strong>操作编号已保留</strong><p>提交结果未知时只查询此编号；404 也不表示检查未执行。</p><code>{write.operation_id}</code></>}
      </div> : null}
      <FormError error={error} />
    </div>
    <div className="dialog__actions health-dialog-actions"><Button type="button" variant="secondary" onClick={() => { sequence.current += 1; controller.current?.abort(); onClose() }}>关闭</Button>{write ? <Button type="button" disabled={busy} onClick={() => void queryOriginal()}>{busy ? '查询中…' : '查询原操作'}</Button> : <Button type="button" disabled={busy || !upstreamID || !proxy.enabled} onClick={() => void start()}>{busy ? '检查中…' : '开始仅握手检查'}</Button>}</div>
  </Dialog>
}
