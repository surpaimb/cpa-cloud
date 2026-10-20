// Package upstreammanifest maps public symbolic fixtures, not databases, to a
// single-table migration plan. No crypto, I/O, adapter or activation is provided.
// Independently authored: see PROVENANCE.md and the source-bound contract.
package upstreammanifest

const (
	SourceHead  = "dbe85f714628792f9a14254912c4a3deaf8c0017"
	Table       = "upstreams"
	Profile     = "upstreams-post-migration-dbe85f7/v1"
	MaxRows     = 256
	maxRevision = 9007199254740991
)

type Code string

const (
	OK                 Code = "UPSTREAM_MANIFEST_OK"
	InvalidScope       Code = "UPSTREAM_MANIFEST_SCOPE"
	InvalidColumns     Code = "UPSTREAM_MANIFEST_COLUMNS"
	InvalidEnumeration Code = "UPSTREAM_MANIFEST_ENUMERATION"
	InvalidRow         Code = "UPSTREAM_MANIFEST_ROW"
	InvalidBinding     Code = "UPSTREAM_MANIFEST_BINDING"
	InvalidDisposition Code = "UPSTREAM_MANIFEST_DISPOSITION"
)

// Symbol is identity of a public synthetic value, never actual field contents.
type Symbol uint64
type Kind uint8

const (
	Null Kind = iota
	TextSymbol
	TimestampSymbol
	BlobSymbol
	EmptyBlob
	Integer
	Enum
)

// Value is a strict tagged union. Only fixed public enums have string contents;
// names, endpoints, IDs, timestamps and ciphertext are nonzero integer symbols.
type Value struct {
	Kind   Kind
	Symbol Symbol
	Number int64
	Enum   string
}
type Cell struct {
	Column string
	Value  Value
}

type ColumnSpec struct {
	Name, DeclaredType                  string
	DeclaredNotNull, PrimaryKey, Unique bool
	SourceLine                          int
}

// Columns is a new slice on each call. Order is an output convention, not a
// SQLite physical-column order: the legacy rebuild appends archive columns.
func Columns() []ColumnSpec {
	return []ColumnSpec{
		{"id", "TEXT", false, true, false, 107},
		{"name", "TEXT", true, false, false, 108},
		{"provider_kind", "TEXT", true, false, false, 109},
		{"endpoint", "TEXT", true, false, false, 110},
		{"enabled", "INTEGER", true, false, false, 111},
		{"credential_ciphertext", "BLOB", true, false, false, 112},
		{"key_version", "INTEGER", true, false, false, 113},
		{"revision", "INTEGER", true, false, false, 114},
		{"archived", "INTEGER", true, false, false, 115},
		{"archived_at", "TEXT", false, false, false, 116},
		{"created_at", "TEXT", true, false, false, 117},
		{"credential_state", "TEXT", false, false, false, 118},
		{"verified_at", "TEXT", false, false, false, 119},
		{"operation_id", "TEXT", false, false, true, 120},
	}
}

type Format string

const (
	APIv1     Format = "api-key/nonce-gcm/v1"
	GeminiV2  Format = "gemini-api-key/nonce-gcm/v2"
	CodexV2   Format = "codex-auth-json/nonce-gcm/v2"
	Destroyed Format = "archived/empty-blob/key-version-0"
)

type ProviderSpec struct {
	Provider, ConsumerID string
	KeyVersion           int64
	Format               Format
	AADPrefix            string
}

func Providers() []ProviderSpec {
	return []ProviderSpec{
		{"openai-compatible", "upstream-api", 1, APIv1, "cpacloud/upstream-api-key/v1\x00"},
		{"anthropic-api-key", "upstream-api", 1, APIv1, "cpacloud/upstream-api-key/v1\x00"},
		{"gemini-api-key", "gemini-api", 2, GeminiV2, "cpacloud/upstream-credential/v2\x00gemini-api-key\x00api-key\x00"},
		{"codex-membership", "codex-auth", 2, CodexV2, "cpacloud/upstream-credential/v2\x00codex-membership\x00auth-json\x00"},
	}
}

// AAD represents the source prefix + opaque object-ID binding, not AEAD bytes.
type AAD struct {
	Prefix string
	Object Symbol
}
type Disposition uint8

const (
	Unresolved Disposition = iota
	PlanReencrypt
	KeepArchivedFacts
)

type Row struct {
	Cells       []Cell
	Generation  uint64
	Format      Format
	AAD         AAD
	Disposition Disposition
}

type Snapshot struct {
	SourceHead, Table, Profile         string
	SourceGeneration, TargetGeneration uint64
	Columns                            []string
	DeclaredRows                       uint32
	Rows                               []Row
}

// Enumeration is a separately supplied public fixture index. Agreement with it
// proves only internal synthetic-set consistency, never actual DB completeness.
type Identity struct {
	ID       Symbol
	Provider string
}
type ProviderCount struct {
	Provider string
	Rows     uint32
}
type Enumeration struct {
	Identities []Identity
	Counts     []ProviderCount
}

type FieldAction uint8

const (
	PreserveFact FieldAction = iota + 1
	PlanM2Ciphertext
	PreserveEncodingVersion
)

// SourceValue always denotes the source value; no replacement ciphertext is
// generated or authenticated by this package.
type ColumnPlan struct {
	Column      string
	SourceValue Value
	Action      FieldAction
}
type Entry struct {
	ID                                 Symbol
	Provider, ConsumerID               string
	SourceGeneration, TargetGeneration uint64
	Format                             Format
	AAD                                AAD
	Disposition                        Disposition
	Fields                             []ColumnPlan
}
type Manifest struct {
	SourceHead, Table, Profile         string
	SourceGeneration, TargetGeneration uint64
	Entries                            []Entry
	Counts                             []ProviderCount
}

func provider(name string) (ProviderSpec, bool) {
	for _, p := range Providers() {
		if p.Provider == name {
			return p, true
		}
	}
	return ProviderSpec{}, false
}

func validColumns(names []string) bool {
	if len(names) != len(Columns()) {
		return false
	}
	seen := map[string]bool{}
	for _, n := range names {
		found := false
		for _, c := range Columns() {
			if c.Name == n {
				found = true
			}
		}
		if !found || seen[n] {
			return false
		}
		seen[n] = true
	}
	return true
}

func canonical(v Value) bool {
	switch v.Kind {
	case Null, EmptyBlob:
		return v.Symbol == 0 && v.Number == 0 && v.Enum == ""
	case TextSymbol, TimestampSymbol, BlobSymbol:
		return v.Symbol != 0 && v.Number == 0 && v.Enum == ""
	case Integer:
		return v.Symbol == 0 && v.Enum == ""
	case Enum:
		return v.Symbol == 0 && v.Number == 0 && v.Enum != ""
	}
	return false
}

func rowValues(r Row) (map[string]Value, Code) {
	names := make([]string, 0, len(r.Cells))
	for _, c := range r.Cells {
		names = append(names, c.Column)
	}
	if !validColumns(names) {
		return nil, InvalidColumns
	}
	values := map[string]Value{}
	for _, c := range r.Cells {
		if !canonical(c.Value) {
			return nil, InvalidRow
		}
		values[c.Column] = c.Value
	}
	for _, name := range []string{"id", "name", "endpoint"} {
		if values[name].Kind != TextSymbol {
			return nil, InvalidRow
		}
	}
	if values["created_at"].Kind != TimestampSymbol || values["provider_kind"].Kind != Enum {
		return nil, InvalidRow
	}
	for _, name := range []string{"enabled", "key_version", "revision", "archived"} {
		if values[name].Kind != Integer {
			return nil, InvalidRow
		}
	}
	for _, name := range []string{"archived_at", "verified_at"} {
		if values[name].Kind != Null && values[name].Kind != TimestampSymbol {
			return nil, InvalidRow
		}
	}
	if values["operation_id"].Kind != Null && values["operation_id"].Kind != TextSymbol {
		return nil, InvalidRow
	}
	if values["enabled"].Number < 0 || values["enabled"].Number > 1 || values["archived"].Number < 0 || values["archived"].Number > 1 ||
		values["revision"].Number < 1 || values["revision"].Number > maxRevision {
		return nil, InvalidRow
	}
	p, known := provider(values["provider_kind"].Enum)
	if !known {
		return nil, InvalidRow
	}
	if p.Provider == "codex-membership" {
		state := values["credential_state"]
		if state.Kind != Enum || (state.Enum != "imported_unverified" && state.Enum != "verified" && state.Enum != "reauth_required") ||
			values["operation_id"].Kind != TextSymbol {
			return nil, InvalidRow
		}
	} else if values["credential_state"].Kind != Null || values["verified_at"].Kind != Null || values["operation_id"].Kind != Null {
		return nil, InvalidRow
	}
	if values["archived"].Number == 1 {
		if values["archived_at"].Kind != TimestampSymbol || values["enabled"].Number != 0 ||
			values["credential_ciphertext"].Kind != EmptyBlob || values["key_version"].Number != 0 ||
			values["verified_at"].Kind != Null || (p.Provider == "codex-membership" && values["credential_state"].Enum != "reauth_required") {
			return nil, InvalidRow
		}
	} else if values["archived_at"].Kind != Null || values["credential_ciphertext"].Kind != BlobSymbol || values["key_version"].Number != p.KeyVersion {
		return nil, InvalidRow
	}
	return values, OK
}

// Build fails without partial output. No completeness boolean or operational
// commit/serve/restore method exists. Generation tags are fixture metadata, not
// derived from key_version or authenticated against an application root.
func Build(s Snapshot, expected Enumeration) (*Manifest, Code) {
	if s.SourceHead != SourceHead || s.Table != Table || s.Profile != Profile || s.SourceGeneration == 0 ||
		s.TargetGeneration <= s.SourceGeneration || s.TargetGeneration-s.SourceGeneration != 1 {
		return nil, InvalidScope
	}
	if !validColumns(s.Columns) {
		return nil, InvalidColumns
	}
	if len(s.Rows) > MaxRows || int(s.DeclaredRows) != len(s.Rows) || len(expected.Identities) != len(s.Rows) || len(expected.Counts) != 4 {
		return nil, InvalidEnumeration
	}
	index := map[Symbol]Identity{}
	counts := map[string]uint32{}
	for _, id := range expected.Identities {
		_, known := provider(id.Provider)
		if _, duplicate := index[id.ID]; duplicate || id.ID == 0 || !known {
			return nil, InvalidEnumeration
		}
		index[id.ID] = id
		counts[id.Provider]++
	}
	seenCounts := map[string]bool{}
	for _, c := range expected.Counts {
		_, known := provider(c.Provider)
		if !known || seenCounts[c.Provider] || c.Rows != counts[c.Provider] {
			return nil, InvalidEnumeration
		}
		seenCounts[c.Provider] = true
	}
	entries := map[Symbol]Entry{}
	operations := map[Symbol]bool{}
	for _, r := range s.Rows {
		if r.Generation != s.SourceGeneration {
			return nil, InvalidBinding
		}
		v, code := rowValues(r)
		if code != OK {
			return nil, code
		}
		id, name := v["id"].Symbol, v["provider_kind"].Enum
		want, exists := index[id]
		if _, duplicate := entries[id]; duplicate || !exists || want.Provider != name {
			return nil, InvalidEnumeration
		}
		operation := v["operation_id"]
		if operation.Kind == TextSymbol {
			if operations[operation.Symbol] {
				return nil, InvalidRow
			}
			operations[operation.Symbol] = true
		}
		p, _ := provider(name)
		archived := v["archived"].Number == 1
		if archived {
			if r.Format != Destroyed || r.AAD != (AAD{}) {
				return nil, InvalidBinding
			}
			if r.Disposition != KeepArchivedFacts {
				return nil, InvalidDisposition
			}
		} else {
			if r.Format != p.Format || r.AAD.Prefix != p.AADPrefix || r.AAD.Object != id {
				return nil, InvalidBinding
			}
			if r.Disposition != PlanReencrypt {
				return nil, InvalidDisposition
			}
		}
		e := Entry{ID: id, Provider: name, ConsumerID: p.ConsumerID, SourceGeneration: s.SourceGeneration, TargetGeneration: s.TargetGeneration, Format: r.Format, AAD: r.AAD, Disposition: r.Disposition}
		for _, c := range Columns() {
			action := PreserveFact
			if c.Name == "key_version" {
				action = PreserveEncodingVersion
			}
			if c.Name == "credential_ciphertext" && !archived {
				action = PlanM2Ciphertext
			}
			e.Fields = append(e.Fields, ColumnPlan{Column: c.Name, SourceValue: v[c.Name], Action: action})
		}
		entries[id] = e
	}
	out := &Manifest{SourceHead: SourceHead, Table: Table, Profile: Profile, SourceGeneration: s.SourceGeneration, TargetGeneration: s.TargetGeneration}
	for _, id := range expected.Identities {
		out.Entries = append(out.Entries, entries[id.ID])
	}
	for _, p := range Providers() {
		out.Counts = append(out.Counts, ProviderCount{Provider: p.Provider, Rows: counts[p.Provider]})
	}
	return out, OK
}
