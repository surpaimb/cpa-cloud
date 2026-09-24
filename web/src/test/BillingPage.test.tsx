import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { BillingPage } from '../pages/BillingPage'

const json = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const emptyPage = { items: [], next_cursor: null }

describe('administrator commercial management', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('gates navigation by capability and presents the default-off boundary', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return json({ username: 'admin', csrf_token: 'csrf' })
      if (url.endsWith('/system/status')) return json({ version: 'dev', ready: true, storage: 'ready', limitations: [], features: { single_instance_billing: true } })
      if (url.endsWith('/billing/settings')) return json({ enabled: false, revision: 1 })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    await userEvent.click(await screen.findByRole('button', { name: '商业管理' }))
    expect(await screen.findByRole('heading', { name: '商业管理' })).toBeInTheDocument()
    expect(await screen.findByRole('heading', { name: '商业执行默认关闭' })).toBeInTheDocument()
    expect(screen.getByText(/没有真实收款、自动续费/)).toBeInTheDocument()
    expect(screen.queryByText(/流水查询暂不可用/)).not.toBeInTheDocument()
  })

  it('keeps exact decimal strings and reuses the same adjustment operation after response loss', async () => {
    const writes: string[] = []
    let attempts = 0
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/settings')) return json({ enabled: true, revision: 2 })
      if (url.includes('/billing/balances?')) return json({ account_id: 'acct-1', owner: { kind: 'employee', employee_id: 'emp-1', key_id: null, resource_kind: null, resource_id: null }, currency: 'USD', balance_micro: '9007199254740991001' })
      if (url.includes('/billing/entries?')) return json({ error: { code: 'not_found', message: 'not available' } }, 404)
      if (url.endsWith('/billing/adjustments') && init?.method === 'POST') {
        writes.push(String(init.body)); attempts += 1
        if (attempts === 1) return Promise.reject(new TypeError('response lost'))
        return json({ operation_id: JSON.parse(String(init.body)).operation_id, entry_id: 'entry-1', account_id: 'acct-1', owner: { kind: 'employee', employee_id: 'emp-1' }, currency: 'USD', amount_micro: '123456789012345678', balance_micro: '9130656043753336679' })
      }
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<BillingPage csrf="csrf" />)
    await screen.findByText('商业执行已开启')
    await userEvent.type(screen.getByLabelText('员工 ID'), 'emp-1')
    await userEvent.click(screen.getByRole('button', { name: '读取钱包' }))
    expect(await screen.findByText('9,007,199,254,740.991001 USD')).toBeInTheDocument()
    expect(screen.getByText('流水查询暂不可用')).toBeInTheDocument()
    await userEvent.type(screen.getByLabelText('人工调整（micro）'), '123456789012345678')
    await userEvent.click(screen.getByRole('button', { name: '核对调整' }))
    let dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('123,456,789,012.345678 USD')).toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: '确认增加' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('响应可能在写入后丢失')
    await userEvent.click(within(dialog).getByRole('button', { name: '用原操作编号重试' }))
    await waitFor(() => expect(writes).toHaveLength(2))
    expect(writes[1]).toBe(writes[0])
    expect(JSON.parse(writes[0]).amount_micro).toBe('123456789012345678')
    expect(JSON.parse(writes[0]).owner).toEqual({ kind: 'employee', employee_id: 'emp-1' })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(await screen.findByText(/调整已记账/)).toHaveTextContent('123,456,789,012.345678 USD')
  })

  it('shows a redemption code once and never persists it in the list', async () => {
    const secret = 'cpa_one_time_redemption_secret_1234'
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/settings')) return json({ enabled: true, revision: 2 })
      if (url.includes('/billing/redemption-codes?') && !init?.method) return json(emptyPage)
      if (url.endsWith('/billing/redemption-codes') && init?.method === 'POST') return json({ receipt: { operation_id: 'op', resource_kind: 'redemption_code', resource_id: 'code-1', revision: 1, created_at: '2026-09-24T00:00:00Z', replay: false }, redemption_code: { id: 'code-1', amount_micro: '1000000', currency: 'USD', max_uses: 1, uses: 0, expires_at: null, enabled: true, created_at: '2026-09-24T00:00:00Z' }, code: secret })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    }))
    render(<BillingPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '兑换码' }))
    await userEvent.click(await screen.findByRole('button', { name: '创建兑换码' }))
    const form = await screen.findByRole('dialog')
    await userEvent.type(within(form).getByLabelText('每次额度（micro）'), '1000000')
    await userEvent.click(within(form).getByRole('button', { name: '创建并显示一次' }))
    const secretView = await screen.findByTestId('redemption-plaintext')
    expect(secretView).toHaveTextContent(secret)
    await userEvent.click(screen.getByRole('button', { name: '我已安全保存，关闭' }))
    expect(screen.queryByText(secret)).not.toBeInTheDocument()
    expect(document.body.innerHTML).not.toContain(secret)
  })

  it('computes refundable balance with BigInt and labels the synthetic payment boundary', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/billing/settings')) return json({ enabled: true, revision: 2 })
      if (url.includes('/billing/payment-connectors?')) return json({ items: [{ id: 'connector-1', name: '本地夹具', enabled: true, revision: 1, created_at: '2026-09-24T00:00:00Z', updated_at: '2026-09-24T00:00:00Z' }], next_cursor: null })
      if (url.includes('/billing/topups?')) return json({ items: [{ id: 'topup-1', payment_id: 'pay-1', connector_id: 'connector-1', external_reference: 'fixture-1', amount_micro: '9007199254740991001', currency: 'USD', status: 'partially_refunded', refunded_micro: '123456789012345678', revision: 2, created_at: '2026-09-24T00:00:00Z', paid_at: '2026-09-24T00:01:00Z' }], next_cursor: null })
      if (url.includes('/billing/refunds?')) return json(emptyPage)
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<BillingPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '充值与退款' }))
    expect(await screen.findByText('仅合成回调测试契约')).toBeInTheDocument()
    expect(screen.getByText('已部分退款冲正')).toBeInTheDocument()
    expect(screen.getByText('剩余 8,883,742,465,728.645323 USD')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '退款冲正' }))
    expect(await screen.findByText(/不会向外部支付服务商发起退款/)).toBeInTheDocument()
  })

  it('keeps a settings conflict visible and reloads the authoritative revision', async () => {
    let settingsReads = 0
    const writes: RequestInit[] = []
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/billing/settings') && init?.method === 'PUT') {
        writes.push(init)
        return json({ error: { code: 'operation_conflict', message: 'conflict' } }, 409)
      }
      if (url.endsWith('/billing/settings')) {
        settingsReads += 1
        return json(settingsReads === 1 ? { enabled: false, revision: 1 } : { enabled: true, revision: 2 })
      }
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<BillingPage csrf="csrf-settings" />)
    await userEvent.click(await screen.findByRole('button', { name: '开启商业执行' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByRole('button', { name: '确认开启' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('revision 与当前状态冲突')
    expect(within(dialog).getAllByText(/操作编号/).length).toBeGreaterThan(0)
    await userEvent.click(within(dialog).getByRole('button', { name: '重新读取' }))
    expect(await screen.findByRole('heading', { name: '商业执行已开启' })).toBeInTheDocument()
    expect(JSON.parse(String(writes[0].body)).expected_revision).toBe(1)
    expect(new Headers(writes[0].headers).get('X-CSRF-Token')).toBe('csrf-settings')
  })
})
