package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

type runtimeFakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []runtimeFakeTimer
}

type runtimeFakeTimer struct {
	at time.Time
	ch chan time.Time
}

func newRuntimeFakeClock() *runtimeFakeClock {
	return &runtimeFakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
}

func (c *runtimeFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *runtimeFakeClock) After(delay time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	at := c.now.Add(delay)
	if !at.After(c.now) {
		ch <- c.now
	} else {
		c.timers = append(c.timers, runtimeFakeTimer{at: at, ch: ch})
	}
	return ch
}

func (c *runtimeFakeClock) Advance(delay time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delay)
	now := c.now
	remaining := c.timers[:0]
	for _, timer := range c.timers {
		if !timer.at.After(now) {
			timer.ch <- now
		} else {
			remaining = append(remaining, timer)
		}
	}
	c.timers = remaining
	c.mu.Unlock()
}

func (c *runtimeFakeClock) timerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.timers)
}

type runtimeSequenceRandom struct {
	mu     sync.Mutex
	values []int
	next   int
}

func (r *runtimeSequenceRandom) Intn(n int) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	value := 0
	if len(r.values) != 0 {
		value = r.values[r.next%len(r.values)]
		r.next++
	}
	return value % n
}

type runtimeFixture struct {
	base  *accountPoolFixture
	clock *runtimeFakeClock
	rt    *accountPoolRuntime
	auth1 employeeAuth
	auth2 employeeAuth
}

func newRuntimeFixture(t *testing.T, random scheduling.Random, leaseTTL time.Duration, maxWaiters int) *runtimeFixture {
	t.Helper()
	base := newAccountPoolFixture(t, false)
	if err := base.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
		t.Fatalf("migrate runtime: %v", err)
	}
	clock := newRuntimeFakeClock()
	rt, err := newAccountPoolRuntimeWithConfig(base.app, accountPoolRuntimeConfig{
		Clock: clock, Random: random, LeaseTTL: leaseTTL, StickyTTL: time.Minute, MaxSticky: 100, MaxWaiters: maxWaiters,
		Cooldowns: map[scheduling.FailureClass]time.Duration{
			scheduling.FailureRateLimit: 12 * time.Second,
			scheduling.FailureTransient: 5 * time.Second,
		},
	})
	if err != nil {
		t.Fatalf("new runtime: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	fixture := &runtimeFixture{base: base, clock: clock, rt: rt,
		auth1: employeeAuth{EmployeeID: "emp_runtime_1", KeyID: "key_runtime_1", Mode: "selected"},
		auth2: employeeAuth{EmployeeID: "emp_runtime_2", KeyID: "key_runtime_2", Mode: "selected"},
	}
	fixture.insertEmployee(t, fixture.auth1)
	fixture.insertEmployee(t, fixture.auth2)
	return fixture
}

func (f *runtimeFixture) insertEmployee(t *testing.T, auth employeeAuth) {
	t.Helper()
	if _, err := f.base.app.store.db.Exec(`INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,'','','active','selected',1,?)`, auth.EmployeeID, auth.EmployeeID, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.base.app.store.db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?, ?,X'01',1,?,?)`, auth.KeyID, auth.EmployeeID, "runtime", "selector-"+auth.KeyID, "operation-"+auth.KeyID, utcNow()); err != nil {
		t.Fatal(err)
	}
}

func (f *runtimeFixture) insertAccount(t *testing.T, id string, enabled bool) {
	t.Helper()
	f.base.insertUpstream(t, id, "openai-compatible")
	if !enabled {
		if _, err := f.base.app.store.db.Exec(`UPDATE upstreams SET enabled=0 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *runtimeFixture) insertModelPool(t *testing.T, model, legacyAccount string, revision int64, routes ...modelAccountView) {
	t.Helper()
	f.base.insertModel(t, model, legacyAccount, "legacy-"+model)
	for _, auth := range []employeeAuth{f.auth1, f.auth2} {
		if _, err := f.base.app.store.db.Exec(`INSERT INTO employee_models(employee_id,model_id) VALUES(?,?)`, auth.EmployeeID, model); err != nil {
			t.Fatal(err)
		}
	}
	if revision == 0 {
		return
	}
	if _, err := f.base.app.store.db.Exec(`INSERT INTO model_account_pool_configs(model_id,revision,updated_at) VALUES(?,?,?)`, model, revision, utcNow()); err != nil {
		t.Fatal(err)
	}
	for position, item := range routes {
		if _, err := f.base.app.store.db.Exec(`INSERT INTO model_account_pool_routes(model_id,upstream_id,upstream_model,priority,weight,max_concurrency,position) VALUES(?,?,?,?,?,?,?)`, model, item.UpstreamID, item.UpstreamModel, item.Priority, item.Weight, item.MaxConcurrency, position); err != nil {
			t.Fatal(err)
		}
	}
}

func acquireRuntime(t *testing.T, rt *accountPoolRuntime, model string, auth employeeAuth, sticky string) accountPoolAcquireResult {
	t.Helper()
	result := rt.Acquire(context.Background(), model, auth, []string{"openai-compatible"}, sticky)
	if result.Code != accountPoolAcquired || result.Lease == nil {
		t.Fatalf("acquire %s: %+v", model, result)
	}
	return result
}

func TestAccountPoolRuntimeMigrationRollbackAndMetadataOnly(t *testing.T) {
	base := newAccountPoolFixture(t, false)
	if _, err := base.app.store.db.Exec(`CREATE TABLE account_pool_runtime_leases(lease_id TEXT PRIMARY KEY,account_id TEXT,public_model TEXT,employee_id TEXT,key_id TEXT,pool_revision INTEGER,account_revision INTEGER,expires_at TEXT,created_at TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := base.app.store.migrateAccountPoolRuntime(context.Background()); err == nil {
		t.Fatal("runtime migration accepted missing foreign keys")
	}
	var cooldownTables int
	if err := base.app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, accountPoolCooldownTable).Scan(&cooldownTables); err != nil || cooldownTables != 0 {
		t.Fatalf("failed migration left cooldown table count=%d err=%v", cooldownTables, err)
	}
	if _, err := base.app.store.db.Exec(`DROP TABLE account_pool_runtime_leases`); err != nil {
		t.Fatal(err)
	}
	if err := base.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	allowed := map[string]bool{"lease_id": true, "account_id": true, "public_model": true, "employee_id": true, "key_id": true, "pool_revision": true, "account_revision": true, "expires_at": true, "created_at": true}
	rows, err := base.app.store.db.Query(`PRAGMA table_info(account_pool_runtime_leases)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, kind string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatal(err)
		}
		if !allowed[name] {
			t.Fatalf("runtime lease table contains non-metadata column %q", name)
		}
		seen[name] = true
	}
	if len(seen) != len(allowed) {
		t.Fatalf("runtime lease columns=%v", seen)
	}
}

func TestAccountPoolRuntimeLegacyDisabledDefaultAndGlobalCapacity(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_legacy_disabled", false)
	f.insertAccount(t, "ups_shared", true)
	f.insertModelPool(t, "legacy-only", "ups_legacy_disabled", 0)
	legacy := f.rt.Acquire(context.Background(), "legacy-only", f.auth1, []string{"openai-compatible"}, "")
	if !legacy.Legacy || legacy.Code != accountPoolLegacy || legacy.Lease != nil {
		t.Fatalf("legacy result=%+v", legacy)
	}

	f.insertModelPool(t, "pool-low", "ups_legacy_disabled", 1, modelAccountView{UpstreamID: "ups_shared", UpstreamModel: "provider-low", Weight: 1, MaxConcurrency: 1})
	f.insertModelPool(t, "pool-high", "ups_legacy_disabled", 1, modelAccountView{UpstreamID: "ups_shared", UpstreamModel: "provider-high", Weight: 1, MaxConcurrency: 10})
	first := acquireRuntime(t, f.rt, "pool-high", f.auth1, "")
	if first.Route.AccountID != "ups_shared" || first.Route.UpstreamModel != "provider-high" {
		t.Fatalf("selected route=%+v", first.Route)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan accountPoolAcquireResult, 1)
	go func() { waited <- f.rt.Acquire(ctx, "pool-high", f.auth2, []string{"openai-compatible"}, "") }()
	select {
	case result := <-waited:
		t.Fatalf("global minimum capacity ignored: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	if result := <-waited; result.Code != accountPoolCancelled {
		t.Fatalf("cancelled waiter=%+v", result)
	}
	if ok, release := first.Lease.Release(context.Background(), scheduling.ReleaseResult{}); !ok || release.Code != accountPoolReleased {
		t.Fatalf("release=%v %+v", ok, release)
	}
}

func TestAccountPoolRuntimeSeparatesAuthorizationAndModelResults(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_codes", true)

	if result := f.rt.Acquire(context.Background(), "missing-model", f.auth1, []string{"openai-compatible"}, ""); result.Code != accountPoolNoCompatible || result.Lease != nil {
		t.Fatalf("missing model result=%+v", result)
	}

	f.insertModelPool(t, "denied-model", "ups_codes", 1, modelAccountView{UpstreamID: "ups_codes", UpstreamModel: "provider-denied", Weight: 1, MaxConcurrency: 1})
	if _, err := f.base.app.store.db.Exec(`DELETE FROM employee_models WHERE employee_id=? AND model_id='denied-model'`, f.auth1.EmployeeID); err != nil {
		t.Fatal(err)
	}
	if result := f.rt.Acquire(context.Background(), "denied-model", f.auth1, []string{"openai-compatible"}, ""); result.Code != accountPoolModelNotAllowed || result.Lease != nil {
		t.Fatalf("model policy result=%+v", result)
	}

	f.insertModelPool(t, "disabled-model", "ups_codes", 1, modelAccountView{UpstreamID: "ups_codes", UpstreamModel: "provider-disabled", Weight: 1, MaxConcurrency: 1})
	if _, err := f.base.app.store.db.Exec(`UPDATE models SET enabled=0 WHERE id='disabled-model'`); err != nil {
		t.Fatal(err)
	}
	if result := f.rt.Acquire(context.Background(), "disabled-model", f.auth1, []string{"openai-compatible"}, ""); result.Code != accountPoolNoCompatible || result.Lease != nil {
		t.Fatalf("disabled model result=%+v", result)
	}

	if _, err := f.base.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), f.auth1.KeyID); err != nil {
		t.Fatal(err)
	}
	if result := f.rt.Acquire(context.Background(), "denied-model", f.auth1, []string{"openai-compatible"}, ""); result.Code != accountPoolAuthorizationChanged || result.Lease != nil {
		t.Fatalf("revoked key result=%+v", result)
	}
}

func TestAccountPoolRuntimeNormalizesSubsecondLeaseBeforeExpiryCleanup(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 4)
	f.insertAccount(t, "ups_subsecond", true)
	f.insertModelPool(t, "subsecond-model", "ups_subsecond", 1, modelAccountView{UpstreamID: "ups_subsecond", UpstreamModel: "provider-subsecond", Weight: 1, MaxConcurrency: 1})
	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	expiresAt := f.clock.Now().Add(500 * time.Millisecond)
	legacyVariableWidth := expiresAt.UTC().Format(time.RFC3339Nano)
	if legacyVariableWidth == formatAccountPoolTime(expiresAt) {
		t.Fatalf("test timestamp unexpectedly fixed width: %q", legacyVariableWidth)
	}
	if _, err := f.base.app.store.db.Exec(`INSERT INTO account_pool_runtime_leases(lease_id,account_id,public_model,employee_id,key_id,pool_revision,account_revision,expires_at,created_at) VALUES('lease_subsecond','ups_subsecond','subsecond-model',?,?,1,1,?,?)`, f.auth1.EmployeeID, f.auth1.KeyID, legacyVariableWidth, formatAccountPoolTime(f.clock.Now())); err != nil {
		t.Fatal(err)
	}

	restarted, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, Random: &runtimeSequenceRandom{}, LeaseTTL: 30 * time.Second, MaxWaiters: 4})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	var stored string
	if err := f.base.app.store.db.QueryRow(`SELECT expires_at FROM account_pool_runtime_leases WHERE lease_id='lease_subsecond'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != formatAccountPoolTime(expiresAt) {
		t.Fatalf("normalized expiry=%q want %q", stored, formatAccountPoolTime(expiresAt))
	}
	waited := make(chan accountPoolAcquireResult, 1)
	go func() {
		waited <- restarted.Acquire(context.Background(), "subsecond-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	select {
	case result := <-waited:
		t.Fatalf("future subsecond lease was released early: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	f.clock.Advance(600 * time.Millisecond)
	select {
	case result := <-waited:
		if result.Code != accountPoolAcquired || result.Lease == nil {
			t.Fatalf("post-expiry acquire=%+v", result)
		}
		result.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	case <-time.After(time.Second):
		t.Fatal("subsecond lease did not expire")
	}
}

func TestAccountPoolRuntimeWaitRevalidatesAccountConfigAndAuthorization(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*runtimeFixture, *testing.T)
		want       accountPoolRuntimeCode
		restoreSQL string
	}{
		{name: "account revision", want: accountPoolAccountChanged, mutate: func(f *runtimeFixture, t *testing.T) {
			_, err := f.base.app.store.db.Exec(`UPDATE upstreams SET enabled=0,revision=revision+1 WHERE id='ups_wait'`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "pool revision", want: accountPoolConfigurationChanged, mutate: func(f *runtimeFixture, t *testing.T) {
			_, err := f.base.app.store.db.Exec(`UPDATE model_account_pool_configs SET revision=revision+1 WHERE model_id='wait-model'`)
			if err != nil {
				t.Fatal(err)
			}
		}},
		{name: "key revoked", want: accountPoolAuthorizationChanged, mutate: func(f *runtimeFixture, t *testing.T) {
			_, err := f.base.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), f.auth2.KeyID)
			if err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
			f.insertAccount(t, "ups_wait", true)
			f.insertModelPool(t, "wait-model", "ups_wait", 1, modelAccountView{UpstreamID: "ups_wait", UpstreamModel: "provider-wait", Weight: 1, MaxConcurrency: 1})
			active := acquireRuntime(t, f.rt, "wait-model", f.auth1, "")
			waited := make(chan accountPoolAcquireResult, 1)
			go func() {
				waited <- f.rt.Acquire(context.Background(), "wait-model", f.auth2, []string{"openai-compatible"}, "")
			}()
			select {
			case result := <-waited:
				t.Fatalf("request did not wait: %+v", result)
			case <-time.After(30 * time.Millisecond):
			}
			test.mutate(f, t)
			active.Lease.Release(context.Background(), scheduling.ReleaseResult{})
			select {
			case result := <-waited:
				if result.Code != test.want || result.Lease != nil || len(result.Route.Ciphertext) != 0 {
					t.Fatalf("revalidation result=%+v", result)
				}
			case <-time.After(time.Second):
				t.Fatal("waiter was not awakened")
			}
		})
	}
}

func TestAccountPoolRuntimeStickyIsEmployeeScoped(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{values: []int{0, 1}}, 30*time.Second, 2)
	f.insertAccount(t, "ups_sticky_a", true)
	f.insertAccount(t, "ups_sticky_b", true)
	f.insertModelPool(t, "sticky-model", "ups_sticky_a", 1,
		modelAccountView{UpstreamID: "ups_sticky_a", UpstreamModel: "a", Weight: 1, MaxConcurrency: 2},
		modelAccountView{UpstreamID: "ups_sticky_b", UpstreamModel: "b", Weight: 1, MaxConcurrency: 2})
	one := acquireRuntime(t, f.rt, "sticky-model", f.auth1, "same-opaque")
	one.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	oneAgain := acquireRuntime(t, f.rt, "sticky-model", f.auth1, "same-opaque")
	if oneAgain.Route.AccountID != one.Route.AccountID {
		t.Fatalf("same employee sticky moved %s -> %s", one.Route.AccountID, oneAgain.Route.AccountID)
	}
	oneAgain.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	two := acquireRuntime(t, f.rt, "sticky-model", f.auth2, "same-opaque")
	if two.Route.AccountID == one.Route.AccountID {
		t.Fatalf("sticky crossed employees onto %s", two.Route.AccountID)
	}
	two.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	var stickyColumns int
	if err := f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('account_pool_runtime_leases') WHERE name LIKE '%sticky%'`).Scan(&stickyColumns); err != nil || stickyColumns != 0 {
		t.Fatalf("sticky persistence columns=%d err=%v", stickyColumns, err)
	}
}

func TestAccountPoolRuntimeHeartbeatFailureReleaseFailureRestartAndCooldown(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 9*time.Second, 2)
	f.insertAccount(t, "ups_lifecycle", true)
	f.insertModelPool(t, "lifecycle-model", "ups_lifecycle", 1, modelAccountView{UpstreamID: "ups_lifecycle", UpstreamModel: "provider", Weight: 1, MaxConcurrency: 1})
	active := acquireRuntime(t, f.rt, "lifecycle-model", f.auth1, "")
	initialExpiry := persistedRuntimeExpiry(t, f, active.Lease.inner.ID())
	waitForRuntimeCondition(t, func() bool { return f.clock.timerCount() > 0 })
	f.clock.Advance(3 * time.Second)
	waitForRuntimeCondition(t, func() bool { return persistedRuntimeExpiry(t, f, active.Lease.inner.ID()).After(initialExpiry) })
	if _, err := f.base.app.store.db.Exec(`CREATE TRIGGER runtime_heartbeat_failure BEFORE UPDATE OF expires_at ON account_pool_runtime_leases BEGIN SELECT RAISE(ABORT,'synthetic heartbeat failure'); END`); err != nil {
		t.Fatal(err)
	}
	waitForRuntimeCondition(t, func() bool { return f.clock.timerCount() > 0 })
	f.clock.Advance(3 * time.Second)
	select {
	case <-active.Lease.Context().Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat persistence failure did not cancel request")
	}
	if code := active.Lease.Heartbeat(context.Background()); code != accountPoolStorageUnavailable {
		t.Fatalf("heartbeat failure was not terminal: %s", code)
	}
	if _, err := f.base.app.store.db.Exec(`DROP TRIGGER runtime_heartbeat_failure`); err != nil {
		t.Fatal(err)
	}
	active.Lease.Release(context.Background(), scheduling.ReleaseResult{})

	failedRelease := acquireRuntime(t, f.rt, "lifecycle-model", f.auth1, "")
	if _, err := f.base.app.store.db.Exec(`CREATE TRIGGER runtime_release_failure BEFORE DELETE ON account_pool_runtime_leases BEGIN SELECT RAISE(ABORT,'synthetic release failure'); END`); err != nil {
		t.Fatal(err)
	}
	if ok, result := failedRelease.Lease.Release(context.Background(), scheduling.ReleaseResult{}); ok || result.Code != accountPoolStorageUnavailable {
		t.Fatalf("failed release=%v %+v", ok, result)
	}
	if ok, result := failedRelease.Lease.Release(context.Background(), scheduling.ReleaseResult{}); ok || result.Code != accountPoolAlreadyReleased {
		t.Fatalf("repeat failed release=%v %+v", ok, result)
	}
	if _, err := f.base.app.store.db.Exec(`DROP TRIGGER runtime_release_failure`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	waited := make(chan accountPoolAcquireResult, 1)
	go func() { waited <- f.rt.Acquire(ctx, "lifecycle-model", f.auth2, []string{"openai-compatible"}, "") }()
	select {
	case result := <-waited:
		t.Fatalf("failed release freed capacity: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	cancel()
	<-waited
	f.clock.Advance(10 * time.Second)

	beforeRestart := acquireRuntime(t, f.rt, "lifecycle-model", f.auth1, "")
	if err := f.rt.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-beforeRestart.Lease.Context().Done():
	default:
		t.Fatal("runtime close did not cancel active request")
	}
	restarted, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, Random: &runtimeSequenceRandom{}, LeaseTTL: 9 * time.Second, MaxWaiters: 2, Cooldowns: f.rt.cooldowns})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = restarted.Close() })
	restartCtx, restartCancel := context.WithCancel(context.Background())
	restartWait := make(chan accountPoolAcquireResult, 1)
	go func() {
		restartWait <- restarted.Acquire(restartCtx, "lifecycle-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	select {
	case result := <-restartWait:
		t.Fatalf("restart ignored persisted lease: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	restartCancel()
	<-restartWait
	f.clock.Advance(10 * time.Second)
	cooldownLease := acquireRuntime(t, restarted, "lifecycle-model", f.auth1, "")
	ok, released := cooldownLease.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit})
	if !ok || released.Code != accountPoolReleased || !released.RetrySuggested {
		t.Fatalf("cooldown release=%v %+v", ok, released)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	cooledRestart, err := newAccountPoolRuntimeWithConfig(f.base.app, accountPoolRuntimeConfig{Clock: f.clock, Random: &runtimeSequenceRandom{}, LeaseTTL: 9 * time.Second, MaxWaiters: 2, Cooldowns: f.rt.cooldowns})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cooledRestart.Close() })
	cooled := make(chan accountPoolAcquireResult, 1)
	go func() {
		cooled <- cooledRestart.Acquire(context.Background(), "lifecycle-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	select {
	case result := <-cooled:
		t.Fatalf("restart ignored persisted cooldown: %+v", result)
	case <-time.After(30 * time.Millisecond):
	}
	f.clock.Advance(12 * time.Second)
	select {
	case result := <-cooled:
		if result.Code != accountPoolAcquired || result.Lease == nil {
			t.Fatalf("cooldown recovery=%+v", result)
		}
		result.Lease.Release(context.Background(), scheduling.ReleaseResult{})
	case <-time.After(time.Second):
		t.Fatal("cooldown did not recover")
	}
}

func persistedRuntimeExpiry(t *testing.T, f *runtimeFixture, leaseID string) time.Time {
	t.Helper()
	var value string
	if err := f.base.app.store.db.QueryRow(`SELECT expires_at FROM account_pool_runtime_leases WHERE lease_id=?`, leaseID).Scan(&value); err != nil {
		t.Fatalf("read persisted expiry: %v", err)
	}
	expires, err := parseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return expires
}

func waitForRuntimeCondition(t *testing.T, condition func() bool) {
	t.Helper()
	for range 200 {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("runtime condition was not reached")
}
