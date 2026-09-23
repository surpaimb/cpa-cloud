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
  latest_observation?: UpstreamObservation | null
  cooldown?: UpstreamCooldown | null
  recovery?: AccountRecoveryState | null
}
export type AccountRecoveryStatus = {
  cli_allowed: boolean
  enabled: boolean
  setting_revision: number
  running: boolean
  next_wake_at: string | null
  history_count: number
  history_full: boolean
  server_time: string
}
export type AccountRecoveryState = {
  account_id: string
  cooldown_event_id: string
  operation_id: string
  recovery_revision: number
  account_revision: number
  pool_revision: number
  public_model: string
  upstream_model: string
  protocol: string
  state: 'required' | 'in_progress' | 'interrupted'
  next_probe_at: string
  due: boolean
  last_result_code: string | null
  last_finished_at: string | null
  attempt_count: number
  auto_eligible: boolean
  attention_code: string | null
  checked_at: string | null
}
export type UpstreamHealthScope = 'local_credential' | 'catalog'
export type UpstreamObservation = {
  operation_id: string
  scope: UpstreamHealthScope
  result_code: string
  account_revision: number
  checked_at: string
  latency_ms: number | null
}
export type UpstreamCooldown = {
  event_id: string
  failure_class: string
  cooldown_until: string
  updated_at: string
  active: boolean
}
export type UpstreamTestOperation = {
  operation_id: string
  upstream_id: string
  provider_kind: Upstream['provider_kind']
  requested_revision: number
  tested_revision: number | null
  scope: UpstreamHealthScope
  state: 'pending' | 'in_progress' | 'completed'
  result_code: string | null
  created_at: string
  started_at: string | null
  finished_at: string | null
  latency_ms: number | null
}
export type UpstreamsResponse = { items: Upstream[]; server_time?: string }
export type OutboundProxy = {
  id: string
  name: string
  scheme: 'https'
  host: string
  port: number
  address_scope: 'public' | 'private'
  enabled: boolean
  revision: number
  connection_revision: number
  has_credentials: boolean
  created_at: string
  updated_at: string
}
export type OutboundProxyPage = { items: OutboundProxy[]; next_cursor: string | null }
export type ProxyCredentials = { username: string; password: string }
export type CreateOutboundProxy = {
  operation_id: string
  name: string
  scheme: 'https'
  host: string
  port: number
  address_scope: 'public' | 'private'
  enabled: boolean
  credentials?: ProxyCredentials
}
export type UpdateOutboundProxy = {
  expected_revision: number
  name: string
  scheme: 'https'
  host: string
  port: number
  address_scope: 'public' | 'private'
  enabled: boolean
  credential_mode: 'keep' | 'replace' | 'clear'
  credentials?: ProxyCredentials
}
export type UpstreamProxyBinding = {
  proxy_id: string
  proxy_revision: number
  connection_revision: number
  enabled: boolean
  name: string
}
export type UpstreamProxyState = {
  upstream_id: string
  upstream_revision: number
  binding: UpstreamProxyBinding | null
}
export type CooldownClearResult = {
  result: 'cleared' | 'already_clear'
  upstream_id: string
  revision: number
  server_time: string
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
    account_recovery?: boolean
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

export type UsageStatus = 'pending' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'
export type UsageProvider = 'openai' | 'openai-compatible' | 'anthropic' | 'gemini' | 'codex'
export type UsageFilters = {
  from: string
  to: string
  employee_id?: string
  key_id?: string
  model_id?: string
  upstream_id?: string
  provider?: UsageProvider
  status?: UsageStatus
}
export type UsageCounter = { known_total: string; unknown_attempts: string }
export type UsageSummary = {
  from: string
  to: string
  requests: Record<'total' | UsageStatus, string>
  attempts: Array<{
    currency: string
    total: string
    pending: string
    succeeded: string
    failed: string
    cancelled: string
    interrupted: string
    known_cost_micro: string
    unknown_cost_attempts: string
    input_tokens: UsageCounter
    output_tokens: UsageCounter
    cache_read_tokens: UsageCounter
    cache_write_tokens: UsageCounter
  }>
}
export type UsageRequestItem = {
  id: string
  employee_id: string
  key_id: string
  model_id: string
  provider: UsageProvider
  status: UsageStatus
  started_at: string
  finished_at: string | null
  attempt_count: string
}
export type UsageRequestsPage = {
  items: UsageRequestItem[]
  next_cursor: string | null
  from: string
  to: string
}
export type UsageAttempt = {
  id: string
  request_id: string
  account_id: string
  provider: UsageProvider
  dispatch: string
  status: UsageStatus
  started_at: string
  finished_at: string | null
  price_version: string | null
  currency: string | null
  input_tokens: string | null
  output_tokens: string | null
  cache_read_tokens: string | null
  cache_write_tokens: string | null
  cost_micro: string | null
}
export type PriceRate = {
  currency: string
  input_per_million_micro: string
  output_per_million_micro: string
  cache_read_per_million_micro: string
  cache_write_per_million_micro: string
}
export type UpstreamPrice = {
  upstream_id: string
  upstream_model: string
  version: string
  revision: number
  created_at: string
  price: PriceRate | null
}

function usageSearch(filters: UsageFilters, cursor?: string) {
  const query = new URLSearchParams({ from: filters.from, to: filters.to })
  for (const key of ['employee_id', 'key_id', 'model_id', 'upstream_id', 'provider', 'status'] as const) {
    const value = filters[key]
    if (value) query.set(key, value)
  }
  if (cursor) query.set('cursor', cursor)
  return query.toString()
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
  upstreams: (signal?: AbortSignal) => request<UpstreamsResponse>('/upstreams', { signal }),
  outboundProxies: (afterId?: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (afterId) query.set('after_id', afterId)
    return request<OutboundProxyPage>(`/outbound-proxies?${query}`, { signal })
  },
  outboundProxy: (id: string, signal?: AbortSignal) =>
    request<OutboundProxy>(`/outbound-proxies/${encodeURIComponent(id)}`, { signal }),
  createOutboundProxy: (body: CreateOutboundProxy, csrf: string) =>
    request<OutboundProxy>('/outbound-proxies', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateOutboundProxy: (id: string, body: UpdateOutboundProxy, csrf: string) =>
    request<OutboundProxy>(`/outbound-proxies/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  upstreamProxy: (id: string, signal?: AbortSignal) =>
    request<UpstreamProxyState>(`/upstreams/${encodeURIComponent(id)}/proxy`, { signal }),
  putUpstreamProxy: (id: string, body: { expected_upstream_revision: number; proxy_id: string; expected_proxy_revision: number; bind: boolean }, csrf: string) =>
    request<UpstreamProxyState>(`/upstreams/${encodeURIComponent(id)}/proxy`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
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
  startUpstreamTest: (id: string, body: { operation_id: string; expected_revision: number; scope: UpstreamHealthScope }, csrf: string, signal?: AbortSignal) =>
    request<UpstreamTestOperation>(`/upstreams/${encodeURIComponent(id)}/tests`, { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  upstreamTest: (id: string, operationId: string, signal?: AbortSignal) =>
    request<UpstreamTestOperation>(`/upstreams/${encodeURIComponent(id)}/tests/${encodeURIComponent(operationId)}`, { signal }),
  clearUpstreamCooldown: (id: string, body: { expected_revision: number; expected_cooldown_event_id: string }, csrf: string, signal?: AbortSignal) =>
    request<CooldownClearResult>(`/upstreams/${encodeURIComponent(id)}/cooldown/clear`, { method: 'POST', body: JSON.stringify(body), signal }, csrf),
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
  usageSummary: (filters: UsageFilters, signal?: AbortSignal) =>
    request<UsageSummary>(`/usage/summary?${usageSearch(filters)}`, { signal }),
  usageRequests: (filters: UsageFilters, cursor?: string, signal?: AbortSignal) =>
    request<UsageRequestsPage>(`/usage/requests?${usageSearch(filters, cursor)}`, { signal }),
  usageAttempts: (requestId: string, signal?: AbortSignal) =>
    request<{ items: UsageAttempt[] }>(`/usage/requests/${encodeURIComponent(requestId)}/attempts`, { signal }),
  upstreamPrices: (upstreamId: string, signal?: AbortSignal) =>
    request<{ items: UpstreamPrice[] }>(`/upstreams/${encodeURIComponent(upstreamId)}/prices`, { signal }),
  saveUpstreamPrice: (upstreamId: string, body: { operation_id: string; expected_revision: number; upstream_model: string; price: PriceRate | null }, csrf: string) =>
    request<UpstreamPrice>(`/upstreams/${encodeURIComponent(upstreamId)}/prices`, { method: 'POST', body: JSON.stringify(body) }, csrf),
  status: () => request<SystemStatus>('/system/status'),
  accountRecovery: () => request<AccountRecoveryStatus>('/account-recovery'),
  accountRecoveryAccounts: () => request<{items: AccountRecoveryState[]; server_time: string}>('/account-recovery/accounts'),
  setAccountRecovery: (enabled: boolean, revision: number, csrf: string) =>
    request<AccountRecoveryStatus>('/account-recovery', { method: 'PUT', body: JSON.stringify({ enabled, expected_revision: revision }) }, csrf),
}
