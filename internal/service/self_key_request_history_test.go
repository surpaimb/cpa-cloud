// Independently authored tests for docs/employee-self-key-request-history-contract.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func keyRequestHistory(t *testing.T, baseURL, keyID, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+"/self/api/v1/keys/"+url.PathEscape(keyID)+"/usage/requests"+query, "", "", cookie, "")
}

func readKeyRequestHistory(t *testing.T, response *http.Response) selfRequestPage {
	t.Helper()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("Key activity status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page selfRequestPage
	decodeResponse(t, response, &page)
	return page
}

func insertKeyActivity(t *testing.T, f selfPasswordFixture, id, employeeID, keyID, started, status string) {
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

func TestSelfKeyRequestHistoryDefaultOffRolesOwnershipAndProjection(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
		r := selfRequestTest(t, method, server.URL+"/self/api/v1/keys/key_missing/usage/requests", "", "", nil, "")
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("default-off %s=%d", method, r.StatusCode)
		}
		r.Body.Close()
	}
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	owned := createTestKey(t, f.server.URL, f.id, "key-activity-owned", f.adminCookie, f.adminCSRF)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	foreign := createTestKey(t, f.server.URL, other.ID, "key-activity-foreign", f.adminCookie, f.adminCSRF)
	for name, cookie := range map[string]*http.Cookie{"anonymous": nil, "admin": f.adminCookie} {
		r := keyRequestHistory(t, f.server.URL, owned.ID, "", cookie)
		if r.StatusCode != http.StatusUnauthorized || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d", name, r.StatusCode)
		}
		r.Body.Close()
	}
	bearer, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/keys/"+owned.ID+"/usage/requests", nil)
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
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/keys/"+owned.ID+"/usage/requests", "", "http://other.invalid", f.cookie, "")
	if r.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin=%d", r.StatusCode)
	}
	r.Body.Close()

	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyActivity(t, f, "owned_activity", f.id, owned.ID, stamp, "succeeded")
	insertKeyActivity(t, f, "foreign_activity", other.ID, foreign.ID, stamp, "succeeded")
	var hiddenBody string
	for _, id := range []string{foreign.ID, "key_not_found"} {
		r = keyRequestHistory(t, f.server.URL, id, "", f.cookie)
		if r.StatusCode != http.StatusNotFound || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("foreign/missing %q=%d", id, r.StatusCode)
		}
		body := readBody(r)
		if hiddenBody != "" && body != hiddenBody {
			t.Fatalf("owner existence hint %q: %q vs %q", id, body, hiddenBody)
		}
		hiddenBody = body
	}
	r = keyRequestHistory(t, f.server.URL, owned.ID, "", f.cookie)
	if r.StatusCode != http.StatusOK || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("owned status=%d", r.StatusCode)
	}
	var projection struct {
		Items []map[string]json.RawMessage `json:"items"`
		Next  *string                      `json:"next_cursor"`
	}
	decodeResponse(t, r, &projection)
	if len(projection.Items) != 1 || projection.Next != nil || len(projection.Items[0]) != 6 ||
		string(projection.Items[0]["id"]) != `"owned_activity"` || string(projection.Items[0]["key_id"]) != quoteJSON(owned.ID) {
		t.Fatalf("unexpected projection: %+v", projection)
	}
	encoded, _ := json.Marshal(projection)
	for _, forbidden := range []string{owned.Key, foreign.Key, "foreign_activity", "provider", "account_id", "input_tokens", "cost", "policy"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSelfKeyRequestHistorySlotAndHistoricalStates(t *testing.T) {
	for _, state := range []string{"pending", "cancelled", "issued"} {
		t.Run(state, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			key := createTestKey(t, f.server.URL, f.id, "key-activity-slot-"+state, f.adminCookie, f.adminCSRF)
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
			r := keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie)
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
	revoked := createTestKey(t, f.server.URL, f.id, "key-activity-revoked", f.adminCookie, f.adminCSRF)
	expired := createTestKey(t, f.server.URL, f.id, "key-activity-expired", f.adminCookie, f.adminCSRF)
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyActivity(t, f, "revoked_activity", f.id, revoked.ID, stamp, "failed")
	insertKeyActivity(t, f, "expired_activity", f.id, expired.ID, stamp, "succeeded")
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, stamp, revoked.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET expires_at=? WHERE id=?`, stamp, expired.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{revoked.ID, expired.ID} {
		page := readKeyRequestHistory(t, keyRequestHistory(t, f.server.URL, id, "", f.cookie))
		if len(page.Items) != 1 || page.Items[0].KeyID != id {
			t.Fatalf("historical %q=%+v", id, page)
		}
	}
}

func TestSelfKeyRequestHistoryWindowCursorAndIsolation(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "key-activity-window", f.adminCookie, f.adminCSRF)
	otherKey := createTestKey(t, f.server.URL, f.id, "key-activity-other", f.adminCookie, f.adminCSRF)
	from, to := "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"
	for _, row := range []struct{ id, at string }{
		{"outside_low", "2025-12-31T23:59:59.999999999Z"},
		{"first", from},
		{"second", "2026-01-01T00:00:00.000000001Z"},
		{"third", "2026-01-01T00:00:00.1Z"},
		{"equal_a", "2026-01-01T01:00:00Z"},
		{"equal_b", "2026-01-01T01:00:00Z"},
		{"outside_high", to},
	} {
		insertKeyActivity(t, f, row.id, f.id, key.ID, row.at, "succeeded")
	}
	insertKeyActivity(t, f, "other_key", f.id, otherKey.ID, "2026-01-01T01:30:00Z", "succeeded")
	query := "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to) + "&limit=1"
	first := readKeyRequestHistory(t, keyRequestHistory(t, f.server.URL, key.ID, query, f.cookie))
	if len(first.Items) != 1 || first.Items[0].ID != "equal_b" || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	for name, path := range map[string]string{
		"other Key": f.server.URL + "/self/api/v1/keys/" + otherKey.ID + "/usage/requests",
		"all Key":   f.server.URL + "/self/api/v1/usage/requests",
	} {
		r := selfRequestTest(t, http.MethodGet, path+"?cursor="+url.QueryEscape(*first.NextCursor), "", "", f.cookie, "")
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s replay=%d", name, r.StatusCode)
		}
		r.Body.Close()
	}
	all := readRequestHistory(t, requestHistory(t, f.server.URL, query, f.cookie))
	if all.NextCursor == nil {
		t.Fatal("missing all-Key cursor")
	}
	r := keyRequestHistory(t, f.server.URL, key.ID, "?cursor="+url.QueryEscape(*all.NextCursor), f.cookie)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("old purpose replay=%d", r.StatusCode)
	}
	r.Body.Close()
	for _, cursor := range []string{*first.NextCursor + "x", "v1.not-base64"} {
		r = keyRequestHistory(t, f.server.URL, key.ID, "?cursor="+url.QueryEscape(cursor), f.cookie)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("forged cursor=%d", r.StatusCode)
		}
		r.Body.Close()
	}
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, secret)
	r = keyRequestHistory(t, f.server.URL, key.ID, "?cursor="+url.QueryEscape(*first.NextCursor), otherCookie)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("cross-employee cursor=%d", r.StatusCode)
	}
	r.Body.Close()
	r = keyRequestHistory(t, f.server.URL, key.ID, "", otherCookie)
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("cross-employee owner=%d", r.StatusCode)
	}
	r.Body.Close()

	insertKeyActivity(t, f, "late_insert", f.id, key.ID, "2026-01-01T00:30:00Z", "pending")
	nextQuery := "?cursor=" + url.QueryEscape(*first.NextCursor) + "&limit=2&from=2026-01-01T08%3A00%3A00%2B08%3A00&to=2026-01-02T08%3A00%3A00%2B08%3A00"
	second := readKeyRequestHistory(t, keyRequestHistory(t, f.server.URL, key.ID, nextQuery, f.cookie))
	if len(second.Items) != 2 || second.Items[0].ID != "equal_a" || second.Items[1].ID != "late_insert" ||
		second.Items[1].FinishedAt != nil || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	third := readKeyRequestHistory(t, keyRequestHistory(t, f.server.URL, key.ID, "?cursor="+url.QueryEscape(*second.NextCursor)+"&limit=3", f.cookie))
	if len(third.Items) != 3 || third.Items[0].ID != "third" || third.Items[1].ID != "second" || third.Items[2].ID != "first" || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}
}

func TestSelfKeyRequestHistoryDefaultWindowAndBadInput(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "key-activity-default", f.adminCookie, f.adminCSRF)
	insertKeyActivity(t, f, "recent", f.id, key.ID, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "pending")
	insertKeyActivity(t, f, "old", f.id, key.ID, time.Now().UTC().Add(-25*time.Hour).Format(time.RFC3339Nano), "succeeded")
	page := readKeyRequestHistory(t, keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].ID != "recent" {
		t.Fatalf("default window=%+v", page)
	}
	for _, bad := range []string{
		"?from=2026-01-01T00:00:00Z", "?to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00.1Z&to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00Z&to=2026-02-02T00:00:00Z",
		"?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"?limit=0", "?limit=01", "?limit=51", "?limit=-1", "?limit=1&limit=2",
		"?cursor=", "?cursor=a&cursor=b", "?key_id=" + key.ID, "?employee_id=other", "?from=%ZZ",
	} {
		r := keyRequestHistory(t, f.server.URL, key.ID, bad, f.cookie)
		if r.StatusCode != http.StatusBadRequest {
			t.Fatalf("bad query %q=%d", bad, r.StatusCode)
		}
		r.Body.Close()
	}
	r := keyRequestHistory(t, f.server.URL, "bad!id", "", f.cookie)
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad path=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/keys/"+key.ID+"/usage/requests", "{}", "", f.cookie, "")
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("body=%d", r.StatusCode)
	}
	r.Body.Close()
	request := httptest.NewRequest(http.MethodGet, "/self/api/v1/keys/"+key.ID+"/usage/requests", nil)
	request.SetPathValue("id", key.ID)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader(`{"unexpected":true}`))
	response := httptest.NewRecorder()
	f.app.selfKeyRequestHistory(response, request, selfSession{EmployeeID: f.id})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unknown-length body=%d", response.Code)
	}
}

func TestSelfKeyRequestHistoryFailureAndSnapshot(t *testing.T) {
	t.Run("corrupt row and schema", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-activity-corrupt", f.adminCookie, f.adminCSRF)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertKeyActivity(t, f, "good_activity", f.id, key.ID, stamp, "succeeded")
		insertKeyActivity(t, f, "bad_activity", f.id, key.ID, stamp, "succeeded")
		if _, err := f.app.store.db.Exec(`UPDATE accounting_requests SET model_id=char(1) WHERE id='bad_activity'`); err != nil {
			t.Fatal(err)
		}
		r := keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("bad row=%d", r.StatusCode)
		}
		body := readBody(r)
		if strings.Contains(body, "good_activity") || strings.Contains(body, "bad_activity") || strings.Contains(body, "items") {
			t.Fatalf("partial response: %s", body)
		}
		if _, err := f.app.store.db.Exec(`DROP TABLE accounting_requests`); err != nil {
			t.Fatal(err)
		}
		r = keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable || strings.Contains(readBody(r), "items") {
			t.Fatalf("missing schema=%d", r.StatusCode)
		}
	})
	t.Run("commit fault and cancellation", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-activity-commit", f.adminCookie, f.adminCSRF)
		f.app.selfKeyRequestCommit = func(*sql.Tx) error { return errors.New("synthetic commit fault") }
		r := keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusServiceUnavailable || strings.Contains(readBody(r), "items") {
			t.Fatalf("commit fault=%d", r.StatusCode)
		}
		f.app.selfKeyRequestCommit = nil
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		q, err := f.app.parseSelfRequestQueryScope("", f.id, key.ID, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.app.readSelfKeyRequestPage(ctx, f.id, key.ID, q); err == nil {
			t.Fatal("cancelled read succeeded")
		}
	})
	t.Run("ownership and page share snapshot", func(t *testing.T) {
		f := newSelfPasswordFixture(t)
		key := createTestKey(t, f.server.URL, f.id, "key-activity-snapshot", f.adminCookie, f.adminCSRF)
		other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
		stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
		insertKeyActivity(t, f, "before_activity", f.id, key.ID, stamp, "succeeded")
		reached := make(chan struct{})
		release := make(chan struct{})
		f.app.selfKeyRequestAfterOwnership = func() { close(reached); <-release }
		request, err := http.NewRequest(http.MethodGet, f.server.URL+"/self/api/v1/keys/"+key.ID+"/usage/requests", nil)
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
		// A second SQLite connection commits a WAL writer between the two
		// SELECTs; the service pool itself intentionally has one connection.
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
				"after_activity", f.id, key.ID, "synthetic-model", "openai", stamp, stamp)
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
			page := readKeyRequestHistory(t, got.response)
			if len(page.Items) != 1 || page.Items[0].ID != "before_activity" {
				t.Fatalf("mixed snapshot=%+v", page)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("snapshot response did not finish")
		}
		f.app.selfKeyRequestAfterOwnership = nil
		r := keyRequestHistory(t, f.server.URL, key.ID, "", f.cookie)
		if r.StatusCode != http.StatusNotFound {
			t.Fatalf("transferred Key=%d", r.StatusCode)
		}
		r.Body.Close()
	})
}

func TestSelfKeyRequestHistoryRestartRecovery(t *testing.T) {
	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "key-activity-restart", f.adminCookie, f.adminCSRF)
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertKeyActivity(t, f, "restart_activity", f.id, key.ID, stamp, "failed")
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	page := readKeyRequestHistory(t, keyRequestHistory(t, server.URL, key.ID, "", f.cookie))
	if len(page.Items) != 1 || page.Items[0].ID != "restart_activity" {
		t.Fatalf("restart=%+v", page)
	}
}
