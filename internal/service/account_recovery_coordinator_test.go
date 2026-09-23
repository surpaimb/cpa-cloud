package service

import (
	"context"
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestAccountRecoverySettingsDualGateAndCAS(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	c := f.base.app.recovery
	if c == nil {
		t.Fatal("App did not initialize recovery coordinator")
	}
	ctx := context.Background()
	initial, err := c.Snapshot(ctx)
	if err != nil || initial.Enabled || initial.SettingRevision != 1 || initial.Running {
		t.Fatalf("initial snapshot=%+v err=%v", initial, err)
	}
	if _, err := c.SetEnabled(ctx, initial.SettingRevision, true); !errors.Is(err, errAccountRecoveryNotAllowed) {
		t.Fatalf("CLI-disabled enable err=%v", err)
	}
	f.base.app.cfg.AccountRecoveryEnabled = true
	enabled, err := c.SetEnabled(ctx, initial.SettingRevision, true)
	if err != nil || !enabled.Enabled || enabled.Revision != initial.SettingRevision+1 {
		t.Fatalf("enable=%+v err=%v", enabled, err)
	}
	if _, err := c.SetEnabled(ctx, initial.SettingRevision, false); !errors.Is(err, errAccountRecoverySettingConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
	disabled, err := c.SetEnabled(ctx, enabled.Revision, false)
	if err != nil || disabled.Enabled || disabled.Revision != enabled.Revision+1 {
		t.Fatalf("disable=%+v err=%v", disabled, err)
	}
	final, err := c.Snapshot(ctx)
	if err != nil || final.Running || final.Enabled {
		t.Fatalf("final snapshot=%+v err=%v", final, err)
	}
}

func TestAccountRecoverySettingWriteUncertaintyReconcilesWorker(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	c := f.base.app.recovery
	f.base.app.cfg.AccountRecoveryEnabled = true
	ctx := context.Background()
	enabled, err := c.SetEnabled(ctx, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	durableWrite := c.casSetting
	writeResponseLost := errors.New("synthetic setting response lost")
	c.writeSetting = func(ctx context.Context, revision int64, value bool) (accountRecoverySettings, error) {
		if _, err := durableWrite(ctx, revision, value); err != nil {
			return accountRecoverySettings{}, err
		}
		return accountRecoverySettings{}, writeResponseLost
	}
	disabled, err := c.SetEnabled(ctx, enabled.Revision, false)
	if err != nil || disabled.Enabled {
		t.Fatalf("resolved committed disable=%+v err=%v", disabled, err)
	}
	snapshot, err := c.Snapshot(ctx)
	if err != nil || snapshot.Running || snapshot.Enabled {
		t.Fatalf("committed disable snapshot=%+v err=%v", snapshot, err)
	}
	c.writeSetting = durableWrite
	enabled, err = c.SetEnabled(ctx, disabled.Revision, true)
	if err != nil {
		t.Fatal(err)
	}
	c.writeSetting = func(context.Context, int64, bool) (accountRecoverySettings, error) {
		return accountRecoverySettings{}, errors.New("synthetic rollback")
	}
	if _, err := c.SetEnabled(ctx, enabled.Revision, false); err == nil {
		t.Fatal("rolled-back disable reported success")
	}
	snapshot, err = c.Snapshot(ctx)
	if err != nil || !snapshot.Running || !snapshot.Enabled {
		t.Fatalf("rollback did not restore worker snapshot=%+v err=%v", snapshot, err)
	}
	c.writeSetting = durableWrite
	if _, err := c.SetEnabled(ctx, enabled.Revision, false); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRecoveryWorkerDoesNotLoseNotifyBetweenScanAndWait(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.base.app.cfg.AccountRecoveryEnabled = true
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_settings SET enabled=1,revision=revision+1`); err != nil {
		t.Fatal(err)
	}
	c := newAccountRecoveryCoordinator(f.base.app)
	seen := make(chan struct{}, 2)
	var scans atomic.Int32
	c.afterScan = func() {
		number := scans.Add(1)
		if number <= 2 {
			seen <- struct{}{}
		}
		if number == 1 {
			c.Notify()
		}
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	for index := 0; index < 2; index++ {
		select {
		case <-seen:
		case <-time.After(time.Second):
			t.Fatal("worker lost a notification published after its scan")
		}
	}
}

func TestAccountRecoveryInterruptedRotationAndThreeAttemptLimit(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.insertAccount(t, "ups_recovery_worker", true)
	f.insertModelPool(t, "recovery-worker-model", "ups_recovery_worker", 1, modelAccountView{UpstreamID: "ups_recovery_worker", UpstreamModel: "provider-worker", Weight: 1, MaxConcurrency: 1})
	operationIDs := []string{
		"70000000-0000-4000-8000-000000000001",
		"70000000-0000-4000-8000-000000000002",
		"70000000-0000-4000-8000-000000000003",
	}
	state := installRecoveryState(t, f, "ups_recovery_worker", "recovery-worker-model", "cool_worker_event", operationIDs[0], 1)
	c := newAccountRecoveryCoordinator(f.base.app)
	nextOperation := 1
	c.newOperationID = func() (string, error) {
		value := operationIDs[nextOperation]
		nextOperation++
		return value, nil
	}
	for attemptIndex := 0; attemptIndex < 3; attemptIndex++ {
		operationID := operationIDs[attemptIndex]
		withRecoveryTx(t, f.base.app.store.db, func(tx *sql.Tx) {
			started := f.clock.Now().Add(time.Duration(attemptIndex) * time.Second)
			if _, err := f.base.app.systemProbes.BeginTx(context.Background(), tx, accounting.SystemProbeStart{
				OperationID: operationID, RecoveryEventID: state.CooldownEventID, AccountID: state.AccountID,
				AccountRevision: state.AccountRevision, PoolRevision: state.PoolRevision, PublicModel: state.PublicModel,
				UpstreamModel: state.UpstreamModel, Provider: accounting.ProviderOpenAICompatible,
				Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: started,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.base.app.systemProbes.FinishTx(context.Background(), tx, accounting.SystemProbeFinish{
				OperationID: operationID, Status: accounting.SystemProbeCancelled, Result: accounting.SystemProbeCancelledResult, FinishedAt: started,
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(`UPDATE account_recovery_states SET state='interrupted',next_probe_at=?,updated_at=? WHERE account_id=?`, formatAccountPoolTime(started), formatAccountPoolTime(started), state.AccountID); err != nil {
				t.Fatal(err)
			}
		})
		stored := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
		if attemptIndex < 2 {
			c.rotateInterrupted(context.Background(), stored)
			rotated := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
			if rotated.OperationID != operationIDs[attemptIndex+1] || rotated.RecoveryRevision != int64(attemptIndex+2) || rotated.State != recoveryRequired || !rotated.CreatedAt.Equal(state.CreatedAt) {
				t.Fatalf("rotation %d=%+v", attemptIndex, rotated)
			}
		}
	}
	stored := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
	c.rotateInterrupted(context.Background(), stored)
	blocked := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
	if blocked.AttentionCode != recoveryAttentionRetryLimit || blocked.OperationID != operationIDs[2] || blocked.RecoveryRevision != 3 {
		t.Fatalf("three-attempt state=%+v", blocked)
	}
	counts, err := f.base.app.systemProbes.Counts(context.Background(), state.CooldownEventID)
	if err != nil || counts.Event != 3 {
		t.Fatalf("counts=%+v err=%v", counts, err)
	}
}

func TestAccountRecoverySettlementAttentionCannotModifyNewEvent(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.insertAccount(t, "ups_recovery_settlement_cas", true)
	f.insertModelPool(t, "recovery-settlement-model", "ups_recovery_settlement_cas", 1, modelAccountView{UpstreamID: "ups_recovery_settlement_cas", UpstreamModel: "provider-settlement", Weight: 1, MaxConcurrency: 1})
	old := installRecoveryState(t, f, "ups_recovery_settlement_cas", "recovery-settlement-model", "cool_settlement_old", "71000000-0000-4000-8000-000000000001", 1)
	if _, err := f.base.app.store.db.Exec(`UPDATE account_pool_runtime_cooldowns SET event_id='cool_settlement_new' WHERE account_id=?`, old.AccountID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET cooldown_event_id='cool_settlement_new',operation_id='71000000-0000-4000-8000-000000000002',recovery_revision=2 WHERE account_id=?`, old.AccountID); err != nil {
		t.Fatal(err)
	}
	c := newAccountRecoveryCoordinator(f.base.app)
	c.blockCurrent(old, recoveryAttentionSettlementPending)
	current := loadRecoveryStateForTest(t, f.base.app.store.db, old.AccountID)
	if current.CooldownEventID != "cool_settlement_new" || current.AttentionCode != "" {
		t.Fatalf("stale settlement modified new event: %+v", current)
	}
}

func TestAccountRecoveryUnsettledAndPermanentAttemptsRequireAttention(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.insertAccount(t, "ups_recovery_attention", true)
	f.insertModelPool(t, "recovery-attention-model", "ups_recovery_attention", 1, modelAccountView{UpstreamID: "ups_recovery_attention", UpstreamModel: "provider-attention", Weight: 1, MaxConcurrency: 1})
	operation := "72000000-0000-4000-8000-000000000001"
	state := installRecoveryState(t, f, "ups_recovery_attention", "recovery-attention-model", "cool_attention_event", operation, 1)
	withRecoveryTx(t, f.base.app.store.db, func(tx *sql.Tx) {
		if _, err := f.base.app.systemProbes.BeginTx(context.Background(), tx, recoveryProbeStart(state, operation, f.clock.Now())); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`UPDATE account_recovery_states SET state='in_progress' WHERE account_id=?`, state.AccountID); err != nil {
			t.Fatal(err)
		}
	})
	c := newAccountRecoveryCoordinator(f.base.app)
	c.reconcileTerminal(context.Background(), loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID))
	pending := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
	if pending.AttentionCode != recoveryAttentionSettlementPending {
		t.Fatalf("pending attention=%+v", pending)
	}
	if _, err := f.base.app.store.db.Exec(`DELETE FROM system_probe_attempts WHERE operation_id=?`, operation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET state='interrupted',attention_code=NULL WHERE account_id=?`, state.AccountID); err != nil {
		t.Fatal(err)
	}
	c.rotateInterrupted(context.Background(), loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID))
	missing := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
	if missing.AttentionCode != recoveryAttentionSettlementPending {
		t.Fatalf("missing-ledger attention=%+v", missing)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET attention_code=NULL WHERE account_id=?`, state.AccountID); err != nil {
		t.Fatal(err)
	}
	withRecoveryTx(t, f.base.app.store.db, func(tx *sql.Tx) {
		started := f.clock.Now()
		if _, err := f.base.app.systemProbes.BeginTx(context.Background(), tx, recoveryProbeStart(state, operation, started)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.base.app.systemProbes.FinishTx(context.Background(), tx, accounting.SystemProbeFinish{OperationID: operation, Status: accounting.SystemProbeFailed, Result: accounting.SystemProbeAuthenticationFailed, FinishedAt: started}); err != nil {
			t.Fatal(err)
		}
	})
	c.rotateInterrupted(context.Background(), loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID))
	permanent := loadRecoveryStateForTest(t, f.base.app.store.db, state.AccountID)
	if permanent.AttentionCode != recoveryAttentionAuthentication {
		t.Fatalf("permanent attention=%+v", permanent)
	}
}

func TestAccountRecoveryCapacityDeferralScansOtherDueAccount(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.base.app.cfg.AccountRecoveryEnabled = true
	operations := map[string]string{"a": "73000000-0000-4000-8000-000000000001", "b": "73000000-0000-4000-8000-000000000002"}
	for _, id := range []string{"a", "b"} {
		account := "ups_recovery_fair_" + id
		model := "recovery-fair-model-" + id
		f.insertAccount(t, account, true)
		f.insertModelPool(t, model, account, 1, modelAccountView{UpstreamID: account, UpstreamModel: "provider-" + id, Weight: 1, MaxConcurrency: 1})
		installRecoveryState(t, f, account, model, "cool_fair_"+id, operations[id], 1)
	}
	due := time.Now().UTC().Add(-time.Second)
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET next_probe_at=?`, formatAccountPoolTime(due)); err != nil {
		t.Fatal(err)
	}
	c := f.base.app.recovery
	called := make(chan string, 2)
	c.execute = func(_ context.Context, request accountMaintenanceAcquireRequest) (accountPoolRuntimeCode, *recoveryExecutionReceipt) {
		called <- request.AccountID
		return accountPoolCapacityUnavailable, nil
	}
	enabled, err := c.SetEnabled(context.Background(), 1, true)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		snapshot, snapshotErr := c.Snapshot(context.Background())
		states, statesErr := c.AccountSnapshots(context.Background())
		t.Fatalf("first due account was not checked snapshot=%+v snapshotErr=%v states=%+v statesErr=%v", snapshot, snapshotErr, states, statesErr)
	}
	select {
	case second := <-called:
		if second != "ups_recovery_fair_b" {
			t.Fatalf("capacity deferral did not advance fair cursor: %s", second)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second due account was starved")
	}
	if _, err := c.SetEnabled(context.Background(), enabled.Revision, false); err != nil {
		t.Fatal(err)
	}
}

func recoveryProbeStart(state accountRecoveryState, operation string, started time.Time) accounting.SystemProbeStart {
	return accounting.SystemProbeStart{OperationID: operation, RecoveryEventID: state.CooldownEventID, AccountID: state.AccountID,
		AccountRevision: state.AccountRevision, PoolRevision: state.PoolRevision, PublicModel: state.PublicModel, UpstreamModel: state.UpstreamModel,
		Provider: accounting.ProviderOpenAICompatible, Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: started}
}

func withRecoveryTx(t *testing.T, db *sql.DB, callback func(*sql.Tx)) {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	callback(tx)
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func loadRecoveryStateForTest(t *testing.T, db *sql.DB, accountID string) accountRecoveryState {
	t.Helper()
	state, err := scanAccountRecoveryState(db.QueryRow(accountRecoverySelect+` WHERE account_id=?`, accountID))
	if err != nil {
		t.Fatal(err)
	}
	return state
}
