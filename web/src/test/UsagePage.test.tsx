import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { UsagePage } from '../pages/UsagePage'

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const emptySummary = {
  from: '2026-09-22T00:00:00Z', to: '2026-09-23T00:00:00Z',
  requests: { total: '0', pending: '0', succeeded: '0', failed: '0', cancelled: '0', interrupted: '0' },
  attempts: [],
}
const emptyPage = { items: [], next_cursor: null, from: emptySummary.from, to: emptySummary.to }
const upstream = { id: 'up-1', name: '主账号', provider_kind: 'openai-compatible', endpoint: 'https://api.example.test/v1', enabled: true, revision: 1, credential_state: null, verified_at: null }

function commonRoute(url: string) {
  if (url.endsWith('/employees')) return json({ items: [{ id: 'emp-1', name: '张三', status: 'active', model_mode: 'all', models: [], revision: 1 }] })
  if (url.endsWith('/models')) return json({ items: [{ id: 'public-model', upstream_id: 'up-1', upstream_model: 'actual-model', enabled: true }] })
  if (url.endsWith('/upstreams')) return json({ items: [upstream] })
  return null
}

describe('usage and pricing page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('opens from the administrator navigation', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return json({ username: 'admin', csrf_token: 'csrf' })
      const common = commonRoute(url)
      if (common) return common
      if (url.includes('/usage/summary?')) return json(emptySummary)
      if (url.includes('/usage/requests?')) return json(emptyPage)
      if (url.endsWith('/upstreams/up-1/prices')) return json({ items: [] })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: '用量与成本' }))
    expect(await screen.findByRole('heading', { name: '用量与成本' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '价格管理' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '最近 24 小时' })).toBeInTheDocument()
  })

  it('recovers the initial load, preserves decimal strings, pins pagination, and opens attempt details', async () => {
    let summaryCalls = 0
    let requestCalls = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      const common = commonRoute(url)
      if (common) return common
      if (url.includes('/usage/summary?')) {
        summaryCalls += 1
        if (summaryCalls === 1) return json({ error: { code: 'storage_unavailable', message: 'Unavailable.' } }, 503)
        return json({
          ...emptySummary,
          requests: { total: '9007199254740991001', pending: '0', succeeded: '1', failed: '0', cancelled: '0', interrupted: '0' },
          attempts: [{
            currency: 'USD', total: '1', pending: '0', succeeded: '1', failed: '0', cancelled: '0', interrupted: '0',
            known_cost_micro: '1234567890123456789', unknown_cost_attempts: '2',
            input_tokens: { known_total: '9007199254740991001', unknown_attempts: '0' },
            output_tokens: { known_total: '2', unknown_attempts: '0' },
            cache_read_tokens: { known_total: '0', unknown_attempts: '1' },
            cache_write_tokens: { known_total: '0', unknown_attempts: '1' },
          }],
        })
      }
      if (url.includes('/usage/requests/req-1/attempts')) return json({ items: [{
        id: 'req-1:1', request_id: 'req-1', account_id: 'up-1', provider: 'openai-compatible', dispatch: 'selected', status: 'succeeded',
        started_at: '2026-09-22T12:00:00Z', finished_at: '2026-09-22T12:00:01Z', price_version: 'price-v1', currency: 'USD',
        input_tokens: '9007199254740991001', output_tokens: '2', cache_read_tokens: '0', cache_write_tokens: '0', cost_micro: '1234567890123456789',
      }] })
      if (url.includes('/usage/requests?')) {
        requestCalls += 1
        if (requestCalls >= 3) return json({ ...emptyPage, from: '2026-09-22T00:00:00Z', to: '2026-09-23T00:00:00Z' })
        return json({
          from: '2026-09-22T00:00:00Z', to: '2026-09-23T00:00:00Z', next_cursor: 'cursor-fixed',
          items: [{ id: 'req-1', employee_id: 'emp-1', key_id: 'key-1', model_id: 'public-model', provider: 'openai-compatible', status: 'succeeded', started_at: '2026-09-22T12:00:00Z', finished_at: '2026-09-22T12:00:01Z', attempt_count: '1' }],
        })
      }
      if (url.endsWith('/upstreams/up-1/prices')) return json({ items: [] })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UsagePage csrf="csrf" />)

    expect(await screen.findByRole('alert')).toHaveTextContent('Unavailable.')
    await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect((await screen.findAllByText('9,007,199,254,740,991,001')).length).toBeGreaterThan(0)
    expect(screen.getByText('1,234,567,890,123.456789 USD')).toBeInTheDocument()
    expect(screen.getByText('2 次成本未知')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '查看尝试' }))
    const dialog = await screen.findByRole('dialog')
    expect(await within(dialog).findByText('price-v1')).toBeInTheDocument()
    expect(within(dialog).getByText('9,007,199,254,740,991,001')).toBeInTheDocument()
    await userEvent.click(within(dialog).getAllByRole('button', { name: '关闭' })[1])

    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(screen.getByText('第 2 页')).toBeInTheDocument())
    const pagedURL = fetchMock.mock.calls.map(([url]) => String(url)).find((url) => url.includes('cursor=cursor-fixed'))
    expect(pagedURL).toContain('from=2026-09-22T00%3A00%3A00Z')
    expect(pagedURL).toContain('to=2026-09-23T00%3A00%3A00Z')
  })

  it('keeps the original operation and payload when save and reload results are unknown', async () => {
    const postBodies: string[] = []
    let priceReads = 0
    let priceWrites = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      const common = commonRoute(url)
      if (common) return common
      if (url.includes('/usage/summary?')) return json(emptySummary)
      if (url.includes('/usage/requests?')) return json(emptyPage)
      if (url.endsWith('/upstreams/up-1/prices') && init?.method === 'POST') {
        priceWrites += 1
        postBodies.push(String(init.body))
        if (priceWrites === 1) return Promise.reject(new TypeError('network lost'))
        return json({ upstream_id: 'up-1', upstream_model: 'actual model', version: 'price-1', revision: 1, created_at: '2026-09-23T00:00:00Z', price: JSON.parse(String(init.body)).price })
      }
      if (url.endsWith('/upstreams/up-1/prices')) {
        priceReads += 1
        if (priceReads === 2) return Promise.reject(new TypeError('still offline'))
        return json({ items: [] })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UsagePage csrf="csrf" />)

    const addPrice = await screen.findByRole('button', { name: '添加模型价格' })
    await waitFor(() => expect(addPrice).toBeEnabled())
    await userEvent.click(addPrice)
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('实际上游模型'), 'actual model')
    await userEvent.clear(within(dialog).getByLabelText('普通输入费率'))
    await userEvent.type(within(dialog).getByLabelText('普通输入费率'), '9007199254740991')
    await userEvent.click(within(dialog).getByRole('button', { name: '追加价格版本' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('未能确认保存结果')

    await userEvent.click(within(dialog).getByRole('button', { name: '重新加载目录' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('原操作编号与提交内容仍保留')
    await userEvent.click(within(dialog).getByRole('button', { name: '用同一操作编号重试' }))
    await waitFor(() => expect(postBodies).toHaveLength(2))
    expect(postBodies[1]).toBe(postBodies[0])
    expect(JSON.parse(postBodies[0]).price.input_per_million_micro).toBe('9007199254740991')
  })

  it('preserves edits on a revision conflict and ignores a stale price list', async () => {
    let resolveFirst!: (response: Response) => void
    const delayed = new Promise<Response>((resolve) => { resolveFirst = resolve })
    const secondUpstream = { ...upstream, id: 'up-2', name: '备用账号' }
    let firstPriceRead = true
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/employees')) return json({ items: [] })
      if (url.endsWith('/models')) return json({ items: [] })
      if (url.endsWith('/upstreams')) return json({ items: [upstream, secondUpstream] })
      if (url.includes('/usage/summary?')) return json(emptySummary)
      if (url.includes('/usage/requests?')) return json(emptyPage)
      if (url.endsWith('/prices') && init?.method === 'POST') return json({ error: { code: 'revision_conflict', message: 'conflict' } }, 409)
      if (url.endsWith('/upstreams/up-1/prices')) {
        if (firstPriceRead) { firstPriceRead = false; return delayed }
        return json({ items: [] })
      }
      if (url.endsWith('/upstreams/up-2/prices')) return json({ items: [{ upstream_id: 'up-2', upstream_model: 'fresh-model', version: 'fresh', revision: 1, created_at: '2026-09-23T00:00:00Z', price: null }] })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UsagePage csrf="csrf" />)

    const selector = await screen.findByLabelText('上游账号')
    await userEvent.selectOptions(selector, 'up-2')
    expect(await screen.findByText('fresh-model')).toBeInTheDocument()
    resolveFirst(new Response(JSON.stringify({ items: [{ upstream_id: 'up-1', upstream_model: 'stale-model', version: 'stale', revision: 1, created_at: '2026-09-23T00:00:00Z', price: null }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await waitFor(() => expect(screen.queryByText('stale-model')).not.toBeInTheDocument())

    await userEvent.click(screen.getByRole('button', { name: '添加模型价格' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('实际上游模型'), 'conflict-model')
    await userEvent.clear(within(dialog).getByLabelText('输出费率'))
    await userEvent.type(within(dialog).getByLabelText('输出费率'), '123456789012345')
    await userEvent.click(within(dialog).getByRole('button', { name: '追加价格版本' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('本次编辑仍保留')
    expect(within(dialog).getByLabelText('输出费率')).toHaveValue('123456789012345')
    expect(within(dialog).getByRole('button', { name: '追加价格版本' })).toBeDisabled()
  })

  it('clears the previous account prices while the newly selected catalog is loading', async () => {
    let resolveSecond!: (response: Response) => void
    const delayedSecond = new Promise<Response>((resolve) => { resolveSecond = resolve })
    const secondUpstream = { ...upstream, id: 'up-2', name: '备用账号' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/employees')) return json({ items: [] })
      if (url.endsWith('/models')) return json({ items: [] })
      if (url.endsWith('/upstreams')) return json({ items: [upstream, secondUpstream] })
      if (url.includes('/usage/summary?')) return json(emptySummary)
      if (url.includes('/usage/requests?')) return json(emptyPage)
      if (url.endsWith('/upstreams/up-1/prices')) return json({ items: [{ upstream_id: 'up-1', upstream_model: 'account-a-model', version: 'a-v1', revision: 7, created_at: '2026-09-23T00:00:00Z', price: null }] })
      if (url.endsWith('/upstreams/up-2/prices')) return delayedSecond
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UsagePage csrf="csrf" />)

    expect(await screen.findByText('account-a-model')).toBeInTheDocument()
    await userEvent.selectOptions(screen.getByLabelText('上游账号'), 'up-2')
    expect(screen.queryByText('account-a-model')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '追加版本' })).not.toBeInTheDocument()
    expect(screen.getByText('正在读取…')).toBeInTheDocument()

    resolveSecond(new Response(JSON.stringify({ items: [{ upstream_id: 'up-2', upstream_model: 'account-b-model', version: 'b-v1', revision: 1, created_at: '2026-09-23T00:00:00Z', price: null }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    expect(await screen.findByText('account-b-model')).toBeInTheDocument()
  })
})
