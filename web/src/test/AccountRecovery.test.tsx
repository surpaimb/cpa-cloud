import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AccountRecoveryPanel, RecoveryStateSummary } from '../AccountRecovery'
import type { AccountRecoveryState, AccountRecoveryStatus } from '../api'

const status: AccountRecoveryStatus = {
  cli_allowed: true, enabled: false, setting_revision: 4, running: false,
  next_wake_at: null, history_count: 0, history_full: false, server_time: '2026-09-23T00:00:00Z',
}
const json = (body: unknown, code = 200) => Promise.resolve(new Response(JSON.stringify(body), { status: code }))
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('account generation recovery controls', () => {
  it('requires the startup allowance and never mutates during initial reads', async () => {
    const fetchMock = vi.fn((url: RequestInfo | URL) => String(url).endsWith('/accounts') ? json({ items: [] }) : json({ ...status, cli_allowed: false }))
    vi.stubGlobal('fetch', fetchMock)
    render(<AccountRecoveryPanel csrf="test-csrf" />)
    expect(await screen.findByRole('button', { name: '启用自动恢复' })).toBeDisabled()
    expect(screen.getByText('--allow-account-recovery')).toBeVisible()
    expect(fetchMock.mock.calls).toHaveLength(2)
  })

  it('writes the observed revision with CSRF and waits for server truth', async () => {
    const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
      if (String(url).endsWith('/accounts')) return json({ items: [] })
      if (init?.method === 'PUT') return json({ ...status, enabled: true, running: true, setting_revision: 5 })
      return json(status)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AccountRecoveryPanel csrf="test-csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '启用自动恢复' }))
    expect(await screen.findByRole('button', { name: '关闭自动恢复' })).toBeEnabled()
    const call = fetchMock.mock.calls.find(([, init]) => init?.method === 'PUT')!
    expect(JSON.parse(call[1]!.body as string)).toEqual({ enabled: true, expected_revision: 4 })
    expect(new Headers(call[1]!.headers).get('X-CSRF-Token')).toBe('test-csrf')
    expect(screen.getByText('运行中')).toBeVisible()
  })

  it('does not replay an uncertain settings write until a successful read', async () => {
    let enabled = false
    const fetchMock = vi.fn((url: RequestInfo | URL, init?: RequestInit) => {
      if (String(url).endsWith('/accounts')) return json({ items: [] })
      if (init?.method === 'PUT') { enabled = true; return Promise.reject(new TypeError('synthetic lost response')) }
      return json({ ...status, enabled, running: enabled, setting_revision: enabled ? 5 : 4 })
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<AccountRecoveryPanel csrf="test-csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '启用自动恢复' }))
    expect(await screen.findByText(/设置结果未确认/)).toBeVisible()
    expect(screen.getByRole('button', { name: '启用自动恢复' })).toBeDisabled()
    await userEvent.click(screen.getByRole('button', { name: '重新读取状态' }))
    await waitFor(() => expect(screen.getByRole('button', { name: '关闭自动恢复' })).toBeEnabled())
    expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT')).toHaveLength(1)
  })

  it('keeps expired cooldown separate from recovery isolation and exposes no raw diagnostic', () => {
    const state: AccountRecoveryState = { account_id: 'synthetic', cooldown_event_id: 'event', operation_id: 'op', recovery_revision: 1, account_revision: 1, pool_revision: 1, public_model: 'public', upstream_model: 'actual', protocol: 'openai-responses', state: 'interrupted', next_probe_at: '2026-09-01T00:00:00Z', due: true, last_result_code: 'upstream_timeout', last_finished_at: '2026-09-01T00:00:00Z', attempt_count: 3, auto_eligible: false, attention_code: 'secret-never-rendered', checked_at: null }
    render(<RecoveryStateSummary state={state} />)
    expect(screen.getByText('恢复隔离中')).toBeVisible()
    expect(screen.getByText(/冷却到期仍需验证恢复/)).toBeVisible()
    expect(screen.queryByText('secret-never-rendered')).not.toBeInTheDocument()
    expect(screen.getByText(/本事件尝试 3 \/ 3/)).toBeVisible()
  })
})
