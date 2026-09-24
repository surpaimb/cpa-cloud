// Independently authored browser-component acceptance for the automated-backup contract.
import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { App } from '../App'
import { BackupsPage } from '../pages/BackupsPage'

const response = (body: unknown, status = 200) => Promise.resolve(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
const status = (configured: boolean, running = false, keyReady = false) => ({ version: 'test', ready: true, storage: 'sqlite-wal', limitations: [], features: { automated_backups_configuration: configured, automated_backups_running: running, backup_key_provider_ready: keyReady } })
const provider = { id: 'bkp_test', kind: 'windows-dpapi-user', scope: 'current-user', status: 'ready', reason_code: null, active_version: 1, revision: 1, created_by_admin_id: 'adm_test', updated_by_admin_id: 'adm_test', created_at: '2026-09-24T08:00:00Z', updated_at: '2026-09-24T08:00:00Z' }
const plan = { id: 'bpl_test', name: '每日恢复演练', key_provider_id: provider.id, interval_seconds: 86400, retention_count: 7, rehearsal_enabled: true, enabled: false, next_run_at: null, revision: 1, created_by_admin_id: 'adm_test', updated_by_admin_id: 'adm_test', created_at: '2026-09-24T08:00:00Z', updated_at: '2026-09-24T08:00:00Z' }

describe('automated backups page', () => {
  beforeEach(() => vi.restoreAllMocks())
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('does not call backup endpoints when the capability is absent', async () => {
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(false))
      throw new Error(`Unexpected request: ${url}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<BackupsPage csrf="csrf" />)
    expect(await screen.findByText('当前服务不支持自动备份')).toBeInTheDocument()
    expect(fetchMock.mock.calls.some(([input]) => String(input).includes('/backups/'))).toBe(false)
  })

  it('creates a protected provider without accepting a path or exposing key material', async () => {
    let providers: typeof provider[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(true, false, providers.length > 0))
      if (url.endsWith('/backups/key-providers') && init?.method === 'POST') { providers = [provider]; return response(provider, 201) }
      if (url.endsWith('/backups/key-providers')) return response({ items: providers, ready: providers.length > 0, store_ready: true, reason_code: null })
      if (url.endsWith('/backups/plans')) return response({ items: [] })
      if (url.includes('/backups/runs?')) return response({ items: [], next_cursor: null })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<BackupsPage csrf="csrf-token" />)
    await userEvent.click(await screen.findByRole('button', { name: '创建密钥' }))
    const dialog = screen.getByRole('dialog')
    expect(within(dialog).queryByLabelText(/路径|密钥/)).not.toBeInTheDocument()
    await userEvent.click(within(dialog).getByRole('button', { name: '创建受保护密钥' }))
    expect(await screen.findByText(provider.id)).toBeInTheDocument()
    const call = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/backups/key-providers') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((call?.[1] as RequestInit).body))).toEqual({ kind: 'windows-dpapi-user' })
    expect(new Headers((call?.[1] as RequestInit).headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(document.body.textContent).not.toContain('wrapping_key')
  })

  it('creates a bounded plan and starts a manual run with revision CAS', async () => {
    let plans: typeof plan[] = []
    const run = { id: 'brn_test', plan_id: plan.id, plan_revision: 1, key_provider_id: provider.id, key_provider_kind: 'windows-dpapi-user', key_provider_version: 1, trigger_kind: 'manual', requested_by_admin_id: 'adm_test', scheduled_for: '2026-09-24T09:00:00Z', started_at: '2026-09-24T09:00:00Z', finished_at: null, status: 'running', package_name: 'cpa-cloud-bpl_test-20260924T090000Z-brn_test.cpacb', package_size: null, package_retained: false, package_deleted_at: null, verified_at: null, rehearsal_status: 'pending', rehearsed_at: null, error_code: null, created_at: '2026-09-24T09:00:00Z' }
    let runs: typeof run[] = []
    const fetchMock = vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.endsWith('/system/status')) return response(status(true, true, true))
      if (url.endsWith('/backups/key-providers')) return response({ items: [provider], ready: true, store_ready: true, reason_code: null })
      if (url.endsWith('/backups/plans') && init?.method === 'POST') { const body = JSON.parse(String(init.body)); plans = [{ ...plan, ...body }]; return response(plans[0], 201) }
      if (url.endsWith('/backups/plans')) return response({ items: plans })
      if (url.endsWith('/backups/runs') && init?.method === 'POST') { runs = [run]; return response(run, 202) }
      if (url.includes('/backups/runs?')) return response({ items: runs, next_cursor: null })
      throw new Error(`Unexpected request: ${url} ${init?.method ?? 'GET'}`)
    })
    vi.stubGlobal('fetch', fetchMock)
    render(<BackupsPage csrf="csrf" />)
    await userEvent.click(await screen.findByRole('button', { name: '创建计划' }))
    const dialog = screen.getByRole('dialog')
    await userEvent.type(within(dialog).getByLabelText('计划名称'), '每日恢复演练')
    await userEvent.click(within(dialog).getByRole('button', { name: '保存计划' }))
    expect(await screen.findByText('每日恢复演练')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '立即运行' }))
    expect(await screen.findByText('手动运行')).toBeInTheDocument()
    const createPlan = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/backups/plans') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((createPlan?.[1] as RequestInit).body))).toEqual({ name: '每日恢复演练', key_provider_id: 'bkp_test', interval_seconds: 86400, retention_count: 7, rehearsal_enabled: true, enabled: false })
    const createRun = fetchMock.mock.calls.find(([input, init]) => String(input).endsWith('/backups/runs') && (init as RequestInit)?.method === 'POST')
    expect(JSON.parse(String((createRun?.[1] as RequestInit).body))).toEqual({ plan_id: 'bpl_test', expected_revision: 1 })
  })

  it('only exposes backup navigation when the capability is advertised', async () => {
    vi.stubGlobal('fetch', vi.fn((input: RequestInfo | URL) => {
      const url = String(input)
      if (url.endsWith('/session')) return response({ username: 'admin', csrf_token: 'csrf' })
      if (url.endsWith('/system/status')) return response(status(true))
      if (url.endsWith('/employees')) return response({ items: [] })
      throw new Error(`Unexpected request: ${url}`)
    }))
    render(<App />)
    expect(await screen.findByRole('button', { name: '自动备份' })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('heading', { name: '员工与 Key' })).toBeInTheDocument())
  })
})
