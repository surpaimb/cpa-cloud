package governance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	_ "modernc.org/sqlite"
)

var governanceStart = time.Date(2026, time.September, 23, 12, 0, 0, 123, time.UTC)

func TestMigrateDefaultsStrictRollbackAndRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "governance.db")
	db := openGovernanceDB(t, path, 1)
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE governance_settings(
		singleton INTEGER PRIMARY KEY,enabled INTEGER NOT NULL,revision INTEGER NOT NULL,
		last_effective_admission_at TEXT,updated_at TEXT NOT NULL,unexpected TEXT)`); err != nil {
		t.Fatal(err)
	}
	coordinator := newTestCoordinator(t, db)
	if err := coordinator.Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("malformed migration error=%v", err)
	}
	assertObjectCount(t, db, "governance_requests", 0)
	if _, err := db.Exec(`DROP TABLE governance_settings`); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Migrate(context.Background()); err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	settings, err := coordinator.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.Enabled || settings.Revision != 1 || settings.LastEffectiveAdmissionTime != nil || !settings.UpdatedAt.Equal(time.Unix(0, 0).UTC()) {
		t.Fatalf("default settings=%+v", settings)
	}
	if _, err := db.Exec(`CREATE UNIQUE INDEX governance_unexpected_unique ON governance_requests(employee_id)`); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Migrate(context.Background()); !errors.Is(err, ErrSchema) {
		t.Fatalf("extra unique index error=%v", err)
	}
	if _, err := db.Exec(`DROP INDEX governance_unexpected_unique`); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Migrate(context.Background()); err != nil {
		t.Fatalf("retry after index repair: %v", err)
	}
}

func TestSettingsDisabledCompleteSnapshotAndCAS(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "disabled.db"), 1)
	defer db.Close()
	input := testAdmission("disabled", governanceStart, employeeScope(1, 1, 1))
	input.SnapshotComplete = false
	if _, _, err := runAdmit(t, db, coordinator, input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("incomplete snapshot error=%v", err)
	}
	input.SnapshotComplete = true
	lease, decision, err := runAdmit(t, db, coordinator, input)
	if err != nil || !decision.Allowed || decision.Code != DecisionAllowed || lease != nil {
		t.Fatalf("disabled admission lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	assertRequestCount(t, db, 0)
	settings := setEnabled(t, db, coordinator, 1, true, governanceStart)
	if !settings.Enabled || settings.Revision != 2 {
		t.Fatalf("enabled settings=%+v", settings)
	}
	empty := testAdmission("no-policy", governanceStart)
	empty.SettingsRevision = 2
	lease, decision, err = runAdmit(t, db, coordinator, empty)
	if err != nil || !decision.Allowed || lease != nil {
		t.Fatalf("empty policy lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	stale := testAdmission("stale", governanceStart, employeeScope(1, 1, 1))
	lease, decision, err = runAdmit(t, db, coordinator, stale)
	if err != nil || decision.Code != DecisionPolicyChanged || decision.Allowed || lease != nil {
		t.Fatalf("stale admission lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.SetEnabledTx(context.Background(), tx, SettingsUpdate{ExpectedRevision: 1, Enabled: false, UpdatedAt: governanceStart}); !errors.Is(err, ErrConflict) {
		tx.Rollback()
		t.Fatalf("stale settings CAS error=%v", err)
	}
	tx.Rollback()
	assertRequestCount(t, db, 0)
}

func TestInvalidScopeSnapshotFailsClosed(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "invalid.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	invalid := []ScopeSnapshot{
		{Kind: ScopeEmployee, ID: "employee-1", PolicyID: "", PolicyRevision: 1, RPMLimit: int64Pointer(1)},
		{Kind: ScopeEmployee, ID: "employee-1", PolicyID: "policy", PolicyRevision: 1},
		{Kind: ScopeEmployee, ID: "other-employee", PolicyID: "policy", PolicyRevision: 1, ConcurrencyLimit: int64Pointer(1)},
		{Kind: ScopeKey, ID: "key-1", PolicyID: "policy", PolicyRevision: 0, RPMLimit: int64Pointer(1)},
		{Kind: ScopeGroup, ID: "group-1", PolicyID: "policy", PolicyRevision: 1, RPMLimit: int64Pointer(-1)},
	}
	for index, scope := range invalid {
		input := testAdmission("invalid-"+string(rune('a'+index)), governanceStart, scope)
		input.SettingsRevision = 2
		if _, _, err := runAdmit(t, db, coordinator, input); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid scope %d error=%v", index, err)
		}
	}
	assertRequestCount(t, db, 0)
}

func TestRPMWindowPersistsAcrossRevisionClockRollbackAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rpm.db")
	db, coordinator := openMigratedCoordinator(t, path, 1)
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	future := governanceStart.Add(2 * time.Minute)
	first := testAdmission("rpm-first", future, employeeScope(1, 1, 0))
	first.SettingsRevision = 2
	lease, decision, err := runAdmit(t, db, coordinator, first)
	if err != nil || !decision.Allowed || lease == nil || !lease.EffectiveStartedAt.Equal(future) {
		t.Fatalf("first lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	if err := runFinish(t, db, coordinator, Finish{RequestID: first.RequestID, Status: accounting.StatusSucceeded, FinishedAt: governanceStart}); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, coordinator = openMigratedCoordinator(t, path, 1)
	defer db.Close()
	second := testAdmission("rpm-second", governanceStart, employeeScope(2, 1, 0))
	second.SettingsRevision = 2
	lease, decision, err = runAdmit(t, db, coordinator, second)
	if err != nil || lease != nil || decision.Code != DecisionRPMExceeded || decision.RetryAt == nil || !decision.RetryAt.Equal(future.Add(time.Minute)) {
		t.Fatalf("rollback lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	atBoundary := testAdmission("rpm-boundary", future.Add(time.Minute), employeeScope(3, 1, 0))
	atBoundary.SettingsRevision = 2
	lease, decision, err = runAdmit(t, db, coordinator, atBoundary)
	if err != nil || !decision.Allowed || lease == nil || !lease.EffectiveStartedAt.Equal(future.Add(time.Minute)) {
		t.Fatalf("boundary lease=%+v decision=%+v err=%v", lease, decision, err)
	}
}

func TestDisableReenableDoesNotResetGovernedScopeHistory(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "toggle.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	rpmFirst := testAdmission("toggle-rpm-first", governanceStart, employeeScope(1, 1, 0))
	rpmFirst.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, rpmFirst); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("RPM first lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	if err := runFinish(t, db, coordinator, Finish{RequestID: rpmFirst.RequestID, Status: accounting.StatusSucceeded, FinishedAt: governanceStart.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	active := testAdmission("toggle-active", governanceStart.Add(2*time.Second), keyScope(1, 0, 1))
	active.SettingsRevision = 2
	activeLease, decision, err := runAdmit(t, db, coordinator, active)
	if err != nil || activeLease == nil || !decision.Allowed {
		t.Fatalf("active lease=%+v decision=%+v err=%v", activeLease, decision, err)
	}
	setEnabled(t, db, coordinator, 2, false, governanceStart.Add(3*time.Second))
	bypass := testAdmission("toggle-bypass", governanceStart.Add(4*time.Second), employeeScope(2, 1, 0))
	bypass.SettingsRevision = 3
	if lease, decision, err := runAdmit(t, db, coordinator, bypass); err != nil || lease != nil || !decision.Allowed {
		t.Fatalf("bypass lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	assertRequestMissing(t, db, bypass.RequestID)
	replayed, decision, err := runAdmit(t, db, coordinator, active)
	if err != nil || replayed == nil || replayed.RequestID != activeLease.RequestID || !decision.Allowed {
		t.Fatalf("disabled replay lease=%+v decision=%+v err=%v", replayed, decision, err)
	}
	changed := active
	changed.Scopes = append([]ScopeSnapshot(nil), active.Scopes...)
	changed.Scopes[0].PolicyRevision++
	if _, _, err := runAdmit(t, db, coordinator, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("disabled changed replay error=%v", err)
	}
	setEnabled(t, db, coordinator, 3, true, governanceStart.Add(5*time.Second))
	rpmAfter := testAdmission("toggle-rpm-after", governanceStart.Add(6*time.Second), employeeScope(9, 1, 0))
	rpmAfter.SettingsRevision = 4
	if lease, decision, err := runAdmit(t, db, coordinator, rpmAfter); err != nil || lease != nil || decision.Code != DecisionRPMExceeded {
		t.Fatalf("RPM after toggle lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	concurrencyAfter := testAdmission("toggle-concurrency-after", governanceStart.Add(6*time.Second), keyScope(9, 0, 1))
	concurrencyAfter.SettingsRevision = 4
	if lease, decision, err := runAdmit(t, db, coordinator, concurrencyAfter); err != nil || lease != nil || decision.Code != DecisionConcurrencyExceeded {
		t.Fatalf("concurrency after toggle lease=%+v decision=%+v err=%v", lease, decision, err)
	}
}

func TestAllScopesAtomicCanonicalAndRequestIdempotency(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "scopes.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	group := scope(ScopeGroup, "group-1", "policy-group-v1", 1, 1, 0)
	prefill := testAdmission("group-prefill", governanceStart, group)
	prefill.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, prefill); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("prefill lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	if err := runFinish(t, db, coordinator, Finish{RequestID: prefill.RequestID, Status: accounting.StatusFailed, FinishedAt: governanceStart.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	employee := employeeScope(1, 10, 2)
	groupV2 := scope(ScopeGroup, "group-1", "policy-group-v2", 2, 1, 2)
	blocked := testAdmission("multi-blocked", governanceStart.Add(2*time.Second), groupV2, employee)
	blocked.SettingsRevision = 2
	lease, decision, err := runAdmit(t, db, coordinator, blocked)
	if err != nil || lease != nil || decision.Code != DecisionRPMExceeded {
		t.Fatalf("blocked lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	assertRequestMissing(t, db, blocked.RequestID)
	var employeeEvents int
	if err := db.QueryRow(`SELECT COUNT(*) FROM governance_request_scopes WHERE scope_kind='employee' AND scope_id='employee-1'`).Scan(&employeeEvents); err != nil || employeeEvents != 0 {
		t.Fatalf("partial employee events=%d err=%v", employeeEvents, err)
	}

	canonical := testAdmission("canonical", governanceStart.Add(time.Minute), keyScope(1, 5, 2), employeeScope(3, 5, 2))
	canonical.SettingsRevision = 2
	lease, decision, err = runAdmit(t, db, coordinator, canonical)
	if err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("canonical lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	reordered := canonical
	reordered.Scopes = []ScopeSnapshot{canonical.Scopes[1], canonical.Scopes[0]}
	replayed, decision, err := runAdmit(t, db, coordinator, reordered)
	if err != nil || replayed == nil || replayed.RequestID != lease.RequestID || !decision.Allowed {
		t.Fatalf("replay lease=%+v decision=%+v err=%v", replayed, decision, err)
	}
	changed := reordered
	changed.Scopes = append([]ScopeSnapshot(nil), reordered.Scopes...)
	changed.Scopes[0].PolicyRevision++
	if _, _, err := runAdmit(t, db, coordinator, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed snapshot error=%v", err)
	}
	duplicate := testAdmission("duplicate", governanceStart, employeeScope(1, 1, 1), employeeScope(2, 2, 2))
	duplicate.SettingsRevision = 2
	if _, _, err := runAdmit(t, db, coordinator, duplicate); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate scope error=%v", err)
	}
	if err := runFinish(t, db, coordinator, Finish{RequestID: canonical.RequestID, Status: accounting.StatusSucceeded, FinishedAt: governanceStart.Add(61 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	terminal, decision, err := runAdmit(t, db, coordinator, reordered)
	if err != nil || terminal == nil || decision.Code != DecisionAlreadyTerminal || decision.Allowed {
		t.Fatalf("terminal replay lease=%+v decision=%+v err=%v", terminal, decision, err)
	}
}

func TestConcurrentAdmissionAndRelease(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "concurrent.db"), 2)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	start := make(chan struct{})
	type result struct {
		id       string
		lease    *Lease
		decision Decision
		err      error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		id := "concurrent-" + string(rune('a'+index))
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			input := testAdmission(id, governanceStart, employeeScope(1, 0, 1))
			input.SettingsRevision = 2
			lease, decision, err := runAdmitNoTest(db, coordinator, input)
			results <- result{id: id, lease: lease, decision: decision, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	allowed := ""
	denied := 0
	for item := range results {
		if item.err != nil {
			t.Fatalf("concurrent %s error=%v", item.id, item.err)
		}
		if item.decision.Allowed {
			allowed = item.id
		} else if item.decision.Code == DecisionConcurrencyExceeded {
			denied++
		}
	}
	if allowed == "" || denied != 1 {
		t.Fatalf("allowed=%q denied=%d", allowed, denied)
	}
	if err := runFinish(t, db, coordinator, Finish{RequestID: allowed, Status: accounting.StatusCancelled, FinishedAt: governanceStart.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	next := testAdmission("concurrent-next", governanceStart.Add(2*time.Second), employeeScope(2, 0, 1))
	next.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, next); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("post-release lease=%+v decision=%+v err=%v", lease, decision, err)
	}
}

func TestFinishObservedSnapshotToggleAndWriteFailure(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "finish.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	first := testAdmission("finish-first", governanceStart.Add(time.Minute), employeeScope(1, 0, 3))
	first.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, first); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("first lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	originalFinish := Finish{RequestID: first.RequestID, Status: accounting.StatusSucceeded, FinishedAt: governanceStart}
	if err := runFinish(t, db, coordinator, originalFinish); err != nil {
		t.Fatal(err)
	}
	second := testAdmission("finish-second", governanceStart.Add(2*time.Minute), employeeScope(2, 0, 3))
	second.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, second); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("second lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	setEnabled(t, db, coordinator, 2, false, governanceStart.Add(3*time.Minute))
	if err := runFinish(t, db, coordinator, originalFinish); err != nil {
		t.Fatalf("idempotent finish after clock/settings change: %v", err)
	}
	var observed, effective string
	if err := db.QueryRow(`SELECT observed_finished_at,effective_finished_at FROM governance_requests WHERE id=?`, first.RequestID).Scan(&observed, &effective); err != nil {
		t.Fatal(err)
	}
	if observed != formatTime(governanceStart) || effective != formatTime(governanceStart.Add(time.Minute)) {
		t.Fatalf("finish observed=%s effective=%s", observed, effective)
	}
	changed := originalFinish
	changed.FinishedAt = changed.FinishedAt.Add(time.Second)
	if err := runFinish(t, db, coordinator, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed finish error=%v", err)
	}
	if _, err := db.Exec(`CREATE TRIGGER governance_fail_finish BEFORE UPDATE ON governance_requests
		WHEN OLD.id='finish-second' BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
		t.Fatal(err)
	}
	failedFinish := Finish{RequestID: second.RequestID, Status: accounting.StatusFailed, FinishedAt: governanceStart.Add(3 * time.Minute)}
	if err := runFinish(t, db, coordinator, failedFinish); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write failure error=%v", err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM governance_requests WHERE id=?`, second.RequestID).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("failed finish status=%q err=%v", status, err)
	}
	if _, err := db.Exec(`DROP TRIGGER governance_fail_finish`); err != nil {
		t.Fatal(err)
	}
	if err := runFinish(t, db, coordinator, failedFinish); err != nil {
		t.Fatalf("finish retry: %v", err)
	}
}

func TestRecoverInterruptedKeepsOriginalTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recover.db")
	db, coordinator := openMigratedCoordinator(t, path, 1)
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	first := testAdmission("recover-first", governanceStart, employeeScope(1, 0, 1))
	first.SettingsRevision = 2
	lease, decision, err := runAdmit(t, db, coordinator, first)
	if err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("first lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	originalExpiry := lease.ExpiresAt
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, coordinator = openMigratedCoordinator(t, path, 1)
	defer db.Close()
	recovered, err := coordinator.RecoverInterrupted(context.Background(), governanceStart.Add(30*time.Second))
	if err != nil || recovered.Interrupted != 1 {
		t.Fatalf("recover=%+v err=%v", recovered, err)
	}
	var status, expires string
	var released sql.NullString
	if err := db.QueryRow(`SELECT status,expires_at,released_at FROM governance_requests WHERE id=?`, first.RequestID).Scan(&status, &expires, &released); err != nil {
		t.Fatal(err)
	}
	if status != "interrupted" || expires != formatTime(originalExpiry) || released.Valid {
		t.Fatalf("status=%s expires=%s released=%+v", status, expires, released)
	}
	blocked := testAdmission("recover-blocked", governanceStart.Add(time.Minute), employeeScope(2, 0, 1))
	blocked.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, blocked); err != nil || lease != nil || decision.Code != DecisionConcurrencyExceeded {
		t.Fatalf("blocked lease=%+v decision=%+v err=%v", lease, decision, err)
	}
	afterTTL := testAdmission("recover-after-ttl", originalExpiry, employeeScope(3, 0, 1))
	afterTTL.SettingsRevision = 2
	if lease, decision, err := runAdmit(t, db, coordinator, afterTTL); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("after TTL lease=%+v decision=%+v err=%v", lease, decision, err)
	}
}

func TestAdmitWriteFailureRollsBackAndRetries(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "write.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	if _, err := db.Exec(`CREATE TRIGGER governance_fail_admit BEFORE INSERT ON governance_requests
		BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
		t.Fatal(err)
	}
	input := testAdmission("write-failure", governanceStart, employeeScope(1, 3, 3))
	input.SettingsRevision = 2
	if _, _, err := runAdmit(t, db, coordinator, input); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("write failure error=%v", err)
	}
	assertRequestMissing(t, db, input.RequestID)
	settings, err := coordinator.Settings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if settings.LastEffectiveAdmissionTime != nil {
		t.Fatalf("failed write advanced clock=%v", settings.LastEffectiveAdmissionTime)
	}
	if _, err := db.Exec(`DROP TRIGGER governance_fail_admit`); err != nil {
		t.Fatal(err)
	}
	if lease, decision, err := runAdmit(t, db, coordinator, input); err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("retry lease=%+v decision=%+v err=%v", lease, decision, err)
	}
}

func openMigratedCoordinator(t *testing.T, path string, maxConnections int) (*sql.DB, *Coordinator) {
	t.Helper()
	db := openGovernanceDB(t, path, maxConnections)
	coordinator := newTestCoordinator(t, db)
	if err := coordinator.Migrate(context.Background()); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db, coordinator
}

func openGovernanceDB(t *testing.T, path string, maxConnections int) *sql.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(maxConnections)
	db.SetMaxIdleConns(maxConnections)
	if err := db.Ping(); err != nil {
		db.Close()
		t.Fatal(err)
	}
	return db
}

func newTestCoordinator(t *testing.T, db *sql.DB) *Coordinator {
	t.Helper()
	coordinator, err := New(db, Config{LeaseTTL: 2 * time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func setEnabled(t *testing.T, db *sql.DB, coordinator *Coordinator, revision int64, enabled bool, at time.Time) Settings {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := coordinator.SetEnabledTx(context.Background(), tx, SettingsUpdate{ExpectedRevision: revision, Enabled: enabled, UpdatedAt: at})
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return settings
}

func runAdmit(t *testing.T, db *sql.DB, coordinator *Coordinator, input AdmissionStart) (*Lease, Decision, error) {
	t.Helper()
	return runAdmitNoTest(db, coordinator, input)
}

func runAdmitNoTest(db *sql.DB, coordinator *Coordinator, input AdmissionStart) (*Lease, Decision, error) {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return nil, Decision{}, err
	}
	lease, decision, err := coordinator.AdmitTx(context.Background(), tx, input)
	if err != nil {
		tx.Rollback()
		return nil, Decision{}, err
	}
	if err := tx.Commit(); err != nil {
		return nil, Decision{}, err
	}
	return lease, decision, nil
}

func runFinish(t *testing.T, db *sql.DB, coordinator *Coordinator, input Finish) error {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := coordinator.FinishTx(context.Background(), tx, input); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

func testAdmission(id string, at time.Time, scopes ...ScopeSnapshot) AdmissionStart {
	return AdmissionStart{
		RequestID:        id,
		Subject:          Subject{EmployeeID: "employee-1", KeyID: "key-1", PublicModel: "模型-1", Protocol: accounting.ProtocolOpenAIResponses},
		SettingsRevision: 1, SnapshotComplete: true, Scopes: scopes, StartedAt: at,
	}
}

func employeeScope(revision, rpm, concurrency int64) ScopeSnapshot {
	return scope(ScopeEmployee, "employee-1", "employee-policy", revision, rpm, concurrency)
}

func keyScope(revision, rpm, concurrency int64) ScopeSnapshot {
	return scope(ScopeKey, "key-1", "key-policy", revision, rpm, concurrency)
}

func scope(kind ScopeKind, id, policy string, revision, rpm, concurrency int64) ScopeSnapshot {
	item := ScopeSnapshot{Kind: kind, ID: id, PolicyID: policy, PolicyRevision: revision}
	if rpm > 0 {
		item.RPMLimit = int64Pointer(rpm)
	}
	if concurrency > 0 {
		item.ConcurrencyLimit = int64Pointer(concurrency)
	}
	return item
}

func int64Pointer(value int64) *int64 { return &value }

func assertObjectCount(t *testing.T, db *sql.DB, name string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("object %s count=%d want=%d", name, got, want)
	}
}

func assertRequestCount(t *testing.T, db *sql.DB, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(`SELECT COUNT(*) FROM governance_requests`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request count=%d want=%d", got, want)
	}
}

func assertRequestMissing(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE id=?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("request %q unexpectedly persisted", id)
	}
}
