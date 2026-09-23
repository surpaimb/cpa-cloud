import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { CodexOAuthAuthorization, isSafeCodexAuthorizationURL } from '../CodexOAuth'

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const session = {
  session_id: 'oauth-session-1',
  authorization_url: 'https://auth.openai.com/oauth/authorize?client_id=public&state=opaque',
  expires_at: '2099-01-01T00:00:00Z',
}
const upstream = {
  id: 'codex-oauth-1', name: 'Codex OAuth', provider_kind: 'codex-membership' as const, endpoint: 'https://chatgpt.com/backend-api/codex', enabled: true, revision: 1,
  credential_state: 'imported_unverified' as const, verified_at: null, oauth_refresh: { eligible: true, state: 'ready' as const },
}

describe('Codex OAuth authorization', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('accepts only the exact OpenAI HTTPS authorization endpoint', () => {
    expect(isSafeCodexAuthorizationURL(session.authorization_url)).toBe(true)
    expect(isSafeCodexAuthorizationURL('https://auth.openai.com/oauth/authorize/extra')).toBe(false)
    expect(isSafeCodexAuthorizationURL('https://auth.openai.com.evil.example/oauth/authorize')).toBe(false)
    expect(isSafeCodexAuthorizationURL('http://auth.openai.com/oauth/authorize')).toBe(false)
    expect(isSafeCodexAuthorizationURL('https://user@auth.openai.com/oauth/authorize')).toBe(false)
  })

  it('renders an explicit safe link and reuses the operation ID when creation is retried', async () => {
    let creates = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') {
        creates += 1
        if (creates === 1) return json({ error: { code: 'temporary', message: 'secret server detail' } }, 500)
        return json(session)
      }
      if (url.endsWith(`/upstreams/codex-oauth-sessions/${session.session_id}`)) return json({ ...session, status: 'pending' })
      throw new Error(`Unexpected request ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)

    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))
    expect(await screen.findByRole('alert')).not.toHaveTextContent('secret server detail')
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    const link = await screen.findByRole('link', { name: '打开 OpenAI 官方授权页' })
    expect(link).toHaveAttribute('href', session.authorization_url)
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')
    const createCalls = fetchMock.mock.calls.filter(([, init]) => (init as RequestInit)?.method === 'POST')
    const first = JSON.parse(String((createCalls[0][1] as RequestInit).body))
    const second = JSON.parse(String((createCalls[1][1] as RequestInit).body))
    expect(second.operation_id).toBe(first.operation_id)
    expect(new Headers((createCalls[0][1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf')
  })

  it('reloads the upstream and offers model sync without creating a route or claiming verification', async () => {
    const onChanged = vi.fn(async () => upstream)
    const onSync = vi.fn()
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') return json(session)
      if (url.endsWith(`/upstreams/codex-oauth-sessions/${session.session_id}`)) return json({ session_id: session.session_id, status: 'succeeded', expires_at: session.expires_at, upstream_id: upstream.id })
      throw new Error(`Unexpected request ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={onChanged} onSync={onSync} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    expect(await screen.findByText(/此连接尚未验证/)).toBeInTheDocument()
    expect(onChanged).toHaveBeenCalledWith(upstream.id)
    expect(screen.queryByText(/^已验证$/)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/models'))).toBe(false)
    await userEvent.click(screen.getByRole('button', { name: '同步模型' }))
    expect(onSync).toHaveBeenCalledWith(upstream)
  })

  it('never renders an unsafe authorization URL', async () => {
    vi.stubGlobal('fetch', vi.fn(() => json({ ...session, authorization_url: 'https://auth.openai.com.evil.example/oauth/authorize?token=secret' })))
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('不安全的授权地址')
    expect(screen.queryByRole('link', { name: '打开 OpenAI 官方授权页' })).not.toBeInTheDocument()
    expect(document.body).not.toHaveTextContent('token=secret')
  })

  it('hides the stale authorization link and shows accurate recovery actions after a terminal failure', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') return json(session)
      return json({ session_id: session.session_id, status: 'failed', expires_at: session.expires_at, error_code: 'codex_oauth_source_mismatch' })
    }))
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    expect(await screen.findByText('授权未完成')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('授权来源与当前服务不匹配')
    expect(screen.queryByRole('link', { name: '打开 OpenAI 官方授权页' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新开始' })).toBeEnabled()
    expect(screen.getAllByRole('button', { name: '关闭' })).toHaveLength(2)
  })

  it.each([
    ['cancelled', '授权已取消'],
    ['expired', '授权会话已过期'],
  ] as const)('renders the %s terminal state without the old authorization link', async (terminalStatus, title) => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') return json(session)
      return json({ session_id: session.session_id, status: terminalStatus, expires_at: session.expires_at })
    }))
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    expect(await screen.findByText(title, { selector: 'strong' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: '打开 OpenAI 官方授权页' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新开始' })).toBeEnabled()
  })

  it.each([
    [{ ...session, session_id: '' }, '无效的授权会话信息'],
    [{ ...session, expires_at: 'not-a-date' }, '无效的授权会话信息'],
    [{ ...session, expires_at: '2020-01-01T00:00:00Z' }, '授权会话已过期'],
  ])('rejects an invalid creation payload without starting a poll', async (payload, expected) => {
    const fetchMock = vi.fn(() => json(payload))
    vi.stubGlobal('fetch', fetchMock)
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(expected)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('link', { name: '打开 OpenAI 官方授权页' })).not.toBeInTheDocument()
  })

  it.each([
    [{ status: 'pending', expires_at: session.expires_at }],
    [{ session_id: 'different-session', status: 'pending', expires_at: session.expires_at }],
    [null],
  ])('stops polling when the status payload is invalid or has a mismatched session ID', async (statusPayload) => {
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') return json(session)
      return json(statusPayload)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={async () => undefined} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('授权状态响应无效或会话不匹配')
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(screen.queryByRole('link', { name: '打开 OpenAI 官方授权页' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新开始' })).toBeEnabled()
  })

  it('ignores a late poll response after the dialog is closed', async () => {
    let resolvePoll!: (response: Response) => void
    const pendingPoll = new Promise<Response>((resolve) => { resolvePoll = resolve })
    const onChanged = vi.fn(async () => upstream)
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/codex-oauth-sessions') && init?.method === 'POST') return json(session)
      return pendingPoll
    })
    vi.stubGlobal('fetch', fetchMock)
    const rendered = render(<CodexOAuthAuthorization csrf="csrf" onClose={() => undefined} onUpstreamsChanged={onChanged} onSync={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '创建授权会话' }))
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    rendered.unmount()
    resolvePoll(new Response(JSON.stringify({ session_id: session.session_id, status: 'succeeded', expires_at: session.expires_at, upstream_id: upstream.id }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await Promise.resolve()
    expect(onChanged).not.toHaveBeenCalled()
  })
})
