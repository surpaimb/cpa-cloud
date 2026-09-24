package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBillingV1AdjustmentBalanceIdempotencyAndIsolation(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	if err := migrateAccountingV2(context.Background(), f.app.store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('billing-employee','Billing employee','active','all',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('other-employee','Other employee','active','all',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('billing-key','billing-employee','Billing key','billing-selector',X'01',1,'billing-key-operation',?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.app.registerAccountingV2Handlers(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	body := `{"operation_id":"10000000-0000-4000-8000-000000000001","owner":{"kind":"key","employee_id":"billing-employee","key_id":"billing-key"},"currency":"USD","amount_micro":"100"}`
	status, response, _ := accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(body), f.csrf, server.URL)
	if status != http.StatusOK {
		t.Fatalf("credit status=%d body=%s", status, response)
	}
	var first map[string]any
	if err := json.Unmarshal(response, &first); err != nil {
		t.Fatal(err)
	}
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(body), f.csrf, server.URL)
	var replay map[string]any
	if err := json.Unmarshal(response, &replay); err != nil || status != http.StatusOK || replay["entry_id"] != first["entry_id"] {
		t.Fatalf("replay status=%d body=%s err=%v", status, response, err)
	}
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/balances?owner_kind=key&employee_id=billing-employee&key_id=billing-key&currency=USD", f.cookie, nil, "", "")
	if status != http.StatusOK || !jsonContainsString(t, response, "balance_micro", "100") {
		t.Fatalf("balance status=%d body=%s", status, response)
	}
	changed := `{"operation_id":"10000000-0000-4000-8000-000000000001","owner":{"kind":"key","employee_id":"billing-employee","key_id":"billing-key"},"currency":"USD","amount_micro":"101"}`
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(changed), f.csrf, server.URL)
	if status != http.StatusConflict {
		t.Fatalf("changed replay status=%d body=%s", status, response)
	}
	overdraw := `{"operation_id":"10000000-0000-4000-8000-000000000002","owner":{"kind":"key","employee_id":"billing-employee","key_id":"billing-key"},"currency":"USD","amount_micro":"-101"}`
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(overdraw), f.csrf, server.URL)
	if status != http.StatusConflict || !jsonContainsString(t, response, "code", "insufficient_balance") {
		t.Fatalf("overdraw status=%d body=%s", status, response)
	}
	wrongOwner := `{"operation_id":"10000000-0000-4000-8000-000000000003","owner":{"kind":"key","employee_id":"other-employee","key_id":"billing-key"},"currency":"USD","amount_micro":"1"}`
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(wrongOwner), f.csrf, server.URL)
	if status != http.StatusConflict {
		t.Fatalf("wrong owner status=%d body=%s", status, response)
	}
	duplicate := `{"operation_id":"10000000-0000-4000-8000-000000000004","owner":{"kind":"employee","kind":"key","employee_id":"billing-employee"},"currency":"USD","amount_micro":"1"}`
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(duplicate), f.csrf, server.URL)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate nested key status=%d", status)
	}
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(body), "", server.URL)
	if status != http.StatusForbidden {
		t.Fatalf("missing csrf status=%d", status)
	}
	debit := `{"operation_id":"10000000-0000-4000-8000-000000000005","owner":{"kind":"key","employee_id":"billing-employee","key_id":"billing-key"},"currency":"USD","amount_micro":"-70"}`
	status, response, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/adjustments", f.cookie, []byte(debit), f.csrf, server.URL)
	if status != http.StatusOK {
		t.Fatalf("debit status=%d body=%s", status, response)
	}

	entriesURL := server.URL + "/admin/api/v1/billing/entries?owner_kind=key&employee_id=billing-employee&key_id=billing-key&currency=USD&limit=1"
	status, response, _ = accountingV2Request(t, entriesURL, f.cookie, nil, "", "")
	var firstPage struct {
		Items []struct {
			ID              string         `json:"id"`
			OperationID     string         `json:"operation_id"`
			AccountID       string         `json:"account_id"`
			Owner           map[string]any `json:"owner"`
			AmountMicro     string         `json:"amount_micro"`
			OriginalEntryID *string        `json:"original_entry_id"`
			CreatedAt       string         `json:"created_at"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response, &firstPage); err != nil || status != http.StatusOK || len(firstPage.Items) != 1 || firstPage.Items[0].AmountMicro != "100" || firstPage.Items[0].OriginalEntryID != nil || firstPage.NextCursor == nil {
		t.Fatalf("first entries status=%d page=%+v body=%s err=%v", status, firstPage, response, err)
	}
	status, response, _ = accountingV2Request(t, entriesURL+"&after_id="+*firstPage.NextCursor, f.cookie, nil, "", "")
	var secondPage struct {
		Items []struct {
			AmountMicro string `json:"amount_micro"`
			ResourceID  string `json:"resource_id"`
		} `json:"items"`
		NextCursor *string `json:"next_cursor"`
	}
	if err := json.Unmarshal(response, &secondPage); err != nil || status != http.StatusOK || len(secondPage.Items) != 1 || secondPage.Items[0].AmountMicro != "-70" || secondPage.Items[0].ResourceID != "10000000-0000-4000-8000-000000000005" || secondPage.NextCursor != nil {
		t.Fatalf("second entries status=%d page=%+v body=%s err=%v", status, secondPage, response, err)
	}
	for _, rawQuery := range []string{"?limit=0", "?limit=01", "?limit=101", "?unknown=1", "?currency=usd", "?after_id=missing"} {
		status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/entries"+rawQuery, f.cookie, nil, "", "")
		if status != http.StatusBadRequest {
			t.Fatalf("entries query %q status=%d", rawQuery, status)
		}
	}
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/entries", nil, nil, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("entries without admin status=%d", status)
	}
}

func jsonContainsString(t *testing.T, body []byte, key, expected string) bool {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		return false
	}
	if object[key] == expected {
		return true
	}
	nested, ok := object["error"].(map[string]any)
	return ok && nested[key] == expected
}
