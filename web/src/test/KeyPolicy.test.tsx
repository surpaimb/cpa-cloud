// Independently authored browser-component tests for KEY-02 access-key policy management.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { EmployeesPage } from '../pages/EmployeesPage'

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const employee = { id: 'emp-1', name: 'Ada', department: 'R&D', status: 'active', model_mode: 'all', models: [], revision: 3 }
const model = { id: 'public-model', upstream_id: 'ups-1', upstream_model: 'provider-model', enabled: true, revision: 2, archived: false }
const allPolicy = {
  revision: 1,
  protocol_mode: 'all',
  protocols: [],
  model_mode: 'all',
  models: [],
  effective_protocols: ['openai-chat', 'openai-responses', 'anthropic-messages', 'gemini-generate-content'],
  effective_models: ['public-model'],
}
const key = { id: 'key-1', name: 'Laptop', expires_at: null, revoked_at: null, policy: allPolicy }

function systemStatus(keyPolicy: boolean | undefined) {
  return { version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: keyPolicy === undefined ? {} : { key_access_policy: keyPolicy } }
}

describe('access-key policy administration', () => {
  beforeEach(() => { vi.restoreAllMocks(); vi.stubGlobal('crypto', { randomUUID: () => 'operation-1' }) })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('creates an explicit selected-empty deny-all policy and reveals the secret only once', async () => {
    const writes: Array<Record<string, unknown>> = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(systemStatus(true))
      if (url.endsWith('/employees') && !init?.method) return response({ items: [employee] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/employees/emp-1/keys') && init?.method === 'POST') {
        writes.push(JSON.parse(String(init.body)) as Record<string, unknown>)
        return response({ ...key, key: 'cpa_plaintext_once' }, 201)
      }
      if (url.endsWith('/employees/emp-1/keys')) return response({ items: [] })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<EmployeesPage csrf="csrf" />)

    await userEvent.click(await screen.findByRole('button', { name: '管理 Key' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.click(within(dialog).getByLabelText('仅指定协议'))
    await userEvent.click(within(dialog).getByLabelText('仅指定模型'))
    expect(dialog).toHaveTextContent('显式 deny-all')
    await userEvent.click(within(dialog).getByRole('button', { name: '生成永久 Key' }))

    expect(await screen.findByTestId('created-key')).toHaveTextContent('cpa_plaintext_once')
    expect(writes).toHaveLength(1)
    expect(writes[0]).toMatchObject({
      name: '默认 Key', operation_id: 'operation-1', expires_at: null,
      policy: { protocol_mode: 'selected', protocols: [], model_mode: 'selected', models: [] },
    })
    await userEvent.click(screen.getByRole('button', { name: '我已保存，关闭' }))
    expect(screen.queryByText('cpa_plaintext_once')).not.toBeInTheDocument()
  })

  it('shows an explicit compatibility state and no fake policy save on an older service', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(systemStatus(undefined))
      if (url.endsWith('/employees') && !init?.method) return response({ items: [employee] })
      if (url.endsWith('/employees/emp-1/keys')) return response({ items: [{ ...key, policy: undefined }] })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    }))
    render(<EmployeesPage csrf="csrf" />)

    await userEvent.click(await screen.findByRole('button', { name: '管理 Key' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('当前服务不支持独立 Key 策略')
    expect(dialog).toHaveTextContent('沿用员工权限')
    expect(within(dialog).queryByRole('button', { name: '编辑独立权限' })).not.toBeInTheDocument()
    expect(within(dialog).queryByRole('button', { name: '保存独立权限' })).not.toBeInTheDocument()
  })

  it('preserves the draft across a conflict and retries against the freshly loaded revision', async () => {
    const policyReads = [
      { ...allPolicy, revision: 2 },
      { ...allPolicy, revision: 3, effective_models: [] },
    ]
    const writes: Array<Record<string, unknown>> = []
    let keyListReads = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(systemStatus(true))
      if (url.endsWith('/employees') && !init?.method) return response({ items: [employee] })
      if (url.endsWith('/models')) return response({ items: [model] })
      if (url.endsWith('/employees/emp-1/keys')) {
        keyListReads += 1
        return response({ items: [{ ...key, policy: keyListReads > 1 ? { ...allPolicy, revision: 4 } : { ...allPolicy, revision: 2 } }] })
      }
      if (url.endsWith('/keys/key-1/policy') && init?.method === 'PUT') {
        writes.push(JSON.parse(String(init.body)) as Record<string, unknown>)
        if (writes.length === 1) return response({ error: { code: 'revision_conflict', message: 'changed' } }, 409)
        return response({ ...allPolicy, revision: 4, protocol_mode: 'selected', protocols: ['openai-chat'] })
      }
      if (url.endsWith('/keys/key-1/policy')) return response(policyReads.shift())
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<EmployeesPage csrf="csrf" />)

    await userEvent.click(await screen.findByRole('button', { name: '管理 Key' }))
    await userEvent.click(await screen.findByRole('button', { name: '编辑独立权限' }))
    const dialogs = await screen.findAllByRole('dialog')
    const editor = dialogs[dialogs.length - 1]
    await userEvent.click(await within(editor).findByLabelText('仅指定协议'))
    await userEvent.click(within(editor).getByLabelText('OpenAI Chat Completions'))
    await userEvent.click(within(editor).getByRole('button', { name: '保存独立权限' }))

    expect(await within(editor).findByText(/策略已被其他操作更新到修订 3/)).toBeInTheDocument()
    expect(within(editor).getByLabelText('OpenAI Chat Completions')).toBeChecked()
    await userEvent.click(within(editor).getByRole('button', { name: '使用最新修订重试' }))
    await waitFor(() => expect(writes).toHaveLength(2))
    expect(writes[0]).toMatchObject({ expected_revision: 2, protocol_mode: 'selected', protocols: ['openai-chat'] })
    expect(writes[1]).toMatchObject({ expected_revision: 3, protocol_mode: 'selected', protocols: ['openai-chat'] })
    expect(new Headers(fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/keys/key-1/policy') && (init as RequestInit)?.method === 'PUT')?.[1]?.headers).get('X-CSRF-Token')).toBe('csrf')
  })
})
