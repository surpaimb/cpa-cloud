import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UpstreamHealth } from '../UpstreamHealth'
import type { Upstream, UpstreamTestOperation } from '../api'

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
}))

const baseUpstream: Upstream = {
  id: 'upstream-1',
  name: '主账号',
  provider_kind: 'openai-compatible',
  endpoint: 'https://api.example/v1',
  enabled: true,
  revision: 7,
  credential_state: null,
  verified_at: null,
}

function operation(overrides: Partial<UpstreamTestOperation> = {}): UpstreamTestOperation {
  return {
    operation_id: '11111111-1111-4111-8111-111111111111',
    upstream_id: baseUpstream.id,
    provider_kind: baseUpstream.provider_kind,
    requested_revision: 7,
    tested_revision: 7,
    scope: 'local_credential',
    state: 'completed',
    result_code: 'local_credential_ok',
    created_at: '2026-09-23T01:00:00Z',
    started_at: '2026-09-23T01:00:01Z',
    finished_at: '2026-09-23T01:00:02Z',
    latency_ms: 1000,
    ...overrides,
  }
}

describe('upstream health controls', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('keeps an ambiguous operation across dialog close and retries the exact payload', async () => {
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
    let posts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith('/upstreams/upstream-1/tests') || init?.method !== 'POST') throw new Error(`unexpected ${String(input)}`)
      posts += 1
      if (posts === 1) return Promise.reject(new TypeError('secret transport detail'))
      return json(operation({ result_code: 'authentication_failed' }))
    })
    vi.stubGlobal('fetch', fetchMock)
    const onReload = vi.fn(async () => undefined)
    render(<UpstreamHealth item={baseUpstream} csrf="csrf" onReload={onReload} />)

    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    await userEvent.click(screen.getByRole('button', { name: '开始测试' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('测试结果未确认')
    expect(screen.queryByText('secret transport detail')).not.toBeInTheDocument()

    const firstDialog = screen.getByRole('dialog')
    await userEvent.click(within(firstDialog).getAllByRole('button', { name: '关闭' })[1])
    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    expect(screen.getByText('11111111-1111-4111-8111-111111111111')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重试相同操作' }))

    expect(await screen.findByText('本地凭据检查未通过')).toBeInTheDocument()
    expect(screen.getByText(/此检查没有访问上游/)).toBeInTheDocument()
    expect(screen.queryByText('上游拒绝当前凭据')).not.toBeInTheDocument()
    expect(onReload).toHaveBeenCalledTimes(1)

    const calls = fetchMock.mock.calls
    expect(calls).toHaveLength(2)
    expect(String((calls[1][1] as RequestInit).body)).toBe(String((calls[0][1] as RequestInit).body))
    expect(JSON.parse(String((calls[0][1] as RequestInit).body))).toEqual({
      operation_id: '11111111-1111-4111-8111-111111111111',
      expected_revision: 7,
      scope: 'local_credential',
    })
  })

  it('does not poll automatically and queries an in-progress operation only on demand', async () => {
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
    const codexUpstream: Upstream = { ...baseUpstream, provider_kind: 'codex-membership' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/upstreams/upstream-1/tests') && init?.method === 'POST') {
        return json(operation({ provider_kind: 'codex-membership', scope: 'catalog', state: 'in_progress', result_code: null, tested_revision: null, finished_at: null, latency_ms: null }), 202)
      }
      if (url.endsWith('/upstreams/upstream-1/tests/11111111-1111-4111-8111-111111111111') && !init?.method) {
        return json(operation({ provider_kind: 'codex-membership', scope: 'catalog', result_code: 'rate_limited' }))
      }
      throw new Error(`unexpected ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const onReload = vi.fn(async () => undefined)
    render(<UpstreamHealth item={codexUpstream} csrf="csrf" onReload={onReload} />)

    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    await userEvent.selectOptions(screen.getByLabelText('测试范围'), 'catalog')
    expect(screen.getByText(/Codex 目录测试可能刷新凭据/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '开始测试' }))
    expect(await screen.findByText('测试仍在进行')).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body)).scope).toBe('catalog')

    await userEvent.click(screen.getByRole('button', { name: '查询原操作' }))
    expect(await screen.findByText('目录请求受到限流')).toBeInTheDocument()
    expect(screen.getByText(/手动新建测试/)).toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(2)
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('starts a new UUID only after a revision conflict is explicitly refreshed', async () => {
    vi.spyOn(globalThis.crypto, 'randomUUID')
      .mockReturnValueOnce('11111111-1111-4111-8111-111111111111')
      .mockReturnValueOnce('22222222-2222-4222-8222-222222222222')
    let current = baseUpstream
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (!String(input).endsWith('/upstreams/upstream-1/tests') || init?.method !== 'POST') throw new Error(`unexpected ${String(input)}`)
      const body = JSON.parse(String(init.body))
      if (body.expected_revision === 7) return json({ error: { code: 'revision_conflict', message: 'secret conflict detail' } }, 409)
      return json(operation({ operation_id: body.operation_id, requested_revision: 8, tested_revision: 8 }))
    })
    vi.stubGlobal('fetch', fetchMock)
    const onReload = vi.fn(async () => undefined)
    const view = render(<UpstreamHealth item={current} csrf="csrf" onReload={onReload} />)

    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    await userEvent.click(screen.getByRole('button', { name: '开始测试' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('账号版本已变化')
    current = { ...current, revision: 8 }
    view.rerender(<UpstreamHealth item={current} csrf="csrf" onReload={onReload} />)
    expect(screen.getByText(/账号已从 r7 更新为 r8/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '按当前版本新建测试' }))
    await screen.findByText('本地凭据可解析')

    const bodies = fetchMock.mock.calls.map(([, init]) => JSON.parse(String((init as RequestInit).body)))
    expect(bodies).toEqual([
      { operation_id: '11111111-1111-4111-8111-111111111111', expected_revision: 7, scope: 'local_credential' },
      { operation_id: '22222222-2222-4222-8222-222222222222', expected_revision: 8, scope: 'local_credential' },
    ])
  })

  it('clears only the displayed cooldown event and never enables a disabled account', async () => {
    const item: Upstream = {
      ...baseUpstream,
      enabled: false,
      cooldown: {
        event_id: 'cooldown-event-7', failure_class: 'rate_limited', cooldown_until: '2026-09-23T02:00:00Z',
        updated_at: '2026-09-23T01:00:00Z', active: true,
      },
    }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/upstreams/upstream-1/cooldown/clear') && init?.method === 'POST') {
        return json({ result: 'cleared', upstream_id: item.id, revision: 7, server_time: '2026-09-23T01:01:00Z' })
      }
      throw new Error(`unexpected ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const onReload = vi.fn(async () => undefined)
    render(<UpstreamHealth item={item} csrf="csrf" onReload={onReload} />)

    expect(screen.getByText('冷却中：上游限流')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '清除冷却' }))
    await waitFor(() => expect(onReload).toHaveBeenCalledTimes(1))
    const body = JSON.parse(String((fetchMock.mock.calls[0][1] as RequestInit).body))
    expect(body).toEqual({ expected_revision: 7, expected_cooldown_event_id: 'cooldown-event-7' })

    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    expect(screen.getByText(/测试或清除冷却都不会启用/)).toBeInTheDocument()
  })

  it('shows a refresh action on cooldown CAS conflict without exposing server detail', async () => {
    const item: Upstream = {
      ...baseUpstream,
      cooldown: {
        event_id: 'old-event', failure_class: 'authentication', cooldown_until: '2026-09-23T02:00:00Z',
        updated_at: '2026-09-23T01:00:00Z', active: true,
      },
    }
    vi.stubGlobal('fetch', vi.fn(() => json({ error: { code: 'cooldown_conflict', message: 'new event secret detail' } }, 409)))
    const onReload = vi.fn(async () => undefined)
    render(<UpstreamHealth item={item} csrf="csrf" onReload={onReload} />)

    await userEvent.click(screen.getByRole('button', { name: '清除冷却' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('冷却状态已更新')
    expect(screen.queryByText('new event secret detail')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '刷新列表' }))
    expect(onReload).toHaveBeenCalledTimes(1)
  })

  it('ignores a completed response after the account control unmounts', async () => {
    vi.spyOn(globalThis.crypto, 'randomUUID').mockReturnValue('11111111-1111-4111-8111-111111111111')
    let resolveResponse: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => { resolveResponse = resolve })))
    const onReload = vi.fn(async () => undefined)
    const view = render(<UpstreamHealth item={baseUpstream} csrf="csrf" onReload={onReload} />)
    await userEvent.click(screen.getByRole('button', { name: '账号测试' }))
    await userEvent.click(screen.getByRole('button', { name: '开始测试' }))
    view.unmount()
    resolveResponse?.(new Response(JSON.stringify(operation()), { status: 200, headers: { 'Content-Type': 'application/json' } }))
    await Promise.resolve()
    await Promise.resolve()
    expect(onReload).not.toHaveBeenCalled()
  })
})
