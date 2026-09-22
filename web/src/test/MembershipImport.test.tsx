import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { UpstreamsPage } from '../pages/UpstreamsPage'

type Route = { status?: number; body?: unknown }
const response = (route: Route) => Promise.resolve(new Response(JSON.stringify(route.body ?? {}), { status: route.status ?? 200, headers: { 'Content-Type': 'application/json' } }))

const apiKeyUpstream = {
  id: 'api-1', name: 'DeepSeek', provider_kind: 'openai-compatible', endpoint: 'https://api.deepseek.com/v1', enabled: true, revision: 1,
}

const membershipUpstream = {
  id: 'codex-1', name: 'Codex 团队账号', provider_kind: 'codex-membership', endpoint: 'https://chatgpt.com/backend-api/codex', enabled: true, revision: 7,
  credential_state: 'imported_unverified', verified_at: null,
}

describe('Codex membership file import', () => {
  beforeEach(() => { vi.restoreAllMocks() })
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('hides import when the feature is disabled while preserving the API-key flow', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ body: { version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { codex_membership_import: false } } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [apiKeyUpstream] } })
      throw new Error(`Unexpected request: ${url}`)
    }))

    render(<UpstreamsPage csrf="csrf" />)
    expect(await screen.findByText(/--experimental-codex-membership/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '导入 Codex auth.json' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '添加上游' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '同步模型' })).toBeEnabled()
    expect(screen.getByText('OpenAI 兼容 API')).toBeInTheDocument()
  })

  it('sends the file as auth_json and reuses operation_id across a failed retry without echoing secrets', async () => {
    const secret = 'secret-token-that-must-never-render'
    let importAttempts = 0
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ body: { version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { codex_membership_import: true } } })
      if (url.endsWith('/upstreams') && !init?.method) return response({ body: { items: [] } })
      if (url.endsWith('/upstreams/codex-import') && init?.method === 'POST') {
        importAttempts += 1
        if (importAttempts === 1) return response({ status: 500, body: { error: { code: 'unexpected_import_failure', message: `bad token ${secret}` } } })
        return response({ body: membershipUpstream })
      }
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<UpstreamsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '导入 Codex auth.json' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.clear(within(dialog).getByLabelText('显示名称'))
    await userEvent.type(within(dialog).getByLabelText('显示名称'), '研发 Codex')
    const authJSON = JSON.stringify({ auth_mode: 'chatgpt', tokens: { access_token: secret, refresh_token: 'refresh-secret' } })
    await userEvent.upload(within(dialog).getByLabelText('Codex auth.json'), new File([authJSON], 'auth.json', { type: 'application/json' }))
    await userEvent.click(within(dialog).getByRole('button', { name: '导入文件' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('无法导入授权文件')
    expect(dialog).not.toHaveTextContent(secret)
    await userEvent.click(within(dialog).getByRole('button', { name: '导入文件' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())

    const calls = fetchMock.mock.calls.filter(([url, init]) => String(url).endsWith('/upstreams/codex-import') && (init as RequestInit)?.method === 'POST')
    expect(calls).toHaveLength(2)
    const first = JSON.parse(String((calls[0][1] as RequestInit).body))
    const second = JSON.parse(String((calls[1][1] as RequestInit).body))
    expect(first).toEqual({ name: '研发 Codex', auth_json: authJSON, operation_id: expect.any(String) })
    expect(second.operation_id).toBe(first.operation_id)
    expect(new Headers((calls[0][1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf')
    expect(document.body).not.toHaveTextContent(secret)
  })

  it('rejects files over 1 MiB before making an import request', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ body: { version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { codex_membership_import: true } } })
      if (url.endsWith('/upstreams')) return response({ body: { items: [] } })
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<UpstreamsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '导入 Codex auth.json' }))
    const dialog = await screen.findByRole('dialog')
    await userEvent.upload(within(dialog).getByLabelText('Codex auth.json'), new File([new Uint8Array(1024 * 1024 + 1)], 'auth.json', { type: 'application/json' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('授权文件不能超过 1 MiB。')
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/upstreams/codex-import'))).toBe(false)
  })

  it('shows membership provider states and keeps the old record after a failed revision-safe reimport', async () => {
    const secret = 'replacement-secret-not-for-dom'
    const items = [
      membershipUpstream,
      { ...membershipUpstream, id: 'codex-2', name: '已验证账号', revision: 2, credential_state: 'verified', verified_at: '2026-09-23T00:00:00Z' },
      { ...membershipUpstream, id: 'codex-3', name: '过期账号', revision: 4, credential_state: 'reauth_required' },
    ]
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response({ body: { version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { codex_membership_import: true } } })
      if (url.endsWith('/upstreams') && !init?.method) return response({ body: { items } })
      if (url.endsWith('/upstreams/codex-1/codex-auth') && init?.method === 'PUT') return response({ status: 409, body: { error: { code: 'revision_conflict', message: secret } } })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)

    render(<UpstreamsPage csrf="csrf" />)
    expect(await screen.findAllByText('Codex 会员')).toHaveLength(3)
    expect(screen.getByText('已导入，未验证')).toBeInTheDocument()
    expect(screen.getByText('已验证')).toBeInTheDocument()
    expect(screen.getByText('需要重新导入')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '同步模型' })).not.toBeInTheDocument()

    const firstRow = screen.getByText('Codex 团队账号').closest('tr')
    expect(firstRow).not.toBeNull()
    await userEvent.click(within(firstRow!).getByRole('button', { name: '重新导入' }))
    const dialog = await screen.findByRole('dialog')
    const authJSON = JSON.stringify({ tokens: { access_token: secret } })
    await userEvent.upload(within(dialog).getByLabelText('Codex auth.json'), new File([authJSON], 'auth.json', { type: 'application/json' }))
    await userEvent.click(within(dialog).getByRole('button', { name: '重新导入文件' }))

    expect(await within(dialog).findByRole('alert')).toHaveTextContent('旧凭据保持不变')
    expect(dialog).not.toHaveTextContent(secret)
    expect(within(firstRow!).getByText('已导入，未验证')).toBeInTheDocument()
    const put = fetchMock.mock.calls.find(([url, init]) => String(url).endsWith('/upstreams/codex-1/codex-auth') && (init as RequestInit)?.method === 'PUT')
    expect(JSON.parse(String((put?.[1] as RequestInit).body))).toEqual({ expected_revision: 7, auth_json: authJSON })
  })
})
