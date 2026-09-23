import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GovernanceObservations } from '../GovernanceObservations'
import type { GovernanceObservationsPage } from '../api'

function response(body: unknown, status = 200) {
  return Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
}

function page(scopeID: string, nextCursor: string | null = null): GovernanceObservationsPage {
  return {
    window_end: '2026-09-23T12:00:00.000000000Z',
    observed_at: '2026-09-23T12:00:02.000000000Z',
    tpm_from: '2026-09-23T11:59:00.000000000Z',
    cost_from: '2026-09-22T12:00:00.000000000Z',
    items: [{
      snapshot: {
        settings_revision: '9007199254740993', scope_kind: 'group', scope_id: scopeID,
        policy_id: 'policy-1', policy_revision: '9007199254740995', group_revision: '9007199254740997',
        shadow_tpm: '900719925474099312345', shadow_cost_micro: '9223372036854775807',
        shadow_currency: 'USD', shadow_window: 'rolling_24h',
      },
      scope_totals: {
        tpm: {
          known_tokens: '900719925474099312344', known_attempts: '12', unknown_token_attempts: '1',
          pending_attempts: '2', pending_requests_without_attempt: '3', zero_attempt_requests: '4',
        },
        cost: {
          known_attempts: '12', unknown_cost_attempts: '1', pending_attempts: '2',
          pending_requests_without_attempt: '3', zero_attempt_requests: '4',
          by_currency: [
            { currency: 'EUR', known_cost_micro: '700000', attempts: '2' },
            { currency: 'USD', known_cost_micro: '3100000', attempts: '10' },
          ],
        },
      },
      interpretation: { tpm_state: 'unknown', cost_state: 'exceeded', incomparable_currency_attempts: '2' },
    }],
    next_cursor: nextCursor,
  }
}

function emptyPage() {
  return { ...page('unused'), items: [], next_cursor: null }
}

afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.unstubAllGlobals() })

describe('GovernanceObservations', () => {
  it('keeps decimal strings exact, separates currencies, and pins cursor pagination', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      calls.push(url)
      return response(url.includes('cursor=cursor-one') ? emptyPage() : page('group-large', 'cursor-one'))
    }))
    render(<GovernanceObservations />)

    expect(screen.queryByText('group-large')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '查看用量观测' }))
    expect(await screen.findByText('group-large')).toBeInTheDocument()
    expect(screen.getByText('900,719,925,474,099,312,344')).toBeInTheDocument()
    expect(screen.getByText(/900,719,925,474,099,312,345 Token/)).toBeInTheDocument()
    expect(screen.getByText('9,223,372,036,854.775807 USD 阈值')).toBeInTheDocument()
    expect(screen.getByText('0.7 EUR')).toBeInTheDocument()
    expect(screen.getByText('3.1 USD')).toBeInTheDocument()
    expect(screen.getByText('无法判断')).toBeInTheDocument()
    expect(screen.getByText('已超过')).toBeInTheDocument()
    expect(screen.getByText(/不同币种不会相加或换算/)).toBeInTheDocument()
    expect(screen.getByText(/同一请求可同时计入员工、Key 与治理组/)).toBeInTheDocument()
    expect(screen.getByText(/请勿把不同卡片的 Token 或金额相加/)).toBeInTheDocument()
    expect(calls[0]).toContain('/admin/api/v1/governance/observations?limit=20')

    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByText('窗口内无观测')).toBeInTheDocument()
    expect(calls[1]).toContain('cursor=cursor-one')
    expect(screen.getByText('第 2 页')).toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '刷新首屏' }))
    expect(await screen.findByText('group-large')).toBeInTheDocument()
    expect(calls[2]).not.toContain('cursor=')
    expect(screen.getByText('第 1 页')).toBeInTheDocument()
  })

  it('allows kind-only filters, rejects id-only filters, and encodes IDs', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      calls.push(String(input))
      return response(emptyPage())
    }))
    render(<GovernanceObservations />)
    await userEvent.click(screen.getByRole('button', { name: '查看用量观测' }))
    await screen.findByText('窗口内无观测')

    await userEvent.selectOptions(screen.getByLabelText('范围类型'), 'employee')
    await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1]).toContain('scope_kind=employee')
    expect(calls[1]).not.toContain('scope_id=')

	await userEvent.selectOptions(screen.getByLabelText('范围类型'), '')
	await userEvent.type(screen.getByLabelText('范围 ID（可选）'), 'id-only')
	await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
	expect(screen.getByRole('alert')).toHaveTextContent('填写范围 ID 时必须选择范围类型')
	expect(calls).toHaveLength(2)

	await userEvent.selectOptions(screen.getByLabelText('范围类型'), 'employee')
	await userEvent.clear(screen.getByLabelText('范围 ID（可选）'))
	await userEvent.type(screen.getByLabelText('范围 ID（可选）'), '中'.repeat(67))
	await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
	expect(screen.getByRole('alert')).toHaveTextContent('最多 200 字节')
	expect(calls).toHaveLength(2)

	await userEvent.clear(screen.getByLabelText('范围 ID（可选）'))
    await userEvent.type(screen.getByLabelText('范围 ID（可选）'), 'employee / 中文')
    await userEvent.type(screen.getByLabelText('策略 ID（可选）'), 'policy + 1')
    await userEvent.click(screen.getByRole('button', { name: '应用筛选' }))
	await waitFor(() => expect(calls).toHaveLength(3))
    expect(calls[2]).toContain('scope_kind=employee')
    expect(calls[2]).toContain('scope_id=employee+%2F+%E4%B8%AD%E6%96%87')
    expect(calls[2]).toContain('policy_id=policy+%2B+1')
  })

  it('retries a failed first read and does not turn an empty window into below', async () => {
    let calls = 0
    vi.stubGlobal('fetch', vi.fn(() => {
      calls += 1
      if (calls === 1) return response({ error: { code: 'storage_unavailable', message: '暂时不可用' } }, 503)
      return response(emptyPage())
    }))
    render(<GovernanceObservations />)
    await userEvent.click(screen.getByRole('button', { name: '查看用量观测' }))
    expect(await screen.findByText('暂时无法读取')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重新加载' }))
    expect(await screen.findByText('窗口内无观测')).toBeInTheDocument()
    expect(screen.getByText(/无法证明用量为零/)).toBeInTheDocument()
    expect(screen.queryByText('未超过阈值')).not.toBeInTheDocument()
    expect(calls).toBe(2)
  })

  it('ignores a late response after a newer filtered request', async () => {
    let resolveRefresh: ((value: Response) => void) | undefined
    let calls = 0
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      calls += 1
      if (calls === 1) return response(page('initial-scope'))
      if (calls === 2) return new Promise<Response>((resolve) => { resolveRefresh = resolve })
      expect(String(input)).toContain('scope_id=new-scope')
      return response(page('new-scope'))
    }))
    const { container } = render(<GovernanceObservations />)
    await userEvent.click(screen.getByRole('button', { name: '查看用量观测' }))
    await screen.findByText('initial-scope')

    await userEvent.click(screen.getByRole('button', { name: '刷新首屏' }))
    await userEvent.selectOptions(screen.getByLabelText('范围类型'), 'group')
	await userEvent.type(screen.getByLabelText('范围 ID（可选）'), 'new-scope')
    fireEvent.submit(container.querySelector('form')!)
    expect(await screen.findByText('new-scope')).toBeInTheDocument()
    await act(async () => { resolveRefresh?.(await response(page('stale-scope'))) })
    expect(screen.queryByText('stale-scope')).not.toBeInTheDocument()
    expect(screen.getByText('new-scope')).toBeInTheDocument()
  })

  it('renders nullable cost interpretation as not applicable rather than zero', async () => {
    const noCost = page('employee-no-cost')
    noCost.items[0].snapshot.shadow_cost_micro = null
    noCost.items[0].snapshot.shadow_currency = ''
    noCost.items[0].snapshot.shadow_window = ''
    noCost.items[0].interpretation.cost_state = null
    noCost.items[0].interpretation.incomparable_currency_attempts = null
    vi.stubGlobal('fetch', vi.fn(() => response(noCost)))
    render(<GovernanceObservations />)
    await userEvent.click(screen.getByRole('button', { name: '查看用量观测' }))
    const card = (await screen.findByText('employee-no-cost')).closest('article')!
    expect(within(card).getByText('未配置成本阈值')).toBeInTheDocument()
    expect(within(card).getByText(/其他币种不可比较尝试：不适用/)).toBeInTheDocument()
  })
})
