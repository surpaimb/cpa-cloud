// Independently authored from docs/sqlite-wal-reset-compat-contract.md and public
// SQLite/driver documentation. Test-only synthetic data; no production importer.
package sqlitecompat_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"cpacloud.local/server/internal/backup"
	"modernc.org/sqlite"
)

// Filled only by the explicit old-driver generation run, before go.mod changes.
// gzip is a source-size encoding, not an authenticity mechanism; hashes pin bytes.
const legacyDB = "H4sIAAAAAAAA/+xW3UvbXBh/nrRNXuXtxavvKB2IB7xwhXamLQ4UBqsujrJatxrphEGJyVHD0qTrSZxebd2Fg/0Lu9vlLnc19v944e1uFLxw9MR+ROwcuK0O8yMk5zzP+fiR54Pf6tOS6VKy6TTrmkvyIAAiPCAEAOIAMAo9SAAQ7ZsjXI443H0/HmsvxpP2/B88gc8/se8HEKMi/7ZuR6TE7Cy2qq62YVHdMehuzdE8d7vGKGOmY7OLbNHFilJQFaIWFkoKuWjFHdMgxbKqPFIq5EmluFyorJPHynqanC2o9fkrypJSUcqLyirp255KFQUxMTGBW5xax9P5RgIULrk29RylRDKJrXF+lmbUTZv5byFwjm8bSN5jtGlrdUpU5ZlKyisq4f8yzmNzAHhwtaiEuMZIYgTmGt6GZeoZtme729Q19QxPmHb8QRg2wRDDQhj/mw2//x8CHg6bSYjfilsYgWy3+RNab1jOHqVxLuqqgB+xCl+hCitXuUQUY74406JSYnISW/9x0dK5jHUHsYB06ZoHqpegcimvlUqpUP9dUf/xv+jX/xHg0a9KtBB/ASSMwsgYCpF2/COQA9yEb5CDd5dvbd2XpMT0NL6dO6u7ekNzazua5VEWmIyeq7Q+1+A69yyrfSrP0jTZ0axgvqZJU3tFFkorC30mZm7ZtHdgz6G55GFBVdTistLL+Kbot6Y8p+81mNukWp11ByMB2l3zQMp6kxrUdk3NqulmY5s2XbrrBimmWomYlJiawpbnF7GuU8ZqL+ge6xtKwXLuOQZe3embAzpSf1tNpYlhblF2nplf/8eAx38i7UJcF4gYgbFoTGzHX8B9wC+4P2xONxeyIMFE8l8xbzsu74GM6o5tvDlz52Q5m+EPkbPzcm5ezi+hBLD8v5hfs822iCGHHz6RGfLSc1xKpskMofUNahjUgFMUXp/6yMm5e5msnMnJRJbn+fM9AAD//3/hWn4AFAAA"
const legacyDBSHA256 = "47280e3495ad814d4eacb939ae173e239de51b978f9c868d58d36f7b6cae271b"
const legacyBackup = "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"
const legacyBackupSHA256 = "455171c741a375c4954e86d8be876cd8a6f5bf1f7ec7db16afc151ee58aa8a61"
const legacySourceID = "2025-07-17 13:25:10 3ce993b8657d6d9deda380a93cdd6404a8c8ba1b185b2bc423703e41ae5f2543"
const legacyFactsSHA256 = "4d6303754563d5eb4288b88202c4ad2ef44f1e262859422822f13e8ed8cf2a6f"

const fixturePassword = "public-synthetic-sqlite-compat-password"
const fixtureDDL = `
PRAGMA page_size=512;
PRAGMA foreign_keys=ON;
CREATE TABLE admins(id INTEGER PRIMARY KEY, username TEXT NOT NULL);
CREATE TABLE sessions(id INTEGER PRIMARY KEY);
CREATE TABLE codex_oauth_sessions(id INTEGER PRIMARY KEY, session_id INTEGER REFERENCES sessions(id));
CREATE TABLE employees(id INTEGER PRIMARY KEY, name TEXT NOT NULL);
CREATE TABLE access_keys(id INTEGER PRIMARY KEY, employee_id INTEGER REFERENCES employees(id), digest BLOB NOT NULL);
CREATE TABLE upstreams(id INTEGER PRIMARY KEY, credential_ciphertext BLOB NOT NULL);
CREATE TABLE compat_values(id INTEGER PRIMARY KEY, nullable TEXT, val TEXT NOT NULL, raw BLOB NOT NULL, signed INTEGER NOT NULL, at DATETIME NOT NULL);
INSERT INTO admins VALUES(1,'public-synthetic-admin');
INSERT INTO employees VALUES(1,'synthetic employee');
INSERT INTO access_keys VALUES(1,1,x'010203');
INSERT INTO upstreams VALUES(1,x'040506');
INSERT INTO compat_values VALUES(1,NULL,'Unicode 雪 / quote '' / embedded',x'00ff0102',9223372036854775807,'2026-10-20 00:00:00');
INSERT INTO compat_values VALUES(2,'not null','second',x'',-9223372036854775808,'2001-01-01 01:02:03');
`

func mustExec(t *testing.T, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatal("SQLITE_COMPAT_EXEC_FAILED", err)
	}
}

func openDB(t *testing.T, path, mode string) *sql.DB {
	t.Helper()
	slash := filepath.ToSlash(path)
	if !strings.HasPrefix(slash, "/") {
		slash = "/" + slash
	}
	u := url.URL{Scheme: "file", Path: slash}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(1)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatal("SQLITE_COMPAT_OPEN_FAILED")
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error("SQLITE_COMPAT_CLOSE_FAILED")
		}
	})
	return db
}

func moduleVersion(t *testing.T) string {
	t.Helper()
	goExe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goExe += ".exe"
	}
	out, err := exec.Command(goExe, "list", "-m", "-f", "{{.Version}}", "modernc.org/sqlite").Output()
	if err != nil {
		t.Fatal("SQLITE_COMPAT_MODULE_IDENTITY_FAILED")
	}
	return strings.TrimSpace(string(out))
}

func runtimeIdentity(t *testing.T, db *sql.DB) (string, string) {
	t.Helper()
	var version, source string
	if err := db.QueryRow(`SELECT sqlite_version(),sqlite_source_id()`).Scan(&version, &source); err != nil {
		t.Fatal("SQLITE_COMPAT_RUNTIME_IDENTITY_FAILED")
	}
	return version, source
}

func fixtureDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db := openDB(t, path, "rwc")
	mustExec(t, db, fixtureDDL)
	return db
}

func orderedFacts(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("SQLITE_COMPAT_INTEGRITY_FAILED")
	}
	fk, err := db.Query(`PRAGMA foreign_key_check`)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_FK_CHECK_FAILED")
	}
	bad := fk.Next()
	iteration := fk.Err()
	closeErr := fk.Close()
	if bad || iteration != nil || closeErr != nil {
		t.Fatal("SQLITE_COMPAT_FK_VIOLATION")
	}
	schema, err := db.Query(`SELECT type,name,tbl_name,sql FROM sqlite_master ORDER BY type,name`)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_SCHEMA_FAILED")
	}
	var facts, tables []string
	for schema.Next() {
		var kind, name, table string
		var ddl sql.NullString
		if err := schema.Scan(&kind, &name, &table, &ddl); err != nil {
			t.Fatal("SQLITE_COMPAT_SCHEMA_SCAN_FAILED")
		}
		facts = append(facts, fmt.Sprintf("%s|%s|%s|%t|%s", kind, name, table, ddl.Valid, ddl.String))
		if kind == "table" {
			tables = append(tables, name)
		}
	}
	if err := schema.Err(); err != nil {
		t.Fatal("SQLITE_COMPAT_SCHEMA_ROWS_FAILED")
	}
	schema.Close()
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		rows, err := db.Query(`SELECT * FROM ` + quoted + ` ORDER BY rowid`)
		if err != nil {
			t.Fatal("SQLITE_COMPAT_ROWS_FAILED")
		}
		columns, err := rows.Columns()
		if err != nil {
			t.Fatal("SQLITE_COMPAT_COLUMNS_FAILED")
		}
		facts = append(facts, table+":"+strings.Join(columns, ","))
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err := rows.Scan(pointers...); err != nil {
				t.Fatal("SQLITE_COMPAT_VALUE_SCAN_FAILED")
			}
			var parts []string
			for _, value := range values {
				if b, ok := value.([]byte); ok {
					parts = append(parts, fmt.Sprintf("blob:%x", b))
				} else {
					parts = append(parts, fmt.Sprintf("%T:%v", value, value))
				}
			}
			facts = append(facts, strings.Join(parts, "|"))
		}
		if err := rows.Err(); err != nil {
			t.Fatal("SQLITE_COMPAT_VALUE_ROWS_FAILED")
		}
		rows.Close()
	}
	return facts
}

func encodedFixture(t *testing.T, data []byte) string {
	t.Helper()
	var compressed bytes.Buffer
	w := gzip.NewWriter(&compressed)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(compressed.Bytes())
}

func decodeFixture(t *testing.T, encoded, hash string) []byte {
	t.Helper()
	compressed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_FIXTURE_ENCODING_FAILED")
	}
	r, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal("SQLITE_COMPAT_FIXTURE_GZIP_FAILED")
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(data)) != hash {
		t.Fatal("SQLITE_COMPAT_FIXTURE_HASH_FAILED")
	}
	return data
}

func TestLegacyFixture(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(source, "cpa-cloud.db")
	if os.Getenv("CPA_SQLITE_LEGACY_GENERATE") == "1" {
		db := fixtureDB(t, path)
		version, id := runtimeIdentity(t, db)
		if version != "3.50.3" || moduleVersion(t) != "v1.38.2" {
			t.Fatal("SQLITE_COMPAT_LEGACY_GENERATOR_WRONG_ENGINE")
		}
		facts := orderedFacts(t, db)
		fmt.Printf("LEGACY_FACTS_SHA256=%x\n", sha256.Sum256([]byte(strings.Join(facts, "\n"))))
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		publicRoot := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32))
		if err := os.WriteFile(filepath.Join(source, "master.key"), []byte(publicRoot), 0600); err != nil {
			t.Fatal(err)
		}
		backupPath := filepath.Join(root, "legacy.cpacb")
		if _, err := backup.Create(context.Background(), source, backupPath, []byte(fixturePassword), "sqlite-v1.38.2-synthetic-compat"); err != nil {
			t.Fatal("SQLITE_COMPAT_LEGACY_BACKUP_CREATE_FAILED", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		pkg, err := os.ReadFile(backupPath)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println("LEGACY_SOURCE_ID=" + id)
		fmt.Println("LEGACY_TOOLCHAIN=" + runtime.Version() + " " + runtime.GOOS + "/" + runtime.GOARCH)
		fmt.Printf("LEGACY_DB_SHA256=%x\nLEGACY_BACKUP_SHA256=%x\n", sha256.Sum256(data), sha256.Sum256(pkg))
		fmt.Println("LEGACY_DB_GZIP=" + encodedFixture(t, data))
		fmt.Println("LEGACY_BACKUP_GZIP=" + encodedFixture(t, pkg))
		return
	}
	data := decodeFixture(t, legacyDB, legacyDBSHA256)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	old := openDB(t, path, "ro")
	oldFacts := orderedFacts(t, old)
	if fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(oldFacts, "\n")))) != legacyFactsSHA256 {
		t.Fatal("SQLITE_COMPAT_ORIGINAL_TYPED_FACTS_CHANGED")
	}
	fresh := fixtureDB(t, filepath.Join(root, "fresh.db"))
	freshFacts := orderedFacts(t, fresh)
	if !reflect.DeepEqual(oldFacts, freshFacts) {
		t.Fatal("SQLITE_COMPAT_OLD_NEW_FACTS_DIFFER")
	}
	var date any
	if err := old.QueryRow(`SELECT at FROM compat_values WHERE id=1`).Scan(&date); err != nil {
		t.Fatal(err)
	}
	if value, ok := date.(time.Time); !ok || !value.Equal(time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatal("SQLITE_COMPAT_DEFAULT_DATE_VALUE_CHANGED")
	}
	metadata, err := old.Query(`SELECT at FROM compat_values WHERE id=1`)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_DATE_METADATA_FAILED")
	}
	types, err := metadata.ColumnTypes()
	if err != nil || len(types) != 1 || types[0].ScanType() != reflect.TypeOf("") {
		t.Fatal("SQLITE_COMPAT_DEFAULT_DATE_METADATA_CHANGED")
	}
	if err := metadata.Close(); err != nil {
		t.Fatal("SQLITE_COMPAT_DATE_METADATA_CLOSE_FAILED")
	}
	backupPath := filepath.Join(root, "old.cpacb")
	if err := os.WriteFile(backupPath, decodeFixture(t, legacyBackup, legacyBackupSHA256), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := backup.Verify(context.Background(), backupPath, []byte(fixturePassword))
	if err != nil || info.SourceVersion != "sqlite-v1.38.2-synthetic-compat" {
		t.Fatal("SQLITE_COMPAT_OLD_BACKUP_VERIFY_FAILED")
	}
	target := filepath.Join(root, "restored")
	if _, err := backup.Restore(context.Background(), backupPath, target, []byte(fixturePassword)); err != nil {
		t.Fatal("SQLITE_COMPAT_OLD_BACKUP_RESTORE_FAILED", err)
	}
	restored := openDB(t, filepath.Join(target, "cpa-cloud.db"), "ro")
	if !reflect.DeepEqual(orderedFacts(t, restored), freshFacts) {
		t.Fatal("SQLITE_COMPAT_OLD_BACKUP_FACTS_DIFFER")
	}
	for _, pattern := range []string{".cpa-cloud-*", "*-wal", "*-shm"} {
		entries, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil || len(entries) != 0 {
			t.Fatal("SQLITE_COMPAT_FIXTURE_CLEANUP_FAILED")
		}
	}
	t.Log("SQLITE_COMPAT_LEGACY_DATABASE_AND_OLD_BACKUP_PASS; original-source-id=" + legacySourceID)
}

func heldConn(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	c, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal("SQLITE_COMPAT_HOLD_FAILED")
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func physical(t *testing.T, c *sql.Conn) any {
	t.Helper()
	var id any
	if err := c.Raw(func(value any) error { id = value; return nil }); err != nil {
		t.Fatal("SQLITE_COMPAT_PHYSICAL_ID_FAILED")
	}
	return id
}

func count(t *testing.T, c interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, query string, want int) {
	t.Helper()
	var got int
	if err := c.QueryRowContext(context.Background(), query).Scan(&got); err != nil || got != want {
		t.Fatal("SQLITE_COMPAT_FACT_COUNT_FAILED")
	}
}

func requireCode(t *testing.T, err error, code int) {
	t.Helper()
	var sqliteErr *sqlite.Error
	if err == nil || !errors.As(err, &sqliteErr) || sqliteErr.Code() != code {
		t.Fatal("SQLITE_COMPAT_WRONG_REFUSAL_CODE")
	}
}

// The first row is a SQL-execution barrier, not just a pre-cancelled context.
// Only then arm cancellation while the second, bounded recursive result runs.
func cancelAfterFirstRow(t *testing.T, run func(context.Context) (*sql.Rows, error)) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := run(ctx)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_CANCEL_QUERY_START_FAILED")
	}
	defer rows.Close()
	var first int
	if !rows.Next() || rows.Scan(&first) != nil || first != 0 {
		t.Fatal("SQLITE_COMPAT_SQL_EXECUTION_BARRIER_FAILED")
	}
	timer := time.AfterFunc(25*time.Millisecond, cancel)
	defer timer.Stop()
	secondRow := rows.Next()
	contextCancelled := ctx.Err() != nil
	rowError := rows.Err() != nil
	if secondRow || !rowError || !contextCancelled {
		t.Logf("SQLITE_COMPAT_CANCEL_PROBE second_row=%t context_cancelled=%t row_error=%t", secondRow, contextCancelled, rowError)
		t.Fatal("SQLITE_COMPAT_RUNNING_QUERY_NOT_CANCELLED")
	}
	if err := rows.Close(); err != nil {
		t.Fatal("SQLITE_COMPAT_CANCEL_ROWS_CLOSE_FAILED")
	}
}

// R1 records a limitation witness separately from its narrow acceptance checks.
type cancellationObservation struct {
	secondRow             bool
	contextCancelled      bool
	rowErrorIsCanceled    bool
	closeSucceeded        bool
	complete              bool
	originalGateWouldFail bool
}

func probeIdentity(t *testing.T, db *sql.DB) {
	t.Helper()
	wantVersion, wantModule := "3.51.3", "v1.46.2"
	if v := os.Getenv("CPA_SQLITE_PROBE_EXPECT_VERSION"); v != "" {
		wantVersion = v
	}
	if v := os.Getenv("CPA_SQLITE_PROBE_EXPECT_MODULE"); v != "" {
		wantModule = v
	}
	if !((wantVersion == "3.51.3" && wantModule == "v1.46.2") || (wantVersion == "3.50.3" && wantModule == "v1.38.2")) {
		t.Fatal("SQLITE_COMPAT_R1_INVALID_PROBE_IDENTITY")
	}
	version, source := runtimeIdentity(t, db)
	if version != wantVersion || moduleVersion(t) != wantModule || runtime.Version() != "go1.26.8" {
		t.Fatal("SQLITE_COMPAT_R1_WRONG_PROBE_IDENTITY")
	}
	t.Logf("SQLITE_COMPAT_R1_PROBE_IDENTITY go=%s target=%s/%s module=%s sqlite=%s source=%s", runtime.Version(), runtime.GOOS, runtime.GOARCH, wantModule, version, source)
}

// The first row proves SQL actually ran before cancellation is armed. R1 does
// not assert Next=false or a latency bound. Even if Next=true, its row is not
// scanned/accepted; all partial data is discarded when cancellation is verified.
func observeCancelledIteration(t *testing.T, run func(context.Context) (*sql.Rows, error)) ([]int64, cancellationObservation, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rows, err := run(ctx)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_R1_QUERY_START_FAILED")
	}
	defer rows.Close()
	var first int64
	if !rows.Next() || rows.Scan(&first) != nil || first != 0 {
		t.Fatal("SQLITE_COMPAT_R1_EXECUTION_BARRIER_FAILED")
	}
	partial := []int64{first}
	timer := time.AfterFunc(25*time.Millisecond, cancel)
	defer timer.Stop()
	second := rows.Next()
	iterationErr := rows.Err()
	contextErr := ctx.Err()
	closeErr := rows.Close()
	afterCloseErr := rows.Err()
	o := cancellationObservation{
		secondRow:             second,
		contextCancelled:      errors.Is(contextErr, context.Canceled),
		rowErrorIsCanceled:    errors.Is(iterationErr, context.Canceled) && errors.Is(afterCloseErr, context.Canceled),
		closeSucceeded:        closeErr == nil,
		complete:              iterationErr == nil && contextErr == nil && closeErr == nil,
		originalGateWouldFail: second || iterationErr == nil || contextErr == nil,
	}
	t.Logf("SQLITE_COMPAT_R1_LIMITATION_WITNESS second_row=%t context_is_canceled=%t rows_error_is_canceled=%t close_succeeded=%t complete=%t original_timely_gate_would_fail=%t", o.secondRow, o.contextCancelled, o.rowErrorIsCanceled, o.closeSucceeded, o.complete, o.originalGateWouldFail)
	if !o.contextCancelled || !o.rowErrorIsCanceled || !o.closeSucceeded || o.complete {
		t.Fatal("SQLITE_COMPAT_R1_CANCEL_ERROR_OR_CLOSE_GATE_FAILED")
	}
	partial = nil // Reject the entire incomplete iteration, including its first row.
	return partial, o, iterationErr
}

func TestWALR1CancellationCloseReuseCheckpointReopen(t *testing.T) {
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("bounded-comparison-%d", i), runWALR1CancellationCloseReuse)
	}
}

func runWALR1CancellationCloseReuse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	db := openDB(t, path, "rwc")
	other := openDB(t, path, "rw")
	probeIdentity(t, db)
	c := heldConn(t, db)
	r := heldConn(t, other)
	rID := physical(t, r)
	mustExec(t, c, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE facts(id INTEGER PRIMARY KEY,p INTEGER REFERENCES facts(id)); INSERT INTO facts VALUES(1,NULL)`)
	read, err := r.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	count(t, read, `SELECT count(*) FROM facts`, 1)
	mustExec(t, c, `INSERT INTO facts VALUES(2,1)`)
	var busy, total, checkpointed int
	if err := c.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &total, &checkpointed); err != nil || busy != 1 || total <= checkpointed {
		t.Fatal("SQLITE_COMPAT_EXPECTED_CHECKPOINT_BUSY")
	}
	count(t, read, `SELECT count(*) FROM facts`, 1) // Snapshot stays isolated.
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	write, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer write.Rollback()
	mustExec(t, write, `INSERT INTO facts VALUES(3,2)`)
	_, err = r.ExecContext(context.Background(), `INSERT INTO facts VALUES(4,1)`)
	requireCode(t, err, 5)
	partial, observation, cancelErr := observeCancelledIteration(t, func(ctx context.Context) (*sql.Rows, error) {
		return r.QueryContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<1000000) SELECT 0 UNION ALL SELECT sum(x) FROM n`)
	})
	if partial != nil || !errors.Is(cancelErr, context.Canceled) || observation.complete {
		t.Fatal("SQLITE_COMPAT_R1_CANCELLED_RESULT_ACCEPTED")
	}
	if physical(t, r) != rID {
		t.Fatal("SQLITE_COMPAT_R1_CANCEL_CONNECTION_REPLACED")
	}
	count(t, r, `PRAGMA foreign_keys`, 1)
	fresh, err := r.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_R1_CANCEL_TRANSACTION_LEAK")
	}
	defer fresh.Rollback()
	count(t, fresh, `SELECT count(*) FROM facts`, 2)
	if err := fresh.Rollback(); err != nil {
		t.Fatal("SQLITE_COMPAT_R1_READ_ROLLBACK_FAILED")
	}
	if err := write.Commit(); err != nil {
		t.Fatal("SQLITE_COMPAT_WAL_COMMIT_FAILED")
	}
	if err := c.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &total, &checkpointed); err != nil || busy != 0 || total != 0 || checkpointed != 0 {
		t.Fatal("SQLITE_COMPAT_WAL_CHECKPOINT_FAILED")
	}
	count(t, r, `SELECT count(*) FROM facts WHERE id IN(1,2,3)`, 3)
	count(t, r, `SELECT count(*) FROM facts WHERE id=4`, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openDB(t, path, "ro")
	orderedFacts(t, reopened)
	count(t, reopened, `SELECT count(*) FROM facts`, 3)
	t.Log("SQLITE_COMPAT_R1_WAL_CANCEL_CLOSE_SAME_PHYSICAL_REUSE_CHECKPOINT_REOPEN_PASS; timely-row-step-gate-excluded; not-wal-reset-bug-reproduction")
}

func TestNativeRuntimeIdentity(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "identity.db"), "rwc")
	version, source := runtimeIdentity(t, db)
	if version != "3.51.3" || moduleVersion(t) != "v1.46.2" || source != "2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6d618" {
		t.Fatal("SQLITE_COMPAT_WRONG_RUNTIME")
	}
	if want := os.Getenv("CPA_SQLITE_EXPECT_GOOS"); want != "" && want != runtime.GOOS {
		t.Fatal("SQLITE_COMPAT_NOT_NATIVE_GOOS")
	}
	if want := os.Getenv("CPA_SQLITE_EXPECT_GOARCH"); want != "" && want != runtime.GOARCH {
		t.Fatal("SQLITE_COMPAT_NOT_NATIVE_GOARCH")
	}
	t.Logf("SQLITE_COMPAT_NATIVE_RUNTIME_PASS go=%s target=%s/%s sqlite=%s source=%s", runtime.Version(), runtime.GOOS, runtime.GOARCH, version, source)
}

func TestDeferredForeignKeyCommitRefusalSamePhysicalConnection(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "fk.db"), "rwc")
	c := heldConn(t, db)
	id := physical(t, c)
	mustExec(t, c, `CREATE TABLE parent(id INTEGER PRIMARY KEY); CREATE TABLE child(id INTEGER PRIMARY KEY,p INTEGER REFERENCES parent(id) DEFERRABLE INITIALLY DEFERRED)`)
	tx, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, tx, `INSERT INTO child VALUES(1,99)`)
	requireCode(t, tx.Commit(), 787)
	// Do not issue a manual ROLLBACK: the pinned driver must clean this failure.
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_CONNECTION_REPLACED")
	}
	count(t, c, `SELECT count(*) FROM child`, 0)
	count(t, c, `PRAGMA foreign_keys`, 1)
	fresh, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_LEAKED_TRANSACTION")
	}
	mustExec(t, fresh, `INSERT INTO parent VALUES(2); INSERT INTO child VALUES(2,2)`)
	if err := fresh.Commit(); err != nil {
		t.Fatal("SQLITE_COMPAT_FRESH_COMMIT_FAILED")
	}
	count(t, c, `SELECT count(*) FROM child WHERE id=2 AND p=2`, 1)
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_CONNECTION_REPLACED")
	}
	t.Log("SQLITE_COMPAT_DEFERRED_FK_COMMIT_REFUSAL_SAME_PHYSICAL_REUSE_PASS; failed-operation-not-retried")
}

func TestBusyCommitRefusalSamePhysicalConnection(t *testing.T) {
	path := filepath.Join(t.TempDir(), "busy.db")
	db := openDB(t, path, "rwc")
	readerDB := openDB(t, path, "rw")
	c := heldConn(t, db)
	r := heldConn(t, readerDB)
	id := physical(t, c)
	mustExec(t, c, `PRAGMA journal_mode=DELETE; CREATE TABLE facts(id INTEGER PRIMARY KEY,value TEXT)`)
	reader, err := r.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	count(t, reader, `SELECT count(*) FROM facts`, 0) // A genuine shared read lock.
	tx, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, tx, `INSERT INTO facts VALUES(1,'must-not-commit')`)
	requireCode(t, tx.Commit(), 5)
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_CONNECTION_REPLACED")
	}
	count(t, c, `SELECT count(*) FROM facts`, 0)
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	fresh, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_BUSY_LEFT_TRANSACTION")
	}
	mustExec(t, fresh, `INSERT INTO facts VALUES(2,'new-operation-not-retry')`)
	if err := fresh.Commit(); err != nil {
		t.Fatal("SQLITE_COMPAT_BUSY_REUSE_FAILED")
	}
	count(t, c, `SELECT count(*) FROM facts WHERE id=1`, 0)
	count(t, c, `SELECT count(*) FROM facts WHERE id=2`, 1)
	count(t, c, `PRAGMA foreign_keys`, 1)
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_CONNECTION_REPLACED")
	}
	t.Log("SQLITE_COMPAT_REAL_BUSY_COMMIT_REFUSAL_SAME_PHYSICAL_REUSE_PASS; failed-operation-not-retried")
}

func TestForeignKeyRestoreAndFailClosedDiscard(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "restore.db"), "rwc")
	c := heldConn(t, db)
	id := physical(t, c)
	mustExec(t, c, `PRAGMA foreign_keys=OFF`)
	count(t, c, `PRAGMA foreign_keys`, 0)
	mustExec(t, c, `PRAGMA foreign_keys=ON`)
	count(t, c, `PRAGMA foreign_keys`, 1)
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_FK_RESTORE_REPLACED_CONNECTION")
	}
	mustExec(t, c, `PRAGMA foreign_keys=OFF`)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ExecContext(ctx, `PRAGMA foreign_keys=ON`); !errors.Is(err, context.Canceled) {
		t.Fatal("SQLITE_COMPAT_EXPECTED_RESTORE_CANCELLATION")
	}
	// Controlled cancellation of restoration, not an injected I/O failure.
	// Discard the unverified physical handle; never return FK-OFF to the pool.
	if err := c.Raw(func(any) error { return driver.ErrBadConn }); !errors.Is(err, driver.ErrBadConn) {
		t.Fatal("SQLITE_COMPAT_DISCARD_FAILED")
	}
	if _, err := c.ExecContext(context.Background(), `SELECT 1`); !errors.Is(err, sql.ErrConnDone) {
		t.Fatal("SQLITE_COMPAT_DISCARDED_CONNECTION_USABLE")
	}
	fresh := heldConn(t, db)
	if physical(t, fresh) == id {
		t.Fatal("SQLITE_COMPAT_BAD_CONNECTION_POOLED")
	}
	count(t, fresh, `PRAGMA foreign_keys`, 1)
	t.Log("SQLITE_COMPAT_FK_RESTORE_OR_DISCARD_PASS; controlled-cancel-boundary")
}

// Historical original gate: its executed FAIL and assertions are retained.
// R1 explicitly excludes timely later-step interruption; this is not an active
// passing test and must never be described as one.
func historicalOriginalWALRowStepGate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal.db")
	db := openDB(t, path, "rwc")
	other := openDB(t, path, "rw")
	c := heldConn(t, db)
	r := heldConn(t, other)
	mustExec(t, c, `PRAGMA journal_mode=WAL; PRAGMA wal_autocheckpoint=0; CREATE TABLE facts(id INTEGER PRIMARY KEY,p INTEGER REFERENCES facts(id)); INSERT INTO facts VALUES(1,NULL)`)
	read, err := r.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer read.Rollback()
	count(t, read, `SELECT count(*) FROM facts`, 1)
	mustExec(t, c, `INSERT INTO facts VALUES(2,1)`)
	var busy, total, checkpointed int
	if err := c.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &total, &checkpointed); err != nil || busy != 1 || total <= checkpointed {
		t.Fatal("SQLITE_COMPAT_EXPECTED_CHECKPOINT_BUSY")
	}
	count(t, read, `SELECT count(*) FROM facts`, 1) // Snapshot stays isolated.
	if err := read.Rollback(); err != nil {
		t.Fatal(err)
	}
	write, err := c.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer write.Rollback()
	mustExec(t, write, `INSERT INTO facts VALUES(3,2)`)
	_, err = r.ExecContext(context.Background(), `INSERT INTO facts VALUES(4,1)`)
	requireCode(t, err, 5)
	cancelAfterFirstRow(t, func(ctx context.Context) (*sql.Rows, error) {
		return r.QueryContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<1000000) SELECT 0 UNION ALL SELECT sum(x) FROM n`)
	})
	if err := write.Commit(); err != nil {
		t.Fatal("SQLITE_COMPAT_WAL_COMMIT_FAILED")
	}
	if err := c.QueryRowContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &total, &checkpointed); err != nil || busy != 0 || total != 0 || checkpointed != 0 {
		t.Fatal("SQLITE_COMPAT_WAL_CHECKPOINT_FAILED")
	}
	count(t, r, `SELECT count(*) FROM facts WHERE id IN(1,2,3)`, 3)
	count(t, r, `SELECT count(*) FROM facts WHERE id=4`, 0)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := other.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openDB(t, path, "ro")
	orderedFacts(t, reopened)
	count(t, reopened, `SELECT count(*) FROM facts`, 3)
	t.Log("SQLITE_COMPAT_WAL_MULTI_CONNECTION_CHECKPOINT_CANCEL_REOPEN_PASS; not-wal-reset-bug-reproduction")
}

func TestPreparedStatementR1ErrorCancellationCloseReuseWithoutParameterBleed(t *testing.T) {
	db := openDB(t, filepath.Join(t.TempDir(), "stmt.db"), "rwc")
	c := heldConn(t, db)
	id := physical(t, c)
	mustExec(t, c, `CREATE TABLE values_test(id INTEGER PRIMARY KEY,value TEXT NOT NULL UNIQUE)`)
	stmt, err := c.PrepareContext(context.Background(), `INSERT INTO values_test VALUES(?,?)`)
	if err != nil {
		t.Fatal(err)
	}
	defer stmt.Close()
	if _, err := stmt.ExecContext(context.Background(), 1, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := stmt.ExecContext(context.Background(), 2, "first"); err == nil {
		t.Fatal("SQLITE_COMPAT_EXPECTED_UNIQUE_REFUSAL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stmt.ExecContext(ctx, 3, "cancelled"); !errors.Is(err, context.Canceled) {
		t.Fatal("SQLITE_COMPAT_EXPECTED_STATEMENT_CANCEL")
	}
	if _, err := stmt.ExecContext(context.Background(), 4, "fourth"); err != nil {
		t.Fatal("SQLITE_COMPAT_STATEMENT_REUSE_FAILED")
	}
	count(t, c, `SELECT count(*) FROM values_test WHERE (id=1 AND value='first') OR (id=4 AND value='fourth')`, 2)
	count(t, c, `SELECT count(*) FROM values_test`, 2)
	query, err := c.PrepareContext(context.Background(), `WITH RECURSIVE n(x) AS (VALUES(0) UNION ALL SELECT x+1 FROM n WHERE x<?) SELECT 0 UNION ALL SELECT sum(x+?) FROM n`)
	if err != nil {
		t.Fatal(err)
	}
	defer query.Close()
	partial, observation, cancelErr := observeCancelledIteration(t, func(ctx context.Context) (*sql.Rows, error) { return query.QueryContext(ctx, 1000000, 999) })
	if partial != nil || observation.complete || !errors.Is(cancelErr, context.Canceled) {
		t.Fatal("SQLITE_COMPAT_R1_STATEMENT_CANCEL_ACCEPTED")
	}
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_R1_STATEMENT_CANCEL_CONNECTION_REPLACED")
	}
	count(t, c, `PRAGMA foreign_keys`, 1)
	rows, err := query.QueryContext(context.Background(), 3, 7)
	if err != nil {
		t.Fatal("SQLITE_COMPAT_CANCELLED_STATEMENT_REUSE_FAILED")
	}
	var got int64
	if !rows.Next() || rows.Scan(&got) != nil || got != 0 || !rows.Next() || rows.Scan(&got) != nil || got != 34 || rows.Next() || rows.Err() != nil {
		t.Fatal("SQLITE_COMPAT_CANCELLED_STATEMENT_PARAMETER_BLEED")
	}
	if err := rows.Close(); err != nil {
		t.Fatal("SQLITE_COMPAT_REUSED_ROWS_CLOSE_FAILED")
	}
	if physical(t, c) != id {
		t.Fatal("SQLITE_COMPAT_STATEMENT_CONNECTION_REPLACED")
	}
	t.Log("SQLITE_COMPAT_R1_PREPARED_ERROR_CANCEL_CLOSE_SAME_PHYSICAL_REUSE_PASS; timely-row-step-gate-excluded")
}

func TestReadOnlyMissingNoCreateWriteRefusalQueryOnly(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "missing.db")
	roMissing := openDB(t, missing, "ro")
	if err := roMissing.Ping(); err == nil {
		t.Fatal("SQLITE_COMPAT_RO_MISSING_ACCEPTED")
	}
	if _, err := os.Stat(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("SQLITE_COMPAT_RO_CREATED_MISSING")
	}
	path := filepath.Join(root, "existing.db")
	rw := fixtureDB(t, path)
	before := orderedFacts(t, rw)
	ro := openDB(t, path, "ro")
	if _, err := ro.Exec(`INSERT INTO employees VALUES(9,'forbidden')`); err == nil {
		t.Fatal("SQLITE_COMPAT_RO_WRITE_ACCEPTED")
	}
	c := heldConn(t, rw)
	mustExec(t, c, `PRAGMA query_only=ON`)
	count(t, c, `PRAGMA query_only`, 1)
	if _, err := c.ExecContext(context.Background(), `INSERT INTO employees VALUES(10,'forbidden')`); err == nil {
		t.Fatal("SQLITE_COMPAT_QUERY_ONLY_WRITE_ACCEPTED")
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, orderedFacts(t, rw)) {
		t.Fatal("SQLITE_COMPAT_RO_FACTS_CHANGED")
	}
	if err := ro.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := roMissing.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "released.db")); err != nil {
		t.Fatal("SQLITE_COMPAT_READONLY_HANDLE_LEAK")
	}
	t.Log("SQLITE_COMPAT_RO_MISSING_WRITE_QUERY_ONLY_CLEANUP_PASS")
}
