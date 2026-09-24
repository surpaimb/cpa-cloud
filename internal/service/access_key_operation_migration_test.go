package service

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestMigrateAccessKeyOperationFingerprintPreservesLegacyRowsAndRetries(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE access_keys (
		id TEXT PRIMARY KEY,
		employee_id TEXT NOT NULL,
		name TEXT NOT NULL,
		selector TEXT NOT NULL UNIQUE,
		digest BLOB NOT NULL,
		digest_version INTEGER NOT NULL,
		operation_id TEXT NOT NULL,
		expires_at TEXT,
		revoked_at TEXT,
		created_at TEXT NOT NULL,
		UNIQUE(employee_id, operation_id)
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at)
		VALUES('key_legacy','employee_legacy','Legacy','selector_legacy',X'01',1,'operation_legacy','2026-09-25T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	s := &store{db: db}
	for attempt := 0; attempt < 2; attempt++ {
		if err := s.migrateAccessKeyOperationFingerprint(context.Background()); err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
	}
	var fingerprint string
	if err := db.QueryRow(`SELECT operation_fingerprint FROM access_keys WHERE id='key_legacy'`).Scan(&fingerprint); err != nil {
		t.Fatal(err)
	}
	if fingerprint != "" {
		t.Fatalf("legacy fingerprint = %q", fingerprint)
	}
	if _, err := db.Exec(`UPDATE access_keys SET operation_fingerprint='not-a-sha256' WHERE id='key_legacy'`); err == nil {
		t.Fatal("invalid fingerprint unexpectedly passed the schema constraint")
	}
}
