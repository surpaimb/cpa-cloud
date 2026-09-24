import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { api, type DiscoveredModel, type ModelRoute } from '../api'
import { AccountPoolDirectory, ModelAccountPoolEditor } from '../AccountPool'
import { messageFor, useResource } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

export function ModelsPage({ csrf }: { csrf: string }) {
	const [includeArchived, setIncludeArchived] = useState(false)
	const loadModels = useCallback(() => api.models(includeArchived), [includeArchived])
  const { data, loading, error, reload } = useResource(loadModels)
  const loadStatus = useCallback(() => api.status(), [])
  const { data: status } = useResource(loadStatus)
  const [creating, setCreating] = useState(false)
  const [managingDirectory, setManagingDirectory] = useState(false)
  const [poolModel, setPoolModel] = useState<NonNullable<typeof data>['items'][number] | null>(null)
	const [editing, setEditing] = useState<ModelRoute | null>(null)
  const poolConfiguration = status?.features?.account_pool_configuration === true
  const poolRouting = status?.features?.account_pool_routing === true
	const lifecycle = status?.features?.account_lifecycle_management === true
  return <>
    <PageHeader title="模型路由" description="将员工可用的模型名称映射到已配置的上游模型。"><div className="page-header-buttons">{lifecycle ? <Button variant="secondary" onClick={() => setIncludeArchived((value) => !value)}>{includeArchived ? '隐藏已归档' : '查看已归档'}</Button> : null}{poolConfiguration ? <Button variant="secondary" onClick={() => setManagingDirectory(true)}><Icon name="settings" />分组与渠道</Button> : null}<Button onClick={() => setCreating(true)}><Icon name="plus" />添加模型路由</Button></div></PageHeader>
    {poolConfiguration ? <div className={`membership-panel ${poolRouting ? 'membership-panel--enabled' : ''}`}><div><h2>{poolRouting ? '账号池路由已启用' : '账号池配置可用，路由尚未启用'}</h2><p>{poolRouting ? '可以为每个模型配置同服务商的多个账号，并按优先级、权重与并发上限调度。' : '可以提前保存账号池配置，但当前请求仍使用原有单账号路由，保存内容暂不参与请求调度。'}</p></div></div> : null}
    <div className="content-panel"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有模型路由" body="先添加上游连接，再建立第一个模型路由。" action={<Button onClick={() => setCreating(true)}>添加模型路由</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table><thead><tr><th>对外模型 ID</th><th>上游</th><th>上游模型</th><th>状态</th>{poolConfiguration || lifecycle ? <th>操作</th> : null}</tr></thead><tbody>{data.items.map((item) => <tr key={item.id}><td><strong>{item.id}</strong>{item.revision ? <small>r{item.revision}</small> : null}</td><td><code>{item.upstream_id}</code></td><td>{item.upstream_model}</td><td><span className={`status status--${item.archived ? 'disabled' : item.enabled ? 'active' : 'disabled'}`}><i />{item.archived ? '已归档' : item.enabled ? '启用' : '已停用'}</span></td>{poolConfiguration || lifecycle ? <td><div className="row-actions">{poolConfiguration && !item.archived ? <button type="button" className="link-button" onClick={() => setPoolModel(item)}>编辑账号池</button> : null}{lifecycle && !item.archived ? <button type="button" className="link-button" onClick={() => setEditing(item)}>修改 / 归档</button> : null}</div></td> : null}</tr>)}</tbody></table></div> : null}
    </div>
    {creating ? <CreateModel csrf={csrf} onClose={() => setCreating(false)} onCreated={() => { setCreating(false); void reload() }} /> : null}
    {managingDirectory ? <AccountPoolDirectory csrf={csrf} onClose={() => setManagingDirectory(false)} /> : null}
    {poolModel ? <ModelAccountPoolEditor key={poolModel.id} model={poolModel} csrf={csrf} routingEnabled={poolRouting} onClose={() => setPoolModel(null)} /> : null}
	{editing ? <EditModelRoute csrf={csrf} item={editing} onClose={() => setEditing(null)} onDone={() => { setEditing(null); void reload() }} /> : null}
  </>
}

function EditModelRoute({ csrf, item, onClose, onDone }: { csrf: string; item: ModelRoute; onClose: () => void; onDone: () => void }) {
	const loadUpstreams = useCallback(() => api.upstreams(), [])
	const { data, loading, error, reload } = useResource(loadUpstreams)
	const [upstreamID, setUpstreamID] = useState(item.upstream_id)
	const [upstreamModel, setUpstreamModel] = useState(item.upstream_model)
	const [enabled, setEnabled] = useState(item.enabled)
	const [busy, setBusy] = useState(false)
	const [saveError, setSaveError] = useState<string | null>(null)
	const revision = item.revision ?? 0
	return <Dialog title={`修改 ${item.id}`} description="公开模型 ID 不可更名。账号池非空时，请先清空账号池再修改默认目标。" onClose={onClose}>
		<PageState loading={loading} error={error} onRetry={() => void reload()} />
		{!loading && !error ? <form onSubmit={async (event) => {
			event.preventDefault(); setBusy(true); setSaveError(null)
			try { await api.updateModel(item.id, { expected_revision: revision, upstream_id: upstreamID, upstream_model: upstreamModel.trim(), enabled }, csrf); onDone() }
			catch (caught) { setSaveError(messageFor(caught)); setBusy(false) }
		}}><div className="form-grid">
			<Field label="上游连接"><select value={upstreamID} onChange={(event) => setUpstreamID(event.target.value)}>{data?.items.filter((upstream) => !upstream.archived && (upstream.enabled || upstream.id === item.upstream_id)).map((upstream) => <option key={upstream.id} value={upstream.id}>{upstream.name}</option>)}</select></Field>
			<Field label="上游模型"><input value={upstreamModel} onChange={(event) => setUpstreamModel(event.target.value)} required /></Field>
			<label className="toggle-field"><input type="checkbox" checked={enabled} onChange={(event) => setEnabled(event.target.checked)} /><span><strong>启用路由</strong><small>停用后员工目录和新请求立即不再使用。</small></span></label>
		</div><FormError error={saveError} /><div className="lifecycle-warning"><strong>归档不可恢复</strong><p>归档会保留此 ID 和历史账本关联，并禁止用相同 ID 新建路由。</p></div><div className="dialog__actions"><Button type="button" variant="secondary" disabled={busy} onClick={async () => {
			if (!window.confirm(`确认归档模型 ${item.id}？归档后不能恢复，也不能重新使用此 ID。`)) return
			setBusy(true); setSaveError(null)
			try { await api.archiveModel(item.id, revision, csrf); onDone() } catch (caught) { setSaveError(messageFor(caught)); setBusy(false) }
		}}>永久归档</Button><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存修改'}</Button></div></form> : null}
	</Dialog>
}

export function CreateModel({ csrf, onClose, onCreated }: { csrf: string; onClose: () => void; onCreated: () => void }) {
  const loadUpstreams = useCallback(() => api.upstreams(), [])
  const { data, loading, error, reload } = useResource(loadUpstreams)
  const [upstreamID, setUpstreamID] = useState('')
  const [discovered, setDiscovered] = useState<DiscoveredModel[]>([])
  const [discovering, setDiscovering] = useState(false)
  const [discoverError, setDiscoverError] = useState<string | null>(null)
  const [upstreamModel, setUpstreamModel] = useState('')
  const [externalID, setExternalID] = useState('')
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const requestVersion = useRef(0)
  const enabled = useMemo(() => data?.items.filter((item) => item.enabled) ?? [], [data])
  const selectedUpstream = useMemo(() => enabled.find((item) => item.id === upstreamID), [enabled, upstreamID])
  const canDiscover = Boolean(selectedUpstream)

  useEffect(() => {
    if (enabled.length === 0) {
      if (upstreamID) setUpstreamID('')
      return
    }
    if (!enabled.some((item) => item.id === upstreamID)) setUpstreamID(enabled[0].id)
  }, [enabled, upstreamID])

  const discover = useCallback(async () => {
    if (!upstreamID || !canDiscover) return
    const version = ++requestVersion.current
    setDiscovering(true)
    setDiscoverError(null)
    setDiscovered([])
    try {
      const result = await api.discoverUpstreamModels(upstreamID, csrf)
      if (requestVersion.current !== version) return
      const seen = new Set<string>()
      setDiscovered(result.items.filter((item) => {
        if (!item.id || seen.has(item.id)) return false
        seen.add(item.id)
        return true
      }))
    } catch (caught) {
      if (requestVersion.current === version) setDiscoverError(messageFor(caught))
    } finally {
      if (requestVersion.current === version) setDiscovering(false)
    }
  }, [canDiscover, csrf, upstreamID])

  useEffect(() => {
    if (upstreamID && canDiscover) void discover()
    return () => { requestVersion.current += 1 }
  }, [canDiscover, discover, upstreamID])

  function chooseUpstream(next: string) {
    requestVersion.current += 1
    setUpstreamID(next)
    setDiscovered([])
    setDiscoverError(null)
    setUpstreamModel('')
    setExternalID('')
  }

  return <Dialog title="添加模型路由" description="选择上游后会读取可用模型，但不会自动创建或启用路由。" onClose={onClose}>
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {!loading && !error && enabled.length === 0 ? <EmptyState title="没有可用上游" body="请先在“上游连接”中添加并启用上游。" /> : null}
    {enabled.length ? <form onSubmit={async (event) => {
      event.preventDefault()
      setBusy(true); setSaveError(null)
      try {
        await api.createModel({ id: externalID.trim(), upstream_id: upstreamID, upstream_model: upstreamModel.trim() }, csrf)
        onCreated()
      } catch (caught) {
        setSaveError(messageFor(caught))
        setBusy(false)
      }
    }}><div className="form-grid">
      <Field label="上游连接"><select name="upstream_id" value={upstreamID} onChange={(event) => chooseUpstream(event.target.value)} required autoFocus>{enabled.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field>
      <Field label="上游模型名称" hint="可从同步结果选择，也可以手动输入上游支持的模型 ID。"><input name="upstream_model" list="discovered-models" value={upstreamModel} placeholder="例如：gpt-4.1" required onChange={(event) => {
        const value = event.target.value
        setUpstreamModel(value)
        if (discovered.some((item) => item.id === value)) setExternalID(value)
      }} /></Field>
      <datalist id="discovered-models">{discovered.map((item) => <option key={item.id} value={item.id} />)}</datalist>
      {canDiscover && discovering ? <div className="field-note" role="status">正在同步模型列表…</div> : null}
      {canDiscover && discoverError ? <div className="model-discovery-error" role="alert"><span>模型同步失败：{discoverError}</span><button type="button" className="link-button" onClick={() => void discover()}>重试同步</button></div> : null}
      {canDiscover && !discovering && !discoverError ? <div className="field-note" role="status">已同步 {discovered.length} 个模型；请选择或手动输入。</div> : null}
      <Field label="对外模型 ID" hint="默认使用所选上游模型 ID，你可以在保存前修改。"><input name="id" value={externalID} placeholder="例如：gpt-4.1" required onChange={(event) => setExternalID(event.target.value)} /></Field>
    </div><FormError error={saveError} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy || discovering}>{busy ? '正在添加…' : '添加路由'}</Button></div></form> : null}
  </Dialog>
}
