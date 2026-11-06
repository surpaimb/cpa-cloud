package service

// Independently authored synthetic-only shadow kernel. No production caller or
// constructor exists. See UPSTREAM_ROTATION_SHADOW_PROVENANCE.md and the contract.
import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

type upstreamShadowCode string

const (
	upstreamShadowOK        upstreamShadowCode = "UPSTREAM_SHADOW_SYNTHETIC_ONLY"
	upstreamShadowInput     upstreamShadowCode = "UPSTREAM_SHADOW_INPUT"
	upstreamShadowSchema    upstreamShadowCode = "UPSTREAM_SHADOW_SCHEMA"
	upstreamShadowRead      upstreamShadowCode = "UPSTREAM_SHADOW_ENUMERATION"
	upstreamShadowData      upstreamShadowCode = "UPSTREAM_SHADOW_DATA"
	upstreamShadowAuth      upstreamShadowCode = "UPSTREAM_SHADOW_AUTHENTICATION"
	upstreamShadowSeal      upstreamShadowCode = "UPSTREAM_SHADOW_ENCRYPTION_BOUNDARY"
	upstreamShadowNonce     upstreamShadowCode = "UPSTREAM_SHADOW_NONCE_CONFLICT"
	upstreamShadowWrite     upstreamShadowCode = "UPSTREAM_SHADOW_WRITE"
	upstreamShadowUncertain upstreamShadowCode = "UPSTREAM_SHADOW_COMMIT_UNCERTAIN"
	upstreamShadowReopen    upstreamShadowCode = "UPSTREAM_SHADOW_REOPEN"
	upstreamShadowVerify    upstreamShadowCode = "UPSTREAM_SHADOW_VERIFICATION"
	upstreamShadowClose     upstreamShadowCode = "UPSTREAM_SHADOW_CLEANUP"
	upstreamShadowCancel    upstreamShadowCode = "UPSTREAM_SHADOW_CANCELLED"
	upstreamShadowMaxRows                      = 256
	upstreamShadowMaxBlob                      = 1 << 20
)

// Only the new _test.go factory constructs this capability. The closure owns a
// single temporary candidate; the kernel has no path/DSN/config constructor.
// Private visibility is a scope boundary, not hostile-host provenance evidence.
type upstreamShadowFixture struct {
	mu       sync.Mutex
	used     bool
	db       *sql.DB
	reopen   func(context.Context) (*sql.DB, error)
	boundary upstreamShadowBoundary
}

// These optional private call boundaries exist only for explicitly labelled
// controlled-return/failure tests. Defaults use actual sql.Tx and the unchanged
// real encryption functions. They never replace crypto/rand's global source.
type upstreamShadowBoundary struct {
	query       func(context.Context, *sql.Tx, string) (*sql.Rows, error)
	beforeWrite func(context.Context, *sql.Tx, int) error
	seal        func(*secrets, upstreamShadowRow, []byte) ([]byte, error)
	commit      func(*sql.Tx) error
}

type upstreamShadowResult struct {
	classification upstreamShadowCode
	rows, live     int
	archived       int
}

type upstreamShadowRow struct {
	id, name, provider, endpoint, created    string
	enabled, version, revision, archived     int64
	archivedAt, state, verifiedAt, operation sql.NullString
	ciphertext                               []byte
}

func upstreamShadowColumns() []string {
	return []string{"id", "name", "provider_kind", "endpoint", "enabled", "credential_ciphertext", "key_version", "revision", "archived", "archived_at", "created_at", "credential_state", "verified_at", "operation_id"}
}

func upstreamShadowRootValid(root []byte) bool {
	if len(root) != 32 {
		return false
	}
	var nonzero byte
	for _, b := range root {
		nonzero |= b
	}
	return nonzero != 0
}

func upstreamShadowRun(ctx context.Context, f *upstreamShadowFixture, m1, m2 []byte) (out *upstreamShadowResult, code upstreamShadowCode) {
	if f == nil {
		return nil, upstreamShadowInput
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.used {
		return nil, upstreamShadowInput
	}
	f.used = true
	db := f.db
	f.db = nil
	var conn *sql.Conn
	var tx *sql.Tx
	code = upstreamShadowInput
	defer func() {
		failed := false
		if tx != nil {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				failed = true
			}
		}
		if conn != nil && conn.Close() != nil {
			failed = true
		}
		if db != nil && db.Close() != nil {
			failed = true
		}
		if code == upstreamShadowOK && ctx != nil && ctx.Err() != nil {
			code = upstreamShadowCancel
		}
		if code == upstreamShadowOK && failed {
			code = upstreamShadowClose
		}
		if code != upstreamShadowOK {
			out = nil
		}
	}()
	if db == nil || f.reopen == nil || !upstreamShadowRootValid(m1) || !upstreamShadowRootValid(m2) || bytes.Equal(m1, m2) {
		return nil, upstreamShadowInput
	}
	if ctx == nil || ctx.Err() != nil {
		return nil, upstreamShadowCancel
	}
	old, err := newSecrets(m1)
	if err != nil {
		return nil, upstreamShadowInput
	}
	defer upstreamShadowClearSecrets(old)
	next, err := newSecrets(m2)
	if err != nil {
		return nil, upstreamShadowInput
	}
	defer upstreamShadowClearSecrets(next)
	conn, err = db.Conn(ctx)
	if err != nil {
		return nil, upstreamShadowRead
	}
	var journal string
	if conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal) != nil || journal != "delete" {
		return nil, upstreamShadowInput
	}
	tx, err = conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, upstreamShadowRead
	}
	if code = upstreamShadowCheckSchema(ctx, tx); code != upstreamShadowOK {
		return nil, code
	}
	rows, code := upstreamShadowReadRows(ctx, tx, f.boundary.query)
	if code != upstreamShadowOK {
		return nil, code
	}
	plain := make([][]byte, len(rows))
	defer func() {
		for _, p := range plain {
			clear(p)
		}
	}()
	nonces := map[string]bool{}
	live, archived := 0, 0
	for i, row := range rows {
		if ctx.Err() != nil {
			return nil, upstreamShadowCancel
		}
		if row.archived == 1 {
			archived++
			continue
		}
		live++
		p, openErr := upstreamShadowOpen(old, row)
		if openErr != nil || len(p) == 0 {
			clear(p)
			return nil, upstreamShadowAuth
		}
		plain[i] = p
		var sealed []byte
		if f.boundary.seal != nil {
			sealed, err = f.boundary.seal(next, row, p)
		} else {
			sealed, err = upstreamShadowEncrypt(next, row, p)
		}
		if err != nil || len(sealed) < 29 || len(sealed) > upstreamShadowMaxBlob {
			return nil, upstreamShadowSeal
		}
		nonce := string(sealed[:next.aead.NonceSize()])
		if nonces[nonce] {
			return nil, upstreamShadowNonce
		}
		nonces[nonce] = true
		candidate := row
		candidate.ciphertext = sealed
		check, checkErr := upstreamShadowOpen(next, candidate)
		match := checkErr == nil && bytes.Equal(check, p)
		clear(check)
		if !match {
			return nil, upstreamShadowSeal
		}
		if f.boundary.beforeWrite != nil && f.boundary.beforeWrite(ctx, tx, i) != nil {
			return nil, upstreamShadowWrite
		}
		if ctx.Err() != nil {
			return nil, upstreamShadowCancel
		}
		result, writeErr := tx.ExecContext(ctx, `UPDATE main.upstreams SET credential_ciphertext=? WHERE id=? AND provider_kind=? AND credential_ciphertext=? AND key_version=? AND archived=0`, sealed, row.id, row.provider, row.ciphertext, row.version)
		if writeErr != nil {
			return nil, upstreamShadowWrite
		}
		n, affectedErr := result.RowsAffected()
		if affectedErr != nil || n != 1 {
			return nil, upstreamShadowWrite
		}
	}
	if code = upstreamShadowVerifyRows(ctx, tx, rows, plain, old, next); code != upstreamShadowOK {
		return nil, code
	}
	if ctx.Err() != nil {
		return nil, upstreamShadowCancel
	}
	if f.boundary.commit != nil {
		err = f.boundary.commit(tx)
	} else {
		err = tx.Commit()
	}
	if err != nil {
		return nil, upstreamShadowUncertain
	}
	// A boundary returning nil without actually completing its Tx cannot publish
	// success. ErrTxDone is normal after the actual successful Commit above.
	if !errors.Is(tx.Rollback(), sql.ErrTxDone) {
		return nil, upstreamShadowUncertain
	}
	tx = nil
	if conn.Close() != nil {
		conn = nil
		return nil, upstreamShadowClose
	}
	conn = nil
	if db.Close() != nil {
		db = nil
		return nil, upstreamShadowClose
	}
	db = nil
	if ctx.Err() != nil {
		return nil, upstreamShadowCancel
	}
	db, err = f.reopen(ctx)
	if err != nil || db == nil {
		return nil, upstreamShadowReopen
	}
	conn, err = db.Conn(ctx)
	if err != nil {
		return nil, upstreamShadowReopen
	}
	if conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal) != nil || journal != "delete" {
		return nil, upstreamShadowVerify
	}
	if _, err = conn.ExecContext(ctx, "PRAGMA query_only=ON"); err != nil {
		return nil, upstreamShadowReopen
	}
	tx, err = conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, upstreamShadowReopen
	}
	if code = upstreamShadowCheckSchema(ctx, tx); code != upstreamShadowOK {
		return nil, upstreamShadowVerify
	}
	if code = upstreamShadowVerifyRows(ctx, tx, rows, plain, old, next); code != upstreamShadowOK {
		return nil, code
	}
	if tx.Rollback() != nil {
		tx = nil
		return nil, upstreamShadowClose
	}
	tx = nil
	return &upstreamShadowResult{classification: upstreamShadowOK, rows: len(rows), live: live, archived: archived}, upstreamShadowOK
}

func upstreamShadowClearSecrets(s *secrets) {
	clear(s.digestKey)
	clear(s.responseFingerprintKey)
	// Go cipher state and immutable strings are not a comprehensive erasure API.
}

func upstreamShadowOpen(s *secrets, row upstreamShadowRow) ([]byte, error) {
	switch row.provider {
	case "openai-compatible", "anthropic-api-key":
		p, err := s.decryptCredential(row.id, row.ciphertext)
		return []byte(p), err
	case "gemini-api-key":
		p, err := s.decryptGeminiAPIKey(row.id, row.ciphertext)
		return []byte(p), err
	case "codex-membership":
		return s.decryptCodexAuth(row.id, row.ciphertext)
	}
	return nil, errors.New("synthetic shadow provider refused")
}

func upstreamShadowEncrypt(s *secrets, row upstreamShadowRow, p []byte) ([]byte, error) {
	switch row.provider {
	case "openai-compatible", "anthropic-api-key":
		return s.encryptCredential(row.id, string(p))
	case "gemini-api-key":
		return s.encryptGeminiAPIKey(row.id, string(p))
	case "codex-membership":
		return s.encryptCodexAuth(row.id, p)
	}
	return nil, errors.New("synthetic shadow provider refused")
}

func upstreamShadowCheckSchema(ctx context.Context, tx *sql.Tx) upstreamShadowCode {
	var kind string
	if tx.QueryRowContext(ctx, `SELECT type FROM main.sqlite_schema WHERE name='upstreams'`).Scan(&kind) != nil || kind != "table" {
		return upstreamShadowSchema
	}
	var triggers int
	if tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM main.sqlite_schema WHERE type='trigger')+(SELECT count(*) FROM temp.sqlite_schema WHERE type='trigger')`).Scan(&triggers) != nil || triggers != 0 {
		return upstreamShadowSchema
	}
	// A ciphertext update must have no trigger/cascade path into another table.
	// Names are bound to a pragma function, never interpolated as executable SQL.
	tables, err := tx.QueryContext(ctx, `SELECT name FROM main.sqlite_schema WHERE type='table'`)
	if err != nil {
		return upstreamShadowSchema
	}
	var tableNames []string
	for tables.Next() {
		var name string
		if tables.Scan(&name) != nil {
			tables.Close()
			return upstreamShadowSchema
		}
		tableNames = append(tableNames, name)
	}
	tableErr, tableCloseErr := tables.Err(), tables.Close()
	if tableErr != nil || tableCloseErr != nil {
		return upstreamShadowSchema
	}
	for _, name := range tableNames {
		var inbound int
		// REFERENCES preserves spelling; match SQLite's ASCII case folding,
		// not Go Unicode EqualFold. https://sqlite.org/datatype3.html#collation
		if tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_foreign_key_list(?,'main') WHERE "table" COLLATE NOCASE='upstreams'`, name).Scan(&inbound) != nil || inbound != 0 {
			return upstreamShadowSchema
		}
	}
	var tableKind string
	var ncol, wr, strict int
	if tx.QueryRowContext(ctx, `SELECT type,ncol,wr,strict FROM pragma_table_list WHERE schema='main' AND name='upstreams'`).Scan(&tableKind, &ncol, &wr, &strict) != nil || tableKind != "table" || ncol != 14 || wr != 0 || strict != 0 {
		return upstreamShadowSchema
	}
	r, err := tx.QueryContext(ctx, `SELECT name,type,"notnull",dflt_value,pk,hidden FROM pragma_table_xinfo('upstreams','main')`)
	if err != nil {
		return upstreamShadowSchema
	}
	seen := map[string]bool{}
	columns := upstreamShadowColumns()
	for r.Next() {
		var name, declared string
		var nn, pk, hidden int
		var dflt sql.NullString
		if r.Scan(&name, &declared, &nn, &dflt, &pk, &hidden) != nil || hidden != 0 || seen[name] {
			r.Close()
			return upstreamShadowSchema
		}
		index := -1
		for i, n := range columns {
			if n == name {
				index = i
			}
		}
		if index < 0 {
			r.Close()
			return upstreamShadowSchema
		}
		wantType, wantNN, wantPK := "TEXT", 1, 0
		if index == 4 || index == 6 || index == 7 || index == 8 {
			wantType = "INTEGER"
		}
		if index == 5 {
			wantType = "BLOB"
		}
		if index == 0 || index == 9 || index == 11 || index == 12 || index == 13 {
			wantNN = 0
		}
		if index == 0 {
			wantPK = 1
		}
		if !strings.EqualFold(declared, wantType) || nn != wantNN || pk != wantPK || (index == 8 && (!dflt.Valid || dflt.String != "0")) || (index != 8 && dflt.Valid) {
			r.Close()
			return upstreamShadowSchema
		}
		seen[name] = true
	}
	iterationErr, closeErr := r.Err(), r.Close()
	if iterationErr != nil || closeErr != nil || len(seen) != 14 {
		return upstreamShadowSchema
	}
	r, err = tx.QueryContext(ctx, `SELECT name,"unique",origin,partial FROM pragma_index_list('upstreams','main')`)
	if err != nil {
		return upstreamShadowSchema
	}
	type shadowIndex struct{ name, origin string }
	var indexes []shadowIndex
	origins := map[string]bool{}
	for r.Next() {
		var name, origin string
		var unique, partial int
		if r.Scan(&name, &unique, &origin, &partial) != nil || unique != 1 || partial != 0 || origins[origin] || (origin != "pk" && origin != "u") {
			r.Close()
			return upstreamShadowSchema
		}
		origins[origin] = true
		indexes = append(indexes, shadowIndex{name, origin})
	}
	iterationErr, closeErr = r.Err(), r.Close()
	if iterationErr != nil || closeErr != nil || len(indexes) != 2 {
		return upstreamShadowSchema
	}
	for _, index := range indexes {
		want := "id"
		if index.origin == "u" {
			want = "operation_id"
		}
		r, err = tx.QueryContext(ctx, `SELECT cid,name,"desc",coll,"key" FROM pragma_index_xinfo(?,'main')`, index.name)
		if err != nil {
			return upstreamShadowSchema
		}
		keys, aux := 0, 0
		for r.Next() {
			var cid, desc, key int
			var name sql.NullString
			var coll string
			if r.Scan(&cid, &name, &desc, &coll, &key) != nil || desc != 0 || coll != "BINARY" {
				r.Close()
				return upstreamShadowSchema
			}
			if key == 1 {
				if cid < 0 || !name.Valid || name.String != want {
					r.Close()
					return upstreamShadowSchema
				}
				keys++
			} else {
				if key != 0 || cid != -1 || name.Valid {
					r.Close()
					return upstreamShadowSchema
				}
				aux++
			}
		}
		iterationErr, closeErr = r.Err(), r.Close()
		if iterationErr != nil || closeErr != nil || keys != 1 || aux != 1 {
			return upstreamShadowSchema
		}
	}
	// Row validation independently enforces the bounded supported state subset;
	// this is not PR92's complete frozen-DDL grammar/provenance verifier.
	return upstreamShadowOK
}

func upstreamShadowReadRows(ctx context.Context, tx *sql.Tx, query func(context.Context, *sql.Tx, string) (*sql.Rows, error)) (out []upstreamShadowRow, code upstreamShadowCode) {
	if ctx.Err() != nil {
		return nil, upstreamShadowCancel
	}
	var count, ids, uniqueIDs, ops, uniqueOps, totalBlob, maxBlob int64
	if tx.QueryRowContext(ctx, `SELECT count(*),count(id),count(DISTINCT id),count(operation_id),count(DISTINCT operation_id),coalesce(sum(length(credential_ciphertext)),0),coalesce(max(length(credential_ciphertext)),0) FROM main.upstreams`).Scan(&count, &ids, &uniqueIDs, &ops, &uniqueOps, &totalBlob, &maxBlob) != nil {
		return nil, upstreamShadowRead
	}
	if count < 0 || count > upstreamShadowMaxRows || ids != count || uniqueIDs != count || uniqueOps != ops || totalBlob > 8<<20 || maxBlob > upstreamShadowMaxBlob {
		return nil, upstreamShadowData
	}
	columns := upstreamShadowColumns()
	var names, selects, limits []string
	for _, name := range columns {
		names = append(names, `"`+name+`"`)
	}
	selects = append(selects, names...)
	for _, name := range names {
		selects = append(selects, "typeof("+name+")")
		limits = append(limits, "coalesce(max(length(cast("+name+" AS BLOB))),0)")
	}
	lengths := make([]int64, 14)
	lengthDest := make([]any, 14)
	for i := range lengths {
		lengthDest[i] = &lengths[i]
	}
	if tx.QueryRowContext(ctx, "SELECT "+strings.Join(limits, ",")+" FROM main.upstreams").Scan(lengthDest...) != nil {
		return nil, upstreamShadowRead
	}
	for i, n := range lengths {
		limit := int64(4096)
		if i == 5 {
			limit = upstreamShadowMaxBlob
		}
		if n < 0 || n > limit {
			return nil, upstreamShadowData
		}
	}
	q := "SELECT " + strings.Join(selects, ",") + ` FROM main.upstreams ORDER BY "id"`
	var r *sql.Rows
	var err error
	if query != nil {
		r, err = query(ctx, tx, q)
	} else {
		r, err = tx.QueryContext(ctx, q)
	}
	if err != nil || r == nil {
		return nil, upstreamShadowRead
	}
	code = upstreamShadowOK
	defer func() {
		if r.Close() != nil && code == upstreamShadowOK {
			code = upstreamShadowClose
		}
		if code != upstreamShadowOK {
			out = nil
		}
	}()
	seenIDs, seenOperations := map[string]bool{}, map[string]bool{}
	for r.Next() {
		if len(out) >= upstreamShadowMaxRows {
			return nil, upstreamShadowData
		}
		var raw [14]any
		var classes [14]string
		dest := make([]any, 28)
		for i := range raw {
			dest[i] = &raw[i]
			dest[14+i] = &classes[i]
		}
		if r.Scan(dest...) != nil {
			return nil, upstreamShadowRead
		}
		row, valid := upstreamShadowDecode(raw, classes)
		if !valid {
			return nil, upstreamShadowData
		}
		if seenIDs[row.id] || (row.operation.Valid && seenOperations[row.operation.String]) {
			return nil, upstreamShadowRead
		}
		seenIDs[row.id] = true
		if row.operation.Valid {
			seenOperations[row.operation.String] = true
		}
		out = append(out, row)
	}
	if r.Err() != nil {
		if ctx.Err() != nil {
			return nil, upstreamShadowCancel
		}
		return nil, upstreamShadowRead
	}
	if r.Close() != nil {
		return nil, upstreamShadowClose
	}
	if int64(len(out)) != count {
		return nil, upstreamShadowRead
	}
	var after int64
	if tx.QueryRowContext(ctx, "SELECT count(*) FROM main.upstreams").Scan(&after) != nil || after != count {
		return nil, upstreamShadowRead
	}
	var providerTotal int64
	for _, provider := range []string{"openai-compatible", "anthropic-api-key", "gemini-api-key", "codex-membership"} {
		var actual int64
		if tx.QueryRowContext(ctx, "SELECT count(*) FROM main.upstreams WHERE provider_kind=?", provider).Scan(&actual) != nil {
			return nil, upstreamShadowRead
		}
		observed := int64(0)
		for _, row := range out {
			if row.provider == provider {
				observed++
			}
		}
		if actual != observed {
			return nil, upstreamShadowRead
		}
		providerTotal += actual
	}
	if providerTotal != count {
		return nil, upstreamShadowRead
	}
	return out, upstreamShadowOK
}

func upstreamShadowDecode(raw [14]any, classes [14]string) (r upstreamShadowRow, valid bool) {
	text := func(i int, nullable bool) (sql.NullString, bool) {
		if classes[i] == "null" {
			return sql.NullString{}, nullable && raw[i] == nil
		}
		v, ok := raw[i].(string)
		return sql.NullString{String: v, Valid: true}, ok && classes[i] == "text" && v != "" && len(v) <= 4096 && utf8.ValidString(v) && !strings.ContainsRune(v, 0)
	}
	for i, dest := range map[int]*string{0: &r.id, 1: &r.name, 2: &r.provider, 3: &r.endpoint, 10: &r.created} {
		v, ok := text(i, false)
		if !ok {
			return r, false
		}
		*dest = v.String
	}
	for i, dest := range map[int]*sql.NullString{9: &r.archivedAt, 11: &r.state, 12: &r.verifiedAt, 13: &r.operation} {
		v, ok := text(i, true)
		if !ok {
			return r, false
		}
		*dest = v
	}
	for i, dest := range map[int]*int64{4: &r.enabled, 6: &r.version, 7: &r.revision, 8: &r.archived} {
		v, ok := raw[i].(int64)
		if !ok || classes[i] != "integer" {
			return r, false
		}
		*dest = v
	}
	b, ok := raw[5].([]byte)
	if !ok || classes[5] != "blob" || len(b) > upstreamShadowMaxBlob {
		return r, false
	}
	r.ciphertext = append([]byte{}, b...)
	if r.enabled < 0 || r.enabled > 1 || r.archived < 0 || r.archived > 1 || r.revision < 1 || r.revision > 9007199254740991 {
		return r, false
	}
	for _, stamp := range []sql.NullString{{String: r.created, Valid: true}, r.archivedAt, r.verifiedAt} {
		if stamp.Valid {
			if _, err := time.Parse(time.RFC3339Nano, stamp.String); err != nil {
				return r, false
			}
		}
	}
	wantVersion := int64(0)
	switch r.provider {
	case "openai-compatible", "anthropic-api-key":
		wantVersion = 1
	case "gemini-api-key":
		wantVersion = 2
	case "codex-membership":
		wantVersion = 2
	default:
		return r, false
	}
	if r.provider == "codex-membership" {
		if !r.state.Valid || !r.operation.Valid || (r.state.String != "imported_unverified" && r.state.String != "verified" && r.state.String != "reauth_required") {
			return r, false
		}
	} else if r.state.Valid || r.operation.Valid || r.verifiedAt.Valid {
		return r, false
	}
	if r.archived == 1 {
		if r.enabled != 0 || !r.archivedAt.Valid || r.version != 0 || len(r.ciphertext) != 0 || r.verifiedAt.Valid || (r.provider == "codex-membership" && r.state.String != "reauth_required") {
			return r, false
		}
	} else if r.archivedAt.Valid || r.version != wantVersion || len(r.ciphertext) < 29 {
		return r, false
	}
	return r, true
}

func upstreamShadowFactsEqual(a, b upstreamShadowRow) bool {
	return a.id == b.id && a.name == b.name && a.provider == b.provider && a.endpoint == b.endpoint && a.created == b.created && a.enabled == b.enabled && a.version == b.version && a.revision == b.revision && a.archived == b.archived && a.archivedAt == b.archivedAt && a.state == b.state && a.verifiedAt == b.verifiedAt && a.operation == b.operation
}

func upstreamShadowVerifyRows(ctx context.Context, tx *sql.Tx, before []upstreamShadowRow, plain [][]byte, old, next *secrets) upstreamShadowCode {
	after, code := upstreamShadowReadRows(ctx, tx, nil)
	if code != upstreamShadowOK {
		return code
	}
	if len(after) != len(before) {
		return upstreamShadowVerify
	}
	for i, row := range after {
		if !upstreamShadowFactsEqual(before[i], row) {
			return upstreamShadowVerify
		}
		if row.archived == 1 {
			if !bytes.Equal(row.ciphertext, before[i].ciphertext) {
				return upstreamShadowVerify
			}
			continue
		}
		p, err := upstreamShadowOpen(next, row)
		same := err == nil && bytes.Equal(p, plain[i])
		clear(p)
		if !same {
			return upstreamShadowVerify
		}
		p, err = upstreamShadowOpen(old, row)
		clear(p)
		if err == nil {
			return upstreamShadowVerify
		}
	}
	return upstreamShadowOK
}
