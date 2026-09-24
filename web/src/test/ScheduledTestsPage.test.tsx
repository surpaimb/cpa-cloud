// Independently authored browser-component acceptance for the scheduled-test contract.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ScheduledTestsPage } from '../pages/ScheduledTestsPage'

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const upstream = { id: 'ups-1', name: '合成目录账号', provider_kind: 'openai-compatible', endpoint: 'http://127.0.0.1:9', enabled: true, revision: 3, credential_state: null, verified_at: null }
const plan = {
  id: 'sch-1', name: '每五分钟目录检查', upstream_id: upstream.id, scope: 'catalog', interval_seconds: 300,
  enabled: true, revision: 2, next_run_at: '2026-09-24T10:00:00Z', created_at: '2026-09-24T09:00:00Z', updated_at: '2026-09-24T09:00:00Z',
  latest_result: { plan_revision: 2, operation_id: '31000000-0000-4000-8000-000000000001', scope: 'catalog', state: 'completed', result_code: 'catalog_ok', started_at: '2026-09-24T09:55:00Z', finished_at: '2026-09-24T09:55:01Z', latency_ms: 1000 },
}

describe('scheduled tests page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('degrades safely when the server omits both capabilities', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ version: 'old', ready: true, storage: 'sqlite-wal', limitations: [], features: {} })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ScheduledTestsPage csrf="csrf" />)
    expect(await screen.findByText('当前服务不支持定时测试')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '创建计划' })).toBeDisabled()
    expect(fetchMock.mock.calls.some(([input]) => String(input).endsWith('/scheduled-tests'))).toBe(false)
  })

  it('shows the default-off boundary and never labels catalog reachability as generation availability', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { scheduled_tests_configuration: true, scheduled_tests_running: false } })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/scheduled-tests')) return response({ items: [plan] })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<ScheduledTestsPage csrf="csrf" />)
    expect(await screen.findByText('后台运行默认关闭')).toBeInTheDocument()
    expect(screen.getByText('等待全局开关')).toBeInTheDocument()
    expect(screen.getAllByText('模型目录可达')).toHaveLength(2)
    expect(screen.getByText('只读取模型目录，不发送生成请求')).toBeInTheDocument()
    expect(screen.queryByText(/^生成可用$|^生成成功$/)).not.toBeInTheDocument()
  })

  it('creates a disabled credential plan with CSRF and bounded fixed interval', async () => {
    let plans = [plan]
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { scheduled_tests_configuration: true, scheduled_tests_running: true } })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.endsWith('/scheduled-tests') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)); plans = [{ ...plan, ...body, id: 'sch-2', revision: 1, latest_result: null, next_run_at: null }]
        return response(plans[0], 201)
      }
      if (url.endsWith('/scheduled-tests')) return response({ items: plans })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ScheduledTestsPage csrf="csrf-token" />)
    await userEvent.click(await screen.findByRole('button', { name: '创建计划' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('计划名称'), '凭据轮询')
    await userEvent.clear(within(dialog).getByLabelText('固定间隔（秒）'))
    await userEvent.type(within(dialog).getByLabelText('固定间隔（秒）'), '600')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存计划' }))
    expect(await screen.findByText('凭据轮询')).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/scheduled-tests') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ name: '凭据轮询', upstream_id: 'ups-1', scope: 'local_credential', interval_seconds: 600, enabled: false })
    expect(new Headers((call?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf-token')
  })

  it('loads bounded history and uses an explicit irreversible archive confirmation', async () => {
    let archived = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { scheduled_tests_configuration: true, scheduled_tests_running: true } })
      if (url.endsWith('/upstreams')) return response({ items: [upstream] })
      if (url.includes('/scheduled-tests/sch-1/runs?')) return response({ items: [plan.latest_result], next_cursor: null })
      if (url.endsWith('/scheduled-tests/sch-1') && init?.method === 'DELETE') { archived = true; return response({ result: 'archived', id: 'sch-1', revision: 3 }) }
      if (url.endsWith('/scheduled-tests')) return response({ items: archived ? [] : [plan] })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ScheduledTestsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '历史' }))
    expect(await screen.findByText(plan.latest_result.operation_id)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '关闭' }))
    await userEvent.click(screen.getByRole('button', { name: '归档' }))
    expect(screen.getByText('归档后不能恢复，计划将立即停用；已有运行历史仍保留。')).toBeInTheDocument()
    expect(screen.getByText('这不是临时停用。若只想暂停执行，请返回列表使用“停用”。')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '确认永久归档' }))
    await waitFor(() => expect(screen.queryByText('每五分钟目录检查')).not.toBeInTheDocument())
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/scheduled-tests/sch-1') && (init as RequestInit)?.method === 'DELETE')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ expected_revision: 2 })
  })
})
