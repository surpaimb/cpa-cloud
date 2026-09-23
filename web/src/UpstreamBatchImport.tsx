import { useEffect, useRef, useState, type ChangeEvent } from 'react'
import { ApiError, api, type UpstreamBatchItem, type UpstreamBatchResult } from './api'
import { Button, Dialog, Field, FormError } from './ui'

const MAX_BATCH_BYTES = 8 * 1024 * 1024
const MAX_BATCH_ITEMS = 100
const providerKinds = new Set(['openai-compatible', 'anthropic-api-key', 'gemini-api-key', 'codex-membership'])
const allowedItemFields = new Set(['item_id', 'name', 'provider_kind', 'endpoint', 'api_key', 'auth_json'])

const SAFE_EXAMPLE = `{
  "items": [
    {
      "item_id": "finance-openai",
      "name": "Finance OpenAI",
      "provider_kind": "openai-compatible",
      "endpoint": "https://gateway.example/v1",
      "api_key": "REPLACE_WITH_API_KEY"
    },
    {
      "item_id": "research-gemini",
      "name": "Research Gemini",
      "provider_kind": "gemini-api-key",
      "api_key": "REPLACE_WITH_GEMINI_API_KEY"
    }
  ]
}`

const providerLabels: Record<UpstreamBatchItem['provider_kind'], string> = {
  'openai-compatible': 'OpenAI 兼容 API',
  'anthropic-api-key': 'Anthropic API',
  'gemini-api-key': 'Gemini 原生 API',
  'codex-membership': 'Codex 会员',
}

const itemErrorLabels: Record<string, string> = {
  invalid_item: '条目字段无效',
  unsupported_provider: '不支持此提供商',
  invalid_endpoint: 'API 端点无效',
  invalid_credential: '凭据无效或缺失',
  invalid_codex_auth: 'Codex auth.json 无效',
  feature_disabled: 'Codex 会员功能未开启',
  service_unavailable: '服务暂时不可用',
  storage_unavailable: '存储暂时不可用',
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object' && !Array.isArray(value)
}

function byteLength(value: string) {
  return new TextEncoder().encode(value).byteLength
}

function validText(value: string, maxBytes: number) {
  return value.length > 0 && byteLength(value) <= maxBytes && !/[\u0000-\u001f\u007f]/.test(value)
}

class SensitiveEndpointError extends Error {}

function validatedEndpoint(value: string, row: number) {
  try {
    const parsed = new URL(value)
    if ((parsed.protocol !== 'https:' && parsed.protocol !== 'http:') || !parsed.host || parsed.username || parsed.password || parsed.search || parsed.hash) throw new Error('invalid')
    return value
  } catch {
    throw new SensitiveEndpointError(`第 ${row} 项的 API 端点无效，且不能包含用户名、密码、查询参数或片段。`)
  }
}

function endpointPreview(value: string) {
  try { return new URL(value).origin } catch { return '自定义端点' }
}

function parseBatchSource(source: string): UpstreamBatchItem[] {
  let parsed: unknown
  try { parsed = JSON.parse(source) } catch { throw new Error('JSON 格式无效，请检查括号、引号和逗号。') }
  if (!isRecord(parsed) || Object.keys(parsed).some((key) => key !== 'items')) throw new Error('顶层只能包含 items 数组；操作编号由页面生成。')
  if (!Array.isArray(parsed.items) || parsed.items.length === 0 || parsed.items.length > MAX_BATCH_ITEMS) throw new Error('items 必须包含 1–100 个条目。')

  const ids = new Set<string>()
  return parsed.items.map((raw, index) => {
    const row = index + 1
    if (!isRecord(raw) || Object.keys(raw).some((key) => !allowedItemFields.has(key))) throw new Error(`第 ${row} 项包含不支持的字段。`)
    const rawItemID = raw.item_id
    const rawName = raw.name
    const providerKind = raw.provider_kind
    if (typeof rawItemID !== 'string') throw new Error(`第 ${row} 项的 item_id 必须为 1–120 个 UTF-8 字节，且不能包含控制字符。`)
    const itemID = rawItemID.trim()
    if (!validText(itemID, 120)) throw new Error(`第 ${row} 项的 item_id 必须为 1–120 个 UTF-8 字节，且不能包含控制字符。`)
    if (ids.has(itemID)) throw new Error(`第 ${row} 项的 item_id 与其他条目重复。`)
    ids.add(itemID)
    if (typeof rawName !== 'string' || !validText(rawName.trim(), 120)) throw new Error(`第 ${row} 项的显示名称必须为 1–120 个 UTF-8 字节，且不能包含控制字符。`)
    const name = rawName.trim()
    if (typeof providerKind !== 'string' || !providerKinds.has(providerKind)) throw new Error(`第 ${row} 项的 provider_kind 不受支持。`)

    const kind = providerKind as UpstreamBatchItem['provider_kind']
    const endpoint = raw.endpoint
    const apiKey = raw.api_key
    const authJSON = raw.auth_json
    if (endpoint !== undefined && typeof endpoint !== 'string') throw new Error(`第 ${row} 项的 endpoint 必须是字符串。`)
    if (kind === 'codex-membership') {
      if (typeof authJSON !== 'string' || !authJSON.trim() || apiKey !== undefined || endpoint !== undefined) throw new Error(`第 ${row} 项的 Codex 条目只接受 auth_json 文本，不接受 API Key 或端点。`)
      return { item_id: itemID, name: name.trim(), provider_kind: kind, auth_json: authJSON }
    }
    if (typeof apiKey !== 'string' || !apiKey.trim() || apiKey.startsWith('REPLACE_WITH_') || authJSON !== undefined) throw new Error(`第 ${row} 项必须提供已替换占位符的 API Key，且不能包含 auth_json。`)
    if (kind !== 'gemini-api-key' && (typeof endpoint !== 'string' || !endpoint.trim())) throw new Error(`第 ${row} 项必须提供 API 端点。`)
    const item: UpstreamBatchItem = { item_id: itemID, name: name.trim(), provider_kind: kind, api_key: apiKey }
    if (typeof endpoint === 'string' && endpoint.trim()) item.endpoint = validatedEndpoint(endpoint.trim(), row)
    return item
  })
}

function batchErrorMessage(error: unknown) {
  if (!(error instanceof ApiError)) return '未能确认批量导入结果。部分条目可能已经写入；请使用同一操作编号重试，不要重复创建批次。'
  if (error.code === 'operation_conflict' || error.status === 409) return '此操作编号已绑定到不同内容。请新建批次后再提交修改后的条目。'
  if (error.status === 400) return '服务端拒绝了批次格式；没有处理任何条目。请返回修改后重试。'
  if (error.status === 503) return '服务暂时不可用。可使用同一操作编号和原内容重试。'
  return '未能确认批量导入结果。部分条目可能已经写入；请使用同一操作编号重试。'
}

function readText(file: File) {
  if (typeof file.text === 'function') return file.text()
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader()
    reader.addEventListener('load', () => resolve(String(reader.result ?? '')))
    reader.addEventListener('error', () => reject(new Error('file_read_failed')))
    reader.readAsText(file)
  })
}

function validBatchResults(value: unknown, payload: UpstreamBatchItem[]): value is { items: UpstreamBatchResult[] } {
  if (!isRecord(value) || !Array.isArray(value.items) || value.items.length !== payload.length) return false
  return value.items.every((raw, index) => {
    if (!isRecord(raw) || raw.item_id !== payload[index].item_id || !['created', 'existing', 'failed'].includes(String(raw.status))) return false
    return (raw.upstream_id === undefined || typeof raw.upstream_id === 'string') && (raw.error_code === undefined || typeof raw.error_code === 'string')
  })
}

export function UpstreamBatchImport({ csrf, membershipEnabled, onClose, onImported }: { csrf: string; membershipEnabled: boolean; onClose: () => void; onImported: () => void | Promise<void> }) {
  const [source, setSource] = useState('')
  const [revealed, setRevealed] = useState(false)
  const [operationID, setOperationID] = useState(() => crypto.randomUUID())
  const [payload, setPayload] = useState<UpstreamBatchItem[] | null>(null)
  const [results, setResults] = useState<UpstreamBatchResult[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [canRetry, setCanRetry] = useState(false)
  const [copied, setCopied] = useState(false)
  const fileRef = useRef<HTMLInputElement>(null)
  const readGeneration = useRef(0)
  const submitGeneration = useRef(0)

  useEffect(() => () => {
    readGeneration.current += 1
    submitGeneration.current += 1
  }, [])

  function clearSensitive(newOperation = false) {
    readGeneration.current += 1
    submitGeneration.current += 1
    setSource('')
    setRevealed(false)
    setPayload(null)
    setResults(null)
    setError(null)
    setCanRetry(false)
    setCopied(false)
    if (fileRef.current) fileRef.current.value = ''
    if (newOperation) setOperationID(crypto.randomUUID())
  }

  function close() {
    if (busy) return
    clearSensitive()
    onClose()
  }

  async function chooseFile(event: ChangeEvent<HTMLInputElement>) {
    const generation = ++readGeneration.current
    const file = event.currentTarget.files?.[0]
    setError(null)
    if (!file) return
    if (file.size > MAX_BATCH_BYTES) {
      event.currentTarget.value = ''
      setSource('')
      setError('批量导入文件不能超过 8 MiB。')
      return
    }
    setSource('')
    setPayload(null)
    setResults(null)
    try {
      const text = await readText(file)
      if (readGeneration.current !== generation) return
      setSource(text)
      setRevealed(false)
    } catch {
      if (readGeneration.current === generation) setError('无法读取所选文件，请重新选择。')
    }
  }

  function preview() {
    setError(null)
    try {
      const items = parseBatchSource(source)
      const bodySize = new TextEncoder().encode(JSON.stringify({ operation_id: operationID, items })).byteLength
      if (bodySize > MAX_BATCH_BYTES) throw new Error('提交内容超过 8 MiB，请拆分为多个批次。')
      setPayload(items)
      setRevealed(false)
    } catch (caught) {
      if (caught instanceof SensitiveEndpointError) {
        readGeneration.current += 1
        setSource('')
        if (fileRef.current) fileRef.current.value = ''
      }
      setError(caught instanceof Error ? caught.message : '无法解析批次内容。')
    }
  }

  async function submit() {
    if (!payload || busy) return
    const generation = ++submitGeneration.current
    setBusy(true)
    setError(null)
    setCanRetry(false)
    try {
      const response = await api.batchImportUpstreams({ operation_id: operationID, items: payload }, csrf)
      if (submitGeneration.current !== generation) return
      if (!validBatchResults(response, payload)) throw new Error('invalid_response')
      if (response.items.some((item) => item.status === 'created' || item.status === 'existing')) await onImported()
      if (submitGeneration.current !== generation) return
      setResults(response.items)
      setSource('')
    } catch (caught) {
      if (submitGeneration.current !== generation) return
      const retryable = !(caught instanceof ApiError) || caught.status >= 500
      setCanRetry(retryable)
      setError(batchErrorMessage(caught))
    } finally {
      if (submitGeneration.current === generation) setBusy(false)
    }
  }

  function repairFailedItems() {
    if (!payload || !results) return
    const failedIDs = new Set(results.filter((item) => item.status === 'failed').map((item) => item.item_id))
    const failedItems = payload.filter((item) => failedIDs.has(item.item_id))
    readGeneration.current += 1
    setSource(JSON.stringify({ items: failedItems }, null, 2))
    setRevealed(false)
    setPayload(null)
    setResults(null)
    setError(null)
    setCanRetry(false)
  }

  return <Dialog title="批量导入上游" description="支持 OpenAI 兼容、Anthropic、Gemini 原生和 Codex 会员；单批最多 100 条。" onClose={close} closeDisabled={busy} wide>
    {!payload ? <>
      <div className="batch-import-notice"><strong>敏感信息处理</strong><p>内容默认遮掩，只保存在当前弹窗内，不写入浏览器存储，也不会在预览或结果中回显 API Key、Token 或 auth.json。请选择你明确提供的本地文件；页面不会读取服务器路径。导入不会联系提供商或在线验证，也不会自动创建模型路由或员工权限。</p></div>
      {!membershipEnabled ? <div className="membership-limitations"><strong>Codex 会员功能未开启</strong>批次仍可导入三种 API Key 上游；Codex 条目会返回“功能未开启”，且导入本身不代表在线验证成功。</div> : null}
      <details className="batch-example"><summary>查看无密钥格式示例</summary><pre>{SAFE_EXAMPLE}</pre><Button type="button" variant="secondary" onClick={async () => {
        try { await navigator.clipboard.writeText(SAFE_EXAMPLE); setCopied(true) } catch { setCopied(false) }
      }}>{copied ? '已复制示例' : '复制示例'}</Button></details>
      <div className="form-grid batch-import-editor">
        <Field label="选择 JSON 文件" hint="仅在你选择后读取；文件上限 8 MiB。"><input ref={fileRef} type="file" accept=".json,application/json" onChange={(event) => void chooseFile(event)} /></Field>
        <Field label="或粘贴 JSON" hint="顶层只写 items；operation_id 由页面生成。auth_json 必须是 JSON 字符串文本。"><textarea className={revealed ? '' : 'sensitive-textarea'} rows={10} value={source} onChange={(event) => { readGeneration.current += 1; setSource(event.target.value); setError(null) }} autoComplete="off" spellCheck={false} /></Field>
        <button type="button" className="link-button batch-reveal" onClick={() => setRevealed((value) => !value)}>{revealed ? '重新遮掩编辑内容' : '临时显示编辑内容'}</button>
      </div>
      <FormError error={error} />
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={close}>取消</Button><Button type="button" disabled={!source.trim()} onClick={preview}>检查并预览</Button></div>
    </> : results ? <>
      <BatchResults results={results} />
      <div className="success-note">批次已分类完成。已创建或已存在的上游列表已重新载入；尚未创建任何模型路由或员工权限。</div>
      <div className="dialog__actions"><Button type="button" variant="secondary" onClick={close}>关闭</Button>{results.some((item) => item.status === 'failed') ? <Button type="button" variant="secondary" onClick={repairFailedItems}>修复失败项</Button> : null}<Button type="button" onClick={() => clearSensitive(true)}>新建批次</Button></div>
    </> : <>
      <div className="batch-preview-heading"><div><strong>安全预览</strong><p>共 {payload.length} 项。预览不显示凭据，确认后才发送。</p></div><code>{operationID}</code></div>
      {!membershipEnabled && payload.some((item) => item.provider_kind === 'codex-membership') ? <div className="membership-limitations">此批次包含 Codex 条目，但会员功能未开启；这些条目预计会逐项失败，其他提供商仍可处理。</div> : null}
      <div className="batch-table-scroll"><table className="batch-table"><thead><tr><th>条目 ID</th><th>名称</th><th>提供商</th><th>端点</th></tr></thead><tbody>{payload.map((item) => <tr key={item.item_id}><td><code>{item.item_id}</code></td><td>{item.name}</td><td>{providerLabels[item.provider_kind]}</td><td>{item.endpoint ? <code>{endpointPreview(item.endpoint)}</code> : <span className="muted-copy">服务端固定</span>}</td></tr>)}</tbody></table></div>
      <FormError error={error} />
      {error && canRetry ? <div className="batch-uncertain" role="status">保留了原始条目与操作编号。重试将发送完全相同的批次，以便服务端返回 created 或 existing，而不是重复创建。</div> : null}
      <div className="dialog__actions">
        {!error ? <Button type="button" variant="secondary" disabled={busy} onClick={() => { setPayload(null); setError(null) }}>返回修改</Button> : null}
        {error && !canRetry ? <Button type="button" variant="secondary" disabled={busy} onClick={() => error.includes('操作编号') ? clearSensitive(true) : setPayload(null)}>{error.includes('操作编号') ? '新建批次' : '返回修改'}</Button> : null}
        <Button type="button" variant="secondary" disabled={busy} onClick={close}>关闭</Button>
        <Button type="button" disabled={busy} onClick={() => void submit()}>{busy ? '正在导入…' : error && canRetry ? '使用同一操作编号重试' : '确认批量导入'}</Button>
      </div>
    </>}
  </Dialog>
}

function BatchResults({ results }: { results: UpstreamBatchResult[] }) {
  return <div className="batch-table-scroll"><table className="batch-table"><thead><tr><th>条目 ID</th><th>结果</th><th>说明</th></tr></thead><tbody>{results.map((item) => {
    const label = item.status === 'created' ? '已创建' : item.status === 'existing' ? '已存在' : '失败'
    const detail = item.status === 'failed' ? (itemErrorLabels[item.error_code ?? ''] ?? '条目未创建，请检查格式后新建批次。') : item.upstream_id ?? '上游记录已保存'
    return <tr key={item.item_id}><td><code>{item.item_id}</code></td><td><span className={`batch-result batch-result--${item.status}`}>{label}</span></td><td>{detail}</td></tr>
  })}</tbody></table></div>
}
