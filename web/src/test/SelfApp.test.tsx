// Independently authored UI checks for docs/employee-self-service-foundation-contract.md.
import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SelfApp } from '../SelfApp'

const reply = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

describe('employee self-service page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('logs in with the independent endpoint and displays only the personal profile', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(401, { error: { code: 'authentication_required' } })
      if (url.endsWith('/sessions') && init?.method === 'POST') return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
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
})
