package service

import (
	"context"
	"testing"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

func TestAccountPoolRuntimeAcquireOptionsExcludeStickyAndBindRevision(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{values: []int{0}}, 30*time.Second, 2)
	f.insertAccount(t, "ups_failover_a", true)
	f.insertAccount(t, "ups_failover_b", true)
	f.insertModelPool(t, "failover-model", "ups_failover_a", 1,
		modelAccountView{UpstreamID: "ups_failover_a", UpstreamModel: "provider-a", Weight: 1, MaxConcurrency: 2},
		modelAccountView{UpstreamID: "ups_failover_b", UpstreamModel: "provider-b", Weight: 1, MaxConcurrency: 2})

	first := acquireRuntime(t, f.rt, "failover-model", f.auth1, "stable-session")
	if first.Lease.PoolRevision() != 1 {
		t.Fatalf("pool revision=%d", first.Lease.PoolRevision())
	}
	failedAccount := first.Route.AccountID
	if ok, released := first.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}); !ok || !released.RetrySuggested {
		t.Fatalf("pre-dispatch release ok=%v result=%+v", ok, released)
	}

	second := f.rt.AcquireWithOptions(context.Background(), "failover-model", f.auth1, []string{"openai-compatible"}, "stable-session", accountPoolAcquireOptions{
		ExcludedAccountIDs:   []string{failedAccount},
		ExpectedPoolRevision: first.PoolRevision,
	})
	if second.Code != accountPoolAcquired || second.Lease == nil || second.Route.AccountID == failedAccount {
		t.Fatalf("excluded sticky account was reused: %+v", second)
	}
	if second.PoolRevision != first.PoolRevision || second.Lease.PoolRevision() != first.PoolRevision {
		t.Fatalf("revision changed result=%d lease=%d want=%d", second.PoolRevision, second.Lease.PoolRevision(), first.PoolRevision)
	}
	second.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	noCompatible := f.rt.AcquireWithOptions(context.Background(), "failover-model", f.auth1, []string{"anthropic"}, "", accountPoolAcquireOptions{ExpectedPoolRevision: first.PoolRevision})
	if noCompatible.Code != accountPoolNoCompatible || noCompatible.Lease != nil {
		t.Fatalf("unchanged pool without eligible candidates result=%+v", noCompatible)
	}

	if _, err := f.base.app.store.db.Exec(`UPDATE model_account_pool_configs SET revision=2 WHERE model_id='failover-model'`); err != nil {
		t.Fatal(err)
	}
	changed := f.rt.AcquireWithOptions(context.Background(), "failover-model", f.auth1, []string{"openai-compatible"}, "stable-session", accountPoolAcquireOptions{ExpectedPoolRevision: first.PoolRevision})
	if changed.Code != accountPoolConfigurationChanged || changed.Lease != nil || len(changed.Route.Ciphertext) != 0 {
		t.Fatalf("changed revision result=%+v", changed)
	}

	f.insertModelPool(t, "removed-pool-model", "ups_failover_a", 7,
		modelAccountView{UpstreamID: "ups_failover_a", UpstreamModel: "removed-provider", Weight: 1, MaxConcurrency: 2})
	removedFirst := acquireRuntime(t, f.rt, "removed-pool-model", f.auth1, "")
	removedRevision := removedFirst.PoolRevision
	removedFirst.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	if _, err := f.base.app.store.db.Exec(`DELETE FROM model_account_pool_routes WHERE model_id='removed-pool-model'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`DELETE FROM model_account_pool_configs WHERE model_id='removed-pool-model'`); err != nil {
		t.Fatal(err)
	}
	removed := f.rt.AcquireWithOptions(context.Background(), "removed-pool-model", f.auth1, []string{"openai-compatible"}, "", accountPoolAcquireOptions{ExpectedPoolRevision: removedRevision})
	if removed.Code != accountPoolConfigurationChanged || removed.Legacy || removed.Lease != nil {
		t.Fatalf("removed pool result=%+v", removed)
	}

	invalid := f.rt.AcquireWithOptions(context.Background(), "failover-model", f.auth1, []string{"openai-compatible"}, "", accountPoolAcquireOptions{ExcludedAccountIDs: []string{"ups_failover_a", "ups_failover_a"}})
	if invalid.Code != accountPoolInvalid || invalid.Lease != nil {
		t.Fatalf("duplicate exclusions result=%+v", invalid)
	}
}

func TestAccountPoolLeaseDispatchEvidenceIsMonotonicAndPositive(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
	f.insertAccount(t, "ups_phase", true)
	f.insertModelPool(t, "phase-model", "ups_phase", 1,
		modelAccountView{UpstreamID: "ups_phase", UpstreamModel: "provider-phase", Weight: 1, MaxConcurrency: 8})

	var nilLease *accountPoolLease
	nilLease.MarkDispatch()
	nilLease.MarkOutput()
	if nilLease.PoolRevision() != 0 {
		t.Fatal("nil lease returned a revision")
	}

	tests := []struct {
		name    string
		prepare func(*accountPoolLease)
		result  scheduling.ReleaseResult
		want    bool
	}{
		{name: "unknown zero value", result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent}},
		{name: "deprecated booleans do not authorize", result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, StreamCommitted: false, ExecutionUncertain: false}},
		{name: "explicit not started", result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}, want: true},
		{name: "caller may have sent", result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.MayHaveSent}},
		{name: "lease marked dispatch", prepare: func(lease *accountPoolLease) { lease.MarkDispatch() }, result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}},
		{name: "lease marked output cannot regress", prepare: func(lease *accountPoolLease) { lease.MarkOutput(); lease.MarkDispatch() }, result: scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			acquired := acquireRuntime(t, f.rt, "phase-model", f.auth1, "")
			if test.prepare != nil {
				test.prepare(acquired.Lease)
			}
			ok, released := acquired.Lease.Release(context.Background(), test.result)
			if !ok || released.Code != accountPoolReleased || released.RetrySuggested != test.want {
				t.Fatalf("release ok=%v result=%+v wantRetry=%v", ok, released, test.want)
			}
		})
	}

	cancelledContext, cancel := context.WithCancel(context.Background())
	cancelled := f.rt.Acquire(cancelledContext, "phase-model", f.auth1, []string{"openai-compatible"}, "")
	if cancelled.Code != accountPoolAcquired || cancelled.Lease == nil {
		t.Fatalf("acquire cancellation lease: %+v", cancelled)
	}
	cancel()
	if ok, released := cancelled.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}); !ok || released.RetrySuggested {
		t.Fatalf("cancelled lease release ok=%v result=%+v", ok, released)
	}

	expired := acquireRuntime(t, f.rt, "phase-model", f.auth1, "")
	if _, err := f.base.app.store.db.Exec(`UPDATE account_pool_runtime_leases SET expires_at=? WHERE lease_id=?`, formatAccountPoolTime(f.clock.Now().Add(-time.Second)), expired.Lease.inner.ID()); err != nil {
		t.Fatal(err)
	}
	if ok, released := expired.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}); ok || released.RetrySuggested || released.Code != accountPoolAlreadyReleased {
		t.Fatalf("expired release ok=%v result=%+v", ok, released)
	}

	failed := acquireRuntime(t, f.rt, "phase-model", f.auth1, "")
	if _, err := f.base.app.store.db.Exec(`CREATE TRIGGER failover_release_failure BEFORE DELETE ON account_pool_runtime_leases BEGIN SELECT RAISE(ABORT,'synthetic release failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, released := failed.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailurePermanent, Phase: scheduling.DispatchNotStarted}); ok || released.RetrySuggested || released.Code != accountPoolStorageUnavailable {
		t.Fatalf("storage release ok=%v result=%+v", ok, released)
	}
	if _, err := f.base.app.store.db.Exec(`DROP TRIGGER failover_release_failure`); err != nil {
		t.Fatal(err)
	}
}
