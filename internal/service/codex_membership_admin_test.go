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
	"testing"
	"time"
)

func TestCodexMembershipAdminImportReplacementAndFeatureFlag(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader("a-strong-preview-password\n")); err != nil {
		t.Fatal(err)
	}

	disabledApp := openCodexAdminTestApp(t, dataDir, false)
	disabledServer := httptest.NewServer(disabledApp.Handler())
	cookie, csrf := loginTestAdmin(t, disabledServer.URL)
	assertCodexFeatureStatus(t, disabledServer.URL, cookie, false)

	authOne := codexAdminAuthJSON(t, time.Now().Add(2*time.Hour), "acct-admin-one", "old-refresh-secret-marker")
	operationID := "52d2f8d4-bef4-4d51-b673-4d2405fd64df"
	disabledImport := codexAdminRequest(t, http.MethodPost, disabledServer.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
		"name": "disabled", "auth_json": authOne, "operation_id": operationID,
	}, cookie, csrf, disabledServer.URL)
	assertCodexAdminError(t, disabledImport, http.StatusForbidden, "feature_disabled")
	disabledReplace := codexAdminRequest(t, http.MethodPut, disabledServer.URL+"/admin/api/v1/upstreams/missing/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": authOne,
	}, cookie, csrf, disabledServer.URL)
	assertCodexAdminError(t, disabledReplace, http.StatusForbidden, "feature_disabled")
	disabledServer.Close()
	if err := disabledApp.Close(); err != nil {
		t.Fatal(err)
	}

	app := openCodexAdminTestApp(t, dataDir, true)
	server := httptest.NewServer(app.Handler())
	cookie, csrf = loginTestAdmin(t, server.URL)
	assertCodexFeatureStatus(t, server.URL, cookie, true)

	invalidFixtures := []struct {
		name string
		auth string
		op   string
	}{
		{name: "malformed", auth: `{"auth_mode":`, op: "f59c4af1-c528-4646-a877-c031384db5ae"},
		{name: "missing account", auth: codexAdminAuthJSON(t, time.Now().Add(2*time.Hour), "", "missing-account-secret"), op: "9d0f0ccd-777b-4897-813b-07fd69304412"},
		{name: "expired", auth: codexAdminAuthJSON(t, time.Now().Add(-time.Hour), "acct-expired", "expired-secret"), op: "12f401f8-f80f-42f6-92cc-428784932b01"},
	}
	for _, fixture := range invalidFixtures {
		t.Run("rejects "+fixture.name, func(t *testing.T) {
			response := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
				"name": fixture.name, "auth_json": fixture.auth, "operation_id": fixture.op,
			}, cookie, csrf, server.URL)
			assertCodexAdminError(t, response, http.StatusBadRequest, "invalid_codex_auth")
		})
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM upstreams`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("invalid imports left upstreams: count=%d err=%v", count, err)
	}

	createdResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
		"name": "Codex membership", "auth_json": authOne, "operation_id": operationID,
	}, cookie, csrf, server.URL)
	if createdResponse.StatusCode != http.StatusCreated {
		t.Fatalf("import status=%d body=%s", createdResponse.StatusCode, readBody(createdResponse))
	}
	var created upstreamView
	decodeResponse(t, createdResponse, &created)
	assertImportedCodexUpstream(t, created, 1)

	var firstCiphertext []byte
	var firstKeyVersion int
	var storedOperation, storedState string
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,key_version,operation_id,credential_state FROM upstreams WHERE id=?`, created.ID).
		Scan(&firstCiphertext, &firstKeyVersion, &storedOperation, &storedState); err != nil {
		t.Fatal(err)
	}
	if firstKeyVersion != 2 || storedOperation != operationID || storedState != codexStateImported {
		t.Fatalf("stored metadata version=%d operation=%q state=%q", firstKeyVersion, storedOperation, storedState)
	}
	if bytes.Contains(firstCiphertext, []byte("old-refresh-secret-marker")) || bytes.Contains(firstCiphertext, []byte("acct-admin-one")) {
		t.Fatal("credential ciphertext contains plaintext credential material")
	}
	decrypted, err := app.secrets.decryptCodexAuth(created.ID, firstCiphertext)
	if err != nil || string(decrypted) != authOne {
		t.Fatalf("decrypt imported auth: equal=%v err=%v", string(decrypted) == authOne, err)
	}
	for i := range decrypted {
		decrypted[i] = 0
	}

	// Idempotency is keyed before parsing: a retry cannot replace the stored
	// name or credential and does not need to re-submit valid credential bytes.
	retryResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/codex-import", map[string]any{
		"name": "ignored retry", "auth_json": "not-json", "operation_id": operationID,
	}, cookie, csrf, server.URL)
	if retryResponse.StatusCode != http.StatusOK {
		t.Fatalf("idempotent retry status=%d body=%s", retryResponse.StatusCode, readBody(retryResponse))
	}
	var retry upstreamView
	decodeResponse(t, retryResponse, &retry)
	if retry.ID != created.ID || retry.Name != created.Name || retry.Revision != created.Revision {
		t.Fatalf("idempotent retry changed object: created=%+v retry=%+v", created, retry)
	}
	var retryCiphertext []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, created.ID).Scan(&retryCiphertext); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstCiphertext, retryCiphertext) {
		t.Fatal("idempotent retry replaced credential ciphertext")
	}

	invalidReplacement := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+created.ID+"/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": `{"tokens":{}}`,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, invalidReplacement, http.StatusBadRequest, "invalid_codex_auth")
	assertStoredCodexCredential(t, app, created.ID, 1, firstCiphertext, codexStateImported, false)

	verifiedAt := utcNow()
	if _, err := app.store.db.Exec(`UPDATE upstreams SET credential_state=?,verified_at=? WHERE id=?`, codexStateVerified, verifiedAt, created.ID); err != nil {
		t.Fatal(err)
	}
	authTwo := codexAdminAuthJSON(t, time.Now().Add(3*time.Hour), "acct-admin-two", "new-refresh-secret-marker")
	staleReplacement := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+created.ID+"/codex-auth", map[string]any{
		"expected_revision": 99, "auth_json": authTwo,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, staleReplacement, http.StatusConflict, "revision_conflict")
	assertStoredCodexCredential(t, app, created.ID, 1, firstCiphertext, codexStateVerified, true)

	replacedResponse := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+created.ID+"/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": authTwo,
	}, cookie, csrf, server.URL)
	if replacedResponse.StatusCode != http.StatusOK {
		t.Fatalf("replace status=%d body=%s", replacedResponse.StatusCode, readBody(replacedResponse))
	}
	var replaced upstreamView
	decodeResponse(t, replacedResponse, &replaced)
	assertImportedCodexUpstream(t, replaced, 2)
	var secondCiphertext []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, created.ID).Scan(&secondCiphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(firstCiphertext, secondCiphertext) {
		t.Fatal("successful replacement did not replace ciphertext")
	}
	replacementPlaintext, err := app.secrets.decryptCodexAuth(created.ID, secondCiphertext)
	if err != nil || string(replacementPlaintext) != authTwo {
		t.Fatalf("decrypt replacement: equal=%v err=%v", string(replacementPlaintext) == authTwo, err)
	}
	for i := range replacementPlaintext {
		replacementPlaintext[i] = 0
	}

	apiKeyPatch := codexAdminRequest(t, http.MethodPatch, server.URL+"/admin/api/v1/upstreams/"+created.ID, map[string]any{
		"expected_revision": 2, "api_key": "must-not-replace-membership-auth",
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, apiKeyPatch, http.StatusBadRequest, "unsupported_feature")
	assertStoredCodexCredential(t, app, created.ID, 2, secondCiphertext, codexStateImported, false)

	disableResponse := codexAdminRequest(t, http.MethodPatch, server.URL+"/admin/api/v1/upstreams/"+created.ID, map[string]any{
		"expected_revision": 2, "enabled": false,
	}, cookie, csrf, server.URL)
	if disableResponse.StatusCode != http.StatusOK {
		t.Fatalf("disable membership upstream status=%d body=%s", disableResponse.StatusCode, readBody(disableResponse))
	}
	var disabled upstreamView
	decodeResponse(t, disableResponse, &disabled)
	if disabled.Enabled || disabled.Revision != 3 {
		t.Fatalf("disabled upstream=%+v", disabled)
	}

	discovery := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams/"+created.ID+"/discover-models", map[string]any{}, cookie, csrf, server.URL)
	assertCodexAdminError(t, discovery, http.StatusConflict, "upstream_disabled")

	apiUpstreamResponse := codexAdminRequest(t, http.MethodPost, server.URL+"/admin/api/v1/upstreams", map[string]any{
		"name": "API key upstream", "provider_kind": "openai-compatible", "endpoint": "http://127.0.0.1:1/v1", "api_key": "api-upstream-secret",
	}, cookie, csrf, server.URL)
	if apiUpstreamResponse.StatusCode != http.StatusCreated {
		t.Fatalf("create API-key upstream status=%d body=%s", apiUpstreamResponse.StatusCode, readBody(apiUpstreamResponse))
	}
	var apiUpstream upstreamView
	decodeResponse(t, apiUpstreamResponse, &apiUpstream)
	var apiCiphertext []byte
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id=?`, apiUpstream.ID).Scan(&apiCiphertext); err != nil {
		t.Fatal(err)
	}
	wrongTypeReplacement := codexAdminRequest(t, http.MethodPut, server.URL+"/admin/api/v1/upstreams/"+apiUpstream.ID+"/codex-auth", map[string]any{
		"expected_revision": 1, "auth_json": authTwo,
	}, cookie, csrf, server.URL)
	assertCodexAdminError(t, wrongTypeReplacement, http.StatusBadRequest, "invalid_upstream_type")
	var apiRevision int64
	var apiCiphertextAfter []byte
	if err := app.store.db.QueryRow(`SELECT revision,credential_ciphertext FROM upstreams WHERE id=?`, apiUpstream.ID).Scan(&apiRevision, &apiCiphertextAfter); err != nil {
		t.Fatal(err)
	}
	if apiRevision != 1 || !bytes.Equal(apiCiphertext, apiCiphertextAfter) {
		t.Fatal("wrong-type Codex replacement mutated API-key upstream")
	}

	server.Close()
	if _, err := app.store.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	assertNoCredentialMarkersOnDisk(t, dataDir, "old-refresh-secret-marker", "new-refresh-secret-marker", "acct-admin-one", "acct-admin-two", "must-not-replace-membership-auth")

	// Existing rows remain visible when the experiment is subsequently off.
	reopened := openCodexAdminTestApp(t, dataDir, false)
	defer reopened.Close()
	reopenedServer := httptest.NewServer(reopened.Handler())
	defer reopenedServer.Close()
	reopenedCookie, _ := loginTestAdmin(t, reopenedServer.URL)
	list := requestJSON(t, http.MethodGet, reopenedServer.URL+"/admin/api/v1/upstreams", "", reopenedCookie, "", "")
	if list.StatusCode != http.StatusOK {
		t.Fatalf("list with flag off status=%d body=%s", list.StatusCode, readBody(list))
	}
	var envelope struct {
		Items []upstreamView `json:"items"`
	}
	decodeResponse(t, list, &envelope)
	var listedMembership *upstreamView
	for i := range envelope.Items {
		if envelope.Items[i].ID == created.ID {
			listedMembership = &envelope.Items[i]
		}
	}
	if listedMembership == nil || listedMembership.CredentialState == nil {
		t.Fatalf("list with flag off=%+v", envelope.Items)
	}
}

func TestCodexMembershipLegacyUpstreamMigrationRollbackRetryAndReopen(t *testing.T) {
	legacyUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer legacy-api-key-secret" {
			t.Errorf("migrated API-key route path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"legacy-result","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"preserved"},"finish_reason":"stop"}]}`))
	}))
	defer legacyUpstream.Close()
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	legacyCiphertext, err := secretStore.encryptCredential("ups_legacy", "legacy-api-key-secret")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dataDir, "cpa-cloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyStatements := []string{
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE upstreams (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			provider_kind TEXT NOT NULL CHECK(provider_kind = 'openai-compatible'),
			endpoint TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			credential_ciphertext BLOB NOT NULL,
			key_version INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE models (
			id TEXT PRIMARY KEY,
			upstream_id TEXT NOT NULL REFERENCES upstreams(id),
			upstream_model TEXT NOT NULL,
			enabled INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE employees (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			department TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL CHECK(status IN ('active','disabled')),
			model_mode TEXT NOT NULL CHECK(model_mode IN ('all','selected')),
			revision INTEGER NOT NULL,
			created_at TEXT NOT NULL
		)`,
		`CREATE TABLE access_keys (
			id TEXT PRIMARY KEY,
			employee_id TEXT NOT NULL REFERENCES employees(id) ON DELETE CASCADE,
			name TEXT NOT NULL,
			selector TEXT NOT NULL UNIQUE,
			digest BLOB NOT NULL,
			digest_version INTEGER NOT NULL,
			operation_id TEXT NOT NULL,
			expires_at TEXT,
			revoked_at TEXT,
			created_at TEXT NOT NULL,
			UNIQUE(employee_id, operation_id)
		)`,
		`CREATE TABLE model_requests (
			id TEXT PRIMARY KEY,
			employee_id TEXT NOT NULL REFERENCES employees(id),
			key_id TEXT NOT NULL REFERENCES access_keys(id),
			model_id TEXT NOT NULL,
			started_at TEXT NOT NULL,
			finished_at TEXT,
			outcome TEXT NOT NULL CHECK(outcome IN ('running','succeeded','failed','cancelled','interrupted')),
			upstream_status INTEGER
		)`,
		`CREATE TABLE ` + upstreamMigrationTable + ` (blocking INTEGER)`,
	}
	for _, statement := range legacyStatements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("create legacy schema: %v", err)
		}
	}
	if _, err := db.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES(?,?,?,?,?,?,?,?,?)`,
		"ups_legacy", "Legacy", "openai-compatible", legacyUpstream.URL+"/v1", 1, legacyCiphertext, 1, 7, "2026-09-22T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,?,?,?,?)`,
		"legacy-model", "ups_legacy", "provider-model", 1, "2026-09-22T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,?,?,?,?,?,?)`,
		"emp_legacy", "Legacy employee", "Engineering", "preserve", "active", "all", 4, "2026-09-22T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,expires_at,revoked_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		"key_legacy", "emp_legacy", "Legacy key", "legacy-selector", secretStore.digest("employee-key/v1\x00legacy-selector", "legacy-secret"), 1, "legacy-operation", nil, nil, "2026-09-22T00:00:00Z"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO model_requests(id,employee_id,key_id,model_id,started_at,finished_at,outcome,upstream_status) VALUES(?,?,?,?,?,?,?,?)`,
		"req_legacy", "emp_legacy", "key_legacy", "legacy-model", "2026-09-22T00:00:00Z", "2026-09-22T00:00:01Z", "succeeded", 200); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if failedStore, err := openStore(dataDir); err == nil {
		failedStore.close()
		t.Fatal("migration unexpectedly succeeded with a conflicting migration table")
	}
	assertLegacyMigrationState(t, dbPath, false)

	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE ` + upstreamMigrationTable); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("retry migration: %v", err)
	}
	assertMigratedLegacyData(t, migrated, secretStore)
	assertMigratedLegacyRoute(t, migrated, secretStore)
	if err := migrated.close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openStore(dataDir)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer reopened.close()
	assertMigratedLegacyData(t, reopened, secretStore)
	assertMigratedLegacyRoute(t, reopened, secretStore)
}

func TestCodexMembershipCredentialAEADBinding(t *testing.T) {
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte(`{"auth_mode":"chatgpt","marker":"membership-aead-secret"}`)
	ciphertext, err := secretStore.encryptCodexAuth("ups_bound", plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("membership-aead-secret")) {
		t.Fatal("Codex ciphertext contains its plaintext marker")
	}
	decrypted, err := secretStore.decryptCodexAuth("ups_bound", ciphertext)
	if err != nil || !bytes.Equal(decrypted, plaintext) {
		t.Fatalf("Codex credential round trip equal=%v err=%v", bytes.Equal(decrypted, plaintext), err)
	}
	clear(decrypted)
	if _, err := secretStore.decryptCodexAuth("ups_other", ciphertext); err == nil {
		t.Fatal("Codex credential decrypted under a different upstream ID")
	}
	if _, err := secretStore.decryptCredential("ups_bound", ciphertext); err == nil {
		t.Fatal("Codex credential decrypted under the API-key purpose")
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err := secretStore.decryptCodexAuth("ups_bound", tampered); err == nil {
		t.Fatal("tampered Codex credential decrypted")
	}
}

func openCodexAdminTestApp(t *testing.T, dataDir string, enabled bool) *App {
	t.Helper()
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true,
		ExperimentalCodexMembership: enabled, Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func codexAdminAuthJSON(t *testing.T, expiresAt time.Time, accountID, refreshSecret string) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`))
	payload, err := json.Marshal(map[string]any{"exp": expiresAt.Unix(), "marker": "codex-admin-access-marker"})
	if err != nil {
		t.Fatal(err)
	}
	accessToken := header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".synthetic-signature"
	auth, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token": accessToken, "refresh_token": refreshSecret, "account_id": accountID,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(auth)
}

func codexAdminRequest(t *testing.T, method, target string, body any, cookie *http.Cookie, csrf, origin string) *http.Response {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return requestJSON(t, method, target, string(encoded), cookie, csrf, origin)
}

func assertCodexFeatureStatus(t *testing.T, baseURL string, cookie *http.Cookie, expected bool) {
	t.Helper()
	response := requestJSON(t, http.MethodGet, baseURL+"/admin/api/v1/system/status", "", cookie, "", "")
	if response.StatusCode != http.StatusOK {
		t.Fatalf("system status=%d body=%s", response.StatusCode, readBody(response))
	}
	var status struct {
		Features map[string]bool `json:"features"`
	}
	decodeResponse(t, response, &status)
	value, exists := status.Features["codex_membership_import"]
	if !exists || value != expected {
		t.Fatalf("codex_membership_import exists=%v value=%v want=%v", exists, value, expected)
	}
}

func assertCodexAdminError(t *testing.T, response *http.Response, expectedStatus int, expectedCode string) {
	t.Helper()
	if response.StatusCode != expectedStatus {
		t.Fatalf("error status=%d want=%d body=%s", response.StatusCode, expectedStatus, readBody(response))
	}
	var envelope struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeResponse(t, response, &envelope)
	if envelope.Error.Code != expectedCode || envelope.Error.Message == "" {
		t.Fatalf("error=%+v want code=%q", envelope.Error, expectedCode)
	}
}

func assertImportedCodexUpstream(t *testing.T, item upstreamView, expectedRevision int64) {
	t.Helper()
	if item.ID == "" || item.ProviderKind != codexMembershipProvider || item.Endpoint != codexMembershipEndpoint || !item.Enabled || item.Revision != expectedRevision {
		t.Fatalf("unexpected Codex upstream=%+v", item)
	}
	if item.CredentialState == nil || *item.CredentialState != codexStateImported || item.VerifiedAt != nil {
		t.Fatalf("unexpected credential status=%+v", item)
	}
}

func assertStoredCodexCredential(t *testing.T, app *App, id string, revision int64, ciphertext []byte, state string, verified bool) {
	t.Helper()
	var actualCiphertext []byte
	var actualRevision int64
	var actualState string
	var verifiedAt sql.NullString
	if err := app.store.db.QueryRow(`SELECT credential_ciphertext,revision,credential_state,verified_at FROM upstreams WHERE id=?`, id).
		Scan(&actualCiphertext, &actualRevision, &actualState, &verifiedAt); err != nil {
		t.Fatal(err)
	}
	if actualRevision != revision || actualState != state || verifiedAt.Valid != verified || !bytes.Equal(actualCiphertext, ciphertext) {
		t.Fatalf("stored credential revision=%d state=%q verified=%v ciphertext_equal=%v", actualRevision, actualState, verifiedAt.Valid, bytes.Equal(actualCiphertext, ciphertext))
	}
}

func assertNoCredentialMarkersOnDisk(t *testing.T, dataDir string, markers ...string) {
	t.Helper()
	err := filepath.WalkDir(dataDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, marker := range markers {
			if bytes.Contains(content, []byte(marker)) {
				return fmt.Errorf("plaintext credential marker %q found in %s", marker, filepath.Base(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func assertLegacyMigrationState(t *testing.T, dbPath string, migrated bool) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var schema string
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='upstreams'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(schema), "codex-membership") != migrated {
		t.Fatalf("migration schema state=%v schema=%s", migrated, schema)
	}
	if strings.Contains(strings.ToLower(schema), anthropicAPIKeyProvider) != migrated {
		t.Fatalf("Anthropic provider migration state=%v schema=%s", migrated, schema)
	}
	var upstreamCount, modelCount, employeeCount, keyCount, requestCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM upstreams WHERE id='ups_legacy' AND revision=7`).Scan(&upstreamCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM models WHERE id='legacy-model' AND upstream_id='ups_legacy'`).Scan(&modelCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM employees WHERE id='emp_legacy' AND revision=4`).Scan(&employeeCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM access_keys WHERE id='key_legacy' AND employee_id='emp_legacy' AND revoked_at IS NULL`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE id='req_legacy' AND key_id='key_legacy' AND outcome='succeeded' AND upstream_status=200`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if upstreamCount != 1 || modelCount != 1 || employeeCount != 1 || keyCount != 1 || requestCount != 1 {
		t.Fatalf("legacy data upstreams=%d models=%d employees=%d keys=%d requests=%d", upstreamCount, modelCount, employeeCount, keyCount, requestCount)
	}
}

func assertMigratedLegacyData(t *testing.T, migrated *store, secretStore *secrets) {
	t.Helper()
	var ciphertext []byte
	var provider string
	var revision int64
	var state, verifiedAt, operationID sql.NullString
	if err := migrated.db.QueryRow(`SELECT provider_kind,credential_ciphertext,revision,credential_state,verified_at,operation_id FROM upstreams WHERE id='ups_legacy'`).
		Scan(&provider, &ciphertext, &revision, &state, &verifiedAt, &operationID); err != nil {
		t.Fatal(err)
	}
	if provider != "openai-compatible" || revision != 7 || state.Valid || verifiedAt.Valid || operationID.Valid {
		t.Fatalf("migrated upstream provider=%q revision=%d state=%v verified=%v operation=%v", provider, revision, state, verifiedAt, operationID)
	}
	plaintext, err := secretStore.decryptCredential("ups_legacy", ciphertext)
	if err != nil || plaintext != "legacy-api-key-secret" {
		t.Fatalf("legacy credential after migration=%q err=%v", plaintext, err)
	}
	var modelCount int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM models WHERE id='legacy-model' AND upstream_id='ups_legacy'`).Scan(&modelCount); err != nil || modelCount != 1 {
		t.Fatalf("migrated model count=%d err=%v", modelCount, err)
	}
	var employeeCount, keyCount, requestCount int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM employees WHERE id='emp_legacy' AND revision=4`).Scan(&employeeCount); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM access_keys WHERE id='key_legacy' AND employee_id='emp_legacy' AND revoked_at IS NULL`).Scan(&keyCount); err != nil {
		t.Fatal(err)
	}
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM model_requests WHERE id='req_legacy' AND key_id='key_legacy' AND outcome='succeeded' AND upstream_status=200`).Scan(&requestCount); err != nil {
		t.Fatal(err)
	}
	if employeeCount != 1 || keyCount != 1 || requestCount != 1 {
		t.Fatalf("migrated related rows employees=%d keys=%d requests=%d", employeeCount, keyCount, requestCount)
	}
	rows, err := migrated.db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	if rows.Next() {
		rows.Close()
		t.Fatal("foreign key violation after migration")
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := migrated.db.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='upstreams'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(schema), "codex-membership") {
		t.Fatalf("migrated schema did not accept Codex membership: %s", schema)
	}
}

func assertMigratedLegacyRoute(t *testing.T, migrated *store, secretStore *secrets) {
	t.Helper()
	app := &App{
		cfg: Config{AllowLoopbackUpstream: true}, store: migrated, secrets: secretStore,
		http: newUpstreamClient(true), codex: newProductionCodexExecutor(), logins: make(map[string]*loginAttempt),
	}
	server := httptest.NewServer(app.Handler())
	defer server.Close()
	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions",
		`{"model":"legacy-model","messages":[{"role":"user","content":"still works"}]}`, "cpac_legacy-selector.legacy-secret", context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"content":"preserved"`) {
		t.Fatalf("migrated API-key route status=%d body=%s", response.StatusCode, body)
	}
}
