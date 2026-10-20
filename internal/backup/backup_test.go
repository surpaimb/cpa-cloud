package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/selfschema"
	_ "modernc.org/sqlite"
)

var testPassword = []byte("synthetic-backup-password")

// D2 independently authored synthetic SQLite admission/atomicity controls.
func d2UnitFixture(t *testing.T, statements []string) (string, map[string][]string) {
	t.Helper()
	dir := t.TempDir()
	db := openSyntheticDatabase(t, dir)
	for _, s := range statements {
		if _, err := db.Exec(s); err != nil {
			t.Fatal("D2_FIXTURE_SQL")
		}
	}
	facts := d2UnitFacts(t, db)
	if err := db.Close(); err != nil {
		t.Fatal("D2_FIXTURE_CLOSE")
	}
	return filepath.Join(dir, databaseFilename), facts
}
func d2CompleteDDL() []string {
	return []string{selfschema.Credentials, selfschema.Sessions, selfschema.ExpiryIndex}
}
func d2SeedDDL() []string {
	return append(d2CompleteDDL(), `INSERT INTO employee_self_credentials VALUES('emp_synthetic',NULL,x'010203','2099-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z')`, `INSERT INTO employee_self_sessions VALUES('self_synthetic','emp_synthetic',x'010203','synthetic-csrf','2099-01-01T00:00:00.000000000Z','2026-01-01T00:00:00.000000000Z')`)
}
func d2UnitFacts(t *testing.T, db *sql.DB) map[string][]string {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`)
	if err != nil {
		t.Fatal("D2_FACT_TABLES")
	}
	var names []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal("D2_FACT_TABLE_SCAN")
		}
		names = append(names, s)
	}
	if rows.Err() != nil || rows.Close() != nil {
		t.Fatal("D2_FACT_TABLE_ROWS")
	}
	out := map[string][]string{}
	for _, name := range append(names, "@schema") {
		query := `SELECT * FROM "` + strings.ReplaceAll(name, `"`, `""`) + `"`
		if name == "@schema" {
			query = `SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`
		}
		r, err := db.Query(query)
		if err != nil {
			t.Fatal("D2_FACT_QUERY")
		}
		cols, err := r.Columns()
		if err != nil {
			t.Fatal("D2_FACT_COLUMNS")
		}
		out[name] = []string{}
		for r.Next() {
			v := make([]any, len(cols))
			refs := make([]any, len(cols))
			for i := range v {
				refs[i] = &v[i]
			}
			if r.Scan(refs...) != nil {
				t.Fatal("D2_FACT_SCAN")
			}
			var b strings.Builder
			for _, x := range v {
				switch v := x.(type) {
				case nil:
					b.WriteString("N;")
				case []byte:
					fmt.Fprintf(&b, "B%x;", v)
				default:
					fmt.Fprintf(&b, "%T:%q;", v, fmt.Sprint(v))
				}
			}
			out[name] = append(out[name], b.String())
		}
		if r.Err() != nil || r.Close() != nil {
			t.Fatal("D2_FACT_ROWS")
		}
		sort.Strings(out[name])
	}
	return out
}
func d2ReopenedFacts(t *testing.T, path string) map[string][]string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("D2_REOPEN")
	}
	facts := d2UnitFacts(t, db)
	if db.Close() != nil {
		t.Fatal("D2_REOPEN_CLOSE")
	}
	return facts
}

func TestD2SchemaAdmission(t *testing.T) {
	cases := []struct {
		name string
		ddl  []string
	}{
		{"weak-check", []string{strings.Replace(selfschema.Credentials, `CHECK (password_hash IS NULL OR enrollment_digest IS NULL)`, `CHECK (1)`, 1), selfschema.Sessions, selfschema.ExpiryIndex}},
		{"view", []string{`CREATE VIEW employee_self_credentials AS SELECT id FROM employees`}},
		{"virtual", []string{`CREATE VIRTUAL TABLE employee_self_credentials USING fts5(value)`}},
		{"wrong-index", []string{selfschema.Credentials, selfschema.Sessions, `CREATE INDEX employee_self_sessions_expiry_idx ON employee_self_sessions(created_at)`}},
		{"extra-index", append(d2CompleteDDL(), `CREATE INDEX extra_self_idx ON employee_self_credentials(updated_at)`)},
	}
	for mask := 1; mask < 7; mask++ {
		var ddl []string
		all := d2CompleteDDL()
		for i := 0; i < 3; i++ {
			if mask&(1<<i) != 0 {
				if i == 2 && mask&2 == 0 {
					ddl = append(ddl, `CREATE INDEX employee_self_sessions_expiry_idx ON employees(name)`)
				} else {
					ddl = append(ddl, all[i])
				}
			}
		}
		cases = append(cases, struct {
			name string
			ddl  []string
		}{fmt.Sprintf("partial-%d", mask), ddl})
	}
	for _, name := range []string{"employee_self_credentials", "employee_self_sessions", "employee_self_sessions_expiry_idx"} {
		ddl := d2CompleteDDL()
		for i := range ddl {
			ddl[i] = strings.ReplaceAll(ddl[i], name, strings.ToUpper(name))
		}
		cases = append(cases, struct {
			name string
			ddl  []string
		}{"alias-" + name, ddl})
	}
	for _, quoted := range []string{`"NULL"`, `"null"`} {
		for _, position := range []int{1, 2} {
			ddl := d2CompleteDDL()
			if position == 1 {
				ddl[0] = strings.Replace(ddl[0], `enrollment_digest IS NULL) = (enrollment_expires_at IS NULL)`, `enrollment_digest IS `+quoted+`) = (enrollment_expires_at IS `+quoted+`)`, 1)
			} else {
				ddl[0] = strings.Replace(ddl[0], `password_hash IS NULL OR enrollment_digest IS NULL`, `password_hash IS `+quoted+` OR enrollment_digest IS `+quoted, 1)
			}
			cases = append(cases, struct {
				name string
				ddl  []string
			}{fmt.Sprintf("quoted-%s-%d", quoted, position), ddl})
		}
	}
	for _, target := range []string{"employee_self_credentials", "employee_self_sessions", "sessions", "codex_oauth_sessions", "codex_oauth_refresh_states"} {
		event := "DELETE"
		if target == "employee_self_credentials" || target == "codex_oauth_refresh_states" {
			event = "UPDATE"
		}
		ddl := append(d2CompleteDDL(), `CREATE TRIGGER d2_trigger BEFORE `+event+` ON `+target+` BEGIN SELECT RAISE(FAIL,'D2_TRIGGER_EXECUTED'); END`)
		cases = append(cases, struct {
			name string
			ddl  []string
		}{"trigger-" + target, ddl})
	}
	for _, target := range []string{"employee_self_credentials", "EMPLOYEE_SELF_CREDENTIALS", "Employee_Self_Sessions"} {
		for _, action := range []string{"CASCADE", "SET NULL"} {
			for _, missing := range []bool{false, true} {
				var ddl []string
				if !missing {
					ddl = d2CompleteDDL()
				}
				column := "employee_id"
				if strings.Contains(strings.ToLower(target), "sessions") {
					column = "selector"
				}
				ddl = append(ddl, `CREATE TABLE inbound_empty(v TEXT REFERENCES "`+target+`"(`+column+`) ON DELETE `+action+`)`)
				cases = append(cases, struct {
					name string
					ddl  []string
				}{fmt.Sprintf("inbound-%s-%s-missing%t", target, action, missing), ddl})
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, before := d2UnitFixture(t, tc.ddl)
			err := sanitizeRestoredDatabase(context.Background(), path)
			if !errors.Is(err, errRestoredSelfSchema) {
				t.Fatal("D2_BAD_SCHEMA_NOT_REJECTED_AT_GATE")
			}
			if !reflect.DeepEqual(before, d2ReopenedFacts(t, path)) {
				t.Fatal("D2_REJECTION_CHANGED_FACTS")
			}
		})
	}
	for _, target := range []string{"EMPLOYEE_SELF_CREDENTIALS", "Employee_Self_Sessions"} {
		for _, action := range []string{"CASCADE", "SET NULL"} {
			t.Run("populated-inbound-"+target+"-"+action, func(t *testing.T) {
				column, value := "employee_id", "emp_synthetic"
				if strings.Contains(strings.ToLower(target), "sessions") {
					column, value = "selector", "self_synthetic"
				}
				ddl := append(d2SeedDDL(), `CREATE TABLE inbound_populated(v TEXT REFERENCES "`+target+`"(`+column+`) ON DELETE `+action+`)`, `INSERT INTO inbound_populated VALUES('`+value+`')`)
				path, before := d2UnitFixture(t, ddl)
				if !errors.Is(sanitizeRestoredDatabase(context.Background(), path), errRestoredSelfSchema) || !reflect.DeepEqual(before, d2ReopenedFacts(t, path)) {
					t.Fatal("D2_POPULATED_INBOUND_NOT_REJECTED_UNCHANGED")
				}
			})
		}
	}
	for _, complete := range []bool{false, true} {
		t.Run(fmt.Sprintf("admit-complete%t", complete), func(t *testing.T) {
			var ddl []string
			if complete {
				ddl = d2SeedDDL()
			}
			path, before := d2UnitFixture(t, ddl)
			if err := sanitizeRestoredDatabase(context.Background(), path); err != nil {
				t.Fatal("D2_VALID_SCHEMA_REJECTED")
			}
			after := d2ReopenedFacts(t, path)
			for _, name := range []string{"sessions", "codex_oauth_sessions", "codex_oauth_refresh_states", "employee_self_sessions", "employee_self_credentials"} {
				delete(before, name)
				delete(after, name)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatal("D2_NON_TARGET_CHANGED")
			}
		})
	}
}

func TestD2SanitizerFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"partial-write", "real-commit-refusal", "committed-error-adapter", "pre-cancel", "during-cleanup-cancel", "committed-cancel"} {
		t.Run(mode, func(t *testing.T) {
			ddl := d2SeedDDL()
			if mode == "real-commit-refusal" {
				ddl = append(ddl, `CREATE TABLE deferred_child(v TEXT REFERENCES sessions(id) DEFERRABLE INITIALLY DEFERRED)`, `INSERT INTO deferred_child VALUES('session_old')`)
			}
			path, before := d2UnitFixture(t, ddl)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			ops := restoredSanitizeOps{}
			calls, commits := 0, 0
			realCommitted := false
			sentinel := errors.New("D2_CONTROLLED_RETURN_ERROR")
			if mode == "pre-cancel" {
				cancel()
			}
			ops.exec = func(c context.Context, tx *sql.Tx, s string) error {
				calls++
				if mode == "partial-write" && calls == 2 {
					return sentinel
				}
				_, err := tx.ExecContext(c, s)
				if mode == "during-cleanup-cancel" && calls == 1 {
					cancel()
				}
				return err
			}
			ops.commit = func(tx *sql.Tx) error {
				commits++
				err := tx.Commit()
				realCommitted = err == nil
				if err == nil && mode == "committed-error-adapter" {
					return sentinel
				}
				if err == nil && mode == "committed-cancel" {
					cancel()
				}
				return err
			}
			err := sanitizeRestoredDatabaseWithOps(ctx, path, ops)
			if err == nil {
				t.Fatal("D2_FAILURE_RETURNED_SUCCESS")
			}
			after := d2ReopenedFacts(t, path)
			if strings.HasPrefix(mode, "committed-") {
				if !realCommitted || commits != 1 || reflect.DeepEqual(before, after) {
					t.Fatal("D2_COMMITTED_BOUNDARY_LOST")
				}
				if len(after["employee_self_sessions"]) != 0 || len(after["sessions"]) != 0 {
					t.Fatal("D2_COMMITTED_FACTS_WRONG")
				}
			} else {
				if realCommitted || !reflect.DeepEqual(before, after) {
					t.Fatal("D2_PRECOMMIT_FACTS_CHANGED")
				}
			}
			if mode == "real-commit-refusal" && commits != 1 {
				t.Fatal("D2_REAL_COMMIT_NOT_REACHED")
			}
			if mode == "partial-write" && calls != 2 {
				t.Fatal("D2_PARTIAL_WRITE_NOT_REACHED")
			}
			if strings.Contains(mode, "cancel") && !errors.Is(err, context.Canceled) {
				t.Fatal("D2_CANCEL_IDENTITY_LOST")
			}
		})
	}
}

func TestD2BadRestoreNeverPublishes(t *testing.T) {
	path, before := d2UnitFixture(t, append(d2CompleteDDL(), `CREATE INDEX extra_self_idx ON employee_self_credentials(updated_at)`))
	dir := filepath.Dir(path)
	writeTestRootKey(t, dir)
	pkg := filepath.Join(t.TempDir(), "bad.cpacb")
	if _, err := Create(context.Background(), dir, pkg, testPassword, "d2"); err != nil {
		t.Fatal("D2_BAD_PACKAGE_CREATE")
	}
	original, err := os.ReadFile(pkg)
	if err != nil {
		t.Fatal("D2_BAD_PACKAGE_READ")
	}
	target := filepath.Join(t.TempDir(), "target")
	if _, err := Restore(context.Background(), pkg, target, testPassword); !errors.Is(err, errRestoredSelfSchema) {
		t.Fatal("D2_BAD_RESTORE_GATE")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("D2_BAD_TARGET_EXISTS")
	}
	after, err := os.ReadFile(pkg)
	if err != nil || !bytes.Equal(original, after) || !reflect.DeepEqual(before, d2ReopenedFacts(t, path)) {
		t.Fatal("D2_BAD_RESTORE_SOURCE_CHANGED")
	}
}

// A test-only database/sql driver makes metadata read/Close failures explicit.
// These are adapter controls, not real SQLite I/O failures.
type d2MetadataDriver struct{}
type d2MetadataConn struct{}
type d2MetadataRows struct {
	mode string
	n    int
}

func init()                                               { sql.Register("d2_metadata_rows", d2MetadataDriver{}) }
func (d2MetadataDriver) Open(string) (driver.Conn, error) { return d2MetadataConn{}, nil }
func (d2MetadataConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("D2_UNUSED_PREPARE")
}
func (d2MetadataConn) Close() error              { return nil }
func (d2MetadataConn) Begin() (driver.Tx, error) { return nil, errors.New("D2_UNUSED_BEGIN") }
func (d2MetadataConn) QueryContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Rows, error) {
	if q == "query-error" {
		return nil, errors.New("D2_QUERY_ERROR")
	}
	return &d2MetadataRows{mode: q}, nil
}
func (r *d2MetadataRows) Columns() []string { return []string{"value"} }
func (r *d2MetadataRows) Close() error {
	if r.mode == "close-error" {
		return errors.New("D2_CLOSE_ERROR")
	}
	return nil
}
func (r *d2MetadataRows) Next(dest []driver.Value) error {
	r.n++
	if r.n == 1 {
		dest[0] = int64(1)
		if r.mode == "scan-error" {
			dest[0] = []string{"unsupported"}
		}
		return nil
	}
	if r.mode == "rows-error" {
		return errors.New("D2_ROWS_ERROR")
	}
	if r.mode == "extra-row" && r.n == 2 {
		dest[0] = int64(2)
		return nil
	}
	return io.EOF
}
func TestD2MetadataFailures(t *testing.T) {
	db, err := sql.Open("d2_metadata_rows", "")
	if err != nil {
		t.Fatal("D2_ADAPTER_OPEN")
	}
	defer db.Close()
	for _, mode := range []string{"ok", "query-error", "scan-error", "rows-error", "close-error", "extra-row"} {
		t.Run(mode, func(t *testing.T) {
			err := restoredRowsMatch(context.Background(), db, mode, [][]string{{"1"}})
			if (err == nil) != (mode == "ok") {
				t.Fatal("D2_METADATA_FAILURE_ACCEPTED")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if !errors.Is(restoredRowsMatch(ctx, db, "ok", [][]string{{"1"}}), context.Canceled) {
		t.Fatal("D2_METADATA_CANCEL_LOST")
	}
}

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
