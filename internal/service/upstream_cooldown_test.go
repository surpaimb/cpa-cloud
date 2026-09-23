package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/scheduling"
)

func TestAccountPoolCooldownMigrationRollbackRetryAndLegacyUpgrade(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	if err := f.app.accountPool.Close(); err != nil {
		t.Fatal(err)
	}
	f.insertUpstream(t, "ups_cooldown_migration", "openai-compatible")

	invalidSchemas := []struct {
		name       string
		ddl        string
		indexDDL   string
		hasEventID bool
	}{
		{
			name:       "current case-sensitive CHECK values",
			ddl:        strings.ReplaceAll(cooldownCurrentDDL, "'transient'", "'TRANSIENT'"),
			hasEventID: true,
		},
		{
			name: "legacy missing primary key",
			ddl: `CREATE TABLE account_pool_runtime_cooldowns (
				account_id TEXT REFERENCES upstreams(id) ON DELETE CASCADE,
				failure_class TEXT NOT NULL,
				cooldown_until TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
		{
			name: "current missing primary key",
			ddl: `CREATE TABLE account_pool_runtime_cooldowns (
				account_id TEXT REFERENCES upstreams(id) ON DELETE CASCADE,
				event_id TEXT NOT NULL UNIQUE,
				failure_class TEXT NOT NULL CHECK(failure_class IN ('rate_limited','overloaded','transient','authentication','permanent')),
				cooldown_until TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			hasEventID: true,
		},
		{
			name: "legacy wrong type and nullability",
			ddl: `CREATE TABLE account_pool_runtime_cooldowns (
				account_id BLOB PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
				failure_class TEXT,
				cooldown_until TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
		},
		{
			name: "current wrong type and nullability",
			ddl: `CREATE TABLE account_pool_runtime_cooldowns (
				account_id TEXT PRIMARY KEY REFERENCES upstreams(id) ON DELETE CASCADE,
				event_id BLOB UNIQUE,
				failure_class TEXT NOT NULL CHECK(failure_class IN ('rate_limited','overloaded','transient','authentication','permanent')),
				cooldown_until TEXT NOT NULL,
				updated_at TEXT NOT NULL
			)`,
			hasEventID: true,
		},
		{
			name:     "legacy extra unique constraint",
			ddl:      cooldownLegacyDDL,
			indexDDL: `CREATE UNIQUE INDEX unexpected_cooldown_failure_unique ON account_pool_runtime_cooldowns(failure_class)`,
		},
		{
			name:       "current extra unique constraint",
			ddl:        cooldownCurrentDDL,
			indexDDL:   `CREATE UNIQUE INDEX unexpected_cooldown_updated_unique ON account_pool_runtime_cooldowns(updated_at)`,
			hasEventID: true,
		},
		{
			name:     "legacy unique expiry index",
			ddl:      cooldownLegacyDDL,
			indexDDL: `CREATE UNIQUE INDEX account_pool_runtime_cooldown_expiry_idx ON account_pool_runtime_cooldowns(cooldown_until)`,
		},
		{
			name:       "current partial expiry index",
			ddl:        cooldownCurrentDDL,
			indexDDL:   `CREATE INDEX account_pool_runtime_cooldown_expiry_idx ON account_pool_runtime_cooldowns(cooldown_until) WHERE failure_class='transient'`,
			hasEventID: true,
		},
	}

	replaceSchema := func(t *testing.T, ddl, indexDDL string) {
		t.Helper()
		if _, err := f.app.store.db.Exec(`DROP TABLE account_pool_runtime_cooldowns`); err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.store.db.Exec(ddl); err != nil {
			t.Fatal(err)
		}
		if indexDDL != "" {
			if _, err := f.app.store.db.Exec(indexDDL); err != nil {
				t.Fatal(err)
			}
		}
	}

	for _, test := range invalidSchemas {
		t.Run(test.name, func(t *testing.T) {
			replaceSchema(t, test.ddl, test.indexDDL)
			if err := f.app.store.migrateAccountPoolRuntime(context.Background()); err == nil {
				t.Fatal("migration accepted incompatible cooldown schema")
			}
			var eventColumns, legacyTables int
			if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('account_pool_runtime_cooldowns') WHERE name='event_id'`).Scan(&eventColumns); err != nil {
				t.Fatal(err)
			}
			wantEventColumns := 0
			if test.hasEventID {
				wantEventColumns = 1
			}
			if eventColumns != wantEventColumns {
				t.Fatalf("failed migration changed source table: event columns=%d want=%d", eventColumns, wantEventColumns)
			}
			if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_pool_runtime_cooldowns_legacy'`).Scan(&legacyTables); err != nil || legacyTables != 0 {
				t.Fatalf("failed migration left a legacy table: count=%d err=%v", legacyTables, err)
			}

			replaceSchema(t, cooldownLegacyDDL, cooldownExpiryIndexDDL)
			if err := f.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
				t.Fatalf("migration was not retryable after repair: %v", err)
			}
			if err := verifyCooldownSchemaFromDB(f.app.store.db); err != nil {
				t.Fatal(err)
			}
		})
	}

	replaceSchema(t, cooldownLegacyDDL, cooldownExpiryIndexDDL)
	if _, err := f.app.store.db.Exec(`INSERT INTO account_pool_runtime_cooldowns(account_id,failure_class,cooldown_until,updated_at) VALUES(?,?,?,?)`,
		"ups_cooldown_migration", "unbounded_error_text", "2027-01-15T08:00:00Z", "2027-01-15T07:59:00Z"); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.migrateAccountPoolRuntime(context.Background()); err == nil {
		t.Fatal("migration accepted a legacy row with an invalid failure class")
	}
	var eventColumns, legacyTables int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('account_pool_runtime_cooldowns') WHERE name='event_id'`).Scan(&eventColumns); err != nil || eventColumns != 0 {
		t.Fatalf("failed migration was not rolled back: event columns=%d err=%v", eventColumns, err)
	}
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='account_pool_runtime_cooldowns_legacy'`).Scan(&legacyTables); err != nil || legacyTables != 0 {
		t.Fatalf("failed migration left a legacy table: count=%d err=%v", legacyTables, err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE account_pool_runtime_cooldowns SET failure_class=?`, string(scheduling.FailureTransient)); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.migrateAccountPoolRuntime(context.Background()); err != nil {
		t.Fatalf("retry legacy migration: %v", err)
	}
	var eventID, failure string
	if err := f.app.store.db.QueryRow(`SELECT event_id,failure_class FROM account_pool_runtime_cooldowns WHERE account_id='ups_cooldown_migration'`).Scan(&eventID, &failure); err != nil {
		t.Fatal(err)
	}
	if !validIdentifier(eventID, cooldownEventMaxLength) || failure != string(scheduling.FailureTransient) {
		t.Fatalf("migrated event=%q failure=%q", eventID, failure)
	}
}

func verifyCooldownSchemaFromDB(db *sql.DB) error {
	var columns int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('account_pool_runtime_cooldowns')`).Scan(&columns); err != nil {
		return err
	}
	if columns != 5 {
		return errors.New("repaired cooldown schema was not upgraded")
	}
	var indexSQL string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='index' AND name='account_pool_runtime_cooldown_expiry_idx'`).Scan(&indexSQL); err != nil {
		return err
	}
	if normalizeCooldownDDL(indexSQL) != normalizeCooldownDDL(cooldownExpiryIndexDDL) {
		return errors.New("repaired cooldown expiry index is not canonical")
	}
	return nil
}

func TestUpstreamCooldownViewClearCASCancellationAndRestart(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, 30*time.Second, 2)
	if err := f.base.app.accountPool.Close(); err != nil {
		t.Fatal(err)
	}
	f.base.app.accountPool = f.rt
	f.insertAccount(t, "ups_cooldown_admin", true)
	f.insertModelPool(t, "cooldown-admin-model", "ups_cooldown_admin", 1, modelAccountView{
		UpstreamID: "ups_cooldown_admin", UpstreamModel: "provider-model", Weight: 1, MaxConcurrency: 1,
	})

	lease := acquireRuntime(t, f.rt, "cooldown-admin-model", f.auth1, "")
	if ok, result := lease.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient}); !ok || result.Code != accountPoolReleased {
		t.Fatalf("release=%v %+v", ok, result)
	}
	eventID, failure, until := readCooldownState(t, f.base.app.store.db, "ups_cooldown_admin")
	if failure != scheduling.FailureTransient {
		t.Fatalf("failure=%s", failure)
	}
	if memory, ok := f.rt.scheduler.Cooldown("ups_cooldown_admin"); !ok || memory.EventID != eventID || memory.Failure != failure || !memory.Until.Equal(until) {
		t.Fatalf("scheduler cooldown=%+v ok=%v; db event=%q failure=%s until=%s", memory, ok, eventID, failure, until)
	}
	items := []upstreamView{{ID: "ups_cooldown_admin"}}
	if err := f.base.app.decorateUpstreamCooldowns(context.Background(), items, f.clock.Now()); err != nil {
		t.Fatal(err)
	}
	if items[0].Cooldown == nil || items[0].Cooldown.EventID != eventID || !items[0].Cooldown.Active {
		t.Fatalf("active cooldown view=%+v", items[0].Cooldown)
	}
	if err := f.base.app.decorateUpstreamCooldowns(context.Background(), items, until.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if items[0].Cooldown == nil || items[0].Cooldown.Active {
		t.Fatalf("expired cooldown view=%+v", items[0].Cooldown)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, code := f.rt.clearCooldown(cancelled, "ups_cooldown_admin", 1, eventID); code != cooldownStorageFailure {
		t.Fatalf("cancelled clear code=%s", code)
	}
	if got, _, _ := readCooldownState(t, f.base.app.store.db, "ups_cooldown_admin"); got != eventID {
		t.Fatalf("cancelled clear changed event %q -> %q", eventID, got)
	}

	waited := make(chan accountPoolAcquireResult, 1)
	go func() {
		waited <- f.rt.Acquire(context.Background(), "cooldown-admin-model", f.auth2, []string{"openai-compatible"}, "")
	}()
	assertRuntimeAcquireStillWaiting(t, waited)

	assertCooldownHTTPStatus(t, f, `{"expected_revision":2,"expected_cooldown_event_id":"`+eventID+`"}`, http.StatusConflict, "revision_conflict")
	assertCooldownHTTPStatus(t, f, `{"expected_revision":1,"expected_cooldown_event_id":"cool_stale"}`, http.StatusConflict, "cooldown_conflict")
	response := requestJSON(t, http.MethodPost, f.base.server.URL+"/admin/api/v1/upstreams/ups_cooldown_admin/cooldown/clear",
		`{"expected_revision":1,"expected_cooldown_event_id":"`+eventID+`"}`, f.base.cookie, f.base.csrf, f.base.server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("clear status=%d body=%s", response.StatusCode, readBody(response))
	}
	var cleared struct {
		Result     string `json:"result"`
		UpstreamID string `json:"upstream_id"`
		Revision   int64  `json:"revision"`
		ServerTime string `json:"server_time"`
	}
	decodeResponse(t, response, &cleared)
	if cleared.Result != "cleared" || cleared.UpstreamID != "ups_cooldown_admin" || cleared.Revision != 1 || cleared.ServerTime == "" {
		t.Fatalf("clear response=%+v", cleared)
	}
	select {
	case result := <-waited:
		if result.Code != accountPoolConfigurationChanged || result.Lease != nil {
			t.Fatalf("old cooldown waiter=%+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("clear did not terminate the old waiter")
	}
	if _, ok := f.rt.scheduler.Cooldown("ups_cooldown_admin"); ok {
		t.Fatal("cleared scheduler cooldown remains")
	}
	var stored int
	if err := f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id='ups_cooldown_admin'`).Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("cleared DB rows=%d err=%v", stored, err)
	}
	assertCooldownHTTPStatus(t, f, `{"expected_revision":1,"expected_cooldown_event_id":"`+eventID+`"}`, http.StatusOK, "")
	assertCooldownHTTPStatus(t, f, `{"expected_revision":1,"expected_revision":1,"expected_cooldown_event_id":"`+eventID+`"}`, http.StatusBadRequest, "invalid_request")

	lease = acquireRuntime(t, f.rt, "cooldown-admin-model", f.auth1, "")
	if ok, result := lease.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit}); !ok || result.Code != accountPoolReleased {
		t.Fatalf("release before restart=%v %+v", ok, result)
	}
	eventID, failure, until = readCooldownState(t, f.base.app.store.db, "ups_cooldown_admin")
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
	f.base.app.accountPool = restarted
	if memory, ok := restarted.scheduler.Cooldown("ups_cooldown_admin"); !ok || memory.EventID != eventID || memory.Failure != failure || !memory.Until.Equal(until) {
		t.Fatalf("restored scheduler cooldown=%+v ok=%v", memory, ok)
	}
	if result, revision, code := restarted.clearCooldown(context.Background(), "ups_cooldown_admin", 1, eventID); result != cooldownCleared || revision != 1 || code != cooldownCleared {
		t.Fatalf("clear after restart result=%s revision=%d code=%s", result, revision, code)
	}
}

type cooldownBarrierRandom struct {
	mu      sync.Mutex
	block   bool
	entered chan struct{}
	resume  chan struct{}
}

func (r *cooldownBarrierRandom) Intn(n int) int {
	r.mu.Lock()
	block, entered, resume := r.block, r.entered, r.resume
	if block {
		r.block = false
	}
	r.mu.Unlock()
	if block {
		close(entered)
		<-resume
	}
	return 0 % n
}

func TestCooldownReleaseFinalAdmissionBarrierAndConcurrentClear(t *testing.T) {
	random := &cooldownBarrierRandom{}
	f := newRuntimeFixture(t, random, 30*time.Second, 3)
	f.insertAccount(t, "ups_cooldown_race", true)
	f.insertModelPool(t, "cooldown-race-model", "ups_cooldown_race", 1, modelAccountView{
		UpstreamID: "ups_cooldown_race", UpstreamModel: "provider-model", Weight: 1, MaxConcurrency: 3,
	})
	first := acquireRuntime(t, f.rt, "cooldown-race-model", f.auth1, "")
	second := acquireRuntime(t, f.rt, "cooldown-race-model", f.auth2, "")
	random.mu.Lock()
	random.block = true
	random.entered = make(chan struct{})
	random.resume = make(chan struct{})
	entered, resume := random.entered, random.resume
	random.mu.Unlock()

	selected := make(chan accountPoolAcquireResult, 1)
	go func() {
		selected <- f.rt.Acquire(context.Background(), "cooldown-race-model", f.auth1, []string{"openai-compatible"}, "")
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("candidate selection did not reach the barrier")
	}
	released := make(chan accountPoolReleaseResult, 1)
	go func() {
		_, result := first.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureTransient})
		released <- result
	}()
	waitForRuntimeCondition(t, func() bool {
		var count int
		return f.base.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id='ups_cooldown_race'`).Scan(&count) == nil && count == 1
	})
	close(resume)
	if result := <-released; result.Code != accountPoolReleased {
		t.Fatalf("release result=%+v", result)
	}
	select {
	case result := <-selected:
		if result.Code != accountPoolConfigurationChanged || result.Lease != nil {
			t.Fatalf("old selected candidate crossed cooldown commit: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("old selected candidate did not terminate")
	}
	oldEvent, _, _ := readCooldownState(t, f.base.app.store.db, "ups_cooldown_race")
	f.clock.Advance(time.Second)

	type clearResult struct {
		result cooldownClearCode
		code   cooldownClearCode
	}
	clearDone := make(chan clearResult, 1)
	releaseDone := make(chan accountPoolReleaseResult, 1)
	go func() {
		result, _, code := f.rt.clearCooldown(context.Background(), "ups_cooldown_race", 1, oldEvent)
		clearDone <- clearResult{result: result, code: code}
	}()
	go func() {
		_, result := second.Lease.Release(context.Background(), scheduling.ReleaseResult{Failure: scheduling.FailureRateLimit})
		releaseDone <- result
	}()
	if result := <-releaseDone; result.Code != accountPoolReleased {
		t.Fatalf("concurrent release=%+v", result)
	}
	clear := <-clearDone
	if clear.code != cooldownCleared && clear.code != cooldownEventConflict {
		t.Fatalf("concurrent clear=%+v", clear)
	}
	newEvent, failure, until := readCooldownState(t, f.base.app.store.db, "ups_cooldown_race")
	if newEvent == oldEvent || failure != scheduling.FailureRateLimit {
		t.Fatalf("new release was lost: old=%q new=%q failure=%s", oldEvent, newEvent, failure)
	}
	if memory, ok := f.rt.scheduler.Cooldown("ups_cooldown_race"); !ok || memory.EventID != newEvent || memory.Failure != failure || !memory.Until.Equal(until) {
		t.Fatalf("concurrent DB/scheduler mismatch memory=%+v ok=%v db=%q/%s/%s", memory, ok, newEvent, failure, until)
	}
}

func readCooldownState(t *testing.T, db *sql.DB, accountID string) (string, scheduling.FailureClass, time.Time) {
	t.Helper()
	var eventID, failure, untilText string
	if err := db.QueryRow(`SELECT event_id,failure_class,cooldown_until FROM account_pool_runtime_cooldowns WHERE account_id=?`, accountID).Scan(&eventID, &failure, &untilText); err != nil {
		t.Fatal(err)
	}
	until, err := parseTime(untilText)
	if err != nil {
		t.Fatal(err)
	}
	return eventID, scheduling.FailureClass(failure), until
}

func assertCooldownHTTPStatus(t *testing.T, f *runtimeFixture, body string, wantStatus int, wantError string) {
	t.Helper()
	response := requestJSON(t, http.MethodPost, f.base.server.URL+"/admin/api/v1/upstreams/ups_cooldown_admin/cooldown/clear", body, f.base.cookie, f.base.csrf, f.base.server.URL)
	if response.StatusCode != wantStatus {
		t.Fatalf("cooldown request status=%d want=%d body=%s", response.StatusCode, wantStatus, readBody(response))
	}
	if wantError == "" {
		response.Body.Close()
		return
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if payload.Error.Code != wantError {
		t.Fatalf("cooldown error=%q want=%q", payload.Error.Code, wantError)
	}
}
