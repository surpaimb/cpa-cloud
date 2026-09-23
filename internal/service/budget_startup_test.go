package service

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"strings"
	"testing"

	"cpacloud.local/server/internal/governance"
)

//go:embed testdata/governance-pre-budget.sql
var preBudgetSchema string

func TestBudgetStartupJointLegacyMigrationRollbackRetry(t *testing.T) {
	f := newGovernanceManagementFixture(t)
	ctx := context.Background()
	for _, table := range []string{"governance_management_audit", "governance_management_operations", "governance_group_members", "governance_policies", "governance_groups", "governance_request_scopes", "governance_requests", "governance_settings"} {
		if _, err := f.base.db.Exec(`DROP TABLE ` + table); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.base.db.Exec(preBudgetSchema); err != nil {
		t.Fatal(err)
	}
	stamp := formatGovernanceTime(governanceManagementTestTime)
	if _, err := f.base.db.Exec(`INSERT INTO governance_settings VALUES(1,1,9,NULL,?); INSERT INTO governance_policies(id,scope_kind,scope_id,enabled,rpm_limit,revision,created_at,updated_at) VALUES('old-policy','employee','employee-one',1,20,6,?,?)`, stamp, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	// Historical policies must retain their paired creation receipt and audit.
	// Omitting them creates an invalid old database, not a migration fixture.
	op := governanceOperationID(985)
	limit := int64(20)
	input := governancePolicyInput{ScopeKind: governance.ScopeEmployee, ScopeID: "employee-one", Enabled: true, Hard: governanceHardLimits{RPM: &limit}}
	digest, err := governancePayloadDigest(governancePolicyDigestInput("", 0, input))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`INSERT INTO governance_management_operations VALUES(?,'admin-one','policy.create',?,'policy','old-policy',1,?)`, op, digest, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.db.Exec(`INSERT INTO governance_management_audit VALUES(?,'admin-one','policy.create','policy','old-policy',1,?)`, op, stamp); err != nil {
		t.Fatal(err)
	}
	a := &App{store: f.base, usage: newUsageLedgerCoordinator(f.base.db)}
	if err := a.usage.ledger.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	budget := governance.NewBudget(f.base.db)
	if _, err := f.base.db.Exec(`CREATE VIEW governance_budget_clock AS SELECT 1 singleton,'bad' last_effective_at`); err != nil {
		t.Fatal(err)
	}
	if err := a.migrateGovernanceBudget(ctx, f.core, f.manager, budget); err == nil {
		t.Fatal("invalid budget sibling accepted")
	}
	tx, _ := f.base.db.BeginTx(ctx, nil)
	coreVersion, err1 := f.core.SchemaVersionTx(ctx, tx)
	policyVersion, err2 := f.manager.schemaVersionTx(ctx, tx)
	tx.Rollback()
	if err1 != nil || err2 != nil || coreVersion != 1 || policyVersion != 1 {
		t.Fatalf("partial migration: %d %d %v %v", coreVersion, policyVersion, err1, err2)
	}
	if _, err := f.base.db.Exec(`DROP VIEW governance_budget_clock`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.migrateGovernanceBudget(ctx, f.core, f.manager, budget); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := f.core.Settings(ctx)
	if err != nil || settings.Revision != 9 || settings.BudgetEnabled || !settings.Enabled {
		t.Fatalf("settings lost: %+v %v", settings, err)
	}
	policy, err := loadGovernancePolicy(ctx, f.base.db, "old-policy")
	if err != nil || policy.Revision != 6 || policy.Budget.TPM != nil || policy.Budget.UnknownMode != "shadow" {
		t.Fatalf("policy lost: %+v %v", policy, err)
	}
	receipt, err := f.manager.createPolicy(ctx, "admin-one", op, input)
	if err != nil || receipt.ResourceID != "old-policy" || receipt.Revision != 1 {
		t.Fatalf("old operation replay changed: %+v %v", receipt, err)
	}
	var keyDigest string
	if err := f.base.db.QueryRow(`SELECT hex(digest) FROM access_keys WHERE id='key-one'`).Scan(&keyDigest); err != nil || keyDigest != "01" {
		t.Fatal("employee key was changed")
	}
}

func TestBudgetStartupRecoveryRollbackIncludesReservation(t *testing.T) {
	a, server, key := newBudgetHTTPFixture(t, 3000000)
	// Keep a mark commit uncertain and reject its cleanup to simulate a crash
	// before the known original attempt can converge to its terminal receipt.
	a.budgetCommit = func(stage string, tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		if stage == "mark" {
			_, err := a.store.db.Exec(`CREATE TRIGGER stop_budget_finish BEFORE UPDATE OF lifecycle ON governance_budget_reservations WHEN NEW.lifecycle='interrupted' BEGIN SELECT RAISE(ABORT,'synthetic'); END`)
			if err != nil {
				return err
			}
			return errors.New("synthetic response loss")
		}
		return nil
	}
	if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 503 || !strings.Contains(body, "budget_unavailable") {
		t.Fatalf("status=%d %s", code, body)
	}
	if err := a.recoverRequestLedgers(context.Background(), a.governance.core); err == nil {
		t.Fatal("budget recovery rejection was ignored")
	}
	assertJointRecoveryStatuses(t, a, "pending")
	if _, err := a.store.db.Exec(`DROP TRIGGER stop_budget_finish`); err != nil {
		t.Fatal(err)
	}
	if err := a.recoverRequestLedgers(context.Background(), a.governance.core); err != nil {
		t.Fatal(err)
	}
	assertJointRecoveryStatuses(t, a, "interrupted")
	var state string
	var actual *int64
	if err := a.store.db.QueryRow(`SELECT lifecycle,actual_tokens FROM governance_budget_reservations`).Scan(&state, &actual); err != nil || state != "interrupted" || actual != nil {
		t.Fatalf("reservation %s %v %v", state, actual, err)
	}
}
