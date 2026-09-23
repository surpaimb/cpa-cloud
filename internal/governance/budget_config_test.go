package governance

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBudgetSchemaRejectsChangedUnknownModeLiteral(t *testing.T) {
	db := openGovernanceDB(t, filepath.Join(t.TempDir(), "budget-bad-schema.db"), 1)
	defer db.Close()
	badScopes := strings.Replace(scopesDDL, "'deny_unknown'", "'deny un known'", 1)
	for _, statement := range []string{settingsDDL, requestsDDL, badScopes, scopesIndexDDL, requestsEffectiveIndexDDL} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	coordinator := newTestCoordinator(t, db)
	tx, _ := db.BeginTx(context.Background(), nil)
	_, err := coordinator.SchemaVersionTx(context.Background(), tx)
	tx.Rollback()
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("changed literal error=%v", err)
	}
	if err := coordinator.Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("migration accepted changed literal: %v", err)
	}
}

func TestBudgetSchemaVersionLegacyUpgradeRollbackAndSnapshot(t *testing.T) {
	ctx := context.Background()
	db := openGovernanceDB(t, filepath.Join(t.TempDir(), "budget-upgrade.db"), 1)
	defer db.Close()
	coordinator := newTestCoordinator(t, db)

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := coordinator.SchemaVersionTx(ctx, tx); err != nil || version != 0 {
		t.Fatalf("empty version=%d err=%v", version, err)
	}
	tx.Rollback()

	for _, statement := range []string{legacySettingsDDL, legacyRequestsDDL, legacyScopesDDL, scopesIndexDDL, requestsEffectiveIndexDDL} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	stamp := formatTime(governanceStart)
	expires := formatTime(governanceStart.Add(time.Minute))
	if _, err := db.Exec(`INSERT INTO governance_settings(singleton,enabled,revision,last_effective_admission_at,updated_at) VALUES(1,1,4,?,?)`, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO governance_requests(id,employee_id,key_id,public_model,protocol,settings_revision,
		observed_started_at,effective_started_at,effective_lease_at,expires_at,status)
		VALUES('legacy-request','employee-one','key-one','public-model','openai-chat-completions',4,?,?,?,?,'pending')`, stamp, stamp, stamp, expires); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO governance_request_scopes(request_id,scope_kind,scope_id,policy_id,policy_revision,
		group_revision,rpm_limit,concurrency_limit,shadow_tpm,shadow_cost_micro,shadow_currency,shadow_window)
		VALUES('legacy-request','employee','employee-one','policy-one',3,NULL,5,NULL,NULL,NULL,'','')`); err != nil {
		t.Fatal(err)
	}

	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := coordinator.SchemaVersionTx(ctx, tx); err != nil || version != 1 {
		t.Fatalf("legacy version=%d err=%v", version, err)
	}
	if err := coordinator.MigrateTx(ctx, tx); err != nil {
		t.Fatal(err)
	}
	if version, err := coordinator.SchemaVersionTx(ctx, tx); err != nil || version != 2 {
		t.Fatalf("upgraded tx version=%d err=%v", version, err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, _ = db.BeginTx(ctx, nil)
	if version, err := coordinator.SchemaVersionTx(ctx, tx); err != nil || version != 1 {
		t.Fatalf("rollback version=%d err=%v", version, err)
	}
	tx.Rollback()

	if err := coordinator.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	settings, err := coordinator.Settings(ctx)
	if err != nil || !settings.Enabled || settings.BudgetEnabled || settings.Revision != 4 {
		t.Fatalf("migrated settings=%+v err=%v", settings, err)
	}
	var requestBudget int
	var hardTPM, hardCost any
	var hardCurrency, hardWindow, unknownMode string
	if err := db.QueryRow(`SELECT r.budget_enabled,s.hard_tpm,s.hard_cost_micro,s.hard_currency,s.hard_window,s.unknown_mode
		FROM governance_requests r JOIN governance_request_scopes s ON s.request_id=r.id WHERE r.id='legacy-request'`).
		Scan(&requestBudget, &hardTPM, &hardCost, &hardCurrency, &hardWindow, &unknownMode); err != nil {
		t.Fatal(err)
	}
	if requestBudget != 0 || hardTPM != nil || hardCost != nil || hardCurrency != "" || hardWindow != "" || unknownMode != "shadow" {
		t.Fatalf("legacy defaults budget=%d hard=%v/%v %q/%q mode=%q", requestBudget, hardTPM, hardCost, hardCurrency, hardWindow, unknownMode)
	}
}

func TestBudgetSettingsAndHardOnlyAdmissionAreImmutable(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "budget-settings.db"), 1)
	defer db.Close()
	ctx := context.Background()
	enabled, budgetEnabled := true, true
	tx, _ := db.BeginTx(ctx, nil)
	settings, err := coordinator.SetSettingsTx(ctx, tx, SettingsChange{ExpectedRevision: 1, Enabled: &enabled,
		BudgetEnabled: &budgetEnabled, UpdatedAt: governanceStart})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !settings.Enabled || !settings.BudgetEnabled || settings.Revision != 2 {
		t.Fatalf("settings=%+v", settings)
	}

	limit := int64(100)
	input := testAdmission("budget-request", governanceStart, ScopeSnapshot{Kind: ScopeEmployee, ID: "employee-1", PolicyID: "budget-policy",
		PolicyRevision: 7, HardTPM: &limit})
	input.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, input); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("admit lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	var requestBudget int
	var storedLimit int64
	var mode string
	if err := db.QueryRow(`SELECT r.budget_enabled,s.hard_tpm,s.unknown_mode FROM governance_requests r
		JOIN governance_request_scopes s ON s.request_id=r.id WHERE r.id=?`, input.RequestID).Scan(&requestBudget, &storedLimit, &mode); err != nil {
		t.Fatal(err)
	}
	if requestBudget != 1 || storedLimit != limit || mode != "shadow" {
		t.Fatalf("snapshot budget=%d limit=%d mode=%q", requestBudget, storedLimit, mode)
	}
	changed := input
	changed.Scopes = append([]ScopeSnapshot(nil), input.Scopes...)
	other := int64(101)
	changed.Scopes[0].HardTPM = &other
	if _, _, err := runAdmit(t, db, coordinator, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed hard replay error=%v", err)
	}

	tx, _ = db.BeginTx(ctx, nil)
	settings, err = coordinator.SetEnabledTx(ctx, tx, SettingsUpdate{ExpectedRevision: 2, Enabled: false, UpdatedAt: governanceStart.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || !settings.BudgetEnabled {
		t.Fatalf("legacy enable update cleared budget: %+v", settings)
	}
}
