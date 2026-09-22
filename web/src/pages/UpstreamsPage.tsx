import { useCallback, useState } from 'react'
import { api, type Upstream } from '../api'
import { messageFor, useResource } from '../hooks'
import { ModelDiscovery } from '../ModelDiscovery'
import { presetForEndpoint, providerPreset, providerPresets, type ProviderChoice, type ProviderPresetId } from '../providerPresets'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

export function UpstreamsPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.upstreams(), [])
  const { data, loading, error, reload } = useResource(load)
  const [creating, setCreating] = useState(false)
  const [syncing, setSyncing] = useState<Upstream | null>(null)
  return <>
    <PageHeader title="上游连接" description="连接兼容 OpenAI 协议的上游服务。密钥加密保存且不会再次显示。"><Button onClick={() => setCreating(true)}><Icon name="plus" />添加上游</Button></PageHeader>
    <div className="content-panel"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有上游连接" body="添加一个兼容 OpenAI 协议的上游，再配置模型路由。" action={<Button onClick={() => setCreating(true)}>添加上游</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table><thead><tr><th>名称</th><th>类型</th><th>端点</th><th>状态</th><th>操作</th></tr></thead><tbody>{data.items.map((item) => <UpstreamRow key={item.id} item={item} csrf={csrf} onSync={() => setSyncing(item)} onDone={() => void reload()} />)}</tbody></table></div> : null}
    </div>
    {creating ? <CreateUpstream csrf={csrf} onClose={() => setCreating(false)} onSaved={() => void reload()} /> : null}
    {syncing ? <Dialog title={`同步 ${syncing.name} 的模型`} description="仅读取上游返回的模型列表；选择后才会创建路由。" onClose={() => setSyncing(null)} wide>
      <ModelDiscovery upstream={syncing} csrf={csrf} />
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={() => setSyncing(null)}>完成</Button></div>
    </Dialog> : null}
  </>
}

function UpstreamRow({ item, csrf, onSync, onDone }: { item: Upstream; csrf: string; onSync: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  return <tr><td><strong>{item.name}</strong></td><td>OpenAI 兼容</td><td><code className="endpoint">{item.endpoint}</code></td><td><span className={`status status--${item.enabled ? 'active' : 'disabled'}`}><i />{item.enabled ? '启用' : '已停用'}</span></td><td><div className="row-actions"><button className="link-button" disabled={busy} onClick={onSync}>同步模型</button><span className="inline-action"><button className="link-button" disabled={busy} onClick={async () => {
    setBusy(true); setError(null)
    try { await api.updateUpstream(item.id, { expected_revision: item.revision, enabled: !item.enabled }, csrf); onDone() }
    catch (caught) { setError(messageFor(caught)); setBusy(false) }
  }}>{busy ? '处理中…' : item.enabled ? '停用' : '启用'}</button>{error ? <span className="action-error">{error}</span> : null}</span></div></td></tr>
}

export function CreateUpstream({ csrf, onClose, onSaved }: { csrf: string; onClose: () => void; onSaved: () => void }) {
  const initial = providerPreset('deepseek')
  const [provider, setProvider] = useState<ProviderChoice>(initial.id)
  const [name, setName] = useState(initial.name)
  const [nameIsAutomatic, setNameIsAutomatic] = useState(true)
  const [endpoint, setEndpoint] = useState(initial.endpoint)
  const [apiKey, setApiKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<Upstream | null>(null)

  function selectProvider(choice: ProviderChoice) {
    if (choice !== provider) setApiKey('')
    setProvider(choice)
    if (choice === 'custom') return
    const preset = providerPreset(choice as ProviderPresetId)
    setName(preset.name)
    setNameIsAutomatic(true)
    setEndpoint(preset.endpoint)
  }

  function changeEndpoint(next: string) {
    if (next !== endpoint) setApiKey('')
    setEndpoint(next)
    const matched = presetForEndpoint(next)
    setProvider(matched?.id ?? 'custom')
    if (matched && nameIsAutomatic) setName(matched.name)
  }

  if (saved) return <Dialog title="上游已保存" description="正在读取模型列表；只有你明确选择的模型才会创建路由。" onClose={onClose} wide>
    <div className="saved-upstream"><strong>{saved.name}</strong><code>{saved.endpoint}</code></div>
    <ModelDiscovery upstream={saved} csrf={csrf} savedNotice />
    <div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>完成</Button></div>
  </Dialog>

  return <Dialog title="添加上游" description="选择服务商会自动填写官方 API 地址；名称与地址仍可修改。" onClose={onClose}>
    <form onSubmit={async (event) => {
      event.preventDefault()
      setBusy(true); setError(null)
      try {
        const created = await api.createUpstream({ name: name.trim(), provider_kind: 'openai-compatible', endpoint: endpoint.trim(), api_key: apiKey }, csrf)
        setSaved(created)
        onSaved()
      } catch (caught) {
        setError(messageFor(caught))
        setBusy(false)
      }
    }}>
      <div className="form-grid">
        <Field label="服务商"><select value={provider} autoFocus onChange={(event) => selectProvider(event.target.value as ProviderChoice)}>
          {providerPresets.map((preset) => <option key={preset.id} value={preset.id}>{preset.name}</option>)}
          <option value="custom">自定义 OpenAI 兼容服务</option>
        </select></Field>
        <Field label="显示名称"><input name="name" value={name} onChange={(event) => { setName(event.target.value); setNameIsAutomatic(false) }} required /></Field>
        <Field label="API 端点" hint="仅精确匹配已核实的 HTTPS origin 与路径；自定义地址是否可用由服务端验证。"><input name="endpoint" type="url" value={endpoint} onChange={(event) => changeEndpoint(event.target.value)} required /></Field>
        {provider === 'custom' ? <div className="field-note" role="status">当前地址按自定义服务处理。修改服务商或地址后，API Key 必须重新填写。</div> : null}
        <Field label="API Key" hint="切换服务商或修改地址会立即清空此字段，避免凭据误发。"><input name="api_key" type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} required /></Field>
      </div>
      <FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存并同步模型'}</Button></div>
    </form>
  </Dialog>
}
