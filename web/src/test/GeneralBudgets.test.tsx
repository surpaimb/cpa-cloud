import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GeneralBudgets } from '../GeneralBudgets'

const at = '2026-09-24T00:00:00.000000000Z'
const policy = {
  id: 'gb-1', scope: { kind: 'employee', id: 'employee-1', protocol: 'openai-responses', model: null }, enabled: true,
  enforcement: 'strict', unknown_mode: 'deny_unknown', token: { limit: 40, window: 'rolling_60s' },
  cost: { limit_micro: null, currency: null, window: null }, revision: 1, created_at: at, updated_at: at,
}

describe('selector-aware general budgets', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('loads on demand and creates a strict policy without losing selector fields', async () => {
    let listCalls = 0
    const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/budgets?')) {
        listCalls++
        return new Response(JSON.stringify({ items: listCalls === 1 ? [] : [policy], next_cursor: null }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (url.endsWith('/budgets') && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        expect(body).toMatchObject({ scope_kind: 'employee', scope_id: 'employee-1', protocol: 'openai-responses', model: null, enabled: true, token_limit: 40, cost_limit_micro: null, currency: null })
        return new Response(JSON.stringify({ operation_id: body.operation_id, resource_kind: 'budget', resource_id: 'gb-1', revision: 1, created_at: at }), { status: 200, headers: { 'Content-Type': 'application/json' } })
      }
      if (url.endsWith('/budgets/gb-1')) return new Response(JSON.stringify(policy), { status: 200, headers: { 'Content-Type': 'application/json' } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<GeneralBudgets csrf="csrf" employees={[{ id: 'employee-1', label: '员工一' }]} keys={[]} groups={[]} />)

    expect(fetchMock).not.toHaveBeenCalled()
    await userEvent.click(screen.getByRole('button', { name: '读取通用预算' }))
    expect(await screen.findByText('还没有通用预算策略')).toBeInTheDocument()
    await userEvent.selectOptions(screen.getByLabelText('作用对象'), 'employee-1')
    await userEvent.selectOptions(screen.getByLabelText('协议 selector'), 'openai-responses')
    await userEvent.type(screen.getByLabelText('60 秒 Token 上限'), '40')
    await userEvent.click(screen.getByRole('button', { name: '创建 strict 预算' }))

    expect(await screen.findByText('strict 启用')).toBeInTheDocument()
    expect(screen.getByText('rolling_60s')).toBeInTheDocument()
    await waitFor(() => expect(listCalls).toBe(2))
  })
})
