// Independently authored tests for docs/employee-self-request-history-contract.md.
package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func requestHistory(t *testing.T, baseURL, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, "GET", baseURL+"/self/api/v1/usage/requests"+query, "", "", cookie, "")
}

func readRequestHistory(t *testing.T, response *http.Response) selfRequestPage {
	t.Helper()
	if response.StatusCode != 200 {
		t.Fatalf("history status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page selfRequestPage
	decodeResponse(t, response, &page)
	return page
}

func insertHistoryRow(t *testing.T, f selfPasswordFixture, id, employeeID, started, status string) {
	t.Helper()
	var finished any
	if status != "pending" {
		finished = started
	}
	_, err := f.app.store.db.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,?)`,
		id, employeeID, "key_"+id, "synthetic-model", "openai", started, finished, status)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfRequestHistoryDefaultOffRolesAndProjection(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	r := requestHistory(t, server.URL, "", nil)
	if r.StatusCode != 404 {
		t.Fatalf("default-off=%d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	for name, cookie := range map[string]*http.Cookie{"anonymous": nil, "admin": f.adminCookie} {
		r = requestHistory(t, f.server.URL, "", cookie)
		if r.StatusCode != 401 || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s status=%d", name, r.StatusCode)
		}
		r.Body.Close()
	}
	key := createTestKey(t, f.server.URL, f.id, "history-key", f.adminCookie, f.adminCSRF)
	request, err := http.NewRequest("GET", f.server.URL+"/self/api/v1/usage/requests", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key.Key)
	r, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 401 {
		t.Fatalf("bearer status=%d", r.StatusCode)
	}
	r.Body.Close()
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertHistoryRow(t, f, "req_owned", f.id, stamp, "failed")
	insertHistoryRow(t, f, "req_other", "other-employee", stamp, "succeeded")
	r = requestHistory(t, f.server.URL, "", f.cookie)
	if r.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("missing no-store")
	}
	var payload struct {
		Items []map[string]json.RawMessage `json:"items"`
		Next  *string                      `json:"next_cursor"`
	}
	decodeResponse(t, r, &payload)
	if len(payload.Items) != 1 || payload.Next != nil {
		t.Fatalf("wrong page: %+v", payload)
	}
	for _, key := range []string{"id", "key_id", "model_id", "status", "started_at", "finished_at"} {
		if _, ok := payload.Items[0][key]; !ok {
			t.Fatalf("missing %s", key)
		}
	}
	if len(payload.Items[0]) != 6 || string(payload.Items[0]["id"]) != `"req_owned"` {
		t.Fatalf("projection or ownership failed: %+v", payload.Items[0])
	}
	r = requestHistory(t, f.server.URL, "?employee_id=other-employee", f.cookie)
	if r.StatusCode != 400 {
		t.Fatalf("employee selector=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfRequestHistoryNanosecondOrderCursorAndIsolation(t *testing.T) {
	f := newSelfPasswordFixture(t)
	from, to := "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z"
	for _, row := range []struct{ id, at string }{
		{"outside_lower", "2025-12-31T23:59:59.999999999Z"},
		{"first", from},
		{"second", "2026-01-01T00:00:00.1Z"},
		{"third", "2026-01-01T00:00:00.100000001Z"},
		{"equal_a", "2026-01-01T01:00:00Z"},
		{"equal_b", "2026-01-01T01:00:00Z"},
		{"outside_upper", to},
	} {
		insertHistoryRow(t, f, row.id, f.id, row.at, "succeeded")
	}
	query := "?from=" + url.QueryEscape(from) + "&to=" + url.QueryEscape(to) + "&limit=2"
	first := readRequestHistory(t, requestHistory(t, f.server.URL, query, f.cookie))
	if len(first.Items) != 2 || first.Items[0].ID != "equal_b" || first.Items[1].ID != "equal_a" || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	r := requestHistory(t, f.server.URL, "?cursor="+url.QueryEscape(*first.NextCursor), otherCookie)
	if r.StatusCode != 400 {
		t.Fatalf("cross employee cursor=%d", r.StatusCode)
	}
	r.Body.Close()
	for _, cursor := range []string{*first.NextCursor + "x", "v1.not-base64"} {
		r = requestHistory(t, f.server.URL, "?cursor="+url.QueryEscape(cursor), f.cookie)
		if r.StatusCode != 400 {
			t.Fatalf("forged cursor=%d", r.StatusCode)
		}
		r.Body.Close()
	}
	r = requestHistory(t, f.server.URL, "?cursor="+url.QueryEscape(*first.NextCursor)+"&from=2026-01-01T00:00:01Z&to="+url.QueryEscape(to), f.cookie)
	if r.StatusCode != 400 {
		t.Fatalf("mismatched window=%d", r.StatusCode)
	}
	r.Body.Close()
	insertHistoryRow(t, f, "late_insert", f.id, "2026-01-01T00:00:00.2Z", "pending")
	second := readRequestHistory(t, requestHistory(t, f.server.URL, "?cursor="+url.QueryEscape(*first.NextCursor)+"&limit=2", f.cookie))
	if len(second.Items) != 2 || second.Items[0].ID != "late_insert" || second.Items[1].ID != "third" || second.NextCursor == nil {
		t.Fatalf("second=%+v", second)
	}
	third := readRequestHistory(t, requestHistory(t, f.server.URL, "?cursor="+url.QueryEscape(*second.NextCursor), f.cookie))
	if len(third.Items) != 2 || third.Items[0].ID != "second" || third.Items[1].ID != "first" || third.NextCursor != nil {
		t.Fatalf("third=%+v", third)
	}
}

func TestSelfRequestHistoryRejectsMalformedRequests(t *testing.T) {
	f := newSelfPasswordFixture(t)
	for _, query := range []string{
		"?from=2026-01-01T00:00:00Z", "?to=2026-01-01T00:00:00Z", "?from=2026-01-01T00:00:00.1Z&to=2026-01-02T00:00:00Z",
		"?from=2026-01-01T00:00:00Z&to=2026-02-02T00:00:00Z", "?from=2026-01-02T00:00:00Z&to=2026-01-01T00:00:00Z",
		"?limit=0", "?limit=01", "?limit=51", "?limit=-1", "?limit=1&limit=2", "?cursor=", "?cursor=a&cursor=b", "?employee_id=other", "?from=%ZZ",
	} {
		r := requestHistory(t, f.server.URL, query, f.cookie)
		if r.StatusCode != 400 {
			t.Fatalf("query %q status=%d", query, r.StatusCode)
		}
		r.Body.Close()
	}
	r := selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/usage/requests", "{}", "", f.cookie, "")
	if r.StatusCode != 400 {
		t.Fatalf("GET body=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfRequestHistoryFailsClosedOnStorageFault(t *testing.T) {
	f := newSelfPasswordFixture(t)
	stamp := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)
	insertHistoryRow(t, f, "good", f.id, stamp, "succeeded")
	insertHistoryRow(t, f, "bad", f.id, stamp, "succeeded")
	if _, err := f.app.store.db.Exec(`UPDATE accounting_requests SET model_id=char(1) WHERE id='bad'`); err != nil {
		t.Fatal(err)
	}
	r := requestHistory(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 503 {
		t.Fatalf("corrupt row=%d", r.StatusCode)
	}
	body, err := io.ReadAll(r.Body)
	r.Body.Close()
	if err != nil || strings.Contains(string(body), "good") || strings.Contains(string(body), "bad") {
		t.Fatalf("partial response: %s, %v", body, err)
	}
	if _, err := f.app.store.db.Exec(`DROP TABLE accounting_requests`); err != nil {
		t.Fatal(err)
	}
	r = requestHistory(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 503 {
		t.Fatalf("missing table=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfRequestHistorySessionLifecycleAndUnknownLengthBody(t *testing.T) {
	f := newSelfPasswordFixture(t)
	insertHistoryRow(t, f, "req_lifecycle", f.id, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), "pending")
	request := httptest.NewRequest("GET", "/self/api/v1/usage/requests", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader(`{"unexpected":true}`))
	response := httptest.NewRecorder()
	f.app.selfRequestHistory(response, request, selfSession{EmployeeID: f.id})
	if response.Code != 400 {
		t.Fatalf("unknown-length body=%d", response.Code)
	}
	selector := strings.Split(f.cookie.Value, ".")[0]
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano), selector); err != nil {
		t.Fatal(err)
	}
	r := requestHistory(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 401 {
		t.Fatalf("expired session=%d", r.StatusCode)
	}
	r.Body.Close()
	r = f.login(t, selfOldPassword)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("fresh login=%d", r.StatusCode)
	}
	cookie := r.Cookies()[0]
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	decodeResponse(t, r, &login)
	f.server.Close()
	_ = f.app.Close()
	restarted := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	page := readRequestHistory(t, requestHistory(t, server.URL, "", cookie))
	if len(page.Items) != 1 || page.Items[0].ID != "req_lifecycle" || page.Items[0].Status != "interrupted" || page.Items[0].FinishedAt == nil {
		t.Fatalf("restart page=%+v", page)
	}
	r = selfRequestTest(t, "DELETE", server.URL+"/self/api/v1/sessions", "", server.URL, cookie, login.CSRF)
	if r.StatusCode != 204 {
		t.Fatalf("logout=%d", r.StatusCode)
	}
	r.Body.Close()
	r = requestHistory(t, server.URL, "", cookie)
	if r.StatusCode != 401 {
		t.Fatalf("logged out=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfRequestTest(t, "POST", server.URL+"/self/api/v1/sessions", `{"employee_id":`+quoteJSON(f.id)+`,"password":"a-long-self-password"}`, server.URL, nil, "")
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("login before disable=%d", r.StatusCode)
	}
	cookie = r.Cookies()[0]
	r.Body.Close()
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r = requestJSON(t, "PATCH", server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	r = requestHistory(t, server.URL, "", cookie)
	if r.StatusCode != 401 {
		t.Fatalf("disabled employee=%d", r.StatusCode)
	}
	r.Body.Close()
}
