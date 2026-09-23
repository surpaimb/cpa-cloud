package service

// Independent coordinator acceptance with synthetic metadata and no provider calls.
import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/scheduling"
)

func TestModelPreflightCoordinator(t *testing.T) {
	f := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 16)
	a := f.base.app
	if err := a.accountPool.Close(); err != nil {
		t.Fatal(err)
	}
	a.accountPool = f.rt
	for _, name := range []string{"success", "capped", "legacy", "request", "cancel", "revoke", "policy", "revision", "release_storage", "price_storage", "release_read_failure"} {
		t.Run(name, func(t *testing.T) {
			model, id := "preflight-"+name, "request-"+name
			auth := employeeAuth{EmployeeID: "employee-" + name, KeyID: "key-" + name, Mode: "selected"}
			f.insertEmployee(t, auth)
			first, second, third := "first-"+name, "second-"+name, "third-"+name
			for _, account := range []string{first, second, third} {
				f.insertAccount(t, account, true)
			}
			revision := int64(1)
			if name == "legacy" {
				revision = 0
			}
			f.insertModelPool(t, model, first, revision,
				modelAccountView{UpstreamID: first, UpstreamModel: "actual-first", Priority: 10, Weight: 1, MaxConcurrency: 1},
				modelAccountView{UpstreamID: second, UpstreamModel: "actual-second", Priority: 5, Weight: 1, MaxConcurrency: 1},
				modelAccountView{UpstreamID: third, UpstreamModel: "actual-third", Priority: 1, Weight: 1, MaxConcurrency: 1})
			if _, err := a.store.db.Exec(`INSERT INTO employee_models(employee_id,model_id) VALUES(?,?)`, auth.EmployeeID, model); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.WithValue(context.Background(), requestIDKey{}, id))
			defer cancel()
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")).WithContext(ctx)
			r.Header.Set("X-CPA-Session", "synthetic-session")
			var candidates []string
			var oldContext context.Context
			selected, lease, failed := a.prepareModelRoute(r, auth, model, []string{"openai-compatible"}, accounting.ProtocolOpenAIChatCompletions, true, func(candidate *http.Request, selected route) (route, *modelPreflightError) {
				candidates = append(candidates, selected.AccountID)
				if candidate.Context().Err() != nil {
					t.Fatal("prepared candidate has a cancelled lease")
				}
				if len(candidates) == 2 {
					if oldContext.Err() == nil {
						t.Fatal("second lease acquired without releasing the first")
					}
					if name == "capped" {
						return route{}, accountPreflightFailure(503, "no_available_route", "No available route.", scheduling.FailurePermanent)
					}
					return selected, nil
				}
				oldContext = candidate.Context()
				switch name {
				case "request":
					return route{}, requestPreflightFailure(400, "invalid_request_error", "Invalid request.")
				case "cancel":
					cancel()
				case "revoke":
					if _, err := a.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), auth.KeyID); err != nil {
						t.Fatal(err)
					}
				case "policy":
					if _, err := a.store.db.Exec(`DELETE FROM employee_models WHERE employee_id=? AND model_id=?`, auth.EmployeeID, model); err != nil {
						t.Fatal(err)
					}
				case "revision":
					if _, err := a.store.db.Exec(`UPDATE model_account_pool_configs SET revision=revision+1 WHERE model_id=?`, model); err != nil {
						t.Fatal(err)
					}
				case "release_storage":
					if _, err := a.store.db.Exec(`CREATE TRIGGER preflight_reject_release BEFORE DELETE ON account_pool_runtime_leases BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _, _ = a.store.db.Exec(`DROP TRIGGER preflight_reject_release`) })
				}
				return route{}, accountPreflightFailure(503, "no_available_route", "No available route.", scheduling.FailurePermanent)
			})
			assertCount := func(table string, attempts bool, wanted int) {
				t.Helper()
				column := "id"
				if attempts {
					column = "request_id"
				}
				var actual int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE `+column+`=?`, id).Scan(&actual); err != nil || actual != wanted {
					t.Fatalf("%s count=%d wanted=%d err=%v", table, actual, wanted, err)
				}
			}
			assertCount("model_requests", false, 1)
			assertCount("accounting_requests", false, 1)
			assertCount("accounting_attempts", true, 0)
			if name != "success" && name != "price_storage" && name != "release_read_failure" {
				if failed == nil || lease != nil {
					t.Fatal("failed preflight returned an executable lease")
				}
				wanted := 1
				if name == "capped" {
					wanted = 2
				}
				if len(candidates) != wanted {
					t.Fatalf("candidate count=%d wanted=%d", len(candidates), wanted)
				}
				wantedStatus := map[string]int{"request": 400, "revoke": 401, "policy": 403, "revision": 409, "release_storage": 503}
				if want, ok := wantedStatus[name]; ok && failed.status != want {
					t.Fatalf("status=%d wanted=%d", failed.status, want)
				}
				if name == "request" || name == "cancel" {
					var cooldowns int
					if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id=?`, first).Scan(&cooldowns); err != nil || cooldowns != 0 {
						t.Fatalf("request-level failure penalized account: count=%d err=%v", cooldowns, err)
					}
				}
				var outcome string
				if err := a.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, id).Scan(&outcome); err != nil || (outcome != "failed" && outcome != "cancelled") {
					t.Fatalf("unsettled parent: %s %v", outcome, err)
				}
				assertUsageHTTPActiveCleared(t, a)
				return
			}
			if failed != nil || lease == nil || selected.AccountID != second || selected.UpstreamModel != "actual-second" || len(candidates) != 2 {
				t.Fatalf("unexpected preparation: route=%s model=%s failure=%v candidates=%v", selected.AccountID, selected.UpstreamModel, failed, candidates)
			}
			if lease.recovery == nil || lease.recovery.AccountID != second || lease.recovery.UpstreamModel != "actual-second" || lease.recovery.Protocol != accounting.ProtocolOpenAIChatCompletions {
				t.Fatalf("failover lease did not bind actual route: %+v", lease.recovery)
			}
			defer a.releaseModelLease(lease, id, true)
			oldLookup := a.usage.priceLookup
			t.Cleanup(func() { a.usage.priceLookup = oldLookup })
			a.usage.priceLookup = func(_ context.Context, account, actualModel string) (*accounting.PriceSnapshot, error) {
				if account != second || actualModel != "actual-second" {
					t.Fatalf("price used abandoned route %q %q", account, actualModel)
				}
				if name == "price_storage" {
					return nil, errors.New("synthetic catalog unavailable")
				}
				return &accounting.PriceSnapshot{Version: "synthetic-second-price", Currency: "USD", InputPerMillionMicro: 1000000, OutputPerMillionMicro: 1000000}, nil
			}
			if err := a.beginRouteUpstreamUsage(lease.Context(), id, selected); name == "price_storage" {
				if !errors.Is(err, errUsageLedgerUnavailable) {
					t.Fatalf("catalog failure: %v", err)
				}
				if err := a.finishRequestChecked(id, "failed", 0); err != nil {
					t.Fatal(err)
				}
				assertCount("accounting_attempts", true, 0)
				a.releaseModelLease(lease, id, true)
				var cooldowns int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id=?`, second).Scan(&cooldowns); err != nil || cooldowns != 0 {
					t.Fatalf("catalog failure penalized account: count=%d err=%v", cooldowns, err)
				}
				return
			} else if err != nil {
				t.Fatal(err)
			}
			lease.MarkDispatch()
			a.observeRequestUsage(id, []byte(`{"usage":{"prompt_tokens":20,"completion_tokens":3,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}`))
			if err := a.finishRequestChecked(id, "succeeded", 200); err != nil {
				t.Fatal(err)
			}
			var account, dispatch, version string
			var cost int64
			if err := a.store.db.QueryRow(`SELECT account_id,dispatch,price_version,cost_micro FROM accounting_attempts WHERE request_id=?`, id).Scan(&account, &dispatch, &version, &cost); err != nil {
				t.Fatal(err)
			}
			if account != second || dispatch != "failover" || version != "synthetic-second-price" || cost != 23 {
				t.Fatalf("wrong dispatched account/price: %q %q %q %d", account, dispatch, version, cost)
			}
			assertCount("accounting_attempts", true, 1)
			if name == "release_read_failure" {
				if _, err := a.store.db.Exec(`ALTER TABLE model_requests RENAME TO preflight_hidden_requests`); err != nil {
					t.Fatal(err)
				}
				a.releaseModelLease(lease, id, true)
				if _, err := a.store.db.Exec(`ALTER TABLE preflight_hidden_requests RENAME TO model_requests`); err != nil {
					t.Fatal(err)
				}
				var cooldowns int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns WHERE account_id=?`, second).Scan(&cooldowns); err != nil || cooldowns != 0 {
					t.Fatalf("release classification read failure penalized account: count=%d err=%v", cooldowns, err)
				}
			}
		})
	}
}
