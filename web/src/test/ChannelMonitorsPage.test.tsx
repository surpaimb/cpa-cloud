// Independently authored component acceptance for docs/channel-monitor-contract.md.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { ChannelMonitorsPage } from '../pages/ChannelMonitorsPage'

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const channel = { id: 'chn-1', name: '研发渠道', revision: 1 }
const upstream = { id: 'ups-1', name: '合成账号', provider_kind: 'openai-compatible', endpoint: 'http://127.0.0.1:9', enabled: true, revision: 1, credential_state: null, verified_at: null }
const model = { id: 'model-1', upstream_id: upstream.id, upstream_model: 'provider-model', enabled: true, revision: 1 }
const route = { upstream_id: upstream.id, upstream_model: 'provider-model', wire_protocol: 'openai-chat', priority: 0, weight: 1, max_concurrency: 1, channel_id: channel.id }
const plan = { id: 'mon-1', name: '目录检查', channel_id: channel.id, model_id: model.id, upstream_id: upstream.id, scope: 'catalog', interval_seconds: 300, enabled: false, revision: 1, next_run_at: null, created_at: '2026-09-30T00:00:00Z', updated_at: '2026-09-30T00:00:00Z', binding_state: 'valid', latest_result: null }

describe('channel monitors page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('hides the navigation entry when an old server omits the capability', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ username: 'admin', csrf_token: 'csrf' })
      if (url.endsWith('/system/status')) return response({ features: {} })
      if (url.endsWith('/employees')) return response({ items: [] })
      throw Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    expect(await screen.findByRole('heading', { name: '员工与 Key' })).toBeInTheDocument()
    await waitFor(() => expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/system/status'))).toBe(true))
    expect(screen.queryByRole('button', { name: '渠道监控' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/channel-monitors'))).toBe(false)
  })

  it('creates a route-bound disabled plan with CSRF and no generation claim', async () => {
    let plans: unknown[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ features: { channel_monitor_configuration: true, channel_monitor_running: false } })
      if (url.endsWith('/channels')) return response({ items: [channel] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/models/model-1/accounts')) return response({ model_id: model.id, revision: 1, items: [route] })
      if (url.endsWith('/channel-monitors') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)); plans = [{ ...plan, ...body }]
        return response(plans[0], 201)
      }
      if (url.endsWith('/channel-monitors')) return response({ items: plans })
      throw Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChannelMonitorsPage csrf="test-csrf" />)
    expect(await screen.findByText('后台运行默认关闭')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '创建计划' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('计划名称'), '凭据检查')
    await userEvent.selectOptions(await within(dialog).findByLabelText('显式账号池路由'), upstream.id)
    await userEvent.click(within(dialog).getByRole('button', { name: '保存计划' }))
    expect(await screen.findByText('凭据检查')).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/channel-monitors') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ name: '凭据检查', channel_id: channel.id, model_id: model.id, upstream_id: upstream.id, scope: 'local_credential', interval_seconds: 300, enabled: false })
    expect(new Headers((call?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('test-csrf')
    expect(screen.queryByText(/^生成可用$/)).not.toBeInTheDocument()
  })

  it('requires explicit confirmation before rebinding a stale plan', async () => {
    const stale = { ...plan, enabled: true, next_run_at: '2026-09-30T01:00:00Z', binding_state: 'stale' }
    let current = stale
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ features: { channel_monitor_configuration: true, channel_monitor_running: true } })
      if (url.endsWith('/channels')) return response({ items: [channel] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/models/model-1/accounts')) return response({ model_id: model.id, revision: 2, items: [route] })
      if (url.endsWith('/channel-monitors/mon-1') && init?.method === 'PATCH') {
        current = { ...current, ...JSON.parse(String(init.body)), revision: 2, binding_state: 'valid' }
        return response(current)
      }
      if (url.endsWith('/channel-monitors')) return response({ items: [current] })
      throw Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChannelMonitorsPage csrf="csrf" />)
    expect(await screen.findByText('需重新绑定')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重新绑定' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByRole('button', { name: '保存计划' })).toBeDisabled()
    await userEvent.click(within(dialog).getByRole('checkbox', { name: /确认重新绑定/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: '保存计划' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([input, init]) => String(input).endsWith('/channel-monitors/mon-1') && (init as RequestInit)?.method === 'PATCH')).toBe(true))
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/channel-monitors/mon-1') && (init as RequestInit)?.method === 'PATCH')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toMatchObject({ expected_revision: 1, rebind: true, channel_id: channel.id, model_id: model.id, upstream_id: upstream.id })
  })

  it('does not probe the summary API when an older service omits its separate capability', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ features: { channel_monitor_configuration: true } })
      if (url.endsWith('/channels')) return response({ items: [channel] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/channel-monitors')) return response({ items: [plan] })
      if (url.includes('/channel-monitors/mon-1/runs')) return response({ items: [], next_cursor: null })
      throw Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChannelMonitorsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '历史' }))
    expect(await screen.findByText('尚无运行记录')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '已保留历史摘要' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/summary'))).toBe(false)
  })

  it('shows only retained facts and a full-window caveat when capability is present', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ features: { channel_monitor_configuration: true, channel_monitor_retained_summary: true } })
      if (url.endsWith('/channels')) return response({ items: [channel] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/channel-monitors')) return response({ items: [plan] })
      if (url.includes('/channel-monitors/mon-1/runs')) return response({ items: [], next_cursor: null })
      if (url.endsWith('/channel-monitors/mon-1/summary')) return response({
        plan_id: plan.id, as_of: '2026-09-30T12:00:00Z', through_sequence: 208,
        retained_completed: 200, retained_window_full: true, running: 1,
        earliest_finished_at: '2026-09-29T12:00:00Z', latest_finished_at: '2026-09-30T11:00:00Z',
        counts: { local_credential: { local_credential_ok: 80 }, catalog: { catalog_ok: 120 } },
      })
      throw Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ChannelMonitorsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '历史' }))
    const region = await screen.findByRole('region', { name: '已保留历史摘要' })
    expect(await within(region).findByText('已保留窗口达到 200 条；不能据此推断更早运行的次数或结果。')).toBeInTheDocument()
    expect(within(region).getByText('凭据检查通过：80')).toBeInTheDocument()
    expect(within(region).getByText('模型目录可达：120')).toBeInTheDocument()
    expect(within(region).queryByText(/成功率|可用率|健康评级/)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/summary'))).toHaveLength(1)
  })
})
