import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { GovernancePage } from '../pages/GovernancePage'

type Reply = { status?: number; body?: unknown }
const response = ({ status = 200, body = {} }: Reply) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const at = '2026-09-23T00:00:00.000000000Z'
const settings = (enabled = false, revision = 1, budget_enabled?: boolean) => ({ enabled, ...(budget_enabled === undefined ? {} : { budget_enabled }), revision, updated_at: at })
const employee = (id: string, name: string) => ({ id, name, status: 'active', model_mode: 'all', models: [], revision: 1 })
const key = (id: string, name: string) => ({ id, name, expires_at: null, revoked_at: null })
const group = (id: string, revision = 1) => ({ id, name: `组 ${id}`, employee_ids: [], revision, created_at: at, updated_at: at })
const policy = (overrides: Record<string, unknown> = {}) => ({
  id: 'policy-1', scope_kind: 'key', scope_id: 'key-1', enabled: true,
  hard: { rpm: 10, concurrency: null }, budget: { tpm: null, cost_micro: null, currency: null, window: null, unknown_mode: 'shadow' }, shadow: { tpm: null, cost_micro: null, currency: null, window: null },
  revision: 4, created_at: at, updated_at: at, ...overrides,
})
const receipt = (operation_id: string, resource_kind: string, resource_id: string, revision: number) => ({ operation_id, resource_kind, resource_id, revision, created_at: at })

function fixture(options: { groups?: unknown[]; policies?: unknown[]; groupCursor?: string | null; policyCursor?: string | null; employees?: ReturnType<typeof employee>[]; keys?: Record<string, ReturnType<typeof key>[]> } = {}) {
  const employees = options.employees ?? []
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input)
    if (url.endsWith('/governance/settings')) return response({ body: settings() })
    if (url.includes('/governance/groups?')) return response({ body: { items: options.groups ?? [], next_cursor: options.groupCursor ?? null } })
    if (url.includes('/governance/policies?')) return response({ body: { items: options.policies ?? [], next_cursor: options.policyCursor ?? null } })
    if (url.endsWith('/employees')) return response({ body: { items: employees } })
    const match = url.match(/\/employees\/([^/]+)\/keys$/)
    if (match) return response({ body: { items: options.keys?.[match[1]] ?? [] } })
    throw new Error(`Unexpected request: ${url}`)
  })
}

describe('governance management page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('shows the default-off boundary, keeps governance groups separate, and paginates by after_id', async () => {
    const fetchMock = fixture({ groups: [group('g-1')], groupCursor: 'g-1' })
    fetchMock.mockImplementation((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('after_id=g-1')) return response({ body: { items: [group('g-2')], next_cursor: null } })
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?')) return response({ body: { items: [group('g-1')], next_cursor: 'g-1' } })
      if (url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    expect(await screen.findByText('默认关闭 / 当前关闭')).toBeInTheDocument()
    expect(screen.getByText('预算默认关闭 / 当前关闭')).toBeInTheDocument()
    expect(screen.getByText(/Token 与成本硬预算另有独立开关/)).toBeInTheDocument()
    expect(screen.getByText(/当前只有 OpenAI 官方/)).toBeInTheDocument()
    expect(screen.getByText(/治理组与上游账号组彼此独立/)).toBeInTheDocument()
    expect(screen.queryByText(/^余额充足$|^低于阈值$|^将会拦截$/)).not.toBeInTheDocument()
    const more = screen.getByRole('button', { name: '加载更多治理组' })
    fireEvent.click(more); fireEvent.click(more)
    expect(await screen.findByText('组 g-2')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([input]) => String(input).includes('limit=50&after_id=g-1'))).toHaveLength(1)
  })

  it('keeps the current page after a pagination failure and retries without duplicates', async () => {
    let attempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.includes('after_id=g-1')) {
        attempts += 1
        if (attempts === 1) return Promise.reject(new TypeError('network down'))
        return response({ body: { items: [group('g-1'), group('g-2')], next_cursor: null } })
      }
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?')) return response({ body: { items: [group('g-1')], next_cursor: 'g-1' } })
      if (url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('组 g-1')
    await userEvent.click(screen.getByRole('button', { name: '加载更多治理组' }))
    expect(await screen.findByText('无法读取更多治理组；现有列表保持不变。')).toBeInTheDocument()
    expect(screen.getAllByText('组 g-1')).toHaveLength(1)
    await userEvent.click(screen.getByRole('button', { name: '重试加载治理组' }))
    expect(await screen.findByText('组 g-2')).toBeInTheDocument()
    expect(screen.getAllByText('组 g-1')).toHaveLength(1)
  })

  it('freezes an unknown settings write and retries only the original operation and payload', async () => {
    let settingReads = 0
    let puts = 0
    const bodies: string[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings') && init?.method === 'PUT') {
        puts += 1; bodies.push(String(init.body)); const operationID = JSON.parse(String(init.body)).operation_id
        if (puts === 1) return response({ status: 503, body: { error: { code: 'storage_unavailable' } } })
        return response({ body: receipt(operationID, 'settings', 'singleton', 2) })
      }
      if (url.includes('/governance/operations/')) return response({ status: 404, body: { error: { code: 'not_found' } } })
      if (url.endsWith('/governance/settings')) { settingReads += 1; return response({ body: settingReads === 1 ? settings() : settings(true, 2) }) }
      if (url.includes('/governance/groups?') || url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('默认关闭 / 当前关闭')
    await userEvent.click(screen.getByRole('button', { name: '启用治理' }))
    expect(await screen.findByText('写入结果尚未确认')).toBeInTheDocument()
    const operationID = JSON.parse(bodies[0]).operation_id
    expect(screen.getByText(operationID)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '启用治理' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '用原操作重试' }))
    expect(await screen.findByText('已启用')).toBeInTheDocument()
    expect(bodies).toHaveLength(2)
    expect(bodies[1]).toBe(bodies[0])
    expect(JSON.parse(bodies[0])).toEqual({ operation_id: operationID, expected_revision: 1, enabled: true })
  })

  it('updates the independently default-off budget switch with the shared settings revision', async () => {
    let reads = 0
    let body: Record<string, unknown> | null = null
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings') && init?.method === 'PUT') {
        body = JSON.parse(String(init.body))
        return response({ body: receipt(String(body!.operation_id), 'settings', 'singleton', 8) })
      }
      if (url.endsWith('/governance/settings')) { reads += 1; return response({ body: reads === 1 ? settings(true, 7) : settings(true, 8, true) }) }
      if (url.includes('/governance/groups?') || url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    expect(await screen.findByText('预算默认关闭 / 当前关闭')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '启用预算限制' }))
    expect(await screen.findByText('预算已启用')).toBeInTheDocument()
    expect(body).toMatchObject({ expected_revision: 7, enabled: true, budget_enabled: true })
    expect(body!.operation_id).toMatch(/^[0-9a-f-]{36}$/i)
  })

  it('treats a legacy policy without budget as an empty shadow-mode budget', async () => {
    const person = employee('employee-1', 'Alice')
    const accessKey = key('key-1', 'Laptop')
    const legacyPolicy = policy({ budget: undefined })
    vi.stubGlobal('fetch', fixture({ policies: [legacyPolicy], employees: [person], keys: { 'employee-1': [accessKey] } }))
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('Key · Alice · Laptop')
    await userEvent.click(screen.getByRole('button', { name: '编辑策略' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).getByLabelText('TPM 硬预算')).toHaveValue('')
    expect(within(dialog).getByLabelText('24 小时成本硬预算（micro）')).toHaveValue('')
    expect(within(dialog).getByLabelText('未知上界处理')).toHaveValue('shadow')
  })

  it('creates a group with sorted explicit members in one frozen payload', async () => {
    const people = [employee('employee-b', 'Bob'), employee('employee-a', 'Alice')]
    let postBody: Record<string, unknown> | null = null
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?') || url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: people } })
      if (url.includes('/employees/') && url.endsWith('/keys')) return response({ body: { items: [] } })
      if (url.endsWith('/governance/groups') && init?.method === 'POST') {
        const parsed = JSON.parse(String(init.body)); postBody = parsed; return response({ body: receipt(String(parsed.operation_id), 'group', 'group-new', 1) })
      }
      if (url.endsWith('/governance/groups/group-new')) return response({ body: { ...group('group-new'), name: '研发治理', employee_ids: ['employee-a', 'employee-b'] } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('还没有治理组')
    await userEvent.click(screen.getByRole('button', { name: '新建治理组' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('治理组名称'), '研发治理')
    await userEvent.click(within(dialog).getByRole('checkbox', { name: /Bob/ }))
    await userEvent.click(within(dialog).getByRole('checkbox', { name: /Alice/ }))
    await userEvent.click(within(dialog).getByRole('button', { name: '保存治理组' }))
    await waitFor(() => expect(postBody).not.toBeNull())
    expect(postBody!.employee_ids).toEqual(['employee-a', 'employee-b'])
    expect(postBody!.operation_id).toMatch(/^[0-9a-f-]{36}$/i)
  })

  it('keeps a maximum cost threshold as a decimal string and labels it shadow-only', async () => {
    const person = employee('employee-1', 'Alice')
    const accessKey = key('key-1', 'Laptop')
    let postBody: Record<string, any> | null = null
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?') || url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [person] } })
      if (url.endsWith('/employees/employee-1/keys')) return response({ body: { items: [accessKey] } })
      if (url.endsWith('/governance/policies') && init?.method === 'POST') {
        const parsed = JSON.parse(String(init.body)); postBody = parsed; return response({ body: receipt(String(parsed.operation_id), 'policy', 'policy-new', 1) })
      }
      if (url.endsWith('/governance/policies/policy-new')) return response({ body: policy({ id: 'policy-new', revision: 1, shadow: postBody!.shadow, hard: postBody!.hard }) })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('还没有治理策略')
    await userEvent.click(screen.getByRole('button', { name: '新建策略' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.selectOptions(within(dialog).getByLabelText('作用范围'), 'key')
    await userEvent.selectOptions(within(dialog).getByLabelText('作用对象'), 'key-1')
    await userEvent.type(within(dialog).getByLabelText('RPM 硬限制'), '12')
    await userEvent.type(within(dialog).getByLabelText('成本 shadow 阈值（micro）'), '9223372036854775807')
    await userEvent.type(within(dialog).getByLabelText('成本币种'), 'USD')
    expect(within(dialog).getByText(/用量观测只解释历史阈值/)).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: '保存策略' }))
    await waitFor(() => expect(postBody).not.toBeNull())
    expect(postBody!.scope_kind).toBe('key')
    expect(postBody!.scope_id).toBe('key-1')
    expect(postBody!.hard).toEqual({ rpm: 12, concurrency: null })
    expect(postBody!.shadow).toEqual({ tpm: null, cost_micro: '9223372036854775807', currency: 'USD', window: 'rolling_24h' })
  })

  it('freezes a hard-only budget policy and retries the same decimal payload after an unknown result', async () => {
    const person = employee('employee-1', 'Alice')
    const bodies: string[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings')) return response({ body: settings(true, 3, true) })
      if (url.includes('/governance/groups?') || url.includes('/governance/policies?')) return response({ body: { items: [], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [person] } })
      if (url.endsWith('/employees/employee-1/keys')) return response({ body: { items: [] } })
      if (url.endsWith('/governance/policies') && init?.method === 'POST') {
        bodies.push(String(init.body))
        if (bodies.length === 1) return response({ status: 503, body: { error: { code: 'storage_unavailable' } } })
        const parsed = JSON.parse(bodies[1])
        return response({ body: receipt(String(parsed.operation_id), 'policy', 'policy-budget', 1) })
      }
      if (url.includes('/governance/operations/')) return response({ status: 404, body: { error: { code: 'not_found' } } })
      if (url.endsWith('/governance/policies/policy-budget')) {
        const parsed = JSON.parse(bodies[1])
        return response({ body: policy({ id: 'policy-budget', scope_kind: 'employee', scope_id: 'employee-1', revision: 1, hard: parsed.hard, budget: parsed.budget, shadow: parsed.shadow }) })
      }
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('还没有治理策略')
    await userEvent.click(screen.getByRole('button', { name: '新建策略' }))
    const dialog = screen.getByRole('dialog')
    const budgetTPM = within(dialog).getByLabelText('TPM 硬预算')
    const budgetCost = within(dialog).getByLabelText('24 小时成本硬预算（micro）')
    await userEvent.type(budgetTPM, '9007199254740991')
    await userEvent.type(budgetCost, '9223372036854775807')
    await userEvent.type(within(dialog).getByLabelText('硬预算币种'), 'USD')
    await userEvent.selectOptions(within(dialog).getByLabelText('未知上界处理'), 'deny_unknown')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存策略' }))
    expect(await within(dialog).findByText('写入结果尚未确认')).toBeInTheDocument()
    expect(budgetTPM).toHaveValue('9007199254740991')
    expect(budgetCost).toHaveValue('9223372036854775807')
    const first = JSON.parse(bodies[0])
    expect(first.hard).toEqual({ rpm: null, concurrency: null })
    expect(first.budget).toEqual({ tpm: 9007199254740991, cost_micro: '9223372036854775807', currency: 'USD', window: 'rolling_24h', unknown_mode: 'deny_unknown' })
    expect(first.shadow).toEqual({ tpm: null, cost_micro: null, currency: null, window: null })
    await userEvent.click(within(dialog).getByRole('button', { name: '用原操作重试' }))
    await waitFor(() => expect(bodies).toHaveLength(2))
    expect(bodies[1]).toBe(bodies[0])
  })

  it('re-reads a policy CAS conflict and requires confirmation before a new operation', async () => {
    const person = employee('employee-1', 'Alice')
    const accessKey = key('key-1', 'Laptop')
    let puts = 0
    const bodies: Record<string, unknown>[] = []
    const latest = policy({ revision: 5, hard: { rpm: 20, concurrency: null } })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?')) return response({ body: { items: [], next_cursor: null } })
      if (url.includes('/governance/policies?')) return response({ body: { items: [puts > 1 ? policy({ revision: 6, hard: { rpm: 21, concurrency: null } }) : policy()], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [person] } })
      if (url.endsWith('/employees/employee-1/keys')) return response({ body: { items: [accessKey] } })
      if (url.endsWith('/governance/policies/policy-1') && init?.method === 'PUT') {
        puts += 1; bodies.push(JSON.parse(String(init.body)))
        if (puts === 1) return response({ status: 409, body: { error: { code: 'revision_conflict' } } })
        return response({ body: receipt(String(bodies[1].operation_id), 'policy', 'policy-1', 6) })
      }
      if (url.endsWith('/governance/policies/policy-1')) return response({ body: puts > 1 ? policy({ revision: 6, hard: { rpm: 21, concurrency: null } }) : latest })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('Key · Alice · Laptop')
    await userEvent.click(screen.getByRole('button', { name: '编辑策略' }))
    const dialog = screen.getByRole('dialog')
    const rpm = within(dialog).getByLabelText('RPM 硬限制')
    await userEvent.clear(rpm); await userEvent.type(rpm, '11')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存策略' }))
    expect(await within(dialog).findByText('服务器当前策略 r5')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '保存策略' })).toBeDisabled()
    await userEvent.click(within(dialog).getByRole('button', { name: '按最新状态重新编辑' }))
    expect(rpm).toHaveValue('20')
    await userEvent.clear(rpm); await userEvent.type(rpm, '21')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存策略' }))
    await waitFor(() => expect(puts).toBe(2))
    expect(bodies[0].expected_revision).toBe(4)
    expect(bodies[1].expected_revision).toBe(5)
    expect(bodies[1]).not.toHaveProperty('scope_kind')
    expect(bodies[1].operation_id).not.toBe(bodies[0].operation_id)
  })

  it('freezes a conflicted policy when the latest state cannot be read', async () => {
    const person = employee('employee-1', 'Alice')
    const accessKey = key('key-1', 'Laptop')
    let detailReads = 0
    const latest = policy({ revision: 5, hard: { rpm: 20, concurrency: null } })
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/governance/settings')) return response({ body: settings() })
      if (url.includes('/governance/groups?')) return response({ body: { items: [], next_cursor: null } })
      if (url.includes('/governance/policies?')) return response({ body: { items: [policy()], next_cursor: null } })
      if (url.endsWith('/employees')) return response({ body: { items: [person] } })
      if (url.endsWith('/employees/employee-1/keys')) return response({ body: { items: [accessKey] } })
      if (url.endsWith('/governance/policies/policy-1') && init?.method === 'PUT') return response({ status: 409, body: { error: { code: 'revision_conflict' } } })
      if (url.endsWith('/governance/policies/policy-1')) {
        detailReads += 1
        if (detailReads === 1) return Promise.reject(new TypeError('network down'))
        return response({ body: latest })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GovernancePage csrf="csrf" />)
    await screen.findByText('Key · Alice · Laptop')
    await userEvent.click(screen.getByRole('button', { name: '编辑策略' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存策略' }))
    expect(await within(dialog).findByText('最新状态尚未确认')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '保存策略' })).toBeDisabled()
    await userEvent.click(within(dialog).getByRole('button', { name: '重新读取最新状态' }))
    expect(await within(dialog).findByText('服务器当前策略 r5')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '保存策略' })).toBeDisabled()
  })

  it('fails closed when the governance management API is unavailable', async () => {
    vi.stubGlobal('fetch', vi.fn(() => response({ status: 404, body: { error: { code: 'not_found' } } })))
    render(<GovernancePage csrf="csrf" />)
    expect(await screen.findByText('当前服务版本不支持请求治理管理，请升级服务后重试。')).toBeInTheDocument()
    expect(screen.queryByText('默认关闭 / 当前关闭')).not.toBeInTheDocument()
  })
})
