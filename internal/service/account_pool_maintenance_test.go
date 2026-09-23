package service

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

func installRecoveryState(t *testing.T, f *runtimeFixture, account, model, event, operation string, recoveryRevision int64) accountRecoveryState {
	t.Helper()
	now := f.clock.Now()
	if _, err := f.base.app.store.db.Exec(`INSERT INTO account_pool_runtime_cooldowns(account_id,event_id,failure_class,cooldown_until,updated_at) VALUES(?,?,?,?,?)`, account, event, string(scheduling.FailureTransient), formatAccountPoolTime(now.Add(-time.Second)), formatAccountPoolTime(now.Add(-2*time.Second))); err != nil {
		t.Fatal(err)
	}
	var accountRevision, poolRevision int64
	var provider, upstreamModel string
	if err := f.base.app.store.db.QueryRow(`SELECT u.revision,c.revision,u.provider_kind,r.upstream_model FROM upstreams u JOIN model_account_pool_routes r ON r.upstream_id=u.id JOIN model_account_pool_configs c ON c.model_id=r.model_id WHERE u.id=? AND r.model_id=?`, account, model).Scan(&accountRevision, &poolRevision, &provider, &upstreamModel); err != nil {
		t.Fatal(err)
	}
	state := accountRecoveryState{
		AccountID: account, CooldownEventID: event, OperationID: operation, RecoveryRevision: recoveryRevision,
		PoolRevision: poolRevision, AccountRevision: accountRevision, ProviderKind: provider, SourceSnapshot: "api_key",
		PublicModel: model, UpstreamModel: upstreamModel, Protocol: "openai-chat-completions", State: recoveryRequired,
		NextProbeAt: now.Add(-time.Second), CreatedAt: now.Add(-time.Minute), UpdatedAt: now,
	}
	tx, err := f.base.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := putAccountRecoveryStateTx(context.Background(), tx, state); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.rt.NotifyChanged()
	return state
}

func maintenanceRequest(state accountRecoveryState) accountMaintenanceAcquireRequest {
	return accountMaintenanceAcquireRequest{OperationID: state.OperationID, AccountID: state.AccountID, CooldownEventID: state.CooldownEventID, ExpectedPoolRevision: state.PoolRevision, ExpectedAccountRevision: state.AccountRevision, ExpectedRecoveryRevision: state.RecoveryRevision}
}

func beginMaintenanceState(ctx context.Context, tx *sql.Tx, state accountRecoveryState) error {
	result, err := tx.ExecContext(ctx, `UPDATE account_recovery_states SET state='in_progress',updated_at=? WHERE account_id=? AND operation_id=? AND recovery_revision=? AND state='required'`, formatAccountPoolTime(state.UpdatedAt.Add(time.Nanosecond)), state.AccountID, state.OperationID, state.RecoveryRevision)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed != 1 {
		return errAccountRecoveryConflict
	}
	return nil
}

func TestMaintenanceLeaseIsolationDispatchAndFinalize(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_maintenance", true)
	f.insertModelPool(t, "maintenance-model", "ups_maintenance", 3, modelAccountView{UpstreamID: "ups_maintenance", UpstreamModel: "provider-maintenance", Weight: 1, MaxConcurrency: 1})
	state := installRecoveryState(t, f, "ups_maintenance", "maintenance-model", "cool_maintenance", "probe_maintenance", 1)

	if result := f.rt.Acquire(context.Background(), "maintenance-model", f.auth1, []string{"openai-compatible"}, ""); result.Code != accountPoolNoCompatible || result.Lease != nil {
		t.Fatalf("isolated account employee admission=%+v", result)
	}
	acquired := f.rt.AcquireMaintenance(context.Background(), maintenanceRequest(state), beginMaintenanceState)
	if acquired.Code != accountPoolAcquired || acquired.Lease == nil || acquired.Route.AccountID != state.AccountID || acquired.State.OperationID != state.OperationID {
		t.Fatalf("maintenance acquire=%+v", acquired)
	}
	lease := acquired.Lease
	if code := lease.MarkDispatch(context.Background(), func(context.Context, *sql.Tx) error { return errors.New("synthetic ledger failure") }); code != accountPoolStorageUnavailable {
		t.Fatalf("failed mark dispatch code=%s", code)
	}
	var phase int
	if err := f.base.app.store.db.QueryRow(`SELECT dispatch_phase FROM account_pool_maintenance_leases WHERE lease_id=?`, lease.inner.ID()).Scan(&phase); err != nil || phase != int(scheduling.DispatchNotStarted) {
		t.Fatalf("phase after rollback=%d err=%v", phase, err)
	}
	if code := lease.MarkDispatch(context.Background(), func(context.Context, *sql.Tx) error { return nil }); code != accountPoolAcquired {
		t.Fatalf("mark dispatch code=%s", code)
	}
	if code := lease.MarkDispatch(context.Background(), func(context.Context, *sql.Tx) error { return nil }); code != accountPoolConfigurationChanged {
		t.Fatalf("duplicate mark dispatch code=%s", code)
	}
	callbackCurrent := false
	if code := lease.Finalize(context.Background(), func(ctx context.Context, tx *sql.Tx, current bool) error {
		callbackCurrent = current
		if !current {
			return errors.New("unexpected stale lease")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_pool_runtime_cooldowns WHERE account_id=? AND event_id=?`, state.AccountID, state.CooldownEventID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM account_recovery_states WHERE account_id=? AND operation_id=?`, state.AccountID, state.OperationID)
		return err
	}); code != accountPoolReleased || !callbackCurrent {
		t.Fatalf("finalize code=%s current=%v", code, callbackCurrent)
	}
	var count int
	if err := f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_maintenance_leases WHERE operation_id=?`, state.OperationID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("maintenance lease remains count=%d err=%v", count, err)
	}
}

func TestMaintenanceSharesCapacityAndRejectsStaleSnapshot(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_shared_maintenance", true)
	f.insertModelPool(t, "shared-maintenance-model", "ups_shared_maintenance", 1, modelAccountView{UpstreamID: "ups_shared_maintenance", UpstreamModel: "provider-shared", Weight: 1, MaxConcurrency: 1})
	employee := acquireRuntime(t, f.rt, "shared-maintenance-model", f.auth1, "")
	state := installRecoveryState(t, f, "ups_shared_maintenance", "shared-maintenance-model", "cool_shared_maintenance", "probe_shared_maintenance", 1)

	resultCh := make(chan accountMaintenanceAcquireResult, 1)
	go func() {
		resultCh <- f.rt.AcquireMaintenance(context.Background(), maintenanceRequest(state), beginMaintenanceState)
	}()
	select {
	case result := <-resultCh:
		t.Fatalf("maintenance bypassed employee capacity: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}
	if ok, result := employee.Lease.Release(context.Background(), scheduling.ReleaseResult{Phase: scheduling.DispatchUnknown}); !ok || result.Code != accountPoolReleased {
		t.Fatalf("employee release ok=%v result=%+v", ok, result)
	}
	var acquired accountMaintenanceAcquireResult
	select {
	case acquired = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("maintenance waiter did not acquire released capacity")
	}
	if acquired.Code != accountPoolAcquired || acquired.Lease == nil {
		t.Fatalf("maintenance waiter=%+v", acquired)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, state.AccountID); err != nil {
		t.Fatal(err)
	}
	if code := acquired.Lease.MarkDispatch(context.Background(), func(context.Context, *sql.Tx) error { return nil }); code != accountPoolConfigurationChanged {
		t.Fatalf("stale account revision dispatch code=%s", code)
	}
	current := true
	if code := acquired.Lease.Finalize(context.Background(), func(_ context.Context, _ *sql.Tx, boolCurrent bool) error { current = boolCurrent; return nil }); code != accountPoolReleased || current {
		t.Fatalf("stale finalize code=%s current=%v", code, current)
	}
}

func TestMaintenanceMayHaveSentClearAndReimportRestartConservatively(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_restart_maintenance", true)
	f.insertModelPool(t, "restart-maintenance-model", "ups_restart_maintenance", 1, modelAccountView{UpstreamID: "ups_restart_maintenance", UpstreamModel: "provider-restart", Weight: 1, MaxConcurrency: 1})
	state := installRecoveryState(t, f, "ups_restart_maintenance", "restart-maintenance-model", "cool_restart_maintenance", "probe_restart_maintenance", 1)
	acquired := f.rt.AcquireMaintenance(context.Background(), maintenanceRequest(state), beginMaintenanceState)
	if acquired.Code != accountPoolAcquired || acquired.Lease == nil {
		t.Fatalf("maintenance acquire=%+v", acquired)
	}
	if code := acquired.Lease.MarkDispatch(context.Background(), func(context.Context, *sql.Tx) error { return nil }); code != accountPoolAcquired {
		t.Fatalf("mark dispatch=%s", code)
	}
	if result, _, code := f.rt.clearCooldown(context.Background(), state.AccountID, state.AccountRevision, state.CooldownEventID); result != cooldownCleared || code != cooldownCleared {
		t.Fatalf("clear result=%s code=%s", result, code)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, state.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, Random: &runtimeSequenceRandom{}, LeaseTTL: 30 * time.Second, StickyTTL: time.Minute, MaxSticky: 10, MaxWaiters: 2})
	if err != nil {
		t.Fatalf("restart after clear/reimport: %v", err)
	}
	defer restarted.Close()
	snapshots := restarted.scheduler.Snapshot()
	if len(snapshots) != 1 || snapshots[0].LeaseID != acquired.Lease.inner.ID() || snapshots[0].AccountID != state.AccountID {
		t.Fatalf("restored conservative capacity=%+v", snapshots)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	result := restarted.Acquire(ctx, state.PublicModel, f.auth1, []string{"openai-compatible"}, "")
	if result.Code != accountPoolCancelled || result.Lease != nil {
		t.Fatalf("restored maintenance capacity admission=%+v", result)
	}
}

func TestMaintenanceFinalizeMatchSeparatesCancellationHeartbeatAndConfiguration(t *testing.T) {
	newLease := func(t *testing.T, suffix string) (*runtimeFixture, accountRecoveryState, *accountMaintenanceLease, context.CancelFunc) {
		t.Helper()
		f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
		account := "ups_finalize_" + suffix
		model := "finalize-model-" + suffix
		f.insertAccount(t, account, true)
		f.insertModelPool(t, model, account, 1, modelAccountView{UpstreamID: account, UpstreamModel: "provider-" + suffix, Weight: 1, MaxConcurrency: 1})
		state := installRecoveryState(t, f, account, model, "cool_finalize_"+suffix, "probe_finalize_"+suffix, 1)
		ctx, cancel := context.WithCancel(context.Background())
		acquired := f.rt.AcquireMaintenance(ctx, maintenanceRequest(state), beginMaintenanceState)
		if acquired.Code != accountPoolAcquired || acquired.Lease == nil {
			cancel()
			t.Fatalf("maintenance acquire=%+v", acquired)
		}
		return f, state, acquired.Lease, cancel
	}

	t.Run("cancelled context remains current", func(t *testing.T) {
		_, _, lease, cancel := newLease(t, "cancel")
		cancel()
		current := false
		if code := lease.Finalize(context.Background(), func(_ context.Context, _ *sql.Tx, match bool) error { current = match; return nil }); code != accountPoolReleased || !current {
			t.Fatalf("cancelled finalize code=%s current=%v", code, current)
		}
	})

	t.Run("heartbeat persistence failure is not current", func(t *testing.T) {
		f, _, lease, cancel := newLease(t, "heartbeat")
		defer cancel()
		if _, err := f.base.app.store.db.Exec(`CREATE TRIGGER fail_maintenance_heartbeat BEFORE UPDATE OF expires_at ON account_pool_maintenance_leases BEGIN SELECT RAISE(FAIL,'synthetic heartbeat failure'); END`); err != nil {
			t.Fatal(err)
		}
		if code := lease.Heartbeat(context.Background()); code != accountPoolStorageUnavailable {
			t.Fatalf("heartbeat code=%s", code)
		}
		if _, err := f.base.app.store.db.Exec(`DROP TRIGGER fail_maintenance_heartbeat`); err != nil {
			t.Fatal(err)
		}
		current := true
		if code := lease.Finalize(context.Background(), func(_ context.Context, _ *sql.Tx, match bool) error { current = match; return nil }); code != accountPoolReleased || current {
			t.Fatalf("heartbeat-failed finalize code=%s current=%v", code, current)
		}
	})

	t.Run("revision change is not current", func(t *testing.T) {
		f, state, lease, cancel := newLease(t, "revision")
		defer cancel()
		if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, state.AccountID); err != nil {
			t.Fatal(err)
		}
		current := true
		if code := lease.Finalize(context.Background(), func(_ context.Context, _ *sql.Tx, match bool) error { current = match; return nil }); code != accountPoolReleased || current {
			t.Fatalf("revision-stale finalize code=%s current=%v", code, current)
		}
	})
}
