package service

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAccountRecoverySchemaRejectsChangedEnumAndRetries(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`DROP TABLE account_recovery_states`); err != nil {
		t.Fatal(err)
	}
	badDDL := strings.Replace(accountRecoveryStateDDL, "'required'", "'REQUIRED'", 1)
	if _, err := f.base.app.store.db.Exec(badDDL); err != nil {
		t.Fatal(err)
	}
	if err := f.base.app.store.migrateAccountPoolRuntime(context.Background()); err == nil {
		t.Fatal("migration accepted changed recovery state enum")
	}
	var ddl string
	if err := f.base.app.store.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='account_recovery_states'`).Scan(&ddl); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ddl, "'REQUIRED'") {
		t.Fatal("failed migration changed source schema")
	}
	if _, err := f.base.app.store.db.Exec(`DROP TABLE account_recovery_states`); err != nil {
		t.Fatal(err)
	}
	if err := f.base.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
}

func TestAccountRecoveryStateCASAndCrossTableDuplicateLease(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
	f.insertAccount(t, "ups_recovery_cas", true)
	f.insertModelPool(t, "recovery-cas-model", "ups_recovery_cas", 1, modelAccountView{UpstreamID: "ups_recovery_cas", UpstreamModel: "provider-cas", Weight: 1, MaxConcurrency: 1})
	state := installRecoveryState(t, f, "ups_recovery_cas", "recovery-cas-model", "cool_recovery_cas", "probe_recovery_cas", 1)
	tx, err := f.base.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := putAccountRecoveryStateTx(context.Background(), tx, state); err != errAccountRecoveryConflict {
		tx.Rollback()
		t.Fatalf("same revision CAS err=%v", err)
	}
	tx.Rollback()

	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	now := f.clock.Now()
	duplicateID := "lease-cross-table"
	if _, err := f.base.app.store.db.Exec(`INSERT INTO account_pool_runtime_leases(lease_id,account_id,public_model,employee_id,key_id,pool_revision,account_revision,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?)`, duplicateID, state.AccountID, state.PublicModel, f.auth1.EmployeeID, f.auth1.KeyID, state.PoolRevision, state.AccountRevision, formatAccountPoolTime(now.Add(time.Minute)), formatAccountPoolTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`INSERT INTO account_pool_maintenance_leases(lease_id,operation_id,account_id,cooldown_event_id,recovery_revision,pool_revision,account_revision,public_model,upstream_model,provider_kind,protocol,dispatch_phase,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, duplicateID, state.OperationID, state.AccountID, state.CooldownEventID, state.RecoveryRevision, state.PoolRevision, state.AccountRevision, state.PublicModel, state.UpstreamModel, state.ProviderKind, state.Protocol, 1, formatAccountPoolTime(now.Add(time.Minute)), formatAccountPoolTime(now)); err != nil {
		t.Fatal(err)
	}
	if _, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, LeaseTTL: 30 * time.Second, MaxWaiters: 1}); err == nil {
		t.Fatal("restart accepted duplicate lease id across employee and maintenance tables")
	}
}

func TestAccountRecoveryRestartKeepsIsolationAcrossReimport(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
	f.insertAccount(t, "ups_recovery_reimport", true)
	f.insertModelPool(t, "recovery-reimport-model", "ups_recovery_reimport", 1, modelAccountView{UpstreamID: "ups_recovery_reimport", UpstreamModel: "provider-before", Weight: 1, MaxConcurrency: 1})
	state := installRecoveryState(t, f, "ups_recovery_reimport", "recovery-reimport-model", "cool_recovery_reimport", "probe_recovery_reimport", 1)
	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, state.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE model_account_pool_routes SET upstream_model='provider-after' WHERE model_id=? AND upstream_id=?`, state.PublicModel, state.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := f.base.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
		t.Fatalf("migration rejected stale isolation after reimport: %v", err)
	}
	restarted, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, LeaseTTL: 30 * time.Second, MaxWaiters: 2})
	if err != nil {
		t.Fatalf("runtime rejected stale isolation after reimport: %v", err)
	}
	defer restarted.Close()
	result := restarted.AcquireMaintenance(context.Background(), maintenanceRequest(state), nil)
	if result.Code != accountPoolConfigurationChanged || result.Lease != nil {
		t.Fatalf("stale recovery snapshot acquired=%+v", result)
	}
	employee := restarted.Acquire(context.Background(), state.PublicModel, f.auth1, []string{"openai-compatible"}, "")
	if employee.Code != accountPoolNoCompatible || employee.Lease != nil {
		t.Fatalf("stale isolation failed to block employee=%+v", employee)
	}
}
