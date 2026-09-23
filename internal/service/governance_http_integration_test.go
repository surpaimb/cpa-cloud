package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/governance"
)

type governanceHTTPHarness struct {
	app          *App
	key          keyView
	calls        *atomic.Int32
	request      func(bool) *http.Response
	jsonMarker   string
	streamMarker string
}

func TestGovernanceHTTPFourProtocolsReleaseAndEnforceSharedScopes(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T) governanceHTTPHarness
	}{
		{"openai chat", newGovernanceHTTPChatHarness},
		{"responses", newGovernanceHTTPResponsesHarness},
		{"anthropic messages", newGovernanceHTTPAnthropicHarness},
		{"gemini generate content", newGovernanceHTTPGeminiHarness},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := test.setup(t)
			configureGovernanceHTTPScopes(t, h.app, h.key.ID, int64Ptr(2), int64Ptr(1))

			jsonResponse := h.request(false)
			jsonBody := readBody(jsonResponse)
			if jsonResponse.StatusCode != http.StatusOK || !strings.Contains(jsonBody, h.jsonMarker) {
				t.Fatalf("JSON status=%d body=%s", jsonResponse.StatusCode, jsonBody)
			}
			streamResponse := h.request(true)
			streamBody := readBody(streamResponse)
			if streamResponse.StatusCode != http.StatusOK || !strings.Contains(streamBody, h.streamMarker) {
				t.Fatalf("SSE status=%d body=%s", streamResponse.StatusCode, streamBody)
			}

			// Concurrency is one on every scope. The second sequential request can
			// succeed only if the first terminal transaction released all leases.
			limited := h.request(false)
			limitedBody := readBody(limited)
			if limited.StatusCode != http.StatusTooManyRequests || !strings.Contains(limitedBody, "Request limit reached") {
				t.Fatalf("limit status=%d body=%s", limited.StatusCode, limitedBody)
			}
			if got := h.calls.Load(); got != 2 {
				t.Fatalf("upstream calls=%d want=2", got)
			}
			assertGovernanceHTTPSuccessRows(t, h.app, 2, 3)
			requests, attempts := usageHTTPCounts(t, h.app)
			if requests != 2 || attempts != 2 {
				t.Fatalf("accounting requests/attempts=%d/%d want=2/2", requests, attempts)
			}
		})
	}
}

func TestGovernanceHTTPFailureEventReleasesWithoutSuccess(t *testing.T) {
	var calls atomic.Int32
	var success atomic.Bool
	app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		if !success.Load() {
			_, _ = io.WriteString(w, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"status\":\"failed\",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
			return
		}
		_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"after_failure\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n")
	}))
	configureGovernanceHTTPScopes(t, app, key.ID, nil, int64Ptr(1))
	body := `{"model":"company-responses","stream":true,"input":"x"}`
	failed := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", body, key.Key, context.Background())
	failedBody := readBody(failed)
	if strings.Contains(failedBody, "response.completed") || !strings.Contains(failedBody, "upstream_protocol_error") {
		t.Fatalf("failed event escaped as success: status=%d body=%s", failed.StatusCode, failedBody)
	}
	assertGovernanceHTTPStatus(t, app, "failed", true)

	success.Store(true)
	recovered := employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", body, key.Key, context.Background())
	recoveredBody := readBody(recovered)
	if recovered.StatusCode != http.StatusOK || !strings.Contains(recoveredBody, "response.completed") {
		t.Fatalf("request after failure status=%d body=%s", recovered.StatusCode, recoveredBody)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d want=2", calls.Load())
	}
	assertGovernanceHTTPTerminalRows(t, app, 1, 1, 2, 6, 3)
}

func TestGovernanceHTTPCancellationReleasesWithoutSuccess(t *testing.T) {
	var calls atomic.Int32
	var allowSuccess atomic.Bool
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if !allowSuccess.Load() {
			_, _ = io.Copy(io.Discard, r.Body)
			_ = r.Body.Close()
			started <- struct{}{}
			select {
			case <-r.Context().Done():
				cancelled <- struct{}{}
			case <-release:
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"after_cancel","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	defer upstream.Close()
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	configureGovernanceHTTPScopes(t, app, key.ID, nil, int64Ptr(1))

	ctx, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"usage-chat","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+key.Key)
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
		t.Fatal("cancelled request did not reach upstream")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not reach upstream")
	}
	select {
	case requestErr := <-done:
		if requestErr == nil {
			t.Fatal("cancelled employee request unexpectedly succeeded")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancelled client request did not return")
	}
	waitUsageHTTPStatus(t, app, "cancelled")
	assertGovernanceHTTPStatus(t, app, "cancelled", true)
	assertGovernanceHTTPScalarString(t, app, `SELECT status FROM accounting_requests`, "cancelled")
	assertGovernanceHTTPScalarString(t, app, `SELECT outcome FROM model_requests`, "cancelled")

	allowSuccess.Store(true)
	recovered := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
	recoveredBody := readBody(recovered)
	if recovered.StatusCode != http.StatusOK || !strings.Contains(recoveredBody, "after_cancel") {
		t.Fatalf("request after cancellation status=%d body=%s", recovered.StatusCode, recoveredBody)
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls=%d want=2", calls.Load())
	}
	var succeeded, cancelledCount, released int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE status='succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE status='cancelled'`).Scan(&cancelledCount); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE released_at IS NOT NULL`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if succeeded != 1 || cancelledCount != 1 || released != 2 {
		t.Fatalf("governance succeeded/cancelled/released=%d/%d/%d want=1/1/2", succeeded, cancelledCount, released)
	}
}

func TestGovernanceHTTPRevocationAndCountTokensDoNotReserve(t *testing.T) {
	t.Run("revoked key", func(t *testing.T) {
		var calls atomic.Int32
		upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
		defer upstream.Close()
		app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
		configureGovernanceHTTPScopes(t, app, key.ID, int64Ptr(1), int64Ptr(1))
		if _, err := app.store.db.Exec(`UPDATE access_keys SET revoked_at=? WHERE id=?`, utcNow(), key.ID); err != nil {
			t.Fatal(err)
		}
		response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", `{"model":"usage-chat","messages":[]}`, key.Key, context.Background())
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked status=%d body=%s", response.StatusCode, readBody(response))
		}
		response.Body.Close()
		if calls.Load() != 0 {
			t.Fatalf("revoked key reached upstream %d times", calls.Load())
		}
		assertGovernanceHTTPTableCount(t, app, "governance_requests", 0)
		assertGovernanceHTTPTableCount(t, app, "accounting_requests", 0)
		assertGovernanceHTTPTableCount(t, app, "accounting_attempts", 0)
		assertGovernanceHTTPTableCount(t, app, "model_requests", 0)
	})

	t.Run("anthropic count_tokens", func(t *testing.T) {
		var calls atomic.Int32
		fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			if strings.HasSuffix(r.URL.Path, "/count_tokens") {
				_, _ = io.WriteString(w, `{"input_tokens":9}`)
				return
			}
			_, _ = io.WriteString(w, `{"id":"governed_message","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
		}))
		configureGovernanceHTTPScopes(t, fixture.app, fixture.key.ID, int64Ptr(1), int64Ptr(1))
		body := `{"model":"company-claude","max_tokens":8,"messages":[]}`
		counted := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages/count_tokens", body, fixture.key.Key, "bearer")
		if counted.StatusCode != http.StatusOK || readBody(counted) != `{"input_tokens":9}` {
			t.Fatalf("count_tokens response was not preserved")
		}
		assertGovernanceHTTPTableCount(t, fixture.app, "governance_requests", 0)
		assertGovernanceHTTPTableCount(t, fixture.app, "accounting_requests", 0)
		assertGovernanceHTTPTableCount(t, fixture.app, "model_requests", 0)

		generated := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "bearer")
		if generated.StatusCode != http.StatusOK {
			t.Fatalf("messages status=%d body=%s", generated.StatusCode, readBody(generated))
		}
		generated.Body.Close()
		limited := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "bearer")
		limitedBody := readBody(limited)
		if limited.StatusCode != http.StatusTooManyRequests || !strings.Contains(limitedBody, "Request limit reached") {
			t.Fatalf("messages limit status=%d body=%s", limited.StatusCode, limitedBody)
		}
		if calls.Load() != 2 {
			t.Fatalf("count_tokens plus generation upstream calls=%d want=2", calls.Load())
		}
		assertGovernanceHTTPSuccessRows(t, fixture.app, 1, 3)
	})
}

type governanceHTTPRecorder struct {
	*httptest.ResponseRecorder
	onStorageFailure func()
	once             sync.Once
}

func (r *governanceHTTPRecorder) Write(body []byte) (int, error) {
	if bytes.Contains(body, []byte("storage_unavailable")) {
		r.once.Do(r.onStorageFailure)
	}
	return r.ResponseRecorder.Write(body)
}

func TestGovernanceHTTPTerminalFailureRollsBackAndRetriesFrozenSettlement(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"governance_partial\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	app, _, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	configureGovernanceHTTPScopes(t, app, key.ID, nil, int64Ptr(1))
	mustUsageHTTPExec(t, app, `CREATE TRIGGER governance_http_fail_terminal BEFORE UPDATE OF status ON governance_requests WHEN NEW.status<>'pending' BEGIN SELECT RAISE(ABORT,'synthetic governance terminal failure'); END`)

	observedRollback := false
	recorder := &governanceHTTPRecorder{ResponseRecorder: httptest.NewRecorder()}
	recorder.onStorageFailure = func() {
		observedRollback = true
		assertGovernanceHTTPStatus(t, app, "pending", false)
		assertGovernanceHTTPScalarString(t, app, `SELECT status FROM accounting_requests`, "pending")
		assertGovernanceHTTPScalarString(t, app, `SELECT status FROM accounting_attempts`, "pending")
		assertGovernanceHTTPScalarString(t, app, `SELECT outcome FROM model_requests`, "running")
		mustUsageHTTPExec(t, app, `DROP TRIGGER governance_http_fail_terminal`)
	}
	request := httptest.NewRequest(http.MethodPost, "http://service.invalid/v1/chat/completions", strings.NewReader(`{"model":"usage-chat","stream":true,"messages":[]}`))
	request.Header.Set("Authorization", "Bearer "+key.Key)
	request.Header.Set("Content-Type", "application/json")
	app.Handler().ServeHTTP(recorder, request)

	if !observedRollback {
		t.Fatal("terminal storage failure was not observed")
	}
	body := recorder.Body.String()
	if strings.Contains(body, "[DONE]") || !strings.Contains(body, "governance_partial") || !strings.Contains(body, "storage_unavailable") {
		t.Fatalf("terminal failure response status=%d body=%s", recorder.Code, body)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls=%d want=1", calls.Load())
	}
	assertGovernanceHTTPStatus(t, app, "succeeded", true)
	assertGovernanceHTTPScalarString(t, app, `SELECT status FROM accounting_requests`, "succeeded")
	assertGovernanceHTTPScalarString(t, app, `SELECT status FROM accounting_attempts`, "succeeded")
	assertGovernanceHTTPScalarString(t, app, `SELECT outcome FROM model_requests`, "succeeded")
	var governanceEnd, requestEnd, attemptEnd, modelEnd string
	if err := app.store.db.QueryRow(`SELECT g.observed_finished_at,r.finished_at,a.finished_at,m.finished_at
		FROM governance_requests g
		JOIN accounting_requests r ON r.id=g.id
		JOIN accounting_attempts a ON a.request_id=g.id
		JOIN model_requests m ON m.id=g.id`).Scan(&governanceEnd, &requestEnd, &attemptEnd, &modelEnd); err != nil {
		t.Fatal(err)
	}
	governanceTime, err := time.Parse(time.RFC3339Nano, governanceEnd)
	if err != nil {
		t.Fatal(err)
	}
	for _, stamp := range []string{requestEnd, attemptEnd, modelEnd} {
		parsed, parseErr := time.Parse(time.RFC3339Nano, stamp)
		if parseErr != nil || !parsed.Equal(governanceTime) {
			t.Fatalf("retried terminal snapshots differ governance/request/attempt/model=%q/%q/%q/%q parse=%v", governanceEnd, requestEnd, attemptEnd, modelEnd, parseErr)
		}
	}
	assertUsageHTTPActiveCleared(t, app)
}

func newGovernanceHTTPChatHarness(t *testing.T) governanceHTTPHarness {
	t.Helper()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"governed_chat_sse\",\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":1,\"total_tokens\":3}}\n\ndata: [DONE]\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"governed_chat_json","choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"total_tokens":3}}`)
	}))
	t.Cleanup(upstream.Close)
	app, server, key, _ := newUsageHTTPChatFixture(t, upstream.URL)
	return governanceHTTPHarness{app: app, key: key, calls: &calls, jsonMarker: "governed_chat_json", streamMarker: "[DONE]", request: func(stream bool) *http.Response {
		body := `{"model":"usage-chat","messages":[]}`
		if stream {
			body = `{"model":"usage-chat","stream":true,"messages":[]}`
		}
		return employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions", body, key.Key, context.Background())
	}}
}

func newGovernanceHTTPResponsesHarness(t *testing.T) governanceHTTPHarness {
	t.Helper()
	var calls atomic.Int32
	app, server, key := newResponsesAPIKeyFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"governed_response_sse\",\"object\":\"response\",\"status\":\"completed\",\"output\":[],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"governed_response_json","object":"response","status":"completed","output":[],"usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	return governanceHTTPHarness{app: app, key: key, calls: &calls, jsonMarker: "governed_response_json", streamMarker: "response.completed", request: func(stream bool) *http.Response {
		body := `{"model":"company-responses","input":"x"}`
		if stream {
			body = `{"model":"company-responses","stream":true,"input":"x"}`
		}
		return employeeRequest(t, http.MethodPost, server.URL+"/v1/responses", body, key.Key, context.Background())
	}}
}

func newGovernanceHTTPAnthropicHarness(t *testing.T) governanceHTTPHarness {
	t.Helper()
	var calls atomic.Int32
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var payload struct {
			Stream bool `json:"stream"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		if payload.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"governed_anthropic_sse\",\"usage\":{\"input_tokens\":2,\"output_tokens\":0}}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"governed_anthropic_json","type":"message","role":"assistant","content":[],"stop_reason":"end_turn","usage":{"input_tokens":2,"output_tokens":1}}`)
	}))
	return governanceHTTPHarness{app: fixture.app, key: fixture.key, calls: &calls, jsonMarker: "governed_anthropic_json", streamMarker: "message_stop", request: func(stream bool) *http.Response {
		body := `{"model":"company-claude","max_tokens":8,"messages":[]}`
		if stream {
			body = `{"model":"company-claude","max_tokens":8,"stream":true,"messages":[]}`
		}
		return anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages", body, fixture.key.Key, "bearer")
	}}
}

func newGovernanceHTTPGeminiHarness(t *testing.T) governanceHTTPHarness {
	t.Helper()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":2,\"candidatesTokenCount\":1,\"totalTokenCount\":3}}\n\n")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"candidates":[{"finishReason":"STOP"}],"modelVersion":"governed_gemini_json","usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":1,"totalTokenCount":3}}`)
	}))
	t.Cleanup(upstream.Close)
	server, app, _, _, _, _, key := setupGeminiTest(t, upstream.URL)
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	return governanceHTTPHarness{app: app, key: key, calls: &calls, jsonMarker: "governed_gemini_json", streamMarker: "finishReason", request: func(stream bool) *http.Response {
		path := "/v1beta/models/company-gemini:generateContent"
		if stream {
			path = "/v1beta/models/company-gemini:streamGenerateContent?alt=sse"
		}
		return employeeRequest(t, http.MethodPost, server.URL+path, `{"contents":[{"role":"user","parts":[{"text":"x"}]}]}`, key.Key, context.Background())
	}}
}

func configureGovernanceHTTPScopes(t *testing.T, app *App, keyID string, rpm, concurrency *int64) {
	t.Helper()
	ctx := context.Background()
	var actorID, employeeID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins ORDER BY created_at,id LIMIT 1`).Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT employee_id FROM access_keys WHERE id=?`, keyID).Scan(&employeeID); err != nil {
		t.Fatal(err)
	}
	app.admission.Lock()
	defer app.admission.Unlock()
	if _, err := app.governancePolicies.updateSettings(ctx, actorID, governanceOperationID(801), 1, true); err != nil {
		t.Fatal(err)
	}
	group, err := app.governancePolicies.createGroup(ctx, actorID, governanceOperationID(802), "HTTP integration", []string{employeeID})
	if err != nil {
		t.Fatal(err)
	}
	for index, scope := range []struct {
		kind governance.ScopeKind
		id   string
	}{
		{governance.ScopeEmployee, employeeID},
		{governance.ScopeKey, keyID},
		{governance.ScopeGroup, group.ResourceID},
	} {
		_, err := app.governancePolicies.createPolicy(ctx, actorID, governanceOperationID(803+index), governancePolicyInput{
			ScopeKind: scope.kind,
			ScopeID:   scope.id,
			Enabled:   true,
			Hard:      governanceHardLimits{RPM: rpm, Concurrency: concurrency},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func assertGovernanceHTTPSuccessRows(t *testing.T, app *App, requests, scopesPerRequest int) {
	t.Helper()
	assertGovernanceHTTPTerminalRows(t, app, requests, 0, requests, requests*scopesPerRequest, scopesPerRequest)
}

func assertGovernanceHTTPTerminalRows(t *testing.T, app *App, succeededWant, failedWant, releasedWant, scopesWant, kindsWant int) {
	t.Helper()
	var succeeded, failed, released, scopes, kinds int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE status='succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE status='failed'`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_requests WHERE released_at IS NOT NULL`).Scan(&released); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM governance_request_scopes`).Scan(&scopes); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(DISTINCT scope_kind) FROM governance_request_scopes`).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if succeeded != succeededWant || failed != failedWant || released != releasedWant || scopes != scopesWant || kinds != kindsWant {
		t.Fatalf("governance succeeded/failed/released/scopes/kinds=%d/%d/%d/%d/%d want=%d/%d/%d/%d/%d", succeeded, failed, released, scopes, kinds, succeededWant, failedWant, releasedWant, scopesWant, kindsWant)
	}
}

func assertGovernanceHTTPStatus(t *testing.T, app *App, want string, released bool) {
	t.Helper()
	var status string
	var releasedAt sql.NullString
	if err := app.store.db.QueryRow(`SELECT status,released_at FROM governance_requests ORDER BY observed_started_at DESC,id DESC LIMIT 1`).Scan(&status, &releasedAt); err != nil {
		t.Fatal(err)
	}
	if status != want || releasedAt.Valid != released {
		t.Fatalf("governance status/released=%q/%v want=%q/%v", status, releasedAt.Valid, want, released)
	}
}

func assertGovernanceHTTPTableCount(t *testing.T, app *App, table string, want int) {
	t.Helper()
	var got int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s count=%d want=%d", table, got, want)
	}
}

func assertGovernanceHTTPScalarString(t *testing.T, app *App, query, want string) {
	t.Helper()
	var got string
	if err := app.store.db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q=%q want=%q", query, got, want)
	}
}
