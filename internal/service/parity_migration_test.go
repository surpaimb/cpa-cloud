package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	parityMigrationPassword      = "a-strong-preview-password"
	parityMigrationEmployeeKey   = "cpac_parity-selector.parity-secret"
	parityMigrationBatchOp       = "6a2204b4-ad30-4f72-8617-66da99d1668c"
	parityMigrationOAuthClientID = "synthetic-parity-client"
)

// TestParityMigrationBase07357acFailureRetryRestart exercises the migration
// boundary as a whole. The fixture is explicitly reduced to the schema present
// at 07357ac after Initialize has created an isolated key and administrator.
// Individual migrations commit independently, so the failure assertions cover
// preservation of old data rather than claiming whole-initialize atomicity.
func TestParityMigrationBase07357acFailureRetryRestart(t *testing.T) {
	dataDir := t.TempDir()
	if err := Initialize(context.Background(), dataDir, strings.NewReader(parityMigrationPassword+"\n")); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("request path=%q", r.URL.Path)
		}
		if authorization := r.Header.Get("Authorization"); authorization != "Bearer parity-api-secret" && authorization != "Bearer parity-batch-secret" {
			t.Errorf("unexpected synthetic authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"parity-result","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"migration preserved"},"finish_reason":"stop"}]}`))
	}))
	defer upstream.Close()

	apiCiphertext, err := secretStore.encryptCredential("ups_parity_api", "parity-api-secret")
	if err != nil {
		t.Fatal(err)
	}
	codexPlaintext := []byte(codexAdminAuthJSON(t, time.Now().Add(2*time.Hour), "acct-parity", "parity-codex-refresh"))
	codexCiphertext, err := secretStore.encryptCodexAuth("ups_parity_codex", codexPlaintext)
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := secretStore.digest("employee-key/v1\x00parity-selector", "parity-secret")

	dbPath := filepath.Join(dataDir, "cpa-cloud.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if err := reduceParityFixtureToBase07357ac(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := seedParityBase07357ac(db, upstream.URL+"/v1", apiCiphertext, codexCiphertext, keyDigest); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE upstream_batch_items(operation_id TEXT PRIMARY KEY, marker TEXT NOT NULL)`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO upstream_batch_items(operation_id,marker) VALUES('preserve','incompatible-parity-marker')`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	if failed, err := openStore(dataDir); err == nil {
		_ = failed.close()
		t.Fatal("migration accepted incompatible batch receipt table")
	}
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	assertParityMigrationCoreState(t, db, apiCiphertext, codexCiphertext, keyDigest)
	var marker string
	if err := db.QueryRow(`SELECT marker FROM upstream_batch_items WHERE operation_id='preserve'`).Scan(&marker); err != nil || marker != "incompatible-parity-marker" {
		db.Close()
		t.Fatalf("incompatible table changed marker=%q err=%v", marker, err)
	}
	// Upstream and OAuth lifecycle migrations run before the rejected batch
	// migration and may already be committed. Verify their safe, retryable state.
	assertParityOAuthLifecycle(t, db)
	if _, err := db.Exec(`DROP TABLE upstream_batch_items`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	app, server := openParityMigrationApp(t, dataDir)
	cookie, csrf := loginTestAdmin(t, server.URL)
	batchBody := map[string]any{
		"operation_id": parityMigrationBatchOp,
		"items": []map[string]any{{
			"item_id": "parity-api", "name": "Parity batch API", "provider_kind": "openai-compatible",
			"endpoint": upstream.URL + "/v1", "api_key": "parity-batch-secret",
		}},
	}
	batchUpstreamID := assertParityBatchStatus(t, server.URL, cookie, csrf, batchBody, "created", "")
	if replayID := assertParityBatchStatus(t, server.URL, cookie, csrf, batchBody, "existing", batchUpstreamID); replayID != batchUpstreamID {
		t.Fatalf("batch replay upstream=%q want=%q", replayID, batchUpstreamID)
	}

	poolResponse := requestJSON(t, http.MethodPut, server.URL+"/admin/api/v1/models/parity-model/accounts", `{
		"expected_revision":0,
		"items":[
			{"upstream_id":"ups_parity_api","upstream_model":"provider-model","priority":10,"weight":2,"max_concurrency":3},
			{"upstream_id":`+quoteJSON(batchUpstreamID)+`,"upstream_model":"batch-provider-model","priority":5,"weight":1,"max_concurrency":2}
		]
	}`, cookie, csrf, server.URL)
	if poolResponse.StatusCode != http.StatusOK {
		t.Fatalf("configure pool status=%d body=%s", poolResponse.StatusCode, readBody(poolResponse))
	}
	poolResponse.Body.Close()
	assertParityPostUpgradeState(t, app.store.db, batchUpstreamID, apiCiphertext, codexCiphertext, keyDigest)

	server.Close()
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}

	app, server = openParityMigrationApp(t, dataDir)
	defer server.Close()
	defer app.Close()
	assertParityPostUpgradeState(t, app.store.db, batchUpstreamID, apiCiphertext, codexCiphertext, keyDigest)
	cookie, csrf = loginTestAdmin(t, server.URL)
	assertParityBatchStatus(t, server.URL, cookie, csrf, batchBody, "existing", batchUpstreamID)

	response := employeeRequest(t, http.MethodPost, server.URL+"/v1/chat/completions",
		`{"model":"parity-model","messages":[{"role":"user","content":"synthetic restart check"}]}`,
		parityMigrationEmployeeKey, context.Background())
	body := readBody(response)
	if response.StatusCode != http.StatusOK || !strings.Contains(body, `"content":"migration preserved"`) {
		t.Fatalf("restart request status=%d body=%s", response.StatusCode, body)
	}
}

func reduceParityFixtureToBase07357ac(db *sql.DB) error {
	if _, err := db.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	statements := []string{
		`DROP TABLE IF EXISTS accounting_attempts`,
		`DROP TABLE IF EXISTS accounting_requests`,
		`DROP TABLE account_pool_runtime_leases`,
		`DROP TABLE account_pool_runtime_cooldowns`,
		`DROP TABLE account_pool_audit`,
		`DROP TABLE model_account_pool_routes`,
		`DROP TABLE model_account_pool_configs`,
		`DROP TABLE account_channels`,
		`DROP TABLE account_groups`,
		`DROP TABLE upstream_batch_items`,
		`DROP TABLE codex_oauth_refresh_states`,
		`CREATE TABLE parity_base_oauth_sessions (
			id TEXT PRIMARY KEY,
			operation_id TEXT NOT NULL UNIQUE,
			admin_id TEXT NOT NULL REFERENCES admins(id) ON DELETE CASCADE,
			admin_session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
			state_digest BLOB NOT NULL UNIQUE,
			secret_ciphertext BLOB NOT NULL,
			name TEXT NOT NULL,
			expires_at TEXT NOT NULL,
			used_at TEXT,
			created_at TEXT NOT NULL
		)`,
		`INSERT INTO parity_base_oauth_sessions(id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,used_at,created_at)
			SELECT id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,used_at,created_at FROM codex_oauth_sessions`,
		`DROP TABLE codex_oauth_sessions`,
		`ALTER TABLE parity_base_oauth_sessions RENAME TO codex_oauth_sessions`,
		`CREATE INDEX codex_oauth_sessions_expiry_idx ON codex_oauth_sessions(expires_at)`,
		`CREATE TABLE parity_base_upstreams (
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
			CHECK(
				(provider_kind = 'openai-compatible' AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL)
				OR
				(provider_kind = 'codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL)
			)
		)`,
		`INSERT INTO parity_base_upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id)
			SELECT id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id FROM upstreams`,
		`DROP TABLE upstreams`,
		`ALTER TABLE parity_base_upstreams RENAME TO upstreams`,
	}
	for _, statement := range statements {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys=ON`); err != nil {
		return err
	}
	var enabled int
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&enabled); err != nil {
		return err
	}
	if enabled != 1 {
		return sql.ErrTxDone
	}
	return nil
}

func seedParityBase07357ac(db *sql.DB, endpoint string, apiCiphertext, codexCiphertext, keyDigest []byte) error {
	var adminID string
	if err := db.QueryRow(`SELECT id FROM admins WHERE username='admin'`).Scan(&adminID); err != nil {
		return err
	}
	statements := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO sessions(id,admin_id,token_digest,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?,?)`, []any{"ses_parity", adminID, []byte{1, 2, 3, 4}, "parity-csrf", "2035-01-01T00:00:00Z", "2026-09-23T00:00:00Z"}},
		{`INSERT INTO codex_oauth_sessions(id,operation_id,admin_id,admin_session_id,state_digest,secret_ciphertext,name,expires_at,used_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"oauth_parity", "oauth-parity-operation", adminID, "ses_parity", []byte{5, 6, 7, 8}, []byte{9, 10, 11}, "Legacy completed OAuth", "2035-01-01T00:00:00Z", "2026-09-23T00:01:00Z", "2026-09-23T00:00:00Z"}},
		{`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"ups_parity_api", "Parity API", "openai-compatible", endpoint, 1, apiCiphertext, 1, 7, "2026-09-23T00:00:00Z", nil, nil, nil}},
		{`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, []any{"ups_parity_codex", "Parity Codex", codexMembershipProvider, codexMembershipEndpoint, 1, codexCiphertext, 2, 3, "2026-09-23T00:00:00Z", codexStateImported, nil, "oauth-parity-upstream-operation"}},
		{`INSERT INTO codex_oauth_bindings(upstream_id,client_id,source,created_at) VALUES(?,?,?,?)`, []any{"ups_parity_codex", parityMigrationOAuthClientID, "authorization_code", "2026-09-23T00:00:00Z"}},
		{`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES(?,?,?,?,?)`, []any{"parity-model", "ups_parity_api", "provider-model", 1, "2026-09-23T00:00:00Z"}},
		{`INSERT INTO employees(id,name,department,note,status,model_mode,revision,created_at) VALUES(?,?,?,?,?,?,?,?)`, []any{"emp_parity", "Parity employee", "Engineering", "migration fixture", "active", "selected", 4, "2026-09-23T00:00:00Z"}},
		{`INSERT INTO employee_models(employee_id,model_id) VALUES(?,?)`, []any{"emp_parity", "parity-model"}},
		{`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,expires_at,revoked_at,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, []any{"key_parity", "emp_parity", "Permanent key", "parity-selector", keyDigest, 1, "key-parity-operation", nil, nil, "2026-09-23T00:00:00Z"}},
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			return err
		}
	}
	return nil
}

func openParityMigrationApp(t *testing.T, dataDir string) (*App, *httptest.Server) {
	t.Helper()
	app, err := Open(context.Background(), Config{
		DataDir: dataDir, Listen: "127.0.0.1:0", AllowLoopbackUpstream: true, Version: "parity-migration-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	return app, httptest.NewServer(app.Handler())
}

func assertParityBatchStatus(t *testing.T, baseURL string, cookie *http.Cookie, csrf string, body map[string]any, wantStatus, wantID string) string {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	response := requestJSON(t, http.MethodPost, baseURL+"/admin/api/v1/upstreams/batch-import", string(encoded), cookie, csrf, baseURL)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("batch status=%d body=%s", response.StatusCode, readBody(response))
	}
	var envelope upstreamBatchEnvelope
	decodeResponse(t, response, &envelope)
	if len(envelope.Items) != 1 || envelope.Items[0].Status != wantStatus || envelope.Items[0].UpstreamID == "" {
		t.Fatalf("batch result=%+v want status=%q", envelope.Items, wantStatus)
	}
	if wantID != "" && envelope.Items[0].UpstreamID != wantID {
		t.Fatalf("batch upstream=%q want=%q", envelope.Items[0].UpstreamID, wantID)
	}
	return envelope.Items[0].UpstreamID
}

func assertParityMigrationCoreState(t *testing.T, db *sql.DB, apiCiphertext, codexCiphertext, keyDigest []byte) {
	t.Helper()
	var gotAPI, gotCodex, gotDigest []byte
	if err := db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id='ups_parity_api' AND revision=7`).Scan(&gotAPI); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id='ups_parity_codex' AND revision=3`).Scan(&gotCodex); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT digest FROM access_keys WHERE id='key_parity' AND revoked_at IS NULL AND expires_at IS NULL`).Scan(&gotDigest); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotAPI, apiCiphertext) || !bytes.Equal(gotCodex, codexCiphertext) || !bytes.Equal(gotDigest, keyDigest) {
		t.Fatal("migration changed an encrypted credential or permanent key digest")
	}
	var bindingCount, policyCount int
	if err := db.QueryRow(`SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id='ups_parity_codex' AND client_id=? AND source='authorization_code'`, parityMigrationOAuthClientID).Scan(&bindingCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM employees e JOIN employee_models em ON em.employee_id=e.id WHERE e.id='emp_parity' AND e.model_mode='selected' AND e.status='active' AND em.model_id='parity-model'`).Scan(&policyCount); err != nil {
		t.Fatal(err)
	}
	if bindingCount != 1 || policyCount != 1 {
		t.Fatalf("binding=%d policy=%d", bindingCount, policyCount)
	}
}

func assertParityOAuthLifecycle(t *testing.T, db *sql.DB) {
	t.Helper()
	var state, status, errorCode string
	var attempt int64
	if err := db.QueryRow(`SELECT state,attempt_revision FROM codex_oauth_refresh_states WHERE upstream_id='ups_parity_codex'`).Scan(&state, &attempt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT status,error_code FROM codex_oauth_sessions WHERE id='oauth_parity'`).Scan(&status, &errorCode); err != nil {
		t.Fatal(err)
	}
	if state != "ready" || attempt != 3 || status != "failed" || errorCode != "legacy_unknown_outcome" {
		t.Fatalf("OAuth lifecycle state=%q attempt=%d session=%q error=%q", state, attempt, status, errorCode)
	}
}

func assertParityPostUpgradeState(t *testing.T, db *sql.DB, batchUpstreamID string, apiCiphertext, codexCiphertext, keyDigest []byte) {
	t.Helper()
	assertParityMigrationCoreState(t, db, apiCiphertext, codexCiphertext, keyDigest)
	assertParityOAuthLifecycle(t, db)
	var receipts, poolRevision, routes int
	if err := db.QueryRow(`SELECT COUNT(*) FROM upstream_batch_items WHERE operation_id=? AND item_id='parity-api' AND upstream_id=?`, parityMigrationBatchOp, batchUpstreamID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT revision FROM model_account_pool_configs WHERE model_id='parity-model'`).Scan(&poolRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM model_account_pool_routes WHERE model_id='parity-model' AND upstream_id IN ('ups_parity_api',?)`, batchUpstreamID).Scan(&routes); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || poolRevision != 1 || routes != 2 {
		t.Fatalf("receipt=%d pool_revision=%d routes=%d", receipts, poolRevision, routes)
	}
}
