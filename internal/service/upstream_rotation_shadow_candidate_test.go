package service

// Independent public synthetic fixtures; no diagnostics print data/errors/paths.
// Controlled-return tests are explicitly labelled, never entropy/disk crash tests.
import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type upstreamShadowTestFixture struct {
	cap       *upstreamShadowFixture
	path      string
	plaintext map[string][]byte
}

func upstreamShadowTestRoots() ([]byte, []byte) {
	return bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32)
}

func upstreamShadowTestDDL(reverse bool) string {
	defs := []string{
		`id TEXT PRIMARY KEY`, `name TEXT NOT NULL`,
		`provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership'))`,
		`endpoint TEXT NOT NULL`, `enabled INTEGER NOT NULL`, `credential_ciphertext BLOB NOT NULL`,
		`key_version INTEGER NOT NULL`, `revision INTEGER NOT NULL`,
		`archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))`, `archived_at TEXT`, `created_at TEXT NOT NULL`,
		`credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required'))`,
		`verified_at TEXT`, `operation_id TEXT UNIQUE`,
	}
	if reverse {
		for i, j := 0, len(defs)-1; i < j; i, j = i+1, j-1 {
			defs[i], defs[j] = defs[j], defs[i]
		}
	}
	defs = append(defs, `CHECK((provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL) OR (provider_kind='codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL))`)
	return "CREATE TABLE upstreams (" + strings.Join(defs, ",") + ")"
}

func upstreamShadowTestExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatal("SHADOW_FIXTURE_SQL_FAILED")
	}
}

// Only this factory constructs a capability. Paths/DSNs never enter kernel args.
func upstreamShadowTestCreate(t *testing.T, ddl string, n int, edit func(*sql.DB)) upstreamShadowTestFixture {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shadow.sqlite")
	open := func(context.Context) (*sql.DB, error) {
		db, err := sql.Open("sqlite", path)
		if err == nil {
			db.SetMaxOpenConns(1)
			db.SetMaxIdleConns(1)
		}
		return db, err
	}
	db, err := open(context.Background())
	if err != nil {
		t.Fatal("SHADOW_FIXTURE_OPEN_FAILED")
	}
	t.Cleanup(func() {
		if db.Close() != nil {
			t.Error("SHADOW_FIXTURE_FINAL_CLOSE_FAILED")
		}
	})
	upstreamShadowTestExec(t, db, "PRAGMA journal_mode=DELETE")
	upstreamShadowTestExec(t, db, "PRAGMA synchronous=FULL")
	upstreamShadowTestExec(t, db, "PRAGMA busy_timeout=100")
	upstreamShadowTestExec(t, db, ddl)
	upstreamShadowTestExec(t, db, `CREATE TABLE shadow_unrelated(id INTEGER PRIMARY KEY,facts TEXT NOT NULL,amount INTEGER NOT NULL)`)
	upstreamShadowTestExec(t, db, `INSERT INTO shadow_unrelated VALUES(1,'public synthetic facts',987654321)`)
	m1, _ := upstreamShadowTestRoots()
	s, err := newSecrets(m1)
	if err != nil {
		t.Fatal("SHADOW_FIXTURE_CRYPTO_FAILED")
	}
	defer upstreamShadowClearSecrets(s)
	plaintext := map[string][]byte{}
	providers := []string{"openai-compatible", "anthropic-api-key", "gemini-api-key", "codex-membership"}
	for i := 0; i < n; i++ {
		provider := providers[i%4]
		id := fmt.Sprintf("shadow-%03d", i)
		p := []byte(fmt.Sprintf("public-synthetic-payload-%03d", i))
		if provider == "codex-membership" {
			p = []byte(fmt.Sprintf("{\"synthetic\":true,\"slot\":%d}", i))
		}
		version := 1
		if i%4 >= 2 {
			version = 2
		}
		enabled := 1
		if i >= 4 {
			enabled = 0
		}
		var state, verified, operation, archivedAt any
		if provider == "codex-membership" {
			state = "verified"
			verified = "2026-10-19T03:04:05.123Z"
			operation = fmt.Sprintf("synthetic-operation-%03d", i)
			if i == 7 {
				state = "imported_unverified"
				verified = nil
			}
		}
		archived := 0
		if i >= 8 && i < 12 {
			archived = 1
			version = 0
			enabled = 0
			archivedAt = "2026-10-20T01:02:03Z"
			verified = nil
			if provider == "codex-membership" {
				state = "reauth_required"
			}
		}
		var ciphertext []byte
		if archived == 1 {
			ciphertext = []byte{}
		} else {
			ciphertext = upstreamShadowTestEncrypt(t, s, provider, id, p)
			plaintext[id] = append([]byte(nil), p...)
		}
		upstreamShadowTestExec(t, db, `INSERT INTO upstreams(id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,archived,archived_at,created_at,credential_state,verified_at,operation_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, "public synthetic é", provider, "https://example.invalid/synthetic", enabled, ciphertext, version, int64(100+i), archived, archivedAt, "2026-10-18T00:00:00Z", state, verified, operation)
	}
	if edit != nil {
		edit(db)
	}
	return upstreamShadowTestFixture{cap: &upstreamShadowFixture{db: db, reopen: open}, path: path, plaintext: plaintext}
}

// Test dispatch is independently written, not the kernel's provider helper.
func upstreamShadowTestEncrypt(t *testing.T, s *secrets, provider, id string, p []byte) []byte {
	t.Helper()
	var b []byte
	var err error
	switch provider {
	case "openai-compatible", "anthropic-api-key":
		b, err = s.encryptCredential(id, string(p))
	case "gemini-api-key":
		b, err = s.encryptGeminiAPIKey(id, string(p))
	case "codex-membership":
		b, err = s.encryptCodexAuth(id, p)
	default:
		t.Fatal("SHADOW_TEST_PROVIDER_FAILED")
	}
	if err != nil {
		t.Fatal("SHADOW_TEST_ENCRYPT_FAILED")
	}
	return b
}

func upstreamShadowTestOpen(s *secrets, provider, id string, b []byte) ([]byte, error) {
	switch provider {
	case "openai-compatible", "anthropic-api-key":
		p, e := s.decryptCredential(id, b)
		return []byte(p), e
	case "gemini-api-key":
		p, e := s.decryptGeminiAPIKey(id, b)
		return []byte(p), e
	case "codex-membership":
		return s.decryptCodexAuth(id, b)
	}
	return nil, errors.New("synthetic provider refused")
}

func upstreamShadowTestDB(t *testing.T, f upstreamShadowTestFixture) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", f.path)
	if err != nil {
		t.Fatal("SHADOW_TEST_REOPEN_FAILED")
	}
	db.SetMaxOpenConns(1)
	return db
}

func upstreamShadowTestCapture(t *testing.T, db *sql.DB, table string) [][]any {
	t.Helper()
	q := `SELECT id,name,provider_kind,endpoint,enabled,credential_ciphertext,key_version,revision,archived,archived_at,created_at,credential_state,verified_at,operation_id FROM upstreams ORDER BY id,rowid`
	width := 14
	if table == "shadow_unrelated" {
		q = `SELECT id,facts,amount FROM shadow_unrelated ORDER BY id`
		width = 3
	}
	r, err := db.Query(q)
	// A deliberately malformed-column fixture has no complete logical-name
	// projection. Capture its raw physical row shape only for before/after
	// rollback comparison; successful/reordered fixtures still use all 14 names.
	if err != nil && table != "shadow_unrelated" {
		r, err = db.Query(`SELECT * FROM upstreams ORDER BY rowid`)
		if err == nil {
			columns, columnErr := r.Columns()
			if columnErr != nil {
				r.Close()
				t.Fatal("SHADOW_CAPTURE_COLUMNS_FAILED")
			}
			width = len(columns)
		}
	}
	if err != nil {
		t.Fatal("SHADOW_CAPTURE_QUERY_FAILED")
	}
	var out [][]any
	for r.Next() {
		values := make([]any, width)
		dest := make([]any, width)
		for i := range values {
			dest[i] = &values[i]
		}
		if r.Scan(dest...) != nil {
			r.Close()
			t.Fatal("SHADOW_CAPTURE_SCAN_FAILED")
		}
		for i, v := range values {
			if b, ok := v.([]byte); ok {
				values[i] = append([]byte{}, b...)
			}
		}
		out = append(out, values)
	}
	iterationErr, closeErr := r.Err(), r.Close()
	if iterationErr != nil || closeErr != nil {
		t.Fatal("SHADOW_CAPTURE_CLOSE_FAILED")
	}
	return out
}

func upstreamShadowTestBefore(t *testing.T, f upstreamShadowTestFixture) ([][]any, [][]any) {
	t.Helper()
	return upstreamShadowTestCapture(t, f.cap.db, "upstreams"), upstreamShadowTestCapture(t, f.cap.db, "shadow_unrelated")
}

func upstreamShadowTestAfter(t *testing.T, f upstreamShadowTestFixture) ([][]any, [][]any) {
	t.Helper()
	db := upstreamShadowTestDB(t, f)
	a, b := upstreamShadowTestCapture(t, db, "upstreams"), upstreamShadowTestCapture(t, db, "shadow_unrelated")
	if db.Close() != nil {
		t.Fatal("SHADOW_CAPTURE_DB_CLOSE_FAILED")
	}
	return a, b
}

func upstreamShadowTestReject(t *testing.T, f upstreamShadowTestFixture, m1, m2 []byte, want upstreamShadowCode) {
	t.Helper()
	before, other := upstreamShadowTestBefore(t, f)
	out, code := upstreamShadowRun(context.Background(), f.cap, m1, m2)
	if out != nil || code != want {
		t.Fatal("SHADOW_REJECTION_CLASSIFICATION_FAILED")
	}
	after, afterOther := upstreamShadowTestAfter(t, f)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(other, afterOther) {
		t.Fatal("SHADOW_FAILURE_CHANGED_FACTS")
	}
}

func upstreamShadowTestCommitted(t *testing.T, f upstreamShadowTestFixture, before, other [][]any, m1, m2 []byte) {
	t.Helper()
	after, afterOther := upstreamShadowTestAfter(t, f)
	if len(after) != len(before) || !reflect.DeepEqual(other, afterOther) {
		t.Fatal("SHADOW_COMMITTED_ENUMERATION_FAILED")
	}
	old, e1 := newSecrets(m1)
	next, e2 := newSecrets(m2)
	if e1 != nil || e2 != nil {
		t.Fatal("SHADOW_VERIFY_KEYS_FAILED")
	}
	defer upstreamShadowClearSecrets(old)
	defer upstreamShadowClearSecrets(next)
	nonces := map[string]bool{}
	for i, row := range after {
		for j := range row {
			if j != 5 && !reflect.DeepEqual(row[j], before[i][j]) {
				t.Fatal("SHADOW_NON_CIPHERTEXT_FACT_CHANGED")
			}
		}
		b, ok := row[5].([]byte)
		if !ok {
			t.Fatal("SHADOW_CIPHERTEXT_TYPE_FAILED")
		}
		if row[8].(int64) == 1 {
			if len(b) != 0 || !reflect.DeepEqual(row[5], before[i][5]) {
				t.Fatal("SHADOW_TOMBSTONE_CHANGED")
			}
			continue
		}
		if bytes.Equal(b, before[i][5].([]byte)) || len(b) < 29 {
			t.Fatal("SHADOW_CIPHERTEXT_NOT_REWRAPPED")
		}
		if nonces[string(b[:12])] {
			t.Fatal("SHADOW_TEST_NONCE_REUSED")
		}
		nonces[string(b[:12])] = true
		id, provider := row[0].(string), row[2].(string)
		p, err := upstreamShadowTestOpen(next, provider, id, b)
		match := err == nil && bytes.Equal(p, f.plaintext[id])
		clear(p)
		if !match {
			t.Fatal("SHADOW_M2_PLAINTEXT_FAILED")
		}
		p, err = upstreamShadowTestOpen(old, provider, id, b)
		clear(p)
		if err == nil {
			t.Fatal("SHADOW_OLD_ROOT_ACCEPTED")
		}
	}
}

func TestUpstreamShadowFreshReorderedReopen(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		name := "fresh"
		if reverse {
			name = "reordered"
		}
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(reverse), 13, nil)
			before, other := upstreamShadowTestBefore(t, f)
			out, code := upstreamShadowRun(context.Background(), f.cap, m1, m2)
			if code != upstreamShadowOK || out == nil || out.classification != upstreamShadowOK || out.rows != 13 || out.live != 9 || out.archived != 4 {
				t.Fatal("SHADOW_SUCCESS_SUMMARY_FAILED")
			}
			upstreamShadowTestCommitted(t, f, before, other, m1, m2)
			if again, c := upstreamShadowRun(context.Background(), f.cap, m1, m2); again != nil || c != upstreamShadowInput {
				t.Fatal("SHADOW_CAPABILITY_REUSED")
			}
		})
	}
}

func TestUpstreamShadowEmptyAndArchivedOnly(t *testing.T) {
	for _, archived := range []bool{false, true} {
		name := "empty"
		n := 0
		if archived {
			name = "archived-only"
			n = 12
		}
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), n, func(db *sql.DB) {
				if archived {
					upstreamShadowTestExec(t, db, `DELETE FROM upstreams WHERE archived=0`)
				}
			})
			before, other := upstreamShadowTestBefore(t, f)
			out, c := upstreamShadowRun(context.Background(), f.cap, m1, m2)
			if c != upstreamShadowOK || out == nil || out.live != 0 || out.rows != len(before) {
				t.Fatal("SHADOW_NO_LIVE_SUCCESS_FAILED")
			}
			upstreamShadowTestCommitted(t, f, before, other, m1, m2)
		})
	}
}

func TestUpstreamShadowInvalidRoots(t *testing.T) {
	m1, m2 := upstreamShadowTestRoots()
	for _, test := range []struct {
		name      string
		old, next []byte
		want      upstreamShadowCode
	}{
		{"wrong-m1", bytes.Repeat([]byte{0x55}, 32), m2, upstreamShadowAuth},
		{"short-m1", m1[:31], m2, upstreamShadowInput}, {"zero-m1", make([]byte, 32), m2, upstreamShadowInput},
		{"same-m2", m1, append([]byte(nil), m1...), upstreamShadowInput}, {"short-m2", m1, m2[:31], upstreamShadowInput},
		{"long-m2", m1, bytes.Repeat([]byte{0x72}, 33), upstreamShadowInput}, {"zero-m2", m1, make([]byte, 32), upstreamShadowInput},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			upstreamShadowTestReject(t, f, test.old, test.next, test.want)
		})
	}
}

func TestUpstreamShadowWrongIDAndAADFamilies(t *testing.T) {
	for _, test := range []struct {
		name     string
		from, to string
		version  int
	}{
		{"wrong-id", "openai-compatible", "openai-compatible", 1},
		{"api-to-gemini", "openai-compatible", "gemini-api-key", 2},
		{"gemini-to-api", "gemini-api-key", "openai-compatible", 1},
		{"codex-to-gemini", "codex-membership", "gemini-api-key", 2},
		{"gemini-to-codex", "gemini-api-key", "codex-membership", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 1, func(db *sql.DB) {
				s, err := newSecrets(m1)
				if err != nil {
					t.Fatal("SHADOW_AAD_FIXTURE_FAILED")
				}
				defer upstreamShadowClearSecrets(s)
				id := "shadow-000"
				if test.name == "wrong-id" {
					id = "different-public-synthetic-id"
				}
				b := upstreamShadowTestEncrypt(t, s, test.from, id, []byte("public synthetic AAD control"))
				var state, op any
				if test.to == "codex-membership" {
					state = "imported_unverified"
					op = "public-operation-control"
				}
				upstreamShadowTestExec(t, db, `UPDATE upstreams SET provider_kind=?,key_version=?,credential_ciphertext=?,credential_state=?,operation_id=?`, test.to, test.version, b, state, op)
			})
			upstreamShadowTestReject(t, f, m1, m2, upstreamShadowAuth)
		})
	}
}

func TestUpstreamShadowSharedAPIAADLimitationWitness(t *testing.T) {
	m1, m2 := upstreamShadowTestRoots()
	f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 1, func(db *sql.DB) {
		upstreamShadowTestExec(t, db, `UPDATE upstreams SET provider_kind='anthropic-api-key'`)
	})
	before, other := upstreamShadowTestBefore(t, f)
	out, c := upstreamShadowRun(context.Background(), f.cap, m1, m2)
	if out == nil || c != upstreamShadowOK {
		t.Fatal("SHADOW_SHARED_AAD_WITNESS_FAILED")
	}
	upstreamShadowTestCommitted(t, f, before, other, m1, m2)
}

func TestUpstreamShadowCiphertextTamperAndTruncation(t *testing.T) {
	for _, test := range []struct {
		name     string
		offset   int
		truncate int
		want     upstreamShadowCode
	}{
		{"nonce", 0, -1, upstreamShadowAuth}, {"body", 12, -1, upstreamShadowAuth}, {"tag", -1, -1, upstreamShadowAuth},
		{"empty", 0, 0, upstreamShadowData}, {"nonce-only", 0, 12, upstreamShadowData}, {"short-tag", 0, 27, upstreamShadowData}, {"empty-plain-bound", 0, 28, upstreamShadowData}, {"truncated-auth", 0, 29, upstreamShadowAuth},
	} {
		t.Run(test.name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, func(db *sql.DB) {
				var b []byte
				if db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id='shadow-003'`).Scan(&b) != nil {
					t.Fatal("SHADOW_TAMPER_READ_FAILED")
				}
				if test.truncate >= 0 {
					b = append([]byte{}, b[:test.truncate]...)
				} else {
					i := test.offset
					if i < 0 {
						i = len(b) - 1
					}
					b[i] ^= 0x80
				}
				upstreamShadowTestExec(t, db, `UPDATE upstreams SET credential_ciphertext=? WHERE id='shadow-003'`, b)
			})
			upstreamShadowTestReject(t, f, m1, m2, test.want)
		})
	}
}

func TestUpstreamShadowUnsupportedRowsAndTombstones(t *testing.T) {
	for _, test := range []struct {
		name, q string
		args    []any
	}{
		{"unknown-provider", `UPDATE upstreams SET provider_kind='unknown-synthetic-provider' WHERE id='shadow-000'`, nil},
		{"unknown-api-version", `UPDATE upstreams SET key_version=3 WHERE id='shadow-000'`, nil},
		{"legacy-gemini-version", `UPDATE upstreams SET key_version=1 WHERE id='shadow-002'`, nil},
		{"legacy-codex-version", `UPDATE upstreams SET key_version=1 WHERE id='shadow-003'`, nil},
		{"invalid-enabled", `UPDATE upstreams SET enabled=2 WHERE id='shadow-000'`, nil},
		{"revision-zero", `UPDATE upstreams SET revision=0 WHERE id='shadow-000'`, nil},
		{"revision-overflow", `UPDATE upstreams SET revision=9007199254740992 WHERE id='shadow-000'`, nil},
		{"integer-storage-text", `UPDATE upstreams SET revision='not-an-integer' WHERE id='shadow-000'`, nil},
		{"text-storage-blob", `UPDATE upstreams SET name=X'616263' WHERE id='shadow-000'`, nil},
		{"ciphertext-storage-text", `UPDATE upstreams SET credential_ciphertext='not-a-blob' WHERE id='shadow-000'`, nil},
		{"empty-id", `UPDATE upstreams SET id='' WHERE id='shadow-000'`, nil},
		{"null-id", `UPDATE upstreams SET id=NULL WHERE id='shadow-000'`, nil},
		{"invalid-utf8", `UPDATE upstreams SET name=CAST(X'ff' AS TEXT) WHERE id='shadow-000'`, nil},
		{"embedded-nul", `UPDATE upstreams SET name=? WHERE id='shadow-000'`, []any{"public\x00synthetic"}},
		{"bad-created-time", `UPDATE upstreams SET created_at='not-a-time' WHERE id='shadow-000'`, nil},
		{"api-state", `UPDATE upstreams SET credential_state='verified' WHERE id='shadow-000'`, nil},
		{"codex-no-state", `UPDATE upstreams SET credential_state=NULL WHERE id='shadow-003'`, nil},
		{"codex-unknown-state", `UPDATE upstreams SET credential_state='unknown' WHERE id='shadow-003'`, nil},
		{"codex-no-operation", `UPDATE upstreams SET operation_id=NULL WHERE id='shadow-003'`, nil},
		{"live-archive-time", `UPDATE upstreams SET archived_at='2026-10-20T00:00:00Z' WHERE id='shadow-000'`, nil},
		{"archive-enabled", `UPDATE upstreams SET enabled=1 WHERE id='shadow-008'`, nil},
		{"archive-no-time", `UPDATE upstreams SET archived_at=NULL WHERE id='shadow-008'`, nil},
		{"archive-ciphertext", `UPDATE upstreams SET credential_ciphertext=X'00' WHERE id='shadow-008'`, nil},
		{"archive-version", `UPDATE upstreams SET key_version=1 WHERE id='shadow-008'`, nil},
		{"archive-codex-state", `UPDATE upstreams SET credential_state='verified' WHERE id='shadow-011'`, nil},
		{"archive-codex-verified", `UPDATE upstreams SET verified_at='2026-10-20T00:00:00Z' WHERE id='shadow-011'`, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 13, func(db *sql.DB) {
				upstreamShadowTestExec(t, db, "PRAGMA ignore_check_constraints=ON")
				upstreamShadowTestExec(t, db, test.q, test.args...)
			})
			upstreamShadowTestReject(t, f, m1, m2, upstreamShadowData)
		})
	}
}

func TestUpstreamShadowSchemaAndLimits(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*sql.DB)
		want upstreamShadowCode
	}{
		{"extra-column", func(db *sql.DB) { upstreamShadowTestExec(t, db, `ALTER TABLE upstreams ADD COLUMN unexpected TEXT`) }, upstreamShadowSchema},
		{"missing-column", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `ALTER TABLE upstreams RENAME COLUMN name TO unexpected`)
		}, upstreamShadowSchema},
		{"trigger", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `CREATE TRIGGER shadow_existing AFTER UPDATE ON upstreams BEGIN UPDATE shadow_unrelated SET amount=0; END`)
		}, upstreamShadowSchema},
		{"inbound-foreign-key", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `CREATE TABLE shadow_inbound(id TEXT REFERENCES upstreams(id) ON UPDATE CASCADE)`)
		}, upstreamShadowSchema},
		{"extra-index", func(db *sql.DB) { upstreamShadowTestExec(t, db, `CREATE INDEX shadow_extra ON upstreams(name)`) }, upstreamShadowSchema},
		{"large-text", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `UPDATE upstreams SET name=? WHERE id='shadow-000'`, strings.Repeat("x", 4097))
		}, upstreamShadowData},
		{"large-blob", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `UPDATE upstreams SET credential_ciphertext=? WHERE id='shadow-000'`, make([]byte, (1<<20)+1))
		}, upstreamShadowData},
		{"aggregate-blob", func(db *sql.DB) {
			upstreamShadowTestExec(t, db, `UPDATE upstreams SET credential_ciphertext=? WHERE archived=0`, make([]byte, 1<<20))
		}, upstreamShadowData},
	} {
		t.Run(test.name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 13, test.edit)
			upstreamShadowTestReject(t, f, m1, m2, test.want)
		})
	}
	t.Run("too-many-rows", func(t *testing.T) {
		m1, m2 := upstreamShadowTestRoots()
		f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 257, nil)
		upstreamShadowTestReject(t, f, m1, m2, upstreamShadowData)
	})
	for _, test := range []struct{ name, old, next string }{
		{"missing-pk", "id TEXT PRIMARY KEY", "id TEXT"}, {"missing-unique", "operation_id TEXT UNIQUE", "operation_id TEXT"},
		{"wrong-type", "name TEXT NOT NULL", "name BLOB NOT NULL"}, {"wrong-default", "archived INTEGER NOT NULL DEFAULT 0", "archived INTEGER NOT NULL DEFAULT 1"},
		{"nullable-ciphertext", "credential_ciphertext BLOB NOT NULL", "credential_ciphertext BLOB"},
	} {
		t.Run(test.name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			ddl := strings.Replace(upstreamShadowTestDDL(false), test.old, test.next, 1)
			f := upstreamShadowTestCreate(t, ddl, 4, nil)
			upstreamShadowTestReject(t, f, m1, m2, upstreamShadowSchema)
		})
	}
}

func TestUpstreamShadowControlledEncryptionBoundaries(t *testing.T) {
	for _, name := range []string{"simulated-random-source-return", "simulated-duplicate-nonce-return", "simulated-truncated-return", "simulated-wrong-m2-return"} {
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			var first []byte
			calls := 0
			f.cap.boundary.seal = func(s *secrets, row upstreamShadowRow, p []byte) ([]byte, error) {
				calls++
				if name == "simulated-random-source-return" {
					return nil, errors.New("controlled boundary failure")
				}
				if name == "simulated-duplicate-nonce-return" && calls == 2 {
					return append([]byte(nil), first...), nil
				}
				if name == "simulated-truncated-return" {
					return make([]byte, 12), nil
				}
				if name == "simulated-wrong-m2-return" {
					wrong, e := newSecrets(m1)
					if e != nil {
						return nil, e
					}
					defer upstreamShadowClearSecrets(wrong)
					return upstreamShadowEncrypt(wrong, row, p)
				}
				b, e := upstreamShadowEncrypt(s, row, p)
				first = append([]byte(nil), b...)
				return b, e
			}
			want := upstreamShadowSeal
			if name == "simulated-duplicate-nonce-return" {
				want = upstreamShadowNonce
			}
			upstreamShadowTestReject(t, f, m1, m2, want)
		})
	}
}

func TestUpstreamShadowActualLateAuthAndWriteRollback(t *testing.T) {
	t.Run("actual-late-authentication", func(t *testing.T) {
		m1, m2 := upstreamShadowTestRoots()
		f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, func(db *sql.DB) {
			var b []byte
			if db.QueryRow(`SELECT credential_ciphertext FROM upstreams WHERE id='shadow-003'`).Scan(&b) != nil {
				t.Fatal("SHADOW_LATE_AUTH_SETUP_FAILED")
			}
			b[len(b)-1] ^= 1
			upstreamShadowTestExec(t, db, `UPDATE upstreams SET credential_ciphertext=? WHERE id='shadow-003'`, b)
		})
		writes := 0
		f.cap.boundary.beforeWrite = func(context.Context, *sql.Tx, int) error { writes++; return nil }
		upstreamShadowTestReject(t, f, m1, m2, upstreamShadowAuth)
		if writes != 3 {
			t.Fatal("SHADOW_LATE_AUTH_DID_NOT_FOLLOW_WRITES")
		}
	})
	for _, name := range []string{"actual-sqlite-abort", "actual-zero-row-update", "actual-business-fact-change"} {
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			writes := 0
			f.cap.boundary.beforeWrite = func(ctx context.Context, tx *sql.Tx, index int) error {
				writes++
				if index != 1 {
					return nil
				}
				var err error
				switch name {
				case "actual-sqlite-abort":
					_, err = tx.ExecContext(ctx, `CREATE TRIGGER shadow_late_abort BEFORE UPDATE ON upstreams WHEN NEW.id='shadow-001' BEGIN SELECT RAISE(ABORT,'synthetic late refusal'); END`)
				case "actual-zero-row-update":
					_, err = tx.ExecContext(ctx, `DELETE FROM upstreams WHERE id='shadow-001'`)
				case "actual-business-fact-change":
					_, err = tx.ExecContext(ctx, `UPDATE upstreams SET revision=revision+1 WHERE id='shadow-001'`)
				}
				return err
			}
			want := upstreamShadowWrite
			if name == "actual-business-fact-change" {
				want = upstreamShadowVerify
			}
			upstreamShadowTestReject(t, f, m1, m2, want)
			if writes < 2 {
				t.Fatal("SHADOW_LATE_WRITE_NOT_REACHED")
			}
		})
	}
}

func TestUpstreamShadowEnumerationFailures(t *testing.T) {
	for _, name := range []string{"controlled-query-error", "actual-short-column-scan", "actual-truncated-row-set", "controlled-duplicate-row-projection", "controlled-duplicate-operation-projection"} {
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 8, nil)
			f.cap.boundary.query = func(ctx context.Context, tx *sql.Tx, q string) (*sql.Rows, error) {
				if name == "controlled-query-error" {
					return nil, errors.New("controlled enumeration refusal")
				}
				if name == "actual-short-column-scan" {
					return tx.QueryContext(ctx, "SELECT id FROM upstreams")
				}
				if name == "controlled-duplicate-row-projection" {
					base := strings.TrimSuffix(q, ` ORDER BY "id"`)
					return tx.QueryContext(ctx, base+` WHERE id<>'shadow-001' UNION ALL `+base+` WHERE id='shadow-000' ORDER BY "id"`)
				}
				if name == "controlled-duplicate-operation-projection" {
					q = strings.Replace(q, `"operation_id"`, `CASE WHEN provider_kind='codex-membership' THEN 'shared-public-operation' ELSE operation_id END AS "operation_id"`, 1)
					return tx.QueryContext(ctx, q)
				}
				q = strings.Replace(q, ` FROM main.upstreams ORDER BY`, ` FROM main.upstreams WHERE id<>'shadow-001' ORDER BY`, 1)
				return tx.QueryContext(ctx, q)
			}
			upstreamShadowTestReject(t, f, m1, m2, upstreamShadowRead)
		})
	}
}

func TestUpstreamShadowCancellationRollsBack(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "before-begin"
		if late {
			name = "after-first-write"
		}
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			before, other := upstreamShadowTestBefore(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if !late {
				cancel()
			}
			writes := 0
			f.cap.boundary.beforeWrite = func(context.Context, *sql.Tx, int) error {
				writes++
				if writes == 2 {
					cancel()
				}
				return nil
			}
			out, c := upstreamShadowRun(ctx, f.cap, m1, m2)
			if out != nil || c != upstreamShadowCancel {
				t.Fatal("SHADOW_CANCELLATION_FAILED")
			}
			after, afterOther := upstreamShadowTestAfter(t, f)
			if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(other, afterOther) {
				t.Fatal("SHADOW_CANCEL_LEFT_PARTIAL_WRITES")
			}
			if late && writes != 2 {
				t.Fatal("SHADOW_CANCEL_LATE_POINT_NOT_REACHED")
			}
		})
	}
}

func TestUpstreamShadowCommitUncertaintyNoRetry(t *testing.T) {
	for _, name := range []string{"controlled-refusal", "controlled-post-commit-error", "controlled-nil-without-commit"} {
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			before, other := upstreamShadowTestBefore(t, f)
			calls := 0
			f.cap.boundary.commit = func(tx *sql.Tx) error {
				calls++
				if name == "controlled-post-commit-error" {
					if err := tx.Commit(); err != nil {
						return err
					}
				}
				if name == "controlled-nil-without-commit" {
					return nil
				}
				return errors.New("controlled commit receipt error")
			}
			out, c := upstreamShadowRun(context.Background(), f.cap, m1, m2)
			if out != nil || c != upstreamShadowUncertain || calls != 1 {
				t.Fatal("SHADOW_COMMIT_UNCERTAINTY_FAILED")
			}
			if name == "controlled-post-commit-error" {
				upstreamShadowTestCommitted(t, f, before, other, m1, m2)
			} else {
				after, afterOther := upstreamShadowTestAfter(t, f)
				if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(other, afterOther) {
					t.Fatal("SHADOW_REFUSED_COMMIT_CHANGED_FACTS")
				}
			}
		})
	}
}

func TestUpstreamShadowActualSQLiteCommitRefusal(t *testing.T) {
	m1, m2 := upstreamShadowTestRoots()
	f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, func(db *sql.DB) {
		upstreamShadowTestExec(t, db, "PRAGMA foreign_keys=ON")
		upstreamShadowTestExec(t, db, `CREATE TABLE shadow_fk_parent(id INTEGER PRIMARY KEY)`)
		upstreamShadowTestExec(t, db, `CREATE TABLE shadow_fk_child(id INTEGER PRIMARY KEY,parent_id INTEGER REFERENCES shadow_fk_parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	})
	before, other := upstreamShadowTestBefore(t, f)
	f.cap.boundary.beforeWrite = func(ctx context.Context, tx *sql.Tx, index int) error {
		if index != 1 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO shadow_fk_child VALUES(1,999)`)
		return err
	}
	commitCalls := 0
	f.cap.boundary.commit = func(tx *sql.Tx) error { commitCalls++; return tx.Commit() }
	out, code := upstreamShadowRun(context.Background(), f.cap, m1, m2)
	if out != nil || code != upstreamShadowUncertain || commitCalls != 1 {
		t.Fatal("SHADOW_ACTUAL_COMMIT_REFUSAL_FAILED")
	}
	after, afterOther := upstreamShadowTestAfter(t, f)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(other, afterOther) {
		t.Fatal("SHADOW_ACTUAL_COMMIT_REFUSAL_CHANGED_FACTS")
	}
	db := upstreamShadowTestDB(t, f)
	var children int
	readErr := db.QueryRow(`SELECT count(*) FROM shadow_fk_child`).Scan(&children)
	closeErr := db.Close()
	if readErr != nil || closeErr != nil || children != 0 {
		t.Fatal("SHADOW_ACTUAL_COMMIT_REFUSAL_LEFT_CHILD")
	}
}

func TestUpstreamShadowReopenAndVerificationFailure(t *testing.T) {
	for _, name := range []string{"controlled-reopen-failure", "post-commit-fact-mutation", "post-commit-old-ciphertext"} {
		t.Run(name, func(t *testing.T) {
			m1, m2 := upstreamShadowTestRoots()
			f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 4, nil)
			before, other := upstreamShadowTestBefore(t, f)
			reopen := f.cap.reopen
			calls := 0
			f.cap.reopen = func(ctx context.Context) (*sql.DB, error) {
				calls++
				if name == "controlled-reopen-failure" {
					return nil, errors.New("controlled reopen failure")
				}
				db, err := reopen(ctx)
				if err != nil {
					return db, err
				}
				if name == "post-commit-fact-mutation" {
					upstreamShadowTestExec(t, db, `UPDATE upstreams SET revision=revision+1 WHERE id='shadow-000'`)
				} else {
					upstreamShadowTestExec(t, db, `UPDATE upstreams SET credential_ciphertext=? WHERE id='shadow-000'`, before[0][5])
				}
				return db, nil
			}
			out, c := upstreamShadowRun(context.Background(), f.cap, m1, m2)
			want := upstreamShadowVerify
			if name == "controlled-reopen-failure" {
				want = upstreamShadowReopen
			}
			if out != nil || c != want || calls != 1 {
				t.Fatal("SHADOW_REOPEN_FAILURE_PUBLISHED_RESULT")
			}
			if name == "controlled-reopen-failure" {
				upstreamShadowTestCommitted(t, f, before, other, m1, m2)
			} else {
				_, afterOther := upstreamShadowTestAfter(t, f)
				if !reflect.DeepEqual(other, afterOther) {
					t.Fatal("SHADOW_REOPEN_CHANGED_UNRELATED_TABLE")
				}
			}
		})
	}
}

func TestUpstreamShadowNilInputsAndWALRefusal(t *testing.T) {
	m1, m2 := upstreamShadowTestRoots()
	if out, c := upstreamShadowRun(context.Background(), nil, m1, m2); out != nil || c != upstreamShadowInput {
		t.Fatal("SHADOW_NIL_CAPABILITY_ACCEPTED")
	}
	t.Run("nil-context", func(t *testing.T) {
		f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 1, nil)
		before, other := upstreamShadowTestBefore(t, f)
		out, c := upstreamShadowRun(nil, f.cap, m1, m2)
		if out != nil || c != upstreamShadowCancel {
			t.Fatal("SHADOW_NIL_CONTEXT_ACCEPTED")
		}
		after, afterOther := upstreamShadowTestAfter(t, f)
		if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(other, afterOther) {
			t.Fatal("SHADOW_NIL_CONTEXT_CHANGED_DATA")
		}
	})
	t.Run("no-reopen-capability", func(t *testing.T) {
		f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 1, nil)
		f.cap.reopen = nil
		upstreamShadowTestReject(t, f, m1, m2, upstreamShadowInput)
	})
	t.Run("wal-is-not-supported", func(t *testing.T) {
		f := upstreamShadowTestCreate(t, upstreamShadowTestDDL(false), 1, func(db *sql.DB) { upstreamShadowTestExec(t, db, "PRAGMA journal_mode=WAL") })
		upstreamShadowTestReject(t, f, m1, m2, upstreamShadowInput)
	})
}
