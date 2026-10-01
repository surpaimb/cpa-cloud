// Independently authored tests for docs/employee-self-key-revocation-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func selfRevokeBody(password string) string {
	return `{"current_password":` + quoteJSON(password) + `}`
}

func (f selfPasswordFixture) revoke(t *testing.T, id, body string, cookie *http.Cookie, csrf, origin string) *http.Response {
	t.Helper()
	return selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/keys/"+id+"/revoke", body, origin, cookie, csrf)
}

func selfKeyRevokedAt(t *testing.T, f selfPasswordFixture, id string) sql.NullString {
	t.Helper()
	var value sql.NullString
	if err := f.app.store.db.QueryRow(`SELECT revoked_at FROM access_keys WHERE id=?`, id).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func newSelfRevokeModelFixture(t *testing.T) selfPasswordFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, AllowLoopbackUpstream: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	employee := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, employee.ID, adminCookie, adminCSRF)
	cookie, csrf := selfRedeem(t, server.URL, employee.ID, secret)
	return selfPasswordFixture{app, server, employee.ID, adminCookie, adminCSRF, cookie, csrf, dir}
}

func TestSelfKeyRevokeDefaultOffAndIsolation(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/keys/key_off/revoke", selfRevokeBody(selfOldPassword), server.URL, nil, "")
	if r.StatusCode != 404 {
		t.Fatalf("default-off revoke=%d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	owned := createTestKey(t, f.server.URL, f.id, "self-revoke-owned", f.adminCookie, f.adminCSRF)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	foreign := createTestKey(t, f.server.URL, other.ID, "self-revoke-foreign", f.adminCookie, f.adminCSRF)
	for _, tc := range []struct {
		name         string
		cookie       *http.Cookie
		csrf, origin string
		want         int
	}{
		{"anonymous", nil, "", f.server.URL, 401},
		{"admin", f.adminCookie, f.adminCSRF, f.server.URL, 401},
		{"missing origin", f.cookie, f.csrf, "", 403},
		{"foreign origin", f.cookie, f.csrf, "http://evil.invalid", 403},
		{"missing csrf", f.cookie, "", f.server.URL, 403},
		{"wrong csrf", f.cookie, "wrong", f.server.URL, 403},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := f.revoke(t, owned.ID, selfRevokeBody(selfOldPassword), tc.cookie, tc.csrf, tc.origin)
			if r.StatusCode != tc.want || r.Header.Get("Cache-Control") != "no-store" {
				t.Fatalf("status=%d cache=%q", r.StatusCode, r.Header.Get("Cache-Control"))
			}
			r.Body.Close()
		})
	}
	request, err := http.NewRequest("POST", f.server.URL+"/self/api/v1/keys/"+owned.ID+"/revoke", strings.NewReader(selfRevokeBody(selfOldPassword)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", f.server.URL)
	request.Header.Set("X-CSRF-Token", f.csrf)
	request.AddCookie(f.cookie)
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 403 {
		t.Fatalf("missing custom header=%d", r.StatusCode)
	}
	r.Body.Close()
	request, err = http.NewRequest("POST", f.server.URL+"/self/api/v1/keys/"+owned.ID+"/revoke", strings.NewReader(selfRevokeBody(selfOldPassword)))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Origin", f.server.URL)
	request.Header.Set("Authorization", "Bearer "+owned.Key)
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 401 {
		t.Fatalf("Bearer authenticated self=%d", r.StatusCode)
	}
	r.Body.Close()
	var notFound string
	for _, id := range []string{foreign.ID, "key_missing", "bad%20id"} {
		r = f.revoke(t, id, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
		body := readBody(r)
		if r.StatusCode != 404 {
			t.Fatalf("foreign/missing/malformed %s=%d %s", id, r.StatusCode, body)
		}
		if notFound == "" {
			notFound = body
		} else if body != notFound {
			t.Fatalf("404 differs for %s", id)
		}
	}
	if selfKeyRevokedAt(t, f, owned.ID).Valid || selfKeyRevokedAt(t, f, foreign.ID).Valid {
		t.Fatal("isolation attempt changed a Key")
	}
}

func TestSelfKeyRevokeOwnedExpiredIdempotentAndRestart(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "self-revoke-expired", f.adminCookie, f.adminCSRF)
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET expires_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), key.ID); err != nil {
		t.Fatal(err)
	}
	var firstStamp string
	for i := 0; i < 2; i++ {
		r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
		if r.StatusCode != 204 || r.Header.Get("Cache-Control") != "no-store" || readBody(r) != "" {
			t.Fatalf("revoke #%d status=%d", i, r.StatusCode)
		}
		stamp := selfKeyRevokedAt(t, f, key.ID)
		if !stamp.Valid {
			t.Fatal("no revoked timestamp")
		}
		if i == 0 {
			firstStamp = stamp.String
		} else if stamp.String != firstStamp {
			t.Fatal("idempotent retry changed timestamp")
		}
	}
	page := readSelfKeys(t, selfKeysRequest(t, f.server.URL, "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].Status != "revoked" {
		t.Fatalf("inventory=%+v", page)
	}
	f.server.Close()
	_ = f.app.Close()
	restarted := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	r := selfRequestTest(t, "POST", server.URL+"/self/api/v1/keys/"+key.ID+"/revoke", selfRevokeBody(selfOldPassword), server.URL, f.cookie, f.csrf)
	if r.StatusCode != 204 {
		t.Fatalf("restart idempotent revoke=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	var restartedStamp sql.NullString
	if err := restarted.store.db.QueryRow(`SELECT revoked_at FROM access_keys WHERE id=?`, key.ID).Scan(&restartedStamp); err != nil || restartedStamp.String != firstStamp {
		t.Fatalf("restart timestamp changed: %v %+v", err, restartedStamp)
	}
}

func TestSelfKeyRevokeStrictInputAndLimiter(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "self-revoke-input", f.adminCookie, f.adminCSRF)
	for _, body := range []string{`{}`, `{"current_password":1}`, `{"current_password":"x","extra":"y"}`, `{"current_password":"x","current_password":"y"}`, `{"current_password":"x"} trailing`, strings.Repeat("x", 4097), `{"current_password":"` + string([]byte{0xff}) + `"}`, `{"current_password":"short"}`, selfRevokeBody(strings.Repeat("x", 73))} {
		r := f.revoke(t, key.ID, body, f.cookie, f.csrf, f.server.URL)
		if r.StatusCode != 400 && r.StatusCode != 401 {
			t.Fatalf("invalid body=%d %s", r.StatusCode, readBody(r))
		}
		r.Body.Close()
		f.app.clearSelfFailures("127.0.0.1", f.id)
	}
	r := selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/keys/"+key.ID+"/revoke?employee_id=other", selfRevokeBody(selfOldPassword), f.server.URL, f.cookie, f.csrf)
	if r.StatusCode != 400 {
		t.Fatalf("query selector=%d", r.StatusCode)
	}
	r.Body.Close()
	f.app.clearSelfFailures("127.0.0.1", f.id)
	for i := 0; i < 5; i++ {
		r = f.revoke(t, key.ID, selfRevokeBody("wrong-old-password"), f.cookie, f.csrf, f.server.URL)
		if r.StatusCode != 401 || strings.Contains(readBody(r), "wrong-old-password") {
			t.Fatalf("wrong password attempt %d=%d", i, r.StatusCode)
		}
	}
	r = f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
	if r.StatusCode != 429 || r.Header.Get("Retry-After") == "" {
		t.Fatalf("limiter=%d", r.StatusCode)
	}
	r.Body.Close()
	if selfKeyRevokedAt(t, f, key.ID).Valid {
		t.Fatal("limited request revoked Key")
	}
}

func TestSelfKeyRevokeTwoDevicesAndLifecycleRaces(t *testing.T) {
	t.Run("two devices", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "self-revoke-race", f.adminCookie, f.adminCSRF)
		login := f.login(t, selfOldPassword)
		if login.StatusCode != 200 {
			t.Fatalf("second login=%d", login.StatusCode)
		}
		cookie := login.Cookies()[0]
		var session struct {
			CSRF string `json:"csrf_token"`
		}
		decodeResponse(t, login, &session)
		var wg sync.WaitGroup
		statuses := make(chan int, 2)
		for i, credentials := range []struct {
			cookie *http.Cookie
			csrf   string
		}{{f.cookie, f.csrf}, {cookie, session.CSRF}} {
			_ = i
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), credentials.cookie, credentials.csrf, f.server.URL)
				statuses <- r.StatusCode
				r.Body.Close()
			}()
		}
		wg.Wait()
		close(statuses)
		for status := range statuses {
			if status != 204 {
				t.Fatalf("concurrent revoke=%d", status)
			}
		}
		if !selfKeyRevokedAt(t, f, key.ID).Valid {
			t.Fatal("concurrent revoke did not commit")
		}
	})
	for _, lifecycle := range []string{"logout", "password change", "disable", "expire", "admin revoke"} {
		t.Run(lifecycle, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			key := createTestKey(t, f.server.URL, f.id, "self-revoke-lifecycle", f.adminCookie, f.adminCSRF)
			entered, release := make(chan struct{}), make(chan struct{})
			f.app.selfKeyRevokeBeforeTx = func() { close(entered); <-release }
			result := make(chan int, 1)
			go func() {
				r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
				result <- r.StatusCode
				r.Body.Close()
			}()
			<-entered
			var r *http.Response
			switch lifecycle {
			case "logout":
				r = selfRequestTest(t, "DELETE", f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
			case "password change":
				r = f.change(t, f.cookie, f.csrf, selfPasswordBody(selfOldPassword, selfNewPassword), f.server.URL)
			case "disable":
				r = requestJSON(t, "PATCH", f.server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, f.adminCookie, f.adminCSRF, f.server.URL)
			case "expire":
				selector := strings.Split(f.cookie.Value, ".")[0]
				if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), selector); err != nil {
					t.Fatal(err)
				}
			case "admin revoke":
				r = requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+key.ID+"/revoke", `{}`, f.adminCookie, f.adminCSRF, f.server.URL)
			}
			if r != nil {
				if r.StatusCode != 204 && r.StatusCode != 200 {
					t.Fatalf("lifecycle %s=%d %s", lifecycle, r.StatusCode, readBody(r))
				}
				r.Body.Close()
			}
			before := selfKeyRevokedAt(t, f, key.ID)
			close(release)
			status := <-result
			want := 401
			if lifecycle == "admin revoke" {
				want = 204
			}
			if status != want {
				t.Fatalf("after %s revoke=%d want=%d", lifecycle, status, want)
			}
			after := selfKeyRevokedAt(t, f, key.ID)
			if lifecycle == "admin revoke" && (!after.Valid || before.String != after.String) {
				t.Fatal("admin timestamp replaced")
			}
			if lifecycle != "admin revoke" && after.Valid {
				t.Fatal("stale session revoked Key")
			}
		})
	}
}

func TestSelfKeyRevokeStorageAndUnknownCommit(t *testing.T) {
	for _, fault := range []string{"statement", "commit refused", "commit uncertain"} {
		t.Run(fault, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			key := createTestKey(t, f.server.URL, f.id, "self-revoke-fault", f.adminCookie, f.adminCSRF)
			switch fault {
			case "statement":
				if _, err := f.app.store.db.Exec(`CREATE TRIGGER fail_self_revoke BEFORE UPDATE ON access_keys BEGIN SELECT RAISE(ABORT, 'private SQL failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "commit refused":
				f.app.selfKeyRevokeCommit = func(*sql.Tx) error { return errors.New("injected") }
			case "commit uncertain":
				f.app.selfKeyRevokeCommit = func(tx *sql.Tx) error {
					if err := tx.Commit(); err != nil {
						return err
					}
					return errors.New("injected unknown result")
				}
			}
			r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
			body := readBody(r)
			if r.StatusCode != 503 || strings.Contains(body, "private SQL failure") || strings.Contains(body, key.Key) || strings.Contains(body, selfOldPassword) {
				t.Fatalf("fault response=%d %s", r.StatusCode, body)
			}
			if (fault == "commit uncertain") != selfKeyRevokedAt(t, f, key.ID).Valid {
				t.Fatalf("fault %s storage outcome", fault)
			}
			if fault == "commit uncertain" {
				f.app.selfKeyRevokeCommit = nil
				r = f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
				if r.StatusCode != 204 {
					t.Fatalf("idempotent recovery=%d %s", r.StatusCode, readBody(r))
				}
				r.Body.Close()
			}
		})
	}
}

func TestSelfKeyRevokeRejectsCorruptStoredTimestamp(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "self-revoke-corrupt", f.adminCookie, f.adminCSRF)
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET revoked_at='invalid-timestamp' WHERE id=?`, key.ID); err != nil {
		t.Fatal(err)
	}
	r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
	if r.StatusCode != 503 || strings.Contains(readBody(r), "invalid-timestamp") {
		t.Fatalf("corrupt state response=%d", r.StatusCode)
	}
}

func TestSelfKeyRevokeWakesQueuedModelWithoutNewUpstreamAttempt(t *testing.T) {
	var attempts atomic.Int32
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		entered <- struct{}{}
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"finished","object":"chat.completion","choices":[]}`))
	}))
	defer upstream.Close()
	f := newSelfRevokeModelFixture(t)
	account := createModelAdmissionUpstream(t, f.server.URL, f.adminCookie, f.adminCSRF, "self-revoke-queue", "openai-compatible", upstream.URL, "synthetic-upstream-secret")
	createModelAdmissionModel(t, f.server.URL, f.adminCookie, f.adminCSRF, "self-revoke-model", account.ID, "provider-model")
	putModelAdmissionPool(t, f.server.URL, f.adminCookie, f.adminCSRF, "self-revoke-model", account.ID, "provider-model")
	key := createTestKey(t, f.server.URL, f.id, "self-revoke-queued-key", f.adminCookie, f.adminCSRF)
	type modelResult struct {
		status int
		err    error
	}
	modelCall := func() modelResult {
		req, err := http.NewRequest("POST", f.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"self-revoke-model","messages":[]}`))
		if err != nil {
			return modelResult{err: err}
		}
		req.Header.Set("Authorization", "Bearer "+key.Key)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return modelResult{err: err}
		}
		defer resp.Body.Close()
		_ = readBody(resp)
		return modelResult{status: resp.StatusCode}
	}
	active := make(chan modelResult, 1)
	go func() { active <- modelCall() }()
	select {
	case <-entered:
	case outcome := <-active:
		t.Fatalf("active request failed before upstream: %+v", outcome)
	case <-time.After(3 * time.Second):
		t.Fatal("active model request never reached upstream")
	}
	queued := make(chan modelResult, 1)
	go func() { queued <- modelCall() }()
	select {
	case outcome := <-queued:
		t.Fatalf("queued request returned before revoke: %+v", outcome)
	case <-time.After(50 * time.Millisecond):
	}
	r := f.revoke(t, key.ID, selfRevokeBody(selfOldPassword), f.cookie, f.csrf, f.server.URL)
	if r.StatusCode != 204 {
		t.Fatalf("self revoke=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	select {
	case outcome := <-queued:
		if outcome.err != nil || outcome.status != 401 {
			t.Fatalf("queued after revoke=%+v", outcome)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued request did not wake after revoke")
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("new upstream attempts after revoke=%d", got)
	}
	close(release)
	if outcome := <-active; outcome.err != nil || outcome.status != 200 {
		t.Fatalf("in-flight request=%+v", outcome)
	}
	if outcome := modelCall(); outcome.err != nil || outcome.status != 401 {
		t.Fatalf("later dispatch=%+v", outcome)
	}
	if got := attempts.Load(); got != 1 {
		t.Fatalf("late upstream attempts=%d", got)
	}
}
