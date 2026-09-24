// Independently authored browser-component tests for CPA Cloud lifecycle management.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ModelsPage } from '../pages/ModelsPage'
import { UpstreamsPage } from '../pages/UpstreamsPage'

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const status = (lifecycle: boolean) => ({ version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { account_lifecycle_management: lifecycle, account_pool_configuration: false, account_pool_routing: false } })
const upstream = { id: 'ups-1', name: 'Synthetic', provider_kind: 'openai-compatible', endpoint: 'https://example.test/v1', enabled: true, revision: 4, archived: false, credential_state: null, verified_at: null }
const model = { id: 'public-model', upstream_id: 'ups-1', upstream_model: 'provider-model', enabled: true, revision: 3, archived: false, archived_at: null }

describe('account lifecycle management UI', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('hides lifecycle actions when an older server omits the capability', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(false))
      if (url.endsWith('/models')) return response({ items: [model] })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<ModelsPage csrf="csrf" />)
    expect(await screen.findByText('public-model')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '修改 / 归档' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '查看已归档' })).not.toBeInTheDocument()
  })

  it('archives a model with its visible revision and an irreversible confirmation', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(true))
      if (url.endsWith('/models') && !init?.method) return response({ items: [model] })
      if (url.endsWith('/upstreams') && !init?.method) return response({ items: [upstream] })
      if (url.endsWith('/models/public-model') && init?.method === 'DELETE') return response({ ...model, enabled: false, archived: true, revision: 4, archive_result: 'archived' })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<ModelsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '修改 / 归档' }))
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('归档不可恢复')
    await userEvent.click(within(dialog).getByRole('button', { name: '永久归档' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith('/models/public-model') && (init as RequestInit)?.method === 'DELETE')).toBe(true))
    const call = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/models/public-model') && (init as RequestInit)?.method === 'DELETE')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ expected_revision: 3 })
    expect(new Headers((call?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf')
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('不能恢复'))
  })

  it('resets an incompatible explicit wire when the model provider changes', async () => {
    const anthropic = { ...upstream, id: 'ups-claude', name: 'Claude', provider_kind: 'anthropic-api-key' }
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(true))
      if (url.endsWith('/models') && !init?.method) return response({ items: [{ ...model, wire_protocol: 'openai-responses' }] })
      if (url.endsWith('/upstreams') && !init?.method) return response({ items: [upstream, anthropic] })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    }))
    render(<ModelsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '修改 / 归档' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('上游 Wire 协议')).toHaveValue('openai-responses')
    await userEvent.selectOptions(within(dialog).getByLabelText('上游连接'), 'ups-claude')
    expect(within(dialog).getByLabelText('上游 Wire 协议')).toHaveValue('legacy-native')
  })

  it('edits and archives upstreams without exposing a saved credential', async () => {
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(true)
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(true))
      if (url.endsWith('/upstreams') && !init?.method) return response({ items: [upstream], server_time: '2026-09-24T00:00:00Z' })
      if (url.endsWith('/upstreams/ups-1') && init?.method === 'DELETE') return response({ ...upstream, enabled: false, archived: true, revision: 5, archive_result: 'archived' })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<UpstreamsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '修改 / 归档' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('替换 API Key')).toHaveValue('')
    expect(dialog).toHaveTextContent('归档会销毁凭据，且不能恢复')
    await userEvent.click(within(dialog).getByRole('button', { name: '永久归档' }))
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith('/upstreams/ups-1') && (init as RequestInit)?.method === 'DELETE')).toBe(true))
    const call = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/upstreams/ups-1') && (init as RequestInit)?.method === 'DELETE')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ expected_revision: 4 })
    expect(confirm).toHaveBeenCalledWith(expect.stringContaining('凭据会被销毁'))
  })
})
