// Independently authored for docs/employee-credential-retirement-contract.md.
// Synthetic temporary SQLite and dynamic HTTP only; no production credentials.
package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/financial"
)

type retirementFixture struct {
	app     *App
	server  *httptest.Server
	dir     string
	cookie  *http.Cookie
	csrf    string
	hooks   atomic.Pointer[employeeRetirementHooks]
	observe atomic.Pointer[func(*http.Request)]
}

func newRetirementFixture(t *testing.T) *retirementFixture {
	t.Helper()
	f := &retirementFixture{dir: t.TempDir()}
	if err := Initialize(t.Context(), f.dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal("synthetic initialization failed")
	}
	f.open(t, true)
	t.Cleanup(func() { f.server.Close(); _ = f.app.Close() })
	return f
}

func (f *retirementFixture) open(t *testing.T, self bool) {
	t.Helper()
	app, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: self, AllowLoopbackUpstream: true, Version: "retirement-test"})
	if err != nil {
		t.Fatal("synthetic reopen failed")
	}
	f.app = app
	f.serve()
	f.cookie, f.csrf = loginTestAdmin(t, f.server.URL)
}

func (f *retirementFixture) serve() {
	app := f.app
	next := app.Handler()
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if observe := f.observe.Load(); observe != nil {
			(*observe)(r)
		}
		if strings.HasSuffix(r.URL.Path, "/retire-credentials") {
			if h := f.hooks.Load(); h != nil {
				r = r.WithContext(context.WithValue(r.Context(), employeeRetirementHooksKey{}, *h))
			}
		}
		next.ServeHTTP(w, r)
	}))
}

func retirementAdapter(t *testing.T, app *App, cookie *http.Cookie, csrf string) *retirementFixture {
	t.Helper()
	f := &retirementFixture{app: app, cookie: cookie, csrf: csrf}
	f.serve()
	t.Cleanup(f.server.Close)
	return f
}

func retirementWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal("synthetic barrier was not reached")
	}
}

func retirementSlotLimits(t *testing.T, f selfSlotFixture) {
	t.Helper()
	// Configure through actual administrator routes so restart sees valid policy
	// creation receipts/audits, not a raw-SQL convenience fixture's orphan rows.
	for _, setup := range []struct{ method, path, body string }{
		{"PUT", "/governance/settings", `{"operation_id":"` + governanceOperationID(9501) + `","expected_revision":1,"enabled":true,"budget_enabled":true}`},
		{"POST", "/governance/policies", `{"operation_id":"` + governanceOperationID(9502) + `","scope_kind":"key","scope_id":` + quoteJSON(f.keyID) + `,"enabled":true,"hard":{"rpm":2,"concurrency":1},"shadow":{"tpm":null,"cost_micro":null,"currency":null,"window":null}}`},
		{"POST", "/budgets", `{"operation_id":"` + governanceOperationID(9503) + `","scope_kind":"key","scope_id":` + quoteJSON(f.keyID) + `,"protocol":null,"model":null,"enabled":true,"token_limit":200,"cost_limit_micro":null,"currency":null}`},
	} {
		r := requestJSON(t, setup.method, f.server.URL+"/admin/api/v1"+setup.path, setup.body, f.adminCookie, f.adminCSRF, f.server.URL)
		if r.StatusCode != 200 {
			r.Body.Close()
			t.Fatalf("synthetic prerequisite %s status=%d", setup.path, r.StatusCode)
		}
		r.Body.Close()
	}
}

type retirementHTTPResult struct {
	response *http.Response
	err      error
}

func retirementAsync(method, target, body string, cookie *http.Cookie, csrf, origin string) <-chan retirementHTTPResult {
	result := make(chan retirementHTTPResult, 1)
	go func() {
		req, err := http.NewRequest(method, target, strings.NewReader(body))
		if err != nil {
			result <- retirementHTTPResult{err: err}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", origin)
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("X-Self-Request", "1")
		if cookie != nil {
			req.AddCookie(cookie)
		}
		r, err := http.DefaultClient.Do(req)
		result <- retirementHTTPResult{r, err}
	}()
	return result
}
func retirementAwait(t *testing.T, result <-chan retirementHTTPResult) *http.Response {
	t.Helper()
	select {
	case r := <-result:
		if r.err != nil {
			t.Fatal("synthetic HTTP failed")
		}
		return r.response
	case <-time.After(10 * time.Second):
		t.Fatal("synthetic HTTP did not finish")
		return nil
	}
}

func TestEmployeeRetirementSameRevisionRaceAndKeyCreationOrders(t *testing.T) {
	f := newRetirementFixture(t)
	t.Run("same-revision", func(t *testing.T) {
		e := f.employee(t)
		reached := make(chan struct{}, 2)
		release := make(chan struct{})
		f.hooks.Store(&employeeRetirementHooks{beforeLock: func() { reached <- struct{}{}; <-release }})
		path := f.server.URL + "/admin/api/v1/employees/" + e.ID + "/retire-credentials"
		one := retirementAsync("POST", path, `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
		two := retirementAsync("POST", path, `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
		retirementWait(t, reached)
		retirementWait(t, reached)
		close(release)
		r1, r2 := retirementAwait(t, one), retirementAwait(t, two)
		statuses := map[int]int{r1.StatusCode: 1}
		statuses[r2.StatusCode]++
		retirementResponse(t, r1, r1.StatusCode)
		retirementResponse(t, r2, r2.StatusCode)
		f.hooks.Store(nil)
		if statuses[200] != 1 || statuses[409] != 1 || retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM account_lifecycle_audit WHERE target_id=? AND action='employee.retire_credentials'`, e.ID) != 1 {
			t.Fatal("CAS did not have one winner")
		}
	})
	for _, createFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("create-first-%t", createFirst), func(t *testing.T) {
			e := f.employee(t)
			reached, release := make(chan struct{}), make(chan struct{})
			var once atomic.Bool
			h := employeeRetirementHooks{}
			if createFirst {
				h.beforeLock = func() { close(reached); <-release }
			} else {
				h.afterStep = func(stage string, _ *sql.Tx) error {
					if stage == "employee" {
						close(reached)
						<-release
					}
					return nil
				}
			}
			f.hooks.Store(&h)
			retire := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+e.ID+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
			retirementWait(t, reached)
			var created keyView
			if createFirst {
				created = createTestKey(t, f.server.URL, e.ID, "create-order", f.cookie, f.csrf)
				close(release)
			} else {
				seen := make(chan struct{})
				observe := func(r *http.Request) {
					if strings.HasSuffix(r.URL.Path, "/keys") && once.CompareAndSwap(false, true) {
						close(seen)
					}
				}
				f.observe.Store(&observe)
				create := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+e.ID+"/keys", `{"name":"later allocation","operation_id":"create-order"}`, f.cookie, f.csrf, f.server.URL)
				retirementWait(t, seen)
				close(release)
				r := retirementAwait(t, create)
				if r.StatusCode != 201 {
					t.Fatal("unchanged Key creation rejected disabled owner")
				}
				decodeResponse(t, r, &created)
				f.observe.Store(nil)
			}
			retirementResponse(t, retirementAwait(t, retire), 200)
			f.hooks.Store(nil)
			if (retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at IS NOT NULL`, created.ID) == 1) != createFirst {
				t.Fatal("Key creation crossed retirement cutoff")
			}
			if _, valid := f.app.lookupEmployeeKey(t.Context(), created.Key); valid {
				t.Fatal("disabled owner authenticated")
			}
			r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":2,"status":"active"}`, f.cookie, f.csrf, f.server.URL)
			if r.StatusCode != 200 {
				t.Fatal("reactivate failed")
			}
			r.Body.Close()
			if _, valid := f.app.lookupEmployeeKey(t.Context(), created.Key); valid == createFirst {
				t.Fatal("old retirement or future allocation semantics changed")
			}
		})
	}
}

func TestEmployeeRetirementSelfIssueBothOrders(t *testing.T) {
	for _, issueFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("issue-first-%t", issueFirst), func(t *testing.T) {
			s := newSelfSlotFixture(t)
			if s.reserve(t, "retirement-issue-race") != 201 {
				t.Fatal("reserve")
			}
			retirementSlotLimits(t, s)
			if s.arm(t) != 200 {
				t.Fatal("arm")
			}
			f := retirementAdapter(t, s.app, s.adminCookie, s.adminCSRF)
			reached, release := make(chan struct{}), make(chan struct{})
			if issueFirst {
				f.hooks.Store(&employeeRetirementHooks{beforeLock: func() { close(reached); <-release }})
				retire := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+s.id+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
				retirementWait(t, reached)
				r := s.issue(t, selfOldPassword)
				if r.StatusCode != 201 {
					t.Fatal("first issue failed")
				}
				r.Body.Close()
				close(release)
				retirementResponse(t, retirementAwait(t, retire), 200)
				if retirementCount(t, s.app.store.db, `SELECT COUNT(*) FROM employee_self_key_slots WHERE employee_id=? AND state='issued'`, s.id) != 1 {
					t.Fatal("issued tombstone changed")
				}
			} else {
				s.app.selfKeyIssueBeforeTx = func() { close(reached); <-release }
				issue := retirementAsync("POST", s.server.URL+"/self/api/v1/keys/issue", `{"slot_id":`+quoteJSON(s.keyID)+`,"current_password":`+quoteJSON(selfOldPassword)+`}`, s.cookie, s.csrf, s.server.URL)
				retirementWait(t, reached)
				retirementResponse(t, f.retire(t, s.id, 1), 200)
				close(release)
				r := retirementAwait(t, issue)
				if r.StatusCode != 401 {
					t.Fatal("stale issue survived retirement")
				}
				if strings.Contains(readBody(r), "cpac_") {
					t.Fatal("stale issue emitted Key")
				}
				r.Body.Close()
				if retirementCount(t, s.app.store.db, `SELECT COUNT(*) FROM employee_self_key_slots WHERE employee_id=? AND state='cancelled'`, s.id) != 1 {
					t.Fatal("unused slot not cancelled")
				}
			}
			if retirementCount(t, s.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at IS NOT NULL`, s.keyID) != 1 {
				t.Fatal("issued or dormant Key not retired")
			}
		})
	}
}

func TestEmployeeRetirementPatchRaceK04AndRestart(t *testing.T) {
	f := newRetirementFixture(t)
	for _, retired := range []bool{false, true} {
		t.Run(fmt.Sprintf("K04-retired-%t", retired), func(t *testing.T) {
			e := f.employee(t)
			key := createTestKey(t, f.server.URL, e.ID, "k04-key", f.cookie, f.csrf)
			if retired {
				retirementResponse(t, f.retire(t, e.ID, 1), 200)
			} else {
				r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":1,"status":"disabled"}`, f.cookie, f.csrf, f.server.URL)
				if r.StatusCode != 200 {
					t.Fatal("ordinary disable")
				}
				r.Body.Close()
			}
			if _, valid := f.app.lookupEmployeeKey(t.Context(), key.Key); valid {
				t.Fatal("disabled Key accepted")
			}
			r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":2,"status":"active"}`, f.cookie, f.csrf, f.server.URL)
			if r.StatusCode != 200 {
				t.Fatal("K04 restore")
			}
			r.Body.Close()
			if _, valid := f.app.lookupEmployeeKey(t.Context(), key.Key); valid == retired {
				t.Fatal("K04 changed")
			}
		})
	}
	t.Run("PATCH-wins-stale-retirement", func(t *testing.T) {
		e := f.employee(t)
		key := createTestKey(t, f.server.URL, e.ID, "patch-race", f.cookie, f.csrf)
		reached, release := make(chan struct{}), make(chan struct{})
		f.hooks.Store(&employeeRetirementHooks{beforeLock: func() { close(reached); <-release }})
		retire := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+e.ID+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
		retirementWait(t, reached)
		r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":1,"status":"disabled"}`, f.cookie, f.csrf, f.server.URL)
		if r.StatusCode != 200 {
			t.Fatal("PATCH race")
		}
		r.Body.Close()
		close(release)
		retirementResponse(t, retirementAwait(t, retire), 409)
		f.hooks.Store(nil)
		if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at IS NULL`, key.ID) != 1 {
			t.Fatal("stale retirement changed Key")
		}
	})
	t.Run("retirement-wins-stale-PATCH", func(t *testing.T) {
		e := f.employee(t)
		reached, release := make(chan struct{}), make(chan struct{})
		f.hooks.Store(&employeeRetirementHooks{afterStep: func(stage string, _ *sql.Tx) error {
			if stage == "employee" {
				close(reached)
				<-release
			}
			return nil
		}})
		retire := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+e.ID+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
		retirementWait(t, reached)
		seen := make(chan struct{})
		observe := func(r *http.Request) {
			if r.Method == "PATCH" {
				close(seen)
			}
		}
		f.observe.Store(&observe)
		patch := retirementAsync("PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":1,"status":"active"}`, f.cookie, f.csrf, f.server.URL)
		retirementWait(t, seen)
		close(release)
		retirementResponse(t, retirementAwait(t, retire), 200)
		r := retirementAwait(t, patch)
		if r.StatusCode != 409 {
			t.Fatal("stale PATCH crossed retirement")
		}
		r.Body.Close()
		f.hooks.Store(nil)
		f.observe.Store(nil)
	})
	for _, enrolled := range []bool{true, false} {
		t.Run(fmt.Sprintf("restart-enrolled-%t", enrolled), func(t *testing.T) {
			e := f.employee(t)
			key := createTestKey(t, f.server.URL, e.ID, "restart-key", f.cookie, f.csrf)
			secret := selfIssue(t, f.server.URL, e.ID, f.cookie, f.csrf)
			var oldCookie *http.Cookie
			if enrolled {
				oldCookie, _ = selfRedeem(t, f.server.URL, e.ID, secret)
			}
			retirementResponse(t, f.retire(t, e.ID, 1), 200)
			f.server.Close()
			if f.app.Close() != nil {
				t.Fatal("close failed")
			}
			f.open(t, true)
			// Re-enable to prove the old authority is gone, not merely status-blocked.
			r := requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+e.ID, `{"expected_revision":2,"status":"active"}`, f.cookie, f.csrf, f.server.URL)
			if r.StatusCode != 200 {
				t.Fatal("restart restore")
			}
			r.Body.Close()
			if _, valid := f.app.lookupEmployeeKey(t.Context(), key.Key); valid {
				t.Fatal("retired Key resurrected")
			}
			r = selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(e.ID)+`,"password":`+quoteJSON(selfOldPassword)+`}`, f.server.URL, nil, "")
			if r.StatusCode != 401 {
				t.Fatal("old password survived")
			}
			r.Body.Close()
			if enrolled {
				r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/session", "", f.server.URL, oldCookie, "")
			} else {
				r = selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/enroll", `{"employee_id":`+quoteJSON(e.ID)+`,"enrollment_secret":`+quoteJSON(secret)+`,"password":`+quoteJSON(selfOldPassword)+`}`, f.server.URL, nil, "")
			}
			if r.StatusCode != 401 {
				t.Fatal("old session/enrollment survived")
			}
			r.Body.Close()
		})
	}
	t.Run("self-feature-off", func(t *testing.T) {
		f.server.Close()
		if f.app.Close() != nil {
			t.Fatal("close")
		}
		f.open(t, false)
		e := f.employee(t)
		retirementResponse(t, f.retire(t, e.ID, 1), 200)
	})
}

func TestEmployeeRetirementCrossOwnerSlotAndMalformedStateRollback(t *testing.T) {
	f := newRetirementFixture(t)
	for _, reverse := range []bool{false, true} {
		t.Run(fmt.Sprintf("cross-owner-reverse-%t", reverse), func(t *testing.T) {
			e, other := f.employee(t), f.employee(t)
			key := createTestKey(t, f.server.URL, other.ID, "cross-owner", f.cookie, f.csrf)
			if _, err := f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)`, e.ID, key.ID, utcNow()); err != nil {
				t.Fatal(err)
			}
			target := e.ID
			if reverse {
				target = other.ID
			}
			before := retirementSnapshot(t, f.app.store.db, false)
			retirementResponse(t, f.retire(t, target, 1), 503)
			if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
				t.Fatal("cross-owner mutation escaped rollback")
			}
		})
	}
	e := f.employee(t)
	key := createTestKey(t, f.server.URL, e.ID, "malformed-time", f.cookie, f.csrf)
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET revoked_at='invalid' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	before := retirementSnapshot(t, f.app.store.db, false)
	retirementResponse(t, f.retire(t, e.ID, 1), 503)
	if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
		t.Fatal("malformed state partially committed")
	}
}

func TestEmployeeRetirementFinalDispatchBothOrders(t *testing.T) {
	for _, retirementFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("retirement-first-%t", retirementFirst), func(t *testing.T) {
			base := newRuntimeFixture(t, &runtimeSequenceRandom{}, time.Minute, 4)
			a := base.base.app
			base.base.enableRuntimeAdminHTTP(t)
			_ = a.accountPool.Close()
			a.accountPool = base.rt
			base.insertAccount(t, "ups_retirement_dispatch", true)
			base.insertModelPool(t, "retirement-dispatch-model", "ups_retirement_dispatch", 1, modelAccountView{UpstreamID: "ups_retirement_dispatch", UpstreamModel: "actual-model", Priority: 1, Weight: 1, MaxConcurrency: 1})
			f := retirementAdapter(t, a, base.base.cookie, base.base.csrf)
			ctx := context.WithValue(t.Context(), requestIDKey{}, "retirement-dispatch-request")
			r := httptest.NewRequest("POST", "/v1/responses", strings.NewReader("{}")).WithContext(ctx)
			selected, lease, failure := a.prepareModelRoute(r, base.auth1, "retirement-dispatch-model", []string{"openai-compatible"}, accounting.ProtocolOpenAIResponses, true, func(_ *http.Request, s route) (route, *modelPreflightError) { return s, nil })
			if failure != nil || lease == nil {
				t.Fatal("synthetic preparation failed")
			}
			defer a.releaseModelLease(lease, requestID(ctx), true)
			if retirementFirst {
				retirementResponse(t, f.retire(t, base.auth1.EmployeeID, 1), 200)
				if _, failed := a.dispatchModelRoute(r, base.auth1, "retirement-dispatch-model", selected, lease, true); failed == nil || failed.status != 401 {
					t.Fatal("stale final dispatch accepted")
				}
				if retirementCount(t, a.store.db, `SELECT COUNT(*) FROM accounting_attempts WHERE request_id=?`, requestID(ctx)) != 0 {
					t.Fatal("retired authority created an upstream attempt")
				}
			} else {
				reached, release := make(chan struct{}), make(chan struct{})
				r = withDispatchBarrierHook(r, func(context.Context, *sql.Tx, string, time.Time) error { close(reached); <-release; return nil })
				dispatched := make(chan *modelAdmissionError, 1)
				go func() {
					_, failed := a.dispatchModelRoute(r, base.auth1, "retirement-dispatch-model", selected, lease, true)
					dispatched <- failed
				}()
				retirementWait(t, reached)
				seen := make(chan struct{})
				observe := func(req *http.Request) {
					if strings.HasSuffix(req.URL.Path, "/retire-credentials") {
						close(seen)
					}
				}
				f.observe.Store(&observe)
				retire := retirementAsync("POST", f.server.URL+"/admin/api/v1/employees/"+base.auth1.EmployeeID+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL)
				retirementWait(t, seen)
				close(release)
				select {
				case failed := <-dispatched:
					if failed != nil {
						t.Fatal("earlier dispatch failed")
					}
				case <-time.After(5 * time.Second):
					t.Fatal("dispatch barrier stuck")
				}
				retirementResponse(t, retirementAwait(t, retire), 200)
				if retirementCount(t, a.store.db, `SELECT COUNT(*) FROM accounting_attempt_dispatches d JOIN accounting_attempts a ON a.id=d.attempt_id WHERE a.request_id=?`, requestID(ctx)) != 1 {
					t.Fatal("earlier durable dispatch was undone")
				}
				if _, failed := a.dispatchModelRoute(httptest.NewRequest("POST", "/v1/responses", nil), base.auth1, "retirement-dispatch-model", selected, nil, false); failed == nil || failed.status != 401 {
					t.Fatal("later final dispatch accepted retired authority")
				}
			}
			a.finishRequest(requestID(ctx), "cancelled", 0)
		})
	}
}

type retirementQueueClock struct{ queued chan struct{} }

func (c *retirementQueueClock) Now() time.Time { return time.Now().UTC() }
func (c *retirementQueueClock) After(delay time.Duration) <-chan time.Time {
	// Scheduler capacity wait targets lease expiry (~60s); heartbeat is 20s.
	if delay > 30*time.Second {
		select {
		case c.queued <- struct{}{}:
		default:
		}
	}
	return time.After(delay)
}

func TestEmployeeRetirementWakesActuallyQueuedHTTPWithoutNewAttempt(t *testing.T) {
	f := newRetirementFixture(t)
	e := f.employee(t)
	key := createTestKey(t, f.server.URL, e.ID, "queue-key", f.cookie, f.csrf)
	entered, release := make(chan struct{}, 1), make(chan struct{})
	var hits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"finished","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()
	defer close(release)
	account := createModelAdmissionUpstream(t, f.server.URL, f.cookie, f.csrf, "retirement-queue", "openai-compatible", upstream.URL, "synthetic-upstream-secret")
	createModelAdmissionModel(t, f.server.URL, f.cookie, f.csrf, "retirement-queue", account.ID, "provider-model")
	putModelAdmissionPool(t, f.server.URL, f.cookie, f.csrf, "retirement-queue", account.ID, "provider-model")
	if f.app.accountPool.Close() != nil {
		t.Fatal("runtime close")
	}
	clock := &retirementQueueClock{queued: make(chan struct{}, 1)}
	runtime, err := newAccountPoolRuntimeWithConfig(f.app, accountPoolRuntimeConfig{Clock: clock, LeaseTTL: time.Minute, MaxWaiters: 4})
	if err != nil {
		t.Fatal("synthetic runtime")
	}
	f.app.accountPool = runtime
	call := func() <-chan retirementHTTPResult {
		ch := make(chan retirementHTTPResult, 1)
		go func() {
			req, _ := http.NewRequest("POST", f.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"retirement-queue","messages":[]}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+key.Key)
			r, err := http.DefaultClient.Do(req)
			ch <- retirementHTTPResult{r, err}
		}()
		return ch
	}
	active := call()
	retirementWait(t, entered)
	queued := call()
	retirementWait(t, clock.queued)
	retirementResponse(t, f.retire(t, e.ID, 1), 200)
	r := retirementAwait(t, queued)
	if r.StatusCode != 401 {
		t.Fatal("queued authority was not revalidated")
	}
	r.Body.Close()
	if hits.Load() != 1 {
		t.Fatal("retirement allowed another upstream request")
	}
	// Release the already-dispatched request without double-closing the barrier.
	release <- struct{}{}
	r = retirementAwait(t, active)
	if r.StatusCode != 200 {
		t.Fatal("already dispatched request semantics changed")
	}
	r.Body.Close()
	if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM accounting_attempts`) != 1 {
		t.Fatal("queued request acquired an extra attempt")
	}
}

func TestEmployeeRetirementCancellationBeforeCommitRollsBack(t *testing.T) {
	f := newRetirementFixture(t)
	e := f.employee(t)
	createTestKey(t, f.server.URL, e.ID, "cancel-key", f.cookie, f.csrf)
	before := retirementSnapshot(t, f.app.store.db, false)
	next := f.app.Handler()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		hooks := employeeRetirementHooks{afterStep: func(stage string, _ *sql.Tx) error {
			if stage == "audit" {
				cancel()
			}
			return nil
		}}
		next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, employeeRetirementHooksKey{}, hooks)))
	}))
	defer server.Close()
	retirementResponse(t, requestJSON(t, "POST", server.URL+"/admin/api/v1/employees/"+e.ID+"/retire-credentials", `{"expected_revision":1}`, f.cookie, f.csrf, server.URL), 503)
	if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
		t.Fatal("cancelled operation partially committed")
	}
}

func TestEmployeeRetirementSafeRevisionCeiling(t *testing.T) {
	f := newRetirementFixture(t)
	e := f.employee(t)
	if _, err := f.app.store.db.Exec(`UPDATE employees SET revision=? WHERE id=?`, employeeRetirementMaxRevision, e.ID); err != nil {
		t.Fatal(err)
	}
	raw := retirementResponse(t, f.retire(t, e.ID, employeeRetirementMaxRevision), 200)
	if !strings.Contains(raw, `"revision":9007199254740991`) {
		t.Fatal("revision ceiling mismatch")
	}
	retirementResponse(t, f.retire(t, e.ID, employeeRetirementMaxRevision), 409)
}

func TestEmployeeRetirementAuditRetentionCannotEraseOwnEvent(t *testing.T) {
	f := newRetirementFixture(t)
	e := f.employee(t)
	createTestKey(t, f.server.URL, e.ID, "audit-retention", f.cookie, f.csrf)
	tx, err := f.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if _, err := tx.Exec(`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?,'synthetic-admin','synthetic.action','employee',?,'succeeded','2099-01-01T00:00:00Z')`, fmt.Sprintf("future-retire-audit-%d", i), e.ID); err != nil {
			_ = tx.Rollback()
			t.Fatal("future audit fixture")
		}
	}
	if tx.Commit() != nil {
		t.Fatal("future audit fixture commit")
	}
	before := retirementSnapshot(t, f.app.store.db, false)
	retirementResponse(t, f.retire(t, e.ID, 1), 503)
	if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
		t.Fatal("audit eviction did not roll back retirement")
	}
}

func (f *retirementFixture) employee(t *testing.T) employee {
	t.Helper()
	return selfCreateEmployee(t, f.server.URL, f.cookie, f.csrf)
}

func (f *retirementFixture) retire(t *testing.T, id string, revision int64) *http.Response {
	t.Helper()
	return requestJSON(t, "POST", f.server.URL+"/admin/api/v1/employees/"+id+"/retire-credentials", fmt.Sprintf(`{"expected_revision":%d}`, revision), f.cookie, f.csrf, f.server.URL)
}

func retirementResponse(t *testing.T, response *http.Response, expected int) string {
	t.Helper()
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != expected || response.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("retirement response status=%d want=%d read_ok=%t", response.StatusCode, expected, err == nil)
	}
	if expected != 200 && (strings.Contains(string(raw), "private-retirement") || strings.Contains(string(raw), "cpac_") || strings.Contains(string(raw), "revision\":")) {
		t.Fatal("failure leaked private or success-shaped data")
	}
	return string(raw)
}

func retirementQueryDigest(t *testing.T, db *sql.DB, query string, args ...any) [32]byte {
	t.Helper()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatal("snapshot query failed")
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		t.Fatal("snapshot columns failed")
	}
	items := make([][]any, 0)
	for rows.Next() {
		values := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if rows.Scan(pointers...) != nil {
			t.Fatal("snapshot scan failed")
		}
		items = append(items, values)
	}
	if rows.Err() != nil {
		t.Fatal("snapshot iteration failed")
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal("snapshot encoding failed")
	}
	return sha256.Sum256(encoded)
}

func retirementSnapshot(t *testing.T, db *sql.DB, excludeMutable bool) map[string][32]byte {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal("table inventory failed")
	}
	var names []string
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			t.Fatal("table inventory scan failed")
		}
		names = append(names, name)
	}
	iterationErr, closeErr := rows.Err(), rows.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatal("table inventory incomplete")
	}
	mutable := map[string]bool{"employees": true, "access_keys": true, "employee_self_key_slots": true, "employee_self_sessions": true, "employee_self_credentials": true, "account_lifecycle_audit": true}
	result := make(map[string][32]byte)
	for _, name := range names {
		if excludeMutable && mutable[name] {
			continue
		}
		result[name] = retirementQueryDigest(t, db, `SELECT * FROM "`+strings.ReplaceAll(name, `"`, `""`)+`" ORDER BY rowid`)
	}
	return result
}

func retirementSeedSelf(t *testing.T, f *retirementFixture, id string) {
	t.Helper()
	if _, err := f.app.store.db.Exec(`INSERT INTO employee_self_credentials(employee_id,password_hash,updated_at) VALUES(?,?,?)`, id, selfDummyHash, utcNow()); err != nil {
		t.Fatal("synthetic credential fixture failed")
	}
	for i := 0; i < 2; i++ {
		if _, err := f.app.store.db.Exec(`INSERT INTO employee_self_sessions(selector,employee_id,verifier_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`, fmt.Sprintf("retirement-session-%s-%d", id, i), id, []byte("synthetic-verifier-digest"), "synthetic-csrf", selfTime(time.Now().Add(time.Hour)), utcNow()); err != nil {
			t.Fatal("synthetic session fixture failed")
		}
	}
}

func retirementCount(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var value int64
	if db.QueryRow(query, args...).Scan(&value) != nil {
		t.Fatal("count query failed")
	}
	return value
}

func TestEmployeeRetirementZeroAndMultipleKeysPreserveOtherFacts(t *testing.T) {
	f := newRetirementFixture(t)
	for _, keys := range []int{0, 4} {
		t.Run(fmt.Sprintf("keys-%d", keys), func(t *testing.T) {
			employee := f.employee(t)
			other := f.employee(t)
			retirementSeedSelf(t, f, employee.ID)
			retirementSeedSelf(t, f, other.ID)
			createTestKey(t, f.server.URL, other.ID, "other-key", f.cookie, f.csrf)
			var keyIDs []string
			for i := 0; i < keys; i++ {
				key := createTestKey(t, f.server.URL, employee.ID, fmt.Sprintf("retire-key-%d", i), f.cookie, f.csrf)
				keyIDs = append(keyIDs, key.ID)
			}
			if keys > 0 {
				if _, err := f.app.store.db.Exec(`UPDATE access_keys SET revoked_at='2026-01-01T00:00:00Z' WHERE id=?`, keyIDs[0]); err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.store.db.Exec(`UPDATE access_keys SET expires_at='2026-01-02T00:00:00Z' WHERE id=?`, keyIDs[1]); err != nil {
					t.Fatal(err)
				}
				if _, err := f.app.store.db.Exec(`INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,finished_at,outcome,upstream_status) VALUES(?,?,?,'historical-model','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z','succeeded',200)`, "retirement-history-"+employee.ID, employee.ID, keyIDs[0]); err != nil {
					t.Fatal("synthetic usage seed failed")
				}
			}
			ledger := financial.NewLedger(f.app.store.db)
			_, err := ledger.Post(t.Context(), financial.Post{OperationID: "retirement-financial-" + employee.ID, Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: employee.ID}, ResourceKind: "adjustment", ResourceID: employee.ID, ObservedAt: time.Now().UTC(), Entries: []financial.EntryInput{{Owner: financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: employee.ID}, Currency: "USD", Kind: financial.EntryAdjustmentCredit, AmountMicro: 42, ResourceKind: "adjustment", ResourceID: employee.ID}}})
			if err != nil {
				t.Fatal("synthetic ledger seed failed")
			}
			before := retirementSnapshot(t, f.app.store.db, true)
			keyFacts := retirementQueryDigest(t, f.app.store.db, `SELECT id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at FROM access_keys ORDER BY id`)
			otherFacts := retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employees WHERE id=?`, other.ID)
			otherSelf := retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employee_self_credentials WHERE employee_id=?`, other.ID)
			otherKeys := retirementQueryDigest(t, f.app.store.db, `SELECT * FROM access_keys WHERE employee_id=? ORDER BY id`, other.ID)
			otherSessions := retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employee_self_sessions WHERE employee_id=? ORDER BY selector`, other.ID)
			raw := retirementResponse(t, f.retire(t, employee.ID, 1), 200)
			var output map[string]any
			if json.Unmarshal([]byte(raw), &output) != nil || len(output) != 3 || output["id"] != employee.ID || output["status"] != "disabled" || output["revision"] != float64(2) {
				t.Fatal("success metadata mismatch")
			}
			if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, true)) || keyFacts != retirementQueryDigest(t, f.app.store.db, `SELECT id,employee_id,name,selector,digest,digest_version,operation_id,operation_fingerprint,expires_at,created_at FROM access_keys ORDER BY id`) || otherFacts != retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employees WHERE id=?`, other.ID) || otherSelf != retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employee_self_credentials WHERE employee_id=?`, other.ID) {
				t.Fatal("unrelated facts changed")
			}
			if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE employee_id=? AND revoked_at IS NULL`, employee.ID) != 0 || retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM employee_self_sessions WHERE employee_id=?`, employee.ID) != 0 || retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM employee_self_credentials WHERE employee_id=?`, employee.ID) != 0 {
				t.Fatal("credential retirement incomplete")
			}
			if otherKeys != retirementQueryDigest(t, f.app.store.db, `SELECT * FROM access_keys WHERE employee_id=? ORDER BY id`, other.ID) || otherSessions != retirementQueryDigest(t, f.app.store.db, `SELECT * FROM employee_self_sessions WHERE employee_id=? ORDER BY selector`, other.ID) {
				t.Fatal("other employee authority changed")
			}
			if keys > 0 {
				if retirementCount(t, f.app.store.db, `SELECT COUNT(DISTINCT revoked_at) FROM access_keys WHERE employee_id=? AND id<>?`, employee.ID, keyIDs[0]) != 1 || retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at='2026-01-01T00:00:00Z'`, keyIDs[0]) != 1 {
					t.Fatal("revocation timestamps changed")
				}
			}
			if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM account_lifecycle_audit a JOIN sessions s ON s.admin_id=a.actor_id WHERE a.action='employee.retire_credentials' AND a.target_type='employee' AND a.target_id=? AND a.result='succeeded'`, employee.ID) != 1 {
				t.Fatal("authenticated actor audit missing")
			}
			retirementResponse(t, f.retire(t, employee.ID, 1), 409)
			retirementResponse(t, f.retire(t, employee.ID, 2), 200)
		})
	}
}

func TestEmployeeRetirementEverySlotStateAndLifetimeTombstone(t *testing.T) {
	for _, state := range []string{"pending", "armed", "issued", "cancelled"} {
		t.Run(state, func(t *testing.T) {
			f := newSelfSlotFixture(t)
			if f.reserve(t, "retirement-slot") != 201 {
				t.Fatal("reserve failed")
			}
			if state == "armed" || state == "issued" {
				retirementSlotLimits(t, f)
				if f.arm(t) != 200 {
					t.Fatal("arm failed")
				}
			}
			if state == "issued" {
				r := f.issue(t, selfOldPassword)
				if r.StatusCode != 201 {
					t.Fatal("issue failed")
				}
				r.Body.Close()
			}
			if state == "cancelled" {
				r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+f.keyID+"/self-key-slot/cancel", `{}`, f.adminCookie, f.adminCSRF, f.server.URL)
				if r.StatusCode != 204 {
					t.Fatal("cancel failed")
				}
				r.Body.Close()
			}
			before := retirementQueryDigest(t, f.app.store.db, `SELECT state,issued_at,cancelled_at,armed_at,created_at FROM employee_self_key_slots WHERE employee_id=?`, f.id)
			r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/employees/"+f.id+"/retire-credentials", `{"expected_revision":1}`, f.adminCookie, f.adminCSRF, f.server.URL)
			retirementResponse(t, r, 200)
			var actual string
			if f.app.store.db.QueryRow(`SELECT state FROM employee_self_key_slots WHERE employee_id=?`, f.id).Scan(&actual) != nil {
				t.Fatal("tombstone missing")
			}
			if state == "pending" || state == "armed" {
				if actual != "cancelled" || retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM employee_self_key_slots WHERE employee_id=? AND arm_fingerprint IS NULL AND arm_employee_revision IS NULL AND arm_key_policy_revision IS NULL AND arm_governance_revision IS NULL AND arm_budget_revision IS NULL AND cancelled_at IS NOT NULL`, f.id) != 1 {
					t.Fatal("slot cancellation incomplete")
				}
			} else if actual != state || before != retirementQueryDigest(t, f.app.store.db, `SELECT state,issued_at,cancelled_at,armed_at,created_at FROM employee_self_key_slots WHERE employee_id=?`, f.id) {
				t.Fatal("terminal tombstone changed")
			}
			if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at IS NOT NULL`, f.keyID) != 1 {
				t.Fatal("slot key not retired")
			}
			f.server.Close()
			if f.app.Close() != nil {
				t.Fatal("slot restart close")
			}
			reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, AllowLoopbackUpstream: true, Version: "retirement-restart"})
			if err != nil {
				t.Fatal(err)
			}
			f.app = reopened
			f.server = httptest.NewServer(reopened.Handler())
			t.Cleanup(func() { f.server.Close(); _ = reopened.Close() })
			r = requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":2,"status":"active"}`, f.adminCookie, f.adminCSRF, f.server.URL)
			if r.StatusCode != 200 {
				t.Fatal("reactivate failed")
			}
			r.Body.Close()
			if f.reserve(t, "new-retirement-slot") != 409 {
				t.Fatal("lifetime slot recycled")
			}
		})
	}
}

func TestEmployeeRetirementAuthorizationStrictInputAndMissingTarget(t *testing.T) {
	f := newRetirementFixture(t)
	e := f.employee(t)
	key := createTestKey(t, f.server.URL, e.ID, "role-key", f.cookie, f.csrf)
	selfSecret := selfIssue(t, f.server.URL, e.ID, f.cookie, f.csrf)
	selfCookie, _ := selfRedeem(t, f.server.URL, e.ID, selfSecret)
	path := f.server.URL + "/admin/api/v1/employees/" + e.ID + "/retire-credentials"
	for _, role := range []string{"anonymous", "model", "self", "origin", "csrf"} {
		t.Run(role, func(t *testing.T) {
			req, _ := http.NewRequest("POST", path, strings.NewReader(`{"expected_revision":1}`))
			req.Header.Set("Content-Type", "application/json")
			want := 401
			switch role {
			case "model":
				req.Header.Set("Authorization", "Bearer "+key.Key)
			case "self":
				req.AddCookie(selfCookie)
			case "origin":
				req.AddCookie(f.cookie)
				req.Header.Set("Origin", "https://foreign.invalid")
				req.Header.Set("X-CSRF-Token", f.csrf)
				want = 403
			case "csrf":
				req.AddCookie(f.cookie)
				req.Header.Set("Origin", f.server.URL)
				req.Header.Set("X-CSRF-Token", "wrong")
				want = 403
			}
			r, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			retirementResponse(t, r, want)
		})
	}
	invalid := []string{`{}`, `null`, `[]`, `{"expected_revision":0}`, `{"expected_revision":-1}`, `{"expected_revision":1.0}`, `{"expected_revision":1e0}`, `{"expected_revision":"1"}`, `{"expected_revision":null}`, `{"expected_revision":true}`, `{"expected_revision":9007199254740991}`, `{"expected_revision":1,"expected_revision":1}`, `{"EXPECTED_REVISION":1}`, `{"expected_revision":1,"operation_id":"x"}`, `{"expected_revision":1} {}`, `{"expected_revision":01}`, "{\"expected_revision\":1}\xff", `{"expected_revision":1,"padding":"` + strings.Repeat("x", 1024) + `"}`}
	before := retirementSnapshot(t, f.app.store.db, false)
	for i, body := range invalid {
		t.Run(fmt.Sprintf("invalid-%02d", i), func(t *testing.T) {
			retirementResponse(t, requestJSON(t, "POST", path, body, f.cookie, f.csrf, f.server.URL), 400)
		})
	}
	for _, suffix := range []string{"?x=1", "?"} {
		retirementResponse(t, requestJSON(t, "POST", path+suffix, `{"expected_revision":1}`, f.cookie, f.csrf, f.server.URL), 400)
	}
	retirementResponse(t, f.retire(t, "missing-employee", 1), 404)
	retirementResponse(t, f.retire(t, e.ID, 2), 409)
	if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
		t.Fatal("rejected input changed state")
	}
}

func TestEmployeeRetirementEveryWriteFailureAndCommitOutcomes(t *testing.T) {
	f := newRetirementFixture(t)
	faults := []struct{ name, trigger string }{
		{"employee", `BEFORE UPDATE ON employees`}, {"keys", `BEFORE UPDATE ON access_keys`},
		{"slot", `BEFORE UPDATE ON employee_self_key_slots`}, {"sessions", `BEFORE DELETE ON employee_self_sessions`},
		{"credentials", `BEFORE DELETE ON employee_self_credentials`}, {"audit", `BEFORE INSERT ON account_lifecycle_audit`},
		{"audit-pruning", `BEFORE DELETE ON account_lifecycle_audit`},
	}
	for _, fault := range faults {
		t.Run(fault.name, func(t *testing.T) {
			e := f.employee(t)
			retirementSeedSelf(t, f, e.ID)
			key := createTestKey(t, f.server.URL, e.ID, "fault-key", f.cookie, f.csrf)
			if _, err := f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)`, e.ID, key.ID, utcNow()); err != nil {
				t.Fatal(err)
			}
			if fault.name == "audit-pruning" {
				seedTx, err := f.app.store.db.Begin()
				if err != nil {
					t.Fatal(err)
				}
				for i := 0; i < 1001; i++ {
					if _, err := seedTx.Exec(`INSERT INTO account_lifecycle_audit(id,actor_id,action,target_type,target_id,result,occurred_at) VALUES(?, 'synthetic-admin','synthetic.action','employee',?,'succeeded','2026-01-01T00:00:00Z')`, fmt.Sprintf("retire-prune-%d", i), e.ID); err != nil {
						_ = seedTx.Rollback()
						t.Fatal(err)
					}
				}
				if seedTx.Commit() != nil {
					t.Fatal("audit fixture commit")
				}
			}
			if _, err := f.app.store.db.Exec(`CREATE TRIGGER retirement_fault ` + fault.trigger + ` BEGIN SELECT RAISE(ABORT,'private-retirement-fault'); END`); err != nil {
				t.Fatal(err)
			}
			before := retirementSnapshot(t, f.app.store.db, false)
			retirementResponse(t, f.retire(t, e.ID, 1), 503)
			if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
				t.Fatal("write failure partially committed")
			}
			if _, err := f.app.store.db.Exec(`DROP TRIGGER retirement_fault`); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, committed := range []bool{false, true} {
		t.Run(fmt.Sprintf("commit-actually-%t", committed), func(t *testing.T) {
			e := f.employee(t)
			key := createTestKey(t, f.server.URL, e.ID, "commit-key", f.cookie, f.csrf)
			retirementSeedSelf(t, f, e.ID)
			before := retirementSnapshot(t, f.app.store.db, false)
			f.hooks.Store(&employeeRetirementHooks{commit: func(tx *sql.Tx) error {
				if committed {
					if err := tx.Commit(); err != nil {
						return err
					}
				}
				return errors.New("private-retirement-commit")
			}})
			retirementResponse(t, f.retire(t, e.ID, 1), 503)
			f.hooks.Store(nil)
			if committed {
				if retirementCount(t, f.app.store.db, `SELECT COUNT(*) FROM access_keys WHERE id=? AND revoked_at IS NOT NULL`, key.ID) != 1 {
					t.Fatal("uncertain committed state missing")
				}
				retirementResponse(t, f.retire(t, e.ID, 1), 409)
			} else {
				if !reflect.DeepEqual(before, retirementSnapshot(t, f.app.store.db, false)) {
					t.Fatal("refused commit changed state")
				}
				retirementResponse(t, f.retire(t, e.ID, 1), 200)
			}
		})
	}
}
