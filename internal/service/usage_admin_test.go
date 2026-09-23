package service

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type usageSummaryTestResponse struct {
	From     string                `json:"from"`
	To       string                `json:"to"`
	Requests usageStatusCounts     `json:"requests"`
	Attempts []usageAttemptSummary `json:"attempts"`
}

type usageRequestsTestResponse struct {
	Items      []usageRequestItem `json:"items"`
	NextCursor *string            `json:"next_cursor"`
	From       string             `json:"from"`
	To         string             `json:"to"`
}

func TestUsageAdminSummaryFiltersUnknownCurrenciesAndOverflow(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	seedUsageRequest(t, f.app.store.db, "req-summary-one", "emp-one", "key-one", "model-one", "codex", "2026-09-23T00:00:00.1Z", "succeeded")
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-summary-usd", requestID: "req-summary-one", accountID: "up-one", provider: "codex", dispatch: "primary", startedAt: "2026-09-23T00:00:00.2Z", status: "succeeded", priceVersion: strPtr("price-1"), currency: strPtr("USD"), input: intPtr(10), output: intPtr(2), cacheRead: intPtr(0), cacheWrite: intPtr(0), cost: intPtr(3)})
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-summary-unknown", requestID: "req-summary-one", accountID: "up-one", provider: "codex", dispatch: "retry", startedAt: "2026-09-23T00:00:00.3Z", status: "failed"})
	seedUsageRequest(t, f.app.store.db, "req-summary-two", "emp-two", "key-two", "model-two", "anthropic", "2026-09-23T00:00:01Z", "failed")
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-summary-eur", requestID: "req-summary-two", accountID: "up-two", provider: "anthropic", dispatch: "primary", startedAt: "2026-09-23T00:00:01.1Z", status: "failed", priceVersion: strPtr("price-2"), currency: strPtr("EUR"), input: intPtr(7), output: intPtr(1), cacheRead: intPtr(0), cacheWrite: intPtr(0), cost: intPtr(9)})
	seedUsageRequest(t, f.app.store.db, "req-summary-pending", "emp-one", "key-one", "model-one", "codex", "2026-09-23T00:00:02Z", "pending")
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-summary-pending", requestID: "req-summary-pending", accountID: "up-one", provider: "codex", dispatch: "primary", startedAt: "2026-09-23T00:00:02Z", status: "pending", priceVersion: strPtr("price-1"), currency: strPtr("USD")})

	query := "?from=2026-09-23T00:00:00Z&to=2026-09-23T00:01:00Z"
	status, body := usageAdminGET(t, f, "/admin/api/v1/usage/summary"+query, true)
	if status != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", status, body)
	}
	var summary usageSummaryTestResponse
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests.Total != "3" || summary.Requests.Succeeded != "1" || summary.Requests.Failed != "1" || summary.Requests.Pending != "1" {
		t.Fatalf("request counts=%+v", summary.Requests)
	}
	byCurrency := make(map[string]usageAttemptSummary)
	for _, item := range summary.Attempts {
		byCurrency[item.Currency] = item
	}
	if len(byCurrency) != 3 || byCurrency["USD"].KnownCostMicro != "3" || byCurrency["USD"].UnknownCostAttempts != "0" || byCurrency["USD"].InputTokens.KnownTotal != "10" {
		t.Fatalf("USD group=%+v all=%+v", byCurrency["USD"], summary.Attempts)
	}
	if unknown := byCurrency["UNKNOWN"]; unknown.Total != "1" || unknown.UnknownCostAttempts != "1" || unknown.InputTokens.UnknownAttempts != "1" {
		t.Fatalf("unknown group=%+v", unknown)
	}
	if eur := byCurrency["EUR"]; eur.KnownCostMicro != "9" || eur.InputTokens.KnownTotal != "7" {
		t.Fatalf("EUR group=%+v", eur)
	}

	status, body = usageAdminGET(t, f, "/admin/api/v1/usage/summary"+query+"&employee_id=emp-one&upstream_id=up-one", true)
	if status != http.StatusOK {
		t.Fatalf("filtered summary status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Requests.Total != "2" || len(summary.Attempts) != 2 {
		t.Fatalf("upstream request dedupe/attempt filter failed: %+v", summary)
	}

	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/summary"+query, false)
	if status != http.StatusUnauthorized {
		t.Fatalf("employee-style bearer without admin session status=%d", status)
	}
	for _, invalidPath := range []string{
		"/admin/api/v1/usage/summary" + query + "&unknown=x",
		"/admin/api/v1/usage/summary" + query + "&status=failed&status=succeeded",
		"/admin/api/v1/usage/summary?from=2026-09-23T00:00:00.000Z&to=2026-09-23T00:01:00Z",
		"/admin/api/v1/usage/summary" + query + "&provider=other",
		"/admin/api/v1/usage/summary" + query + "&employee_id=" + strings.Repeat("x", usageMaxIDLength+1),
	} {
		status, body = usageAdminGET(t, f, invalidPath, true)
		if status != http.StatusBadRequest {
			t.Fatalf("invalid query %q status=%d body=%s", invalidPath, status, body)
		}
	}
	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/summary?from=2026-08-01T00:00:00Z&to=2026-09-23T00:00:00Z", true)
	if status != http.StatusBadRequest {
		t.Fatalf("range over 31 days status=%d", status)
	}
	for _, rawQuery := range []string{"from=2026-09-23T00:00:00Z%zz", "from=2026-09-23T00:00:00Z;to=2026-09-23T00:01:00Z"} {
		status, body = usageAdminRawQuery(t, f, "/admin/api/v1/usage/summary", rawQuery)
		if status != http.StatusBadRequest {
			t.Fatalf("malformed raw query %q status=%d body=%s", rawQuery, status, body)
		}
	}

	seedUsageRequest(t, f.app.store.db, "req-overflow-a", "emp-overflow", "key-overflow", "model-overflow", "openai", "2026-09-23T02:00:00Z", "succeeded")
	seedUsageRequest(t, f.app.store.db, "req-overflow-b", "emp-overflow", "key-overflow", "model-overflow", "openai", "2026-09-23T02:00:01Z", "succeeded")
	for i, entry := range []struct {
		request string
		cost    int64
	}{{"req-overflow-a", math.MaxInt64}, {"req-overflow-b", 1}} {
		seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: fmt.Sprintf("att-overflow-%d", i), requestID: entry.request, accountID: "up-overflow", provider: "openai", dispatch: "primary", startedAt: fmt.Sprintf("2026-09-23T02:00:0%dZ", i), status: "succeeded", priceVersion: strPtr("price-overflow"), currency: strPtr("USD"), input: intPtr(0), output: intPtr(0), cacheRead: intPtr(0), cacheWrite: intPtr(0), cost: &entry.cost})
	}
	status, body = usageAdminGET(t, f, "/admin/api/v1/usage/summary?from=2026-09-23T02:00:00Z&to=2026-09-23T02:01:00Z", true)
	if status != http.StatusServiceUnavailable || strings.Contains(string(body), "integer overflow") {
		t.Fatalf("overflow status=%d body=%s", status, body)
	}
}

func TestUsageAdminRequestsNanosecondKeysetAndBoundCursor(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	entries := []struct{ id, at string }{
		{"req-whole", "2026-09-23T03:00:00Z"},
		{"req-low", "2026-09-23T03:00:00.05Z"},
		{"req-tie-a", "2026-09-23T03:00:00.500000000Z"},
		{"req-tie-b", "2026-09-23T03:00:00.5Z"},
		{"req-new", "2026-09-23T03:00:00.500000001Z"},
		{"req-to-exclusive", "2026-09-23T03:00:01Z"},
	}
	for _, entry := range entries {
		seedUsageRequest(t, f.app.store.db, entry.id, "emp-page", "key-page", "model-page", "gemini", entry.at, "succeeded")
		seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-" + entry.id, requestID: entry.id, accountID: "up-page", provider: "gemini", dispatch: "primary", startedAt: entry.at, status: "succeeded"})
	}
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-req-tie-b-retry", requestID: "req-tie-b", accountID: "up-page", provider: "gemini", dispatch: "retry", startedAt: "2026-09-23T03:00:00.6Z", status: "failed"})

	path := "/admin/api/v1/usage/requests?from=2026-09-23T03:00:00Z&to=2026-09-23T03:00:01Z&upstream_id=up-page&limit=2"
	status, body := usageAdminGET(t, f, path, true)
	if status != http.StatusOK {
		t.Fatalf("page one status=%d body=%s", status, body)
	}
	var page usageRequestsTestResponse
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	assertUsageRequestIDs(t, page.Items, "req-new", "req-tie-b")
	if page.Items[1].AttemptCount != "2" || page.NextCursor == nil {
		t.Fatalf("page one attempts/cursor=%+v cursor=%v", page.Items, page.NextCursor)
	}

	status, body = usageAdminGET(t, f, "/admin/api/v1/usage/requests?upstream_id=up-page&limit=2&cursor="+url.QueryEscape(*page.NextCursor), true)
	if status != http.StatusOK {
		t.Fatalf("page two status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	assertUsageRequestIDs(t, page.Items, "req-tie-a", "req-low")
	if page.From != "2026-09-23T03:00:00Z" || page.To != "2026-09-23T03:00:01Z" || page.NextCursor == nil {
		t.Fatalf("cursor did not preserve range: %+v", page)
	}

	status, body = usageAdminGET(t, f, "/admin/api/v1/usage/requests?upstream_id=up-page&limit=2&cursor="+url.QueryEscape(*page.NextCursor), true)
	if status != http.StatusOK {
		t.Fatalf("page three status=%d body=%s", status, body)
	}
	if err := json.Unmarshal(body, &page); err != nil {
		t.Fatal(err)
	}
	assertUsageRequestIDs(t, page.Items, "req-whole")
	if page.NextCursor != nil {
		t.Fatalf("unexpected final cursor %q", *page.NextCursor)
	}

	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/requests?upstream_id=up-page&model_id=changed&cursor="+url.QueryEscape(*pageCursorFromFirstPage(t, f, path)), true)
	if status != http.StatusBadRequest {
		t.Fatalf("cursor accepted changed filters status=%d", status)
	}
	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/requests?cursor="+strings.Repeat("a", usageMaxCursor+1), true)
	if status != http.StatusBadRequest {
		t.Fatalf("oversize cursor status=%d", status)
	}
	duplicateCursor := base64.RawURLEncoding.EncodeToString([]byte(`{"v":1,"v":1,"started_key":"2026-09-23T03:00:00.000000000Z","id":"req-whole","from":"2026-09-23T03:00:00Z","to":"2026-09-23T03:00:01Z","fingerprint":"` + strings.Repeat("0", 64) + `"}`))
	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/requests?cursor="+duplicateCursor, true)
	if status != http.StatusBadRequest {
		t.Fatalf("duplicate-key cursor status=%d", status)
	}
}

func TestUsageAdminAttemptDetailNullsLimitAndRedaction(t *testing.T) {
	f := newAccountPoolFixture(t, false)
	seedUsageRequest(t, f.app.store.db, "req-detail", "emp-detail", "key-detail", "model-detail", "openai-compatible", "2026-09-23T04:00:00Z", "failed")
	seedUsageAttempt(t, f.app.store.db, usageAttemptSeed{id: "att-detail", requestID: "req-detail", accountID: "up-detail", provider: "openai-compatible", dispatch: "primary", startedAt: "2026-09-23T04:00:00.25Z", status: "failed", input: intPtr(12)})
	status, body := usageAdminGET(t, f, "/admin/api/v1/usage/requests/req-detail/attempts", true)
	if status != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", status, body)
	}
	var response struct {
		Items []usageAttemptItem `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 1 || response.Items[0].InputTokens == nil || *response.Items[0].InputTokens != "12" || response.Items[0].OutputTokens != nil || response.Items[0].CostMicro != nil || response.Items[0].FinishedAt == nil {
		t.Fatalf("detail nullable fields=%+v", response.Items)
	}
	for _, forbidden := range []string{"credential", "authorization", "prompt", "response_body", "error_text", "token_secret"} {
		if strings.Contains(strings.ToLower(string(body)), forbidden) {
			t.Fatalf("detail leaked forbidden field %q: %s", forbidden, body)
		}
	}
	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/requests/missing/attempts", true)
	if status != http.StatusNotFound {
		t.Fatalf("missing detail status=%d", status)
	}
	status, _ = usageAdminGET(t, f, "/admin/api/v1/usage/requests/req-detail/attempts?extra=x", true)
	if status != http.StatusBadRequest {
		t.Fatalf("detail query accepted status=%d", status)
	}

	seedUsageRequest(t, f.app.store.db, "req-many", "emp-detail", "key-detail", "model-detail", "openai-compatible", "2026-09-23T04:01:00Z", "failed")
	tx, err := f.app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		seedUsageAttemptDB(t, tx, usageAttemptSeed{id: fmt.Sprintf("att-many-%03d", i), requestID: "req-many", accountID: "up-detail", provider: "openai-compatible", dispatch: "retry", startedAt: fmt.Sprintf("2026-09-23T04:01:00.%09dZ", i+1), status: "failed"})
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	status, body = usageAdminGET(t, f, "/admin/api/v1/usage/requests/req-many/attempts", true)
	if status != http.StatusConflict {
		t.Fatalf("detail over limit status=%d body=%s", status, body)
	}
}

type usageAttemptSeed struct {
	id, requestID, accountID, provider, dispatch, startedAt, status string
	priceVersion, currency                                          *string
	input, output, cacheRead, cacheWrite, cost                      *int64
}

type usageExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func seedUsageRequest(t *testing.T, db *sql.DB, id, employee, key, model, provider, startedAt, status string) {
	t.Helper()
	var finished any
	if status != "pending" {
		parsed, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			t.Fatal(err)
		}
		finished = parsed.Add(time.Second).UTC().Format(time.RFC3339Nano)
	}
	if _, err := db.Exec(`INSERT INTO accounting_requests(id,employee_id,key_id,model_id,provider,started_at,finished_at,status) VALUES(?,?,?,?,?,?,?,?)`, id, employee, key, model, provider, startedAt, finished, status); err != nil {
		t.Fatalf("seed request %s: %v", id, err)
	}
}

func seedUsageAttempt(t *testing.T, db *sql.DB, seed usageAttemptSeed) {
	t.Helper()
	seedUsageAttemptDB(t, db, seed)
}

func seedUsageAttemptDB(t *testing.T, db usageExecer, seed usageAttemptSeed) {
	t.Helper()
	var finished any
	if seed.status != "pending" {
		parsed, err := time.Parse(time.RFC3339Nano, seed.startedAt)
		if err != nil {
			t.Fatal(err)
		}
		finished = parsed.Add(500 * time.Millisecond).UTC().Format(time.RFC3339Nano)
	}
	var inputRate, outputRate, cacheReadRate, cacheWriteRate any
	if seed.priceVersion != nil {
		inputRate, outputRate, cacheReadRate, cacheWriteRate = int64(1), int64(1), int64(1), int64(1)
	}
	_, err := db.Exec(`INSERT INTO accounting_attempts(id,request_id,account_id,provider,dispatch,started_at,finished_at,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,price_version,currency,input_rate,output_rate,cache_read_rate,cache_write_rate,cost_micro) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		seed.id, seed.requestID, seed.accountID, seed.provider, seed.dispatch, seed.startedAt, finished, seed.status, ptrValue(seed.input), ptrValue(seed.output), ptrValue(seed.cacheRead), ptrValue(seed.cacheWrite), ptrValue(seed.priceVersion), ptrValue(seed.currency), inputRate, outputRate, cacheReadRate, cacheWriteRate, ptrValue(seed.cost))
	if err != nil {
		t.Fatalf("seed attempt %s: %v", seed.id, err)
	}
}

func usageAdminGET(t *testing.T, f *accountPoolFixture, path string, admin bool) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, f.server.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if admin {
		req.AddCookie(f.cookie)
	} else {
		req.Header.Set("Authorization", "Bearer employee-key-value")
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response.StatusCode, body
}

func usageAdminRawQuery(t *testing.T, f *accountPoolFixture, path, rawQuery string) (int, []byte) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.URL.RawQuery = rawQuery
	req.AddCookie(f.cookie)
	recorder := httptest.NewRecorder()
	f.app.Handler().ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.Bytes()
}

func assertUsageRequestIDs(t *testing.T, items []usageRequestItem, expected ...string) {
	t.Helper()
	if len(items) != len(expected) {
		t.Fatalf("ids count=%d want=%d items=%+v", len(items), len(expected), items)
	}
	for i := range expected {
		if items[i].ID != expected[i] {
			t.Fatalf("id[%d]=%s want=%s items=%+v", i, items[i].ID, expected[i], items)
		}
	}
}

func pageCursorFromFirstPage(t *testing.T, f *accountPoolFixture, path string) *string {
	t.Helper()
	status, body := usageAdminGET(t, f, path, true)
	if status != http.StatusOK {
		t.Fatalf("reload first page status=%d body=%s", status, body)
	}
	var page usageRequestsTestResponse
	if err := json.Unmarshal(body, &page); err != nil || page.NextCursor == nil {
		t.Fatalf("reload first page cursor err=%v body=%s", err, body)
	}
	return page.NextCursor
}

func strPtr(value string) *string { return &value }
func intPtr(value int64) *int64   { return &value }

func ptrValue[T any](value *T) any {
	if value == nil {
		return nil
	}
	return *value
}
