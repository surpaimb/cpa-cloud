package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestUpstreamHealthCodexDisabledDoesNotDecryptRefreshOrList(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_disabled", time.Now().Add(time.Hour), true)
	if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_ciphertext=X'00' WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	app.cfg.ExperimentalCodexMembership = false
	var refreshCalls, catalogCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		refreshCalls.Add(1)
		return nil, context.Canceled
	})}
	app.codexCatalog = codexCatalogFunc(func(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
		catalogCalls.Add(1)
		return membership.CodexModelCatalog{}, nil
	})

	for index, scope := range []string{"local_credential", "catalog"} {
		operationID := []string{"ba2bdd4e-7a25-4ff4-91ed-f503a8a81001", "ba2bdd4e-7a25-4ff4-91ed-f503a8a81002"}[index]
		view := runCodexHealthOperation(t, app, id, operationID, 1, scope)
		if view.ResultCode == nil || *view.ResultCode != "unsupported" || view.TestedRevision == nil || *view.TestedRevision != 1 {
			t.Fatalf("disabled %s result=%+v", scope, view)
		}
	}
	if refreshCalls.Load() != 0 || catalogCalls.Load() != 0 {
		t.Fatalf("disabled health touched provider: refresh=%d catalog=%d", refreshCalls.Load(), catalogCalls.Load())
	}
}

func TestUpstreamHealthCodexCredentialCatalogRefreshStaleAnd401(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()

	t.Run("local does not refresh", func(t *testing.T) {
		id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_local", time.Now().Add(time.Hour), true)
		originalNow := app.refresh.now
		app.refresh.now = func() time.Time { return time.Now().UTC().Add(56 * time.Minute) }
		defer func() { app.refresh.now = originalNow }()
		var refreshCalls, catalogCalls atomic.Int32
		app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			refreshCalls.Add(1)
			return nil, context.Canceled
		})}
		app.codexCatalog = codexCatalogFunc(func(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
			catalogCalls.Add(1)
			return membership.CodexModelCatalog{}, nil
		})
		view := runCodexHealthOperation(t, app, id, "ba2bdd4e-7a25-4ff4-91ed-f503a8a81101", 1, "local_credential")
		if view.ResultCode == nil || *view.ResultCode != "local_credential_ok" || view.TestedRevision == nil || *view.TestedRevision != 1 {
			t.Fatalf("local result code=%q tested=%d view=%+v", healthResultValue(view.ResultCode), healthRevisionValue(view.TestedRevision), view)
		}
		if refreshCalls.Load() != 0 || catalogCalls.Load() != 0 {
			t.Fatalf("local check refreshed or listed: refresh=%d catalog=%d", refreshCalls.Load(), catalogCalls.Load())
		}
	})

	t.Run("catalog bypasses display cache and operation is idempotent", func(t *testing.T) {
		id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_cache", time.Now().Add(time.Hour), true)
		app.catalogMu.Lock()
		if app.catalogs == nil {
			app.catalogs = make(map[string]codexCatalogCacheEntry)
		}
		app.catalogs[id] = codexCatalogCacheEntry{revision: 1, expires: time.Now().Add(codexCatalogTTL), body: []byte(`{"items":[{"id":"cached-only"}]}`)}
		app.catalogMu.Unlock()
		var calls atomic.Int32
		app.codexCatalog = codexCatalogFunc(func(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
			calls.Add(1)
			return membership.CodexModelCatalog{Models: []membership.CodexCatalogModel{{ID: "fresh-model"}}}, nil
		})
		firstID := "ba2bdd4e-7a25-4ff4-91ed-f503a8a81201"
		secondID := "ba2bdd4e-7a25-4ff4-91ed-f503a8a81202"
		for _, operationID := range []string{firstID, secondID} {
			view := runCodexHealthOperation(t, app, id, operationID, 1, "catalog")
			if view.ResultCode == nil || *view.ResultCode != "catalog_ok" {
				t.Fatalf("fresh catalog result=%+v", view)
			}
		}
		same := runCodexHealthOperation(t, app, id, secondID, 1, "catalog")
		if same.OperationID != secondID || calls.Load() != 2 {
			t.Fatalf("catalog calls=%d repeated=%+v", calls.Load(), same)
		}
	})

	t.Run("shared refresh publishes tested revision without self deadlock", func(t *testing.T) {
		id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_refresh", time.Now().Add(time.Minute), true)
		rotatedAccess := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
		rotatedID := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "health-refreshed-account"}})
		var tokenCalls, catalogCalls atomic.Int32
		app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			tokenCalls.Add(1)
			return oauthHTTPResponse(http.StatusOK, map[string]string{"access_token": rotatedAccess, "id_token": rotatedID, "refresh_token": "health-rotated-refresh"}), nil
		})}
		app.codexCatalog = codexCatalogFunc(func(_ context.Context, credential *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
			catalogCalls.Add(1)
			if credential.AccessTokenSecret() != rotatedAccess || credential.AccountIDSecret() != "health-refreshed-account" {
				t.Errorf("catalog received stale refreshed credential")
			}
			return membership.CodexModelCatalog{Models: []membership.CodexCatalogModel{{ID: "refresh-model"}}}, nil
		})
		type result struct {
			view upstreamTestOperationView
			err  error
		}
		done := make(chan result, 1)
		go func() {
			view, status, code, err := app.healthTests.run(context.Background(), id, "ba2bdd4e-7a25-4ff4-91ed-f503a8a81301", 1, "catalog")
			if err == nil && (status != http.StatusOK || code != "") {
				err = &healthTestUnexpectedResult{status: status, code: code}
			}
			done <- result{view: view, err: err}
		}()
		select {
		case got := <-done:
			if got.err != nil || got.view.RequestedRevision != 1 || got.view.TestedRevision == nil || *got.view.TestedRevision != 2 || got.view.ResultCode == nil || *got.view.ResultCode != "catalog_ok" {
				t.Fatalf("refresh health result=%+v err=%v", got.view, got.err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("health catalog deadlocked with the shared refresh/mutation lock")
		}
		if tokenCalls.Load() != 1 || catalogCalls.Load() != 1 {
			t.Fatalf("refresh calls=%d catalog calls=%d", tokenCalls.Load(), catalogCalls.Load())
		}
	})

	t.Run("concurrent reimport records stale and does not project observation", func(t *testing.T) {
		id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_stale", time.Now().Add(time.Hour), true)
		started := make(chan struct{})
		release := make(chan struct{})
		app.codexCatalog = codexCatalogFunc(func(ctx context.Context, _ *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
			close(started)
			select {
			case <-release:
				return membership.CodexModelCatalog{Models: []membership.CodexCatalogModel{{ID: "stale-model"}}}, nil
			case <-ctx.Done():
				return membership.CodexModelCatalog{}, ctx.Err()
			}
		})
		result := make(chan upstreamTestOperationView, 1)
		go func() {
			result <- runCodexHealthOperation(t, app, id, "ba2bdd4e-7a25-4ff4-91ed-f503a8a81401", 1, "catalog")
		}()
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("catalog did not start")
		}
		replaceCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		body := marshalTestJSON(t, map[string]any{"expected_revision": 1, "auth_json": syntheticCodexAuth(t, "health-reimport", time.Now().Add(time.Hour))})
		request, err := http.NewRequestWithContext(replaceCtx, http.MethodPut, "/admin/api/v1/upstreams/"+id+"/codex-auth", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.SetPathValue("id", id)
		recorder := httptest.NewRecorder()
		replaced := make(chan struct{})
		go func() {
			app.replaceCodexCredential(recorder, request, adminSession{})
			close(replaced)
		}()
		select {
		case <-replaced:
			if recorder.Code != http.StatusOK {
				t.Fatalf("concurrent reimport status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		case <-replaceCtx.Done():
			t.Fatal("health catalog retained the mutation lock during provider I/O")
		}
		close(release)
		var stale upstreamTestOperationView
		select {
		case stale = <-result:
		case <-time.After(3 * time.Second):
			t.Fatal("stale health operation did not finish")
		}
		if stale.ResultCode == nil || *stale.ResultCode != "stale" || stale.TestedRevision == nil || *stale.TestedRevision != 1 {
			t.Fatalf("stale operation=%+v", stale)
		}
		items := []upstreamView{{ID: id, ProviderKind: codexMembershipProvider, Revision: 2}}
		if err := app.decorateUpstreamObservations(context.Background(), items); err != nil {
			t.Fatal(err)
		}
		if items[0].LatestObservation != nil {
			t.Fatalf("stale operation projected as current observation: %+v", items[0].LatestObservation)
		}
		freshID := "ba2bdd4e-7a25-4ff4-91ed-f503a8a81402"
		fresh := runCodexHealthOperation(t, app, id, freshID, 2, "local_credential")
		if fresh.ResultCode == nil || *fresh.ResultCode != "local_credential_ok" {
			t.Fatalf("fresh observation=%+v", fresh)
		}
		if err := app.decorateUpstreamObservations(context.Background(), items); err != nil {
			t.Fatal(err)
		}
		if items[0].LatestObservation == nil || items[0].LatestObservation.OperationID != freshID {
			t.Fatalf("new observation was not projected: %+v", items[0].LatestObservation)
		}
	})

	t.Run("catalog 401 does not write reauthentication state", func(t *testing.T) {
		id := insertRefreshLifecycleAccount(t, app, "ups_health_codex_401", time.Now().Add(time.Hour), true)
		client, err := membership.NewCodexModelsClientWithTransport("0.1.0", roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusUnauthorized, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":"synthetic-private"}`))}, nil
		}))
		if err != nil {
			t.Fatal(err)
		}
		app.codexCatalog = client
		view := runCodexHealthOperation(t, app, id, "ba2bdd4e-7a25-4ff4-91ed-f503a8a81501", 1, "catalog")
		if view.ResultCode == nil || *view.ResultCode != "authentication_failed" {
			t.Fatalf("401 result=%+v", view)
		}
		var credentialState, refreshState string
		if err := app.store.db.QueryRow(`SELECT u.credential_state,r.state FROM upstreams u JOIN codex_oauth_refresh_states r ON r.upstream_id=u.id WHERE u.id=?`, id).Scan(&credentialState, &refreshState); err != nil {
			t.Fatal(err)
		}
		if credentialState != codexStateImported || refreshState != "ready" {
			t.Fatalf("401 mutated credential state=%q refresh state=%q", credentialState, refreshState)
		}
	})
}

type healthTestUnexpectedResult struct {
	status int
	code   string
}

func (e *healthTestUnexpectedResult) Error() string {
	return "unexpected health result"
}

func runCodexHealthOperation(t *testing.T, app *App, upstreamID, operationID string, revision int64, scope string) upstreamTestOperationView {
	t.Helper()
	view, status, code, err := app.healthTests.run(context.Background(), upstreamID, operationID, revision, scope)
	if err != nil || status != http.StatusOK || code != "" {
		t.Fatalf("run Codex health status=%d code=%q err=%v view=%+v", status, code, err, view)
	}
	return view
}

func healthResultValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func healthRevisionValue(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
