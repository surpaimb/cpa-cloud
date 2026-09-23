package service

// Real App, employee middleware and SQLite with a synthetic HTTP transport;
// no provider endpoint or real credential is contacted.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

const budgetTestPayload = `{"model":"budget-chat","messages":[{"role":"user","content":"synthetic-budget-request"}],"max_completion_tokens":7,"n":1,"modalities":["text"],"store":false,"stream":false}`

func TestBudgetDispatchPoolFreezesActualModelPrice(t *testing.T) {
	a, server, key := newBudgetHTTPFixture(t, 3000000)
	cookie, csrf := loginTestAdmin(t, server.URL)
	var account, adminID string
	if err := a.store.db.QueryRow(`SELECT upstream_id FROM models WHERE id='budget-chat'`).Scan(&account); err != nil {
		t.Fatal(err)
	}
	if err := a.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"expected_revision":0,"items":[{"upstream_id":%q,"upstream_model":"gpt-4.1-2025-04-14","priority":1,"weight":1,"max_concurrency":1}]}`, account)
	res := requestJSON(t, "PUT", server.URL+"/admin/api/v1/models/budget-chat/accounts", body, cookie, csrf, server.URL)
	if res.StatusCode != 200 {
		t.Fatalf("pool status=%d %s", res.StatusCode, readBody(res))
	}
	res.Body.Close()
	cost := int64(2000000)
	currency, window := "USD", "rolling_24h"
	if _, err := a.governancePolicies.createPolicy(context.Background(), adminID, governanceOperationID(993), governancePolicyInput{ScopeKind: governance.ScopeKey, ScopeID: key.ID, Enabled: true, Budget: &governanceBudgetLimits{CostMicro: &cost, Currency: &currency, Window: &window, UnknownMode: "deny_unknown"}}); err != nil {
		t.Fatal(err)
	}
	catalog := accounting.NewPriceCatalog(a.store.db)
	price := &accounting.PriceSnapshot{Currency: "USD", InputPerMillionMicro: 1000000, OutputPerMillionMicro: 1000000, CacheReadPerMillionMicro: 1000000, CacheWritePerMillionMicro: 1000000}
	first, err := catalog.Save(context.Background(), accounting.PriceSave{AccountID: account, ActualModel: "gpt-4.1-2025-04-14", OperationID: governanceOperationID(994), ExpectedRevision: 0, Price: price})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		changed := *price
		changed.OutputPerMillionMicro = 9000000
		_, err := catalog.Save(context.Background(), accounting.PriceSave{AccountID: account, ActualModel: "gpt-4.1-2025-04-14", OperationID: governanceOperationID(995), ExpectedRevision: 1, Price: &changed})
		if err != nil {
			return nil, err
		}
		return budgetResponse(7), nil
	})}
	if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 200 {
		t.Fatalf("pool generation=%d %s", code, body)
	}
	var poolRevision, actualCost int64
	var version string
	if err := a.store.db.QueryRow(`SELECT pool_revision,actual_cost_micro,price_version FROM governance_budget_reservations`).Scan(&poolRevision, &actualCost, &version); err != nil || poolRevision != 1 || actualCost != 107 || version != first.Version {
		t.Fatalf("price snapshot=%d/%d/%s %v", poolRevision, actualCost, version, err)
	}
	if calls.Load() != 1 {
		t.Fatal("generation repeated")
	}
}

func newBudgetHTTPFixture(t *testing.T, limit int64) (*App, *httptest.Server, keyView) {
	t.Helper()
	a, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "budget upstream", "openai-compatible", "https://api.openai.com/v1", "synthetic-budget-key")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "budget-chat", account.ID, "gpt-4.1-2025-04-14")
	employee := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Budget employee")
	key := createTestKey(t, server.URL, employee.ID, "budget-key", cookie, csrf)
	var adminID string
	if err := a.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := a.governancePolicies.updateSettingsPatch(context.Background(), adminID, "a0000000-0000-4000-8000-000000000001", 1, true, &enabled); err != nil {
		t.Fatal(err)
	}
	if _, err := a.governancePolicies.createPolicy(context.Background(), adminID, "a0000000-0000-4000-8000-000000000002", governancePolicyInput{ScopeKind: governance.ScopeEmployee, ScopeID: employee.ID, Enabled: true, Budget: &governanceBudgetLimits{TPM: &limit, UnknownMode: "deny_unknown"}}); err != nil {
		t.Fatal(err)
	}
	return a, server, key
}

func TestBudgetDispatchJointRenewalFailureRetainsBothDeadlines(t *testing.T) {
	a, server, key := newBudgetHTTPFixture(t, 3000000)
	a.governance.renewEvery = time.Hour // Drive the two ticks deterministically below.
	var checked atomic.Bool
	a.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		guard, ok := r.Context().Value(governedRequestKey{}).(*governedRequest)
		if !ok {
			return nil, errors.New("missing governance guard")
		}
		before := guard.expires
		if !guard.renewOnce() {
			return nil, errors.New("joint renewal failed")
		}
		var first, second string
		query := `SELECT g.expires_at,b.execution_expires_at FROM governance_requests g JOIN governance_budget_reservations b ON b.request_id=g.id`
		if err := a.store.db.QueryRow(query).Scan(&first, &second); err != nil || first != second || !guard.expires.After(before) {
			return nil, errors.New("renewal did not advance both deadlines")
		}
		if _, err := a.store.db.Exec(`CREATE TRIGGER reject_budget_renew BEFORE UPDATE OF execution_expires_at ON governance_budget_reservations BEGIN SELECT RAISE(ABORT,'synthetic'); END`); err != nil {
			return nil, err
		}
		if guard.renewOnce() || r.Context().Err() == nil {
			return nil, errors.New("failed renewal did not cancel")
		}
		var afterFirst, afterSecond string
		if err := a.store.db.QueryRow(query).Scan(&afterFirst, &afterSecond); err != nil || afterFirst != first || afterSecond != second {
			return nil, errors.New("partial deadline update survived failure")
		}
		checked.Store(true)
		return nil, r.Context().Err()
	})}
	// An in-process cancellation may leave an empty HTTP response; it must not
	// publish a successful completion. The authoritative outcome is the ledger.
	if _, body := budgetTestRequest(t, server, key, budgetTestPayload); strings.Contains(body, `"choices"`) {
		t.Fatal("cancelled execution published a completion")
	}
	if !checked.Load() {
		t.Fatal("joint renewal assertions not reached")
	}
	assertJointRecoveryStatuses(t, a, "cancelled")
}

func TestBudgetDispatchSettlementRollbackAndUncertainCommit(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(fmt.Sprintf("committed-but-unconfirmed-%t", uncertain), func(t *testing.T) {
			a, _, key := newBudgetHTTPFixture(t, 3000000)
			var calls atomic.Int32
			a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return budgetResponse(7), nil })}
			if uncertain {
				a.usage.budgetCommit = func(tx *sql.Tx) error {
					if err := tx.Commit(); err != nil {
						return err
					}
					return errors.New("synthetic commit reply lost")
				}
			} else {
				mustUsageHTTPExec(t, a, `CREATE TRIGGER reject_budget_settle BEFORE UPDATE OF lifecycle ON governance_budget_reservations WHEN NEW.lifecycle='settled' BEGIN SELECT RAISE(ABORT,'synthetic'); END`)
			}
			observed := false
			recorder := &governanceHTTPRecorder{ResponseRecorder: httptest.NewRecorder()}
			recorder.onStorageFailure = func() {
				observed = true
				if uncertain {
					assertJointRecoveryStatuses(t, a, "succeeded")
					a.usage.budgetCommit = nil
				} else {
					assertJointRecoveryStatuses(t, a, "pending")
					assertGovernanceHTTPScalarString(t, a, `SELECT lifecycle FROM governance_budget_reservations`, "may_have_sent")
					mustUsageHTTPExec(t, a, `DROP TRIGGER reject_budget_settle`)
				}
			}
			r := httptest.NewRequest("POST", "http://service.invalid/v1/chat/completions", strings.NewReader(budgetTestPayload))
			r.Header.Set("Authorization", "Bearer "+key.Key)
			r.Header.Set("Content-Type", "application/json")
			a.Handler().ServeHTTP(recorder, r)
			if !observed || recorder.Code != 503 || calls.Load() != 1 || strings.Contains(recorder.Body.String(), `"choices"`) {
				t.Fatalf("terminal failure status=%d calls=%d observed=%t", recorder.Code, calls.Load(), observed)
			}
			assertJointRecoveryStatuses(t, a, "succeeded")
			assertGovernanceHTTPScalarString(t, a, `SELECT lifecycle FROM governance_budget_reservations`, "settled")
			assertUsageHTTPActiveCleared(t, a)
		})
	}
}

func TestBudgetDispatchFourProtocolsRejectUnprovenThenDisabledPasses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T) governanceHTTPHarness
	}{
		{"chat", newGovernanceHTTPChatHarness}, {"responses", newGovernanceHTTPResponsesHarness},
		{"messages", newGovernanceHTTPAnthropicHarness}, {"gemini", newGovernanceHTTPGeminiHarness},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := tc.setup(t)
			var adminID, employeeID string
			if err := h.app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
				t.Fatal(err)
			}
			if err := h.app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, h.key.ID).Scan(&employeeID); err != nil {
				t.Fatal(err)
			}
			enabled := true
			if _, err := h.app.governancePolicies.updateSettingsPatch(context.Background(), adminID, governanceOperationID(990), 1, true, &enabled); err != nil {
				t.Fatal(err)
			}
			limit := int64(3000000)
			if _, err := h.app.governancePolicies.createPolicy(context.Background(), adminID, governanceOperationID(991), governancePolicyInput{ScopeKind: governance.ScopeEmployee, ScopeID: employeeID, Enabled: true, Budget: &governanceBudgetLimits{TPM: &limit, UnknownMode: "deny_unknown"}}); err != nil {
				t.Fatal(err)
			}
			for _, stream := range []bool{false, true} {
				res := h.request(stream)
				body := readBody(res)
				if res.StatusCode != 503 || !strings.Contains(body, "no supported budget bound") {
					t.Fatalf("hard budget status=%d %s", res.StatusCode, body)
				}
			}
			if h.calls.Load() != 0 {
				t.Fatal("unproven request reached upstream")
			}
			enabled = false
			if _, err := h.app.governancePolicies.updateSettingsPatch(context.Background(), adminID, governanceOperationID(992), 2, true, &enabled); err != nil {
				t.Fatal(err)
			}
			for _, stream := range []bool{false, true} {
				res := h.request(stream)
				body := readBody(res)
				if res.StatusCode != 200 {
					t.Fatalf("disabled status=%d %s", res.StatusCode, body)
				}
			}
			if h.calls.Load() != 2 {
				t.Fatal("disabled budget changed existing execution")
			}
		})
	}
}

func budgetTestRequest(t *testing.T, server *httptest.Server, key keyView, payload string) (int, string) {
	t.Helper()
	r, err := http.NewRequest("POST", server.URL+"/v1/chat/completions", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+key.Key)
	r.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, string(data)
}

func budgetResponse(output int) *http.Response {
	body := fmt.Sprintf(`{"model":"gpt-4.1-2025-04-14","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":%d,"total_tokens":%d,"prompt_tokens_details":{"cached_tokens":20}}}`, output, 100+output)
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestBudgetDispatchHTTPKnownUsageAndProfileOverage(t *testing.T) {
	a, server, key := newBudgetHTTPFixture(t, 3000000)
	var calls atomic.Int32
	var output atomic.Int32
	output.Store(7)
	a.http = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.String() != "https://api.openai.com/v1/chat/completions" || r.Header.Get("Authorization") == "Bearer "+key.Key {
			return nil, errors.New("unexpected synthetic route")
		}
		var active int
		if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM governance_budget_reservations WHERE lifecycle='may_have_sent'`).Scan(&active); err != nil || active != 1 {
			return nil, errors.New("missing durable dispatch mark")
		}
		return budgetResponse(int(output.Load())), nil
	})}
	if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 200 {
		t.Fatalf("first status=%d %s", code, body)
	}
	var lifecycle string
	var known, costKnown int
	var actual int64
	if err := a.store.db.QueryRow(`SELECT lifecycle,token_known,actual_tokens,cost_known FROM governance_budget_reservations`).Scan(&lifecycle, &known, &actual, &costKnown); err != nil || lifecycle != "settled" || known != 1 || actual != 107 || costKnown != 0 {
		t.Fatalf("settlement: %s %d %d %d %v", lifecycle, known, actual, costKnown, err)
	}
	output.Store(8) // Total remains below C+M; the output component violates M.
	if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 200 {
		t.Fatalf("overage status=%d %s", code, body)
	}
	if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 503 || !strings.Contains(body, "budget_profile_quarantined") {
		t.Fatalf("quarantine status=%d %s", code, body)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d", calls.Load())
	}
	var quarantined int
	if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM governance_budget_profile_quarantine`).Scan(&quarantined); err != nil || quarantined != 1 {
		t.Fatal("missing durable profile isolation")
	}
}

func TestBudgetDispatchRejectsLimitOrUnprovenPayloadWithoutAttempt(t *testing.T) {
	for _, tc := range []struct {
		name          string
		limit         int64
		payload, code string
		status        int
	}{
		{"limit", 1, budgetTestPayload, "budget_exceeded", 429},
		{"stream", 3000000, strings.Replace(budgetTestPayload, `"stream":false`, `"stream":true`, 1), "budget_bound_unavailable", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, server, key := newBudgetHTTPFixture(t, tc.limit)
			var calls atomic.Int32
			a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return budgetResponse(7), nil })}
			if code, body := budgetTestRequest(t, server, key, tc.payload); code != tc.status || !strings.Contains(body, tc.code) {
				t.Fatalf("status=%d %s", code, body)
			}
			var attempts int
			if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 0 || calls.Load() != 0 {
				t.Fatalf("rejection consumed attempt/network: %d %d %v", attempts, calls.Load(), err)
			}
		})
	}
}

func TestBudgetDispatchUncertainCommitNeverSends(t *testing.T) {
	for _, stage := range []string{"reserve", "mark"} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-committed-%t", stage, committed), func(t *testing.T) {
				a, server, key := newBudgetHTTPFixture(t, 3000000)
				var calls atomic.Int32
				a.http = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { calls.Add(1); return budgetResponse(7), nil })}
				a.budgetCommit = func(current string, tx *sql.Tx) error {
					if current != stage {
						return tx.Commit()
					}
					if committed {
						if err := tx.Commit(); err != nil {
							return err
						}
					} else {
						_ = tx.Rollback()
					}
					return errors.New("synthetic uncertain commit")
				}
				if code, body := budgetTestRequest(t, server, key, budgetTestPayload); code != 503 || !strings.Contains(body, "budget_unavailable") {
					t.Fatalf("status=%d %s", code, body)
				}
				if calls.Load() != 0 {
					t.Fatal("uncertain write regained send permission")
				}
				var count int
				if err := a.store.db.QueryRow(`SELECT COUNT(*) FROM governance_budget_reservations`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if stage == "reserve" && !committed {
					if count != 0 {
						t.Fatal("rolled back reserve retained")
					}
					return
				}
				var state string
				var known int
				if err := a.store.db.QueryRow(`SELECT lifecycle,token_known FROM governance_budget_reservations`).Scan(&state, &known); err != nil {
					t.Fatal(err)
				}
				want := "interrupted"
				if stage == "reserve" {
					want = "released_not_started"
				}
				if state != want || known != 0 {
					t.Fatalf("state=%s known=%d want=%s", state, known, want)
				}
			})
		}
	}
}
