package service

// Independent failure-mode tests use synthetic SQLite state only. No provider
// request or real credential is needed to reproduce a lost commit response.
import (
	"context"
	"database/sql"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

func TestRecoveryReceiptResolvesOnlyCompleteAtomicSettlement(t *testing.T) {
	for _, variant := range []string{"success", "failed", "pending", "wrong_time", "missing_state"} {
		t.Run(variant, func(t *testing.T) {
			f := newRuntimeFixture(t, nil, time.Minute, 2)
			a := f.base.app
			f.insertAccount(t, "settlement-account", true)
			f.insertModelPool(t, "settlement-model", "settlement-account", 1, modelAccountView{UpstreamID: "settlement-account", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 1})
			state := installRecoveryState(t, f, "settlement-account", "settlement-model", "cool_lost_response", "abf905eb-e1da-4a45-97fe-7b310e024233", 1)
			ctx := context.Background()
			acquired := f.rt.AcquireMaintenance(ctx, maintenanceRequest(state), func(ctx context.Context, tx *sql.Tx, snapshot accountRecoveryState) error {
				_, err := a.systemProbes.BeginTx(ctx, tx, accounting.SystemProbeStart{OperationID: snapshot.OperationID, RecoveryEventID: snapshot.CooldownEventID, AccountID: snapshot.AccountID, AccountRevision: 1, PoolRevision: 1, PublicModel: snapshot.PublicModel, UpstreamModel: snapshot.UpstreamModel, Provider: accounting.ProviderOpenAICompatible, Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: f.clock.Now()})
				if err != nil {
					return err
				}
				return beginMaintenanceState(ctx, tx, snapshot)
			})
			if acquired.Code != accountPoolAcquired {
				t.Fatalf("acquire=%s", acquired.Code)
			}
			lease := acquired.Lease
			if code := lease.MarkDispatch(ctx, func(ctx context.Context, tx *sql.Tx) error {
				_, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, state.OperationID, f.clock.Now())
				return err
			}); code != accountPoolAcquired {
				t.Fatalf("dispatch=%s", code)
			}
			result := generationProbeResult{Status: accounting.StatusSucceeded, Code: "generation_ok"}
			if variant == "failed" {
				result.Status, result.Code = accounting.StatusFailed, "upstream_timeout"
			}
			receipt := &recoveryExecutionReceipt{app: a, lease: lease, result: result, finished: f.clock.Now()}
			// Perform the durable half of finalization, deliberately omitting its
			// memory acknowledgement as if Commit returned an uncertain error.
			tx, err := a.store.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			if variant != "pending" {
				finished := receipt.finished
				if variant == "wrong_time" {
					finished = finished.Add(time.Second)
				}
				_, err = a.systemProbes.FinishTx(ctx, tx, accounting.SystemProbeFinish{OperationID: state.OperationID, Status: accounting.SystemProbeStatus(result.Status), Result: accounting.SystemProbeResult(result.Code), FinishedAt: finished})
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(`DELETE FROM account_pool_maintenance_leases WHERE operation_id=?`, state.OperationID); err != nil {
				t.Fatal(err)
			}
			if variant == "failed" {
				_, err = tx.Exec(`UPDATE account_recovery_states SET state='interrupted',next_probe_at=? WHERE operation_id=?`, formatAccountPoolTime(receipt.finished.Add(recoveryRetryDelay)), state.OperationID)
			} else {
				_, err = tx.Exec(`DELETE FROM account_recovery_states WHERE operation_id=?`, state.OperationID)
				if variant != "missing_state" && err == nil {
					_, err = tx.Exec(`DELETE FROM account_pool_runtime_cooldowns WHERE account_id=?`, state.AccountID)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			want := accountPoolStorageUnavailable
			if variant == "success" || variant == "failed" {
				want = accountPoolReleased
			}
			if code := receipt.Finalize(ctx); code != want {
				t.Fatalf("resolution=%s want=%s", code, want)
			}
			if lease.finished != (want == accountPoolReleased) {
				t.Fatal("capacity was released without settlement proof")
			}
			if want == accountPoolReleased && receipt.Finalize(ctx) != accountPoolAlreadyReleased {
				t.Fatal("receipt was not idempotent")
			}
		})
	}
}

func TestMaintenanceCapacityWaitDoesNotCancelAdmittedLease(t *testing.T) {
	f := newRuntimeFixture(t, nil, time.Minute, 2)
	f.insertAccount(t, "queue-account", true)
	f.insertModelPool(t, "queue-model", "queue-account", 1, modelAccountView{UpstreamID: "queue-account", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 1})
	employee := acquireRuntime(t, f.rt, "queue-model", f.auth1, "")
	state := installRecoveryState(t, f, "queue-account", "queue-model", "cool_queue", "queue-op", 1)
	req := maintenanceRequest(state)
	req.CapacityWait = 20 * time.Millisecond
	start := time.Now()
	if result := f.rt.AcquireMaintenance(context.Background(), req, beginMaintenanceState); result.Code != accountPoolCapacityUnavailable {
		t.Fatalf("queue=%s", result.Code)
	}
	if time.Since(start) > time.Second {
		t.Fatal("capacity wait was not bounded")
	}
	if _, result := employee.Lease.Release(context.Background(), scheduling.ReleaseResult{Phase: scheduling.DispatchUnknown}); result.Code != accountPoolReleased {
		t.Fatal(result.Code)
	}
	result := f.rt.AcquireMaintenance(context.Background(), req, beginMaintenanceState)
	if result.Code != accountPoolAcquired {
		t.Fatal(result.Code)
	}
	select {
	case <-result.Lease.Context().Done():
		t.Fatal("queue timeout leaked into execution")
	case <-time.After(40 * time.Millisecond):
	}
	if code := result.Lease.Finalize(context.Background(), func(context.Context, *sql.Tx, bool) error { return nil }); code != accountPoolReleased {
		t.Fatal(code)
	}
}

func TestRecoverySettlementSurvivesClockRollback(t *testing.T) {
	f := newRuntimeFixture(t, nil, time.Minute, 2)
	a := f.base.app
	f.insertAccount(t, "clock-account", true)
	f.insertModelPool(t, "clock-model", "clock-account", 1, modelAccountView{UpstreamID: "clock-account", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 1})
	state := installRecoveryState(t, f, "clock-account", "clock-model", "cool_clock", "bcf905eb-e1da-4a45-97fe-7b310e024233", 1)
	ctx := context.Background()
	started := f.clock.Now()
	acquired := f.rt.AcquireMaintenance(ctx, maintenanceRequest(state), func(ctx context.Context, tx *sql.Tx, snapshot accountRecoveryState) error {
		_, err := a.systemProbes.BeginTx(ctx, tx, accounting.SystemProbeStart{OperationID: snapshot.OperationID, RecoveryEventID: snapshot.CooldownEventID, AccountID: snapshot.AccountID, AccountRevision: 1, PoolRevision: 1, PublicModel: snapshot.PublicModel, UpstreamModel: snapshot.UpstreamModel, Provider: accounting.ProviderOpenAICompatible, Protocol: accounting.ProtocolOpenAIChatCompletions, StartedAt: started})
		if err != nil {
			return err
		}
		return beginMaintenanceState(ctx, tx, snapshot)
	})
	if acquired.Code != accountPoolAcquired {
		t.Fatal(acquired.Code)
	}
	dispatched := started.Add(time.Second)
	if code := acquired.Lease.MarkDispatch(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := a.systemProbes.MarkMayHaveSentTx(ctx, tx, state.OperationID, dispatched)
		return err
	}); code != accountPoolAcquired {
		t.Fatal(code)
	}
	receipt := &recoveryExecutionReceipt{app: a, lease: acquired.Lease, result: generationProbeResult{Status: accounting.StatusFailed, Code: "upstream_timeout"}, finished: started.Add(-time.Minute)}
	if code := receipt.Finalize(ctx); code != accountPoolReleased {
		t.Fatalf("clock rollback settlement=%s", code)
	}
	tx, err := a.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	attempt, err := a.systemProbes.GetTx(ctx, tx, state.OperationID)
	if err != nil || attempt.FinishedAt == nil || !attempt.FinishedAt.Equal(dispatched) {
		t.Fatalf("invalid clamped completion=%v err=%v", attempt.FinishedAt, err)
	}
	if err := receipt.proveCommitted(ctx, tx); err != nil {
		t.Fatalf("clamped receipt proof=%v", err)
	}
	current, err := loadAccountRecoveryStateTx(ctx, tx, state.AccountID)
	if err != nil || current.NextProbeAt.Before(dispatched.Add(recoveryRetryDelay)) {
		t.Fatalf("retry delay shortened after clock rollback: %v", err)
	}
}
