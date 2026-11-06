// Independently authored, test-private synthetic SQLite artifacts only.
// All diagnostics are fixed labels: never pass raw data/paths/DDL/errors to T.
package upstreamfixturereader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	m "cpacloud.local/server/internal/upstreammanifest"
	"modernc.org/sqlite"
)

// Independent literal schema oracle: never generated from definitions/Columns.
const freshDDL = `CREATE TABLE upstreams (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
 endpoint TEXT NOT NULL,
 enabled INTEGER NOT NULL,
 credential_ciphertext BLOB NOT NULL,
 key_version INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1)),
 archived_at TEXT,
 created_at TEXT NOT NULL,
 credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required')),
 verified_at TEXT,
 operation_id TEXT UNIQUE,
 CHECK((provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL) OR (provider_kind='codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL))
)`
const rebuiltDDL = `CREATE TABLE stage_upstreams (
 id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership')),
 endpoint TEXT NOT NULL,
 enabled INTEGER NOT NULL,
 credential_ciphertext BLOB NOT NULL,
 key_version INTEGER NOT NULL,
 revision INTEGER NOT NULL,
 created_at TEXT NOT NULL,
 credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required')),
 verified_at TEXT,
 operation_id TEXT UNIQUE,
 CHECK((provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL) OR (provider_kind='codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL))
);
ALTER TABLE stage_upstreams RENAME TO upstreams;
ALTER TABLE upstreams ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
ALTER TABLE upstreams ADD COLUMN archived_at TEXT;`

func privateDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "upstreams-fixture-")
	if err != nil {
		t.Fatal("FIXTURE_TEMP_CREATE")
	}
	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal("FIXTURE_TEMP_CANONICAL")
	}
	dir, err = filepath.Abs(dir)
	if err != nil {
		t.Fatal("FIXTURE_TEMP_ABSOLUTE")
	}
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatal("FIXTURE_TEMP_ROOT")
	}
	root, err = filepath.Abs(root)
	if err != nil {
		t.Fatal("FIXTURE_TEMP_ROOT")
	}
	t.Cleanup(func() {
		// Validate the exact created absolute target before recursive cleanup.
		if filepath.Dir(dir) != root || !strings.HasPrefix(filepath.Base(dir), "upstreams-fixture-") || dir == root {
			t.Error("FIXTURE_CLEANUP_BOUNDARY")
			return
		}
		if os.RemoveAll(dir) != nil {
			t.Error("FIXTURE_CLEANUP_FAILED")
		}
	})
	return dir
}

func execFixed(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal("FIXTURE_WRITE_FAILED")
	}
}
func reseal(t *testing.T, dir string) sealedFixture {
	t.Helper()
	path := filepath.Join(dir, fixtureName)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal("FIXTURE_SEAL_STAT")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal("FIXTURE_SEAL_OPEN")
	}
	b, readErr := io.ReadAll(io.LimitReader(f, maxFile+4097))
	closeErr := f.Close()
	defer clear(b)
	if readErr != nil || closeErr != nil {
		t.Fatal("FIXTURE_SEAL_READ")
	}
	return sealedFixture{dir: dir, identity: info, digest: sha256.Sum256(b)}
}

func createFixture(t *testing.T, ddl string, n int, edit func(*sql.DB)) sealedFixture {
	t.Helper()
	dir := privateDir(t)
	path := filepath.Join(dir, fixtureName)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal("FIXTURE_WRITER_OPEN")
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if db.Close() != nil {
			t.Error("FIXTURE_WRITER_CLEANUP")
		}
	})
	execFixed(t, db, "PRAGMA journal_mode=DELETE")
	execFixed(t, db, ddl)
	if n > 0 {
		tx, err := db.Begin()
		if err != nil {
			t.Fatal("FIXTURE_WRITER_BEGIN")
		}
		defer tx.Rollback()
		for i := 1; i <= n; i++ {
			provider, version := "openai-compatible", 1
			if n == 7 {
				switch i {
				case 2, 6:
					provider = "anthropic-api-key"
				case 3:
					provider = "gemini-api-key"
					version = 2
				case 4, 7:
					provider = "codex-membership"
					version = 2
				}
			}
			enabled, archived := 1, 0
			var archive, state, verified, op any
			if provider == "codex-membership" {
				state = "verified"
				verified = "2026-10-20T00:00:00Z"
				op = fmt.Sprintf("public-operation-%04d", i)
			}
			blob := bytes.Repeat([]byte{byte(65 + i%26)}, 40)
			if n == 7 && i == 5 {
				enabled = 0
			}
			if n == 7 && (i == 6 || i == 7) {
				enabled = 0
				archived = 1
				version = 0
				archive = "2026-10-20T00:00:00Z"
				blob = []byte{}
				verified = nil
				if provider == "codex-membership" {
					state = "reauth_required"
				}
			}
			_, err = tx.Exec(`INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,archived,archived_at,created_at,credential_state,verified_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, fmt.Sprintf("public-object-%04d", i), "Public fixture", provider, "https://fixture.invalid", enabled, blob, version, 3, archived, archive, "2026-10-20T00:00:00Z", state, verified, op)
			clear(blob)
			if err != nil {
				t.Fatal("FIXTURE_ROW_WRITE")
			}
		}
		if tx.Commit() != nil {
			t.Fatal("FIXTURE_WRITER_COMMIT")
		}
	}
	if edit != nil {
		edit(db)
	} // Test-only fixture construction, not a reader callback.
	if db.Close() != nil {
		t.Fatal("FIXTURE_WRITER_CLOSE")
	}
	return reseal(t, dir)
}

func requireRejected(t *testing.T, s sealedFixture) {
	t.Helper()
	out, c := readFixture(context.Background(), s, 7, 8)
	if out != nil || c == ok || c == "" {
		t.Fatal("REFUSAL_PUBLISHED_RESULT")
	}
}

func TestFreshAndRebuiltSealedEnumeration(t *testing.T) {
	for _, test := range []struct{ name, ddl string }{{"fresh", freshDDL}, {"rebuilt", rebuiltDDL}} {
		t.Run(test.name, func(t *testing.T) {
			s := createFixture(t, test.ddl, 7, nil)
			if !verifySeal(s) {
				t.Fatal("SEALED_FIXTURE_REQUIRED")
			}
			out, c := readFixture(context.Background(), s, 7, 8)
			if c != ok || out == nil || len(out.Entries) != 7 || len(out.Counts) != 4 {
				t.Fatal("ENUMERATION_FAILED")
			}
			want := []uint32{2, 2, 1, 2}
			for i, v := range out.Counts {
				if v.Rows != want[i] {
					t.Fatal("CATEGORY_COUNT_MISMATCH")
				}
			}
			columns := []string{"id", "name", "provider_kind", "endpoint", "enabled", "credential_ciphertext", "key_version", "revision", "archived", "archived_at", "created_at", "credential_state", "verified_at", "operation_id"}
			live, archives, disabled := 0, 0, 0
			for _, e := range out.Entries {
				if len(e.Fields) != 14 {
					t.Fatal("COLUMN_ENUMERATION_MISMATCH")
				}
				for i, f := range e.Fields {
					if f.Column != columns[i] {
						t.Fatal("COLUMN_NAME_ORDER_MISMATCH")
					}
				}
				if e.Disposition == m.KeepArchivedFacts {
					archives++
					if e.AAD != (m.AAD{}) || e.Fields[5].SourceValue.Kind != m.EmptyBlob || e.Fields[6].SourceValue.Number != 0 {
						t.Fatal("TOMBSTONE_REVIVED")
					}
				} else {
					live++
					if e.AAD.Object != e.ID || e.Fields[5].Action != m.PlanM2Ciphertext {
						t.Fatal("AAD_PLAN_MISMATCH")
					}
					if e.Fields[4].SourceValue.Number == 0 {
						disabled++
					}
				}
			}
			if live != 5 || archives != 2 || disabled != 1 || !verifySeal(s) {
				t.Fatal("SEALED_RESULT_BOUNDARY")
			}
		})
	}
}

func TestEmptyTableHasFourExplicitZeroCategories(t *testing.T) {
	s := createFixture(t, freshDDL, 0, nil)
	out, c := readFixture(context.Background(), s, 7, 8)
	if c != ok || out == nil || len(out.Entries) != 0 || len(out.Counts) != 4 {
		t.Fatal("EMPTY_ENUMERATION_FAILED")
	}
	for _, v := range out.Counts {
		if v.Rows != 0 {
			t.Fatal("EMPTY_CATEGORY_NOT_ZERO")
		}
	}
}

func TestSchemaDefectsRefuseWithoutPartialOutput(t *testing.T) {
	cases := []struct{ name, ddl string }{
		{"missing-table", `CREATE TABLE other(id TEXT)`},
		{"view", `CREATE TABLE base(id TEXT);CREATE VIEW upstreams AS SELECT id FROM base`},
		{"virtual", `CREATE VIRTUAL TABLE upstreams USING fts5(id,name)`},
		{"missing-column", strings.Replace(freshDDL, " name TEXT NOT NULL,\n", "", 1)},
		{"extra-column", strings.Replace(freshDDL, " name TEXT NOT NULL,", " name TEXT NOT NULL, extra TEXT,", 1)},
		{"hidden-column", strings.Replace(freshDDL, " name TEXT NOT NULL,", " name TEXT NOT NULL, extra TEXT GENERATED ALWAYS AS(name) VIRTUAL,", 1)},
		{"wrong-affinity", strings.Replace(freshDDL, "name TEXT NOT NULL", "name BLOB NOT NULL", 1)},
		{"missing-not-null", strings.Replace(freshDDL, "credential_ciphertext BLOB NOT NULL", "credential_ciphertext BLOB", 1)},
		{"missing-pk", strings.Replace(freshDDL, "id TEXT PRIMARY KEY", "id TEXT", 1)},
		{"missing-unique", strings.Replace(freshDDL, "operation_id TEXT UNIQUE", "operation_id TEXT", 1)},
		{"wrong-default", strings.Replace(freshDDL, "archived INTEGER NOT NULL DEFAULT 0", "archived INTEGER NOT NULL DEFAULT 1", 1)},
		{"changed-provider-check", strings.Replace(freshDDL, "'gemini-api-key','codex-membership'", "'gemini-api-key','unknown-provider'", 1)},
		{"changed-literal-space", strings.Replace(freshDDL, "'imported_unverified'", "'imported_unverified '", 1)},
		{"collation", strings.Replace(freshDDL, "id TEXT PRIMARY KEY", "id TEXT COLLATE NOCASE PRIMARY KEY", 1)},
		{"without-rowid", freshDDL + " WITHOUT ROWID"},
		{"extra-index", freshDDL + `;CREATE UNIQUE INDEX extra_index ON upstreams(name)`},
		{"partial-substitute", strings.Replace(freshDDL, "operation_id TEXT UNIQUE", "operation_id TEXT", 1) + `;CREATE UNIQUE INDEX substitute ON upstreams(operation_id) WHERE operation_id IS NOT NULL`},
		{"trigger", freshDDL + `;CREATE TRIGGER extra_trigger AFTER UPDATE ON upstreams BEGIN SELECT 1;END`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) { requireRejected(t, createFixture(t, test.ddl, 0, nil)) })
	}
}

func TestStoredClassAndStateDefectsRefuse(t *testing.T) {
	cases := []struct {
		name, q string
		args    []any
	}{
		{"blob-id", `UPDATE upstreams SET id=? WHERE rowid=1`, []any{[]byte("public-blob-id")}},
		{"real-enabled", `UPDATE upstreams SET enabled=0.5 WHERE rowid=1`, nil},
		{"text-blob", `UPDATE upstreams SET credential_ciphertext='public-not-a-blob' WHERE rowid=1`, nil},
		{"empty-live-blob", `UPDATE upstreams SET credential_ciphertext=x'' WHERE rowid=1`, nil},
		{"short-live-blob", `UPDATE upstreams SET credential_ciphertext=x'01' WHERE rowid=1`, nil},
		{"null-identity", `UPDATE upstreams SET id=NULL WHERE rowid=1`, nil},
		{"invalid-created-time", `UPDATE upstreams SET created_at='public-invalid-time' WHERE rowid=1`, nil},
		{"empty-endpoint", `UPDATE upstreams SET endpoint='' WHERE rowid=1`, nil},
		{"nul-name", `UPDATE upstreams SET name=? WHERE rowid=1`, []any{"public\x00name"}},
		{"wrong-version", `UPDATE upstreams SET key_version=9 WHERE rowid=1`, nil},
		{"revision-zero", `UPDATE upstreams SET revision=0 WHERE rowid=1`, nil},
		{"archived-still-enabled", `UPDATE upstreams SET enabled=1 WHERE rowid=6`, nil},
		{"archived-live-blob", `UPDATE upstreams SET credential_ciphertext=? WHERE rowid=6`, []any{bytes.Repeat([]byte("P"), 40)}},
		{"archived-key-version", `UPDATE upstreams SET key_version=1 WHERE rowid=6`, nil},
		{"archived-no-time", `UPDATE upstreams SET archived_at=NULL WHERE rowid=6`, nil},
		{"archived-codex-state", `UPDATE upstreams SET credential_state='verified' WHERE rowid=7`, nil},
		{"archived-verified-time", `UPDATE upstreams SET verified_at='2026-10-20T00:00:00Z' WHERE rowid=7`, nil},
		{"oversize-field", `UPDATE upstreams SET endpoint=? WHERE rowid=1`, []any{strings.Repeat("P", 4097)}},
		{"oversize-blob", `UPDATE upstreams SET credential_ciphertext=zeroblob(?) WHERE rowid=1`, []any{maxBlob + 1}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			s := createFixture(t, freshDDL, 7, func(db *sql.DB) { execFixed(t, db, test.q, test.args...) })
			requireRejected(t, s)
		})
	}
}

func TestBypassedChecksAndUnknownEpochsRefuse(t *testing.T) {
	for _, q := range []string{`UPDATE upstreams SET provider_kind='unknown' WHERE rowid=1`, `UPDATE upstreams SET credential_state=NULL WHERE rowid=4`, `UPDATE upstreams SET operation_id=NULL WHERE rowid=4`, `UPDATE upstreams SET archived=2 WHERE rowid=1`} {
		s := createFixture(t, freshDDL, 7, func(db *sql.DB) { execFixed(t, db, "PRAGMA ignore_check_constraints=ON"); execFixed(t, db, q) })
		requireRejected(t, s)
	}
	s := createFixture(t, freshDDL, 1, nil)
	for _, epochs := range [][2]uint64{{0, 1}, {7, 7}, {7, 9}, {^uint64(0), 0}} {
		out, c := readFixture(context.Background(), s, epochs[0], epochs[1])
		if out != nil || c == ok {
			t.Fatal("UNKNOWN_EPOCH_ACCEPTED")
		}
	}
}

// Duplicate stored values require weakening the test schema first. The reader
// must reject that actual fixture at its schema gate, not call it a valid-
// schema scan-error injection or weaken its real PK/UNIQUE requirements.
func TestDuplicateDataWithMissingConstraintsRefuses(t *testing.T) {
	ddl := strings.Replace(freshDDL, "id TEXT PRIMARY KEY", "id TEXT", 1)
	ddl = strings.Replace(ddl, "operation_id TEXT UNIQUE", "operation_id TEXT", 1)
	s := createFixture(t, ddl, 7, func(db *sql.DB) {
		execFixed(t, db, "PRAGMA ignore_check_constraints=ON")
		execFixed(t, db, `UPDATE upstreams SET id='public-duplicate',operation_id='public-duplicate-operation'`)
	})
	requireRejected(t, s)
}

func TestOverLimitAndCorruptSealedArtifactsRefuse(t *testing.T) {
	requireRejected(t, createFixture(t, freshDDL, m.MaxRows+1, nil))
	s := createFixture(t, freshDDL, 1, nil)
	f, err := os.OpenFile(filepath.Join(s.dir, fixtureName), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("CORRUPTION_FIXTURE_OPEN")
	}
	_, writeErr := f.WriteAt([]byte{0x7f}, 100)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		t.Fatal("CORRUPTION_FIXTURE_WRITE")
	}
	s = reseal(t, s.dir)
	requireRejected(t, s)
	large := createFixture(t, freshDDL, 0, nil)
	f, err = os.OpenFile(filepath.Join(large.dir, fixtureName), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal("SIZE_FIXTURE_OPEN")
	}
	truncateErr := f.Truncate(maxFile + 512)
	closeErr = f.Close()
	if truncateErr != nil || closeErr != nil {
		t.Fatal("SIZE_FIXTURE_WRITE")
	}
	requireRejected(t, reseal(t, large.dir))
}

func TestSealAndCompanionRefusals(t *testing.T) {
	for _, name := range []string{fixtureName + "-wal", fixtureName + "-shm", fixtureName + "-journal", "unexpected"} {
		s := createFixture(t, freshDDL, 1, nil)
		if os.WriteFile(filepath.Join(s.dir, name), []byte{}, 0600) != nil {
			t.Fatal("COMPANION_FIXTURE_WRITE")
		}
		requireRejected(t, s)
	}
	s := createFixture(t, freshDDL, 1, nil)
	s.digest[0] ^= 1
	requireRejected(t, s)
	s = createFixture(t, freshDDL, 1, nil)
	s.identity = nil
	requireRejected(t, s)
	s = createFixture(t, freshDDL, 1, nil)
	s.dir = s.dir + string(filepath.Separator) + "child" + string(filepath.Separator) + ".."
	if filepath.Clean(s.dir) == s.dir {
		t.Fatal("NONCANONICAL_FIXTURE_NORMALIZED")
	}
	requireRejected(t, s)
	s = createFixture(t, freshDDL, 1, nil)
	if os.Remove(filepath.Join(s.dir, fixtureName)) != nil {
		t.Fatal("MISSING_FIXTURE_REMOVE")
	}
	requireRejected(t, s)
	if _, err := os.Lstat(filepath.Join(s.dir, fixtureName)); !os.IsNotExist(err) {
		t.Fatal("MISSING_FIXTURE_CREATED")
	}
	w := createFixture(t, freshDDL, 1, func(db *sql.DB) { execFixed(t, db, "PRAGMA journal_mode=WAL") })
	requireRejected(t, w)
}

func TestReadOnlyURIReallyRejectsWritesAndCreation(t *testing.T) {
	s := createFixture(t, freshDDL, 1, nil)
	db, err := sql.Open("sqlite", readOnlyURI(filepath.Join(s.dir, fixtureName)))
	if err != nil {
		t.Fatal("RO_PROBE_OPEN")
	}
	execFixed(t, db, "PRAGMA query_only=OFF")
	_, writeErr := db.Exec(`UPDATE main.upstreams SET name='public-changed'`)
	closeErr := db.Close()
	if writeErr == nil || closeErr != nil || !verifySeal(s) {
		t.Fatal("RO_WRITE_PROTECTION_FAILED")
	}
	dir := privateDir(t)
	db, err = sql.Open("sqlite", readOnlyURI(filepath.Join(dir, fixtureName)))
	if err != nil {
		t.Fatal("RO_ABSENT_HANDLE")
	}
	openErr := db.Ping()
	closeErr = db.Close()
	entries, dirErr := os.ReadDir(dir)
	if openErr == nil || closeErr != nil || dirErr != nil || len(entries) != 0 {
		t.Fatal("RO_ABSENT_PATH_CREATED")
	}
}

func TestCancellationReturnsNoManifest(t *testing.T) {
	s := createFixture(t, freshDDL, 7, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, c := readFixture(ctx, s, 7, 8)
	if out != nil || c != cancelled || !verifySeal(s) {
		t.Fatal("CANCELLATION_PUBLISHED_RESULT")
	}
	out, c = readFixture(nil, s, 7, 8)
	if out != nil || c != cancelled {
		t.Fatal("NIL_CONTEXT_ACCEPTED")
	}
}

// These real fixed-driver execution/finalize and rollback probes use the same
// sealed synthetic artifact, not a callback in the reader's SELECT path.
func TestActualDriverJointFailuresCannotPublishResult(t *testing.T) {
	s := createFixture(t, freshDDL, 2, nil)
	dc, err := (&sqlite.Driver{}).Open(readOnlyURI(filepath.Join(s.dir, fixtureName)))
	if err != nil {
		t.Fatal("DRIVER_PROBE_OPEN")
	}
	defer func() {
		if dc.Close() != nil {
			t.Error("DRIVER_PROBE_CLOSE")
		}
	}()
	q, yes := dc.(driver.QueryerContext)
	if !yes {
		t.Fatal("DRIVER_QUERY_INTERFACE")
	}
	r, err := q.QueryContext(context.Background(), `SELECT CASE WHEN rowid=2 THEN abs(-9223372036854775808) ELSE 1 END FROM main.upstreams ORDER BY rowid`, nil)
	if err != nil {
		t.Fatal("DRIVER_PROBE_QUERY")
	}
	open := true
	defer func() {
		if open && r.Close() != nil {
			t.Error("DRIVER_PROBE_ROWS_CLEANUP")
		}
	}()
	values := make([]driver.Value, len(r.Columns()))
	if r.Next(values) != nil {
		t.Fatal("DRIVER_PROBE_FIRST_ROW")
	}
	stepErr := r.Next(values)
	closeErr := r.Close()
	open = false
	if stepErr == nil || stepErr == io.EOF || closeErr == nil {
		t.Fatal("DRIVER_JOINT_FAILURE_NOT_OBSERVED")
	}
	out, c := finish(&m.Manifest{}, badData, closeErr != nil)
	if out != nil || c != badData {
		t.Fatal("PRIMARY_FAILURE_LOST")
	}
	out, c = finish(&m.Manifest{}, ok, closeErr != nil)
	if out != nil || c != badClose {
		t.Fatal("CLOSE_FAILURE_PUBLISHED_RESULT")
	}
	dt, err := dc.Begin()
	if err != nil {
		t.Fatal("DRIVER_ROLLBACK_BEGIN")
	}
	if dt.Rollback() != nil {
		t.Fatal("DRIVER_ROLLBACK_FIRST")
	}
	rollbackErr := dt.Rollback()
	if rollbackErr == nil {
		t.Fatal("DRIVER_ROLLBACK_FAILURE_NOT_OBSERVED")
	}
	out, c = finish(&m.Manifest{}, badData, rollbackErr != nil)
	if out != nil || c != badData {
		t.Fatal("ROLLBACK_PRIMARY_LOST")
	}
	if !verifySeal(s) {
		t.Fatal("DRIVER_PROBE_MUTATED_FIXTURE")
	}
}

func TestStrictDDLTokenizationRetainsLiteralMeaning(t *testing.T) {
	if !knownDDL(freshDDL) || !knownDDL(strings.ReplaceAll(freshDDL, "\n", "\r\n\t")) {
		t.Fatal("DDL_ORACLE_REJECTED")
	}
	for _, ddl := range []string{freshDDL + " STRICT", freshDDL + ";", strings.Replace(freshDDL, "'imported_unverified'", "'imported_ unverified'", 1), strings.Replace(freshDDL, "name TEXT", "name /* hidden */ TEXT", 1), "CREATE TABLE upstreams ('unterminated)", strings.Repeat("P", 16385)} {
		if knownDDL(ddl) {
			t.Fatal("UNKNOWN_DDL_ACCEPTED")
		}
	}
	if out, c := finish(nil, ok, false); out != nil || c != badData {
		t.Fatal("NIL_SUCCESS_PUBLISHED")
	}
}

// Each mutation changes a CHECK expression only, not column nullability.
// These public fixtures are independent of the reader's frozen definitions.
func quotedNULLVariants(t *testing.T) []struct{ name, ddl string } {
	t.Helper()
	changes := []struct{ name, old, replacement string }{
		{"inline_state", "CHECK(credential_state IS NULL OR", `CHECK(credential_state IS "NULL" OR`},
		{"api_state", "AND credential_state IS NULL AND", `AND credential_state IS "NULL" AND`},
		{"api_verified", "AND verified_at IS NULL AND", `AND verified_at IS "NULL" AND`},
		{"api_operation", "AND operation_id IS NULL)", `AND operation_id IS "NULL")`},
		{"codex_state", "credential_state IS NOT NULL", `credential_state IS NOT "NULL"`},
		{"codex_operation", "operation_id IS NOT NULL", `operation_id IS NOT "NULL"`},
		{"codex_both", "credential_state IS NOT NULL AND operation_id IS NOT NULL", `credential_state IS NOT "NULL" AND operation_id IS NOT "NULL"`},
		{"inline_lowercase", "CHECK(credential_state IS NULL OR", `CHECK(credential_state IS "null" OR`},
	}
	var variants []struct{ name, ddl string }
	for _, change := range changes {
		if strings.Count(freshDDL, change.old) != 1 {
			t.Fatal("QUOTED_NULL_MUTATION_BOUNDARY")
		}
		variants = append(variants, struct{ name, ddl string }{change.name, strings.Replace(freshDDL, change.old, change.replacement, 1)})
	}
	return variants
}

func TestQuotedNULLKnownDDL(t *testing.T) {
	for _, variant := range quotedNULLVariants(t) {
		t.Run(variant.name, func(t *testing.T) {
			if knownDDL(variant.ddl) {
				t.Fatal("QUOTED_NULL_DDL_ACCEPTED")
			}
		})
	}
}

func quotedNULLFixture(t *testing.T, ddl string, legalRow bool) sealedFixture {
	t.Helper()
	return createFixture(t, ddl, 0, func(db *sql.DB) {
		// Exercise the pinned driver's default DQS behavior; never disable DQS.
		var bare, quoted int
		if db.QueryRow(`SELECT NULL IS NOT NULL, NULL IS NOT "NULL"`).Scan(&bare, &quoted) != nil || bare != 0 || quoted != 1 {
			t.Fatal("DEFAULT_DQS_SEMANTICS_CHANGED")
		}
		if legalRow {
			blob := bytes.Repeat([]byte{'P'}, 40)
			defer clear(blob)
			execFixed(t, db, `INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,archived,archived_at,created_at,credential_state,verified_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, "public-quoted-null-control", "Public fixture", "codex-membership", "https://fixture.invalid", 1, blob, 2, 3, 0, nil, "2026-10-20T00:00:00Z", "verified", "2026-10-20T00:00:00Z", "public-quoted-null-operation")
		}
		tx, err := db.Begin()
		if err != nil {
			t.Fatal("QUOTED_NULL_METADATA_BEGIN")
		}
		defer tx.Rollback()
		ctx := context.Background()
		if checkTable(ctx, tx) != ok || checkColumns(ctx, tx) != ok || checkIndexes(ctx, tx) != ok || checkIntegrity(ctx, tx) != ok {
			t.Fatal("QUOTED_NULL_METADATA_CONTROL_FAILED")
		}
		if tx.Rollback() != nil {
			t.Fatal("QUOTED_NULL_METADATA_ROLLBACK")
		}
	})
}

func TestQuotedNULLSealedSchema(t *testing.T) {
	for _, variant := range quotedNULLVariants(t) {
		for _, row := range []struct {
			name  string
			legal bool
		}{{"empty", false}, {"legal_row", true}} {
			t.Run(variant.name+"/"+row.name, func(t *testing.T) {
				s := quotedNULLFixture(t, variant.ddl, row.legal)
				out, c := readFixture(context.Background(), s, 7, 8)
				if out != nil || c != badSchema {
					if c == ok && out != nil {
						t.Fatal("QUOTED_NULL_SCHEMA_ACCEPTED_MANIFEST")
					}
					t.Fatal("QUOTED_NULL_NOT_BAD_SCHEMA")
				}
				if !verifySeal(s) {
					t.Fatal("QUOTED_NULL_FIXTURE_MUTATED")
				}
			})
		}
	}
}

func TestQuotedNULLFixtureControl(t *testing.T) {
	for _, legal := range []bool{false, true} {
		s := quotedNULLFixture(t, freshDDL, legal)
		out, c := readFixture(context.Background(), s, 7, 8)
		wantRows := 0
		if legal {
			wantRows = 1
		}
		if c != ok || out == nil || len(out.Entries) != wantRows || !verifySeal(s) {
			t.Fatal("QUOTED_NULL_LEGAL_CONTROL_REJECTED")
		}
	}
}

func TestTokenCategoriesRemainDistinct(t *testing.T) {
	for _, pair := range []struct{ name, a, b string }{
		{"null_quote", "NULL", `"NULL"`}, {"keyword_quote", "IS", `"IS"`},
		{"type_quote", "TEXT", `"TEXT"`}, {"number_quote", "0", `"0"`},
		{"number_string", "0", "'0'"}, {"string_identifier", "'NULL'", `"NULL"`},
	} {
		t.Run(pair.name, func(t *testing.T) {
			a, aOK := tokens(pair.a)
			b, bOK := tokens(pair.b)
			if !aOK || !bOK || equal(a, b) {
				t.Fatal("TOKEN_CATEGORIES_COLLAPSED")
			}
		})
	}
}

func TestFrozenIdentifierQuotesRemainAccepted(t *testing.T) {
	ddl := strings.Replace(freshDDL, "CREATE TABLE upstreams", `CREATE TABLE "upstreams"`, 1)
	for _, name := range []string{"provider_kind", "credential_state", "verified_at", "operation_id"} {
		ddl = strings.ReplaceAll(ddl, name, `"`+name+`"`)
	}
	if !knownDDL(ddl) {
		t.Fatal("FROZEN_IDENTIFIER_QUOTES_REJECTED")
	}
	s := createFixture(t, ddl, 7, nil)
	out, c := readFixture(context.Background(), s, 7, 8)
	if c != ok || out == nil || len(out.Entries) != 7 || !verifySeal(s) {
		t.Fatal("QUOTED_IDENTIFIER_FIXTURE_REJECTED")
	}
}

func TestFrozenGrammarRejectsQuotedNonIdentifiers(t *testing.T) {
	for _, change := range []struct{ name, old, replacement string }{
		{"create", "CREATE TABLE", `"CREATE" TABLE`},
		{"type", "name TEXT", `name "TEXT"`},
		{"check", "CHECK(archived", `"CHECK"(archived`},
		{"default_number", "DEFAULT 0", `DEFAULT "0"`},
		{"check_number", "IN (0,1)", `IN ("0",1)`},
		{"null_string", "IS NULL OR", "IS 'NULL' OR"},
		{"null_number", "IS NULL OR", "IS 0 OR"},
		{"null_identifier", "IS NULL OR", "IS credential_state OR"},
	} {
		t.Run(change.name, func(t *testing.T) {
			if strings.Count(freshDDL, change.old) != 1 {
				t.Fatal("FROZEN_GRAMMAR_MUTATION_BOUNDARY")
			}
			if knownDDL(strings.Replace(freshDDL, change.old, change.replacement, 1)) {
				t.Fatal("QUOTED_NON_IDENTIFIER_ACCEPTED")
			}
		})
	}
}

func TestCleanupFoldPreservesEveryPrimaryFailure(t *testing.T) {
	for _, primary := range []code{badSeal, badOpen, badSchema, badData, badClose, cancelled} {
		for _, cleanupFailed := range []bool{false, true} {
			out, c := finish(&m.Manifest{}, primary, cleanupFailed)
			if out != nil || c != primary {
				t.Fatal("FIXTURE_PRIMARY_FAILURE_REPLACED")
			}
		}
	}
}
