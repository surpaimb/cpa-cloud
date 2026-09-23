package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type pricingAdminFixture struct {
	t          *testing.T
	dataDir    string
	app        *App
	server     *httptest.Server
	cookie     *http.Cookie
	csrf       string
	upstreamID string
}

func newPricingAdminFixture(t *testing.T) *pricingAdminFixture {
	t.Helper()
	fixture := &pricingAdminFixture{t: t, dataDir: t.TempDir()}
	if err := Initialize(context.Background(), fixture.dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	fixture.open()
	created := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/upstreams",
		`{"name":"Synthetic pricing account","provider_kind":"openai-compatible","endpoint":"http://127.0.0.1:19091/v1","api_key":"synthetic-price-secret"}`,
		fixture.cookie, fixture.csrf, fixture.server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", created.StatusCode, readBody(created))
	}
	var upstream upstreamView
	decodeResponse(t, created, &upstream)
	fixture.upstreamID = upstream.ID
	t.Cleanup(func() { fixture.close() })
	return fixture
}

func (f *pricingAdminFixture) open() {
	f.t.Helper()
	f.app = openTestApp(f.t, f.dataDir)
	// Constructing the complete handler is itself a regression check for
	// ServeMux precedence with the Codex OAuth status route.
	f.server = httptest.NewServer(f.app.Handler())
	f.cookie, f.csrf = loginTestAdmin(f.t, f.server.URL)
}

func (f *pricingAdminFixture) close() {
	if f.server != nil {
		f.server.Close()
		f.server = nil
	}
	if f.app != nil {
		_ = f.app.Close()
		f.app = nil
	}
}

func (f *pricingAdminFixture) priceURL() string {
	return f.server.URL + "/admin/api/v1/upstreams/" + f.upstreamID + "/prices"
}

func TestPricingAdminVersionReplayDisableRestartAndRoutePrecedence(t *testing.T) {
	fixture := newPricingAdminFixture(t)
	model := strings.Repeat("m", 140) + " internal space " + strings.Repeat("n", 80)
	firstBody := priceAdminBody("550e8400-e29b-41d4-a716-446655440000", 0, model, "10")
	firstResponse := requestJSON(t, http.MethodPost, fixture.priceURL(), firstBody, fixture.cookie, fixture.csrf, fixture.server.URL)
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first price status=%d body=%s", firstResponse.StatusCode, readBody(firstResponse))
	}
	var first priceVersionView
	decodeResponse(t, firstResponse, &first)
	if first.UpstreamID != fixture.upstreamID || first.UpstreamModel != model || first.Revision != 1 || first.Price == nil || first.Price.InputPerMillionMicro != "10" {
		t.Fatalf("unexpected first response: %+v", first)
	}
	secondBody := priceAdminBody("650e8400-e29b-41d4-a716-446655440000", 1, model, "20")
	secondResponse := requestJSON(t, http.MethodPost, fixture.priceURL(), secondBody, fixture.cookie, fixture.csrf, fixture.server.URL)
	if secondResponse.StatusCode != http.StatusOK {
		t.Fatalf("second price status=%d body=%s", secondResponse.StatusCode, readBody(secondResponse))
	}
	var second priceVersionView
	decodeResponse(t, secondResponse, &second)
	if second.Revision != 2 || second.Version == first.Version {
		t.Fatalf("unexpected second response: %+v", second)
	}
	replayResponse := requestJSON(t, http.MethodPost, fixture.priceURL(), firstBody, fixture.cookie, fixture.csrf, fixture.server.URL)
	if replayResponse.StatusCode != http.StatusOK {
		t.Fatalf("replay status=%d body=%s", replayResponse.StatusCode, readBody(replayResponse))
	}
	var replay priceVersionView
	decodeResponse(t, replayResponse, &replay)
	if replay.Version != first.Version || replay.CreatedAt != first.CreatedAt {
		t.Fatalf("replay changed original: first=%+v replay=%+v", first, replay)
	}
	operationConflict := requestJSON(t, http.MethodPost, fixture.priceURL(),
		priceAdminBody("550e8400-e29b-41d4-a716-446655440000", 0, model, "11"), fixture.cookie, fixture.csrf, fixture.server.URL)
	assertCodexAdminError(t, operationConflict, http.StatusConflict, "operation_conflict")
	revisionConflict := requestJSON(t, http.MethodPost, fixture.priceURL(),
		priceAdminBody("6f0e8400-e29b-41d4-a716-446655440000", 0, model, "11"), fixture.cookie, fixture.csrf, fixture.server.URL)
	assertCodexAdminError(t, revisionConflict, http.StatusConflict, "revision_conflict")
	listed := requestJSON(t, http.MethodGet, fixture.priceURL(), "", fixture.cookie, "", "")
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.StatusCode, readBody(listed))
	}
	var list struct {
		Items []priceVersionView `json:"items"`
	}
	decodeResponse(t, listed, &list)
	if len(list.Items) != 1 || list.Items[0].Version != second.Version || list.Items[0].Price.InputPerMillionMicro != "20" {
		t.Fatalf("replay moved current pointer: %+v", list.Items)
	}
	disableBody := fmt.Sprintf(`{"operation_id":"750e8400-e29b-41d4-a716-446655440000","expected_revision":2,"upstream_model":%q,"price":null}`, model)
	disableResponse := requestJSON(t, http.MethodPost, fixture.priceURL(), disableBody, fixture.cookie, fixture.csrf, fixture.server.URL)
	if disableResponse.StatusCode != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disableResponse.StatusCode, readBody(disableResponse))
	}
	var disabled priceVersionView
	decodeResponse(t, disableResponse, &disabled)
	if disabled.Revision != 3 || disabled.Price != nil {
		t.Fatalf("unexpected disabled response: %+v", disabled)
	}

	// The literal OAuth session route must remain reachable rather than being
	// consumed by the generic price-list pattern.
	oauthStatus := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/missing", "", fixture.cookie, "", "")
	if !strings.HasPrefix(oauthStatus.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("OAuth route was shadowed: status=%d content-type=%q body=%s", oauthStatus.StatusCode, oauthStatus.Header.Get("Content-Type"), readBody(oauthStatus))
	}
	oauthStatus.Body.Close()

	fixture.close()
	fixture.open()
	listed = requestJSON(t, http.MethodGet, fixture.priceURL(), "", fixture.cookie, "", "")
	if listed.StatusCode != http.StatusOK {
		t.Fatalf("restart list status=%d body=%s", listed.StatusCode, readBody(listed))
	}
	decodeResponse(t, listed, &list)
	if len(list.Items) != 1 || list.Items[0].Version != disabled.Version || list.Items[0].Price != nil {
		t.Fatalf("restart lost disabled version: %+v", list.Items)
	}
}

func TestPricingAdminStrictJSONAuthorizationAndRedaction(t *testing.T) {
	fixture := newPricingAdminFixture(t)
	valid := priceAdminBody("850e8400-e29b-41d4-a716-446655440000", 0, "model-a", "1")
	unauthenticated := requestJSON(t, http.MethodGet, fixture.priceURL(), "", nil, "", "")
	assertCodexAdminError(t, unauthenticated, http.StatusUnauthorized, "authentication_required")
	employeeOnly, err := http.NewRequest(http.MethodGet, fixture.priceURL(), nil)
	if err != nil {
		t.Fatal(err)
	}
	employeeOnly.Header.Set("Authorization", "Bearer employee-key-marker")
	employeeResponse, err := http.DefaultClient.Do(employeeOnly)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexAdminError(t, employeeResponse, http.StatusUnauthorized, "authentication_required")
	missingCSRF := requestJSON(t, http.MethodPost, fixture.priceURL(), valid, fixture.cookie, "", fixture.server.URL)
	assertCodexAdminError(t, missingCSRF, http.StatusForbidden, "csrf_rejected")
	badOrigin := requestJSON(t, http.MethodPost, fixture.priceURL(), valid, fixture.cookie, fixture.csrf, "https://attacker.invalid")
	assertCodexAdminError(t, badOrigin, http.StatusForbidden, "origin_rejected")

	tests := []string{
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","operation_id":"950e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":null}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"USD","currency":"EUR","input_per_million_micro":"1","output_per_million_micro":"2","cache_read_per_million_micro":"3","cache_write_per_million_micro":"4"}}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a"}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":null,"api_key":"secret-marker"}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0.0,"upstream_model":"model-a","price":null}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":9007199254740992,"upstream_model":"model-a","price":null}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":" model-a","price":null}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model\u000amodel","price":null}`,
		fmt.Sprintf(`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":%q,"price":null}`, strings.Repeat("m", 257)),
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"USD","input_per_million_micro":"01","output_per_million_micro":"2","cache_read_per_million_micro":"3","cache_write_per_million_micro":"4"}}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"USD","input_per_million_micro":1,"output_per_million_micro":"2","cache_read_per_million_micro":"3","cache_write_per_million_micro":"4"}}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"usd","input_per_million_micro":"1","output_per_million_micro":"2","cache_read_per_million_micro":"3","cache_write_per_million_micro":"4"}}`,
		`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"model-a","price":{"currency":"USD","input_per_million_micro":"9007199254740992","output_per_million_micro":"2","cache_read_per_million_micro":"3","cache_write_per_million_micro":"4"}}`,
	}
	for index, body := range tests {
		response := requestJSON(t, http.MethodPost, fixture.priceURL(), body, fixture.cookie, fixture.csrf, fixture.server.URL)
		content := readBody(response)
		if response.StatusCode != http.StatusBadRequest || !strings.Contains(content, `"code":"invalid_request"`) {
			t.Fatalf("case %d status=%d body=%s", index, response.StatusCode, content)
		}
		if strings.Contains(content, "secret-marker") {
			t.Fatalf("case %d reflected secret: %s", index, content)
		}
	}
	invalidUTF8 := append([]byte(`{"operation_id":"850e8400-e29b-41d4-a716-446655440000","expected_revision":0,"upstream_model":"`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`","price":null}`)...)
	request, err := http.NewRequest(http.MethodPost, fixture.priceURL(), bytes.NewReader(invalidUTF8))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", fixture.server.URL)
	request.Header.Set("X-CSRF-Token", fixture.csrf)
	request.AddCookie(fixture.cookie)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	assertCodexAdminError(t, response, http.StatusBadRequest, "invalid_request")
	missing := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/upstreams/missing/prices", "", fixture.cookie, "", "")
	assertCodexAdminError(t, missing, http.StatusNotFound, "not_found")
}

func TestPricingAdminConcurrentRevisionCAS(t *testing.T) {
	fixture := newPricingAdminFixture(t)
	bodies := []string{
		priceAdminBody("950e8400-e29b-41d4-a716-446655440000", 0, "concurrent-model", "1"),
		priceAdminBody("a50e8400-e29b-41d4-a716-446655440000", 0, "concurrent-model", "2"),
	}
	statuses := make(chan int, len(bodies))
	errorsOut := make(chan error, len(bodies))
	var wg sync.WaitGroup
	for _, body := range bodies {
		body := body
		wg.Add(1)
		go func() {
			defer wg.Done()
			request, err := http.NewRequest(http.MethodPost, fixture.priceURL(), strings.NewReader(body))
			if err != nil {
				errorsOut <- err
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Origin", fixture.server.URL)
			request.Header.Set("X-CSRF-Token", fixture.csrf)
			request.AddCookie(fixture.cookie)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				errorsOut <- err
				return
			}
			_, _ = io.Copy(io.Discard, response.Body)
			response.Body.Close()
			statuses <- response.StatusCode
		}()
	}
	wg.Wait()
	close(statuses)
	close(errorsOut)
	for err := range errorsOut {
		t.Fatal(err)
	}
	counts := map[int]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts[http.StatusOK] != 1 || counts[http.StatusConflict] != 1 {
		t.Fatalf("concurrent statuses=%v", counts)
	}
}

func priceAdminBody(operationID string, expected int64, model, inputRate string) string {
	value := map[string]any{
		"operation_id": operationID, "expected_revision": expected, "upstream_model": model,
		"price": map[string]string{"currency": "USD", "input_per_million_micro": inputRate, "output_per_million_micro": "2", "cache_read_per_million_micro": "3", "cache_write_per_million_micro": "4"},
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
