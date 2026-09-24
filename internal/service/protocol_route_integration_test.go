// Independently authored acceptance tests for explicit persisted wire routes.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/accounting"
	"cpacloud.local/server/internal/governance"
)

type explicitWireFixture struct {
	app    *App
	server *httptest.Server
	key    keyView
}

func newExplicitWireFixture(t *testing.T, wire string, upstream http.Handler) explicitWireFixture {
	return newExplicitProviderWireFixture(t, "openai-compatible", wire, upstream)
}

func newExplicitProviderWireFixture(t *testing.T, providerKind, wire string, upstream http.Handler) explicitWireFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(upstream)
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() {
		server.Close()
		_ = app.Close()
		provider.Close()
	})
	cookie, csrf := loginTestAdmin(t, server.URL)
	created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams", marshalTestJSON(t, map[string]any{
		"name": "explicit wire", "provider_kind": providerKind, "endpoint": provider.URL, "api_key": "upstream-secret",
	}), cookie, csrf, server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream: %d %s", created.StatusCode, readBody(created))
	}
	var upstreamObject upstreamView
	decodeResponse(t, created, &upstreamObject)
	model := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
		"id": "wire-model", "upstream_id": upstreamObject.ID, "upstream_model": "actual-model", "wire_protocol": wire,
	}), cookie, csrf, server.URL)
	if model.StatusCode != http.StatusCreated {
		t.Fatalf("create model: %d %s", model.StatusCode, readBody(model))
	}
	model.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"Wire User"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee: %d %s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employee employee
	decodeResponse(t, employeeResponse, &employee)
	return explicitWireFixture{app: app, server: server, key: createTestKey(t, server.URL, employee.ID, "wire-key", cookie, csrf)}
}

func TestExplicitWireChatToResponsesDispatchesOnceAndAttributesUpstream(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer upstream-secret" {
			t.Errorf("request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"actual-model"`) || !strings.Contains(string(body), `"type":"message"`) {
			t.Errorf("unexpected converted request: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_wire","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	enableGovernanceForExplicitWire(t, fixture.app, fixture.key.ID)
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"wire-model","messages":[{"role":"user","content":"hi"}]}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["object"] != "chat.completion" || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "openai-responses")
	assertSingleWireUsage(t, fixture.app, nil, int64Ptr(2))
	assertSingleClientGovernance(t, fixture.app, "openai-chat-completions")
}

func TestExplicitWireResponsesToChatDispatchesOnceAndAttributesUpstream(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolOpenAIChat), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path=%q", r.URL.Path)
		}
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || !strings.Contains(string(body["messages"]), "hello") {
			t.Errorf("unexpected converted request: %#v err=%v", body, err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl_wire","object":"chat.completion","created":1,"model":"actual-model","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	enableGovernanceForExplicitWire(t, fixture.app, fixture.key.ID)
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"hello","store":false}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["object"] != "response" || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "openai-chat-completions")
	assertSingleWireUsage(t, fixture.app, nil, int64Ptr(2))
	assertSingleClientGovernance(t, fixture.app, "openai-responses")
}

func TestExplicitWireStrictBudgetRejectsUnprovedFinalWireBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	var adminID, employeeID string
	if err := fixture.app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, fixture.key.ID).Scan(&employeeID); err != nil {
		t.Fatal(err)
	}
	enabled, limit := true, int64(3_000_000)
	if _, err := fixture.app.governancePolicies.updateSettingsPatch(context.Background(), adminID, "a0000000-0000-4000-8000-000000000101", 1, true, &enabled); err != nil {
		t.Fatal(err)
	}
	if _, err := writeGeneralBudgetPolicy(context.Background(), fixture.app.store.db, adminID, "a0000000-0000-4000-8000-000000000102", "budget.create", "", 0, generalBudgetPolicy{
		ScopeKind: governance.ScopeEmployee, ScopeID: employeeID, Protocol: accounting.ProtocolOpenAIChatCompletions,
		Model: "wire-model", Enabled: true, TokenLimit: &limit,
	}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"wire-model","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":7}`, fixture.key.Key, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusServiceUnavailable || !strings.Contains(body, "budget_bound_unavailable") {
		t.Fatalf("status=%d body=%s", response.StatusCode, body)
	}
	if calls.Load() != 0 {
		t.Fatalf("unproved converted wire made %d upstream calls", calls.Load())
	}
	var attempts int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	var governanceProtocol, selectorProtocol, selectorModel string
	if err := fixture.app.store.db.QueryRow(`SELECT protocol FROM governance_requests`).Scan(&governanceProtocol); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT protocol,model FROM governance_general_budget_request_scopes`).Scan(&selectorProtocol, &selectorModel); err != nil {
		t.Fatal(err)
	}
	if governanceProtocol != "openai-chat-completions" || selectorProtocol != "openai-chat-completions" || selectorModel != "wire-model" {
		t.Fatalf("governance/selector protocol/model=%q/%q/%q", governanceProtocol, selectorProtocol, selectorModel)
	}
}

func TestExplicitWireCancellationPropagatesAndSettlesSharedLedgers(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	enableGovernanceForExplicitWire(t, fixture.app, fixture.key.ID)
	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, fixture.server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"wire-model","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+fixture.key.Key)
	request.Header.Set("Content-Type", "application/json")
	done := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			response.Body.Close()
		}
		done <- requestErr
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("converted request did not reach upstream")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach converted upstream request")
	}
	select {
	case requestErr := <-done:
		if requestErr == nil {
			t.Fatal("cancelled converted request unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled converted request did not return")
	}
	waitUsageHTTPStatus(t, fixture.app, "cancelled")
	var requestStatus, modelOutcome, governanceStatus, attemptStatus, protocol string
	if err := fixture.app.store.db.QueryRow(`SELECT a.status,m.outcome,g.status,t.status,c.protocol
		FROM accounting_requests a
		JOIN model_requests m ON m.id=a.id
		JOIN governance_requests g ON g.id=a.id
		JOIN accounting_attempts t ON t.request_id=a.id
		JOIN accounting_attempt_contexts c ON c.attempt_id=t.id`).Scan(&requestStatus, &modelOutcome, &governanceStatus, &attemptStatus, &protocol); err != nil {
		t.Fatal(err)
	}
	if requestStatus != "cancelled" || modelOutcome != "cancelled" || governanceStatus != "cancelled" || attemptStatus != "cancelled" || protocol != "openai-responses" || calls.Load() != 1 {
		t.Fatalf("request/model/governance/attempt/protocol/calls=%s/%s/%s/%s/%s/%d", requestStatus, modelOutcome, governanceStatus, attemptStatus, protocol, calls.Load())
	}
}

func TestExplicitCrossProtocolStreamRejectsBeforeDispatch(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"wire-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	if calls.Load() != 0 {
		t.Fatalf("cross-protocol stream dispatched %d upstream requests", calls.Load())
	}
	var attempts int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM accounting_attempt_contexts`).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestExplicitWireRateLimitPreservesRetryAfter(t *testing.T) {
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", `{"model":"wire-model","messages":[{"role":"user","content":"hi"}]}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "17" {
		t.Fatalf("status=%d retry-after=%q body=%s", response.StatusCode, response.Header.Get("Retry-After"), readBody(response))
	}
	response.Body.Close()
	assertSingleWireAttempt(t, fixture.app, "openai-responses")
}

func TestExplicitWireMessagesToResponsesUsesRuntime(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_messages","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	request, _ := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/messages", strings.NewReader(`{"model":"wire-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+fixture.key.Key)
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Content-Type", "application/json")
	response, err := http.DefaultClient.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("status=%v err=%v body=%s", statusOf(response), err, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["type"] != "message" || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "openai-responses")
	assertSingleWireUsage(t, fixture.app, nil, int64Ptr(2))
}

func TestExplicitWireGeminiToResponsesUsesRuntime(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitWireFixture(t, string(wireProtocolResponses), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/responses" {
			t.Errorf("request path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"resp_gemini","object":"response","created_at":1,"model":"actual-model","status":"completed","output":[{"id":"msg_1","type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}`)
	}))
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1beta/models/wire-model:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["candidates"] == nil || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "openai-responses")
	assertSingleWireUsage(t, fixture.app, nil, int64Ptr(2))
}

func TestExplicitWireResponsesToMessagesUsesRuntime(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitProviderWireFixture(t, anthropicAPIKeyProvider, string(wireProtocolMessages), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages" || r.Header.Get("Anthropic-Version") == "" {
			t.Errorf("request path=%q version=%q", r.URL.Path, r.Header.Get("Anthropic-Version"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"msg_upstream","type":"message","role":"assistant","model":"actual-model","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":4,"output_tokens":2}}`)
	}))
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"hello","max_output_tokens":16,"store":false}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["object"] != "response" || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "anthropic-messages")
	assertSingleWireUsage(t, fixture.app, int64Ptr(4), int64Ptr(2))
}

func TestExplicitWireResponsesToGeminiUsesRuntime(t *testing.T) {
	var calls atomic.Int32
	fixture := newExplicitProviderWireFixture(t, geminiAPIKeyProvider, string(wireProtocolGemini), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/models/actual-model:generateContent") || r.Header.Get("x-goog-api-key") != "upstream-secret" {
			t.Errorf("request path=%q key=%q", r.URL.Path, r.Header.Get("x-goog-api-key"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"responseId":"gemini_upstream","modelVersion":"actual-model","candidates":[{"content":{"role":"model","parts":[{"text":"hello"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":4,"candidatesTokenCount":2,"totalTokenCount":6}}`)
	}))
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"wire-model","input":"hello","max_output_tokens":16,"store":false}`, fixture.key.Key, context.Background())
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var body map[string]any
	decodeResponse(t, response, &body)
	if body["object"] != "response" || calls.Load() != 1 {
		t.Fatalf("response=%#v calls=%d", body, calls.Load())
	}
	assertSingleWireAttempt(t, fixture.app, "gemini-generate-content")
	assertSingleWireUsage(t, fixture.app, nil, int64Ptr(2))
}

func assertSingleWireAttempt(t *testing.T, app *App, protocol string) {
	t.Helper()
	var requests, attempts int
	var stored string
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&requests); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*),COALESCE(MIN(protocol),'') FROM accounting_attempt_contexts`).Scan(&attempts, &stored); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || attempts != 1 || stored != protocol {
		t.Fatalf("requests=%d attempts=%d protocol=%q", requests, attempts, stored)
	}
}

func assertSingleWireUsage(t *testing.T, app *App, input, output *int64) {
	t.Helper()
	var storedInput, storedOutput sql.NullInt64
	if err := app.store.db.QueryRow(`SELECT input_tokens,output_tokens FROM accounting_attempts`).Scan(&storedInput, &storedOutput); err != nil {
		t.Fatal(err)
	}
	for name, check := range map[string]struct {
		actual   sql.NullInt64
		expected *int64
	}{"input": {storedInput, input}, "output": {storedOutput, output}} {
		actual, expected := check.actual, check.expected
		if expected == nil && actual.Valid || expected != nil && (!actual.Valid || actual.Int64 != *expected) {
			t.Fatalf("%s tokens=%v want=%v", name, actual, expected)
		}
	}
}

func assertSingleClientGovernance(t *testing.T, app *App, protocol string) {
	t.Helper()
	var requests int
	var publicModel, storedProtocol string
	if err := app.store.db.QueryRow(`SELECT COUNT(*),COALESCE(MIN(public_model),''),COALESCE(MIN(protocol),'') FROM governance_requests`).Scan(&requests, &publicModel, &storedProtocol); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || publicModel != "wire-model" || storedProtocol != protocol {
		t.Fatalf("governance requests=%d public_model=%q protocol=%q want=%q", requests, publicModel, storedProtocol, protocol)
	}
}

func enableGovernanceForExplicitWire(t *testing.T, app *App, keyID string) {
	t.Helper()
	var adminID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins LIMIT 1`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	enabled := true
	if _, err := app.governancePolicies.updateSettingsPatch(context.Background(), adminID, "a0000000-0000-4000-8000-000000000100", 1, true, &enabled); err != nil {
		t.Fatal(err)
	}
	rpm := int64(100)
	if _, err := app.governancePolicies.createPolicy(context.Background(), adminID, "a0000000-0000-4000-8000-000000000103", governancePolicyInput{
		ScopeKind: governance.ScopeKey, ScopeID: keyID, Enabled: true, Hard: governanceHardLimits{RPM: &rpm},
	}); err != nil {
		t.Fatal(err)
	}
}

func TestRouteWireProtocolAdminPersistenceAndPoolPreservation(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer provider.Close()
	app := openTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams", marshalTestJSON(t, map[string]any{
		"name": "wire persistence", "provider_kind": "openai-compatible", "endpoint": provider.URL, "api_key": "upstream-secret",
	}), cookie, csrf, server.URL)
	var upstream upstreamView
	decodeResponse(t, created, &upstream)
	modelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{
		"id": "persisted-wire", "upstream_id": upstream.ID, "upstream_model": "actual-model", "wire_protocol": "openai-responses",
	}), cookie, csrf, server.URL)
	if modelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create model: %d %s", modelResponse.StatusCode, readBody(modelResponse))
	}
	var model modelView
	decodeResponse(t, modelResponse, &model)
	patchResponse := requestJSON(t, http.MethodPatch, server.URL+"/admin/api/v1/models/persisted-wire", `{"expected_revision":1,"enabled":true}`, cookie, csrf, server.URL)
	if patchResponse.StatusCode != http.StatusOK {
		t.Fatalf("patch model: %d %s", patchResponse.StatusCode, readBody(patchResponse))
	}
	decodeResponse(t, patchResponse, &model)
	if model.WireProtocol != "openai-responses" || model.Revision != 2 {
		t.Fatalf("omitted model wire was not preserved: %#v", model)
	}
	poolResponse := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/models/persisted-wire/accounts", "", cookie, "", "")
	var pool modelAccountsView
	decodeResponse(t, poolResponse, &pool)
	if pool.Revision != 0 || len(pool.Items) != 1 || pool.Items[0].WireProtocol == nil || *pool.Items[0].WireProtocol != "openai-responses" {
		t.Fatalf("legacy pool view lost explicit wire: %#v", pool)
	}
	putResponse := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/persisted-wire/accounts", marshalTestJSON(t, map[string]any{
		"expected_revision": 0,
		"items":             []map[string]any{{"upstream_id": upstream.ID, "upstream_model": "actual-model", "priority": 0, "weight": 1, "max_concurrency": 1}},
	}), cookie, csrf, server.URL)
	if putResponse.StatusCode != http.StatusOK {
		t.Fatalf("put pool: %d %s", putResponse.StatusCode, readBody(putResponse))
	}
	decodeResponse(t, putResponse, &pool)
	if pool.Items[0].WireProtocol == nil || *pool.Items[0].WireProtocol != "openai-responses" {
		t.Fatalf("omitted pool wire was not preserved: %#v", pool)
	}
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app = openTestApp(t, dataDir)
	defer app.Close()
	var modelWire, poolWire string
	if err := app.store.db.QueryRow(`SELECT wire_protocol FROM models WHERE id='persisted-wire'`).Scan(&modelWire); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT wire_protocol FROM model_account_pool_routes WHERE model_id='persisted-wire'`).Scan(&poolWire); err != nil {
		t.Fatal(err)
	}
	if modelWire != "openai-responses" || poolWire != "openai-responses" {
		t.Fatalf("restart wires model=%q pool=%q", modelWire, poolWire)
	}
}
