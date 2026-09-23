package governance

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

// These tests are independently authored from the repository contracts and
// exercise only synthetic SQLite metadata.
func TestRecoverInterruptedTxCommitBoundaryAndIdempotency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery-commit.db")
	db, coordinator := openMigratedCoordinator(t, path, 2)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)

	seedRecoveryRequest(t, db, coordinator, "recover-pending", governanceStart)
	seedRecoveryRequest(t, db, coordinator, "recover-terminal", governanceStart.Add(time.Second))
	terminalAt := governanceStart.Add(2 * time.Second)
	if err := runFinish(t, db, coordinator, Finish{
		RequestID:  "recover-terminal",
		Status:     accounting.StatusFailed,
		FinishedAt: terminalAt,
	}); err != nil {
		t.Fatal(err)
	}

	observer := openGovernanceDB(t, path, 1)
	defer observer.Close()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.RecoverInterruptedTx(context.Background(), tx, governanceStart.Add(30*time.Second))
	if err != nil || result.Interrupted != 1 {
		t.Fatalf("recover result=%+v err=%v", result, err)
	}
	if got := requestStatus(t, tx, "recover-pending"); got != "interrupted" {
		t.Fatalf("transaction status=%q", got)
	}
	if got := requestStatus(t, observer, "recover-pending"); got != "pending" {
		t.Fatalf("uncommitted recovery visible as %q", got)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, observer, "recover-pending"); got != "interrupted" {
		t.Fatalf("committed status=%q", got)
	}
	var interrupted int64
	if err := observer.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE status='interrupted'`).Scan(&interrupted); err != nil {
		t.Fatal(err)
	}
	if interrupted != result.Interrupted {
		t.Fatalf("committed interrupted=%d result=%d", interrupted, result.Interrupted)
	}
	if got := requestStatus(t, observer, "recover-terminal"); got != "failed" {
		t.Fatalf("terminal request changed to %q", got)
	}

	replay, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err = coordinator.RecoverInterruptedTx(context.Background(), replay, governanceStart.Add(time.Minute))
	if err != nil || result.Interrupted != 0 {
		replay.Rollback()
		t.Fatalf("repeated recovery result=%+v err=%v", result, err)
	}
	if err := replay.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverInterruptedTxCallerRollbackAndSiblingFailure(t *testing.T) {
	for _, test := range []struct {
		name    string
		sibling bool
	}{
		{name: "caller rollback"},
		{name: "sibling failure", sibling: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "recovery-rollback.db"), 1)
			defer db.Close()
			setEnabled(t, db, coordinator, 1, true, governanceStart)
			seedRecoveryRequest(t, db, coordinator, "recover-rollback", governanceStart)

			tx, err := db.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			result, err := coordinator.RecoverInterruptedTx(context.Background(), tx, governanceStart.Add(30*time.Second))
			if err != nil || result.Interrupted != 1 {
				tx.Rollback()
				t.Fatalf("recover result=%+v err=%v", result, err)
			}
			if test.sibling {
				if _, err := tx.Exec(`INSERT INTO governance_missing_sibling(id) VALUES('synthetic')`); err == nil {
					tx.Rollback()
					t.Fatal("synthetic sibling write unexpectedly succeeded")
				}
			}
			if err := tx.Rollback(); err != nil {
				t.Fatal(err)
			}
			if got := requestStatus(t, db, "recover-rollback"); got != "pending" {
				t.Fatalf("rolled back recovery status=%q", got)
			}
		})
	}
}

func TestRecoverInterruptedTxLeaseAndClockSemantics(t *testing.T) {
	t.Run("unexpired lease stays reserved", func(t *testing.T) {
		db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "unexpired.db"), 1)
		defer db.Close()
		setEnabled(t, db, coordinator, 1, true, governanceStart)
		lease := seedRecoveryRequest(t, db, coordinator, "recover-unexpired", governanceStart)
		result := recoverAndCommit(t, db, coordinator, governanceStart.Add(30*time.Second))
		if result.Interrupted != 1 {
			t.Fatalf("result=%+v", result)
		}
		observed, effective, expires, released := recoveryTimes(t, db, "recover-unexpired")
		if observed != formatTime(governanceStart.Add(30*time.Second)) || effective != observed || expires != formatTime(lease.ExpiresAt) || released.Valid {
			t.Fatalf("observed=%s effective=%s expires=%s released=%+v", observed, effective, expires, released)
		}
	})

	t.Run("expired lease releases at original expiry", func(t *testing.T) {
		db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "expired.db"), 1)
		defer db.Close()
		setEnabled(t, db, coordinator, 1, true, governanceStart)
		lease := seedRecoveryRequest(t, db, coordinator, "recover-expired", governanceStart)
		recoverAt := governanceStart.Add(3 * time.Minute)
		result := recoverAndCommit(t, db, coordinator, recoverAt)
		if result.Interrupted != 1 {
			t.Fatalf("result=%+v", result)
		}
		observed, effective, expires, released := recoveryTimes(t, db, "recover-expired")
		if observed != formatTime(recoverAt) || effective != observed || expires != formatTime(lease.ExpiresAt) || !released.Valid || released.String != formatTime(lease.ExpiresAt) {
			t.Fatalf("observed=%s effective=%s expires=%s released=%+v", observed, effective, expires, released)
		}
	})

	t.Run("clock rollback uses persisted effective time", func(t *testing.T) {
		db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "rollback-clock.db"), 1)
		defer db.Close()
		setEnabled(t, db, coordinator, 1, true, governanceStart)
		future := governanceStart.Add(5 * time.Minute)
		lease := seedRecoveryRequest(t, db, coordinator, "recover-clock", future)
		observedRecovery := governanceStart.Add(time.Minute)
		result := recoverAndCommit(t, db, coordinator, observedRecovery)
		if result.Interrupted != 1 {
			t.Fatalf("result=%+v", result)
		}
		observed, effective, expires, released := recoveryTimes(t, db, "recover-clock")
		if observed != formatTime(observedRecovery) || effective != formatTime(future) || expires != formatTime(lease.ExpiresAt) || released.Valid {
			t.Fatalf("observed=%s effective=%s expires=%s released=%+v", observed, effective, expires, released)
		}
	})
}

func TestRecoverInterruptedTxValidationAndCancellation(t *testing.T) {
	db, coordinator := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "validation.db"), 1)
	defer db.Close()
	setEnabled(t, db, coordinator, 1, true, governanceStart)
	seedRecoveryRequest(t, db, coordinator, "recover-validation", governanceStart)

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	var nilCoordinator *Coordinator
	var nilContext context.Context
	for name, call := range map[string]func() error{
		"nil coordinator": func() error {
			_, err := nilCoordinator.RecoverInterruptedTx(context.Background(), tx, governanceStart)
			return err
		},
		"nil context": func() error {
			_, err := coordinator.RecoverInterruptedTx(nilContext, tx, governanceStart)
			return err
		},
		"nil transaction": func() error {
			_, err := coordinator.RecoverInterruptedTx(context.Background(), nil, governanceStart)
			return err
		},
		"zero time": func() error {
			_, err := coordinator.RecoverInterruptedTx(context.Background(), tx, time.Time{})
			return err
		},
		"non UTC time": func() error {
			_, err := coordinator.RecoverInterruptedTx(context.Background(), tx, governanceStart.In(time.FixedZone("offset", 3600)))
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := call(); !errors.Is(err, ErrInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RecoverInterruptedTx(cancelled, tx, governanceStart.Add(time.Second)); !errors.Is(err, ErrUnavailable) {
		tx.Rollback()
		t.Fatalf("cancelled error=%v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got := requestStatus(t, db, "recover-validation"); got != "pending" {
		t.Fatalf("cancelled recovery status=%q", got)
	}

	closed, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.RecoverInterruptedTx(context.Background(), closed, governanceStart.Add(time.Second)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed transaction error=%v", err)
	}
}

func seedRecoveryRequest(t *testing.T, db *sql.DB, coordinator *Coordinator, id string, at time.Time) *Lease {
	t.Helper()
	input := testAdmission(id, at, employeeScope(1, 0, 10))
	input.SettingsRevision = 2
	lease, decision, err := runAdmit(t, db, coordinator, input)
	if err != nil || lease == nil || !decision.Allowed {
		t.Fatalf("seed %q lease=%+v decision=%+v err=%v", id, lease, decision, err)
	}
	return lease
}

func recoverAndCommit(t *testing.T, db *sql.DB, coordinator *Coordinator, at time.Time) RecoveryResult {
	t.Helper()
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := coordinator.RecoverInterruptedTx(context.Background(), tx, at)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return result
}

type statusQuery interface {
	QueryRow(query string, args ...any) *sql.Row
}

func requestStatus(t *testing.T, query statusQuery, id string) string {
	t.Helper()
	var status string
	if err := query.QueryRow(`SELECT status FROM governance_requests WHERE id=?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func recoveryTimes(t *testing.T, db *sql.DB, id string) (string, string, string, sql.NullString) {
	t.Helper()
	var observed, effective, expires string
	var released sql.NullString
	if err := db.QueryRow(`SELECT observed_finished_at,effective_finished_at,expires_at,released_at
		FROM governance_requests WHERE id=?`, id).Scan(&observed, &effective, &expires, &released); err != nil {
		t.Fatal(err)
	}
	return observed, effective, expires, released
}
