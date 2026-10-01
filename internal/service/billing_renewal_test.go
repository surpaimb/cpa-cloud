package service

// Independently authored for docs/subscription-manual-renewal-contract.md.

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/financial"
)

func TestBillingManualRenewalAdminBoundaryAndProjection(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.app.subscriptionExpiry.Close()
	f.app.subscriptionExpiry = nil // Keep the predecessor stored active past its end.
	db := f.app.store.db
	if _, err := db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('renew-employee','Renew','active','all',1,'2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	owner := financial.Owner{Kind: financial.OwnerEmployee, EmployeeID: "renew-employee"}
	posted, err := financial.NewLedger(db).Post(context.Background(), financial.Post{OperationID: "renew-http-seed", Action: "adjustment", Actor: financial.Actor{Kind: financial.ActorEmployee, ID: owner.EmployeeID}, ResourceKind: "adjustment", ResourceID: "renew-http-seed", ObservedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Entries: []financial.EntryInput{{Owner: owner, Currency: "USD", Kind: financial.EntryAdjustmentCredit, AmountMicro: 100, ResourceKind: "adjustment", ResourceID: "renew-http-seed"}}})
	if err != nil || len(posted) != 1 {
		t.Fatalf("seed entries=%+v err=%v", posted, err)
	}
	for _, statement := range []string{
		`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at) VALUES('renew-http-plan','Renew','USD',10,20,'monthly',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('renew-http-old','` + posted[0].AccountID + `','renew-http-plan',1,10,20,'USD','monthly','active','2026-01-31T08:00:00Z','2026-02-28T08:00:00.000000000Z',1)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	url := f.server.URL + "/admin/api/v1/billing/subscriptions/renew-http-old/renew"
	body := `{"operation_id":"20000000-0000-4000-8000-000000000111"}`
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, body, nil, "", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, body, f.cookie, "", "", nil); status != http.StatusForbidden {
		t.Fatalf("missing csrf/origin status=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, body, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusConflict {
		t.Fatalf("default-off status=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/subscriptions/missing/renew", `{"operation_id":"20000000-0000-4000-8000-000000000113"}`, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusNotFound {
		t.Fatalf("missing predecessor while off status=%d", status)
	}
	if _, err := db.Exec(`UPDATE financial_settings SET enabled=1 WHERE singleton=1`); err != nil {
		t.Fatal(err)
	}
	status, response := billingHTTPRequest(t, http.MethodPost, url, body, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"predecessor_id":"renew-http-old"`) || !strings.Contains(string(response), `"status":"active"`) {
		t.Fatalf("renew status=%d body=%s", status, response)
	}
	newID := billingNestedString(t, response, "subscription", "id")
	status, response = billingHTTPRequest(t, http.MethodGet, f.server.URL+"/admin/api/v1/billing/subscriptions/renew-http-old", "", f.cookie, "", "", nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"successor_id":"`+newID+`"`) || !strings.Contains(string(response), `"status":"expired"`) {
		t.Fatalf("old detail status=%d body=%s", status, response)
	}
	status, response = billingHTTPRequest(t, http.MethodGet, f.server.URL+"/admin/api/v1/billing/subscriptions/"+newID, "", f.cookie, "", "", nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"predecessor_id":"renew-http-old"`) || !strings.Contains(string(response), `"period_end_at":`) {
		t.Fatalf("new detail status=%d body=%s", status, response)
	}
	status, response = billingHTTPRequest(t, http.MethodPost, url, body, f.cookie, f.csrf, f.server.URL, nil)
	if status != http.StatusOK || !strings.Contains(string(response), `"replay":true`) {
		t.Fatalf("retry status=%d body=%s", status, response)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, url, `{"operation_id":"20000000-0000-4000-8000-000000000112","extra":true}`, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusBadRequest {
		t.Fatalf("extra field status=%d", status)
	}
	if status, _ := billingHTTPRequest(t, http.MethodPost, f.server.URL+"/admin/api/v1/billing/subscriptions/missing/renew", `{"operation_id":"20000000-0000-4000-8000-000000000113"}`, f.cookie, f.csrf, f.server.URL, nil); status != http.StatusNotFound {
		t.Fatalf("missing predecessor status=%d", status)
	}
}
