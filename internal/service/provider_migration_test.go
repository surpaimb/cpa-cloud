package service

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

// This fixture represents the OAuth-enabled schema before native providers
// were added. Its OAuth binding is a child of the table being rebuilt.
func TestNativeProviderMigrationPreservesOAuthBindingAndRetries(t *testing.T) {
	dir := t.TempDir()
	if err := Initialize(context.Background(), dir, strings.NewReader("synthetic-migration-password\n")); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "cpa-cloud.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	statements := []string{
		`PRAGMA foreign_keys=OFF`,
		`DROP TABLE upstreams`,
		`CREATE TABLE upstreams (
			id TEXT PRIMARY KEY, name TEXT NOT NULL,
			provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','codex-membership')),
			endpoint TEXT NOT NULL, enabled INTEGER NOT NULL,
			credential_ciphertext BLOB NOT NULL, key_version INTEGER NOT NULL,
			revision INTEGER NOT NULL, created_at TEXT NOT NULL,
			credential_state TEXT, verified_at TEXT, operation_id TEXT UNIQUE
		)`,
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at,credential_state,verified_at,operation_id) VALUES('ups_oauth','synthetic','codex-membership','https://chatgpt.com',1,X'01020304',2,7,'2026-01-01T00:00:00Z','verified','2026-01-02T00:00:00Z','operation-synthetic')`,
		`INSERT INTO codex_oauth_bindings VALUES('ups_oauth','synthetic-client','authorization_code','2026-01-01T00:00:00Z')`,
		`INSERT INTO models(id,upstream_id,upstream_model,enabled,created_at) VALUES('public-model','ups_oauth','native-model',1,'2026-01-01T00:00:00Z')`,
		`INSERT INTO employees VALUES('employee','synthetic','','','active','all',3,'2026-01-01T00:00:00Z')`,
		`INSERT INTO access_keys VALUES('key','employee','synthetic','selector',X'05060708',1,'key-operation',NULL,NULL,'2026-01-01T00:00:00Z')`,
		`PRAGMA foreign_keys=ON`,
		`CREATE TABLE _upstreams_membership_migration (sentinel TEXT)`,
		`INSERT INTO _upstreams_membership_migration VALUES('preserve')`,
	}
	for _, statement := range statements {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	s := &store{db: db}
	if err := s.migrateUpstreamsForCodexMembership(context.Background()); err == nil {
		t.Fatal("expected migration staging collision")
	}
	assertNativeProviderFixture(t, db, false)
	var sentinel string
	if err := db.QueryRow(`SELECT sentinel FROM _upstreams_membership_migration`).Scan(&sentinel); err != nil || sentinel != "preserve" {
		t.Fatalf("unowned staging data changed: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE _upstreams_membership_migration`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.migrateUpstreamsForCodexMembership(context.Background()); err != nil {
			t.Fatal(err)
		}
		assertNativeProviderFixture(t, db, true)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.close()
	assertNativeProviderFixture(t, reopened.db, true)
}

func assertNativeProviderFixture(t *testing.T, db *sql.DB, migrated bool) {
	t.Helper()
	var clientID, source, state, verified, operation, schema string
	var revision int64
	var ciphertext, digest []byte
	if err := db.QueryRow(`SELECT b.client_id,b.source,u.revision,u.credential_ciphertext,u.credential_state,u.verified_at,u.operation_id FROM upstreams u JOIN codex_oauth_bindings b ON b.upstream_id=u.id WHERE u.id='ups_oauth'`).Scan(&clientID, &source, &revision, &ciphertext, &state, &verified, &operation); err != nil {
		t.Fatal(err)
	}
	if clientID != "synthetic-client" || source != "authorization_code" || revision != 7 || !bytes.Equal(ciphertext, []byte{1, 2, 3, 4}) || state != "verified" || verified != "2026-01-02T00:00:00Z" || operation != "operation-synthetic" {
		t.Fatal("OAuth credential or provenance changed during provider migration")
	}
	if err := db.QueryRow(`SELECT digest FROM access_keys WHERE id='key' AND revoked_at IS NULL`).Scan(&digest); err != nil || !bytes.Equal(digest, []byte{5, 6, 7, 8}) {
		t.Fatalf("employee key changed: %v", err)
	}
	var routes, foreignKeys int
	if err := db.QueryRow(`SELECT COUNT(*) FROM models WHERE id='public-model' AND upstream_id='ups_oauth'`).Scan(&routes); err != nil || routes != 1 {
		t.Fatalf("route changed: %v", err)
	}
	if err := db.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil || foreignKeys != 1 {
		t.Fatalf("foreign keys not restored: %v", err)
	}
	rows, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal(err)
	}
	violated := rows.Next()
	iterationErr := rows.Err()
	closeErr := rows.Close()
	if violated || iterationErr != nil || closeErr != nil {
		t.Fatal("invalid foreign keys after migration")
	}
	if err := db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='upstreams'`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{anthropicAPIKeyProvider, geminiAPIKeyProvider} {
		if strings.Contains(schema, provider) != migrated {
			t.Fatalf("unexpected provider constraint for %s", provider)
		}
	}
}
