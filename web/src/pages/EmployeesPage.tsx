import { useCallback, useEffect, useMemo, useState } from 'react'
import { ApiError, api, type AccountGroup, type ClientProtocol, type Employee, type EmployeeKey, type KeyAccessPolicy, type KeyPolicyInput, type ModelRoute } from '../api'
import { messageFor, useResource } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'

export function EmployeesPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.employees(), [])
  const { data, loading, error, reload } = useResource(load)
  const [creating, setCreating] = useState(false)
  const [selected, setSelected] = useState<Employee | null>(null)
  const [policy, setPolicy] = useState<Employee | null>(null)
  const [keyPolicyCapability, setKeyPolicyCapability] = useState<boolean | null>(null)
  const [keySourcePolicyCapability, setKeySourcePolicyCapability] = useState<boolean | null>(null)
  const [keyAccountGroupPolicyCapability, setKeyAccountGroupPolicyCapability] = useState<boolean | null>(null)
  const [trustedProxySource, setTrustedProxySource] = useState(false)
  const [openAIEmbeddings, setOpenAIEmbeddings] = useState(false)

  useEffect(() => {
    let active = true
    void api.status().then((status) => {
      if (active) {
        setKeyPolicyCapability(status.features?.key_access_policy === true)
        setKeySourcePolicyCapability(status.features?.key_source_policy === true)
        setKeyAccountGroupPolicyCapability(status.features?.key_account_group_policy === true)
        setTrustedProxySource(status.features?.trusted_proxy_source === true)
        setOpenAIEmbeddings(status.features?.openai_embeddings === true)
      }
    }).catch(() => {
      if (active) {
        setKeyPolicyCapability(false)
        setKeySourcePolicyCapability(false)
        setKeyAccountGroupPolicyCapability(false)
        setTrustedProxySource(false)
        setOpenAIEmbeddings(false)
      }
    })
    return () => { active = false }
  }, [])

  return <>
    <PageHeader title="员工与 Key" description="管理员工的 AI 访问权限，为员工创建和管理 API Key，控制其可使用的模型范围。"><Button onClick={() => setCreating(true)}><Icon name="plus" />创建员工</Button></PageHeader>
    <div className="content-panel">
      <PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有员工" body="创建员工后，即可分配模型权限并生成永久 Key。" action={<Button onClick={() => setCreating(true)}>创建第一位员工</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table>
        <thead><tr><th>员工</th><th>部门</th><th>状态</th><th>模型权限</th><th>Key</th><th>操作</th></tr></thead>
        <tbody>{data.items.map((employee) => <tr key={employee.id}>
          <td><strong>{employee.name}</strong>{employee.note ? <small>{employee.note}</small> : null}</td>
          <td>{employee.department || '—'}</td>
          <td><span className={`status status--${employee.status}`}><i />{employee.status === 'active' ? '启用' : '已停用'}</span></td>
          <td><button className="link-button" onClick={() => setPolicy(employee)}>{employee.model_mode === 'all' ? '全部可用模型' : `${employee.models.length} 个指定模型`}</button></td>
          <td><button className="link-button" onClick={() => setSelected(employee)}><Icon name="key" />管理 Key</button></td>
          <td><div className="row-actions"><ToggleEmployee employee={employee} csrf={csrf} onDone={() => void reload()} /></div></td>
        </tr>)}</tbody>
      </table></div> : null}
    </div>
    {creating ? <CreateEmployee csrf={csrf} onClose={() => setCreating(false)} onCreated={() => { setCreating(false); void reload() }} /> : null}
    {selected ? <KeysDialog employee={selected} csrf={csrf} keyPolicyCapability={keyPolicyCapability} sourcePolicyCapability={keySourcePolicyCapability} accountGroupPolicyCapability={keyAccountGroupPolicyCapability} trustedProxySource={trustedProxySource} openAIEmbeddings={openAIEmbeddings} onClose={() => setSelected(null)} /> : null}
    {policy ? <PolicyDialog employee={policy} csrf={csrf} onClose={() => setPolicy(null)} onSaved={() => { setPolicy(null); void reload() }} /> : null}
  </>
}

export function PageHeader({ title, description, children }: { title: string; description: string; children?: React.ReactNode }) {
  return <header className="page-header"><div><h1>{title}</h1><p>{description}</p></div>{children ? <div className="page-header__action">{children}</div> : null}</header>
}

function CreateEmployee({ csrf, onClose, onCreated }: { csrf: string; onClose: () => void; onCreated: () => void }) {
  const [error, setError] = useState<string | null>(null); const [busy, setBusy] = useState(false)
  return <Dialog title="创建员工" description="员工创建后默认启用，并可访问全部已配置模型。" onClose={onClose}>
    <form onSubmit={submitHandler(async (form) => {
      setBusy(true); setError(null)
      try { await api.createEmployee({ name: String(form.get('name')), department: String(form.get('department')) || undefined, note: String(form.get('note')) || undefined }, csrf); onCreated() }
      catch (caught) { setError(messageFor(caught)); setBusy(false) }
    })}>
      <div className="form-grid"><Field label="姓名"><input name="name" required autoFocus /></Field><Field label="部门（可选）"><input name="department" /></Field><Field label="备注（可选）"><textarea name="note" rows={3} /></Field></div>
      <FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在创建…' : '创建员工'}</Button></div>
    </form>
  </Dialog>
}

function ToggleEmployee({ employee, csrf, onDone }: { employee: Employee; csrf: string; onDone: () => void }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState<string | null>(null)
  return <span className="inline-action"><button className="link-button" disabled={busy} onClick={async () => {
    setBusy(true); setError(null)
    try { await api.updateEmployee(employee.id, { expected_revision: employee.revision, status: employee.status === 'active' ? 'disabled' : 'active' }, csrf); onDone() }
    catch (caught) { setError(messageFor(caught)); setBusy(false) }
  }}>{busy ? '处理中…' : employee.status === 'active' ? '停用' : '启用'}</button>{error ? <span className="action-error" role="alert">{error}</span> : null}</span>
}

const protocolChoices: Array<{ id: ClientProtocol; label: string }> = [
  { id: 'openai-chat', label: 'OpenAI Chat Completions' },
  { id: 'openai-responses', label: 'OpenAI Responses' },
  { id: 'openai-embeddings', label: 'OpenAI Embeddings（文本 / float）' },
  { id: 'anthropic-messages', label: 'Anthropic Messages' },
  { id: 'gemini-generate-content', label: 'Gemini generateContent' },
]

const defaultKeyPolicy: KeyPolicyInput = { protocol_mode: 'all', protocols: [], model_mode: 'all', models: [], source_mode: 'all', source_cidrs: [], account_group_mode: 'all', account_group_ids: [] }

type SourcePolicyFields = { source_mode: 'all' | 'selected'; source_cidrs: string[] }

const invalidSourcePolicyMessage = '服务返回的 Key 来源策略无效；已禁止保存，以避免覆盖现有来源限制。'
const invalidAccountGroupPolicyMessage = '服务返回的 Key 账号池分组策略无效；已禁止保存，以避免扩大账号范围。'

function sourcePolicyFields(value: KeyAccessPolicy | KeyPolicyInput): SourcePolicyFields | null {
  const candidate = value as { source_mode?: unknown; source_cidrs?: unknown }
  if ((candidate.source_mode !== 'all' && candidate.source_mode !== 'selected') || !Array.isArray(candidate.source_cidrs) || !candidate.source_cidrs.every((item) => typeof item === 'string')) return null
  if (candidate.source_mode === 'all' && candidate.source_cidrs.length !== 0) return null
  return { source_mode: candidate.source_mode, source_cidrs: candidate.source_cidrs }
}

type AccountGroupPolicyFields = { account_group_mode: 'all' | 'selected'; account_group_ids: string[] }

function accountGroupPolicyFields(value: KeyAccessPolicy | KeyPolicyInput): AccountGroupPolicyFields | null {
  const candidate = value as { account_group_mode?: unknown; account_group_ids?: unknown }
  if ((candidate.account_group_mode !== 'all' && candidate.account_group_mode !== 'selected') || !Array.isArray(candidate.account_group_ids) || !candidate.account_group_ids.every((item) => typeof item === 'string')) return null
  if (candidate.account_group_mode === 'all' && candidate.account_group_ids.length !== 0) return null
  return { account_group_mode: candidate.account_group_mode, account_group_ids: candidate.account_group_ids }
}

function normalizedPolicy(draft: KeyPolicyInput, includeSource: boolean, includeAccountGroups: boolean): KeyPolicyInput {
  const normalized: KeyPolicyInput = {
    protocol_mode: draft.protocol_mode,
    protocols: draft.protocol_mode === 'all' ? [] : [...draft.protocols],
    model_mode: draft.model_mode,
    models: draft.model_mode === 'all' ? [] : [...draft.models],
  }
  if (includeSource) {
    const source = sourcePolicyFields(draft)
    if (!source) throw new Error(invalidSourcePolicyMessage)
    normalized.source_mode = source.source_mode
    normalized.source_cidrs = source.source_mode === 'all' ? [] : [...source.source_cidrs]
  }
  if (includeAccountGroups) {
    const groups = accountGroupPolicyFields(draft)
    if (!groups) throw new Error(invalidAccountGroupPolicyMessage)
    normalized.account_group_mode = groups.account_group_mode
    normalized.account_group_ids = groups.account_group_mode === 'all' ? [] : [...groups.account_group_ids]
  }
  return normalized
}

function KeysDialog({ employee, csrf, keyPolicyCapability, sourcePolicyCapability, accountGroupPolicyCapability, trustedProxySource, openAIEmbeddings, onClose }: { employee: Employee; csrf: string; keyPolicyCapability: boolean | null; sourcePolicyCapability: boolean | null; accountGroupPolicyCapability: boolean | null; trustedProxySource: boolean; openAIEmbeddings: boolean; onClose: () => void }) {
  const load = useCallback(() => api.keys(employee.id), [employee.id])
  const { data, loading, error, reload } = useResource(load)
  const loadModels = useCallback(() => keyPolicyCapability === true ? api.models() : Promise.resolve({ items: [] as ModelRoute[] }), [keyPolicyCapability])
  const { data: modelData, loading: modelsLoading, error: modelsError, reload: reloadModels } = useResource(loadModels)
  const loadAccountGroups = useCallback(() => accountGroupPolicyCapability === true ? api.accountGroups() : Promise.resolve({ items: [] as AccountGroup[] }), [accountGroupPolicyCapability])
  const { data: accountGroupData, loading: accountGroupsLoading, error: accountGroupsError, reload: reloadAccountGroups } = useResource(loadAccountGroups)
  const [name, setName] = useState('默认 Key')
  const [created, setCreated] = useState<EmployeeKey | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [draft, setDraft] = useState<KeyPolicyInput>(defaultKeyPolicy)
  const [editing, setEditing] = useState<EmployeeKey | null>(null)
  const modelIds = useMemo(() => {
    const employeeModels = new Set(employee.models)
    return [...new Set((modelData?.items ?? [])
      .filter((model) => !model.archived && (employee.model_mode === 'all' || employeeModels.has(model.id)))
      .map((model) => model.id))].sort()
  }, [employee.model_mode, employee.models, modelData])
  const accountGroups = accountGroupData?.items ?? []
  if (created?.key) return <KeyReveal created={created} onClose={() => { setCreated(null); onClose() }} />
  if (editing) return <EditKeyPolicyDialog employee={employee} keyItem={editing} csrf={csrf} sourcePolicyCapability={sourcePolicyCapability === true} accountGroupPolicyCapability={accountGroupPolicyCapability === true} trustedProxySource={trustedProxySource} openAIEmbeddings={openAIEmbeddings} modelIds={modelIds} modelsLoading={modelsLoading} modelsError={modelsError} onRetryModels={() => void reloadModels()} accountGroups={accountGroups} accountGroupsLoading={accountGroupsLoading} accountGroupsError={accountGroupsError} onRetryAccountGroups={() => void reloadAccountGroups()} onClose={() => setEditing(null)} onSaved={() => { setEditing(null); void reload() }} />
  return <Dialog title={`${employee.name} 的 Key`} description="Key 默认永久有效；可为不同设备或用途分别创建。" onClose={onClose} wide>
    {keyPolicyCapability === null ? <div className="key-policy-compat">正在确认服务是否支持独立 Key 权限…</div> : null}
    {keyPolicyCapability === false ? <div className="key-policy-compat"><strong>当前服务不支持独立 Key 策略</strong><span>新 Key 将沿用员工权限；此页面不会提供无效的策略保存操作。</span></div> : null}
    {keyPolicyCapability === true && accountGroupPolicyCapability === false ? <div className="key-policy-compat"><strong>当前服务不支持 Key 账号池分组限制</strong><span>仍可配置协议、模型和来源；保存时不会发送账号池分组字段。</span></div> : null}
    {keyPolicyCapability === true ? <KeyPolicyEditor draft={draft} onChange={setDraft} sourcePolicyCapability={sourcePolicyCapability === true} accountGroupPolicyCapability={accountGroupPolicyCapability === true} trustedProxySource={trustedProxySource} openAIEmbeddings={openAIEmbeddings} modelIds={modelIds} loading={modelsLoading} error={modelsError} onRetry={() => void reloadModels()} accountGroups={accountGroups} accountGroupsLoading={accountGroupsLoading} accountGroupsError={accountGroupsError} onRetryAccountGroups={() => void reloadAccountGroups()} context="创建 Key 的独立权限" /> : null}
    <div className="key-create"><Field label="Key 名称"><input value={name} onChange={(e) => setName(e.target.value)} /></Field><Button disabled={busy || !name.trim() || keyPolicyCapability === null || (keyPolicyCapability === true && (sourcePolicyCapability === null || accountGroupPolicyCapability === null))} onClick={async () => {
      setBusy(true); setActionError(null)
      try { setCreated(await api.createKey(employee.id, name.trim(), csrf, keyPolicyCapability === true ? normalizedPolicy(draft, sourcePolicyCapability === true, accountGroupPolicyCapability === true) : undefined)); await reload() }
      catch (caught) { setActionError(messageFor(caught)); setBusy(false) }
    }}><Icon name="plus" />{busy ? '正在生成…' : '生成永久 Key'}</Button></div>
    <FormError error={actionError} /><PageState loading={loading} error={error} onRetry={() => void reload()} />
    {data?.items.length === 0 ? <EmptyState title="还没有 Key" body="生成后明文只展示一次，请立即妥善保存。" /> : null}
    {data?.items.length ? <div className="key-list">{data.items.map((key) => <div key={key.id} className="key-row key-row--policy"><div className="key-row__identity"><strong>{key.name}</strong><span>{key.revoked_at ? '已撤销' : key.expires_at ? `到期：${new Date(key.expires_at).toLocaleDateString('zh-CN')}` : '永久有效'}</span></div>
      {keyPolicyCapability === true && key.policy ? <KeyPolicySummary policy={key.policy} sourcePolicyCapability={sourcePolicyCapability === true} accountGroupPolicyCapability={accountGroupPolicyCapability === true} /> : <div className="key-policy-unavailable">{keyPolicyCapability === true ? '策略信息不可用；重新载入后再编辑。' : '沿用员工权限'}</div>}
      <div className="key-row__actions">{keyPolicyCapability === true && key.policy && !key.revoked_at && (!accountGroupPolicyCapability || accountGroupPolicyFields(key.policy)) ? <Button variant="secondary" onClick={() => setEditing(key)}>编辑独立权限</Button> : null}{key.revoked_at ? null : <Button variant="danger" onClick={async () => { try { await api.revokeKey(key.id, csrf); await reload() } catch (caught) { setActionError(messageFor(caught)) } }}>撤销</Button>}</div>
    </div>)}</div> : null}
  </Dialog>
}

function KeyPolicyEditor({ draft, onChange, sourcePolicyCapability, accountGroupPolicyCapability, trustedProxySource, openAIEmbeddings, modelIds, loading, error, onRetry, accountGroups, accountGroupsLoading, accountGroupsError, onRetryAccountGroups, context }: { draft: KeyPolicyInput; onChange: (next: KeyPolicyInput) => void; sourcePolicyCapability: boolean; accountGroupPolicyCapability: boolean; trustedProxySource: boolean; openAIEmbeddings: boolean; modelIds: string[]; loading: boolean; error: string | null; onRetry: () => void; accountGroups: AccountGroup[]; accountGroupsLoading: boolean; accountGroupsError: string | null; onRetryAccountGroups: () => void; context: string }) {
  const selectedProtocols = new Set(draft.protocols)
  const selectedModels = new Set(draft.models)
  const source = sourcePolicyFields(draft)
  const groupPolicy = accountGroupPolicyFields(draft)
  const selectedAccountGroups = new Set(groupPolicy?.account_group_ids ?? [])
  const sourceInputId = context.startsWith('创建') ? 'create-key-source-cidrs' : 'edit-key-source-cidrs'
  return <section className="key-policy-editor" aria-label={context}>
    <h3>{context}</h3>
    <div className="key-policy-grid">
      <fieldset><legend>入口协议</legend>
        <label><input type="radio" name={`${context}-protocol-mode`} checked={draft.protocol_mode === 'all'} onChange={() => onChange({ ...draft, protocol_mode: 'all' })} />{openAIEmbeddings ? '全部协议（含 Embeddings）' : '全部协议'}</label>
        <label><input type="radio" name={`${context}-protocol-mode`} checked={draft.protocol_mode === 'selected'} onChange={() => onChange({ ...draft, protocol_mode: 'selected' })} />仅指定协议</label>
        {draft.protocol_mode === 'selected' ? <div className="key-policy-options">{protocolChoices.filter((protocol) => openAIEmbeddings || protocol.id !== 'openai-embeddings').map((protocol) => <label key={protocol.id}><input type="checkbox" checked={selectedProtocols.has(protocol.id)} onChange={(event) => onChange({ ...draft, protocols: event.target.checked ? [...draft.protocols, protocol.id] : draft.protocols.filter((id) => id !== protocol.id) })} />{protocol.label}</label>)}</div> : null}
      </fieldset>
      <fieldset><legend>公开模型</legend>
        <label><input type="radio" name={`${context}-model-mode`} checked={draft.model_mode === 'all'} onChange={() => onChange({ ...draft, model_mode: 'all' })} />员工可用的全部模型</label>
        <label><input type="radio" name={`${context}-model-mode`} checked={draft.model_mode === 'selected'} onChange={() => onChange({ ...draft, model_mode: 'selected' })} />仅指定模型</label>
        {draft.model_mode === 'selected' ? <><PageState loading={loading} error={error} onRetry={onRetry} />{!loading && !error ? <div className="key-policy-options">{modelIds.length ? modelIds.map((model) => <label key={model}><input type="checkbox" checked={selectedModels.has(model)} onChange={(event) => onChange({ ...draft, models: event.target.checked ? [...draft.models, model] : draft.models.filter((id) => id !== model) })} />{model}</label>) : <p>员工当前没有可供此 Key 选择的模型。</p>}</div> : null}</> : null}
      </fieldset>
      {sourcePolicyCapability && source ? <fieldset><legend>来源地址</legend>
        <label><input type="radio" name={`${context}-source-mode`} checked={source.source_mode === 'all'} onChange={() => onChange({ ...draft, source_mode: 'all', source_cidrs: [] })} />任意 socket peer</label>
        <label><input type="radio" name={`${context}-source-mode`} checked={source.source_mode === 'selected'} onChange={() => onChange({ ...draft, source_mode: 'selected' })} />仅指定 IP / CIDR</label>
        {source.source_mode === 'selected' ? <div className="key-policy-source-input"><label htmlFor={sourceInputId}>允许的 IP / CIDR，每行一项</label><textarea id={sourceInputId} aria-label="允许的 IP / CIDR" rows={5} value={source.source_cidrs.join('\n')} onChange={(event) => onChange({ ...draft, source_cidrs: event.target.value === '' ? [] : event.target.value.split(/\r?\n/) })} placeholder={'192.0.2.10\n10.20.0.0/16\n2001:db8::/48'} /><small>{source.source_cidrs.length} / 64 项</small></div> : null}
      </fieldset> : null}
      {accountGroupPolicyCapability && groupPolicy ? <fieldset><legend>账号池分组</legend>
        <label><input type="radio" name={`${context}-account-group-mode`} checked={groupPolicy.account_group_mode === 'all'} onChange={() => onChange({ ...draft, account_group_mode: 'all', account_group_ids: [] })} />全部账号池分组</label>
        <label><input type="radio" name={`${context}-account-group-mode`} checked={groupPolicy.account_group_mode === 'selected'} onChange={() => onChange({ ...draft, account_group_mode: 'selected' })} />仅指定账号池分组</label>
        {groupPolicy.account_group_mode === 'selected' ? <><PageState loading={accountGroupsLoading} error={accountGroupsError} onRetry={onRetryAccountGroups} />{!accountGroupsLoading && !accountGroupsError ? <div className="key-policy-options">{accountGroups.length ? accountGroups.map((group) => <label key={group.id}><input type="checkbox" checked={selectedAccountGroups.has(group.id)} onChange={(event) => onChange({ ...draft, account_group_ids: event.target.checked ? [...groupPolicy.account_group_ids, group.id] : groupPolicy.account_group_ids.filter((id) => id !== group.id) })} />{group.name}</label>) : <p>尚未配置账号池分组；“仅指定”会拒绝全部账号候选。</p>}</div> : null}</> : null}
      </fieldset> : null}
    </div>
    {sourcePolicyCapability ? <div className="key-policy-peer-note">{trustedProxySource ? <><strong>仅显式可信代理可转交来源</strong><span>只有真实 socket peer 命中管理员显式配置的可信代理 CIDR 时，服务才使用一条 X-Forwarded-For 链，并将从右向左的第一个不可信 hop 作为来源。Forwarded 和 X-Real-IP 仍会忽略；该传输能力不会授予 Key 访问权限。</span></> : <><strong>按真实 socket peer 判断</strong><span>服务不会读取 Forwarded、X-Forwarded-For 或 X-Real-IP。使用反向代理时，此处通常匹配代理地址，而不是最终用户地址。</span></>}</div> : <div className="key-policy-compat"><strong>当前服务不支持 Key 来源限制</strong><span>仍可配置协议和模型；保存时不会发送来源字段。</span></div>}
    {(draft.protocol_mode === 'selected' && draft.protocols.length === 0) || (draft.model_mode === 'selected' && draft.models.length === 0) || (sourcePolicyCapability && source?.source_mode === 'selected' && source.source_cidrs.length === 0) || (accountGroupPolicyCapability && groupPolicy?.account_group_mode === 'selected' && groupPolicy.account_group_ids.length === 0) ? <div className="key-policy-deny" role="status">空的“仅指定”列表是显式 deny-all：该 Key 将不能使用对应入口、模型、来源或账号池分组。</div> : null}
  </section>
}

function KeyPolicySummary({ policy, sourcePolicyCapability, accountGroupPolicyCapability }: { policy: KeyAccessPolicy; sourcePolicyCapability: boolean; accountGroupPolicyCapability: boolean }) {
  const configuredProtocols = policy.protocol_mode === 'all' ? '全部协议' : policy.protocols.length ? `${policy.protocols.length} 个协议` : '禁止全部协议'
  const configuredModels = policy.model_mode === 'all' ? '员工可用的全部模型' : policy.models.length ? `${policy.models.length} 个模型` : '禁止全部模型'
  const source = sourcePolicyFields(policy)
  const configuredSources = !sourcePolicyCapability ? '来源限制不可用' : !source ? '来源策略响应无效' : source.source_mode === 'all' ? '任意 socket peer' : source.source_cidrs.length ? `${source.source_cidrs.length} 个来源网段` : '禁止全部来源'
  const groups = accountGroupPolicyFields(policy)
  const configuredGroups = !accountGroupPolicyCapability ? '账号组限制不可用' : !groups ? '账号组策略响应无效' : groups.account_group_mode === 'all' ? '全部账号池分组' : groups.account_group_ids.length ? `${groups.account_group_ids.length} 个账号池分组` : '禁止全部账号池分组'
  return <div className="key-policy-summary"><span>配置：{configuredProtocols} · {configuredModels} · {configuredSources} · {configuredGroups}</span><small>当前生效：{policy.effective_protocols.length} 个协议 · {policy.effective_models.length} 个模型 · 修订 {policy.revision}</small></div>
}

function EditKeyPolicyDialog({ employee, keyItem, csrf, sourcePolicyCapability, accountGroupPolicyCapability, trustedProxySource, openAIEmbeddings, modelIds, modelsLoading, modelsError, onRetryModels, accountGroups, accountGroupsLoading, accountGroupsError, onRetryAccountGroups, onClose, onSaved }: { employee: Employee; keyItem: EmployeeKey; csrf: string; sourcePolicyCapability: boolean; accountGroupPolicyCapability: boolean; trustedProxySource: boolean; openAIEmbeddings: boolean; modelIds: string[]; modelsLoading: boolean; modelsError: string | null; onRetryModels: () => void; accountGroups: AccountGroup[]; accountGroupsLoading: boolean; accountGroupsError: string | null; onRetryAccountGroups: () => void; onClose: () => void; onSaved: () => void }) {
  const [loaded, setLoaded] = useState<KeyAccessPolicy | null>(null)
  const [draft, setDraft] = useState<KeyPolicyInput | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [conflict, setConflict] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const load = useCallback(async () => {
    setLoading(true); setError(null)
    try {
      const policy = await api.keyPolicy(keyItem.id)
      setLoaded(policy)
      if (sourcePolicyCapability && !sourcePolicyFields(policy)) {
        setError(invalidSourcePolicyMessage)
        return
      }
      if (accountGroupPolicyCapability && !accountGroupPolicyFields(policy)) {
        setError(invalidAccountGroupPolicyMessage)
        return
      }
      setDraft((current) => current ?? normalizedPolicy(policy, sourcePolicyCapability, accountGroupPolicyCapability))
    } catch (caught) { setError(messageFor(caught)) }
    finally { setLoading(false) }
  }, [keyItem.id, sourcePolicyCapability, accountGroupPolicyCapability])
  useEffect(() => { void load() }, [load])
  return <Dialog title={`${keyItem.name} 的独立权限`} description={`权限只会进一步限制 ${employee.name} 的员工权限，不能扩大权限。`} onClose={onClose} wide>
    <PageState loading={loading} error={error} onRetry={() => void load()} />
    {draft ? <KeyPolicyEditor draft={draft} onChange={(next) => { setDraft(next); setConflict(null) }} sourcePolicyCapability={sourcePolicyCapability} accountGroupPolicyCapability={accountGroupPolicyCapability} trustedProxySource={trustedProxySource} openAIEmbeddings={openAIEmbeddings} modelIds={modelIds} loading={modelsLoading} error={modelsError} onRetry={onRetryModels} accountGroups={accountGroups} accountGroupsLoading={accountGroupsLoading} accountGroupsError={accountGroupsError} onRetryAccountGroups={onRetryAccountGroups} context="编辑 Key 独立权限" /> : null}
    {conflict ? <div className="key-policy-conflict" role="alert">{conflict}</div> : null}
    {loaded ? <KeyPolicySummary policy={loaded} sourcePolicyCapability={sourcePolicyCapability} accountGroupPolicyCapability={accountGroupPolicyCapability} /> : null}
    <div className="dialog__actions"><Button variant="secondary" onClick={onClose}>取消</Button><Button disabled={!draft || !loaded || saving || loading || Boolean(error)} onClick={async () => {
      if (!draft || !loaded) return
      setSaving(true); setError(null); setConflict(null)
      try {
        await api.putKeyPolicy(keyItem.id, { expected_revision: loaded.revision, ...normalizedPolicy(draft, sourcePolicyCapability, accountGroupPolicyCapability) }, csrf)
        onSaved()
      } catch (caught) {
        if (caught instanceof ApiError && caught.status === 409) {
          try {
            const latest = await api.keyPolicy(keyItem.id)
            setLoaded(latest)
            if (sourcePolicyCapability && !sourcePolicyFields(latest)) {
              setError(invalidSourcePolicyMessage)
              setConflict(null)
              setSaving(false)
              return
            }
            if (accountGroupPolicyCapability && !accountGroupPolicyFields(latest)) {
              setError(invalidAccountGroupPolicyMessage)
              setConflict(null)
              setSaving(false)
              return
            }
            setConflict(`策略已被其他操作更新到修订 ${latest.revision}。你的未保存选择仍保留；检查后可用最新修订重试。`)
          } catch (reloadError) { setError(messageFor(reloadError)) }
        } else { setError(messageFor(caught)) }
        setSaving(false)
      }
    }}>{saving ? '正在保存…' : conflict ? '使用最新修订重试' : '保存独立权限'}</Button></div>
  </Dialog>
}

function KeyReveal({ created, onClose }: { created: EmployeeKey; onClose: () => void }) {
  const [copied, setCopied] = useState(false)
  return <Dialog title="Key 已创建" description="这是该员工的 API Key，请立即复制并妥善保存。" onClose={onClose} wide>
    <div className="key-warning">出于安全考虑，Key 只会显示一次，关闭后将无法再次查看。</div>
    <div className="field"><span>API Key</span><div className="copy-field"><code data-testid="created-key">{created.key}</code><Button onClick={async () => { await navigator.clipboard.writeText(created.key!); setCopied(true) }}><Icon name="copy" />{copied ? '已复制' : '复制 Key'}</Button></div></div>
    <div className="key-tip">请将 Key 保存在安全的地方，例如密码管理工具中。</div>
    <div className="dialog__actions"><Button variant="secondary" onClick={onClose}>我已保存，关闭</Button></div>
  </Dialog>
}

function PolicyDialog({ employee, csrf, onClose, onSaved }: { employee: Employee; csrf: string; onClose: () => void; onSaved: () => void }) {
  const load = useCallback(() => api.models(), [])
  const { data, loading, error, reload } = useResource(load)
  const [mode, setMode] = useState(employee.model_mode)
  const [models, setModels] = useState(() => new Set(employee.models))
  const [saving, setSaving] = useState(false); const [saveError, setSaveError] = useState<string | null>(null)
  const items: ModelRoute[] = data?.items ?? []
  return <Dialog title={`${employee.name} 的模型权限`} description="选择该员工通过 Key 可以看到和调用的模型。" onClose={onClose}>
    <div className="choice-list"><label><input type="radio" checked={mode === 'all'} onChange={() => setMode('all')} />全部可用模型</label><label><input type="radio" checked={mode === 'selected'} onChange={() => setMode('selected')} />仅指定模型</label></div>
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {mode === 'selected' && !loading && !error ? <div className="model-picker">{items.length === 0 ? <p>尚未配置模型路由，请先前往“模型路由”。</p> : items.map((model) => <label key={model.id}><input type="checkbox" checked={models.has(model.id)} onChange={(event) => setModels((current) => { const next = new Set(current); event.target.checked ? next.add(model.id) : next.delete(model.id); return next })} />{model.id}</label>)}</div> : null}
    {mode === 'selected' && models.size === 0 ? <div className="key-policy-deny" role="status">空的指定模型列表会显式禁止该员工访问全部模型。</div> : null}
    <FormError error={saveError} /><div className="dialog__actions"><Button variant="secondary" onClick={onClose}>取消</Button><Button disabled={saving} onClick={async () => {
      setSaving(true); setSaveError(null)
      try { await api.updateModelPolicy(employee.id, { expected_revision: employee.revision, mode, models: mode === 'all' ? [] : [...models] }, csrf); onSaved() }
      catch (caught) { setSaveError(messageFor(caught)); setSaving(false) }
    }}>{saving ? '正在保存…' : '保存权限'}</Button></div>
  </Dialog>
}
