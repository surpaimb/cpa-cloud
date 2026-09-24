package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var testPassword = []byte("synthetic-backup-password")

func TestCreateVerifyRestoreLiveWALAndSafetyState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO durable_marker(value) VALUES('committed-in-wal')`); err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(source, databaseFilename+"-wal")
	if info, err := os.Stat(walPath); err != nil || info.Size() == 0 {
		t.Fatalf("expected live WAL: info=%v err=%v", info, err)
	}

	before := hashKnownSourceFiles(t, source)
	packagePath := filepath.Join(root, "instance.cpacb")
	info, err := Create(ctx, source, packagePath, testPassword, "test-build")
	if err != nil {
		t.Fatal(err)
	}
	if info.SourceVersion != "test-build" || len(info.Files) != 2 {
		t.Fatalf("unexpected metadata: %#v", info)
	}
	if !mapsEqual(before, hashKnownSourceFiles(t, source)) {
		t.Fatal("create modified source instance files")
	}
	verified, err := Verify(ctx, packagePath, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if verified.CreatedAt != info.CreatedAt {
		t.Fatalf("verify metadata mismatch: %#v %#v", verified, info)
	}

	target := filepath.Join(root, "restored")
	if _, err := Restore(ctx, packagePath, target, testPassword); err != nil {
		t.Fatal(err)
	}
	if leftovers, err := filepath.Glob(filepath.Join(root, ".cpa-cloud-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("plaintext staging paths remain: %v, %v", leftovers, err)
	}
	restored, err := sql.Open("sqlite", filepath.Join(target, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	assertCount(t, restored, `SELECT COUNT(*) FROM durable_marker WHERE value='committed-in-wal'`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM sessions`, 0)
	assertCount(t, restored, `SELECT COUNT(*) FROM codex_oauth_sessions`, 0)
	assertCount(t, restored, `SELECT COUNT(*) FROM employees WHERE id='emp_synthetic'`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM access_keys WHERE id='key_synthetic'`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM upstreams WHERE id='ups_codex' AND length(credential_ciphertext)>0`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM codex_oauth_bindings WHERE upstream_id='ups_codex'`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM accounting_requests WHERE id='usage_synthetic'`, 1)
	assertCount(t, restored, `SELECT COUNT(*) FROM governance_budget_clock WHERE singleton=1`, 1)
	var state, reason string
	if err := restored.QueryRow(`SELECT state,reason_code FROM codex_oauth_refresh_states WHERE upstream_id='ups_codex'`).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "paused" || reason != "backup_restore_uncertain_refresh" {
		t.Fatalf("state=%q reason=%q", state, reason)
	}
	if _, err := Restore(ctx, packagePath, target, testPassword); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected no-overwrite error, got %v", err)
	}
	if !mapsEqual(before, hashKnownSourceFiles(t, source)) {
		t.Fatal("restore modified source instance files")
	}
}

func TestVerifyRejectsWrongPasswordDamageTruncationAndLimits(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	packagePath := filepath.Join(root, "backup.cpacb")
	if _, err := Create(context.Background(), source, packagePath, testPassword, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(context.Background(), packagePath, []byte("wrong-password")); err == nil {
		t.Fatal("wrong password was accepted")
	}
	original, err := os.ReadFile(packagePath)
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"tampered":      func() []byte { v := append([]byte(nil), original...); v[len(v)-1] ^= 1; return v }(),
		"truncated":     append([]byte(nil), original[:len(original)-7]...),
		"unknown":       func() []byte { v := append([]byte(nil), original...); v[0] ^= 1; return v }(),
		"excessive kdf": func() []byte { v := append([]byte(nil), original...); v[20] = 0xff; return v }(),
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(root, name+".cpacb")
			if err := os.WriteFile(path, body, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Verify(context.Background(), path, testPassword); err == nil {
				t.Fatal("invalid package was accepted")
			}
		})
	}
	oversize := filepath.Join(root, "oversize.cpacb")
	f, err := os.OpenFile(oversize, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxPackageBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	if _, err := Verify(context.Background(), oversize, testPassword); err == nil || !strings.Contains(err.Error(), "size limit") {
		t.Fatalf("expected size error, got %v", err)
	}
}

func TestCreateTakesConsistentSnapshotDuringConcurrentWALWrites(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	defer db.Close()
	stop := make(chan struct{})
	done := make(chan error, 1)
	firstCommitted := make(chan struct{})
	go func() {
		for index := 0; ; index++ {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if _, err := db.Exec(`INSERT INTO durable_marker(value) VALUES(?)`, fmt.Sprintf("concurrent-%d", index)); err != nil {
				done <- err
				return
			}
			if index == 0 {
				close(firstCommitted)
			}
			time.Sleep(time.Millisecond)
		}
	}()
	select {
	case <-firstCommitted:
	case err := <-done:
		t.Fatalf("first concurrent commit failed: %v", err)
	}
	packagePath := filepath.Join(root, "concurrent.cpacb")
	_, createErr := Create(context.Background(), source, packagePath, testPassword, "test")
	close(stop)
	if writeErr := <-done; writeErr != nil {
		t.Fatal(writeErr)
	}
	if createErr != nil {
		t.Fatal(createErr)
	}
	if _, err := Verify(context.Background(), packagePath, testPassword); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	if _, err := Restore(context.Background(), packagePath, target, testPassword); err != nil {
		t.Fatal(err)
	}
	restored, err := sql.Open("sqlite", filepath.Join(target, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err := restored.QueryRow(`SELECT COUNT(*) FROM durable_marker`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("consistent snapshot omitted all committed concurrent writes")
	}
}

func TestWriteNewSyncedFileRemovesLinkedOutputAfterSyncFailure(t *testing.T) {
	output := filepath.Join(t.TempDir(), "backup.cpacb")
	want := errors.New("synthetic directory sync failure")
	if err := writeNewSyncedFileWithSync(output, []byte("complete-but-uncommitted"), func(string) error { return want }); !errors.Is(err, want) {
		t.Fatalf("got %v want %v", err, want)
	}
	if _, err := os.Lstat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed commit left output behind: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(output), ".cpa-cloud-backup-output-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("temporary outputs remain: %v, %v", leftovers, err)
	}
}

func TestCreateNoOverwriteCancellationAndNoPartialOutput(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	existing := filepath.Join(root, "existing.cpacb")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(context.Background(), source, existing, testPassword, "test"); err == nil {
		t.Fatal("existing output was overwritten")
	}
	if got, _ := os.ReadFile(existing); string(got) != "keep" {
		t.Fatalf("existing output changed: %q", got)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	output := filepath.Join(root, "cancelled.cpacb")
	if _, err := Create(cancelled, source, output, testPassword, "test"); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled output exists: %v", err)
	}
	validPackage := filepath.Join(root, "valid.cpacb")
	if _, err := Create(context.Background(), source, validPackage, testPassword, "test"); err != nil {
		t.Fatal(err)
	}
	cancelledRestore, cancelRestore := context.WithCancel(context.Background())
	cancelRestore()
	cancelledTarget := filepath.Join(root, "cancelled-restore")
	if _, err := Restore(cancelledRestore, validPackage, cancelledTarget, testPassword); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected restore cancellation, got %v", err)
	}
	if _, err := os.Lstat(cancelledTarget); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cancelled restore target exists: %v", err)
	}
	if leftovers, err := filepath.Glob(filepath.Join(root, ".cpa-cloud-*")); err != nil || len(leftovers) != 0 {
		t.Fatalf("cancelled operation left staging paths: %v, %v", leftovers, err)
	}
	badOutput := filepath.Join(root, "missing", "backup.cpacb")
	if _, err := Create(context.Background(), source, badOutput, testPassword, "test"); err == nil {
		t.Fatal("expected write-path failure")
	}
	if _, err := os.Stat(badOutput); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed output exists: %v", err)
	}
}

func TestRestoreRejectsLinkedParent(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	packagePath := filepath.Join(root, "backup.cpacb")
	if _, err := Create(context.Background(), source, packagePath, testPassword, "test"); err != nil {
		t.Fatal(err)
	}
	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	linkParent := filepath.Join(root, "linked-parent")
	if err := os.Symlink(realParent, linkParent); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := Restore(context.Background(), packagePath, filepath.Join(linkParent, "restored"), testPassword); err == nil {
		t.Fatal("restore accepted linked parent")
	}
}

func TestDecodeRejectsTrailingAndDuplicateLikePayloads(t *testing.T) {
	db := []byte("synthetic-db")
	key := []byte(base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	records := []record{{name: databaseFilename, data: db, sum: sha256.Sum256(db)}, {name: rootKeyFilename, data: key, sum: sha256.Sum256(key)}}
	payload, err := encodePayload(newInfo("test", records), records)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePayload(append(payload, 0)); err == nil {
		t.Fatal("trailing data accepted")
	}
	records[1].name = databaseFilename
	payload, err = encodePayload(newInfo("test", records), records)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePayload(payload); err == nil {
		t.Fatal("duplicate/unexpected record accepted")
	}
}

func openSyntheticDatabase(t *testing.T, source string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(source, databaseFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA wal_autocheckpoint=0`, `PRAGMA foreign_keys=ON`,
		`CREATE TABLE admins(id TEXT PRIMARY KEY, username TEXT NOT NULL)`,
		`CREATE TABLE sessions(id TEXT PRIMARY KEY)`,
		`CREATE TABLE codex_oauth_sessions(id TEXT PRIMARY KEY, session_id TEXT REFERENCES sessions(id) ON DELETE CASCADE)`,
		`CREATE TABLE employees(id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE access_keys(id TEXT PRIMARY KEY, employee_id TEXT NOT NULL, digest BLOB NOT NULL)`,
		`CREATE TABLE upstreams(id TEXT PRIMARY KEY, credential_ciphertext BLOB NOT NULL)`,
		`CREATE TABLE codex_oauth_bindings(upstream_id TEXT PRIMARY KEY, source TEXT NOT NULL)`,
		`CREATE TABLE codex_oauth_refresh_states(upstream_id TEXT PRIMARY KEY, state TEXT NOT NULL, reason_code TEXT, updated_at TEXT NOT NULL)`,
		`CREATE TABLE accounting_requests(id TEXT PRIMARY KEY, status TEXT NOT NULL)`,
		`CREATE TABLE governance_budget_clock(singleton INTEGER PRIMARY KEY, last_effective_at TEXT NOT NULL)`,
		`CREATE TABLE durable_marker(value TEXT NOT NULL)`,
		`INSERT INTO admins VALUES('admin_synthetic','admin')`,
		`INSERT INTO sessions VALUES('session_old')`,
		`INSERT INTO codex_oauth_sessions VALUES('oauth_old','session_old')`,
		`INSERT INTO employees VALUES('emp_synthetic','Synthetic Employee')`,
		`INSERT INTO access_keys VALUES('key_synthetic','emp_synthetic',x'010203')`,
		`INSERT INTO upstreams VALUES('ups_codex',x'040506')`,
		`INSERT INTO codex_oauth_bindings VALUES('ups_codex','authorization_code')`,
		`INSERT INTO codex_oauth_refresh_states VALUES('ups_codex','in_progress',NULL,'2026-09-24T00:00:00Z')`,
		`INSERT INTO accounting_requests VALUES('usage_synthetic','succeeded')`,
		`INSERT INTO governance_budget_clock VALUES(1,'2026-09-24T00:00:00Z')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			db.Close()
			t.Fatalf("%s: %v", statement, err)
		}
	}
	return db
}

func writeTestRootKey(t *testing.T, dir string) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, rootKeyFilename), []byte(base64.RawStdEncoding.EncodeToString(key)), 0o600); err != nil {
		t.Fatal(err)
	}
}

func hashKnownSourceFiles(t *testing.T, dir string) map[string][sha256.Size]byte {
	t.Helper()
	result := make(map[string][sha256.Size]byte)
	// SQLite's shared-memory index is transient coordination state and may be
	// updated by any reader. The database, committed WAL, and root key are the
	// durable source-instance files that create must leave byte-for-byte intact.
	for _, name := range []string{databaseFilename, databaseFilename + "-wal", rootKeyFilename} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		result[name] = sha256.Sum256(data)
	}
	return result
}

func mapsEqual(a, b map[string][sha256.Size]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func assertCount(t *testing.T, db *sql.DB, query string, want int) {
	t.Helper()
	var got int
	if err := db.QueryRow(query).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("query %q got %d want %d", query, got, want)
	}
}

func TestRestoredPermissionsAreRestricted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows DACL coverage is in secure_windows_test.go")
	}
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestRootKey(t, source)
	db := openSyntheticDatabase(t, source)
	db.Close()
	pkg := filepath.Join(root, "backup.cpacb")
	if _, err := Create(context.Background(), source, pkg, testPassword, "test"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "restored")
	if _, err := Restore(context.Background(), pkg, target, testPassword); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]os.FileMode{target: 0o700, filepath.Join(target, databaseFilename): 0o600, filepath.Join(target, rootKeyFilename): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != want {
			t.Fatalf("%s mode=%o want=%o", path, info.Mode().Perm(), want)
		}
	}
}
