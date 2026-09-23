package service

// Independent request-lifecycle tests use synthetic database identities only.
import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

type testGovernanceResolver struct{ scopes []governance.ScopeSnapshot }

func (s *testGovernanceResolver) ResolveScopesTx(ctx context.Context, tx *sql.Tx, _, _ string) (governance.Settings, []governance.ScopeSnapshot, error) {
	var settings governance.Settings
	err := tx.QueryRowContext(ctx, `SELECT enabled,revision FROM governance_settings WHERE singleton=1`).Scan(&settings.Enabled, &settings.Revision)
	return settings, s.scopes, err
}

type requestGovernanceFixture struct {
	pool     *runtimeFixture
	g        *requestGovernance
	resolver *testGovernanceResolver
}

func newRequestGovernanceFixture(t *testing.T, rpm, concurrent int64, enabled bool, interval time.Duration) *requestGovernanceFixture {
	t.Helper()
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
	a := f.base.app
	a.accountPool.Close()
	a.accountPool = f.rt
	f.insertAccount(t, "governance-account", true)
	f.insertModelPool(t, "governance-model", "governance-account", 1, modelAccountView{UpstreamID: "governance-account", UpstreamModel: "actual", Priority: 1, Weight: 1, MaxConcurrency: 8})
	core, err := governance.New(a.store.db, governance.Config{LeaseTTL: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if err := core.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if enabled {
		tx, err := a.store.db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := core.SetEnabledTx(context.Background(), tx, governance.SettingsUpdate{ExpectedRevision: 1, Enabled: true, UpdatedAt: time.Now().UTC()}); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	resolver := &testGovernanceResolver{scopes: []governance.ScopeSnapshot{{Kind: governance.ScopeEmployee, ID: f.auth1.EmployeeID, PolicyID: "policy-test", PolicyRevision: 1, RPMLimit: &rpm, ConcurrencyLimit: &concurrent}}}
	g, err := newRequestGovernance(a, core, resolver)
	if err != nil {
		t.Fatal(err)
	}
	g.renewEvery = interval
	a.governance, a.usage.governance = g, core
	t.Cleanup(g.Close)
	return &requestGovernanceFixture{f, g, resolver}
}

func (f *requestGovernanceFixture) admit(t *testing.T, id string) (*http.Request, *governedRequest, *modelAdmissionError) {
	t.Helper()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, id))
	return f.g.Admit(r, f.pool.auth1, "governance-model", accounting.ProtocolOpenAIChatCompletions)
}

func assertGovernanceCount(t *testing.T, db *sql.DB, table string, want int) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != want {
		t.Fatalf("%s count=%d want=%d err=%v", table, count, want, err)
	}
}

func TestRequestGovernanceAdmissionBeforeAccountSelection(t *testing.T) {
	for _, kind := range []string{"concurrency", "rpm"} {
		t.Run(kind, func(t *testing.T) {
			rpm, concurrent := int64(100), int64(1)
			if kind == "rpm" {
				rpm, concurrent = 1, 100
			}
			f := newRequestGovernanceFixture(t, rpm, concurrent, true, time.Hour)
			_, first, failed := f.admit(t, "request-first")
			if failed != nil || first == nil {
				t.Fatalf("first=%+v", failed)
			}
			defer first.Close()
			if kind == "rpm" {
				first.Close()
			}
			_, second, failed := f.admit(t, "request-second")
			if second != nil || failed == nil || failed.status != 429 {
				t.Fatalf("limit=%+v", failed)
			}
			for table, want := range map[string]int{"governance_requests": 1, "accounting_requests": 0, "model_requests": 0, "account_pool_runtime_leases": 0, "accounting_attempts": 0} {
				assertGovernanceCount(t, f.g.app.store.db, table, want)
			}
			first.Close()
			var status string
			var released sql.NullString
			if err := f.g.app.store.db.QueryRow(`SELECT status,released_at FROM governance_requests WHERE id='request-first'`).Scan(&status, &released); err != nil || status != "failed" || !released.Valid {
				t.Fatalf("route failure cleanup %s/%v/%v", status, released, err)
			}
			assertGovernanceCount(t, f.g.app.store.db, "governance_request_scopes", 1)
			if kind == "concurrency" {
				_, third, failed := f.admit(t, "request-third")
				if failed != nil || third == nil {
					t.Fatalf("released concurrency=%+v", failed)
				}
				third.Close()
			}
		})
	}
}

func TestRequestGovernanceDisabledNoPolicyAndRevokedKey(t *testing.T) {
	for _, mode := range []string{"disabled", "no-policy", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f := newRequestGovernanceFixture(t, 1, 1, mode != "disabled", time.Hour)
			if mode == "no-policy" {
				f.resolver.scopes = nil
			}
			if mode == "revoked" {
				if _, err := f.g.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), f.pool.auth1.KeyID); err != nil {
					t.Fatal(err)
				}
			}
			_, guard, failed := f.admit(t, "unreserved")
			if guard != nil {
				guard.Close()
				t.Fatal("unexpected reservation")
			}
			if (mode == "revoked") != (failed != nil) {
				t.Fatalf("mode=%s failure=%+v", mode, failed)
			}
			assertGovernanceCount(t, f.g.app.store.db, "governance_requests", 0)
		})
	}
}

func TestRequestGovernanceAtomicTerminalRollbackAndRetry(t *testing.T) {
	f := newRequestGovernanceFixture(t, 100, 1, true, time.Hour)
	a := f.g.app
	r, guard, failed := f.admit(t, "terminal-atomic")
	if failed != nil || guard == nil {
		t.Fatalf("admit=%+v", failed)
	}
	defer guard.Close()
	selected, lease, failed := a.selectModelRoute(r, f.pool.auth1, "governance-model", []string{"openai-compatible"}, true)
	if failed != nil || lease == nil {
		t.Fatalf("route=%+v", failed)
	}
	defer a.releaseModelLease(lease, "terminal-atomic", true)
	if err := a.beginRouteUpstreamUsage(r.Context(), "terminal-atomic", selected); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`CREATE TRIGGER reject_governance_finish BEFORE UPDATE OF status ON governance_requests BEGIN SELECT RAISE(ABORT,'synthetic terminal failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.finishRequestChecked("terminal-atomic", "succeeded", 200); err == nil {
		t.Fatal("success despite failed governance persistence")
	}
	for _, table := range []string{"accounting_requests", "accounting_attempts", "governance_requests"} {
		var status string
		if err := a.store.db.QueryRow(`SELECT status FROM ` + table).Scan(&status); err != nil || status != "pending" {
			t.Fatalf("rollback %s=%s %v", table, status, err)
		}
	}
	var legacy string
	if err := a.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id='terminal-atomic'`).Scan(&legacy); err != nil || legacy != "running" {
		t.Fatalf("legacy rollback %s %v", legacy, err)
	}
	value, _ := a.usageRequests.Load("terminal-atomic")
	frozen := value.(*activeUsageRequest).endedAt
	if _, err := a.store.db.Exec(`DROP TRIGGER reject_governance_finish`); err != nil {
		t.Fatal(err)
	}
	if err := a.finishRequestChecked("terminal-atomic", "succeeded", 200); err != nil {
		t.Fatal(err)
	}
	var observed, accountingEnd, legacyEnd string
	if err := a.store.db.QueryRow(`SELECT g.observed_finished_at,a.finished_at,m.finished_at FROM governance_requests g JOIN accounting_requests a ON a.id=g.id JOIN model_requests m ON m.id=g.id WHERE g.id='terminal-atomic'`).Scan(&observed, &accountingEnd, &legacyEnd); err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{observed, accountingEnd, legacyEnd} {
		parsed, err := time.Parse(time.RFC3339Nano, stamp)
		if err != nil || !parsed.Equal(frozen) {
			t.Fatalf("terminal retry changed snapshot %v", err)
		}
	}
	_, next, failed := f.admit(t, "after-success")
	if failed != nil || next == nil {
		t.Fatalf("concurrency retained after success: %+v", failed)
	}
	next.Close()
}

func TestRequestGovernanceRenewFailureAndTerminalRace(t *testing.T) {
	for _, mode := range []string{"renew", "failure", "terminal", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f := newRequestGovernanceFixture(t, 100, 4, true, 10*time.Millisecond)
			r, guard, failed := f.admit(t, "renew-"+mode)
			if failed != nil || guard == nil {
				t.Fatalf("admit=%+v", failed)
			}
			defer guard.Close()
			var before string
			if err := f.g.app.store.db.QueryRow(`SELECT expires_at FROM governance_requests WHERE id=?`, guard.id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "failure":
				if _, err := f.g.app.store.db.Exec(`CREATE TRIGGER reject_renew BEFORE UPDATE OF expires_at ON governance_requests BEGIN SELECT RAISE(ABORT,'synthetic renewal failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "terminal":
				tx, err := f.g.app.store.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				if err := f.g.core.FinishTx(context.Background(), tx, governance.Finish{RequestID: guard.id, Status: accounting.StatusSucceeded, FinishedAt: time.Now().UTC()}); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					t.Fatal(err)
				}
			case "shutdown":
				f.g.Close()
			}
			if mode == "renew" {
				deadline := time.Now().Add(2 * time.Second)
				for {
					var after string
					if err := f.g.app.store.db.QueryRow(`SELECT expires_at FROM governance_requests WHERE id=?`, guard.id).Scan(&after); err != nil {
						t.Fatal(err)
					}
					if after != before {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("lease did not renew")
					}
					time.Sleep(5 * time.Millisecond)
				}
				if r.Context().Err() != nil {
					t.Fatal("healthy renewal cancelled request")
				}
			} else {
				select {
				case <-guard.done:
				case <-time.After(2 * time.Second):
					t.Fatal("worker did not end")
				}
				if (mode != "terminal") != (r.Context().Err() != nil) {
					t.Fatal(fmt.Sprintf("unexpected %s context state %v", mode, r.Context().Err()))
				}
			}
		})
	}
}

func TestRequestGovernanceFailedCleanupAndRestartRetainsOriginalLease(t *testing.T) {
	f := newRequestGovernanceFixture(t, 100, 1, true, time.Hour)
	a := f.g.app
	_, guard, failed := f.admit(t, "restart-before-route")
	if failed != nil || guard == nil {
		t.Fatalf("admit=%+v", failed)
	}
	var originalExpiry string
	if err := a.store.db.QueryRow(`SELECT expires_at FROM governance_requests WHERE id=?`, guard.id).Scan(&originalExpiry); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.db.Exec(`CREATE TRIGGER reject_early_finish BEFORE UPDATE OF status ON governance_requests BEGIN SELECT RAISE(ABORT,'synthetic finish failure'); END`); err != nil {
		t.Fatal(err)
	}
	guard.Close()
	var status string
	var released sql.NullString
	if err := a.store.db.QueryRow(`SELECT status,released_at FROM governance_requests WHERE id=?`, guard.id).Scan(&status, &released); err != nil || status != "pending" || released.Valid {
		t.Fatalf("failed cleanup released lease %s/%v/%v", status, released, err)
	}
	f.g.Close()
	if _, err := a.store.db.Exec(`DROP TRIGGER reject_early_finish`); err != nil {
		t.Fatal(err)
	}
	restarted, err := newRequestGovernance(a, f.g.core, f.resolver)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	var afterExpiry string
	if err := a.store.db.QueryRow(`SELECT status,expires_at,released_at FROM governance_requests WHERE id=?`, guard.id).Scan(&status, &afterExpiry, &released); err != nil || status != "interrupted" || afterExpiry != originalExpiry || released.Valid {
		t.Fatalf("restart occupancy %s/%s/%v/%v", status, afterExpiry, released, err)
	}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, "restart-new-request"))
	_, next, failed := restarted.Admit(r, f.pool.auth1, "governance-model", accounting.ProtocolOpenAIChatCompletions)
	if next != nil || failed == nil || failed.status != 429 {
		t.Fatalf("restart lost occupied capacity: %+v", failed)
	}
	for _, table := range []string{"accounting_requests", "accounting_attempts", "model_requests"} {
		assertGovernanceCount(t, a.store.db, table, 0)
	}
}
