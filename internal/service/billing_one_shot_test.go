package service

// Independently authored for docs/subscription-one-shot-renewal-contract.md.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func TestBillingOneShotAdminGuardsAndTerminalRead(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	db := f.app.store.db
	if _, err := db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('one-shot-employee','One shot','active','all',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: "one-shot-employee"}
	if _, err := financial.NewLedger(db).Post(context.Background(), financial.Post{OperationID: "one-shot-http-seed", Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: owner.EmployeeID}, ResourceKind: "adjustment", ResourceID: "one-shot-http-seed", ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Entries: []financial.EntryInput{{Owner: owner, Currency: "USD", Kind: financial.EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "one-shot-http-seed"}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at) VALUES('one-shot-http-plan','One shot','USD',10,20,'monthly',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	base := f.server.URL + "/admin/api/v1/billing/subscriptions"
	armBody := `{"operation_id":"20000000-0000-4000-8000-000000000211","expected_revision":1}`
	if status, _ := billingHTTPRequest(t, http.MethodPost, base+"/missing/one-shot-renewal", armBody, nil, "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated arm=%d", status)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	purchaseBody := `{"operation_id":"20000000-0000-4000-8000-000000000210","owner":{"kind":"employee","employee_id":"one-shot-employee"},"plan_id":"one-shot-http-plan"}`
	status, response := billingHTTPRequest(t, http.MethodPost, base, purchaseBody, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("purchase status=%d body=%s", status, response)
	}
	id := billingNestedString(t, response, "subscription", "id")
	url := base + "/" + id + "/one-shot-renewal"
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=0 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, armBody, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusConflict {
		t.Fatalf("commercial-off arm=%d", status)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	if status, _ := billingHTTPRequest(t, http.MethodGet, url, "", nil, "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	if status, response := billingHTTPRequest(t, http.MethodGet, url, "", f.cookie, "", "", nil); status != http.StatusOK || !strings.Contains(string(response), `"state":"none"`) {
		t.Fatalf("initial status=%d body=%s", status, response)
	}
	if status, _ := billingHTTPRequest(t, http.MethodGet, url+"?extra=1", "", f.cookie, "", "", nil); status != http.StatusBadRequest {
		t.Fatalf("query status=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, armBody, f.cookie, "", "", nil); status != http.StatusForbidden {
		t.Fatalf("missing CSRF/Origin=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, armBody, f.cookie, f.csrf, "https://other.invalid", nil); status != http.StatusForbidden {
		t.Fatalf("foreign Origin=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, `{"operation_id":"20000000-0000-4000-8000-000000000212","expected_revision":1,"extra":true}`, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusBadRequest {
		t.Fatalf("extra field=%d", status)
	}
	status, response = billingHTTPRequest(t, http.MethodPost, url, armBody, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"state":"armed"`) {
		t.Fatalf("arm status=%d body=%s", status, response)
	}
	status, response = billingHTTPRequest(t, http.MethodPost, url, armBody, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"replay":true`) {
		t.Fatalf("arm retry status=%d body=%s", status, response)
	}
	disarm := `{"operation_id":"20000000-0000-4000-8000-000000000213","expected_revision":1}`
	status, response = billingHTTPRequest(t, http.MethodPost, url+"/disarm", disarm, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"state":"disarmed"`) {
		t.Fatalf("disarm status=%d body=%s", status, response)
	}
	status, response = billingHTTPRequest(t, http.MethodGet, url, "", f.cookie, "", "", nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"reason":"disarmed"`) {
		t.Fatalf("terminal status=%d body=%s", status, response)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, `{"operation_id":"20000000-0000-4000-8000-000000000214","expected_revision":1}`, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusConflict {
		t.Fatalf("rearm after disarm=%d", status)
	}
}
