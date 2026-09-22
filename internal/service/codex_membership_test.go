package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

type fakeCodexExecutor struct {
	mu         sync.RWMutex
	completeFn func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError)
	streamFn   func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest, func(codexExecutionEvent) error) *codexRunError
}

func (f *fakeCodexExecutor) Complete(ctx context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
	f.mu.RLock()
	fn := f.completeFn
	f.mu.RUnlock()
	return fn(ctx, credential, request)
}

func (f *fakeCodexExecutor) Stream(ctx context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
	f.mu.RLock()
	fn := f.streamFn
	f.mu.RUnlock()
	return fn(ctx, credential, request, consume)
}

func (f *fakeCodexExecutor) setComplete(fn func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError)) {
	f.mu.Lock()
	f.completeFn = fn
	f.mu.Unlock()
}

type codexServiceFixture struct {
	dataDir     string
	app         *App
	server      *httptest.Server
	cookie      *http.Cookie
	csrf        string
	upstream    upstreamView
	employee    employee
	employeeKey keyView
}

func newCodexServiceFixture(t *testing.T, executor codexExecutor) *codexServiceFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", ExperimentalCodexMembership: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	app.codex = executor
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	fixture := &codexServiceFixture{dataDir: dataDir, app: app, server: server, cookie: cookie, csrf: csrf}
	fixture.upstream = importTestCodexUpstream(t, server.URL, cookie, csrf, "550e8400-e29b-41d4-a716-446655440000", syntheticCodexAuth(t, "first-secret", time.Now().Add(time.Hour)))

	modelResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models",
		marshalTestJSON(t, map[string]any{"id": "company-codex", "upstream_id": fixture.upstream.ID, "upstream_model": "gpt-codex-provider"}), cookie, csrf, server.URL)
	if modelResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create Codex model status=%d body=%s", modelResponse.StatusCode, readBody(modelResponse))
	}
	modelResponse.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees",
		`{"name":"Codex User","department":"Engineering","note":"test"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	decodeResponse(t, employeeResponse, &fixture.employee)
	fixture.employeeKey = createTestKey(t, server.URL, fixture.employee.ID, "membership-key-op", cookie, csrf)
	return fixture
}

func (f *codexServiceFixture) close() {
	f.server.Close()
	_ = f.app.Close()
}

func TestCodexMembershipEmployeeTextMappingStateAndEmployeeDisable(t *testing.T) {
	var calls atomic.Int32
	requests := make(chan membership.CodexTextRequest, 2)
	runner := &fakeCodexExecutor{}
	runner.setComplete(func(_ context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		calls.Add(1)
		if !strings.Contains(string(credential.RawAuthJSONSecret()), "first-secret") {
			t.Error("fake executor received the wrong credential generation")
		}
		requests <- request
		input, output, total := int64(7), int64(3), int64(10)
		return codexExecutionResult{Text: "membership answer", Usage: membership.CodexUsage{InputTokens: &input, OutputTokens: &output, TotalTokens: &total}}, nil
	})
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()

	body := `{"model":"company-codex","messages":[{"role":"user","content":"question"},{"role":"assistant","content":"context"}]}`
	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	responseBody := readBody(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(responseBody, `"content":"membership answer"`) || !strings.Contains(responseBody, `"prompt_tokens":7`) {
		t.Fatalf("membership completion status=%d body=%s", response.StatusCode, responseBody)
	}
	mapped := <-requests
	if mapped.Model != "gpt-codex-provider" || len(mapped.Messages) != 2 || mapped.Messages[0].Role != membership.CodexRoleUser || mapped.Messages[0].Text != "question" || mapped.Messages[1].Role != membership.CodexRoleAssistant {
		t.Fatalf("mapped request = %#v", mapped)
	}
	var state string
	var verifiedAt *string
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state,verified_at FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if state != codexStateVerified || verifiedAt == nil || *verifiedAt == "" {
		t.Fatalf("successful state=%q verified_at=%v", state, verifiedAt)
	}

	unsupported := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions",
		`{"model":"company-codex","messages":[{"role":"user","content":"question"}],"tools":[]}`, fixture.employeeKey.Key, context.Background())
	unsupportedBody := readBody(unsupported)
	if unsupported.StatusCode != http.StatusBadRequest || !strings.Contains(unsupportedBody, `"code":"unsupported_feature"`) || calls.Load() != 1 {
		t.Fatalf("unsupported request status=%d calls=%d body=%s", unsupported.StatusCode, calls.Load(), unsupportedBody)
	}
	runner.setComplete(func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		calls.Add(1)
		return codexExecutionResult{}, &codexRunError{Code: membership.CodexErrorCancelled}
	})
	upstreamCancelled := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	upstreamCancelledBody := readBody(upstreamCancelled)
	if upstreamCancelled.StatusCode != http.StatusBadGateway || !strings.Contains(upstreamCancelledBody, `"code":"upstream_error"`) || calls.Load() != 2 {
		t.Fatalf("upstream cancellation status=%d calls=%d body=%s", upstreamCancelled.StatusCode, calls.Load(), upstreamCancelledBody)
	}

	disable := requestJSON(t, http.MethodPatch, fixture.server.URL+"/admin/api/v1/employees/"+fixture.employee.ID,
		`{"expected_revision":1,"status":"disabled"}`, fixture.cookie, fixture.csrf, fixture.server.URL)
	if disable.StatusCode != http.StatusOK {
		t.Fatalf("disable employee status=%d body=%s", disable.StatusCode, readBody(disable))
	}
	disable.Body.Close()
	rejected := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	if rejected.StatusCode != http.StatusUnauthorized {
		t.Fatalf("disabled employee status=%d body=%s", rejected.StatusCode, readBody(rejected))
	}
	rejected.Body.Close()
}

func TestCodexMembershipStreamingSuccessPartialFailureAndCancellation(t *testing.T) {
	runner := &fakeCodexExecutor{}
	runner.streamFn = func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
		output, total := int64(2), int64(5)
		for _, event := range []codexExecutionEvent{
			{Kind: membership.CodexEventStarted},
			{Kind: membership.CodexEventTextDelta, Text: "hello "},
			{Kind: membership.CodexEventTextDelta, Text: "stream"},
			{Kind: membership.CodexEventCompleted, Usage: membership.CodexUsage{OutputTokens: &output, TotalTokens: &total}},
		} {
			if err := consume(event); err != nil {
				return &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
			}
		}
		return nil
	}
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()
	body := `{"model":"company-codex","stream":true,"messages":[{"role":"user","content":"stream please"}]}`
	success := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	successBody := readBody(success)
	if success.StatusCode != http.StatusOK || !strings.Contains(successBody, `"role":"assistant"`) || !strings.Contains(successBody, `"content":"hello "`) || !strings.Contains(successBody, `"finish_reason":"stop"`) || !strings.Contains(successBody, "data: [DONE]") {
		t.Fatalf("stream success status=%d body=%s", success.StatusCode, successBody)
	}

	runner.mu.Lock()
	runner.streamFn = func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
		_ = consume(codexExecutionEvent{Kind: membership.CodexEventStarted})
		_ = consume(codexExecutionEvent{Kind: membership.CodexEventTextDelta, Text: "partial"})
		return &codexRunError{Code: membership.CodexErrorProtocol, UpstreamStatus: http.StatusOK}
	}
	runner.mu.Unlock()
	failure := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	failureBody := readBody(failure)
	if failure.StatusCode != http.StatusOK || !strings.Contains(failureBody, "event: error") || !strings.Contains(failureBody, `"code":"upstream_protocol_error"`) || strings.Contains(failureBody, `"finish_reason":"stop"`) || strings.Contains(failureBody, "data: [DONE]") {
		t.Fatalf("partial stream failure status=%d body=%s", failure.StatusCode, failureBody)
	}

	cancelled := make(chan struct{})
	runner.mu.Lock()
	runner.streamFn = func(ctx context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
		if err := consume(codexExecutionEvent{Kind: membership.CodexEventStarted}); err != nil {
			return &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
		}
		<-ctx.Done()
		close(cancelled)
		return &codexRunError{Code: membership.CodexErrorCancelled}
	}
	runner.mu.Unlock()
	requestContext, cancel := context.WithCancel(context.Background())
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, fixture.server.URL+"/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+fixture.employeeKey.Key)
	request.Header.Set("Content-Type", "application/json")
	cancelResponse, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	requestID := cancelResponse.Header.Get("X-Request-ID")
	cancel()
	cancelResponse.Body.Close()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not reach injected Codex executor")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var outcome string
		err := fixture.app.store.db.QueryRow(`SELECT outcome FROM model_requests WHERE id=?`, requestID).Scan(&outcome)
		if err == nil && outcome == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("cancelled request outcome=%q err=%v", outcome, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCodexMembershipStaleReauthenticationCannotOverrideReplacement(t *testing.T) {
	runner := &fakeCodexExecutor{}
	started := make(chan struct{})
	release := make(chan struct{})
	runner.setComplete(func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		close(started)
		<-release
		return codexExecutionResult{}, &codexRunError{Code: membership.CodexErrorReauthentication, UpstreamStatus: http.StatusUnauthorized}
	})
	fixture := newCodexServiceFixture(t, runner)
	defer fixture.close()
	body := `{"model":"company-codex","messages":[{"role":"user","content":"wait"}]}`
	responseChannel := make(chan *http.Response, 1)
	errorChannel := make(chan error, 1)
	go func() {
		request, _ := http.NewRequest(http.MethodPost, fixture.server.URL+"/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+fixture.employeeKey.Key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			errorChannel <- err
			return
		}
		responseChannel <- response
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight request did not reach fake executor")
	}
	replacement := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/upstreams/"+fixture.upstream.ID+"/codex-auth",
		marshalTestJSON(t, map[string]any{"expected_revision": 1, "auth_json": syntheticCodexAuth(t, "replacement-secret", time.Now().Add(time.Hour))}), fixture.cookie, fixture.csrf, fixture.server.URL)
	if replacement.StatusCode != http.StatusOK {
		t.Fatalf("replacement status=%d body=%s", replacement.StatusCode, readBody(replacement))
	}
	var replaced upstreamView
	decodeResponse(t, replacement, &replaced)
	if replaced.Revision != 2 || replaced.CredentialState == nil || *replaced.CredentialState != codexStateImported || replaced.VerifiedAt != nil {
		t.Fatalf("replacement response=%+v", replaced)
	}
	close(release)
	select {
	case err := <-errorChannel:
		t.Fatal(err)
	case stale := <-responseChannel:
		staleBody := readBody(stale)
		if stale.StatusCode != http.StatusBadGateway || !strings.Contains(staleBody, `"code":"upstream_reauthentication_required"`) {
			t.Fatalf("stale request status=%d body=%s", stale.StatusCode, staleBody)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stale request did not finish")
	}
	var state string
	var revision int64
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state,revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if state != codexStateImported || revision != 2 {
		t.Fatalf("stale 401 overwrote replacement: state=%q revision=%d", state, revision)
	}

	runner.setComplete(func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{}, &codexRunError{Code: membership.CodexErrorRateLimited, UpstreamStatus: http.StatusTooManyRequests, RetryAfter: 2 * time.Second}
	})
	rateLimited := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	rateBody := readBody(rateLimited)
	if rateLimited.StatusCode != http.StatusTooManyRequests || rateLimited.Header.Get("Retry-After") != "2" || !strings.Contains(rateBody, `"code":"upstream_rate_limited"`) {
		t.Fatalf("rate limit status=%d retry=%q body=%s", rateLimited.StatusCode, rateLimited.Header.Get("Retry-After"), rateBody)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state,revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if state != codexStateImported || revision != 2 {
		t.Fatalf("rate limit changed credential state=%q revision=%d", state, revision)
	}

	runner.setComplete(func(_ context.Context, credential *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		if !strings.Contains(string(credential.RawAuthJSONSecret()), "replacement-secret") {
			t.Error("new request did not use replacement credential")
		}
		return codexExecutionResult{Text: "fresh"}, nil
	})
	fresh := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	if fresh.StatusCode != http.StatusOK {
		t.Fatalf("fresh request status=%d body=%s", fresh.StatusCode, readBody(fresh))
	}
	fresh.Body.Close()
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state,revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if state != codexStateVerified || revision != 2 {
		t.Fatalf("fresh request state=%q revision=%d", state, revision)
	}

	runner.setComplete(func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{}, &codexRunError{Code: membership.CodexErrorReauthentication, UpstreamStatus: http.StatusUnauthorized}
	})
	unauthorized := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", body, fixture.employeeKey.Key, context.Background())
	unauthorizedBody := readBody(unauthorized)
	if unauthorized.StatusCode != http.StatusBadGateway || !strings.Contains(unauthorizedBody, `"code":"upstream_reauthentication_required"`) {
		t.Fatalf("upstream 401 status=%d body=%s", unauthorized.StatusCode, unauthorizedBody)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT credential_state,revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&state, &revision); err != nil {
		t.Fatal(err)
	}
	if state != codexStateReauth || revision != 2 {
		t.Fatalf("upstream 401 state=%q revision=%d", state, revision)
	}
	reimport := requestJSON(t, http.MethodPut, fixture.server.URL+"/admin/api/v1/upstreams/"+fixture.upstream.ID+"/codex-auth",
		marshalTestJSON(t, map[string]any{"expected_revision": 2, "auth_json": syntheticCodexAuth(t, "third-secret", time.Now().Add(time.Hour))}), fixture.cookie, fixture.csrf, fixture.server.URL)
	if reimport.StatusCode != http.StatusOK {
		t.Fatalf("reimport status=%d body=%s", reimport.StatusCode, readBody(reimport))
	}
	var reimported upstreamView
	decodeResponse(t, reimport, &reimported)
	if reimported.Revision != 3 || reimported.CredentialState == nil || *reimported.CredentialState != codexStateImported || reimported.VerifiedAt != nil {
		t.Fatalf("reimport response=%+v", reimported)
	}
}

func TestCodexMembershipExecutionDisabledAfterRestart(t *testing.T) {
	runner := &fakeCodexExecutor{}
	runner.setComplete(func(_ context.Context, _ *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{Text: "should not run"}, nil
	})
	fixture := newCodexServiceFixture(t, runner)
	dataDir, key := fixture.dataDir, fixture.employeeKey.Key
	fixture.close()
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions",
		`{"model":"company-codex","messages":[{"role":"user","content":"disabled"}]}`, key, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusForbidden || !strings.Contains(body, `"code":"feature_disabled"`) {
		t.Fatalf("disabled execution status=%d body=%s", response.StatusCode, body)
	}
}

func importTestCodexUpstream(t *testing.T, baseURL string, cookie *http.Cookie, csrf, operationID, authJSON string) upstreamView {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/upstreams/codex-import",
		marshalTestJSON(t, map[string]any{"name": "Team Codex", "auth_json": authJSON, "operation_id": operationID}), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated && response.StatusCode != http.StatusOK {
		t.Fatalf("import Codex upstream status=%d body=%s", response.StatusCode, readBody(response))
	}
	var item upstreamView
	decodeResponse(t, response, &item)
	return item
}

func syntheticCodexAuth(t *testing.T, marker string, expiresAt time.Time) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{"exp": expiresAt.Unix(), "marker": marker})
	if err != nil {
		t.Fatal(err)
	}
	token := header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + marker
	return marshalTestJSON(t, map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"access_token": token, "refresh_token": "refresh-" + marker, "account_id": "account-" + marker,
		},
	})
}

func marshalTestJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func readAllAndClose(t *testing.T, response *http.Response) string {
	t.Helper()
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
