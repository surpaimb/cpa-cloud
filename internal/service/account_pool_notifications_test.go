package service

import (
	"context"
	"testing"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

func TestAccountPoolRuntimeNotifyChangedRevalidatesQueuedAuthorization(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.insertAccount(t, "ups_notify_revoke", true)
	f.insertModelPool(t, "notify-revoke-model", "ups_notify_revoke", 1, modelAccountView{
		UpstreamID: "ups_notify_revoke", UpstreamModel: "provider-model", Weight: 1, MaxConcurrency: 1,
	})
	active := acquireRuntime(t, f.rt, "notify-revoke-model", f.auth1, "")
	defer active.Lease.Release(context.Background(), scheduling.ReleaseResult{})

	waited := make(chan accountPoolAcquireResult, 1)
	go func() {
		waited <- f.rt.Acquire(context.Background(), "notify-revoke-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	assertRuntimeAcquireStillWaiting(t, waited)

	if _, err := f.base.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), f.auth2.KeyID); err != nil {
		t.Fatal(err)
	}
	f.rt.NotifyChanged()

	select {
	case result := <-waited:
		if result.Code != accountPoolAuthorizationChanged || result.Lease != nil || len(result.Route.Ciphertext) != 0 {
			t.Fatalf("revoked queued admission=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("revoked queued admission was not awakened")
	}
}

func TestAccountPoolRuntimeNotifyChangedRejectsQueuedConfigurationSnapshot(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 1)
	f.insertAccount(t, "ups_notify_capacity", true)
	f.insertModelPool(t, "notify-capacity-model", "ups_notify_capacity", 1, modelAccountView{
		UpstreamID: "ups_notify_capacity", UpstreamModel: "provider-model", Weight: 1, MaxConcurrency: 1,
	})
	active := acquireRuntime(t, f.rt, "notify-capacity-model", f.auth1, "")
	defer active.Lease.Release(context.Background(), scheduling.ReleaseResult{})

	waited := make(chan accountPoolAcquireResult, 1)
	go func() {
		waited <- f.rt.Acquire(context.Background(), "notify-capacity-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	assertRuntimeAcquireStillWaiting(t, waited)

	tx, err := f.base.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE model_account_pool_routes SET max_concurrency=2 WHERE model_id=? AND upstream_id=?`, "notify-capacity-model", "ups_notify_capacity"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE model_account_pool_configs SET revision=revision+1,updated_at=? WHERE model_id=?`, utcNow(), "notify-capacity-model"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	f.rt.NotifyChanged()

	select {
	case result := <-waited:
		if result.Code != accountPoolConfigurationChanged || result.Lease != nil || len(result.Route.Ciphertext) != 0 {
			t.Fatalf("changed queued admission=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("changed queued admission was not awakened")
	}
}

func TestAccountPoolRuntimeCooldownWinnerPersistsOneEventAcrossRestart(t *testing.T) {
	tests := []struct {
		name          string
		first         scheduling.FailureClass
		second        scheduling.FailureClass
		wantUntilFrom time.Duration
		wantUpdatedAt time.Duration
	}{
		{name: "long then short", first: scheduling.FailureRateLimit, second: scheduling.FailureTransient, wantUntilFrom: 12 * time.Second},
		{name: "short then long", first: scheduling.FailureTransient, second: scheduling.FailureRateLimit, wantUntilFrom: 13 * time.Second, wantUpdatedAt: time.Second},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
			accountID := "ups_cooldown_winner"
			modelID := "cooldown-winner-model"
			f.insertAccount(t, accountID, true)
			f.insertModelPool(t, modelID, accountID, 1, modelAccountView{
				UpstreamID: accountID, UpstreamModel: "provider-model", Weight: 1, MaxConcurrency: 2,
			})
			first := acquireRuntime(t, f.rt, modelID, f.auth1, "")
			second := acquireRuntime(t, f.rt, modelID, f.auth2, "")
			started := f.clock.Now()
			if ok, result := first.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: test.first}); !ok || result.Code != accountPoolReleased {
				t.Fatalf("first release=%+v ok=%v", result, ok)
			}
			f.clock.Advance(time.Second)
			if ok, result := second.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: test.second}); !ok || result.Code != accountPoolReleased {
				t.Fatalf("second release=%+v ok=%v", result, ok)
			}

			wantUntil := started.Add(test.wantUntilFrom)
			wantUpdated := started.Add(test.wantUpdatedAt)
			assertStoredRuntimeCooldown(t, f, accountID, scheduling.FailureRateLimit, wantUntil, wantUpdated)
			if err := f.rt.Close(); err != nil {
				t.Fatal(err)
			}
			restarted, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{
				Clock: f.clock, Random: &runtimeSequenceRandom{}, LeaseTTL: 30 * time.Second, StickyTTL: time.Minute, MaxSticky: 100, MaxWaiters: 2, Cooldowns: f.rt.cooldowns,
			})
			if err != nil {
				t.Fatalf("restart runtime: %v", err)
			}
			t.Cleanup(func() { _ = restarted.Close() })
			assertStoredRuntimeCooldown(t, f, accountID, scheduling.FailureRateLimit, wantUntil, wantUpdated)

			waited := make(chan accountPoolAcquireResult, 1)
			go func() {
				waited <- restarted.Acquire(context.Background(), modelID, f.auth1, []string{"openai-compatible"}, "")
			}()
			assertRuntimeAcquireStillWaiting(t, waited)
			f.clock.Advance(wantUntil.Sub(f.clock.Now()))
			select {
			case result := <-waited:
				if result.Code != accountPoolAcquired || result.Lease == nil {
					t.Fatalf("post-cooldown admission=%+v", result)
				}
				result.Lease.Release(context.Background(), scheduling.ReleaseResult{})
			case <-time.After(time.Second):
				t.Fatal("persisted cooldown did not expire at winning deadline")
			}
		})
	}
}

func assertRuntimeAcquireStillWaiting(t *testing.T, result <-chan accountPoolAcquireResult) {
	t.Helper()
	select {
	case got := <-result:
		t.Fatalf("admission did not wait: %+v", got)
	case <-time.After(30 * time.Millisecond):
	}
}

func assertStoredRuntimeCooldown(t *testing.T, f *runtimeFixture, accountID string, wantClass scheduling.FailureClass, wantUntil, wantUpdated time.Time) {
	t.Helper()
	var failureClass, untilText, updatedText string
	if err := f.base.app.store.db.QueryRow(`SELECT failure_class,cooldown_until,updated_at FROM account_pool_runtime_cooldowns WHERE account_id=?`, accountID).Scan(&failureClass, &untilText, &updatedText); err != nil {
		t.Fatal(err)
	}
	until, err := parseTime(untilText)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := parseTime(updatedText)
	if err != nil {
		t.Fatal(err)
	}
	if failureClass != string(wantClass) || !until.Equal(wantUntil) || !updated.Equal(wantUpdated) {
		t.Fatalf("stored cooldown class=%q until=%s updated=%s; want class=%q until=%s updated=%s", failureClass, until, updated, wantClass, wantUntil, wantUpdated)
	}
}
