package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type upstreamBatchFixture struct {
	t       *testing.T
	dataDir string
	app     *App
	server  *httptest.Server
	cookie  *http.Cookie
	csrf    string
}

type upstreamBatchEnvelope struct {
	Items []upstreamBatchResult `json:"items"`
}

func newUpstreamBatchFixture(t *testing.T, codexEnabled bool) *upstreamBatchFixture {
	t.Helper()
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", Version: "test",
		AllowLoopbackUpstream: true, ExperimentalCodexMembership: codexEnabled,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.store.migrateUpstreamBatchItems(context.Background()); err != nil {
		app.Close()
		t.Fatal(err)
	}
	fixture := &upstreamBatchFixture{t: t, dataDir: dataDir, app: app}
	fixture.startServer()
	t.Cleanup(func() {
		if fixture.server != nil {
			fixture.server.Close()
		}
		if fixture.app != nil {
			_ = fixture.app.Close()
		}
	})
	return fixture
}

func (f *upstreamBatchFixture) startServer() {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /admin/api/v1/sessions", f.app.login)
	mux.HandleFunc("POST /admin/api/v1/upstreams/batch-import", f.app.requireAdmin(f.app.batchImportUpstreams, true))
	f.server = httptest.NewServer(requestMiddleware(mux))
	f.cookie, f.csrf = loginTestAdmin(f.t, f.server.URL)
}

func (f *upstreamBatchFixture) request(body any) *http.Response {
	f.t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		f.t.Fatal(err)
	}
	return requestJSON(f.t, http.MethodPost, f.server.URL+"/admin/api/v1/upstreams/batch-import", string(encoded), f.cookie, f.csrf, f.server.URL)
}

func TestUpstreamBatchImportsProvidersAndPersistsIdempotency(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, true)
	authJSON := syntheticBatchCodexAuth(t, "batch-access-marker", "batch-refresh-marker", "batch-account-marker")
	body := map[string]any{
		"operation_id": "550e8400-e29b-41d4-a716-446655440000",
		"items": []map[string]any{
			{"item_id": "openai", "name": "OpenAI compatible", "provider_kind": "openai-compatible", "endpoint": "http://127.0.0.1:18001/v1", "api_key": "batch-openai-secret"},
			{"item_id": "anthropic", "name": "Anthropic", "provider_kind": anthropicAPIKeyProvider, "endpoint": "http://127.0.0.1:18002", "api_key": "batch-anthropic-secret"},
			{"item_id": "gemini", "name": "Gemini", "provider_kind": geminiAPIKeyProvider, "api_key": "batch-gemini-secret"},
			{"item_id": "codex", "name": "Codex", "provider_kind": codexMembershipProvider, "auth_json": authJSON},
			{"item_id": "bad", "name": "Bad", "provider_kind": "unknown", "api_key": "batch-unknown-secret"},
		},
	}
	response := fixture.request(body)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.StatusCode, readBody(response))
	}
	var first upstreamBatchEnvelope
	decodeResponse(t, response, &first)
	if len(first.Items) != 5 {
		t.Fatalf("items=%+v", first.Items)
	}
	for i := 0; i < 4; i++ {
		if first.Items[i].Status != "created" || first.Items[i].UpstreamID == "" || first.Items[i].ErrorCode != "" {
			t.Fatalf("item %d=%+v", i, first.Items[i])
		}
	}
	if first.Items[4].Status != "failed" || first.Items[4].ErrorCode != "unsupported_provider" {
		t.Fatalf("failed item=%+v", first.Items[4])
	}

	var upstreamCount, batchCount, modelCount, bindingCount int
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM upstreams`).Scan(&upstreamCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM upstream_batch_items`).Scan(&batchCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_bindings`).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if upstreamCount != 4 || batchCount != 4 || modelCount != 0 || bindingCount != 0 {
		t.Fatalf("upstreams=%d batch=%d models=%d bindings=%d", upstreamCount, batchCount, modelCount, bindingCount)
	}

	assertBatchStoredCredential(t, fixture.app, first.Items[0].UpstreamID, "openai-compatible", 1, "batch-openai-secret")
	assertBatchStoredCredential(t, fixture.app, first.Items[1].UpstreamID, anthropicAPIKeyProvider, 1, "batch-anthropic-secret")
	assertBatchStoredCredential(t, fixture.app, first.Items[2].UpstreamID, geminiAPIKeyProvider, 2, "batch-gemini-secret")
	assertBatchStoredCredential(t, fixture.app, first.Items[3].UpstreamID, codexMembershipProvider, 2, "batch-refresh-marker")

	retry := fixture.request(body)
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retry.StatusCode, readBody(retry))
	}
	var repeated upstreamBatchEnvelope
	decodeResponse(t, retry, &repeated)
	for i := 0; i < 4; i++ {
		if repeated.Items[i].Status != "existing" || repeated.Items[i].UpstreamID != first.Items[i].UpstreamID {
			t.Fatalf("retry item %d=%+v first=%+v", i, repeated.Items[i], first.Items[i])
		}
	}
	if repeated.Items[4].Status != "failed" {
		t.Fatalf("failed items must be reevaluated: %+v", repeated.Items[4])
	}

	assertNoBatchPlaintextOnDisk(t, fixture.dataDir,
		"batch-openai-secret", "batch-anthropic-secret", "batch-gemini-secret",
		"batch-access-marker", "batch-refresh-marker", "batch-account-marker", "batch-unknown-secret")
}

func TestUpstreamBatchRejectsDuplicateAndConflictBeforeWrites(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	operationID := "650e8400-e29b-41d4-a716-446655440000"
	tooMany := make([]map[string]any, upstreamBatchMaxItems+1)
	for i := range tooMany {
		tooMany[i] = map[string]any{"item_id": fmt.Sprintf("item-%03d", i), "name": "Item", "provider_kind": geminiAPIKeyProvider, "api_key": "count-limit-secret"}
	}
	invalidTopLevel := fixture.request(map[string]any{"operation_id": operationID, "items": tooMany})
	assertCodexAdminError(t, invalidTopLevel, http.StatusBadRequest, "invalid_request")
	assertBatchCounts(t, fixture.app, 0, 0)

	duplicate := fixture.request(map[string]any{"operation_id": operationID, "items": []map[string]any{
		{"item_id": "same", "name": "One", "provider_kind": geminiAPIKeyProvider, "api_key": "duplicate-one"},
		{"item_id": "same", "name": "Two", "provider_kind": geminiAPIKeyProvider, "api_key": "duplicate-two"},
	}})
	assertCodexAdminError(t, duplicate, http.StatusBadRequest, "duplicate_item_id")
	assertBatchCounts(t, fixture.app, 0, 0)

	firstBody := map[string]any{"operation_id": operationID, "items": []map[string]any{
		{"item_id": "saved", "name": "Saved", "provider_kind": geminiAPIKeyProvider, "api_key": "conflict-original"},
	}}
	created := fixture.request(firstBody)
	if created.StatusCode != http.StatusOK {
		t.Fatalf("create=%d body=%s", created.StatusCode, readBody(created))
	}
	created.Body.Close()

	conflict := fixture.request(map[string]any{"operation_id": operationID, "items": []map[string]any{
		{"item_id": "must-not-create", "name": "New", "provider_kind": geminiAPIKeyProvider, "api_key": "new-secret"},
		{"item_id": "saved", "name": "Changed", "provider_kind": geminiAPIKeyProvider, "api_key": "conflict-changed"},
	}})
	assertCodexAdminError(t, conflict, http.StatusConflict, "operation_conflict")
	assertBatchCounts(t, fixture.app, 1, 1)
}

func TestUpstreamBatchFeatureFlagCSRFAndMixedFailures(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	body := map[string]any{"operation_id": "750e8400-e29b-41d4-a716-446655440000", "items": []map[string]any{
		{"item_id": "codex", "name": "Codex", "provider_kind": codexMembershipProvider, "auth_json": syntheticBatchCodexAuth(t, "disabled-access", "disabled-refresh", "disabled-account")},
		{"item_id": "missing-key", "name": "Missing", "provider_kind": geminiAPIKeyProvider},
		{"item_id": "bad-endpoint", "name": "Bad endpoint", "provider_kind": "openai-compatible", "endpoint": "file:///tmp/no", "api_key": "invalid-endpoint-secret"},
		{"item_id": "good", "name": "Good", "provider_kind": geminiAPIKeyProvider, "api_key": "mixed-good-secret"},
	}}
	response := fixture.request(body)
	var envelope upstreamBatchEnvelope
	decodeResponse(t, response, &envelope)
	wantCodes := []string{"feature_disabled", "invalid_credential", "invalid_endpoint", ""}
	for i, want := range wantCodes {
		if envelope.Items[i].ErrorCode != want {
			t.Fatalf("item %d=%+v want error=%q", i, envelope.Items[i], want)
		}
	}
	if envelope.Items[3].Status != "created" {
		t.Fatalf("good item=%+v", envelope.Items[3])
	}

	encoded, _ := json.Marshal(body)
	missingCSRF := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/upstreams/batch-import", string(encoded), fixture.cookie, "", fixture.server.URL)
	assertCodexAdminError(t, missingCSRF, http.StatusForbidden, "csrf_rejected")
	badOrigin := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/upstreams/batch-import", string(encoded), fixture.cookie, fixture.csrf, "https://attacker.example")
	assertCodexAdminError(t, badOrigin, http.StatusForbidden, "origin_rejected")
	assertBatchCounts(t, fixture.app, 1, 1)
}

func TestUpstreamBatchHTTPValidationIsWriteFreeAndRedacted(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	operationID := "760e8400-e29b-41d4-a716-446655440000"
	secret := "must-not-escape-http"

	tests := []struct {
		name string
		body string
	}{
		{name: "malformed", body: `{"operation_id":`},
		{name: "forged OAuth source", body: fmt.Sprintf(`{"operation_id":%q,"items":[{"item_id":"oauth","name":"OAuth","provider_kind":"codex-membership","auth_json":%q,"oauth_source":"authorization_code"}]}`, operationID, secret)},
		{name: "oversized", body: fmt.Sprintf(`{"operation_id":%q,"items":[{"item_id":"large","name":"Large","provider_kind":"gemini-api-key","api_key":%q}]}`, operationID, strings.Repeat("x", upstreamBatchMaxBody))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/upstreams/batch-import", test.body, fixture.cookie, fixture.csrf, fixture.server.URL)
			body := readBody(response)
			if response.StatusCode != http.StatusBadRequest || !strings.Contains(body, `"code":"invalid_request"`) {
				t.Fatalf("status=%d body=%s", response.StatusCode, body)
			}
			if strings.Contains(body, secret) || strings.Contains(body, "authorization_code") {
				t.Fatalf("response exposed request data: %s", body)
			}
			assertBatchCounts(t, fixture.app, 0, 0)
		})
	}

	encoded, err := json.Marshal(map[string]any{"operation_id": operationID, "items": []map[string]any{{
		"item_id": "unauthorized", "name": "Unauthorized", "provider_kind": geminiAPIKeyProvider, "api_key": secret,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := requestJSON(t, http.MethodPost, fixture.server.URL+"/admin/api/v1/upstreams/batch-import", string(encoded), nil, "", fixture.server.URL)
	unauthorizedBody := readBody(unauthorized)
	if unauthorized.StatusCode != http.StatusUnauthorized || strings.Contains(unauthorizedBody, secret) {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.StatusCode, unauthorizedBody)
	}
	assertBatchCounts(t, fixture.app, 0, 0)
}

func TestUpstreamBatchSQLiteFailureRollsBackAndCanRetry(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	const databaseMarker = "synthetic-receipt-database-error"
	if _, err := fixture.app.store.db.Exec(`CREATE TRIGGER reject_batch_receipt BEFORE INSERT ON upstream_batch_items BEGIN SELECT RAISE(FAIL, '` + databaseMarker + `'); END`); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{"operation_id": "770e8400-e29b-41d4-a716-446655440000", "items": []map[string]any{{
		"item_id": "retry", "name": "Retry", "provider_kind": geminiAPIKeyProvider, "api_key": "sqlite-retry-secret",
	}}}
	failed := fixture.request(body)
	failedBody := readBody(failed)
	if failed.StatusCode != http.StatusOK || !strings.Contains(failedBody, `"error_code":"storage_unavailable"`) {
		t.Fatalf("failed status=%d body=%s", failed.StatusCode, failedBody)
	}
	if strings.Contains(failedBody, databaseMarker) || strings.Contains(failedBody, "sqlite-retry-secret") {
		t.Fatalf("database or credential detail leaked: %s", failedBody)
	}
	assertBatchCounts(t, fixture.app, 0, 0)

	if _, err := fixture.app.store.db.Exec(`DROP TRIGGER reject_batch_receipt`); err != nil {
		t.Fatal(err)
	}
	retried := fixture.request(body)
	if retried.StatusCode != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", retried.StatusCode, readBody(retried))
	}
	var result upstreamBatchEnvelope
	decodeResponse(t, retried, &result)
	if len(result.Items) != 1 || result.Items[0].Status != "created" || result.Items[0].UpstreamID == "" {
		t.Fatalf("retry result=%+v", result.Items)
	}
	assertBatchCounts(t, fixture.app, 1, 1)
	if rendered := fmt.Sprint(result.Items[0]); strings.Contains(rendered, result.Items[0].UpstreamID) || strings.Contains(rendered, "sqlite-retry-secret") {
		t.Fatalf("log representation exposed sensitive fields: %s", rendered)
	}
	assertNoBatchPlaintextOnDisk(t, fixture.dataDir, "sqlite-retry-secret")
}

func TestUpstreamBatchMigrationRestartAndConcurrentRetry(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	if err := fixture.app.store.migrateUpstreamBatchItems(context.Background()); err != nil {
		t.Fatalf("idempotent migration: %v", err)
	}
	secondApp, err := Open(context.Background(), Config{DataDir: fixture.dataDir, Listen: "127.0.0.1:0", Version: "test", AllowLoopbackUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondApp.store.migrateUpstreamBatchItems(context.Background()); err != nil {
		secondApp.Close()
		t.Fatal(err)
	}
	secondMux := http.NewServeMux()
	secondMux.HandleFunc("POST /admin/api/v1/sessions", secondApp.login)
	secondMux.HandleFunc("POST /admin/api/v1/upstreams/batch-import", secondApp.requireAdmin(secondApp.batchImportUpstreams, true))
	secondServer := httptest.NewServer(requestMiddleware(secondMux))
	secondCookie, secondCSRF := loginTestAdmin(t, secondServer.URL)
	defer func() {
		if secondServer != nil {
			secondServer.Close()
		}
		if secondApp != nil {
			_ = secondApp.Close()
		}
	}()

	body := map[string]any{"operation_id": "850e8400-e29b-41d4-a716-446655440000", "items": []map[string]any{
		{"item_id": "concurrent", "name": "Concurrent", "provider_kind": geminiAPIKeyProvider, "api_key": "concurrent-secret"},
	}}
	encoded, _ := json.Marshal(body)
	var wg sync.WaitGroup
	statuses := make(chan string, 2)
	targets := []struct {
		baseURL string
		cookie  *http.Cookie
		csrf    string
	}{
		{baseURL: fixture.server.URL, cookie: fixture.cookie, csrf: fixture.csrf},
		{baseURL: secondServer.URL, cookie: secondCookie, csrf: secondCSRF},
	}
	for _, target := range targets {
		target := target
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := requestJSON(t, http.MethodPost, target.baseURL+"/admin/api/v1/upstreams/batch-import", string(encoded), target.cookie, target.csrf, target.baseURL)
			if response.StatusCode != http.StatusOK {
				statuses <- fmt.Sprintf("http-%d", response.StatusCode)
				response.Body.Close()
				return
			}
			var result upstreamBatchEnvelope
			decodeResponse(t, response, &result)
			statuses <- result.Items[0].Status
		}()
	}
	wg.Wait()
	close(statuses)
	seen := map[string]int{}
	for status := range statuses {
		seen[status]++
	}
	if seen["created"] != 1 || seen["existing"] != 1 {
		t.Fatalf("concurrent statuses=%v", seen)
	}
	assertBatchCounts(t, fixture.app, 1, 1)
	secondServer.Close()
	secondServer = nil
	if err := secondApp.Close(); err != nil {
		t.Fatal(err)
	}
	secondApp = nil

	fixture.server.Close()
	fixture.server = nil
	if err := fixture.app.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.app = nil
	reopened, err := Open(context.Background(), Config{DataDir: fixture.dataDir, Listen: "127.0.0.1:0", Version: "test", AllowLoopbackUpstream: true})
	if err != nil {
		t.Fatal(err)
	}
	fixture.app = reopened
	if err := fixture.app.store.migrateUpstreamBatchItems(context.Background()); err != nil {
		t.Fatal(err)
	}
	fixture.startServer()
	retry := fixture.request(body)
	var result upstreamBatchEnvelope
	decodeResponse(t, retry, &result)
	if result.Items[0].Status != "existing" {
		t.Fatalf("restart result=%+v", result.Items)
	}
	assertBatchCounts(t, fixture.app, 1, 1)
}

func TestUpstreamBatchMigrationRejectsIncompatibleSchemaWithoutReplacement(t *testing.T) {
	fixture := newUpstreamBatchFixture(t, false)
	if _, err := fixture.app.store.db.Exec(`DROP TABLE upstream_batch_items`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`CREATE TABLE upstream_batch_items (operation_id TEXT PRIMARY KEY, marker TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := fixture.app.store.db.Exec(`INSERT INTO upstream_batch_items(operation_id,marker) VALUES('preserve','migration-marker')`); err != nil {
		t.Fatal(err)
	}
	if err := fixture.app.store.migrateUpstreamBatchItems(context.Background()); err == nil {
		t.Fatal("expected incompatible migration error")
	}
	var marker string
	if err := fixture.app.store.db.QueryRow(`SELECT marker FROM upstream_batch_items WHERE operation_id='preserve'`).Scan(&marker); err != nil || marker != "migration-marker" {
		t.Fatalf("schema/data changed marker=%q err=%v", marker, err)
	}
}

func syntheticBatchCodexAuth(t *testing.T, accessMarker, refreshMarker, accountMarker string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{"exp": time.Now().Add(time.Hour).Unix(), "marker": accessMarker})
	if err != nil {
		t.Fatal(err)
	}
	accessToken := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic"
	encoded, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens":    map[string]string{"access_token": accessToken, "refresh_token": refreshMarker, "account_id": accountMarker},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func assertBatchStoredCredential(t *testing.T, app *App, id, provider string, keyVersion int, marker string) {
	t.Helper()
	var ciphertext []byte
	var actualProvider string
	var actualVersion int
	if err := app.store.db.QueryRow(`SELECT provider_kind,key_version,credential_ciphertext FROM upstreams WHERE id=?`, id).Scan(&actualProvider, &actualVersion, &ciphertext); err != nil {
		t.Fatal(err)
	}
	if actualProvider != provider || actualVersion != keyVersion || bytes.Contains(ciphertext, []byte(marker)) {
		t.Fatalf("provider=%q version=%d ciphertext contains plaintext=%v", actualProvider, actualVersion, bytes.Contains(ciphertext, []byte(marker)))
	}
	var plaintext []byte
	var err error
	switch provider {
	case geminiAPIKeyProvider:
		var value string
		value, err = app.secrets.decryptGeminiAPIKey(id, ciphertext)
		plaintext = []byte(value)
	case codexMembershipProvider:
		plaintext, err = app.secrets.decryptCodexAuth(id, ciphertext)
	default:
		var value string
		value, err = app.secrets.decryptCredential(id, ciphertext)
		plaintext = []byte(value)
	}
	defer clear(plaintext)
	if err != nil || !bytes.Contains(plaintext, []byte(marker)) {
		t.Fatalf("decrypt provider=%q marker present=%v err=%v", provider, bytes.Contains(plaintext, []byte(marker)), err)
	}
}

func assertBatchCounts(t *testing.T, app *App, upstreams, records int) {
	t.Helper()
	var upstreamCount, recordCount int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM upstreams`).Scan(&upstreamCount); err != nil {
		t.Fatal(err)
	}
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM upstream_batch_items`).Scan(&recordCount); err != nil {
		t.Fatal(err)
	}
	if upstreamCount != upstreams || recordCount != records {
		t.Fatalf("upstreams=%d records=%d want=%d/%d", upstreamCount, recordCount, upstreams, records)
	}
}

func assertNoBatchPlaintextOnDisk(t *testing.T, dataDir string, markers ...string) {
	t.Helper()
	if err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range markers {
			if bytes.Contains(content, []byte(marker)) {
				return fmt.Errorf("plaintext marker %q found in %s", marker, filepath.Base(path))
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpstreamBatchMigrationUsesSQLiteTransaction(t *testing.T) {
	// Compile-time coverage that the migration operates on the real SQLite
	// store rather than an in-memory fake used only by the handler tests.
	fixture := newUpstreamBatchFixture(t, false)
	var foreignKeys int
	if err := fixture.app.store.db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys=%d", foreignKeys)
	}
	var _ *sql.DB = fixture.app.store.db
}
