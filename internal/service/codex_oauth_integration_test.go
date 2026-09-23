package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

// TestCodexOAuthRefreshFeedsBothEmployeeProtocolsAcrossRestart exercises the
// real management and employee HTTP surfaces. The only provider-facing pieces
// are injected token and model executors using synthetic credentials.
func TestCodexOAuthRefreshFeedsBothEmployeeProtocolsAcrossRestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	config := Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test", ExperimentalCodexMembership: true,
		CodexOAuthClientID: "synthetic-oauth-client", CodexOAuthRedirectURI: "http://127.0.0.1/admin/api/v1/codex/oauth/callback",
	}
	app, err := Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	firstApp := app
	firstAppOpen := true
	t.Cleanup(func() {
		if firstAppOpen {
			_ = firstApp.Close()
		}
	})
	server := httptest.NewServer(app.Handler())
	firstServer := server
	firstServerOpen := true
	t.Cleanup(func() {
		if firstServerOpen {
			firstServer.Close()
		}
	})
	cookie, csrf := loginTestAdmin(t, server.URL)

	initialAccess := codexOAuthIntegrationJWT(t, time.Now().Add(time.Hour), "initial-access")
	rotatedAccess := codexOAuthIntegrationJWT(t, time.Now().Add(2*time.Hour), "rotated-access")
	initialID := codexOAuthIntegrationIDToken(t, "account-oauth-integrated")
	rotatedID := codexOAuthIntegrationIDToken(t, "account-oauth-integrated")
	var tokenCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]string
		if request.URL.String() != codexOAuthTokenURL || json.NewDecoder(request.Body).Decode(&payload) != nil {
			t.Error("unexpected OAuth token request")
		}
		switch call := tokenCalls.Add(1); call {
		case 1:
			if payload["grant_type"] != "authorization_code" || payload["code"] != "synthetic-code" || payload["code_verifier"] == "" {
				t.Errorf("authorization payload=%v", payload)
			}
			return codexOAuthIntegrationResponse(http.StatusOK, map[string]string{"access_token": initialAccess, "id_token": initialID, "refresh_token": "initial-refresh"}), nil
		case 2:
			if payload["grant_type"] != "refresh_token" || payload["refresh_token"] != "initial-refresh" {
				t.Errorf("refresh payload=%v", payload)
			}
			return codexOAuthIntegrationResponse(http.StatusOK, map[string]string{"access_token": rotatedAccess, "id_token": rotatedID, "refresh_token": "rotated-refresh"}), nil
		default:
			t.Errorf("unexpected token call %d", call)
			return codexOAuthIntegrationResponse(http.StatusInternalServerError, map[string]string{"error": "server_error"}), nil
		}
	})}

	operationID := "ca3eb455-687d-48aa-82fb-8ada975aba11"
	createBody := marshalTestJSON(t, map[string]any{"name": "OAuth integration", "operation_id": operationID})
	created := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", createBody, cookie, csrf, server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("create OAuth session: %d %s", created.StatusCode, readBody(created))
	}
	var session codexOAuthSessionResponse
	decodeResponse(t, created, &session)
	repeated := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", createBody, cookie, csrf, server.URL)
	if repeated.StatusCode != http.StatusOK {
		t.Fatalf("repeat OAuth session: %d %s", repeated.StatusCode, readBody(repeated))
	}
	var sameSession codexOAuthSessionResponse
	decodeResponse(t, repeated, &sameSession)
	if sameSession != session {
		t.Fatalf("idempotent OAuth session changed: %#v %#v", session, sameSession)
	}
	authorizeURL, err := url.Parse(session.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	callback := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(authorizeURL.Query().Get("state"))+"&code=synthetic-code", "", cookie, "", "")
	if callback.StatusCode != http.StatusOK {
		t.Fatalf("OAuth callback: %d %s", callback.StatusCode, readBody(callback))
	}
	callback.Body.Close()

	var upstreamID string
	var revision int64
	if err := app.store.db.QueryRow(`SELECT id,revision FROM upstreams WHERE operation_id=?`, operationID).Scan(&upstreamID, &revision); err != nil {
		t.Fatal(err)
	}
	refresh := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstreamID+"/codex-refresh", `{"expected_revision":1}`, cookie, csrf, server.URL)
	if refresh.StatusCode != http.StatusOK {
		t.Fatalf("manual refresh: %d %s", refresh.StatusCode, readBody(refresh))
	}
	var refreshed upstreamView
	decodeResponse(t, refresh, &refreshed)
	if refreshed.Revision != 2 || refreshed.CredentialState == nil || *refreshed.CredentialState != codexStateImported || tokenCalls.Load() != 2 {
		t.Fatalf("refreshed=%+v token_calls=%d", refreshed, tokenCalls.Load())
	}

	model := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/models", marshalTestJSON(t, map[string]any{"id": "oauth-codex", "upstream_id": upstreamID, "upstream_model": "provider-codex"}), cookie, csrf, server.URL)
	if model.StatusCode != http.StatusCreated {
		t.Fatalf("create model: %d %s", model.StatusCode, readBody(model))
	}
	model.Body.Close()
	employeeResponse := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/employees", `{"name":"OAuth employee"}`, cookie, csrf, server.URL)
	if employeeResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create employee: %d %s", employeeResponse.StatusCode, readBody(employeeResponse))
	}
	var employeeObject employee
	decodeResponse(t, employeeResponse, &employeeObject)
	employeeKey := createTestKey(t, server.URL, employeeObject.ID, "oauth-integration-key", cookie, csrf)

	chatCalls, responseCalls := &atomic.Int32{}, &atomic.Int32{}
	installCodexOAuthIntegrationExecutors(t, app, rotatedAccess, "rotated-refresh", "account-oauth-integrated", chatCalls, responseCalls)
	assertCodexOAuthIntegrationProtocols(t, server.URL, employeeKey.Key)
	if chatCalls.Load() != 2 || responseCalls.Load() != 2 {
		t.Fatalf("executor calls chat=%d responses=%d", chatCalls.Load(), responseCalls.Load())
	}
	var state string
	var verifiedAt *string
	if err := app.store.db.QueryRow(`SELECT credential_state,verified_at FROM upstreams WHERE id=?`, upstreamID).Scan(&state, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if state != codexStateVerified || verifiedAt == nil || *verifiedAt == "" {
		t.Fatalf("state=%q verified_at=%v", state, verifiedAt)
	}

	server.Close()
	firstServerOpen = false
	server = nil
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	firstAppOpen = false
	app = nil
	app, err = Open(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	secondApp := app
	t.Cleanup(func() { _ = secondApp.Close() })
	server = httptest.NewServer(app.Handler())
	secondServer := server
	t.Cleanup(secondServer.Close)
	chatCalls.Store(0)
	responseCalls.Store(0)
	installCodexOAuthIntegrationExecutors(t, app, rotatedAccess, "rotated-refresh", "account-oauth-integrated", chatCalls, responseCalls)
	assertCodexOAuthIntegrationProtocols(t, server.URL, employeeKey.Key)
	if chatCalls.Load() != 2 || responseCalls.Load() != 2 {
		t.Fatalf("restart calls chat=%d responses=%d", chatCalls.Load(), responseCalls.Load())
	}
	restartCookie, restartCSRF := loginTestAdmin(t, server.URL)
	revoke := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/keys/"+employeeKey.ID+"/revoke", `{}`, restartCookie, restartCSRF, server.URL)
	if revoke.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d %s", revoke.StatusCode, readBody(revoke))
	}
	revoke.Body.Close()
	beforeChat, beforeResponses := chatCalls.Load(), responseCalls.Load()
	for _, path := range []string{"/v1/chat/completions", "/v1/responses"} {
		response := employeeRequest(t, http.MethodPost, server.URL+path, `{"model":"oauth-codex","messages":[{"role":"user","content":"denied"}]}`, employeeKey.Key, context.Background())
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("revoked %s status=%d body=%s", path, response.StatusCode, readBody(response))
		}
		response.Body.Close()
	}
	if chatCalls.Load() != beforeChat || responseCalls.Load() != beforeResponses {
		t.Fatal("revoked requests reached an executor")
	}
}

func installCodexOAuthIntegrationExecutors(t *testing.T, app *App, access, refresh, account string, chatCalls, responseCalls *atomic.Int32) {
	t.Helper()
	check := func(c *membership.CodexAuthCredential) {
		if c.AccessTokenSecret() != access || c.RefreshTokenSecret() != refresh || c.AccountIDSecret() != account {
			t.Errorf("executor received stale credential")
		}
	}
	chat := &fakeCodexExecutor{}
	checkChatRequest := func(request membership.CodexTextRequest) {
		if request.Model != "provider-codex" || len(request.Messages) != 1 || request.Messages[0].Role != membership.CodexRoleUser || request.Messages[0].Text != "hello" {
			t.Errorf("unexpected mapped Chat request: %#v", request)
		}
	}
	chat.setComplete(func(_ context.Context, c *membership.CodexAuthCredential, request membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
		chatCalls.Add(1)
		check(c)
		checkChatRequest(request)
		return codexExecutionResult{Text: "chat-ok"}, nil
	})
	chat.streamFn = func(_ context.Context, c *membership.CodexAuthCredential, request membership.CodexTextRequest, consume func(codexExecutionEvent) error) *codexRunError {
		chatCalls.Add(1)
		check(c)
		checkChatRequest(request)
		for _, event := range []codexExecutionEvent{{Kind: membership.CodexEventStarted}, {Kind: membership.CodexEventTextDelta, Text: "chat-stream"}, {Kind: membership.CodexEventCompleted}} {
			if consume(event) != nil {
				return &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
			}
		}
		return nil
	}
	app.codex = chat
	app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, c *membership.CodexAuthCredential, body []byte, consume func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
		responseCalls.Add(1)
		check(c)
		var request map[string]json.RawMessage
		if json.Unmarshal(body, &request) != nil || string(request["model"]) != `"provider-codex"` || !strings.Contains(string(request["input"]), "hello") ||
			!strings.Contains(string(request["tools"]), `"name":"lookup"`) || !strings.Contains(string(request["tool_choice"]), `"name":"lookup"`) {
			t.Errorf("unexpected mapped Responses request: %s", body)
		}
		final := json.RawMessage(`{"id":"resp_oauth","object":"response","status":"completed","output":[]}`)
		if consume != nil {
			for _, event := range []json.RawMessage{json.RawMessage(`{"type":"response.created","response":{"id":"resp_oauth"}}`), json.RawMessage(`{"type":"response.completed","response":{"id":"resp_oauth","object":"response","status":"completed","output":[]}}`)} {
				if consume(event) != nil {
					return nil, &codexRunError{Code: membership.CodexErrorEventConsumerStopped}
				}
			}
		}
		return final, nil
	}}
}

func assertCodexOAuthIntegrationProtocols(t *testing.T, baseURL, key string) {
	t.Helper()
	tests := []struct{ name, path, body, want string }{{"chat", "/v1/chat/completions", `{"model":"oauth-codex","messages":[{"role":"user","content":"hello"}]}`, "chat-ok"}, {"chat stream", "/v1/chat/completions", `{"model":"oauth-codex","stream":true,"messages":[{"role":"user","content":"hello"}]}`, "data: [DONE]"}, {"responses", "/v1/responses", `{"model":"oauth-codex","input":"hello","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}}`, `"id":"resp_oauth"`}, {"responses stream", "/v1/responses", `{"model":"oauth-codex","input":"hello","stream":true,"tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"lookup"}}`, "response.completed"}}
	for _, test := range tests {
		response := employeeRequest(t, http.MethodPost, baseURL+test.path, test.body, key, context.Background())
		body := readBody(response)
		if response.StatusCode != http.StatusOK || !strings.Contains(body, test.want) {
			t.Fatalf("%s status=%d body=%s", test.name, response.StatusCode, body)
		}
	}
}

func codexOAuthIntegrationJWT(t *testing.T, expires time.Time, marker string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"exp": expires.Unix(), "marker": marker})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + marker
}
func codexOAuthIntegrationIDToken(t *testing.T, account string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, _ := json.Marshal(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": account}})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
}
func codexOAuthIntegrationResponse(status int, body any) *http.Response {
	encoded, _ := json.Marshal(body)
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(encoded)))}
}
