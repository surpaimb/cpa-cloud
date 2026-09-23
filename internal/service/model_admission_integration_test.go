package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestModelAdmissionPoolUsesEnabledBackupAndScopesSession(t *testing.T) {
	var defaultCalls, backupCalls atomic.Int32
	defaultUpstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		defaultCalls.Add(1)
	}))
	defer defaultUpstream.Close()
	backupUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backupCalls.Add(1)
		if r.Header.Get("X-CPA-Session") != "" {
			t.Error("X-CPA-Session was forwarded upstream")
		}
		if r.Header.Get("Authorization") != "Bearer backup-secret" {
			t.Errorf("upstream authorization=%q", r.Header.Get("Authorization"))
		}
		var payload map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if string(payload["model"]) != `"provider-backup"` {
			t.Errorf("upstream model=%s", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat_pool","object":"chat.completion","choices":[]}`)
	}))
	defer backupUpstream.Close()

	app, server, cookie, csrf := newModelAdmissionApp(t, false)
	defaultAccount := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "default", "openai-compatible", defaultUpstream.URL, "default-secret")
	backupAccount := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "backup", "openai-compatible", backupUpstream.URL, "backup-secret")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "pool-chat", defaultAccount.ID, "provider-default")
	if _, err := app.store.db.Exec(`UPDATE upstreams SET enabled=0,revision=revision+1 WHERE id=?`, defaultAccount.ID); err != nil {
		t.Fatal(err)
	}
	putModelAdmissionPool(t, server.URL, cookie, csrf, "pool-chat", backupAccount.ID, "provider-backup")

	employeeOne := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Pool Employee One")
	keyOne := createTestKey(t, server.URL, employeeOne.ID, "pool-session-key-one", cookie, csrf)
	keyTwo := createTestKey(t, server.URL, employeeOne.ID, "pool-session-key-two", cookie, csrf)
	employeeTwo := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Pool Employee Two")
	keyThree := createTestKey(t, server.URL, employeeTwo.ID, "pool-session-key-three", cookie, csrf)

	request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"pool-chat","messages":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+keyOne.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-CPA-Session", "same-client-session")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("pooled chat status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	if defaultCalls.Load() != 0 || backupCalls.Load() != 1 {
		t.Fatalf("default calls=%d backup calls=%d", defaultCalls.Load(), backupCalls.Load())
	}

	digestRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	digestRequest.Header.Set("X-CPA-Session", "same-client-session")
	digestOne, failure := app.poolSessionDigest(digestRequest, employeeAuth{EmployeeID: employeeOne.ID, KeyID: keyOne.ID}, "pool-chat")
	if failure != nil || digestOne == "" || strings.Contains(digestOne, "same-client-session") {
		t.Fatalf("first session digest=%q failure=%v", digestOne, failure)
	}
	digestAgain, _ := app.poolSessionDigest(digestRequest, employeeAuth{EmployeeID: employeeOne.ID, KeyID: keyOne.ID}, "pool-chat")
	digestOtherKey, _ := app.poolSessionDigest(digestRequest, employeeAuth{EmployeeID: employeeOne.ID, KeyID: keyTwo.ID}, "pool-chat")
	digestOtherEmployee, _ := app.poolSessionDigest(digestRequest, employeeAuth{EmployeeID: employeeTwo.ID, KeyID: keyThree.ID}, "pool-chat")
	if digestAgain != digestOne || digestOtherKey == digestOne || digestOtherEmployee == digestOne || digestOtherKey == digestOtherEmployee {
		t.Fatalf("session namespace collision: one=%q again=%q key=%q employee=%q", digestOne, digestAgain, digestOtherKey, digestOtherEmployee)
	}
}

func TestModelAdmissionCountTokensReleasesPoolLeaseWithoutGenerationRecord(t *testing.T) {
	var calls atomic.Int32
	fixture := newAnthropicFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/v1/messages/count_tokens" {
			t.Errorf("upstream path=%q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"input_tokens":17}`)
	}))
	var accountID string
	if err := fixture.app.store.db.QueryRow(`SELECT upstream_id FROM models WHERE id='company-claude'`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	putModelAdmissionPool(t, fixture.server.URL, fixture.cookie, fixture.csrf, "company-claude", accountID, "claude-provider-model")
	body := `{"model":"company-claude","max_tokens":8,"messages":[{"role":"user","content":"count"}]}`
	for i := 0; i < 2; i++ {
		response := anthropicEmployeeRequest(t, context.Background(), http.MethodPost, fixture.server.URL+"/v1/messages/count_tokens", body, fixture.key.Key, "bearer")
		if response.StatusCode != http.StatusOK || readBody(response) != `{"input_tokens":17}` {
			t.Fatalf("count %d status=%d", i, response.StatusCode)
		}
		var leases int
		if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_leases`).Scan(&leases); err != nil || leases != 0 {
			t.Fatalf("count %d retained leases=%d err=%v", i, leases, err)
		}
	}
	var generated int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests`).Scan(&generated); err != nil || generated != 0 || calls.Load() != 2 {
		t.Fatalf("generation records=%d calls=%d err=%v", generated, calls.Load(), err)
	}
}

func TestModelAdmissionQueuedRevocationReturnsBeforeActiveRequestReleases(t *testing.T) {
	entered := make(chan struct{}, 1)
	unblock := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-unblock
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chat_active","object":"chat.completion","choices":[]}`)
	}))
	defer upstream.Close()
	_, server, cookie, csrf := newModelAdmissionApp(t, false)
	account := createModelAdmissionUpstream(t, server.URL, cookie, csrf, "queued", "openai-compatible", upstream.URL, "queued-secret")
	createModelAdmissionModel(t, server.URL, cookie, csrf, "queued-model", account.ID, "provider-queued")
	putModelAdmissionPool(t, server.URL, cookie, csrf, "queued-model", account.ID, "provider-queued")
	employeeOne := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Active Employee")
	keyOne := createTestKey(t, server.URL, employeeOne.ID, "active-key", cookie, csrf)
	employeeTwo := createModelAdmissionEmployee(t, server.URL, cookie, csrf, "Queued Employee")
	keyTwo := createTestKey(t, server.URL, employeeTwo.ID, "queued-key", cookie, csrf)

	type requestResult struct {
		response *http.Response
		err      error
	}
	doRequest := func(key string) requestResult {
		request, err := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"queued-model","messages":[]}`))
		if err != nil {
			return requestResult{err: err}
		}
		request.Header.Set("Authorization", "Bearer "+key)
		request.Header.Set("Content-Type", "application/json")
		response, err := http.DefaultClient.Do(request)
		return requestResult{response: response, err: err}
	}
	active := make(chan requestResult, 1)
	go func() { active <- doRequest(keyOne.Key) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("active request did not reach upstream")
	}
	queued := make(chan requestResult, 1)
	go func() { queued <- doRequest(keyTwo.Key) }()
	select {
	case result := <-queued:
		close(unblock)
		if result.err != nil {
			t.Fatalf("queued request failed before revocation: %v", result.err)
		}
		t.Fatalf("queued request returned before revocation: status=%d body=%s", result.response.StatusCode, readBody(result.response))
	case <-time.After(50 * time.Millisecond):
	}
	revoke := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/keys/"+keyTwo.ID+"/revoke", `{}`, cookie, csrf, server.URL)
	if revoke.StatusCode != http.StatusOK {
		close(unblock)
		t.Fatalf("revoke queued key status=%d body=%s", revoke.StatusCode, readBody(revoke))
	}
	revoke.Body.Close()
	select {
	case result := <-queued:
		if result.err != nil {
			close(unblock)
			t.Fatal(result.err)
		}
		body := readBody(result.response)
		if result.response.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Invalid API key") {
			close(unblock)
			t.Fatalf("queued revocation status=%d body=%s", result.response.StatusCode, body)
		}
	case <-time.After(time.Second):
		close(unblock)
		t.Fatal("queued revoked request waited for active capacity")
	}
	close(unblock)
	result := <-active
	if result.err != nil {
		t.Fatal(result.err)
	}
	if result.response.StatusCode != http.StatusOK {
		t.Fatalf("active request status=%d body=%s", result.response.StatusCode, readBody(result.response))
	}
	result.response.Body.Close()
}

func TestModelAdmissionCodexPoolUsesSharedRefreshForResponses(t *testing.T) {
	chat := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		return codexExecutionResult{Text: "unused"}, nil
	}}
	fixture := newCodexServiceFixture(t, chat)
	defer fixture.close()
	putModelAdmissionPool(t, fixture.server.URL, fixture.cookie, fixture.csrf, "company-codex", fixture.upstream.ID, "gpt-codex-provider")
	fixture.app.cfg.CodexOAuthClientID = testOAuthClientID
	fixture.app.cfg.CodexOAuthRedirectURI = testOAuthRedirectURI
	bindOAuthTestUpstream(t, fixture.app, fixture.upstream.ID, testOAuthClientID)
	fixture.app.refresh.now = func() time.Time { return time.Now().UTC().Add(56 * time.Minute) }

	rotatedAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	rotatedID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "pool-account-refreshed"}})
	var tokenCalls, responseCalls atomic.Int32
	fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": rotatedAccess, "id_token": rotatedID, "refresh_token": "pool-refresh-rotated"}), nil
	})}
	fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, credential *membership.CodexAuthCredential, body []byte, _ func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		responseCalls.Add(1)
		if credential.AccessTokenSecret() != rotatedAccess || credential.RefreshTokenSecret() != "pool-refresh-rotated" || credential.AccountIDSecret() != "pool-account-refreshed" {
			t.Error("pooled Responses executor received stale credential")
		}
		if !strings.Contains(string(body), `"model":"gpt-codex-provider"`) {
			t.Errorf("pooled Responses body=%s", body)
		}
		return json.RawMessage(`{"id":"resp_pool","object":"response","status":"completed","output":[]}`), nil
	}}

	response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/responses", `{"model":"company-codex","input":"refresh through pool"}`, fixture.employeeKey.Key, context.Background())
	responseBody := readBody(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(responseBody, `"id":"resp_pool"`) {
		t.Fatalf("pooled Responses status=%d body=%s", response.StatusCode, responseBody)
	}
	var leases, succeeded int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_leases`).Scan(&leases); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE model_id='company-codex' AND outcome='succeeded'`).Scan(&succeeded); err != nil {
		t.Fatal(err)
	}
	if tokenCalls.Load() != 1 || responseCalls.Load() != 1 || leases != 0 || succeeded != 1 {
		t.Fatalf("token calls=%d response calls=%d leases=%d succeeded=%d", tokenCalls.Load(), responseCalls.Load(), leases, succeeded)
	}
}

func newModelAdmissionApp(t *testing.T, codex bool) (*App, *httptest.Server, *http.Cookie, string) {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true, ExperimentalCodexMembership: codex})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	t.Cleanup(func() { server.Close(); _ = app.Close() })
	cookie, csrf := loginTestAdmin(t, server.URL)
	return app, server, cookie, csrf
}

func createModelAdmissionUpstream(t *testing.T, baseURL string, cookie *http.Cookie, csrf, name, provider, endpoint, apiKey string) upstreamView {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/upstreams", marshalTestJSON(t, map[string]any{"name": name, "provider_kind": provider, "endpoint": endpoint, "api_key": apiKey}), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream status=%d body=%s", response.StatusCode, readBody(response))
	}
	var item upstreamView
	decodeResponse(t, response, &item)
	return item
}

func createModelAdmissionModel(t *testing.T, baseURL string, cookie *http.Cookie, csrf, id, upstreamID, upstreamModel string) {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{"id": id, "upstream_id": upstreamID, "upstream_model": upstreamModel}), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create model status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
}

func createModelAdmissionEmployee(t *testing.T, baseURL string, cookie *http.Cookie, csrf, name string) employee {
	t.Helper()
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/employees", marshalTestJSON(t, map[string]string{"name": name}), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("create employee status=%d body=%s", response.StatusCode, readBody(response))
	}
	var item employee
	decodeResponse(t, response, &item)
	return item
}

func putModelAdmissionPool(t *testing.T, baseURL string, cookie *http.Cookie, csrf, model, account, upstreamModel string) {
	t.Helper()
	body := marshalTestJSON(t, map[string]any{"expected_revision": 0, "items": []map[string]any{{"upstream_id": account, "upstream_model": upstreamModel, "priority": 0, "weight": 1, "max_concurrency": 1}}})
	response := requestJSON(t, http.MethodPut, baseURL+"/admin/api/v1/models/"+model+"/accounts", body, cookie, csrf, baseURL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("configure pool for %s status=%d body=%s", model, response.StatusCode, readBody(response))
	}
	response.Body.Close()
}
