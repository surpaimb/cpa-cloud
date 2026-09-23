import { useCallback, useEffect, useRef, useState } from 'react'
import {
  api,
  ApiError,
  type CreateOutboundProxy,
  type OutboundProxy,
  type OutboundProxyPage,
  type UpdateOutboundProxy,
  type Upstream,
  type UpstreamProxyState,
} from '../api'
import { messageFor } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

type BindingRead = { state: UpstreamProxyState | null; failed: boolean }
type Bindings = Record<string, BindingRead>
type Verification = 'none' | 'verified' | 'failed'

function uncertainWrite(error: unknown) {
  return !(error instanceof ApiError) || error.status >= 500
}

function proxyError(error: unknown) {
  if (error instanceof ApiError) {
    if (error.code === 'unsupported_proxy_binding') return '此账号类型或目标地址不支持出站代理。'
    if (error.status === 409) return '记录已被其他操作更新；没有覆盖实际状态。'
    return error.message
  }
  return '网络连接中断，无法确认操作是否已经生效。'
}

function bindingUnsupported(upstream: Upstream) {
  if (upstream.provider_kind === 'codex-membership') return 'Codex 使用服务端固定目标，当前不支持绑定代理。'
  try {
    if (new URL(upstream.endpoint).protocol !== 'https:') return 'HTTP 目标不支持绑定代理。'
  } catch {
    return '目标地址无效，不能绑定代理。'
  }
  return null
}

export function ProxiesPage({ csrf }: { csrf: string }) {
  const [page, setPage] = useState<OutboundProxyPage | null>(null)
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [bindings, setBindings] = useState<Bindings>({})
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [unsupported, setUnsupported] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<OutboundProxy | null>(null)
  const [binding, setBinding] = useState<Upstream | null>(null)
  const generation = useRef(0)

  const reload = useCallback(async () => {
    const current = ++generation.current
    setLoading(true)
    setError(null)
    setUnsupported(false)
    try {
      const [proxyPage, upstreamPage] = await Promise.all([api.outboundProxies(), api.upstreams()])
      if (current !== generation.current) return
      setPage(proxyPage)
      setUpstreams(upstreamPage.items)
      const results = await Promise.all(upstreamPage.items.map(async (item) => {
        try { return [item.id, { state: await api.upstreamProxy(item.id), failed: false }] as const }
        catch { return [item.id, { state: null, failed: true }] as const }
      }))
      if (current === generation.current) setBindings(Object.fromEntries(results))
    } catch (caught) {
      if (current !== generation.current) return
      if (caught instanceof ApiError && caught.status === 404) {
        setUnsupported(true)
        setError('当前服务版本不支持出站代理管理，请升级服务后重试。')
      } else setError(messageFor(caught))
    } finally {
      if (current === generation.current) setLoading(false)
    }
  }, [])

  useEffect(() => { void reload(); return () => { generation.current += 1 } }, [reload])

  async function loadMore() {
    if (!page?.next_cursor || loadingMore) return
    const cursor = page.next_cursor
    setLoadingMore(true)
    try {
      const next = await api.outboundProxies(cursor)
      setPage((current) => current?.next_cursor === cursor
        ? { items: [...current.items, ...next.items], next_cursor: next.next_cursor }
        : current)
    } catch (caught) { setError(messageFor(caught)) }
    finally { setLoadingMore(false) }
  }

  const proxies = page?.items ?? []
  return <>
    <PageHeader title="出站代理" description="管理 API Key 账号使用的 HTTPS CONNECT 代理。保存配置不代表代理连通或模型生成正常。">
      <Button onClick={() => setCreating(true)} disabled={unsupported}><Icon name="plus" />添加代理</Button>
    </PageHeader>
    <section className="proxy-notice" aria-label="代理边界说明">
      <strong>连接边界</strong>
      <p>公网 scope 只允许公网地址；公司内网 scope 可访问公司内网代理，但仍不能把模型目标改为内网地址。停用代理不会自动解绑账号。</p>
      <p>编辑代理不会清除账号的故障冷却或恢复隔离。页面不执行握手测试，也不展示代理密码。</p>
    </section>
    <section className="content-panel proxy-panel">
      <div className="section-heading"><div><h2>代理目录</h2><p>连接版本用于执行快照；名称修改不代表连接已变化。</p></div></div>
      <PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && proxies.length === 0 ? <EmptyState title="还没有出站代理" body="添加 HTTPS CONNECT 代理后，可再为支持的 API Key 账号绑定。" action={<Button onClick={() => setCreating(true)}>添加代理</Button>} /> : null}
      {proxies.length ? <div className="table-scroll"><table className="proxy-table"><thead><tr><th>名称</th><th>代理地址</th><th>范围</th><th>认证</th><th>状态</th><th>版本</th><th>操作</th></tr></thead><tbody>
        {proxies.map((item) => <tr key={item.id}><td><strong>{item.name}</strong><small><code>{item.id}</code></small></td><td><code>{item.host}:{item.port}</code><small>HTTPS CONNECT</small></td><td>{item.address_scope === 'private' ? '公司内网' : '公网'}</td><td>{item.has_credentials ? '已保存' : '无认证'}</td><td><span className={`status status--${item.enabled ? 'active' : 'disabled'}`}><i />{item.enabled ? '启用' : '已停用'}</span></td><td>配置 {item.revision}<small>连接 {item.connection_revision}</small></td><td><button className="link-button" onClick={() => setEditing(item)}>编辑 / 启停</button></td></tr>)}
      </tbody></table></div> : null}
      {page?.next_cursor ? <div className="pagination"><span>已显示 {proxies.length} 条</span><Button variant="secondary" disabled={loadingMore} onClick={() => void loadMore()}>{loadingMore ? '读取中…' : '加载更多'}</Button></div> : null}
    </section>
    <section className="content-panel proxy-panel proxy-bindings">
      <div className="section-heading"><div><h2>账号绑定</h2><p>只为 HTTPS API Key 账号选择代理；明确解绑后该账号恢复直连。</p></div></div>
      {!loading && upstreams.length === 0 ? <EmptyState title="还没有上游账号" body="请先在“上游连接”中添加账号。" /> : null}
      {upstreams.length ? <div className="table-scroll"><table className="proxy-table"><thead><tr><th>账号</th><th>目标</th><th>当前连接</th><th>操作</th></tr></thead><tbody>
        {upstreams.map((item) => {
          const reason = bindingUnsupported(item)
          const read = bindings[item.id]
          const current = read?.state?.binding
          const status = !read ? <span className="muted-copy">正在读取 / 未确认</span> : read.failed ? <span className="proxy-binding-limit">状态未确认 / 读取失败</span> : current ? <><strong>{current.name}</strong><small>{current.enabled ? '代理已启用' : '代理已停用，绑定仍保留'}</small></> : <span className="muted-copy">直连</span>
          return <tr key={item.id}><td><strong>{item.name}</strong><small>{item.provider_kind}</small></td><td><code className="endpoint">{item.provider_kind === 'codex-membership' ? '服务端固定' : item.endpoint}</code></td><td>{status}</td><td>{reason ? <span className="proxy-binding-limit">{reason}</span> : !read ? <span className="muted-copy">正在读取</span> : read.failed ? <button className="link-button" onClick={() => void reload()}>重新读取绑定</button> : <button className="link-button" onClick={() => setBinding(item)}>{current ? '修改 / 解绑' : '绑定代理'}</button>}</td></tr>
        })}
      </tbody></table></div> : null}
    </section>
    {creating ? <CreateProxyDialog csrf={csrf} onClose={() => setCreating(false)} onSaved={() => { setCreating(false); void reload() }} /> : null}
    {editing ? <EditProxyDialog key={editing.id} proxyID={editing.id} csrf={csrf} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void reload() }} /> : null}
    {binding ? <BindingDialog key={binding.id} upstream={binding} proxies={proxies} csrf={csrf} onClose={() => setBinding(null)} onSaved={() => { setBinding(null); void reload() }} /> : null}
  </>
}

type ProxyFields = { name: string; host: string; port: string; scope: 'public' | 'private'; enabled: boolean; username: string; password: string }
const emptyFields = (): ProxyFields => ({ name: '', host: '', port: '443', scope: 'public', enabled: true, username: '', password: '' })

function ProxyForm({ fields, setFields, locked, includeCredentialMode, credentialMode, setCredentialMode }: {
  fields: ProxyFields
  setFields: (fields: ProxyFields) => void
  locked: boolean
  includeCredentialMode?: boolean
  credentialMode?: UpdateOutboundProxy['credential_mode']
  setCredentialMode?: (mode: UpdateOutboundProxy['credential_mode']) => void
}) {
  const showCredentials = !includeCredentialMode || credentialMode === 'replace'
  return <div className="proxy-form-grid">
    <Field label="显示名称"><input value={fields.name} onChange={(event) => setFields({ ...fields, name: event.target.value })} disabled={locked} required autoFocus maxLength={120} /></Field>
    <Field label="代理主机" hint="填写主机名或裸 IP，不要填写 URL。"><input value={fields.host} onChange={(event) => setFields({ ...fields, host: event.target.value })} disabled={locked} required /></Field>
    <Field label="端口"><input type="number" min="1" max="65535" value={fields.port} onChange={(event) => setFields({ ...fields, port: event.target.value })} disabled={locked} required /></Field>
    <Field label="地址范围"><select value={fields.scope} onChange={(event) => setFields({ ...fields, scope: event.target.value as ProxyFields['scope'] })} disabled={locked}><option value="public">公网</option><option value="private">公司内网</option></select></Field>
    <label className="toggle-field"><input type="checkbox" checked={fields.enabled} onChange={(event) => setFields({ ...fields, enabled: event.target.checked })} disabled={locked} /><span><strong>启用代理</strong><small>停用不会自动解绑现有账号。</small></span></label>
    {includeCredentialMode ? <Field label="代理认证"><select value={credentialMode} onChange={(event) => setCredentialMode?.(event.target.value as UpdateOutboundProxy['credential_mode'])} disabled={locked}><option value="keep">保留现有认证</option><option value="replace">替换认证</option><option value="clear">清除认证</option></select></Field> : null}
    {showCredentials ? <><Field label="用户名" hint="无需认证时，用户名和密码均留空。"><input autoComplete="off" value={fields.username} onChange={(event) => setFields({ ...fields, username: event.target.value })} disabled={locked} /></Field><Field label="密码"><input type="password" autoComplete="new-password" value={fields.password} onChange={(event) => setFields({ ...fields, password: event.target.value })} disabled={locked} /></Field></> : null}
  </div>
}

function CreateProxyDialog({ csrf, onClose, onSaved }: { csrf: string; onClose: () => void; onSaved: () => void }) {
  const [fields, setFields] = useState(emptyFields)
  const [operationID, setOperationID] = useState(() => crypto.randomUUID())
  const [attempt, setAttempt] = useState<CreateOutboundProxy | null>(null)
  const [busy, setBusy] = useState(false)
  const [uncertain, setUncertain] = useState(false)
  const [error, setError] = useState<string | null>(null)

  function build(): CreateOutboundProxy | null {
    const username = fields.username
    if (!username && fields.password) { setError('填写密码时必须同时填写用户名。'); return null }
    const body: CreateOutboundProxy = { operation_id: operationID, name: fields.name.trim(), scheme: 'https', host: fields.host.trim(), port: Number(fields.port), address_scope: fields.scope, enabled: fields.enabled }
    if (username) body.credentials = { username, password: fields.password }
    return body
  }

  async function send(body: CreateOutboundProxy) {
    setBusy(true); setError(null)
    try { await api.createOutboundProxy(body, csrf); setFields(emptyFields()); setAttempt(null); onSaved() }
    catch (caught) { setUncertain(uncertainWrite(caught)); setError(proxyError(caught)); setBusy(false) }
  }

  function abandon() {
    setFields(emptyFields())
    setOperationID(crypto.randomUUID())
    setAttempt(null); setUncertain(false); setError(null)
  }

  return <Dialog title="添加出站代理" description="只保存 HTTPS CONNECT 配置；不会在此验证连通性。" onClose={onClose} closeDisabled={busy}>
    <form onSubmit={(event) => { event.preventDefault(); const body = attempt ?? build(); if (!body) return; if (!attempt) setAttempt(body); void send(body) }}>
      <ProxyForm fields={fields} setFields={setFields} locked={Boolean(attempt)} />
      {uncertain ? <div className="proxy-uncertain" role="alert"><strong>创建结果未知</strong><p>服务可能已经保存此代理。只能显式重试原创建操作，系统会复用同一操作编号和完全相同的载荷。</p></div> : <FormError error={error} />}
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={attempt ? abandon : onClose}>{attempt ? '放弃本次操作并重新填写' : '取消'}</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : uncertain ? '重试原创建操作' : attempt ? '重试相同操作' : '保存代理'}</Button></div>
    </form>
  </Dialog>
}

function EditProxyDialog({ proxyID, csrf, onClose, onSaved }: { proxyID: string; csrf: string; onClose: () => void; onSaved: () => void }) {
  const [proxy, setProxy] = useState<OutboundProxy | null>(null)
  const [fields, setFields] = useState(emptyFields)
  const [mode, setMode] = useState<UpdateOutboundProxy['credential_mode']>('keep')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [verification, setVerification] = useState<Verification>('none')
  const [error, setError] = useState<string | null>(null)

  const loadActual = useCallback(async (signal?: AbortSignal) => {
    const actual = await api.outboundProxy(proxyID, signal)
    setProxy(actual)
    setFields({ name: actual.name, host: actual.host, port: String(actual.port), scope: actual.address_scope, enabled: actual.enabled, username: '', password: '' })
    setMode('keep')
    return actual
  }, [proxyID])

  useEffect(() => {
    const controller = new AbortController()
    setLoading(true)
    loadActual(controller.signal).catch((caught) => { if (!controller.signal.aborted) setError(messageFor(caught)) }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [loadActual])

  async function verify(caught: unknown) {
    setFields((value) => ({ ...value, username: '', password: '' }))
    setVerification('failed')
    setError(proxyError(caught))
    try { await loadActual(); setVerification('verified') }
    catch { setError('无法确认写入结果，也无法读取代理的实际状态。请重新读取实际状态。') }
  }

  async function retryVerification() {
    setBusy(true)
    try { await loadActual(); setVerification('verified'); setError('已读取代理的当前状态。') }
    catch { setVerification('failed'); setError('仍无法读取代理的实际状态。写入保持锁定。') }
    finally { setBusy(false) }
  }

  async function save() {
    if (!proxy) return
    if (mode === 'replace' && !fields.username) { setError('替换认证时必须填写用户名；密码可以为空。'); return }
    const body: UpdateOutboundProxy = { expected_revision: proxy.revision, name: fields.name.trim(), scheme: 'https', host: fields.host.trim(), port: Number(fields.port), address_scope: fields.scope, enabled: fields.enabled, credential_mode: mode }
    if (mode === 'replace') body.credentials = { username: fields.username, password: fields.password }
    setBusy(true); setError(null)
    try { await api.updateOutboundProxy(proxyID, body, csrf); setFields(emptyFields()); onSaved() }
    catch (caught) {
      if (uncertainWrite(caught) || caught instanceof ApiError && caught.status === 409) await verify(caught)
      else { setFields((value) => ({ ...value, username: '', password: '' })); setError(proxyError(caught)) }
      setBusy(false)
    }
  }

  return <Dialog title="编辑出站代理" description="认证默认保持不变；密码不会从服务端读回。" onClose={onClose} closeDisabled={busy}>
    {loading ? <div className="loading" role="status"><span />正在读取实际状态…</div> : proxy ? <form onSubmit={(event) => { event.preventDefault(); void save() }}>
      <ProxyForm fields={fields} setFields={setFields} locked={busy || verification !== 'none'} includeCredentialMode credentialMode={mode} setCredentialMode={(next) => { setMode(next); setFields((value) => ({ ...value, username: '', password: '' })) }} />
      {verification !== 'none' ? <div className="proxy-uncertain" role="alert"><strong>{verification === 'verified' ? '已读取实际状态，请核对' : '实际状态尚未确认'}</strong><p>{error} 页面没有自动重放写入。{verification === 'verified' ? `当前显示版本 ${proxy.revision}。` : '在成功读取前不能继续写入。'}</p></div> : <FormError error={error} />}
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button>{verification === 'verified' ? <Button type="button" disabled={busy} onClick={(event) => { event.preventDefault(); setVerification('none'); setError(null) }}>按最新状态重新编辑</Button> : verification === 'failed' ? <Button type="button" disabled={busy} onClick={() => void retryVerification()}>{busy ? '读取中…' : '重新读取实际状态'}</Button> : <Button type="submit" disabled={busy}>{busy ? '保存中…' : '保存变更'}</Button>}</div>
    </form> : <><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>关闭</Button><Button type="button" onClick={() => { setLoading(true); setError(null); loadActual().catch((caught) => setError(messageFor(caught))).finally(() => setLoading(false)) }}>重新读取实际状态</Button></div></>}
  </Dialog>
}

function BindingDialog({ upstream, proxies, csrf, onClose, onSaved }: { upstream: Upstream; proxies: OutboundProxy[]; csrf: string; onClose: () => void; onSaved: () => void }) {
  const [proxySnapshots, setProxySnapshots] = useState(proxies)
  const [actual, setActual] = useState<UpstreamProxyState | null>(null)
  const [selected, setSelected] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [verification, setVerification] = useState<Verification>('none')
  const [error, setError] = useState<string | null>(null)
  const enabled = proxySnapshots.filter((item) => item.enabled)

  const loadActual = useCallback(async (signal?: AbortSignal) => {
    const state = await api.upstreamProxy(upstream.id, signal)
    setActual(state)
    setSelected(state.binding?.proxy_id ?? enabled[0]?.id ?? '')
    return state
  }, [enabled, upstream.id])

  useEffect(() => {
    const controller = new AbortController()
    loadActual(controller.signal).catch((caught) => { if (!controller.signal.aborted) setError(messageFor(caught)) }).finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
    // The proxy list is a snapshot for this dialog; reopening refreshes it.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [upstream.id])

  async function verify(caught: unknown) {
    setVerification('failed'); setError(proxyError(caught))
    try { await loadActual(); setVerification('verified') }
    catch { setError('无法确认绑定结果，也无法读取账号的实际状态。请重新读取实际状态。') }
  }

  async function retryVerification() {
    setBusy(true)
    try { await loadActual(); setVerification('verified'); setError('已读取账号的当前绑定。') }
    catch { setVerification('failed'); setError('仍无法读取账号的实际绑定。写入保持锁定。') }
    finally { setBusy(false) }
  }

  function bindingChanged(current: UpstreamProxyState) {
    if (!actual || current.upstream_revision !== actual.upstream_revision) return true
    const before = actual.binding
    const after = current.binding
    return before?.proxy_id !== after?.proxy_id || before?.proxy_revision !== after?.proxy_revision || before?.connection_revision !== after?.connection_revision || before?.enabled !== after?.enabled
  }

  async function write(bind: boolean) {
    setBusy(true); setError(null)
    try {
      const current = await api.upstreamProxy(upstream.id)
      if (bindingChanged(current)) {
        setActual(current)
        setSelected(current.binding?.proxy_id ?? enabled[0]?.id ?? '')
        setVerification('verified')
        setError('账号版本或绑定已在操作前变化；已显示最新状态。')
        setBusy(false)
        return
      }
      const proxyID = bind ? selected : current.binding?.proxy_id
      if (!proxyID) throw new ApiError(409, 'binding_conflict', '当前账号没有可解绑的代理。')
      let proxyRevision = current.binding?.proxy_revision ?? 0
      if (bind) {
        const latestProxy = await api.outboundProxy(proxyID)
        const shownProxy = proxySnapshots.find((item) => item.id === proxyID)
        if (!shownProxy || shownProxy.revision !== latestProxy.revision) {
          setProxySnapshots((items) => [...items.filter((item) => item.id !== latestProxy.id), latestProxy])
          setActual(current)
          setSelected(latestProxy.id)
          setVerification('verified')
          setError(`所选代理配置已变化；当前为 ${latestProxy.host}:${latestProxy.port}，配置版本 ${latestProxy.revision}${latestProxy.enabled ? '' : '，且已停用'}。`)
          setBusy(false)
          return
        }
        if (!latestProxy.enabled) throw new ApiError(409, 'binding_conflict', '所选代理已停用。')
        proxyRevision = latestProxy.revision
      }
      await api.putUpstreamProxy(upstream.id, { expected_upstream_revision: current.upstream_revision, proxy_id: proxyID, expected_proxy_revision: proxyRevision, bind }, csrf)
      onSaved()
    } catch (caught) {
      if (uncertainWrite(caught) || caught instanceof ApiError && caught.status === 409) await verify(caught)
      else setError(proxyError(caught))
      setBusy(false)
    }
  }

  return <Dialog title={`配置 ${upstream.name} 的代理`} description="绑定只选择连接路径，不代表代理或模型可用。" onClose={onClose} closeDisabled={busy}>
    {loading ? <div className="loading" role="status"><span />正在读取实际绑定…</div> : actual ? <>
      <div className="proxy-state-summary"><span>当前连接</span><strong>{actual.binding ? actual.binding.name : '直连'}</strong>{actual.binding && !actual.binding.enabled ? <small>该代理已停用，但绑定仍保留。</small> : null}</div>
      <Field label="选择已启用代理" hint="停用代理不会用于新绑定；现有停用绑定仍可明确解绑。"><select value={selected} onChange={(event) => setSelected(event.target.value)} disabled={busy || verification !== 'none'}><option value="">请选择</option>{enabled.map((item) => <option key={item.id} value={item.id}>{item.name} · {item.host}:{item.port}</option>)}</select></Field>
      {verification !== 'none' ? <div className="proxy-uncertain" role="alert"><strong>{verification === 'verified' ? '已读取实际绑定，请核对' : '实际绑定尚未确认'}</strong><p>{error} 页面没有自动重放写入。{verification === 'failed' ? '在成功读取前不能继续写入。' : ''}</p></div> : <FormError error={error} />}
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button>{verification === 'none' && actual.binding ? <Button type="button" variant="danger" disabled={busy} onClick={() => void write(false)}>明确解绑为直连</Button> : null}{verification === 'verified' ? <Button type="button" disabled={busy} onClick={() => { setVerification('none'); setError(null) }}>按实际状态重新选择</Button> : verification === 'failed' ? <Button type="button" disabled={busy} onClick={() => void retryVerification()}>{busy ? '读取中…' : '重新读取实际状态'}</Button> : <Button type="button" disabled={busy || !selected} onClick={() => void write(true)}>{busy ? '处理中…' : actual.binding ? '保存绑定' : '绑定代理'}</Button>}</div>
    </> : <><FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>关闭</Button></div></>}
  </Dialog>
}
