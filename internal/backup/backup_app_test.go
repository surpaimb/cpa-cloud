package backup_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"cpacloud.local/server/internal/backup"
	"cpacloud.local/server/internal/service"
	_ "modernc.org/sqlite"
)

func TestEncryptedBackupRestoresIntoRealAppWithEmployeeKey(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	adminPassword := "synthetic-admin-password"
	if err := service.Initialize(ctx, source, bytes.NewBufferString(adminPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	app, err := service.Open(ctx, service.Config{DataDir: source, Listen: "127.0.0.1:0", Version: "backup-e2e"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(app.Handler())
	client := newCookieClient(t)
	csrf := loginAdmin(t, client, server.URL, adminPassword)

	employee := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/employees", csrf,
		map[string]any{"name": "Synthetic Employee", "department": "QA", "note": "backup e2e"}, http.StatusCreated)
	employeeID := employee["id"].(string)
	keyView := adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/employees/"+employeeID+"/keys", csrf,
		map[string]any{"name": "Synthetic Key", "operation_id": "backup-e2e-key"}, http.StatusCreated)
	employeeKey := keyView["key"].(string)
	adminJSON(t, client, http.MethodPost, server.URL+"/admin/api/v1/upstreams", csrf,
		map[string]any{"name": "Synthetic API", "provider_kind": "openai-compatible", "endpoint": "https://example.com", "api_key": "synthetic-upstream-secret"}, http.StatusCreated)

	observer, err := sql.Open("sqlite", filepath.Join(source, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,operation_id)
		 VALUES('ups_backup_codex','Synthetic Membership','codex-membership','https://chatgpt.com/backend-api/codex',1,x'01020304',2,1,'2026-09-24T00:00:00Z','imported_unverified','00000000-0000-0000-0000-000000000001')`,
		`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES('ups_backup_codex','synthetic-client','authorization_code','2026-09-24T00:00:00Z')`,
		`INSERT INTO codex_oauth_refresh_states(upstream_id,state,reason_code,attempt_revision,updated_at) VALUES('ups_backup_codex','in_progress',NULL,1,'2026-09-24T00:00:00Z')`,
	} {
		if _, err := observer.Exec(statement); err != nil {
			observer.Close()
			t.Fatal(err)
		}
	}
	observer.Close()
	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app, err = service.Open(ctx, service.Config{DataDir: source, Listen: "127.0.0.1:0", Version: "backup-e2e-source-reopen"})
	if err != nil {
		t.Fatalf("reopen synthetic source before backup: %v", err)
	}
	defer app.Close()
	observer, err = sql.Open("sqlite", filepath.Join(source, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Exec(`UPDATE governance_budget_clock SET last_effective_at='2026-01-02T12:34:56.000000000Z' WHERE singleton=1`); err != nil {
		observer.Close()
		t.Fatal(err)
	}
	observer.Close()

	packagePath := filepath.Join(root, "real-app.cpacb")
	packagePassword := []byte("synthetic-package-password")
	if _, err := backup.Create(ctx, source, packagePath, packagePassword, "backup-e2e"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	if _, err := backup.Restore(ctx, packagePath, target, packagePassword); err != nil {
		t.Fatal(err)
	}
	preOpenDB, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	var preservedBudgetClock string
	if err := preOpenDB.QueryRow(`SELECT last_effective_at FROM governance_budget_clock WHERE singleton=1`).Scan(&preservedBudgetClock); err != nil {
		preOpenDB.Close()
		t.Fatal(err)
	}
	preOpenDB.Close()
	if preservedBudgetClock != "2026-01-02T12:34:56.000000000Z" {
		t.Fatalf("pre-start budget clock=%q", preservedBudgetClock)
	}

	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	restored, err := service.Open(ctx, service.Config{DataDir: target, Listen: "127.0.0.1:0", Version: "backup-e2e-restored"})
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	restoredServer := httptest.NewServer(restored.Handler())
	defer restoredServer.Close()

	req, _ := http.NewRequest(http.MethodGet, restoredServer.URL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+employeeKey)
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("restored employee key status=%d", response.StatusCode)
	}

	oldSessionReq, _ := http.NewRequest(http.MethodGet, restoredServer.URL+"/admin/api/v1/session", nil)
	for _, cookie := range client.Jar.Cookies(oldSessionReq.URL) {
		oldSessionReq.AddCookie(cookie)
	}
	oldSessionResponse, err := http.DefaultClient.Do(oldSessionReq)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, oldSessionResponse.Body)
	oldSessionResponse.Body.Close()
	if oldSessionResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("restored old admin session status=%d", oldSessionResponse.StatusCode)
	}

	newClient := newCookieClient(t)
	loginAdmin(t, newClient, restoredServer.URL, adminPassword)
	db, err := sql.Open("sqlite", filepath.Join(target, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var refreshState, reason string
	if err := db.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id='ups_backup_codex'`).Scan(&refreshState, &reason); err != nil {
		t.Fatal(err)
	}
	if refreshState != "paused" || reason != "backup_restore_uncertain_refresh" {
		t.Fatalf("refresh state=%q reason=%q", refreshState, reason)
	}
	var preserved int
	if err := db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_bindings b JOIN upstreams u ON u.id=b.upstream_id WHERE b.upstream_id='ups_backup_codex' AND length(u.credential_ciphertext)>0`).Scan(&preserved); err != nil || preserved != 1 {
		t.Fatalf("membership state count=%d err=%v", preserved, err)
	}

	// Package plaintext must not contain any credential or employee-key secret.
	packageBytes, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{employeeKey, "synthetic-upstream-secret", adminPassword} {
		if bytes.Contains(packageBytes, []byte(secret)) {
			t.Fatalf("encrypted package exposed a secret marker")
		}
	}
}

func newCookieClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar}
}

func loginAdmin(t *testing.T, client *http.Client, baseURL, password string) string {
	t.Helper()
	requestBody, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
	response, err := client.Post(baseURL+"/admin/api/v1/sessions", "application/json", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("login status=%d body=%s", response.StatusCode, body)
	}
	var result map[string]string
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result["csrf_token"]
}

func adminJSON(t *testing.T, client *http.Client, method, endpoint, csrf string, input map[string]any, want int) map[string]any {
	t.Helper()
	body, _ := json.Marshal(input)
	req, err := http.NewRequest(method, endpoint, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", originFor(endpoint))
	req.Header.Set("X-CSRF-Token", csrf)
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != want {
		responseBody, _ := io.ReadAll(response.Body)
		t.Fatalf("%s status=%d want=%d body=%s", endpoint, response.StatusCode, want, responseBody)
	}
	var result map[string]any
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}

func originFor(endpoint string) string {
	for index := len("http://"); index < len(endpoint); index++ {
		if endpoint[index] == '/' {
			return endpoint[:index]
		}
	}
	panic(fmt.Sprintf("invalid endpoint %q", endpoint))
}
