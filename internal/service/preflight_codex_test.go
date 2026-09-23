package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestCodexPreflightFailoverChatAndResponsesUseSecondCredentialAndModel(t *testing.T) {
	protocols := []struct {
		name string
		path string
		body string
	}{
		{name: "chat", path: "/v1/chat/completions", body: `{"model":"company-codex","messages":[{"role":"user","content":"synthetic"}]}`},
		{name: "responses", path: "/v1/responses", body: `{"model":"company-codex","input":"synthetic"}`},
	}
	for index, protocol := range protocols {
		t.Run(protocol.name, func(t *testing.T) {
			var calls atomic.Int32
			runner := &fakeCodexExecutor{completeFn: func(_ context.Context, credential *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
				calls.Add(1)
				if request.Model != "actual-second" || !strings.Contains(credential.AccessTokenSecret(), "second-preflight") {
					t.Errorf("chat model=%q credential=%q", request.Model, credential.AccessTokenSecret())
				}
				return codexExecutionResult{Text: "ok"}, nil
			}}
			fixture := newCodexServiceFixture(t, runner)
			defer fixture.close()
			second := importTestCodexUpstream(t, fixture.server.URL, fixture.cookie, fixture.csrf,
				[]string{"650e8400-e29b-41d4-a716-446655440000", "750e8400-e29b-41d4-a716-446655440000"}[index],
				syntheticCodexAuth(t, "second-preflight", time.Now().Add(time.Hour)))
			putOpenAIPreflightPoolItems(t, fixture.server.URL, fixture.cookie, fixture.csrf, "company-codex", []map[string]any{
				{"upstream_id": fixture.upstream.ID, "upstream_model": "actual-first", "priority": 20, "weight": 1, "max_concurrency": 1},
				{"upstream_id": second.ID, "upstream_model": "actual-second", "priority": 10, "weight": 1, "max_concurrency": 1},
			})
			if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id=?`, fixture.upstream.ID); err != nil {
				t.Fatal(err)
			}
			fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, credential *membership.CodexAuthCredential, body []byte, _ func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
				calls.Add(1)
				if !strings.Contains(string(body), `"model":"actual-second"`) || !strings.Contains(credential.AccessTokenSecret(), "second-preflight") {
					t.Errorf("responses body=%s credential=%q", body, credential.AccessTokenSecret())
				}
				return json.RawMessage(`{"id":"resp_preflight","object":"response","status":"completed","output":[]}`), nil
			}}
			response := employeeRequest(t, http.MethodPost, fixture.server.URL+protocol.path, protocol.body, fixture.employeeKey.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			if calls.Load() != 1 {
				t.Fatalf("executor calls=%d", calls.Load())
			}
			assertOpenAIPreflightAccounting(t, fixture.app, "company-codex", 1, second.ID, "failover")
		})
	}
}

func TestCodexMappingFailureAndExecutorFailureNeverSwitch(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		executor   func() *codexRunError
		wantStatus int
	}{
		{name: "mapping", body: `{"model":"company-codex","messages":[{"role":"system","content":"not supported"}]}`, wantStatus: http.StatusBadRequest},
		{name: "executor", body: `{"model":"company-codex","messages":[{"role":"user","content":"sent once"}]}`, executor: func() *codexRunError { return &codexRunError{Code: membership.CodexErrorUpstream} }, wantStatus: http.StatusBadGateway},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			runner := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
				calls.Add(1)
				return codexExecutionResult{}, test.executor()
			}}
			fixture := newCodexServiceFixture(t, runner)
			defer fixture.close()
			second := importTestCodexUpstream(t, fixture.server.URL, fixture.cookie, fixture.csrf,
				[]string{"850e8400-e29b-41d4-a716-446655440000", "950e8400-e29b-41d4-a716-446655440000"}[index],
				syntheticCodexAuth(t, "second-never-run", time.Now().Add(time.Hour)))
			putOpenAIPreflightPoolItems(t, fixture.server.URL, fixture.cookie, fixture.csrf, "company-codex", []map[string]any{
				{"upstream_id": fixture.upstream.ID, "upstream_model": "actual-first", "priority": 20, "weight": 1, "max_concurrency": 1},
				{"upstream_id": second.ID, "upstream_model": "actual-second", "priority": 10, "weight": 1, "max_concurrency": 1},
			})
			response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions", test.body, fixture.employeeKey.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			wantCalls, wantAttempts := int32(0), 0
			if test.executor != nil {
				wantCalls, wantAttempts = 1, 1
			}
			if calls.Load() != wantCalls {
				t.Fatalf("executor calls=%d", calls.Load())
			}
			account, dispatch := "", ""
			if wantAttempts == 1 {
				account, dispatch = fixture.upstream.ID, "primary"
			}
			assertOpenAIPreflightAccounting(t, fixture.app, "company-codex", wantAttempts, account, dispatch)
		})
	}
}

func TestCodexRefreshGlobalAndPausePersistenceFailuresNeverSwitch(t *testing.T) {
	tests := []string{"pause_write", "before_persist", "retry_guard"}
	for index, name := range tests {
		t.Run(name, func(t *testing.T) {
			var executorCalls atomic.Int32
			var tokenCalls atomic.Int32
			runner := &fakeCodexExecutor{completeFn: func(context.Context, *membership.CodexAuthCredential, membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
				executorCalls.Add(1)
				return codexExecutionResult{Text: "must not run"}, nil
			}}
			fixture := newCodexServiceFixture(t, runner)
			defer fixture.close()
			second := importTestCodexUpstream(t, fixture.server.URL, fixture.cookie, fixture.csrf,
				[]string{"a50e8400-e29b-41d4-a716-446655440000", "b50e8400-e29b-41d4-a716-446655440000", "c50e8400-e29b-41d4-a716-446655440000"}[index],
				syntheticCodexAuth(t, "second-global", time.Now().Add(time.Hour)))
			putOpenAIPreflightPoolItems(t, fixture.server.URL, fixture.cookie, fixture.csrf, "company-codex", []map[string]any{
				{"upstream_id": fixture.upstream.ID, "upstream_model": "actual-first", "priority": 20, "weight": 1, "max_concurrency": 1},
				{"upstream_id": second.ID, "upstream_model": "actual-second", "priority": 10, "weight": 1, "max_concurrency": 1},
			})
			configureExpiredCodexRefresh(t, fixture)
			switch name {
			case "pause_write":
				fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					tokenCalls.Add(1)
					return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": "incomplete"}), nil
				})}
				if _, err := fixture.app.store.db.Exec(`CREATE TRIGGER reject_preflight_pause BEFORE UPDATE OF state ON codex_oauth_refresh_states WHEN NEW.state='paused' BEGIN SELECT RAISE(ABORT,'synthetic pause failure'); END`); err != nil {
					t.Fatal(err)
				}
			case "before_persist":
				access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
				idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "preflight-global"}})
				fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					tokenCalls.Add(1)
					return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "rotated-global"}), nil
				})}
				fixture.app.refresh.beforePersist = func(string) error { return errors.New("synthetic global persistence failure") }
			case "retry_guard":
				fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					tokenCalls.Add(1)
					_, _ = fixture.app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, fixture.upstream.ID)
					return oauthHTTPResponse(http.StatusTooManyRequests, map[string]string{"error": "temporarily_unavailable"}), nil
				})}
			}
			response := employeeRequest(t, http.MethodPost, fixture.server.URL+"/v1/chat/completions",
				`{"model":"company-codex","messages":[{"role":"user","content":"must not switch"}]}`, fixture.employeeKey.Key, context.Background())
			body := readBody(response)
			if response.StatusCode != http.StatusBadGateway || executorCalls.Load() != 0 {
				t.Fatalf("status=%d executor calls=%d body=%s", response.StatusCode, executorCalls.Load(), body)
			}
			assertOpenAIPreflightAccounting(t, fixture.app, "company-codex", 0, "", "")
			var cooldowns int
			if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM account_pool_runtime_cooldowns`).Scan(&cooldowns); err != nil || cooldowns != 0 {
				t.Fatalf("global failure cooldowns=%d err=%v", cooldowns, err)
			}
			if name == "before_persist" {
				var state string
				if err := fixture.app.store.db.QueryRow(`SELECT state FROM codex_oauth_refresh_states WHERE upstream_id=?`, fixture.upstream.ID).Scan(&state); err != nil || state != "paused" {
					t.Fatalf("refresh state=%q err=%v", state, err)
				}
			}
			if name == "retry_guard" && tokenCalls.Load() != 1 {
				t.Fatalf("token endpoint calls=%d", tokenCalls.Load())
			}
		})
	}
}

func configureExpiredCodexRefresh(t *testing.T, fixture *codexServiceFixture) {
	t.Helper()
	fixture.app.cfg.CodexOAuthClientID = testOAuthClientID
	fixture.app.cfg.CodexOAuthRedirectURI = testOAuthRedirectURI
	bindOAuthTestUpstream(t, fixture.app, fixture.upstream.ID, testOAuthClientID)
	raw := []byte(syntheticCodexAuth(t, "expired-preflight", time.Now().Add(-time.Minute)))
	ciphertext, err := fixture.app.secrets.encryptCodexAuth(fixture.upstream.ID, raw)
	clear(raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=? WHERE id=?`, ciphertext, fixture.upstream.ID); err != nil {
		t.Fatal(err)
	}
}
