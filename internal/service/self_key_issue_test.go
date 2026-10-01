// Independently authored tests for docs/employee-self-key-issuance-contract.md.
package service

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

type selfSlotFixture struct {
	selfPasswordFixture
	keyID string
	hits  *atomic.Int64
}

func newSelfSlotFixture(t *testing.T) selfSlotFixture {
	t.Helper()
	hits := new(atomic.Int64)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"test","object":"chat.completion","choices":[]}`))
	}))
	t.Cleanup(upstream.Close)
	f := newSelfRevokeModelFixture(t)
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/upstreams", `{"name":"slot mock","provider_kind":"openai-compatible","endpoint":`+quoteJSON(upstream.URL)+`,"api_key":"mock-secret"}`, f.adminCookie, f.adminCSRF, f.server.URL)
	if r.StatusCode != 201 {
		t.Fatalf("upstream: %d %s", r.StatusCode, readBody(r))
	}
	var upstreamObject upstreamView
	decodeResponse(t, r, &upstreamObject)
	r = requestJSON(t, "POST", f.server.URL+"/admin/api/v1/models", `{"id":"slot-model","upstream_id":`+quoteJSON(upstreamObject.ID)+`,"upstream_model":"provider-model"}`, f.adminCookie, f.adminCSRF, f.server.URL)
	if r.StatusCode != 201 {
		t.Fatalf("model: %d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	return selfSlotFixture{selfPasswordFixture: f, hits: hits}
}

func (f *selfSlotFixture) reserve(t *testing.T, operation string) int {
	t.Helper()
	body := `{"name":"Employee self Key","operation_id":` + quoteJSON(operation) + `,"expires_at":null,"policy":{"protocol_mode":"selected","protocols":["openai-chat"],"model_mode":"selected","models":["slot-model"],"source_mode":"all","source_cidrs":[],"account_group_mode":"all","account_group_ids":[]}}`
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/employees/"+f.id+"/self-key-slot", body, f.adminCookie, f.adminCSRF, f.server.URL)
	status := r.StatusCode
	if status == 201 || status == 200 {
		var result struct {
			ID  string `json:"id"`
			Key string `json:"key"`
		}
		decodeResponse(t, r, &result)
		if result.ID == "" || result.Key != "" {
			t.Fatalf("reservation exposed no valid slot or plaintext")
		}
		f.keyID = result.ID
	} else {
		r.Body.Close()
	}
	return status
}

func (f selfSlotFixture) limits(t *testing.T) {
	t.Helper()
	stamp := utcNow()
	for _, query := range []string{
		`UPDATE governance_settings SET enabled=1,budget_enabled=1,revision=revision+1,updated_at=? WHERE singleton=1`,
	} {
		if _, err := f.app.store.db.Exec(query, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO governance_policies(id,scope_kind,scope_id,enabled,rpm_limit,concurrency_limit,unknown_mode,revision,created_at,updated_at) VALUES(?, 'key', ?,1,2,1,'shadow',1,?,?)`, "slot-governance-"+f.keyID, f.keyID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO governance_general_budget_policies(id,scope_kind,scope_id,protocol,model,enabled,token_limit,token_window,currency,cost_window,revision,created_at,updated_at) VALUES(?,'key',?,'','',1,200,'rolling_60s','','',1,?,?)`, "slot-budget-"+f.keyID, f.keyID, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func (f selfSlotFixture) arm(t *testing.T) int {
	t.Helper()
	var employeeRevision, policyRevision int64
	if err := f.app.store.db.QueryRow(`SELECT revision FROM employees WHERE id=?`, f.id).Scan(&employeeRevision); err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.db.QueryRow(`SELECT revision FROM access_key_policies WHERE key_id=?`, f.keyID).Scan(&policyRevision); err != nil {
		t.Fatal(err)
	}
	body := `{"expected_employee_revision":` + strconv.FormatInt(employeeRevision, 10) + `,"expected_key_policy_revision":` + strconv.FormatInt(policyRevision, 10) + `}`
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+f.keyID+"/self-key-slot/arm", body, f.adminCookie, f.adminCSRF, f.server.URL)
	status := r.StatusCode
	r.Body.Close()
	return status
}

func (f selfSlotFixture) issue(t *testing.T, password string) *http.Response {
	t.Helper()
	return selfRequestTest(t, "POST", f.server.URL+"/self/api/v1/keys/issue", `{"slot_id":`+quoteJSON(f.keyID)+`,"current_password":`+quoteJSON(password)+`}`, f.server.URL, f.cookie, f.csrf)
}

func TestSelfKeySlotDefaultOffAndOneLifetimeReservation(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openSelfTestApp(t, dir, false)
	server := httptest.NewServer(app.Handler())
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	var r *http.Response
	for _, path := range []string{"/self/api/v1/key-slots", "/self/api/v1/keys/issue"} {
		for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			for _, role := range []string{"anonymous", "admin"} {
				var cookie *http.Cookie
				csrf := ""
				if role == "admin" {
					cookie, csrf = adminCookie, adminCSRF
				}
				r = selfRequestTest(t, method, server.URL+path, `{}`, server.URL, cookie, csrf)
				if r.StatusCode != http.StatusNotFound || r.Header.Get("Allow") != "" {
					t.Fatalf("default-off %s %s as %s: status=%d Allow=%q", method, path, role, r.StatusCode, r.Header.Get("Allow"))
				}
				r.Body.Close()
			}
		}
	}
	for _, path := range []string{
		"/admin/api/v1/employees/emp_missing/self-key-slot",
		"/admin/api/v1/keys/key_missing/self-key-slot/arm",
		"/admin/api/v1/keys/key_missing/self-key-slot/cancel",
	} {
		for _, method := range []string{http.MethodPost, http.MethodGet, http.MethodOptions} {
			for _, role := range []string{"admin", "anonymous"} {
				var cookie *http.Cookie
				csrf := ""
				if role == "admin" {
					cookie, csrf = adminCookie, adminCSRF
				}
				r = requestJSON(t, method, server.URL+path, `{}`, cookie, csrf, server.URL)
				if r.StatusCode != http.StatusNotFound || r.Header.Get("Allow") != "" {
					t.Fatalf("default-off %s %s as %s: status=%d Allow=%q", method, path, role, r.StatusCode, r.Header.Get("Allow"))
				}
				r.Body.Close()
			}
		}
	}
	server.Close()
	_ = app.Close()

	f := newSelfSlotFixture(t)
	if status := f.reserve(t, "slot-op-one"); status != 201 {
		t.Fatalf("reserve=%d", status)
	}
	firstID := f.keyID
	if status := f.reserve(t, "slot-op-one"); status != 200 || f.keyID != firstID {
		t.Fatalf("retry=%d id=%q", status, f.keyID)
	}
	if status := f.reserve(t, "slot-op-other"); status != 409 {
		t.Fatalf("second slot=%d", status)
	}
	f.keyID = firstID
	var selector string
	if err := f.app.store.db.QueryRow(`SELECT selector FROM access_keys WHERE id=?`, firstID).Scan(&selector); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(selector, "slot_") {
		t.Fatalf("dormant selector=%q", selector)
	}
	// Even a test-known matching digest must not turn a pending row into authority.
	forgedSecret := strings.Repeat("s", 43)
	if _, err := f.app.store.db.Exec(`UPDATE access_keys SET digest=? WHERE id=?`, f.app.secrets.digest("employee-key/v1\x00"+selector, forgedSecret), firstID); err != nil {
		t.Fatal(err)
	}
	forged := "cpac_" + selector + "." + forgedSecret
	if _, valid := f.app.lookupEmployeeKey(context.Background(), forged); valid {
		t.Fatal("pending slot passed Bearer authentication")
	}
	modelsRequest, _ := http.NewRequest("GET", f.server.URL+"/v1/models", nil)
	modelsRequest.Header.Set("Authorization", "Bearer "+forged)
	modelsResponse, err := http.DefaultClient.Do(modelsRequest)
	if err != nil || modelsResponse.StatusCode != 401 {
		t.Fatalf("pending model directory status=%v err=%v", statusOf(modelsResponse), err)
	}
	modelsResponse.Body.Close()
	r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/keys", "", "", f.cookie, "")
	if strings.Contains(readBody(r), firstID) {
		t.Fatal("pending slot appeared in self inventory")
	}
	r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/key-slots", "", "", f.cookie, "")
	if strings.Contains(readBody(r), firstID) {
		t.Fatal("pending slot appeared as armed")
	}
	if status := f.arm(t); status != 409 {
		t.Fatalf("arm without limits=%d", status)
	}
	f.limits(t)
	if status := f.arm(t); status != 200 {
		t.Fatalf("arm with limits=%d", status)
	}
	r = selfRequestTest(t, "GET", f.server.URL+"/self/api/v1/key-slots", "", "", f.cookie, "")
	if !strings.Contains(readBody(r), firstID) {
		t.Fatal("armed own slot missing")
	}
	if _, err := f.app.store.db.Exec(`UPDATE governance_general_budget_policies SET token_limit=201,revision=revision+1 WHERE scope_id=?`, firstID); err != nil {
		t.Fatal(err)
	}
	r = f.issue(t, selfOldPassword)
	if r.StatusCode != 409 || strings.Contains(readBody(r), "cpac_") {
		t.Fatal("changed budget activated slot")
	}
	if status := f.arm(t); status != 200 {
		t.Fatalf("rearm=%d", status)
	}
	r = f.issue(t, "wrong-password-indeed")
	if r.StatusCode != 401 {
		t.Fatalf("wrong password=%d", r.StatusCode)
	}
	r.Body.Close()
	r = f.issue(t, selfOldPassword)
	if r.StatusCode != 201 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("issue=%d cache=%q", r.StatusCode, r.Header.Get("Cache-Control"))
	}
	var issued struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	decodeResponse(t, r, &issued)
	if issued.ID != firstID || !strings.HasPrefix(issued.Key, "cpac_") {
		t.Fatalf("issue did not preserve Key ID")
	}
	r = f.issue(t, selfOldPassword)
	if r.StatusCode != 409 || strings.Contains(readBody(r), issued.Key) {
		t.Fatal("retry re-exposed Key")
	}
	var count int
	if err := f.app.store.db.QueryRow(`SELECT COUNT(*) FROM access_keys WHERE employee_id=?`, f.id).Scan(&count); err != nil || count != 1 {
		t.Fatalf("access Key count=%d err=%v", count, err)
	}
	if f.hits.Load() != 0 {
		t.Fatal("pre-issue traffic reached upstream")
	}
}

func TestSelfKeySlotConcurrentIssueAndUnknownCommit(t *testing.T) {
	f := newSelfSlotFixture(t)
	if status := f.reserve(t, "slot-race"); status != 201 {
		t.Fatalf("reserve=%d", status)
	}
	f.limits(t)
	if status := f.arm(t); status != 200 {
		t.Fatalf("arm=%d", status)
	}
	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r := f.issue(t, selfOldPassword); statuses <- r.StatusCode; r.Body.Close() }()
	}
	wg.Wait()
	close(statuses)
	created, conflict := 0, 0
	for status := range statuses {
		if status == 201 {
			created++
		} else if status == 409 {
			conflict++
		} else {
			t.Fatalf("race status=%d", status)
		}
	}
	if created != 1 || conflict != 1 {
		t.Fatalf("race created=%d conflict=%d", created, conflict)
	}

	g := newSelfSlotFixture(t)
	if status := g.reserve(t, "slot-unknown"); status != 201 {
		t.Fatalf("reserve=%d", status)
	}
	g.limits(t)
	if status := g.arm(t); status != 200 {
		t.Fatalf("arm=%d", status)
	}
	g.app.selfKeyIssueCommit = func(tx *sql.Tx) error {
		if err := tx.Commit(); err != nil {
			return err
		}
		return errors.New("ambiguous commit")
	}
	r := g.issue(t, selfOldPassword)
	if r.StatusCode != 503 || strings.Contains(readBody(r), "cpac_") {
		t.Fatal("unknown commit exposed success")
	}
	g.app.selfKeyIssueCommit = nil
	r = g.issue(t, selfOldPassword)
	if r.StatusCode != 409 {
		t.Fatalf("retry after committed unknown=%d", r.StatusCode)
	}
	r.Body.Close()
	var state string
	if err := g.app.store.db.QueryRow(`SELECT state FROM employee_self_key_slots WHERE key_id=?`, g.keyID).Scan(&state); err != nil || state != "issued" {
		t.Fatalf("state=%q err=%v", state, err)
	}
}

func TestSelfKeySlotCancel(t *testing.T) {
	f := newSelfSlotFixture(t)
	if status := f.reserve(t, "slot-cancel"); status != 201 {
		t.Fatalf("reserve=%d", status)
	}
	f.limits(t)
	if status := f.arm(t); status != 200 {
		t.Fatalf("arm=%d", status)
	}
	r := requestJSON(t, "POST", f.server.URL+"/admin/api/v1/keys/"+f.keyID+"/revoke", `{}`, f.adminCookie, f.adminCSRF, f.server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("admin revoke=%d %s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	var state string
	if err := f.app.store.db.QueryRow(`SELECT state FROM employee_self_key_slots WHERE key_id=?`, f.keyID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("state=%q err=%v", state, err)
	}
	r = f.issue(t, selfOldPassword)
	if r.StatusCode != 409 {
		t.Fatalf("cancelled issue=%d", r.StatusCode)
	}
	r.Body.Close()
	if status := f.reserve(t, "slot-after-cancel"); status != 409 {
		t.Fatalf("reservation reused tombstone=%d", status)
	}
	if _, err := f.app.store.db.Exec(`DELETE FROM access_keys WHERE id=?`, f.keyID); err == nil {
		t.Fatal("RESTRICT tombstone allowed Key deletion")
	}
}

func TestSelfKeySlotPendingRestart(t *testing.T) {
	f := newSelfSlotFixture(t)
	if status := f.reserve(t, "slot-restart"); status != 201 {
		t.Fatalf("reserve=%d", status)
	}
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true, AllowLoopbackUpstream: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	var state string
	if err := reopened.store.db.QueryRow(`SELECT state FROM employee_self_key_slots WHERE key_id=?`, f.keyID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("restart state=%q err=%v", state, err)
	}
}
