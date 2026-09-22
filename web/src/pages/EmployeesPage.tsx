import { useCallback, useState } from 'react'
import { api, type Employee, type EmployeeKey, type ModelRoute } from '../api'
import { messageFor, useResource } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'

export function EmployeesPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.employees(), [])
  const { data, loading, error, reload } = useResource(load)
  const [creating, setCreating] = useState(false)
  const [selected, setSelected] = useState<Employee | null>(null)
  const [policy, setPolicy] = useState<Employee | null>(null)

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
    {selected ? <KeysDialog employee={selected} csrf={csrf} onClose={() => setSelected(null)} /> : null}
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

function KeysDialog({ employee, csrf, onClose }: { employee: Employee; csrf: string; onClose: () => void }) {
  const load = useCallback(() => api.keys(employee.id), [employee.id])
  const { data, loading, error, reload } = useResource(load)
  const [name, setName] = useState('默认 Key')
  const [created, setCreated] = useState<EmployeeKey | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  if (created?.key) return <KeyReveal created={created} onClose={() => { setCreated(null); onClose() }} />
  return <Dialog title={`${employee.name} 的 Key`} description="Key 默认永久有效；可为不同设备或用途分别创建。" onClose={onClose} wide>
    <div className="key-create"><Field label="Key 名称"><input value={name} onChange={(e) => setName(e.target.value)} /></Field><Button disabled={busy || !name.trim()} onClick={async () => {
      setBusy(true); setActionError(null)
      try { setCreated(await api.createKey(employee.id, name.trim(), csrf)); await reload() }
      catch (caught) { setActionError(messageFor(caught)); setBusy(false) }
    }}><Icon name="plus" />{busy ? '正在生成…' : '生成永久 Key'}</Button></div>
    <FormError error={actionError} /><PageState loading={loading} error={error} onRetry={() => void reload()} />
    {data?.items.length === 0 ? <EmptyState title="还没有 Key" body="生成后明文只展示一次，请立即妥善保存。" /> : null}
    {data?.items.length ? <div className="key-list">{data.items.map((key) => <div key={key.id} className="key-row"><div><strong>{key.name}</strong><span>{key.revoked_at ? '已撤销' : key.expires_at ? `到期：${new Date(key.expires_at).toLocaleDateString('zh-CN')}` : '永久有效'}</span></div>{key.revoked_at ? null : <Button variant="danger" onClick={async () => { try { await api.revokeKey(key.id, csrf); await reload() } catch (caught) { setActionError(messageFor(caught)) } }}>撤销</Button>}</div>)}</div> : null}
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
    <FormError error={saveError} /><div className="dialog__actions"><Button variant="secondary" onClick={onClose}>取消</Button><Button disabled={saving || (mode === 'selected' && models.size === 0)} onClick={async () => {
      setSaving(true); setSaveError(null)
      try { await api.updateModelPolicy(employee.id, { expected_revision: employee.revision, mode, models: mode === 'all' ? [] : [...models] }, csrf); onSaved() }
      catch (caught) { setSaveError(messageFor(caught)); setSaving(false) }
    }}>{saving ? '正在保存…' : '保存权限'}</Button></div>
  </Dialog>
}
