// Independently authored tests for docs/employee-self-key-token-summary-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func keyTokenSummaryRequest(t *testing.T, baseURL, keyID, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/keys/"+url.PathEscape(keyID)+"/usage/summary"+query, "", "", cookie, "")
}

func insertKeyTokenRequest(t *testing.T, f selfPasswordFixture, id, employeeID, keyID, started, status string) {
	t.Helper()
	var finished any
	if status != "pending" {
		finished = started
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,?)`,
		id, employeeID, keyID, "synthetic-model", "openai", started, finished, status); err != nil {
		t.Fatal(err)
	}
}

func TestSelfKeyTokenSummaryDefaultOffRolesAndOwnership(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
		r := selfRequestTest(t, method, server.URL+"/self/api/v1/keys/key_missing/usage/summary", "", "", nil, "")
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("default-off %s=%d", method, r.StatusCode)
		}
		r.Body.Close()
	}
	server.Close()

	f := newSelfPasswordFixture(t)
	owned := createTestKey(t, f.server.URL, f.id, "key-token-owned", f.adminCookie, f.adminCSRF)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	foreign := createTestKey(t, f.server.URL, other.ID, "key-token-foreign", f.adminCookie, f.adminCSRF)
	for name, cookie := range map[string]*http.Cookie{"anonymous": nil, "admin": f.adminCookie} {
		r := keyTokenSummaryRequest(t, f.server.URL, owned.ID, "", cookie)
		if r.StatusCode != http.StatusUnauthorized || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d", name, r.StatusCode)
		}
		r.Body.Close()
	}
	bearer, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/keys/"+owned.ID+"/usage/summary", nil)
	if err != nil {
		t.Fatal(err)
	}
	bearer.Header.Set("Authorization", "Bearer "+owned.Key)
	r, err := http.DefaultClient.Do(bearer)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("model Bearer admitted=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/keys/"+owned.ID+"/usage/summary", "", "http://other.invalid", f.cookie, "")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin=%d", r.StatusCode)
	}
	r.Body.Close()

	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyTokenRequest(t, f, "owned_request", f.id, owned.ID, stamp, "succeeded")
	insertTokenAttempt(t, f, "owned_attempt", "owned_request", stamp, "succeeded", int64(17), int64(0), nil, nil)
	insertKeyTokenRequest(t, f, "foreign_request", other.ID, foreign.ID, stamp, "succeeded")
	insertTokenAttempt(t, f, "foreign_attempt", "foreign_request", stamp, "succeeded", int64(999), nil, nil, nil)
	for _, id := range []string{foreign.ID, "key_not_found"} {
		r = keyTokenSummaryRequest(t, f.server.URL, id, "", f.cookie)
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("foreign/missing %q=%d", id, r.StatusCode)
		}
		body := readBody(r)
		if strings.Contains(body, id) || strings.Contains(body, "999") {
			t.Fatalf("existence/data leak: %s", body)
		}
	}
	r = keyTokenSummaryRequest(t, f.server.URL, owned.ID, "", f.cookie)
	if r.StatusCode != http.StatusOK || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("owned status=%d", r.StatusCode)
	}
	var raw map[string]json.RawMessage
	decodeResponse(t, r, &raw)
	if len(raw) != 4 {
		t.Fatalf("unexpected projection: %v", raw)
	}
	encoded, _ := json.Marshal(raw)
	for _, forbidden := range []string{owned.Key, foreign.Key, "foreign_request", "foreign_attempt", "999", "provider", "cost", "currency", "account_id"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leaked %q: %s", forbidden, encoded)
		}
	}
	var summary selfTokenSummaryResponse
	if err := json.Unmarshal(encoded, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests.Total != "1" || summary.Attempts.Total != "1" || summary.Attempts.InputTokens != (selfTokenCounts{"17", "0"}) {
		t.Fatalf("owned projection=%+v", summary)
	}
}

func TestSelfKeyTokenSummarySlotAndHistoricalStates(t *testing.T) {
	for _, state := range []string{"pending", "cancelled", "issued"} {
		t.Run(state, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			key := createTestKey(t, f.server.URL, f.id, "key-token-slot-"+state, f.adminCookie, f.adminCSRF)
			stamp := utcNow()
			var err error
			switch state {
			case "pending":
				_, err = f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at) VALUES(?,?,'pending',?)`, f.id, key.ID, stamp)
			case "cancelled":
				_, err = f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,created_at,cancelled_at) VALUES(?,?,'cancelled',?,?)`, f.id, key.ID, stamp, stamp)
			case "issued":
				_, err = f.app.store.db.Exec(`INSERT INTO employee_self_key_slots(employee_id,key_id,state,arm_fingerprint,created_at,issued_at) VALUES(?,?,'issued',?,?,?)`, f.id, key.ID, make([]byte, 32), stamp, stamp)
			}
			if err != nil {
				t.Fatal(err)
			}
			r := keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
			want := http.StatusNotFound
			if state == "issued" {
				want = http.StatusOK
			}
			if r.StatusCode != want {
				t.Fatalf("slot %s=%d", state, r.StatusCode)
			}
			r.Body.Close()
		})
	}
	f := newSelfPasswordFixture(t)
	revoked := createTestKey(t, f.server.URL, f.id, "key-token-revoked", f.adminCookie, f.adminCSRF)
	expired := createTestKey(t, f.server.URL, f.id, "key-token-expired", f.adminCookie, f.adminCSRF)
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyTokenRequest(t, f, "revoked_history", f.id, revoked.ID, stamp, "failed")
	insertKeyTokenRequest(t, f, "expired_history", f.id, expired.ID, stamp, "succeeded")
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, stamp, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET expires_at=? WHERE id=?`, stamp, expired.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{revoked.ID, expired.ID} {
		r := keyTokenSummaryRequest(t, f.server.URL, id, "", f.cookie)
		if r.StatusCode != http.StatusOK {
			t.Fatalf("historical %q=%d", id, r.StatusCode)
		}
		var summary selfTokenSummaryResponse
		decodeResponse(t, r, &summary)
		if summary.Requests.Total != "1" {
			t.Fatalf("historical %q=%+v", id, summary)
		}
	}
}

func TestSelfKeyTokenSummaryWindowAttemptsAndBadInput(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "key-token-window", f.adminCookie, f.adminCSRF)
	other := createTestKey(t, f.server.URL, f.id, "key-token-other", f.adminCookie, f.adminCSRF)
	from, to := "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"
	insertKeyTokenRequest(t, f, "outside_low", f.id, key.ID, "2025-12-31T23:59:59.999999999Z", "succeeded")
	insertKeyTokenRequest(t, f, "first", f.id, key.ID, from, "succeeded")
	insertKeyTokenRequest(t, f, "second", f.id, key.ID, "2026-01-01T00:00:00.000000001Z", "failed")
	insertKeyTokenRequest(t, f, "pending", f.id, key.ID, "2026-01-01T00:00:00.1Z", "pending")
	insertKeyTokenRequest(t, f, "outside_high", f.id, key.ID, to, "succeeded")
	insertKeyTokenRequest(t, f, "other_key", f.id, other.ID, from, "succeeded")
	insertTokenAttempt(t, f, "known", "first", from, "succeeded", int64(10), int64(0), int64(5), int64(0))
	insertTokenAttempt(t, f, "unknown", "first", from, "failed", nil, nil, nil, nil)
	insertTokenAttempt(t, f, "waiting", "pending", "2026-01-01T00:00:00.1Z", "pending", nil, nil, nil, nil)
	insertTokenAttempt(t, f, "other_attempt", "other_key", from, "succeeded", int64(999), nil, nil, nil)
	query := "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to)
	r := keyTokenSummaryRequest(t, f.server.URL, key.ID, query, f.cookie)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("window=%d %s", r.StatusCode, readBody(r))
	}
	var summary selfTokenSummaryResponse
	decodeResponse(t, r, &summary)
	if summary.Requests != (selfTokenRequestCounts{"3", "1", "1", "1", "0", "0"}) ||
		summary.Attempts.Total != "3" || summary.Attempts.Pending != "1" ||
		summary.Attempts.InputTokens != (selfTokenCounts{"10", "1"}) ||
		summary.Attempts.OutputTokens != (selfTokenCounts{"0", "1"}) ||
		summary.Attempts.CacheReadTokens != (selfTokenCounts{"5", "1"}) ||
		summary.Attempts.CacheWriteTokens != (selfTokenCounts{"0", "1"}) {
		t.Fatalf("wrong summary: %+v", summary)
	}
	shifted := keyTokenSummaryRequest(t, f.server.URL, key.ID, "?from=2026-01-01T08%3A00%3A00%2B08%3A00&to=2026-01-02T08%3A00%3A00%2B08%3A00", f.cookie)
	var shiftedSummary selfTokenSummaryResponse
	decodeResponse(t, shifted, &shiftedSummary)
	if shiftedSummary != summary {
		t.Fatalf("offset mismatch %+v vs %+v", shiftedSummary, summary)
	}
	for _, bad := range []string{
		"?from=2026-01-01T00:00:00Z", "?to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00.1Z&to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00Z&to=2026-02-02T00:00:00Z",
		"?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"?from=", "?from=2026-01-01T00:00:00Z&from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z",
		"?employee_id=other", "?key_id=" + key.ID, "?from=%ZZ",
	} {
		r := keyTokenSummaryRequest(t, f.server.URL, key.ID, bad, f.cookie)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad query %q=%d", bad, r.StatusCode)
		}
		r.Body.Close()
	}
	r = keyTokenSummaryRequest(t, f.server.URL, "bad!id", "", f.cookie)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad path=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/keys/"+key.ID+"/usage/summary", "{}", "", f.cookie, "")
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("body=%d", r.StatusCode)
	}
	r.Body.Close()
	request := httptest.NewRequest(http.MethodGet, "/self/api/v1/keys/"+key.ID+"/usage/summary", nil)
	request.SetPathValue("id", key.ID)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader(`{"unexpected":true}`))
	response := httptest.NewRecorder()
	f.app.selfKeyTokenSummary(response, request, selfSession{EmployeeID: f.id})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-length body=%d", response.Code)
	}
}

func TestSelfKeyTokenSummaryFailureAndSnapshot(t *testing.T) {
	t.Run("overflow", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-token-overflow", f.adminCookie, f.adminCSRF)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertKeyTokenRequest(t, f, "huge", f.id, key.ID, stamp, "succeeded")
		insertTokenAttempt(t, f, "huge_one", "huge", stamp, "succeeded", int64(math.MaxInt64), nil, nil, nil)
		insertTokenAttempt(t, f, "huge_two", "huge", stamp, "succeeded", int64(1), nil, nil, nil)
		r := keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("overflow=%d", r.StatusCode)
		}
		body := readBody(r)
		if strings.Contains(body, "922337") || strings.Contains(body, "huge") || strings.Contains(body, "known_total") {
			t.Fatalf("partial/leaked body: %s", body)
		}
	})
	t.Run("bad numeric and schema", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-token-schema", f.adminCookie, f.adminCSRF)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertKeyTokenRequest(t, f, "typed", f.id, key.ID, stamp, "failed")
		insertTokenAttempt(t, f, "typed_attempt", "typed", stamp, "failed", nil, nil, nil, nil)
		if _, err := f.app.store.db.Exec(`UPDATE accounting_attempts SET input_tokens='not-an-integer' WHERE id='typed_attempt'`); err != nil {
			t.Fatal(err)
		}
		r := keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable || strings.Contains(readBody(r), "known_total") {
			t.Fatalf("bad numeric not closed: %d", r.StatusCode)
		}
		if _, err := f.app.store.db.Exec(`DROP TABLE accounting_attempts`); err != nil {
			t.Fatal(err)
		}
		r = keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable || strings.Contains(readBody(r), "known_total") {
			t.Fatalf("bad schema not closed: %d", r.StatusCode)
		}
	})
	t.Run("commit fault and cancellation", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-token-commit", f.adminCookie, f.adminCSRF)
		f.app.selfKeyTokenSummaryCommit = func(*sql.Tx) error { return errors.New("synthetic commit fault") }
		r := keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable || strings.Contains(readBody(r), "known_total") {
			t.Fatalf("commit fault not closed: %d", r.StatusCode)
		}
		f.app.selfKeyTokenSummaryCommit = nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := readSelfTokenSummaryScope(ctx, f.app.store.db, f.id, key.ID, time.Now().UTC().Add(-time.Hour), time.Now().UTC(), nil, nil)
		if err == nil {
			t.Fatal("cancelled read succeeded")
		}
	})
	t.Run("ownership and aggregates share snapshot", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-token-snapshot", f.adminCookie, f.adminCSRF)
		other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertKeyTokenRequest(t, f, "before", f.id, key.ID, stamp, "succeeded")
		insertTokenAttempt(t, f, "before_attempt", "before", stamp, "succeeded", int64(7), nil, nil, nil)
		reached := make(chan struct{})
		release := make(chan struct{})
		f.app.selfKeyTokenSummaryAfterOwnership = func() { close(reached); <-release }
		request, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/keys/"+key.ID+"/usage/summary", nil)
		if err != nil {
			t.Fatal(err)
		}
		request.AddCookie(f.cookie)
		type result struct {
			response *http.Response
			err      error
		}
		finished := make(chan result, 1)
		go func() { response, err := http.DefaultClient.Do(request); finished <- result{response, err} }()
		select {
		case <-reached:
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("ownership read did not reach boundary")
		}
		// Production intentionally uses one pooled connection. A second SQLite
		// connection exercises a real WAL writer between the ownership and
		// aggregate SELECTs instead of merely waiting for that pooled connection.
		writerDB, err := sql.Open("sqlite", filepath.Join(f.dir, "cpa-cloud.db"))
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		defer writerDB.Close()
		if _, err := writerDB.Exec(`PRAGMA foreign_keys=ON`); err != nil {
			close(release)
			t.Fatal(err)
		}
		tx, err := writerDB.BeginTx(context.Background(), nil)
		if err != nil {
			close(release)
			t.Fatal(err)
		}
		_, err = tx.Exec(`UPDATE access_keys SET employee_id=? WHERE id=?`, other.ID, key.ID)
		if err == nil {
			_, err = tx.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,'succeeded')`,
				"after", f.id, key.ID, "synthetic-model", "openai", stamp, stamp)
		}
		if err == nil {
			_, err = tx.Exec(`INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,finished_at,status,input_tokens) VALUES(?,?,?,?,?,?,?,'succeeded',9)`,
				"after_attempt", "after", "synthetic-account", "openai", "primary", stamp, stamp)
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
		close(release)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-finished:
			if got.err != nil {
				t.Fatal(got.err)
			}
			if got.response.StatusCode != http.StatusOK {
				t.Fatalf("snapshot response=%d %s", got.response.StatusCode, readBody(got.response))
			}
			var summary selfTokenSummaryResponse
			decodeResponse(t, got.response, &summary)
			if summary.Requests.Total != "1" || summary.Attempts.InputTokens.KnownTotal != "7" {
				t.Fatalf("mixed snapshot=%+v", summary)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("snapshot response did not finish")
		}
		f.app.selfKeyTokenSummaryAfterOwnership = nil
		r := keyTokenSummaryRequest(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("transferred key=%d", r.StatusCode)
		}
		r.Body.Close()
	})
}

func TestSelfKeyTokenSummaryRestartRecovery(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "key-token-restart", f.adminCookie, f.adminCSRF)
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyTokenRequest(t, f, "restart_request", f.id, key.ID, stamp, "failed")
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	r := keyTokenSummaryRequest(t, server.URL, key.ID, "", f.cookie)
	if r.StatusCode != http.StatusOK {
		t.Fatalf("restart=%d %s", r.StatusCode, readBody(r))
	}
	var summary selfTokenSummaryResponse
	decodeResponse(t, r, &summary)
	if summary.Requests.Total != "1" || summary.Attempts.Total != "0" {
		t.Fatalf("restart history=%+v", summary)
	}
}
