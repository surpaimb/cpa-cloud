import { useCallback, useState, type ChangeEvent } from 'react'
import { ApiError, api, type Upstream } from '../api'
import { CodexOAuthAuthorization } from '../CodexOAuth'
import { membershipMessageFor, messageFor, refreshMessageFor, useResource } from '../hooks'
import { ModelDiscovery } from '../ModelDiscovery'
import { UpstreamBatchImport } from '../UpstreamBatchImport'
import { presetForEndpoint, providerPreset, providerPresets, type ProviderChoice, type ProviderPresetId } from '../providerPresets'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState } from '../ui'
import { PageHeader } from './EmployeesPage'

export function UpstreamsPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.upstreams(), [])
  const { data, setData, loading, error, reload } = useResource(load)
  const loadStatus = useCallback(() => api.status(), [])
  const { data: status, loading: statusLoading, error: statusError, reload: reloadStatus } = useResource(loadStatus)
  const [creating, setCreating] = useState(false)
  const [authorizing, setAuthorizing] = useState(false)
  const [batchImporting, setBatchImporting] = useState(false)
  const [importing, setImporting] = useState(false)
  const [reimporting, setReimporting] = useState<Upstream | null>(null)
  const [syncing, setSyncing] = useState<Upstream | null>(null)
  const membershipEnabled = status?.features?.codex_membership_import === true
  const oauthEnabled = status?.features?.codex_membership_oauth === true
  const autoRefreshEnabled = status?.features?.codex_membership_auto_refresh === true
  const reloadUpstreamsAndFind = useCallback(async (upstreamID?: string) => {
    const next = await api.upstreams()
    setData(next)
    return upstreamID ? next.items.find((item) => item.id === upstreamID) : undefined
  }, [setData])
  return <>
    <PageHeader title="上游连接" description="连接 OpenAI 兼容 API、Anthropic Messages API、Gemini 原生 API 或 Codex 会员。密钥加密保存且不会再次显示。"><div className="page-header-buttons"><Button variant="secondary" onClick={() => setBatchImporting(true)}><Icon name="link" />批量导入</Button><Button onClick={() => setCreating(true)}><Icon name="plus" />添加上游</Button></div></PageHeader>
    <MembershipFeaturePanel importEnabled={membershipEnabled} oauthEnabled={oauthEnabled} autoRefreshEnabled={autoRefreshEnabled} loading={statusLoading} error={statusError} onRetry={() => void reloadStatus()} onImport={() => setImporting(true)} onOAuth={() => setAuthorizing(true)} />
    <div className="content-panel"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有上游连接" body="添加 API Key 上游或授权 Codex 会员，再配置模型路由。" action={<Button onClick={() => setCreating(true)}>添加上游</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table className="upstreams-table"><thead><tr><th>名称</th><th>提供商</th><th>端点</th><th>凭据</th><th>状态</th><th>操作</th></tr></thead><tbody>{data.items.map((item) => <UpstreamRow key={item.id} item={item} csrf={csrf} membershipEnabled={membershipEnabled} autoRefreshEnabled={autoRefreshEnabled} onSync={() => setSyncing(item)} onReimport={() => setReimporting(item)} onDone={() => void reload()} />)}</tbody></table></div> : null}
    </div>
    {creating ? <CreateUpstream csrf={csrf} onClose={() => setCreating(false)} onSaved={() => void reload()} /> : null}
    {batchImporting ? <UpstreamBatchImport csrf={csrf} membershipEnabled={membershipEnabled} onClose={() => setBatchImporting(false)} onImported={reload} /> : null}
    {authorizing ? <CodexOAuthAuthorization csrf={csrf} onClose={() => setAuthorizing(false)} onUpstreamsChanged={reloadUpstreamsAndFind} onSync={(upstream) => { setAuthorizing(false); setSyncing(upstream) }} /> : null}
    {importing ? <CodexAuthImport csrf={csrf} onClose={() => setImporting(false)} onSaved={() => { setImporting(false); void reload() }} /> : null}
    {reimporting ? <CodexAuthImport csrf={csrf} upstream={reimporting} onClose={() => setReimporting(null)} onSaved={() => { setReimporting(null); void reload() }} /> : null}
    {syncing ? <Dialog title={`同步 ${syncing.name} 的模型`} description="仅读取上游返回的模型列表；选择后才会创建路由。" onClose={() => setSyncing(null)} wide>
      <ModelDiscovery upstream={syncing} csrf={csrf} />
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={() => setSyncing(null)}>完成</Button></div>
    </Dialog> : null}
  </>
}

function MembershipFeaturePanel({ importEnabled, oauthEnabled, autoRefreshEnabled, loading, error, onRetry, onImport, onOAuth }: { importEnabled: boolean; oauthEnabled: boolean; autoRefreshEnabled: boolean; loading: boolean; error: string | null; onRetry: () => void; onImport: () => void; onOAuth: () => void }) {
  const available = importEnabled || oauthEnabled
  return <section className={`membership-panel ${available ? 'membership-panel--enabled' : ''}`} aria-labelledby="membership-title">
    <div><h2 id="membership-title">Codex 会员接入（实验）</h2>
      {loading ? <p>正在读取实验开关…</p> : error ? <p>无法确认实验开关状态；为安全起见，授权入口已隐藏。</p> : available
        ? <p>可通过 OpenAI 官方 OAuth 授权或导入 <code>auth.json</code>。保存凭据不代表已验证，模型路由也不会自动创建。</p>
        : <p>此实例未启用 Codex 会员接入。请使用 <code>--experimental-codex-membership</code> 启动服务后重试。</p>}
      {!loading && !error ? <p className="membership-panel__limits">{oauthEnabled ? (autoRefreshEnabled ? 'OAuth 授权与自动刷新已启用。' : 'OAuth 已启用；自动刷新不可用，仍可在连接行中手动刷新。') : 'OAuth 未配置或未启用；可用入口取决于服务端功能开关。'} 不支持 Claude/Gemini 会员导入；它们的 API Key 请从“添加上游”配置。</p> : null}
    </div>
    <div className="membership-panel__actions">
      {oauthEnabled ? <Button type="button" onClick={onOAuth}>通过 OpenAI 授权</Button> : null}
      {importEnabled ? <Button type="button" variant={oauthEnabled ? 'secondary' : 'primary'} onClick={onImport}>导入 Codex auth.json</Button> : null}
      {error ? <Button type="button" variant="secondary" onClick={onRetry}>重试读取开关</Button> : null}
    </div>
  </section>
}

const credentialStateCopy = {
  imported_unverified: { label: '已导入，未验证', tone: 'warning' },
  verified: { label: '已验证', tone: 'active' },
  reauth_required: { label: '需要重新导入', tone: 'danger' },
} as const

function refreshStateCopy(item: Upstream, autoRefreshEnabled: boolean) {
  const refresh = item.oauth_refresh
  if (!refresh) return { label: '刷新状态不可用', detail: '请升级服务或重新载入列表。', canRefresh: false }
  if (refresh.state === 'ready') return { label: autoRefreshEnabled ? '自动刷新就绪' : 'OAuth 可手动刷新', detail: autoRefreshEnabled ? '服务会按需刷新；也可立即手动刷新。' : '自动刷新未启用，可在此手动刷新。', canRefresh: refresh.eligible }
  if (refresh.state === 'refreshing') return { label: '正在刷新', detail: '请稍后重新载入列表核对结果。', canRefresh: false }
  if (refresh.state === 'paused') return { label: '自动刷新已暂停', detail: '请检查 OAuth 服务配置，恢复后重新载入列表。', canRefresh: false }
  if (refresh.state === 'reauth_required') return { label: '需要重新授权', detail: '请从上方 OAuth 入口创建新连接，再停用此记录。', canRefresh: false }
  return { label: 'OAuth 刷新不可用', detail: '此凭据不支持 OAuth 刷新；可重新授权或重新导入。', canRefresh: false }
}

function UpstreamRow({ item, csrf, membershipEnabled, autoRefreshEnabled, onSync, onReimport, onDone }: { item: Upstream; csrf: string; membershipEnabled: boolean; autoRefreshEnabled: boolean; onSync: () => void; onReimport: () => void; onDone: () => void }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [refreshBlocked, setRefreshBlocked] = useState(false)
  const membership = item.provider_kind === 'codex-membership'
  const anthropic = item.provider_kind === 'anthropic-api-key'
  const gemini = item.provider_kind === 'gemini-api-key'
  const credential = item.credential_state ? credentialStateCopy[item.credential_state] : null
  const refresh = membership ? refreshStateCopy(item, autoRefreshEnabled) : null
  return <tr><td><strong>{item.name}</strong></td><td>{membership ? 'Codex 会员' : anthropic ? 'Anthropic API' : gemini ? 'Gemini 原生 API' : 'OpenAI 兼容 API'}</td><td>{membership ? <span className="muted-copy">服务端固定</span> : <code className="endpoint">{item.endpoint}</code>}</td><td>{membership && credential ? <div className="credential-stack"><span className={`status status--${credential.tone}`}><i />{credential.label}</span>{refresh ? <><small>{refresh.label}</small><small>{refresh.detail}</small></> : null}</div> : <span className="muted-copy">API Key</span>}</td><td><span className={`status status--${item.enabled ? 'active' : 'disabled'}`}><i />{item.enabled ? '启用' : '已停用'}</span></td><td><div className="row-actions">{membership ? <><button className="link-button" disabled={busy} onClick={onSync}>同步模型</button>{refresh?.canRefresh ? <button className="link-button" disabled={busy || refreshBlocked} onClick={async () => {
    setBusy(true); setError(null)
    try { await api.refreshCodexMembership(item.id, { expected_revision: item.revision }, csrf); onDone(); setBusy(false) }
    catch (caught) {
      setError(refreshMessageFor(caught))
      if (!(caught instanceof ApiError) || ['codex_refresh_not_bound', 'codex_reauthorization_required', 'revision_conflict', 'credential_unavailable', 'codex_oauth_not_configured'].includes(caught.code)) setRefreshBlocked(true)
      setBusy(false)
    }
  }}>{busy ? '刷新中…' : '手动刷新'}</button> : null}<button className="link-button" disabled={busy || !membershipEnabled} title={membershipEnabled ? undefined : '需先启用 --experimental-codex-membership'} onClick={onReimport}>重新导入</button></> : <button className="link-button" disabled={busy} onClick={onSync}>同步模型</button>}<span className="inline-action"><button className="link-button" disabled={busy} onClick={async () => {
    setBusy(true); setError(null)
    try { await api.updateUpstream(item.id, { expected_revision: item.revision, enabled: !item.enabled }, csrf); onDone(); setBusy(false) }
    catch (caught) { setError(messageFor(caught)); setBusy(false) }
  }}>{busy ? '处理中…' : item.enabled ? '停用' : '启用'}</button>{error ? <span className="action-error">{error}</span> : null}</span></div></td></tr>
}

const MAX_CODEX_AUTH_BYTES = 1024 * 1024

function readFileText(file: File) {
  if (typeof file.text === 'function') return file.text()
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result ?? '')))
    reader.addEventListener('error', () => reject(new Error('file_read_failed')))
    reader.readAsText(file)
  })
}

export function CodexAuthImport({ csrf, upstream, onClose, onSaved }: { csrf: string; upstream?: Upstream; onClose: () => void; onSaved: () => void }) {
  const [name, setName] = useState('Codex 会员')
  const [file, setFile] = useState<File | null>(null)
  const [operationID] = useState(() => crypto.randomUUID())
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const replacing = upstream?.provider_kind === 'codex-membership'

  function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const next = event.currentTarget.files?.[0] ?? null
    setError(null)
    if (next && next.size > MAX_CODEX_AUTH_BYTES) {
      setFile(null)
      event.currentTarget.value = ''
      setError('授权文件不能超过 1 MiB。')
      return
    }
    setFile(next)
  }

  return <Dialog title={replacing ? `重新导入 ${upstream.name}` : '导入 Codex 授权文件'} description={replacing ? '服务端确认替换成功后记录才会更新；结果不明时请刷新列表核对。' : '这是文件导入实验，不是网页授权登录。'} onClose={onClose}>
    <div className="membership-limitations"><strong>导入前请确认</strong><ul>
      <li>只接受你主动选择的 Codex <code>auth.json</code>，最大 1 MiB。</li>
      <li>导入只验证文件结构；不会显示文件正文、Token 或账号内容，也不代表授权成功。</li>
      <li>文件导入仍可用于兼容或恢复；如需 OAuth 刷新，请从页面上方另行创建 OAuth 连接。</li>
      <li>不支持 Claude/Gemini 会员导入。导入后可同步 Codex 模型，但不会自动创建路由。</li>
    </ul></div>
    <form onSubmit={async (event) => {
      event.preventDefault()
      if (!file) { setError('请选择 Codex auth.json。'); return }
      setBusy(true); setError(null)
      try {
        const authJSON = await readFileText(file)
        if (replacing) await api.replaceCodexMembershipAuth(upstream.id, { expected_revision: upstream.revision, auth_json: authJSON }, csrf)
        else await api.importCodexMembership({ name: name.trim(), auth_json: authJSON, operation_id: operationID }, csrf)
        onSaved()
      } catch (caught) {
        setError(membershipMessageFor(caught))
        setBusy(false)
      }
    }}>
      <div className="form-grid">
        {!replacing ? <Field label="显示名称" hint="仅用于后台识别此会员上游。"><input name="name" value={name} onChange={(event) => setName(event.target.value)} required autoFocus maxLength={120} /></Field> : null}
        <Field label="Codex auth.json" hint="文件内容仅发送至当前 CPA Cloud 服务，由服务端加密保存；不会在页面中显示。"><input name="auth_json_file" type="file" accept=".json,application/json" autoFocus={replacing} onChange={chooseFile} /></Field>
      </div>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在导入…' : replacing ? '重新导入文件' : '导入文件'}</Button></div>
    </form>
  </Dialog>
}

export function CreateUpstream({ csrf, onClose, onSaved }: { csrf: string; onClose: () => void; onSaved: () => void }) {
  const initial = providerPreset('deepseek')
  const [provider, setProvider] = useState<ProviderChoice>(initial.id)
  const [name, setName] = useState(initial.name)
  const [nameIsAutomatic, setNameIsAutomatic] = useState(true)
  const [endpoint, setEndpoint] = useState(initial.endpoint)
  const [customProviderKind, setCustomProviderKind] = useState<'openai-compatible' | 'anthropic-api-key' | 'gemini-api-key'>(initial.providerKind)
  const [apiKey, setApiKey] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saved, setSaved] = useState<Upstream | null>(null)

  function selectProvider(choice: ProviderChoice) {
    if (choice !== provider) setApiKey('')
    setProvider(choice)
    if (choice === 'custom') {
      if (customProviderKind === 'gemini-api-key') setCustomProviderKind('openai-compatible')
      return
    }
    const preset = providerPreset(choice as ProviderPresetId)
    setCustomProviderKind(preset.providerKind)
    setName(preset.name)
    setNameIsAutomatic(true)
    setEndpoint(preset.endpoint)
  }

  const selectedPreset = provider === 'custom' ? null : providerPreset(provider as ProviderPresetId)
  const endpointHint = selectedPreset?.endpointLocked
    ? 'Gemini 原生协议固定使用 Google 官方 HTTPS 端点。若要使用 OpenAI 协议，请选择“Google Gemini（OpenAI 兼容）”。'
    : '仅精确匹配已核实的 HTTPS origin 与路径；自定义地址是否可用由服务端验证。'
  const keyHint = selectedPreset?.providerKind === 'anthropic-api-key'
    ? '切换服务商或修改地址会立即清空此字段。这里只接受 Anthropic Console API Key，不是 Claude 会员凭据。'
    : selectedPreset?.providerKind === 'gemini-api-key'
      ? '切换服务商会立即清空此字段。请输入 Google AI Studio 或 Google Cloud 提供的 Gemini API Key。'
      : '切换服务商或修改地址会立即清空此字段，避免凭据误发。'

  function changeEndpoint(next: string) {
    if (next !== endpoint) setApiKey('')
    setEndpoint(next)
    const matched = presetForEndpoint(next)
    setProvider(matched?.id ?? 'custom')
    if (matched) {
      setCustomProviderKind(matched.providerKind)
      if (nameIsAutomatic) setName(matched.name)
    } else if (provider !== 'custom') {
      setCustomProviderKind(providerPreset(provider as ProviderPresetId).providerKind)
    }
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
        const providerKind = provider === 'custom' ? customProviderKind : providerPreset(provider as ProviderPresetId).providerKind
        const created = await api.createUpstream({ name: name.trim(), provider_kind: providerKind, endpoint: endpoint.trim(), api_key: apiKey }, csrf)
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
          <option value="custom">自定义地址（保持当前协议）</option>
        </select></Field>
        <Field label="显示名称"><input name="name" value={name} onChange={(event) => { setName(event.target.value); setNameIsAutomatic(false) }} required /></Field>
        <Field label="API 端点" hint={endpointHint}><input name="endpoint" type="url" value={endpoint} onChange={(event) => changeEndpoint(event.target.value)} readOnly={selectedPreset?.endpointLocked} required /></Field>
        {provider === 'custom' ? <div className="field-note" role="status">当前地址按自定义服务处理。修改服务商或地址后，API Key 必须重新填写。</div> : null}
        <Field label="API Key" hint={keyHint}><input name="api_key" type="password" autoComplete="new-password" value={apiKey} onChange={(event) => setApiKey(event.target.value)} required /></Field>
      </div>
      <FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在保存…' : '保存并同步模型'}</Button></div>
    </form>
  </Dialog>
}
