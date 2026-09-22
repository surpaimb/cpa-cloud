import { useCallback, useState } from 'react'
import { api } from '../api'
import { messageFor, useResource } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { PageHeader } from './EmployeesPage'

export function ModelsPage({ csrf }: { csrf: string }) {
  const loadModels = useCallback(() => api.models(), [])
  const { data, loading, error, reload } = useResource(loadModels)
  const [creating, setCreating] = useState(false)
  return <>
    <PageHeader title="模型路由" description="将员工可用的模型名称映射到已配置的上游模型。"><Button onClick={() => setCreating(true)}><Icon name="plus" />添加模型路由</Button></PageHeader>
    <div className="content-panel"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有模型路由" body="先添加上游连接，再建立第一个模型路由。" action={<Button onClick={() => setCreating(true)}>添加模型路由</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table><thead><tr><th>对外模型 ID</th><th>上游</th><th>上游模型</th><th>状态</th></tr></thead><tbody>{data.items.map((item) => <tr key={item.id}><td><strong>{item.id}</strong></td><td><code>{item.upstream_id}</code></td><td>{item.upstream_model}</td><td><span className={`status status--${item.enabled ? 'active' : 'disabled'}`}><i />{item.enabled ? '启用' : '已停用'}</span></td></tr>)}</tbody></table></div> : null}
    </div>
    {creating ? <CreateModel csrf={csrf} onClose={() => setCreating(false)} onCreated={() => { setCreating(false); void reload() }} /> : null}
  </>
}

function CreateModel({ csrf, onClose, onCreated }: { csrf: string; onClose: () => void; onCreated: () => void }) {
  const loadUpstreams = useCallback(() => api.upstreams(), [])
  const { data, loading, error, reload } = useResource(loadUpstreams)
  const [busy, setBusy] = useState(false); const [saveError, setSaveError] = useState<string | null>(null)
  const enabled = data?.items.filter((item) => item.enabled) ?? []
  return <Dialog title="添加模型路由" description="预览版中，一个对外模型对应一条上游路由。" onClose={onClose}>
    <PageState loading={loading} error={error} onRetry={() => void reload()} />
    {!loading && !error && enabled.length === 0 ? <EmptyState title="没有可用上游" body="请先在“上游连接”中添加并启用上游。" /> : null}
    {enabled.length ? <form onSubmit={submitHandler(async (form) => {
      setBusy(true); setSaveError(null)
      try { await api.createModel({ id: String(form.get('id')), upstream_id: String(form.get('upstream_id')), upstream_model: String(form.get('upstream_model')) }, csrf); onCreated() }
      catch (caught) { setSaveError(messageFor(caught)); setBusy(false) }
    })}><div className="form-grid"><Field label="对外模型 ID" hint="员工工具在 /v1/models 中看到的名称。"><input name="id" placeholder="例如：gpt-4.1" required autoFocus /></Field><Field label="上游连接"><select name="upstream_id" required>{enabled.map((item) => <option key={item.id} value={item.id}>{item.name}</option>)}</select></Field><Field label="上游模型名称"><input name="upstream_model" placeholder="例如：gpt-4.1" required /></Field></div><FormError error={saveError} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在添加…' : '添加路由'}</Button></div></form> : null}
  </Dialog>
}
