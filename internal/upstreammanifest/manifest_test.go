// Independently authored public integer fixtures, not captured database rows.
package upstreammanifest_test

import (
	"testing"

	m "cpacloud.local/server/internal/upstreammanifest"
)

// The fixture's columns/providers/AAD are handwritten, not built from the
// implementation's catalogs. Header and row ordering deliberately differ.
var columns = []string{"id", "name", "provider_kind", "endpoint", "enabled", "credential_ciphertext", "key_version", "revision", "archived", "archived_at", "created_at", "credential_state", "verified_at", "operation_id"}

type providerOracle struct {
	name, consumer string
	version        int64
	format         m.Format
	prefix         string
}

var providers = []providerOracle{
	{"openai-compatible", "upstream-api", 1, m.APIv1, "cpacloud/upstream-api-key/v1\x00"},
	{"anthropic-api-key", "upstream-api", 1, m.APIv1, "cpacloud/upstream-api-key/v1\x00"},
	{"gemini-api-key", "gemini-api", 2, m.GeminiV2, "cpacloud/upstream-credential/v2\x00gemini-api-key\x00api-key\x00"},
	{"codex-membership", "codex-auth", 2, m.CodexV2, "cpacloud/upstream-credential/v2\x00codex-membership\x00auth-json\x00"},
}

func symbol(k m.Kind, n m.Symbol) m.Value { return m.Value{Kind: k, Symbol: n} }
func number(n int64) m.Value              { return m.Value{Kind: m.Integer, Number: n} }
func enum(s string) m.Value               { return m.Value{Kind: m.Enum, Enum: s} }
func row(id m.Symbol, p providerOracle, archived, disabled bool) m.Row {
	r := m.Row{Generation: 7, Format: p.format, AAD: m.AAD{Prefix: p.prefix, Object: id}, Disposition: m.PlanReencrypt}
	values := []m.Value{
		symbol(m.TextSymbol, id), symbol(m.TextSymbol, id+100), enum(p.name), symbol(m.TextSymbol, id+200), number(1), symbol(m.BlobSymbol, id+300), number(p.version), number(3), number(0), {}, symbol(m.TimestampSymbol, id+400), {}, {}, {},
	}
	if p.name == "codex-membership" {
		values[11] = enum("verified")
		values[12] = symbol(m.TimestampSymbol, id+500)
		values[13] = symbol(m.TextSymbol, id+600)
	}
	if disabled {
		values[4] = number(0)
	}
	if archived {
		values[4] = number(0)
		values[5] = m.Value{Kind: m.EmptyBlob}
		values[6] = number(0)
		values[8] = number(1)
		values[9] = symbol(m.TimestampSymbol, id+700)
		values[12] = m.Value{}
		if p.name == "codex-membership" {
			values[11] = enum("reauth_required")
		}
		r.Format = m.Destroyed
		r.AAD = m.AAD{}
		r.Disposition = m.KeepArchivedFacts
	}
	for i, n := range columns {
		r.Cells = append(r.Cells, m.Cell{Column: n, Value: values[i]})
	}
	return r
}

func fixture() (m.Snapshot, m.Enumeration) {
	s := m.Snapshot{SourceHead: m.SourceHead, Table: "upstreams", Profile: m.Profile, SourceGeneration: 7, TargetGeneration: 8, Columns: append([]string(nil), columns...), DeclaredRows: 7}
	s.Rows = []m.Row{row(1, providers[0], false, false), row(2, providers[1], false, false), row(3, providers[2], false, false), row(4, providers[3], false, false), row(5, providers[0], false, true), row(6, providers[1], true, false), row(7, providers[3], true, false)}
	e := m.Enumeration{
		Identities: []m.Identity{{ID: 1, Provider: "openai-compatible"}, {ID: 2, Provider: "anthropic-api-key"}, {ID: 3, Provider: "gemini-api-key"}, {ID: 4, Provider: "codex-membership"}, {ID: 5, Provider: "openai-compatible"}, {ID: 6, Provider: "anthropic-api-key"}, {ID: 7, Provider: "codex-membership"}},
		Counts:     []m.ProviderCount{{Provider: "openai-compatible", Rows: 2}, {Provider: "anthropic-api-key", Rows: 2}, {Provider: "gemini-api-key", Rows: 1}, {Provider: "codex-membership", Rows: 2}},
	}
	return s, e
}

func cell(r *m.Row, name string) *m.Value {
	for i := range r.Cells {
		if r.Cells[i].Column == name {
			return &r.Cells[i].Value
		}
	}
	panic("fixture column absent")
}
func require(t *testing.T, got, want m.Code) {
	t.Helper()
	if got != want {
		t.Fatalf("fixed result: got %s want %s", got, want)
	}
}
func build(t *testing.T, s m.Snapshot, e m.Enumeration) *m.Manifest {
	t.Helper()
	out, code := m.Build(s, e)
	require(t, code, m.OK)
	if out == nil {
		t.Fatal("missing manifest")
	}
	return out
}

func TestIndependentCatalogOracle(t *testing.T) {
	got := m.Columns()
	if len(got) != 14 {
		t.Fatal("column count")
	}
	for i, n := range columns {
		c := got[i]
		sqlType := "TEXT"
		notNull := true
		if n == "enabled" || n == "key_version" || n == "revision" || n == "archived" {
			sqlType = "INTEGER"
		}
		if n == "credential_ciphertext" {
			sqlType = "BLOB"
		}
		if n == "id" || n == "archived_at" || n == "credential_state" || n == "verified_at" || n == "operation_id" {
			notNull = false
		}
		if c.Name != n || c.DeclaredType != sqlType || c.DeclaredNotNull != notNull || c.PrimaryKey != (n == "id") || c.Unique != (n == "operation_id") || c.SourceLine != 107+i {
			t.Fatal("column/source oracle")
		}
	}
	pg := m.Providers()
	if len(pg) != 4 {
		t.Fatal("provider count")
	}
	for i, p := range providers {
		g := pg[i]
		if g.Provider != p.name || g.ConsumerID != p.consumer || g.KeyVersion != p.version || g.Format != p.format || g.AADPrefix != p.prefix {
			t.Fatal("provider/source oracle")
		}
	}
	got[0].Name = "modified"
	pg[0].AADPrefix = "modified"
	if m.Columns()[0].Name != "id" || m.Providers()[0].AADPrefix != providers[0].prefix {
		t.Fatal("catalog alias")
	}
}

func TestCompleteSyntheticTablePlansEveryColumnWithoutActivation(t *testing.T) {
	s, e := fixture()
	out := build(t, s, e)
	if out.SourceHead != m.SourceHead || out.Table != "upstreams" || out.Profile != m.Profile || out.SourceGeneration != 7 || out.TargetGeneration != 8 || len(out.Entries) != 7 || len(out.Counts) != 4 {
		t.Fatal("manifest scope")
	}
	for i, entry := range out.Entries {
		if entry.ID != e.Identities[i].ID || entry.Provider != e.Identities[i].Provider || len(entry.Fields) != 14 || entry.SourceGeneration != 7 || entry.TargetGeneration != 8 {
			t.Fatal("entry identity")
		}
		for j, field := range entry.Fields {
			if field.Column != columns[j] || field.SourceValue != s.Rows[i].Cells[j].Value {
				t.Fatal("source fact changed")
			}
			action := m.PreserveFact
			if field.Column == "key_version" {
				action = m.PreserveEncodingVersion
			}
			if field.Column == "credential_ciphertext" && i < 5 {
				action = m.PlanM2Ciphertext
			}
			if field.Action != action {
				t.Fatal("column action")
			}
		}
		if i < 5 {
			if entry.Disposition != m.PlanReencrypt || entry.AAD.Object != entry.ID {
				t.Fatal("planned binding")
			}
		} else if entry.Disposition != m.KeepArchivedFacts || entry.Format != m.Destroyed || entry.AAD != (m.AAD{}) {
			t.Fatal("archive authority revived")
		}
	}
	// Disabled-but-unarchived rows stay disabled; mapping grants no access.
	if out.Entries[4].Fields[4].SourceValue.Number != 0 {
		t.Fatal("disabled expanded")
	}
	for i, c := range out.Counts {
		if c != e.Counts[i] {
			t.Fatal("provider counts")
		}
	}
}

func TestEmptyTableStillRequiresAllColumnsAndFourZeroCategories(t *testing.T) {
	s, e := fixture()
	s.Rows = nil
	s.DeclaredRows = 0
	e.Identities = nil
	for i := range e.Counts {
		e.Counts[i].Rows = 0
	}
	out := build(t, s, e)
	if len(out.Entries) != 0 || len(out.Counts) != 4 {
		t.Fatal("empty table")
	}
	s.Columns = nil
	bad, code := m.Build(s, e)
	require(t, code, m.InvalidColumns)
	if bad != nil {
		t.Fatal("partial empty output")
	}
}

func TestSnapshotRefusals(t *testing.T) {
	cases := []struct {
		name   string
		change func(*m.Snapshot, *m.Enumeration)
		code   m.Code
	}{
		{"wrong-head", func(s *m.Snapshot, e *m.Enumeration) { s.SourceHead = "6056270c612bdc3741dfee5a6c0c338cf9926b0c" }, m.InvalidScope},
		{"wrong-table", func(s *m.Snapshot, e *m.Enumeration) { s.Table = "access_keys" }, m.InvalidScope},
		{"old-profile", func(s *m.Snapshot, e *m.Enumeration) { s.Profile = "upstreams-pre-codex" }, m.InvalidScope},
		{"future-profile", func(s *m.Snapshot, e *m.Enumeration) { s.Profile = "unknown" }, m.InvalidScope},
		{"no-generation", func(s *m.Snapshot, e *m.Enumeration) { s.SourceGeneration = 0 }, m.InvalidScope},
		{"same-generation", func(s *m.Snapshot, e *m.Enumeration) { s.TargetGeneration = 7 }, m.InvalidScope},
		{"generation-gap", func(s *m.Snapshot, e *m.Enumeration) { s.TargetGeneration = 9 }, m.InvalidScope},
		{"generation-wrap", func(s *m.Snapshot, e *m.Enumeration) { s.SourceGeneration = ^uint64(0); s.TargetGeneration = 0 }, m.InvalidScope},
		{"column-missing", func(s *m.Snapshot, e *m.Enumeration) { s.Columns = s.Columns[1:] }, m.InvalidColumns},
		{"column-extra", func(s *m.Snapshot, e *m.Enumeration) { s.Columns = append(s.Columns, "root_epoch") }, m.InvalidColumns},
		{"column-duplicate", func(s *m.Snapshot, e *m.Enumeration) { s.Columns[1] = s.Columns[0] }, m.InvalidColumns},
		{"column-unknown", func(s *m.Snapshot, e *m.Enumeration) { s.Columns[1] = "future_field" }, m.InvalidColumns},
		{"column-case", func(s *m.Snapshot, e *m.Enumeration) { s.Columns[1] = "NAME" }, m.InvalidColumns},
		{"row-limit", func(s *m.Snapshot, e *m.Enumeration) { s.Rows = make([]m.Row, m.MaxRows+1) }, m.InvalidEnumeration},
		{"row-count", func(s *m.Snapshot, e *m.Enumeration) { s.DeclaredRows-- }, m.InvalidEnumeration},
		{"row-missing", func(s *m.Snapshot, e *m.Enumeration) { s.Rows = s.Rows[1:]; s.DeclaredRows-- }, m.InvalidEnumeration},
		{"row-extra", func(s *m.Snapshot, e *m.Enumeration) { s.Rows = append(s.Rows, s.Rows[0]); s.DeclaredRows++ }, m.InvalidEnumeration},
		{"index-missing", func(s *m.Snapshot, e *m.Enumeration) { e.Identities = e.Identities[1:] }, m.InvalidEnumeration},
		{"index-duplicate", func(s *m.Snapshot, e *m.Enumeration) { e.Identities[1] = e.Identities[0] }, m.InvalidEnumeration},
		{"index-zero", func(s *m.Snapshot, e *m.Enumeration) { e.Identities[0].ID = 0 }, m.InvalidEnumeration},
		{"index-unknown-provider", func(s *m.Snapshot, e *m.Enumeration) { e.Identities[0].Provider = "unknown" }, m.InvalidEnumeration},
		{"count-missing", func(s *m.Snapshot, e *m.Enumeration) { e.Counts = e.Counts[1:] }, m.InvalidEnumeration},
		{"count-extra", func(s *m.Snapshot, e *m.Enumeration) { e.Counts = append(e.Counts, e.Counts[0]) }, m.InvalidEnumeration},
		{"count-duplicate", func(s *m.Snapshot, e *m.Enumeration) { e.Counts[1] = e.Counts[0] }, m.InvalidEnumeration},
		{"count-unknown-provider", func(s *m.Snapshot, e *m.Enumeration) { e.Counts[0].Provider = "unknown" }, m.InvalidEnumeration},
		{"count-wrong", func(s *m.Snapshot, e *m.Enumeration) { e.Counts[0].Rows++ }, m.InvalidEnumeration},
		{"duplicate-row-id", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[1] = s.Rows[0] }, m.InvalidEnumeration},
		{"extra-identity", func(s *m.Snapshot, e *m.Enumeration) {
			*cell(&s.Rows[0], "id") = symbol(m.TextSymbol, 99)
			s.Rows[0].AAD.Object = 99
		}, m.InvalidEnumeration},
		{"provider-index-mismatch", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[0] = row(1, providers[1], false, false) }, m.InvalidEnumeration},
		{"mixed-generation", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[1].Generation = 8 }, m.InvalidBinding},
		{"zero-row-generation", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[1].Generation = 0 }, m.InvalidBinding},
		{"old-aad", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[2].AAD.Prefix = providers[0].prefix }, m.InvalidBinding},
		{"wrong-aad-object", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[2].AAD.Object = 1 }, m.InvalidBinding},
		{"missing-aad", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[2].AAD = m.AAD{} }, m.InvalidBinding},
		{"legacy-format", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[2].Format = m.APIv1 }, m.InvalidBinding},
		{"unknown-format", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[2].Format = "future" }, m.InvalidBinding},
		{"unresolved", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[0].Disposition = m.Unresolved }, m.InvalidDisposition},
		{"wrong-disposition", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[0].Disposition = m.KeepArchivedFacts }, m.InvalidDisposition},
		{"archive-live-format", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[5].Format = m.APIv1 }, m.InvalidBinding},
		{"archive-aad", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[5].AAD = m.AAD{Prefix: providers[1].prefix, Object: 6} }, m.InvalidBinding},
		{"archive-reencrypt", func(s *m.Snapshot, e *m.Enumeration) { s.Rows[5].Disposition = m.PlanReencrypt }, m.InvalidDisposition},
		{"duplicate-operation", func(s *m.Snapshot, e *m.Enumeration) {
			*cell(&s.Rows[6], "operation_id") = *cell(&s.Rows[3], "operation_id")
		}, m.InvalidRow},
		{"unknown-provider", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "provider_kind") = enum("unknown") }, m.InvalidRow},
		{"api-wrong-version", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "key_version") = number(2) }, m.InvalidRow},
		{"gemini-legacy-version", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[2], "key_version") = number(1) }, m.InvalidRow},
		{"codex-legacy-version", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[3], "key_version") = number(1) }, m.InvalidRow},
		{"unknown-state", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[3], "credential_state") = enum("pending") }, m.InvalidRow},
		{"codex-null-state", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[3], "credential_state") = m.Value{} }, m.InvalidRow},
		{"codex-null-operation", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[3], "operation_id") = m.Value{} }, m.InvalidRow},
		{"api-nonnull-state", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "credential_state") = enum("verified") }, m.InvalidRow},
		{"api-nonnull-verified", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "verified_at") = symbol(m.TimestampSymbol, 9) }, m.InvalidRow},
		{"api-nonnull-operation", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "operation_id") = symbol(m.TextSymbol, 9) }, m.InvalidRow},
		{"archive-null-blob", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[5], "credential_ciphertext") = m.Value{} }, m.InvalidRow},
		{"archive-retained-blob", func(s *m.Snapshot, e *m.Enumeration) {
			*cell(&s.Rows[5], "credential_ciphertext") = symbol(m.BlobSymbol, 9)
		}, m.InvalidRow},
		{"archive-key-version", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[5], "key_version") = number(1) }, m.InvalidRow},
		{"archive-enabled", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[5], "enabled") = number(1) }, m.InvalidRow},
		{"archive-null-date", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[5], "archived_at") = m.Value{} }, m.InvalidRow},
		{"archive-verified", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[6], "verified_at") = symbol(m.TimestampSymbol, 9) }, m.InvalidRow},
		{"archive-codex-state", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[6], "credential_state") = enum("verified") }, m.InvalidRow},
		{"live-archived-date", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "archived_at") = symbol(m.TimestampSymbol, 9) }, m.InvalidRow},
		{"live-empty-blob", func(s *m.Snapshot, e *m.Enumeration) {
			*cell(&s.Rows[0], "credential_ciphertext") = m.Value{Kind: m.EmptyBlob}
		}, m.InvalidRow},
		{"enabled-range", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "enabled") = number(2) }, m.InvalidRow},
		{"archived-range", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "archived") = number(-1) }, m.InvalidRow},
		{"revision-zero", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "revision") = number(0) }, m.InvalidRow},
		{"revision-over-bound", func(s *m.Snapshot, e *m.Enumeration) { *cell(&s.Rows[0], "revision") = number(9007199254740992) }, m.InvalidRow},
		{"hidden-null-payload", func(s *m.Snapshot, e *m.Enumeration) { cell(&s.Rows[0], "verified_at").Symbol = 9 }, m.InvalidRow},
		{"hidden-integer-payload", func(s *m.Snapshot, e *m.Enumeration) { cell(&s.Rows[0], "enabled").Enum = "hidden" }, m.InvalidRow},
		{"hidden-symbol-payload", func(s *m.Snapshot, e *m.Enumeration) { cell(&s.Rows[0], "id").Number = 9 }, m.InvalidRow},
		{"hidden-enum-payload", func(s *m.Snapshot, e *m.Enumeration) { cell(&s.Rows[0], "provider_kind").Symbol = 9 }, m.InvalidRow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, e := fixture()
			c.change(&s, &e)
			out, code := m.Build(s, e)
			require(t, code, c.code)
			if out != nil {
				t.Fatal("refusal leaked partial manifest")
			}
		})
	}
}

func TestEachColumnRejectsOmissionDuplicateUnknownAndInvalidKind(t *testing.T) {
	for i, name := range columns {
		t.Run(name, func(t *testing.T) {
			for _, change := range []string{"omit", "duplicate", "unknown", "kind"} {
				s, e := fixture()
				r := &s.Rows[0]
				want := m.InvalidColumns
				switch change {
				case "omit":
					r.Cells = append(r.Cells[:i], r.Cells[i+1:]...)
				case "duplicate":
					r.Cells[i].Column = columns[(i+1)%14]
				case "unknown":
					r.Cells[i].Column = "future"
				case "kind":
					r.Cells[i].Value = m.Value{Kind: 255}
					want = m.InvalidRow
				}
				out, code := m.Build(s, e)
				require(t, code, want)
				if out != nil {
					t.Fatal("partial output")
				}
			}
		})
	}
}

func TestAllProvidersAcceptSupportedLiveDisabledAndArchivedShapes(t *testing.T) {
	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			for _, shape := range []string{"live", "disabled", "archived"} {
				s, e := fixture()
				s.Rows = []m.Row{row(1, p, shape == "archived", shape == "disabled")}
				s.DeclaredRows = 1
				e.Identities = []m.Identity{{ID: 1, Provider: p.name}}
				for i := range e.Counts {
					e.Counts[i].Rows = 0
					if e.Counts[i].Provider == p.name {
						e.Counts[i].Rows = 1
					}
				}
				build(t, s, e)
			}
		})
	}
}

func TestPhysicalOrderingIsNotSchemaIdentity(t *testing.T) {
	s, e := fixture()
	for i, j := 0, 13; i < j; i, j = i+1, j-1 {
		s.Columns[i], s.Columns[j] = s.Columns[j], s.Columns[i]
		for k := range s.Rows {
			s.Rows[k].Cells[i], s.Rows[k].Cells[j] = s.Rows[k].Cells[j], s.Rows[k].Cells[i]
		}
	}
	for i, j := 0, len(s.Rows)-1; i < j; i, j = i+1, j-1 {
		s.Rows[i], s.Rows[j] = s.Rows[j], s.Rows[i]
	}
	out := build(t, s, e)
	for i, entry := range out.Entries {
		if entry.ID != e.Identities[i].ID {
			t.Fatal("fixture index order")
		}
		for j, field := range entry.Fields {
			if field.Column != columns[j] {
				t.Fatal("canonical field order")
			}
		}
	}
}

func TestInputOutputAndCatalogAliasesAreIndependent(t *testing.T) {
	s, e := fixture()
	out := build(t, s, e)
	s.Columns[0] = "modified"
	s.Rows[0].Cells[0].Value.Symbol = 99
	e.Identities[0].ID = 99
	e.Counts[0].Rows = 99
	if out.Entries[0].ID != 1 || out.Entries[0].Fields[0].SourceValue.Symbol != 1 || out.Counts[0].Rows != 2 {
		t.Fatal("retained input aliases")
	}
	out.Entries[0].Fields[0].SourceValue.Symbol = 99
	out.Counts[0].Rows = 99
	s2, e2 := fixture()
	fresh := build(t, s2, e2)
	if fresh.Entries[0].Fields[0].SourceValue.Symbol != 1 || fresh.Counts[0].Rows != 2 {
		t.Fatal("shared output state")
	}
}

// Explicit limitation witness: if BOTH independent fixture inputs omit a row
// coherently, pure computation cannot discover an absent real database row.
// This acceptance is not a database-completeness or migration-approval claim.
func TestCoordinatedFixtureOmissionDoesNotProveDatabaseCompleteness(t *testing.T) {
	s, e := fixture()
	s.Rows = s.Rows[1:]
	s.DeclaredRows = 6
	e.Identities = e.Identities[1:]
	e.Counts[0].Rows = 1
	out := build(t, s, e)
	if len(out.Entries) != 6 {
		t.Fatal("provided synthetic universe")
	}
}
