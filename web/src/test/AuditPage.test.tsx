// Independently authored browser-facing tests for the administrator audit overview.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import type { AdminAuditPage } from '../api'
import { AuditPage } from '../pages/AuditPage'

function response(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
}

function auditPage(nextCursor: string | null = null): AdminAuditPage {
  return {
    from: '2026-09-28T08:30:00Z',
    to: '2026-09-29T08:30:00Z',
    snapshot_at: '2026-09-29T08:30:00.123456789Z',
    sources: ['account_pool', 'account_lifecycle', 'governance_management', 'governance_general_budget'],
    items: [
      { source: 'account_pool', event_id: 'pool-2', actor_id: 'admin-1', action: 'account_group.update', target_type: 'account_group', target_id: 'group-1', result: 'succeeded', revision: null, occurred_at: '2026-09-29T08:00:00.1Z' },
      { source: 'account_lifecycle', event_id: 'life-1', actor_id: 'admin-1', action: 'account.archive', target_type: 'account', target_id: 'account-1', result: 'succeeded', revision: null, occurred_at: '2026-09-29T08:00:00.1Z' },
      { source: 'governance_management', event_id: 'gov-1', actor_id: 'admin-1', action: 'policy.update', target_type: 'policy', target_id: 'policy-1', result: 'succeeded', revision: 7, occurred_at: '2026-09-29T07:00:00.123456789Z' },
      { source: 'governance_general_budget', event_id: 'budget-1', actor_id: 'admin-1', action: 'budget.create', target_type: 'budget', target_id: 'budget-1', result: 'succeeded', revision: 1, occurred_at: '2026-09-29T06:00:00Z' },
    ],
    next_cursor: nextCursor,
  }
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('AuditPage', () => {
  it('renders all four sources, applies exact filters, and sends only the cursor on continuation', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (url.includes('cursor=cursor_one')) return response({ ...auditPage(), items: [], next_cursor: null })
      return response(auditPage('cursor_one'))
    }))
    render(<AuditPage />)

    expect(await screen.findByText('account_group.update')).toBeInTheDocument()
    expect(screen.getAllByText('账号池')).toHaveLength(2)
    expect(screen.getAllByText('账号生命周期')).toHaveLength(2)
    expect(screen.getAllByText('治理管理')).toHaveLength(2)
    expect(screen.getAllByText('通用预算')).toHaveLength(2)
    expect(screen.getByText('r7')).toBeInTheDocument()
    expect(screen.getAllByText('无 revision')).toHaveLength(2)
    expect(screen.getByText(/不是完整历史/)).toBeInTheDocument()
    expect(calls[0].endsWith('/admin/api/v1/audit/events?limit=20')).toBe(true)

    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText('当前窗口没有匹配事实')).toBeInTheDocument()
    expect(calls[1].endsWith('/admin/api/v1/audit/events?cursor=cursor_one')).toBe(true)
    expect(calls[1]).not.toContain('limit=')

    await userEvent.click(screen.getByRole('button', { name: '上一页' }))
    expect(await screen.findByText('account_group.update')).toBeInTheDocument()
    expect(calls).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText('当前窗口没有匹配事实')).toBeInTheDocument()
    expect(calls).toHaveLength(2)

    await userEvent.type(screen.getByLabelText('操作人（精确）'), 'admin-1')
    await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
    await waitFor(() => expect(calls).toHaveLength(3))
    expect(calls[2]).toContain('actor_id=admin-1')
    expect(calls[2]).not.toContain('cursor=')
    expect(screen.getByText('第 1 页')).toBeInTheDocument()
  })

  it('fails the whole page closed for a malformed claimed-new response', async () => {
    const malformed = auditPage()
    malformed.items[2] = { ...malformed.items[2], source: 'unknown_source' as never, target_id: 'must-not-render' }
    vi.stubGlobal('fetch', vi.fn(() => response(malformed)))
    render(<AuditPage />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('审计响应格式无效，未显示任何结果。')
    expect(screen.queryByText('must-not-render')).not.toBeInTheDocument()
    expect(screen.queryByText('account_group.update')).not.toBeInTheDocument()
  })

  it('rejects unordered and duplicate response items without showing a partial list', async () => {
    const malformed = auditPage()
    malformed.items = [malformed.items[1], malformed.items[0], { ...malformed.items[0] }]
    vi.stubGlobal('fetch', vi.fn(() => response(malformed)))
    render(<AuditPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('审计响应格式无效')
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
  })

  it.each([
    ['missing field', (page: Record<string, any>) => { delete page.items[0].actor_id }],
    ['unknown result', (page: Record<string, any>) => { page.items[0].result = 'failed' }],
    ['non-UTC time', (page: Record<string, any>) => { page.items[0].occurred_at = '2026-09-29T16:00:00+08:00' }],
    ['unsafe revision', (page: Record<string, any>) => { page.items[2].revision = 9_007_199_254_740_992 }],
    ['malformed cursor', (page: Record<string, any>) => { page.next_cursor = 'bad cursor' }],
  ])('fails closed for %s', async (_name, mutate) => {
    const malformed = structuredClone(auditPage()) as unknown as Record<string, any>
    mutate(malformed)
    vi.stubGlobal('fetch', vi.fn(() => response(malformed)))
    render(<AuditPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('审计响应格式无效，未显示任何结果。')
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
  })

  it('retries the newly submitted filter after its first read fails', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (calls.length === 1) return response(auditPage())
      return response({ error: { code: 'storage_unavailable', message: '审计暂时不可用' } }, 503)
    }))
    render(<AuditPage />)
    await screen.findByText('account_group.update')
    await userEvent.type(screen.getByLabelText('操作人（精确）'), 'new-admin')
    await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('审计暂时不可用')
    await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
    await waitFor(() => expect(calls).toHaveLength(3))
    expect(calls[1]).toContain('actor_id=new-admin')
    expect(calls[2]).toContain('actor_id=new-admin')
    expect(calls[2]).not.toContain('cursor=')
  })
})

describe('audit capability negotiation', () => {
  it('hides audit navigation and makes zero audit requests when an old server omits the capability', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (url.endsWith('/session')) return response({ username: 'admin', csrf_token: 'csrf' })
      if (url.endsWith('/employees')) return response({ items: [] })
      if (url.endsWith('/system/status')) return response({ version: 'old', ready: true, storage: 'ready', limitations: [], features: {} })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    expect(await screen.findByRole('heading', { name: '员工与 Key' })).toBeInTheDocument()
    await waitFor(() => expect(calls.some((url) => url.endsWith('/system/status'))).toBe(true))
    expect(screen.queryByRole('button', { name: '管理审计' })).not.toBeInTheDocument()
    expect(calls.some((url) => url.includes('/audit/events'))).toBe(false)
  })

  it('shows the gated navigation and loads audit only after the administrator opens it', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      if (url.endsWith('/session')) return response({ username: 'admin', csrf_token: 'csrf' })
      if (url.endsWith('/employees')) return response({ items: [] })
      if (url.endsWith('/system/status')) return response({ version: 'new', ready: true, storage: 'ready', limitations: [], features: { admin_audit_overview: true } })
      if (url.includes('/audit/events?')) return response(auditPage())
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    const navigation = await screen.findByRole('navigation', { name: '主导航' })
    const auditButton = await within(navigation).findByRole('button', { name: '管理审计' })
    expect(calls.some((url) => url.includes('/audit/events'))).toBe(false)
    await userEvent.click(auditButton)
    expect(await screen.findByRole('heading', { name: '管理审计' })).toBeInTheDocument()
    expect(await screen.findByText('account_group.update')).toBeInTheDocument()
    expect(calls.filter((url) => url.includes('/audit/events'))).toHaveLength(1)
  })
})
