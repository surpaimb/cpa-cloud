// Independently authored UI checks for docs/employee-self-service-foundation-contract.md
// docs/employee-self-password-change-contract.md, and
// docs/employee-self-key-inventory-contract.md.
// docs/employee-self-request-history-contract.md.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SelfApp } from '../SelfApp'

const reply = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

describe('employee self-service page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('logs in with the independent endpoint and displays only owned metadata', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(401, { error: { code: 'authentication_required' } })
      if (url.endsWith('/sessions') && init?.method === 'POST') return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.type(await screen.findByLabelText('员工 ID'), 'emp-1')
    await userEvent.type(screen.getByLabelText('密码'), 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.getByText('Research')).toBeInTheDocument()
    expect(screen.queryByText('private memo')).not.toBeInTheDocument()
    const login = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/sessions') && init?.method === 'POST')
    expect(String(login?.[0])).toBe('/self/api/v1/sessions')
    expect(new Headers(login?.[1]?.headers).get('X-Self-Request')).toBe('1')
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    await waitFor(() => expect(screen.getByRole('heading', { name: '员工登录' })).toBeInTheDocument())
    const logout = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/sessions') && init?.method === 'DELETE')
    expect(new Headers(logout?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
  })

  it('supports enrollment without placing the secret in browser storage', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(401, { error: { code: 'authentication_required' } })
      if (url.endsWith('/enroll') && init?.method === 'POST') return reply(200, { csrf_token: 'self-csrf', profile: { id: 'emp-1', name: 'Alice', department: '', status: 'active' } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '持有开通码？首次设置密码' }))
    await userEvent.type(screen.getByLabelText('员工 ID'), 'emp-1')
    await userEvent.type(screen.getByLabelText('一次性开通码'), 'synthetic-secret')
    await userEvent.type(screen.getByLabelText('设置密码（12–72 字节）'), 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '开通并登录' }))
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByText('synthetic-secret')).not.toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/enroll'))
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ employee_id: 'emp-1', enrollment_secret: 'synthetic-secret', password: 'a-long-self-password' })
  })

  it('changes the current employee password and requires a fresh login', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/password') && init?.method === 'POST') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '修改密码' }))
    const current = screen.getByLabelText('当前密码') as HTMLInputElement
    const next = screen.getByLabelText('新密码（12–72 字节）') as HTMLInputElement
    expect(current.type).toBe('password')
    expect(next.type).toBe('password')
    await userEvent.type(current, 'a-long-self-password')
    await userEvent.type(next, 'a-new-long-password')
    await userEvent.click(screen.getByRole('button', { name: '确认修改密码' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('请使用新密码重新登录')
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/password'))
    expect(String(call?.[0])).toBe('/self/api/v1/password')
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ current_password: 'a-long-self-password', new_password: 'a-new-long-password' })
    expect(new Headers(call?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    expect(new Headers(call?.[1]?.headers).get('X-Self-Request')).toBe('1')
  })

  it('clears password fields after a failed change without exposing the submitted values', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => String(input).endsWith('/session')
      ? reply(200, { csrf_token: 'self-csrf', profile })
      : String(input).endsWith('/keys') ? reply(200, { items: [], next_cursor: null })
      : String(input).endsWith('/usage/requests') ? reply(200, { items: [], next_cursor: null })
      : reply(401, { error: { code: 'invalid_credentials', message: 'Invalid credentials or enrollment secret.' } })))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '修改密码' }))
    const current = screen.getByLabelText('当前密码') as HTMLInputElement
    const next = screen.getByLabelText('新密码（12–72 字节）') as HTMLInputElement
    await userEvent.type(current, 'wrong-old-password')
    await userEvent.type(next, 'a-new-long-password')
    await userEvent.click(screen.getByRole('button', { name: '确认修改密码' }))
    await waitFor(() => expect(current.value).toBe(''))
    expect(next.value).toBe('')
    expect(screen.getByText('当前密码无效或请求未通过，请检查后重试。')).toBeInTheDocument()
    expect(screen.queryByText('wrong-old-password')).not.toBeInTheDocument()
  })

  it('does not claim success when a password commit outcome is unavailable', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => String(input).endsWith('/session')
      ? reply(200, { csrf_token: 'self-csrf', profile })
      : String(input).endsWith('/keys') ? reply(200, { items: [], next_cursor: null })
      : String(input).endsWith('/usage/requests') ? reply(200, { items: [], next_cursor: null })
      : reply(503, { error: { code: 'storage_unavailable' } })))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '修改密码' }))
    await userEvent.type(screen.getByLabelText('当前密码'), 'a-long-self-password')
    await userEvent.type(screen.getByLabelText('新密码（12–72 字节）'), 'a-new-long-password')
    await userEvent.click(screen.getByRole('button', { name: '确认修改密码' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('结果未确认')
    expect(screen.getByRole('status')).not.toHaveTextContent('密码已更新')
  })

  it('shows only Key metadata, pages explicitly, and clears partial data on a failed page', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const first = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active', key: 'cpac-secret-not-for-display', policy: { source_cidrs: ['private'] } }
    const second = { id: 'key_two', name: 'Batch', created_at: '2026-10-01T02:00:00Z', expires_at: '2026-10-02T00:00:00Z', revoked_at: null, status: 'expired' }
    let failNext = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [first], next_cursor: 'v1.cursor' })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/keys?cursor=v1.cursor')) return reply(200, { items: [second], next_cursor: 'v1.next' })
      if (url.endsWith('/keys?cursor=v1.next') && failNext) return reply(503, { error: { code: 'storage_unavailable' } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByText('Editor')).toBeInTheDocument()
    expect(screen.getByText('key_one')).toBeInTheDocument()
    expect(screen.getByText('永不过期')).toBeInTheDocument()
    expect(screen.queryByText('cpac-secret-not-for-display')).not.toBeInTheDocument()
    expect(screen.queryByText('private')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByText('Batch')).toBeInTheDocument()
    failNext = true
    await userEvent.click(screen.getByRole('button', { name: '加载更多' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('暂时无法读取')
    expect(screen.queryByText('Editor')).not.toBeInTheDocument()
    expect(screen.queryByText('Batch')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/keys')).length).toBe(3)
  })

  it('does not render a late inventory response after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let resolveKeys: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return new Promise<Response>((resolve) => { resolveKeys = resolve })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('status')).toHaveTextContent('正在读取 Key 列表')
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    resolveKeys?.(new Response(JSON.stringify({ items: [{ id: 'key_old', name: 'Old account Key' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(screen.queryByText('Old account Key')).not.toBeInTheDocument())
  })

  it('shows only own request projection and clears all pages after a failed read', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const first = { id: 'req_one', key_id: 'key_one', model_id: 'safe-model', status: 'succeeded', started_at: '2026-10-01T01:00:00Z', finished_at: '2026-10-01T01:00:01Z', provider: 'secret-provider', cost_micro: 123, error_text: 'private failure' }
    const second = { id: 'req_two', key_id: 'key_two', model_id: 'other-model', status: 'pending', started_at: '2026-10-01T01:01:00Z', finished_at: null }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [first], next_cursor: 'cursor.one' })
      if (url.endsWith('/usage/requests?cursor=cursor.one')) return reply(200, { items: [second], next_cursor: 'cursor.two' })
      if (url.endsWith('/usage/requests?cursor=cursor.two')) return reply(503, { error: { code: 'storage_unavailable' } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByText('safe-model')).toBeInTheDocument()
    expect(screen.getByText('req_one')).toBeInTheDocument()
    expect(screen.getByText('key_one')).toBeInTheDocument()
    expect(screen.queryByText('secret-provider')).not.toBeInTheDocument()
    expect(screen.queryByText('private failure')).not.toBeInTheDocument()
    expect(screen.queryByText('123')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '加载更多请求' }))
    expect(await screen.findByText('other-model')).toBeInTheDocument()
    expect(screen.getByText('尚未结束')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '加载更多请求' }))
    expect(await screen.findByText('请求记录暂时无法读取，请稍后重新登录或刷新页面。')).toBeInTheDocument()
    expect(screen.queryByText('safe-model')).not.toBeInTheDocument()
    expect(screen.queryByText('other-model')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/usage/requests')).length).toBe(3)
  })

  it('does not restore a late request-history response after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let resolveHistory: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return new Promise<Response>((resolve) => { resolveHistory = resolve })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    expect(await screen.findByText('正在读取请求记录…')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    resolveHistory?.(new Response(JSON.stringify({ items: [{ id: 'req_old', model_id: 'old-model' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(screen.queryByText('old-model')).not.toBeInTheDocument())
  })
})
