package governance

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

// This verifies transaction composition, not App startup or a budget schema.
func TestJointAccountingGovernanceRecoveryTransaction(t *testing.T) {
	for _, accountingFirst := range []bool{true, false} {
		name := "governance-first"
		if accountingFirst {
			name = "accounting-first"
		}
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			db, core := openMigratedCoordinator(t, filepath.Join(t.TempDir(), "joint.db"), 1)
			defer db.Close()
			ledger := accounting.NewLedger(db)
			if err := ledger.Migrate(ctx); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
			settings := setEnabled(t, db, core, 1, true, at)
			input := testAdmission("joint-request", at, employeeScope(1, 20, 1))
			input.SettingsRevision = settings.Revision
			lease, decision, err := runAdmit(t, db, core, input)
			if err != nil || !decision.Allowed || lease == nil {
				t.Fatalf("admit=%+v err=%v", decision, err)
			}
			if err := ledger.BeginRequest(ctx, accounting.RequestStart{ID: input.RequestID, EmployeeID: input.Subject.EmployeeID, KeyID: input.Subject.KeyID, ModelID: input.Subject.PublicModel, Provider: accounting.ProviderOpenAICompatible, StartedAt: at}); err != nil {
				t.Fatal(err)
			}
			if err := ledger.BeginAttempt(ctx, accounting.AttemptStart{ID: "joint-attempt", RequestID: input.RequestID, AccountID: "synthetic-account", Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: at}); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE joint_recovery_receipt(id INTEGER CHECK(id=1))`); err != nil {
				t.Fatal(err)
			}
			for _, commit := range []bool{false, true} {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatal(err)
				}
				account := func() {
					result, err := ledger.RecoverInterruptedTx(ctx, tx, at.Add(time.Second))
					if err != nil || result.Requests != 1 || result.Attempts != 1 {
						t.Fatalf("accounting=%+v err=%v", result, err)
					}
				}
				govern := func() {
					result, err := core.RecoverInterruptedTx(ctx, tx, at.Add(time.Second))
					if err != nil || result.Interrupted != 1 {
						t.Fatalf("governance=%+v err=%v", result, err)
					}
				}
				if accountingFirst {
					account()
					govern()
				} else {
					govern()
					account()
				}
				if !commit {
					if _, err := tx.Exec(`INSERT INTO joint_recovery_receipt(id) VALUES(2)`); err == nil {
						t.Fatal("expected receipt failure")
					}
					if err := tx.Rollback(); err != nil {
						t.Fatal(err)
					}
					assertJointRecoveryState(t, db, "pending", lease.ExpiresAt)
					continue
				}
				if _, err := tx.Exec(`INSERT INTO joint_recovery_receipt(id) VALUES(1)`); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
				assertJointRecoveryState(t, db, "interrupted", lease.ExpiresAt)
			}
			var unknown int
			if err := db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts WHERE input_tokens IS NULL AND output_tokens IS NULL AND cache_read_tokens IS NULL AND cache_write_tokens IS NULL AND cost_micro IS NULL`).Scan(&unknown); err != nil || unknown != 1 {
				t.Fatalf("unknown count=%d err=%v", unknown, err)
			}
		})
	}
}

func assertJointRecoveryState(t *testing.T, db *sql.DB, status string, expiry time.Time) {
	t.Helper()
	for _, table := range []string{"accounting_requests", "accounting_attempts", "governance_requests"} {
		var actual string
		if err := db.QueryRow(`SELECT status FROM ` + table).Scan(&actual); err != nil || actual != status {
			t.Fatalf("%s status=%s want=%s err=%v", table, actual, status, err)
		}
	}
	var storedExpiry string
	var released sql.NullString
	if err := db.QueryRow(`SELECT expires_at,released_at FROM governance_requests`).Scan(&storedExpiry, &released); err != nil {
		t.Fatal(err)
	}
	if storedExpiry != formatTime(expiry) || released.Valid {
		t.Fatalf("unexpired capacity changed: %s %+v", storedExpiry, released)
	}
}
