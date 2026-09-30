// Independently authored tests for docs/employee-self-token-summary-contract.md.
package service

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func tokenSummaryRequest(t *testing.T, baseURL, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, "GET", baseURL+"/self/api/v1/usage/summary"+query, "", "", cookie, "")
}

func readTokenSummary(t *testing.T, response *http.Response) selfTokenSummaryResponse {
	t.Helper()
	if response.StatusCode != 200 {
		t.Fatalf("summary status=%d body=%s", response.StatusCode, readBody(response))
	}
	var summary selfTokenSummaryResponse
	decodeResponse(t, response, &summary)
	return summary
}

func insertTokenRequest(t *testing.T, f selfPasswordFixture, id, employeeID, started, status string) {
	t.Helper()
	var finished any
	if status != "pending" {
		finished = started
	}
	_, err := f.app.store.db.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,?)`, id, employeeID, "key_"+id, "synthetic-model", "openai", started, finished, status)
	if err != nil {
		t.Fatal(err)
	}
}

func insertTokenAttempt(t *testing.T, f selfPasswordFixture, id, requestID, started, status string, input, output, cacheRead, cacheWrite any) {
	t.Helper()
	var finished any
	if status != "pending" {
		finished = started
	}
	_, err := f.app.store.db.Exec(`INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,finished_at,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, requestID, "upstream_synthetic_secret", "openai", "primary", started, finished, status, input, output, cacheRead, cacheWrite)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfTokenSummaryDefaultOffRolesOwnershipAndProjection(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	r := tokenSummaryRequest(t, server.URL, "", nil)
	if r.StatusCode != 404 {
		t.Fatalf("default-off=%d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	for role, cookie := range map[string]*http.Cookie{"anonymous": nil, "admin": f.adminCookie} {
		r = tokenSummaryRequest(t, f.server.URL, "", cookie)
		if r.StatusCode != 401 || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s=%d", role, r.StatusCode)
		}
		r.Body.Close()
	}
	key := createTestKey(t, f.server.URL, f.id, "summary-old-key", f.adminCookie, f.adminCSRF)
	request, err := http.NewRequest("GET", f.server.URL+"/self/api/v1/usage/summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key.Key)
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 401 {
		t.Fatalf("Bearer admitted=%d", r.StatusCode)
	}
	r.Body.Close()
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertTokenRequest(t, f, "req_owned", f.id, stamp, "succeeded")
	insertTokenAttempt(t, f, "att_owned", "req_owned", stamp, "succeeded", int64(17), int64(2), nil, nil)
	insertTokenRequest(t, f, "req_other", "other-employee", stamp, "succeeded")
	insertTokenAttempt(t, f, "att_other", "req_other", stamp, "succeeded", int64(999), int64(999), nil, nil)
	r = tokenSummaryRequest(t, f.server.URL, "", f.cookie)
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	var raw map[string]json.RawMessage
	decodeResponse(t, r, &raw)
	if len(raw) != 4 {
		t.Fatalf("unexpected summary fields: %v", raw)
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"req_owned", "att_owned", "req_other", "upstream_synthetic_secret", "provider", "account_id", "cost", "currency", key.Key, "999"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leak %q in %s", forbidden, encoded)
		}
	}
	var summary selfTokenSummaryResponse
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests.Total != "1" || summary.Attempts.Total != "1" || summary.Attempts.InputTokens.KnownTotal != "17" || summary.Attempts.InputTokens.UnknownAttempts != "0" {
		t.Fatalf("wrong owner summary: %+v", summary)
	}
	r = tokenSummaryRequest(t, f.server.URL, "?employee_id=other-employee", f.cookie)
	if r.StatusCode != 400 {
		t.Fatalf("employee selector=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfTokenSummaryWindowKnownUnknownPendingAndRetries(t *testing.T) {
	f := newSelfPasswordFixture(t)
	from, to := "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"
	insertTokenRequest(t, f, "outside_low", f.id, "2025-12-31T23:59:59.999999999Z", "succeeded")
	insertTokenRequest(t, f, "first", f.id, from, "succeeded")
	insertTokenRequest(t, f, "second", f.id, "2026-01-01T00:00:00.000000001Z", "failed")
	insertTokenRequest(t, f, "pending", f.id, "2026-01-01T00:00:00.1Z", "pending")
	insertTokenRequest(t, f, "outside_high", f.id, to, "succeeded")
	insertTokenAttempt(t, f, "known", "first", from, "succeeded", int64(10), int64(0), int64(5), int64(0))
	insertTokenAttempt(t, f, "unknown", "first", from, "failed", nil, nil, nil, nil)
	insertTokenAttempt(t, f, "waiting", "pending", "2026-01-01T00:00:00.1Z", "pending", nil, nil, nil, nil)
	query := "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)
	s := readTokenSummary(t, tokenSummaryRequest(t, f.server.URL, query, f.cookie))
	if s.From != from || s.To != to || s.Requests.Total != "3" || s.Requests.Pending != "1" || s.Requests.Succeeded != "1" || s.Requests.Failed != "1" || s.Requests.Cancelled != "0" || s.Requests.Interrupted != "0" {
		t.Fatalf("request counts=%+v", s)
	}
	if s.Attempts.Total != "3" || s.Attempts.Pending != "1" || s.Attempts.InputTokens != (selfTokenCounts{"10", "1"}) || s.Attempts.OutputTokens != (selfTokenCounts{"0", "1"}) || s.Attempts.CacheReadTokens != (selfTokenCounts{"5", "1"}) || s.Attempts.CacheWriteTokens != (selfTokenCounts{"0", "1"}) {
		t.Fatalf("attempt counts=%+v", s.Attempts)
	}
	// Both timezone offsets and whole-second boundaries normalize to UTC.
	shifted := readTokenSummary(t, tokenSummaryRequest(t, f.server.URL, "?from=2026-01-01T08%3A00%3A00%2B08%3A00&to=2026-01-02T08%3A00%3A00%2B08%3A00", f.cookie))
	if shifted != s {
		t.Fatalf("offset window differs: %+v vs %+v", shifted, s)
	}
}

func TestSelfTokenSummaryRejectsMalformedWindowAndBody(t *testing.T) {
	f := newSelfPasswordFixture(t)
	empty := readTokenSummary(t, tokenSummaryRequest(t, f.server.URL, "", f.cookie))
	if empty.Requests.Total != "0" || empty.Attempts.Total != "0" || empty.Attempts.InputTokens.KnownTotal != "0" || empty.Attempts.InputTokens.UnknownAttempts != "0" {
		t.Fatalf("empty summary=%+v", empty)
	}
	for _, query := range []string{
		"?from=2026-01-01T00:00:00Z", "?to=2026-01-02T00:00:00Z", "?from=2026-01-01T00:00:00.1Z&to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00Z&to=2026-02-02T00:00:00Z", "?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"?from=", "?to=", "?from=2026-01-01T00:00:00Z&from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z",
		"?cursor=bad", "?limit=2", "?employee_id=other", "?from=%ZZ",
	} {
		r := tokenSummaryRequest(t, f.server.URL, query, f.cookie)
		if r.StatusCode != 400 {
			t.Fatalf("query %q=%d", query, r.StatusCode)
		}
		r.Body.Close()
	}
	r := selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/usage/summary", "{}", "", f.cookie, "")
	if r.StatusCode != 400 {
		t.Fatalf("GET body=%d", r.StatusCode)
	}
	r.Body.Close()
	request := httptest.NewRequest("GET", "/self/api/v1/usage/summary", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader(`{"unexpected":true}`))
	response := httptest.NewRecorder()
	f.app.selfTokenSummary(response, request, selfSession{EmployeeID: f.id})
	if response.Code != 400 {
		t.Fatalf("unknown-length body=%d", response.Code)
	}
	from, to, err := parseSelfTokenWindow("", time.Date(2026, 10, 1, 0, 0, 0, 1, time.UTC))
	if err != nil || to.Format(time.RFC3339) != "2026-10-01T00:00:01Z" || to.Sub(from) != 24*time.Hour {
		t.Fatalf("default window %v %v %v", from, to, err)
	}
}

func TestSelfTokenSummaryFailsClosedOnOverflowBadDataSchemaAndCancellation(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertTokenRequest(t, f, "huge", f.id, stamp, "succeeded")
		insertTokenAttempt(t, f, "huge_one", "huge", stamp, "succeeded", int64(math.MaxInt64), nil, nil, nil)
		insertTokenAttempt(t, f, "huge_two", "huge", stamp, "succeeded", int64(1), nil, nil, nil)
		r := tokenSummaryRequest(t, f.server.URL, "", f.cookie)
		if r.StatusCode != 503 {
			t.Fatalf("overflow=%d", r.StatusCode)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		if strings.Contains(string(body), "922337") || strings.Contains(string(body), "huge") {
			t.Fatalf("partial or leaked body: %s", body)
		}
	})
	t.Run("bad numeric type", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertTokenRequest(t, f, "typed", f.id, stamp, "failed")
		insertTokenAttempt(t, f, "typed_attempt", "typed", stamp, "failed", nil, nil, nil, nil)
		if _, err := f.app.store.db.Exec(`UPDATE accounting_attempts SET input_tokens='not-an-integer' WHERE id='typed_attempt'`); err != nil {
			t.Fatal(err)
		}
		r := tokenSummaryRequest(t, f.server.URL, "", f.cookie)
		if r.StatusCode != 503 {
			t.Fatalf("wrong numeric type=%d", r.StatusCode)
		}
		r.Body.Close()
	})
	t.Run("missing table and cancelled context", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := readSelfTokenSummary(ctx, f.app.store.db, f.id, time.Now().UTC().Add(-time.Hour), time.Now().UTC())
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
		if _, err := f.app.store.db.Exec(`DROP TABLE accounting_attempts`); err != nil {
			t.Fatal(err)
		}
		r := tokenSummaryRequest(t, f.server.URL, "", f.cookie)
		if r.StatusCode != 503 {
			t.Fatalf("missing attempts=%d", r.StatusCode)
		}
		r.Body.Close()
	})
}

func TestSelfTokenSummarySessionLifecycleAndConcurrentSnapshot(t *testing.T) {
	f := newSelfPasswordFixture(t)
	// Each transaction inserts a request and exactly one attempt. A reader must
	// never combine request counts from one state with attempt counts from another.
	var wg sync.WaitGroup
	writerErrors := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 15; i++ {
			tx, err := f.app.store.db.Begin()
			if err != nil {
				writerErrors <- err
				return
			}
			id := "concurrent_" + decimal(int64(i))
			stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
			_, err = tx.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,'succeeded')`, id, f.id, "key_"+id, "model", "openai", stamp, stamp)
			if err == nil {
				_, err = tx.Exec(`INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,finished_at,status,input_tokens) VALUES(?,?,?,?,?,?,?,'succeeded',1)`, "att_"+id, id, "account", "openai", "primary", stamp, stamp)
			}
			if err != nil {
				_ = tx.Rollback()
				writerErrors <- err
				return
			}
			if err := tx.Commit(); err != nil {
				writerErrors <- err
				return
			}
		}
	}()
	for i := 0; i < 15; i++ {
		s := readTokenSummary(t, tokenSummaryRequest(t, f.server.URL, "", f.cookie))
		if s.Requests.Total != s.Attempts.Total || s.Attempts.InputTokens.KnownTotal != s.Requests.Total {
			t.Fatalf("non-snapshot counts: %+v", s)
		}
	}
	wg.Wait()
	select {
	case err := <-writerErrors:
		t.Fatal(err)
	default:
	}
	// Lifecycle uses an independent fixture without synthetic attempt rows: a
	// production restart also validates allocation coverage for every attempt.
	lifecycle := newSelfPasswordFixture(t)
	insertTokenRequest(t, lifecycle, "lifecycle_request", lifecycle.id, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "failed")
	selector := strings.Split(lifecycle.cookie.Value, ".")[0]
	if _, err := lifecycle.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), selector); err != nil {
		t.Fatal(err)
	}
	r := tokenSummaryRequest(t, lifecycle.server.URL, "", lifecycle.cookie)
	if r.StatusCode != 401 {
		t.Fatalf("expired=%d", r.StatusCode)
	}
	r.Body.Close()
	r = lifecycle.login(t, selfOldPassword)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("fresh login=%d", r.StatusCode)
	}
	cookie := r.Cookies()[0]
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	decodeResponse(t, r, &login)
	lifecycle.server.Close()
	_ = lifecycle.app.Close()
	restarted := openSelfTestApp(t, lifecycle.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	if s := readTokenSummary(t, tokenSummaryRequest(t, server.URL, "", cookie)); s.Requests.Total != "1" {
		t.Fatalf("restart count=%+v", s)
	}
	r = selfRequestTest(t, "DELETE", server.URL+"/self/api/v1/sessions", "", server.URL, cookie, login.CSRF)
	if r.StatusCode != 204 {
		t.Fatalf("logout=%d", r.StatusCode)
	}
	r.Body.Close()
	r = tokenSummaryRequest(t, server.URL, "", cookie)
	if r.StatusCode != 401 {
		t.Fatalf("logged out=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(lifecycle.id)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("login before disable=%d", r.StatusCode)
	}
	cookie = r.Cookies()[0]
	r.Body.Close()
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r = requestJSON(t, "PATCH", server.URL+"/admin/api/v1/employees/"+lifecycle.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	r = tokenSummaryRequest(t, server.URL, "", cookie)
	if r.StatusCode != 401 {
		t.Fatalf("disabled=%d", r.StatusCode)
	}
	r.Body.Close()
}
