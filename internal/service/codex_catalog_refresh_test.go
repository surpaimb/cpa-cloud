package service

// Independently authored integration checks for the shared credential service.
// All credentials and transports are synthetic; no provider network is used.
import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

func TestCodexCatalogUsesRotatedCredentialAndRevision(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	id := insertRefreshLifecycleAccount(t, app, "ups_catalog_refresh", time.Now().Add(time.Minute), true)
	access := oauthTestJWT(t, map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	idToken := oauthTestJWT(t, map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "catalog-rotated-account"}})
	var tokenCalls, catalogCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return oauthHTTPResponse(200, map[string]string{"access_token": access, "id_token": idToken, "refresh_token": "catalog-rotated-refresh"}), nil
	})}
	app.codexCatalog = codexCatalogFunc(func(_ context.Context, credential *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
		catalogCalls.Add(1)
		if credential.AccessTokenSecret() != access || credential.AccountIDSecret() != "catalog-rotated-account" {
			t.Error("catalog received stale credentials")
		}
		return membership.CodexModelCatalog{Models: []membership.CodexCatalogModel{{ID: "synthetic-catalog-model"}}}, nil
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	for range 2 {
		response := requestJSON(t, "POST", server.URL+"/admin/api/v1/upstreams/"+id+"/discover-models", "{}", cookie, csrf, server.URL)
		if response.StatusCode != 200 {
			t.Fatalf("catalog status %d: %s", response.StatusCode, readBody(response))
		}
		readBody(response)
	}
	if tokenCalls.Load() != 1 || catalogCalls.Load() != 1 || app.catalogs[id].revision != 2 {
		t.Fatal("refresh/cache did not share the saved credential revision")
	}
}

func TestCodexCatalogPausedRefreshAllowsOnlyUnexpiredAccess(t *testing.T) {
	app := openOAuthTestApp(t, t.TempDir())
	defer app.Close()
	var tokenCalls, catalogCalls atomic.Int32
	app.oauthHTTP = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		tokenCalls.Add(1)
		return oauthHTTPResponse(500, nil), nil
	})}
	app.codexCatalog = codexCatalogFunc(func(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
		catalogCalls.Add(1)
		return membership.CodexModelCatalog{}, nil
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	cookie, csrf := loginTestAdmin(t, server.URL)
	for _, test := range []struct {
		id      string
		expires time.Time
		status  int
	}{
		{"ups_valid_paused_catalog", time.Now().Add(30 * time.Second), 200},
		{"ups_expired_paused_catalog", time.Now().Add(-time.Minute), 409},
	} {
		id := insertRefreshLifecycleAccount(t, app, test.id, test.expires, true)
		if _, err := app.store.db.Exec(`UPDATE codex_oauth_refresh_states SET state='paused',reason_code='uncertain_refresh_outcome' WHERE upstream_id=?`, id); err != nil {
			t.Fatal(err)
		}
		response := requestJSON(t, "POST", server.URL+"/admin/api/v1/upstreams/"+id+"/discover-models", "{}", cookie, csrf, server.URL)
		if response.StatusCode != test.status {
			t.Fatalf("catalog status %d: %s", response.StatusCode, readBody(response))
		}
		readBody(response)
		var state string
		if err := app.store.db.QueryRow(`SELECT state FROM codex_oauth_refresh_states WHERE upstream_id=?`, id).Scan(&state); err != nil || state != "paused" {
			t.Fatal("catalog changed paused state")
		}
	}
	if tokenCalls.Load() != 0 || catalogCalls.Load() != 1 {
		t.Fatal("paused refresh replayed or expired access reached catalog")
	}
}
