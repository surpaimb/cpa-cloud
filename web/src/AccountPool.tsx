import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import {
  ApiError,
  api,
  type AccountChannel,
  type AccountGroup,
  type ModelAccount,
  type ModelAccounts,
  type ModelRoute,
  type Upstream,
} from './api'
import { Button, Dialog, Field, FormError, PageState } from './ui'

const encoder = new TextEncoder()

function validText(value: string, maxBytes: number) {
  return value.length > 0 && value === value.trim() && encoder.encode(value).length <= maxBytes && !/\p{Cc}/u.test(value)
}

function directoryError(error: unknown, action: 'create' | 'rename') {
  if (error instanceof ApiError) {
    if (error.status === 409 || error.code === 'revision_conflict') return action === 'rename' ? '分组已被其他管理员修改。当前输入已保留，请重新加载列表后再编辑。' : '名称或数据发生冲突，请重新加载列表后确认。'
    if (error.status < 500) return '提交内容无效或名称已存在，请检查后重试。'
  }
  return '无法确认本次操作是否成功。为避免重复创建或覆盖，请重新加载列表后再操作。'
}

function poolError(error: unknown) {
  if (error instanceof ApiError) {
    if (error.status === 409 || error.code === 'revision_conflict') return '账号池已被其他管理员修改。当前编辑已保留，请重新加载后再修改。'
    if (error.code === 'provider_mismatch') return '账号池中的账号必须属于同一服务商。'
    if (error.code === 'unsupported_feature') return '当前服务端未启用账号池配置。'
    if (error.status < 500) return '账号池配置无效，请检查每个字段后重试。'
  }
  return '无法确认本次保存是否成功。当前编辑已保留，请重新加载账号池确认。'
}

function isUncertain(error: unknown) {
  return !(error instanceof ApiError) || error.status >= 500
}

function isConflict(error: unknown) {
  return error instanceof ApiError && (error.status === 409 || error.code === 'revision_conflict')
}

export function AccountPoolDirectory({ csrf, onClose }: { csrf: string; onClose: () => void }) {
  const [groups, setGroups] = useState<AccountGroup[]>([])
  const [channels, setChannels] = useState<AccountChannel[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [groupName, setGroupName] = useState('')
  const [channelName, setChannelName] = useState('')
  const [channelGroup, setChannelGroup] = useState('')
  const [renames, setRenames] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState<string | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [needsReload, setNeedsReload] = useState(false)
  const requestVersion = useRef(0)
  const activeController = useRef<AbortController | null>(null)

  const reload = useCallback(async () => {
    const version = ++requestVersion.current
    activeController.current?.abort()
    const controller = new AbortController()
    activeController.current = controller
    setLoading(true)
    setLoadError(null)
    try {
      const [nextGroups, nextChannels] = await Promise.all([
        api.accountGroups(controller.signal),
        api.accountChannels(controller.signal),
      ])
      if (requestVersion.current !== version) return
      setGroups(nextGroups.items)
      setChannels(nextChannels.items)
      setRenames(Object.fromEntries(nextGroups.items.map((item) => [item.id, item.name])))
      setNeedsReload(false)
      setError(null)
    } catch (caught) {
      if (requestVersion.current === version && !(caught instanceof DOMException && caught.name === 'AbortError')) {
        setLoadError('无法读取分组与渠道，请稍后重试。')
      }
    } finally {
      if (requestVersion.current === version) setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
    return () => { requestVersion.current += 1; activeController.current?.abort() }
  }, [reload])

  async function createGroup(event: FormEvent) {
    event.preventDefault()
    const name = groupName.trim()
    if (!validText(name, 120)) return setError('分组名称需为 1–120 字节，且不能包含控制字符。')
    setBusy('create-group'); setError(null)
    try {
      const created = await api.createAccountGroup({ name }, csrf)
      setGroups((items) => [...items, created])
      setRenames((items) => ({ ...items, [created.id]: created.name }))
      setGroupName('')
    } catch (caught) {
      setError(directoryError(caught, 'create'))
      if (isUncertain(caught)) setNeedsReload(true)
    } finally { setBusy(null) }
  }

  async function createChannel(event: FormEvent) {
    event.preventDefault()
    const name = channelName.trim()
    if (!validText(name, 120)) return setError('渠道名称需为 1–120 字节，且不能包含控制字符。')
    setBusy('create-channel'); setError(null)
    try {
      const created = await api.createAccountChannel({ name, ...(channelGroup ? { group_id: channelGroup } : {}) }, csrf)
      setChannels((items) => [...items, created])
      setChannelName('')
      setChannelGroup('')
    } catch (caught) {
      setError(directoryError(caught, 'create'))
      if (isUncertain(caught)) setNeedsReload(true)
    } finally { setBusy(null) }
  }

  async function renameGroup(item: AccountGroup) {
    const name = (renames[item.id] ?? '').trim()
    if (!validText(name, 120)) return setError('分组名称需为 1–120 字节，且不能包含控制字符。')
    setBusy(`rename-${item.id}`); setError(null)
    try {
      const updated = await api.updateAccountGroup(item.id, { expected_revision: item.revision, name }, csrf)
      setGroups((items) => items.map((group) => group.id === item.id ? updated : group))
      setRenames((items) => ({ ...items, [item.id]: updated.name }))
    } catch (caught) {
      setError(directoryError(caught, 'rename'))
      if (isConflict(caught) || isUncertain(caught)) setNeedsReload(true)
    } finally { setBusy(null) }
  }

  return <Dialog title="分组与渠道" description="创建账号分组与渠道，供各模型账号池选用。当前不支持删除分组、删除渠道或修改渠道。" onClose={onClose} wide closeDisabled={busy !== null}>
    <PageState loading={loading} error={loadError} onRetry={() => void reload()} />
    {!loading && !loadError ? <div className="pool-directory">
      <section className="pool-section">
        <h3>账号分组</h3>
        <form className="pool-create-row" onSubmit={createGroup}>
          <Field label="新分组名称"><input value={groupName} onChange={(event) => setGroupName(event.target.value)} maxLength={120} /></Field>
          <Button type="submit" disabled={busy !== null || needsReload}>创建分组</Button>
        </form>
        <div className="pool-directory-list">{groups.length ? groups.map((item) => <div className="pool-directory-row" key={item.id}>
          <label htmlFor={`group-${item.id}`}>分组名称</label>
          <input id={`group-${item.id}`} value={renames[item.id] ?? item.name} onChange={(event) => setRenames((names) => ({ ...names, [item.id]: event.target.value }))} />
          <Button type="button" variant="secondary" disabled={busy !== null || needsReload || renames[item.id] === item.name} onClick={() => void renameGroup(item)}>{busy === `rename-${item.id}` ? '保存中…' : '重命名'}</Button>
        </div>) : <p className="muted-copy">还没有分组。渠道也可以不归属任何分组。</p>}</div>
      </section>
      <section className="pool-section">
        <h3>渠道</h3>
        <form className="pool-create-row pool-create-row--channel" onSubmit={createChannel}>
          <Field label="新渠道名称"><input value={channelName} onChange={(event) => setChannelName(event.target.value)} maxLength={120} /></Field>
          <Field label="所属分组（可选）"><select value={channelGroup} onChange={(event) => setChannelGroup(event.target.value)}><option value="">不分组</option>{groups.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>
          <Button type="submit" disabled={busy !== null || needsReload}>创建渠道</Button>
        </form>
        <div className="pool-channel-list">{channels.length ? channels.map((item) => <div key={item.id}><strong>{item.name}</strong><span>{groups.find((group) => group.id === item.group_id)?.name ?? '未分组'}</span></div>) : <p className="muted-copy">还没有渠道。</p>}</div>
      </section>
      <FormError error={error} />
      {needsReload ? <div className="pool-reload"><span>请先重新加载列表，确认服务端的最新状态。</span><Button type="button" variant="secondary" onClick={() => void reload()}>重新加载列表</Button></div> : null}
    </div> : null}
    <div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy !== null} onClick={onClose}>关闭</Button></div>
  </Dialog>
}

function validateAccounts(items: ModelAccount[], upstreams: Upstream[]) {
  if (items.length < 1 || items.length > 64) return '账号池必须包含 1–64 个账号。'
  const ids = new Set<string>()
  let provider: Upstream['provider_kind'] | undefined
  for (const item of items) {
    if (ids.has(item.upstream_id)) return '同一个上游账号不能重复添加。'
    ids.add(item.upstream_id)
    const upstream = upstreams.find((candidate) => candidate.id === item.upstream_id)
    if (!upstream) return '存在已删除的上游账号，请移除后再保存。'
    provider ??= upstream.provider_kind
    if (upstream.provider_kind !== provider) return '账号池中的账号必须属于同一服务商。'
    if (!validText(item.upstream_model, 256)) return '上游模型名称需为 1–256 字节，不能有首尾空格或控制字符。'
    if (!Number.isInteger(item.priority) || item.priority < -1_000_000 || item.priority > 1_000_000) return '优先级必须是 -1000000 到 1000000 的整数。'
    if (!Number.isInteger(item.weight) || item.weight < 1 || item.weight > 10_000) return '权重必须是 1 到 10000 的整数。'
    if (!Number.isInteger(item.max_concurrency) || item.max_concurrency < 1 || item.max_concurrency > 1024) return '最大并发必须是 1 到 1024 的整数。'
  }
  return null
}

export function ModelAccountPoolEditor({ model, csrf, routingEnabled, onClose }: { model: ModelRoute; csrf: string; routingEnabled: boolean; onClose: () => void }) {
  const [accounts, setAccounts] = useState<ModelAccounts | null>(null)
  const [items, setItems] = useState<ModelAccount[]>([])
  const [upstreams, setUpstreams] = useState<Upstream[]>([])
  const [channels, setChannels] = useState<AccountChannel[]>([])
  const [groups, setGroups] = useState<AccountGroup[]>([])
  const [loading, setLoading] = useState(true)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [mustReload, setMustReload] = useState(false)
  const [candidateID, setCandidateID] = useState('')
  const requestVersion = useRef(0)
  const activeController = useRef<AbortController | null>(null)

  const reload = useCallback(async () => {
    const version = ++requestVersion.current
    activeController.current?.abort()
    const controller = new AbortController()
    activeController.current = controller
    setLoading(true); setLoadError(null); setSaved(false)
    try {
      const [nextAccounts, nextUpstreams, nextChannels, nextGroups] = await Promise.all([
        api.modelAccounts(model.id, controller.signal),
        api.upstreams(controller.signal),
        api.accountChannels(controller.signal),
        api.accountGroups(controller.signal),
      ])
      if (requestVersion.current !== version) return
      setAccounts(nextAccounts)
      setItems(nextAccounts.items)
      setUpstreams(nextUpstreams.items)
      setChannels(nextChannels.items)
      setGroups(nextGroups.items)
      setMustReload(false)
      setSaveError(null)
    } catch (caught) {
      if (requestVersion.current === version && !(caught instanceof DOMException && caught.name === 'AbortError')) setLoadError('无法读取账号池配置，请稍后重试。')
    } finally {
      if (requestVersion.current === version) setLoading(false)
    }
  }, [model.id])

  useEffect(() => {
    void reload()
    return () => { requestVersion.current += 1; activeController.current?.abort() }
  }, [reload])

  const poolProvider = useMemo(() => {
    const baseID = items[0]?.upstream_id ?? model.upstream_id
    return upstreams.find((item) => item.id === baseID)?.provider_kind
  }, [items, model.upstream_id, upstreams])
  const candidates = useMemo(() => upstreams.filter((item) => item.enabled && item.provider_kind === poolProvider && !items.some((account) => account.upstream_id === item.id)), [items, poolProvider, upstreams])

  useEffect(() => {
    if (!candidates.some((item) => item.id === candidateID)) setCandidateID(candidates[0]?.id ?? '')
  }, [candidateID, candidates])

  function updateItem(index: number, patch: Partial<ModelAccount>) {
    setItems((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item))
    setSaved(false)
  }

  function addAccount() {
    if (!candidateID || items.length >= 64) return
    setItems((current) => [...current, { upstream_id: candidateID, upstream_model: model.upstream_model, priority: 0, weight: 1, max_concurrency: 1 }])
    setSaved(false)
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    if (!accounts) return
    const validation = validateAccounts(items, upstreams)
    if (validation) return setSaveError(validation)
    setBusy(true); setSaveError(null); setSaved(false)
    try {
      const result = await api.putModelAccounts(model.id, { expected_revision: accounts.revision, items }, csrf)
      setAccounts(result)
      setItems(result.items)
      setSaved(true)
    } catch (caught) {
      setSaveError(poolError(caught))
      if (isConflict(caught) || isUncertain(caught)) setMustReload(true)
    } finally { setBusy(false) }
  }

  return <Dialog title={`编辑账号池：${model.id}`} description="为同一个对外模型配置同服务商的多个上游账号。" onClose={onClose} wide closeDisabled={busy}>
    <PageState loading={loading} error={loadError} onRetry={() => void reload()} />
    {!loading && !loadError && accounts ? <form onSubmit={save}>
      <div className={`pool-routing-note ${routingEnabled ? 'pool-routing-note--enabled' : ''}`}>
        {routingEnabled ? '账号池路由已启用，保存后的配置会用于请求调度。' : '账号池路由尚未启用：配置可以保存，但当前请求仍使用原有单账号路由。'}
      </div>
      {accounts.revision === 0 ? <div className="membership-limitations"><strong>当前为兼容默认路由</strong>只有点击“保存账号池”才会写入新配置；打开或关闭此窗口都不会自动保存。</div> : null}
      <div className="pool-global-note">同一上游账号如果出现在多个已启用账号池中，全局并发上限按这些账号池里最小的“最大并发”执行。</div>
      <div className="pool-account-list">{items.map((item, index) => {
        const upstream = upstreams.find((candidate) => candidate.id === item.upstream_id)
        const disabled = upstream && !upstream.enabled
        const groupName = groups.find((group) => group.id === channels.find((channel) => channel.id === item.channel_id)?.group_id)?.name
        return <fieldset className="pool-account-card" key={item.upstream_id}>
          <legend>账号 {index + 1}</legend>
          <div className="pool-account-heading"><div><strong>{upstream?.name ?? item.upstream_id}</strong>{disabled ? <span className="pool-disabled-badge">已停用，可移除</span> : null}{!upstream ? <span className="pool-disabled-badge">已删除，可移除</span> : null}<small><code>{item.upstream_id}</code>{groupName ? ` · ${groupName}` : ''}</small></div><Button type="button" variant="danger" disabled={items.length === 1 || busy} onClick={() => { setItems((current) => current.filter((_, itemIndex) => itemIndex !== index)); setSaved(false) }}>移除</Button></div>
          <div className="pool-account-grid">
            <Field label={`账号 ${index + 1} 上游模型`}><input value={item.upstream_model} onChange={(event) => updateItem(index, { upstream_model: event.target.value })} /></Field>
            <Field label={`账号 ${index + 1} 渠道`}><select value={item.channel_id ?? ''} onChange={(event) => updateItem(index, { channel_id: event.target.value || undefined })}><option value="">无渠道</option>{channels.map((channel) => <option key={channel.id} value={channel.id}>{groups.find((group) => group.id === channel.group_id)?.name ? `${groups.find((group) => group.id === channel.group_id)?.name} / ` : ''}{channel.name}</option>)}</select></Field>
            <Field label={`账号 ${index + 1} 优先级`}><input type="number" min={-1000000} max={1000000} step={1} value={item.priority} onChange={(event) => updateItem(index, { priority: Number(event.target.value) })} /></Field>
            <Field label={`账号 ${index + 1} 权重`}><input type="number" min={1} max={10000} step={1} value={item.weight} onChange={(event) => updateItem(index, { weight: Number(event.target.value) })} /></Field>
            <Field label={`账号 ${index + 1} 最大并发`}><input type="number" min={1} max={1024} step={1} value={item.max_concurrency} onChange={(event) => updateItem(index, { max_concurrency: Number(event.target.value) })} /></Field>
          </div>
        </fieldset>
      })}</div>
      <div className="pool-add-account"><Field label="添加同服务商账号"><select value={candidateID} onChange={(event) => setCandidateID(event.target.value)} disabled={!candidates.length}><option value="">{candidates.length ? '请选择账号' : '没有可添加的已启用账号'}</option>{candidates.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Button type="button" variant="secondary" onClick={addAccount} disabled={!candidateID || items.length >= 64 || busy}>添加账号</Button></div>
      <FormError error={saveError} />
      {saved ? <div className="success-note" role="status">账号池已保存。</div> : null}
      {mustReload ? <div className="pool-reload"><span>保存前请重新加载服务器配置。重新加载会放弃当前编辑。</span><Button type="button" variant="secondary" onClick={() => void reload()}>重新加载账号池</Button></div> : null}
      <div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={onClose}>关闭</Button><Button type="submit" disabled={busy || mustReload}>{busy ? '保存中…' : '保存账号池'}</Button></div>
    </form> : null}
  </Dialog>
}
