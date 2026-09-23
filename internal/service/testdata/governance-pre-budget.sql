-- CPA Cloud legacy governance schema from own commit 4403958. Synthetic migration fixture.

CREATE TABLE IF NOT EXISTS governance_settings (
	singleton INTEGER PRIMARY KEY CHECK(singleton=1),
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	last_effective_admission_at TEXT,
	updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS governance_requests (
	id TEXT PRIMARY KEY,
	employee_id TEXT NOT NULL,
	key_id TEXT NOT NULL,
	public_model TEXT NOT NULL,
	protocol TEXT NOT NULL CHECK(protocol IN ('openai-chat-completions','openai-responses','anthropic-messages','gemini-generate-content')),
	settings_revision INTEGER NOT NULL CHECK(typeof(settings_revision)='integer' AND settings_revision BETWEEN 1 AND 9007199254740991),
	observed_started_at TEXT NOT NULL,
	effective_started_at TEXT NOT NULL,
	effective_lease_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	observed_finished_at TEXT,
	effective_finished_at TEXT,
	released_at TEXT,
	status TEXT NOT NULL CHECK(status IN ('pending','succeeded','failed','cancelled','interrupted')),
	CHECK(effective_started_at>=observed_started_at),
	CHECK(effective_lease_at>=effective_started_at),
	CHECK(expires_at>effective_lease_at),
	CHECK(effective_finished_at IS NULL OR effective_finished_at>=effective_lease_at),
	CHECK(released_at IS NULL OR released_at>=effective_lease_at),
	CHECK(
		(status='pending' AND observed_finished_at IS NULL AND effective_finished_at IS NULL AND released_at IS NULL)
		OR (status='interrupted' AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL)
		OR (status IN ('succeeded','failed','cancelled') AND observed_finished_at IS NOT NULL AND effective_finished_at IS NOT NULL AND released_at IS NOT NULL)
	)
);

CREATE TABLE IF NOT EXISTS governance_request_scopes (
	request_id TEXT NOT NULL REFERENCES governance_requests(id) ON DELETE CASCADE,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	policy_id TEXT NOT NULL,
	policy_revision INTEGER NOT NULL CHECK(typeof(policy_revision)='integer' AND policy_revision BETWEEN 1 AND 9007199254740991),
	group_revision INTEGER CHECK(
		(scope_kind='group' AND typeof(group_revision)='integer' AND group_revision BETWEEN 1 AND 9007199254740991)
		OR (scope_kind IN ('employee','key') AND group_revision IS NULL)
	),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit>0)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit>0)),
	shadow_tpm INTEGER CHECK(shadow_tpm IS NULL OR (typeof(shadow_tpm)='integer' AND shadow_tpm>0)),
	shadow_cost_micro INTEGER CHECK(shadow_cost_micro IS NULL OR (typeof(shadow_cost_micro)='integer' AND shadow_cost_micro>0)),
	shadow_currency TEXT NOT NULL,
	shadow_window TEXT NOT NULL,
	PRIMARY KEY(request_id,scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL OR shadow_tpm IS NOT NULL OR shadow_cost_micro IS NOT NULL),
	CHECK(
		(shadow_cost_micro IS NULL AND shadow_currency='' AND shadow_window='')
		OR (shadow_cost_micro IS NOT NULL AND length(shadow_currency)=3 AND shadow_currency GLOB '[A-Z][A-Z][A-Z]' AND shadow_window='rolling_24h')
	)
);

CREATE INDEX IF NOT EXISTS governance_request_scopes_scope_idx
	ON governance_request_scopes(scope_kind,scope_id,request_id);

CREATE INDEX IF NOT EXISTS governance_requests_effective_idx
	ON governance_requests(effective_started_at,id);

CREATE TABLE IF NOT EXISTS governance_groups (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(updated_at>=created_at)
);

CREATE TABLE IF NOT EXISTS governance_group_members (
	group_id TEXT NOT NULL REFERENCES governance_groups(id) ON DELETE RESTRICT,
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	PRIMARY KEY(group_id,employee_id)
);

CREATE INDEX IF NOT EXISTS governance_group_members_employee_idx
	ON governance_group_members(employee_id,group_id);

CREATE TABLE IF NOT EXISTS governance_policies (
	id TEXT PRIMARY KEY,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit BETWEEN 1 AND 9007199254740991)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit BETWEEN 1 AND 9007199254740991)),
	shadow_tpm INTEGER CHECK(shadow_tpm IS NULL OR (typeof(shadow_tpm)='integer' AND shadow_tpm BETWEEN 1 AND 9007199254740991)),
	shadow_cost_micro INTEGER CHECK(shadow_cost_micro IS NULL OR (typeof(shadow_cost_micro)='integer' AND shadow_cost_micro>0)),
	shadow_currency TEXT,
	shadow_window TEXT,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL OR shadow_tpm IS NOT NULL OR shadow_cost_micro IS NOT NULL),
	CHECK(
		(shadow_cost_micro IS NULL AND shadow_currency IS NULL AND shadow_window IS NULL)
		OR (shadow_cost_micro IS NOT NULL AND length(shadow_currency)=3 AND shadow_currency GLOB '[A-Z][A-Z][A-Z]' AND shadow_window='rolling_24h')
	),
	CHECK(updated_at>=created_at)
);

CREATE TABLE IF NOT EXISTS governance_management_operations (
	operation_id TEXT PRIMARY KEY,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('settings.update','group.create','group.update','policy.create','policy.update')),
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	resource_kind TEXT NOT NULL CHECK(resource_kind IN ('settings','group','policy')),
	resource_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS governance_management_audit (
	operation_id TEXT PRIMARY KEY REFERENCES governance_management_operations(operation_id) ON DELETE RESTRICT,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('settings.update','group.create','group.update','policy.create','policy.update')),
	resource_kind TEXT NOT NULL CHECK(resource_kind IN ('settings','group','policy')),
	resource_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
);