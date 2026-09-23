import { act, cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ProxiesPage } from '../pages/ProxiesPage'

type Route = { status?: number; body?: unknown }
const response = (route: Route) => Promise.resolve(new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200, headers: { 'Content-Type': 'application/json' } }))
const proxy = (id: string, overrides: Record<string, unknown> = {}) => ({
  id,
  name: `代理 ${id}`,
  scheme: 'https',
  host: `${id}.proxy.example`,
  port: 443,
  address_scope: 'public',
  enabled: true,
  revision: 2,
  connection_revision: 2,
  has_credentials: false,
  created_at: '2026-09-23T00:00:00Z',
  updated_at: '2026-09-23T00:00:00Z',
  ...overrides,
})
const upstream = (id: string, kind = 'openai-compatible', endpoint = 'https://models.example/v1') => ({
  id,
  name: `账号 ${id}`,
  provider_kind: kind,
  endpoint,
  enabled: true,
  revision: 4,
  credential_state: null,
  verified_at: null,
})

function baseFetch(proxies = [proxy('proxy-1')], upstreams: ReturnType<typeof upstream>[] = []) {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.includes('/outbound-proxies?')) return response({ body: { items: proxies, next_cursor: null } })
    if (url.endsWith('/upstreams')) return response({ body: { items: upstreams } })
    if (/\/upstreams\/[^/]+\/proxy$/.test(url)) {
      const id = url.split('/').at(-2)
      return response({ body: { upstream_id: id, upstream_revision: 4, binding: null } })
    }
    throw new Error(`Unexpected request: ${url}`)
  })
}

describe('outbound proxy management', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('paginates the directory and explains an older backend 404', async () => {
    let second = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/upstreams')) return response({ body: { items: [] } })
      if (url.includes('after_id=proxy-1')) { second = true; return response({ body: { items: [proxy('proxy-2')], next_cursor: null } }) }
      if (url.includes('/outbound-proxies?')) return response({ body: { items: [proxy('proxy-1')], next_cursor: 'proxy-1' } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ProxiesPage csrf="csrf" />)
    expect(await screen.findByText('代理 proxy-1')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText('代理 proxy-2')).toBeInTheDocument()
    expect(second).toBe(true)

    cleanup()
    vi.stubGlobal('fetch', vi.fn(() => response({ status: 404, body: { error: { code: 'not_found', message: 'missing' } } })))
    render(<ProxiesPage csrf="csrf" />)
    expect(await screen.findByText('当前服务版本不支持出站代理管理，请升级服务后重试。')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '添加代理' })).toBeDisabled()
    view.unmount()
  })

  it('retries an unknown create only with the original operation and clears the secret after success', async () => {
    const posts: string[] = []
    let attempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/outbound-proxies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [] } })
      if (url.endsWith('/outbound-proxies') && init?.method === 'POST') {
        posts.push(String(init.body)); attempts += 1
        if (attempts === 1) return Promise.reject(new TypeError('connection reset'))
        return response({ body: proxy('created', { has_credentials: true }) })
      }
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    const localSpy = vi.spyOn(Storage.prototype, 'setItem')
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxiesPage csrf="csrf-token" />)
    await screen.findByText('还没有出站代理')
    await userEvent.click(screen.getAllByRole('button', { name: '添加代理' })[0])
    await userEvent.type(screen.getByLabelText('显示名称'), '北京出口')
    await userEvent.type(screen.getByLabelText('代理主机'), 'proxy.corp.example')
    await userEvent.type(screen.getByLabelText('用户名'), 'operator')
    await userEvent.type(screen.getByLabelText('密码'), 'proxy-secret')
    await userEvent.click(screen.getByRole('button', { name: '保存代理' }))
    expect(await screen.findByText('创建结果未知')).toBeInTheDocument()
    expect(screen.getByLabelText('密码')).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '重试原创建操作' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posts).toHaveLength(2)
    expect(posts[1]).toBe(posts[0])
    const body = JSON.parse(posts[0])
    expect(body.operation_id).toMatch(/^[0-9a-f-]{36}$/i)
    expect(body.credentials).toEqual({ username: 'operator', password: 'proxy-secret' })
    expect(localSpy).not.toHaveBeenCalled()

    await userEvent.click(screen.getAllByRole('button', { name: '添加代理' })[0])
    expect(screen.getByLabelText('密码')).toHaveValue('')
  })

  it('reads the actual proxy after a conflict, clears replacement credentials, and never replays PATCH', async () => {
    const item = proxy('proxy-1')
    let detailReads = 0
    let patches = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/outbound-proxies?')) return response({ body: { items: [item], next_cursor: null } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [] } })
      if (url.endsWith('/outbound-proxies/proxy-1') && init?.method === 'PATCH') { patches += 1; return response({ status: 409, body: { error: { code: 'revision_conflict', message: 'conflict' } } }) }
      if (url.endsWith('/outbound-proxies/proxy-1')) { detailReads += 1; return response({ body: proxy('proxy-1', { revision: detailReads === 1 ? 2 : 3, name: detailReads === 1 ? '旧名称' : '并发后的名称' }) }) }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxiesPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '编辑 / 启停' }))
    await screen.findByDisplayValue('旧名称')
    await userEvent.selectOptions(screen.getByLabelText('代理认证'), 'replace')
    await userEvent.type(screen.getByLabelText('用户名'), 'operator')
    await userEvent.type(screen.getByLabelText('密码'), 'must-disappear')
    await userEvent.click(screen.getByRole('button', { name: '保存变更' }))
    expect(await screen.findByText('已读取实际状态，请核对')).toBeInTheDocument()
    expect(screen.getByDisplayValue('并发后的名称')).toBeDisabled()
    expect(screen.queryByDisplayValue('must-disappear')).not.toBeInTheDocument()
    expect(patches).toBe(1)
    expect(detailReads).toBe(2)
  })

  it('blocks Codex and HTTP bindings, excludes disabled proxies, and verifies a binding conflict without replay', async () => {
    const upstreams = [upstream('codex', 'codex-membership', ''), upstream('http', 'openai-compatible', 'http://legacy.example/v1'), upstream('https')]
    const proxies = [proxy('enabled'), proxy('disabled', { enabled: false })]
    let puts = 0
    let bindingReads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/outbound-proxies?')) return response({ body: { items: proxies, next_cursor: null } })
      if (url.endsWith('/upstreams')) return response({ body: { items: upstreams } })
      if (url.endsWith('/upstreams/https/proxy') && init?.method === 'PUT') { puts += 1; return response({ status: 409, body: { error: { code: 'revision_conflict', message: 'conflict' } } }) }
      if (/\/upstreams\/[^/]+\/proxy$/.test(url)) {
        const id = url.split('/').at(-2)
        if (id === 'https') bindingReads += 1
        return response({ body: { upstream_id: id, upstream_revision: id === 'https' && bindingReads > 2 ? 5 : 4, binding: null } })
      }
      if (url.endsWith('/outbound-proxies/enabled')) return response({ body: proxy('enabled') })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxiesPage csrf="csrf" />)
    expect(await screen.findByText('Codex 使用服务端固定目标，当前不支持绑定代理。')).toBeInTheDocument()
    expect(screen.getByText('HTTP 目标不支持绑定代理。')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '绑定代理' })).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: '绑定代理' }))
    const dialog = await screen.findByRole('dialog')
    const select = within(dialog).getByLabelText('选择已启用代理')
    expect(select).toHaveTextContent('代理 enabled')
    expect(select).not.toHaveTextContent('代理 disabled')
    await userEvent.selectOptions(select, 'enabled')
    await userEvent.click(within(dialog).getByRole('button', { name: '绑定代理' }))
    expect(await within(dialog).findByText('已读取实际绑定，请核对')).toBeInTheDocument()
    expect(puts).toBe(1)
    expect(fetchMock.mock.calls.some(([url]) => /cooldown|recovery/.test(String(url)))).toBe(false)
  })

  it('keeps a disabled existing binding until an explicit unbind', async () => {
    const account = upstream('https')
    const disabled = proxy('disabled', { enabled: false })
    let puts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/outbound-proxies?')) return response({ body: { items: [disabled], next_cursor: null } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [account] } })
      if (url.endsWith('/outbound-proxies/disabled')) return response({ body: disabled })
      if (url.endsWith('/upstreams/https/proxy') && init?.method === 'PUT') { puts += 1; return response({ body: { upstream_id: 'https', upstream_revision: 5, binding: null } }) }
      if (url.endsWith('/upstreams/https/proxy')) return response({ body: { upstream_id: 'https', upstream_revision: 4, binding: { proxy_id: 'disabled', proxy_revision: 2, connection_revision: 2, enabled: false, name: '代理 disabled' } } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxiesPage csrf="csrf" />)
    expect(await screen.findByText('代理已停用，绑定仍保留')).toBeInTheDocument()
    expect(puts).toBe(0)
    await userEvent.click(screen.getByRole('button', { name: '修改 / 解绑' }))
    await userEvent.click(await screen.findByRole('button', { name: '明确解绑为直连' }))
    await waitFor(() => expect(puts).toBe(1))
  })

  it('does not let a delayed editor response replace a newly opened proxy', async () => {
    let resolveFirst!: (response: Response) => void
    const first = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('/outbound-proxies?')) return response({ body: { items: [proxy('a'), proxy('b')], next_cursor: null } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [] } })
      if (url.endsWith('/outbound-proxies/a')) return first
      if (url.endsWith('/outbound-proxies/b')) return response({ body: proxy('b', { name: '当前代理 B' }) })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxiesPage csrf="csrf" />)
    const edits = await screen.findAllByRole('button', { name: '编辑 / 启停' })
    await userEvent.click(edits[0])
    await userEvent.click(await screen.findByRole('button', { name: '关闭' }))
    await userEvent.click(edits[1])
    expect(await screen.findByDisplayValue('当前代理 B')).toBeInTheDocument()
    await act(async () => { resolveFirst(await response({ body: proxy('a', { name: '过期代理 A' }) })) })
    await waitFor(() => expect(screen.queryByDisplayValue('过期代理 A')).not.toBeInTheDocument())
    expect(screen.getByDisplayValue('当前代理 B')).toBeInTheDocument()
  })
})
