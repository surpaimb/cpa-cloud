package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestCodexOnDemandRefreshFeedsChatAndResponses(t *testing.T) {
	for _, protocol := range []string{"chat", "responses"} {
		t.Run(protocol, func(t *testing.T) {
			chat := &fakeCodexExecutor{}
			fixture := newCodexServiceFixture(t, chat)
			defer fixture.close()
			fixture.app.cfg.CodexOAuthClientID = testOAuthClientID
			fixture.app.cfg.CodexOAuthRedirectURI = testOAuthRedirectURI
			bindOAuthTestUpstream(t, fixture.app, fixture.upstream.ID, testOAuthClientID)
			fixture.app.refresh.now = func() time.Time { return time.Now().UTC().Add(56 * time.Minute) }

			rotatedAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
			rotatedID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-refreshed"}})
			var tokenCalls atomic.Int32
			fixture.app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				tokenCalls.Add(1)
				return oauthHTTPResponse(http.StatusOK, map[string]string{
					"access_token": rotatedAccess, "id_token": rotatedID, "refresh_token": "refresh-refreshed",
				}), nil
			})}
			assertCredential := func(credential *membership.CodexAuthCredential) {
				if credential.AccessTokenSecret() != rotatedAccess || credential.RefreshTokenSecret() != "refresh-refreshed" || credential.AccountIDSecret() != "account-refreshed" {
					t.Errorf("executor received stale credential")
				}
			}
			chat.setComplete(func(_ context.Context, credential *membership.CodexAuthCredential, _ membership.CodexTextRequest) (codexExecutionResult, *codexRunError) {
				assertCredential(credential)
				return codexExecutionResult{Text: "ok"}, nil
			})
			fixture.app.responses = fakeCodexResponsesExecutor{fn: func(_ context.Context, credential *membership.CodexAuthCredential, _ []byte, _ func(json.RawMessage) error) (json.RawMessage, *codexRunError) {
				assertCredential(credential)
				return json.RawMessage(`{"id":"resp_refresh","object":"response","status":"completed","output":[]}`), nil
			}}

			path, body := "/v1/chat/completions", `{"model":"company-codex","messages":[{"role":"user","content":"refresh"}]}`
			if protocol == "responses" {
				path, body = "/v1/responses", `{"model":"company-codex","input":"refresh"}`
			}
			response := employeeRequest(t, http.MethodPost, fixture.server.URL+path, body, fixture.employeeKey.Key, context.Background())
			if response.StatusCode != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
			}
			response.Body.Close()
			var revision int64
			if err := fixture.app.store.db.QueryRow(`SELECT revision FROM upstreams WHERE id=?`, fixture.upstream.ID).Scan(&revision); err != nil {
				t.Fatal(err)
			}
			if tokenCalls.Load() != 1 || revision != 2 {
				t.Fatalf("token calls=%d revision=%d", tokenCalls.Load(), revision)
			}
		})
	}
}

func TestCodexRefreshRestartRecoversInProgressAsPaused(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	id := insertRefreshLifecycleAccount(t, app, "ups_restart_refresh", time.Now().Add(time.Hour), true)
	if _, err := app.store.db.Exec(`UPDATE codex_oauth_refresh_states SET state='in_progress',attempt_revision=1 WHERE upstream_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true, CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var state, reason string
	if err := app.store.db.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id=?`, id).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "paused" || reason != "refresh_interrupted" {
		t.Fatalf("state=%q reason=%q", state, reason)
	}
}

func TestCodexRefreshSaveFailurePausesWithoutReplay(t *testing.T) {
	dataDir := t.TempDir()
	app := openOAuthTestApp(t, dataDir)
	id := insertRefreshLifecycleAccount(t, app, "ups_save_failure", time.Now().Add(time.Minute), true)
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-save-failure"}})
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "rotated"}), nil
	})}
	app.refresh.beforePersist = func(string) error { return errors.New("synthetic persistence failure") }
	_, err := app.refresh.refresh(context.Background(), id, nil, false)
	var failure *codexRefreshFailure
	if !errors.As(err, &failure) || failure.code != "refresh_paused" {
		t.Fatalf("refresh error=%v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", ExperimentalCodexMembership: true, CodexOAuthClientID: testOAuthClientID, CodexOAuthRedirectURI: testOAuthRedirectURI})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("paused refresh must not reach provider")
	})}
	var selected route
	if err := app.store.db.QueryRow(`SELECT id,endpoint,credential_ciphertext,provider_kind,revision,credential_state,key_version FROM upstreams WHERE id=?`, id).
		Scan(&selected.AccountID, &selected.Endpoint, &selected.Ciphertext, &selected.ProviderKind, &selected.Revision, &selected.CredentialState, &selected.KeyVersion); err != nil {
		t.Fatal(err)
	}
	_, credential, runErr := app.acquireCodexCredential(context.Background(), selected)
	if runErr != nil || credential == nil {
		t.Fatalf("still-valid paused access was rejected: %v", runErr)
	}
	credential.Destroy()
	_, err = app.refresh.refresh(context.Background(), id, nil, true)
	if !errors.As(err, &failure) || failure.code != "refresh_paused" || calls.Load() != 1 {
		t.Fatalf("replay error=%v calls=%d", err, calls.Load())
	}
	app.refresh.now = func() time.Time { return time.Now().UTC().Add(2 * time.Minute) }
	_, credential, runErr = app.acquireCodexCredential(context.Background(), selected)
	if credential != nil || runErr == nil || runErr.Code != membership.CodexErrorReauthentication || calls.Load() != 1 {
		t.Fatalf("expired paused access credential=%v error=%v calls=%d", credential, runErr, calls.Load())
	}
}

func TestCodexRefreshScanIsBoundedAndFair(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	for index := 0; index < codexRefreshScanLimit; index++ {
		insertRefreshLifecycleAccount(t, app, fmt.Sprintf("ups_scan_%02d", index), time.Now().Add(time.Hour), true)
	}
	target := insertRefreshLifecycleAccount(t, app, "ups_scan_16", time.Now().Add(time.Minute), true)
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-scan"}})
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "rotated-scan"}), nil
	})}
	app.refresh.scan(context.Background())
	if calls.Load() != 0 {
		t.Fatalf("first bounded scan made %d token calls", calls.Load())
	}
	app.refresh.scan(context.Background())
	var revision int64
	if err := app.store.db.QueryRow(`SELECT revision FROM upstreams WHERE id=?`, target).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || revision != 2 {
		t.Fatalf("fair scan calls=%d revision=%d", calls.Load(), revision)
	}
}

func TestCodexRefreshWaitIsCancellableAndReloadsReplacement(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	id := insertRefreshLifecycleAccount(t, app, "ups_wait_refresh", time.Now().Add(time.Hour), true)
	lock := app.refresh.accountLock(id)
	<-lock.token
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.refresh.refresh(cancelled, id, nil, false); err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait error=%v", err)
	}

	var old route
	if err := app.store.db.QueryRow(`SELECT id,endpoint,credential_ciphertext,provider_kind,revision,credential_state,key_version FROM upstreams WHERE id=?`, id).
		Scan(&old.AccountID, &old.Endpoint, &old.Ciphertext, &old.ProviderKind, &old.Revision, &old.CredentialState, &old.KeyVersion); err != nil {
		t.Fatal(err)
	}
	type result struct {
		route route
		cred  *membership.CodexAuthCredential
		err   *codexRunError
	}
	done := make(chan result, 1)
	go func() {
		r, credential, runErr := app.acquireCodexCredential(context.Background(), old)
		done <- result{route: r, cred: credential, err: runErr}
	}()
	newRaw := []byte(syntheticCodexAuth(t, "replacement", time.Now().Add(time.Hour)))
	newCiphertext, err := app.secrets.encryptCodexAuth(id, newRaw)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := app.store.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`UPDATE upstreams SET credential_ciphertext=?,revision=2 WHERE id=? AND revision=1`, newCiphertext, id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM codex_oauth_bindings WHERE upstream_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM codex_oauth_refresh_states WHERE upstream_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	lock.token <- struct{}{}
	got := <-done
	if got.err != nil || got.cred == nil {
		t.Fatalf("acquire error=%v", got.err)
	}
	defer got.cred.Destroy()
	if got.route.Revision != 2 || got.cred.RefreshTokenSecret() != "refresh-replacement" {
		t.Fatalf("revision=%d refresh=%q", got.route.Revision, got.cred.RefreshTokenSecret())
	}
}

func TestCodexRequestAndManualRefreshShareRevisionLock(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	id := insertRefreshLifecycleAccount(t, app, "ups_shared_refresh", time.Now().Add(time.Minute), true)
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "account-shared"}})
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "rotated-shared"}), nil
	})}
	type refreshResult struct {
		snapshot codexRefreshSnapshot
		err      error
	}
	requestDone := make(chan refreshResult, 1)
	go func() {
		snapshot, err := app.refresh.refresh(context.Background(), id, nil, false)
		requestDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	<-started
	expected := int64(1)
	manualDone := make(chan refreshResult, 1)
	go func() {
		snapshot, err := app.refresh.refresh(context.Background(), id, &expected, true)
		manualDone <- refreshResult{snapshot: snapshot, err: err}
	}()
	close(release)
	requestResult := <-requestDone
	manualResult := <-manualDone
	if requestResult.err != nil || requestResult.snapshot.revision != 2 {
		t.Fatalf("request result revision=%d err=%v", requestResult.snapshot.revision, requestResult.err)
	}
	var failure *codexRefreshFailure
	if !errors.As(manualResult.err, &failure) || failure.code != "revision_conflict" || manualResult.snapshot.revision != 2 {
		t.Fatalf("manual result revision=%d err=%v", manualResult.snapshot.revision, manualResult.err)
	}
	if calls.Load() != 1 {
		t.Fatalf("token calls=%d", calls.Load())
	}
}

func TestCodexRefreshSkipsDisabledUpstream(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	id := insertRefreshLifecycleAccount(t, app, "ups_disabled_refresh", time.Now().Add(time.Minute), false)
	var calls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not be called")
	})}
	if _, err := app.refresh.refresh(context.Background(), id, nil, false); err == nil {
		t.Fatal("disabled upstream unexpectedly refreshed")
	}
	var state string
	if err := app.store.db.QueryRow(`SELECT state FROM codex_oauth_refresh_states WHERE upstream_id=?`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || state != "ready" {
		t.Fatalf("calls=%d state=%q", calls.Load(), state)
	}
}

func insertRefreshLifecycleAccount(t *testing.T, app *App, id string, expires time.Time, enabled bool) string {
	t.Helper()
	raw := []byte(syntheticCodexAuth(t, id, expires))
	ciphertext, err := app.secrets.encryptCodexAuth(id, raw)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO upstreams(
		id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id
	) VALUES(?,?,?,?,?,?,?,?,?,?,NULL,?)`, id, id, codexMembershipProvider, codexMembershipEndpoint, boolInt(enabled), ciphertext, 2, 1, utcNow(), codexStateImported, "operation-"+id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES(?,?,?,?)`, id, testOAuthClientID, "authorization_code", utcNow()); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO codex_oauth_refresh_states(upstream_id,state,reason_code,attempt_revision,updated_at) VALUES(?,'ready',NULL,1,?)`, id, utcNow()); err != nil {
		t.Fatal(err)
	}
	return id
}
