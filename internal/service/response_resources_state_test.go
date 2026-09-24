package service

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"cpacloud.local/server/internal/keypolicy"
)

func TestMigrateResponseResourcesIsExactRetryableAndAtomic(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	ctx := context.Background()
	for attempt := 0; attempt < 2; attempt++ {
		if err := migrateResponseResources(ctx, s.db); err != nil {
			t.Fatalf("migration attempt %d: %v", attempt+1, err)
		}
	}
	for name, expected := range responseResourceSchemaObjects {
		var actual string
		if err := s.db.QueryRow(`SELECT sql FROM sqlite_master WHERE name=?`, name).Scan(&actual); err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if normalizeResponseResourceSQL(actual) != normalizeResponseResourceSQL(expected) {
			t.Fatalf("schema mismatch for %s", name)
		}
	}

	conflicted, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conflicted.close() })
	if _, err := conflicted.db.Exec(`CREATE TABLE response_resources(id TEXT PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := migrateResponseResources(ctx, conflicted.db); !errors.Is(err, errResponseStateUnavailable) {
		t.Fatalf("expected incompatible schema rejection, got %v", err)
	}
	for _, name := range []string{"response_resource_items", "background_tasks", "managed_tool_runs", "response_resources_owner_idx"} {
		var count int
		if err := conflicted.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name=?`, name).Scan(&count); err != nil || count != 0 {
			t.Fatalf("migration was not atomic for %s: count=%d err=%v", name, count, err)
		}
	}
}

func TestResponseStateEncryptionBindsPurposeOwnerSequenceAndType(t *testing.T) {
	dataDir := t.TempDir()
	if err := createRootKey(dataDir); err != nil {
		t.Fatal(err)
	}
	secretStore, err := loadSecrets(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	binding := responseDEKBinding{employeeID: "emp_one", keyID: "key_one", responseID: "resp_one"}
	dek, err := newResponseDEK()
	if err != nil {
		t.Fatal(err)
	}
	defer clear(dek)
	nonce, wrapped, err := secretStore.wrapResponseDEK(binding, dek)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonce) != 12 || len(wrapped) != 48 || bytes.Contains(wrapped, dek) {
		t.Fatal("wrapped DEK shape or confidentiality is invalid")
	}
	unwrapped, err := secretStore.unwrapResponseDEK(binding, nonce, wrapped)
	if err != nil || !bytes.Equal(unwrapped, dek) {
		t.Fatalf("unwrap equal=%v err=%v", bytes.Equal(unwrapped, dek), err)
	}
	defer clear(unwrapped)
	if _, err := secretStore.unwrapResponseDEK(responseDEKBinding{employeeID: "emp_other", keyID: "key_one", responseID: "resp_one"}, nonce, wrapped); !errors.Is(err, errResponseStateUnavailable) {
		t.Fatalf("cross-owner unwrap was not rejected: %v", err)
	}

	itemBinding := responseItemBinding{responseDEKBinding: binding, sequence: 7, itemType: "function_call_output"}
	plaintext := []byte(`{"call_id":"call_one","output":"synthetic-secret-marker"}`)
	itemNonce, ciphertext, err := sealResponseItem(dek, itemBinding, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte("synthetic-secret-marker")) {
		t.Fatal("response item ciphertext exposed plaintext")
	}
	opened, err := openResponseItem(dek, itemBinding, itemNonce, ciphertext)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("open equal=%v err=%v", bytes.Equal(opened, plaintext), err)
	}
	clear(opened)
	changed := itemBinding
	changed.sequence++
	if _, err := openResponseItem(dek, changed, itemNonce, ciphertext); !errors.Is(err, errResponseStateUnavailable) {
		t.Fatalf("cross-sequence open was not rejected: %v", err)
	}
	tampered := append([]byte(nil), ciphertext...)
	tampered[len(tampered)-1] ^= 1
	if _, err := openResponseItem(dek, itemBinding, itemNonce, tampered); !errors.Is(err, errResponseStateUnavailable) {
		t.Fatalf("tampered ciphertext was not rejected: %v", err)
	}

	credentialCiphertext, err := secretStore.encryptCredential(binding.responseID, string(bytes.Repeat([]byte{'x'}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := secretStore.unwrapResponseDEK(binding, credentialCiphertext[:12], credentialCiphertext[12:]); !errors.Is(err, errResponseStateUnavailable) {
		t.Fatalf("credential purpose crossed into response state: %v", err)
	}
}

func TestResponseResourceConstraintsRejectUnsafeRows(t *testing.T) {
	s, err := openStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })
	if err := migrateResponseResources(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	if err := keypolicy.Migrate(context.Background(), s.db); err != nil {
		t.Fatal(err)
	}
	seedResponseResourceParents(t, s.db)
	base := []any{
		"resp_one", "op_one", bytes.Repeat([]byte{1}, 32), "emp_one", "key_one", "model_one", nil,
		0, 1, "completed", 1, 1, bytes.Repeat([]byte{2}, 12), bytes.Repeat([]byte{3}, 48),
		"2026-09-24T00:00:00Z", "2026-09-24T00:00:00Z", "2026-09-24T00:00:00Z", "2026-10-24T00:00:00Z",
	}
	statement := `INSERT INTO response_resources(id,operation_id,input_fingerprint,employee_id,key_id,public_model,parent_response_id,background,store_body,status,revision,schema_version,dek_wrap_nonce,wrapped_dek,created_at,updated_at,terminal_at,expires_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`
	if _, err := s.db.Exec(statement, base...); err != nil {
		t.Fatalf("insert valid resource: %v", err)
	}
	base[0], base[1], base[3] = "resp_cross", "op_cross", "emp_other"
	if _, err := s.db.Exec(statement, base...); err != nil {
		t.Fatalf("schema permits an existing key to be paired with a different employee; coordinator must reject it, insert failed unexpectedly: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO response_resource_items(response_id,sequence,item_type,schema_version,nonce,ciphertext,created_at) VALUES('resp_one',0,'reasoning',1,?,?,?)`, bytes.Repeat([]byte{4}, 12), bytes.Repeat([]byte{5}, 17), "2026-09-24T00:00:00Z"); err == nil {
		t.Fatal("unsupported persisted item type was accepted")
	}
}

func seedResponseResourceParents(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('emp_one','one','active','all',1,'2026-09-24T00:00:00Z')`,
		`INSERT INTO employees(id,name,status,model_mode,revision,created_at) VALUES('emp_other','other','active','all',1,'2026-09-24T00:00:00Z')`,
		`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('key_one','emp_one','key','sel_one',X'01',1,'key_op','2026-09-24T00:00:00Z')`,
		`INSERT INTO access_keys(id,employee_id,name,selector,digest,digest_version,operation_id,created_at) VALUES('key_other','emp_other','key','sel_other',X'02',1,'key_other_op','2026-09-24T00:00:00Z')`,
		`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,created_at) VALUES('ups_one','upstream','openai-compatible','https://example.invalid/v1',1,X'01',1,1,'2026-09-24T00:00:00Z')`,
		`INSERT INTO models(id,upstream_id,upstream_model,enabled,revision,created_at) VALUES('model_one','ups_one','actual',1,1,'2026-09-24T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatalf("seed response resource parent: %v", err)
		}
	}
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	at := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, keyID := range []string{"key_one", "key_other"} {
		if err := keypolicy.CreateDefaultTx(context.Background(), tx, keyID, at); err != nil {
			t.Fatalf("seed response resource policy: %v", err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
