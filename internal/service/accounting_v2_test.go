package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
)

func TestAccountingV2AdminReportsExportAndCorrection(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	if err := migrateAccountingV2(context.Background(), f.app.store.db); err != nil {
		t.Fatal(err)
	}
	started, eventID := seedAccountingV2ServiceAttempt(t, f.app.store.db)
	mux := http.NewServeMux()
	f.app.registerAccountingV2Handlers(mux)
	server := httptest.NewServer(mux)
	defer server.Close()

	query := "?from=" + started.Add(-time.Second).Format(time.RFC3339) + "&to=" + started.Add(time.Hour).Format(time.RFC3339)
	status, body, headers := accountingV2Request(t, server.URL+"/admin/api/v1/usage/daily"+query, nil, nil, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", status, body)
	}
	status, body, _ = accountingV2Request(t, server.URL+"/admin/api/v1/usage/daily"+query, f.cookie, nil, "", "")
	if status != http.StatusOK {
		t.Fatalf("daily status=%d body=%s", status, body)
	}
	var daily struct {
		Items []accountingV2ReportView `json:"items"`
	}
	if err := json.Unmarshal(body, &daily); err != nil {
		t.Fatal(err)
	}
	if len(daily.Items) != 1 || daily.Items[0].Currency != "USD" || daily.Items[0].KnownEstimatedCostMicro != "3" || daily.Items[0].ReasoningTokens.KnownTotal != "1" {
		t.Fatalf("daily=%+v", daily)
	}
	status, body, headers = accountingV2Request(t, server.URL+"/admin/api/v1/usage/export"+query+"&limit=10", f.cookie, nil, "", "")
	if status != http.StatusOK || !strings.HasPrefix(headers.Get("Content-Type"), "text/csv") || !strings.Contains(string(body), "estimated_cost_micro") || !strings.Contains(string(body), "response-late") {
		t.Fatalf("export status=%d headers=%v body=%s", status, headers, body)
	}

	correctionAt := started.Add(4 * time.Second).Format(time.RFC3339Nano)
	correction := `{"id":"correction-http","attempt_id":"attempt-http","target_event_id":"` + eventID + `","operation_id":"operation-http","reason":"admin_reconciliation","corrected_at":"` + correctionAt + `","currency":"USD","output_tokens_delta":"1000000","estimated_cost_delta_micro":"2"}`
	status, body, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/usage-corrections", f.cookie, []byte(correction), f.csrf, server.URL)
	if status != http.StatusOK {
		t.Fatalf("correction status=%d body=%s", status, body)
	}
	status, body, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/usage-corrections", f.cookie, []byte(correction), f.csrf, server.URL)
	if status != http.StatusOK {
		t.Fatalf("correction replay status=%d body=%s", status, body)
	}
	status, body, _ = accountingV2Request(t, server.URL+"/admin/api/v1/usage/monthly"+query, f.cookie, nil, "", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"known_estimated_cost_micro":"5"`) || !strings.Contains(string(body), `"corrections":"1"`) {
		t.Fatalf("monthly status=%d body=%s", status, body)
	}

	duplicate := strings.Replace(correction, `"id":`, `"id":"duplicate","id":`, 1)
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/usage-corrections", f.cookie, []byte(duplicate), f.csrf, server.URL)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate JSON status=%d", status)
	}
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/billing/usage-corrections", f.cookie, []byte(correction), "wrong", server.URL)
	if status != http.StatusForbidden {
		t.Fatalf("bad csrf status=%d", status)
	}
	status, _, _ = accountingV2Request(t, server.URL+"/admin/api/v1/usage/export"+query+"&limit=5001", f.cookie, nil, "", "")
	if status != http.StatusBadRequest {
		t.Fatalf("oversize limit status=%d", status)
	}
}

func seedAccountingV2ServiceAttempt(t *testing.T, db *sql.DB) (time.Time, string) {
	t.Helper()
	ledger := accounting.NewLedger(db)
	started := time.Date(2026, time.September, 24, 1, 2, 3, 0, time.UTC)
	request := accounting.RequestStart{ID: "request-http", EmployeeID: "employee-http", KeyID: "key-http", ModelID: "public-http", Provider: accounting.ProviderOpenAICompatible, StartedAt: started}
	price := accounting.PriceSnapshot{Version: "price-http", Currency: "USD", InputPerMillionMicro: 1, OutputPerMillionMicro: 2, CacheReadPerMillionMicro: 3, CacheWritePerMillionMicro: 4}
	attempt := accounting.AttemptStart{ID: "attempt-http", RequestID: request.ID, AccountID: "account-http", Provider: request.Provider, Dispatch: accounting.DispatchPrimary, StartedAt: started, Price: &price, Protocol: accounting.ProtocolOpenAIResponses, EffectiveModel: "actual-http", Evidence: accounting.EvidenceProviderResponse}
	if err := ledger.BeginRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := ledger.BeginAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	if err := ledger.MarkAttemptDispatched(context.Background(), accounting.AttemptDispatch{ID: attempt.ID, OperationID: "dispatch-http", DispatchedAt: started.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	oneMillion, one := int64(1_000_000), int64(1)
	responseID := "response-late"
	if err := ledger.FinishAttempt(context.Background(), accounting.AttemptFinish{ID: attempt.ID, Status: accounting.StatusSucceeded, FinishedAt: started.Add(2 * time.Second), Usage: accounting.Usage{InputTokens: &oneMillion, OutputTokens: &oneMillion, CacheReadTokens: int64ServicePointer(0), CacheWriteTokens: int64ServicePointer(0)}, ReasoningTokens: &one, ResponseID: &responseID, ReliableUsage: true}); err != nil {
		t.Fatal(err)
	}
	if err := ledger.FinishRequest(context.Background(), accounting.RequestFinish{ID: request.ID, Status: accounting.StatusSucceeded, FinishedAt: started.Add(3 * time.Second)}); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := db.QueryRow(`SELECT id FROM accounting_usage_events WHERE attempt_id=?`, attempt.ID).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	return started, eventID
}

func accountingV2Request(t *testing.T, rawURL string, cookie *http.Cookie, body []byte, csrf, origin string) (int, []byte, http.Header) {
	t.Helper()
	method := http.MethodGet
	var reader io.Reader
	if body != nil {
		method, reader = http.MethodPost, strings.NewReader(string(body))
	}
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, responseBody, response.Header.Clone()
}

func int64ServicePointer(value int64) *int64 { return &value }
