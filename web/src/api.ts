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
  provider_kind: 'openai-compatible' | 'anthropic-api-key' | 'codex-membership'
  endpoint: string
  enabled: boolean
  revision: number
  credential_state: 'imported_unverified' | 'verified' | 'reauth_required' | null
  verified_at: string | null
}
export type ModelRoute = {
  id: string
  upstream_id: string
  upstream_model: string
  enabled: boolean
}
export type DiscoveredModel = { id: string }
export type SystemStatus = {
  version: string
  ready: boolean
  storage: string
  limitations: string[]
  features?: {
    codex_membership_import: boolean
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
  upstreams: () => request<{ items: Upstream[] }>('/upstreams'),
  createUpstream: (body: Record<string, unknown>, csrf: string) =>
    request<Upstream>('/upstreams', { method: 'POST', body: JSON.stringify(body) }, csrf),
  importCodexMembership: (body: { name: string; auth_json: string; operation_id: string }, csrf: string) =>
    request<Upstream>('/upstreams/codex-import', { method: 'POST', body: JSON.stringify(body) }, csrf),
  replaceCodexMembershipAuth: (id: string, body: { expected_revision: number; auth_json: string }, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}/codex-auth`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  updateUpstream: (id: string, body: Record<string, unknown>, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  discoverUpstreamModels: (id: string, csrf: string) =>
    request<{ items: DiscoveredModel[] }>(`/upstreams/${encodeURIComponent(id)}/discover-models`, { method: 'POST' }, csrf),
  models: () => request<{ items: ModelRoute[] }>('/models'),
  createModel: (body: Record<string, unknown>, csrf: string) =>
    request<ModelRoute>('/models', { method: 'POST', body: JSON.stringify(body) }, csrf),
  status: () => request<SystemStatus>('/system/status'),
}
