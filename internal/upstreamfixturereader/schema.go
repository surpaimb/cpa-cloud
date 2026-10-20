package upstreamfixturereader

import (
	"context"
	"database/sql"
	"strings"

	m "cpacloud.local/server/internal/upstreammanifest"
)

// Strict frozen-profile grammar, not a generic SQL parser. Literal contents
// are preserved; whitespace/case normalization occurs outside literals only.
func tokens(s string) ([]string, bool) {
	if len(s) > 16384 {
		return nil, false
	}
	var out []string
	for i := 0; i < len(s); {
		c := s[i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == '\f' {
			i++
			continue
		}
		if c == '\'' {
			start := i
			i++
			closed := false
			for i < len(s) {
				if s[i] == '\'' {
					i++
					if i < len(s) && s[i] == '\'' {
						i++
						continue
					}
					closed = true
					break
				}
				i++
			}
			if !closed {
				return nil, false
			}
			out = append(out, s[start:i])
			continue
		}
		if c == '"' {
			i++
			start := i
			for i < len(s) && s[i] != '"' {
				if !word(s[i]) {
					return nil, false
				}
				i++
			}
			if i == len(s) || i == start {
				return nil, false
			}
			out = append(out, strings.ToLower(s[start:i]))
			i++
			continue
		}
		if word(c) {
			start := i
			for i < len(s) && word(s[i]) {
				i++
			}
			out = append(out, strings.ToLower(s[start:i]))
			continue
		}
		if strings.ContainsRune("(),=", rune(c)) {
			out = append(out, string(c))
			i++
			continue
		}
		return nil, false
	}
	return out, true
}
func word(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_'
}
func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func definitions() map[string]string {
	return map[string]string{
		"id":                    "id TEXT PRIMARY KEY",
		"name":                  "name TEXT NOT NULL",
		"provider_kind":         "provider_kind TEXT NOT NULL CHECK(provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key','codex-membership'))",
		"endpoint":              "endpoint TEXT NOT NULL",
		"enabled":               "enabled INTEGER NOT NULL",
		"credential_ciphertext": "credential_ciphertext BLOB NOT NULL",
		"key_version":           "key_version INTEGER NOT NULL",
		"revision":              "revision INTEGER NOT NULL",
		"archived":              "archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1))",
		"archived_at":           "archived_at TEXT",
		"created_at":            "created_at TEXT NOT NULL",
		"credential_state":      "credential_state TEXT CHECK(credential_state IS NULL OR credential_state IN ('imported_unverified','verified','reauth_required'))",
		"verified_at":           "verified_at TEXT",
		"operation_id":          "operation_id TEXT UNIQUE",
	}
}

const stateConstraint = `CHECK((provider_kind IN ('openai-compatible','anthropic-api-key','gemini-api-key') AND credential_state IS NULL AND verified_at IS NULL AND operation_id IS NULL) OR (provider_kind='codex-membership' AND credential_state IS NOT NULL AND operation_id IS NOT NULL))`

func knownDDL(ddl string) bool {
	t, valid := tokens(ddl)
	if !valid || len(t) < 6 || !equal(t[:4], []string{"create", "table", "upstreams", "("}) || t[len(t)-1] != ")" {
		return false
	}
	var parts [][]string
	start, depth := 4, 0
	for i := 4; i < len(t)-1; i++ {
		switch t[i] {
		case "(":
			depth++
		case ")":
			depth--
			if depth < 0 {
				return false
			}
		case ",":
			if depth == 0 {
				parts = append(parts, t[start:i])
				start = i + 1
			}
		}
	}
	if depth != 0 {
		return false
	}
	parts = append(parts, t[start:len(t)-1])
	if len(parts) != 15 {
		return false
	}
	defs := definitions()
	seen := map[string]bool{}
	checks := 0
	for _, p := range parts {
		if len(p) == 0 {
			return false
		}
		if p[0] == "check" {
			want, _ := tokens(stateConstraint)
			if !equal(p, want) {
				return false
			}
			checks++
			continue
		}
		definition, found := defs[p[0]]
		if !found || seen[p[0]] {
			return false
		}
		want, _ := tokens(definition)
		if !equal(p, want) {
			return false
		}
		seen[p[0]] = true
	}
	return len(seen) == 14 && checks == 1
}

func checkSchema(ctx context.Context, tx *sql.Tx) code {
	if c := checkObjects(ctx, tx); c != ok {
		return c
	}
	if c := checkTable(ctx, tx); c != ok {
		return c
	}
	if c := checkColumns(ctx, tx); c != ok {
		return c
	}
	if c := checkIndexes(ctx, tx); c != ok {
		return c
	}
	if c := checkIntegrity(ctx, tx); c != ok {
		return c
	}
	return ok
}

func checkObjects(ctx context.Context, tx *sql.Tx) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `SELECT type,name,tbl_name,sql FROM main.sqlite_schema`)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	tables, indexes := 0, 0
	for r.Next() {
		var kind, name, table string
		var ddl sql.NullString
		if r.Scan(&kind, &name, &table, &ddl) != nil || table != "upstreams" {
			return badSchema
		}
		switch kind {
		case "table":
			if name != "upstreams" || !ddl.Valid || !knownDDL(ddl.String) {
				return badSchema
			}
			tables++
		case "index":
			if ddl.Valid || !strings.HasPrefix(name, "sqlite_autoindex_upstreams_") {
				return badSchema
			}
			indexes++
		default:
			return badSchema
		}
		if tables > 1 || indexes > 2 {
			return badSchema
		}
	}
	if r.Err() != nil || tables != 1 || indexes != 2 {
		return badSchema
	}
	return ok
}

func checkTable(ctx context.Context, tx *sql.Tx) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `PRAGMA main.table_list`)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	seen := 0
	for r.Next() {
		var schema, name, kind string
		var ncol, wr, strict int
		if r.Scan(&schema, &name, &kind, &ncol, &wr, &strict) != nil {
			return badSchema
		}
		if name == "upstreams" {
			if schema != "main" || kind != "table" || ncol != 14 || wr != 0 || strict != 0 {
				return badSchema
			}
			seen++
		}
	}
	if r.Err() != nil || seen != 1 {
		return badSchema
	}
	return ok
}

func checkColumns(ctx context.Context, tx *sql.Tx) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `SELECT name,type,"notnull",dflt_value,pk,hidden FROM pragma_table_xinfo('upstreams','main')`)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	seen := map[string]bool{}
	for r.Next() {
		var name, kind string
		var nn, pk, hidden int
		var dflt sql.NullString
		if r.Scan(&name, &kind, &nn, &dflt, &pk, &hidden) != nil || hidden != 0 || seen[name] {
			return badSchema
		}
		found := false
		for _, spec := range m.Columns() {
			if spec.Name == name {
				found = true
				wantNN, wantPK := 0, 0
				if spec.DeclaredNotNull {
					wantNN = 1
				}
				if spec.PrimaryKey {
					wantPK = 1
				}
				if !strings.EqualFold(kind, spec.DeclaredType) || nn != wantNN || pk != wantPK {
					return badSchema
				}
				if name == "archived" {
					if !dflt.Valid || dflt.String != "0" {
						return badSchema
					}
				} else if dflt.Valid {
					return badSchema
				}
			}
		}
		if !found {
			return badSchema
		}
		seen[name] = true
	}
	if r.Err() != nil || len(seen) != 14 {
		return badSchema
	}
	return ok
}

func checkIndexes(ctx context.Context, tx *sql.Tx) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `SELECT name,"unique",origin,partial FROM pragma_index_list('upstreams','main')`)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	type index struct{ name, origin string }
	var indexes []index
	seen := map[string]bool{}
	for r.Next() {
		var name, origin string
		var unique, partial int
		if r.Scan(&name, &unique, &origin, &partial) != nil || unique != 1 || partial != 0 || seen[origin] || (origin != "pk" && origin != "u") {
			return badSchema
		}
		seen[origin] = true
		indexes = append(indexes, index{name, origin})
	}
	if r.Err() != nil || len(indexes) != 2 {
		return badSchema
	}
	if r.Close() != nil {
		return badClose
	}
	for _, idx := range indexes {
		want := "id"
		if idx.origin == "u" {
			want = "operation_id"
		}
		if c = checkIndex(ctx, tx, idx.name, want); c != ok {
			return c
		}
	}
	return ok
}

func checkIndex(ctx context.Context, tx *sql.Tx, name, want string) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `SELECT cid,name,"desc",coll,"key" FROM pragma_index_xinfo(?,'main')`, name)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	keys, aux := 0, 0
	for r.Next() {
		var cid, desc, key int
		var col sql.NullString
		var coll string
		if r.Scan(&cid, &col, &desc, &coll, &key) != nil || desc != 0 || coll != "BINARY" {
			return badSchema
		}
		if key == 1 {
			if cid < 0 || !col.Valid || col.String != want {
				return badSchema
			}
			keys++
		} else {
			if key != 0 || cid != -1 || col.Valid {
				return badSchema
			}
			aux++
		}
	}
	if r.Err() != nil || keys != 1 || aux != 1 {
		return badSchema
	}
	return ok
}

func checkIntegrity(ctx context.Context, tx *sql.Tx) (c code) {
	c = ok
	r, err := tx.QueryContext(ctx, `PRAGMA main.integrity_check`)
	if err != nil {
		return badSchema
	}
	defer closeRows(r, &c)
	n := 0
	for r.Next() {
		var result string
		if r.Scan(&result) != nil || result != "ok" {
			return badSchema
		}
		n++
		if n > 1 {
			return badSchema
		}
	}
	if r.Err() != nil || n != 1 {
		return badSchema
	}
	return ok
}
