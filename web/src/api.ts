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
export type ClientProtocol = 'openai-chat' | 'openai-responses' | 'openai-embeddings' | 'anthropic-messages' | 'gemini-generate-content'
export type KeyAccessPolicy = {
  revision: number
  protocol_mode: 'all' | 'selected'
  protocols: ClientProtocol[]
  model_mode: 'all' | 'selected'
  models: string[]
  source_mode?: 'all' | 'selected'
  source_cidrs?: string[]
  account_group_mode?: 'all' | 'selected'
  account_group_ids?: string[]
  effective_protocols: ClientProtocol[]
  effective_models: string[]
}
export type KeyPolicyInput = Pick<KeyAccessPolicy, 'protocol_mode' | 'protocols' | 'model_mode' | 'models' | 'source_mode' | 'source_cidrs' | 'account_group_mode' | 'account_group_ids'>
export type EmployeeKey = {
  id: string
  name: string
  key?: string
  expires_at: string | null
  revoked_at: string | null
  policy?: KeyAccessPolicy
}
export type Upstream = {
  id: string
  name: string
  provider_kind: 'openai-compatible' | 'anthropic-api-key' | 'gemini-api-key' | 'codex-membership'
  endpoint: string
  enabled: boolean
  revision: number
  archived?: boolean
  archived_at?: string | null
  archive_result?: 'archived' | 'already_archived'
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
export type OutboundProxyTestResultCode = 'handshake_ok' | 'configuration_changed' | 'proxy_unavailable' | 'target_unavailable' | 'address_rejected' | 'timeout' | 'cancelled' | 'interrupted' | 'internal_failure'
export type OutboundProxyTestOperation = {
  operation_id: string
  proxy_id: string
  proxy_revision: number
  connection_revision: number
  upstream_id: string
  upstream_revision: number
  state: 'pending' | 'in_progress' | 'completed'
  result_code: OutboundProxyTestResultCode | null
  created_at: string
  started_at: string | null
  finished_at: string | null
  latency_ms: number | null
}
export type OutboundProxyTestWrite = {
  operation_id: string
  expected_proxy_revision: number
  expected_connection_revision: number
  upstream_id: string
  expected_upstream_revision: number
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
  model_kind?: 'generation' | 'embedding'
  upstream_id: string
  upstream_model: string
  wire_protocol?: WireProtocol
  enabled: boolean
  revision?: number
  archived?: boolean
  archived_at?: string | null
  archive_result?: 'archived' | 'already_archived'
}
export type AccountGroupAllocation = { version: string; multiplier_ppm: string; created_at: string }
export type AccountGroup = { id: string; name: string; revision: number; allocation?: AccountGroupAllocation }
export type AccountGroupAllocationVersion = { group_id: string; version: string; revision: number; allocation_multiplier_ppm: string; created_at: string }
export type AccountChannel = { id: string; name: string; group_id?: string | null; revision: number }
export type WireProtocol = 'legacy-native' | 'openai-chat' | 'openai-responses' | 'openai-embeddings' | 'anthropic-messages' | 'gemini-generate-content'
export type ModelAccount = {
  upstream_id: string
  upstream_model: string
  wire_protocol?: WireProtocol
  priority: number
  weight: number
  max_concurrency: number
  channel_id?: string | null
}
export type ModelAccounts = { model_id: string; revision: number; items: ModelAccount[] }
export type PoolRuntimeBlockReason = 'model_disabled' | 'upstream_disabled' | 'cooldown_active' | 'recovery_isolated' | 'membership_disabled' | 'reauth_required' | 'capacity_reserved'
export type PoolRuntimeRoute = {
  upstream_id: string
  account_revision: number
  configured_max_concurrency: number
  global_max_concurrency: number | null
  request_reservations: number
  maintenance_reservations: number
  remaining_local_slots: number | null
  block_reasons: PoolRuntimeBlockReason[]
  cooldown_until: string | null
}
export type PoolRuntimeObservation = {
  model_id: string
  model_revision: number
  pool_revision: number
  pool_status: 'explicit_pool' | 'legacy_no_pool' | 'model_disabled'
  as_of: string
  items: PoolRuntimeRoute[]
}
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
	  scheduled_tests_configuration?: boolean
	  scheduled_tests_running?: boolean
	  scheduled_tests_daily_local?: boolean
	  automated_backups_configuration?: boolean
	  automated_backups_running?: boolean
	  backup_key_provider_ready?: boolean
    account_recovery?: boolean
    codex_membership_import?: boolean
    codex_membership_oauth?: boolean
    codex_membership_auto_refresh?: boolean
    codex_model_discovery?: boolean
    anthropic_native_api?: boolean
    gemini_native_api?: boolean
    account_pool_configuration?: boolean
    account_pool_runtime_observation?: boolean
    account_pool_routing?: boolean
    channel_monitor_configuration?: boolean
    channel_monitor_running?: boolean
    channel_monitor_retained_summary?: boolean
    account_group_cost_allocation?: boolean
    openai_embeddings?: boolean
    account_lifecycle_management?: boolean
    single_instance_billing?: boolean
    admin_audit_overview?: boolean
    admin_audit_financial_source?: boolean
    admin_audit_csv_export?: boolean
    key_access_policy?: boolean
    key_source_policy?: boolean
    key_account_group_policy?: boolean
    trusted_proxy_source?: boolean
  }
}

export const adminAuditSources = ['account_pool', 'account_lifecycle', 'governance_management', 'governance_general_budget', 'financial_commercial'] as const
export type AdminAuditSource = typeof adminAuditSources[number]
export type AdminAuditEvent = {
  source: AdminAuditSource
  event_id: string
  actor_id: string | null
  action: string
  target_type: string
  target_id: string
  result: 'succeeded'
  revision: number | null
  occurred_at: string
}
export type AdminAuditPage = {
  from: string
  to: string
  snapshot_at: string
  sources: AdminAuditSource[]
  items: AdminAuditEvent[]
  next_cursor: string | null
}
export type AdminAuditFilters = {
  sources?: AdminAuditSource[]
  actor_id?: string
  action?: string
  target_type?: string
  target_id?: string
  result?: 'succeeded'
  from?: string
  to?: string
  limit?: number
}

export type BillingOwner = {
  kind: 'employee' | 'key' | 'resource'
  employee_id: string
  key_id?: string | null
  resource_kind?: string | null
  resource_id?: string | null
}
export type BillingBalance = { account_id: string | null; owner: BillingOwner; currency: string; balance_micro: string }
export type BillingAdjustment = BillingBalance & { operation_id: string; entry_id: string; amount_micro: string }
export type BillingReceipt = { operation_id: string; resource_kind: string; resource_id: string; revision: number; created_at: string; replay: boolean }
export type BillingSettings = { enabled: boolean; revision: number }
export type BillingPlan = { id: string; name: string; currency: string; price_micro: string; credit_micro: string; interval: 'one_time' | 'monthly'; enabled: boolean; revision: number; created_at: string; updated_at: string }
export type BillingConnector = { id: string; name: string; enabled: boolean; revision: number; created_at: string; updated_at: string }
export type BillingTopUp = { id: string; payment_id: string; connector_id: string; external_reference: string; amount_micro: string; currency: string; status: 'pending' | 'paid' | 'partially_refunded' | 'refunded'; refunded_micro: string; revision: number; created_at: string; paid_at: string | null }
export type BillingSubscription = { id: string; plan_id: string; plan_revision: number; price_micro: string; credit_micro: string; currency: string; interval: 'one_time' | 'monthly'; status: 'active' | 'cancelled'; revision: number; started_at: string }
export type BillingRedemptionCode = { id: string; amount_micro: string; currency: string; max_uses: number; uses: number; expires_at: string | null; enabled: boolean; created_at: string }
export type BillingRefund = { id: string; payment_id: string; entry_id: string; amount_micro: string; created_at: string }
export type BillingEntry = { id: string; operation_id: string; account_id: string; owner: BillingOwner; currency: string; kind: string; amount_micro: string; original_entry_id: string | null; resource_kind: string; resource_id: string; created_at: string }
export type BillingPage<T> = { items: T[]; next_cursor: string | null }
export type BillingPlanWrite = { operation_id: string; name: string; currency: string; price_micro: string; credit_micro: string; interval: BillingPlan['interval']; enabled: boolean }

export type ScheduledTestScope = 'local_credential' | 'catalog'
export type ScheduledTestScheduleMode = 'interval' | 'daily_local'
export type ScheduledTestRun = {
  plan_revision: number
  operation_id: string
  scope: ScheduledTestScope
  state: 'running' | 'completed'
  result_code: string | null
  started_at: string
  finished_at: string | null
  latency_ms: number | null
}
export type ScheduledTestPlan = {
  id: string
  name: string
  upstream_id: string
  scope: ScheduledTestScope
  interval_seconds: number
  schedule_mode?: ScheduledTestScheduleMode
  time_zone?: string | null
  local_time?: string | null
  enabled: boolean
  revision: number
  next_run_at: string | null
  latest_result: ScheduledTestRun | null
  created_at: string
  updated_at: string
}
export type ScheduledTestInput = {
  name: string
  upstream_id: string
  scope: ScheduledTestScope
  enabled: boolean
} & (
  { schedule_mode?: 'interval'; interval_seconds: number; time_zone?: never; local_time?: never } |
  { schedule_mode: 'daily_local'; time_zone: string; local_time: string; interval_seconds?: never }
)
export type ScheduledTestRunsPage = { items: ScheduledTestRun[]; next_cursor: string | null }

// Independently authored for docs/channel-monitor-contract.md.
export type ChannelMonitorRun = ScheduledTestRun & {
  channel_id: string
  channel_revision: number
  model_id: string
  model_revision: number
  pool_revision: number
  upstream_id: string
  upstream_revision: number
  route_upstream_model: string
  route_wire_protocol: string
  route_position: number
}
export type ChannelMonitorPlan = {
  id: string
  name: string
  channel_id: string
  model_id: string
  upstream_id: string
  scope: ScheduledTestScope
  interval_seconds: number
  enabled: boolean
  revision: number
  next_run_at: string | null
  created_at: string
  updated_at: string
  binding_state: 'valid' | 'stale'
  latest_result: ChannelMonitorRun | null
}
export type ChannelMonitorInput = Pick<ChannelMonitorPlan, 'name' | 'channel_id' | 'model_id' | 'upstream_id' | 'scope' | 'interval_seconds' | 'enabled'>
export type ChannelMonitorRunsPage = { items: ChannelMonitorRun[]; next_cursor: string | null }
// Independently authored for docs/channel-monitor-summary-contract.md.
export type ChannelMonitorSummary = {
  plan_id: string
  as_of: string
  through_sequence: number
  retained_completed: number
  retained_window_full: boolean
  running: number
  earliest_finished_at: string | null
  latest_finished_at: string | null
  counts: Record<ScheduledTestScope, Record<string, number>>
}

export type BackupKeyProvider = {
  id: string
  kind: 'windows-dpapi-user'
  scope: 'current-user'
  status: 'ready' | 'unavailable' | 'degraded'
  reason_code: string | null
  active_version: number
  revision: number
  created_by_admin_id: string
  updated_by_admin_id: string
  created_at: string
  updated_at: string
}
export type BackupRun = {
  id: string
  plan_id: string
  plan_revision: number
  key_provider_id: string
  key_provider_kind: 'windows-dpapi-user'
  key_provider_version: number
  trigger_kind: 'scheduled' | 'manual'
  requested_by_admin_id: string | null
  scheduled_for: string
  started_at: string
  finished_at: string | null
  status: 'running' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted'
  package_name: string
  package_size: number | null
  package_retained: boolean
  package_deleted_at: string | null
  verified_at: string | null
  rehearsal_status: 'pending' | 'succeeded' | 'failed' | 'skipped'
  rehearsed_at: string | null
  error_code: string | null
  created_at: string
}
export type BackupPlan = {
  id: string
  name: string
  key_provider_id: string
  interval_seconds: number
  retention_count: number
  rehearsal_enabled: boolean
  enabled: boolean
  next_run_at: string | null
  revision: number
  created_by_admin_id: string
  updated_by_admin_id: string
  created_at: string
  updated_at: string
  latest_run?: BackupRun
}
export type BackupPlanInput = {
  name: string
  key_provider_id: string
  interval_seconds: number
  retention_count: number
  rehearsal_enabled: boolean
  enabled: boolean
}
export type BackupRunsPage = { items: BackupRun[]; next_cursor: string | null }

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
    known_adjusted_allocation_cost_micro?: string
    unknown_adjusted_allocation_attempts?: string
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
  account_group_id?: string | null
  allocation_multiplier_version?: string | null
  allocation_multiplier_ppm?: string | null
  adjusted_allocation_cost_micro?: string | null
}
export type UsageSettlementReportItem = {
  period_start: string
  period_end: string
  currency: string
  requests: string
  attempts: string
  corrections: string
  missing_evidence_attempts: string
  known_estimated_cost_micro: string
  unknown_cost_attempts: string
  known_adjusted_allocation_cost_micro?: string
  unknown_adjusted_allocation_attempts?: string
  input_tokens: UsageCounter
  output_tokens: UsageCounter
  cache_read_tokens: UsageCounter
  cache_write_tokens: UsageCounter
  reasoning_tokens: UsageCounter
}
export type UsageSettlementReport = {
  from: string
  to: string
  granularity: 'day' | 'month'
  items: UsageSettlementReportItem[]
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
export type GovernanceSettings = { enabled: boolean; budget_enabled?: boolean; revision: number; updated_at: string }
export type GovernanceGroup = { id: string; name: string; employee_ids: string[]; revision: number; created_at: string; updated_at: string }
export type GovernanceHardLimits = { rpm: number | null; concurrency: number | null }
export type GovernanceBudgetLimits = {
  tpm: number | null
  cost_micro: string | null
  currency: string | null
  window: 'rolling_24h' | null
  unknown_mode: 'shadow' | 'deny_unknown'
}
export type GovernanceShadowLimits = { tpm: number | null; cost_micro: string | null; currency: string | null; window: 'rolling_24h' | null }
export type GovernanceScopeKind = 'employee' | 'key' | 'group'
export type GeneralBudgetProtocol = 'openai-chat-completions' | 'openai-responses' | 'openai-embeddings' | 'anthropic-messages' | 'gemini-generate-content'
export type GovernancePolicy = {
  id: string
  scope_kind: GovernanceScopeKind
  scope_id: string
  enabled: boolean
  hard: GovernanceHardLimits
  budget?: GovernanceBudgetLimits
  shadow: GovernanceShadowLimits
  revision: number
  created_at: string
  updated_at: string
}
export type GovernancePage<T> = { items: T[]; next_cursor: string | null }
export type GovernanceReceipt = {
  operation_id: string
  resource_kind: 'settings' | 'group' | 'policy' | 'budget'
  resource_id: string
  revision: number
  created_at: string
}
export type GovernancePolicyInput = {
  enabled: boolean
  hard: GovernanceHardLimits
  budget?: GovernanceBudgetLimits
  shadow: GovernanceShadowLimits
}
export type GovernanceObservationState = 'exceeded' | 'below' | 'unknown'
export type GovernanceObservationFilters = {
  scope_kind?: GovernanceScopeKind
  scope_id?: string
  policy_id?: string
  limit?: number
}
export type GovernanceObservationSnapshot = {
  settings_revision: string
  scope_kind: GovernanceScopeKind
  scope_id: string
  policy_id: string
  policy_revision: string
  group_revision: string | null
  shadow_tpm: string | null
  shadow_cost_micro: string | null
  shadow_currency: string
  shadow_window: 'rolling_24h' | ''
}
export type GovernanceObservationCounts = {
  pending_requests: string
  pending_attempts: string
  pending_requests_without_attempt: string
  zero_attempt_requests: string
}
export type GovernanceObservationItem = {
  snapshot: GovernanceObservationSnapshot
  scope_totals: {
    tpm: GovernanceObservationCounts & {
      known_tokens: string
      known_attempts: string
      unknown_token_attempts: string
    }
    cost: GovernanceObservationCounts & {
      known_attempts: string
      unknown_cost_attempts: string
      by_currency: Array<{ currency: string; known_cost_micro: string; attempts: string }>
    }
  }
  interpretation: {
    tpm_state: GovernanceObservationState | null
    cost_state: GovernanceObservationState | null
    incomparable_currency_attempts: string | null
  }
}
export type GovernanceObservationsPage = {
  window_end: string
  observed_at: string
  tpm_from: string
  cost_from: string
  items: GovernanceObservationItem[]
  next_cursor: string | null
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
export type GeneralBudgetPolicy = {
  id: string
  scope: { kind: GovernanceScopeKind; id: string; protocol: GeneralBudgetProtocol | null; model: string | null }
  enabled: boolean
  enforcement: 'strict'
  unknown_mode: 'deny_unknown'
  token: { limit: number | null; window: 'rolling_60s' | null }
  cost: { limit_micro: string | null; currency: string | null; window: 'rolling_24h' | null }
  revision: number
  created_at: string
  updated_at: string
}
export type GeneralBudgetCreate = {
  operation_id: string
  scope_kind: GovernanceScopeKind
  scope_id: string
  protocol: GeneralBudgetProtocol | null
  model: string | null
  enabled: boolean
  token_limit: number | null
  cost_limit_micro: string | null
  currency: string | null
}
export type GeneralBudgetUpdate = {
  operation_id: string
  expected_revision: number
  enabled: boolean
  token_limit: number | null
  cost_limit_micro: string | null
  currency: string | null
}

export function usageExportURL(filters: UsageFilters, limit = 5000) {
  return `${API_ROOT}/usage/export?${usageSearch(filters)}&limit=${limit}`
}

function governanceObservationSearch(filters: GovernanceObservationFilters, cursor?: string) {
  const query = new URLSearchParams({ limit: String(filters.limit ?? 20) })
  if (filters.scope_kind) query.set('scope_kind', filters.scope_kind)
  if (filters.scope_id) query.set('scope_id', filters.scope_id)
  if (filters.policy_id) query.set('policy_id', filters.policy_id)
  if (cursor) query.set('cursor', cursor)
  return query.toString()
}

function billingList<T>(path: string, afterId?: string, limit = 50, signal?: AbortSignal) {
  const query = new URLSearchParams({ limit: String(limit) })
  if (afterId) query.set('after_id', afterId)
  return request<BillingPage<T>>(`${path}?${query}`, { signal })
}

const adminAuditSourceRanks = new Map<AdminAuditSource, number>(adminAuditSources.map((source, index) => [source, index]))
const adminAuditPageKeys = ['from', 'to', 'snapshot_at', 'sources', 'items', 'next_cursor']
const adminAuditEventKeys = ['source', 'event_id', 'actor_id', 'action', 'target_type', 'target_id', 'result', 'revision', 'occurred_at']

function auditRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function hasExactKeys(value: Record<string, unknown>, keys: string[]) {
  const actual = Object.keys(value).sort()
  const expected = [...keys].sort()
  return actual.length === expected.length && actual.every((key, index) => key === expected[index])
}

function auditMetadata(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0 && value.trim() === value && new TextEncoder().encode(value).length <= 256 && !/[\x00-\x1f\x7f-\x9f]/u.test(value)
}

function auditTimeKey(value: unknown): string | null {
  if (typeof value !== 'string') return null
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?Z$/.exec(value)
  if (!match || (match[7]?.endsWith('0') ?? false)) return null
  const parsed = new Date(value)
  if (!Number.isFinite(parsed.getTime())) return null
  const numbers = match.slice(1, 7).map(Number)
  const millis = Number((match[7] ?? '').padEnd(3, '0').slice(0, 3))
  if (parsed.getUTCFullYear() !== numbers[0] || parsed.getUTCMonth() + 1 !== numbers[1] || parsed.getUTCDate() !== numbers[2]
    || parsed.getUTCHours() !== numbers[3] || parsed.getUTCMinutes() !== numbers[4] || parsed.getUTCSeconds() !== numbers[5]
    || parsed.getUTCMilliseconds() !== millis) return null
  return `${value.slice(0, 19)}.${(match[7] ?? '').padEnd(9, '0')}Z`
}

function compareUTF8(left: string, right: string) {
  const leftBytes = new TextEncoder().encode(left)
  const rightBytes = new TextEncoder().encode(right)
  for (let index = 0; index < Math.min(leftBytes.length, rightBytes.length); index += 1) {
    if (leftBytes[index] !== rightBytes[index]) return leftBytes[index] - rightBytes[index]
  }
  return leftBytes.length - rightBytes.length
}

function invalidAdminAuditResponse(): never {
  throw new ApiError(502, 'invalid_response', '审计响应格式无效，未显示任何结果。')
}

function parseAdminAuditPage(value: unknown): AdminAuditPage {
  if (!auditRecord(value) || !hasExactKeys(value, adminAuditPageKeys)) invalidAdminAuditResponse()
  const fromKey = auditTimeKey(value.from)
  const toKey = auditTimeKey(value.to)
  const snapshotKey = auditTimeKey(value.snapshot_at)
  if (!fromKey || !toKey || !snapshotKey || fromKey >= toKey
    || new Date(value.to as string).getTime() - new Date(value.from as string).getTime() > 31 * 24 * 60 * 60 * 1000) invalidAdminAuditResponse()
  if (!Array.isArray(value.sources) || value.sources.length < 1 || value.sources.length > adminAuditSources.length) invalidAdminAuditResponse()
  const sources: AdminAuditSource[] = []
  let previousRank = -1
  for (const source of value.sources) {
    if (typeof source !== 'string' || !adminAuditSourceRanks.has(source as AdminAuditSource)) invalidAdminAuditResponse()
    const typed = source as AdminAuditSource
    const rank = adminAuditSourceRanks.get(typed)!
    if (rank <= previousRank) invalidAdminAuditResponse()
    previousRank = rank
    sources.push(typed)
  }
  if (!Array.isArray(value.items) || value.items.length > 100) invalidAdminAuditResponse()
  const items: AdminAuditEvent[] = []
  const seen = new Set<string>()
  let previous: { time: string; rank: number; id: string } | null = null
  for (const raw of value.items) {
    if (!auditRecord(raw) || !hasExactKeys(raw, adminAuditEventKeys)) invalidAdminAuditResponse()
    if (typeof raw.source !== 'string' || !sources.includes(raw.source as AdminAuditSource)) invalidAdminAuditResponse()
    const source = raw.source as AdminAuditSource
    const strings = [raw.event_id, raw.action, raw.target_type, raw.target_id]
    if (!strings.every(auditMetadata) || (raw.actor_id === null ? source !== 'financial_commercial' : !auditMetadata(raw.actor_id)) || raw.result !== 'succeeded') invalidAdminAuditResponse()
    if (raw.revision !== null && (!Number.isSafeInteger(raw.revision) || (raw.revision as number) < 1)) invalidAdminAuditResponse()
    const occurredKey = auditTimeKey(raw.occurred_at)
    if (!occurredKey || occurredKey < fromKey || occurredKey >= toKey) invalidAdminAuditResponse()
    const identity = `${source}\u0000${raw.event_id as string}`
    if (seen.has(identity)) invalidAdminAuditResponse()
    seen.add(identity)
    const current = { time: occurredKey, rank: adminAuditSourceRanks.get(source)!, id: raw.event_id as string }
    if (previous && (previous.time < current.time
      || (previous.time === current.time && previous.rank > current.rank)
      || (previous.time === current.time && previous.rank === current.rank && compareUTF8(previous.id, current.id) <= 0))) invalidAdminAuditResponse()
    previous = current
    items.push({
      source,
      event_id: raw.event_id as string,
      actor_id: raw.actor_id as string | null,
      action: raw.action as string,
      target_type: raw.target_type as string,
      target_id: raw.target_id as string,
      result: 'succeeded',
      revision: raw.revision as number | null,
      occurred_at: raw.occurred_at as string,
    })
  }
  if (value.next_cursor !== null && (typeof value.next_cursor !== 'string' || value.next_cursor.length < 1 || value.next_cursor.length > 4096 || !/^[A-Za-z0-9_-]+$/.test(value.next_cursor))) invalidAdminAuditResponse()
  return {
    from: value.from as string,
    to: value.to as string,
    snapshot_at: value.snapshot_at as string,
    sources,
    items,
    next_cursor: value.next_cursor as string | null,
  }
}

function adminAuditSearch(filters: AdminAuditFilters, cursor?: string) {
  if (cursor) return new URLSearchParams({ cursor }).toString()
  const query = new URLSearchParams({ limit: String(filters.limit ?? 50) })
  if (filters.sources && filters.sources.length !== adminAuditSources.length) query.set('sources', filters.sources.join(','))
  if (filters.from) query.set('from', filters.from)
  if (filters.to) query.set('to', filters.to)
  if (filters.actor_id) query.set('actor_id', filters.actor_id)
  if (filters.action) query.set('action', filters.action)
  if (filters.target_type) query.set('target_type', filters.target_type)
  if (filters.target_id) query.set('target_id', filters.target_id)
  if (filters.result) query.set('result', filters.result)
  return query.toString()
}

// Independently authored for docs/admin-audit-export-contract.md.
async function adminAuditExport(filters: AdminAuditFilters, signal?: AbortSignal): Promise<Blob> {
  const query = new URLSearchParams(adminAuditSearch(filters))
  query.delete('limit')
  const response = await fetch(`${API_ROOT}/audit/events/export.csv?${query}`, { credentials: 'same-origin', signal })
  if (!response.ok) {
    let envelope: ErrorEnvelope = {}
    try { envelope = (await response.json()) as ErrorEnvelope } catch { /* Keep a stable fallback. */ }
    throw new ApiError(response.status, envelope.error?.code ?? 'request_failed', envelope.error?.message ?? '导出失败，请稍后重试。')
  }
  if (!response.headers.get('Content-Type')?.toLowerCase().startsWith('text/csv')
    || response.headers.get('Content-Disposition') !== 'attachment; filename="cpa-cloud-admin-audit.csv"') {
    throw new ApiError(502, 'invalid_response', '导出响应格式无效，未保存文件。')
  }
  const blob = await response.blob()
  if (blob.size > 2 * 1024 * 1024) throw new ApiError(502, 'invalid_response', '导出响应过大，未保存文件。')
  return blob
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
  createKey: (employeeId: string, name: string, csrf: string, policy?: KeyPolicyInput) =>
    request<EmployeeKey>(`/employees/${encodeURIComponent(employeeId)}/keys`, {
      method: 'POST',
      body: JSON.stringify({ name, operation_id: crypto.randomUUID(), expires_at: null, ...(policy ? { policy } : {}) }),
    }, csrf),
  keyPolicy: (keyId: string) =>
    request<KeyAccessPolicy>(`/keys/${encodeURIComponent(keyId)}/policy`),
  putKeyPolicy: (keyId: string, body: KeyPolicyInput & { expected_revision: number }, csrf: string) =>
    request<KeyAccessPolicy>(`/keys/${encodeURIComponent(keyId)}/policy`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  revokeKey: (keyId: string, csrf: string) =>
    request<{ ok: true }>(`/keys/${encodeURIComponent(keyId)}/revoke`, { method: 'POST', body: '{}' }, csrf),
  upstreams: (signal?: AbortSignal, includeArchived = false) => request<UpstreamsResponse>(`/upstreams${includeArchived ? '?include_archived=true' : ''}`, { signal }),
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
  startOutboundProxyTest: (id: string, body: OutboundProxyTestWrite, csrf: string, signal?: AbortSignal) =>
    request<OutboundProxyTestOperation>(`/outbound-proxies/${encodeURIComponent(id)}/tests`, { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  outboundProxyTest: (id: string, operationId: string, signal?: AbortSignal) =>
    request<OutboundProxyTestOperation>(`/outbound-proxies/${encodeURIComponent(id)}/tests/${encodeURIComponent(operationId)}`, { signal }),
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
  archiveUpstream: (id: string, expectedRevision: number, csrf: string) =>
    request<Upstream>(`/upstreams/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ expected_revision: expectedRevision }) }, csrf),
  discoverUpstreamModels: (id: string, csrf: string) =>
    request<{ items: DiscoveredModel[] }>(`/upstreams/${encodeURIComponent(id)}/discover-models`, { method: 'POST' }, csrf),
  startUpstreamTest: (id: string, body: { operation_id: string; expected_revision: number; scope: UpstreamHealthScope }, csrf: string, signal?: AbortSignal) =>
    request<UpstreamTestOperation>(`/upstreams/${encodeURIComponent(id)}/tests`, { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  upstreamTest: (id: string, operationId: string, signal?: AbortSignal) =>
    request<UpstreamTestOperation>(`/upstreams/${encodeURIComponent(id)}/tests/${encodeURIComponent(operationId)}`, { signal }),
  clearUpstreamCooldown: (id: string, body: { expected_revision: number; expected_cooldown_event_id: string }, csrf: string, signal?: AbortSignal) =>
    request<CooldownClearResult>(`/upstreams/${encodeURIComponent(id)}/cooldown/clear`, { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  models: (includeArchived = false) => request<{ items: ModelRoute[] }>(`/models${includeArchived ? '?include_archived=true' : ''}`),
  createModel: (body: Record<string, unknown>, csrf: string) =>
    request<ModelRoute>('/models', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateModel: (id: string, body: Record<string, unknown>, csrf: string) =>
    request<ModelRoute>(`/models/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  archiveModel: (id: string, expectedRevision: number, csrf: string) =>
    request<ModelRoute>(`/models/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ expected_revision: expectedRevision }) }, csrf),
  accountGroups: (signal?: AbortSignal) => request<{ items: AccountGroup[] }>('/account-groups', { signal }),
  createAccountGroup: (body: { name: string }, csrf: string) =>
    request<AccountGroup>('/account-groups', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateAccountGroup: (id: string, body: { expected_revision: number; name: string }, csrf: string) =>
    request<AccountGroup>(`/account-groups/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  updateAccountGroupAllocation: (id: string, body: { operation_id: string; expected_revision: number; allocation_multiplier_ppm: string }, csrf: string) =>
    request<AccountGroupAllocationVersion>(`/account-groups/${encodeURIComponent(id)}/allocation`, { method: 'POST', body: JSON.stringify(body) }, csrf),
  accountChannels: (signal?: AbortSignal) => request<{ items: AccountChannel[] }>('/channels', { signal }),
  createAccountChannel: (body: { name: string; group_id?: string }, csrf: string) =>
    request<AccountChannel>('/channels', { method: 'POST', body: JSON.stringify(body) }, csrf),
  modelAccounts: (id: string, signal?: AbortSignal) =>
    request<ModelAccounts>(`/models/${encodeURIComponent(id)}/accounts`, { signal }),
  modelPoolRuntime: (id: string, signal?: AbortSignal) =>
    request<PoolRuntimeObservation>(`/models/${encodeURIComponent(id)}/pool-runtime`, { signal }),
  putModelAccounts: (id: string, body: { expected_revision: number; items: ModelAccount[] }, csrf: string) =>
    request<ModelAccounts>(`/models/${encodeURIComponent(id)}/accounts`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  usageSummary: (filters: UsageFilters, signal?: AbortSignal) =>
    request<UsageSummary>(`/usage/summary?${usageSearch(filters)}`, { signal }),
  usageRequests: (filters: UsageFilters, cursor?: string, signal?: AbortSignal) =>
    request<UsageRequestsPage>(`/usage/requests?${usageSearch(filters, cursor)}`, { signal }),
  usageAttempts: (requestId: string, signal?: AbortSignal) =>
    request<{ items: UsageAttempt[] }>(`/usage/requests/${encodeURIComponent(requestId)}/attempts`, { signal }),
  usageSettlementDaily: (filters: UsageFilters, signal?: AbortSignal) =>
    request<UsageSettlementReport>(`/usage/daily?${usageSearch(filters)}`, { signal }),
  usageSettlementMonthly: (filters: UsageFilters, signal?: AbortSignal) =>
    request<UsageSettlementReport>(`/usage/monthly?${usageSearch(filters)}`, { signal }),
  upstreamPrices: (upstreamId: string, signal?: AbortSignal) =>
    request<{ items: UpstreamPrice[] }>(`/upstreams/${encodeURIComponent(upstreamId)}/prices`, { signal }),
  governanceSettings: (signal?: AbortSignal) => request<GovernanceSettings>('/governance/settings', { signal }),
  putGovernanceSettings: (body: { operation_id: string; expected_revision: number; enabled: boolean; budget_enabled?: boolean }, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>('/governance/settings', { method: 'PUT', body: JSON.stringify(body), signal }, csrf),
  governanceGroups: (afterId?: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (afterId) query.set('after_id', afterId)
    return request<GovernancePage<GovernanceGroup>>(`/governance/groups?${query}`, { signal })
  },
  governanceGroup: (id: string, signal?: AbortSignal) => request<GovernanceGroup>(`/governance/groups/${encodeURIComponent(id)}`, { signal }),
  createGovernanceGroup: (body: { operation_id: string; name: string; employee_ids: string[] }, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>('/governance/groups', { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  updateGovernanceGroup: (id: string, body: { operation_id: string; expected_revision: number; name: string; employee_ids: string[] }, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>(`/governance/groups/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body), signal }, csrf),
  governancePolicies: (afterId?: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (afterId) query.set('after_id', afterId)
    return request<GovernancePage<GovernancePolicy>>(`/governance/policies?${query}`, { signal })
  },
  governancePolicy: (id: string, signal?: AbortSignal) => request<GovernancePolicy>(`/governance/policies/${encodeURIComponent(id)}`, { signal }),
  createGovernancePolicy: (body: { operation_id: string; scope_kind: GovernanceScopeKind; scope_id: string } & GovernancePolicyInput, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>('/governance/policies', { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  updateGovernancePolicy: (id: string, body: { operation_id: string; expected_revision: number } & GovernancePolicyInput, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>(`/governance/policies/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body), signal }, csrf),
  governanceOperation: (operationId: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>(`/governance/operations/${encodeURIComponent(operationId)}`, { signal }),
  generalBudgets: (afterId?: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: String(limit) })
    if (afterId) query.set('after_id', afterId)
    return request<GovernancePage<GeneralBudgetPolicy>>(`/budgets?${query}`, { signal })
  },
  generalBudget: (id: string, signal?: AbortSignal) => request<GeneralBudgetPolicy>(`/budgets/${encodeURIComponent(id)}`, { signal }),
  createGeneralBudget: (body: GeneralBudgetCreate, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>('/budgets', { method: 'POST', body: JSON.stringify(body), signal }, csrf),
  updateGeneralBudget: (id: string, body: GeneralBudgetUpdate, csrf: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>(`/budgets/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body), signal }, csrf),
  generalBudgetOperation: (operationId: string, signal?: AbortSignal) =>
    request<GovernanceReceipt>(`/budgets/operations/${encodeURIComponent(operationId)}`, { signal }),
  governanceObservations: (filters: GovernanceObservationFilters, cursor?: string, signal?: AbortSignal) =>
    request<GovernanceObservationsPage>(`/governance/observations?${governanceObservationSearch(filters, cursor)}`, { signal }),
  auditEvents: async (filters: AdminAuditFilters, cursor?: string, signal?: AbortSignal) =>
    parseAdminAuditPage(await request<unknown>(`/audit/events?${adminAuditSearch(filters, cursor)}`, { signal })),
  auditExport: adminAuditExport,
  saveUpstreamPrice: (upstreamId: string, body: { operation_id: string; expected_revision: number; upstream_model: string; price: PriceRate | null }, csrf: string) =>
    request<UpstreamPrice>(`/upstreams/${encodeURIComponent(upstreamId)}/prices`, { method: 'POST', body: JSON.stringify(body) }, csrf),
  billingSettings: (signal?: AbortSignal) => request<BillingSettings>('/billing/settings', { signal }),
  putBillingSettings: (body: { operation_id: string; expected_revision: number; enabled: boolean }, csrf: string) =>
    request<BillingReceipt>('/billing/settings', { method: 'PUT', body: JSON.stringify(body) }, csrf),
  billingBalance: (owner: BillingOwner, currency: string, signal?: AbortSignal) => {
    const query = new URLSearchParams({ owner_kind: owner.kind, employee_id: owner.employee_id, currency })
    if (owner.key_id) query.set('key_id', owner.key_id)
    if (owner.resource_kind) query.set('resource_kind', owner.resource_kind)
    if (owner.resource_id) query.set('resource_id', owner.resource_id)
    return request<BillingBalance>(`/billing/balances?${query}`, { signal })
  },
  billingEntries: (owner: BillingOwner, currency: string, afterId?: string, limit = 50, signal?: AbortSignal) => {
    const query = new URLSearchParams({ owner_kind: owner.kind, employee_id: owner.employee_id, currency, limit: String(limit) })
    if (owner.key_id) query.set('key_id', owner.key_id)
    if (owner.resource_kind) query.set('resource_kind', owner.resource_kind)
    if (owner.resource_id) query.set('resource_id', owner.resource_id)
    if (afterId) query.set('after_id', afterId)
    return request<BillingPage<BillingEntry>>(`/billing/entries?${query}`, { signal })
  },
  createBillingAdjustment: (body: { operation_id: string; owner: BillingOwner; currency: string; amount_micro: string }, csrf: string) =>
    request<BillingAdjustment>('/billing/adjustments', { method: 'POST', body: JSON.stringify(body) }, csrf),
  billingPlans: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingPlan>('/billing/plans', afterId, limit, signal),
  createBillingPlan: (body: BillingPlanWrite, csrf: string) => request<{ receipt: BillingReceipt; plan: BillingPlan }>('/billing/plans', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateBillingPlan: (id: string, body: BillingPlanWrite & { expected_revision: number }, csrf: string) => request<{ receipt: BillingReceipt; plan: BillingPlan }>(`/billing/plans/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  billingConnectors: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingConnector>('/billing/payment-connectors', afterId, limit, signal),
  createBillingConnector: (body: { operation_id: string; name: string; webhook_secret: string; enabled: boolean }, csrf: string) => request<{ receipt: BillingReceipt; connector: BillingConnector }>('/billing/payment-connectors', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateBillingConnector: (id: string, body: { operation_id: string; expected_revision: number; name: string; webhook_secret: string | null; enabled: boolean }, csrf: string) => request<{ receipt: BillingReceipt; connector: BillingConnector }>(`/billing/payment-connectors/${encodeURIComponent(id)}`, { method: 'PUT', body: JSON.stringify(body) }, csrf),
  billingTopUps: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingTopUp>('/billing/topups', afterId, limit, signal),
  createBillingTopUp: (body: { operation_id: string; owner: BillingOwner; connector_id: string; currency: string; amount_micro: string }, csrf: string) => request<{ receipt: BillingReceipt; topup: BillingTopUp }>('/billing/topups', { method: 'POST', body: JSON.stringify(body) }, csrf),
  billingSubscriptions: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingSubscription>('/billing/subscriptions', afterId, limit, signal),
  createBillingSubscription: (body: { operation_id: string; owner: BillingOwner; plan_id: string }, csrf: string) => request<{ receipt: BillingReceipt; subscription: BillingSubscription }>('/billing/subscriptions', { method: 'POST', body: JSON.stringify(body) }, csrf),
  cancelBillingSubscription: (id: string, body: { operation_id: string; expected_revision: number }, csrf: string) => request<{ receipt: BillingReceipt; subscription: BillingSubscription }>(`/billing/subscriptions/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: JSON.stringify(body) }, csrf),
  billingRedemptionCodes: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingRedemptionCode>('/billing/redemption-codes', afterId, limit, signal),
  createBillingRedemptionCode: (body: { operation_id: string; currency: string; amount_micro: string; max_uses: number; expires_at: string | null }, csrf: string) => request<{ receipt: BillingReceipt; redemption_code: BillingRedemptionCode; code: string | null }>('/billing/redemption-codes', { method: 'POST', body: JSON.stringify(body) }, csrf),
  redeemBillingCode: (body: { operation_id: string; owner: BillingOwner; code: string }, csrf: string) => request<{ receipt: BillingReceipt; entry_id: string; amount_micro: string; currency: string }>('/billing/redemptions', { method: 'POST', body: JSON.stringify(body) }, csrf),
  billingRefunds: (afterId?: string, limit = 50, signal?: AbortSignal) => billingList<BillingRefund>('/billing/refunds', afterId, limit, signal),
  createBillingRefund: (body: { operation_id: string; payment_id: string; amount_micro: string }, csrf: string) => request<{ receipt: BillingReceipt; refund: BillingRefund }>('/billing/refunds', { method: 'POST', body: JSON.stringify(body) }, csrf),
  status: (signal?: AbortSignal) => request<SystemStatus>('/system/status', { signal }),
	backupKeyProviders: (signal?: AbortSignal) => request<{ items: BackupKeyProvider[]; ready: boolean; store_ready: boolean; reason_code: string | null }>('/backups/key-providers', { signal }),
	createBackupKeyProvider: (csrf: string) => request<BackupKeyProvider>('/backups/key-providers', { method: 'POST', body: JSON.stringify({ kind: 'windows-dpapi-user' }) }, csrf),
	rotateBackupKeyProvider: (id: string, expectedRevision: number, csrf: string) => request<BackupKeyProvider>(`/backups/key-providers/${encodeURIComponent(id)}/rotate`, { method: 'POST', body: JSON.stringify({ expected_revision: expectedRevision }) }, csrf),
	activateBackupKeyProviderVersion: (id: string, expectedRevision: number, version: number, csrf: string) => request<BackupKeyProvider>(`/backups/key-providers/${encodeURIComponent(id)}/activate-version`, { method: 'POST', body: JSON.stringify({ expected_revision: expectedRevision, version }) }, csrf),
	backupPlans: (signal?: AbortSignal) => request<{ items: BackupPlan[] }>('/backups/plans', { signal }),
	createBackupPlan: (body: BackupPlanInput, csrf: string) => request<BackupPlan>('/backups/plans', { method: 'POST', body: JSON.stringify(body) }, csrf),
	updateBackupPlan: (id: string, body: { expected_revision: number } & Partial<BackupPlanInput>, csrf: string) => request<BackupPlan>(`/backups/plans/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
	disableBackupPlan: (id: string, expectedRevision: number, csrf: string) => request<{ result: 'disabled'; id: string; revision: number }>(`/backups/plans/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ expected_revision: expectedRevision }) }, csrf),
	backupRuns: (planId?: string, cursor?: string, signal?: AbortSignal) => {
	  const query = new URLSearchParams({ limit: '50' })
	  if (planId) query.set('plan_id', planId)
	  if (cursor) query.set('cursor', cursor)
	  return request<BackupRunsPage>(`/backups/runs?${query}`, { signal })
	},
	createBackupRun: (planId: string, expectedRevision: number, csrf: string) => request<BackupRun>('/backups/runs', { method: 'POST', body: JSON.stringify({ plan_id: planId, expected_revision: expectedRevision }) }, csrf),
	cancelBackupRun: (id: string, csrf: string) => request<{ result: 'cancellation_requested'; id: string }>(`/backups/runs/${encodeURIComponent(id)}/cancel`, { method: 'POST', body: '{}' }, csrf),
	scheduledTests: (signal?: AbortSignal) => request<{ items: ScheduledTestPlan[] }>('/scheduled-tests', { signal }),
	scheduledTest: (id: string, signal?: AbortSignal) => request<ScheduledTestPlan>(`/scheduled-tests/${encodeURIComponent(id)}`, { signal }),
	createScheduledTest: (body: ScheduledTestInput, csrf: string) => request<ScheduledTestPlan>('/scheduled-tests', { method: 'POST', body: JSON.stringify(body) }, csrf),
	updateScheduledTest: (id: string, body: { expected_revision: number } & Partial<ScheduledTestInput>, csrf: string) => request<ScheduledTestPlan>(`/scheduled-tests/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
	deleteScheduledTest: (id: string, revision: number, csrf: string) => request<{ result: 'archived' | 'already_archived'; id: string; revision: number }>(`/scheduled-tests/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ expected_revision: revision }) }, csrf),
	scheduledTestRuns: (id: string, cursor?: string, signal?: AbortSignal) => {
	  const query = new URLSearchParams({ limit: '50' })
	  if (cursor) query.set('cursor', cursor)
	  return request<ScheduledTestRunsPage>(`/scheduled-tests/${encodeURIComponent(id)}/runs?${query}`, { signal })
	},
  channelMonitors: (signal?: AbortSignal) => request<{ items: ChannelMonitorPlan[] }>('/channel-monitors', { signal }),
  createChannelMonitor: (body: ChannelMonitorInput, csrf: string) => request<ChannelMonitorPlan>('/channel-monitors', { method: 'POST', body: JSON.stringify(body) }, csrf),
  updateChannelMonitor: (id: string, body: { expected_revision: number; rebind?: true } & Partial<ChannelMonitorInput>, csrf: string) =>
    request<ChannelMonitorPlan>(`/channel-monitors/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(body) }, csrf),
  deleteChannelMonitor: (id: string, revision: number, csrf: string) =>
    request<{ result: 'archived' | 'already_archived'; id: string; revision: number }>(`/channel-monitors/${encodeURIComponent(id)}`, { method: 'DELETE', body: JSON.stringify({ expected_revision: revision }) }, csrf),
  channelMonitorRuns: (id: string, cursor?: string, signal?: AbortSignal) => {
    const query = new URLSearchParams({ limit: '50' })
    if (cursor) query.set('cursor', cursor)
    return request<ChannelMonitorRunsPage>(`/channel-monitors/${encodeURIComponent(id)}/runs?${query}`, { signal })
  },
  channelMonitorSummary: (id: string, signal?: AbortSignal) => request<ChannelMonitorSummary>(`/channel-monitors/${encodeURIComponent(id)}/summary`, { signal }),
  accountRecovery: () => request<AccountRecoveryStatus>('/account-recovery'),
  accountRecoveryAccounts: () => request<{items: AccountRecoveryState[]; server_time: string}>('/account-recovery/accounts'),
  setAccountRecovery: (enabled: boolean, revision: number, csrf: string) =>
    request<AccountRecoveryStatus>('/account-recovery', { method: 'PUT', body: JSON.stringify({ enabled, expected_revision: revision }) }, csrf),
}
