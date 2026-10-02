// Independently authored tests for docs/employee-self-wallet-entry-classification-contract.md.
package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func newSelfClassificationFixture(t *testing.T) selfWalletFixture {
	t.Helper()
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true,
		EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	server := httptest.NewServer(app.Handler())
	t.Cleanup(server.Close)
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	item := selfCreateEmployee(t, server.URL, adminCookie, adminCSRF)
	secret := selfIssue(t, server.URL, item.ID, adminCookie, adminCSRF)
	cookie, csrf := selfRedeem(t, server.URL, item.ID, secret)
	return selfWalletFixture{app: app, server: server, dir: dir, id: item.ID, cookie: cookie, csrf: csrf, adminCookie: adminCookie, adminCSRF: adminCSRF}
}

func selfClassificationRequest(t *testing.T, baseURL, query, body, origin string, cookie *http.Cookie) *http.Response {
	t.Helper()
	return selfRequestTest(t, http.MethodGet, baseURL+selfClassificationPath+query, body, origin, cookie, "")
}

type selfClassificationHTTPPage struct {
	Currency    string `json:"currency"`
	HasAccount  bool   `json:"has_account"`
	WindowStart string `json:"window_start"`
	WindowEnd   string `json:"window_end"`
	Items       []struct {
		OccurredAt string `json:"occurred_at"`
		DeltaMicro string `json:"delta_micro"`
		EntryKind  string `json:"entry_kind"`
	} `json:"items"`
	NextCursor *string `json:"next_cursor"`
}

func readClassificationPage(t *testing.T, r *http.Response) selfClassificationHTTPPage {
	t.Helper()
	defer r.Body.Close()
	if r.StatusCode != 200 || r.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("status=%d cache=%q body=%s", r.StatusCode, r.Header.Get("Cache-Control"), readBody(r))
	}
	var page selfClassificationHTTPPage
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&page); err != nil {
		t.Fatal(err)
	}
	if page.Items == nil || page.Currency == "" || page.WindowStart == "" || page.WindowEnd == "" {
		t.Fatalf("incomplete page=%+v", page)
	}
	return page
}

func TestSelfClassificationGatesRolesAndStrictInput(t *testing.T) {
	for _, cfg := range []Config{{EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true}} {
		if _, err := Open(context.Background(), cfg); err == nil {
			t.Fatalf("accepted missing classification prerequisite: %+v", cfg)
		}
	}
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{{}, {EmployeeSelfServiceEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true},
		{EmployeeSelfServiceEnabled: true, EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true}} {
		cfg.DataDir, cfg.Listen, cfg.Version = dir, "127.0.0.1:0", "test"
		app, err := Open(context.Background(), cfg)
		if err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(app.Handler())
		r := selfClassificationRequest(t, server.URL, "?currency=USD", "", "", nil)
		if r.StatusCode != 404 {
			t.Fatalf("disabled classification config=%+v status=%d", cfg, r.StatusCode)
		}
		r.Body.Close()
		server.Close()
		_ = app.Close()
	}
	f := newSelfClassificationFixture(t)
	for _, cookie := range []*http.Cookie{nil, f.adminCookie} {
		readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "", cookie), 401)
	}
	key := createTestKey(t, f.server.URL, f.id, "classification-role-key", f.adminCookie, f.adminCSRF)
	req, err := http.NewRequest(http.MethodGet, f.server.URL+selfClassificationPath+"?currency=USD", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key.Key)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	readSelfWalletResponse(t, r, 401)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "http://evil.invalid", f.cookie), 403)
	readSelfWalletResponse(t, selfRequestTest(t, http.MethodPost, f.server.URL+selfClassificationPath+"?currency=USD", "", f.server.URL, f.cookie, ""), 405)
	head := selfRequestTest(t, http.MethodHead, f.server.URL+selfClassificationPath+"?currency=USD", "", f.server.URL, f.cookie, "")
	if head.StatusCode != 405 || head.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("HEAD status=%d cache=%q", head.StatusCode, head.Header.Get("Cache-Control"))
	}
	head.Body.Close()
	for _, query := range []string{"", "?currency=", "?currency=usd", "?currency=US", "?currency=USDD", "?currency=%EF%BC%B5SD",
		"?currency=USD&currency=EUR", "?currency=USD&employee_id=x", "?currency=USD&account_id=x", "?currency=USD&key_id=x",
		"?currency=USD&bad=1", "?currency=USD&limit=0", "?currency=USD&limit=51", "?currency=USD&limit=01",
		"?currency=USD&limit=1&limit=2", "?currency=USD&cursor=x", "?currency=USD&limit=1&cursor=", "?currency=USD;bad=x"} {
		value := readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, query, "", "", f.cookie), 400)
		if value["items"] != nil {
			t.Fatalf("invalid query %q leaked page=%v", query, value)
		}
	}
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", `{}`, "", f.cookie), 400)
	features := readSelfWalletResponse(t, selfRequestTest(t, http.MethodGet, f.server.URL+"/self/api/v1/session", "", "", f.cookie, ""), 200)["features"].(map[string]any)
	if features["employee_self_wallet_entry_classification"] != true || features["employee_self_wallet_activity"] != true {
		t.Fatalf("classification feature=%v", features)
	}
	old := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie))
	if old.HasAccount || len(old.Items) != 0 {
		t.Fatalf("old activity route changed=%+v", old)
	}
}

func TestSelfClassificationPageCursorSeparationAndRevocation(t *testing.T) {
	f := newSelfClassificationFixture(t)
	missing := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=JPY", "", "", f.cookie))
	if missing.HasAccount || len(missing.Items) != 0 || missing.NextCursor != nil {
		t.Fatalf("missing=%+v", missing)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}
	activityPost(t, f, "classification-own-credit", owner, "USD", financial.EntryAdjustmentCredit, 42, at)
	activityPost(t, f, "classification-own-debit", owner, "USD", financial.EntryAdjustmentDebit, -7, at.Add(time.Second))
	activityPost(t, f, "classification-other-currency", owner, "EUR", financial.EntryAdjustmentCredit, 11, at)
	other := selfCreateEmployee(t, f.server.URL, f.adminCookie, f.adminCSRF)
	otherSecret := selfIssue(t, f.server.URL, other.ID, f.adminCookie, f.adminCSRF)
	otherCookie, _ := selfRedeem(t, f.server.URL, other.ID, otherSecret)
	activityPost(t, f, "classification-other-employee", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: other.ID}, "USD", financial.EntryAdjustmentCredit, 91, at)
	key := createTestKey(t, f.server.URL, f.id, "classification-owner-key", f.adminCookie, f.adminCSRF)
	activityPost(t, f, "classification-key", financial.Owner{Kind: financial.OwnerKey, EmployeeID: f.id, KeyID: key.ID}, "USD", financial.EntryAdjustmentCredit, 500, at)
	activityPost(t, f, "classification-resource", financial.Owner{Kind: financial.OwnerResource, EmployeeID: f.id, ResourceKind: "response", ResourceID: "one"}, "USD", financial.EntryAdjustmentCredit, 600, at)
	first := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if !first.HasAccount || len(first.Items) != 1 || first.Items[0].EntryKind != "adjustment_debit" || first.Items[0].DeltaMicro != "-7" || first.NextCursor == nil {
		t.Fatalf("first=%+v", first)
	}
	secondQuery := "?currency=USD&limit=1&cursor=" + url.QueryEscape(*first.NextCursor)
	second := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, secondQuery, "", "", f.cookie))
	if len(second.Items) != 1 || second.Items[0].EntryKind != "adjustment_credit" || second.NextCursor != nil || second.WindowEnd != first.WindowEnd {
		t.Fatalf("second=%+v", second)
	}
	readSelfWalletResponse(t, selfActivityRequest(t, f.server.URL, secondQuery, "", "", f.cookie), 400)
	old := readActivityPage(t, selfActivityRequest(t, f.server.URL, "?currency=USD&limit=1", "", "", f.cookie))
	if old.NextCursor == nil || len(old.Items) != 1 {
		t.Fatalf("old page=%+v", old)
	}
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(*old.NextCursor), "", "", f.cookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", otherCookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=EUR&limit=1&cursor="+url.QueryEscape(*first.NextCursor), "", "", f.cookie), 400)
	readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD&limit=2&cursor="+url.QueryEscape(*first.NextCursor), "", "", f.cookie), 400)
	otherPage := readClassificationPage(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "", otherCookie))
	if len(otherPage.Items) != 1 || otherPage.Items[0].DeltaMicro != "91" {
		t.Fatalf("other employee=%+v", otherPage)
	}
	f.server.Close()
	if err := f.app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(context.Background(), Config{DataDir: f.dir, Listen: "127.0.0.1:0", EmployeeSelfServiceEnabled: true,
		EmployeeSelfWalletBalanceEnabled: true, EmployeeSelfWalletActivityEnabled: true, EmployeeSelfWalletEntryClassificationEnabled: true, Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	server := httptest.NewServer(reopened.Handler())
	defer server.Close()
	continued := readClassificationPage(t, selfClassificationRequest(t, server.URL, secondQuery, "", "", f.cookie))
	if len(continued.Items) != 1 || continued.Items[0].DeltaMicro != "42" {
		t.Fatalf("restart continuation=%+v", continued)
	}
	adminCookie, adminCSRF := loginTestAdmin(t, server.URL)
	r := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/employees/"+f.id, `{"expected_revision":1,"status":"disabled"}`, adminCookie, adminCSRF, server.URL)
	if r.StatusCode != 200 {
		t.Fatalf("disable status=%d body=%s", r.StatusCode, readBody(r))
	}
	r.Body.Close()
	readSelfWalletResponse(t, selfClassificationRequest(t, server.URL, secondQuery, "", "", f.cookie), 401)
}

func TestSelfClassificationOrphanActorReturnsOnlyStorageError(t *testing.T) {
	f := newSelfClassificationFixture(t)
	activityPost(t, f, "classification-orphan-actor", financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: f.id}, "USD",
		financial.EntryAdjustmentCredit, 42, time.Now().UTC().Add(-time.Minute).Truncate(time.Second))
	ctx := context.Background()
	conn, err := f.app.store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var triggerDDL string
	if err := conn.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='trigger' AND name='financial_operations_no_update'`).Scan(&triggerDDL); err != nil {
		conn.Close()
		t.Fatal(err)
	}
	for _, statement := range []string{`PRAGMA foreign_keys=OFF`, `DROP TRIGGER financial_operations_no_update`,
		`UPDATE financial_operations SET actor_employee_id='missing-employee' WHERE operation_id='classification-orphan-actor'`,
		triggerDDL, `PRAGMA foreign_keys=ON`} {
		if _, err := conn.ExecContext(ctx, statement); err != nil {
			conn.Close()
			t.Fatal(err)
		}
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	response := readSelfWalletResponse(t, selfClassificationRequest(t, f.server.URL, "?currency=USD", "", "", f.cookie), 503)
	if len(response) != 1 || response["error"] == nil || response["items"] != nil || response["next_cursor"] != nil {
		t.Fatalf("orphan actor leaked a partial page: %v", response)
	}
	errorBody, ok := response["error"].(map[string]any)
	if !ok || errorBody["code"] != "storage_unavailable" {
		t.Fatalf("orphan actor error=%v", response)
	}
}

func TestSelfClassificationMalformedPathsDoNotRedirect(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "off", true: "on"}[enabled], func(t *testing.T) {
			var f selfWalletFixture
			if enabled {
				f = newSelfClassificationFixture(t)
			} else {
				f = newSelfActivityFixture(t)
			}
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			for _, rawPath := range []string{
				"/self/api/v1/billing//entry-classifications?currency=USD",
				"/self/api/v1/billing/./entry-classifications?currency=USD",
				"/self/api/v1/billing/entry-classifications/..?currency=USD",
				"/self/api/v1/billing/%65ntry-classifications?currency=USD",
				"/self/api/v1/billing%2Fentry-classifications?currency=USD",
				"/self/api/v1/billing/entry-classifications%2F?currency=USD",
				"/self/api/v1/billing/entry-classifications/../entries?currency=USD",
			} {
				for _, authenticated := range []bool{false, true} {
					request, err := http.NewRequest(http.MethodGet, f.server.URL+rawPath, nil)
					if err != nil {
						t.Fatal(err)
					}
					if authenticated {
						request.AddCookie(f.cookie)
					}
					response, err := client.Do(request)
					if err != nil {
						t.Fatal(err)
					}
					want := http.StatusNotFound
					if enabled {
						want = http.StatusUnauthorized
						if authenticated {
							want = http.StatusBadRequest
						}
					}
					if response.StatusCode != want || response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Location") != "" {
						t.Fatalf("enabled=%t auth=%t path=%s status=%d location=%q cache=%q", enabled, authenticated, rawPath,
							response.StatusCode, response.Header.Get("Location"), response.Header.Get("Cache-Control"))
					}
					response.Body.Close()
				}
			}
		})
	}
}

func TestSelfClassificationFinalOutputSerializesSessionRevocation(t *testing.T) {
	for _, mutation := range []string{"logout", "password"} {
		t.Run(mutation, func(t *testing.T) {
			f := newSelfClassificationFixture(t)
			atFinal, release := make(chan struct{}), make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			f.app.selfClassificationBeforeWrite = func() { close(atFinal); <-release }
			mutationPath, method, body := "/self/api/v1/sessions", http.MethodDelete, ""
			if mutation == "password" {
				mutationPath, method, body = "/self/api/v1/password", http.MethodPost, selfPasswordBody(selfOldPassword, selfNewPassword)
			}
			arrived := make(chan struct{})
			inner := f.app.Handler()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == mutationPath && r.Method == method {
					close(arrived)
				}
				inner.ServeHTTP(w, r)
			}))
			defer server.Close()
			type result struct {
				response *http.Response
				err      error
			}
			classificationRequest, err := http.NewRequest(http.MethodGet, server.URL+selfClassificationPath+"?currency=USD", nil)
			if err != nil {
				t.Fatal(err)
			}
			classificationRequest.AddCookie(f.cookie)
			classificationDone := make(chan result, 1)
			go func() {
				response, err := http.DefaultClient.Do(classificationRequest)
				classificationDone <- result{response, err}
			}()
			select {
			case <-atFinal:
			case <-time.After(10 * time.Second):
				t.Fatal("classification did not reach final output boundary")
			}
			mutationRequest, err := http.NewRequest(method, server.URL+mutationPath, strings.NewReader(body))
			if err != nil {
				t.Fatal(err)
			}
			mutationRequest.AddCookie(f.cookie)
			mutationRequest.Header.Set("Origin", server.URL)
			mutationRequest.Header.Set("X-CSRF-Token", f.csrf)
			mutationRequest.Header.Set("X-Self-Request", "1")
			if body != "" {
				mutationRequest.Header.Set("Content-Type", "application/json")
			}
			mutationDone := make(chan result, 1)
			go func() {
				response, err := http.DefaultClient.Do(mutationRequest)
				mutationDone <- result{response, err}
			}()
			select {
			case <-arrived:
			case <-time.After(10 * time.Second):
				t.Fatal("session mutation did not arrive")
			}
			select {
			case result := <-mutationDone:
				if result.response != nil {
					result.response.Body.Close()
				}
				t.Fatalf("%s completed before classification output: %v", mutation, result.err)
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			select {
			case result := <-classificationDone:
				if result.err != nil {
					t.Fatal(result.err)
				}
				readClassificationPage(t, result.response)
			case <-time.After(10 * time.Second):
				t.Fatal("classification response stalled")
			}
			select {
			case result := <-mutationDone:
				if result.err != nil {
					t.Fatal(result.err)
				}
				if result.response.StatusCode != http.StatusNoContent {
					t.Fatalf("%s status=%d", mutation, result.response.StatusCode)
				}
				result.response.Body.Close()
			case <-time.After(15 * time.Second):
				t.Fatalf("%s response stalled", mutation)
			}
			readSelfWalletResponse(t, selfClassificationRequest(t, server.URL, "?currency=USD", "", "", f.cookie), 401)
		})
	}
}
