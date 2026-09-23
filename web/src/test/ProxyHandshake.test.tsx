import { act, cleanup, fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ProxyHandshakeDialog, type ProxyHandshakeAccount } from '../ProxyHandshake'
import type { OutboundProxy, OutboundProxyTestOperation, Upstream } from '../api'

const uuid = '11111111-1111-4111-8111-111111111111'
const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const proxy = (revision = 3): OutboundProxy => ({ id: 'proxy-1', name: '主出口', scheme: 'https', host: 'proxy.example', port: 443, address_scope: 'public', enabled: true, revision, connection_revision: 2, has_credentials: true, created_at: '2026-09-23T00:00:00Z', updated_at: '2026-09-23T00:00:00Z' })
const upstream = (overrides: Partial<Upstream> = {}): Upstream => ({ id: 'upstream-1', name: '生产账号', provider_kind: 'openai-compatible', endpoint: 'https://models.example/v1', enabled: true, revision: 7, credential_state: null, verified_at: null, ...overrides })
const account = (upstreamOverrides: Partial<Upstream> = {}, proxyID = 'proxy-1'): ProxyHandshakeAccount => {
  const item = upstream(upstreamOverrides)
  return { upstream: item, proxyState: { upstream_id: item.id, upstream_revision: item.revision, binding: { proxy_id: proxyID, proxy_revision: 3, connection_revision: 2, enabled: true, name: '主出口' } } }
}
const operation = (overrides: Partial<OutboundProxyTestOperation> = {}): OutboundProxyTestOperation => ({
  operation_id: uuid,
  proxy_id: 'proxy-1',
  proxy_revision: 3,
  connection_revision: 2,
  upstream_id: 'upstream-1',
  upstream_revision: 7,
  state: 'completed',
  result_code: 'handshake_ok',
  created_at: '2026-09-23T01:00:00Z',
  started_at: '2026-09-23T01:00:01Z',
  finished_at: '2026-09-23T01:00:02Z',
  latency_ms: 123,
  ...overrides,
})

describe('outbound proxy handshake UI', () => {
  beforeEach(() => { vi.restoreAllMocks(); vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue(uuid) })
  afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals() })

  it('offers only explicitly bound enabled HTTPS API-key accounts and freezes every revision', async () => {
    const accounts = [
      account(),
      account({ id: 'codex', name: 'Codex', provider_kind: 'codex-membership' }),
      account({ id: 'http', name: 'HTTP', endpoint: 'http://models.example/v1' }),
      account({ id: 'disabled', name: '停用', enabled: false }),
      account({ id: 'other', name: '其他绑定' }, 'proxy-2'),
    ]
    let sent: Record<string, unknown> | undefined
    vi.stubGlobal('fetch', vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      sent = JSON.parse(String(init?.body))
      return json(operation())
    }))
    render(<ProxyHandshakeDialog proxy={proxy()} accounts={accounts} csrf="csrf" onClose={() => undefined} />)

    const select = screen.getByLabelText('已绑定目标账号')
    expect(select).toHaveTextContent('生产账号 · 账号 r7')
    for (const excluded of ['Codex', 'HTTP', '停用', '其他绑定']) expect(select).not.toHaveTextContent(excluded)
    await userEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    expect(await screen.findByText('代理与目标 TLS 握手完成')).toBeInTheDocument()
    expect(screen.getByText(/未发送模型请求，也不表示模型生成可用/)).toBeInTheDocument()
    expect(screen.getByText('r3 / 连接 r2')).toBeInTheDocument()
    expect(screen.getByText('r7')).toBeInTheDocument()
    expect(sent).toEqual({ operation_id: uuid, expected_proxy_revision: 3, expected_connection_revision: 2, upstream_id: 'upstream-1', expected_upstream_revision: 7 })
  })

  it('queries the original UUID after a lost POST and treats 404 as unknown rather than not executed', async () => {
    let gets = 0
    let posts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (init?.method === 'POST') { posts += 1; return Promise.reject(new TypeError('secret network detail')) }
      if (url.endsWith(`/tests/${uuid}`)) {
        gets += 1
        if (gets === 1) return json({ error: { code: 'not_found', message: 'secret server detail' } }, 404)
        return json(operation({ result_code: 'proxy_unavailable' }))
      }
      throw new Error(`unexpected ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ProxyHandshakeDialog proxy={proxy()} accounts={[account()]} csrf="csrf" onClose={() => undefined} />)

    await userEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('这不表示检查未执行')
    expect(screen.queryByText(/secret/)).not.toBeInTheDocument()
    expect(posts).toBe(1)
    expect(gets).toBe(1)
    expect(screen.getByText(uuid)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '查询原操作' }))
    expect(await screen.findByText('代理不可用')).toBeInTheDocument()
    expect(posts).toBe(1)
    expect(gets).toBe(2)
  })

  it('stops automatic polling at a fixed limit and retains a manual query action', async () => {
    vi.useFakeTimers()
    let gets = 0
    const pending = operation({ state: 'in_progress', result_code: null, finished_at: null, latency_ms: null })
    vi.stubGlobal('fetch', vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') return json(pending, 202)
      gets += 1
      return json(pending)
    }))
    render(<ProxyHandshakeDialog proxy={proxy()} accounts={[account()]} csrf="csrf" onClose={() => undefined} />)

    fireEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    await act(async () => { await Promise.resolve(); await vi.advanceTimersByTimeAsync(5000) })
    expect(gets).toBe(8)
    expect(screen.getByText(/自动查询已停止/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '查询原操作' })).toBeEnabled()
    fireEvent.click(screen.getByRole('button', { name: '查询原操作' }))
    await act(async () => { await Promise.resolve() })
    expect(gets).toBe(9)
  })

  it('does not present an old handshake success as current after the proxy revision changes', async () => {
    vi.stubGlobal('fetch', vi.fn(() => json(operation())))
    const view = render(<ProxyHandshakeDialog proxy={proxy()} accounts={[account()]} csrf="csrf" onClose={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    expect(await screen.findByText('代理与目标 TLS 握手完成')).toBeInTheDocument()
    view.rerender(<ProxyHandshakeDialog proxy={proxy(4)} accounts={[account()]} csrf="csrf" onClose={() => undefined} />)
    expect(screen.getByText('历史握手通过，当前配置未验证')).toBeInTheDocument()
    expect(screen.getByText(/旧版本快照/)).toBeInTheDocument()
  })

  it('cancels browser polling on close without issuing another request or cancelling the service operation', async () => {
    vi.useFakeTimers()
    let gets = 0
    const pending = operation({ state: 'pending', result_code: null, started_at: null, finished_at: null, latency_ms: null })
    vi.stubGlobal('fetch', vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      if (init?.method === 'POST') return json(pending, 202)
      gets += 1
      return json(pending)
    }))
    const onClose = vi.fn()
    render(<ProxyHandshakeDialog proxy={proxy()} accounts={[account()]} csrf="csrf" onClose={onClose} />)
    fireEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    await act(async () => { await Promise.resolve() })
    fireEvent.click(screen.getAllByRole('button', { name: '关闭' }).at(-1)!)
    await act(async () => { await vi.advanceTimersByTimeAsync(5000) })
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(gets).toBe(0)
    expect(screen.queryByText('AbortError')).not.toBeInTheDocument()
  })

  it('rejects a mismatched operation response and never displays its raw result as trusted', async () => {
    vi.stubGlobal('fetch', vi.fn(() => json(operation({ proxy_id: 'other-proxy' }))))
    render(<ProxyHandshakeDialog proxy={proxy()} accounts={[account()]} csrf="csrf" onClose={() => undefined} />)
    await userEvent.click(screen.getByRole('button', { name: '开始仅握手检查' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('操作记录与已冻结请求不匹配')
    expect(screen.queryByText('代理与目标 TLS 握手完成')).not.toBeInTheDocument()
  })
})
