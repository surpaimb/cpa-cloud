package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	runtimeFixtureTemplateOnce sync.Once
	runtimeFixtureTemplateDir  string
	runtimeFixtureTemplateErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if runtimeFixtureTemplateDir != "" {
		_ = os.RemoveAll(runtimeFixtureTemplateDir)
	}
	os.Exit(code)
}

func runtimeTestDataDir(t *testing.T) string {
	t.Helper()
	runtimeFixtureTemplateOnce.Do(func() {
		runtimeFixtureTemplateDir, runtimeFixtureTemplateErr = os.MkdirTemp("", "cpa-cloud-runtime-template-")
		if runtimeFixtureTemplateErr != nil {
			return
		}
		if err := Initialize(context.Background(), runtimeFixtureTemplateDir, strings.NewReader(accountPoolTestPassword+"\n")); err != nil {
			runtimeFixtureTemplateErr = fmt.Errorf("initialize runtime template: %w", err)
			return
		}
		app, err := Open(context.Background(), Config{DataDir: runtimeFixtureTemplateDir, Listen: "127.0.0.1:0", Version: "test"})
		if err != nil {
			runtimeFixtureTemplateErr = fmt.Errorf("open runtime template: %w", err)
			return
		}
		if err := app.store.migrateAccountPools(context.Background()); err != nil {
			_ = app.Close()
			runtimeFixtureTemplateErr = fmt.Errorf("migrate runtime template: %w", err)
			return
		}
		if err := app.Close(); err != nil {
			runtimeFixtureTemplateErr = fmt.Errorf("close runtime template: %w", err)
		}
	})
	if runtimeFixtureTemplateErr != nil {
		t.Fatal(runtimeFixtureTemplateErr)
	}
	destination := t.TempDir()
	entries, err := os.ReadDir(runtimeFixtureTemplateDir)
	if err != nil {
		t.Fatalf("read runtime template: %v", err)
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			t.Fatalf("runtime template contains non-regular entry %q", entry.Name())
		}
		source := filepath.Join(runtimeFixtureTemplateDir, entry.Name())
		contents, err := os.ReadFile(source)
		if err != nil {
			t.Fatalf("read runtime template file %q: %v", entry.Name(), err)
		}
		info, err := entry.Info()
		if err != nil {
			t.Fatalf("inspect runtime template file %q: %v", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), contents, info.Mode().Perm()); err != nil {
			t.Fatalf("copy runtime template file %q: %v", entry.Name(), err)
		}
	}
	return destination
}

func newRuntimeAccountPoolFixture(t *testing.T) *accountPoolFixture {
	t.Helper()
	dataDir := runtimeTestDataDir(t)
	app, err := Open(context.Background(), Config{DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test"})
	if err != nil {
		t.Fatalf("open runtime fixture: %v", err)
	}
	if err := app.store.migrateAccountPools(context.Background()); err != nil {
		_ = app.Close()
		t.Fatalf("migrate runtime fixture: %v", err)
	}
	t.Cleanup(func() {
		if err := app.Close(); err != nil {
			t.Errorf("close runtime fixture: %v", err)
		}
	})
	return &accountPoolFixture{app: app}
}

func (f *accountPoolFixture) enableRuntimeAdminHTTP(t *testing.T) {
	t.Helper()
	if f.server != nil {
		return
	}
	f.server = httptest.NewServer(f.app.Handler())
	t.Cleanup(func() { f.server.Close() })
	f.cookie, f.csrf = installRuntimeAdminSession(t, f.app, time.Now().UTC().Add(time.Hour))
}

func installRuntimeAdminSession(t *testing.T, app *App, expires time.Time) (*http.Cookie, string) {
	t.Helper()
	var adminID string
	if err := app.store.db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatalf("load runtime fixture administrator: %v", err)
	}
	token, err := randomToken(32)
	if err != nil {
		t.Fatalf("create runtime fixture session token: %v", err)
	}
	csrf, err := randomToken(32)
	if err != nil {
		t.Fatalf("create runtime fixture CSRF token: %v", err)
	}
	sessionID, err := newID("ses")
	if err != nil {
		t.Fatalf("create runtime fixture session id: %v", err)
	}
	if _, err := app.store.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`,
		sessionID, adminID, app.secrets.digest("admin-session/v1", token), csrf, expires.Format(time.RFC3339Nano), utcNow()); err != nil {
		t.Fatalf("insert runtime fixture session: %v", err)
	}
	return &http.Cookie{Name: adminCookieName, Value: token, Path: "/admin/"}, csrf
}

func TestRuntimeFixtureTemplateIsIndependentAndKeepsBcryptCost(t *testing.T) {
	firstDir := runtimeTestDataDir(t)
	secondDir := runtimeTestDataDir(t)
	first, err := openStore(firstDir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	second, err := openStore(secondDir)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	var hash []byte
	if err := first.db.QueryRow(`SELECT password_hash FROM admins WHERE username='admin'`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if cost, err := bcrypt.Cost(hash); err != nil || cost != 12 {
		t.Fatalf("template administrator bcrypt cost=%d err=%v", cost, err)
	}
	for _, table := range []string{"employees", "access_keys", "upstreams", "sessions"} {
		var count int
		if err := first.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("template table %s count=%d err=%v", table, count, err)
		}
	}
	var adminID string
	if err := first.db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := first.db.Exec(`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES('ses_isolation',?,X'01','csrf','2099-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, adminID); err != nil {
		t.Fatal(err)
	}
	var secondSessions int
	if err := second.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&secondSessions); err != nil || secondSessions != 0 {
		t.Fatalf("template copies shared session rows=%d err=%v", secondSessions, err)
	}
}

func TestRuntimeFixtureDirectSessionKeepsAuthorizationBoundaries(t *testing.T) {
	fixture := newRuntimeAccountPoolFixture(t)
	fixture.enableRuntimeAdminHTTP(t)
	response := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/session", "", fixture.cookie, "", fixture.server.URL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("direct session was not authorized: status=%d body=%s", response.StatusCode, readBody(response))
	}
	response.Body.Close()
	withoutCSRF := requestJSON(t, http.MethodDelete, fixture.server.URL+"/admin/api/v1/sessions", "", fixture.cookie, "", fixture.server.URL)
	if withoutCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("write without CSRF status=%d body=%s", withoutCSRF.StatusCode, readBody(withoutCSRF))
	}
	withoutCSRF.Body.Close()
	wrongCSRF := requestJSON(t, http.MethodDelete, fixture.server.URL+"/admin/api/v1/sessions", "", fixture.cookie, fixture.csrf+"-wrong", fixture.server.URL)
	if wrongCSRF.StatusCode != http.StatusForbidden {
		t.Fatalf("write with wrong CSRF status=%d body=%s", wrongCSRF.StatusCode, readBody(wrongCSRF))
	}
	wrongCSRF.Body.Close()
	logout := requestJSON(t, http.MethodDelete, fixture.server.URL+"/admin/api/v1/sessions", "", fixture.cookie, fixture.csrf, fixture.server.URL)
	if logout.StatusCode != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logout.StatusCode, readBody(logout))
	}
	logout.Body.Close()
	afterLogout := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/session", "", fixture.cookie, "", fixture.server.URL)
	if afterLogout.StatusCode != http.StatusUnauthorized {
		t.Fatalf("deleted session remained authorized: status=%d body=%s", afterLogout.StatusCode, readBody(afterLogout))
	}
	afterLogout.Body.Close()
	expiredCookie, _ := installRuntimeAdminSession(t, fixture.app, time.Now().UTC().Add(-time.Second))
	expired := requestJSON(t, http.MethodGet, fixture.server.URL+"/admin/api/v1/session", "", expiredCookie, "", fixture.server.URL)
	if expired.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expired session was authorized: status=%d body=%s", expired.StatusCode, readBody(expired))
	}
	expired.Body.Close()
}
