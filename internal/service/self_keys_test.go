// Independently authored tests for docs/employee-self-key-inventory-contract.md.
package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func selfKeysRequest(t *testing.T, baseURL, query string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, "GET", baseURL+"/self/api/v1/keys"+query, "", "", cookie, "")
}

func readSelfKeys(t *testing.T, response *http.Response) selfKeyPage {
	t.Helper()
	if response.StatusCode != 200 {
		t.Fatalf("self keys status=%d body=%s", response.StatusCode, readBody(response))
	}
	var page selfKeyPage
	decodeResponse(t, response, &page)
	return page
}

func insertSelfKeyRow(t *testing.T, f selfPasswordFixture, id, employeeID, name, created string) {
	t.Helper()
	_, err := f.app.store.db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES(?,?,?,?,?,?,?,?)`, id, employeeID, name, "sel_"+id, []byte("synthetic digest"), 1, "op_"+id, created)
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelfKeyInventoryDefaultOffAndRoles(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	r := selfKeysRequest(t, server.URL, "", nil)
	if r.StatusCode != 404 {
		t.Fatalf("default-off inventory=%d", r.StatusCode)
	}
	r.Body.Close()
	server.Close()
	_ = app.Close()

	f := newSelfPasswordFixture(t)
	key := createTestKey(t, f.server.URL, f.id, "self-keys-role", f.adminCookie, f.adminCSRF)
	for name, cookie := range map[string]*http.Cookie{"anonymous": nil, "admin": f.adminCookie} {
		r = selfKeysRequest(t, f.server.URL, "", cookie)
		if r.StatusCode != 401 || r.Header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s inventory=%d cache=%q", name, r.StatusCode, r.Header.Get("Cache-Control"))
		}
		r.Body.Close()
	}
	bearerRequest, err := http.NewRequest("GET", f.server.URL+"/self/api/v1/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	bearerRequest.Header.Set("Authorization", "Bearer "+key.Key)
	r, err = http.DefaultClient.Do(bearerRequest)
	if err != nil {
		t.Fatal(err)
	}
	if r.StatusCode != 401 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("employee API Key authenticated self inventory: %d", r.StatusCode)
	}
	r.Body.Close()
	r = selfKeysRequest(t, f.server.URL, "", f.cookie)
	page := readSelfKeys(t, r)
	if len(page.Items) != 1 || page.Items[0].ID != key.ID || page.Items[0].Status != "active" {
		t.Fatalf("own inventory=%+v", page)
	}
	r = selfKeysRequest(t, f.server.URL, "?employee_id=other", f.cookie)
	if r.StatusCode != 400 {
		t.Fatalf("employee selector accepted: %d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfKeyInventoryRejectsUnknownLengthBodyWithoutReadingIt(t *testing.T) {
	f := newSelfPasswordFixture(t)
	request := httptest.NewRequest("GET", "/self/api/v1/keys", nil)
	request.ContentLength = -1
	request.Body = io.NopCloser(strings.NewReader(`{"not":"a query"}`))
	request.TransferEncoding = nil // HTTP/2 DATA need not have Transfer-Encoding.
	response := httptest.NewRecorder()
	f.app.selfListKeys(response, request, selfSession{EmployeeID: f.id})
	if response.Code != 400 || strings.Contains(response.Body.String(), "not a query") {
		t.Fatalf("unknown-length body accepted: %d %s", response.Code, response.Body.String())
	}
}

func TestSelfKeyInventoryRejectsHTTP2BodyWithoutContentLength(t *testing.T) {
	f := newSelfPasswordFixture(t)
	server := httptest.NewUnstartedServer(f.app.Handler())
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()
	emptyRequest, err := http.NewRequest("GET", server.URL+"/self/api/v1/keys", nil)
	if err != nil {
		t.Fatal(err)
	}
	emptyRequest.AddCookie(f.cookie)
	emptyResponse, err := server.Client().Do(emptyRequest)
	if err != nil {
		t.Fatal(err)
	}
	if emptyResponse.ProtoMajor != 2 || emptyResponse.StatusCode != 200 {
		t.Fatalf("bodyless HTTP/2 read: proto=%s status=%d", emptyResponse.Proto, emptyResponse.StatusCode)
	}
	emptyResponse.Body.Close()
	request, err := http.NewRequest("GET", server.URL+"/self/api/v1/keys", io.NopCloser(strings.NewReader(`{"unexpected":true}`)))
	if err != nil {
		t.Fatal(err)
	}
	request.ContentLength = -1
	request.AddCookie(f.cookie)
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.ProtoMajor != 2 || response.StatusCode != 400 {
		t.Fatalf("HTTP/2 unknown-length body: proto=%s status=%d", response.Proto, response.StatusCode)
	}
}

func TestSelfKeyInventoryProjectionAndOwnership(t *testing.T) {
	f := newSelfPasswordFixture(t)
	first := createTestKey(t, f.server.URL, f.id, "own-key", f.adminCookie, f.adminCSRF)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	second := createTestKey(t, f.server.URL, other.ID, "other-key", f.adminCookie, f.adminCSRF)
	secret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, secret)
	r := selfKeysRequest(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 200 {
		t.Fatalf("owner read status=%d", r.StatusCode)
	}
	var raw struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if len(raw.Items) != 1 || raw.Items[0]["id"] != first.ID || len(raw.Items[0]) != 6 {
		t.Fatalf("wrong projection: %+v", raw.Items)
	}
	for _, field := range []string{"key", "selector", "digest", "operation_id", "operation_fingerprint", "policy", "source_cidrs", "note", "upstream", "usage", "billing"} {
		if _, found := raw.Items[0][field]; found {
			t.Fatalf("inventory leaked %s", field)
		}
	}
	if raw.Items[0]["created_at"] == nil || raw.Items[0]["expires_at"] != nil || raw.Items[0]["revoked_at"] != nil {
		t.Fatalf("wrong timestamps: %+v", raw.Items[0])
	}
	r = selfKeysRequest(t, f.server.URL, "", otherCookie)
	page := readSelfKeys(t, r)
	if len(page.Items) != 1 || page.Items[0].ID != second.ID {
		t.Fatalf("other employee saw wrong keys: %+v", page.Items)
	}
	if strings.Contains(fmt.Sprint(raw.Items), first.Key) || strings.Contains(fmt.Sprint(page), second.Key) {
		t.Fatal("plaintext Key re-exposed")
	}
}

func TestSelfKeyInventoryStatusAndSessionRevocation(t *testing.T) {
	f := newSelfPasswordFixture(t)
	active := createTestKey(t, f.server.URL, f.id, "status-active", f.adminCookie, f.adminCSRF)
	expired := createTestKey(t, f.server.URL, f.id, "status-expired", f.adminCookie, f.adminCSRF)
	revoked := createTestKey(t, f.server.URL, f.id, "status-revoked", f.adminCookie, f.adminCSRF)
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET expires_at=? WHERE id IN (?,?)`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), expired.ID, revoked.ID); err != nil {
		t.Fatal(err)
	}
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+revoked.ID+"/revoke", `{}`, f.adminCookie, f.adminCSRF, f.server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("admin revoke=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	page := readSelfKeys(t, selfKeysRequest(t, f.server.URL, "", f.cookie))
	statuses := map[string]string{}
	for _, item := range page.Items {
		statuses[item.ID] = item.Status
	}
	if statuses[active.ID] != "active" || statuses[expired.ID] != "expired" || statuses[revoked.ID] != "revoked" {
		t.Fatalf("statuses=%+v", statuses)
	}
	r = selfRequestTest(t, "DELETE", f.server.URL+"/self/api/v1/sessions", "", f.server.URL, f.cookie, f.csrf)
	if r.StatusCode != 204 {
		t.Fatalf("logout=%d", r.StatusCode)
	}
	r.Body.Close()
	r = selfKeysRequest(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 401 {
		t.Fatalf("logged-out inventory=%d", r.StatusCode)
	}
	r.Body.Close()
}

func TestSelfKeyInventoryPaginationValidationAndConcurrentInsert(t *testing.T) {
	f := newSelfPasswordFixture(t)
	created := time.Now().UTC().Format(time.RFC3339Nano)
	for i := 0; i < 54; i++ {
		insertSelfKeyRow(t, f, fmt.Sprintf("key_page_%03d", i), f.id, fmt.Sprintf("Key %d", i), created)
	}
	page := readSelfKeys(t, selfKeysRequest(t, f.server.URL, "", f.cookie))
	if len(page.Items) != 20 || page.NextCursor == nil || page.Items[0].ID != "key_page_000" {
		t.Fatalf("default page=%+v", page)
	}
	all := append([]selfKeyItem{}, page.Items...)
	for page.NextCursor != nil {
		page = readSelfKeys(t, selfKeysRequest(t, f.server.URL, "?cursor="+url.QueryEscape(*page.NextCursor), f.cookie))
		all = append(all, page.Items...)
	}
	if len(all) != 54 || all[53].ID != "key_page_053" {
		t.Fatalf("pages returned %d, last=%+v", len(all), all[len(all)-1])
	}
	first := readSelfKeys(t, selfKeysRequest(t, f.server.URL, "?limit=2", f.cookie))
	insertSelfKeyRow(t, f, "key_page_055", f.id, "New Key", created)
	// A mutation between pages is reflected in the later read, not frozen at
	// the first page's status; the ID cursor still keeps ownership and order.
	revocation := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/key_page_003/revoke", `{}`, f.adminCookie, f.adminCSRF, f.server.URL)
	if revocation.StatusCode != 200 {
		t.Fatalf("between-page revoke=%d %s", revocation.StatusCode, readBody(revocation))
	}
	revocation.Body.Close()
	page = readSelfKeys(t, selfKeysRequest(t, f.server.URL, "?limit=50&cursor="+url.QueryEscape(*first.NextCursor), f.cookie))
	if len(page.Items) != 50 || page.NextCursor == nil {
		t.Fatalf("concurrent insertion page=%+v", page)
	}
	if page.Items[1].ID != "key_page_003" || page.Items[1].Status != "revoked" {
		t.Fatalf("between-page status=%+v", page.Items[1])
	}
	page = readSelfKeys(t, selfKeysRequest(t, f.server.URL, "?limit=50&cursor="+url.QueryEscape(*page.NextCursor), f.cookie))
	if len(page.Items) != 3 || page.Items[2].ID != "key_page_055" {
		t.Fatalf("concurrent insertion final page=%+v", page)
	}
	for _, query := range []string{"?limit=0", "?limit=51", "?limit=01", "?limit=+1", "?limit=-1", "?limit=1&limit=2", "?cursor=", "?cursor=v1.invalid!", "?cursor=" + strings.Repeat("x", 181), "?cursor=v1." + base64.RawURLEncoding.EncodeToString([]byte("bad id")), "?cursor=v1.YQ==", "?employee_id=x", "?%GG=1"} {
		r := selfKeysRequest(t, f.server.URL, query, f.cookie)
		if r.StatusCode != 400 {
			t.Fatalf("accepted query %q: %d", query, r.StatusCode)
		}
		r.Body.Close()
	}
}

func TestSelfKeyInventoryCorruptRowsFailWithoutPartialOutput(t *testing.T) {
	for _, corrupt := range []struct{ name, statement string }{
		{"created", `UPDATE access_keys SET created_at='not a date' WHERE id='key_fault_002'`},
		{"name type", `UPDATE access_keys SET name=x'00ff' WHERE id='key_fault_002'`},
		{"expiry", `UPDATE access_keys SET expires_at='bad expiry' WHERE id='key_fault_002'`},
		{"revocation", `UPDATE access_keys SET revoked_at='bad revocation' WHERE id='key_fault_002'`},
		{"missing schema", `DROP TABLE access_keys`},
	} {
		t.Run(corrupt.name, func(t *testing.T) {
			f := newSelfPasswordFixture(t)
			created := time.Now().UTC().Format(time.RFC3339Nano)
			insertSelfKeyRow(t, f, "key_fault_001", f.id, "First", created)
			insertSelfKeyRow(t, f, "key_fault_002", f.id, "Second", created)
			if _, err := f.app.store.db.Exec(corrupt.statement); err != nil {
				t.Fatal(err)
			}
			r := selfKeysRequest(t, f.server.URL, "", f.cookie)
			body := readBody(r)
			if r.StatusCode != 503 || strings.Contains(body, "First") || strings.Contains(body, "key_fault") || strings.Contains(body, "Second") || strings.Contains(body, "bad expiry") {
				t.Fatalf("partial or unsafe response: %d %s", r.StatusCode, body)
			}
		})
	}
}

func TestSelfKeyInventoryExpiredDisabledAndRestart(t *testing.T) {
	f := newSelfPasswordFixture(t)
	createTestKey(t, f.server.URL, f.id, "restart-key", f.adminCookie, f.adminCSRF)
	selector := strings.Split(f.cookie.Value, ".")[0]
	if _, err := f.app.store.db.Exec(`UPDATE employee_self_sessions SET expires_at=? WHERE selector=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), selector); err != nil {
		t.Fatal(err)
	}
	r := selfKeysRequest(t, f.server.URL, "", f.cookie)
	if r.StatusCode != 401 {
		t.Fatalf("expired session inventory=%d", r.StatusCode)
	}
	r.Body.Close()
	r = f.login(t, selfOldPassword)
	if r.StatusCode != 200 || len(r.Cookies()) != 1 {
		t.Fatalf("new login=%d", r.StatusCode)
	}
	cookie := r.Cookies()[0]
	r.Body.Close()
	f.server.Close()
	_ = f.app.Close()
	restarted := openSelfTestApp(t, f.dir, true)
	server := httptest.NewServer(restarted.Handler())
	defer server.Close()
	page := readSelfKeys(t, selfKeysRequest(t, server.URL, "", cookie))
	if len(page.Items) != 1 {
		t.Fatalf("restart inventory=%+v", page)
	}
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r = requestJSON(t, "PATCH", server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	r = selfKeysRequest(t, server.URL, "", cookie)
	if r.StatusCode != 401 {
		t.Fatalf("disabled inventory=%d", r.StatusCode)
	}
	r.Body.Close()
}
