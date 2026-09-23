const API_ROOT = '/admin/api/v1'

export class ApiError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message)
  }
}

type ErrorEnvelope = { error?: { code?: string; message?: string } }

export async function request<T>(path: string, init: RequestInit = {}, csrf?: string): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
  if (csrf) headers.set('X-CSRF-Token', csrf)
  const response = await fetch(`${API_ROOT}${path}`, {
    ...init,
    headers,
    credentials: 'same-origin',
  })
  if (!response.ok) {
    let envelope: ErrorEnvelope = {}
    try {
      envelope = (await response.json()) as ErrorEnvelope
    } catch {
      // Keep a stable, non-sensitive fallback for malformed error responses.
    }
    throw new ApiError(
      response.status,
      envelope.error?.code ?? 'request_failed',
      envelope.error?.message ?? '请求失败，请稍后重试。',
    )
  }
  if (response.status === 204) return undefined as T
  return (await response.json()) as T
}

export type Session = { username: string; csrf_token: string }
export type Employee = {
  id: string
  name: string
  department?: string
  note?: string
  status: 'active' | 'disabled'
  model_mode: 'all' | 'selected'
  models: string[]
  revision: number
}
export type EmployeeKey = {
  id: string
  name: string
  key?: string
  expires_at: string | null
  revoked_at: string | null
}
export type Upstream = {
  id: string
  name: string
  provider_kind: 'openai-compatible' | 'anthropic-api-key' | 'gemini-api-key' | 'codex-membership'
  endpoint: string
  enabled: boolean
  revision: number
  credential_state: 'imported_unverified' | 'verified' | 'reauth_required' | null
  verified_at: string | null
  oauth_refresh?: {
    eligible: boolean
    state: 'ready' | 'refreshing' | 'paused' | 'reauth_required' | 'unavailable'
    reason_code?: string
  }
}
export type CodexOAuthSession = {
  session_id: string
  authorization_url: string
  expires_at: string
}
export type CodexOAuthSessionStatus = {
  session_id: string
  status: 'pending' | 'exchanging' | 'succeeded' | 'failed' | 'cancelled' | 'expired'
  expires_at: string
  upstream_id?: string
  error_code?: string
}
export type UpstreamBatchItem = {
  item_id: string
  name: string
  provider_kind: Upstream['provider_kind']
  endpoint?: string
  api_key?: string
  auth_json?: string
}
export type UpstreamBatchResult = {
  item_id: string
  status: 'created' | 'existing' | 'failed'
  upstream_id?: string
  error_code?: string
}
export type ModelRoute = {
  id: string
  upstream_id: string
  upstream_model: string
  enabled: boolean
}
export type AccountGroup = { id: string; name: string; revision: number }
export type AccountChannel = { id: string; name: string; group_id?: string | null; revision: number }
export type ModelAccount = {
  upstream_id: string
  upstream_model: string
  priority: number
  weight: number
  max_concurrency: number
  channel_id?: string | null
}
export type ModelAccounts = { model_id: string; revision: number; items: ModelAccount[] }
export type DiscoveredModel = {
  id: string
  display_name?: string
  upstream_capabilities?: {
    supported_in_api?: boolean
    input_modalities?: string[]
    context_window?: number
    reasoning_levels?: string[]
    search_tool?: boolean
    verbosity?: boolean
  }
}
export type SystemStatus = {
  version: string
  ready: boolean
  storage: string
  limitations: string[]
  features?: {
    codex_membership_import?: boolean
    codex_membership_oauth?: boolean
    codex_membership_auto_refresh?: boolean
    codex_model_discovery?: boolean
    anthropic_native_api?: boolean
    gemini_native_api?: boolean
    account_pool_configuration?: boolean
    account_pool_routing?: boolean
  }
}

export const api = {
  session: () => request<Session>('/session'),
  login: (username: string, password: string) =>
    request<{ csrf_token: string }>('/sessions', {
      method: 'POST',
      body: JSON.stringify({ username, password }),
    }),
  logout: (csrf: string) => request<void>('/sessions', { method: 'DELETE' }, csrf),
  employees: () => request<{ items: Employee[] }>('/employees'),
  createEmployee: (body: { name: string; department?: string; note?: string }, csrf: string) =>
    request<Employee>('/employees', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateEmployee: (id: string, body: Record<string, unknown>, csrf: string) =>
    request<Employee>(`/employees/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  updateModelPolicy: (id: string, body: Record<string, unknown>, csrf: string) =>
    request<Employee>(`/employees/${encodeURIComponent(id)}/model-policy`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  keys: (employeeId: string) => request<{ items: EmployeeKey[] }>(`/employees/${encodeURIComponent(employeeId)}/keys`),
  createKey: (employeeId: string, name: string, csrf: string) =>
    request<EmployeeKey>(`/employees/${encodeURIComponent(employeeId)}/keys`, {
      method: 'POST',
      body: JSON.stringify({ name, operation_id: crypto.randomUUID(), expires_at: null }),
    }, csrf),
  revokeKey: (keyId: string, csrf: string) =>
    request<{ ok: true }>(`/keys/${encodeURIComponent(keyId)}/revoke`, { method: 'POST', body: '{}' }, csrf),
  upstreams: (signal?: AbortSignal) => request<{ items: Upstream[] }>('/upstreams', { signal }),
  createUpstream: (body: Record<string, unknown>, csrf: string) =>
    request<Upstream>('/upstreams', { method: 'POST', body: JSON.stringify(body) }, csrf),
  batchImportUpstreams: (body: { operation_id: string; items: UpstreamBatchItem[] }, csrf: string) =>
    request<{ items: UpstreamBatchResult[] }>('/upstreams/batch-import', { method: 'POST', body: JSON.stringify(body) }, csrf),
  importCodexMembership: (body: { name: string; auth_json: string; operation_id: string }, csrf: string) =>
    request<Upstream>('/upstreams/codex-import', { method: 'POST', body: JSON.stringify(body) }, csrf),
  replaceCodexMembershipAuth: (id: string, body: { expected_revision: number; auth_json: string }, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}/codex-auth`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  createCodexOAuthSession: (body: { name: string; operation_id: string }, csrf: string) =>
    request<CodexOAuthSession>('/upstreams/codex-oauth-sessions', { method: 'POST', body: JSON.stringify(body) }, csrf),
  codexOAuthSession: (id: string, signal?: AbortSignal) =>
    request<CodexOAuthSessionStatus>(`/upstreams/codex-oauth-sessions/${encodeURIComponent(id)}`, { signal }),
  refreshCodexMembership: (id: string, body: { expected_revision: number }, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}/codex-refresh`, { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateUpstream: (id: string, body: Record<string, unknown>, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  discoverUpstreamModels: (id: string, csrf: string) =>
    request<{ items: DiscoveredModel[] }>(`/upstreams/${encodeURIComponent(id)}/discover-models`, { method: 'POST' }, csrf),
  models: () => request<{ items: ModelRoute[] }>('/models'),
  createModel: (body: Record<string, unknown>, csrf: string) =>
    request<ModelRoute>('/models', { method: 'POST', body: JSON.stringify(body) }, csrf),
  accountGroups: (signal?: AbortSignal) => request<{ items: AccountGroup[] }>('/account-groups', { signal }),
  createAccountGroup: (body: { name: string }, csrf: string) =>
    request<AccountGroup>('/account-groups', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateAccountGroup: (id: string, body: { expected_revision: number; name: string }, csrf: string) =>
    request<AccountGroup>(`/account-groups/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  accountChannels: (signal?: AbortSignal) => request<{ items: AccountChannel[] }>('/channels', { signal }),
  createAccountChannel: (body: { name: string; group_id?: string }, csrf: string) =>
    request<AccountChannel>('/channels', { method: 'POST', body: JSON.stringify(body) }, csrf),
  modelAccounts: (id: string, signal?: AbortSignal) =>
    request<ModelAccounts>(`/models/${encodeURIComponent(id)}/accounts`, { signal }),
  putModelAccounts: (id: string, body: { expected_revision: number; items: ModelAccount[] }, csrf: string) =>
    request<ModelAccounts>(`/models/${encodeURIComponent(id)}/accounts`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  status: () => request<SystemStatus>('/system/status'),
}
