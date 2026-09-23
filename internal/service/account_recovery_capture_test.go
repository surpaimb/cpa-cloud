package service

// Synthetic metadata tests for employee failure capture. No provider is called.
import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

func enableRecoveryCapture(t *testing.T, f *runtimeFixture) {
	t.Helper()
	f.base.app.cfg.AccountRecoveryEnabled = true
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_settings SET enabled=1,revision=revision+1,updated_at=? WHERE singleton=1`, formatAccountPoolTime(f.clock.Now())); err != nil {
		t.Fatal(err)
	}
}

func acquireBoundRecoveryLease(t *testing.T, f *runtimeFixture, model string, protocol accounting.UsageProtocol) accountPoolAcquireResult {
	t.Helper()
	result := acquireRuntime(t, f.rt, model, f.auth1, "")
	if code := result.Lease.BindRecoverySnapshot(context.Background(), model, protocol, result.Route); code != accountPoolAcquired {
		t.Fatalf("bind recovery snapshot=%s", code)
	}
	result.Lease.MarkDispatch()
	return result
}

func loadCapturedState(t *testing.T, f *runtimeFixture, account string) accountRecoveryState {
	t.Helper()
	tx, err := f.base.app.store.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := loadAccountRecoveryStateTx(context.Background(), tx, account)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestAccountRecoveryCaptureKeepsWinningCauseAndAdvancesDisabledIsolation(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	enableRecoveryCapture(t, f)
	f.insertAccount(t, "ups_capture", true)
	for _, item := range []struct{ model, actual string }{{"capture-short", "actual-short"}, {"capture-long", "actual-long"}, {"capture-later-short", "actual-later-short"}} {
		f.insertModelPool(t, item.model, "ups_capture", 1, modelAccountView{UpstreamID: "ups_capture", UpstreamModel: item.actual, Weight: 1, MaxConcurrency: 4})
	}
	short := acquireBoundRecoveryLease(t, f, "capture-short", accounting.ProtocolOpenAIChatCompletions)
	long := acquireBoundRecoveryLease(t, f, "capture-long", accounting.ProtocolOpenAIResponses)
	laterShort := acquireBoundRecoveryLease(t, f, "capture-later-short", accounting.ProtocolOpenAIChatCompletions)

	if ok, got := short.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient, Phase: scheduling.MayHaveSent}); !ok || got.Code != accountPoolReleased {
		t.Fatalf("short release=%v %+v", ok, got)
	}
	first := loadCapturedState(t, f, "ups_capture")
	if first.PublicModel != "capture-short" || first.Protocol != string(accounting.ProtocolOpenAIChatCompletions) || first.RecoveryRevision != 1 || first.AttentionCode != "" || !first.CheckedAt.IsZero() {
		t.Fatalf("first captured state=%+v", first)
	}

	if ok, got := long.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit, Phase: scheduling.MayHaveSent}); !ok || got.Code != accountPoolReleased {
		t.Fatalf("long release=%v %+v", ok, got)
	}
	second := loadCapturedState(t, f, "ups_capture")
	if second.PublicModel != "capture-long" || second.UpstreamModel != "actual-long" || second.Protocol != string(accounting.ProtocolOpenAIResponses) || second.RecoveryRevision != 2 || second.CooldownEventID == first.CooldownEventID || !second.CreatedAt.Equal(first.CreatedAt) {
		t.Fatalf("longer failure did not replace complete path: first=%+v second=%+v", first, second)
	}
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_states SET attention_code='storage_unavailable',checked_at=? WHERE account_id='ups_capture'`, formatAccountPoolTime(f.clock.Now())); err != nil {
		t.Fatal(err)
	}

	// Process and persisted switches no longer permit new isolation, but an
	// existing isolation must still follow every later cooldown event.
	f.base.app.cfg.AccountRecoveryEnabled = false
	if _, err := f.base.app.store.db.Exec(`UPDATE account_recovery_settings SET enabled=0,revision=revision+1,updated_at=? WHERE singleton=1`, formatAccountPoolTime(f.clock.Now())); err != nil {
		t.Fatal(err)
	}
	if ok, got := laterShort.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient, Phase: scheduling.MayHaveSent}); !ok || got.Code != accountPoolReleased {
		t.Fatalf("disabled release=%v %+v", ok, got)
	}
	third := loadCapturedState(t, f, "ups_capture")
	if third.RecoveryRevision != 3 || third.CooldownEventID == second.CooldownEventID || third.PublicModel != second.PublicModel || third.UpstreamModel != second.UpstreamModel || third.Protocol != second.Protocol || !third.NextProbeAt.Equal(second.NextProbeAt) || third.AttentionCode != "" || !third.CheckedAt.IsZero() {
		t.Fatalf("shorter disabled failure changed winning path: second=%+v third=%+v", second, third)
	}
	var cooldownEvent, failure, until string
	if err := f.base.app.store.db.QueryRow(`SELECT event_id,failure_class,cooldown_until FROM account_pool_runtime_cooldowns WHERE account_id='ups_capture'`).Scan(&cooldownEvent, &failure, &until); err != nil {
		t.Fatal(err)
	}
	if cooldownEvent != third.CooldownEventID || failure != string(scheduling.FailureRateLimit) || until != formatAccountPoolTime(third.NextProbeAt) {
		t.Fatalf("cooldown/state mismatch event=%q failure=%q until=%q state=%+v", cooldownEvent, failure, until, third)
	}

	provider, err := usageProvider(third.ProviderKind, accounting.UsageProtocol(third.Protocol))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.base.app.store.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := f.base.app.systemProbes.BeginTx(context.Background(), tx, accounting.SystemProbeStart{
		OperationID: third.OperationID, RecoveryEventID: third.CooldownEventID, AccountID: third.AccountID,
		AccountRevision: third.AccountRevision, PoolRevision: third.PoolRevision, PublicModel: third.PublicModel,
		UpstreamModel: third.UpstreamModel, Provider: provider, Protocol: accounting.UsageProtocol(third.Protocol), StartedAt: f.clock.Now(),
	}); err != nil {
		t.Fatalf("captured operation rejected by real probe ledger: %v", err)
	}
}

func TestAccountRecoveryCaptureRejectsStaleCredentialAndRollsBackAtomically(t *testing.T) {
	t.Run("reimport", func(t *testing.T) {
		f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
		enableRecoveryCapture(t, f)
		f.insertAccount(t, "ups_reimport", true)
		f.insertModelPool(t, "reimport-model", "ups_reimport", 1, modelAccountView{UpstreamID: "ups_reimport", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 2})
		lease := acquireBoundRecoveryLease(t, f, "reimport-model", accounting.ProtocolOpenAIChatCompletions)
		if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id='ups_reimport'`); err != nil {
			t.Fatal(err)
		}
		if ok, got := lease.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit, Phase: scheduling.MayHaveSent}); !ok || got.Code != accountPoolReleased || got.RetrySuggested {
			t.Fatalf("stale release=%v %+v", ok, got)
		}
		for _, table := range []string{"account_pool_runtime_cooldowns", "account_recovery_states"} {
			var count int
			if err := f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE account_id='ups_reimport'`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("stale credential wrote %s count=%d err=%v", table, count, err)
			}
		}
	})

	t.Run("state write failure", func(t *testing.T) {
		f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
		enableRecoveryCapture(t, f)
		f.insertAccount(t, "ups_rollback_capture", true)
		for _, model := range []string{"rollback-first", "rollback-second"} {
			f.insertModelPool(t, model, "ups_rollback_capture", 1, modelAccountView{UpstreamID: "ups_rollback_capture", UpstreamModel: model, Weight: 1, MaxConcurrency: 2})
		}
		first := acquireBoundRecoveryLease(t, f, "rollback-first", accounting.ProtocolOpenAIChatCompletions)
		second := acquireBoundRecoveryLease(t, f, "rollback-second", accounting.ProtocolOpenAIResponses)
		if ok, got := first.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient, Phase: scheduling.MayHaveSent}); !ok || got.Code != accountPoolReleased {
			t.Fatalf("initial release=%v %+v", ok, got)
		}
		before := loadCapturedState(t, f, "ups_rollback_capture")
		if _, err := f.base.app.store.db.Exec(`CREATE TRIGGER reject_capture_state BEFORE UPDATE ON account_recovery_states BEGIN SELECT RAISE(ABORT,'synthetic capture failure'); END`); err != nil {
			t.Fatal(err)
		}
		defer f.base.app.store.db.Exec(`DROP TRIGGER reject_capture_state`)
		if ok, got := second.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit, Phase: scheduling.MayHaveSent}); ok || got.Code != accountPoolStorageUnavailable {
			t.Fatalf("failed atomic release=%v %+v", ok, got)
		}
		after := loadCapturedState(t, f, "ups_rollback_capture")
		var cooldownEvent string
		var leases int
		if err := f.base.app.store.db.QueryRow(`SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id='ups_rollback_capture'`).Scan(&cooldownEvent); err != nil {
			t.Fatal(err)
		}
		if err := f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_leases WHERE lease_id=?`, second.Lease.inner.ID()).Scan(&leases); err != nil {
			t.Fatal(err)
		}
		if after.CooldownEventID != before.CooldownEventID || cooldownEvent != before.CooldownEventID || leases != 1 {
			t.Fatalf("partial capture commit before=%+v after=%+v cooldown=%q leases=%d", before, after, cooldownEvent, leases)
		}
	})
}

func TestAccountRecoveryCaptureClearReleaseSerialization(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	enableRecoveryCapture(t, f)
	f.insertAccount(t, "ups_capture_race", true)
	for _, model := range []string{"capture-race-first", "capture-race-second"} {
		f.insertModelPool(t, model, "ups_capture_race", 1, modelAccountView{UpstreamID: "ups_capture_race", UpstreamModel: model, Weight: 1, MaxConcurrency: 2})
	}
	first := acquireBoundRecoveryLease(t, f, "capture-race-first", accounting.ProtocolOpenAIChatCompletions)
	second := acquireBoundRecoveryLease(t, f, "capture-race-second", accounting.ProtocolOpenAIResponses)
	first.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient, Phase: scheduling.MayHaveSent})
	state := loadCapturedState(t, f, "ups_capture_race")
	done := make(chan struct{}, 2)
	go func() {
		f.rt.clearCooldown(context.Background(), "ups_capture_race", 1, state.CooldownEventID)
		done <- struct{}{}
	}()
	go func() {
		second.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit, Phase: scheduling.MayHaveSent})
		done <- struct{}{}
	}()
	for range 2 {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("clear and release deadlocked")
		}
	}
	var cooldownEvent, recoveryEvent sql.NullString
	if err := f.base.app.store.db.QueryRow(`SELECT
		(SELECT event_id FROM account_pool_runtime_cooldowns WHERE account_id='ups_capture_race'),
		(SELECT cooldown_event_id FROM account_recovery_states WHERE account_id='ups_capture_race')`).Scan(&cooldownEvent, &recoveryEvent); err != nil {
		t.Fatal(err)
	}
	if cooldownEvent.Valid != recoveryEvent.Valid || cooldownEvent.String != recoveryEvent.String {
		t.Fatalf("clear/release split cooldown=%v recovery=%v", cooldownEvent, recoveryEvent)
	}
}

func TestModelPreflightNonGenerationDoesNotBindRecoverySnapshot(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
	if err := f.base.app.accountPool.Close(); err != nil {
		t.Fatal(err)
	}
	f.base.app.accountPool = f.rt
	f.insertAccount(t, "ups_count_tokens", true)
	f.insertModelPool(t, "count-tokens-model", "ups_count_tokens", 1, modelAccountView{UpstreamID: "ups_count_tokens", UpstreamModel: "actual", Weight: 1, MaxConcurrency: 1})
	r := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil).WithContext(context.WithValue(context.Background(), requestIDKey{}, "count-tokens-preflight"))
	selected, lease, failed := f.base.app.prepareModelRoute(r, f.auth1, "count-tokens-model", []string{"openai-compatible"}, "", false,
		func(_ *http.Request, candidate route) (route, *modelPreflightError) { return candidate, nil })
	if failed != nil || lease == nil || selected.AccountID != "ups_count_tokens" {
		t.Fatalf("non-generation preflight route=%+v lease=%v failure=%v", selected, lease, failed)
	}
	if lease.recovery != nil {
		t.Fatalf("non-generation request bound recovery snapshot: %+v", lease.recovery)
	}
	lease.Release(context.Background(), scheduling.ReleaseResult{})
}
