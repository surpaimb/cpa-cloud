package service

// Synthetic pending records exercise startup transactions without an upstream.
import (
	"context"
	"fmt"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

func seedJointRecovery(t *testing.T) (*App, time.Time) {
	t.Helper()
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
	a := f.base.app
	ctx := context.Background()
	at := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Millisecond)
	tx, err := a.store.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	settings, err := a.governance.core.SetEnabledTx(ctx, tx, governance.SettingsUpdate{ExpectedRevision: 1, Enabled: true, UpdatedAt: at})
	if err != nil {
		t.Fatal(err)
	}
	rpm := int64(100)
	_, decision, err := a.governance.core.AdmitTx(ctx, tx, governance.AdmissionStart{
		RequestID: "joint-recovery", Subject: governance.Subject{EmployeeID: f.auth1.EmployeeID, KeyID: f.auth1.KeyID, PublicModel: "synthetic-model", Protocol: accounting.ProtocolOpenAIChatCompletions}, SettingsRevision: settings.Revision, SnapshotComplete: true,
		Scopes: []governance.ScopeSnapshot{{Kind: governance.ScopeEmployee, ID: f.auth1.EmployeeID, PolicyID: "synthetic-policy", PolicyRevision: 1, RPMLimit: &rpm}}, StartedAt: at, ObservedAt: at,
	})
	if err != nil || !decision.Allowed {
		t.Fatalf("admit: %v %v", decision, err)
	}
	if err := a.usage.ledger.BeginRequestTx(ctx, tx, accounting.RequestStart{ID: "joint-recovery", EmployeeID: f.auth1.EmployeeID, KeyID: f.auth1.KeyID, ModelID: "synthetic-model", Provider: accounting.ProviderOpenAICompatible, StartedAt: at}); err != nil {
		t.Fatal(err)
	}
	if err := a.usage.ledger.BeginAttemptTx(ctx, tx, accounting.AttemptStart{ID: "joint-recovery:1", RequestID: "joint-recovery", AccountID: "synthetic-account", Provider: accounting.ProviderOpenAICompatible, Dispatch: accounting.DispatchPrimary, StartedAt: at.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,outcome) VALUES('joint-recovery',?,?,'synthetic-model',?,'running')`, f.auth1.EmployeeID, f.auth1.KeyID, at.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	return a, at.Add(time.Second)
}

func assertJointRecoveryStatuses(t *testing.T, a *App, status string) {
	t.Helper()
	for _, table := range []string{"accounting_requests", "accounting_attempts", "governance_requests", "model_requests"} {
		column, expected := "status", status
		if table == "model_requests" {
			column = "outcome"
			if status == "pending" {
				expected = "running"
			}
		}
		var actual string
		if err := a.store.db.QueryRow(`SELECT ` + column + ` FROM ` + table + ` LIMIT 1`).Scan(&actual); err != nil || actual != expected {
			t.Fatalf("%s: %s, %v; expected %s", table, actual, err, expected)
		}
	}
}

func TestRequestLedgerJointRecoveryRollbackRetryAndClockRollback(t *testing.T) {
	for _, table := range []string{"accounting_attempts", "accounting_requests", "governance_requests", "model_requests"} {
		t.Run(table, func(t *testing.T) {
			a, at := seedJointRecovery(t)
			if _, err := a.store.db.Exec(fmt.Sprintf(`CREATE TRIGGER reject_joint_recovery BEFORE UPDATE ON %s BEGIN SELECT RAISE(ABORT,'synthetic rejection'); END`, table)); err != nil {
				t.Fatal(err)
			}
			if err := a.recoverRequestLedgers(context.Background(), a.governance.core); err == nil {
				t.Fatal("accepted partial recovery")
			}
			assertJointRecoveryStatuses(t, a, "pending")
			if _, err := a.store.db.Exec(`DROP TRIGGER reject_joint_recovery`); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := a.recoverRequestLedgers(context.Background(), a.governance.core); err != nil {
					t.Fatal(err)
				}
				assertJointRecoveryStatuses(t, a, "interrupted")
			}
			var finished string
			var input *int64
			if err := a.store.db.QueryRow(`SELECT finished_at,input_tokens FROM accounting_attempts WHERE id='joint-recovery:1'`).Scan(&finished, &input); err != nil {
				t.Fatal(err)
			}
			parsed, err := time.Parse(time.RFC3339Nano, finished)
			if err != nil || !parsed.Equal(at) || input != nil {
				t.Fatalf("incorrect recovery time/usage: %s %v %v", finished, input, err)
			}
			var released *string
			if err := a.store.db.QueryRow(`SELECT released_at FROM governance_requests WHERE id='joint-recovery'`).Scan(&released); err != nil || released != nil {
				t.Fatal("recovery discarded original lease")
			}
		})
	}
}

func TestRequestLedgerRecoveryOwnedByAppStartup(t *testing.T) {
	a, _ := seedJointRecovery(t)
	// Opening schema for an initialization/check command cannot half-recover
	// model_requests before the accounting/governance transactions run.
	s, err := openStore(a.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.close(); err != nil {
		t.Fatal(err)
	}
	assertJointRecoveryStatuses(t, a, "pending")
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(context.Background(), a.cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	assertJointRecoveryStatuses(t, restarted, "interrupted")
}
