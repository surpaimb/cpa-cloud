// Independently authored UI checks for docs/employee-self-service-foundation-contract.md
// docs/employee-self-password-change-contract.md, and
// docs/employee-self-key-inventory-contract.md.
// docs/employee-self-request-history-contract.md.
// docs/employee-self-token-summary-contract.md.
// docs/employee-self-key-revocation-contract.md.
// docs/employee-self-signout-others-contract.md.
// docs/employee-self-key-issuance-contract.md.
// docs/employee-self-key-token-summary-contract.md.
// docs/employee-self-key-request-history-contract.md.
// docs/employee-self-wallet-balance-contract.md.
// docs/employee-self-wallet-activity-contract.md.
// docs/employee-self-wallet-entry-classification-contract.md.
// docs/employee-self-subscription-status-contract.md.
// docs/employee-self-plan-catalog-contract.md.
// docs/employee-self-plan-catalog-copy-truth-contract.md.
// docs/employee-self-plan-purchase-contract.md.
// docs/employee-self-subscription-cancel-contract.md.
// docs/employee-self-one-shot-disarm-contract.md.
// docs/employee-self-subscription-purchase-snapshot-contract.md.
// docs/employee-self-monthly-renewal-contract.md.
// docs/employee-self-subscription-renewal-links-contract.md.
// docs/employee-self-redemption-contract.md.
// docs/employee-self-redemption-credit-history-contract.md.
// docs/employee-self-admin-adjustment-history-contract.md.
// docs/employee-self-upstream-estimated-cost-summary-contract.md.
import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SelfApp } from '../SelfApp'

const reply = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const neutralCatalogCopy = '仅展示查询时该币种已启用套餐的当前信息，不是权益或持久报价。此目录只读，不会在这里购买、取消或续购；本人能否执行这些操作，以各自独立授权入口及服务端资格校验为准。'
const emptyTokenSummary = {
  from: '2026-09-30T00:00:00Z', to: '2026-10-01T00:00:00Z',
  requests: { total: '0', pending: '0', succeeded: '0', failed: '0', cancelled: '0', interrupted: '0' },
  attempts: { total: '0', pending: '0', input_tokens: { known_total: '0', unknown_attempts: '0' }, output_tokens: { known_total: '0', unknown_attempts: '0' }, cache_read_tokens: { known_total: '0', unknown_attempts: '0' }, cache_write_tokens: { known_total: '0', unknown_attempts: '0' } },
}

describe('employee self-service page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('gates the internal upstream estimate and only reads after an explicit click', async () => {
    const profile = { id: 'emp-cost', name: 'Cost Reader', department: '', status: 'active' }
    let enabled = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'cost-csrf', profile, features: enabled ? { employee_self_upstream_estimated_cost_summary: true } : {} })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/usage/estimated-cost-summary')) return reply(200, {
        from: '2026-10-02T12:00:00Z', to: '2026-10-03T12:00:00Z', attempts: { total: '4', pending: '1', terminal: '3' },
        costs: [
          { price_currency: 'EUR', attempts: '1', known_estimated_cost_micro: '0', unknown_cost_attempts: '1' },
          { price_currency: 'USD', attempts: '1', known_estimated_cost_micro: '1234567', unknown_cost_attempts: '0' },
          { price_currency: null, attempts: '1', known_estimated_cost_micro: '0', unknown_cost_attempts: '1' },
        ],
      })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const first = render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '你好，Cost Reader' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '上游内部估算成本' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/usage/estimated-cost-summary'))).toBe(false)
    first.unmount()
    enabled = true
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '上游内部估算成本' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/usage/estimated-cost-summary'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(await within(panel).findByText('1.234567 USD')).toBeInTheDocument()
    expect(within(panel).getByText('未配置价格')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/usage/estimated-cost-summary'))).toHaveLength(1)
  })

  it('clears an estimate on failure and ignores a late response after logout', async () => {
    const profile = { id: 'emp-cost', name: 'Cost Reader', department: '', status: 'active' }
    let reads = 0
    let resolveLate: ((response: Response) => void) | undefined
    const valid = { from: '2026-10-02T12:00:00Z', to: '2026-10-03T12:00:00Z', attempts: { total: '1', pending: '0', terminal: '1' },
      costs: [{ price_currency: 'USD', attempts: '1', known_estimated_cost_micro: '1000000', unknown_cost_attempts: '0' }] }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'cost-csrf', profile, features: { employee_self_upstream_estimated_cost_summary: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/usage/estimated-cost-summary')) {
        reads++
        if (reads === 1) return reply(200, valid)
        if (reads === 2) return reply(503, { error: { code: 'storage_unavailable' } })
        return new Promise<Response>(resolve => { resolveLate = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '上游内部估算成本' })
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(await within(panel).findByText('1.000000 USD')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent('已清除旧结果')
    expect(within(panel).queryByText('1.000000 USD')).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(resolveLate).toBeDefined()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(screen.queryByRole('region', { name: '上游内部估算成本' })).not.toBeInTheDocument()
    await act(async () => { resolveLate!(new Response(JSON.stringify(valid), { status: 200, headers: { 'Content-Type': 'application/json' } })) })
    expect(screen.queryByText('1.000000 USD')).not.toBeInTheDocument()
  })

  it.each([
    ['impossible UTC calendar date', { from: '2026-02-30T00:00:00Z', to: '2026-03-03T00:00:00Z',
      attempts: { total: '1', pending: '0', terminal: '1' }, costs: [{ price_currency: 'USD', attempts: '1', known_estimated_cost_micro: '1000000', unknown_cost_attempts: '0' }] }],
    ['unpriced positive known cost', { from: '2026-10-02T12:00:00Z', to: '2026-10-03T12:00:00Z',
      attempts: { total: '1', pending: '0', terminal: '1' }, costs: [{ price_currency: null, attempts: '1', known_estimated_cost_micro: '1', unknown_cost_attempts: '1' }] }],
    ['unpriced attempt not marked unknown', { from: '2026-10-02T12:00:00Z', to: '2026-10-03T12:00:00Z',
      attempts: { total: '1', pending: '0', terminal: '1' }, costs: [{ price_currency: null, attempts: '1', known_estimated_cost_micro: '0', unknown_cost_attempts: '0' }] }],
  ])('rejects %s and clears a previously displayed estimate', async (_name, malformed) => {
    const profile = { id: 'emp-cost', name: 'Cost Reader', department: '', status: 'active' }
    const valid = { from: '2026-10-02T12:00:00Z', to: '2026-10-03T12:00:00Z', attempts: { total: '1', pending: '0', terminal: '1' },
      costs: [{ price_currency: 'USD', attempts: '1', known_estimated_cost_micro: '1000000', unknown_cost_attempts: '0' }] }
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'cost-csrf', profile, features: { employee_self_upstream_estimated_cost_summary: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/usage/estimated-cost-summary')) return reply(200, reads++ === 0 ? valid : malformed)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '上游内部估算成本' })
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(await within(panel).findByText('1.000000 USD')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '查看最近 24 小时估算' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent('已清除旧结果')
    expect(within(panel).queryByText('1.000000 USD')).not.toBeInTheDocument()
  })

  it('keeps the wallet control absent without its independent capability', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: false } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '我的钱包余额' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '兑换码充值本人钱包' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/balance'))).toBe(false)
  })

  it('keeps the redemption code only in a short-lived explicit retry and reuses the same operation', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const submitted: Array<{ operation_id: string; code: string; current_password: string }> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_redemption: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/redemptions')) {
        const body = JSON.parse(String(init?.body)) as { operation_id: string; code: string; current_password: string }
        submitted.push(body)
        if (submitted.length === 1) return Promise.reject(new TypeError('synthetic network uncertainty'))
        return reply(200, { operation_id: body.operation_id, replay: true, currency: 'EUR', amount_micro: '37', credited_at: '2026-10-02T12:00:00Z' })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '兑换码充值本人钱包' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/redemptions'))).toBe(false)
    expect(within(panel).getByLabelText('管理员发放的兑换码')).toHaveAttribute('type', 'password')
    await userEvent.type(within(panel).getByLabelText('管理员发放的兑换码'), 'cpa_synthetic-code')
    await userEvent.type(within(panel).getByLabelText('当前密码'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认将此码兑换/ }))
    await userEvent.click(within(panel).getByRole('button', { name: '确认兑换' }))
    expect(await within(panel).findByText(/结果未确认/)).toBeInTheDocument()
    expect(within(panel).queryByLabelText('管理员发放的兑换码')).not.toBeInTheDocument()
    expect(within(panel).getByLabelText('当前密码（重试原操作）')).toHaveValue('')
    await userEvent.type(within(panel).getByLabelText('当前密码（重试原操作）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认重试原兑换操作/ }))
    await userEvent.click(within(panel).getByRole('button', { name: '重试原兑换' }))
    expect(await within(panel).findByText(/已确认原兑换/)).toBeInTheDocument()
    expect(submitted).toHaveLength(2)
    expect(submitted[0].operation_id).toMatch(/^self-redeem-[a-f0-9]{32}$/)
    expect(submitted[1]).toEqual(submitted[0])
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('cpa_synthetic-code'))).toHaveLength(0)
    expect(within(panel).queryByText('cpa_synthetic-code')).not.toBeInTheDocument()
  })

  it('bounds a hanging redemption and requires administrator review when the retry envelope expires', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const submitted: Array<{ operation_id: string }> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_redemption: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/redemptions')) {
        submitted.push(JSON.parse(String(init?.body)) as { operation_id: string })
        return new Promise<Response>(() => {})
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '兑换码充值本人钱包' })
    vi.useFakeTimers()
    try {
      fireEvent.change(within(panel).getByLabelText('管理员发放的兑换码'), { target: { value: 'cpa_synthetic-code' } })
      fireEvent.change(within(panel).getByLabelText('当前密码'), { target: { value: 'a-long-self-password' } })
      fireEvent.click(within(panel).getByRole('checkbox', { name: /我确认将此码兑换/ }))
      await act(async () => { fireEvent.submit(within(panel).getByRole('button', { name: '确认兑换' }).closest('form')!) })
      expect(submitted).toHaveLength(1)
      await act(async () => { await vi.advanceTimersByTimeAsync(10_000) })
      expect(within(panel).getByRole('alert')).toHaveTextContent('结果未确认')
      expect(within(panel).queryByLabelText('管理员发放的兑换码')).not.toBeInTheDocument()
      expect(within(panel).getByLabelText('当前密码（重试原操作）')).toHaveValue('')
      fireEvent.change(within(panel).getByLabelText('当前密码（重试原操作）'), { target: { value: 'a-long-self-password' } })
      fireEvent.click(within(panel).getByRole('checkbox', { name: /我确认重试原兑换操作/ }))
      await act(async () => { fireEvent.submit(within(panel).getByRole('button', { name: '重试原兑换' }).closest('form')!) })
      expect(submitted).toHaveLength(2)
      expect(submitted[1].operation_id).toBe(submitted[0].operation_id)
      await act(async () => { await vi.advanceTimersByTimeAsync(2 * 60 * 1000) })
      expect(within(panel).getByRole('alert')).toHaveTextContent('请让管理员通过安全审计核对')
      expect(within(panel).queryByRole('button', { name: '确认兑换' })).not.toBeInTheDocument()
      expect(within(panel).queryByRole('button', { name: '重试原兑换' })).not.toBeInTheDocument()
      expect(within(panel).queryByLabelText('管理员发放的兑换码')).not.toBeInTheDocument()
    } finally {
      vi.useRealTimers()
    }
  })

  it('clears a definite redemption failure and ignores a late result after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let finish: ((response: Response) => void) | undefined
    let calls = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_redemption: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/redemptions')) {
        calls++
        if (calls === 1) return reply(409, { error: { code: 'redemption_unavailable' } })
        return new Promise<Response>((resolve) => { finish = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '兑换码充值本人钱包' })
    const submit = async () => {
      await userEvent.type(within(panel).getByLabelText('管理员发放的兑换码'), 'cpa_synthetic-code')
      await userEvent.type(within(panel).getByLabelText('当前密码'), 'a-long-self-password')
      await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认将此码兑换/ }))
      await userEvent.click(within(panel).getByRole('button', { name: '确认兑换' }))
    }
    await submit()
    expect(await within(panel).findByText(/兑换暂不可用/)).toBeInTheDocument()
    expect(within(panel).getByLabelText('管理员发放的兑换码')).toHaveValue('')
    await submit()
    await waitFor(() => expect(finish).toBeTypeOf('function'))
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    finish?.(new Response(JSON.stringify({ operation_id: 'late', replay: false, currency: 'EUR', amount_micro: '37', credited_at: '2026-10-02T12:00:00Z' }), { status: 201 }))
    expect(screen.queryByRole('heading', { name: '兑换码充值本人钱包' })).not.toBeInTheDocument()
    expect(screen.queryByText(/兑换成功/)).not.toBeInTheDocument()
  })

  it('reads one typed currency on demand and distinguishes missing from zero', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/balance?currency=USD')) return reply(200, { currency: 'USD', has_account: false, amount_micro: null })
      if (url.endsWith('/billing/balance?currency=EUR')) return reply(200, { currency: 'EUR', has_account: true, amount_micro: '0' })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const wallet = await screen.findByRole('region', { name: '我的钱包余额' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/balance'))).toBe(false)
    await userEvent.type(within(wallet).getByLabelText('币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(wallet).getByRole('button', { name: '读取余额' }))
    expect(await within(wallet).findByText(/暂无员工钱包账户/)).toBeInTheDocument()
    await userEvent.clear(within(wallet).getByLabelText('币种（三位大写字母，如 USD）'))
    expect(within(wallet).queryByText(/暂无员工钱包账户/)).not.toBeInTheDocument()
    await userEvent.type(within(wallet).getByLabelText('币种（三位大写字母，如 USD）'), 'EUR')
    await userEvent.click(within(wallet).getByRole('button', { name: '读取余额' }))
    expect(await within(wallet).findByText('0')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/balance'))).toHaveLength(2)
  })

  it('clears stale wallet results on currency switch, failure and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let finishUSD: ((value: Response) => void) | undefined
    let failEUR = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/balance?currency=USD')) return new Promise<Response>((resolve) => { finishUSD = resolve })
      if (url.endsWith('/billing/balance?currency=EUR')) return failEUR
        ? reply(503, { error: { code: 'storage_unavailable' } })
        : reply(200, { currency: 'EUR', has_account: true, amount_micro: '42' })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const wallet = await screen.findByRole('region', { name: '我的钱包余额' })
    const field = within(wallet).getByLabelText('币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    await userEvent.click(within(wallet).getByRole('button', { name: '读取余额' }))
    await waitFor(() => expect(finishUSD).toBeTypeOf('function'))
    await userEvent.clear(field)
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(wallet).getByRole('button', { name: '读取余额' }))
    expect(await within(wallet).findByText('42')).toBeInTheDocument()
    finishUSD?.(new Response(JSON.stringify({ currency: 'USD', has_account: true, amount_micro: '999' }), { status: 200 }))
    await waitFor(() => expect(within(wallet).queryByText('999')).not.toBeInTheDocument())
    failEUR = true
    await userEvent.click(within(wallet).getByRole('button', { name: '读取余额' }))
    expect(await within(wallet).findByRole('alert')).toBeInTheDocument()
    expect(within(wallet).queryByText('42')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '我的钱包余额' })).not.toBeInTheDocument()
  })

  it('hides recent wallet activity without its own capability and never fetches on mount', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: false } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '我的钱包余额' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '最近钱包变动' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entries'))).toBe(false)
  })

  it('opens one typed currency, distinguishes missing/present-empty, and appends a stable next page', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-01T12:00:00Z', window_end: '2026-10-02T12:00:00Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/entries?currency=JPY&limit=20')) return reply(200, { currency: 'JPY', has_account: false, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/entries?currency=EUR&limit=20')) return reply(200, { currency: 'EUR', has_account: true, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/entries?currency=USD&limit=20')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-02T11:59:59Z', delta_micro: '-42' }], next_cursor: 'opaque-next' })
      if (url.endsWith('/billing/entries?currency=USD&limit=20&cursor=opaque-next')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-02T11:59:58.5Z', delta_micro: '42' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '最近钱包变动' })
    const field = within(panel).getByLabelText('流水币种（三位大写字母，如 USD）')
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entries'))).toBe(false)
    await userEvent.type(field, 'JPY')
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    expect(await within(panel).findByText(/暂无员工钱包账户/)).toBeInTheDocument()
    await userEvent.clear(field)
    expect(within(panel).queryByText(/暂无员工钱包账户/)).not.toBeInTheDocument()
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    expect(await within(panel).findByText(/此窗口暂无变动/)).toBeInTheDocument()
    await userEvent.clear(field)
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    expect(await within(panel).findByText('-42 micro')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多变动' }))
    expect(await within(panel).findByText('+42 micro')).toBeInTheDocument()
    expect(within(panel).getByText('-42 micro')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '加载更多变动' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/entries'))).toHaveLength(4)
  })

  it('clears stale recent activity on currency switch, page failure and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-01T12:00:00Z', window_end: '2026-10-02T12:00:00Z' }
    let finishUSD: ((value: Response) => void) | undefined
    let failEUR = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/entries?currency=USD&limit=20')) return new Promise<Response>((resolve) => { finishUSD = resolve })
      if (url.endsWith('/billing/entries?currency=EUR&limit=20')) return failEUR
        ? reply(503, { error: { code: 'storage_unavailable' } })
        : reply(200, { currency: 'EUR', has_account: true, ...window, items: [{ occurred_at: '2026-10-02T11:59:59Z', delta_micro: '7' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '最近钱包变动' })
    const field = within(panel).getByLabelText('流水币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    await waitFor(() => expect(finishUSD).toBeTypeOf('function'))
    await userEvent.clear(field)
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    expect(await within(panel).findByText('+7 micro')).toBeInTheDocument()
    finishUSD?.(new Response(JSON.stringify({ currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-02T11:59:59Z', delta_micro: '999' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(within(panel).queryByText('+999 micro')).not.toBeInTheDocument())
    failEUR = true
    await userEvent.click(within(panel).getByRole('button', { name: '读取最近变动' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    expect(within(panel).queryByText('+7 micro')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '最近钱包变动' })).not.toBeInTheDocument()
  })

  it('keeps wallet classification absent without its own capability and never fetches on mount', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: false } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('region', { name: '最近钱包变动' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '钱包分录类型' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entry-classifications'))).toBe(false)
  })

  it('opens one currency classification only on click and appends raw kinds without changing old activity', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/entry-classifications?currency=JPY&limit=20')) return reply(200, { currency: 'JPY', has_account: false, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/entry-classifications?currency=EUR&limit=20')) return reply(200, { currency: 'EUR', has_account: true, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/entry-classifications?currency=USD&limit=20')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:59Z', delta_micro: '-42', entry_kind: 'adjustment_debit' }], next_cursor: 'opaque-classification-next' })
      if (url.endsWith('/billing/entry-classifications?currency=USD&limit=20&cursor=opaque-classification-next')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:58Z', delta_micro: '42', entry_kind: 'redemption' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '钱包分录类型' })
    const field = within(panel).getByLabelText('类型视图币种（三位大写字母，如 USD）')
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entry-classifications') || String(url).includes('/billing/entries'))).toBe(false)
    await userEvent.type(field, 'JPY')
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    expect(await within(panel).findByText(/暂无员工钱包账户/)).toBeInTheDocument()
    await userEvent.clear(field)
    expect(within(panel).queryByText(/暂无员工钱包账户/)).not.toBeInTheDocument()
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    expect(await within(panel).findByText(/此窗口暂无分录/)).toBeInTheDocument()
    await userEvent.clear(field)
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    expect(await within(panel).findByText('adjustment_debit')).toBeInTheDocument()
    expect(within(panel).getByText('-42 micro')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多分录' }))
    expect(await within(panel).findByText('redemption')).toBeInTheDocument()
    expect(within(panel).getByText('+42 micro')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '加载更多分录' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/entry-classifications'))).toHaveLength(4)
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entries'))).toBe(false)
  })

  it('clears classification on currency switch, failed read, late response and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    let finishUSD: ((value: Response) => void) | undefined
    let failEUR = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/entry-classifications?currency=USD&limit=20')) return new Promise<Response>((resolve) => { finishUSD = resolve })
      if (url.endsWith('/billing/entry-classifications?currency=EUR&limit=20')) return failEUR
        ? reply(503, { error: { code: 'storage_unavailable' } })
        : reply(200, { currency: 'EUR', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:59Z', delta_micro: '7', entry_kind: 'topup' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '钱包分录类型' })
    const field = within(panel).getByLabelText('类型视图币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    await waitFor(() => expect(finishUSD).toBeTypeOf('function'))
    await userEvent.clear(field)
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    expect(await within(panel).findByText('+7 micro')).toBeInTheDocument()
    finishUSD?.(new Response(JSON.stringify({ currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:59Z', delta_micro: '999', entry_kind: 'redemption' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(within(panel).queryByText('+999 micro')).not.toBeInTheDocument())
    failEUR = true
    await userEvent.click(within(panel).getByRole('button', { name: '读取分录类型' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    expect(within(panel).queryByText('+7 micro')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '钱包分录类型' })).not.toBeInTheDocument()
  })

  it('keeps self redemption credit history capability-only and never fetches on mount', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true, employee_self_redemption_credit_history: false } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('region', { name: '钱包分录类型' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '自助兑换入账历史' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/redemption-credits'))).toBe(false)
  })

  it('reads only one clicked currency and paginates verified local self credits', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true, employee_self_redemption_credit_history: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/redemption-credits?currency=JPY&limit=20')) return reply(200, { currency: 'JPY', has_account: false, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/redemption-credits?currency=EUR&limit=20')) return reply(200, { currency: 'EUR', has_account: true, ...window, items: [], next_cursor: null })
      if (url.endsWith('/billing/redemption-credits?currency=USD&limit=20')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ credited_at: '2026-10-03T11:59:59Z', amount_micro: '42' }], next_cursor: 'opaque-history-next' })
      if (url.endsWith('/billing/redemption-credits?currency=USD&limit=20&cursor=opaque-history-next')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ credited_at: '2026-10-03T11:59:58Z', amount_micro: '43' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '自助兑换入账历史' })
    const field = within(panel).getByLabelText('自助兑换入账币种（三位大写字母，如 USD）')
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/redemption-credits') || String(url).includes('/billing/entries') || String(url).includes('/billing/entry-classifications'))).toBe(false)
    await userEvent.type(field, 'JPY')
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    expect(await within(panel).findByText(/暂无员工钱包账户/)).toBeInTheDocument()
    await userEvent.clear(field)
    expect(within(panel).queryByText(/暂无员工钱包账户/)).not.toBeInTheDocument()
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    expect(await within(panel).findByText(/暂无已验证的本人自助兑换入账/)).toBeInTheDocument()
    await userEvent.clear(field)
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    expect(await within(panel).findByText('+42 micro')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多自助兑换入账' }))
    expect(await within(panel).findByText('+43 micro')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '加载更多自助兑换入账' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/redemption-credits'))).toHaveLength(4)
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/entries') || String(url).includes('/billing/entry-classifications'))).toBe(false)
  })

  it('clears self redemption history on currency switch, failed read, late response and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    let finishUSD: ((value: Response) => void) | undefined
    let failEUR = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true, employee_self_redemption_credit_history: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/redemption-credits?currency=USD&limit=20')) return new Promise<Response>((resolve) => { finishUSD = resolve })
      if (url.endsWith('/billing/redemption-credits?currency=EUR&limit=20')) return failEUR
        ? reply(503, { error: { code: 'storage_unavailable' } })
        : reply(200, { currency: 'EUR', has_account: true, ...window, items: [{ credited_at: '2026-10-03T11:59:59Z', amount_micro: '7' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '自助兑换入账历史' })
    const field = within(panel).getByLabelText('自助兑换入账币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    await waitFor(() => expect(finishUSD).toBeTypeOf('function'))
    await userEvent.clear(field)
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    expect(await within(panel).findByText('+7 micro')).toBeInTheDocument()
    finishUSD?.(new Response(JSON.stringify({ currency: 'USD', has_account: true, ...window, items: [{ credited_at: '2026-10-03T11:59:59Z', amount_micro: '999' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(within(panel).queryByText('+999 micro')).not.toBeInTheDocument())
    failEUR = true
    await userEvent.click(within(panel).getByRole('button', { name: '读取自助兑换入账' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    expect(within(panel).queryByText('+7 micro')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '自助兑换入账历史' })).not.toBeInTheDocument()
  })

  it('shows admin local adjustments only with the capability and explicit single-currency click', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true, employee_self_admin_adjustment_history: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/admin-adjustments?currency=USD&limit=20')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:59Z', delta_micro: '42' }], next_cursor: 'opaque-next' })
      if (url.endsWith('/billing/admin-adjustments?currency=USD&limit=20&cursor=opaque-next')) return reply(200, { currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:58Z', delta_micro: '-7' }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '管理员本地钱包调整记录' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/admin-adjustments'))).toBe(false)
    const field = within(panel).getByLabelText('本地调整币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/admin-adjustments'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取本地调整记录' }))
    expect(await within(panel).findByText('+42 micro')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多本地调整' }))
    expect(await within(panel).findByText('-7 micro')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/admin-adjustments'))).toHaveLength(2)
    await userEvent.clear(field)
    expect(within(panel).queryByText('+42 micro')).not.toBeInTheDocument()
    expect(within(panel).queryByText('-7 micro')).not.toBeInTheDocument()
  })

  it('clears admin adjustment results after a failure and ignores a stale response', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const window = { window_start: '2026-09-02T12:00:00Z', window_end: '2026-10-03T12:00:00Z' }
    let finishUSD: ((value: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true, employee_self_wallet_activity: true, employee_self_wallet_entry_classification: true, employee_self_admin_adjustment_history: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/admin-adjustments?currency=USD&limit=20')) return new Promise<Response>((resolve) => { finishUSD = resolve })
      if (url.endsWith('/billing/admin-adjustments?currency=EUR&limit=20')) return reply(503, { error: { code: 'storage_unavailable' } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '管理员本地钱包调整记录' })
    const field = within(panel).getByLabelText('本地调整币种（三位大写字母，如 USD）')
    await userEvent.type(field, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取本地调整记录' }))
    await waitFor(() => expect(finishUSD).toBeTypeOf('function'))
    await userEvent.clear(field)
    await userEvent.type(field, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取本地调整记录' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    finishUSD?.(new Response(JSON.stringify({ currency: 'USD', has_account: true, ...window, items: [{ occurred_at: '2026-10-03T11:59:59Z', delta_micro: '999' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(within(panel).queryByText('+999 micro')).not.toBeInTheDocument())
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
  })

  it('keeps subscription status absent without its independent capability', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_wallet_balance: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '我的订阅状态' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/subscriptions'))).toBe(false)
  })

  it('reads existing subscription statuses only on click and appends a bound page', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const first = { subscription_id: 'sub-z', interval: 'one_time', status: 'active', started_at: '2026-01-31T08:00:00Z', period_end_at: null, cancelled_at: null }
    const second = { subscription_id: 'sub-y', interval: 'monthly', status: 'expired', started_at: '2026-01-31T08:00:00Z', period_end_at: '2026-02-28T08:00:00.000000000Z', cancelled_at: null }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [first], next_cursor: 'opaque-sub-cursor' })
      if (url.endsWith('/billing/subscriptions?limit=20&cursor=opaque-sub-cursor')) return reply(200, { items: [second], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(screen.queryByRole('region', { name: '我的钱包余额' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/subscriptions'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-z')).toBeInTheDocument()
    expect(within(panel).getByText('一次性')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多订阅' }))
    expect(await within(panel).findByText('sub-y')).toBeInTheDocument()
    expect(within(panel).getByText('已到期')).toBeInTheDocument()
    expect(within(panel).getByText('sub-z')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '加载更多订阅' })).not.toBeInTheDocument()
    expect(within(panel).queryByText(/plan|credit|price|支付/i)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/subscriptions'))).toHaveLength(2)
    expect(within(panel).queryByRole('button', { name: '查看购买时记录' })).not.toBeInTheDocument()
  })

  it('reads only an explicitly selected historical purchase snapshot and clears on target, page, failure and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const item = (subscription_id: string) => ({ subscription_id, interval: 'one_time', status: 'active', started_at: '2026-10-02T08:00:00Z', period_end_at: null, cancelled_at: null })
    const snapshot = (subscription_id: string) => ({ subscription_id, plan_id: `plan-${subscription_id}`, plan_revision: 1, currency: 'USD', interval: 'one_time', price_micro: '10', credit_micro: '20', started_at: '2026-10-02T08:00:00Z', period_end_at: null })
    let pending: ((value: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    let bad = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_purchase_snapshot: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [item('sub-z'), item('sub-a')], next_cursor: 'next' })
      if (url.endsWith('/billing/subscriptions?limit=20&cursor=next')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-z/purchase-snapshot')) {
        if (bad) return reply(200, { ...snapshot('sub-z'), price_micro: 10 })
        return reply(200, snapshot('sub-z'))
      }
      if (url.endsWith('/billing/subscriptions/sub-a/purchase-snapshot')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { pending = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/purchase-snapshot'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const first = await within(panel).findByRole('region', { name: '订阅 sub-z 的本地钱包购买记录' })
    const second = within(panel).getByRole('region', { name: '订阅 sub-a 的本地钱包购买记录' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/purchase-snapshot'))).toBe(false)
    await userEvent.click(within(first).getByRole('button', { name: '查看购买时记录' }))
    expect(await within(first).findByText('plan-sub-z')).toBeInTheDocument()
    expect(within(first).getByText(/不是当前套餐价格/)).toBeInTheDocument()
    await userEvent.click(within(second).getByRole('button', { name: '查看购买时记录' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    expect(within(first).queryByText('plan-sub-z')).not.toBeInTheDocument()
    await userEvent.click(within(first).getByRole('button', { name: '查看购买时记录' }))
    expect(pendingSignal?.aborted).toBe(true)
    pending?.(new Response(JSON.stringify(snapshot('sub-a')), { status: 200 }))
    expect(await within(first).findByText('plan-sub-z')).toBeInTheDocument()
    expect(within(second).queryByText('plan-sub-a')).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多订阅' }))
    expect(within(first).queryByText('plan-sub-z')).not.toBeInTheDocument()
    bad = true
    await userEvent.click(within(first).getByRole('button', { name: '查看购买时记录' }))
    expect(await within(first).findByRole('alert')).toBeInTheDocument()
    expect(within(first).queryByText('plan-sub-z')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('plan-sub-z')).not.toBeInTheDocument()
  })

  it('gates one-hop renewal links and reads only the explicitly selected listed subscription', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const item = (subscription_id: string) => ({ subscription_id, interval: 'monthly', status: 'expired', started_at: '2026-08-31T08:00:00Z', period_end_at: '2026-09-30T08:00:00.000000000Z', cancelled_at: null })
    let enabled = false
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_renewal_links: enabled } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [item('sub-z'), item('sub-a')], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-z/renewal-links')) return reply(200, { subscription_id: 'sub-z', predecessor_id: null, successor_id: 'sub-a' })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    const firstRender = render(<SelfApp />)
    const firstPanel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(firstPanel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(within(firstPanel).queryByRole('button', { name: '查看直接续购关联' })).not.toBeInTheDocument()
    firstRender.unmount()
    enabled = true
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/renewal-links'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const target = await within(panel).findByRole('region', { name: '订阅 sub-z 的直接续购关联' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/renewal-links'))).toBe(false)
    await userEvent.click(within(target).getByRole('button', { name: '查看直接续购关联' }))
    expect(await within(target).findByText('无直接前驱')).toBeInTheDocument()
    expect(within(target).getByText('sub-a')).toBeInTheDocument()
    expect(within(target).getByText(/不是完整链、账单/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/renewal-links'))).toHaveLength(1)
  })

  it('clears renewal links on target, page, failure and logout and ignores a late response', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const item = (subscription_id: string) => ({ subscription_id, interval: 'one_time', status: 'active', started_at: '2026-10-02T08:00:00Z', period_end_at: null, cancelled_at: null })
    let pending: ((value: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    let fail = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_renewal_links: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [item('sub-z'), item('sub-a')], next_cursor: 'next' })
      if (url.endsWith('/billing/subscriptions?limit=20&cursor=next')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-z/renewal-links')) {
        if (fail) return reply(503, { error: { code: 'storage_unavailable' } })
        return reply(200, { subscription_id: 'sub-z', predecessor_id: null, successor_id: 'sub-next' })
      }
      if (url.endsWith('/billing/subscriptions/sub-a/renewal-links')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { pending = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const first = await within(panel).findByRole('region', { name: '订阅 sub-z 的直接续购关联' })
    const second = within(panel).getByRole('region', { name: '订阅 sub-a 的直接续购关联' })
    await userEvent.click(within(first).getByRole('button', { name: '查看直接续购关联' }))
    expect(await within(first).findByText('sub-next')).toBeInTheDocument()
    await userEvent.click(within(second).getByRole('button', { name: '查看直接续购关联' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    expect(within(first).queryByText('sub-next')).not.toBeInTheDocument()
    await userEvent.click(within(first).getByRole('button', { name: '查看直接续购关联' }))
    expect(pendingSignal?.aborted).toBe(true)
    pending?.(new Response(JSON.stringify({ subscription_id: 'sub-a', predecessor_id: null, successor_id: null }), { status: 200 }))
    expect(await within(first).findByText('sub-next')).toBeInTheDocument()
    expect(within(second).queryByText('无直接后继')).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多订阅' }))
    expect(within(first).queryByText('sub-next')).not.toBeInTheDocument()
    fail = true
    await userEvent.click(within(first).getByRole('button', { name: '查看直接续购关联' }))
    expect(await within(first).findByRole('alert')).toBeInTheDocument()
    expect(within(first).queryByText('sub-next')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('sub-next')).not.toBeInTheDocument()
  })

  it('clears subscription results on failure and ignores a late response after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let pending: ((value: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    let failure = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) {
        if (failure) return reply(503, { error: { code: 'storage_unavailable' } })
        return reply(200, { items: [{ subscription_id: 'sub-z', interval: 'one_time', status: 'active', started_at: '2026-01-31T08:00:00Z', period_end_at: null, cancelled_at: null }], next_cursor: 'late' })
      }
      if (url.endsWith('/billing/subscriptions?limit=20&cursor=late')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { pending = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-z')).toBeInTheDocument()
    failure = true
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    expect(within(panel).queryByText('sub-z')).not.toBeInTheDocument()
    failure = false
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-z')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多订阅' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('sub-z')).not.toBeInTheDocument()
    expect(pendingSignal?.aborted).toBe(true)
    pending?.(new Response(JSON.stringify({ items: [{ subscription_id: 'sub-a-late', interval: 'one_time', status: 'active', started_at: '2026-01-31T08:00:00Z', period_end_at: null, cancelled_at: null }], next_cursor: null }), { status: 200 }))
    expect(screen.queryByText('sub-a-late')).not.toBeInTheDocument()
  })

  it('requires a separate confirmation before cancelling an active own subscription and refreshes authority', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let cancelled = false
    const cancelledAt = '2026-10-02T08:00:00Z'
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_cancel: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [
        { subscription_id: 'sub-own', interval: 'one_time', status: cancelled ? 'cancelled' : 'active', revision: cancelled ? 2 : 1, started_at: '2026-10-01T08:00:00Z', period_end_at: null, cancelled_at: cancelled ? cancelledAt : null },
        { subscription_id: 'sub-expired', interval: 'monthly', status: 'expired', revision: 1, started_at: '2026-01-31T08:00:00Z', period_end_at: '2026-02-28T08:00:00.000000000Z', cancelled_at: null },
      ], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-own/cancel') && init?.method === 'POST') {
        cancelled = true
        const body = JSON.parse(String(init.body)) as { operation_id: string }
        return reply(200, { operation_id: body.operation_id, subscription_id: 'sub-own', replay: false, status: 'cancelled', revision: 2, cancelled_at: cancelledAt })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/subscriptions'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-own')).toBeInTheDocument()
    expect(within(panel).getAllByRole('button', { name: '取消此订阅' })).toHaveLength(1)
    await userEvent.click(within(panel).getByRole('button', { name: '取消此订阅' }))
    expect(within(panel).getByText(/不退款、不撤回已授额度/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/cancel'))).toBe(false)
    await userEvent.click(within(panel).getByLabelText('我已阅读并确认取消这条订阅。'))
    const field = within(panel).getByLabelText('当前密码（确认取消）') as HTMLInputElement
    expect(field.type).toBe('password')
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认取消订阅' }))
    await waitFor(() => expect(within(panel).getByText('已取消')).toBeInTheDocument())
    expect(within(panel).getByText(/取消已确认/)).toBeInTheDocument()
    expect(within(panel).queryByLabelText('当前密码（确认取消）')).not.toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/sub-own/cancel'))
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ operation_id: expect.stringMatching(/^self-cancel-[0-9a-f]{32}$/), expected_revision: 1, current_password: 'a-long-self-password' })
    expect(new Headers(call?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/billing/subscriptions?limit=20'))).toHaveLength(2)
  })

  it('keeps only an in-memory retry envelope after an uncertain cancellation', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let attempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_cancel: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [{ subscription_id: 'sub-retry', interval: 'one_time', status: attempts ? 'cancelled' : 'active', revision: attempts ? 2 : 1, started_at: '2026-10-01T08:00:00Z', period_end_at: null, cancelled_at: attempts ? '2026-10-02T08:00:00Z' : null }], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-retry/cancel') && init?.method === 'POST') {
        attempts++
        if (attempts === 1) return reply(503, { error: { code: 'storage_unavailable' } })
        const body = JSON.parse(String(init.body)) as { operation_id: string }
        return reply(200, { operation_id: body.operation_id, subscription_id: 'sub-retry', replay: true, status: 'cancelled', revision: 2, cancelled_at: '2026-10-02T08:00:00Z' })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    window.localStorage.clear()
    window.sessionStorage.clear()
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    await userEvent.click(await within(panel).findByRole('button', { name: '取消此订阅' }))
    await userEvent.click(within(panel).getByLabelText('我已阅读并确认取消这条订阅。'))
    await userEvent.type(within(panel).getByLabelText('当前密码（确认取消）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认取消订阅' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent('取消结果未确认')
    expect(within(panel).queryByText(/取消已确认/)).not.toBeInTheDocument()
    expect(within(panel).queryByLabelText('当前密码（确认取消）')).not.toBeInTheDocument()
    expect(window.localStorage.length).toBe(0)
    expect(window.sessionStorage.length).toBe(0)
    await userEvent.click(within(panel).getByLabelText('我确认显式重试原取消操作。'))
    await userEvent.type(within(panel).getByLabelText('当前密码（重试原操作）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认重试取消' }))
    expect(await within(panel).findByText(/已确认原取消/)).toBeInTheDocument()
    const calls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/sub-retry/cancel'))
    expect(calls).toHaveLength(2)
    const first = JSON.parse(String(calls[0]?.[1]?.body)) as { operation_id: string; expected_revision: number }
    const second = JSON.parse(String(calls[1]?.[1]?.body)) as { operation_id: string; expected_revision: number }
    expect(second.operation_id).toBe(first.operation_id)
    expect(second.expected_revision).toBe(1)
  })

  it('keeps one-shot controls absent without their independent capability', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [{ subscription_id: 'sub-month', interval: 'monthly', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: '2026-11-01T08:00:00.000000000Z', cancelled_at: null }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-month')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '查看续购预约' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/one-shot-renewal'))).toBe(false)
  })

  it('reads an armed monthly reservation only on click and disarms after separate confirmation', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const due = '2026-11-01T08:00:00Z'
    const terminal = '2026-10-02T08:00:00Z'
    let disarmed = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_one_shot_renewal_disarm: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [
        { subscription_id: 'sub-once', interval: 'one_time', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: null, cancelled_at: null },
        { subscription_id: 'sub-month', interval: 'monthly', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: '2026-11-01T08:00:00.000000000Z', cancelled_at: null },
      ], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-month/one-shot-renewal') && init?.method !== 'POST') return reply(200, { subscription_id: 'sub-month', state: disarmed ? 'disarmed' : 'armed', revision: disarmed ? 2 : 1, due_at: due, reason: disarmed ? 'disarmed' : null, terminal_at: disarmed ? terminal : null })
      if (url.endsWith('/billing/subscriptions/sub-month/one-shot-renewal/disarm') && init?.method === 'POST') {
        disarmed = true
        const body = JSON.parse(String(init.body)) as { operation_id: string }
        return reply(200, { operation_id: body.operation_id, subscription_id: 'sub-month', replay: false, state: 'disarmed', revision: 2, due_at: due, reason: 'disarmed', terminal_at: terminal })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/one-shot-renewal'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    expect(await within(panel).findByText('sub-month')).toBeInTheDocument()
    expect(within(panel).getAllByRole('button', { name: '查看续购预约' })).toHaveLength(1)
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/one-shot-renewal'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '查看续购预约' }))
    expect(await within(panel).findByText('已预约，尚未执行')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '撤销这一笔预约' }))
    expect(within(panel).getByText(/不取消当前订阅、不退款/)).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/disarm'))).toBe(false)
    await userEvent.click(within(panel).getByLabelText('我已阅读并确认只撤销这一笔预约。'))
    const password = within(panel).getByLabelText('当前密码（确认撤销预约）') as HTMLInputElement
    expect(password.type).toBe('password')
    await userEvent.type(password, 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认撤销预约' }))
    expect(await within(panel).findByText('已撤销预约')).toBeInTheDocument()
    expect(within(panel).getByText(/撤销已确认/)).toBeInTheDocument()
    expect(within(panel).queryByLabelText('当前密码（确认撤销预约）')).not.toBeInTheDocument()
    const post = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/one-shot-renewal/disarm'))
    expect(JSON.parse(String(post?.[1]?.body))).toEqual({ operation_id: expect.stringMatching(/^self-one-shot-[0-9a-f]{32}$/), expected_revision: 1, current_password: 'a-long-self-password' })
    expect(new Headers(post?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/sub-month/one-shot-renewal'))).toHaveLength(2)
  })

  it('keeps an uncertain one-shot retry through a failed refresh and aborts late reads on logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const due = '2026-11-01T08:00:00Z'
    const terminal = '2026-10-02T08:00:00Z'
    let attempts = 0
    let reads = 0
    let pending: ((response: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_one_shot_renewal_disarm: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [{ subscription_id: 'sub-retry', interval: 'monthly', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: '2026-11-01T08:00:00.000000000Z', cancelled_at: null }], next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-retry/one-shot-renewal') && init?.method !== 'POST') {
        reads++
        if (reads === 2) return reply(503, { error: { code: 'storage_unavailable' } })
        if (attempts > 1) { pendingSignal = init?.signal as AbortSignal; return new Promise<Response>((resolve) => { pending = resolve }) }
        return reply(200, { subscription_id: 'sub-retry', state: 'armed', revision: 1, due_at: due, reason: null, terminal_at: null })
      }
      if (url.endsWith('/billing/subscriptions/sub-retry/one-shot-renewal/disarm') && init?.method === 'POST') {
        attempts++
        if (attempts === 1) return reply(503, { error: { code: 'storage_unavailable' } })
        const body = JSON.parse(String(init.body)) as { operation_id: string }
        return reply(200, { operation_id: body.operation_id, subscription_id: 'sub-retry', replay: true, state: 'disarmed', revision: 2, due_at: due, reason: 'disarmed', terminal_at: terminal })
      }
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return reply(200, { ok: true })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    window.localStorage.clear(); window.sessionStorage.clear()
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    await userEvent.click(await within(panel).findByRole('button', { name: '查看续购预约' }))
    await userEvent.click(await within(panel).findByRole('button', { name: '撤销这一笔预约' }))
    await userEvent.click(within(panel).getByLabelText('我已阅读并确认只撤销这一笔预约。'))
    await userEvent.type(within(panel).getByLabelText('当前密码（确认撤销预约）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认撤销预约' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent('撤销结果未确认')
    expect(window.localStorage.length).toBe(0); expect(window.sessionStorage.length).toBe(0)
    await userEvent.click(within(panel).getByRole('button', { name: '查看续购预约' }))
    expect(await within(panel).findByRole('alert')).toHaveTextContent('预约状态暂时无法读取')
    expect(within(panel).getByLabelText('我确认显式重试原撤销操作。')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/one-shot-renewal/disarm'))).toHaveLength(1)
    await userEvent.click(within(panel).getByLabelText('我确认显式重试原撤销操作。'))
    await userEvent.type(within(panel).getByLabelText('当前密码（重试原撤销）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认重试撤销' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    const calls = fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/one-shot-renewal/disarm'))
    expect(calls).toHaveLength(2)
    expect(JSON.parse(String(calls[0]?.[1]?.body)).operation_id).toBe(JSON.parse(String(calls[1]?.[1]?.body)).operation_id)
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(pendingSignal?.aborted).toBe(true)
    pending?.(new Response(JSON.stringify({ subscription_id: 'sub-retry', state: 'armed', revision: 1, due_at: due, reason: null, terminal_at: null }), { status: 200 }))
    expect(screen.queryByText('已预约，尚未执行')).not.toBeInTheDocument()
  })

  it('clears the first reservation confirmation and password when switching monthly targets', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const due = '2026-11-01T08:00:00Z'
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_one_shot_renewal_disarm: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: ['sub-z', 'sub-a'].map(subscription_id => ({ subscription_id, interval: 'monthly', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: '2026-11-01T08:00:00.000000000Z', cancelled_at: null })), next_cursor: null })
      if (url.endsWith('/billing/subscriptions/sub-z/one-shot-renewal')) return reply(200, { subscription_id: 'sub-z', state: 'armed', revision: 1, due_at: due, reason: null, terminal_at: null })
      if (url.endsWith('/billing/subscriptions/sub-a/one-shot-renewal')) return reply(200, { subscription_id: 'sub-a', state: 'none', revision: 0, due_at: null, reason: null, terminal_at: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const first = await within(panel).findByRole('region', { name: '订阅 sub-z 的一次性续购预约' })
    const second = within(panel).getByRole('region', { name: '订阅 sub-a 的一次性续购预约' })
    await userEvent.click(within(first).getByRole('button', { name: '查看续购预约' }))
    await userEvent.click(await within(first).findByRole('button', { name: '撤销这一笔预约' }))
    await userEvent.type(within(first).getByLabelText('当前密码（确认撤销预约）'), 'a-long-self-password')
    await userEvent.click(within(second).getByRole('button', { name: '查看续购预约' }))
    expect(await within(second).findByText('没有一次性续购预约')).toBeInTheDocument()
    expect(within(first).queryByLabelText('当前密码（确认撤销预约）')).not.toBeInTheDocument()
    expect(within(first).queryByText('已预约，尚未执行')).not.toBeInTheDocument()
  })

  it('keeps the plan catalog absent without its independent capability', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: false } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '当前可用套餐目录' })).not.toBeInTheDocument()
    expect(screen.queryByText(neutralCatalogCopy)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/plans'))).toBe(false)
  })

  it('reads the current plan catalog only after currency selection and clears old quotes when unavailable', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const first = { plan_id: 'plan-a', name: 'Current monthly', interval: 'monthly', price_micro: '13', credit_micro: '29', revision: 2 }
    const second = { plan_id: 'plan-b', name: 'Current once', interval: 'one_time', price_micro: '10', credit_micro: '20', revision: 1 }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) return reply(200, { currency: 'USD', available: true, items: [first], next_cursor: 'opaque-plan-cursor' })
      if (url.endsWith('/billing/plans?currency=USD&limit=20&cursor=opaque-plan-cursor')) return reply(200, { currency: 'USD', available: true, items: [second], next_cursor: null })
      if (url.endsWith('/billing/plans?currency=EUR&limit=20')) return reply(200, { currency: 'EUR', available: false, items: [], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '当前可用套餐目录' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/plans'))).toBe(false)
    const input = within(panel).getByLabelText('套餐币种（三位大写字母，如 USD）')
    await userEvent.type(input, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('Current monthly')).toBeInTheDocument()
    expect(within(panel).getByText('13 micro USD')).toBeInTheDocument()
    expect(within(panel).getByText(neutralCatalogCopy)).toBeInTheDocument()
    expect(within(panel).queryByText(/购买、取消与续购仍须管理员操作/)).not.toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本人钱包购买一次性套餐' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('purchase-quotes'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多套餐' }))
    expect(await within(panel).findByText('Current once')).toBeInTheDocument()
    expect(within(panel).getByText('Current monthly')).toBeInTheDocument()
    await userEvent.clear(input)
    expect(within(panel).queryByText('Current monthly')).not.toBeInTheDocument()
    await userEvent.type(input, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('当前套餐目录不可用；未展示旧报价。')).toBeInTheDocument()
    expect(within(panel).queryByText('Current monthly')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/billing/plans'))).toHaveLength(3)
  })

  it('keeps the catalog read-only while separately authorized cancellation remains available', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true, employee_self_plan_purchase: false, employee_self_subscription_status: true, employee_self_subscription_cancel: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) return reply(200, { currency: 'USD', available: true, items: [{ plan_id: 'plan-one', name: 'Current one-time', interval: 'one_time', price_micro: '13', credit_micro: '29', revision: 2 }], next_cursor: null })
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [{ subscription_id: 'sub-active', interval: 'one_time', status: 'active', revision: 1, started_at: '2026-10-01T08:00:00Z', period_end_at: null, cancelled_at: null }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const catalog = await screen.findByRole('region', { name: '当前可用套餐目录' })
    const subscriptions = screen.getByRole('region', { name: '我的订阅状态' })
    expect(within(catalog).getByText(neutralCatalogCopy)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本人钱包购买一次性套餐' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/plans') || String(url).includes('/billing/subscriptions'))).toBe(false)
    await userEvent.type(within(catalog).getByLabelText('套餐币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(catalog).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(catalog).findByText('Current one-time')).toBeInTheDocument()
    expect(within(catalog).queryByRole('button', { name: /购买|取消|续购/ })).not.toBeInTheDocument()
    await userEvent.click(within(subscriptions).getByRole('button', { name: '读取我的订阅状态' }))
    await within(subscriptions).findByText('sub-active')
    await userEvent.click(within(subscriptions).getByRole('button', { name: '取消此订阅' }))
    expect(within(subscriptions).getByRole('heading', { name: '确认取消本人订阅' })).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('purchase-quotes') || String(url).endsWith('/cancel'))).toBe(false)
  })

  it('keeps the catalog read-only while separately authorized monthly renewal remains available', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true, employee_self_plan_purchase: false, employee_self_wallet_balance: true, employee_self_subscription_status: true, employee_self_subscription_renewal: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) return reply(200, { currency: 'USD', available: true, items: [{ plan_id: 'plan-monthly', name: 'Current monthly', interval: 'monthly', price_micro: '13', credit_micro: '29', revision: 2 }], next_cursor: null })
      if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [
        { subscription_id: 'sub-expired', interval: 'monthly', status: 'expired', started_at: '2026-01-31T08:00:00Z', period_end_at: '2026-02-28T08:00:00.000000000Z', cancelled_at: null },
        { subscription_id: 'sub-active', interval: 'monthly', status: 'active', started_at: '2026-10-01T08:00:00Z', period_end_at: '2026-11-01T08:00:00.000000000Z', cancelled_at: null },
      ], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const catalog = await screen.findByRole('region', { name: '当前可用套餐目录' })
    const subscriptions = screen.getByRole('region', { name: '我的订阅状态' })
    expect(within(catalog).getByText(neutralCatalogCopy)).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本人钱包购买一次性套餐' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/plans') || String(url).includes('/billing/subscriptions') || String(url).includes('/billing/balance'))).toBe(false)
    await userEvent.type(within(catalog).getByLabelText('套餐币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(catalog).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(catalog).findByText('Current monthly')).toBeInTheDocument()
    expect(within(catalog).queryByRole('button', { name: /购买|取消|续购/ })).not.toBeInTheDocument()
    await userEvent.click(within(subscriptions).getByRole('button', { name: '读取我的订阅状态' }))
    const renewal = await within(subscriptions).findByRole('region', { name: '订阅 sub-expired 的月度续购' })
    expect(within(renewal).getByRole('button', { name: '获取此订阅的续购报价' })).toBeInTheDocument()
    expect(within(subscriptions).getAllByRole('button', { name: '获取此订阅的续购报价' })).toHaveLength(1)
    expect(within(subscriptions).queryByRole('button', { name: '取消此订阅' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('purchase-quotes') || String(url).includes('renewal-quotes') || String(url).endsWith('/renew'))).toBe(false)
  })

  it('accepts catalog page order using the server UTF-8 ID order', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const plan = (planID: string, name: string) => ({ plan_id: planID, name, interval: 'monthly', price_micro: '10', credit_micro: '20', revision: 1 })
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) return reply(200, { currency: 'USD', available: true, items: [plan('plan one', 'Space ID')], next_cursor: 'p1' })
      if (url.endsWith('/billing/plans?currency=USD&limit=20&cursor=p1')) return reply(200, { currency: 'USD', available: true, items: [plan('\uE000', 'BMP ID')], next_cursor: 'p2' })
      if (url.endsWith('/billing/plans?currency=USD&limit=20&cursor=p2')) return reply(200, { currency: 'USD', available: true, items: [plan('😀', 'Supplementary ID')], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '当前可用套餐目录' })
    await userEvent.type(within(panel).getByLabelText('套餐币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('Space ID')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多套餐' }))
    expect(await within(panel).findByText('BMP ID')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多套餐' }))
    expect(await within(panel).findByText('Supplementary ID')).toBeInTheDocument()
    expect(within(panel).queryByRole('alert')).not.toBeInTheDocument()
  })

  it('clears failed plan reads and prevents late catalog results after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let pending: ((value: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    let failure = false
    const first = { plan_id: 'plan-a', name: 'Current monthly', interval: 'monthly', price_micro: '13', credit_micro: '29', revision: 2 }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true } })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) {
        if (failure) return reply(503, { error: { code: 'storage_unavailable' } })
        return reply(200, { currency: 'USD', available: true, items: [first], next_cursor: 'late' })
      }
      if (url.endsWith('/billing/plans?currency=USD&limit=20&cursor=late')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { pending = resolve })
      }
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '当前可用套餐目录' })
    await userEvent.type(within(panel).getByLabelText('套餐币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('Current monthly')).toBeInTheDocument()
    failure = true
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByRole('alert')).toBeInTheDocument()
    expect(within(panel).queryByText('Current monthly')).not.toBeInTheDocument()
    failure = false
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('Current monthly')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '加载更多套餐' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('Current monthly')).not.toBeInTheDocument()
    expect(pendingSignal?.aborted).toBe(true)
    pending?.(new Response(JSON.stringify({ currency: 'USD', available: true, items: [{ ...first, plan_id: 'plan-b', name: 'Late plan' }], next_cursor: null }), { status: 200 }))
    expect(screen.queryByText('Late plan')).not.toBeInTheDocument()
  })

  it('abandons a pending plan quote when currency changes', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let pending: ((value: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_plan_catalog: true } })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/billing/plans?currency=USD&limit=20')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { pending = resolve })
      }
      if (url.endsWith('/billing/plans?currency=EUR&limit=20')) return reply(200, { currency: 'EUR', available: true, items: [{ plan_id: 'plan-eur', name: 'EUR plan', interval: 'one_time', price_micro: '7', credit_micro: '9', revision: 1 }], next_cursor: null })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '当前可用套餐目录' })
    const input = within(panel).getByLabelText('套餐币种（三位大写字母，如 USD）')
    await userEvent.type(input, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    await waitFor(() => expect(pending).toBeTypeOf('function'))
    await userEvent.clear(input)
    expect(pendingSignal?.aborted).toBe(true)
    await userEvent.type(input, 'EUR')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('EUR plan')).toBeInTheDocument()
    pending?.(new Response(JSON.stringify({ currency: 'USD', available: true, items: [{ plan_id: 'plan-usd', name: 'Late USD plan', interval: 'monthly', price_micro: '10', credit_micro: '20', revision: 1 }], next_cursor: null }), { status: 200 }))
    expect(within(panel).queryByText('Late USD plan')).not.toBeInTheDocument()
    expect(within(panel).getByText('EUR plan')).toBeInTheDocument()
  })

  it('logs in with the independent endpoint and displays only owned metadata', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: 'Research', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(401, { error: { code: 'authentication_required' } })
      if (url.endsWith('/sessions') && init?.method === 'POST') return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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
    expect(screen.queryByRole('heading', { name: '我的 API Key' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '我的请求记录' })).not.toBeInTheDocument()
    expect(screen.queryByRole('heading', { name: '我的 Token 用量' })).not.toBeInTheDocument()
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
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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
      : String(input).endsWith('/usage/summary') ? reply(200, emptyTokenSummary)
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
      : String(input).endsWith('/usage/summary') ? reply(200, emptyTokenSummary)
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
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: [first], next_cursor: 'v1.cursor' })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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

  it('requires a second explicit step and current password before revoking an owned Key', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const key = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' }
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) {
        reads++
        return reply(200, { items: [{ ...key, revoked_at: reads > 1 ? '2026-10-01T02:00:00Z' : null, status: reads > 1 ? 'revoked' : 'active' }], next_cursor: null })
      }
      if (url.endsWith('/keys/key_one/revoke') && init?.method === 'POST') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '撤销 Editor' }))
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/revoke'))).toHaveLength(0)
    const field = screen.getByLabelText('当前密码（撤销确认）') as HTMLInputElement
    expect(field.type).toBe('password')
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认撤销 Key' }))
    await waitFor(() => expect(screen.getByText('已撤销')).toBeInTheDocument())
    expect(screen.queryByRole('button', { name: '撤销 Editor' })).not.toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/keys/key_one/revoke'))
    expect(String(call?.[0])).toBe('/self/api/v1/keys/key_one/revoke')
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ current_password: 'a-long-self-password' })
    expect(new Headers(call?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    expect(new Headers(call?.[1]?.headers).get('X-Self-Request')).toBe('1')
    expect(reads).toBe(2)
  })

  it('clears a failed revocation password and does not optimistically change Key state', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const key = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: [key], next_cursor: null })
      if (url.endsWith('/keys/key_one/revoke')) return reply(503, { error: { code: 'storage_unavailable' } })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '撤销 Editor' }))
    const field = screen.getByLabelText('当前密码（撤销确认）') as HTMLInputElement
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认撤销 Key' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('结果未确认'))
    expect(field.value).toBe('')
    expect(screen.getByText('有效')).toBeInTheDocument()
    expect(screen.queryByText('a-long-self-password')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByLabelText('当前密码（撤销确认）')).not.toBeInTheDocument()
  })

  it('unmounts and clears a pending revocation when the employee logs out', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const key = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' }
    let resolveRevoke: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [key], next_cursor: null })
      if (url.endsWith('/keys/key_one/revoke')) return new Promise<Response>((resolve) => { resolveRevoke = resolve })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '撤销 Editor' }))
    const field = screen.getByLabelText('当前密码（撤销确认）') as HTMLInputElement
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认撤销 Key' }))
    expect(field.value).toBe('')
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    resolveRevoke?.(new Response(null, { status: 204 }))
    await waitFor(() => expect(screen.queryByText('Editor')).not.toBeInTheDocument())
    expect(screen.queryByLabelText('当前密码（撤销确认）')).not.toBeInTheDocument()
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
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
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

  it('renders only known upstream-attempt Token sums and unknown attempt counts as strings', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const summary = {
      ...emptyTokenSummary,
      requests: { total: '3', pending: '1', succeeded: '1', failed: '1', cancelled: '0', interrupted: '0' },
      attempts: { ...emptyTokenSummary.attempts, total: '4', pending: '1', input_tokens: { known_total: '9007199254740993', unknown_attempts: '2' }, output_tokens: { known_total: '0', unknown_attempts: '1' }, provider: 'private-provider', account_id: 'private-account', cost_micro: '999' },
    }
    const fetchMock = vi.fn((input: RequestInfo | URL, _init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, summary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByText('9007199254740993')).toBeInTheDocument()
    const section = screen.getByRole('region', { name: '我的 Token 用量' })
    expect(section).toHaveTextContent('已知 Token（上游尝试）')
    expect(section).toHaveTextContent('未知尝试')
    expect(section).toHaveTextContent('不是完整用量，也不是账单')
    expect(section).toHaveTextContent('待结束尝试')
    expect(screen.queryByText('private-provider')).not.toBeInTheDocument()
    expect(screen.queryByText('private-account')).not.toBeInTheDocument()
    expect(screen.queryByText('999')).not.toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/usage/summary'))
    expect(String(call?.[0])).toBe('/self/api/v1/usage/summary')
    expect(new Headers(call?.[1]?.headers).get('X-Self-Request')).toBe('1')
  })

  it('does not retain failed or late summary data after logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let resolveSummary: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return new Promise<Response>((resolve) => { resolveSummary = resolve })
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByText('正在读取 Token 汇总…')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    resolveSummary?.(new Response(JSON.stringify({ ...emptyTokenSummary, attempts: { ...emptyTokenSummary.attempts, input_tokens: { known_total: 'secret-late', unknown_attempts: '0' } } }), { status: 200 }))
    await waitFor(() => expect(screen.queryByText('secret-late')).not.toBeInTheDocument())
    expect(screen.queryByRole('heading', { name: '我的 Token 用量' })).not.toBeInTheDocument()
  })

  it('clears the Token summary on a failed read', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(503, { error: { code: 'storage_unavailable' } })
      throw new Error(`Unexpected request: ${url}`)
    }))
    const { container } = render(<SelfApp />)
    expect(await screen.findByText('Token 汇总暂时无法读取，请稍后重新登录或刷新页面。')).toBeInTheDocument()
    expect(container.querySelector('.self-summary-counts')).toBeNull()
    expect(container.querySelector('.self-summary-tokens')).toBeNull()
  })

  it('uses two steps and a transient password to sign out other devices while preserving this session', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions/revoke-others') && init?.method === 'POST') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '退出其他设备' }))
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/sessions/revoke-others'))).toHaveLength(0)
    const field = screen.getByLabelText('当前密码（退出其他设备）') as HTMLInputElement
    expect(field.type).toBe('password')
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认退出其他设备' }))
    expect(await screen.findByRole('status')).toHaveTextContent('其他设备已退出；当前设备仍保持登录')
    expect(screen.getByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByLabelText('当前密码（退出其他设备）')).not.toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/sessions/revoke-others'))
    expect(String(call?.[0])).toBe('/self/api/v1/sessions/revoke-others')
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ current_password: 'a-long-self-password' })
    expect(new Headers(call?.[1]?.headers).get('X-Self-Request')).toBe('1')
    expect(new Headers(call?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/sessions/revoke-others'))).toHaveLength(1)
  })

  it('clears the password and avoids a success claim after uncertain sign-out', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions/revoke-others')) return reply(503, { error: { code: 'storage_unavailable' } })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '退出其他设备' }))
    const field = screen.getByLabelText('当前密码（退出其他设备）') as HTMLInputElement
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认退出其他设备' }))
    await waitFor(() => expect(screen.getByRole('alert')).toHaveTextContent('结果未确认'))
    expect(field.value).toBe('')
    expect(screen.queryByText('其他设备已退出；当前设备仍保持登录。')).not.toBeInTheDocument()
    expect(screen.queryByText('a-long-self-password')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '取消' }))
    expect(screen.queryByLabelText('当前密码（退出其他设备）')).not.toBeInTheDocument()
  })

  it('aborts a pending other-device sign-out and clears its password on logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let pendingSignal: AbortSignal | undefined
    let resolveMutation: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions/revoke-others')) { pendingSignal = init?.signal ?? undefined; return new Promise<Response>((resolve) => { resolveMutation = resolve }) }
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '退出其他设备' }))
    const field = screen.getByLabelText('当前密码（退出其他设备）') as HTMLInputElement
    await userEvent.type(field, 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认退出其他设备' }))
    expect(field.value).toBe('')
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(pendingSignal?.aborted).toBe(true)
    resolveMutation?.(new Response(null, { status: 204 }))
    expect(screen.queryByText('其他设备已退出；当前设备仍保持登录。')).not.toBeInTheDocument()
  })

  it('requires explicit confirmation and shows an authorized Key exactly once', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let issued = false
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [{ id: 'key_reserved', name: 'Workstation', expires_at: null }] })
      if (url.endsWith('/keys/issue') && init?.method === 'POST') { issued = true; return reply(201, { id: 'key_reserved', name: 'Workstation', expires_at: null, key: 'cpac_once-only-secret' }) }
      if (url.endsWith('/keys')) return reply(200, { items: issued ? [{ id: 'key_reserved', name: 'Workstation', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' }] : [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '领取此 Key' }))
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/keys/issue'))).toHaveLength(0)
    await userEvent.type(screen.getByLabelText('当前密码（领取确认）'), 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认领取 Key' }))
    expect(await screen.findByTestId('self-issued-key')).toHaveTextContent('cpac_once-only-secret')
    const call = fetchMock.mock.calls.find(([url]) => String(url).endsWith('/keys/issue'))
    expect(JSON.parse(String(call?.[1]?.body))).toEqual({ slot_id: 'key_reserved', current_password: 'a-long-self-password' })
    expect(new Headers(call?.[1]?.headers).get('X-CSRF-Token')).toBe('self-csrf')
    await userEvent.click(screen.getByRole('button', { name: '我已保存，关闭' }))
    expect(screen.queryByText('cpac_once-only-secret')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('cpac_once-only-secret')).not.toBeInTheDocument()
  })

  it('aborts an in-flight Key issue on logout without rendering a late secret', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    let issueSignal: AbortSignal | undefined
    let finishIssue: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [{ id: 'key_reserved', name: 'Workstation', expires_at: null }] })
      if (url.endsWith('/keys/issue')) { issueSignal = init?.signal ?? undefined; return new Promise<Response>((resolve) => { finishIssue = resolve }) }
      if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '领取此 Key' }))
    await userEvent.type(screen.getByLabelText('当前密码（领取确认）'), 'a-long-self-password')
    await userEvent.click(screen.getByRole('button', { name: '确认领取 Key' }))
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(issueSignal?.aborted).toBe(true)
    finishIssue?.(new Response(JSON.stringify({ id: 'key_reserved', name: 'Workstation', expires_at: null, key: 'cpac_late-secret' }), { status: 201 }))
    expect(screen.queryByText('cpac_late-secret')).not.toBeInTheDocument()
  })

  it('fetches only the selected Key summary and ignores a late response after switching or logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const keys = [
      { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' },
      { id: 'key_two', name: 'Archive', created_at: '2026-10-01T02:00:00Z', expires_at: '2026-10-01T03:00:00Z', revoked_at: null, status: 'expired' },
    ]
    let firstSignal: AbortSignal | undefined
    let resolveFirst: ((response: Response) => void) | undefined
    const secondSummary = { ...emptyTokenSummary, requests: { ...emptyTokenSummary.requests, total: '2' }, attempts: { ...emptyTokenSummary.attempts, total: '3', pending: '1', input_tokens: { known_total: '9007199254740993', unknown_attempts: '1' } } }
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: keys, next_cursor: null })
      if (url.endsWith('/keys/key_one/usage/summary')) { firstSignal = init?.signal ?? undefined; return new Promise<Response>((resolve) => { resolveFirst = resolve }) }
      if (url.endsWith('/keys/key_two/usage/summary')) return reply(200, secondSummary)
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    window.localStorage.clear()
    window.sessionStorage.clear()
    render(<SelfApp />)
    expect(await screen.findByText('Editor')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/keys/') && String(url).endsWith('/usage/summary'))).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: '查看 Editor 的已知 Token' }))
    expect(await screen.findByText('正在读取此 Key 的 Token 汇总…')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '查看 Archive 的已知 Token' }))
    expect(firstSignal?.aborted).toBe(true)
    const region = await screen.findByRole('region', { name: 'Archive 的已知 Token' })
    expect(region).toHaveTextContent('9007199254740993')
    expect(region).toHaveTextContent('未知尝试')
    expect(region).toHaveTextContent('待结束尝试')
    expect(region).toHaveTextContent('不是完整用量、计费或合规结论')
    resolveFirst?.(new Response(JSON.stringify({ ...secondSummary, attempts: { ...secondSummary.attempts, input_tokens: { known_total: 'stale-secret', unknown_attempts: '0' } } }), { status: 200 }))
    await waitFor(() => expect(screen.queryByText('stale-secret')).not.toBeInTheDocument())
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/keys/') && String(url).endsWith('/usage/summary'))).toHaveLength(2)
    expect(window.localStorage.length).toBe(0)
    expect(window.sessionStorage.length).toBe(0)
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('9007199254740993')).not.toBeInTheDocument()
  })

  it('drops an earlier Key summary before a failed retry and never shows Key policy', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const key = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: '2026-10-01T02:00:00Z', status: 'revoked', policy: { source_cidrs: ['private-policy'] } }
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: [key], next_cursor: null })
      if (url.endsWith('/keys/key_one/usage/summary')) {
        reads++
        return reads === 1 ? reply(200, { ...emptyTokenSummary, attempts: { ...emptyTokenSummary.attempts, input_tokens: { known_total: '777', unknown_attempts: '0' } } }) : reply(503, { error: { code: 'storage_unavailable' } })
      }
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '查看 Editor 的已知 Token' }))
    expect(await screen.findByText('777')).toBeInTheDocument()
    expect(screen.queryByText('private-policy')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '关闭汇总' }))
    expect(screen.queryByText('777')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '查看 Editor 的已知 Token' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('此 Key 的 Token 汇总暂时无法读取')
    expect(screen.queryByText('777')).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).endsWith('/keys/key_one/usage/summary'))).toHaveLength(2)
  })

  it('loads request activity only for the selected Key and discards a late response on switch and logout', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const keys = [
      { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active' },
      { id: 'key_two', name: 'Archive', created_at: '2026-10-01T02:00:00Z', expires_at: null, revoked_at: '2026-10-01T03:00:00Z', status: 'revoked' },
    ]
    const request = { id: 'req-archive', key_id: 'key_two', model_id: 'model-public', status: 'succeeded', started_at: '2026-10-01T04:00:00Z', finished_at: '2026-10-01T04:00:01Z', prompt: 'private-prompt' }
    let firstSignal: AbortSignal | undefined
    let resolveFirst: ((response: Response) => void) | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: keys, next_cursor: null })
      if (url.endsWith('/keys/key_one/usage/requests')) { firstSignal = init?.signal ?? undefined; return new Promise<Response>((resolve) => { resolveFirst = resolve }) }
      if (url.endsWith('/keys/key_two/usage/requests')) return reply(200, { items: [request], next_cursor: null })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      if (url.endsWith('/sessions') && init?.method === 'DELETE') return Promise.resolve(new Response(null, { status: 204 }))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByText('Editor')).toBeInTheDocument()
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/keys/') && String(url).includes('/usage/requests'))).toHaveLength(0)
    await userEvent.click(screen.getByRole('button', { name: '查看 Editor 的请求活动' }))
    expect(await screen.findByText('正在读取此 Key 的请求活动…')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '查看 Archive 的请求活动' }))
    expect(firstSignal?.aborted).toBe(true)
    const region = await screen.findByRole('region', { name: 'Archive 的请求活动' })
    expect(region).toHaveTextContent('req-archive')
    expect(region).toHaveTextContent('不代表完整用量、账单或合规结论')
    expect(region).not.toHaveTextContent('private-prompt')
    resolveFirst?.(new Response(JSON.stringify({ items: [{ ...request, id: 'stale-request', key_id: 'key_one' }], next_cursor: null }), { status: 200 }))
    await waitFor(() => expect(screen.queryByText('stale-request')).not.toBeInTheDocument())
    expect(fetchMock.mock.calls.filter(([url]) => String(url).includes('/keys/') && String(url).includes('/usage/requests'))).toHaveLength(2)
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(await screen.findByRole('heading', { name: '员工登录' })).toBeInTheDocument()
    expect(screen.queryByText('req-archive')).not.toBeInTheDocument()
  })

  it('clears a failed Key activity page and permits an explicit fresh read', async () => {
    const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
    const key = { id: 'key_one', name: 'Editor', created_at: '2026-10-01T01:00:00Z', expires_at: null, revoked_at: null, status: 'active', policy: { source_cidrs: ['private-policy'] } }
    const request = { id: 'req-one', key_id: key.id, model_id: 'model-public', status: 'pending', started_at: '2026-10-01T01:01:00Z', finished_at: null }
    let reads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile })
      if (url.endsWith('/key-slots')) return reply(200, { items: [] })
      if (url.endsWith('/keys')) return reply(200, { items: [key], next_cursor: null })
      if (url.endsWith('/keys/key_one/usage/requests')) {
        reads++
        return reads === 1 ? reply(200, { items: [request], next_cursor: 'opaque-cursor' }) : reply(200, { items: [], next_cursor: null })
      }
      if (url.includes('/keys/key_one/usage/requests?cursor=')) return reply(503, { error: { code: 'storage_unavailable' } })
      if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
      if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    await userEvent.click(await screen.findByRole('button', { name: '查看 Editor 的请求活动' }))
    expect(await screen.findByText('req-one')).toBeInTheDocument()
    expect(screen.queryByText('private-policy')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '加载更多请求' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('此 Key 的请求活动暂时无法读取')
    expect(screen.queryByText('req-one')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '加载更多请求' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '关闭活动' }))
    await userEvent.click(screen.getByRole('button', { name: '查看 Editor 的请求活动' }))
    expect(await screen.findByText('此 Key 最近 24 小时暂无请求活动。')).toBeInTheDocument()
    expect(reads).toBe(2)
  })
})

describe('employee self monthly renewal', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
  const item = (subscription_id: string, status: 'expired' | 'active' = 'expired') => ({
    subscription_id, interval: 'monthly', status, started_at: '2026-08-31T08:00:00Z',
    period_end_at: '2026-09-30T08:00:00.000000000Z', cancelled_at: null,
  })
  const quote = (predecessor_id: string) => ({
    quote_token: 'opaque-renewal-token', predecessor_id,
    predecessor_period_end_at: '2026-09-30T08:00:00.000000000Z', plan_id: 'plan-monthly',
    revision: 2, currency: 'USD', interval: 'monthly', price_micro: '42', credit_micro: '11',
    expires_at: '2026-10-02T01:05:00Z',
  })

  function baseline(url: string, renewal = true) {
    if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { employee_self_subscription_status: true, employee_self_subscription_renewal: renewal } })
    if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
    if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
    if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
    if (url.endsWith('/billing/subscriptions?limit=20')) return reply(200, { items: [item('sub-z'), item('sub-active', 'active'), item('sub-a')], next_cursor: null })
    return null
  }

  it('only offers renewal for explicitly read expired monthly rows with the independent capability', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const response = baseline(String(input), false)
      if (response) return response
      throw new Error(`Unexpected request: ${String(input)}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(within(panel).queryByRole('button', { name: '获取此订阅的续购报价' })).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    await within(panel).findByText('sub-z')
    expect(within(panel).queryByRole('button', { name: '获取此订阅的续购报价' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/renewal-quotes'))).toBe(false)
  })

  it('requires a quote and current password, then shows one committed successor', async () => {
    const submissions: Array<{ body: Record<string, string>; headers: Headers }> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/subscriptions/sub-z/renewal-quotes')) return reply(201, quote('sub-z'))
      if (url.endsWith('/billing/subscriptions/sub-z/renew')) {
        const body = JSON.parse(String(init?.body)) as Record<string, string>
        submissions.push({ body, headers: new Headers(init?.headers) })
        return reply(201, { operation_id: body.operation_id, predecessor_id: 'sub-z', subscription_id: 'sub-new', replay: false,
          plan_id: 'plan-monthly', revision: 2, currency: 'USD', interval: 'monthly', price_micro: '42', credit_micro: '11',
          started_at: '2026-10-02T01:00:00Z', period_end_at: '2026-11-02T01:00:00Z' })
      }
      const response = baseline(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/renewal-quotes'))).toBe(false)
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const renewal = await within(panel).findByRole('region', { name: '订阅 sub-z 的月度续购' })
    expect(within(panel).getAllByRole('button', { name: '获取此订阅的续购报价' })).toHaveLength(2)
    await userEvent.click(within(renewal).getByRole('button', { name: '获取此订阅的续购报价' }))
    expect(await within(renewal).findByRole('heading', { name: '确认本人月度续购' })).toBeInTheDocument()
    expect(within(renewal).getByText(/尚未扣款/)).toBeInTheDocument()
    await userEvent.click(within(renewal).getByRole('checkbox', { name: /我确认从本人现有钱包扣款/ }))
    await userEvent.type(within(renewal).getByLabelText('当前密码（确认续购）'), 'a-long-self-password')
    await userEvent.click(within(renewal).getByRole('button', { name: '确认续购一个月' }))
    expect(await within(panel).findByText(/续购已确认：原订阅 sub-z，新订阅 sub-new/)).toBeInTheDocument()
    expect(submissions).toHaveLength(1)
    expect(Object.keys(submissions[0].body).sort()).toEqual(['current_password', 'operation_id', 'quote_token'])
    expect(submissions[0].body).toMatchObject({ quote_token: 'opaque-renewal-token', current_password: 'a-long-self-password' })
    expect(submissions[0].body.operation_id).toMatch(/^self-renew-[0-9a-f]{32}$/)
    expect(submissions[0].headers.get('X-CSRF-Token')).toBe('self-csrf')
  })

  it('keeps only the same ID/token after uncertainty and drops a late quote on target change', async () => {
    let lateQuote: ((response: Response) => void) | undefined
    let lateSignal: AbortSignal | undefined
    const bodies: Array<Record<string, string>> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/subscriptions/sub-a/renewal-quotes')) {
        lateSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { lateQuote = resolve })
      }
      if (url.endsWith('/billing/subscriptions/sub-z/renewal-quotes')) return reply(201, quote('sub-z'))
      if (url.endsWith('/billing/subscriptions/sub-z/renew')) {
        const body = JSON.parse(String(init?.body)) as Record<string, string>
        bodies.push(body)
        return bodies.length === 1 ? reply(503, { error: { code: 'storage_unavailable' } }) : reply(200, {
          operation_id: body.operation_id, predecessor_id: 'sub-z', subscription_id: 'sub-original', replay: true,
          plan_id: 'plan-monthly', revision: 2, currency: 'USD', interval: 'monthly', price_micro: '42', credit_micro: '11',
          started_at: '2026-10-02T01:00:00Z', period_end_at: '2026-11-02T01:00:00Z',
        })
      }
      const response = baseline(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '我的订阅状态' })
    await userEvent.click(within(panel).getByRole('button', { name: '读取我的订阅状态' }))
    const a = await within(panel).findByRole('region', { name: '订阅 sub-a 的月度续购' })
    const z = within(panel).getByRole('region', { name: '订阅 sub-z 的月度续购' })
    await userEvent.click(within(a).getByRole('button', { name: '获取此订阅的续购报价' }))
    await waitFor(() => expect(lateQuote).toBeTypeOf('function'))
    await userEvent.click(within(z).getByRole('button', { name: '获取此订阅的续购报价' }))
    expect(lateSignal?.aborted).toBe(true)
    lateQuote?.(new Response(JSON.stringify(quote('sub-a')), { status: 201 }))
    expect(within(a).queryByRole('heading', { name: '确认本人月度续购' })).not.toBeInTheDocument()
    await within(z).findByRole('heading', { name: '确认本人月度续购' })
    await userEvent.click(within(z).getByRole('checkbox', { name: /我确认从本人现有钱包扣款/ }))
    await userEvent.type(within(z).getByLabelText('当前密码（确认续购）'), 'a-long-self-password')
    await userEvent.click(within(z).getByRole('button', { name: '确认续购一个月' }))
    expect(await within(z).findByRole('heading', { name: '上次续购结果未确认' })).toBeInTheDocument()
    expect(within(z).queryByRole('heading', { name: '确认本人月度续购' })).not.toBeInTheDocument()
    await userEvent.click(within(z).getByRole('checkbox', { name: /我确认显式重试原续购操作/ }))
    await userEvent.type(within(z).getByLabelText('当前密码（重试原操作）'), 'a-long-self-password')
    await userEvent.click(within(z).getByRole('button', { name: '确认重试续购' }))
    expect(await within(panel).findByText(/已确认原续购：原订阅 sub-z，新订阅 sub-original/)).toBeInTheDocument()
    expect(bodies).toHaveLength(2)
    expect(bodies[1].operation_id).toBe(bodies[0].operation_id)
    expect(bodies[1].quote_token).toBe(bodies[0].quote_token)
  })
})

describe('employee self plan purchase', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  const profile = { id: 'emp-1', name: 'Alice', department: '', status: 'active' }
  const features = { employee_self_wallet_balance: true, employee_self_plan_catalog: true, employee_self_plan_purchase: true }
  const catalog = { currency: 'USD', available: true, items: [
    { plan_id: 'plan-monthly', name: 'Monthly', interval: 'monthly', price_micro: '12', credit_micro: '20', revision: 1 },
    { plan_id: 'plan-once', name: 'Once', interval: 'one_time', price_micro: '40', credit_micro: '10', revision: 1 },
  ], next_cursor: null }
  const quote = { quote_token: 'opaque-quote', plan_id: 'plan-once', revision: 2, currency: 'USD', interval: 'one_time', price_micro: '42', credit_micro: '11', expires_at: '2026-10-02T01:00:00Z' }

  function baseReply(url: string) {
    if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features })
    if (url.endsWith('/keys')) return reply(200, { items: [], next_cursor: null })
    if (url.endsWith('/usage/requests')) return reply(200, { items: [], next_cursor: null })
    if (url.endsWith('/usage/summary')) return reply(200, emptyTokenSummary)
    if (url.endsWith('/billing/plans?currency=USD&limit=20')) return reply(200, catalog)
    if (url.endsWith('/billing/balance?currency=USD')) return reply(200, { currency: 'USD', has_account: true, amount_micro: '100' })
    if (url.endsWith('/billing/plan-purchase-quotes')) return reply(201, quote)
    return null
  }

  it('never mounts a purchase control when its independent capability is absent', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return reply(200, { csrf_token: 'self-csrf', profile, features: { ...features, employee_self_plan_purchase: false } })
      const response = baseReply(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    expect(await screen.findByRole('heading', { name: '你好，Alice' })).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: '本人钱包购买一次性套餐' })).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('purchase-quotes'))).toBe(false)
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/subscriptions') && !String(url).includes('?limit'))).toBe(false)
  })

  it('requires explicit catalog, wallet, frozen quote and password confirmation', async () => {
    const purchases: Array<{ body: Record<string, string>; headers: Headers }> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/subscriptions') && init?.method === 'POST') {
        purchases.push({ body: JSON.parse(String(init.body)) as Record<string, string>, headers: new Headers(init.headers) })
        return reply(201, { operation_id: purchases[0].body.operation_id, subscription_id: 'sub-one', replay: false, plan_id: 'plan-once', revision: 2, currency: 'USD', interval: 'one_time', price_micro: '42', credit_micro: '11' })
      }
      const response = baseReply(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '本人钱包购买一次性套餐' })
    expect(screen.queryByRole('region', { name: '当前可用套餐目录' })).not.toBeInTheDocument()
    expect(screen.queryByText(neutralCatalogCopy)).not.toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([url]) => String(url).includes('/billing/'))).toBe(false)
    await userEvent.type(within(panel).getByLabelText('购买币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    expect(await within(panel).findByText('Monthly')).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: '获取此套餐的冻结报价' })).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('button', { name: '读取本币种本人钱包' }))
    expect(await within(panel).findByText(/钱包当前余额：100 micro/)).toBeInTheDocument()
    expect(within(panel).getAllByRole('button', { name: '获取此套餐的冻结报价' })).toHaveLength(1)
    await userEvent.click(within(panel).getByRole('button', { name: '获取此套餐的冻结报价' }))
    expect(await within(panel).findByRole('heading', { name: '确认冻结报价' })).toBeInTheDocument()
    expect(within(panel).getByText('42 micro')).toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认上述本人钱包扣款/ }))
    await userEvent.type(within(panel).getByLabelText('当前密码（确认购买）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认购买一次性套餐' }))
    expect(await within(panel).findByText(/购买已确认：订阅 sub-one/)).toBeInTheDocument()
    expect(within(panel).queryByRole('heading', { name: '确认冻结报价' })).not.toBeInTheDocument()
    expect(purchases).toHaveLength(1)
    expect(Object.keys(purchases[0].body).sort()).toEqual(['current_password', 'operation_id', 'quote_token'])
    expect(purchases[0].body).toMatchObject({ quote_token: 'opaque-quote', current_password: 'a-long-self-password' })
    expect(purchases[0].body.operation_id).toMatch(/^self-buy-[0-9a-f]{32}$/)
    expect(purchases[0].headers.get('X-CSRF-Token')).toBe('self-csrf')
  })

  it('clears the offer after an uncertain result and explicitly retries the same ID with a new password input', async () => {
    const bodies: Array<Record<string, string>> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/subscriptions') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body)) as Record<string, string>
        bodies.push(body)
        return bodies.length === 1 ? reply(503, { error: { code: 'storage_unavailable' } })
          : reply(200, { operation_id: body.operation_id, subscription_id: 'sub-original', replay: true, plan_id: 'plan-once', revision: 2, currency: 'USD', interval: 'one_time', price_micro: '42', credit_micro: '11' })
      }
      const response = baseReply(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '本人钱包购买一次性套餐' })
    await userEvent.type(within(panel).getByLabelText('购买币种（三位大写字母，如 USD）'), 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    await within(panel).findByText('Once')
    await userEvent.click(within(panel).getByRole('button', { name: '读取本币种本人钱包' }))
    await within(panel).findByText(/钱包当前余额：100 micro/)
    await userEvent.click(within(panel).getByRole('button', { name: '获取此套餐的冻结报价' }))
    await within(panel).findByRole('heading', { name: '确认冻结报价' })
    await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认上述本人钱包扣款/ }))
    await userEvent.type(within(panel).getByLabelText('当前密码（确认购买）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '确认购买一次性套餐' }))
    expect(await within(panel).findByRole('heading', { name: '上次提交结果未确认' })).toBeInTheDocument()
    expect(within(panel).queryByRole('heading', { name: '确认冻结报价' })).not.toBeInTheDocument()
    expect(within(panel).queryByText(/钱包当前余额：100 micro/)).not.toBeInTheDocument()
    await userEvent.click(within(panel).getByRole('checkbox', { name: /我确认显式重试上次操作/ }))
    await userEvent.type(within(panel).getByLabelText('当前密码（重试原操作）'), 'a-long-self-password')
    await userEvent.click(within(panel).getByRole('button', { name: '用同一操作编号重试' }))
    expect(await within(panel).findByText(/已确认原购买：订阅 sub-original/)).toBeInTheDocument()
    expect(bodies).toHaveLength(2)
    expect(bodies[1].operation_id).toBe(bodies[0].operation_id)
    expect(bodies[1].quote_token).toBe(bodies[0].quote_token)
    expect(bodies[1].current_password).toBe('a-long-self-password')
  })

  it('aborts an outstanding purchase quote and never backfills it after currency changes', async () => {
    let resolveQuote: ((response: Response) => void) | undefined
    let pendingSignal: AbortSignal | undefined
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/plan-purchase-quotes')) {
        pendingSignal = init?.signal ?? undefined
        return new Promise<Response>((resolve) => { resolveQuote = resolve })
      }
      const response = baseReply(url)
      if (response) return response
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<SelfApp />)
    const panel = await screen.findByRole('region', { name: '本人钱包购买一次性套餐' })
    const input = within(panel).getByLabelText('购买币种（三位大写字母，如 USD）')
    await userEvent.type(input, 'USD')
    await userEvent.click(within(panel).getByRole('button', { name: '读取当前套餐' }))
    await within(panel).findByText('Once')
    await userEvent.click(within(panel).getByRole('button', { name: '读取本币种本人钱包' }))
    await within(panel).findByText(/钱包当前余额：100 micro/)
    await userEvent.click(within(panel).getByRole('button', { name: '获取此套餐的冻结报价' }))
    await waitFor(() => expect(resolveQuote).toBeTypeOf('function'))
    await userEvent.clear(input)
    expect(pendingSignal?.aborted).toBe(true)
    resolveQuote?.(new Response(JSON.stringify(quote), { status: 201 }))
    expect(within(panel).queryByRole('heading', { name: '确认冻结报价' })).not.toBeInTheDocument()
  })
})
