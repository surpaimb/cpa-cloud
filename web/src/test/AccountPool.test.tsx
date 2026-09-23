import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AccountPoolDirectory, ModelAccountPoolEditor } from '../AccountPool'
import { ModelsPage } from '../pages/ModelsPage'
import type { ModelRoute } from '../api'

type Route = { status?: number; body?: unknown }
const response = (route: Route) => Promise.resolve(new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200, headers: { 'Content-Type': 'application/json' } }))
const model = (id = 'public-model'): ModelRoute => ({ id, upstream_id: 'up-1', upstream_model: 'provider-model', enabled: true })
const upstreams = [
  { id: 'up-1', name: '主账号', provider_kind: 'openai-compatible', endpoint: 'https://one.example/v1', enabled: true, revision: 1, credential_state: null, verified_at: null },
  { id: 'up-2', name: '备用账号', provider_kind: 'openai-compatible', endpoint: 'https://two.example/v1', enabled: true, revision: 1, credential_state: null, verified_at: null },
  { id: 'up-disabled', name: '停用账号', provider_kind: 'openai-compatible', endpoint: 'https://disabled.example/v1', enabled: false, revision: 1, credential_state: null, verified_at: null },
  { id: 'anthropic-1', name: '其他服务商', provider_kind: 'anthropic-api-key', endpoint: 'https://anthropic.example', enabled: true, revision: 1, credential_state: null, verified_at: null },
]

function editorFetch(accountsBody: unknown, putBody: Route = { body: { model_id: 'public-model', revision: 2, items: [{ upstream_id: 'up-1', upstream_model: 'provider-model', priority: 0, weight: 1, max_concurrency: 1 }] } }) {
  return vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input)
    if (url.endsWith('/models/public-model/accounts') && init?.method === 'PUT') return response(putBody)
    if (url.endsWith('/models/public-model/accounts')) return response({ body: accountsBody })
    if (url.endsWith('/upstreams')) return response({ body: { items: upstreams } })
    if (url.endsWith('/channels')) return response({ body: { items: [{ id: 'channel-1', name: '主渠道', group_id: 'group-1', revision: 1 }] } })
    if (url.endsWith('/account-groups')) return response({ body: { items: [{ id: 'group-1', name: '生产组', revision: 1 }] } })
    throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
  })
}

describe('account pool configuration', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('creates and renames groups and creates a channel with CSRF protection', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/account-groups') && !init?.method) return response({ body: { items: [] } })
      if (url.endsWith('/channels') && !init?.method) return response({ body: { items: [] } })
      if (url.endsWith('/account-groups') && init?.method === 'POST') return response({ body: { id: 'group-1', name: '生产组', revision: 1 } })
      if (url.endsWith('/account-groups/group-1') && init?.method === 'PUT') return response({ body: { id: 'group-1', name: '核心组', revision: 2 } })
      if (url.endsWith('/channels') && init?.method === 'POST') return response({ body: { id: 'channel-1', name: '主渠道', group_id: 'group-1', revision: 1 } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AccountPoolDirectory csrf="csrf-token" onClose={() => undefined} />)

    await screen.findByText('还没有分组。渠道也可以不归属任何分组。')
    await userEvent.type(screen.getByLabelText('新分组名称'), '生产组')
    await userEvent.click(screen.getByRole('button', { name: '创建分组' }))
    const rename = await screen.findByLabelText('分组名称')
    await userEvent.clear(rename)
    await userEvent.type(rename, '核心组')
    await userEvent.click(screen.getByRole('button', { name: '重命名' }))
    await waitFor(() => expect(rename).toHaveValue('核心组'))

    await userEvent.type(screen.getByLabelText('新渠道名称'), '主渠道')
    await userEvent.selectOptions(screen.getByLabelText('所属分组（可选）'), 'group-1')
    await userEvent.click(screen.getByRole('button', { name: '创建渠道' }))
    expect(await screen.findByText('主渠道')).toBeInTheDocument()

    const mutations = fetchMock.mock.calls.filter(([, init]) => Boolean((init as RequestInit | undefined)?.method))
    expect(mutations).toHaveLength(3)
    for (const [, init] of mutations) expect(new Headers((init as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(JSON.parse(String((mutations[1][1] as RequestInit).body))).toEqual({ expected_revision: 1, name: '核心组' })
    expect(JSON.parse(String((mutations[2][1] as RequestInit).body))).toEqual({ name: '主渠道', group_id: 'group-1' })
  })

  it('does not blindly retry an uncertain group create before lists are reloaded', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (!init?.method && (url.endsWith('/account-groups') || url.endsWith('/channels'))) return response({ body: { items: [] } })
      if (url.endsWith('/account-groups') && init?.method === 'POST') return Promise.reject(new TypeError('connection lost'))
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AccountPoolDirectory csrf="csrf" onClose={() => undefined} />)
    await screen.findByText('还没有分组。渠道也可以不归属任何分组。')
    await userEvent.type(screen.getByLabelText('新分组名称'), '可能已创建')
    await userEvent.click(screen.getByRole('button', { name: '创建分组' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('无法确认本次操作是否成功')
    expect(screen.getByRole('button', { name: '创建分组' })).toBeDisabled()
    expect(fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith('/account-groups') && (init as RequestInit | undefined)?.method === 'POST')).toHaveLength(1)
    expect(screen.getByRole('button', { name: '重新加载列表' })).toBeInTheDocument()
  })

  it('keeps revision zero read-only until explicit save and sends the exact CAS revision', async () => {
    const accounts = { model_id: 'public-model', revision: 0, items: [{ upstream_id: 'up-1', upstream_model: 'provider-model', priority: 0, weight: 1, max_concurrency: 1 }] }
    const fetchMock = editorFetch(accounts)
    vi.stubGlobal('fetch', fetchMock)
    render(<ModelAccountPoolEditor model={model()} csrf="csrf-token" routingEnabled={false} onClose={() => undefined} />)

    expect(await screen.findByText('当前为兼容默认路由')).toBeInTheDocument()
    expect(screen.getByText(/配置可以保存，但当前请求仍使用原有单账号路由/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([, init]) => (init as RequestInit | undefined)?.method === 'PUT')).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: '保存账号池' }))
    expect(await screen.findByText('账号池已保存。')).toBeInTheDocument()

    const put = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/accounts') && (init as RequestInit | undefined)?.method === 'PUT')
    expect(JSON.parse(String((put?.[1] as RequestInit).body))).toEqual({ expected_revision: 0, items: accounts.items })
    expect(new Headers((put?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/employees'))).toBe(false)
  })

  it('allows a saved disabled account to be removed but never offers disabled or different-provider accounts to add', async () => {
    const fetchMock = editorFetch({ model_id: 'public-model', revision: 4, items: [
      { upstream_id: 'up-1', upstream_model: 'provider-model', priority: 0, weight: 1, max_concurrency: 3 },
      { upstream_id: 'up-disabled', upstream_model: 'provider-model', priority: 1, weight: 1, max_concurrency: 2 },
    ] })
    vi.stubGlobal('fetch', fetchMock)
    render(<ModelAccountPoolEditor model={model()} csrf="csrf" routingEnabled onClose={() => undefined} />)

    expect(await screen.findByText('已停用，可移除')).toBeInTheDocument()
    const addSelect = screen.getByLabelText('添加同服务商账号')
    expect(addSelect).toHaveTextContent('备用账号')
    expect(addSelect).not.toHaveTextContent('停用账号')
    expect(addSelect).not.toHaveTextContent('其他服务商')
    expect(screen.getByText(/全局并发上限按这些账号池里最小的/)).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: '移除' })[1]).toBeEnabled()
  })

  it('preserves edits after a CAS conflict and requires an explicit reload', async () => {
    const accounts = { model_id: 'public-model', revision: 8, items: [{ upstream_id: 'up-1', upstream_model: 'provider-model', priority: 0, weight: 1, max_concurrency: 1 }] }
    const fetchMock = editorFetch(accounts, { status: 409, body: { error: { code: 'revision_conflict', message: 'server detail' } } })
    vi.stubGlobal('fetch', fetchMock)
    render(<ModelAccountPoolEditor model={model()} csrf="csrf" routingEnabled onClose={() => undefined} />)

    const mapping = await screen.findByLabelText('账号 1 上游模型')
    await userEvent.clear(mapping)
    await userEvent.type(mapping, 'edited-model')
    await userEvent.click(screen.getByRole('button', { name: '保存账号池' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('当前编辑已保留')
    expect(mapping).toHaveValue('edited-model')
    expect(screen.getByRole('button', { name: '保存账号池' })).toBeDisabled()
    expect(screen.getByRole('button', { name: '重新加载账号池' })).toBeInTheDocument()
  })

  it('ignores a stale model response after switching models', async () => {
    let resolveFirst!: (value: Response) => void
    const firstAccounts = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/models/model-a/accounts')) return firstAccounts
      if (url.endsWith('/models/model-b/accounts')) return response({ body: { model_id: 'model-b', revision: 2, items: [{ upstream_id: 'up-1', upstream_model: 'model-b-mapping', priority: 0, weight: 1, max_concurrency: 1 }] } })
      if (url.endsWith('/upstreams')) return response({ body: { items: upstreams } })
      if (url.endsWith('/channels') || url.endsWith('/account-groups')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ModelAccountPoolEditor model={model('model-a')} csrf="csrf" routingEnabled onClose={() => undefined} />)
    view.rerender(<ModelAccountPoolEditor model={model('model-b')} csrf="csrf" routingEnabled onClose={() => undefined} />)
    expect(await screen.findByLabelText('账号 1 上游模型')).toHaveValue('model-b-mapping')

    await act(async () => { resolveFirst(await response({ body: { model_id: 'model-a', revision: 1, items: [{ upstream_id: 'up-1', upstream_model: 'stale-mapping', priority: 0, weight: 1, max_concurrency: 1 }] } })) })
    await waitFor(() => expect(screen.getByLabelText('账号 1 上游模型')).toHaveValue('model-b-mapping'))
  })

  it('shows account-pool controls only when the configuration feature is enabled', async () => {
    let enabled = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/models')) return response({ body: { items: [model()] } })
      if (url.endsWith('/system/status')) return response({ body: { version: 'test', ready: true, storage: 'memory', limitations: [], features: { account_pool_configuration: enabled, account_pool_routing: false } } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const view = render(<ModelsPage csrf="csrf" />)
    await screen.findByText('public-model')
    expect(screen.queryByRole('button', { name: '分组与渠道' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '编辑账号池' })).not.toBeInTheDocument()

    cleanup()
    enabled = true
    render(<ModelsPage csrf="csrf" />)
    expect(await screen.findByRole('button', { name: '分组与渠道' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '编辑账号池' })).toBeInTheDocument()
    expect(screen.getByText(/保存内容暂不参与请求调度/)).toBeInTheDocument()
    view.unmount()
  })
})
