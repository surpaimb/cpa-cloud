import { useCallback, useState } from 'react'
import { api, type Upstream } from '../api'
import { messageFor, useResource } from '../hooks'
import { Button, Dialog, EmptyState, Field, FormError, Icon, PageState, submitHandler } from '../ui'
import { PageHeader } from './EmployeesPage'

export function UpstreamsPage({ csrf }: { csrf: string }) {
  const load = useCallback(() => api.upstreams(), [])
  const { data, loading, error, reload } = useResource(load)
  const [creating, setCreating] = useState(false)
  return <>
    <PageHeader title="上游连接" description="连接兼容 OpenAI 协议的上游服务。密钥加密保存且不会再次显示。"><Button onClick={() => setCreating(true)}><Icon name="plus" />添加上游</Button></PageHeader>
    <div className="content-panel"><PageState loading={loading} error={error} onRetry={() => void reload()} />
      {!loading && !error && data?.items.length === 0 ? <EmptyState title="还没有上游连接" body="添加一个兼容 OpenAI 协议的上游，再配置模型路由。" action={<Button onClick={() => setCreating(true)}>添加上游</Button>} /> : null}
      {data?.items.length ? <div className="table-scroll"><table><thead><tr><th>名称</th><th>类型</th><th>端点</th><th>状态</th><th>操作</th></tr></thead><tbody>{data.items.map((item) => <UpstreamRow key={item.id} item={item} csrf={csrf} onDone={() => void reload()} />)}</tbody></table></div> : null}
    </div>
    {creating ? <CreateUpstream csrf={csrf} onClose={() => setCreating(false)} onCreated={() => { setCreating(false); void reload() }} /> : null}
  </>
}

function UpstreamRow({ item, csrf, onDone }: { item: Upstream; csrf: string; onDone: () => void }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState<string | null>(null)
  return <tr><td><strong>{item.name}</strong></td><td>OpenAI 兼容</td><td><code className="endpoint">{item.endpoint}</code></td><td><span className={`status status--${item.enabled ? 'active' : 'disabled'}`}><i />{item.enabled ? '启用' : '已停用'}</span></td><td><div className="row-actions"><button className="link-button" disabled={busy} onClick={async () => {
    setBusy(true); setError(null)
    try { await api.updateUpstream(item.id, { expected_revision: item.revision, enabled: !item.enabled }, csrf); onDone() }
    catch (caught) { setError(messageFor(caught)); setBusy(false) }
  }}>{busy ? '处理中…' : item.enabled ? '停用' : '启用'}</button>{error ? <span className="action-error">{error}</span> : null}</div></td></tr>
}

function CreateUpstream({ csrf, onClose, onCreated }: { csrf: string; onClose: () => void; onCreated: () => void }) {
  const [busy, setBusy] = useState(false); const [error, setError] = useState<string | null>(null)
  return <Dialog title="添加上游" description="密钥仅用于服务端请求，不会在列表或日志中显示。" onClose={onClose}>
    <form onSubmit={submitHandler(async (form) => {
      setBusy(true); setError(null)
      try { await api.createUpstream({ name: String(form.get('name')), provider_kind: 'openai-compatible', endpoint: String(form.get('endpoint')), api_key: String(form.get('api_key')) }, csrf); onCreated() }
      catch (caught) { setError(messageFor(caught)); setBusy(false) }
    })}>
      <div className="form-grid"><Field label="名称"><input name="name" placeholder="例如：公司 OpenAI" required autoFocus /></Field><Field label="API 端点" hint="请输入完整 HTTPS 地址；测试环境是否允许回环地址由服务端决定。"><input name="endpoint" type="url" placeholder="https://api.example.com/v1" required /></Field><Field label="API Key"><input name="api_key" type="password" autoComplete="new-password" required /></Field></div>
      <FormError error={error} /><div className="dialog__actions"><Button type="button" variant="secondary" onClick={onClose}>取消</Button><Button type="submit" disabled={busy}>{busy ? '正在添加…' : '添加上游'}</Button></div>
    </form>
  </Dialog>
}
