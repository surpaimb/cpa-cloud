package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBillingCommercialSignedPaymentReplayAndOneTimeRedemptionCode(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	if err := migrateAccountingV2(t.Context(), f.app.store.db); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.db.Exec(`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('commercial-employee','Commercial employee','active','all',1,?)`, utcNow()); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	f.app.registerAccountingV2Handlers(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	settings := `{"operation_id":"20000000-0000-4000-8000-000000000001","expected_revision":1,"enabled":true}`
	status, body := billingHTTPRequest(t, http.MethodPut, server.URL+"/admin/api/v1/billing/settings", settings, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", status, body)
	}
	secret := "synthetic-webhook-secret-that-is-at-least-thirty-two-bytes"
	connectorBody := `{"operation_id":"20000000-0000-4000-8000-000000000002","name":"Synthetic","webhook_secret":"` + secret + `","enabled":true}`
	status, body = billingHTTPRequest(t, http.MethodPost, server.URL+"/admin/api/v1/billing/payment-connectors", connectorBody, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("connector status=%d body=%s", status, body)
	}
	connectorID := billingNestedString(t, body, "connector", "id")
	topupBody := `{"operation_id":"20000000-0000-4000-8000-000000000003","owner":{"kind":"employee","employee_id":"commercial-employee"},"connector_id":"` + connectorID + `","currency":"USD","amount_micro":"50"}`
	status, body = billingHTTPRequest(t, http.MethodPost, server.URL+"/admin/api/v1/billing/topups", topupBody, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("topup status=%d body=%s", status, body)
	}
	paymentID := billingNestedString(t, body, "topup", "payment_id")
	external := billingNestedString(t, body, "topup", "external_reference")
	callback := `{"status":"paid","payment_id":"` + paymentID + `","external_reference":"` + external + `","amount_micro":"50","currency":"USD"}`
	timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte{'\n'})
	mac.Write([]byte("event-http-one"))
	mac.Write([]byte{'\n'})
	mac.Write([]byte(callback))
	headers := map[string]string{"X-Billing-Event-ID": "event-http-one", "X-Billing-Timestamp": timestamp, "X-Billing-Signature": hex.EncodeToString(mac.Sum(nil))}
	callbackURL := server.URL + "/admin/api/v1/billing/payment-callbacks/" + connectorID
	status, body = billingHTTPRequest(t, http.MethodPost, callbackURL, callback, nil, "", "", headers)
	if status != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", status, body)
	}
	status, body = billingHTTPRequest(t, http.MethodPost, callbackURL, callback, nil, "", "", headers)
	if status != http.StatusOK {
		t.Fatalf("callback replay status=%d body=%s", status, body)
	}
	badHeaders := map[string]string{"X-Billing-Event-ID": "event-http-two", "X-Billing-Timestamp": timestamp, "X-Billing-Signature": strings.Repeat("0", 64)}
	status, _ = billingHTTPRequest(t, http.MethodPost, callbackURL, callback, nil, "", "", badHeaders)
	if status != http.StatusUnauthorized {
		t.Fatalf("bad signature status=%d", status)
	}
	status, body = billingHTTPRequest(t, http.MethodGet, server.URL+"/admin/api/v1/billing/balances?owner_kind=employee&employee_id=commercial-employee&currency=USD", "", f.cookie, "", "", nil)
	if status != http.StatusOK || !jsonContainsString(t, body, "balance_micro", "50") {
		t.Fatalf("paid balance status=%d body=%s", status, body)
	}
	expires := time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
	codeBody := `{"operation_id":"20000000-0000-4000-8000-000000000004","currency":"USD","amount_micro":"15","max_uses":1,"expires_at":"` + expires + `"}`
	status, body = billingHTTPRequest(t, http.MethodPost, server.URL+"/admin/api/v1/billing/redemption-codes", codeBody, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("code status=%d body=%s", status, body)
	}
	var created map[string]any
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatal(err)
	}
	code, ok := created["code"].(string)
	if !ok || code == "" {
		t.Fatalf("plaintext code missing body=%s", body)
	}
	status, body = billingHTTPRequest(t, http.MethodPost, server.URL+"/admin/api/v1/billing/redemption-codes", codeBody, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("code replay status=%d body=%s", status, body)
	}
	var replay map[string]any
	if err := json.Unmarshal(body, &replay); err != nil || replay["code"] != nil {
		t.Fatalf("code replay leaked secret body=%s err=%v", body, err)
	}
	redeemBody := `{"operation_id":"20000000-0000-4000-8000-000000000005","owner":{"kind":"employee","employee_id":"commercial-employee"},"code":"` + code + `"}`
	status, body = billingHTTPRequest(t, http.MethodPost, server.URL+"/admin/api/v1/billing/redemptions", redeemBody, f.cookie, f.csrf, server.URL, nil)
	if status != http.StatusOK {
		t.Fatalf("redeem status=%d body=%s", status, body)
	}
	status, body = billingHTTPRequest(t, http.MethodGet, server.URL+"/admin/api/v1/billing/balances?owner_kind=employee&employee_id=commercial-employee&currency=USD", "", f.cookie, "", "", nil)
	if status != http.StatusOK || !jsonContainsString(t, body, "balance_micro", "65") {
		t.Fatalf("redeemed balance status=%d body=%s", status, body)
	}
}

func TestBillingSubscriptionAdminProjectionBeforeExpiryWorker(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	f.app.subscriptionExpiry.Close()
	f.app.subscriptionExpiry = nil // Simulate a delayed worker; reads must still fail closed.
	for _, statement := range []string{
		`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('expiry-employee','Expiry','active','all',1,'2026-01-01T00:00:00Z')`,
		`INSERT INTO financial_accounts(id,owner_kind,owner_key,employee_id,key_id,resource_kind,resource_id,currency,created_at) VALUES('expiry-account','employee','expiry-employee','expiry-employee',NULL,'','','USD','2026-01-01T00:00:00Z')`,
		`INSERT INTO financial_plans(id,name,currency,price_micro,credit_micro,interval,enabled,revision,created_at,updated_at) VALUES('expiry-plan','Expiry','USD',10,20,'monthly',1,1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`,
		`INSERT INTO financial_subscriptions(id,account_id,plan_id,plan_revision,price_micro,credit_micro,currency,interval,status,started_at,period_end_at,revision) VALUES('expiry-sub','expiry-account','expiry-plan',1,10,20,'USD','monthly','active','2026-01-31T08:00:00Z','2026-02-28T08:00:00.000000000Z',1)`,
	} {
		if _, err := f.app.store.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	url := f.server.URL + "/admin/api/v1/billing/subscriptions"
	status, body := billingHTTPRequest(t, http.MethodGet, url, "", f.cookie, "", "", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"expired"`) || !strings.Contains(string(body), `"period_end_at":"2026-02-28T08:00:00Z"`) {
		t.Fatalf("list status=%d body=%s", status, body)
	}
	status, body = billingHTTPRequest(t, http.MethodGet, url+"/expiry-sub", "", f.cookie, "", "", nil)
	if status != http.StatusOK || !strings.Contains(string(body), `"status":"expired"`) || !strings.Contains(string(body), `"revision":1`) {
		t.Fatalf("detail status=%d body=%s", status, body)
	}
	status, _ = billingHTTPRequest(t, http.MethodGet, url+"/expiry-sub", "", nil, "", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated detail status=%d", status)
	}
	status, _ = billingHTTPRequest(t, http.MethodGet, url+"/expiry-sub?extra=1", "", f.cookie, "", "", nil)
	if status != http.StatusBadRequest {
		t.Fatalf("query detail status=%d", status)
	}
}

func billingHTTPRequest(t *testing.T, method, url, body string, cookie *http.Cookie, csrf, origin string, headers map[string]string) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		request.Header.Set("Origin", origin)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, payload
}
func billingNestedString(t *testing.T, body []byte, parent, key string) string {
	t.Helper()
	var object map[string]any
	if err := json.Unmarshal(body, &object); err != nil {
		t.Fatal(err)
	}
	nested, ok := object[parent].(map[string]any)
	if !ok {
		t.Fatalf("missing %s body=%s", parent, body)
	}
	value, ok := nested[key].(string)
	if !ok {
		t.Fatalf("missing %s.%s body=%s", parent, key, body)
	}
	return value
}
