// Package upstreamfixturereader is deliberately unexported test-fixture scope.
// It has no production constructor/caller; see PROVENANCE.md and the contract.
package upstreamfixturereader

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	m "cpacloud.local/server/internal/upstreammanifest"
	_ "modernc.org/sqlite"
)

type code string

const (
	ok          code = "FIXTURE_READER_OK"
	badSeal     code = "FIXTURE_READER_SEAL"
	badOpen     code = "FIXTURE_READER_OPEN"
	badSchema   code = "FIXTURE_READER_SCHEMA"
	badData     code = "FIXTURE_READER_DATA"
	badClose    code = "FIXTURE_READER_CLOSE"
	cancelled   code = "FIXTURE_READER_CANCELLED"
	fixtureName      = "fixture.sqlite"
	maxFile          = 8 << 20
	maxBlob          = 1 << 20
)

// Only the independently authored _test.go factory constructs this capability.
// Identity/digest are change observations, not provenance or M1 authentication.
type sealedFixture struct {
	dir      string
	identity os.FileInfo
	digest   [32]byte
}

func readOnlyURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	u.RawQuery = url.Values{"mode": {"ro"}, "cache": {"private"}}.Encode()
	return u.String()
}

func verifySeal(s sealedFixture) bool {
	if s.identity == nil || !filepath.IsAbs(s.dir) || filepath.Clean(s.dir) != s.dir || strings.HasPrefix(filepath.ToSlash(s.dir), "//") || !strings.HasPrefix(filepath.Base(s.dir), "upstreams-fixture-") {
		return false
	}
	canonical, err := filepath.EvalSymlinks(s.dir)
	if err != nil || canonical != s.dir {
		return false
	}
	dir, err := os.Lstat(s.dir)
	if err != nil || !dir.IsDir() || dir.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != fixtureName {
		return false
	}
	path := filepath.Join(s.dir, fixtureName)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(info, s.identity) || info.Size() != s.identity.Size() || !info.ModTime().Equal(s.identity.ModTime()) || info.Size() < 512 || info.Size() > maxFile {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	opened, statErr := f.Stat()
	b, readErr := io.ReadAll(io.LimitReader(f, maxFile+1))
	closeErr := f.Close()
	defer clear(b)
	if statErr != nil || readErr != nil || closeErr != nil || !os.SameFile(info, opened) || int64(len(b)) != info.Size() || len(b) < 100 || string(b[:16]) != "SQLite format 3\x00" || b[18] != 1 || b[19] != 1 {
		return false
	}
	return sha256.Sum256(b) == s.digest
}

// Primary failure has precedence; cleanup failure can never publish a result.
// Error inputs here are booleans from checked concrete resource operations,
// not injection callbacks. Tests also exercise this fold with real driver errors.
func finish(out *m.Manifest, c code, cleanupFailed bool) (*m.Manifest, code) {
	if c != ok {
		return nil, c
	}
	if cleanupFailed {
		return nil, badClose
	}
	if out == nil {
		return nil, badData
	}
	return out, ok
}

func closeRows(rows *sql.Rows, c *code) {
	if rows.Close() != nil && *c == ok {
		*c = badClose
	}
}

func readFixture(ctx context.Context, s sealedFixture, source, target uint64) (out *m.Manifest, c code) {
	if ctx == nil || ctx.Err() != nil {
		return nil, cancelled
	}
	if !verifySeal(s) {
		return nil, badSeal
	}
	db, err := sql.Open("sqlite", readOnlyURI(filepath.Join(s.dir, fixtureName)))
	if err != nil {
		return nil, badOpen
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var conn *sql.Conn
	var tx *sql.Tx
	defer func() {
		if c == ok && ctx.Err() != nil {
			out = nil
			c = cancelled
		}
		failed := false
		if tx != nil && tx.Rollback() != nil {
			failed = true
		}
		if conn != nil && conn.Close() != nil {
			failed = true
		}
		if db.Close() != nil {
			failed = true
		}
		out, c = finish(out, c, failed)
		if !verifySeal(s) {
			out = nil
			if c == ok {
				c = badSeal
			}
		}
		if c == ok && ctx.Err() != nil {
			out = nil
			c = cancelled
		}
	}()
	conn, err = db.Conn(ctx)
	if err != nil {
		return nil, badOpen
	}
	for _, q := range []string{"PRAGMA query_only=ON", "PRAGMA trusted_schema=OFF", "PRAGMA temp_store=MEMORY", "PRAGMA busy_timeout=100"} {
		if _, err = conn.ExecContext(ctx, q); err != nil {
			return nil, badOpen
		}
	}
	var queryOnly int
	if conn.QueryRowContext(ctx, "PRAGMA query_only").Scan(&queryOnly) != nil || queryOnly != 1 {
		return nil, badOpen
	}
	tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, badOpen
	}
	if c = checkSchema(ctx, tx); c != ok {
		return nil, c
	}
	before, c := tableCounts(ctx, tx)
	if c != ok {
		return nil, c
	}
	counts, c := providerCounts(ctx, tx)
	if c != ok {
		return nil, c
	}
	if c = checkLengths(ctx, tx); c != ok {
		return nil, c
	}
	snapshot, index, c := scanTable(ctx, tx, source, target)
	if c != ok {
		return nil, c
	}
	if int64(len(snapshot.Rows)) != before {
		return nil, badData
	}
	index.Counts = counts
	after, c := tableCounts(ctx, tx)
	if c != ok || after != before {
		return nil, badData
	}
	manifest, result := m.Build(snapshot, index)
	if result != m.OK {
		return nil, badData
	}
	return manifest, ok
}

func tableCounts(ctx context.Context, tx *sql.Tx) (int64, code) {
	var total, ids, uniqueIDs, ops, uniqueOps int64
	err := tx.QueryRowContext(ctx, `SELECT count(*),count(id),count(DISTINCT id),count(operation_id),count(DISTINCT operation_id) FROM main.upstreams`).Scan(&total, &ids, &uniqueIDs, &ops, &uniqueOps)
	if err != nil || total < 0 || total > m.MaxRows || ids != total || uniqueIDs != total || uniqueOps != ops {
		return 0, badData
	}
	return total, ok
}

func providerCounts(ctx context.Context, tx *sql.Tx) (out []m.ProviderCount, c code) {
	c = ok
	rows, err := tx.QueryContext(ctx, `SELECT provider_kind,count(*) FROM main.upstreams GROUP BY provider_kind`)
	if err != nil {
		return nil, badData
	}
	defer closeRows(rows, &c)
	counts := map[string]uint32{}
	for rows.Next() {
		var p string
		var n int64
		if rows.Scan(&p, &n) != nil || n < 0 || n > m.MaxRows {
			return nil, badData
		}
		known := false
		for _, spec := range m.Providers() {
			if spec.Provider == p {
				known = true
			}
		}
		if _, dup := counts[p]; !known || dup {
			return nil, badData
		}
		counts[p] = uint32(n)
	}
	if rows.Err() != nil {
		return nil, badData
	}
	for _, p := range m.Providers() {
		out = append(out, m.ProviderCount{Provider: p.Provider, Rows: counts[p.Provider]})
	}
	return out, ok
}

func quotedColumns() []string {
	var out []string
	for _, c := range m.Columns() {
		out = append(out, `"`+c.Name+`"`)
	}
	return out
}

func checkLengths(ctx context.Context, tx *sql.Tx) code {
	var exprs []string
	for _, n := range quotedColumns() {
		exprs = append(exprs, "coalesce(max(length("+n+")),0)")
	}
	values := make([]int64, 14)
	dest := make([]any, 14)
	for i := range values {
		dest[i] = &values[i]
	}
	if tx.QueryRowContext(ctx, "SELECT "+strings.Join(exprs, ",")+" FROM main.upstreams").Scan(dest...) != nil {
		return badData
	}
	for i, n := range values {
		limit := int64(4096)
		if i == 5 {
			limit = maxBlob
		}
		if n < 0 || n > limit {
			return badData
		}
	}
	return ok
}

type symbolKey struct {
	kind  m.Kind
	value string
}
type symbolizer struct {
	values map[symbolKey]m.Symbol
	next   m.Symbol
}

func (s *symbolizer) value(kind m.Kind, text string) m.Value {
	k := symbolKey{kind, text}
	v := s.values[k]
	if v == 0 {
		s.next++
		v = s.next
		s.values[k] = v
	}
	return m.Value{Kind: kind, Symbol: v}
}

func actualValue(name, class string, raw any, s *symbolizer) (m.Value, bool) {
	if class == "null" {
		return m.Value{}, raw == nil
	}
	if class == "integer" {
		v, yes := raw.(int64)
		return m.Value{Kind: m.Integer, Number: v}, yes
	}
	if class == "blob" {
		v, yes := raw.([]byte)
		if !yes || len(v) > maxBlob {
			return m.Value{}, false
		}
		defer clear(v)
		if len(v) == 0 {
			return m.Value{Kind: m.EmptyBlob}, true
		}
		if len(v) < 29 {
			return m.Value{}, false
		}
		return s.value(m.BlobSymbol, string(v)), true
	}
	if class != "text" {
		return m.Value{}, false
	}
	v, yes := raw.(string)
	if !yes || v == "" || len(v) > 4096 || !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
		return m.Value{}, false
	}
	if name == "provider_kind" || name == "credential_state" {
		return m.Value{Kind: m.Enum, Enum: v}, true
	}
	if name == "created_at" || name == "verified_at" || name == "archived_at" {
		if _, err := time.Parse(time.RFC3339Nano, v); err != nil {
			return m.Value{}, false
		}
		return s.value(m.TimestampSymbol, v), true
	}
	return s.value(m.TextSymbol, v), true
}

func scanTable(ctx context.Context, tx *sql.Tx, source, target uint64) (snapshot m.Snapshot, index m.Enumeration, c code) {
	c = ok
	columns := m.Columns()
	names := quotedColumns()
	selects := append([]string(nil), names...)
	for _, n := range names {
		selects = append(selects, "typeof("+n+")")
	}
	rows, err := tx.QueryContext(ctx, "SELECT "+strings.Join(selects, ",")+` FROM main.upstreams ORDER BY "id"`)
	if err != nil {
		return snapshot, index, badData
	}
	defer closeRows(rows, &c)
	snapshot = m.Snapshot{SourceHead: m.SourceHead, Table: m.Table, Profile: m.Profile, SourceGeneration: source, TargetGeneration: target}
	for _, col := range columns {
		snapshot.Columns = append(snapshot.Columns, col.Name)
	}
	symbols := symbolizer{values: map[symbolKey]m.Symbol{}}
	for rows.Next() {
		if len(snapshot.Rows) >= m.MaxRows {
			return m.Snapshot{}, m.Enumeration{}, badData
		}
		raw := make([]any, 14)
		classes := make([]string, 14)
		dest := make([]any, 28)
		for i := range raw {
			dest[i] = &raw[i]
			dest[14+i] = &classes[i]
		}
		if rows.Scan(dest...) != nil {
			return m.Snapshot{}, m.Enumeration{}, badData
		}
		row := m.Row{Generation: source}
		values := map[string]m.Value{}
		for i, col := range columns {
			want := strings.ToLower(col.DeclaredType)
			if classes[i] != want && !(classes[i] == "null" && !col.DeclaredNotNull && !col.PrimaryKey) {
				return m.Snapshot{}, m.Enumeration{}, badData
			}
			v, valid := actualValue(col.Name, classes[i], raw[i], &symbols)
			if !valid {
				return m.Snapshot{}, m.Enumeration{}, badData
			}
			values[col.Name] = v
			row.Cells = append(row.Cells, m.Cell{Column: col.Name, Value: v})
		}
		p := values["provider_kind"].Enum
		id := values["id"].Symbol
		found := false
		for _, spec := range m.Providers() {
			if spec.Provider == p {
				found = true
				row.Format = spec.Format
				row.AAD = m.AAD{Prefix: spec.AADPrefix, Object: id}
			}
		}
		if !found {
			return m.Snapshot{}, m.Enumeration{}, badData
		}
		row.Disposition = m.PlanReencrypt
		if values["archived"].Number == 1 {
			row.Format = m.Destroyed
			row.AAD = m.AAD{}
			row.Disposition = m.KeepArchivedFacts
		}
		snapshot.Rows = append(snapshot.Rows, row)
		index.Identities = append(index.Identities, m.Identity{ID: id, Provider: p})
	}
	if rows.Err() != nil {
		return m.Snapshot{}, m.Enumeration{}, badData
	}
	snapshot.DeclaredRows = uint32(len(snapshot.Rows))
	return snapshot, index, ok
}
