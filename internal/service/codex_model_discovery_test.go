package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cpacloud.local/server/internal/membership"
)

type codexCatalogFunc func(context.Context, *membership.CodexAuthCredential) (membership.CodexModelCatalog, error)

func (f codexCatalogFunc) List(ctx context.Context, credential *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
	return f(ctx, credential)
}

func TestCodexModelCatalogAdminCacheReplacementAndNoPermissionMutation(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("synthetic-catalog-password\n")); err != nil {
		t.Fatal(err)
	}
	app := openCodexAdminTestApp(t, dir, true)
	defer app.Close()
	var calls atomic.Int32
	hidden, visible, apiUnsupported := "hide", "list", false
	app.codexCatalog = codexCatalogFunc(func(ctx context.Context, credential *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
		calls.Add(1)
		if credential.AccountIDSecret() != "synthetic-account" {
			t.Error("incorrect catalog credential")
		}
		return membership.CodexModelCatalog{Models: []membership.CodexCatalogModel{
			{ID: "z-model", DisplayName: "Z", Visibility: &visible, SupportedInAPI: &apiUnsupported},
			{ID: "hidden-model", DisplayName: "Hidden", Visibility: &hidden},
			{ID: "a-model", DisplayName: "A"},
			{ID: "a-model", DisplayName: "A"},
		}}, nil
	})
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	// The shared login helper uses this password.
	response := requestJSON(t, http.MethodPost, server.URL+"/admin/api/v1/sessions", `{"username":"admin","password":"synthetic-catalog-password"}`, nil, "", server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login: %s", readBody(response))
	}
	var login struct {
		CSRF string `json:"csrf_token"`
	}
	cookie := response.Cookies()[0]
	decodeResponse(t, response, &login)
	csrf := login.CSRF
	auth := codexAdminAuthJSON(t, time.Now().Add(time.Hour), "synthetic-account", "synthetic-refresh")
	created := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
		"name": "catalog", "auth_json": auth, "operation_id": "1fa5fa10-702c-4727-86dd-39bce9c32d78",
	}, cookie, csrf, server.URL)
	if created.StatusCode != http.StatusCreated {
		t.Fatalf("import: %s", readBody(created))
	}
	var upstream upstreamView
	decodeResponse(t, created, &upstream)
	target := server.URL + "/admin/api/v1/upstreams/" + upstream.ID + "/discover-models"
	denied := requestJSON(t, http.MethodPost, target, "{}", cookie, "", server.URL)
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("CSRF: %d", denied.StatusCode)
	}
	readBody(denied)
	if calls.Load() != 0 {
		t.Fatal("unauthorized catalog call")
	}
	for attempt := 0; attempt < 2; attempt++ {
		result := requestJSON(t, http.MethodPost, target, "{}", cookie, csrf, server.URL)
		if result.StatusCode != http.StatusOK {
			t.Fatalf("discover: %s", readBody(result))
		}
		var envelope struct {
			Items []codexCatalogItem `json:"items"`
		}
		decodeResponse(t, result, &envelope)
		if len(envelope.Items) != 2 || envelope.Items[0].ID != "a-model" || envelope.Items[1].ID != "z-model" {
			t.Fatalf("catalog: %+v", envelope)
		}
		if envelope.Items[0].Capabilities.SupportedInAPI != nil || envelope.Items[1].Capabilities.SupportedInAPI == nil || *envelope.Items[1].Capabilities.SupportedInAPI {
			t.Fatal("invented or lost upstream capability")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("cache calls=%d", calls.Load())
	}
	var models, permissions int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM employee_models`).Scan(&permissions); err != nil {
		t.Fatal(err)
	}
	if models != 0 || permissions != 0 {
		t.Fatal("discovery changed route/permission")
	}
	replaced := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+upstream.ID+"/codex-auth", map[string]any{"expected_revision": 1, "auth_json": auth}, cookie, csrf, server.URL)
	if replaced.StatusCode != http.StatusOK {
		t.Fatalf("replace: %s", readBody(replaced))
	}
	readBody(replaced)
	result := requestJSON(t, http.MethodPost, target, "{}", cookie, csrf, server.URL)
	if result.StatusCode != http.StatusOK {
		t.Fatalf("fresh discover: %s", readBody(result))
	}
	readBody(result)
	if calls.Load() != 2 {
		t.Fatal("replacement did not invalidate catalog")
	}
	// A credential change during the remote call must discard the old result.
	app.catalogMu.Lock()
	app.catalogs = nil
	app.catalogMu.Unlock()
	app.codexCatalog = codexCatalogFunc(func(_ context.Context, _ *membership.CodexAuthCredential) (membership.CodexModelCatalog, error) {
		_, err := app.store.db.Exec(`UPDATE upstreams SET revision=revision+1 WHERE id=?`, upstream.ID)
		if err != nil {
			t.Error(err)
		}
		return membership.CodexModelCatalog{}, nil
	})
	conflict := requestJSON(t, http.MethodPost, target, "{}", cookie, csrf, server.URL)
	assertCodexAdminError(t, conflict, http.StatusConflict, "revision_conflict")
	if len(app.catalogs) != 0 {
		t.Fatal("stale result cached")
	}
	// Default-off gate remains authoritative even with a populated cache/client.
	app.cfg.ExperimentalCodexMembership = false
	disabled := requestJSON(t, http.MethodPost, target, "{}", cookie, csrf, server.URL)
	assertCodexAdminError(t, disabled, http.StatusForbidden, "feature_disabled")
}
