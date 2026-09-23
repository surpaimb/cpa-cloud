package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

const (
	testOAuthClientID    = "cpa-registered-client"
	testOAuthRedirectURI = "http://127.0.0.1/admin/api/v1/codex/oauth/callback"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type failingOAuthBody struct{}

func (failingOAuthBody) Read([]byte) (int, error) {
	return 0, errors.New("synthetic response read failure")
}
func (failingOAuthBody) Close() error { return nil }

func TestCodexOAuthConfigurationValidation(t *testing.T) {
	valid := Config{CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI}
	if err := ValidateCodexOAuthConfig(valid); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []Config{
		{CodexOAuthClientID: " padded ", CodexOAuthRedirectURI: testOAuthRedirectURI},
		{CodexOAuthClientID: "client\nother", CodexOAuthRedirectURI: testOAuthRedirectURI},
		{CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: "http://example.com" + codexOAuthCallbackPath},
		{CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: "https://example.com/wrong"},
		{CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: "https://example.com" + codexOAuthCallbackPath + "?target=other"},
	} {
		if err := ValidateCodexOAuthConfig(cfg); err == nil {
			t.Fatalf("invalid OAuth config accepted: %+v", cfg)
		}
	}
}

func TestCodexOAuthFeatureRequiresExperimentAndExplicitClientConfiguration(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name     string
		config   Config
		wantCode string
	}{
		{name: "experiment disabled", config: Config{}, wantCode: "feature_disabled"},
		{name: "client not configured", config: Config{ExperimentalCodexMembership: true}, wantCode: "codex_oauth_not_configured"},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := test.config
			cfg.DataDir = dataDir
			cfg.Listen = "127.0.0.1:0"
			app, err := Open(context.Background(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			server := httptest.NewServer(app.Handler())
			defer server.Close()
			cookie, csrf := loginTestAdmin(t, server.URL)
			response := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
				"name": "not configured", "operation_id": "6509f36a-0230-4f02-8b76-2ffdf755183e",
			}, cookie, csrf, server.URL)
			assertCodexAdminError(t, response, map[string]int{"feature_disabled": http.StatusForbidden, "codex_oauth_not_configured": http.StatusConflict}[test.wantCode], test.wantCode)
		})
	}
}

func TestCodexOAuthAuthorizationSessionCallbackAndReplay(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	defer app.Close()

	accessToken := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-oauth-test"}})
	var tokenCalls atomic.Int32
	var captured codexOAuthTokenRequest
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		if request.URL.String() != codexOAuthTokenURL || request.Method != http.MethodPost || request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected token request method=%s url=%s headers=%v", request.Method, request.URL, request.Header)
		}
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			t.Errorf("decode token request: %v", err)
		}
		return oauthHTTPResponse(http.StatusOK, map[string]string{
			"access_token": accessToken, "id_token": idToken, "refresh_token": "oauth-rotated-refresh-secret",
		}), nil
	})}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	missingOrigin := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "OAuth account", "operation_id": "51a09f73-0432-4450-8888-d540a85aee1e",
	}, cookie, csrf, "")
	assertCodexAdminError(t, missingOrigin, http.StatusForbidden, "origin_rejected")

	operationID := "75ee0360-2df6-4d73-8d9b-26c5d8553cd2"
	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "OAuth account", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create OAuth session status=%d body=%s", createdResponse.StatusCode, readBody(createdResponse))
	}
	var created codexOAuthSessionResponse
	decodeResponse(t, createdResponse, &created)
	authorize, err := url.Parse(created.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	if authorize.Scheme != "https" || authorize.Host != "auth.openai.com" || authorize.Path != "/oauth/authorize" ||
		query.Get("client_id") != testOAuthClientID || query.Get("redirect_uri") != testOAuthRedirectURI || query.Get("scope") != codexOAuthScopes ||
		query.Get("response_type") != "code" || query.Get("code_challenge_method") != "S256" || query.Get("state") == "" || query.Get("code_challenge") == "" {
		t.Fatalf("unexpected authorization URL: %s", created.AuthorizationURL)
	}

	retry := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "ignored", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("OAuth session retry status=%d body=%s", retry.StatusCode, readBody(retry))
	}
	var retried codexOAuthSessionResponse
	decodeResponse(t, retry, &retried)
	if retried != created {
		t.Fatalf("idempotent OAuth session changed: first=%+v second=%+v", created, retried)
	}

	var sessionID string
	var encrypted []byte
	if err := app.store.db.QueryRow(`SELECT id,secret_ciphertext FROM codex_oauth_sessions WHERE operation_id=?`, operationID).Scan(&sessionID, &encrypted); err != nil {
		t.Fatal(err)
	}
	plain, err := app.secrets.decryptCodexOAuthSession(sessionID, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var secret codexOAuthSessionSecret
	if json.Unmarshal(plain, &secret) != nil {
		t.Fatal("stored OAuth session secret is invalid")
	}
	clear(plain)
	challenge := sha256String(secret.Verifier)
	if secret.State != query.Get("state") || secret.ClientID != testOAuthClientID || secret.RedirectURI != testOAuthRedirectURI || challenge != query.Get("code_challenge") || bytes.Contains(encrypted, []byte(secret.State)) || bytes.Contains(encrypted, []byte(secret.Verifier)) {
		t.Fatal("OAuth state/PKCE persistence did not match the encrypted contract")
	}

	otherCookie, _ := loginTestAdmin(t, server.URL)
	wrongSession := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(secret.State)+"&code=private-code", "", otherCookie, "", "")
	wrongSessionBody := readBody(wrongSession)
	if wrongSession.StatusCode != http.StatusBadRequest || tokenCalls.Load() != 0 || strings.Contains(wrongSessionBody, "private-code") {
		t.Fatalf("wrong-session callback status=%d calls=%d body=%s", wrongSession.StatusCode, tokenCalls.Load(), wrongSessionBody)
	}

	callback := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(secret.State)+"&code=private-code", "", cookie, "", "")
	callbackBody := readBody(callback)
	if callback.StatusCode != http.StatusOK || tokenCalls.Load() != 1 || strings.Contains(callbackBody, "private-code") || strings.Contains(callbackBody, "oauth-rotated-refresh-secret") {
		t.Fatalf("callback status=%d calls=%d body=%s", callback.StatusCode, tokenCalls.Load(), callbackBody)
	}
	if captured.GrantType != "authorization_code" || captured.ClientID != testOAuthClientID || captured.Code != "private-code" || captured.RedirectURI != testOAuthRedirectURI || captured.CodeVerifier != secret.Verifier || captured.RefreshToken != "" {
		t.Fatalf("authorization exchange payload=%+v", captured)
	}

	replay := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(secret.State)+"&code=replayed-code", "", cookie, "", "")
	if replay.StatusCode != http.StatusBadRequest || tokenCalls.Load() != 1 {
		t.Fatalf("callback replay status=%d calls=%d", replay.StatusCode, tokenCalls.Load())
	}
	replay.Body.Close()

	var upstreamID, state string
	var storedCiphertext []byte
	var revision int64
	if err := app.store.db.QueryRow(`SELECT id,credential_ciphertext,credential_state,revision FROM upstreams WHERE operation_id=?`, operationID).
		Scan(&upstreamID, &storedCiphertext, &state, &revision); err != nil {
		t.Fatal(err)
	}
	if state != codexStateImported || revision != 1 || bytes.Contains(storedCiphertext, []byte("oauth-rotated-refresh-secret")) {
		t.Fatalf("OAuth upstream state=%q revision=%d", state, revision)
	}
	var bindingClient, bindingSource string
	if err := app.store.db.QueryRow(`SELECT client_id,source FROM codex_oauth_bindings WHERE upstream_id=?`, upstreamID).Scan(&bindingClient, &bindingSource); err != nil {
		t.Fatal(err)
	}
	if bindingClient != testOAuthClientID || bindingSource != "authorization_code" {
		t.Fatalf("OAuth binding client=%q source=%q", bindingClient, bindingSource)
	}
	storedAuth, err := app.secrets.decryptCodexAuth(upstreamID, storedCiphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(storedAuth)
	credential, err := membership.ParseCodexAuthJSON(storedAuth)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if credential.AccountIDSecret() != "acct-oauth-test" || credential.RefreshTokenSecret() != "oauth-rotated-refresh-secret" {
		t.Fatal("OAuth-created credential did not preserve the normalized account and rotated token")
	}

	expiredResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Expired OAuth", "operation_id": "ce338738-8c3d-418c-af04-1ea9a9232c8a",
	}, cookie, csrf, server.URL)
	var expiredSession codexOAuthSessionResponse
	decodeResponse(t, expiredResponse, &expiredSession)
	expiredURL, _ := url.Parse(expiredSession.AuthorizationURL)
	if _, err := app.store.db.Exec(`UPDATE codex_oauth_sessions SET expires_at=? WHERE operation_id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), "ce338738-8c3d-418c-af04-1ea9a9232c8a"); err != nil {
		t.Fatal(err)
	}
	expiredCallback := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(expiredURL.Query().Get("state"))+"&code=expired-code", "", cookie, "", "")
	if expiredCallback.StatusCode != http.StatusBadRequest || tokenCalls.Load() != 1 {
		t.Fatalf("expired callback status=%d calls=%d", expiredCallback.StatusCode, tokenCalls.Load())
	}
	expiredCallback.Body.Close()

	deniedResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Denied OAuth", "operation_id": "b427005d-4c22-47f8-94fb-17d26578aad0",
	}, cookie, csrf, server.URL)
	var deniedSession codexOAuthSessionResponse
	decodeResponse(t, deniedResponse, &deniedSession)
	deniedURL, _ := url.Parse(deniedSession.AuthorizationURL)
	deniedCallback := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(deniedURL.Query().Get("state"))+"&error=access_denied&error_description=private-provider-detail", "", cookie, "", "")
	deniedBody := readBody(deniedCallback)
	if deniedCallback.StatusCode != http.StatusBadRequest || tokenCalls.Load() != 1 || strings.Contains(deniedBody, "private-provider-detail") {
		t.Fatalf("denied callback status=%d calls=%d body=%s", deniedCallback.StatusCode, tokenCalls.Load(), deniedBody)
	}

	codeFailureResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Lost code response", "operation_id": "ef10d39c-c56d-4fe9-a8e7-cdc6564fa528",
	}, cookie, csrf, server.URL)
	var codeFailureSession codexOAuthSessionResponse
	decodeResponse(t, codeFailureResponse, &codeFailureSession)
	codeFailureURL, _ := url.Parse(codeFailureSession.AuthorizationURL)
	var codeFailureCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		codeFailureCalls.Add(1)
		return nil, errors.New("provider may have consumed the authorization code")
	})}
	failedCallbackURL := server.URL + codexOAuthCallbackPath + "?state=" + url.QueryEscape(codeFailureURL.Query().Get("state")) + "&code=possibly-consumed-code"
	failedCallback := requestJSON(t, http.MethodGet, failedCallbackURL, "", cookie, "", "")
	if failedCallback.StatusCode != http.StatusBadGateway || codeFailureCalls.Load() != 1 {
		t.Fatalf("lost code response status=%d calls=%d", failedCallback.StatusCode, codeFailureCalls.Load())
	}
	failedCallback.Body.Close()
	failedReplay := requestJSON(t, http.MethodGet, failedCallbackURL, "", cookie, "", "")
	if failedReplay.StatusCode != http.StatusBadRequest || codeFailureCalls.Load() != 1 {
		t.Fatalf("lost code replay status=%d calls=%d", failedReplay.StatusCode, codeFailureCalls.Load())
	}
	failedReplay.Body.Close()

	replacementAuth := codexAdminAuthJSON(t, time.Now().Add(2*time.Hour), "acct-oauth-test", "administrator-import-refresh")
	replaced := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+upstreamID+"/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": replacementAuth,
	}, cookie, csrf, server.URL)
	if replaced.StatusCode != http.StatusOK {
		t.Fatalf("replace OAuth-created upstream status=%d body=%s", replaced.StatusCode, readBody(replaced))
	}
	replaced.Body.Close()
	var bindingCount int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id=?`, upstreamID).Scan(&bindingCount); err != nil || bindingCount != 0 {
		t.Fatalf("administrator replacement retained OAuth binding count=%d err=%v", bindingCount, err)
	}
	refreshAfterReplace := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstreamID+"/codex-refresh", map[string]any{"expected_revision": 2}, cookie, csrf, server.URL)
	assertCodexAdminError(t, refreshAfterReplace, http.StatusConflict, "codex_refresh_not_bound")
	if codeFailureCalls.Load() != 1 {
		t.Fatal("unbound administrator import reached the token endpoint")
	}
}

func TestCodexOAuthAuthorizationSessionSurvivesRestart(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	operationID := "41769f59-05e9-48dd-ac17-695025d1acf2"
	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Restart OAuth", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	var created codexOAuthSessionResponse
	decodeResponse(t, createdResponse, &created)
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	var err error
	app, err = Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true,
		CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	retry := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "ignored after restart", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("restart retry status=%d body=%s", retry.StatusCode, readBody(retry))
	}
	var reopened codexOAuthSessionResponse
	decodeResponse(t, retry, &reopened)
	if reopened != created {
		t.Fatalf("authorization session changed across restart: before=%+v after=%+v", created, reopened)
	}
}

func TestCodexOAuthSessionRejectsChangedOrMissingConfigurationBindingWithoutConsumption(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	operationID := "cbb3a7be-8536-44ba-9c5f-e43aa0bf2f10"
	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Configuration snapshot", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	var created codexOAuthSessionResponse
	decodeResponse(t, createdResponse, &created)
	createdURL, _ := url.Parse(created.AuthorizationURL)
	state := createdURL.Query().Get("state")
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true,
		CodexOAuthClientID: "changed-registered-client", CodexOAuthRedirectURI: testOAuthRedirectURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var tokenCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return nil, errors.New("must not be called")
	})}
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	retry := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "ignored", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, retry, http.StatusConflict, "codex_oauth_configuration_changed")
	callback := requestJSON(t, http.MethodGet, server.URL+codexOAuthCallbackPath+"?state="+url.QueryEscape(state)+"&code=must-not-exchange", "", cookie, "", "")
	if callback.StatusCode != http.StatusConflict || callback.Header.Get("X-CPA-Error-Code") != "codex_oauth_configuration_changed" || tokenCalls.Load() != 0 {
		t.Fatalf("changed config callback status=%d code=%q calls=%d", callback.StatusCode, callback.Header.Get("X-CPA-Error-Code"), tokenCalls.Load())
	}
	callback.Body.Close()
	var used sql.NullString
	if err := app.store.db.QueryRow(`SELECT used_at FROM codex_oauth_sessions WHERE operation_id=?`, operationID).Scan(&used); err != nil || used.Valid {
		t.Fatalf("changed configuration consumed state used=%v err=%v", used, err)
	}

	var sessionID string
	var encrypted []byte
	if err := app.store.db.QueryRow(`SELECT id,secret_ciphertext FROM codex_oauth_sessions WHERE operation_id=?`, operationID).Scan(&sessionID, &encrypted); err != nil {
		t.Fatal(err)
	}
	plaintext, err := app.secrets.decryptCodexOAuthSession(sessionID, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var oldSecret codexOAuthSessionSecret
	if json.Unmarshal(plaintext, &oldSecret) != nil {
		t.Fatal("decode OAuth session secret")
	}
	clear(plaintext)
	oldSecret.ClientID = ""
	oldSecret.RedirectURI = ""
	legacyPlaintext, _ := json.Marshal(oldSecret)
	legacyCiphertext, err := app.secrets.encryptCodexOAuthSession(sessionID, legacyPlaintext)
	clear(legacyPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`UPDATE codex_oauth_sessions SET secret_ciphertext=? WHERE id=?`, legacyCiphertext, sessionID); err != nil {
		t.Fatal(err)
	}
	legacyRetry := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "legacy", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, legacyRetry, http.StatusConflict, "codex_oauth_configuration_changed")
}

func TestCodexOAuthRefreshRotationRetriesAndReauthorization(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)

	oldAuth := codexAdminAuthJSON(t, time.Now().Add(time.Hour), "acct-refresh", "old-refresh-secret")
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, "1fd133dd-d6b4-4493-9aca-9fed016e4bc9", oldAuth)
	newAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	newID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-refresh"}})
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := calls.Add(1)
		var payload codexOAuthTokenRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.GrantType != "refresh_token" || payload.ClientID != testOAuthClientID || payload.RefreshToken != "old-refresh-secret" || payload.Code != "" {
			t.Errorf("refresh payload=%+v", payload)
		}
		if call < 3 {
			return oauthHTTPResponse(http.StatusTooManyRequests, map[string]string{"error": "temporarily_unavailable"}), nil
		}
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": newAccess, "id_token": newID, "refresh_token": "rotated-refresh-secret"}), nil
	})}
	unbound := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	assertCodexAdminError(t, unbound, http.StatusConflict, "codex_refresh_not_bound")
	if calls.Load() != 0 {
		t.Fatal("imported credential without OAuth source binding reached token endpoint")
	}
	bindOAuthTestUpstream(t, app, upstream.ID, "different-registered-client")
	mismatch := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	assertCodexAdminError(t, mismatch, http.StatusConflict, "codex_refresh_not_bound")
	if calls.Load() != 0 {
		t.Fatal("mismatched OAuth client binding reached token endpoint")
	}
	if _, err := app.store.db.Exec(`UPDATE codex_oauth_bindings SET client_id=? WHERE upstream_id=?`, testOAuthClientID, upstream.ID); err != nil {
		t.Fatal(err)
	}
	refresh := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	if refresh.StatusCode != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", refresh.StatusCode, readBody(refresh))
	}
	var refreshed upstreamView
	decodeResponse(t, refresh, &refreshed)
	if refreshed.Revision != 2 || refreshed.CredentialState == nil || *refreshed.CredentialState != codexStateImported || calls.Load() != 3 {
		t.Fatalf("refreshed=%+v calls=%d", refreshed, calls.Load())
	}
	assertRefreshTokenStored(t, app, upstream.ID, "rotated-refresh-secret")

	var before []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, upstream.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return oauthHTTPResponse(http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "do-not-expose-private-provider-detail"}), nil
	})}
	invalid := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 2}, cookie, csrf, server.URL)
	invalidBody := readBody(invalid)
	if invalid.StatusCode != http.StatusConflict || !strings.Contains(invalidBody, "codex_reauthorization_required") || strings.Contains(invalidBody, "do-not-expose") {
		t.Fatalf("invalid grant status=%d body=%s", invalid.StatusCode, invalidBody)
	}
	var after []byte
	var revision int64
	var state string
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,revision,credential_state FROM upstreams WHERE id=?`, upstream.ID).Scan(&after, &revision, &state); err != nil {
		t.Fatal(err)
	}
	if revision != 2 || state != codexStateReauth || !bytes.Equal(before, after) {
		t.Fatal("invalid_grant partially updated the stored credential")
	}
}

func TestCodexOAuthConcurrentRefreshAndAdministratorReplacement(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	oldAuth := codexAdminAuthJSON(t, time.Now().Add(time.Hour), "acct-race", "race-old-refresh")
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, "d408f811-3cb1-4701-821b-fe85de09c3f1", oldAuth)
	bindOAuthTestUpstream(t, app, upstream.ID, testOAuthClientID)

	started := make(chan struct{})
	release := make(chan struct{})
	newAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	newID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-race"}})
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		close(started)
		<-release
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": newAccess, "id_token": newID, "refresh_token": "stale-network-refresh"}), nil
	})}

	refreshResult := make(chan *http.Response, 1)
	go func() {
		refreshResult <- codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("refresh request did not reach token transport")
	}
	replacementAuth := codexAdminAuthJSON(t, time.Now().Add(3*time.Hour), "acct-race", "administrator-reimport-wins")
	replaced := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": replacementAuth,
	}, cookie, csrf, server.URL)
	if replaced.StatusCode != http.StatusOK {
		t.Fatalf("administrator replacement status=%d body=%s", replaced.StatusCode, readBody(replaced))
	}
	replaced.Body.Close()
	close(release)
	result := <-refreshResult
	if result.StatusCode != http.StatusConflict {
		t.Fatalf("stale refresh status=%d body=%s", result.StatusCode, readBody(result))
	}
	result.Body.Close()
	assertRefreshTokenStored(t, app, upstream.ID, "administrator-reimport-wins")
}

func TestCodexOAuthRefreshLostResponseIsNotRetriedOrMarkedReauth(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	auth := codexAdminAuthJSON(t, time.Now().Add(time.Hour), "acct-lost-response", "possibly-rotated-refresh")
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, "cc493e19-7846-4e61-94a2-3cf9c9a05672", auth)
	bindOAuthTestUpstream(t, app, upstream.ID, testOAuthClientID)
	var before []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, upstream.ID).Scan(&before); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("provider rotated token but response was lost")
	})}
	response := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	assertCodexAdminError(t, response, http.StatusConflict, "codex_refresh_paused")
	replay := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", map[string]any{"expected_revision": 1}, cookie, csrf, server.URL)
	assertCodexAdminError(t, replay, http.StatusConflict, "codex_refresh_paused")
	var after []byte
	var revision int64
	var state string
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,revision,credential_state FROM upstreams WHERE id=?`, upstream.ID).Scan(&after, &revision, &state); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || revision != 1 || state != codexStateImported || !bytes.Equal(before, after) {
		t.Fatalf("lost response calls=%d revision=%d state=%q credential_equal=%v", calls.Load(), revision, state, bytes.Equal(before, after))
	}
	var refreshState, reason string
	if err := app.store.db.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id=?`, upstream.ID).Scan(&refreshState, &reason); err != nil {
		t.Fatal(err)
	}
	if refreshState != "paused" || reason != "uncertain_refresh_outcome" {
		t.Fatalf("refresh state=%q reason=%q", refreshState, reason)
	}
}

func TestCodexOAuthRefreshExpectedRevisionValidationAlwaysResponds(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	defer app.Close()
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	auth := codexAdminAuthJSON(t, time.Now().Add(time.Hour), "acct-revision", "revision-refresh")
	upstream := importTestCodexUpstream(t, server.URL, cookie, csrf, "03bdf09c-c35e-43bf-9399-8941ea0dd5dc", auth)
	bindOAuthTestUpstream(t, app, upstream.ID, testOAuthClientID)
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	})}
	for _, body := range []map[string]any{{}, {"expected_revision": nil}, {"expected_revision": 0}, {"expected_revision": -1}} {
		response := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-refresh", body, cookie, csrf, server.URL)
		assertCodexAdminError(t, response, http.StatusBadRequest, "invalid_request")
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid revisions reached token endpoint calls=%d", calls.Load())
	}
}

func TestCodexOAuthSessionStatusIsScopedAndRecoversExchanging(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Status session", "operation_id": "78bda71b-a39f-4cf3-bdad-5049e8149941",
	}, cookie, csrf, server.URL)
	var created codexOAuthSessionResponse
	decodeResponse(t, createdResponse, &created)
	if created.SessionID == "" {
		t.Fatal("create response omitted session_id")
	}
	statusResponse := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/"+created.SessionID, "", cookie, "", "")
	body := readBody(statusResponse)
	if statusResponse.StatusCode != http.StatusOK || strings.Contains(body, "authorization_url") || strings.Contains(body, "state") || strings.Contains(body, "token") {
		t.Fatalf("status=%d body=%s", statusResponse.StatusCode, body)
	}
	var status codexOAuthSessionStatusResponse
	if json.Unmarshal([]byte(body), &status) != nil || status.Status != "pending" {
		t.Fatalf("status view=%+v body=%s", status, body)
	}
	otherCookie, _ := loginTestAdmin(t, server.URL)
	other := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/"+created.SessionID, "", otherCookie, "", "")
	assertCodexAdminError(t, other, http.StatusNotFound, "not_found")
	if _, err := app.store.db.Exec(`UPDATE codex_oauth_sessions SET status='exchanging',used_at=? WHERE id=?`, utcNow(), created.SessionID); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true, CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	recovered := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/"+created.SessionID, "", cookie, "", "")
	if recovered.StatusCode != http.StatusOK {
		t.Fatalf("recovered status=%d body=%s", recovered.StatusCode, readBody(recovered))
	}
	status = codexOAuthSessionStatusResponse{}
	decodeResponse(t, recovered, &status)
	if status.Status != "failed" || status.ErrorCode == nil || *status.ErrorCode != "authorization_result_unknown" {
		t.Fatalf("recovered view=%+v", status)
	}
}

func TestCodexOAuthSessionStatusProjectsConfigurationChangeWithoutConsumption(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	server := httptest.NewServer(app.Handler())
	cookie, csrf := loginTestAdmin(t, server.URL)
	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions", map[string]any{
		"name": "Config projection", "operation_id": "18e8dd55-23c1-4441-a48a-9f74cc369f0d",
	}, cookie, csrf, server.URL)
	var created codexOAuthSessionResponse
	decodeResponse(t, createdResponse, &created)
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	changed := Config{DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true, CodexOAuthClientID: "changed-client", CodexOAuthRedirectURI: testOAuthRedirectURI}
	app, err := Open(context.Background(), changed)
	if err != nil {
		t.Fatal(err)
	}
	server = httptest.NewServer(app.Handler())
	projected := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/"+created.SessionID, "", cookie, "", "")
	var status codexOAuthSessionStatusResponse
	decodeResponse(t, projected, &status)
	if status.Status != "failed" || status.ErrorCode == nil || *status.ErrorCode != "codex_oauth_configuration_changed" {
		t.Fatalf("projected view=%+v", status)
	}
	var stored string
	var used sql.NullString
	if err := app.store.db.QueryRow(`SELECT status,used_at FROM codex_oauth_sessions WHERE id=?`, created.SessionID).Scan(&stored, &used); err != nil || stored != "pending" || used.Valid {
		t.Fatalf("stored status=%q used=%v err=%v", stored, used, err)
	}
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	app, err = Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true, CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	server = httptest.NewServer(app.Handler())
	defer server.Close()
	restored := requestJSON(t, http.MethodGet, server.URL+"/admin/api/v1/upstreams/codex-oauth-sessions/"+created.SessionID, "", cookie, "", "")
	status = codexOAuthSessionStatusResponse{}
	decodeResponse(t, restored, &status)
	if status.Status != "pending" || status.ErrorCode != nil {
		t.Fatalf("restored view=%+v", status)
	}
}

func TestCodexOAuthTokenRetrySafety(t *testing.T) {
	for _, grantType := range []string{"authorization_code", "refresh_token"} {
		for _, failure := range []struct {
			name    string
			status  int
			netErr  bool
			readErr bool
			code    string
		}{
			{name: "network response lost", netErr: true},
			{name: "response body lost", status: http.StatusOK, readErr: true},
			{name: "server failure", status: http.StatusInternalServerError, code: "server_error"},
			{name: "rate limited", status: http.StatusTooManyRequests, code: "temporarily_unavailable"},
			{name: "rate limited invalid grant", status: http.StatusTooManyRequests, code: "invalid_grant"},
		} {
			t.Run(grantType+" "+failure.name, func(t *testing.T) {
				var calls atomic.Int32
				app := &App{oauthHTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					if failure.netErr {
						return nil, errors.New("provider may have consumed or rotated the grant")
					}
					if failure.readErr {
						return &http.Response{StatusCode: failure.status, Header: make(http.Header), Body: failingOAuthBody{}}, nil
					}
					return oauthHTTPResponse(failure.status, map[string]string{"error": failure.code}), nil
				})}}
				payload := codexOAuthTokenRequest{GrantType: grantType, ClientID: testOAuthClientID}
				if grantType == "authorization_code" {
					payload.Code = "one-time-code"
				} else {
					payload.RefreshToken = "rotating-refresh"
				}
				_, wireErr := app.requestCodexOAuthTokens(context.Background(), payload)
				wantCalls := int32(1)
				if grantType == "refresh_token" && failure.status == http.StatusTooManyRequests && failure.code != "invalid_grant" {
					wantCalls = codexOAuthMaxAttempts
				}
				if wireErr == nil || calls.Load() != wantCalls {
					t.Fatalf("wireErr=%v calls=%d want=%d", wireErr, calls.Load(), wantCalls)
				}
			})
		}
	}
}

func TestCodexOAuthRetryAfterNumericCapDoesNotOverflow(t *testing.T) {
	for _, test := range []struct {
		value string
		want  time.Duration
	}{
		{value: "0", want: 0},
		{value: "1", want: time.Second},
		{value: "9223372036854775807", want: 2 * time.Second},
	} {
		if got := retryDelay(test.value, 0); got != test.want {
			t.Fatalf("Retry-After %q delay=%s want=%s", test.value, got, test.want)
		}
	}
}

func TestCodexOAuthBindingMigrationRollbackRetryAndLegacyData(t *testing.T) {
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "cpa-cloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE upstreams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','codex-membership')),
			endpoint TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			credential_ciphertext BLOB NOT NULL,
			key_version INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL,
			credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required')),
			verified_at TEXT,
			operation_id TEXT UNIQUE,
			CHECK((provider_kind='openai-compatible' AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL) OR (provider_kind='codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL))
		)`,
		`CREATE TABLE models (
			id TEXT PRIMARY KEY,
			upstream_id TEXT NOT NULL REFERENCES upstreams(id),
			upstream_model TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE codex_oauth_bindings (blocking INTEGER)`,
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('ups_legacy_oauth','Legacy API','openai-compatible','https://api.example.invalid/v1',1,X'0102',1,9,'2026-09-23T00:00:00Z')`,
		`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('legacy-oauth-model','ups_legacy_oauth','provider-model',1,'2026-09-23T00:00:00Z')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if failed, err := openStore(dataDir); err == nil {
		failed.close()
		t.Fatal("incompatible OAuth binding table unexpectedly migrated")
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	var upstreamCount, modelCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM upstreams WHERE id='ups_legacy_oauth' AND revision=9`).Scan(&upstreamCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM models WHERE id='legacy-oauth-model' AND upstream_id='ups_legacy_oauth'`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if upstreamCount != 1 || modelCount != 1 {
		t.Fatalf("failed binding migration changed legacy data upstreams=%d models=%d", upstreamCount, modelCount)
	}
	if _, err := db.Exec(`DROP TABLE codex_oauth_bindings`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("binding migration retry: %v", err)
	}
	defer migrated.close()
	columns, err := tableColumns(context.Background(), migrated.db, codexOAuthBindingTable)
	if err != nil || !columns["upstream_id"] || !columns["client_id"] || !columns["source"] || !columns["created_at"] {
		t.Fatalf("binding schema columns=%v err=%v", columns, err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM upstreams WHERE id='ups_legacy_oauth' AND revision=9`).Scan(&upstreamCount); err != nil || upstreamCount != 1 {
		t.Fatalf("legacy upstream after retry count=%d err=%v", upstreamCount, err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM models WHERE id='legacy-oauth-model' AND upstream_id='ups_legacy_oauth'`).Scan(&modelCount); err != nil || modelCount != 1 {
		t.Fatalf("legacy model after retry count=%d err=%v", modelCount, err)
	}
}

func TestCodexOAuthLifecycleMigrationFailureLeavesLegacySessionSchemaUntouched(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "cpa-cloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`DROP TABLE codex_oauth_refresh_states`,
		`DROP TABLE codex_oauth_sessions`,
		`CREATE TABLE codex_oauth_sessions (
			id TEXT PRIMARY KEY, operation_id TEXT NOT NULL UNIQUE,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			admin_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			state_digest BLOB NOT NULL UNIQUE, secret_ciphertext BLOB NOT NULL,
			name TEXT NOT NULL, expires_at TEXT NOT NULL, used_at TEXT, created_at TEXT NOT NULL
		)`,
		`CREATE TABLE codex_oauth_refresh_states (blocking INTEGER)`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("prepare legacy schema: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if migrated, err := openStore(dataDir); err == nil {
		migrated.close()
		t.Fatal("incompatible refresh schema unexpectedly migrated")
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := tableColumns(context.Background(), db, "codex_oauth_sessions")
	if err != nil {
		t.Fatal(err)
	}
	if columns["status"] || columns["upstream_id"] || columns["error_code"] {
		t.Fatalf("failed migration partially altered session columns: %v", columns)
	}
	if _, err := db.Exec(`DROP TABLE codex_oauth_refresh_states`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	migrated, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("retry lifecycle migration: %v", err)
	}
	defer migrated.close()
	columns, err = tableColumns(context.Background(), migrated.db, "codex_oauth_sessions")
	if err != nil || !columns["status"] || !columns["upstream_id"] || !columns["error_code"] {
		t.Fatalf("migrated session columns=%v err=%v", columns, err)
	}
}

func openOAuthTestApp(t *testing.T, dataDir string) *App {
	t.Helper()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true, Version: "test",
		ExperimentalCodexMembership: true, CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI,
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func oauthHTTPResponse(status int, body any) *http.Response {
	encoded, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(encoded)),
	}
}

func oauthTestJWT(t *testing.T, claims any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic-signature"
}

func sha256String(value string) string {
	digest := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func assertRefreshTokenStored(t *testing.T, app *App, upstreamID, expected string) {
	t.Helper()
	var ciphertext []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, upstreamID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	plaintext, err := app.secrets.decryptCodexAuth(upstreamID, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(plaintext)
	credential, err := membership.ParseCodexAuthJSON(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	defer credential.Destroy()
	if credential.RefreshTokenSecret() != expected {
		t.Fatalf("stored refresh token did not match expected rotation")
	}
}

func bindOAuthTestUpstream(t *testing.T, app *App, upstreamID, clientID string) {
	t.Helper()
	if _, err := app.store.db.Exec(`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES(?,?,?,?)`, upstreamID, clientID, "authorization_code", utcNow()); err != nil {
		t.Fatal(err)
	}
}
