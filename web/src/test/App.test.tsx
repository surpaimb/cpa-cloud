import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'

type Route = { status?: number; body?: unknown }
const response = (route: Route) => Promise.resolve(new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200, headers: { 'Content-Type': 'application/json' } }))

describe('CPA Cloud admin flow', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('restores a session and renders the employees empty state', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ body: { username: 'admin', csrf_token: 'csrf' } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    expect(await screen.findByRole('heading', { name: '员工与 Key' })).toBeInTheDocument()
    expect(await screen.findByText('还没有员工')).toBeInTheDocument()
  })

  it('logs in after an expired session', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ status: 401, body: { error: { code: 'unauthorized', message: '请登录' } } })
      if (url.endsWith('/sessions')) return response({ body: { csrf_token: 'fresh-csrf' } })
      if (url.endsWith('/employees')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    const password = await screen.findByLabelText('密码')
    await userEvent.type(password, 'test-password')
    await userEvent.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('heading', { name: '员工与 Key' })).toBeInTheDocument()
    const loginCall = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/sessions'))
    expect(JSON.parse(String((loginCall?.[1] as RequestInit).body))).toEqual({ username: 'admin', password: 'test-password' })
  })

  it('creates a one-time permanent key, copies it, then clears it from the DOM', async () => {
    const employee = { id: 'emp-1', name: '张三', department: '研发部', note: '', status: 'active', model_mode: 'all', models: [], revision: 1 }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ body: { username: 'admin', csrf_token: 'csrf' } })
      if (url.endsWith('/employees')) return response({ body: { items: [employee] } })
      if (url.endsWith('/employees/emp-1/keys') && init?.method === 'POST') return response({ body: { id: 'key-1', name: '默认 Key', key: 'cpa_test_secret_once', expires_at: null, revoked_at: null } })
      if (url.endsWith('/employees/emp-1/keys')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: '管理 Key' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: '生成永久 Key' }))
    expect(await screen.findByTestId('created-key')).toHaveTextContent('cpa_test_secret_once')
    await userEvent.click(screen.getByRole('button', { name: '复制 Key' }))
    expect(screen.getByRole('button', { name: '已复制' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '我已保存，关闭' }))
    await waitFor(() => expect(screen.queryByText('cpa_test_secret_once')).not.toBeInTheDocument())
    const createCall = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/employees/emp-1/keys') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((createCall?.[1] as RequestInit).body)).expires_at).toBeNull()
    expect(new Headers((createCall?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf')
  })

  it('shows a conflict message when an employee update loses its revision race', async () => {
    const employee = { id: 'emp-1', name: '张三', department: '研发部', status: 'active', model_mode: 'all', models: [], revision: 3 }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ body: { username: 'admin', csrf_token: 'csrf' } })
      if (url.endsWith('/employees') && !init?.method) return response({ body: { items: [employee] } })
      if (url.endsWith('/employees/emp-1') && init?.method === 'PATCH') return response({ status: 409, body: { error: { code: 'conflict', message: 'conflict' } } })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: '停用' }))
    expect(await screen.findByText('数据已被其他操作更新，请刷新后重试。')).toBeInTheDocument()
  })
})
