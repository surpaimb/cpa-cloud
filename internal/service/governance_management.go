package service

// Independently authored from docs/governance-management-contract.md. This
// store persists metadata-only administrator configuration. It does not
// authenticate employee requests, observe usage, or execute model traffic.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"cpacloud.local/server/internal/governance"
)

var (
	errGovernanceManagementInvalid           = errors.New("invalid governance management input")
	errGovernanceManagementNotFound          = errors.New("governance management resource not found")
	errGovernanceManagementRevisionConflict  = errors.New("governance management revision conflict")
	errGovernanceManagementOperationConflict = errors.New("governance management operation conflict")
	errGovernanceManagementResourceConflict  = errors.New("governance management resource conflict")
	errGovernanceManagementUnavailable       = errors.New("governance management storage unavailable")
	errGovernanceManagementSchema            = errors.New("invalid governance management schema")
)

const (
	governanceManagementMaxMembers           = 1000
	governanceManagementMaxGroupsPerEmployee = 64
	governanceManagementDefaultPage          = 50
	governanceManagementMaxPage              = 100
)

const governanceGroupsDDL = `CREATE TABLE IF NOT EXISTS governance_groups (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL UNIQUE,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	CHECK(updated_at>=created_at)
)`

const governanceGroupMembersDDL = `CREATE TABLE IF NOT EXISTS governance_group_members (
	group_id TEXT NOT NULL REFERENCES governance_groups(id) ON DELETE RESTRICT,
	employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE RESTRICT,
	PRIMARY KEY(group_id,employee_id)
)`

const governanceGroupMembersIndexDDL = `CREATE INDEX IF NOT EXISTS governance_group_members_employee_idx
	ON governance_group_members(employee_id,group_id)`

const legacyGovernancePoliciesDDL = `CREATE TABLE IF NOT EXISTS governance_policies (
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
)`

const governancePoliciesDDL = `CREATE TABLE IF NOT EXISTS governance_policies (
	id TEXT PRIMARY KEY,
	scope_kind TEXT NOT NULL CHECK(scope_kind IN ('employee','key','group')),
	scope_id TEXT NOT NULL,
	enabled INTEGER NOT NULL CHECK(typeof(enabled)='integer' AND enabled IN (0,1)),
	rpm_limit INTEGER CHECK(rpm_limit IS NULL OR (typeof(rpm_limit)='integer' AND rpm_limit BETWEEN 1 AND 9007199254740991)),
	concurrency_limit INTEGER CHECK(concurrency_limit IS NULL OR (typeof(concurrency_limit)='integer' AND concurrency_limit BETWEEN 1 AND 9007199254740991)),
	hard_tpm INTEGER CHECK(hard_tpm IS NULL OR (typeof(hard_tpm)='integer' AND hard_tpm BETWEEN 1 AND 9007199254740991)),
	hard_cost_micro INTEGER CHECK(hard_cost_micro IS NULL OR (typeof(hard_cost_micro)='integer' AND hard_cost_micro>0)),
	hard_currency TEXT,
	hard_window TEXT,
	unknown_mode TEXT NOT NULL CHECK(unknown_mode IN ('shadow','deny_unknown')),
	shadow_tpm INTEGER CHECK(shadow_tpm IS NULL OR (typeof(shadow_tpm)='integer' AND shadow_tpm BETWEEN 1 AND 9007199254740991)),
	shadow_cost_micro INTEGER CHECK(shadow_cost_micro IS NULL OR (typeof(shadow_cost_micro)='integer' AND shadow_cost_micro>0)),
	shadow_currency TEXT,
	shadow_window TEXT,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	UNIQUE(scope_kind,scope_id),
	CHECK(rpm_limit IS NOT NULL OR concurrency_limit IS NOT NULL OR hard_tpm IS NOT NULL OR hard_cost_micro IS NOT NULL OR shadow_tpm IS NOT NULL OR shadow_cost_micro IS NOT NULL),
	CHECK(
		(hard_cost_micro IS NULL AND hard_currency IS NULL AND hard_window IS NULL)
		OR (hard_cost_micro IS NOT NULL AND length(hard_currency)=3 AND hard_currency GLOB '[A-Z][A-Z][A-Z]' AND hard_window='rolling_24h')
	),
	CHECK(
		(shadow_cost_micro IS NULL AND shadow_currency IS NULL AND shadow_window IS NULL)
		OR (shadow_cost_micro IS NOT NULL AND length(shadow_currency)=3 AND shadow_currency GLOB '[A-Z][A-Z][A-Z]' AND shadow_window='rolling_24h')
	),
	CHECK(updated_at>=created_at)
)`

const governanceOperationsDDL = `CREATE TABLE IF NOT EXISTS governance_management_operations (
	operation_id TEXT PRIMARY KEY,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('settings.update','group.create','group.update','policy.create','policy.update')),
	payload_digest BLOB NOT NULL CHECK(typeof(payload_digest)='blob' AND length(payload_digest)=32),
	resource_kind TEXT NOT NULL CHECK(resource_kind IN ('settings','group','policy')),
	resource_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
)`

const governanceAuditDDL = `CREATE TABLE IF NOT EXISTS governance_management_audit (
	operation_id TEXT PRIMARY KEY REFERENCES governance_management_operations(operation_id) ON DELETE RESTRICT,
	actor_id TEXT NOT NULL REFERENCES admins(id) ON DELETE RESTRICT,
	action TEXT NOT NULL CHECK(action IN ('settings.update','group.create','group.update','policy.create','policy.update')),
	resource_kind TEXT NOT NULL CHECK(resource_kind IN ('settings','group','policy')),
	resource_id TEXT NOT NULL,
	revision INTEGER NOT NULL CHECK(typeof(revision)='integer' AND revision BETWEEN 1 AND 9007199254740991),
	created_at TEXT NOT NULL
)`

type governanceManagementStore struct {
	db   *sql.DB
	core *governance.Coordinator
	now  func() time.Time
}

type governanceOperationReceipt struct {
	OperationID  string `json:"operation_id"`
	ResourceKind string `json:"resource_kind"`
	ResourceID   string `json:"resource_id"`
	Revision     int64  `json:"revision"`
	CreatedAt    string `json:"created_at"`
}

type governanceGroupView struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	EmployeeIDs []string `json:"employee_ids"`
	Revision    int64    `json:"revision"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type governanceHardLimits struct {
	RPM         *int64 `json:"rpm"`
	Concurrency *int64 `json:"concurrency"`
}

type governanceShadowLimits struct {
	TPM       *int64  `json:"tpm"`
	CostMicro *int64  `json:"cost_micro,string"`
	Currency  *string `json:"currency"`
	Window    *string `json:"window"`
}

type governanceBudgetLimits struct {
	TPM         *int64  `json:"tpm"`
	CostMicro   *int64  `json:"cost_micro,string"`
	Currency    *string `json:"currency"`
	Window      *string `json:"window"`
	UnknownMode string  `json:"unknown_mode"`
}

type governancePolicyView struct {
	ID        string                 `json:"id"`
	ScopeKind governance.ScopeKind   `json:"scope_kind"`
	ScopeID   string                 `json:"scope_id"`
	Enabled   bool                   `json:"enabled"`
	Hard      governanceHardLimits   `json:"hard"`
	Budget    governanceBudgetLimits `json:"-"`
	Shadow    governanceShadowLimits `json:"-"`
	Revision  int64                  `json:"revision"`
	CreatedAt string                 `json:"created_at"`
	UpdatedAt string                 `json:"updated_at"`
}

type governancePolicyInput struct {
	ScopeKind governance.ScopeKind
	ScopeID   string
	Enabled   bool
	Hard      governanceHardLimits
	Budget    *governanceBudgetLimits
	Shadow    governanceShadowLimits
}

func newGovernanceManagementStore(db *sql.DB, core *governance.Coordinator) (*governanceManagementStore, error) {
	if db == nil || core == nil {
		return nil, errGovernanceManagementInvalid
	}
	return &governanceManagementStore{db: db, core: core, now: time.Now}, nil
}

func (s *governanceManagementStore) Migrate(ctx context.Context) error {
	if s == nil || s.db == nil || s.core == nil || ctx == nil {
		return errGovernanceManagementInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if err := s.MigrateTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return errGovernanceManagementUnavailable
	}
	return nil
}

func (s *governanceManagementStore) schemaVersionTx(ctx context.Context, tx *sql.Tx) (int, error) {
	if s == nil || s.db == nil || s.core == nil || ctx == nil || tx == nil {
		return 0, errGovernanceManagementInvalid
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name IN (
		'governance_groups','governance_group_members','governance_group_members_employee_idx','governance_policies',
		'governance_management_operations','governance_management_audit')`).Scan(&count); err != nil {
		return 0, errGovernanceManagementUnavailable
	}
	if count == 0 {
		return 0, nil
	}
	matched, err := governanceManagementSchemaMatches(ctx, tx, governancePoliciesDDL)
	if err != nil {
		return 0, err
	}
	if matched {
		return 2, nil
	}
	matched, err = governanceManagementSchemaMatches(ctx, tx, legacyGovernancePoliciesDDL)
	if err != nil {
		return 0, err
	}
	if matched {
		return 1, nil
	}
	return 0, errGovernanceManagementSchema
}

// MigrateTx upgrades or validates management metadata inside a caller-owned
// transaction. It does not commit or roll back.
func (s *governanceManagementStore) MigrateTx(ctx context.Context, tx *sql.Tx) error {
	version, err := s.schemaVersionTx(ctx, tx)
	if err != nil {
		return err
	}
	if version == 1 {
		if err := validateGovernanceManagementDataVersion(ctx, tx, 1); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE governance_policies RENAME TO governance_policies_legacy_budget`); err != nil {
			return errGovernanceManagementUnavailable
		}
		if _, err := tx.ExecContext(ctx, governancePoliciesDDL); err != nil {
			return errGovernanceManagementUnavailable
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO governance_policies(id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,
			hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window,
			revision,created_at,updated_at)
			SELECT id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,NULL,NULL,NULL,NULL,'shadow',shadow_tpm,shadow_cost_micro,
			shadow_currency,shadow_window,revision,created_at,updated_at FROM governance_policies_legacy_budget`); err != nil {
			return errGovernanceManagementUnavailable
		}
		if _, err := tx.ExecContext(ctx, `DROP TABLE governance_policies_legacy_budget`); err != nil {
			return errGovernanceManagementUnavailable
		}
	} else if version == 0 {
		for _, statement := range []string{governanceGroupsDDL, governanceGroupMembersDDL, governanceGroupMembersIndexDDL,
			governancePoliciesDDL, governanceOperationsDDL, governanceAuditDDL} {
			if _, err := tx.ExecContext(ctx, statement); err != nil {
				return errGovernanceManagementUnavailable
			}
		}
	}
	if err := validateGovernanceManagementSchema(ctx, tx); err != nil {
		return err
	}
	return validateGovernanceManagementDataVersion(ctx, tx, 2)
}

func (s *governanceManagementStore) updateSettings(ctx context.Context, actorID, operationID string, expectedRevision int64, enabled bool) (governanceOperationReceipt, error) {
	return s.updateSettingsPatch(ctx, actorID, operationID, expectedRevision, enabled, nil)
}

func (s *governanceManagementStore) updateSettingsPatch(ctx context.Context, actorID, operationID string, expectedRevision int64, enabled bool, budgetEnabled *bool) (governanceOperationReceipt, error) {
	legacyPayload := struct {
		ExpectedRevision int64 `json:"expected_revision"`
		Enabled          bool  `json:"enabled"`
	}{expectedRevision, enabled}
	var payload any = legacyPayload
	if budgetEnabled != nil {
		payload = struct {
			ExpectedRevision int64 `json:"expected_revision"`
			Enabled          bool  `json:"enabled"`
			BudgetEnabled    bool  `json:"budget_enabled"`
		}{expectedRevision, enabled, *budgetEnabled}
	}
	digest, err := governancePayloadDigest(payload)
	if err != nil || !validGovernanceActor(actorID) || !validGovernanceOperationID(operationID) || !validGovernanceRevision(expectedRevision) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if prior, found, err := loadGovernanceOperation(ctx, tx, operationID); err != nil {
		return governanceOperationReceipt{}, err
	} else if found {
		return prior.match(actorID, "settings.update", digest)
	}
	settings, err := readGovernanceSettingsTx(ctx, tx)
	if err != nil {
		return governanceOperationReceipt{}, err
	}
	updatedAt := s.effectiveTime(settings.UpdatedAt)
	updated, err := s.core.SetSettingsTx(ctx, tx, governance.SettingsChange{ExpectedRevision: expectedRevision, Enabled: &enabled,
		BudgetEnabled: budgetEnabled, UpdatedAt: updatedAt})
	if errors.Is(err, governance.ErrConflict) {
		return governanceOperationReceipt{}, errGovernanceManagementRevisionConflict
	}
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	receipt := governanceOperationReceipt{operationID, "settings", "singleton", updated.Revision, formatGovernanceTime(updatedAt)}
	if err := recordGovernanceOperation(ctx, tx, actorID, "settings.update", digest, receipt); err != nil {
		return governanceOperationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

func (s *governanceManagementStore) createGroup(ctx context.Context, actorID, operationID, name string, employeeIDs []string) (governanceOperationReceipt, error) {
	name, employeeIDs, ok := normalizeGovernanceGroup(name, employeeIDs)
	if !ok || !validGovernanceActor(actorID) || !validGovernanceOperationID(operationID) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	payload := struct {
		Name        string   `json:"name"`
		EmployeeIDs []string `json:"employee_ids"`
	}{name, employeeIDs}
	digest, _ := governancePayloadDigest(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if prior, found, err := loadGovernanceOperation(ctx, tx, operationID); err != nil {
		return governanceOperationReceipt{}, err
	} else if found {
		return prior.match(actorID, "group.create", digest)
	}
	if err := validateGovernanceMembers(ctx, tx, employeeIDs, ""); err != nil {
		return governanceOperationReceipt{}, err
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_groups WHERE name=?`, name).Scan(&duplicate); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if duplicate != 0 {
		return governanceOperationReceipt{}, errGovernanceManagementResourceConflict
	}
	id, err := newID("gg")
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	now := s.effectiveTime(time.Time{})
	stamp := formatGovernanceTime(now)
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_groups(id,name,revision,created_at,updated_at) VALUES(?,?,1,?,?)`, id, name, stamp, stamp); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if err := replaceGovernanceMembers(ctx, tx, id, employeeIDs); err != nil {
		return governanceOperationReceipt{}, err
	}
	receipt := governanceOperationReceipt{operationID, "group", id, 1, stamp}
	if err := recordGovernanceOperation(ctx, tx, actorID, "group.create", digest, receipt); err != nil {
		return governanceOperationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

func (s *governanceManagementStore) updateGroup(ctx context.Context, actorID, operationID, id string, expectedRevision int64, name string, employeeIDs []string) (governanceOperationReceipt, error) {
	name, employeeIDs, ok := normalizeGovernanceGroup(name, employeeIDs)
	if !ok || !validGovernanceActor(actorID) || !validGovernanceOperationID(operationID) || !validGovernanceMetadata(id, 256) || !validGovernanceRevision(expectedRevision) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	payload := struct {
		ID               string   `json:"id"`
		ExpectedRevision int64    `json:"expected_revision"`
		Name             string   `json:"name"`
		EmployeeIDs      []string `json:"employee_ids"`
	}{id, expectedRevision, name, employeeIDs}
	digest, _ := governancePayloadDigest(payload)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if prior, found, err := loadGovernanceOperation(ctx, tx, operationID); err != nil {
		return governanceOperationReceipt{}, err
	} else if found {
		return prior.match(actorID, "group.update", digest)
	}
	group, err := loadGovernanceGroup(ctx, tx, id)
	if err != nil {
		return governanceOperationReceipt{}, err
	}
	if group.Revision != expectedRevision || group.Revision >= governance.MaxRevision {
		return governanceOperationReceipt{}, errGovernanceManagementRevisionConflict
	}
	if err := validateGovernanceMembers(ctx, tx, employeeIDs, id); err != nil {
		return governanceOperationReceipt{}, err
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_groups WHERE name=? AND id<>?`, name, id).Scan(&duplicate); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if duplicate != 0 {
		return governanceOperationReceipt{}, errGovernanceManagementResourceConflict
	}
	now := s.effectiveTime(mustParseGovernanceTime(group.UpdatedAt))
	stamp := formatGovernanceTime(now)
	result, err := tx.ExecContext(ctx, `UPDATE governance_groups SET name=?,revision=revision+1,updated_at=? WHERE id=? AND revision=? AND revision<?`,
		name, stamp, id, expectedRevision, governance.MaxRevision)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return governanceOperationReceipt{}, errGovernanceManagementRevisionConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM governance_group_members WHERE group_id=?`, id); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if err := replaceGovernanceMembers(ctx, tx, id, employeeIDs); err != nil {
		return governanceOperationReceipt{}, err
	}
	receipt := governanceOperationReceipt{operationID, "group", id, expectedRevision + 1, stamp}
	if err := recordGovernanceOperation(ctx, tx, actorID, "group.update", digest, receipt); err != nil {
		return governanceOperationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

func (s *governanceManagementStore) createPolicy(ctx context.Context, actorID, operationID string, input governancePolicyInput) (governanceOperationReceipt, error) {
	if !validGovernanceActor(actorID) || !validGovernanceOperationID(operationID) || !validGovernancePolicyInput(input) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	digest, _ := governancePayloadDigest(governancePolicyDigestInput("", 0, input))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if prior, found, err := loadGovernanceOperation(ctx, tx, operationID); err != nil {
		return governanceOperationReceipt{}, err
	} else if found {
		return prior.match(actorID, "policy.create", digest)
	}
	if err := validateGovernanceScopeExists(ctx, tx, input.ScopeKind, input.ScopeID); err != nil {
		return governanceOperationReceipt{}, err
	}
	var duplicate int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_policies WHERE scope_kind=? AND scope_id=?`, input.ScopeKind, input.ScopeID).Scan(&duplicate); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if duplicate != 0 {
		return governanceOperationReceipt{}, errGovernanceManagementResourceConflict
	}
	id, err := newID("gp")
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	stamp := formatGovernanceTime(s.effectiveTime(time.Time{}))
	budget := normalizedGovernanceBudget(input.Budget)
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_policies(
		id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,hard_tpm,hard_cost_micro,hard_currency,hard_window,unknown_mode,
		shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window,revision,created_at,updated_at
	) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`, id, input.ScopeKind, input.ScopeID, boolGovernance(input.Enabled), nullableGovernanceInt(input.Hard.RPM),
		nullableGovernanceInt(input.Hard.Concurrency), nullableGovernanceInt(budget.TPM), nullableGovernanceInt(budget.CostMicro),
		nullableGovernanceString(budget.Currency), nullableGovernanceString(budget.Window), budget.UnknownMode,
		nullableGovernanceInt(input.Shadow.TPM), nullableGovernanceInt(input.Shadow.CostMicro),
		nullableGovernanceString(input.Shadow.Currency), nullableGovernanceString(input.Shadow.Window), stamp, stamp); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	receipt := governanceOperationReceipt{operationID, "policy", id, 1, stamp}
	if err := recordGovernanceOperation(ctx, tx, actorID, "policy.create", digest, receipt); err != nil {
		return governanceOperationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

func (s *governanceManagementStore) updatePolicy(ctx context.Context, actorID, operationID, id string, expectedRevision int64, input governancePolicyInput) (governanceOperationReceipt, error) {
	if !validGovernanceActor(actorID) || !validGovernanceOperationID(operationID) || !validGovernanceMetadata(id, 256) ||
		!validGovernanceRevision(expectedRevision) || !validGovernancePolicyComponents(input) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	digest, _ := governancePayloadDigest(governancePolicyDigestInput(id, expectedRevision, input))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	if prior, found, err := loadGovernanceOperation(ctx, tx, operationID); err != nil {
		return governanceOperationReceipt{}, err
	} else if found {
		return prior.match(actorID, "policy.update", digest)
	}
	policy, err := loadGovernancePolicy(ctx, tx, id)
	if err != nil {
		return governanceOperationReceipt{}, err
	}
	if policy.Revision != expectedRevision || policy.Revision >= governance.MaxRevision {
		return governanceOperationReceipt{}, errGovernanceManagementRevisionConflict
	}
	if input.Budget == nil {
		preserved := policy.Budget
		input.Budget = &preserved
	}
	if !validGovernancePolicyLimits(input) {
		return governanceOperationReceipt{}, errGovernanceManagementInvalid
	}
	input.ScopeKind, input.ScopeID = policy.ScopeKind, policy.ScopeID
	if err := validateGovernanceScopeExists(ctx, tx, input.ScopeKind, input.ScopeID); err != nil {
		return governanceOperationReceipt{}, err
	}
	now := s.effectiveTime(mustParseGovernanceTime(policy.UpdatedAt))
	stamp := formatGovernanceTime(now)
	budget := normalizedGovernanceBudget(input.Budget)
	result, err := tx.ExecContext(ctx, `UPDATE governance_policies SET enabled=?,rpm_limit=?,concurrency_limit=?,hard_tpm=?,hard_cost_micro=?,
		hard_currency=?,hard_window=?,unknown_mode=?,shadow_tpm=?,shadow_cost_micro=?,shadow_currency=?,shadow_window=?,
		revision=revision+1,updated_at=? WHERE id=? AND revision=? AND revision<?`, boolGovernance(input.Enabled),
		nullableGovernanceInt(input.Hard.RPM), nullableGovernanceInt(input.Hard.Concurrency), nullableGovernanceInt(budget.TPM),
		nullableGovernanceInt(budget.CostMicro), nullableGovernanceString(budget.Currency), nullableGovernanceString(budget.Window), budget.UnknownMode,
		nullableGovernanceInt(input.Shadow.TPM),
		nullableGovernanceInt(input.Shadow.CostMicro), nullableGovernanceString(input.Shadow.Currency), nullableGovernanceString(input.Shadow.Window),
		stamp, id, expectedRevision, governance.MaxRevision)
	if err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return governanceOperationReceipt{}, errGovernanceManagementRevisionConflict
	}
	receipt := governanceOperationReceipt{operationID, "policy", id, expectedRevision + 1, stamp}
	if err := recordGovernanceOperation(ctx, tx, actorID, "policy.update", digest, receipt); err != nil {
		return governanceOperationReceipt{}, err
	}
	if err := tx.Commit(); err != nil {
		return governanceOperationReceipt{}, errGovernanceManagementUnavailable
	}
	return receipt, nil
}

// ResolveScopesTx returns a settings and policy snapshot from the caller's
// transaction. The caller still owns employee/key authorization and admission
// locking. Key ownership is checked here so a foreign key policy cannot leak
// into another employee's request.
func (s *governanceManagementStore) ResolveScopesTx(ctx context.Context, tx *sql.Tx, employeeID, keyID string) (governance.Settings, []governance.ScopeSnapshot, error) {
	if s == nil || ctx == nil || tx == nil || !validGovernanceMetadata(employeeID, 256) || !validGovernanceMetadata(keyID, 256) {
		return governance.Settings{}, nil, errGovernanceManagementInvalid
	}
	settings, err := readGovernanceSettingsTx(ctx, tx)
	if err != nil {
		return governance.Settings{}, nil, err
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT employee_id FROM access_keys WHERE id=?`, keyID).Scan(&owner); errors.Is(err, sql.ErrNoRows) {
		return governance.Settings{}, nil, errGovernanceManagementNotFound
	} else if err != nil {
		return governance.Settings{}, nil, errGovernanceManagementUnavailable
	}
	if owner != employeeID {
		return governance.Settings{}, nil, errGovernanceManagementInvalid
	}
	rows, err := tx.QueryContext(ctx, `SELECT p.scope_kind,p.scope_id,p.id,p.revision,NULL,
		p.rpm_limit,p.concurrency_limit,p.hard_tpm,p.hard_cost_micro,p.hard_currency,p.hard_window,p.unknown_mode,
		p.shadow_tpm,p.shadow_cost_micro,p.shadow_currency,p.shadow_window
		FROM governance_policies p
		WHERE p.enabled=1 AND ((p.scope_kind='employee' AND p.scope_id=?) OR (p.scope_kind='key' AND p.scope_id=?))
		UNION ALL
		SELECT p.scope_kind,p.scope_id,p.id,p.revision,g.revision,
		p.rpm_limit,p.concurrency_limit,p.hard_tpm,p.hard_cost_micro,p.hard_currency,p.hard_window,p.unknown_mode,
		p.shadow_tpm,p.shadow_cost_micro,p.shadow_currency,p.shadow_window
		FROM governance_group_members m
		JOIN governance_groups g ON g.id=m.group_id
		JOIN governance_policies p ON p.scope_kind='group' AND p.scope_id=g.id AND p.enabled=1
		WHERE m.employee_id=?
		ORDER BY 1,2`, employeeID, keyID, employeeID)
	if err != nil {
		return governance.Settings{}, nil, errGovernanceManagementUnavailable
	}
	defer rows.Close()
	scopes := make([]governance.ScopeSnapshot, 0)
	for rows.Next() {
		var scope governance.ScopeSnapshot
		var groupRevision, rpm, concurrency, hardTPM, hardCost, shadowTPM, shadowCost sql.NullInt64
		var hardCurrency, hardWindow, currency, window sql.NullString
		if err := rows.Scan(&scope.Kind, &scope.ID, &scope.PolicyID, &scope.PolicyRevision, &groupRevision, &rpm, &concurrency,
			&hardTPM, &hardCost, &hardCurrency, &hardWindow, &scope.UnknownMode,
			&shadowTPM, &shadowCost, &currency, &window); err != nil {
			return governance.Settings{}, nil, errGovernanceManagementUnavailable
		}
		setGovernanceOptionalInt(&scope.GroupRevision, groupRevision)
		setGovernanceOptionalInt(&scope.RPMLimit, rpm)
		setGovernanceOptionalInt(&scope.ConcurrencyLimit, concurrency)
		setGovernanceOptionalInt(&scope.HardTPM, hardTPM)
		setGovernanceOptionalInt(&scope.HardCostMicro, hardCost)
		if hardCurrency.Valid {
			scope.HardCurrency = hardCurrency.String
		}
		if hardWindow.Valid {
			scope.HardWindow = hardWindow.String
		}
		setGovernanceOptionalInt(&scope.ShadowTPM, shadowTPM)
		setGovernanceOptionalInt(&scope.ShadowCostMicro, shadowCost)
		if currency.Valid {
			scope.ShadowCurrency = currency.String
		}
		if window.Valid {
			scope.ShadowWindow = window.String
		}
		scopes = append(scopes, scope)
	}
	if err := rows.Err(); err != nil {
		return governance.Settings{}, nil, errGovernanceManagementUnavailable
	}
	return settings, scopes, nil
}

func (s *governanceManagementStore) effectiveTime(floor time.Time) time.Time {
	now := s.now().UTC()
	if !floor.IsZero() && now.Before(floor) {
		return floor.UTC()
	}
	return now
}

func readGovernanceSettingsTx(ctx context.Context, tx *sql.Tx) (governance.Settings, error) {
	var item governance.Settings
	var enabled, budgetEnabled int
	var last sql.NullString
	var updated string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,budget_enabled,revision,last_effective_admission_at,updated_at FROM governance_settings WHERE singleton=1`).
		Scan(&enabled, &budgetEnabled, &item.Revision, &last, &updated); err != nil {
		return governance.Settings{}, errGovernanceManagementUnavailable
	}
	if enabled != 0 && enabled != 1 || budgetEnabled != 0 && budgetEnabled != 1 || !validGovernanceRevision(item.Revision) {
		return governance.Settings{}, errGovernanceManagementSchema
	}
	item.Enabled = enabled == 1
	item.BudgetEnabled = budgetEnabled == 1
	parsed, err := parseCanonicalGovernanceTime(updated)
	if err != nil {
		return governance.Settings{}, errGovernanceManagementSchema
	}
	item.UpdatedAt = parsed.UTC()
	if last.Valid {
		parsed, err := parseCanonicalGovernanceTime(last.String)
		if err != nil {
			return governance.Settings{}, errGovernanceManagementSchema
		}
		parsed = parsed.UTC()
		item.LastEffectiveAdmissionTime = &parsed
	}
	return item, nil
}

type storedGovernanceOperation struct {
	receipt governanceOperationReceipt
	actor   string
	action  string
	digest  []byte
}

func loadGovernanceOperation(ctx context.Context, tx *sql.Tx, id string) (storedGovernanceOperation, bool, error) {
	var stored storedGovernanceOperation
	err := tx.QueryRowContext(ctx, `SELECT operation_id,actor_id,action,payload_digest,resource_kind,resource_id,revision,created_at
		FROM governance_management_operations WHERE operation_id=?`, id).Scan(&stored.receipt.OperationID, &stored.actor, &stored.action, &stored.digest,
		&stored.receipt.ResourceKind, &stored.receipt.ResourceID, &stored.receipt.Revision, &stored.receipt.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return storedGovernanceOperation{}, false, nil
	}
	if err != nil {
		return storedGovernanceOperation{}, false, errGovernanceManagementUnavailable
	}
	return stored, true, nil
}

func (stored storedGovernanceOperation) match(actor, action string, digest []byte) (governanceOperationReceipt, error) {
	if stored.actor != actor || stored.action != action || !bytes.Equal(stored.digest, digest) {
		return governanceOperationReceipt{}, errGovernanceManagementOperationConflict
	}
	return stored.receipt, nil
}

func recordGovernanceOperation(ctx context.Context, tx *sql.Tx, actor, action string, digest []byte, receipt governanceOperationReceipt) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_management_operations(
		operation_id,actor_id,action,payload_digest,resource_kind,resource_id,revision,created_at
	) VALUES(?,?,?,?,?,?,?,?)`, receipt.OperationID, actor, action, digest, receipt.ResourceKind, receipt.ResourceID, receipt.Revision, receipt.CreatedAt); err != nil {
		return errGovernanceManagementUnavailable
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO governance_management_audit(
		operation_id,actor_id,action,resource_kind,resource_id,revision,created_at
	) VALUES(?,?,?,?,?,?,?)`, receipt.OperationID, actor, action, receipt.ResourceKind, receipt.ResourceID, receipt.Revision, receipt.CreatedAt); err != nil {
		return errGovernanceManagementUnavailable
	}
	return nil
}

func governancePayloadDigest(value any) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(encoded)
	return digest[:], nil
}

func normalizeGovernanceGroup(name string, employeeIDs []string) (string, []string, bool) {
	name = strings.TrimSpace(name)
	if !validGovernanceName(name) {
		return "", nil, false
	}
	ids := append([]string(nil), employeeIDs...)
	for _, id := range ids {
		if !validGovernanceMetadata(id, 256) {
			return "", nil, false
		}
	}
	sort.Strings(ids)
	unique := ids[:0]
	for _, id := range ids {
		if len(unique) == 0 || unique[len(unique)-1] != id {
			unique = append(unique, id)
		}
	}
	if len(unique) > governanceManagementMaxMembers {
		return "", nil, false
	}
	return name, unique, true
}

func validateGovernanceMembers(ctx context.Context, tx *sql.Tx, ids []string, excludingGroup string) error {
	for _, id := range ids {
		var exists, count int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM employees WHERE id=?`, id).Scan(&exists); err != nil {
			return errGovernanceManagementUnavailable
		}
		if exists != 1 {
			return errGovernanceManagementNotFound
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_group_members WHERE employee_id=? AND group_id<>?`, id, excludingGroup).Scan(&count); err != nil {
			return errGovernanceManagementUnavailable
		}
		if count >= governanceManagementMaxGroupsPerEmployee {
			return errGovernanceManagementResourceConflict
		}
	}
	return nil
}

func replaceGovernanceMembers(ctx context.Context, tx *sql.Tx, groupID string, ids []string) error {
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `INSERT INTO governance_group_members(group_id,employee_id) VALUES(?,?)`, groupID, id); err != nil {
			return errGovernanceManagementUnavailable
		}
	}
	return nil
}

func validateGovernanceScopeExists(ctx context.Context, tx *sql.Tx, kind governance.ScopeKind, id string) error {
	var query string
	switch kind {
	case governance.ScopeEmployee:
		query = `SELECT COUNT(*) FROM employees WHERE id=?`
	case governance.ScopeKey:
		query = `SELECT COUNT(*) FROM access_keys WHERE id=?`
	case governance.ScopeGroup:
		query = `SELECT COUNT(*) FROM governance_groups WHERE id=?`
	default:
		return errGovernanceManagementInvalid
	}
	var count int
	if err := tx.QueryRowContext(ctx, query, id).Scan(&count); err != nil {
		return errGovernanceManagementUnavailable
	}
	if count != 1 {
		return errGovernanceManagementNotFound
	}
	return nil
}

func validGovernancePolicyInput(input governancePolicyInput) bool {
	return (input.ScopeKind == governance.ScopeEmployee || input.ScopeKind == governance.ScopeKey || input.ScopeKind == governance.ScopeGroup) &&
		validGovernanceMetadata(input.ScopeID, 256) && validGovernancePolicyLimits(input)
}

func validGovernancePolicyLimits(input governancePolicyInput) bool {
	if !validGovernancePolicyComponents(input) {
		return false
	}
	budget := normalizedGovernanceBudget(input.Budget)
	return input.Hard.RPM != nil || input.Hard.Concurrency != nil || budget.TPM != nil || budget.CostMicro != nil || input.Shadow.TPM != nil || input.Shadow.CostMicro != nil
}

func validGovernancePolicyComponents(input governancePolicyInput) bool {
	budget := normalizedGovernanceBudget(input.Budget)
	if !validGovernanceSafeLimit(input.Hard.RPM) || !validGovernanceSafeLimit(input.Hard.Concurrency) ||
		!validGovernanceSafeLimit(budget.TPM) || !validGovernanceCostLimit(budget.CostMicro) || !validGovernanceBudget(budget) ||
		!validGovernanceSafeLimit(input.Shadow.TPM) || !validGovernanceCostLimit(input.Shadow.CostMicro) {
		return false
	}
	if input.Shadow.CostMicro == nil {
		return input.Shadow.Currency == nil && input.Shadow.Window == nil
	}
	if input.Shadow.Currency == nil || input.Shadow.Window == nil || len(*input.Shadow.Currency) != 3 || *input.Shadow.Window != governance.ShadowWindowRolling24h {
		return false
	}
	for _, b := range []byte(*input.Shadow.Currency) {
		if b < 'A' || b > 'Z' {
			return false
		}
	}
	return true
}

func normalizedGovernanceBudget(input *governanceBudgetLimits) governanceBudgetLimits {
	if input == nil {
		return governanceBudgetLimits{UnknownMode: "shadow"}
	}
	value := *input
	if value.UnknownMode == "" {
		value.UnknownMode = "shadow"
	}
	return value
}

func validGovernanceBudget(input governanceBudgetLimits) bool {
	if input.UnknownMode != "shadow" && input.UnknownMode != "deny_unknown" {
		return false
	}
	if input.CostMicro == nil {
		return input.Currency == nil && input.Window == nil
	}
	if input.Currency == nil || input.Window == nil || len(*input.Currency) != 3 || *input.Window != governance.ShadowWindowRolling24h {
		return false
	}
	for _, b := range []byte(*input.Currency) {
		if b < 'A' || b > 'Z' {
			return false
		}
	}
	return true
}

func governancePolicyDigestInput(id string, expected int64, input governancePolicyInput) any {
	legacy := struct {
		ID               string                 `json:"id,omitempty"`
		ExpectedRevision int64                  `json:"expected_revision,omitempty"`
		ScopeKind        governance.ScopeKind   `json:"scope_kind,omitempty"`
		ScopeID          string                 `json:"scope_id,omitempty"`
		Enabled          bool                   `json:"enabled"`
		Hard             governanceHardLimits   `json:"hard"`
		Shadow           governanceShadowLimits `json:"shadow"`
	}{id, expected, input.ScopeKind, input.ScopeID, input.Enabled, input.Hard, input.Shadow}
	if input.Budget == nil {
		return legacy
	}
	return struct {
		ID               string                 `json:"id,omitempty"`
		ExpectedRevision int64                  `json:"expected_revision,omitempty"`
		ScopeKind        governance.ScopeKind   `json:"scope_kind,omitempty"`
		ScopeID          string                 `json:"scope_id,omitempty"`
		Enabled          bool                   `json:"enabled"`
		Hard             governanceHardLimits   `json:"hard"`
		Budget           governanceBudgetLimits `json:"budget"`
		Shadow           governanceShadowLimits `json:"shadow"`
	}{id, expected, input.ScopeKind, input.ScopeID, input.Enabled, input.Hard, normalizedGovernanceBudget(input.Budget), input.Shadow}
}

type governanceManagementQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// The revision and full membership must come from one database snapshot.
// Otherwise a concurrent member replacement could return a revision paired
// with members from another version and undermine the editor's CAS baseline.
func (s *governanceManagementStore) group(ctx context.Context, id string) (governanceGroupView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return governanceGroupView{}, errGovernanceManagementUnavailable
	}
	defer tx.Rollback()
	return loadGovernanceGroup(ctx, tx, id)
}

func loadGovernanceGroup(ctx context.Context, query governanceManagementQueryer, id string) (governanceGroupView, error) {
	var item governanceGroupView
	err := query.QueryRowContext(ctx, `SELECT id,name,revision,created_at,updated_at FROM governance_groups WHERE id=?`, id).
		Scan(&item.ID, &item.Name, &item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errGovernanceManagementNotFound
	}
	if err != nil {
		return item, errGovernanceManagementUnavailable
	}
	rows, err := query.QueryContext(ctx, `SELECT employee_id FROM governance_group_members WHERE group_id=? ORDER BY employee_id`, id)
	if err != nil {
		return item, errGovernanceManagementUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var employee string
		if err := rows.Scan(&employee); err != nil {
			return item, errGovernanceManagementUnavailable
		}
		item.EmployeeIDs = append(item.EmployeeIDs, employee)
	}
	if err := rows.Err(); err != nil {
		return item, errGovernanceManagementUnavailable
	}
	if item.EmployeeIDs == nil {
		item.EmployeeIDs = []string{}
	}
	return item, nil
}

func loadGovernancePolicy(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string) (governancePolicyView, error) {
	var item governancePolicyView
	var enabled int
	var rpm, concurrency, hardTPM, hardCost, tpm, cost sql.NullInt64
	var hardCurrency, hardWindow, currency, window sql.NullString
	err := query.QueryRowContext(ctx, `SELECT id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,hard_tpm,hard_cost_micro,
		hard_currency,hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window,revision,created_at,updated_at
		FROM governance_policies WHERE id=?`, id).Scan(
		&item.ID, &item.ScopeKind, &item.ScopeID, &enabled, &rpm, &concurrency, &hardTPM, &hardCost, &hardCurrency, &hardWindow,
		&item.Budget.UnknownMode, &tpm, &cost, &currency, &window,
		&item.Revision, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, errGovernanceManagementNotFound
	}
	if err != nil {
		return item, errGovernanceManagementUnavailable
	}
	item.Enabled = enabled == 1
	setGovernanceOptionalInt(&item.Hard.RPM, rpm)
	setGovernanceOptionalInt(&item.Hard.Concurrency, concurrency)
	setGovernanceOptionalInt(&item.Budget.TPM, hardTPM)
	setGovernanceOptionalInt(&item.Budget.CostMicro, hardCost)
	if hardCurrency.Valid {
		value := hardCurrency.String
		item.Budget.Currency = &value
	}
	if hardWindow.Valid {
		value := hardWindow.String
		item.Budget.Window = &value
	}
	setGovernanceOptionalInt(&item.Shadow.TPM, tpm)
	setGovernanceOptionalInt(&item.Shadow.CostMicro, cost)
	if currency.Valid {
		value := currency.String
		item.Shadow.Currency = &value
	}
	if window.Valid {
		value := window.String
		item.Shadow.Window = &value
	}
	return item, nil
}

func setGovernanceOptionalInt(target **int64, source sql.NullInt64) {
	if source.Valid {
		value := source.Int64
		*target = &value
	}
}

func validGovernanceName(value string) bool {
	if value == "" || len([]byte(value)) > 128 || !utf8.ValidString(value) {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

func validGovernanceMetadata(value string, max int) bool {
	return value != "" && len([]byte(value)) <= max && utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validGovernanceActor(value string) bool { return validGovernanceMetadata(value, 256) }
func validGovernanceOperationID(value string) bool {
	return validUUIDOperation(value) && value == strings.ToLower(value)
}
func validGovernanceRevision(value int64) bool { return value >= 1 && value <= governance.MaxRevision }
func validGovernanceSafeLimit(value *int64) bool {
	return value == nil || *value >= 1 && *value <= governance.MaxRevision
}
func validGovernanceCostLimit(value *int64) bool { return value == nil || *value > 0 }
func boolGovernance(value bool) int {
	if value {
		return 1
	}
	return 0
}
func nullableGovernanceInt(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}
func nullableGovernanceString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func formatGovernanceTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }
func mustParseGovernanceTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func governanceManagementSchemaMatches(ctx context.Context, tx *sql.Tx, policyDDL string) (bool, error) {
	expected := map[string]string{
		"governance_groups": governanceGroupsDDL, "governance_group_members": governanceGroupMembersDDL,
		"governance_policies": policyDDL, "governance_management_operations": governanceOperationsDDL,
		"governance_management_audit": governanceAuditDDL,
	}
	for name, ddl := range expected {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, errGovernanceManagementUnavailable
		}
		if kind != "table" || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(ddl)) {
			return false, nil
		}
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='index' AND tbl_name IN (
		'governance_groups','governance_group_members','governance_policies','governance_management_operations','governance_management_audit') AND sql IS NOT NULL`)
	if err != nil {
		return false, errGovernanceManagementUnavailable
	}
	count := 0
	mismatch := false
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			rows.Close()
			return false, errGovernanceManagementUnavailable
		}
		if name != "governance_group_members_employee_idx" || normalizeGovernanceDDL(ddl) != normalizeGovernanceDDL(storedGovernanceDDL(governanceGroupMembersIndexDDL)) {
			mismatch = true
		}
		count++
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		return false, errGovernanceManagementUnavailable
	}
	if mismatch {
		return false, nil
	}
	if count != 1 {
		return false, nil
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN (
		'governance_groups','governance_group_members','governance_policies','governance_management_operations','governance_management_audit')`).Scan(&triggers); err != nil {
		return false, errGovernanceManagementUnavailable
	}
	return triggers == 0, nil
}

func validateGovernanceManagementSchema(ctx context.Context, tx *sql.Tx) error {
	for _, name := range []string{"admins", "employees", "access_keys", "governance_settings"} {
		var kind string
		if err := tx.QueryRowContext(ctx, `SELECT type FROM sqlite_master WHERE name=?`, name).Scan(&kind); err != nil || kind != "table" {
			return errGovernanceManagementSchema
		}
	}
	var foreignKeys int
	if err := tx.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		return errGovernanceManagementUnavailable
	}
	if foreignKeys != 1 {
		return errGovernanceManagementSchema
	}
	expected := map[string]string{
		"governance_groups":                governanceGroupsDDL,
		"governance_group_members":         governanceGroupMembersDDL,
		"governance_policies":              governancePoliciesDDL,
		"governance_management_operations": governanceOperationsDDL,
		"governance_management_audit":      governanceAuditDDL,
	}
	for name, ddl := range expected {
		var kind, actual string
		if err := tx.QueryRowContext(ctx, `SELECT type,sql FROM sqlite_master WHERE name=?`, name).Scan(&kind, &actual); err != nil ||
			kind != "table" || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(ddl)) {
			return errGovernanceManagementSchema
		}
	}
	var kind, table, actual string
	if err := tx.QueryRowContext(ctx, `SELECT type,tbl_name,sql FROM sqlite_master WHERE name='governance_group_members_employee_idx'`).Scan(&kind, &table, &actual); err != nil ||
		kind != "index" || table != "governance_group_members" || normalizeGovernanceDDL(actual) != normalizeGovernanceDDL(storedGovernanceDDL(governanceGroupMembersIndexDDL)) {
		return errGovernanceManagementSchema
	}
	rows, err := tx.QueryContext(ctx, `SELECT name,sql FROM sqlite_master WHERE type='index' AND tbl_name IN (
		'governance_groups','governance_group_members','governance_policies','governance_management_operations','governance_management_audit') AND sql IS NOT NULL`)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	count := 0
	for rows.Next() {
		var name, ddl string
		if err := rows.Scan(&name, &ddl); err != nil {
			rows.Close()
			return errGovernanceManagementUnavailable
		}
		if name != "governance_group_members_employee_idx" || normalizeGovernanceDDL(ddl) != normalizeGovernanceDDL(storedGovernanceDDL(governanceGroupMembersIndexDDL)) {
			rows.Close()
			return errGovernanceManagementSchema
		}
		count++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return errGovernanceManagementUnavailable
	}
	if err := rows.Close(); err != nil {
		return errGovernanceManagementUnavailable
	}
	if count != 1 {
		return errGovernanceManagementSchema
	}
	var triggers int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND tbl_name IN (
		'governance_groups','governance_group_members','governance_policies','governance_management_operations','governance_management_audit')`).Scan(&triggers); err != nil {
		return errGovernanceManagementUnavailable
	}
	if triggers != 0 {
		return errGovernanceManagementSchema
	}
	return nil
}

func validateGovernanceManagementData(ctx context.Context, tx *sql.Tx) error {
	return validateGovernanceManagementDataVersion(ctx, tx, 2)
}

func validateGovernanceManagementDataVersion(ctx context.Context, tx *sql.Tx, version int) error {
	if err := validateGovernanceSettingsForMigration(ctx, tx); err != nil {
		return errGovernanceManagementSchema
	}
	groupRows, err := tx.QueryContext(ctx, `SELECT id,name,revision,created_at,updated_at FROM governance_groups ORDER BY id`)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	for groupRows.Next() {
		var id, name, created, updated string
		var revision int64
		if err := groupRows.Scan(&id, &name, &revision, &created, &updated); err != nil {
			groupRows.Close()
			return errGovernanceManagementSchema
		}
		createdAt, e1 := parseCanonicalGovernanceTime(created)
		updatedAt, e2 := parseCanonicalGovernanceTime(updated)
		if !validGovernanceMetadata(id, 256) || !validGovernanceName(name) || strings.TrimSpace(name) != name || !validGovernanceRevision(revision) || e1 != nil || e2 != nil || updatedAt.Before(createdAt) {
			groupRows.Close()
			return errGovernanceManagementSchema
		}
	}
	if err := groupRows.Err(); err != nil {
		groupRows.Close()
		return errGovernanceManagementUnavailable
	}
	if err := groupRows.Close(); err != nil {
		return errGovernanceManagementUnavailable
	}
	var tooLarge int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT group_id FROM governance_group_members GROUP BY group_id HAVING COUNT(*)>1000)`).Scan(&tooLarge); err != nil {
		return errGovernanceManagementUnavailable
	}
	if tooLarge != 0 {
		return errGovernanceManagementSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT employee_id FROM governance_group_members GROUP BY employee_id HAVING COUNT(*)>64)`).Scan(&tooLarge); err != nil {
		return errGovernanceManagementUnavailable
	}
	if tooLarge != 0 {
		return errGovernanceManagementSchema
	}
	policyQuery := `SELECT id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,NULL,NULL,NULL,NULL,'shadow',
		shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window,revision,created_at,updated_at FROM governance_policies ORDER BY id`
	if version == 2 {
		policyQuery = `SELECT id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,hard_tpm,hard_cost_micro,hard_currency,
			hard_window,unknown_mode,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window,revision,created_at,updated_at
			FROM governance_policies ORDER BY id`
	}
	policyRows, err := tx.QueryContext(ctx, policyQuery)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	for policyRows.Next() {
		var id, kind, scopeID, created, updated string
		var enabled int
		var revision int64
		var rpm, concurrency, hardTPM, hardCost, tpm, cost sql.NullInt64
		var hardCurrency, hardWindow, currency, window sql.NullString
		var unknownMode string
		if err := policyRows.Scan(&id, &kind, &scopeID, &enabled, &rpm, &concurrency, &hardTPM, &hardCost, &hardCurrency, &hardWindow,
			&unknownMode, &tpm, &cost, &currency, &window, &revision, &created, &updated); err != nil {
			policyRows.Close()
			return errGovernanceManagementSchema
		}
		input := governancePolicyInput{ScopeKind: governance.ScopeKind(kind), ScopeID: scopeID, Enabled: enabled == 1}
		setGovernanceOptionalInt(&input.Hard.RPM, rpm)
		setGovernanceOptionalInt(&input.Hard.Concurrency, concurrency)
		budget := governanceBudgetLimits{UnknownMode: unknownMode}
		setGovernanceOptionalInt(&budget.TPM, hardTPM)
		setGovernanceOptionalInt(&budget.CostMicro, hardCost)
		if hardCurrency.Valid {
			v := hardCurrency.String
			budget.Currency = &v
		}
		if hardWindow.Valid {
			v := hardWindow.String
			budget.Window = &v
		}
		input.Budget = &budget
		setGovernanceOptionalInt(&input.Shadow.TPM, tpm)
		setGovernanceOptionalInt(&input.Shadow.CostMicro, cost)
		if currency.Valid {
			v := currency.String
			input.Shadow.Currency = &v
		}
		if window.Valid {
			v := window.String
			input.Shadow.Window = &v
		}
		createdAt, e1 := parseCanonicalGovernanceTime(created)
		updatedAt, e2 := parseCanonicalGovernanceTime(updated)
		if !validGovernanceMetadata(id, 256) || !validGovernanceRevision(revision) || enabled < 0 || enabled > 1 || !validGovernancePolicyInput(input) || e1 != nil || e2 != nil || updatedAt.Before(createdAt) {
			policyRows.Close()
			return errGovernanceManagementSchema
		}
		if err := validateGovernanceScopeExists(ctx, tx, input.ScopeKind, input.ScopeID); err != nil {
			policyRows.Close()
			return errGovernanceManagementSchema
		}
	}
	if err := policyRows.Err(); err != nil {
		policyRows.Close()
		return errGovernanceManagementUnavailable
	}
	if err := policyRows.Close(); err != nil {
		return errGovernanceManagementUnavailable
	}
	operationRows, err := tx.QueryContext(ctx, `SELECT operation_id,actor_id,action,payload_digest,resource_kind,resource_id,revision,created_at
		FROM governance_management_operations ORDER BY operation_id`)
	if err != nil {
		return errGovernanceManagementUnavailable
	}
	for operationRows.Next() {
		var operationID, actorID, action, resourceKind, resourceID, created string
		var digest []byte
		var revision int64
		if err := operationRows.Scan(&operationID, &actorID, &action, &digest, &resourceKind, &resourceID, &revision, &created); err != nil {
			operationRows.Close()
			return errGovernanceManagementSchema
		}
		_, timeErr := parseCanonicalGovernanceTime(created)
		if !validGovernanceOperationID(operationID) || !validGovernanceActor(actorID) || !validGovernanceActionResource(action, resourceKind, revision) ||
			!validGovernanceMetadata(resourceID, 256) || !validGovernanceRevision(revision) || len(digest) != sha256.Size || timeErr != nil {
			operationRows.Close()
			return errGovernanceManagementSchema
		}
		var resources int
		switch resourceKind {
		case "settings":
			if resourceID != "singleton" {
				operationRows.Close()
				return errGovernanceManagementSchema
			}
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_settings WHERE singleton=1`).Scan(&resources)
		case "group":
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_groups WHERE id=?`, resourceID).Scan(&resources)
		case "policy":
			err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_policies WHERE id=?`, resourceID).Scan(&resources)
		}
		if err != nil {
			operationRows.Close()
			return errGovernanceManagementUnavailable
		}
		if resources != 1 {
			operationRows.Close()
			return errGovernanceManagementSchema
		}
	}
	if err := operationRows.Err(); err != nil {
		operationRows.Close()
		return errGovernanceManagementUnavailable
	}
	if err := operationRows.Close(); err != nil {
		return errGovernanceManagementUnavailable
	}
	var mismatches int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_management_operations o LEFT JOIN governance_management_audit a ON a.operation_id=o.operation_id
		WHERE a.operation_id IS NULL OR a.actor_id<>o.actor_id OR a.action<>o.action OR a.resource_kind<>o.resource_kind OR a.resource_id<>o.resource_id OR a.revision<>o.revision OR a.created_at<>o.created_at`).Scan(&mismatches); err != nil {
		return errGovernanceManagementUnavailable
	}
	if mismatches != 0 {
		return errGovernanceManagementSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_groups g LEFT JOIN governance_management_operations o
		ON o.resource_kind='group' AND o.resource_id=g.id AND o.action='group.create' WHERE o.operation_id IS NULL`).Scan(&mismatches); err != nil {
		return errGovernanceManagementUnavailable
	}
	if mismatches != 0 {
		return errGovernanceManagementSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_policies p LEFT JOIN governance_management_operations o
		ON o.resource_kind='policy' AND o.resource_id=p.id AND o.action='policy.create' WHERE o.operation_id IS NULL`).Scan(&mismatches); err != nil {
		return errGovernanceManagementUnavailable
	}
	if mismatches != 0 {
		return errGovernanceManagementSchema
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM governance_management_audit a LEFT JOIN governance_management_operations o ON o.operation_id=a.operation_id WHERE o.operation_id IS NULL`).Scan(&mismatches); err != nil {
		return errGovernanceManagementUnavailable
	}
	if mismatches != 0 {
		return errGovernanceManagementSchema
	}
	for _, table := range []string{"governance_group_members", "governance_management_operations", "governance_management_audit"} {
		rows, err := tx.QueryContext(ctx, `PRAGMA foreign_key_check(`+table+`)`)
		if err != nil {
			return errGovernanceManagementUnavailable
		}
		violation := rows.Next()
		iterationErr := rows.Err()
		closeErr := rows.Close()
		if iterationErr != nil || closeErr != nil {
			return errGovernanceManagementUnavailable
		}
		if violation {
			return errGovernanceManagementSchema
		}
	}
	return nil
}

func validateGovernanceSettingsForMigration(ctx context.Context, tx *sql.Tx) error {
	var budgetColumns int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('governance_settings') WHERE name='budget_enabled'`).Scan(&budgetColumns); err != nil {
		return errGovernanceManagementUnavailable
	}
	if budgetColumns == 1 {
		_, err := readGovernanceSettingsTx(ctx, tx)
		return err
	}
	if budgetColumns != 0 {
		return errGovernanceManagementSchema
	}
	var enabled int
	var revision int64
	var last sql.NullString
	var updated string
	if err := tx.QueryRowContext(ctx, `SELECT enabled,revision,last_effective_admission_at,updated_at FROM governance_settings WHERE singleton=1`).
		Scan(&enabled, &revision, &last, &updated); err != nil {
		return errGovernanceManagementSchema
	}
	if enabled < 0 || enabled > 1 || !validGovernanceRevision(revision) {
		return errGovernanceManagementSchema
	}
	if _, err := parseCanonicalGovernanceTime(updated); err != nil {
		return err
	}
	if last.Valid {
		if _, err := parseCanonicalGovernanceTime(last.String); err != nil {
			return err
		}
	}
	return nil
}

func validGovernanceActionResource(action, resourceKind string, revision int64) bool {
	switch action {
	case "settings.update":
		return resourceKind == "settings" && revision >= 2
	case "group.create":
		return resourceKind == "group" && revision == 1
	case "group.update":
		return resourceKind == "group" && revision >= 2
	case "policy.create":
		return resourceKind == "policy" && revision == 1
	case "policy.update":
		return resourceKind == "policy" && revision >= 2
	default:
		return false
	}
}

func parseCanonicalGovernanceTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || parsed.Location() != time.UTC {
		return time.Time{}, errGovernanceManagementSchema
	}
	return parsed, nil
}

func storedGovernanceDDL(statement string) string {
	statement = strings.Replace(statement, "CREATE TABLE IF NOT EXISTS", "CREATE TABLE", 1)
	return strings.Replace(statement, "CREATE INDEX IF NOT EXISTS", "CREATE INDEX", 1)
}

func normalizeGovernanceDDL(statement string) string {
	var output strings.Builder
	var quote byte
	for index := 0; index < len(statement); index++ {
		character := statement[index]
		if quote != 0 {
			output.WriteByte(character)
			if character == quote {
				if index+1 < len(statement) && statement[index+1] == quote {
					index++
					output.WriteByte(statement[index])
				} else {
					quote = 0
				}
			}
			continue
		}
		switch character {
		case '\'', '"', '`':
			quote = character
			output.WriteByte(character)
		case ' ', '\t', '\r', '\n':
		default:
			if character >= 'A' && character <= 'Z' {
				character += 'a' - 'A'
			}
			output.WriteByte(character)
		}
	}
	return output.String()
}
