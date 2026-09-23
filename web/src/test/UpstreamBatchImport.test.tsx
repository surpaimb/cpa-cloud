import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UpstreamBatchImport } from '../UpstreamBatchImport'

const jsonResponse = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

const apiItem = {
  item_id: 'finance-openai',
  name: 'Finance OpenAI',
  provider_kind: 'openai-compatible',
  endpoint: 'https://gateway.example/v1',
  api_key: 'mock-api-key-never-render',
}

function inputFor(items: unknown[]) {
  return JSON.stringify({ items })
}

async function preview(items: unknown[]) {
  fireEvent.change(screen.getByLabelText('或粘贴 JSON'), { target: { value: inputFor(items) } })
  await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
  return screen.findByText('安全预览')
}

describe('upstream batch import', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('renders a secret-free preview and readable mixed per-item results', async () => {
    const codexSecret = JSON.stringify({ tokens: { access_token: 'mock-token-never-render' } })
    const onImported = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/batch-import') && init?.method === 'POST') return jsonResponse({ items: [
        { item_id: 'finance-openai', status: 'created', upstream_id: 'ups-1' },
        { item_id: 'existing-gemini', status: 'existing', upstream_id: 'ups-2' },
        { item_id: 'codex-disabled', status: 'failed', error_code: 'feature_disabled' },
      ] })
      throw new Error(`Unexpected request ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled={false} onClose={() => undefined} onImported={onImported} />)

    await preview([
      apiItem,
      { item_id: 'existing-gemini', name: 'Gemini', provider_kind: 'gemini-api-key', api_key: 'mock-gemini-key' },
      { item_id: 'codex-disabled', name: 'Codex', provider_kind: 'codex-membership', auth_json: codexSecret },
    ])
    expect(screen.getByText(/会员功能未开启/)).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent('mock-api-key-never-render')
    expect(document.body).not.toHaveTextContent('mock-token-never-render')

    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    expect(await screen.findByText('已创建')).toBeInTheDocument()
    expect(screen.getByText('已存在')).toBeInTheDocument()
    expect(screen.getByText('Codex 会员功能未开启')).toBeInTheDocument()
    expect(screen.getByText(/尚未创建任何模型路由或员工权限/)).toBeInTheDocument()
    expect(onImported).toHaveBeenCalledTimes(1)
    expect(document.body).not.toHaveTextContent('mock-api-key-never-render')
    expect(document.body).not.toHaveTextContent('mock-token-never-render')

    const request = fetchMock.mock.calls[0][1] as RequestInit
    const body = JSON.parse(String(request.body))
    expect(body.operation_id).toMatch(/^[0-9a-f-]{36}$/)
    expect(body.items[0]).toEqual(apiItem)
    expect(body.items[2].auth_json).toBe(codexSecret)
    expect(new Headers(request.headers).get('X-CSRF-Token')).toBe('csrf')
  })

  it('retries an ambiguous network result with the same operation ID and exact payload', async () => {
    let attempt = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith('/upstreams/batch-import')) throw new Error(`Unexpected request ${String(input)}`)
      attempt += 1
      if (attempt === 1) return Promise.reject(new TypeError('connection reset'))
      return jsonResponse({ items: [{ item_id: apiItem.item_id, status: 'existing', upstream_id: 'ups-1' }] })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    await preview([apiItem])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('部分条目可能已经写入')
    expect(screen.getByText(/保留了原始条目与操作编号/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '使用同一操作编号重试' }))
    expect(await screen.findByText('已存在')).toBeInTheDocument()

    expect(fetchMock).toHaveBeenCalledTimes(2)
    const firstBody = String((fetchMock.mock.calls[0][1] as RequestInit).body)
    const secondBody = String((fetchMock.mock.calls[1][1] as RequestInit).body)
    expect(secondBody).toBe(firstBody)
  })

  it('rejects invalid JSON and an oversized explicitly selected file before any request', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const first = render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    await userEvent.upload(screen.getByLabelText('选择 JSON 文件'), new File(['{'], 'invalid.json', { type: 'application/json' }))
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('JSON 格式无效')
    expect(fetchMock).not.toHaveBeenCalled()

    fireEvent.change(screen.getByLabelText('或粘贴 JSON'), { target: { value: inputFor([{ ...apiItem, client_id: 'not-allowed' }]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('不支持的字段')
    expect(fetchMock).not.toHaveBeenCalled()

    first.unmount()
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    const oversized = new File([new Uint8Array(8 * 1024 * 1024 + 1)], 'too-large.json', { type: 'application/json' })
    await userEvent.upload(screen.getByLabelText('选择 JSON 文件'), oversized)
    expect(await screen.findByRole('alert')).toHaveTextContent('不能超过 8 MiB')
    expect(screen.getByRole('button', { name: '检查并预览' })).toBeDisabled()
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('clears sensitive input when closed and starts empty when reopened', async () => {
    function Harness() {
      const [open, setOpen] = useState(true)
      return <>{open ? <UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => setOpen(false)} onImported={() => undefined} /> : <button onClick={() => setOpen(true)}>重新打开</button>}</>
    }
    render(<Harness />)
    const textarea = screen.getByLabelText('或粘贴 JSON')
    fireEvent.change(textarea, { target: { value: inputFor([apiItem]) } })
    expect((textarea as HTMLTextAreaElement).value).toContain('mock-api-key-never-render')
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重新打开' }))
    expect(screen.getByLabelText('或粘贴 JSON')).toHaveValue('')
    expect(document.body).not.toHaveTextContent('mock-api-key-never-render')
  })

  it('requires an explicit new batch before modified successful items receive a new operation ID', async () => {
    const bodies: Array<{ operation_id: string }> = []
    vi.stubGlobal('fetch', vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body)))
      return jsonResponse({ items: [{ item_id: apiItem.item_id, status: 'created', upstream_id: `ups-${bodies.length}` }] })
    }))
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    await preview([apiItem])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    await screen.findByText('已创建')
    expect(screen.queryByLabelText('或粘贴 JSON')).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '新建批次' }))
    const changed = { ...apiItem, name: 'Finance OpenAI Updated' }
    await preview([changed])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].operation_id).not.toBe(bodies[0].operation_id)
  })

  it('rejects secret-bearing endpoint components and previews only the endpoint origin', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    const textarea = screen.getByLabelText('或粘贴 JSON')
    const secretEndpoint = { ...apiItem, endpoint: 'https://user:password@gateway.example/private?token=query-secret-sentinel#fragment' }
    fireEvent.change(textarea, { target: { value: inputFor([secretEndpoint]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('不能包含用户名、密码、查询参数或片段')
    expect(document.body).not.toHaveTextContent('query-secret-sentinel')

    const pathEndpoint = { ...apiItem, endpoint: 'https://gateway.example/private-path-secret-sentinel/v1' }
    fireEvent.change(textarea, { target: { value: inputFor([pathEndpoint]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByText('安全预览')).toBeInTheDocument()
    expect(screen.getByText('https://gateway.example')).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent('private-path-secret-sentinel')
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('locks modification and closing while submission is in flight', async () => {
    let resolveRequest!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { resolveRequest = resolve })
    const onClose = vi.fn()
    vi.stubGlobal('fetch', vi.fn(() => pending))
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={onClose} onImported={() => undefined} />)
    await preview([apiItem])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))

    expect(screen.getByRole('button', { name: '返回修改' })).toBeDisabled()
    expect(screen.getAllByRole('button', { name: '关闭' })).toHaveLength(2)
    expect(screen.getAllByRole('button', { name: '关闭' }).every((button) => button.hasAttribute('disabled'))).toBe(true)
    fireEvent.mouseDown(document.querySelector('.dialog-backdrop')!)
    expect(onClose).not.toHaveBeenCalled()

    resolveRequest(new Response(JSON.stringify({ items: [{ item_id: apiItem.item_id, status: 'created', upstream_id: 'ups-1' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    expect(await screen.findByText('已创建')).toBeInTheDocument()
  })

  it('ignores a late submission response after a forced unmount without aborting the request', async () => {
    let resolveRequest!: (response: Response) => void
    const pending = new Promise<Response>((resolve) => { resolveRequest = resolve })
    const onImported = vi.fn()
    vi.stubGlobal('fetch', vi.fn(() => pending))
    const rendered = render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={onImported} />)
    await preview([apiItem])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    rendered.unmount()

    resolveRequest(new Response(JSON.stringify({ items: [{ item_id: apiItem.item_id, status: 'created', upstream_id: 'ups-late' }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await Promise.resolve()
    expect(onImported).not.toHaveBeenCalled()
  })

  it('keeps only the latest file read and lets manual input invalidate a pending read', async () => {
    let resolveA!: (value: string) => void
    let resolveB!: (value: string) => void
    let resolveC!: (value: string) => void
    const fileA = new File([''], 'a.json', { type: 'application/json' })
    const fileB = new File([''], 'b.json', { type: 'application/json' })
    const fileC = new File([''], 'c.json', { type: 'application/json' })
    Object.defineProperty(fileA, 'text', { value: () => new Promise<string>((resolve) => { resolveA = resolve }) })
    Object.defineProperty(fileB, 'text', { value: () => new Promise<string>((resolve) => { resolveB = resolve }) })
    Object.defineProperty(fileC, 'text', { value: () => new Promise<string>((resolve) => { resolveC = resolve }) })
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    const fileInput = screen.getByLabelText('选择 JSON 文件')
    await userEvent.upload(fileInput, fileA)
    await userEvent.upload(fileInput, fileB)
    const sourceB = inputFor([{ ...apiItem, item_id: 'from-b' }])
    const sourceA = inputFor([{ ...apiItem, item_id: 'from-a' }])
    resolveB(sourceB)
    await waitFor(() => expect(screen.getByLabelText('或粘贴 JSON')).toHaveValue(sourceB))
    resolveA(sourceA)
    await Promise.resolve()
    expect(screen.getByLabelText('或粘贴 JSON')).toHaveValue(sourceB)

    await userEvent.upload(fileInput, fileC)
    const manual = inputFor([{ ...apiItem, item_id: 'manual-input' }])
    fireEvent.change(screen.getByLabelText('或粘贴 JSON'), { target: { value: manual } })
    resolveC(inputFor([{ ...apiItem, item_id: 'from-c' }]))
    await Promise.resolve()
    expect(screen.getByLabelText('或粘贴 JSON')).toHaveValue(manual)
  })

  it('repairs only failed items without resubmitting successful siblings', async () => {
    const failedItem = { item_id: 'retry-gemini', name: 'Retry Gemini', provider_kind: 'gemini-api-key', api_key: 'retry-only-secret' }
    const bodies: Array<{ operation_id: string; items: Array<{ item_id: string }> }> = []
    vi.stubGlobal('fetch', vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      const body = JSON.parse(String(init?.body))
      bodies.push(body)
      return bodies.length === 1
        ? jsonResponse({ items: [{ item_id: apiItem.item_id, status: 'created', upstream_id: 'ups-1' }, { item_id: failedItem.item_id, status: 'failed', error_code: 'storage_unavailable' }] })
        : jsonResponse({ items: [{ item_id: failedItem.item_id, status: 'created', upstream_id: 'ups-2' }] })
    }))
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    await preview([apiItem, failedItem])
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    expect(await screen.findByText('失败')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '修复失败项' }))

    const repairedSource = (screen.getByLabelText('或粘贴 JSON') as HTMLTextAreaElement).value
    expect(repairedSource).toContain('retry-only-secret')
    expect(repairedSource).not.toContain('mock-api-key-never-render')
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    await userEvent.click(screen.getByRole('button', { name: '确认批量导入' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1].operation_id).toBe(bodies[0].operation_id)
    expect(bodies[1].items.map((item) => item.item_id)).toEqual([failedItem.item_id])
  })

  it('validates item IDs and names by UTF-8 bytes and rejects control characters', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    render(<UpstreamBatchImport csrf="csrf" membershipEnabled onClose={() => undefined} onImported={() => undefined} />)
    const textarea = screen.getByLabelText('或粘贴 JSON')

    fireEvent.change(textarea, { target: { value: inputFor([{ ...apiItem, item_id: '中'.repeat(60) }]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('item_id 必须为 1–120 个 UTF-8 字节')

    fireEvent.change(textarea, { target: { value: inputFor([{ ...apiItem, name: '中'.repeat(60) }]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('显示名称必须为 1–120 个 UTF-8 字节')

    fireEvent.change(textarea, { target: { value: inputFor([{ ...apiItem, name: `Bad${String.fromCharCode(1)}Name` }]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('不能包含控制字符')

    const boundary = '中'.repeat(40)
    fireEvent.change(textarea, { target: { value: inputFor([{ ...apiItem, item_id: boundary, name: boundary }]) } })
    await userEvent.click(screen.getByRole('button', { name: '检查并预览' }))
    expect(await screen.findByText('安全预览')).toBeInTheDocument()
    expect(fetchMock).not.toHaveBeenCalled()
  })
})
