package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"cpacloud.local/server/internal/governance"
)

var governanceManagementTestTime = time.Date(2026, time.September, 23, 9, 30, 0, 123, time.UTC)

type governanceManagementFixture struct {
	base    *store
	manager *governanceManagementStore
	core    *governance.Coordinator
	dir     string
}

func newGovernanceManagementFixture(t *testing.T) *governanceManagementFixture {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	base, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.close() })
	core, err := governance.New(base.db, governance.Config{LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager, err := newGovernanceManagementStore(base.db, core)
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return governanceManagementTestTime }
	if err := manager.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	insertGovernanceManagementPrincipals(t, base.db)
	return &governanceManagementFixture{base: base, manager: manager, core: core, dir: dir}
}

func insertGovernanceManagementPrincipals(t *testing.T, db *sql.DB) {
	t.Helper()
	stamp := governanceManagementTestTime.Format(time.RFC3339Nano)
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO admins(id,username,password_hash,created_at) VALUES(?,?,X'01',?)`, []any{"admin-one", "admin-one", stamp}},
		{`INSERT INTO admins(id,username,password_hash,created_at) VALUES(?,?,X'02',?)`, []any{"admin-two", "admin-two", stamp}},
		{`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,?,'active','all',1,?)`, []any{"employee-one", "Employee One", stamp}},
		{`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES(?,?,'active','all',1,?)`, []any{"employee-two", "Employee Two", stamp}},
		{`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?, ?,X'01',1,?,?)`, []any{"key-one", "employee-one", "Key One", "selector-one", "key-operation-one", stamp}},
		{`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?, ?,X'02',1,?,?)`, []any{"key-two", "employee-two", "Key Two", "selector-two", "key-operation-two", stamp}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
}

func governanceOperationID(value int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012x", value)
}

func TestGovernanceManagementLifecycleIdempotencyAndResolve(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	settingsOne, err := f.manager.updateSettings(ctx, "admin-one", governanceOperationID(1), 1, true)
	if err != nil || settingsOne.Revision != 2 {
		t.Fatalf("settings one=%+v err=%v", settingsOne, err)
	}
	f.manager.now = func() time.Time { return governanceManagementTestTime.Add(-time.Hour) }
	settingsTwo, err := f.manager.updateSettings(ctx, "admin-one", governanceOperationID(2), 2, true)
	if err != nil || settingsTwo.Revision != 3 || settingsTwo.CreatedAt != settingsOne.CreatedAt {
		t.Fatalf("settings two=%+v err=%v", settingsTwo, err)
	}
	replayedSettings, err := f.manager.updateSettings(ctx, "admin-one", governanceOperationID(1), 1, true)
	if err != nil || replayedSettings != settingsOne {
		t.Fatalf("settings replay=%+v err=%v", replayedSettings, err)
	}

	f.manager.now = func() time.Time { return governanceManagementTestTime }
	groupCreated, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(3), "  Runtime Team  ", []string{"employee-one", "employee-one"})
	if err != nil || groupCreated.Revision != 1 {
		t.Fatalf("group create=%+v err=%v", groupCreated, err)
	}
	f.manager.now = func() time.Time { return governanceManagementTestTime.Add(-2 * time.Hour) }
	groupUpdated, err := f.manager.updateGroup(ctx, "admin-one", governanceOperationID(4), groupCreated.ResourceID, 1, "Runtime Team", []string{"employee-two", "employee-one"})
	if err != nil || groupUpdated.Revision != 2 || groupUpdated.CreatedAt != groupCreated.CreatedAt {
		t.Fatalf("group update=%+v err=%v", groupUpdated, err)
	}
	replayedGroup, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(3), "Runtime Team", []string{"employee-one"})
	if err != nil || replayedGroup != groupCreated {
		t.Fatalf("group replay=%+v err=%v", replayedGroup, err)
	}
	if _, err := f.manager.createGroup(ctx, "admin-two", governanceOperationID(3), "Runtime Team", []string{"employee-one"}); !errors.Is(err, errGovernanceManagementOperationConflict) {
		t.Fatalf("actor operation conflict=%v", err)
	}
	if _, err := f.manager.updateSettings(ctx, "admin-one", governanceOperationID(3), 3, false); !errors.Is(err, errGovernanceManagementOperationConflict) {
		t.Fatalf("action operation conflict=%v", err)
	}

	employeePolicy, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(5), governancePolicyInput{
		ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true,
		Hard: governanceHardLimits{RPM: governanceInt64(5)},
	})
	if err != nil {
		t.Fatal(err)
	}
	currency, window := "USD", governance.ShadowWindowRolling24h
	keyPolicy, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(6), governancePolicyInput{
		ScopeKind: governance.ScopeKey, ScopeID: "key-one", Enabled: true,
		Shadow: governanceShadowLimits{CostMicro: governanceInt64(math.MaxInt64), Currency: &currency, Window: &window},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(6), governancePolicyInput{
		ScopeKind: governance.ScopeKey, ScopeID: "key-one", Enabled: true,
		Shadow: governanceShadowLimits{CostMicro: governanceInt64(math.MaxInt64 - 1), Currency: &currency, Window: &window},
	}); !errors.Is(err, errGovernanceManagementOperationConflict) {
		t.Fatalf("changed cost operation conflict=%v", err)
	}
	groupPolicy, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(7), governancePolicyInput{
		ScopeKind: governance.ScopeGroup, ScopeID: groupCreated.ResourceID, Enabled: true,
		Shadow: governanceShadowLimits{TPM: governanceInt64(governance.MaxRevision)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if employeePolicy.ResourceID == keyPolicy.ResourceID || keyPolicy.ResourceID == groupPolicy.ResourceID {
		t.Fatal("policy IDs not unique")
	}

	tx, err := f.base.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, scopes, err := f.manager.ResolveScopesTx(ctx, tx, "employee-one", "key-one")
	_ = tx.Rollback()
	if err != nil || settings.Revision != 3 || !settings.Enabled || len(scopes) != 3 {
		t.Fatalf("resolve settings=%+v scopes=%+v err=%v", settings, scopes, err)
	}
	if scopes[0].Kind != governance.ScopeEmployee || scopes[1].Kind != governance.ScopeGroup || scopes[2].Kind != governance.ScopeKey {
		t.Fatalf("scope ordering=%+v", scopes)
	}
	if scopes[1].GroupRevision == nil || *scopes[1].GroupRevision != 2 || scopes[1].ShadowTPM == nil {
		t.Fatalf("group snapshot=%+v", scopes[1])
	}
	if scopes[2].ShadowCostMicro == nil || *scopes[2].ShadowCostMicro != math.MaxInt64 || scopes[2].ShadowCurrency != "USD" {
		t.Fatalf("key snapshot=%+v", scopes[2])
	}
	if _, err := f.manager.updatePolicy(ctx, "admin-one", governanceOperationID(8), keyPolicy.ResourceID, 1, governancePolicyInput{
		Enabled: false, Shadow: governanceShadowLimits{CostMicro: governanceInt64(math.MaxInt64), Currency: &currency, Window: &window},
	}); err != nil {
		t.Fatalf("disable key policy: %v", err)
	}
	tx, _ = f.base.db.BeginTx(ctx, nil)
	_, enabledScopes, err := f.manager.ResolveScopesTx(ctx, tx, "employee-one", "key-one")
	_ = tx.Rollback()
	if err != nil || len(enabledScopes) != 2 {
		t.Fatalf("enabled scopes=%+v err=%v", enabledScopes, err)
	}
	tx, _ = f.base.db.BeginTx(ctx, nil)
	_, _, err = f.manager.ResolveScopesTx(ctx, tx, "employee-two", "key-one")
	_ = tx.Rollback()
	if !errors.Is(err, errGovernanceManagementInvalid) {
		t.Fatalf("foreign key ownership error=%v", err)
	}

	var operations, audits int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_operations`).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_audit`).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if operations != 8 || audits != 8 {
		t.Fatalf("operations=%d audits=%d", operations, audits)
	}
	if err := f.manager.Migrate(ctx); err != nil {
		t.Fatalf("restart migration with history: %v", err)
	}
}

func TestGovernanceManagementScopeExistenceAndConcurrentCAS(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	missing := []governancePolicyInput{
		{ScopeKind: governance.ScopeEmployee, ScopeID: "missing-employee", Enabled: true, Hard: governanceHardLimits{RPM: governanceInt64(1)}},
		{ScopeKind: governance.ScopeKey, ScopeID: "missing-key", Enabled: true, Hard: governanceHardLimits{RPM: governanceInt64(1)}},
		{ScopeKind: governance.ScopeGroup, ScopeID: "missing-group", Enabled: true, Hard: governanceHardLimits{RPM: governanceInt64(1)}},
	}
	for index, input := range missing {
		if _, err := f.manager.createPolicy(ctx, "admin-one", governanceOperationID(60+index), input); !errors.Is(err, errGovernanceManagementNotFound) {
			t.Fatalf("missing scope %d error=%v", index, err)
		}
	}
	var operations int
	if err := f.base.db.QueryRow(`SELECT COUNT(*) FROM governance_management_operations`).Scan(&operations); err != nil || operations != 0 {
		t.Fatalf("missing scope operations=%d err=%v", operations, err)
	}
	group, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(63), "Concurrent", []string{"employee-one"})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		receipt governanceOperationReceipt
		err     error
	}
	results := make(chan result, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			receipt, err := f.manager.updateGroup(ctx, "admin-one", governanceOperationID(64+index), group.ResourceID, 1,
				fmt.Sprintf("Concurrent %d", index), []string{"employee-one"})
			results <- result{receipt, err}
		}()
	}
	allowed, conflicts := 0, 0
	for index := 0; index < 2; index++ {
		result := <-results
		switch {
		case result.err == nil && result.receipt.Revision == 2:
			allowed++
		case errors.Is(result.err, errGovernanceManagementRevisionConflict):
			conflicts++
		default:
			t.Fatalf("concurrent result=%+v", result)
		}
	}
	if allowed != 1 || conflicts != 1 {
		t.Fatalf("allowed=%d conflicts=%d", allowed, conflicts)
	}
}

func TestGovernanceManagementAtomicFailuresLimitsAndRevisionOverflow(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	if _, err := f.base.db.Exec(`CREATE TRIGGER governance_test_resource_failure BEFORE INSERT ON governance_groups BEGIN SELECT RAISE(ABORT,'synthetic resource failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(19), "Resource failure", nil); !errors.Is(err, errGovernanceManagementUnavailable) {
		t.Fatalf("resource failure=%v", err)
	}
	assertGovernanceManagementCounts(t, f.base.db, 0, 0, 0)
	if _, err := f.base.db.Exec(`DROP TRIGGER governance_test_resource_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`CREATE TRIGGER governance_test_operation_failure BEFORE INSERT ON governance_management_operations BEGIN SELECT RAISE(ABORT,'synthetic receipt failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(20), "Receipt failure", nil); !errors.Is(err, errGovernanceManagementUnavailable) {
		t.Fatalf("receipt failure=%v", err)
	}
	assertGovernanceManagementCounts(t, f.base.db, 0, 0, 0)
	if _, err := f.base.db.Exec(`DROP TRIGGER governance_test_operation_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`CREATE TRIGGER governance_test_audit_failure BEFORE INSERT ON governance_management_audit BEGIN SELECT RAISE(ABORT,'synthetic audit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(21), "Audit failure", nil); !errors.Is(err, errGovernanceManagementUnavailable) {
		t.Fatalf("audit failure=%v", err)
	}
	assertGovernanceManagementCounts(t, f.base.db, 0, 0, 0)
	if _, err := f.base.db.Exec(`DROP TRIGGER governance_test_audit_failure`); err != nil {
		t.Fatal(err)
	}
	created, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(21), "Audit failure", nil)
	if err != nil {
		t.Fatalf("same operation retry=%v", err)
	}
	if _, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(22), "Audit failure", nil); !errors.Is(err, errGovernanceManagementResourceConflict) {
		t.Fatalf("duplicate resource error=%v", err)
	}
	assertGovernanceManagementCounts(t, f.base.db, 1, 1, 1)

	if _, err := f.base.db.Exec(`UPDATE governance_groups SET revision=? WHERE id=?`, governance.MaxRevision, created.ResourceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.manager.updateGroup(ctx, "admin-one", governanceOperationID(23), created.ResourceID, governance.MaxRevision, "Overflow", nil); !errors.Is(err, errGovernanceManagementRevisionConflict) {
		t.Fatalf("group overflow=%v", err)
	}
	var revision int64
	if err := f.base.db.QueryRow(`SELECT revision FROM governance_groups WHERE id=?`, created.ResourceID).Scan(&revision); err != nil || revision != governance.MaxRevision {
		t.Fatalf("revision=%d err=%v", revision, err)
	}

	ids := make([]string, governanceManagementMaxMembers+1)
	for index := range ids {
		ids[index] = fmt.Sprintf("employee-%04d", index)
	}
	if _, _, ok := normalizeGovernanceGroup("Too large", ids); ok {
		t.Fatal("accepted group over member limit")
	}
	stamp := governanceManagementTestTime.Format(time.RFC3339Nano)
	for index := 0; index < governanceManagementMaxGroupsPerEmployee; index++ {
		id := fmt.Sprintf("existing-group-%02d", index)
		if _, err := f.base.db.Exec(`INSERT INTO governance_groups(id,name,revision,created_at,updated_at) VALUES(?,?,1,?,?)`, id, id, stamp, stamp); err != nil {
			t.Fatal(err)
		}
		if _, err := f.base.db.Exec(`INSERT INTO governance_group_members(group_id,employee_id) VALUES(?,?)`, id, "employee-one"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.manager.createGroup(ctx, "admin-one", governanceOperationID(24), "Membership overflow", []string{"employee-one"}); !errors.Is(err, errGovernanceManagementResourceConflict) {
		t.Fatalf("membership overflow=%v", err)
	}
}

func TestGovernanceManagementStrictMigrationRollbackAndStoredValidation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	base, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer base.close()
	core, err := governance.New(base.db, governance.Config{LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	manager, _ := newGovernanceManagementStore(base.db, core)
	if _, err := base.db.Exec(`CREATE TABLE governance_groups(id TEXT PRIMARY KEY,name TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if err := manager.Migrate(context.Background()); !errors.Is(err, errGovernanceManagementSchema) {
		t.Fatalf("bad schema error=%v", err)
	}
	for _, name := range []string{"governance_group_members", "governance_policies", "governance_management_operations", "governance_management_audit"} {
		var count int
		if err := base.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("partial object %s count=%d err=%v", name, count, err)
		}
	}
	if _, err := base.db.Exec(`DROP TABLE governance_groups`); err != nil {
		t.Fatal(err)
	}
	if err := manager.Migrate(context.Background()); err != nil {
		t.Fatalf("migration retry=%v", err)
	}
	insertGovernanceManagementPrincipals(t, base.db)
	stamp := governanceManagementTestTime.Format(time.RFC3339Nano)
	if _, err := base.db.Exec(`INSERT INTO governance_policies(id,scope_kind,scope_id,enabled,rpm_limit,revision,created_at,updated_at) VALUES('orphan','employee','missing',1,1,1,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if err := manager.Migrate(context.Background()); !errors.Is(err, errGovernanceManagementSchema) {
		t.Fatalf("orphan policy error=%v", err)
	}
	if _, err := base.db.Exec(`DELETE FROM governance_policies WHERE id='orphan'`); err != nil {
		t.Fatal(err)
	}
	if err := manager.Migrate(context.Background()); err != nil {
		t.Fatalf("stored repair retry=%v", err)
	}
}

func governanceInt64(value int64) *int64 { return &value }

func assertGovernanceManagementCounts(t *testing.T, db *sql.DB, groups, operations, audits int) {
	t.Helper()
	for table, want := range map[string]int{"governance_groups": groups, "governance_management_operations": operations, "governance_management_audit": audits} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil || got != want {
			t.Fatalf("%s count=%d want=%d err=%v", table, got, want, err)
		}
	}
}
